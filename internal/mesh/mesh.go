// Package mesh joins nebu daemons on one network into a mesh: identity, the shared secret, the
// handshake that admits members, the session tokens node to node calls carry, the sync that
// keeps every member's record on every node, discovery by multicast beacon, and the measured
// links between members.
package mesh

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nickheyer/nebu/internal/auth"
	"github.com/nickheyer/nebu/internal/db"
	"github.com/nickheyer/nebu/internal/gateway"
	"github.com/nickheyer/nebu/internal/installs"
	"github.com/nickheyer/nebu/internal/mesh/links"
	"github.com/nickheyer/nebu/internal/settings"
	"github.com/nickheyer/nebu/pkg/events"
	"github.com/nickheyer/nebu/pkg/host"
	"github.com/nickheyer/nebu/pkg/perf"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/runtimes"
	"github.com/nickheyer/nebu/pkg/store"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	// Every member syncs with every other member this often, and on every change it makes
	SyncInterval = 5 * time.Second
	// Syncs a member may miss before it is unreachable
	UnreachableAfter = 3
	// How long a member may be missing before it is gone
	GoneAfter = 10 * time.Minute
	// Syncs to a gone member slow to this interval until it answers again
	goneRetry = time.Minute
	// A session token lasts this long, renewed on every sync
	SessionTTL = time.Hour
	// Links are measured this often, and on join
	ProbeInterval = 5 * time.Minute
	// The mesh listener bound when the API listener stays on loopback
	DefaultListen = "0.0.0.0:8485"
	// Exposure settings for seats other seats connect to
	ExposureGuard  = "guard"
	ExposureDirect = "direct"
	// A pending handshake waits this long for its second call
	handshakeTTL = 30 * time.Second
	// Self record events coalesce over this window
	publishDelay = 200 * time.Millisecond
	callTimeout  = 10 * time.Second
)

var (
	// Returned when the node belongs to no mesh
	ErrNoMesh = errors.New("this node belongs to no mesh")
	// Returned when a mesh request is malformed or refused
	ErrMesh = errors.New("mesh")
	// Returned when a node id names no member
	ErrUnknownNode = errors.New("unknown node")
)

// What the formation conductor tells the mesh about this node, and takes from other members
type Formations interface {
	// Formations conducted on this node
	Conducted() []*v1.Formation
	// Seats hosted on this node
	Seats() []*v1.SeatRef
	// Slots on this node
	Slots() []*v1.SlotRef
	// Whether a formation with a seat on the node is serving, so bandwidth runs hold
	Serving(nodeID string) bool
	// Takes the formations a member conducts, as its sync carried them
	Merge(conductor string, list []*v1.Formation)
	// Tells the conductor a member changed state, so its formations follow
	MemberState(nodeID string, state v1.NodeState)
}

// A member as this node tracks it
type member struct {
	rec *v1.Node
	// Syncs missed in a row, and when the last attempt was
	missed    int
	lastTried time.Time
	probedAt  time.Time
	// Whether the record came from gossip alone and no sync has filled it yet
	sketch bool
}

// A session with one peer: the token it sends us and the token we send it
type session struct {
	peer    string
	accept  string
	send    string
	expires time.Time
	granted time.Time
	// When the token we send expires, learned from the peer's sync answers
	sendExpires time.Time
}

// A handshake awaiting its second call
type pending struct {
	nonceA, nonceB []byte
	expires        time.Time
}

// Membership, sessions, sync, discovery, and links
type Manager struct {
	DB       *db.DB
	Config   *v1.MeshConfig
	Host     *host.Prober
	Installs *installs.Manager
	Runtimes *runtimes.Registry
	Store    *store.Store
	Perf     *perf.Table
	Routes   *gateway.Table
	Settings *settings.Manager
	Events   *events.Bus
	Guard    *auth.Guard
	Log      *slog.Logger
	Version  string
	// The API listener's address and TLS, node traffic sharing it when it is not loopback
	APIListen string
	APITLS    *tls.Config
	// The API handler, served on the mesh listener too
	Handler http.Handler
	// Set by the daemon once the conductor exists
	Formations Formations

	identity *db.Identity
	prober   *links.Prober

	mu       sync.Mutex
	mesh     *db.MeshRow
	members  map[string]*member
	sessions map[string]*session
	accept   map[string]string
	pendings map[string]*pending
	// One handshake at a time per peer, so callers wanting the same session share one
	dialing map[string]*sync.Mutex
	links   map[string]*v1.Link
	// Nodes heard by beacon, and admissions under way, both sides
	nearby     map[string]*heard
	admissions map[string]*v1.Admission
	// Why the node traffic listener is not bound, when the default address could not be taken
	listenErr string
	statusAt  *time.Timer
	// Connections by member address and TLS state
	h2         map[string]http.RoundTripper
	seq        uint64
	dirty      bool
	publishAt  *time.Timer
	listener   net.Listener
	server     *http.Server
	listenAddr string
	base       context.Context
	cancel     context.CancelFunc
	wg         sync.WaitGroup
	wake       chan struct{}
	running    bool
}

// Loads the identity, making one on first start, and the mesh with its members, sessions, and links
func (m *Manager) Open(ctx context.Context) error {
	if m.Log == nil {
		m.Log = slog.Default()
	}
	if m.Config == nil {
		m.Config = &v1.MeshConfig{}
	}
	m.members = map[string]*member{}
	m.sessions = map[string]*session{}
	m.accept = map[string]string{}
	m.pendings = map[string]*pending{}
	m.dialing = map[string]*sync.Mutex{}
	m.links = map[string]*v1.Link{}
	m.nearby = map[string]*heard{}
	m.admissions = map[string]*v1.Admission{}
	m.h2 = map[string]http.RoundTripper{}
	m.wake = make(chan struct{}, 1)
	m.prober = &links.Prober{Log: m.Log}
	// Sequences climb across restarts, so a returning node's record wins over stale copies.
	m.seq = uint64(time.Now().UnixMilli())
	id, err := m.DB.GetIdentity(ctx)
	if db.IsNotFound(err) {
		id, err = newIdentity()
		if err != nil {
			return err
		}
		if err := m.DB.PutIdentity(ctx, id); err != nil {
			return err
		}
		m.Log.Info("mesh identity made", "node", id.ID)
	} else if err != nil {
		return err
	}
	m.identity = id
	row, err := m.DB.GetMesh(ctx)
	if err != nil && !db.IsNotFound(err) {
		return err
	}
	if err == nil {
		m.mesh = row
	}
	recs, err := m.DB.ListMembers(ctx)
	if err != nil {
		return err
	}
	for _, rec := range recs {
		if rec.GetId() == id.ID {
			continue
		}
		m.members[rec.GetId()] = &member{rec: rec, sketch: rec.GetProfile() == nil}
	}
	sessions, err := m.DB.ListSessions(ctx)
	if err != nil {
		return err
	}
	for _, s := range sessions {
		if time.Now().After(s.Expires) {
			continue
		}
		m.sessions[s.PeerID] = &session{peer: s.PeerID, accept: s.Accept, send: s.Send, expires: s.Expires, granted: s.Granted, sendExpires: s.Expires}
		if s.Accept != "" {
			m.accept[s.Accept] = s.PeerID
		}
	}
	list, err := m.DB.ListLinks(ctx, id.ID)
	if err != nil {
		return err
	}
	for _, l := range list {
		m.links[l.GetTo()] = l
	}
	admissions, err := m.DB.ListAdmissions(ctx)
	if err != nil {
		return err
	}
	for _, a := range admissions {
		m.admissions[a.GetId()] = a
	}
	if m.Guard != nil {
		m.Guard.SetPeers(m)
	}
	if m.Perf != nil {
		m.Perf.OnChange = m.bump
	}
	return nil
}

// A random 128 bit id and an Ed25519 key pair
func newIdentity() (*db.Identity, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	return &db.Identity{ID: hex.EncodeToString(raw), PublicKey: pub, PrivateKey: priv, CreatedAt: time.Now()}, nil
}

// This node's id
func (m *Manager) Self() string { return m.identity.ID }

// Whether the node belongs to a mesh
func (m *Manager) Joined() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.mesh != nil
}

// The mesh's record for the API, ErrNoMesh when the node belongs to none
func (m *Manager) Mesh() (*v1.Mesh, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.mesh == nil {
		return nil, ErrNoMesh
	}
	return m.meshLocked(), nil
}

func (m *Manager) meshLocked() *v1.Mesh {
	return &v1.Mesh{
		Id:        m.mesh.ID,
		Name:      m.mesh.Name,
		Tls:       m.mesh.TLS,
		Self:      m.identity.ID,
		CreatedAt: timestamppb.New(m.mesh.CreatedAt),
		JoinedAt:  timestamppb.New(m.mesh.JoinedAt),
		Address:   m.advertiseLocked(),
		Announce:  m.announceOn(),
		Exposure:  m.Exposure(),
		Members:   uint32(len(m.members) + 1),
	}
}

// Whether multicast beacons are on
func (m *Manager) announceOn() bool {
	return m.Config.Announce == nil || m.Config.GetAnnounce()
}

// How seats other seats connect to are exposed: guard or direct
func (m *Manager) Exposure() string {
	if strings.EqualFold(m.Config.GetExposure(), ExposureDirect) {
		return ExposureDirect
	}
	return ExposureGuard
}

// Starts the node traffic listener, the beacon that makes the node discoverable, and the sync
// and probe loops when the node belongs to a mesh. The listener binds whenever beacons are on,
// so a node outside any mesh can be invited from another node's page; a listener config names
// must bind, the default one reports on the page when it cannot.
func (m *Manager) Start(ctx context.Context) error {
	m.base, m.cancel = context.WithCancel(ctx)
	m.mu.Lock()
	joined := m.mesh != nil
	m.mu.Unlock()
	if joined || m.Config.GetListen() != "" {
		if err := m.listen(); err != nil {
			return err
		}
	} else if m.announceOn() {
		if err := m.listen(); err != nil {
			m.mu.Lock()
			m.listenErr = err.Error()
			m.mu.Unlock()
			m.Log.Warn("node traffic listener not bound, other nodes cannot invite this one until mesh.listen names a free address", "err", err)
		}
	}
	m.wg.Add(3)
	go func() {
		defer m.wg.Done()
		m.watch(m.base)
	}()
	go func() {
		defer m.wg.Done()
		m.announceLoop(m.base)
	}()
	go func() {
		defer m.wg.Done()
		m.admissionLoop(m.base)
	}()
	if joined {
		m.startLoops()
		m.publishSelf()
		for id, mb := range m.snapshotMembers() {
			m.Events.Publish(v1.EventKind_EVENT_KIND_NODE, v1.EventAction_EVENT_ACTION_CREATED, id, mb)
		}
	}
	m.publishStatus()
	return nil
}

// Starts the loops a member runs, once
func (m *Manager) startLoops() {
	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		return
	}
	m.running = true
	m.mu.Unlock()
	m.wg.Add(2)
	go func() {
		defer m.wg.Done()
		m.syncLoop(m.base)
	}()
	go func() {
		defer m.wg.Done()
		m.probeLoop(m.base)
	}()
}

// Stops loops and the listener
func (m *Manager) Close() {
	if m.cancel != nil {
		m.cancel()
	}
	m.mu.Lock()
	srv, ln := m.server, m.listener
	m.server, m.listener = nil, nil
	m.mu.Unlock()
	if srv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		srv.Shutdown(ctx)
		cancel()
	}
	if ln != nil {
		ln.Close()
	}
	m.wg.Wait()
}

// Marks this node's record changed: the sequence climbs and the next sync carries it
func (m *Manager) bump() {
	m.mu.Lock()
	m.seq++
	m.dirty = true
	joined := m.mesh != nil
	if m.publishAt == nil {
		m.publishAt = time.AfterFunc(publishDelay, func() {
			m.mu.Lock()
			m.publishAt = nil
			m.mu.Unlock()
			m.publishSelf()
		})
	}
	m.mu.Unlock()
	if joined {
		select {
		case m.wake <- struct{}{}:
		default:
		}
	}
}

// Publishes this node's record
func (m *Manager) publishSelf() {
	if !m.Joined() {
		return
	}
	rec := m.Record()
	m.Events.Publish(v1.EventKind_EVENT_KIND_NODE, v1.EventAction_EVENT_ACTION_UPDATED, rec.GetId(), rec)
}

// The mesh from this node: membership, the nodes and meshes heard on the network, and the
// admissions under way. The Mesh page draws it and the MESH event carries every change.
func (m *Manager) Status() *v1.MeshStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := &v1.MeshStatus{
		NodeId:      m.identity.ID,
		NodeName:    m.name(),
		Listen:      m.listenAddr,
		Address:     m.advertiseLocked(),
		Announce:    m.announceOn(),
		ListenError: m.listenErr,
		Nodes:       m.nearbyListLocked(),
		Meshes:      m.meshesNearbyLocked(),
		Admissions:  m.admissionListLocked(),
	}
	if m.mesh != nil {
		st.Mesh = m.meshLocked()
	}
	return st
}

// Publishes the status, changes within a short window folded into one event
func (m *Manager) publishStatus() {
	m.mu.Lock()
	if m.statusAt != nil {
		m.mu.Unlock()
		return
	}
	m.statusAt = time.AfterFunc(publishDelay, func() {
		m.mu.Lock()
		m.statusAt = nil
		m.mu.Unlock()
		m.Events.Publish(v1.EventKind_EVENT_KIND_MESH, v1.EventAction_EVENT_ACTION_UPDATED, "mesh", m.Status())
	})
	m.mu.Unlock()
}

// Marks the record changed whenever what it carries changes
func (m *Manager) watch(ctx context.Context) {
	kinds := []v1.EventKind{v1.EventKind_EVENT_KIND_HOST, v1.EventKind_EVENT_KIND_INSTALL, v1.EventKind_EVENT_KIND_MODEL, v1.EventKind_EVENT_KIND_STORE, v1.EventKind_EVENT_KIND_ROUTE, v1.EventKind_EVENT_KIND_SLOT, v1.EventKind_EVENT_KIND_INSTANCE, v1.EventKind_EVENT_KIND_FORMATION, v1.EventKind_EVENT_KIND_SETTINGS}
	sub := m.Events.Subscribe(ctx, kinds)
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-sub.Events():
			// A copy of another member's formation changes nothing about this node.
			if f := ev.GetFormation(); f != nil && f.GetConductor() != m.identity.ID {
				continue
			}
			if ev.GetSeq() > 0 {
				m.bump()
			}
		}
	}
}

// This node's record as every other member sees it
func (m *Manager) Record() *v1.Node {
	m.mu.Lock()
	seq := m.seq
	links := make([]*v1.Link, 0, len(m.links))
	for _, l := range m.links {
		links = append(links, proto.Clone(l).(*v1.Link))
	}
	address := m.advertiseLocked()
	fingerprint, hasTLS := m.servedCertLocked()
	m.mu.Unlock()
	sort.Slice(links, func(i, j int) bool { return links[i].GetTo() < links[j].GetTo() })
	rec := &v1.Node{
		Id:             m.identity.ID,
		Name:           m.name(),
		Version:        m.Version,
		Links:          links,
		State:          v1.NodeState_NODE_STATE_READY,
		Sequence:       seq,
		SeenAt:         timestamppb.Now(),
		Self:           true,
		PublicKey:      m.identity.PublicKey,
		TlsFingerprint: fingerprint,
		Os:             runtime.GOOS,
		Arch:           runtime.GOARCH,
		Address:        address,
		Tls:            hasTLS,
		Addresses:      m.addresses(),
	}
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	if profile, err := m.Host.Profile(ctx, false); err == nil {
		rec.Profile = profile
	}
	if list, err := m.Installs.List(ctx, ""); err == nil {
		rec.Installs = list
		for _, in := range list {
			rt, err := m.Runtimes.Get(in.GetRuntimeId())
			if err != nil {
				continue
			}
			rec.Capabilities = append(rec.Capabilities, &v1.Capability{RuntimeId: rt.ID(), InstallId: in.GetId(), Version: in.GetVersion(), Shapes: rt.Shapes(in)})
		}
	}
	if manifests, err := m.Store.ListManifests(); err == nil {
		for _, s := range manifests {
			rec.Stored = append(rec.Stored, Summary(s))
		}
	}
	if m.Perf != nil {
		rec.Throughput = m.Perf.Throughputs()
	}
	if m.Routes != nil {
		for _, r := range m.Routes.List() {
			if !r.GetForwarded() {
				rec.Routes = append(rec.Routes, r)
			}
		}
	}
	if m.Formations != nil {
		for _, f := range m.Formations.Conducted() {
			rec.FormationIds = append(rec.FormationIds, f.GetId())
		}
		rec.Seats = m.Formations.Seats()
		rec.Slots = m.Formations.Slots()
	}
	return rec
}

// A stored model as a member advertises it, its descriptor digested so a puller knows the model
// the moment the pull ends
func Summary(s *v1.StoredModel) *v1.StoredSummary {
	out := &v1.StoredSummary{
		SourceId:       s.GetSourceId(),
		Repo:           s.GetRepo(),
		Revision:       s.GetRevision(),
		Commit:         s.GetCommit(),
		Group:          s.GetGroup(),
		FormatId:       s.GetFormatId(),
		Bytes:          s.GetBytes(),
		PulledAt:       s.GetPulledAt(),
		UsedAt:         s.GetUsedAt(),
		Kind:           s.GetDescriptor_().GetKind(),
		Architecture:   s.GetDescriptor_().GetArchitecture(),
		ParameterCount: s.GetDescriptor_().GetParameterCount(),
	}
	out.DescriptorDigest = store.DescriptorDigest(s.GetDescriptor_())
	return out
}

// The name other members show for this node: the host label, else the hostname
func (m *Manager) name() string {
	if m.Settings != nil {
		if label := m.Settings.Get().GetHostLabel(); label != "" {
			return label
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	if profile, err := m.Host.Profile(ctx, false); err == nil && profile.GetHostname() != "" {
		return profile.GetHostname()
	}
	return m.identity.ID[:8]
}

// Every address this node can be reached at: each non loopback interface address on the mesh port
func (m *Manager) addresses() []string {
	m.mu.Lock()
	port := m.portLocked()
	m.mu.Unlock()
	if port == "" {
		return nil
	}
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []string
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipNet, ok := a.(*net.IPNet)
			if !ok || ipNet.IP.IsLoopback() || ipNet.IP.IsLinkLocalUnicast() {
				continue
			}
			out = append(out, net.JoinHostPort(ipNet.IP.String(), port))
		}
	}
	sort.Strings(out)
	return out
}

// Members' records, newest sequence copies, this node excluded
func (m *Manager) snapshotMembers() map[string]*v1.Node {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]*v1.Node, len(m.members))
	for id, mb := range m.members {
		out[id] = proto.Clone(mb.rec).(*v1.Node)
	}
	return out
}

// Every node's record, this node first, then members by name
func (m *Manager) Nodes() []*v1.Node {
	out := []*v1.Node{m.Record()}
	members := m.snapshotMembers()
	rest := make([]*v1.Node, 0, len(members))
	for _, rec := range members {
		rest = append(rest, rec)
	}
	sort.Slice(rest, func(i, j int) bool {
		if rest[i].GetName() != rest[j].GetName() {
			return rest[i].GetName() < rest[j].GetName()
		}
		return rest[i].GetId() < rest[j].GetId()
	})
	return append(out, rest...)
}

// One member's record by id or name, this node included
func (m *Manager) Node(id string) (*v1.Node, error) {
	if id == m.identity.ID || id == "self" {
		return m.Record(), nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if mb, ok := m.members[id]; ok {
		return proto.Clone(mb.rec).(*v1.Node), nil
	}
	for _, mb := range m.members {
		if mb.rec.GetName() == id {
			return proto.Clone(mb.rec).(*v1.Node), nil
		}
	}
	if m.name() == id {
		return m.Record(), nil
	}
	return nil, fmt.Errorf("%w %q", ErrUnknownNode, id)
}

// Every link measured by every member, this node's first
func (m *Manager) Links() []*v1.Link {
	var out []*v1.Link
	m.mu.Lock()
	for _, l := range m.links {
		out = append(out, proto.Clone(l).(*v1.Link))
	}
	for _, mb := range m.members {
		for _, l := range mb.rec.GetLinks() {
			out = append(out, proto.Clone(l).(*v1.Link))
		}
	}
	m.mu.Unlock()
	sort.Slice(out, func(i, j int) bool {
		if out[i].GetFrom() != out[j].GetFrom() {
			return out[i].GetFrom() < out[j].GetFrom()
		}
		return out[i].GetTo() < out[j].GetTo()
	})
	return out
}

// The mesh session token a peer sends, if it is one we minted and it has not expired
func (m *Manager) Peer(token string) (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id, ok := m.accept[token]
	if !ok {
		return "", false
	}
	s := m.sessions[id]
	if s == nil || s.accept != token || time.Now().After(s.expires) {
		return "", false
	}
	return id, true
}

// A random URL safe token
func randomToken() (string, error) {
	return auth.RandomString(32)
}

// The hash of a mesh id that beacons and handshakes carry, so a node answers only for its mesh
func meshHash(id string) []byte {
	sum := sha256.Sum256([]byte("nebu-mesh:" + id))
	return sum[:]
}

// Marks this node's record changed, for the conductor after a formation change
func (m *Manager) Bump() { m.bump() }
