package mesh

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/proto/nebu/v1/nebuv1connect"
)

// Makes a mesh on this node, which holds its authority as issuer, and returns it with a join token
func (m *Manager) Init(ctx context.Context, name string) (*v1.Mesh, string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "mesh"
	}
	m.changing.Lock()
	defer m.changing.Unlock()
	m.mu.Lock()
	if m.mesh != nil {
		have := m.mesh.Name
		m.mu.Unlock()
		return nil, "", fmt.Errorf("%w: this node already belongs to %s and must leave it first", ErrMesh, have)
	}
	m.mu.Unlock()
	if err := m.listen(); err != nil {
		return nil, "", err
	}
	if err := m.requireReachable(); err != nil {
		return nil, "", err
	}
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return nil, "", err
	}
	now := time.Now()
	caDER, caKey, err := newCA(name, now)
	if err != nil {
		return nil, "", err
	}
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		return nil, "", err
	}
	key, err := newKey()
	if err != nil {
		return nil, "", err
	}
	certDER, _, err := signNode(ca, caKey, key.Public().(ed25519.PublicKey), m.identity.ID, now)
	if err != nil {
		return nil, "", err
	}
	creds, err := newCredentials(caDER, certDER, key, caKey)
	if err != nil {
		return nil, "", err
	}
	row, err := newMeshRow(hex.EncodeToString(raw), name, m.identity.ID, 0, creds, now)
	if err != nil {
		return nil, "", err
	}
	if err := m.DB.PutMesh(ctx, row); err != nil {
		return nil, "", err
	}
	m.mu.Lock()
	m.mesh, m.creds = row, creds
	m.mu.Unlock()
	m.Log.Info("mesh made", "mesh", row.ID, "name", name, "authority", creds.caFingerprint()[:16])
	m.clearAdmissions()
	m.startLoops()
	m.bump()
	m.publishStatus()
	token, _, err := m.Token(ctx, 0)
	if err != nil {
		return nil, "", err
	}
	mesh, err := m.Mesh()
	return mesh, token, err
}

// Joins the mesh a token names, the issuer signing a request for a fresh key this node makes
func (m *Manager) Join(ctx context.Context, token string) (*v1.Mesh, *v1.Node, error) {
	h, err := decodeGrant(token)
	if err != nil {
		return nil, nil, err
	}
	m.changing.Lock()
	defer m.changing.Unlock()
	m.mu.Lock()
	if m.mesh != nil {
		have := m.mesh.Name
		m.mu.Unlock()
		return nil, nil, fmt.Errorf("%w: this node already belongs to %s and must leave it first", ErrMesh, have)
	}
	m.mu.Unlock()
	if time.Now().After(h.Expires) {
		return nil, nil, fmt.Errorf("%w: the join token expired at %s", ErrMesh, h.Expires.Format(time.RFC3339))
	}
	if err := m.listen(); err != nil {
		return nil, nil, err
	}
	if err := m.requireReachable(); err != nil {
		return nil, nil, err
	}
	key, err := newKey()
	if err != nil {
		return nil, nil, err
	}
	csr, err := newCSR(key, m.identity.ID)
	if err != nil {
		return nil, nil, err
	}
	rt := transport(grantDialConfig(h))
	defer rt.CloseIdleConnections()
	issuer := nebuv1connect.NewMeshPeerServiceClient(&http.Client{Transport: rt}, "https://"+h.Address)
	cctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	resp, err := issuer.Enroll(cctx, connect.NewRequest(&v1.EnrollRequest{Csr: csr, Node: m.Record()}))
	if err != nil {
		return nil, nil, fmt.Errorf("enroll with %s: %w", h.Address, err)
	}
	out := resp.Msg
	if fingerprintOf(out.GetCaCertificate()) != h.ca {
		return nil, nil, fmt.Errorf("%w: the issuer answered with another authority than the token pins", ErrMesh)
	}
	creds, err := newCredentials(out.GetCaCertificate(), out.GetCertificate(), key, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: the certificate the issuer signed is not usable: %v", ErrMesh, err)
	}
	if id, ok := nodeIDOf(creds.leaf); !ok || id != m.identity.ID {
		return nil, nil, fmt.Errorf("%w: the issuer signed a certificate for another node", ErrMesh)
	}
	meshInfo := out.GetMesh()
	if meshHex(meshInfo.GetId()) != h.MeshHash {
		return nil, nil, fmt.Errorf("%w: the issuer answered for another mesh than the token names", ErrMesh)
	}
	row, err := newMeshRow(meshInfo.GetId(), meshInfo.GetName(), h.Issuer, meshInfo.GetIssuerEpoch(), creds, meshInfo.GetCreatedAt().AsTime())
	if err != nil {
		return nil, nil, err
	}
	m.rows.Lock()
	if err := m.DB.PutMesh(ctx, row); err != nil {
		m.rows.Unlock()
		return nil, nil, err
	}
	m.mu.Lock()
	m.mesh, m.creds = row, creds
	m.mu.Unlock()
	m.rows.Unlock()
	bootstrap := out.GetIssuer()
	m.mergeMembers(out.GetMembers())
	for _, r := range meshInfo.GetRevocations() {
		if _, err := m.revoke(r); err != nil {
			m.Log.Error("revocation from the issuer not held, the next exchange carries it again", "serial", r.GetSerial(), "err", err)
		}
	}
	m.Log.Info("mesh joined", "mesh", row.ID, "name", row.Name, "issuer", h.Issuer, "through", h.Address)
	m.settleAfterJoin(row.ID)
	loops := m.startLoops()
	m.bump()
	m.publishStatus()
	m.spawn(func() {
		select {
		case <-loops.Done():
		case <-time.After(m.interval):
			if _, err := m.Probe(loops, "", false); err != nil && loops.Err() == nil {
				m.Log.Warn("link probe after join failed", "err", err)
			}
		}
	})
	mesh, err := m.Mesh()
	return mesh, bootstrap, err
}

// Leaves the mesh, handing the issuer role on first, telling members so each revokes this node's certificate
func (m *Manager) Leave(ctx context.Context) (*v1.Mesh, error) {
	m.changing.Lock()
	defer m.changing.Unlock()
	return m.leave(ctx, true)
}

// Leaves with the membership lock held, the issuer role handed to a ready member first when asked
func (m *Manager) leave(ctx context.Context, handOff bool) (*v1.Mesh, error) {
	m.mu.Lock()
	if m.mesh == nil {
		m.mu.Unlock()
		return nil, ErrNoMesh
	}
	out := m.meshLocked()
	issuer := m.creds.caKey != nil
	var ready []string
	ids := make([]string, 0, len(m.members))
	for id, mb := range m.members {
		ids = append(ids, id)
		if mb.rec.GetState() == v1.NodeState_NODE_STATE_READY {
			ready = append(ready, id)
		}
	}
	m.mu.Unlock()
	sort.Strings(ready)
	if issuer && handOff && len(ids) > 0 {
		var errs []string
		handed := false
		for _, id := range ready {
			if _, err := m.handOff(ctx, id); err != nil {
				errs = append(errs, err.Error())
				continue
			}
			handed = true
			break
		}
		if !handed {
			return nil, fmt.Errorf("%w: this node is the issuer and no member took the role, so leaving would strand the mesh without an authority key; reset the mesh to leave regardless: %s", ErrMesh, strings.Join(errs, "; "))
		}
	}
	if m.Formations != nil {
		m.Formations.Clear(ctx)
	}
	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Go(func() {
			cl, err := m.peer(id)
			if err == nil {
				cctx, cancel := context.WithTimeout(ctx, callTimeout)
				defer cancel()
				_, err = cl.Bye(cctx, connect.NewRequest(&v1.ByeRequest{}))
			}
			if err != nil {
				m.Log.Warn("leave: member not told, it learns from gossip as others revoke this node", "node", id, "err", err)
			}
		})
	}
	wg.Wait()
	m.stopLoops()
	m.mu.Lock()
	m.mesh, m.creds = nil, nil
	m.mu.Unlock()
	if err := m.clearState(ctx); err != nil {
		return nil, err
	}
	// Copies a late sync brought go too
	if m.Formations != nil {
		m.Formations.Clear(ctx)
	}
	m.Events.Publish(v1.EventKind_EVENT_KIND_NODE, v1.EventAction_EVENT_ACTION_DELETED, m.identity.ID, &v1.Node{Id: m.identity.ID, Self: true})
	m.Log.Info("mesh left", "mesh", out.GetId(), "name", out.GetName())
	m.publishStatus()
	return out, nil
}

// Forgets members, sessions, links, revocations, imports and admissions everywhere
func (m *Manager) clearState(ctx context.Context) error {
	m.rows.Lock()
	m.mu.Lock()
	members := m.members
	m.members = map[string]*member{}
	m.sessions = map[string]*session{}
	m.accept = map[string]string{}
	m.links = map[string]*v1.Link{}
	m.admissions = map[string]*v1.Admission{}
	m.grants = map[string]string{}
	m.revoked = map[string]*v1.Revocation{}
	m.mu.Unlock()
	err := m.DB.DeleteMesh(ctx)
	m.rows.Unlock()
	m.dropTransports("")
	for id, mb := range members {
		m.dropImports(ctx, id)
		m.Events.Publish(v1.EventKind_EVENT_KIND_NODE, v1.EventAction_EVENT_ACTION_DELETED, id, mb.rec)
	}
	return err
}

// Leaves any mesh without handing the issuer role on, and clears every trace of it
func (m *Manager) Reset(ctx context.Context) (*v1.Mesh, error) {
	m.changing.Lock()
	defer m.changing.Unlock()
	if m.Joined() {
		return m.leave(ctx, false)
	}
	if m.Formations != nil {
		m.Formations.Clear(ctx)
	}
	if err := m.clearState(ctx); err != nil {
		return nil, err
	}
	m.Log.Info("mesh state reset")
	m.publishStatus()
	return nil, nil
}

// Revokes a member's certificate, the revocation reaching every member in gossip so each drops it
func (m *Manager) Forget(ctx context.Context, ref string) (*v1.Node, error) {
	m.changing.Lock()
	defer m.changing.Unlock()
	if !m.Joined() {
		return nil, ErrNoMesh
	}
	rec, err := m.Node(ref)
	if err != nil {
		return nil, err
	}
	if rec.GetSelf() {
		return nil, fmt.Errorf("%w: this node cannot forget itself, leave the mesh instead", ErrMesh)
	}
	if rec.GetIssuer() {
		return nil, fmt.Errorf("%w: %s holds the issuer role, hand it to another member before forgetting it", ErrMesh, rec.GetName())
	}
	if err := m.revokeMember(rec.GetId()); err != nil {
		return nil, err
	}
	m.Log.Info("mesh member forgotten", "node", rec.GetId(), "name", rec.GetName())
	return rec, nil
}
