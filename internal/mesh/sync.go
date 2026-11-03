package mesh

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"fmt"
	"slices"
	"sort"
	"time"

	"connectrpc.com/connect"
	"github.com/nickheyer/nebu/internal/db"
	"github.com/nickheyer/nebu/pkg/config"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Syncs with one member, sending the parts it lacks and taking the parts this node lacks of its record
func (m *Manager) syncWith(ctx context.Context, id string) error {
	m.mu.Lock()
	mb, ok := m.members[id]
	if m.mesh == nil || m.creds == nil {
		m.mu.Unlock()
		return ErrNoMesh
	}
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("%w %q", ErrUnknownNode, id)
	}
	address, held, sent, seen := mb.rec.GetAddress(), mb.held(), mb.sent, mb.digest
	mb.lastTried = time.Now()
	// A spoke exchanges records with its hubs alone, any other sync carrying membership and a session only
	bare := m.topology() == config.MeshTopologySpoke && !slices.Contains(m.hubsLocked(), id)
	m.mu.Unlock()
	if address == "" {
		return fmt.Errorf("%w: %s has no mesh address yet", ErrMesh, id)
	}
	node, formations, admissions := m.outgoing(sent)
	mode := m.syncMode()
	if mode == config.MeshSyncPull || bare {
		// A pull carries nothing of this node's record, the callee learning it from the pushes of others
		node, formations, admissions = &v1.Node{Id: m.identity.ID}, nil, nil
	}
	state, digest := m.meshState()
	req := &v1.SyncRequest{Node: node, Formations: formations, Admissions: admissions, Members: m.memberList(), DeltaBase: held, MeshDigest: digest}
	if !bytes.Equal(digest, seen) {
		req.Mesh = state
	}
	cctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	resp, err := m.peerClient(id, address).Sync(cctx, connect.NewRequest(req))
	if err != nil {
		m.Log.Debug("mesh sync missed", "node", id, "err", err)
		return err
	}
	out := resp.Msg
	m.mu.Lock()
	if mb, ok := m.members[id]; ok {
		mb.sent, mb.digest = out.GetHeld(), out.GetMeshDigest()
	}
	if s := m.sessions[id]; s != nil || out.GetSessionToken() != "" {
		if s == nil {
			s = &session{peer: id, granted: time.Now()}
			m.sessions[id] = s
		}
		s.send, s.sendExpires = out.GetSessionToken(), out.GetExpiresAt().AsTime()
	}
	m.mu.Unlock()
	if err := m.persistSession(id); err != nil {
		m.Log.Warn("session write failed", "peer", id, "err", err)
	}
	if mode != config.MeshSyncPush && !bare {
		m.take(id, nil, out.GetNode(), out.GetFormations(), out.GetAdmissions())
	}
	m.mergeMembers(out.GetMembers())
	m.mergeMeshState(out.GetMesh())
	return nil
}

// Answers a member's sync, taking its record parts, members and mesh state and returning this node's with a renewed bearer
func (m *Manager) sync(ctx context.Context, c caller, req *v1.SyncRequest) (*v1.SyncResponse, error) {
	if !m.Joined() {
		return nil, connect.NewError(connect.CodeFailedPrecondition, ErrNoMesh)
	}
	if req.GetNode().GetId() != c.id {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("%w: a sync carries the caller's own record", ErrMesh))
	}
	if m.isRevoked(serialOf(c.cert)) {
		return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("%w: certificate %s is revoked", ErrMesh, serialOf(c.cert)))
	}
	held := m.take(c.id, c.cert, req.GetNode(), req.GetFormations(), req.GetAdmissions())
	m.mergeMembers(req.GetMembers())
	m.mergeMeshState(req.GetMesh())
	token, expires, err := m.renewSession(c.id)
	if err != nil {
		return nil, err
	}
	node, formations, admissions := m.outgoing(req.GetDeltaBase())
	state, digest := m.meshState()
	resp := &v1.SyncResponse{Node: node, Formations: formations, Admissions: admissions, Members: m.memberList(), SessionToken: token, ExpiresAt: timestamppb.New(expires), Held: held, MeshDigest: digest}
	if !bytes.Equal(digest, req.GetMeshDigest()) {
		resp.Mesh = state
	}
	return resp, nil
}

// Takes a member's own record copy, its certificate admitting it, and returns the sequence held whole
func (m *Manager) take(id string, cert *x509.Certificate, in *v1.Node, formations []*v1.Formation, admissions []*v1.Admission) uint64 {
	if in.GetId() != id || id == m.identity.ID {
		return 0
	}
	m.mu.Lock()
	if m.mesh == nil {
		m.mu.Unlock()
		return 0
	}
	mb, known := m.members[id]
	if !known && cert == nil {
		// An answer from a member dropped while the sync was out brings nothing back
		m.mu.Unlock()
		return 0
	}
	if !known {
		mb = &member{rec: &v1.Node{Id: id}, sketch: true}
		m.members[id] = mb
	}
	stateBefore := mb.rec.GetState()
	oldAddress := mb.rec.GetAddress()
	now := time.Now()
	mb.direct, mb.seenAt = now, now
	mb.rec.State, mb.rec.SeenAt = v1.NodeState_NODE_STATE_READY, timestamppb.New(now)
	held, changed := m.applyRecordLocked(mb, in)
	if cert != nil {
		mb.rec.Certificate = cert.Raw
	}
	if serial := recordSerial(mb.rec); serial != "" {
		mb.serial = serial
	}
	relinked := m.reclassifyLocked(id)
	out := proto.Clone(mb.rec).(*v1.Node)
	m.mu.Unlock()
	if oldAddress != "" && oldAddress != out.GetAddress() {
		m.dropTransports(oldAddress)
	}
	if relinked {
		m.persistLink(id)
		m.bump()
	}
	if len(changed) > 0 || !known || stateBefore != v1.NodeState_NODE_STATE_READY {
		m.persist(id)
		action := v1.EventAction_EVENT_ACTION_UPDATED
		if !known {
			action = v1.EventAction_EVENT_ACTION_CREATED
		}
		m.Events.Publish(v1.EventKind_EVENT_KIND_NODE, action, id, out)
	}
	if m.Formations != nil && stateBefore != v1.NodeState_NODE_STATE_READY {
		m.Formations.MemberState(id, v1.NodeState_NODE_STATE_READY)
	}
	for _, p := range changed {
		switch p {
		case v1.RecordPart_RECORD_PART_FORMATIONS:
			if m.Formations != nil {
				m.Formations.Merge(id, formations)
			}
		case v1.RecordPart_RECORD_PART_ADMISSIONS:
			m.mergeAdmissions(admissions)
		}
	}
	if !known {
		m.Log.Info("mesh member met", "node", id, "name", out.GetName(), "address", out.GetAddress())
		m.markJoined(id)
		m.publishStatus()
	}
	if !known || stateBefore != v1.NodeState_NODE_STATE_READY || touchesImports(changed) {
		m.importFrom(context.Background(), id)
	}
	return held
}

// Whether changed parts touch what this node imports from the member
func touchesImports(changed []v1.RecordPart) bool {
	for _, p := range changed {
		if p == v1.RecordPart_RECORD_PART_POOLS || p == v1.RecordPart_RECORD_PART_ROUTES || p == v1.RecordPart_RECORD_PART_OBJECTIVES {
			return true
		}
	}
	return false
}

// Adds members gossip names that this node has not met and raises the heartbeats and sequences it heard
func (m *Manager) mergeMembers(list []*v1.Member) {
	var created []*v1.Node
	now := time.Now()
	m.mu.Lock()
	if m.mesh == nil {
		m.mu.Unlock()
		return
	}
	for _, mem := range list {
		if mem.GetId() == "" || mem.GetId() == m.identity.ID {
			continue
		}
		mb, known := m.members[mem.GetId()]
		if !known {
			// A member is learned from gossip only under a certificate serial that is not revoked
			if mem.GetSerial() == "" || m.revoked[mem.GetSerial()] != nil {
				continue
			}
			rec := &v1.Node{Id: mem.GetId(), Name: mem.GetName(), Address: mem.GetAddress(), Addresses: mem.GetAddresses(), State: v1.NodeState_NODE_STATE_READY, SeenAt: timestamppb.New(now)}
			mb = &member{rec: rec, sketch: true, seenAt: now, heartbeat: mem.GetHeartbeat(), advertised: mem.GetSequence(), serial: mem.GetSerial()}
			m.members[mem.GetId()] = mb
			created = append(created, proto.Clone(rec).(*v1.Node))
			continue
		}
		if mb.serial == "" && m.revoked[mem.GetSerial()] == nil {
			mb.serial = mem.GetSerial()
		}
		if mem.GetHeartbeat() > mb.heartbeat {
			mb.heartbeat, mb.seenAt = mem.GetHeartbeat(), now
		}
		if mem.GetSequence() > mb.advertised {
			mb.advertised = mem.GetSequence()
		}
		if mb.sketch && mem.GetAddress() != "" && mb.rec.GetAddress() != mem.GetAddress() {
			mb.rec.Address, mb.rec.Addresses, mb.rec.Name = mem.GetAddress(), mem.GetAddresses(), mem.GetName()
		}
	}
	m.mu.Unlock()
	for _, rec := range created {
		m.persist(rec.GetId())
		m.Events.Publish(v1.EventKind_EVENT_KIND_NODE, v1.EventAction_EVENT_ACTION_CREATED, rec.GetId(), rec)
		m.Log.Info("mesh member learned from gossip", "node", rec.GetId(), "name", rec.GetName(), "address", rec.GetAddress())
		m.markJoined(rec.GetId())
	}
	if len(created) > 0 {
		m.publishStatus()
	}
}

// This node and every member as gossip carries them, with heartbeats and the serials that keep revoked members out
func (m *Manager) memberList() []*v1.Member {
	name := m.name()
	m.mu.Lock()
	defer m.mu.Unlock()
	self := &v1.Member{Id: m.identity.ID, Name: name, Address: m.advertiseLocked(), Sequence: m.seq, Heartbeat: m.heartbeat}
	if m.creds != nil {
		self.Serial = serialOf(m.creds.leaf)
	}
	out := []*v1.Member{self}
	for _, mb := range m.members {
		r := mb.rec
		out = append(out, &v1.Member{Id: r.GetId(), Name: r.GetName(), Addresses: r.GetAddresses(), Address: r.GetAddress(), Sequence: max(mb.held(), mb.advertised), Heartbeat: mb.heartbeat, Serial: mb.serial})
	}
	return out
}

// The serial of the certificate a record carries, empty when it carries none
func recordSerial(rec *v1.Node) string {
	if len(rec.GetCertificate()) == 0 {
		return ""
	}
	cert, err := x509.ParseCertificate(rec.GetCertificate())
	if err != nil {
		return ""
	}
	return serialOf(cert)
}

// The mesh state gossip carries, issuer and revocations under the current authority, with its digest
func (m *Manager) meshState() (*v1.Mesh, []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.mesh == nil || m.creds == nil {
		return nil, nil
	}
	state := &v1.Mesh{Id: m.mesh.ID, Issuer: m.mesh.Issuer, IssuerEpoch: m.mesh.IssuerEpoch, CaFingerprint: m.creds.caFingerprint(), Revocations: m.revocationsLocked()}
	h := sha256.New()
	fmt.Fprintf(h, "%s|%s|%d", state.GetCaFingerprint(), state.GetIssuer(), state.GetIssuerEpoch())
	for _, r := range state.GetRevocations() {
		fmt.Fprintf(h, "|%s", r.GetSerial())
	}
	return state, h.Sum(nil)
}

// Revocations held, by serial
func (m *Manager) revocationsLocked() []*v1.Revocation {
	out := make([]*v1.Revocation, 0, len(m.revoked))
	for _, r := range m.revoked {
		out = append(out, proto.Clone(r).(*v1.Revocation))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].GetSerial() < out[j].GetSerial() })
	return out
}

// Whether a serial is revoked
func (m *Manager) isRevoked(serial string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.revoked[serial] != nil
}

// Takes another member's mesh state under the same authority, the higher issuer epoch winning and revocations joined
func (m *Manager) mergeMeshState(state *v1.Mesh) {
	if state == nil {
		return
	}
	m.mu.Lock()
	if m.mesh == nil || m.creds == nil || state.GetId() != m.mesh.ID || state.GetCaFingerprint() != m.creds.caFingerprint() {
		m.mu.Unlock()
		return
	}
	issuerMoved := state.GetIssuerEpoch() > m.mesh.IssuerEpoch
	m.mu.Unlock()
	if issuerMoved {
		m.followIssuer(state.GetIssuer(), state.GetIssuerEpoch())
	}
	for _, r := range state.GetRevocations() {
		if _, err := m.revoke(r); err != nil {
			m.Log.Error("revocation from gossip not held, the next exchange carries it again", "serial", r.GetSerial(), "err", err)
		}
	}
}

// Records a revocation and drops the member it removes, a renewal leaving its node a member, true when new and an error when it could not be written
func (m *Manager) revoke(r *v1.Revocation) (bool, error) {
	if r.GetSerial() == "" || time.Now().After(r.GetNotAfter().AsTime()) {
		return false, nil
	}
	m.rows.Lock()
	m.mu.Lock()
	if m.mesh == nil || m.revoked[r.GetSerial()] != nil {
		m.mu.Unlock()
		m.rows.Unlock()
		return false, nil
	}
	m.mu.Unlock()
	if err := m.DB.PutRevocation(context.Background(), r); err != nil {
		m.rows.Unlock()
		return false, fmt.Errorf("revocation of %s not written: %w", r.GetSerial(), err)
	}
	m.mu.Lock()
	m.revoked[r.GetSerial()] = proto.Clone(r).(*v1.Revocation)
	var drop string
	for id, mb := range m.members {
		if mb.serial == r.GetSerial() && !r.GetRenewed() {
			drop = id
		}
	}
	m.mu.Unlock()
	m.rows.Unlock()
	m.Log.Info("mesh certificate revoked", "serial", r.GetSerial(), "node", r.GetNodeId())
	if drop != "" {
		m.drop(drop)
	}
	m.wakeLoop()
	m.publishStatus()
	return true, nil
}

// Revokes the certificate a member holds, dropping it here and on every member the revocation reaches
func (m *Manager) revokeMember(id string) error {
	m.mu.Lock()
	mb, ok := m.members[id]
	var cert []byte
	var serial string
	if ok {
		cert, serial = mb.rec.GetCertificate(), mb.serial
	}
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("%w %q", ErrUnknownNode, id)
	}
	r := &v1.Revocation{Serial: serial, NodeId: id, NotAfter: timestamppb.New(time.Now().Add(CertificateLifetime)), RevokedAt: timestamppb.Now()}
	if parsed, err := x509.ParseCertificate(cert); err == nil && serialOf(parsed) == serial {
		r = revocationOf(parsed, id)
	}
	if serial == "" {
		m.drop(id)
		return nil
	}
	held, err := m.revoke(r)
	if err != nil {
		return err
	}
	if !held {
		m.drop(id)
	}
	return nil
}

// The revocation of one certificate whose node is removed
func revocationOf(cert *x509.Certificate, nodeID string) *v1.Revocation {
	return &v1.Revocation{Serial: serialOf(cert), NodeId: nodeID, NotAfter: timestamppb.New(cert.NotAfter), RevokedAt: timestamppb.Now()}
}

// The revocation of a certificate a renewal replaced, its node staying a member
func renewalOf(cert *x509.Certificate, nodeID string) *v1.Revocation {
	r := revocationOf(cert, nodeID)
	r.Renewed = true
	return r
}

// Drops a member with its links, session, imports and formation copies
func (m *Manager) drop(id string) {
	m.rows.Lock()
	m.mu.Lock()
	mb, ok := m.members[id]
	delete(m.members, id)
	if s := m.sessions[id]; s != nil {
		delete(m.accept, s.accept)
		delete(m.sessions, id)
	}
	delete(m.links, id)
	m.mu.Unlock()
	ctx := context.Background()
	if err := m.DB.DeleteMember(ctx, id); err != nil {
		m.Log.Warn("member delete failed", "node", id, "err", err)
	}
	if err := m.DB.DeleteSession(ctx, id); err != nil {
		m.Log.Warn("session delete failed", "node", id, "err", err)
	}
	if err := m.DB.DeleteLinks(ctx, id); err != nil {
		m.Log.Warn("links delete failed", "node", id, "err", err)
	}
	m.rows.Unlock()
	if mb.GetAddress() != "" {
		m.dropTransports(mb.GetAddress())
	}
	m.dropImports(ctx, id)
	if ok {
		m.Events.Publish(v1.EventKind_EVENT_KIND_NODE, v1.EventAction_EVENT_ACTION_DELETED, id, mb.rec)
		if m.Formations != nil {
			m.Formations.Forget(id)
		}
		m.bump()
		m.publishStatus()
	}
}

// Writes a member's record as it stands now, nothing once it is no member
func (m *Manager) persist(id string) {
	m.rows.Lock()
	defer m.rows.Unlock()
	m.mu.Lock()
	var rec *v1.Node
	if mb, ok := m.members[id]; ok {
		rec = proto.Clone(mb.rec).(*v1.Node)
	}
	m.mu.Unlock()
	if rec == nil {
		return
	}
	if err := m.DB.PutMember(context.Background(), rec); err != nil {
		m.Log.Warn("member record write failed", "node", id, "err", err)
	}
}

// Mints or extends the bearer a member sends this node, returning it with its expiry
func (m *Manager) renewSession(peer string) (string, time.Time, error) {
	m.mu.Lock()
	s := m.sessions[peer]
	if s == nil {
		s = &session{peer: peer}
		m.sessions[peer] = s
	}
	if s.accept == "" || time.Now().After(s.expires) {
		token, err := randomToken()
		if err != nil {
			m.mu.Unlock()
			return "", time.Time{}, err
		}
		delete(m.accept, s.accept)
		s.accept, s.granted = token, time.Now()
		m.accept[token] = peer
	}
	s.expires = time.Now().Add(SessionTTL)
	token, expires := s.accept, s.expires
	m.mu.Unlock()
	if err := m.persistSession(peer); err != nil {
		m.Log.Warn("session write failed", "peer", peer, "err", err)
	}
	return token, expires, nil
}

// Writes a peer's session as it stands now, or drops its row once the session is gone
func (m *Manager) persistSession(peer string) error {
	m.rows.Lock()
	defer m.rows.Unlock()
	m.mu.Lock()
	var row *db.SessionRow
	if s := m.sessions[peer]; s != nil {
		row = s.row()
	}
	m.mu.Unlock()
	if row == nil {
		return m.DB.DeleteSession(context.Background(), peer)
	}
	return m.DB.PutSession(context.Background(), row)
}

// Takes a member's departure, revoking the certificate it left with
func (m *Manager) bye(c caller) error {
	held, err := m.revoke(revocationOf(c.cert, c.id))
	if err != nil {
		return err
	}
	if !held {
		m.drop(c.id)
	}
	m.Log.Info("mesh member left", "node", c.id)
	return nil
}

// The session as stored, expiring with the later of the two tokens
func (s *session) row() *db.SessionRow {
	expires := s.expires
	if s.sendExpires.After(expires) {
		expires = s.sendExpires
	}
	return &db.SessionRow{PeerID: s.peer, Accept: s.accept, Send: s.send, Expires: expires, Granted: s.granted}
}
