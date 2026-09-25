package mesh

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/nickheyer/nebu/internal/db"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// Makes a mesh on this node and returns it with a join token
func (m *Manager) Init(ctx context.Context, name string, useTLS bool) (*v1.Mesh, string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "mesh"
	}
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
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, "", err
	}
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return nil, "", err
	}
	now := time.Now()
	row := &db.MeshRow{ID: hex.EncodeToString(raw), Name: name, Secret: secret, TLS: useTLS, CreatedAt: now, JoinedAt: now}
	if useTLS {
		ca, key, err := newCA(name)
		if err != nil {
			return nil, "", err
		}
		cert, err := issue(ca, key, m.identity.PublicKey, m.identity.ID, m.name())
		if err != nil {
			return nil, "", err
		}
		row.CACertificate, row.CAKey, row.Certificate = ca, key, cert
	}
	if err := m.DB.PutMesh(ctx, row); err != nil {
		return nil, "", err
	}
	m.mu.Lock()
	m.mesh = row
	m.mu.Unlock()
	if useTLS && m.APITLS == nil {
		if err := m.rebind(); err != nil {
			return nil, "", err
		}
	}
	m.Log.Info("mesh made", "mesh", row.ID, "name", name, "tls", useTLS)
	m.clearAdmissions()
	m.startLoops()
	m.bump()
	m.publishStatus()
	token, err := m.Token()
	if err != nil {
		return nil, "", err
	}
	mesh, _ := m.Mesh()
	return mesh, token, nil
}

// A join token naming this node as the member to contact
func (m *Manager) Token() (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.mesh == nil {
		return "", ErrNoMesh
	}
	address := m.advertiseLocked()
	if address == "" {
		return "", fmt.Errorf("%w: this node listens on loopback alone, so no other node can join through it. Set mesh.listen or mesh.advertise", ErrMesh)
	}
	fingerprint, hasTLS := m.servedCertLocked()
	return encodeToken(joinToken{ID: m.mesh.ID, Name: m.mesh.Name, Secret: m.mesh.Secret, Address: address, TLS: hasTLS, Fingerprint: fingerprint}), nil
}

// Joins the mesh a token names through the member it names
func (m *Manager) Join(ctx context.Context, token string) (*v1.Mesh, *v1.Node, []string, error) {
	t, err := decodeToken(token)
	if err != nil {
		return nil, nil, nil, err
	}
	m.mu.Lock()
	if m.mesh != nil {
		have := m.mesh.Name
		m.mu.Unlock()
		return nil, nil, nil, fmt.Errorf("%w: this node already belongs to %s and must leave it first", ErrMesh, have)
	}
	m.mu.Unlock()
	if err := m.listen(); err != nil {
		return nil, nil, nil, err
	}
	if err := m.requireReachable(); err != nil {
		return nil, nil, nil, err
	}
	// The mesh is on record as soon as the member proves the secret, so its first sync, which
	// follows its admission at once, finds this node a member; a failed second call takes it back.
	provisional := &db.MeshRow{ID: t.ID, Name: t.Name, Secret: t.Secret, CreatedAt: time.Now(), JoinedAt: time.Now()}
	verified := func() error {
		if err := m.DB.PutMesh(ctx, provisional); err != nil {
			return err
		}
		m.mu.Lock()
		m.mesh = provisional
		m.mu.Unlock()
		return nil
	}
	resp, err := m.handshake(ctx, t.Address, t.TLS, t.Fingerprint, &t, verified)
	if err != nil {
		m.mu.Lock()
		joined := m.mesh == provisional
		if joined {
			m.mesh = nil
		}
		m.mu.Unlock()
		if joined {
			if derr := m.DB.DeleteMesh(ctx); derr != nil {
				return nil, nil, nil, fmt.Errorf("%w; the provisional membership could not be taken back either: %v", err, derr)
			}
		}
		return nil, nil, nil, err
	}
	created := time.Now()
	if resp.GetMesh().GetCreatedAt() != nil {
		created = resp.GetMesh().GetCreatedAt().AsTime()
	}
	name := resp.GetMesh().GetName()
	if name == "" {
		name = t.Name
	}
	row := &db.MeshRow{
		ID:            t.ID,
		Name:          name,
		Secret:        t.Secret,
		TLS:           resp.GetMesh().GetTls(),
		CACertificate: resp.GetCaCertificate(),
		CAKey:         resp.GetCaKey(),
		Certificate:   resp.GetCertificate(),
		CreatedAt:     created,
		JoinedAt:      time.Now(),
	}
	if row.TLS && len(row.Certificate) == 0 {
		return nil, nil, nil, fmt.Errorf("%w: the mesh has TLS but %s issued no certificate", ErrMesh, t.Address)
	}
	if err := m.DB.PutMesh(ctx, row); err != nil {
		return nil, nil, nil, err
	}
	m.mu.Lock()
	m.mesh = row
	m.mu.Unlock()
	bootstrap := resp.GetNode()
	m.absorb(bootstrap, resp.GetMembers(), true)
	var warnings []string
	if row.TLS && m.APITLS == nil {
		if err := m.rebind(); err != nil {
			return nil, nil, nil, err
		}
	}
	if !bootstrap.GetTls() {
		warnings = append(warnings, fmt.Sprintf("node traffic with %s is plain HTTP; make meshes with TLS on, or set tls.cert_file on every member", bootstrap.GetName()))
	}
	m.mu.Lock()
	_, served := m.servedCertLocked()
	m.mu.Unlock()
	if !served {
		warnings = append(warnings, "this node serves node traffic over plain HTTP")
	}
	m.Log.Info("mesh joined", "mesh", row.ID, "name", row.Name, "through", t.Address, "tls", row.TLS)
	m.settleAfterJoin(row.ID)
	m.startLoops()
	m.bump()
	m.publishStatus()
	go func() {
		select {
		case <-m.base.Done():
		case <-time.After(SyncInterval):
			if _, err := m.Probe(m.base, "", false); err != nil {
				m.Log.Warn("link probe after join failed", "err", err)
			}
		}
	}()
	mesh, _ := m.Mesh()
	return mesh, bootstrap, warnings, nil
}

// Leaves the mesh, telling every member, and forgets it
func (m *Manager) Leave(ctx context.Context) (*v1.Mesh, error) {
	m.mu.Lock()
	if m.mesh == nil {
		m.mu.Unlock()
		return nil, ErrNoMesh
	}
	out := m.meshLocked()
	wasTLS := m.mesh.TLS
	ids := make([]string, 0, len(m.members))
	for id := range m.members {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			cl, err := m.Client(ctx, id)
			if err != nil {
				m.Log.Warn("leave: member not told, it learns from the missed syncs", "node", id, "err", err)
				return
			}
			cctx, cancel := context.WithTimeout(ctx, callTimeout)
			defer cancel()
			if _, err := cl.Mesh.Bye(cctx, connect.NewRequest(&v1.ByeRequest{NodeId: m.identity.ID})); err != nil {
				m.Log.Warn("leave: member not told, it learns from the missed syncs", "node", id, "err", err)
			}
		}(id)
	}
	wg.Wait()
	m.mu.Lock()
	members := m.members
	m.members = map[string]*member{}
	m.sessions = map[string]*session{}
	m.accept = map[string]string{}
	m.links = map[string]*v1.Link{}
	m.admissions = map[string]*v1.Admission{}
	m.mesh = nil
	m.running = false
	m.mu.Unlock()
	if err := m.DB.DeleteMesh(ctx); err != nil {
		return nil, err
	}
	for id, mb := range members {
		m.Events.Publish(v1.EventKind_EVENT_KIND_NODE, v1.EventAction_EVENT_ACTION_DELETED, id, mb.rec)
		if m.Formations != nil {
			m.Formations.MemberState(id, v1.NodeState_NODE_STATE_GONE)
		}
	}
	m.Events.Publish(v1.EventKind_EVENT_KIND_NODE, v1.EventAction_EVENT_ACTION_DELETED, m.identity.ID, &v1.Node{Id: m.identity.ID, Self: true})
	if wasTLS && m.APITLS == nil {
		if err := m.rebind(); err != nil {
			return nil, err
		}
	}
	m.Log.Info("mesh left", "mesh", out.GetId(), "name", out.GetName())
	m.publishStatus()
	return out, nil
}

// Issues a new secret to every reachable member. Members unreachable now must join again.
func (m *Manager) Rotate(ctx context.Context) (*v1.Mesh, []string, []string, error) {
	m.mu.Lock()
	if m.mesh == nil {
		m.mu.Unlock()
		return nil, nil, nil, ErrNoMesh
	}
	ids := make([]string, 0, len(m.members))
	for id := range m.members {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, nil, nil, err
	}
	var mu sync.Mutex
	var rotated, missed []string
	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			cl, err := m.Client(ctx, id)
			if err == nil {
				cctx, cancel := context.WithTimeout(ctx, callTimeout)
				defer cancel()
				_, err = cl.Mesh.Rekey(cctx, connect.NewRequest(&v1.RekeyRequest{Secret: secret}))
			}
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				m.Log.Warn("rotate: member missed, it must join again", "node", id, "err", err)
				missed = append(missed, id)
				return
			}
			rotated = append(rotated, id)
		}(id)
	}
	wg.Wait()
	if err := m.Rekey(secret); err != nil {
		return nil, nil, nil, err
	}
	m.Log.Info("mesh secret rotated", "rotated", len(rotated), "missed", len(missed))
	m.publishStatus()
	mesh, _ := m.Mesh()
	return mesh, rotated, missed, nil
}
