// Package mesh joins nebu daemons under one certificate authority with gossip, beacons, links and exported pools
package mesh

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nickheyer/nebu/internal/auth"
	"github.com/nickheyer/nebu/internal/db"
	"github.com/nickheyer/nebu/internal/installs"
	"github.com/nickheyer/nebu/internal/mesh/links"
	"github.com/nickheyer/nebu/internal/pools"
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
	// A gossip round lasts this long, each one syncing with the fan-out and counting liveness
	SyncInterval = 5 * time.Second
	// Rounds a member may go unseen in every peer's view before it is unreachable
	UnreachableAfter = 3
	// How long a member may go unseen before it is gone
	GoneAfter = 10 * time.Minute
	// A gone member is synced with at most this often until it is seen again
	goneRetry = time.Minute
	// A session token lasts this long, renewed on every sync
	SessionTTL = 15 * time.Minute
	// Links are measured this often, and on join
	ProbeInterval = 5 * time.Minute
	// The mesh listener bound when mesh.listen names none, once the node belongs to a mesh or starts an admission
	DefaultListen = "0.0.0.0:8485"
	// Exposure settings for seats other seats connect to
	ExposureGuard  = "guard"
	ExposureDirect = "direct"
	// Record changes coalesce over this window
	publishDelay = 200 * time.Millisecond
	callTimeout  = 10 * time.Second
	// A failed renewal is tried again after this long
	renewRetry = time.Minute
)

var (
	// Returned when the node belongs to no mesh
	ErrNoMesh = errors.New("this node belongs to no mesh")
	// Returned when a mesh request is malformed or refused
	ErrMesh = errors.New("mesh")
	// Returned when a node id names no member
	ErrUnknownNode = errors.New("unknown node")
	// Returned once Close began
	errClosed = fmt.Errorf("%w: this node is shutting down", ErrMesh)
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
	// Drops a forgotten member's formation copies
	Forget(nodeID string)
	// Stops every formation conducted here and drops every record
	Clear(ctx context.Context)
}

// A member as this node tracks it
type member struct {
	rec *v1.Node
	// Whether the record came from gossip alone and no sync has filled it yet
	sketch bool
	// The sequence of this node's record the member holds whole, as its last answer said
	sent uint64
	// The highest heartbeat heard for the member in any view, when it last rose, and the last direct contact
	heartbeat uint64
	seenAt    time.Time
	direct    time.Time
	// The highest sequence of the member's record gossip named
	advertised uint64
	// The serial of the member's certificate, from its record or the gossip that named it
	serial string
	// The mesh state digest the member last sent
	digest    []byte
	lastTried time.Time
	// When the member's link was last probed and how many probes in a row failed since one measured it
	probedAt    time.Time
	probeMisses int
}

// The sequence of the member's record held whole, zero for a sketch
func (mb *member) held() uint64 {
	if mb.sketch {
		return 0
	}
	return mb.rec.GetSequence()
}

// The name the member is known by, empty for no member
func (mb *member) GetName() string {
	if mb == nil {
		return ""
	}
	return mb.rec.GetName()
}

// The address the member is known at, empty for no member
func (mb *member) GetAddress() string {
	if mb == nil {
		return ""
	}
	return mb.rec.GetAddress()
}

// The bearer a peer sends this node and the bearer this node sends it
type session struct {
	peer    string
	accept  string
	send    string
	expires time.Time
	granted time.Time
	// When the token we send expires, learned from the peer's sync answers
	sendExpires time.Time
}

// One part of this node's record with the sequence it last changed at and its digest
type partState struct {
	seq  uint64
	hash [32]byte
}

// Membership, certificates, gossip, discovery, links, and the pools members export
type Manager struct {
	DB       *db.DB
	Config   *v1.MeshConfig
	Host     *host.Prober
	Installs *installs.Manager
	Runtimes *runtimes.Registry
	Store    *store.Store
	Perf     *perf.Table
	// The pools and routes this node serves, read into its record
	Pools pools.View
	// Where pools and routes other members export are published
	Registrar pools.Registrar
	Settings  *settings.Manager
	Events    *events.Bus
	Guard     *auth.Guard
	Log       *slog.Logger
	Version   string
	// The API handler members reach on the mesh listener
	Handler http.Handler
	// Set by the daemon once the conductor exists
	Formations Formations
	// Requests in flight on an instance through this node's gateway, read while a withdrawn import drains
	InFlight func(instanceID string) int
	// How long a withdrawn import waits for its requests in flight before it is unpublished
	DrainTimeout time.Duration

	identity *db.Identity
	// Pools published for each exporter by pool id and the objectives held for it by name, guarded by importing
	imports    map[string]map[string]*imported
	objectives map[string]map[string][32]byte
	// Not published conditions of imports by pool id, guarded by viewing
	viewing     sync.Mutex
	unpublished map[string]*v1.Condition
	// Exported local addresses served over TLS on the mesh host, guarded by relaying
	relaying sync.Mutex
	relays   map[string]*relay
	// Where imported endpoint addresses lead, guarded by mu
	remoteAddrs map[string]remoteTarget
	// Nodes of other meshes this node imports from and exports to, guarded by mu
	exporters map[string]*federated
	importers map[string]*v1.Node
	// The address bound when mesh.listen names none
	defaultListen string
	// Signs one member's key under a new authority and hands it over, swapped by tests
	reissueMember func(ctx context.Context, id string, current []byte, ca *x509.Certificate, caKey ed25519.PrivateKey, now time.Time) error
	// The self signed certificate admissions run on, made at each start
	ident  tls.Certificate
	prober *links.Prober
	// How long a gossip round lasts
	interval time.Duration

	// One membership change at a time across make, join, leave, reset, forget, rotate and handoff
	changing sync.Mutex
	// Held from reading a record to writing it, so a write never lands after the clear that dropped it
	rows sync.Mutex
	// One record rebuild at a time, so a slower one never replaces a newer one
	building sync.Mutex
	// Guards the imports table across registrar calls
	importing sync.Mutex
	// One change of this node's credentials at a time, from reading them to installing the next
	crediting sync.Mutex

	mu sync.Mutex
	// Set once Close began, after which nothing binds or starts
	closed  bool
	mesh    *db.MeshRow
	creds   *credentials
	revoked map[string]*v1.Revocation
	members map[string]*member
	// Bearer tokens by peer, and the peer each accepted token names
	sessions map[string]*session
	accept   map[string]string
	links    map[string]*v1.Link
	// Nodes heard by beacon, admissions under way on both sides, and grants invitations carried
	nearby     map[string]*heard
	admissions map[string]*v1.Admission
	grants     map[string]string
	statusAt   *time.Timer
	// Transports by member address and trust
	h2 map[string]http.RoundTripper
	// The record sequence, the sequence this run began at, and the heartbeat counted each round
	seq       uint64
	epoch     uint64
	heartbeat uint64
	parts     map[v1.RecordPart]*partState
	// The record as last built with the formations and admissions it shares
	record     *v1.Node
	formations []*v1.Formation
	shared     []*v1.Admission
	dirty      bool
	// Whether a rebuild changed a part the fan-out hears of at once, and the sequence last published as an event
	unpushed   bool
	announced  uint64
	publishAt  *time.Timer
	renewTried time.Time
	listener   net.Listener
	server     *http.Server
	// Closed once the server stopped accepting
	served     chan struct{}
	listenAddr string
	base       context.Context
	cancel     context.CancelFunc
	wg         sync.WaitGroup
	wake       chan struct{}
	loopCtx    context.Context
	loopCancel context.CancelFunc
	loops      sync.WaitGroup
}

// Loads the identity, making one on first start, and the mesh with its credentials, members, sessions, links and revocations
func (m *Manager) Open(ctx context.Context) error {
	if m.Pools == nil || m.Registrar == nil {
		return fmt.Errorf("%w: the pool view and registrar are wired before the mesh opens", ErrMesh)
	}
	if m.Log == nil {
		m.Log = slog.Default()
	}
	if m.Config == nil {
		m.Config = &v1.MeshConfig{}
	}
	if m.interval == 0 {
		m.interval = SyncInterval
	}
	m.members = map[string]*member{}
	m.sessions = map[string]*session{}
	m.accept = map[string]string{}
	m.links = map[string]*v1.Link{}
	m.nearby = map[string]*heard{}
	m.admissions = map[string]*v1.Admission{}
	m.grants = map[string]string{}
	m.revoked = map[string]*v1.Revocation{}
	m.parts = map[v1.RecordPart]*partState{}
	m.h2 = map[string]http.RoundTripper{}
	m.wake = make(chan struct{}, 1)
	m.prober = &links.Prober{Log: m.Log}
	m.imports = map[string]map[string]*imported{}
	m.objectives = map[string]map[string][32]byte{}
	m.unpublished = map[string]*v1.Condition{}
	m.relays = map[string]*relay{}
	m.remoteAddrs = map[string]remoteTarget{}
	m.exporters = map[string]*federated{}
	m.importers = map[string]*v1.Node{}
	if m.defaultListen == "" {
		m.defaultListen = DefaultListen
	}
	if m.reissueMember == nil {
		m.reissueMember = m.reissueOne
	}
	// Sequences and heartbeats climb across restarts, so a returning node's record and liveness win over stale copies
	m.seq = uint64(time.Now().UnixMilli())
	m.epoch, m.heartbeat = m.seq, m.seq
	id, made, err := loadIdentity(ctx, m.DB)
	if err != nil {
		return err
	}
	if made {
		m.Log.Info("mesh identity made", "node", id.ID)
	}
	m.identity = id
	if m.ident, err = identityCertificate(id.ID, ed25519.PrivateKey(id.PrivateKey), id.CreatedAt); err != nil {
		return err
	}
	row, err := m.DB.GetMesh(ctx)
	if err != nil && !db.IsNotFound(err) {
		return err
	}
	if err == nil {
		creds, err := credentialsOf(row)
		if err != nil {
			return fmt.Errorf("%w: the mesh credentials on record are unreadable, reset the mesh: %v", ErrMesh, err)
		}
		m.mesh, m.creds = row, creds
	}
	if err := m.loadMembers(ctx); err != nil {
		return err
	}
	admissions, err := m.DB.ListAdmissions(ctx)
	if err != nil {
		return err
	}
	for _, a := range admissions {
		m.admissions[a.GetId()] = a
	}
	if m.grants, err = m.DB.AdmissionGrants(ctx); err != nil {
		return err
	}
	if m.Guard != nil {
		m.Guard.SetPeers(m)
	}
	if m.Perf != nil {
		m.Perf.OnChange = m.bump
	}
	return nil
}

// The credentials a mesh row holds
func credentialsOf(row *db.MeshRow) (*credentials, error) {
	key, err := parseKey(row.Key)
	if err != nil {
		return nil, err
	}
	var caKey ed25519.PrivateKey
	if len(row.CAKey) > 0 {
		if caKey, err = parseKey(row.CAKey); err != nil {
			return nil, err
		}
	}
	return newCredentials(row.CACertificate, row.Certificate, key, caKey)
}

// Loads members, sessions, links and revocations written last
func (m *Manager) loadMembers(ctx context.Context) error {
	revs, err := m.DB.ListRevocations(ctx)
	if err != nil {
		return err
	}
	for _, r := range revs {
		m.revoked[r.GetSerial()] = r
	}
	recs, err := m.DB.ListMembers(ctx)
	if err != nil {
		return err
	}
	for _, rec := range recs {
		switch {
		case rec.GetId() == m.identity.ID:
			continue
		case rec.GetFederation().GetRole() == v1.FederationRole_FEDERATION_ROLE_EXPORTER:
			m.exporters[rec.GetId()] = &federated{rec: rec}
			continue
		case rec.GetFederation().GetRole() == v1.FederationRole_FEDERATION_ROLE_IMPORTER:
			m.importers[rec.GetId()] = rec
			continue
		case m.mesh == nil:
			continue
		}
		mb := &member{rec: rec, sketch: rec.GetSequence() == 0, seenAt: rec.GetSeenAt().AsTime(), serial: recordSerial(rec)}
		m.members[rec.GetId()] = mb
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
	list, err := m.DB.ListLinks(ctx, m.identity.ID)
	if err != nil {
		return err
	}
	for _, l := range list {
		m.links[l.GetTo()] = l
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
	out := &v1.Mesh{
		Id:          m.mesh.ID,
		Name:        m.mesh.Name,
		Self:        m.identity.ID,
		CreatedAt:   timestamppb.New(m.mesh.CreatedAt),
		JoinedAt:    timestamppb.New(m.mesh.JoinedAt),
		Address:     m.advertiseLocked(),
		Announce:    m.announceOn(),
		Exposure:    m.Exposure(),
		Members:     uint32(len(m.members) + 1),
		Issuer:      m.mesh.Issuer,
		IssuerEpoch: m.mesh.IssuerEpoch,
		Revocations: m.revocationsLocked(),
	}
	if m.creds != nil {
		out.CaFingerprint = m.creds.caFingerprint()
		out.CertificateExpiresAt = timestamppb.New(m.creds.leaf.NotAfter)
	}
	return out
}

// Whether multicast beacons are on
func (m *Manager) announceOn() bool {
	return m.Config.Announce == nil || m.Config.GetAnnounce()
}

// How seats other seats connect to are exposed, guard or direct
func (m *Manager) Exposure() string {
	if strings.EqualFold(m.Config.GetExposure(), ExposureDirect) {
		return ExposureDirect
	}
	return ExposureGuard
}

// The inclusive port range seats, guards and rendezvous take on the mesh address, if configured
func (m *Manager) Ports() (int, int, bool) {
	spec := strings.TrimSpace(m.Config.GetPorts())
	if spec == "" {
		return 0, 0, false
	}
	lo, hi, found := strings.Cut(spec, "-")
	if !found {
		hi = lo
	}
	from, err := strconv.Atoi(strings.TrimSpace(lo))
	if err != nil || from <= 0 || from > 65535 {
		return 0, 0, false
	}
	to, err := strconv.Atoi(strings.TrimSpace(hi))
	if err != nil || to < from || to > 65535 {
		return 0, 0, false
	}
	return from, to, true
}

// Starts beacons, admission expiry and the import pulls, the listener once a mesh exists or mesh.listen names it, and the gossip and probe loops inside a mesh
func (m *Manager) Start(ctx context.Context) error {
	if m.Handler == nil {
		return fmt.Errorf("%w: the API handler is wired before the mesh starts", ErrMesh)
	}
	if m.InFlight == nil {
		return fmt.Errorf("%w: the gateway's in flight counter is wired before the mesh starts", ErrMesh)
	}
	m.base, m.cancel = context.WithCancel(ctx)
	m.mu.Lock()
	joined := m.mesh != nil
	m.mu.Unlock()
	if joined || m.Config.GetListen() != "" {
		if err := m.listen(); err != nil {
			return err
		}
	}
	for _, loop := range []func(context.Context){m.watch, m.announceLoop, m.admissionLoop, m.federationLoop} {
		if !m.spawn(func() { loop(m.base) }) {
			return errClosed
		}
	}
	if joined {
		m.startLoops()
		m.publishSelf()
		for id, mb := range m.snapshotMembers() {
			m.Events.Publish(v1.EventKind_EVENT_KIND_NODE, v1.EventAction_EVENT_ACTION_CREATED, id, mb)
			m.importFrom(m.base, id)
		}
	}
	m.mu.Lock()
	exporters := slices.Sorted(maps.Keys(m.exporters))
	m.mu.Unlock()
	for _, id := range exporters {
		m.importFrom(m.base, id)
	}
	m.publishStatus()
	return nil
}

// Starts the loops a member runs, once per membership, and returns the context they run under
func (m *Manager) startLoops() context.Context {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.loopCancel != nil {
		return m.loopCtx
	}
	ctx, cancel := context.WithCancel(m.base)
	if m.closed {
		cancel()
		return ctx
	}
	m.loopCtx, m.loopCancel = ctx, cancel
	m.loops.Add(2)
	m.spawnLocked(func() {
		defer m.loops.Done()
		m.gossipLoop(ctx)
	})
	m.spawnLocked(func() {
		defer m.loops.Done()
		m.probeLoop(ctx)
	})
	return ctx
}

// Counts work Close waits for, false once Close began
func (m *Manager) holdLocked() bool {
	if m.closed {
		return false
	}
	m.wg.Add(1)
	return true
}

// Counts work Close waits for, false once Close began
func (m *Manager) hold() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.holdLocked()
}

// Runs work in the background with Close waiting for it, false once Close began
func (m *Manager) spawnLocked(work func()) bool {
	if !m.holdLocked() {
		return false
	}
	go func() {
		defer m.wg.Done()
		work()
	}()
	return true
}

// Runs work in the background with Close waiting for it, false once Close began
func (m *Manager) spawn(work func()) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.spawnLocked(work)
}

// Stops the member loops and waits for them
func (m *Manager) stopLoops() {
	m.mu.Lock()
	cancel := m.loopCancel
	m.loopCtx, m.loopCancel = nil, nil
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	m.loops.Wait()
}

// Stops loops and the listener, returning once every request and background task has ended
func (m *Manager) Close() {
	m.mu.Lock()
	m.closed = true
	srv, ln := m.server, m.listener
	m.server, m.listener = nil, nil
	m.mu.Unlock()
	if m.cancel != nil {
		m.cancel()
	}
	if srv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if srv.Shutdown(ctx) != nil {
			srv.Close()
		}
		cancel()
	}
	if ln != nil {
		ln.Close()
	}
	m.closeRelays()
	m.wg.Wait()
	m.dropTransports("")
}

// Marks this node's record for a rebuild, the parts that changed pushed to the fan-out
func (m *Manager) bump() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.dirty = true
	if m.publishAt != nil {
		return
	}
	m.publishAt = time.AfterFunc(publishDelay, func() {
		m.mu.Lock()
		m.publishAt = nil
		held := m.holdLocked()
		m.mu.Unlock()
		if !held {
			return
		}
		defer m.wg.Done()
		m.refresh()
		m.mu.Lock()
		push, announce := m.unpushed, m.seq > m.announced
		m.unpushed, m.announced = false, m.seq
		m.mu.Unlock()
		if announce {
			m.publishSelf()
		}
		if push {
			m.wakeLoop()
		}
	})
}

// Wakes the gossip loop for an immediate push to the fan-out
func (m *Manager) wakeLoop() {
	select {
	case m.wake <- struct{}{}:
	default:
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

// Membership, the nodes and meshes heard nearby, and admissions under way, as the Mesh page draws them
func (m *Manager) Status() *v1.MeshStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := &v1.MeshStatus{
		NodeId:      m.identity.ID,
		NodeName:    m.name(),
		Listen:      m.listenAddr,
		Address:     m.advertiseLocked(),
		Announce:    m.announceOn(),
		Nodes:       m.nearbyListLocked(),
		Meshes:      m.meshesNearbyLocked(),
		Admissions:  m.admissionListLocked(),
		Federations: m.federationsLocked(),
	}
	if m.mesh != nil {
		st.Mesh = m.meshLocked()
	}
	return st
}

// Publishes the status, changes within a short window folded into one event
func (m *Manager) publishStatus() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.statusAt != nil {
		return
	}
	m.statusAt = time.AfterFunc(publishDelay, func() {
		m.mu.Lock()
		m.statusAt = nil
		held := m.holdLocked()
		m.mu.Unlock()
		if !held {
			return
		}
		defer m.wg.Done()
		m.Events.Publish(v1.EventKind_EVENT_KIND_MESH, v1.EventAction_EVENT_ACTION_UPDATED, "mesh", m.Status())
	})
}

// Marks the record for a rebuild whenever something it carries may have changed
func (m *Manager) watch(ctx context.Context) {
	kinds := []v1.EventKind{v1.EventKind_EVENT_KIND_HOST, v1.EventKind_EVENT_KIND_INSTALL, v1.EventKind_EVENT_KIND_MODEL, v1.EventKind_EVENT_KIND_STORE, v1.EventKind_EVENT_KIND_ROUTE, v1.EventKind_EVENT_KIND_POOL, v1.EventKind_EVENT_KIND_SLOT, v1.EventKind_EVENT_KIND_INSTANCE, v1.EventKind_EVENT_KIND_FORMATION, v1.EventKind_EVENT_KIND_SETTINGS}
	sub := m.Events.Subscribe(ctx, kinds)
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-sub.Events():
			// A copy of another member's formation changes nothing about this node
			if f := ev.GetFormation(); f != nil && f.GetConductor() != m.identity.ID {
				continue
			}
			if ev.GetSeq() > 0 {
				m.bump()
			}
		}
	}
}

// A stored model as a member advertises it, with its descriptor digest
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

// The host label, else the hostname, other members show for this node
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

// Each non loopback interface address on the mesh port
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

// Members' records, this node excluded
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
	if mb, ok := m.members[id]; ok {
		defer m.mu.Unlock()
		return proto.Clone(mb.rec).(*v1.Node), nil
	}
	for _, mb := range m.members {
		if mb.rec.GetName() == id {
			defer m.mu.Unlock()
			return proto.Clone(mb.rec).(*v1.Node), nil
		}
	}
	m.mu.Unlock()
	if m.name() == id {
		return m.Record(), nil
	}
	return nil, fmt.Errorf("%w %q", ErrUnknownNode, id)
}

// The member record behind a client, for callers that need its facts
func (m *Manager) Member(nodeID string) (*v1.Node, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	mb, ok := m.members[nodeID]
	if !ok {
		return nil, false
	}
	return proto.Clone(mb.rec).(*v1.Node), true
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

// The member a bearer token names, when this node minted it and it has not expired
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

// A random URL safe token of 32 bytes
func randomToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

// The hash of a mesh id that beacons, admissions and grants carry, so a node answers only for its mesh
func meshHash(id string) []byte {
	sum := sha256.Sum256([]byte("nebu-mesh:" + id))
	return sum[:]
}

// Reads this node's identity, making and storing one on first start, true when it was made
func loadIdentity(ctx context.Context, store *db.DB) (*db.Identity, bool, error) {
	id, err := store.GetIdentity(ctx)
	if err == nil || !db.IsNotFound(err) {
		return id, false, err
	}
	if id, err = newIdentity(); err != nil {
		return nil, false, err
	}
	if err := store.PutIdentity(ctx, id); err != nil {
		return nil, false, err
	}
	return id, true, nil
}

// This node's id, made and stored on first start, for managers built before the mesh opens
func NodeID(ctx context.Context, store *db.DB) (string, error) {
	id, _, err := loadIdentity(ctx, store)
	if err != nil {
		return "", err
	}
	return id.ID, nil
}

// The hash of a mesh id in hex
func meshHex(id string) string {
	return hex.EncodeToString(meshHash(id))
}

// The hash of a mesh id as beacons and admissions carry it, for callers outside the package
func HashOf(id string) string { return meshHex(id) }

// Marks this node's record for a rebuild, for the conductor after a formation change
func (m *Manager) Bump() { m.bump() }
