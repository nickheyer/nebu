package mesh

import (
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/proto/nebu/v1/nebuv1connect"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Export tokens start with this, so a join token is never taken for one
const exportTokenPrefix = "nebu-export."

// What an export token names, signed by the exporter's identity key
type exportGrant struct {
	Nonce       string    `json:"nonce"`
	MeshHash    string    `json:"mesh"`
	Node        string    `json:"node"`
	Address     string    `json:"address"`
	Fingerprint string    `json:"fingerprint"`
	Expires     time.Time `json:"expires"`
}

// A node of another mesh this node imports from, with the record it last answered and how its pulls go
type federated struct {
	rec       *v1.Node
	failures  int
	lastTried time.Time
}

// Mints a token a node of another mesh presents to import this node's exported pools
func (m *Manager) ExportToken(ctx context.Context, ttl time.Duration) (string, time.Time, error) {
	if ttl <= 0 {
		ttl = DefaultGrantTTL
	}
	m.mu.Lock()
	joined := m.mesh != nil
	var hash string
	if joined {
		hash = meshHex(m.mesh.ID)
	}
	address := m.advertiseLocked()
	m.mu.Unlock()
	if !joined {
		return "", time.Time{}, ErrNoMesh
	}
	if address == "" {
		return "", time.Time{}, fmt.Errorf("%w: this node listens on loopback alone, so no other mesh can reach it. Set mesh.listen or mesh.advertise", ErrMesh)
	}
	nonce, err := randomToken()
	if err != nil {
		return "", time.Time{}, err
	}
	g := exportGrant{Nonce: nonce[:32], MeshHash: hash, Node: m.identity.ID, Address: address, Fingerprint: m.identityFingerprint(), Expires: time.Now().Add(ttl)}
	payload, err := json.Marshal(g)
	if err != nil {
		return "", time.Time{}, err
	}
	sig := ed25519.Sign(ed25519.PrivateKey(m.identity.PrivateKey), payload)
	token := exportTokenPrefix + base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(sig)
	return token, g.Expires, nil
}

// Reads an export token's claims and signature without checking them
func decodeExportToken(token string) (exportGrant, []byte, []byte, error) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(token), exportTokenPrefix)
	if !ok {
		return exportGrant{}, nil, nil, fmt.Errorf("%w: this is not an export token", ErrMesh)
	}
	body, sig, ok := strings.Cut(rest, ".")
	if !ok {
		return exportGrant{}, nil, nil, fmt.Errorf("%w: the export token carries no signature", ErrMesh)
	}
	payload, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return exportGrant{}, nil, nil, fmt.Errorf("%w: the export token is not readable: %v", ErrMesh, err)
	}
	signature, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil {
		return exportGrant{}, nil, nil, fmt.Errorf("%w: the export token signature is not readable: %v", ErrMesh, err)
	}
	var g exportGrant
	if err := json.Unmarshal(payload, &g); err != nil {
		return exportGrant{}, nil, nil, fmt.Errorf("%w: the export token is not readable: %v", ErrMesh, err)
	}
	if g.Node == "" || g.Address == "" || g.Fingerprint == "" || g.Nonce == "" {
		return exportGrant{}, nil, nil, fmt.Errorf("%w: the export token names no node, address, fingerprint or nonce", ErrMesh)
	}
	return g, payload, signature, nil
}

// Checks an export token this node minted, its signature, expiry and mesh, then takes its nonce once
func (m *Manager) checkExportToken(ctx context.Context, token string, now time.Time) (exportGrant, error) {
	g, payload, sig, err := decodeExportToken(token)
	if err != nil {
		return exportGrant{}, err
	}
	if !ed25519.Verify(ed25519.PublicKey(m.identity.PublicKey), payload, sig) {
		return exportGrant{}, fmt.Errorf("%w: the export token is not signed by this node", ErrMesh)
	}
	if g.Node != m.identity.ID {
		return exportGrant{}, fmt.Errorf("%w: the export token names node %s, not this one", ErrMesh, g.Node)
	}
	if now.After(g.Expires) {
		return exportGrant{}, fmt.Errorf("%w: the export token expired at %s", ErrMesh, g.Expires.Format(time.RFC3339))
	}
	m.mu.Lock()
	joined := m.mesh != nil
	var hash string
	if joined {
		hash = meshHex(m.mesh.ID)
	}
	m.mu.Unlock()
	if !joined {
		return exportGrant{}, ErrNoMesh
	}
	if g.MeshHash != hash {
		return exportGrant{}, fmt.Errorf("%w: the export token names another mesh", ErrMesh)
	}
	fresh, err := m.DB.UseGrant(ctx, g.Nonce, g.Expires)
	if err != nil {
		return exportGrant{}, err
	}
	if !fresh {
		return exportGrant{}, fmt.Errorf("%w: the export token was used already; mint another", ErrMesh)
	}
	return g, nil
}

// This node's exported pools, routes and objectives as another mesh takes them
func (m *Manager) exportRecord() *v1.Node {
	rec := m.Record()
	return &v1.Node{Id: rec.GetId(), Name: rec.GetName(), Address: rec.GetAddress(), Version: rec.GetVersion(), State: v1.NodeState_NODE_STATE_READY, SeenAt: timestamppb.Now(), Pools: rec.GetPools(), Routes: rec.GetRoutes(), Objectives: rec.GetObjectives()}
}

// Takes an export token from a node of another mesh, records it as an importer, and answers with the exported record and its session
func (m *Manager) federate(ctx context.Context, c caller, req *v1.FederateRequest) (*v1.FederateResponse, error) {
	g, err := m.checkExportToken(ctx, req.GetToken(), time.Now())
	if err != nil {
		return nil, err
	}
	node := req.GetNode()
	if node.GetId() != c.id {
		return nil, fmt.Errorf("%w: the importer presents its own identity", ErrMesh)
	}
	if m.Joined() && node.GetMeshHash() == g.MeshHash {
		return nil, fmt.Errorf("%w: %s belongs to this mesh already, exports inside a mesh need no token", ErrMesh, node.GetName())
	}
	rec := &v1.Node{
		Id: c.id, Name: node.GetName(), Address: node.GetAddress(), Version: node.GetVersion(), State: v1.NodeState_NODE_STATE_READY, SeenAt: timestamppb.Now(),
		Federation: &v1.Federation{Role: v1.FederationRole_FEDERATION_ROLE_IMPORTER, Fingerprint: fingerprintOf(c.cert.Raw), MeshHash: node.GetMeshHash(), MeshName: node.GetMeshName(), Address: node.GetAddress(), Since: timestamppb.Now()},
	}
	m.rows.Lock()
	if err := m.DB.PutMember(ctx, rec); err != nil {
		m.rows.Unlock()
		return nil, err
	}
	m.mu.Lock()
	m.importers[c.id] = rec
	m.mu.Unlock()
	m.rows.Unlock()
	token, expires, err := m.renewSession(c.id)
	if err != nil {
		return nil, err
	}
	m.Log.Info("mesh importer admitted", "node", c.id, "name", node.GetName(), "mesh", node.GetMeshName())
	m.publishStatus()
	return &v1.FederateResponse{MeshHash: g.MeshHash, MeshName: m.meshName(), Node: m.exportRecord(), SessionToken: token, ExpiresAt: timestamppb.New(expires)}, nil
}

// Answers an importer's pull with the exported record and a renewed session
func (m *Manager) exports(c caller) (*v1.ExportsResponse, error) {
	token, expires, err := m.renewSession(c.id)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	var hash string
	if m.mesh != nil {
		hash = meshHex(m.mesh.ID)
	}
	m.mu.Unlock()
	return &v1.ExportsResponse{Node: m.exportRecord(), MeshHash: hash, SessionToken: token, ExpiresAt: timestamppb.New(expires)}, nil
}

// The name of this node's mesh, empty outside one
func (m *Manager) meshName() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.mesh == nil {
		return ""
	}
	return m.mesh.Name
}

// The importer a pinned identity certificate names, false when the fingerprint is no importer's
func (m *Manager) importerByPin(fingerprint string) (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, rec := range m.importers {
		if rec.GetFederation().GetFingerprint() == fingerprint {
			return id, true
		}
	}
	return "", false
}

// TLS to an exporter of another mesh by its pinned identity certificate
func (m *Manager) exporterDialConfig(nodeID string) *tls.Config {
	m.mu.Lock()
	pin := m.exporters[nodeID].pinLocked()
	m.mu.Unlock()
	return m.admissionDialConfig(nodeID, pin, nil)
}

// The identity fingerprint an exporter is pinned by, empty for none
func (f *federated) pinLocked() string {
	if f == nil {
		return ""
	}
	return f.rec.GetFederation().GetFingerprint()
}

// The membership client to an exporter of another mesh, dialed by identity certificate
func (m *Manager) exporterClient(nodeID string) (nebuv1connect.MeshPeerServiceClient, string, error) {
	m.mu.Lock()
	f, ok := m.exporters[nodeID]
	var address string
	if ok {
		address = f.rec.GetFederation().GetAddress()
	}
	m.mu.Unlock()
	if !ok {
		return nil, "", fmt.Errorf("%w %q", ErrUnknownNode, nodeID)
	}
	rt := m.roundTripper(address+"|federated|"+nodeID, m.exporterDialConfig(nodeID))
	return nebuv1connect.NewMeshPeerServiceClient(&http.Client{Transport: rt}, "https://"+address), address, nil
}

// Presents an export token to the node of another mesh it names and imports that node's pools from then on
func (m *Manager) ImportMesh(ctx context.Context, token string) (*v1.Node, error) {
	g, _, _, err := decodeExportToken(token)
	if err != nil {
		return nil, err
	}
	if time.Now().After(g.Expires) {
		return nil, fmt.Errorf("%w: the export token expired at %s", ErrMesh, g.Expires.Format(time.RFC3339))
	}
	if g.Node == m.identity.ID {
		return nil, fmt.Errorf("%w: a node does not import from itself", ErrMesh)
	}
	m.mu.Lock()
	if _, member := m.members[g.Node]; member {
		m.mu.Unlock()
		return nil, fmt.Errorf("%w: %s is a member of this mesh, its pools are imported without a token", ErrMesh, g.Node)
	}
	self := m.selfNearbyLocked()
	m.mu.Unlock()
	rt := transport(m.admissionDialConfig(g.Node, g.Fingerprint, nil))
	defer rt.CloseIdleConnections()
	cl := nebuv1connect.NewMeshPeerServiceClient(&http.Client{Transport: rt}, "https://"+g.Address)
	cctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	resp, err := cl.Federate(cctx, connect.NewRequest(&v1.FederateRequest{Token: token, Node: self}))
	if err != nil {
		return nil, fmt.Errorf("federate with %s: %w", g.Address, err)
	}
	out := resp.Msg
	if out.GetNode().GetId() != g.Node {
		return nil, fmt.Errorf("%w: the node at %s answered as %s, not %s", ErrMesh, g.Address, out.GetNode().GetId(), g.Node)
	}
	rec := proto.Clone(out.GetNode()).(*v1.Node)
	rec.State, rec.SeenAt = v1.NodeState_NODE_STATE_READY, timestamppb.Now()
	rec.Federation = &v1.Federation{Role: v1.FederationRole_FEDERATION_ROLE_EXPORTER, MeshHash: out.GetMeshHash(), MeshName: out.GetMeshName(), Fingerprint: g.Fingerprint, Address: g.Address, Since: timestamppb.Now()}
	m.rows.Lock()
	if err := m.DB.PutMember(ctx, rec); err != nil {
		m.rows.Unlock()
		return nil, err
	}
	m.mu.Lock()
	m.exporters[g.Node] = &federated{rec: rec, lastTried: time.Now()}
	m.takeSessionLocked(g.Node, out.GetSessionToken(), out.GetExpiresAt().AsTime())
	m.mu.Unlock()
	m.rows.Unlock()
	if err := m.persistSession(g.Node); err != nil {
		return nil, err
	}
	m.Log.Info("mesh export imported", "node", g.Node, "name", rec.GetName(), "mesh", out.GetMeshName(), "address", g.Address)
	m.importFrom(ctx, g.Node)
	m.Events.Publish(v1.EventKind_EVENT_KIND_NODE, v1.EventAction_EVENT_ACTION_CREATED, g.Node, proto.Clone(rec).(*v1.Node))
	m.publishStatus()
	return proto.Clone(rec).(*v1.Node), nil
}

// Keeps the bearer a peer handed this node, with its expiry
func (m *Manager) takeSessionLocked(peer, token string, expires time.Time) {
	s := m.sessions[peer]
	if s == nil {
		s = &session{peer: peer, granted: time.Now()}
		m.sessions[peer] = s
	}
	s.send, s.sendExpires = token, expires
}

// Pulls an exporter's record, marking it unreachable after repeated misses and dropping it once the exporter refuses this node
func (m *Manager) pullExports(ctx context.Context, id string) error {
	cl, _, err := m.exporterClient(id)
	if err != nil {
		return err
	}
	cctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	resp, err := cl.Exports(cctx, connect.NewRequest(&v1.ExportsRequest{}))
	if err != nil {
		if connect.CodeOf(err) == connect.CodePermissionDenied {
			m.Log.Warn("mesh exporter refused this node, its export is dropped", "node", id, "err", err)
			if _, derr := m.DropFederation(ctx, id); derr != nil {
				return errors.Join(err, derr)
			}
			return err
		}
		m.mu.Lock()
		f, ok := m.exporters[id]
		state := v1.NodeState_NODE_STATE_UNREACHABLE
		var rec *v1.Node
		if ok {
			f.failures++
			f.lastTried = time.Now()
			if f.failures >= UnreachableAfter && f.rec.GetState() != state {
				f.rec.State = state
				rec = proto.Clone(f.rec).(*v1.Node)
			}
		}
		m.mu.Unlock()
		if rec != nil {
			m.Log.Warn("mesh exporter unreachable", "node", id, "err", err)
			m.Events.Publish(v1.EventKind_EVENT_KIND_NODE, v1.EventAction_EVENT_ACTION_UPDATED, id, rec)
			m.importFrom(ctx, id)
		}
		return err
	}
	out := resp.Msg
	m.mu.Lock()
	f, ok := m.exporters[id]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("%w %q", ErrUnknownNode, id)
	}
	f.failures, f.lastTried = 0, time.Now()
	before := f.rec.GetState()
	node := out.GetNode()
	f.rec.Name, f.rec.Version, f.rec.Address = node.GetName(), node.GetVersion(), node.GetAddress()
	f.rec.Pools, f.rec.Routes, f.rec.Objectives = node.GetPools(), node.GetRoutes(), node.GetObjectives()
	f.rec.State, f.rec.SeenAt = v1.NodeState_NODE_STATE_READY, timestamppb.Now()
	f.rec.Federation.MeshHash = out.GetMeshHash()
	m.takeSessionLocked(id, out.GetSessionToken(), out.GetExpiresAt().AsTime())
	rec := proto.Clone(f.rec).(*v1.Node)
	m.mu.Unlock()
	if err := m.persistSession(id); err != nil {
		return err
	}
	m.rows.Lock()
	err = m.DB.PutMember(ctx, rec)
	m.rows.Unlock()
	if err != nil {
		return err
	}
	m.importFrom(ctx, id)
	action := v1.EventAction_EVENT_ACTION_UPDATED
	if before != v1.NodeState_NODE_STATE_READY {
		m.Log.Info("mesh exporter reachable", "node", id)
	}
	m.Events.Publish(v1.EventKind_EVENT_KIND_NODE, action, id, rec)
	return nil
}

// Pulls every exporter's record once per interval, each on its own goroutine
func (m *Manager) federationLoop(ctx context.Context) {
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.mu.Lock()
			ids := make([]string, 0, len(m.exporters))
			for id := range m.exporters {
				ids = append(ids, id)
			}
			m.mu.Unlock()
			var wg sync.WaitGroup
			for _, id := range ids {
				wg.Go(func() { m.pullExports(ctx, id) })
			}
			wg.Wait()
		}
	}
}

// Ends the export to or the import from a node of another mesh, its imports drained and its session dropped
func (m *Manager) DropFederation(ctx context.Context, ref string) (*v1.Node, error) {
	m.mu.Lock()
	var rec *v1.Node
	id := ""
	for nodeID, f := range m.exporters {
		if nodeID == ref || f.rec.GetName() == ref {
			id, rec = nodeID, proto.Clone(f.rec).(*v1.Node)
		}
	}
	for nodeID, r := range m.importers {
		if nodeID == ref || r.GetName() == ref {
			id, rec = nodeID, proto.Clone(r).(*v1.Node)
		}
	}
	if rec == nil {
		m.mu.Unlock()
		return nil, fmt.Errorf("%w %q", ErrUnknownNode, ref)
	}
	address := rec.GetFederation().GetAddress()
	delete(m.exporters, id)
	delete(m.importers, id)
	if s := m.sessions[id]; s != nil {
		delete(m.accept, s.accept)
		delete(m.sessions, id)
	}
	m.mu.Unlock()
	m.rows.Lock()
	err := errors.Join(m.DB.DeleteMember(ctx, id), m.DB.DeleteSession(ctx, id))
	m.rows.Unlock()
	if err != nil {
		return nil, err
	}
	if address != "" {
		m.dropTransports(address)
	}
	m.dropImports(ctx, id)
	m.Log.Info("mesh federation dropped", "node", id, "name", rec.GetName(), "role", rec.GetFederation().GetRole())
	m.Events.Publish(v1.EventKind_EVENT_KIND_NODE, v1.EventAction_EVENT_ACTION_DELETED, id, rec)
	m.publishStatus()
	return rec, nil
}

// Nodes of other meshes this node exports to or imports from, by name
func (m *Manager) Federations() []*v1.Node {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.federationsLocked()
}

func (m *Manager) federationsLocked() []*v1.Node {
	out := make([]*v1.Node, 0, len(m.exporters)+len(m.importers))
	for _, f := range m.exporters {
		out = append(out, proto.Clone(f.rec).(*v1.Node))
	}
	for _, r := range m.importers {
		out = append(out, proto.Clone(r).(*v1.Node))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].GetName() != out[j].GetName() {
			return out[i].GetName() < out[j].GetName()
		}
		return out[i].GetId() < out[j].GetId()
	})
	return out
}

// The record and mesh hash a pool is imported from, a member's or an exporter's of another mesh
func (m *Manager) exportSource(exporter string) (*v1.Node, string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if mb, ok := m.members[exporter]; ok {
		hash := ""
		if m.mesh != nil {
			hash = meshHex(m.mesh.ID)
		}
		return proto.Clone(mb.rec).(*v1.Node), hash, true
	}
	if f, ok := m.exporters[exporter]; ok {
		return proto.Clone(f.rec).(*v1.Node), f.rec.GetFederation().GetMeshHash(), true
	}
	return nil, "", false
}
