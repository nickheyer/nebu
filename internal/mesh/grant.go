package mesh

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"
)

const tokenPrefix = "nebu-mesh-"

// The mesh, issuer, issuer address, expiry and single use nonce a join grant names
type grant struct {
	Nonce    string
	MeshHash string
	Issuer   string
	Address  string
	Expires  time.Time
}

// The nebu grant URI carrying the claims, the nonce as path and the rest as query
func (g grant) uri() *url.URL {
	q := url.Values{"mesh": {g.MeshHash}, "issuer": {g.Issuer}, "address": {g.Address}}
	return &url.URL{Scheme: uriScheme, Host: "grant", Path: "/" + g.Nonce, RawQuery: q.Encode()}
}

// The claims a grant certificate carries, refused when any is missing
func grantOf(cert *x509.Certificate) (grant, error) {
	for _, u := range cert.URIs {
		if u.Scheme != uriScheme || u.Host != "grant" {
			continue
		}
		q := u.Query()
		g := grant{Nonce: strings.TrimPrefix(u.Path, "/"), MeshHash: q.Get("mesh"), Issuer: q.Get("issuer"), Address: q.Get("address"), Expires: cert.NotAfter}
		if g.Nonce == "" || g.MeshHash == "" || g.Issuer == "" || g.Address == "" {
			return grant{}, fmt.Errorf("%w: the join token leaves its mesh, issuer, address, or nonce out", ErrMesh)
		}
		return g, nil
	}
	return grant{}, fmt.Errorf("%w: the certificate carries no join grant", ErrMesh)
}

// The token as it travels with the grant certificate, its key and the authority fingerprint
type grantToken struct {
	Certificate []byte `json:"c"`
	Key         []byte `json:"k"`
	CA          string `json:"ca"`
}

// A decoded join token with the client certificate it presents, its claims and the authority pin
type heldGrant struct {
	grant
	cert tls.Certificate
	ca   string
}

// Signs a grant with the authority key, a certificate for a fresh key presented to the issuer as a client
func (c *credentials) mintGrant(g grant, now time.Time) (string, error) {
	if c.caKey == nil {
		return "", fmt.Errorf("%w: only the issuer signs join tokens", ErrMesh)
	}
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", err
	}
	serial, err := newSerial()
	if err != nil {
		return "", err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "nebu join grant", Organization: []string{"nebu"}},
		NotBefore:    now.Add(-clockSkew),
		NotAfter:     g.Expires,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		URIs:         []*url.URL{g.uri()},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, c.ca, pub, c.caKey)
	if err != nil {
		return "", err
	}
	raw, err := json.Marshal(grantToken{Certificate: der, Key: key.Seed(), CA: c.caFingerprint()})
	if err != nil {
		return "", err
	}
	return tokenPrefix + base64.RawURLEncoding.EncodeToString(raw), nil
}

// Reads a join token into the certificate it presents and the claims it carries
func decodeGrant(s string) (*heldGrant, error) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, tokenPrefix) {
		return nil, fmt.Errorf("%w: a join token starts with %s; the issuer's Mesh page makes one", ErrMesh, tokenPrefix)
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(s, tokenPrefix))
	if err != nil {
		return nil, fmt.Errorf("%w: the join token is not readable: %v", ErrMesh, err)
	}
	var t grantToken
	if err := json.Unmarshal(raw, &t); err != nil {
		return nil, fmt.Errorf("%w: the join token is not readable: %v", ErrMesh, err)
	}
	if len(t.Key) != ed25519.SeedSize {
		return nil, fmt.Errorf("%w: the join token carries no key", ErrMesh)
	}
	if _, err := hex.DecodeString(t.CA); err != nil || len(t.CA) != 64 {
		return nil, fmt.Errorf("%w: the join token carries no authority fingerprint", ErrMesh)
	}
	cert, err := x509.ParseCertificate(t.Certificate)
	if err != nil {
		return nil, fmt.Errorf("%w: the join token's certificate is not readable: %v", ErrMesh, err)
	}
	key := ed25519.NewKeyFromSeed(t.Key)
	if pub, ok := cert.PublicKey.(ed25519.PublicKey); !ok || !pub.Equal(key.Public()) {
		return nil, fmt.Errorf("%w: the join token's key does not match its certificate", ErrMesh)
	}
	g, err := grantOf(cert)
	if err != nil {
		return nil, err
	}
	return &heldGrant{grant: g, cert: tls.Certificate{Certificate: [][]byte{t.Certificate}, PrivateKey: key, Leaf: cert}, ca: t.CA}, nil
}

// Checks a grant's signature, expiry, mesh and issuer, then takes its nonce, refusing one taken before
func (m *Manager) checkGrant(ctx context.Context, cert *x509.Certificate, now time.Time) (grant, error) {
	m.mu.Lock()
	creds, mesh := m.creds, m.mesh
	m.mu.Unlock()
	if mesh == nil || creds == nil {
		return grant{}, ErrNoMesh
	}
	g, err := grantOf(cert)
	if err != nil {
		return grant{}, err
	}
	if err := cert.CheckSignatureFrom(creds.ca); err != nil {
		return grant{}, fmt.Errorf("%w: the join token is not signed by this mesh's authority", ErrMesh)
	}
	if now.After(cert.NotAfter) {
		return grant{}, fmt.Errorf("%w: the join token expired at %s", ErrMesh, cert.NotAfter.Format(time.RFC3339))
	}
	if now.Before(cert.NotBefore) {
		return grant{}, fmt.Errorf("%w: the join token is not valid before %s", ErrMesh, cert.NotBefore.Format(time.RFC3339))
	}
	if g.MeshHash != meshHex(mesh.ID) {
		return grant{}, fmt.Errorf("%w: the join token names another mesh", ErrMesh)
	}
	if g.Issuer != m.identity.ID {
		return grant{}, fmt.Errorf("%w: the join token names issuer %s, not this node", ErrMesh, g.Issuer)
	}
	fresh, err := m.DB.UseGrant(ctx, g.Nonce, cert.NotAfter)
	if err != nil {
		return grant{}, err
	}
	if !fresh {
		return grant{}, fmt.Errorf("%w: the join token was used already; ask the issuer for another", ErrMesh)
	}
	return g, nil
}
