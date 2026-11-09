package mesh

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"time"
)

const (
	// The server name a member dials another with, choosing mutual TLS under the mesh authority
	meshServerName = "mesh.nebu"
	// The server name admissions dial with, choosing the self signed identity certificates nodes outside hold
	admissionServerName = "admission.nebu"
)

// The listener configuration, picking per connection between admissions and mutual TLS under the authority
func (m *Manager) listenerTLS() *tls.Config {
	return &tls.Config{MinVersion: tls.VersionTLS13, GetConfigForClient: m.configForClient}
}

// Admissions get the identity certificate, every other client mutual TLS requiring a certificate from the authority
func (m *Manager) configForClient(hello *tls.ClientHelloInfo) (*tls.Config, error) {
	if hello.ServerName == admissionServerName {
		return m.admissionServerConfig(), nil
	}
	creds := m.currentCreds()
	if creds == nil {
		return nil, ErrNoMesh
	}
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		NextProtos:   []string{"h2", "http/1.1"},
		Certificates: []tls.Certificate{creds.tlsCert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    creds.pool,
		VerifyConnection: func(cs tls.ConnectionState) error {
			return m.admitClient(cs.PeerCertificates[0])
		},
	}, nil
}

// Refuses a client certificate the authority signed when it is revoked or names neither a node nor a grant
func (m *Manager) admitClient(leaf *x509.Certificate) error {
	if _, err := grantOf(leaf); err == nil {
		return nil
	}
	if _, ok := nodeIDOf(leaf); !ok {
		return errors.New("the client certificate names no node")
	}
	if m.isRevoked(serialOf(leaf)) {
		return fmt.Errorf("certificate %s is revoked", serialOf(leaf))
	}
	return nil
}

// Serves the identity certificate and requires the client's own, proven signed by the key it carries
func (m *Manager) admissionServerConfig() *tls.Config {
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		NextProtos:   []string{"h2", "http/1.1"},
		Certificates: []tls.Certificate{m.ident},
		ClientAuth:   tls.RequireAnyClientCert,
		VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
			leaf, err := x509.ParseCertificate(raw[0])
			if err != nil {
				return err
			}
			_, err = verifyIdentity(leaf, time.Now())
			return err
		},
	}
}

// The credentials held now, nil outside a mesh
func (m *Manager) currentCreds() *credentials {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.creds
}

// Mutual TLS to a member, verifying its chain to the authority, its node URI, and its serial against revocations
func (m *Manager) memberDialConfig(nodeID string) *tls.Config {
	return &tls.Config{
		MinVersion: tls.VersionTLS13,
		ServerName: meshServerName,
		NextProtos: []string{"h2"},
		// Members are named by URI, which the standard host name check cannot match, so VerifyConnection checks it
		InsecureSkipVerify: true,
		GetClientCertificate: func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
			creds := m.currentCreds()
			if creds == nil {
				return nil, ErrNoMesh
			}
			return &creds.tlsCert, nil
		},
		VerifyConnection: func(cs tls.ConnectionState) error {
			return m.verifyMember(cs.PeerCertificates, nodeID)
		},
	}
}

// Checks a member's presented chain reaches the authority, names the node dialed, and is not revoked
func (m *Manager) verifyMember(chain []*x509.Certificate, nodeID string) error {
	creds := m.currentCreds()
	if creds == nil {
		return ErrNoMesh
	}
	id, err := verifyChain(chain, creds.pool, x509.ExtKeyUsageServerAuth, time.Now())
	if err != nil {
		return err
	}
	if id != nodeID {
		return fmt.Errorf("the member answered as %s, not %s", id, nodeID)
	}
	if m.isRevoked(serialOf(chain[0])) {
		return fmt.Errorf("%s answered with revoked certificate %s", id, serialOf(chain[0]))
	}
	return nil
}

// Mutual TLS to the issuer a grant names, presenting the grant and pinning the authority it carries
func grantDialConfig(h *heldGrant) *tls.Config {
	return &tls.Config{
		MinVersion:         tls.VersionTLS13,
		ServerName:         meshServerName,
		NextProtos:         []string{"h2"},
		InsecureSkipVerify: true,
		GetClientCertificate: func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
			return &h.cert, nil
		},
		VerifyConnection: func(cs tls.ConnectionState) error {
			pool := x509.NewCertPool()
			for _, c := range cs.PeerCertificates {
				if c.IsCA && fingerprintOf(c.Raw) == h.ca {
					pool.AddCert(c)
				}
			}
			id, err := verifyChain(cs.PeerCertificates, pool, x509.ExtKeyUsageServerAuth, time.Now())
			if err != nil {
				return fmt.Errorf("the issuer's certificate does not reach the authority the token pins: %w", err)
			}
			if id != h.Issuer {
				return fmt.Errorf("the node at %s answered as %s, not the issuer %s", h.Address, id, h.Issuer)
			}
			return nil
		},
	}
}

// What an admission dial learned of the node across, its id and identity fingerprint
type identitySeen struct {
	id          string
	fingerprint string
}

// TLS to a node by identity certificate, checking the expected node and the pin when known
func (m *Manager) admissionDialConfig(expectID, pin string, seen *identitySeen) *tls.Config {
	return &tls.Config{
		MinVersion: tls.VersionTLS13,
		ServerName: admissionServerName,
		NextProtos: []string{"h2"},
		// Identity certificates are self signed, so VerifyConnection proves the signature, the node and the pin
		InsecureSkipVerify: true,
		Certificates:       []tls.Certificate{m.ident},
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return errors.New("the node presented no certificate")
			}
			leaf := cs.PeerCertificates[0]
			id, err := verifyIdentity(leaf, time.Now())
			if err != nil {
				return err
			}
			fingerprint := fingerprintOf(leaf.Raw)
			if expectID != "" && id != expectID {
				return fmt.Errorf("the node answered as %s, not %s", id, expectID)
			}
			if pin != "" && fingerprint != pin {
				return fmt.Errorf("the node's identity certificate %s is not the one on record", fingerprint[:16])
			}
			if seen != nil {
				seen.id, seen.fingerprint = id, fingerprint
			}
			return nil
		},
	}
}
