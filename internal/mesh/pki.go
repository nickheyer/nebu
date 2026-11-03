package mesh

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"strings"
	"time"
)

const (
	// Members' certificates last this long
	CertificateLifetime = 90 * 24 * time.Hour
	// A member renews once its certificate expires within this long
	RenewWithin = 30 * 24 * time.Hour
	// Join tokens last this long unless asked otherwise
	DefaultGrantTTL = time.Hour
	// The authority lasts this long, rotation replacing it sooner
	caLifetime = 10 * 365 * 24 * time.Hour
	// Every certificate is backdated this much so clocks that disagree still accept it
	clockSkew = 5 * time.Minute
	// The identity certificate a node answers admissions with lasts this long from the identity's making
	identityLifetime = 100 * 365 * 24 * time.Hour
	uriScheme        = "nebu"
)

// The nebu node URI a member certificate names its node by
func nodeURI(id string) *url.URL {
	return &url.URL{Scheme: uriScheme, Host: "node", Path: "/" + id}
}

// The node a certificate names in its URI, false when it names none
func nodeIDOf(cert *x509.Certificate) (string, bool) {
	for _, u := range cert.URIs {
		if u.Scheme != uriScheme || u.Host != "node" {
			continue
		}
		id := strings.TrimPrefix(u.Path, "/")
		if id == "" || strings.Contains(id, "/") {
			return "", false
		}
		return id, true
	}
	return "", false
}

// A random 128 bit certificate serial
func newSerial() (*big.Int, error) {
	return rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
}

// The serial of a certificate in hex, as revocations name it
func serialOf(cert *x509.Certificate) string {
	return cert.SerialNumber.Text(16)
}

// The sha256 of DER bytes in hex
func fingerprintOf(der []byte) string {
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:])
}

// Makes the mesh certificate authority with an Ed25519 key, returning its DER and key
func newCA(name string, now time.Time) ([]byte, ed25519.PrivateKey, error) {
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	serial, err := newSerial()
	if err != nil {
		return nil, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "nebu mesh " + name, Organization: []string{"nebu"}},
		NotBefore:             now.Add(-clockSkew),
		NotAfter:              now.Add(caLifetime),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, key)
	if err != nil {
		return nil, nil, err
	}
	return der, key, nil
}

// A PKCS 10 request in DER for a key, naming the node by URI
func newCSR(key ed25519.PrivateKey, nodeID string) ([]byte, error) {
	tmpl := &x509.CertificateRequest{Subject: pkix.Name{CommonName: nodeID, Organization: []string{"nebu"}}, URIs: []*url.URL{nodeURI(nodeID)}}
	return x509.CreateCertificateRequest(rand.Reader, tmpl, key)
}

// Checks a request's signature and that it names the node wanted with an Ed25519 key
func parseCSR(der []byte, want string) (ed25519.PublicKey, error) {
	csr, err := x509.ParseCertificateRequest(der)
	if err != nil {
		return nil, fmt.Errorf("%w: the certificate request is not readable: %v", ErrMesh, err)
	}
	if err := csr.CheckSignature(); err != nil {
		return nil, fmt.Errorf("%w: the certificate request is not signed by its key: %v", ErrMesh, err)
	}
	pub, ok := csr.PublicKey.(ed25519.PublicKey)
	if !ok {
		return nil, fmt.Errorf("%w: the certificate request carries no Ed25519 key", ErrMesh)
	}
	if len(csr.URIs) != 1 || csr.URIs[0].String() != nodeURI(want).String() {
		return nil, fmt.Errorf("%w: the certificate request names another node than %s", ErrMesh, want)
	}
	return pub, nil
}

// Signs a node certificate for a key under the authority, naming the node by URI for 90 days
func signNode(ca *x509.Certificate, caKey ed25519.PrivateKey, pub ed25519.PublicKey, nodeID string, now time.Time) ([]byte, *x509.Certificate, error) {
	serial, err := newSerial()
	if err != nil {
		return nil, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: nodeID, Organization: []string{"nebu"}},
		NotBefore:    now.Add(-clockSkew),
		NotAfter:     now.Add(CertificateLifetime),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		URIs:         []*url.URL{nodeURI(nodeID)},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, pub, caKey)
	if err != nil {
		return nil, nil, err
	}
	cert, err := x509.ParseCertificate(der)
	return der, cert, err
}

// The self signed certificate admissions run on, the same bytes on every start so its fingerprint stays put
func identityCertificate(nodeID string, key ed25519.PrivateKey, made time.Time) (tls.Certificate, error) {
	sum := sha256.Sum256([]byte("nebu-identity:" + nodeID))
	made = made.UTC().Truncate(time.Second)
	tmpl := &x509.Certificate{
		SerialNumber: new(big.Int).SetBytes(sum[:16]),
		Subject:      pkix.Name{CommonName: nodeID, Organization: []string{"nebu"}},
		NotBefore:    made.Add(-clockSkew),
		NotAfter:     made.Add(identityLifetime),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		URIs:         []*url.URL{nodeURI(nodeID)},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	if err != nil {
		return tls.Certificate{}, err
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, nil
}

// Proves an identity certificate is signed by its own Ed25519 key, current, and names one node
func verifyIdentity(cert *x509.Certificate, now time.Time) (string, error) {
	if _, ok := cert.PublicKey.(ed25519.PublicKey); !ok {
		return "", errors.New("the identity certificate carries no Ed25519 key")
	}
	if err := cert.CheckSignature(cert.SignatureAlgorithm, cert.RawTBSCertificate, cert.Signature); err != nil {
		return "", fmt.Errorf("the identity certificate is not signed by its own key: %w", err)
	}
	if now.Before(cert.NotBefore) || now.After(cert.NotAfter) {
		return "", fmt.Errorf("the identity certificate is valid from %s to %s", cert.NotBefore.Format(time.RFC3339), cert.NotAfter.Format(time.RFC3339))
	}
	id, ok := nodeIDOf(cert)
	if !ok {
		return "", errors.New("the identity certificate names no node")
	}
	return id, nil
}

// Verifies a presented chain against the authority for a usage, returning the node it names
func verifyChain(chain []*x509.Certificate, pool *x509.CertPool, usage x509.ExtKeyUsage, now time.Time) (string, error) {
	if len(chain) == 0 {
		return "", errors.New("no certificate was presented")
	}
	opts := x509.VerifyOptions{Roots: pool, Intermediates: x509.NewCertPool(), CurrentTime: now, KeyUsages: []x509.ExtKeyUsage{usage}}
	for _, c := range chain[1:] {
		opts.Intermediates.AddCert(c)
	}
	if _, err := chain[0].Verify(opts); err != nil {
		return "", fmt.Errorf("the certificate does not chain to the mesh authority: %w", err)
	}
	id, ok := nodeIDOf(chain[0])
	if !ok {
		return "", errors.New("the certificate names no node")
	}
	return id, nil
}

// The authority, this node's key and certificate, and the authority key held by the issuer alone
type credentials struct {
	ca      *x509.Certificate
	pool    *x509.CertPool
	caKey   ed25519.PrivateKey
	key     ed25519.PrivateKey
	leaf    *x509.Certificate
	tlsCert tls.Certificate
}

// Builds credentials from the authority, this node's certificate and key, and the authority key when held
func newCredentials(caDER, certDER []byte, key, caKey ed25519.PrivateKey) (*credentials, error) {
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		return nil, fmt.Errorf("mesh authority: %w", err)
	}
	if !ca.IsCA {
		return nil, errors.New("the mesh authority certificate is no authority")
	}
	leaf, err := x509.ParseCertificate(certDER)
	if err != nil {
		return nil, fmt.Errorf("mesh certificate: %w", err)
	}
	if err := leaf.CheckSignatureFrom(ca); err != nil {
		return nil, fmt.Errorf("the mesh certificate is not signed by the authority: %w", err)
	}
	pub, ok := leaf.PublicKey.(ed25519.PublicKey)
	if !ok || !pub.Equal(key.Public()) {
		return nil, errors.New("the mesh certificate is not for this node's key")
	}
	if caKey != nil {
		if caPub, ok := ca.PublicKey.(ed25519.PublicKey); !ok || !caPub.Equal(caKey.Public()) {
			return nil, errors.New("the authority key does not match the authority certificate")
		}
	}
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	return &credentials{ca: ca, pool: pool, caKey: caKey, key: key, leaf: leaf, tlsCert: tls.Certificate{Certificate: [][]byte{certDER, caDER}, PrivateKey: key, Leaf: leaf}}, nil
}

// The authority's fingerprint, the pin join tokens carry
func (c *credentials) caFingerprint() string { return fingerprintOf(c.ca.Raw) }

// Whether a certificate was signed by this authority, by its key identifier and signature
func (c *credentials) issued(cert *x509.Certificate) bool {
	return string(cert.AuthorityKeyId) == string(c.ca.SubjectKeyId) && cert.CheckSignatureFrom(c.ca) == nil
}

// The same credentials with a new certificate and key under the same authority
func (c *credentials) with(certDER []byte, key ed25519.PrivateKey) (*credentials, error) {
	return newCredentials(c.ca.Raw, certDER, key, c.caKey)
}

// Encodes an Ed25519 key as PKCS 8 DER
func marshalKey(key ed25519.PrivateKey) ([]byte, error) {
	return x509.MarshalPKCS8PrivateKey(key)
}

// Decodes a PKCS 8 DER Ed25519 key
func parseKey(der []byte) (ed25519.PrivateKey, error) {
	k, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return nil, err
	}
	key, ok := k.(ed25519.PrivateKey)
	if !ok {
		return nil, errors.New("the key is not Ed25519")
	}
	return key, nil
}

// A fresh Ed25519 key
func newKey() (ed25519.PrivateKey, error) {
	_, key, err := ed25519.GenerateKey(rand.Reader)
	return key, err
}
