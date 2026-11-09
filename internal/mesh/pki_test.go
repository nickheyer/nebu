package mesh

import (
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// A client presenting one certificate on the mesh path and checking nothing of the server, the listener's checks alone deciding
func presenting(cert tls.Certificate) *http.Client {
	cfg := &tls.Config{
		MinVersion: tls.VersionTLS13, ServerName: meshServerName, NextProtos: []string{"h2"}, InsecureSkipVerify: true,
		GetClientCertificate: func(*tls.CertificateRequestInfo) (*tls.Certificate, error) { return &cert, nil },
	}
	return &http.Client{Transport: transport(cfg)}
}

// A certificate for a node under an authority with a fresh key, ready to present
func certFor(t *testing.T, ca *x509.Certificate, caKey ed25519.PrivateKey, nodeID string, now time.Time) tls.Certificate {
	t.Helper()
	key, err := newKey()
	if err != nil {
		t.Fatal(err)
	}
	der, leaf, err := signNode(ca, caKey, key.Public().(ed25519.PublicKey), nodeID, now)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}
}

// The listener takes only unrevoked authority certificates and dialers check the node they dialed
func TestHandshakeNeedsTheAuthorityAndTheNode(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := newNode(t, ctx, time.Hour)
	if _, _, err := a.Init(ctx, "pki"); err != nil {
		t.Fatal(err)
	}
	creds := a.currentCreds()
	url := "https://" + a.Address() + "/test/peer"
	member := certFor(t, creds.ca, creds.caKey, "member-x", time.Now())
	client := presenting(member)
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("a certificate from the mesh authority is taken: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "member-x|" {
		t.Fatalf("the member is named by its certificate: %d %q", resp.StatusCode, body)
	}
	foreignDER, foreignKey, err := newCA("other", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	foreign, _ := x509.ParseCertificate(foreignDER)
	if resp, err := presenting(certFor(t, foreign, foreignKey, "member-x", time.Now())).Get(url); err == nil {
		resp.Body.Close()
		t.Fatal("a certificate from another authority is refused at the handshake")
	}
	if resp, err := presenting(a.ident).Get(url); err == nil {
		resp.Body.Close()
		t.Fatal("a self signed identity certificate is refused on the mesh path")
	}
	expired := certFor(t, creds.ca, creds.caKey, "member-y", time.Now().Add(-CertificateLifetime-time.Hour))
	if resp, err := presenting(expired).Get(url); err == nil {
		resp.Body.Close()
		t.Fatal("an expired certificate is refused at the handshake")
	}
	// A revoked serial is refused on the next handshake and on the next request of a kept connection
	a.revoke(revocationOf(member.Leaf, "member-x"))
	resp, err = client.Get(url)
	if err == nil {
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("a kept connection with a revoked certificate answered %d", resp.StatusCode)
		}
	}
	if resp, err := presenting(member).Get(url); err == nil {
		resp.Body.Close()
		t.Fatal("a revoked certificate is refused at the handshake")
	}
	// A dialer refuses a member answering as another node than the one dialed, and one answering with a revoked serial
	wrong := &http.Client{Transport: transport(a.memberDialConfig("someone-else"))}
	if resp, err := wrong.Get(url); err == nil || !strings.Contains(err.Error(), "answered as") {
		if err == nil {
			resp.Body.Close()
		}
		t.Fatalf("a dial to the wrong node is refused: %v", err)
	}
	right := &http.Client{Transport: transport(a.memberDialConfig(a.Self()))}
	resp, err = right.Get(url)
	if err != nil {
		t.Fatalf("a dial to the right node is taken: %v", err)
	}
	resp.Body.Close()
	a.revoke(revocationOf(creds.leaf, a.Self()))
	again := &http.Client{Transport: transport(a.memberDialConfig(a.Self()))}
	if resp, err := again.Get(url); err == nil || !strings.Contains(err.Error(), "revoked") {
		if err == nil {
			resp.Body.Close()
		}
		t.Fatalf("a server answering with a revoked serial is refused: %v", err)
	}
}

// The issuer refuses a grant past its expiry, one taken before, one for another issuer, and one from another authority
func TestGrantExpiryAndNonceReuse(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := newNode(t, ctx, time.Hour)
	b := newNode(t, ctx, time.Hour)
	c := newNode(t, ctx, time.Hour)
	if _, _, err := a.Init(ctx, "grants"); err != nil {
		t.Fatal(err)
	}
	creds := a.currentCreds()
	mesh, _ := a.Mesh()
	claims := func(nonce, issuer string, expires time.Time) grant {
		return grant{Nonce: nonce, MeshHash: meshHex(mesh.GetId()), Issuer: issuer, Address: a.Address(), Expires: expires}
	}
	held := func(token string) *x509.Certificate {
		h, err := decodeGrant(token)
		if err != nil {
			t.Fatal(err)
		}
		return h.cert.Leaf
	}
	now := time.Now()
	expiredToken, err := creds.mintGrant(claims("n-expired", a.Self(), now.Add(-time.Minute)), now.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.checkGrant(ctx, held(expiredToken), now); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("an expired grant is refused: %v", err)
	}
	fresh, err := creds.mintGrant(claims("n-fresh", a.Self(), now.Add(DefaultGrantTTL)), now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.checkGrant(ctx, held(fresh), now); err != nil {
		t.Fatalf("a fresh grant is taken: %v", err)
	}
	if _, err := a.checkGrant(ctx, held(fresh), now); err == nil || !strings.Contains(err.Error(), "used already") {
		t.Fatalf("a grant is taken once: %v", err)
	}
	otherIssuer, _ := creds.mintGrant(claims("n-other", "someone-else", now.Add(time.Hour)), now)
	if _, err := a.checkGrant(ctx, held(otherIssuer), now); err == nil || !strings.Contains(err.Error(), "issuer") {
		t.Fatalf("a grant for another issuer is refused: %v", err)
	}
	foreignDER, foreignKey, _ := newCA("other", now)
	foreignCreds := &credentials{caKey: foreignKey}
	foreignCreds.ca, _ = x509.ParseCertificate(foreignDER)
	forged, err := foreignCreds.mintGrant(claims("n-forged", a.Self(), now.Add(time.Hour)), now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.checkGrant(ctx, held(forged), now); err == nil || !strings.Contains(err.Error(), "not signed") {
		t.Fatalf("a grant from another authority is refused: %v", err)
	}
	// Joining with an expired token is refused, a token joins once, and a second node with it is refused
	if _, _, err := b.Join(ctx, expiredToken); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("joining with an expired token: %v", err)
	}
	token, expires, err := a.Token(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if left := time.Until(expires); left < 59*time.Minute || left > time.Hour {
		t.Fatalf("a token lasts an hour by default, %s left", left)
	}
	if _, _, err := b.Join(ctx, token); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.Join(ctx, token); err == nil || !strings.Contains(err.Error(), "used already") {
		t.Fatalf("a token joins once: %v", err)
	}
	if c.Joined() {
		t.Fatal("c did not join")
	}
}

// The issuer signs a joiner's request, renews a member's fresh key within thirty days of expiry, and renews its own
func TestCertificateSigningAndRenewal(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := newNode(t, ctx, 200*time.Millisecond)
	b := newNode(t, ctx, 200*time.Millisecond)
	formMesh(t, ctx, a, b)
	eventually(t, "a and b meet", 10*time.Second, func() bool { return allMet(a, b) })
	issuer := a.currentCreds()
	key, err := newKey()
	if err != nil {
		t.Fatal(err)
	}
	csr, err := newCSR(key, "node-z")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseCSR(csr, "node-other"); err == nil {
		t.Fatal("a request naming another node is refused")
	}
	pub, err := parseCSR(csr, "node-z")
	if err != nil || !pub.Equal(key.Public()) {
		t.Fatalf("the request's key is read: %v", err)
	}
	// b's certificate is moved to within thirty days of expiry, and the next check renews it through the issuer
	nearDER, _, err := signNode(issuer.ca, issuer.caKey, b.currentCreds().key.Public().(ed25519.PublicKey), b.Self(), time.Now().Add(-CertificateLifetime+20*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	near, err := b.currentCreds().with(nearDER, b.currentCreds().key)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.install(near); err != nil {
		t.Fatal(err)
	}
	b.renewIfDue(ctx)
	renewed := b.currentCreds()
	if serialOf(renewed.leaf) == serialOf(near.leaf) || time.Until(renewed.leaf.NotAfter) < CertificateLifetime-time.Hour {
		t.Fatalf("b's certificate is renewed for 90 days: expires %s", renewed.leaf.NotAfter)
	}
	if renewed.key.Equal(near.key) {
		t.Fatal("renewal signs a fresh key")
	}
	if !a.isRevoked(serialOf(near.leaf)) {
		t.Fatal("the issuer revokes the certificate a renewal replaces")
	}
	if !chainsTo(renewed.leaf, issuer.pool) {
		t.Fatal("the renewed certificate chains to the authority")
	}
	// The issuer renews itself without a call
	selfNear, _, err := signNode(issuer.ca, issuer.caKey, issuer.key.Public().(ed25519.PublicKey), a.Self(), time.Now().Add(-CertificateLifetime+10*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	aNear, err := issuer.with(selfNear, issuer.key)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.install(aNear); err != nil {
		t.Fatal(err)
	}
	a.renewIfDue(ctx)
	if got := a.currentCreds(); serialOf(got.leaf) == serialOf(aNear.leaf) || got.caKey == nil || !a.isRevoked(serialOf(aNear.leaf)) {
		t.Fatal("the issuer renews its own certificate and keeps the authority key")
	}
	// The members still sync under the renewed certificates
	before, _ := b.Member(a.Self())
	a.pools.set([]*v1.Pool{testPool(a.Self(), "127.0.0.1:9200", true)}, nil)
	a.Bump()
	eventually(t, "b takes a's record after both renewed", 10*time.Second, func() bool {
		rec, _ := b.Member(a.Self())
		return rec.GetSequence() > before.GetSequence() && len(rec.GetPools()) == 1
	})
}

// Rotation makes a new authority on the issuer, reissues every reachable member, and drops the ones it missed
func TestRotateReissuesEveryMember(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := newNode(t, ctx, 200*time.Millisecond)
	b := newNode(t, ctx, 200*time.Millisecond)
	c := newNode(t, ctx, 200*time.Millisecond)
	d := newNode(t, ctx, 200*time.Millisecond)
	formMesh(t, ctx, a, b, c, d)
	eventually(t, "every member meets every other", 15*time.Second, func() bool { return allMet(a, b, c, d) })
	if _, _, _, err := b.Rotate(ctx); err == nil || !strings.Contains(err.Error(), "issuer") {
		t.Fatalf("rotation runs on the issuer: %v", err)
	}
	oldCA := a.currentCreds().caFingerprint()
	d.Close()
	mesh, rotated, missed, err := a.Rotate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rotated) != 2 || len(missed) != 1 || missed[0] != d.Self() {
		t.Fatalf("rotated %v missed %v", rotated, missed)
	}
	if mesh.GetCaFingerprint() == oldCA {
		t.Fatal("rotation makes a new authority")
	}
	for _, n := range []*testNode{b, c} {
		creds := n.currentCreds()
		if creds.caFingerprint() != mesh.GetCaFingerprint() || creds.caKey != nil {
			t.Fatalf("%s holds the new authority and no authority key", n.Self())
		}
		if !chainsTo(creds.leaf, a.currentCreds().pool) {
			t.Fatalf("%s's certificate chains to the new authority", n.Self())
		}
	}
	if _, still := a.Member(d.Self()); still {
		t.Fatal("the issuer drops the member rotation missed")
	}
	eventually(t, "b and c drop the member rotation missed and sync under the new authority", 15*time.Second, func() bool {
		_, bStill := b.Member(d.Self())
		_, cStill := c.Member(d.Self())
		rec, _ := c.Member(b.Self())
		return !bStill && !cStill && conditionIs(rec, ConditionCertificateValid, v1.ConditionStatus_CONDITION_STATUS_TRUE)
	})
}

// The issuer role and authority key move to another member and joins go through it
func TestHandOffIssuer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := newNode(t, ctx, 200*time.Millisecond)
	b := newNode(t, ctx, 200*time.Millisecond)
	c := newNode(t, ctx, 200*time.Millisecond)
	formMesh(t, ctx, a, b, c)
	eventually(t, "every member meets every other", 15*time.Second, func() bool { return allMet(a, b, c) })
	if _, err := b.HandOff(ctx, c.Self()); err == nil {
		t.Fatal("only the issuer hands its role over")
	}
	mesh, err := a.HandOff(ctx, b.Self())
	if err != nil {
		t.Fatal(err)
	}
	if mesh.GetIssuer() != b.Self() || mesh.GetIssuerEpoch() != 1 {
		t.Fatalf("mesh after the handoff %v", mesh)
	}
	if a.currentCreds().caKey != nil || b.currentCreds().caKey == nil {
		t.Fatal("the authority key moved with the role")
	}
	row, err := a.DB.GetMesh(ctx)
	if err != nil || len(row.CAKey) != 0 || row.Issuer != b.Self() {
		t.Fatalf("a's row holds no authority key: %v", err)
	}
	eventually(t, "c follows the new issuer", 10*time.Second, func() bool {
		m, _ := c.Mesh()
		return m.GetIssuer() == b.Self() && m.GetIssuerEpoch() == 1
	})
	token, _, err := c.Token(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	h, err := decodeGrant(token)
	if err != nil || h.Issuer != b.Self() {
		t.Fatalf("a token asked of a member names the new issuer: %v", err)
	}
	d := newNode(t, ctx, 200*time.Millisecond)
	if _, _, err := d.Join(ctx, token); err != nil {
		t.Fatal(err)
	}
	eventually(t, "d meets everyone", 15*time.Second, func() bool { return allMet(a, b, c, d) })
}

// Whether a record holds a condition of a type at a status
func conditionIs(rec *v1.Node, typ string, status v1.ConditionStatus) bool {
	for _, c := range rec.GetConditions() {
		if c.GetType() == typ {
			return c.GetStatus() == status
		}
	}
	return false
}

// The identity certificate is the same on every start, so pins taken from beacons survive restarts
func TestIdentityCertificateIsStable(t *testing.T) {
	key, err := newKey()
	if err != nil {
		t.Fatal(err)
	}
	made := time.Now()
	one, err := identityCertificate("node-a", key, made)
	if err != nil {
		t.Fatal(err)
	}
	two, err := identityCertificate("node-a", key, made)
	if err != nil {
		t.Fatal(err)
	}
	if fingerprintOf(one.Certificate[0]) != fingerprintOf(two.Certificate[0]) {
		t.Fatal("the identity certificate changed between starts")
	}
	if id, err := verifyIdentity(one.Leaf, time.Now()); err != nil || id != "node-a" {
		t.Fatalf("the identity certificate proves its node: %q %v", id, err)
	}
}

// Rotation reissues every member at once rather than one after another
func TestRotateReissuesConcurrently(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	const pause = 300 * time.Millisecond
	a := newNode(t, ctx, 200*time.Millisecond, func(m *Manager) {
		m.reissueMember = func(ctx context.Context, id string, current []byte, ca *x509.Certificate, caKey ed25519.PrivateKey, now time.Time) error {
			time.Sleep(pause)
			return m.reissueOne(ctx, id, current, ca, caKey, now)
		}
	})
	members := []*testNode{a}
	for range 3 {
		members = append(members, newNode(t, ctx, 200*time.Millisecond))
	}
	formMesh(t, ctx, members...)
	eventually(t, "every member meets every other", 15*time.Second, func() bool { return allMet(members...) })
	start := time.Now()
	_, rotated, missed, err := a.Rotate(ctx)
	took := time.Since(start)
	if err != nil || len(rotated) != 3 || len(missed) != 0 {
		t.Fatalf("rotated %v missed %v err %v", rotated, missed, err)
	}
	if took >= 2*pause {
		t.Fatalf("three reissues took %s, so they ran one after another", took)
	}
	if !sort.StringsAreSorted(rotated) {
		t.Fatalf("rotated members are listed in order: %v", rotated)
	}
}
