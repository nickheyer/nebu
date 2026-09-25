package mesh

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"time"
)

const (
	caLifetime   = 10 * 365 * 24 * time.Hour
	certLifetime = 10 * 365 * 24 * time.Hour
)

// Makes the mesh certificate authority
func newCA(name string) (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return nil, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "nebu mesh " + name, Organization: []string{"nebu"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(caLifetime),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), nil
}

// Issues a member its certificate for its Ed25519 identity key, signed by the mesh authority
func issue(caPEM, caKeyPEM []byte, pub ed25519.PublicKey, nodeID, name string) ([]byte, error) {
	ca, err := parseCert(caPEM)
	if err != nil {
		return nil, fmt.Errorf("mesh certificate authority: %w", err)
	}
	keyBlock, _ := pem.Decode(caKeyPEM)
	if keyBlock == nil {
		return nil, errors.New("mesh certificate authority key is not PEM")
	}
	caKey, err := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
	if err != nil {
		return nil, fmt.Errorf("mesh certificate authority key: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: nodeID, Organization: []string{"nebu"}, OrganizationalUnit: []string{name}},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(certLifetime),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		DNSNames:     []string{nodeID},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, pub, caKey)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), nil
}

func parseCert(certPEM []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return nil, errors.New("not PEM")
	}
	return x509.ParseCertificate(block.Bytes)
}

// The sha256 of a certificate's DER bytes, in hex
func fingerprintOf(der []byte) string {
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:])
}

// The server configuration the mesh listener serves when the mesh has a certificate authority and
// this node holds its certificate, nil otherwise
func (m *Manager) meshServerTLSLocked() *tls.Config {
	if m.mesh == nil || !m.mesh.TLS || len(m.mesh.Certificate) == 0 {
		return nil
	}
	block, _ := pem.Decode(m.mesh.Certificate)
	if block == nil {
		return nil
	}
	cert := tls.Certificate{Certificate: [][]byte{block.Bytes}, PrivateKey: ed25519.PrivateKey(m.identity.PrivateKey)}
	if leaf, err := x509.ParseCertificate(block.Bytes); err == nil {
		cert.Leaf = leaf
	}
	return &tls.Config{Certificates: []tls.Certificate{cert}, NextProtos: []string{"h2", "http/1.1"}, MinVersion: tls.VersionTLS12}
}

// The configuration the mesh listener serves: the API listener's certificate covers node
// traffic when it is configured, else the mesh certificate, else plain
func (m *Manager) serverTLSLocked() *tls.Config {
	if m.APITLS != nil {
		return m.APITLS
	}
	return m.meshServerTLSLocked()
}

// The fingerprint of the certificate node traffic is served with, and whether there is one
func (m *Manager) servedCertLocked() (string, bool) {
	cfg := m.serverTLSLocked()
	if cfg == nil || len(cfg.Certificates) == 0 || len(cfg.Certificates[0].Certificate) == 0 {
		return "", false
	}
	return fingerprintOf(cfg.Certificates[0].Certificate[0]), true
}

// The pool holding the mesh certificate authority, nil when the mesh has none
func (m *Manager) caPoolLocked() *x509.CertPool {
	if m.mesh == nil || len(m.mesh.CACertificate) == 0 {
		return nil
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(m.mesh.CACertificate) {
		return nil
	}
	return pool
}

// The configuration to dial a member with TLS: its certificate must chain to the mesh authority
// or match the fingerprint it advertises. Members are dialed by address, so no host name is checked.
func (m *Manager) dialTLSLocked(fingerprint string) *tls.Config {
	pool := m.caPoolLocked()
	return &tls.Config{
		MinVersion:         tls.VersionTLS12,
		NextProtos:         []string{"h2"},
		InsecureSkipVerify: true,
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return errors.New("the member presented no certificate")
			}
			leaf := cs.PeerCertificates[0]
			if fingerprint != "" && fingerprintOf(leaf.Raw) == fingerprint {
				return nil
			}
			if pool != nil {
				opts := x509.VerifyOptions{Roots: pool, Intermediates: x509.NewCertPool(), KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageAny}}
				for _, c := range cs.PeerCertificates[1:] {
					opts.Intermediates.AddCert(c)
				}
				if _, err := leaf.Verify(opts); err == nil {
					return nil
				}
			}
			return fmt.Errorf("the member's certificate %s neither matches the fingerprint on record nor chains to the mesh authority", fingerprintOf(leaf.Raw)[:16])
		},
	}
}
