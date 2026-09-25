package mesh

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"fmt"
	"time"

	"connectrpc.com/connect"
	"github.com/nickheyer/nebu/internal/db"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// HMAC under the secret of a nonce and the two ids, in the order the side proving it names them
func proof(secret, nonce []byte, a, b string) []byte {
	mac := hmac.New(sha256.New, secret)
	mac.Write(nonce)
	mac.Write([]byte(a))
	mac.Write([]byte{0})
	mac.Write([]byte(b))
	return mac.Sum(nil)
}

func nonce() ([]byte, error) {
	out := make([]byte, 32)
	_, err := rand.Read(out)
	return out, err
}

// Runs the handshake with a member and holds the session token it mints. The token, when
// joining, supplies the secret this node does not hold yet, and verified runs once the member has
// proven it holds that secret, before the second call, so a join is on record by the time the
// member's first sync arrives.
func (m *Manager) handshake(ctx context.Context, address string, useTLS bool, fingerprint string, joining *joinToken, verified func() error) (*v1.HelloResponse, error) {
	m.mu.Lock()
	var secret []byte
	var meshID string
	if joining != nil {
		secret, meshID = joining.Secret, joining.ID
	} else if m.mesh != nil {
		secret, meshID = m.mesh.Secret, m.mesh.ID
	}
	var cfg *tls.Config
	if useTLS {
		cfg = m.dialTLSLocked(fingerprint)
	}
	served, _ := m.servedCertLocked()
	m.mu.Unlock()
	if secret == nil {
		return nil, ErrNoMesh
	}
	cl := m.anonymous(address, cfg, fingerprint)
	nonceA, err := nonce()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	first, err := cl.Mesh.Hello(ctx, connect.NewRequest(&v1.HelloRequest{NodeId: m.identity.ID, Nonce: nonceA, MeshHash: meshHash(meshID), Version: m.Version}))
	if err != nil {
		return nil, fmt.Errorf("hello %s: %w", address, err)
	}
	peer := first.Msg.GetNodeId()
	if peer == "" || peer == m.identity.ID {
		return nil, fmt.Errorf("%w: %s answered the handshake as %q", ErrMesh, address, peer)
	}
	if !hmac.Equal(first.Msg.GetProof(), proof(secret, nonceA, m.identity.ID, peer)) {
		return nil, fmt.Errorf("%w: %s does not hold this mesh's secret", ErrMesh, address)
	}
	if verified != nil {
		if err := verified(); err != nil {
			return nil, err
		}
	}
	second, err := cl.Mesh.Hello(ctx, connect.NewRequest(&v1.HelloRequest{
		NodeId:         m.identity.ID,
		Nonce:          nonceA,
		MeshHash:       meshHash(meshID),
		Proof:          proof(secret, first.Msg.GetNonce(), peer, m.identity.ID),
		Node:           m.Record(),
		Version:        m.Version,
		TlsFingerprint: served,
	}))
	if err != nil {
		return nil, fmt.Errorf("hello %s: %w", address, err)
	}
	resp := second.Msg
	if resp.GetSessionToken() == "" {
		return nil, fmt.Errorf("%w: %s admitted this node without a session token", ErrMesh, address)
	}
	m.mu.Lock()
	s := m.sessions[peer]
	if s == nil {
		s = &session{peer: peer, granted: time.Now()}
		m.sessions[peer] = s
	}
	s.send = resp.GetSessionToken()
	s.sendExpires = resp.GetExpiresAt().AsTime()
	if resp.GetExpiresAt() == nil {
		s.sendExpires = time.Now().Add(SessionTTL)
	}
	row := s.row()
	m.mu.Unlock()
	if err := m.DB.PutSession(context.Background(), row); err != nil {
		m.Log.Warn("session write failed", "peer", peer, "err", err)
	}
	return resp, nil
}

// The session as stored
func (s *session) row() *db.SessionRow {
	expires := s.expires
	if s.sendExpires.After(expires) {
		expires = s.sendExpires
	}
	return &db.SessionRow{PeerID: s.peer, Accept: s.accept, Send: s.send, Expires: expires, Granted: s.granted}
}

// Answers a handshake. The first call carries a nonce and gets ours with the proof of theirs;
// the second carries the proof of ours and gets a session token, our record, the member list,
// and the mesh's certificate material when it has any.
func (m *Manager) Hello(ctx context.Context, req *v1.HelloRequest) (*v1.HelloResponse, error) {
	m.mu.Lock()
	mesh := m.mesh
	m.mu.Unlock()
	if mesh == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, ErrNoMesh)
	}
	if !bytes.Equal(req.GetMeshHash(), meshHash(mesh.ID)) {
		return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("%w: this node belongs to another mesh", ErrMesh))
	}
	caller := req.GetNodeId()
	if caller == "" || caller == m.identity.ID {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("%w: a handshake names the calling node", ErrMesh))
	}
	if len(req.GetProof()) == 0 {
		nonceB, err := nonce()
		if err != nil {
			return nil, err
		}
		m.mu.Lock()
		m.pendings[caller] = &pending{nonceA: req.GetNonce(), nonceB: nonceB, expires: time.Now().Add(handshakeTTL)}
		for id, p := range m.pendings {
			if time.Now().After(p.expires) {
				delete(m.pendings, id)
			}
		}
		m.mu.Unlock()
		return &v1.HelloResponse{NodeId: m.identity.ID, Nonce: nonceB, Proof: proof(mesh.Secret, req.GetNonce(), caller, m.identity.ID)}, nil
	}
	m.mu.Lock()
	p := m.pendings[caller]
	delete(m.pendings, caller)
	m.mu.Unlock()
	if p == nil || time.Now().After(p.expires) || !bytes.Equal(p.nonceA, req.GetNonce()) {
		return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("%w: no handshake is open for %s, start again", ErrMesh, caller))
	}
	if !hmac.Equal(req.GetProof(), proof(mesh.Secret, p.nonceB, m.identity.ID, caller)) {
		return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("%w: %s does not hold this mesh's secret", ErrMesh, caller))
	}
	token, err := m.mint(caller)
	if err != nil {
		return nil, err
	}
	if rec := req.GetNode(); rec != nil && rec.GetId() == caller {
		if rec.TlsFingerprint == "" && req.GetTlsFingerprint() != "" {
			rec.TlsFingerprint, rec.Tls = req.GetTlsFingerprint(), true
		}
		if rec.Version == "" {
			rec.Version = req.GetVersion()
		}
		m.absorb(rec, nil, true)
	}
	m.mu.Lock()
	resp := &v1.HelloResponse{
		NodeId:       m.identity.ID,
		SessionToken: token,
		Members:      m.memberListLocked(),
		Mesh:         m.meshLocked(),
		ExpiresAt:    timestamppb.New(m.sessions[caller].expires),
	}
	var issueErr error
	if mesh.TLS {
		resp.CaCertificate, resp.CaKey = mesh.CACertificate, mesh.CAKey
		if pub := req.GetNode().GetPublicKey(); len(pub) == ed25519.PublicKeySize {
			resp.Certificate, issueErr = issue(mesh.CACertificate, mesh.CAKey, ed25519.PublicKey(pub), caller, req.GetNode().GetName())
		} else {
			issueErr = fmt.Errorf("%w: %s sent no identity key, so no certificate can be issued", ErrMesh, caller)
		}
	}
	m.mu.Unlock()
	if issueErr != nil {
		return nil, issueErr
	}
	resp.Node = m.Record()
	m.Log.Info("mesh member admitted", "node", caller, "name", req.GetNode().GetName())
	m.bump()
	return resp, nil
}

// Mints the token a peer sends us, replacing any earlier one
func (m *Manager) mint(peer string) (string, error) {
	token, err := randomToken()
	if err != nil {
		return "", err
	}
	m.mu.Lock()
	s := m.sessions[peer]
	if s == nil {
		s = &session{peer: peer}
		m.sessions[peer] = s
	}
	if s.accept != "" {
		delete(m.accept, s.accept)
	}
	s.accept, s.expires, s.granted = token, time.Now().Add(SessionTTL), time.Now()
	m.accept[token] = peer
	row := s.row()
	m.mu.Unlock()
	if err := m.DB.PutSession(context.Background(), row); err != nil {
		return "", err
	}
	return token, nil
}

// Extends the token a peer sends us, on every sync it makes
func (m *Manager) renew(peer string) time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.sessions[peer]
	if s == nil {
		return time.Time{}
	}
	s.expires = time.Now().Add(SessionTTL)
	row := s.row()
	go func() {
		if err := m.DB.PutSession(context.Background(), row); err != nil {
			m.Log.Warn("session write failed", "peer", peer, "err", err)
		}
	}()
	return s.expires
}

// Forgets the token we send a peer, so the next call handshakes again
func (m *Manager) dropSend(peer string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s := m.sessions[peer]; s != nil {
		s.send, s.sendExpires = "", time.Time{}
	}
}
