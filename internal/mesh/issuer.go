package mesh

import (
	"context"
	"crypto/ed25519"
	"crypto/x509"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/nickheyer/nebu/internal/db"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// A single use join token the issuer signs, minted here on the issuer and asked of it elsewhere
func (m *Manager) Token(ctx context.Context, ttl time.Duration) (string, time.Time, error) {
	if ttl <= 0 {
		ttl = DefaultGrantTTL
	}
	m.mu.Lock()
	if m.mesh == nil {
		m.mu.Unlock()
		return "", time.Time{}, ErrNoMesh
	}
	issuer := m.mesh.Issuer
	m.mu.Unlock()
	if issuer == m.identity.ID {
		return m.mint(ttl)
	}
	cl, err := m.peer(issuer)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("%w: the issuer %s signs join tokens and is not reachable: %v", ErrMesh, issuer, err)
	}
	cctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	resp, err := cl.Grant(cctx, connect.NewRequest(&v1.GrantRequest{TtlSeconds: uint32(ttl / time.Second)}))
	if err != nil {
		return "", time.Time{}, err
	}
	return resp.Msg.GetToken(), resp.Msg.GetExpiresAt().AsTime(), nil
}

// Signs a join grant naming this issuer at its mesh address
func (m *Manager) mint(ttl time.Duration) (string, time.Time, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.mesh == nil || m.creds == nil {
		return "", time.Time{}, ErrNoMesh
	}
	address := m.advertiseLocked()
	if address == "" {
		return "", time.Time{}, fmt.Errorf("%w: this node listens on loopback alone, so no other node can join through it. Set mesh.listen or mesh.advertise", ErrMesh)
	}
	now := time.Now()
	nonce, err := randomToken()
	if err != nil {
		return "", time.Time{}, err
	}
	g := grant{Nonce: nonce[:32], MeshHash: meshHex(m.mesh.ID), Issuer: m.identity.ID, Address: address, Expires: now.Add(ttl)}
	token, err := m.creds.mintGrant(g, now)
	return token, g.Expires, err
}

// Signs a joiner's request once its grant checks out, making it a member and answering with what it syncs with
func (m *Manager) enroll(ctx context.Context, c caller, req *v1.EnrollRequest) (*v1.EnrollResponse, error) {
	if c.kind != peerGrant {
		return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("%w: enrollment takes a join grant", ErrMesh))
	}
	node := req.GetNode()
	if node.GetId() == "" || node.GetId() == m.identity.ID {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("%w: enrollment names the joining node", ErrMesh))
	}
	pub, err := parseCSR(req.GetCsr(), node.GetId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	creds := m.currentCreds()
	if creds == nil || creds.caKey == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("%w: this node no longer holds the issuer role, ask the current issuer for a token", ErrMesh))
	}
	if _, err := m.checkGrant(ctx, c.cert, time.Now()); err != nil {
		return nil, connect.NewError(connect.CodePermissionDenied, err)
	}
	der, cert, err := signNode(creds.ca, creds.caKey, pub, node.GetId(), time.Now())
	if err != nil {
		return nil, err
	}
	// A node that held a certificate before, left behind by a reset, has the old one revoked
	if _, known := m.Member(node.GetId()); known {
		if err := m.revokeMember(node.GetId()); err != nil {
			return nil, err
		}
	}
	m.mergeMembers([]*v1.Member{{Id: node.GetId(), Name: node.GetName(), Address: node.GetAddress(), Addresses: node.GetAddresses(), Sequence: node.GetSequence(), Serial: serialOf(cert)}})
	m.mu.Lock()
	if mb, ok := m.members[node.GetId()]; ok {
		mb.rec.Certificate = der
	}
	m.mu.Unlock()
	m.persist(node.GetId())
	m.wakeLoop()
	m.Log.Info("mesh member enrolled", "node", node.GetId(), "name", node.GetName(), "serial", serialOf(cert))
	mesh, err := m.Mesh()
	if err != nil {
		return nil, err
	}
	return &v1.EnrollResponse{Mesh: mesh, CaCertificate: creds.ca.Raw, Certificate: der, Issuer: m.Record(), Members: m.memberList()}, nil
}

// Signs a member's fresh key and revokes the certificate it renews
func (m *Manager) renewFor(c caller, req *v1.RenewRequest) (*v1.RenewResponse, error) {
	if c.kind != peerMember {
		return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("%w: renewal is for members", ErrMesh))
	}
	creds := m.currentCreds()
	if creds == nil || creds.caKey == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("%w: this node is not the issuer", ErrMesh))
	}
	pub, err := parseCSR(req.GetCsr(), c.id)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	der, cert, err := signNode(creds.ca, creds.caKey, pub, c.id, time.Now())
	if err != nil {
		return nil, err
	}
	m.revoke(renewalOf(c.cert, c.id))
	m.Log.Info("mesh certificate renewed", "node", c.id, "serial", serialOf(cert), "replaces", serialOf(c.cert))
	return &v1.RenewResponse{Certificate: der}, nil
}

// Renews this node's certificate once it expires within thirty days, through the issuer or by itself as issuer
func (m *Manager) renewIfDue(ctx context.Context) {
	m.mu.Lock()
	creds := m.creds
	due := creds != nil && time.Until(creds.leaf.NotAfter) < RenewWithin && time.Since(m.renewTried) > renewRetry
	if due {
		m.renewTried = time.Now()
	}
	m.mu.Unlock()
	if !due {
		return
	}
	if err := m.renew(ctx); err != nil {
		m.Log.Warn("mesh certificate renewal failed", "expires", creds.leaf.NotAfter, "err", err)
	}
}

// Takes a certificate for a fresh key from the issuer, or signs one as issuer, revoking the one it replaces
func (m *Manager) renew(ctx context.Context) error {
	m.crediting.Lock()
	defer m.crediting.Unlock()
	m.mu.Lock()
	creds, mesh := m.creds, m.mesh
	m.mu.Unlock()
	if creds == nil || mesh == nil {
		return ErrNoMesh
	}
	issuer := mesh.Issuer
	key, err := newKey()
	if err != nil {
		return err
	}
	var der []byte
	old := creds.leaf
	if creds.caKey != nil {
		if der, _, err = signNode(creds.ca, creds.caKey, key.Public().(ed25519.PublicKey), m.identity.ID, time.Now()); err != nil {
			return err
		}
	} else {
		csr, err := newCSR(key, m.identity.ID)
		if err != nil {
			return err
		}
		cl, err := m.peer(issuer)
		if err != nil {
			return err
		}
		cctx, cancel := context.WithTimeout(ctx, callTimeout)
		defer cancel()
		resp, err := cl.Renew(cctx, connect.NewRequest(&v1.RenewRequest{Csr: csr}))
		if err != nil {
			return err
		}
		der = resp.Msg.GetCertificate()
	}
	next, err := creds.with(der, key)
	if err != nil {
		return err
	}
	if err := m.install(next); err != nil {
		return err
	}
	m.revoke(renewalOf(old, m.identity.ID))
	m.Log.Info("mesh certificate renewed", "serial", serialOf(next.leaf), "expires", next.leaf.NotAfter)
	return nil
}

// Swaps in new credentials, writing them to the mesh row and redialing every member under them
func (m *Manager) install(next *credentials) error {
	keyDER, err := marshalKey(next.key)
	if err != nil {
		return err
	}
	var caKeyDER []byte
	if next.caKey != nil {
		if caKeyDER, err = marshalKey(next.caKey); err != nil {
			return err
		}
	}
	m.rows.Lock()
	m.mu.Lock()
	if m.mesh == nil {
		m.mu.Unlock()
		m.rows.Unlock()
		return ErrNoMesh
	}
	row := *m.mesh
	row.CACertificate, row.CAKey, row.Certificate, row.Key = next.ca.Raw, caKeyDER, next.leaf.Raw, keyDER
	m.mu.Unlock()
	if err := m.DB.PutMesh(context.Background(), &row); err != nil {
		m.rows.Unlock()
		return err
	}
	m.mu.Lock()
	m.mesh, m.creds = &row, next
	m.mu.Unlock()
	m.rows.Unlock()
	m.dropTransports("")
	m.bump()
	m.publishStatus()
	return nil
}

// Answers a member asking for a join token on behalf of a node outside
func (m *Manager) grantFor(c caller, req *v1.GrantRequest) (*v1.GrantResponse, error) {
	if c.kind != peerMember {
		return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("%w: join tokens are for members to hand out", ErrMesh))
	}
	if creds := m.currentCreds(); creds == nil || creds.caKey == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("%w: this node is not the issuer", ErrMesh))
	}
	ttl := time.Duration(req.GetTtlSeconds()) * time.Second
	if ttl <= 0 {
		ttl = DefaultGrantTTL
	}
	token, expires, err := m.mint(ttl)
	if err != nil {
		return nil, err
	}
	return &v1.GrantResponse{Token: token, ExpiresAt: timestamppb.New(expires)}, nil
}

// Takes a certificate under a new authority from the issuer during rotation, for this node's own key
func (m *Manager) reissue(c caller, req *v1.ReissueRequest) (*v1.ReissueResponse, error) {
	m.crediting.Lock()
	defer m.crediting.Unlock()
	m.mu.Lock()
	creds, mesh := m.creds, m.mesh
	m.mu.Unlock()
	if mesh == nil || creds == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, ErrNoMesh)
	}
	if c.kind != peerMember || c.id != mesh.Issuer {
		return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("%w: only the issuer %s reissues certificates", ErrMesh, mesh.Issuer))
	}
	next, err := newCredentials(req.GetCaCertificate(), req.GetCertificate(), creds.key, nil)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if id, ok := nodeIDOf(next.leaf); !ok || id != m.identity.ID {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("%w: the reissued certificate names another node", ErrMesh))
	}
	if err := m.clearRevocations(); err != nil {
		return nil, err
	}
	if err := m.install(next); err != nil {
		return nil, err
	}
	m.Log.Info("mesh certificate reissued under a new authority", "authority", next.caFingerprint()[:16])
	return &v1.ReissueResponse{NodeId: m.identity.ID}, nil
}

// Drops every revocation, the serials they name belonging to an authority no longer held
func (m *Manager) clearRevocations() error {
	m.rows.Lock()
	defer m.rows.Unlock()
	m.mu.Lock()
	m.revoked = map[string]*v1.Revocation{}
	m.mu.Unlock()
	return m.DB.DeleteRevocations(context.Background(), time.Time{})
}

// Takes the issuer role and the authority key from the current issuer
func (m *Manager) takeIssuer(c caller, req *v1.TakeIssuerRequest) (*v1.TakeIssuerResponse, error) {
	m.crediting.Lock()
	defer m.crediting.Unlock()
	m.mu.Lock()
	creds, mesh := m.creds, m.mesh
	m.mu.Unlock()
	if mesh == nil || creds == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, ErrNoMesh)
	}
	if c.kind != peerMember || c.id != mesh.Issuer {
		return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("%w: only the issuer %s hands its role over", ErrMesh, mesh.Issuer))
	}
	if req.GetIssuerEpoch() <= mesh.IssuerEpoch {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("%w: the handoff names epoch %d, this node holds %d", ErrMesh, req.GetIssuerEpoch(), mesh.IssuerEpoch))
	}
	caKey, err := parseKey(req.GetCaKey())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	next, err := newCredentials(creds.ca.Raw, creds.leaf.Raw, creds.key, caKey)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if err := m.setIssuer(m.identity.ID, req.GetIssuerEpoch(), next); err != nil {
		return nil, err
	}
	m.Log.Info("mesh issuer role taken", "from", c.id, "epoch", req.GetIssuerEpoch())
	return &v1.TakeIssuerResponse{}, nil
}

// Follows a newer issuer epoch gossip carries, giving up the authority key when another member holds the role
func (m *Manager) followIssuer(issuer string, epoch uint64) {
	m.crediting.Lock()
	defer m.crediting.Unlock()
	creds := m.currentCreds()
	if creds == nil {
		return
	}
	next := creds
	if issuer != m.identity.ID && creds.caKey != nil {
		var err error
		if next, err = newCredentials(creds.ca.Raw, creds.leaf.Raw, creds.key, nil); err != nil {
			m.Log.Warn("issuer change not taken", "issuer", issuer, "err", err)
			return
		}
	}
	if err := m.setIssuer(issuer, epoch, next); err != nil {
		m.Log.Warn("issuer change not written", "issuer", issuer, "err", err)
	}
}

// Records the issuer and its epoch with the credentials that go with the role here
func (m *Manager) setIssuer(issuer string, epoch uint64, next *credentials) error {
	m.rows.Lock()
	m.mu.Lock()
	if m.mesh == nil || epoch < m.mesh.IssuerEpoch {
		m.mu.Unlock()
		m.rows.Unlock()
		return nil
	}
	row := *m.mesh
	row.Issuer, row.IssuerEpoch, row.CAKey = issuer, epoch, nil
	m.mu.Unlock()
	if next.caKey != nil {
		der, err := marshalKey(next.caKey)
		if err != nil {
			m.rows.Unlock()
			return err
		}
		row.CAKey = der
	}
	if err := m.DB.PutMesh(context.Background(), &row); err != nil {
		m.rows.Unlock()
		return err
	}
	m.mu.Lock()
	m.mesh, m.creds = &row, next
	m.mu.Unlock()
	m.rows.Unlock()
	m.bump()
	m.wakeLoop()
	m.publishStatus()
	return nil
}

// Hands the issuer role and the authority key to a ready member, this node keeping its own certificate
func (m *Manager) HandOff(ctx context.Context, ref string) (*v1.Mesh, error) {
	m.changing.Lock()
	defer m.changing.Unlock()
	return m.handOff(ctx, ref)
}

// Hands the role over while the membership lock is held
func (m *Manager) handOff(ctx context.Context, ref string) (*v1.Mesh, error) {
	m.crediting.Lock()
	defer m.crediting.Unlock()
	m.mu.Lock()
	creds, mesh := m.creds, m.mesh
	m.mu.Unlock()
	if mesh == nil {
		return nil, ErrNoMesh
	}
	if creds.caKey == nil {
		return nil, fmt.Errorf("%w: the issuer %s hands its role over, this node does not hold it", ErrMesh, mesh.Issuer)
	}
	rec, err := m.Node(ref)
	if err != nil {
		return nil, err
	}
	if rec.GetSelf() {
		return nil, fmt.Errorf("%w: this node holds the issuer role already", ErrMesh)
	}
	if rec.GetState() != v1.NodeState_NODE_STATE_READY {
		return nil, fmt.Errorf("%w: %s is %s, the role goes to a ready member", ErrMesh, rec.GetName(), stateWord(rec.GetState()))
	}
	keyDER, err := marshalKey(creds.caKey)
	if err != nil {
		return nil, err
	}
	cl, err := m.peer(rec.GetId())
	if err != nil {
		return nil, err
	}
	epoch := mesh.IssuerEpoch + 1
	cctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	if _, err := cl.TakeIssuer(cctx, connect.NewRequest(&v1.TakeIssuerRequest{CaKey: keyDER, IssuerEpoch: epoch})); err != nil {
		return nil, fmt.Errorf("%w: %s did not take the issuer role: %v", ErrMesh, rec.GetName(), err)
	}
	next, err := newCredentials(creds.ca.Raw, creds.leaf.Raw, creds.key, nil)
	if err != nil {
		return nil, err
	}
	if err := m.setIssuer(rec.GetId(), epoch, next); err != nil {
		return nil, err
	}
	m.Log.Info("mesh issuer role handed over", "to", rec.GetId(), "name", rec.GetName(), "epoch", epoch)
	return m.Mesh()
}

// Makes a new authority and reissues every member's certificate for its own key, the issuer switching last
func (m *Manager) Rotate(ctx context.Context) (*v1.Mesh, []string, []string, error) {
	m.changing.Lock()
	defer m.changing.Unlock()
	m.crediting.Lock()
	defer m.crediting.Unlock()
	m.mu.Lock()
	creds, mesh := m.creds, m.mesh
	type target struct {
		id   string
		cert []byte
	}
	var members []target
	for id, mb := range m.members {
		members = append(members, target{id: id, cert: mb.rec.GetCertificate()})
	}
	m.mu.Unlock()
	if mesh == nil {
		return nil, nil, nil, ErrNoMesh
	}
	if creds.caKey == nil {
		return nil, nil, nil, fmt.Errorf("%w: rotation runs on the issuer %s", ErrMesh, mesh.Issuer)
	}
	now := time.Now()
	caDER, caKey, err := newCA(mesh.Name, now)
	if err != nil {
		return nil, nil, nil, err
	}
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		return nil, nil, nil, err
	}
	var rotated, missed []string
	var lost []*v1.Revocation
	var outcomes sync.Mutex
	var wg sync.WaitGroup
	for _, t := range members {
		wg.Go(func() {
			err := m.reissueMember(ctx, t.id, t.cert, ca, caKey, now)
			outcomes.Lock()
			defer outcomes.Unlock()
			if err != nil {
				m.Log.Warn("rotate: member missed, it must join again", "node", t.id, "err", err)
				missed = append(missed, t.id)
				if old, perr := x509.ParseCertificate(t.cert); perr == nil {
					lost = append(lost, revocationOf(old, t.id))
				}
				return
			}
			rotated = append(rotated, t.id)
		})
	}
	wg.Wait()
	sort.Strings(rotated)
	sort.Strings(missed)
	selfDER, _, err := signNode(ca, caKey, creds.key.Public().(ed25519.PublicKey), m.identity.ID, now)
	if err != nil {
		return nil, nil, nil, err
	}
	next, err := newCredentials(caDER, selfDER, creds.key, caKey)
	if err != nil {
		return nil, nil, nil, err
	}
	if err := m.clearRevocations(); err != nil {
		return nil, nil, nil, err
	}
	if err := m.install(next); err != nil {
		return nil, nil, nil, err
	}
	// Members that missed the rotation hold certificates under the old authority, dropped here and everywhere by revocation
	var unrevoked error
	for _, r := range lost {
		if _, err := m.revoke(r); err != nil {
			unrevoked = errors.Join(unrevoked, err)
		}
	}
	m.Log.Info("mesh authority rotated", "rotated", len(rotated), "missed", len(missed), "authority", next.caFingerprint()[:16])
	out, err := m.Mesh()
	return out, rotated, missed, errors.Join(err, unrevoked)
}

// Signs one member's key under the new authority and hands it the certificate over the old one
func (m *Manager) reissueOne(ctx context.Context, id string, current []byte, ca *x509.Certificate, caKey ed25519.PrivateKey, now time.Time) error {
	old, err := x509.ParseCertificate(current)
	if err != nil {
		return fmt.Errorf("no certificate on record: %w", err)
	}
	pub, ok := old.PublicKey.(ed25519.PublicKey)
	if !ok {
		return fmt.Errorf("the certificate on record carries no Ed25519 key")
	}
	der, _, err := signNode(ca, caKey, pub, id, now)
	if err != nil {
		return err
	}
	cl, err := m.peer(id)
	if err != nil {
		return err
	}
	cctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	_, err = cl.Reissue(cctx, connect.NewRequest(&v1.ReissueRequest{CaCertificate: ca.Raw, Certificate: der}))
	return err
}

// The mesh row a fresh membership writes
func newMeshRow(id, name, issuer string, epoch uint64, creds *credentials, created time.Time) (*db.MeshRow, error) {
	keyDER, err := marshalKey(creds.key)
	if err != nil {
		return nil, err
	}
	row := &db.MeshRow{ID: id, Name: name, CACertificate: creds.ca.Raw, Certificate: creds.leaf.Raw, Key: keyDER, Issuer: issuer, IssuerEpoch: epoch, CreatedAt: created, JoinedAt: time.Now()}
	if creds.caKey != nil {
		if row.CAKey, err = marshalKey(creds.caKey); err != nil {
			return nil, err
		}
	}
	return row, nil
}
