package auth

import (
	"crypto/ecdsa"
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
	"io/fs"
	"math/big"
	"net"
	"os"
	"strings"
	"time"
)

const (
	// How long a generated certificate is valid
	certLifetime = 5 * 365 * 24 * time.Hour
	// A generated certificate this close to expiry is replaced at start
	certRenewBefore = 30 * 24 * time.Hour
)

// Loads a self-signed listener pair, generating an ECDSA P-256 one when missing, expired, or not naming every host
func LoadOrCreateCert(certFile, keyFile string, hosts []string, now time.Time) (cert tls.Certificate, created bool, err error) {
	cert, err = tls.LoadX509KeyPair(certFile, keyFile)
	switch {
	case err == nil && covers(cert.Leaf, hosts, now):
		return cert, false, nil
	case err != nil && !errors.Is(err, fs.ErrNotExist):
		return tls.Certificate{}, false, fmt.Errorf("tls certificate %s: %w, delete it and %s to make a new one", certFile, err, keyFile)
	}
	certPEM, keyPEM, err := selfSigned(hosts, now)
	if err != nil {
		return tls.Certificate{}, false, err
	}
	if err := os.WriteFile(keyFile, keyPEM, 0o600); err != nil {
		return tls.Certificate{}, false, fmt.Errorf("tls key: %w", err)
	}
	if err := os.WriteFile(certFile, certPEM, 0o644); err != nil {
		return tls.Certificate{}, false, fmt.Errorf("tls certificate: %w", err)
	}
	cert, err = tls.X509KeyPair(certPEM, keyPEM)
	return cert, err == nil, err
}

// Whether a certificate is valid past the renewal window and names every host
func covers(leaf *x509.Certificate, hosts []string, now time.Time) bool {
	if leaf == nil || now.Before(leaf.NotBefore) || now.Add(certRenewBefore).After(leaf.NotAfter) {
		return false
	}
	for _, h := range hosts {
		if leaf.VerifyHostname(h) != nil {
			return false
		}
	}
	return true
}

// A PEM certificate and PKCS 8 key for the hosts, names as DNS entries and addresses as IP entries
func selfSigned(hosts []string, now time.Time) (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "nebu", Organization: []string{"nebu self-signed"}},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(certLifetime),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	for _, h := range hosts {
		if ip := net.ParseIP(strings.Trim(h, "[]")); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, h)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	pkcs8, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8}), nil
}

// The sha256 of the leaf certificate as colon separated uppercase hex, the form browsers show
func Fingerprint(cert tls.Certificate) string {
	if len(cert.Certificate) == 0 {
		return ""
	}
	sum := sha256.Sum256(cert.Certificate[0])
	pairs := make([]string, len(sum))
	for i, b := range sum {
		pairs[i] = strings.ToUpper(hex.EncodeToString([]byte{b}))
	}
	return strings.Join(pairs, ":")
}

// Names a generated certificate covers, localhost and loopback addresses, the machine's name, and each address's host
func CertHosts(listens ...string) []string {
	hosts := []string{"localhost", "127.0.0.1", "::1"}
	if name, err := os.Hostname(); err == nil && name != "" {
		hosts = append(hosts, name)
	}
	for _, addr := range listens {
		host, _, err := net.SplitHostPort(addr)
		if err != nil {
			host = strings.Trim(addr, "[]")
		}
		if ip := net.ParseIP(host); host == "" || ip != nil && ip.IsUnspecified() {
			continue
		}
		hosts = append(hosts, host)
	}
	seen := map[string]bool{}
	out := hosts[:0]
	for _, h := range hosts {
		if key := strings.ToLower(h); !seen[key] {
			seen[key] = true
			out = append(out, h)
		}
	}
	return out
}
