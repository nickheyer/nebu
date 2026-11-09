package mesh

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"runtime"
	"sort"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/nickheyer/nebu/internal/db"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/proto/nebu/v1/nebuv1connect"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	// A pending admission waits this long for a decision
	AdmissionTTL = 10 * time.Minute
	// A settled admission stays on the page this long
	settledTTL = 10 * time.Minute
	// Admissions held at once, so a flood of knocks cannot fill memory
	maxAdmissions = 128
	// The grant delivery waits this long, the candidate's join inside it
	welcomeTimeout = 30 * time.Second
	// Expiry runs this often
	admissionSweep = 30 * time.Second
)

// Whether an admission is decided, one way or the other
func settled(s v1.AdmissionState) bool {
	switch s {
	case v1.AdmissionState_ADMISSION_STATE_PENDING, v1.AdmissionState_ADMISSION_STATE_ADMITTED:
		return false
	}
	return true
}

// This node as a beacon or an admission names it
func (m *Manager) selfNearbyLocked() *v1.NearbyNode {
	n := &v1.NearbyNode{
		Id:             m.identity.ID,
		Name:           m.name(),
		Address:        m.advertiseLocked(),
		TlsFingerprint: m.identityFingerprint(),
		Version:        m.Version,
		Os:             runtime.GOOS,
		Arch:           runtime.GOARCH,
		HeardAt:        timestamppb.Now(),
	}
	if m.mesh != nil {
		n.MeshHash, n.MeshName, n.MeshMembers = meshHex(m.mesh.ID), m.mesh.Name, uint32(len(m.members)+1)
	}
	return n
}

// The fingerprint of the identity certificate admissions run on
func (m *Manager) identityFingerprint() string {
	return fingerprintOf(m.ident.Certificate[0])
}

// A member's admission for a node outside, nil when none
func (m *Manager) admissionByNodeLocked(nodeID string) *v1.Admission {
	if nodeID == "" {
		return nil
	}
	for _, a := range m.admissions {
		if a.GetSide() == v1.AdmissionSide_ADMISSION_SIDE_MEMBER && a.GetNode().GetId() == nodeID {
			return proto.Clone(a).(*v1.Admission)
		}
	}
	return nil
}

// A candidate's admission for a mesh, nil when none
func (m *Manager) admissionByMeshLocked(hash string) *v1.Admission {
	if hash == "" {
		return nil
	}
	for _, a := range m.admissions {
		if a.GetSide() == v1.AdmissionSide_ADMISSION_SIDE_CANDIDATE && a.GetMeshHash() == hash {
			return proto.Clone(a).(*v1.Admission)
		}
	}
	return nil
}

// A member's admission for a node reached by address whose identity is not known yet, nil when none
func (m *Manager) admissionByAddressLocked(address string) *v1.Admission {
	if address == "" {
		return nil
	}
	for _, a := range m.admissions {
		if a.GetSide() == v1.AdmissionSide_ADMISSION_SIDE_MEMBER && a.GetNode().GetId() == "" && a.GetNode().GetAddress() == address {
			return proto.Clone(a).(*v1.Admission)
		}
	}
	return nil
}

// A candidate's admission made by address before the mesh across was known, nil when none
func (m *Manager) admissionByMemberLocked(member *v1.NearbyNode) *v1.Admission {
	for _, a := range m.admissions {
		if a.GetSide() != v1.AdmissionSide_ADMISSION_SIDE_CANDIDATE || a.GetMeshHash() != "" {
			continue
		}
		if a.GetNode().GetId() == member.GetId() || a.GetNode().GetAddress() == member.GetAddress() {
			return proto.Clone(a).(*v1.Admission)
		}
	}
	return nil
}

// A copy of the admission named by id or by the id or name of the node across
func (m *Manager) admissionLocked(ref string) *v1.Admission {
	if a, ok := m.admissions[ref]; ok {
		return proto.Clone(a).(*v1.Admission)
	}
	for _, a := range m.admissions {
		if a.GetNode().GetId() == ref || a.GetNode().GetName() == ref || a.GetMeshName() == ref || a.GetMeshHash() == ref {
			return proto.Clone(a).(*v1.Admission)
		}
	}
	return nil
}

// Admissions the page shows, pending first and then the latest change first
func (m *Manager) admissionListLocked() []*v1.Admission {
	out := make([]*v1.Admission, 0, len(m.admissions))
	for _, a := range m.admissions {
		if a.GetState() == v1.AdmissionState_ADMISSION_STATE_DISMISSED {
			continue
		}
		out = append(out, proto.Clone(a).(*v1.Admission))
	}
	sort.Slice(out, func(i, j int) bool {
		pi, pj := out[i].GetState() == v1.AdmissionState_ADMISSION_STATE_PENDING, out[j].GetState() == v1.AdmissionState_ADMISSION_STATE_PENDING
		if pi != pj {
			return pi
		}
		ti, tj := out[i].GetUpdatedAt().AsTime(), out[j].GetUpdatedAt().AsTime()
		if !ti.Equal(tj) {
			return ti.After(tj)
		}
		return out[i].GetId() < out[j].GetId()
	})
	return out
}

// Every admission this node holds
func (m *Manager) Admissions() []*v1.Admission {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.admissionListLocked()
}

// Stores an admission, tells the page, and for a member's copy the other members on the next sync
func (m *Manager) saveAdmission(a *v1.Admission) *v1.Admission {
	fresh := a.GetUpdatedAt() == nil
	a.UpdatedAt = timestamppb.Now()
	if a.GetCreatedAt() == nil {
		a.CreatedAt = a.UpdatedAt
	}
	m.rows.Lock()
	m.mu.Lock()
	if _, have := m.admissions[a.GetId()]; !have && !fresh {
		// Another member's copy of the record took its place under that copy's id, else a clear dropped it for good
		other := m.admissionByNodeLocked(a.GetNode().GetId())
		if a.GetSide() != v1.AdmissionSide_ADMISSION_SIDE_MEMBER || other == nil || other.GetMeshHash() != a.GetMeshHash() {
			m.mu.Unlock()
			m.rows.Unlock()
			return proto.Clone(a).(*v1.Admission)
		}
		a.Id, a.CreatedAt = other.GetId(), other.GetCreatedAt()
		a.Invited, a.Asked, a.Reached = a.GetInvited() || other.GetInvited(), a.GetAsked() || other.GetAsked(), a.GetReached() || other.GetReached()
	}
	m.admissions[a.GetId()] = proto.Clone(a).(*v1.Admission)
	member := a.GetSide() == v1.AdmissionSide_ADMISSION_SIDE_MEMBER
	m.mu.Unlock()
	if err := m.DB.PutAdmission(context.Background(), a); err != nil {
		m.Log.Warn("admission write failed", "id", a.GetId(), "err", err)
	}
	m.rows.Unlock()
	m.publishStatus()
	if member {
		m.bump()
	}
	return proto.Clone(a).(*v1.Admission)
}

// Drops an admission
func (m *Manager) dropAdmission(id string) {
	m.rows.Lock()
	m.mu.Lock()
	delete(m.admissions, id)
	delete(m.grants, id)
	m.mu.Unlock()
	if err := m.DB.DeleteAdmission(context.Background(), id); err != nil {
		m.Log.Warn("admission delete failed", "id", id, "err", err)
	}
	m.rows.Unlock()
	m.publishStatus()
}

// A fresh admission on one side
func newAdmission(side v1.AdmissionSide) *v1.Admission {
	return &v1.Admission{Id: db.NewID(), Side: side, State: v1.AdmissionState_ADMISSION_STATE_PENDING, CreatedAt: timestamppb.Now(), ExpiresAt: timestamppb.New(time.Now().Add(AdmissionTTL))}
}

// Whether a node has been heard by beacon from the address on record for it
func (m *Manager) reachedLocked(nodeID, address string) bool {
	h, ok := m.nearby[nodeID]
	return ok && address != "" && h.rec.GetAddress() == address
}

// Marks a member's admission reached when the beacon comes from the address on record, true when that changed
func (m *Manager) noteReachedLocked(nodeID, address string) bool {
	a := m.admissionByNodeLocked(nodeID)
	if a == nil || a.GetReached() || address == "" || a.GetNode().GetAddress() != address {
		return false
	}
	a.Reached = true
	m.admissions[a.GetId()] = a
	return true
}

// The identity a node at an address answers admissions with, learned on first contact
func (m *Manager) probeIdentity(ctx context.Context, address string) (identitySeen, error) {
	var seen identitySeen
	d := tls.Dialer{NetDialer: &net.Dialer{Timeout: dialTimeout}, Config: m.admissionDialConfig("", "", &seen)}
	conn, err := d.DialContext(ctx, "tcp", address)
	if err != nil {
		return identitySeen{}, err
	}
	conn.Close()
	return seen, nil
}

// The admission client to a node and its identity fingerprint, the identity learned on first contact when not known
func (m *Manager) reach(ctx context.Context, address, nodeID, fingerprint string) (nebuv1connect.MeshPeerServiceClient, string, error) {
	if address == "" {
		return nil, "", fmt.Errorf("%w: the node has no address to reach it at, it listens on loopback alone", ErrMesh)
	}
	if fingerprint == "" {
		seen, err := m.probeIdentity(ctx, address)
		if err != nil {
			return nil, "", err
		}
		if nodeID != "" && seen.id != nodeID {
			return nil, "", fmt.Errorf("%s answered as %s, not %s", address, seen.id, nodeID)
		}
		nodeID, fingerprint = seen.id, seen.fingerprint
	}
	return m.admissionClient(address, nodeID, fingerprint), fingerprint, nil
}

// A node to reach for admission, heard by beacon by id or name or named by address
func (m *Manager) targetLocked(nodeID, address string) (*v1.NearbyNode, error) {
	if nodeID != "" {
		if n := m.nearbyLocked(nodeID); n != nil {
			return n, nil
		}
		for _, h := range m.nearby {
			if h.rec.GetName() == nodeID {
				return proto.Clone(h.rec).(*v1.NearbyNode), nil
			}
		}
		return nil, fmt.Errorf("%w: no node %q has been heard on the network", ErrUnknownNode, nodeID)
	}
	if address = strings.TrimSpace(address); address != "" {
		if _, _, err := net.SplitHostPort(address); err != nil {
			address = net.JoinHostPort(strings.Trim(address, "[]"), defaultPort())
		}
		return &v1.NearbyNode{Address: address}, nil
	}
	return nil, fmt.Errorf("%w: name a node heard on the network or an address", ErrMesh)
}

// The port of the default mesh listener
func defaultPort() string {
	_, port, _ := net.SplitHostPort(DefaultListen)
	return port
}

// Invites a node heard by beacon or named by address, handing it a grant its page accepts with
func (m *Manager) Invite(ctx context.Context, nodeID, address string) (*v1.Admission, error) {
	m.mu.Lock()
	if m.mesh == nil {
		m.mu.Unlock()
		return nil, ErrNoMesh
	}
	target, err := m.targetLocked(nodeID, address)
	if err != nil {
		m.mu.Unlock()
		return nil, err
	}
	if _, member := m.members[target.GetId()]; member && target.GetId() != "" {
		m.mu.Unlock()
		return nil, fmt.Errorf("%w: %s is a member already", ErrMesh, target.GetName())
	}
	own := meshHex(m.mesh.ID)
	if target.GetMeshHash() != "" && target.GetMeshHash() != own {
		m.mu.Unlock()
		return nil, fmt.Errorf("%w: %s belongs to mesh %s and must leave it first", ErrMesh, target.GetName(), target.GetMeshName())
	}
	a := m.admissionByNodeLocked(target.GetId())
	if a == nil {
		a = m.admissionByAddressLocked(target.GetAddress())
	}
	req := &v1.OfferRequest{Member: m.selfNearbyLocked(), MeshHash: own, MeshName: m.mesh.Name, MeshMembers: uint32(len(m.members) + 1)}
	by := req.GetMember().GetName()
	m.mu.Unlock()
	if a == nil {
		a = newAdmission(v1.AdmissionSide_ADMISSION_SIDE_MEMBER)
		a.MeshHash, a.MeshName, a.MeshMembers = own, req.GetMeshName(), req.GetMeshMembers()
	}
	a.Node, a.By = target, by
	fail := func(err error) (*v1.Admission, error) {
		a.State, a.Detail = v1.AdmissionState_ADMISSION_STATE_FAILED, fmt.Sprintf("the invitation did not reach %s: %v", describe(target), err)
		m.saveAdmission(a)
		return nil, fmt.Errorf("%w: %s", ErrMesh, a.GetDetail())
	}
	grant, _, err := m.Token(ctx, AdmissionTTL)
	if err != nil {
		return fail(err)
	}
	req.Grant = grant
	cctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	cl, fingerprint, err := m.reach(cctx, target.GetAddress(), target.GetId(), target.GetTlsFingerprint())
	if err != nil {
		return fail(err)
	}
	resp, err := cl.Offer(cctx, connect.NewRequest(req))
	if err != nil {
		return fail(err)
	}
	node := resp.Msg.GetNode()
	if node.GetId() == "" {
		return fail(fmt.Errorf("it answered without an identity"))
	}
	if target.GetId() != "" && node.GetId() != target.GetId() {
		return fail(fmt.Errorf("%s answered as %s", target.GetAddress(), node.GetName()))
	}
	m.mu.Lock()
	if _, member := m.members[node.GetId()]; member || resp.Msg.GetState() == v1.AdmissionState_ADMISSION_STATE_JOINED {
		have := m.admissionByNodeLocked(node.GetId())
		m.mu.Unlock()
		if have != nil && have.GetState() != v1.AdmissionState_ADMISSION_STATE_JOINED {
			have.State, have.Detail = v1.AdmissionState_ADMISSION_STATE_JOINED, ""
			m.saveAdmission(have)
		}
		return nil, fmt.Errorf("%w: %s is a member already", ErrMesh, node.GetName())
	}
	// A record for the node may exist under another id when it was reached by address
	if have := m.admissionByNodeLocked(node.GetId()); have != nil && have.GetId() != a.GetId() {
		delete(m.admissions, a.GetId())
		a = have
	}
	node.TlsFingerprint = fingerprint
	if node.GetAddress() == "" {
		node.Address = target.GetAddress()
	}
	node.Member = false
	m.mu.Unlock()
	a.Node, a.Invited, a.By, a.Detail, a.Reached = node, true, by, "", true
	a.ExpiresAt = timestamppb.New(time.Now().Add(AdmissionTTL))
	switch resp.Msg.GetState() {
	case v1.AdmissionState_ADMISSION_STATE_JOINED:
		a.State = v1.AdmissionState_ADMISSION_STATE_JOINED
	default:
		a.State = v1.AdmissionState_ADMISSION_STATE_PENDING
	}
	m.Log.Info("mesh invitation sent", "node", node.GetId(), "name", node.GetName(), "address", node.GetAddress())
	return m.saveAdmission(a), nil
}

// A node as a detail line names it
func describe(n *v1.NearbyNode) string {
	if n.GetName() != "" && n.GetAddress() != "" {
		return n.GetName() + " at " + n.GetAddress()
	}
	if n.GetName() != "" {
		return n.GetName()
	}
	return n.GetAddress()
}

// Takes an invitation and its grant from a member nearby, which the page accepts by joining
func (m *Manager) offer(ctx context.Context, c caller, req *v1.OfferRequest) (*v1.OfferResponse, error) {
	member := req.GetMember()
	if member.GetId() == "" || req.GetMeshHash() == "" || req.GetGrant() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("%w: an invitation names the member, its mesh and a grant", ErrMesh))
	}
	if member.GetId() != c.id {
		return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("%w: the invitation names %s but came from %s", ErrMesh, member.GetId(), c.id))
	}
	if g, err := decodeGrant(req.GetGrant()); err != nil || g.MeshHash != req.GetMeshHash() {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("%w: the invitation's grant is not for the mesh it names", ErrMesh))
	}
	if member.GetId() == m.identity.ID {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("%w: a node cannot invite itself", ErrMesh))
	}
	m.mu.Lock()
	self := m.selfNearbyLocked()
	if m.mesh != nil {
		if meshHex(m.mesh.ID) == req.GetMeshHash() {
			m.mu.Unlock()
			return &v1.OfferResponse{State: v1.AdmissionState_ADMISSION_STATE_JOINED, Node: self}, nil
		}
		name := m.mesh.Name
		m.mu.Unlock()
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("%w: this node belongs to mesh %s already", ErrMesh, name))
	}
	a := m.admissionByMeshLocked(req.GetMeshHash())
	if a == nil {
		a = m.admissionByMemberLocked(member)
	}
	if a == nil && len(m.admissions) >= maxAdmissions {
		m.mu.Unlock()
		return nil, connect.NewError(connect.CodeResourceExhausted, fmt.Errorf("%w: too many admissions are open on this node", ErrMesh))
	}
	reached := m.reachedLocked(member.GetId(), member.GetAddress())
	m.mu.Unlock()
	if a == nil {
		a = newAdmission(v1.AdmissionSide_ADMISSION_SIDE_CANDIDATE)
	}
	member.Member, member.TlsFingerprint = false, fingerprintOf(c.cert.Raw)
	a.Node, a.MeshHash, a.MeshName, a.MeshMembers = member, req.GetMeshHash(), req.GetMeshName(), req.GetMeshMembers()
	a.Invited, a.By, a.Detail, a.Reached = true, member.GetName(), "", reached
	a.ExpiresAt = timestamppb.New(time.Now().Add(AdmissionTTL))
	if settled(a.GetState()) {
		a.State = v1.AdmissionState_ADMISSION_STATE_PENDING
	}
	out := m.saveAdmission(a)
	if err := m.holdGrant(out.GetId(), req.GetGrant()); err != nil {
		return nil, err
	}
	m.Log.Info("mesh invitation received", "mesh", req.GetMeshName(), "from", member.GetName(), "address", member.GetAddress())
	if out.GetAsked() && out.GetState() == v1.AdmissionState_ADMISSION_STATE_PENDING {
		// The admin here asked before the invitation came, so it is answered now
		if !m.spawn(func() {
			if _, err := m.Ask(m.base, out.GetMeshHash(), "", ""); err != nil {
				m.Log.Warn("mesh invitation could not be answered", "mesh", req.GetMeshName(), "err", err)
			}
		}) {
			a.State, a.Detail = v1.AdmissionState_ADMISSION_STATE_FAILED, "this node shut down before it answered the invitation"
			out = m.saveAdmission(a)
		}
	}
	return &v1.OfferResponse{State: out.GetState(), Node: self}, nil
}

// Accepts an invitation by joining with its grant, or asks a member to join and waits for its admission, the listener bound for the grant to arrive on
func (m *Manager) Ask(ctx context.Context, meshHash, nodeID, address string) (*v1.Admission, error) {
	if err := m.listen(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	if m.mesh != nil {
		name := m.mesh.Name
		m.mu.Unlock()
		return nil, fmt.Errorf("%w: this node belongs to mesh %s already and must leave it first", ErrMesh, name)
	}
	var target *v1.NearbyNode
	var err error
	switch {
	case strings.TrimSpace(address) != "", nodeID != "":
		if target, err = m.targetLocked(nodeID, address); err != nil {
			m.mu.Unlock()
			return nil, err
		}
		if nodeID != "" {
			if target.GetMeshHash() == "" {
				m.mu.Unlock()
				return nil, fmt.Errorf("%w: %s belongs to no mesh", ErrMesh, target.GetName())
			}
			if meshHash != "" && target.GetMeshHash() != meshHash {
				m.mu.Unlock()
				return nil, fmt.Errorf("%w: %s belongs to another mesh, %s", ErrMesh, target.GetName(), target.GetMeshName())
			}
		}
	case meshHash != "":
		if a := m.admissionByMeshLocked(meshHash); a != nil && a.GetNode().GetAddress() != "" {
			target = proto.Clone(a.GetNode()).(*v1.NearbyNode)
		}
		if fresh := m.freshestMemberLocked(meshHash); fresh != nil && (target == nil || !m.reachedLocked(target.GetId(), target.GetAddress())) {
			target = fresh
		}
		if target == nil {
			m.mu.Unlock()
			return nil, fmt.Errorf("%w: no member of that mesh has been heard on the network", ErrMesh)
		}
	default:
		m.mu.Unlock()
		return nil, fmt.Errorf("%w: name a mesh heard on the network, one of its members, or a member's address", ErrMesh)
	}
	a := m.admissionByMeshLocked(target.GetMeshHash())
	if a == nil {
		a = m.admissionByMemberLocked(target)
	}
	if a == nil && len(m.admissions) >= maxAdmissions {
		m.mu.Unlock()
		return nil, fmt.Errorf("%w: too many admissions are open on this node", ErrMesh)
	}
	self := m.selfNearbyLocked()
	m.mu.Unlock()
	if a == nil {
		a = newAdmission(v1.AdmissionSide_ADMISSION_SIDE_CANDIDATE)
	}
	// The record says asked before the knock goes out, so the grant that answers it is taken
	a.Node, a.Asked, a.Detail = target, true, ""
	a.MeshHash, a.MeshName, a.MeshMembers = target.GetMeshHash(), target.GetMeshName(), target.GetMeshMembers()
	a.ExpiresAt = timestamppb.New(time.Now().Add(AdmissionTTL))
	if settled(a.GetState()) {
		a.State = v1.AdmissionState_ADMISSION_STATE_PENDING
	}
	m.saveAdmission(a)
	if grant := m.heldGrant(a.GetId()); grant != "" {
		return m.acceptWith(ctx, a, grant)
	}
	fail := func(err error) (*v1.Admission, error) {
		a.State, a.Detail = v1.AdmissionState_ADMISSION_STATE_FAILED, fmt.Sprintf("the request did not reach %s: %v", describe(target), err)
		m.saveAdmission(a)
		return nil, fmt.Errorf("%w: %s", ErrMesh, a.GetDetail())
	}
	cctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	cl, fingerprint, err := m.reach(cctx, target.GetAddress(), target.GetId(), target.GetTlsFingerprint())
	if err != nil {
		return fail(err)
	}
	resp, err := cl.Knock(cctx, connect.NewRequest(&v1.KnockRequest{Node: self, MeshHash: target.GetMeshHash()}))
	if err != nil {
		return fail(err)
	}
	member := resp.Msg.GetMember()
	if member.GetId() == "" || member.GetMeshHash() == "" {
		return fail(fmt.Errorf("it belongs to no mesh"))
	}
	if target.GetMeshHash() != "" && member.GetMeshHash() != target.GetMeshHash() {
		return fail(fmt.Errorf("it belongs to another mesh, %s", member.GetMeshName()))
	}
	m.mu.Lock()
	// The token may have landed while the knock was out
	if have := m.admissionByMeshLocked(member.GetMeshHash()); have != nil {
		if have.GetId() != a.GetId() {
			delete(m.admissions, a.GetId())
		}
		have.Asked = true
		a = have
	}
	member.TlsFingerprint, member.Member = fingerprint, false
	if member.GetAddress() == "" {
		member.Address = target.GetAddress()
	}
	m.mu.Unlock()
	a.Node, a.Reached = member, true
	a.MeshHash, a.MeshName, a.MeshMembers = member.GetMeshHash(), member.GetMeshName(), member.GetMeshMembers()
	switch resp.Msg.GetState() {
	case v1.AdmissionState_ADMISSION_STATE_ADMITTED, v1.AdmissionState_ADMISSION_STATE_JOINED:
		// The grant follows on its own call, the record waiting for it unless it came already
		if a.GetState() != v1.AdmissionState_ADMISSION_STATE_JOINED {
			a.State = v1.AdmissionState_ADMISSION_STATE_ADMITTED
		}
	default:
		if a.GetState() != v1.AdmissionState_ADMISSION_STATE_JOINED && a.GetState() != v1.AdmissionState_ADMISSION_STATE_ADMITTED {
			a.State = v1.AdmissionState_ADMISSION_STATE_PENDING
		}
	}
	m.Log.Info("mesh join asked", "mesh", member.GetMeshName(), "through", member.GetName(), "address", member.GetAddress())
	return m.saveAdmission(a), nil
}

// Takes a request to join or a declined invitation from a node outside, admitting an invited one at once
func (m *Manager) knock(ctx context.Context, c caller, req *v1.KnockRequest) (*v1.KnockResponse, error) {
	node := req.GetNode()
	if node.GetId() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("%w: a knock names the node knocking", ErrMesh))
	}
	if node.GetId() != c.id {
		return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("%w: the knock names %s but came from %s", ErrMesh, node.GetId(), c.id))
	}
	if node.GetId() == m.identity.ID {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("%w: a node cannot knock on itself", ErrMesh))
	}
	m.mu.Lock()
	if m.mesh == nil {
		m.mu.Unlock()
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("%w: this node belongs to no mesh", ErrMesh))
	}
	own := meshHex(m.mesh.ID)
	if req.GetMeshHash() != "" && req.GetMeshHash() != own {
		m.mu.Unlock()
		return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("%w: this node belongs to another mesh", ErrMesh))
	}
	self := m.selfNearbyLocked()
	if _, member := m.members[node.GetId()]; member {
		m.mu.Unlock()
		return &v1.KnockResponse{State: v1.AdmissionState_ADMISSION_STATE_JOINED, Member: self}, nil
	}
	a := m.admissionByNodeLocked(node.GetId())
	if req.GetDeclined() {
		m.mu.Unlock()
		if a != nil {
			a.State, a.Detail = v1.AdmissionState_ADMISSION_STATE_DECLINED, node.GetName()+" declined the invitation"
			m.saveAdmission(a)
		}
		return &v1.KnockResponse{State: v1.AdmissionState_ADMISSION_STATE_DECLINED, Member: self}, nil
	}
	if a == nil && len(m.admissions) >= maxAdmissions {
		m.mu.Unlock()
		return nil, connect.NewError(connect.CodeResourceExhausted, fmt.Errorf("%w: too many admissions are open on this node", ErrMesh))
	}
	reached := m.reachedLocked(node.GetId(), node.GetAddress())
	if h, ok := m.nearby[node.GetId()]; ok {
		node.From = h.rec.GetFrom()
	}
	meshName, members := m.mesh.Name, uint32(len(m.members)+1)
	m.mu.Unlock()
	if a == nil {
		a = newAdmission(v1.AdmissionSide_ADMISSION_SIDE_MEMBER)
	}
	node.Member, node.TlsFingerprint = false, fingerprintOf(c.cert.Raw)
	a.Node, a.Asked, a.Detail, a.Reached = node, true, "", reached
	a.MeshHash, a.MeshName, a.MeshMembers = own, meshName, members
	a.ExpiresAt = timestamppb.New(time.Now().Add(AdmissionTTL))
	if settled(a.GetState()) {
		a.State = v1.AdmissionState_ADMISSION_STATE_PENDING
	}
	if a.GetInvited() && a.GetState() == v1.AdmissionState_ADMISSION_STATE_PENDING {
		// The invitation stands as the decision
		a.State = v1.AdmissionState_ADMISSION_STATE_ADMITTED
		out := m.saveAdmission(a)
		if !m.spawn(func() {
			if err := m.deliver(m.base, a); err != nil {
				m.Log.Warn("mesh grant delivery failed", "node", node.GetName(), "err", err)
			}
		}) {
			a.State, a.Detail = v1.AdmissionState_ADMISSION_STATE_FAILED, "this node shut down before the grant went out"
			m.saveAdmission(a)
			return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("%w: %s", ErrMesh, a.GetDetail()))
		}
		m.Log.Info("mesh invitation accepted", "node", node.GetId(), "name", node.GetName())
		return &v1.KnockResponse{State: out.GetState(), Member: self}, nil
	}
	out := m.saveAdmission(a)
	m.Log.Info("mesh join requested", "node", node.GetId(), "name", node.GetName(), "address", node.GetAddress())
	return &v1.KnockResponse{State: out.GetState(), Member: self}, nil
}

// Admits a node that asked, a join grant going to it so it joins
func (m *Manager) Admit(ctx context.Context, ref string) (*v1.Admission, error) {
	m.mu.Lock()
	if m.mesh == nil {
		m.mu.Unlock()
		return nil, ErrNoMesh
	}
	a := m.admissionLocked(ref)
	if a == nil || a.GetSide() != v1.AdmissionSide_ADMISSION_SIDE_MEMBER {
		m.mu.Unlock()
		return nil, fmt.Errorf("%w: no node %q is waiting to join", ErrUnknownNode, ref)
	}
	by := m.name()
	m.mu.Unlock()
	if a.GetState() == v1.AdmissionState_ADMISSION_STATE_JOINED {
		return proto.Clone(a).(*v1.Admission), nil
	}
	if !a.GetAsked() {
		return nil, fmt.Errorf("%w: %s has not asked to join; it was invited and its own page decides", ErrMesh, a.GetNode().GetName())
	}
	a.State, a.By, a.Detail = v1.AdmissionState_ADMISSION_STATE_ADMITTED, by, ""
	m.saveAdmission(a)
	if err := m.deliver(ctx, a); err != nil {
		return nil, err
	}
	m.mu.Lock()
	out := proto.Clone(a).(*v1.Admission)
	m.mu.Unlock()
	return out, nil
}

// Hands an admitted node a join grant and confirms the join by the certificate it answers with
func (m *Manager) deliver(ctx context.Context, a *v1.Admission) error {
	node := a.GetNode()
	fail := func(err error) error {
		a.State, a.Detail = v1.AdmissionState_ADMISSION_STATE_FAILED, fmt.Sprintf("the grant did not reach %s: %v", describe(node), err)
		m.saveAdmission(a)
		return fmt.Errorf("%w: %s", ErrMesh, a.GetDetail())
	}
	grant, _, err := m.Token(ctx, welcomeTimeout+time.Minute)
	if err != nil {
		return fail(err)
	}
	m.mu.Lock()
	self := m.selfNearbyLocked()
	m.mu.Unlock()
	cctx, cancel := context.WithTimeout(ctx, welcomeTimeout)
	defer cancel()
	cl, _, err := m.reach(cctx, node.GetAddress(), node.GetId(), node.GetTlsFingerprint())
	if err != nil {
		return fail(err)
	}
	resp, err := cl.Welcome(cctx, connect.NewRequest(&v1.WelcomeRequest{Member: self, MeshHash: a.GetMeshHash(), Grant: grant}))
	if err != nil {
		return fail(err)
	}
	if resp.Msg.GetNodeId() != node.GetId() {
		return fail(fmt.Errorf("%s answered as another node", node.GetAddress()))
	}
	a.Reached = true
	if !resp.Msg.GetJoined() {
		a.State = v1.AdmissionState_ADMISSION_STATE_ADMITTED
		m.saveAdmission(a)
		return nil
	}
	if err := m.confirmJoined(resp.Msg.GetCertificate(), node.GetId()); err != nil {
		return fail(err)
	}
	a.State, a.Detail = v1.AdmissionState_ADMISSION_STATE_JOINED, ""
	m.Log.Info("mesh member admitted from the page", "node", node.GetId(), "name", node.GetName())
	m.saveAdmission(a)
	return nil
}

// Proves a node joined by the certificate it holds, signed by this mesh's authority and naming the node
func (m *Manager) confirmJoined(der []byte, nodeID string) error {
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return fmt.Errorf("it answered joined without a readable certificate: %v", err)
	}
	creds := m.currentCreds()
	if creds == nil {
		return ErrNoMesh
	}
	id, err := verifyChain([]*x509.Certificate{cert}, creds.pool, x509.ExtKeyUsageClientAuth, time.Now())
	if err != nil {
		return err
	}
	if id != nodeID {
		return fmt.Errorf("its certificate names %s", id)
	}
	return nil
}

// Joins with the grant an admitting member hands over, answering with the signed certificate, or takes its refusal
func (m *Manager) welcome(ctx context.Context, c caller, req *v1.WelcomeRequest) (*v1.WelcomeResponse, error) {
	member := req.GetMember()
	if member.GetId() == "" || req.GetMeshHash() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("%w: a welcome names the member and its mesh", ErrMesh))
	}
	if member.GetId() != c.id {
		return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("%w: the welcome names %s but came from %s", ErrMesh, member.GetId(), c.id))
	}
	m.mu.Lock()
	if m.mesh != nil {
		if meshHex(m.mesh.ID) == req.GetMeshHash() {
			der := m.creds.leaf.Raw
			m.mu.Unlock()
			return &v1.WelcomeResponse{Joined: true, NodeId: m.identity.ID, Certificate: der}, nil
		}
		name := m.mesh.Name
		m.mu.Unlock()
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("%w: this node belongs to mesh %s already", ErrMesh, name))
	}
	a := m.admissionByMeshLocked(req.GetMeshHash())
	if a == nil {
		a = m.admissionByMemberLocked(member)
	}
	m.mu.Unlock()
	if a == nil || !a.GetAsked() {
		return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("%w: this node did not ask to join %s", ErrMesh, member.GetMeshName()))
	}
	switch a.GetState() {
	case v1.AdmissionState_ADMISSION_STATE_DECLINED, v1.AdmissionState_ADMISSION_STATE_DISMISSED:
		return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("%w: this node withdrew its request to join %s", ErrMesh, member.GetMeshName()))
	}
	if req.GetDenied() {
		a.State, a.By = v1.AdmissionState_ADMISSION_STATE_DENIED, member.GetName()
		a.Detail = req.GetDetail()
		if a.Detail == "" {
			a.Detail = "denied by " + member.GetName()
		}
		m.saveAdmission(a)
		m.Log.Info("mesh join denied", "mesh", member.GetMeshName(), "by", member.GetName())
		return &v1.WelcomeResponse{NodeId: m.identity.ID}, nil
	}
	g, err := decodeGrant(req.GetGrant())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if g.MeshHash != req.GetMeshHash() || a.GetMeshHash() != "" && a.GetMeshHash() != req.GetMeshHash() {
		return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("%w: the grant names another mesh than the one this node asked to join", ErrMesh))
	}
	a.MeshHash, a.By = req.GetMeshHash(), member.GetName()
	a.State = v1.AdmissionState_ADMISSION_STATE_ADMITTED
	m.saveAdmission(a)
	if _, err := m.acceptWith(ctx, a, req.GetGrant()); err != nil {
		return nil, err
	}
	m.mu.Lock()
	der := m.creds.leaf.Raw
	m.mu.Unlock()
	return &v1.WelcomeResponse{Joined: true, NodeId: m.identity.ID, Certificate: der}, nil
}

// Joins with a grant an admission holds, recording the join or why it failed
func (m *Manager) acceptWith(ctx context.Context, a *v1.Admission, grant string) (*v1.Admission, error) {
	mesh, _, err := m.Join(ctx, grant)
	if err != nil {
		a.State, a.Detail = v1.AdmissionState_ADMISSION_STATE_FAILED, fmt.Sprintf("the join through %s failed: %v", describe(a.GetNode()), err)
		m.saveAdmission(a)
		return nil, err
	}
	a.State, a.Detail, a.MeshName = v1.AdmissionState_ADMISSION_STATE_JOINED, "", mesh.GetName()
	return m.saveAdmission(a), nil
}

// Keeps the grant an invitation carried with its admission
func (m *Manager) holdGrant(id, grant string) error {
	m.rows.Lock()
	defer m.rows.Unlock()
	m.mu.Lock()
	m.grants[id] = grant
	m.mu.Unlock()
	return m.DB.SetAdmissionGrant(context.Background(), id, grant)
}

// The grant an admission holds, empty when none
func (m *Manager) heldGrant(id string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.grants[id]
}

// Denies a node that asked, or declines an invitation, and tells the other side
func (m *Manager) Refuse(ctx context.Context, ref string) (*v1.Admission, error) {
	m.mu.Lock()
	a := m.admissionLocked(ref)
	if a == nil {
		m.mu.Unlock()
		return nil, fmt.Errorf("%w: no admission %q", ErrUnknownNode, ref)
	}
	self := m.selfNearbyLocked()
	m.mu.Unlock()
	node := a.GetNode()
	cctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	var told error
	switch a.GetSide() {
	case v1.AdmissionSide_ADMISSION_SIDE_MEMBER:
		a.State, a.By, a.Detail = v1.AdmissionState_ADMISSION_STATE_DENIED, self.GetName(), "denied by "+self.GetName()
		m.saveAdmission(a)
		cl, _, err := m.reach(cctx, node.GetAddress(), node.GetId(), node.GetTlsFingerprint())
		if err == nil {
			_, err = cl.Welcome(cctx, connect.NewRequest(&v1.WelcomeRequest{Member: self, MeshHash: a.GetMeshHash(), Denied: true, Detail: a.GetDetail()}))
		}
		told = err
	default:
		a.State, a.Detail = v1.AdmissionState_ADMISSION_STATE_DECLINED, "declined here"
		m.saveAdmission(a)
		cl, _, err := m.reach(cctx, node.GetAddress(), node.GetId(), node.GetTlsFingerprint())
		if err == nil {
			_, err = cl.Knock(cctx, connect.NewRequest(&v1.KnockRequest{Node: self, MeshHash: a.GetMeshHash(), Declined: true}))
		}
		told = err
	}
	if told != nil {
		a.Detail = fmt.Sprintf("%s; %s was not told and learns when its request expires: %v", a.GetDetail(), describe(node), told)
	}
	return m.saveAdmission(a), nil
}

// Clears a settled admission, a member's copy kept as dismissed so others clear theirs and a candidate's dropped
func (m *Manager) Dismiss(ctx context.Context, ref string) error {
	m.mu.Lock()
	a := m.admissionLocked(ref)
	m.mu.Unlock()
	if a == nil {
		return fmt.Errorf("%w: no admission %q", ErrUnknownNode, ref)
	}
	if !settled(a.GetState()) {
		return fmt.Errorf("%w: %s is still waiting; refuse it instead", ErrMesh, describe(a.GetNode()))
	}
	if a.GetSide() == v1.AdmissionSide_ADMISSION_SIDE_MEMBER {
		a.State = v1.AdmissionState_ADMISSION_STATE_DISMISSED
		m.saveAdmission(a)
		return nil
	}
	m.dropAdmission(a.GetId())
	return nil
}

// A member's admissions as the sync carries them to the other members
func (m *Manager) admissionsForSyncLocked() []*v1.Admission {
	var out []*v1.Admission
	for _, a := range m.admissions {
		if a.GetSide() == v1.AdmissionSide_ADMISSION_SIDE_MEMBER {
			out = append(out, proto.Clone(a).(*v1.Admission))
		}
	}
	return out
}

// Takes another member's admissions, one record per node, the latest copy winning and set flags holding
func (m *Manager) mergeAdmissions(list []*v1.Admission) {
	if len(list) == 0 {
		return
	}
	var changed []*v1.Admission
	var dropped []string
	m.rows.Lock()
	m.mu.Lock()
	own := ""
	if m.mesh != nil {
		own = meshHex(m.mesh.ID)
	}
	for _, in := range list {
		if own == "" || in.GetSide() != v1.AdmissionSide_ADMISSION_SIDE_MEMBER || in.GetMeshHash() != own || in.GetNode().GetId() == "" || in.GetId() == "" {
			continue
		}
		if in.GetNode().GetId() == m.identity.ID {
			continue
		}
		have := m.admissionByNodeLocked(in.GetNode().GetId())
		switch {
		case have == nil:
			rec := proto.Clone(in).(*v1.Admission)
			if _, member := m.members[rec.GetNode().GetId()]; member && rec.GetState() != v1.AdmissionState_ADMISSION_STATE_DISMISSED {
				rec.State = v1.AdmissionState_ADMISSION_STATE_JOINED
			}
			m.admissions[rec.GetId()] = rec
			changed = append(changed, proto.Clone(rec).(*v1.Admission))
		case have.GetId() != in.GetId() && in.GetCreatedAt().AsTime().Before(have.GetCreatedAt().AsTime()):
			// Two members opened a record for the same node before syncing, so the older id stays
			rec := proto.Clone(in).(*v1.Admission)
			rec.Invited, rec.Asked = rec.GetInvited() || have.GetInvited(), rec.GetAsked() || have.GetAsked()
			if have.GetUpdatedAt().AsTime().After(rec.GetUpdatedAt().AsTime()) {
				rec.State, rec.By, rec.Detail, rec.Node, rec.ExpiresAt, rec.UpdatedAt, rec.Reached = have.GetState(), have.GetBy(), have.GetDetail(), have.GetNode(), have.GetExpiresAt(), have.GetUpdatedAt(), have.GetReached()
			}
			delete(m.admissions, have.GetId())
			dropped = append(dropped, have.GetId())
			m.admissions[rec.GetId()] = rec
			changed = append(changed, proto.Clone(rec).(*v1.Admission))
		case in.GetUpdatedAt().AsTime().After(have.GetUpdatedAt().AsTime()):
			have.State, have.By, have.Detail, have.Node, have.ExpiresAt, have.UpdatedAt = in.GetState(), in.GetBy(), in.GetDetail(), proto.Clone(in.GetNode()).(*v1.NearbyNode), in.GetExpiresAt(), in.GetUpdatedAt()
			have.Invited, have.Asked, have.Reached = have.GetInvited() || in.GetInvited(), have.GetAsked() || in.GetAsked(), have.GetReached() || in.GetReached()
			m.admissions[have.GetId()] = have
			changed = append(changed, proto.Clone(have).(*v1.Admission))
		default:
			if in.GetInvited() && !have.GetInvited() || in.GetAsked() && !have.GetAsked() || in.GetReached() && !have.GetReached() {
				have.Invited, have.Asked, have.Reached = have.GetInvited() || in.GetInvited(), have.GetAsked() || in.GetAsked(), have.GetReached() || in.GetReached()
				m.admissions[have.GetId()] = have
				changed = append(changed, proto.Clone(have).(*v1.Admission))
			}
		}
	}
	m.mu.Unlock()
	ctx := context.Background()
	for _, id := range dropped {
		if err := m.DB.DeleteAdmission(ctx, id); err != nil {
			m.Log.Warn("admission delete failed", "id", id, "err", err)
		}
	}
	for _, a := range changed {
		if err := m.DB.PutAdmission(ctx, a); err != nil {
			m.Log.Warn("admission write failed", "id", a.GetId(), "err", err)
		}
	}
	m.rows.Unlock()
	if len(changed) > 0 || len(dropped) > 0 {
		m.publishStatus()
	}
}

// Marks a member's admission joined once the node is a member
func (m *Manager) markJoined(nodeID string) {
	m.mu.Lock()
	a := m.admissionByNodeLocked(nodeID)
	if a == nil || a.GetState() == v1.AdmissionState_ADMISSION_STATE_JOINED || a.GetState() == v1.AdmissionState_ADMISSION_STATE_DISMISSED {
		m.mu.Unlock()
		return
	}
	a.State, a.Detail = v1.AdmissionState_ADMISSION_STATE_JOINED, ""
	m.mu.Unlock()
	m.saveAdmission(a)
}

// Marks the candidate record for the joined mesh joined and drops the records for every other mesh
func (m *Manager) settleAfterJoin(meshID string) {
	hash := meshHex(meshID)
	m.mu.Lock()
	var joined *v1.Admission
	var drop []string
	for id, a := range m.admissions {
		if a.GetSide() != v1.AdmissionSide_ADMISSION_SIDE_CANDIDATE {
			continue
		}
		if a.GetMeshHash() == hash {
			joined = proto.Clone(a).(*v1.Admission)
			continue
		}
		drop = append(drop, id)
	}
	m.mu.Unlock()
	for _, id := range drop {
		m.dropAdmission(id)
	}
	if joined != nil && joined.GetState() != v1.AdmissionState_ADMISSION_STATE_JOINED {
		joined.State = v1.AdmissionState_ADMISSION_STATE_JOINED
		m.saveAdmission(joined)
	}
}

// Forgets every admission, when the node leaves its mesh or makes one
func (m *Manager) clearAdmissions() {
	m.rows.Lock()
	m.mu.Lock()
	m.admissions = map[string]*v1.Admission{}
	m.grants = map[string]string{}
	m.mu.Unlock()
	if err := m.DB.DeleteAdmissions(context.Background()); err != nil {
		m.Log.Warn("admissions delete failed", "err", err)
	}
	m.rows.Unlock()
	m.publishStatus()
}

// Expires admissions nobody decided and drops settled ones once they have been shown long enough
func (m *Manager) admissionLoop(ctx context.Context) {
	ticker := time.NewTicker(admissionSweep)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		now := time.Now()
		var expired []*v1.Admission
		var drop []string
		m.mu.Lock()
		for id, a := range m.admissions {
			switch {
			case a.GetState() == v1.AdmissionState_ADMISSION_STATE_PENDING && a.GetExpiresAt() != nil && now.After(a.GetExpiresAt().AsTime()):
				a.State, a.Detail = v1.AdmissionState_ADMISSION_STATE_EXPIRED, "nobody decided within "+AdmissionTTL.String()
				expired = append(expired, proto.Clone(a).(*v1.Admission))
			case settled(a.GetState()) && now.Sub(a.GetUpdatedAt().AsTime()) > settledTTL:
				drop = append(drop, id)
			}
		}
		m.mu.Unlock()
		for _, a := range expired {
			m.saveAdmission(a)
		}
		for _, id := range drop {
			m.dropAdmission(id)
		}
	}
}
