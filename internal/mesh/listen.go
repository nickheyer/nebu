package mesh

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/nickheyer/nebu/internal/mesh/links"
)

const (
	readHeaderTimeout = 10 * time.Second
	// How long a kept alive connection may sit between requests before it is closed
	idleTimeout = 2 * time.Minute
	// How long a server replaced by a rebind may finish the requests it already took
	rebindDrain = 30 * time.Second
)

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

// Binds the mesh listener: mesh.listen when set, the default when the API listener stays on
// loopback, and nothing when the API listener already reaches the network
func (m *Manager) listen() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.listener != nil {
		return nil
	}
	addr := m.Config.GetListen()
	if addr == "" {
		if !loopbackOnly(m.APIListen) {
			m.listenAddr = m.APIListen
			return nil
		}
		addr = DefaultListen
	}
	return m.bindLocked(addr)
}

func (m *Manager) bindLocked(addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("mesh.listen %s: %w", addr, err)
	}
	if cfg := m.serverTLSLocked(); cfg != nil {
		ln = tls.NewListener(ln, cfg)
	}
	srv := &http.Server{Handler: m.Handler, ReadHeaderTimeout: readHeaderTimeout, IdleTimeout: idleTimeout, Protocols: links.Protocols()}
	served := make(chan struct{})
	m.listener, m.server, m.served, m.listenAddr = ln, srv, served, ln.Addr().String()
	m.Log.Info("mesh listening", "addr", m.listenAddr, "tls", m.serverTLSLocked() != nil)
	go func() {
		defer close(served)
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			m.Log.Warn("mesh listener stopped", "addr", addr, "err", err)
		}
	}()
	return nil
}

// Binds the listener again after its certificate changed, the old server first answering what it already took
func (m *Manager) rebind() error {
	m.mu.Lock()
	srv, served, addr := m.server, m.served, m.listenAddr
	m.server, m.listener, m.served = nil, nil, nil
	m.mu.Unlock()
	if srv == nil {
		return m.listen()
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), rebindDrain)
		defer cancel()
		if srv.Shutdown(ctx) != nil {
			srv.Close()
		}
	}()
	<-served
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.bindLocked(addr)
}

// The port node traffic listens on
func (m *Manager) portLocked() string {
	_, port, err := net.SplitHostPort(m.listenAddr)
	if err != nil {
		return ""
	}
	return port
}

// The address this node tells other members: mesh.advertise, else the address of the interface
// the default route leaves through on the mesh port. Empty when the node listens on loopback alone.
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

// The address of the interface the route to the peers, or to the internet, leaves through. No
// packet is sent, the kernel picks the source address when the socket connects.
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

// The base URL other members reach this node's gateway at: the mesh address, over TLS when node
// traffic is served with it
func (m *Manager) GatewayBase() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	address := m.advertiseLocked()
	if address == "" {
		return ""
	}
	if m.serverTLSLocked() != nil {
		return "https://" + address
	}
	return "http://" + address
}
