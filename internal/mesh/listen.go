package mesh

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/nickheyer/nebu/internal/auth"
	"github.com/nickheyer/nebu/internal/mesh/links"
	"github.com/nickheyer/nebu/pkg/proto/nebu/v1/nebuv1connect"
)

const (
	readHeaderTimeout = 10 * time.Second
	// How long a kept alive connection may sit between requests before it is closed
	idleTimeout = 2 * time.Minute
)

// Who a request comes from, as the certificate its connection presented proves
type peerKind int

const (
	// A member holding a certificate the authority signed
	peerMember peerKind = iota + 1
	// A node presenting a join grant to the issuer
	peerGrant
	// A node outside the mesh on the admission path, known by its identity certificate
	peerStranger
	// A node of another mesh importing this node's pools, known by its pinned identity certificate
	peerImporter
)

// The caller of one request and the certificate it proved itself with
type caller struct {
	kind peerKind
	id   string
	cert *x509.Certificate
}

type callerKey struct{}

// The caller a request context carries
func callerOf(ctx context.Context) (caller, bool) {
	c, ok := ctx.Value(callerKey{}).(caller)
	return c, ok
}

// Whether an address binds loopback or nothing routable
func loopbackOnly(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return true
	}
	if host == "" {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Binds the mesh listener on mesh.listen or the default address, always under TLS
func (m *Manager) listen() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.listener != nil {
		return nil
	}
	addr := m.Config.GetListen()
	if addr == "" {
		addr = m.defaultListen
	}
	return m.bindLocked(addr)
}

// Binds one address and serves the mesh handler on it until Close
func (m *Manager) bindLocked(addr string) error {
	if m.closed {
		return errClosed
	}
	raw, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("mesh.listen %s: %w", addr, err)
	}
	ln := tls.NewListener(raw, m.listenerTLS())
	base := m.base
	srv := &http.Server{
		Handler:           m.counted(m.identify(m.routes())),
		ReadHeaderTimeout: readHeaderTimeout,
		IdleTimeout:       idleTimeout,
		Protocols:         links.Protocols(),
		BaseContext:       func(net.Listener) context.Context { return base },
		ErrorLog:          slog.NewLogLogger(m.Log.Handler(), slog.LevelDebug),
	}
	served := make(chan struct{})
	m.listener, m.server, m.served, m.listenAddr = ln, srv, served, raw.Addr().String()
	m.Log.Info("mesh listening", "addr", m.listenAddr)
	if !loopbackOnly(m.listenAddr) {
		m.Log.Warn("the mesh listener takes node traffic beyond loopback, members under mutual TLS, importers of other meshes by pinned certificate, and other nodes on the admission calls alone", "addr", m.listenAddr)
	}
	go func() {
		defer close(served)
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			m.Log.Warn("mesh listener stopped", "addr", addr, "err", err)
		}
	}()
	return nil
}

// The membership service, the bandwidth sink, and the API handler behind them
func (m *Manager) routes() http.Handler {
	mux := http.NewServeMux()
	path, handler := nebuv1connect.NewMeshPeerServiceHandler(&peerService{m: m})
	mux.Handle(path, handler)
	mux.HandleFunc(links.SinkPath, links.Sink)
	mux.Handle("/", m.Handler)
	return mux
}

// Whether an importer of another mesh may reach a path, the export pull and every path outside the node to node services
func importerReaches(path string) bool {
	return path == nebuv1connect.MeshPeerServiceExportsProcedure || !strings.HasPrefix(path, "/nebu.")
}

// Procedures a node outside may call on the admission path
var admissionProcedures = map[string]bool{
	nebuv1connect.MeshPeerServiceKnockProcedure:    true,
	nebuv1connect.MeshPeerServiceOfferProcedure:    true,
	nebuv1connect.MeshPeerServiceWelcomeProcedure:  true,
	nebuv1connect.MeshPeerServiceFederateProcedure: true,
}

// Names the caller by its certificate, rechecks revocation and authority per request, and limits what each kind reaches
func (m *Manager) identify(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
			http.Error(w, "mesh: a client certificate is required", http.StatusUnauthorized)
			return
		}
		c, err := m.callerFor(r.TLS)
		if err != nil {
			http.Error(w, "mesh: "+err.Error(), http.StatusForbidden)
			return
		}
		switch {
		case c.kind == peerStranger && !admissionProcedures[r.URL.Path]:
			http.Error(w, "mesh: a node outside the mesh reaches the admission calls alone", http.StatusForbidden)
			return
		case c.kind == peerImporter && !importerReaches(r.URL.Path):
			http.Error(w, "mesh: an importer of another mesh reaches the export pull and the gateway alone", http.StatusForbidden)
			return
		case c.kind == peerGrant && r.URL.Path != nebuv1connect.MeshPeerServiceEnrollProcedure:
			http.Error(w, "mesh: a join grant reaches enrollment alone", http.StatusForbidden)
			return
		case c.kind == peerMember, c.kind == peerImporter:
			if bearer := auth.Bearer(r.Header); bearer != "" {
				if peer, ok := m.Peer(bearer); ok && peer != c.id {
					http.Error(w, "mesh: the bearer token belongs to another member than the certificate", http.StatusUnauthorized)
					return
				}
			}
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), callerKey{}, c)))
	})
}

// The caller a connection's certificate proves, refusing members whose certificate is revoked or from a replaced authority
func (m *Manager) callerFor(cs *tls.ConnectionState) (caller, error) {
	leaf := cs.PeerCertificates[0]
	if cs.ServerName == admissionServerName {
		id, err := verifyIdentity(leaf, time.Now())
		if err != nil {
			return caller{}, err
		}
		if importer, ok := m.importerByPin(fingerprintOf(leaf.Raw)); ok && importer == id {
			return caller{kind: peerImporter, id: id, cert: leaf}, nil
		}
		return caller{kind: peerStranger, id: id, cert: leaf}, nil
	}
	creds := m.currentCreds()
	if creds == nil {
		return caller{}, ErrNoMesh
	}
	if !bytes.Equal(leaf.AuthorityKeyId, creds.ca.SubjectKeyId) {
		return caller{}, errors.New("the certificate is from an authority this mesh no longer holds")
	}
	if _, err := grantOf(leaf); err == nil {
		return caller{kind: peerGrant, cert: leaf}, nil
	}
	id, ok := nodeIDOf(leaf)
	if !ok {
		return caller{}, errors.New("the certificate names no node")
	}
	if m.isRevoked(serialOf(leaf)) {
		return caller{}, fmt.Errorf("certificate %s is revoked", serialOf(leaf))
	}
	return caller{kind: peerMember, id: id, cert: leaf}, nil
}

// Serves requests while the manager is open, each one counted so Close waits for it
func (m *Manager) counted(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !m.hold() {
			http.Error(w, "mesh: this node is shutting down", http.StatusServiceUnavailable)
			return
		}
		defer m.wg.Done()
		next.ServeHTTP(w, r)
	})
}

// The port node traffic listens on
func (m *Manager) portLocked() string {
	_, port, err := net.SplitHostPort(m.listenAddr)
	if err != nil {
		return ""
	}
	return port
}

// The address this node tells other members, mesh.advertise or the outbound interface on the mesh port
func (m *Manager) advertiseLocked() string {
	port := m.portLocked()
	if adv := strings.TrimSpace(m.Config.GetAdvertise()); adv != "" {
		if _, _, err := net.SplitHostPort(adv); err == nil {
			return adv
		}
		if port == "" {
			return ""
		}
		return net.JoinHostPort(strings.Trim(adv, "[]"), port)
	}
	if port == "" || loopbackOnly(m.listenAddr) {
		return ""
	}
	host, _, _ := net.SplitHostPort(m.listenAddr)
	if ip := net.ParseIP(host); host != "" && ip != nil && !ip.IsUnspecified() {
		return m.listenAddr
	}
	var peers []string
	for _, mb := range m.members {
		if a := mb.rec.GetAddress(); a != "" {
			peers = append(peers, a)
		}
	}
	ip := outboundIP(peers)
	if ip == nil {
		return ""
	}
	return net.JoinHostPort(ip.String(), port)
}

// The address of the interface the route to the peers or the internet leaves through, no packet sent
func outboundIP(peers []string) net.IP {
	for _, target := range append(peers, "8.8.8.8:53") {
		conn, err := net.Dial("udp", target)
		if err != nil {
			continue
		}
		ip := conn.LocalAddr().(*net.UDPAddr).IP
		conn.Close()
		if ip != nil && !ip.IsLoopback() {
			return ip
		}
	}
	return nil
}

// Refuses a node other members cannot reach
func (m *Manager) requireReachable() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.advertiseLocked() == "" {
		return fmt.Errorf("%w: this node listens on loopback alone, so other members cannot reach it. Set mesh.listen to an address they reach, such as 0.0.0.0:8485, or mesh.advertise to the address on the link to them", ErrMesh)
	}
	return nil
}

// The address other members reach this node at
func (m *Manager) Address() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.advertiseLocked()
}

// The base URL other members reach this node's gateway at, the mesh address under mutual TLS
func (m *Manager) GatewayBase() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	address := m.advertiseLocked()
	if address == "" {
		return ""
	}
	return "https://" + address
}
