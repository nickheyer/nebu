package mesh

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"connectrpc.com/connect"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Syncs with every member every interval and whenever this node's record changes
func (m *Manager) syncLoop(ctx context.Context) {
	ticker := time.NewTicker(SyncInterval)
	defer ticker.Stop()
	m.syncAll(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-m.wake:
		}
		if !m.Joined() {
			continue
		}
		m.syncAll(ctx)
	}
}

// Syncs with every member at once, gone members on a slower cadence
func (m *Manager) syncAll(ctx context.Context) {
	m.mu.Lock()
	m.dirty = false
	var due []string
	now := time.Now()
	for id, mb := range m.members {
		if mb.rec.GetState() == v1.NodeState_NODE_STATE_GONE && now.Sub(mb.lastTried) < goneRetry {
			continue
		}
		mb.lastTried = now
		due = append(due, id)
	}
	m.mu.Unlock()
	if len(due) == 0 {
		return
	}
	rec := m.Record()
	var formations []*v1.Formation
	if m.Formations != nil {
		formations = m.Formations.Conducted()
	}
	m.mu.Lock()
	members := m.memberListLocked()
	admissions := m.admissionsForSyncLocked()
	m.mu.Unlock()
	var wg sync.WaitGroup
	for _, id := range due {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			m.syncOne(ctx, id, rec, members, formations, admissions)
		}(id)
	}
	wg.Wait()
}

// One sync with one member: our record, member list, formations, and admissions for theirs
func (m *Manager) syncOne(ctx context.Context, id string, rec *v1.Node, members []*v1.Member, formations []*v1.Formation, admissions []*v1.Admission) {
	cl, err := m.Client(ctx, id)
	if err != nil {
		m.missed(id, err)
		return
	}
	cctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	resp, err := cl.Mesh.Sync(cctx, connect.NewRequest(&v1.SyncRequest{Node: rec, Members: members, Formations: formations, Admissions: admissions}))
	if err != nil {
		if connect.CodeOf(err) == connect.CodeUnauthenticated || connect.CodeOf(err) == connect.CodePermissionDenied {
			m.dropSend(id)
		}
		m.missed(id, err)
		return
	}
	if resp.Msg.GetExpiresAt() != nil {
		m.mu.Lock()
		if s := m.sessions[id]; s != nil {
			s.sendExpires = resp.Msg.GetExpiresAt().AsTime()
		}
		m.mu.Unlock()
	}
	if node := resp.Msg.GetNode(); node != nil && node.GetId() == id {
		m.absorb(node, resp.Msg.GetMembers(), true)
	} else {
		m.absorb(nil, resp.Msg.GetMembers(), false)
		m.reached(id)
	}
	// An answer from a member forgotten meanwhile changes nothing more
	if _, ok := m.Member(id); !ok {
		return
	}
	if m.Formations != nil {
		m.Formations.Merge(id, resp.Msg.GetFormations())
	}
	m.mergeAdmissions(resp.Msg.GetAdmissions())
}

// Counts a missed sync and moves the member through unreachable to gone
func (m *Manager) missed(id string, err error) {
	m.mu.Lock()
	mb, ok := m.members[id]
	if !ok {
		m.mu.Unlock()
		return
	}
	mb.missed++
	before := mb.rec.GetState()
	seen := mb.lastTried
	if mb.rec.GetSeenAt() != nil {
		seen = mb.rec.GetSeenAt().AsTime()
	}
	switch {
	case time.Since(seen) > GoneAfter:
		mb.rec.State = v1.NodeState_NODE_STATE_GONE
	case mb.missed >= UnreachableAfter:
		mb.rec.State = v1.NodeState_NODE_STATE_UNREACHABLE
	}
	after := mb.rec.GetState()
	count := mb.missed
	rec := proto.Clone(mb.rec).(*v1.Node)
	m.mu.Unlock()
	if after == before {
		m.Log.Debug("mesh sync missed", "node", id, "missed", count, "err", err)
		return
	}
	m.Log.Warn("mesh member "+stateWord(after), "node", id, "name", rec.GetName(), "err", err)
	m.persist(rec)
	m.Events.Publish(v1.EventKind_EVENT_KIND_NODE, v1.EventAction_EVENT_ACTION_UPDATED, id, rec)
	if m.Formations != nil {
		m.Formations.MemberState(id, after)
	}
}

// Marks a member reached without a new record
func (m *Manager) reached(id string) {
	m.mu.Lock()
	mb, ok := m.members[id]
	if !ok {
		m.mu.Unlock()
		return
	}
	before := mb.rec.GetState()
	mb.missed = 0
	mb.rec.State = v1.NodeState_NODE_STATE_READY
	mb.rec.SeenAt = timestamppb.Now()
	rec := proto.Clone(mb.rec).(*v1.Node)
	m.mu.Unlock()
	if before != v1.NodeState_NODE_STATE_READY {
		m.persist(rec)
		m.Events.Publish(v1.EventKind_EVENT_KIND_NODE, v1.EventAction_EVENT_ACTION_UPDATED, id, rec)
		if m.Formations != nil {
			m.Formations.MemberState(id, v1.NodeState_NODE_STATE_READY)
		}
	}
}

func stateWord(s v1.NodeState) string {
	switch s {
	case v1.NodeState_NODE_STATE_READY:
		return "ready"
	case v1.NodeState_NODE_STATE_UNREACHABLE:
		return "unreachable"
	case v1.NodeState_NODE_STATE_GONE:
		return "gone"
	}
	return "unknown"
}

// Takes a member's record and the members it knows. A record that came from the member itself
// marks it ready; one relayed by gossip only fills what was missing.
func (m *Manager) absorb(rec *v1.Node, members []*v1.Member, direct bool) {
	if rec != nil && rec.GetId() != "" && rec.GetId() != m.identity.ID {
		m.mergeRecord(rec, direct)
	}
	m.mergeMembers(members)
}

// Keeps the higher sequence of a member's record, publishing the change
func (m *Manager) mergeRecord(rec *v1.Node, direct bool) {
	rec = proto.Clone(rec).(*v1.Node)
	rec.Self = false
	m.mu.Lock()
	mb, known := m.members[rec.GetId()]

	// NOTE: Some race conditions occur here because peers re-ping or retry to join after host intended
	//		 to forget
	if m.mesh == nil || (!known && m.sessions[rec.GetId()] == nil) {
		m.mu.Unlock()
		return
	}
	action := v1.EventAction_EVENT_ACTION_UPDATED
	stateBefore := v1.NodeState_NODE_STATE_UNSPECIFIED
	if known {
		stateBefore = mb.rec.GetState()
		if !mb.sketch && rec.GetSequence() < mb.rec.GetSequence() {
			// An older copy: keep ours, but a direct contact still proves the member is up.
			if direct {
				mb.missed = 0
				mb.rec.State = v1.NodeState_NODE_STATE_READY
				mb.rec.SeenAt = timestamppb.Now()
			}
			out := proto.Clone(mb.rec).(*v1.Node)
			m.mu.Unlock()
			if direct && stateBefore != v1.NodeState_NODE_STATE_READY {
				m.persist(out)
				m.Events.Publish(v1.EventKind_EVENT_KIND_NODE, action, out.GetId(), out)
				if m.Formations != nil {
					m.Formations.MemberState(out.GetId(), v1.NodeState_NODE_STATE_READY)
				}
			}
			return
		}
		if direct {
			rec.State = v1.NodeState_NODE_STATE_READY
			rec.SeenAt = timestamppb.Now()
			mb.missed = 0
			delete(m.forgotten, rec.GetId())
		} else {
			rec.State = mb.rec.GetState()
			rec.SeenAt = mb.rec.GetSeenAt()
		}
		mb.rec, mb.sketch = rec, rec.GetProfile() == nil && !direct
	} else {
		action = v1.EventAction_EVENT_ACTION_CREATED
		if direct {
			rec.State = v1.NodeState_NODE_STATE_READY
			rec.SeenAt = timestamppb.Now()
			delete(m.forgotten, rec.GetId())
		}
		m.members[rec.GetId()] = &member{rec: rec, sketch: rec.GetProfile() == nil && !direct}
	}
	m.reclassifyLocked(rec.GetId())
	out := proto.Clone(rec).(*v1.Node)
	m.mu.Unlock()
	m.persist(out)
	m.Events.Publish(v1.EventKind_EVENT_KIND_NODE, action, out.GetId(), out)
	if m.Formations != nil && out.GetState() != stateBefore {
		m.Formations.MemberState(out.GetId(), out.GetState())
	}
	if action == v1.EventAction_EVENT_ACTION_CREATED {
		m.markJoined(out.GetId())
		m.publishStatus()
	}
}

// Adds members gossip names that this node has not met, so one address reaches the whole mesh
func (m *Manager) mergeMembers(list []*v1.Member) {
	var created []*v1.Node
	m.mu.Lock()
	if m.mesh == nil {
		m.mu.Unlock()
		return
	}
	for _, mem := range list {
		if mem.GetId() == "" || mem.GetId() == m.identity.ID || m.forgottenLocked(mem.GetId()) {
			continue
		}
		mb, known := m.members[mem.GetId()]
		if known {
			if mb.sketch && mem.GetAddress() != "" && mb.rec.GetAddress() != mem.GetAddress() {
				mb.rec.Address, mb.rec.Addresses, mb.rec.Tls, mb.rec.TlsFingerprint = mem.GetAddress(), mem.GetAddresses(), mem.GetTls(), mem.GetTlsFingerprint()
			}
			continue
		}
		rec := &v1.Node{Id: mem.GetId(), Name: mem.GetName(), Address: mem.GetAddress(), Addresses: mem.GetAddresses(), Tls: mem.GetTls(), TlsFingerprint: mem.GetTlsFingerprint()}
		m.members[mem.GetId()] = &member{rec: rec, sketch: true}
		created = append(created, proto.Clone(rec).(*v1.Node))
	}
	m.mu.Unlock()
	for _, rec := range created {
		m.persist(rec)
		m.Events.Publish(v1.EventKind_EVENT_KIND_NODE, v1.EventAction_EVENT_ACTION_CREATED, rec.GetId(), rec)
		m.Log.Info("mesh member learned from gossip", "node", rec.GetId(), "name", rec.GetName(), "address", rec.GetAddress())
		m.markJoined(rec.GetId())
	}
	if len(created) > 0 {
		m.publishStatus()
		select {
		case m.wake <- struct{}{}:
		default:
		}
	}
}

// Whether a member forgotten lately is still refused from gossip
func (m *Manager) forgottenLocked(id string) bool {
	at, ok := m.forgotten[id]
	if ok && time.Since(at) < GoneAfter {
		return true
	}
	delete(m.forgotten, id)
	return false
}

// Writes a member's record
func (m *Manager) persist(rec *v1.Node) {
	if err := m.DB.PutMember(context.Background(), rec); err != nil {
		m.Log.Warn("member record write failed", "node", rec.GetId(), "err", err)
	}
}

// This node and every member as gossip carries them
func (m *Manager) memberListLocked() []*v1.Member {
	fingerprint, hasTLS := m.servedCertLocked()
	out := []*v1.Member{{Id: m.identity.ID, Name: m.name(), Address: m.advertiseLocked(), TlsFingerprint: fingerprint, Tls: hasTLS, State: v1.NodeState_NODE_STATE_READY, SeenAt: timestamppb.Now(), Sequence: m.seq}}
	for _, mb := range m.members {
		r := mb.rec
		out = append(out, &v1.Member{Id: r.GetId(), Name: r.GetName(), Addresses: r.GetAddresses(), Address: r.GetAddress(), TlsFingerprint: r.GetTlsFingerprint(), State: r.GetState(), SeenAt: r.GetSeenAt(), Sequence: r.GetSequence(), Tls: r.GetTls()})
	}
	return out
}

// Answers a member's sync: takes its record, members, and formations, renews its session,
// and returns ours
func (m *Manager) Sync(ctx context.Context, peer string, req *v1.SyncRequest) (*v1.SyncResponse, error) {
	if !m.Joined() {
		return nil, connect.NewError(connect.CodeFailedPrecondition, ErrNoMesh)
	}
	if node := req.GetNode(); node != nil && node.GetId() == peer {
		m.absorb(node, req.GetMembers(), true)
	} else {
		m.absorb(nil, req.GetMembers(), false)
		m.reached(peer)
	}
	// A sync sent before its sender was forgotten here changes nothing more
	if _, ok := m.Member(peer); !ok {
		return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("%w: %s is not a member here, handshake again", ErrMesh, peer))
	}
	if m.Formations != nil {
		m.Formations.Merge(peer, req.GetFormations())
	}
	m.mergeAdmissions(req.GetAdmissions())
	expires := m.renew(peer)
	var formations []*v1.Formation
	if m.Formations != nil {
		formations = m.Formations.Conducted()
	}
	m.mu.Lock()
	members := m.memberListLocked()
	admissions := m.admissionsForSyncLocked()
	m.mu.Unlock()
	return &v1.SyncResponse{Node: m.Record(), Members: members, Formations: formations, ExpiresAt: timestamppb.New(expires), Admissions: admissions}, nil
}

// Takes a member's departure
func (m *Manager) Bye(peer string) {
	m.forget(peer)
	m.Log.Info("mesh member left", "node", peer)
}

// Drops a member with its links and session
func (m *Manager) forget(id string) {
	m.mu.Lock()
	m.forgotten[id] = time.Now()
	mb, ok := m.members[id]
	delete(m.members, id)
	if s := m.sessions[id]; s != nil {
		delete(m.accept, s.accept)
		delete(m.sessions, id)
	}
	delete(m.links, id)
	m.mu.Unlock()
	ctx := context.Background()
	m.DB.DeleteMember(ctx, id)
	m.DB.DeleteSession(ctx, id)
	m.DB.DeleteLinks(ctx, id)
	if ok {
		m.Events.Publish(v1.EventKind_EVENT_KIND_NODE, v1.EventAction_EVENT_ACTION_DELETED, id, mb.rec)
		if m.Formations != nil {
			m.Formations.Forget(id)
		}
		m.bump()
		m.publishStatus()
	}
}

// Takes a new secret during rotation
func (m *Manager) Rekey(secret []byte) error {
	if len(secret) != 32 {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("a mesh secret holds 32 bytes"))
	}
	m.mu.Lock()
	if m.mesh == nil {
		m.mu.Unlock()
		return connect.NewError(connect.CodeFailedPrecondition, ErrNoMesh)
	}
	m.mesh.Secret = secret
	row := *m.mesh
	m.mu.Unlock()
	return m.DB.PutMesh(context.Background(), &row)
}
