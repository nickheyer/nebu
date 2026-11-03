package mesh

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"time"
)

// A relay handshake completes within this long
const relayHandshakeTimeout = 10 * time.Second

// One exported local address, served to members and importers over TLS on the mesh host and piped byte for byte
type relay struct {
	target string
	ln     net.Listener
	port   int
}

// The relay port an exported local address is served on, bound on first use
func (m *Manager) relayFor(target string) (int, error) {
	m.relaying.Lock()
	defer m.relaying.Unlock()
	if r, ok := m.relays[target]; ok {
		return r.port, nil
	}
	m.mu.Lock()
	host, _, err := net.SplitHostPort(m.listenAddr)
	closed := m.closed
	from, to, ranged := m.Ports()
	m.mu.Unlock()
	if closed {
		return 0, errClosed
	}
	if err != nil {
		return 0, fmt.Errorf("%w: no mesh listener is bound, so %s cannot be exported", ErrMesh, target)
	}
	ln, err := m.bindRelay(host, from, to, ranged)
	if err != nil {
		return 0, err
	}
	r := &relay{target: target, ln: ln, port: ln.Addr().(*net.TCPAddr).Port}
	m.relays[target] = r
	if !m.spawn(func() { m.serveRelay(r) }) {
		ln.Close()
		delete(m.relays, target)
		return 0, errClosed
	}
	m.Log.Info("mesh relay bound", "target", target, "port", r.port)
	return r.port, nil
}

// Binds a relay on the mesh host, inside the configured port range when one is set, else on any free port
func (m *Manager) bindRelay(host string, from, to int, ranged bool) (net.Listener, error) {
	if !ranged {
		return net.Listen("tcp", net.JoinHostPort(host, "0"))
	}
	taken := map[int]bool{}
	for _, r := range m.relays {
		taken[r.port] = true
	}
	var last error
	for port := from; port <= to; port++ {
		if taken[port] {
			continue
		}
		ln, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
		if err == nil {
			return ln, nil
		}
		last = err
	}
	return nil, fmt.Errorf("%w: no free relay port in mesh.ports %d-%d: %v", ErrMesh, from, to, last)
}

// Closes relays whose target is no longer exported
func (m *Manager) pruneRelays(keep map[string]bool) {
	m.relaying.Lock()
	defer m.relaying.Unlock()
	for target, r := range m.relays {
		if keep[target] {
			continue
		}
		r.ln.Close()
		delete(m.relays, target)
		m.Log.Info("mesh relay closed", "target", target, "port", r.port)
	}
}

// Closes every relay
func (m *Manager) closeRelays() {
	m.pruneRelays(nil)
}

// The relay port serving a local address, zero when none does
func (m *Manager) relayPort(target string) int {
	m.relaying.Lock()
	defer m.relaying.Unlock()
	if r, ok := m.relays[target]; ok {
		return r.port
	}
	return 0
}

// Accepts connections on a relay until it is closed, each one proven and piped on its own goroutine
func (m *Manager) serveRelay(r *relay) {
	for {
		conn, err := r.ln.Accept()
		if err != nil {
			if !errors.Is(err, net.ErrClosed) {
				m.Log.Warn("mesh relay stopped", "target", r.target, "err", err)
			}
			return
		}
		if !m.spawn(func() { m.relayConn(conn, r.target) }) {
			conn.Close()
			return
		}
	}
}

// Completes the TLS handshake, refuses every caller but members and importers, then pipes the connection to the local target
func (m *Manager) relayConn(raw net.Conn, target string) {
	defer raw.Close()
	tc := tls.Server(raw, m.relayTLS())
	ctx, cancel := context.WithTimeout(m.base, relayHandshakeTimeout)
	err := tc.HandshakeContext(ctx)
	cancel()
	if err != nil {
		m.Log.Debug("mesh relay handshake failed", "target", target, "from", raw.RemoteAddr(), "err", err)
		return
	}
	cs := tc.ConnectionState()
	if err := m.admitRelay(&cs); err != nil {
		m.Log.Warn("mesh relay refused a caller", "target", target, "from", raw.RemoteAddr(), "err", err)
		return
	}
	local, err := (&net.Dialer{Timeout: dialTimeout}).DialContext(m.base, "tcp", target)
	if err != nil {
		m.Log.Warn("mesh relay target unreachable", "target", target, "err", err)
		return
	}
	defer local.Close()
	stop := context.AfterFunc(m.base, func() {
		tc.Close()
		local.Close()
	})
	defer stop()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		io.Copy(local, tc)
		local.(*net.TCPConn).CloseWrite()
	}()
	go func() {
		defer wg.Done()
		io.Copy(tc, local)
		tc.CloseWrite()
	}()
	wg.Wait()
}

// Members under the authority on the mesh server name, importers of other meshes by pinned identity certificate on the admission server name
func (m *Manager) relayTLS() *tls.Config {
	return &tls.Config{MinVersion: tls.VersionTLS13, GetConfigForClient: m.configForClient}
}

// Refuses a relay connection unless its certificate proves a member or a federated importer
func (m *Manager) admitRelay(cs *tls.ConnectionState) error {
	if len(cs.PeerCertificates) == 0 {
		return errors.New("no client certificate")
	}
	c, err := m.callerFor(cs)
	if err != nil {
		return err
	}
	switch c.kind {
	case peerMember, peerImporter:
		return nil
	}
	return fmt.Errorf("a %s reaches no exported endpoint", c.kind)
}

// A caller kind as log lines name it
func (k peerKind) String() string {
	switch k {
	case peerMember:
		return "member"
	case peerGrant:
		return "join grant holder"
	case peerStranger:
		return "node outside the mesh"
	case peerImporter:
		return "importer"
	}
	return "unknown caller"
}
