package mesh

import (
	"context"
	"crypto/tls"
	"net"
)

// Where an imported endpoint address leads, the exporter and whether it is reached by identity certificate
type remoteTarget struct {
	node      string
	federated bool
}

// Rebuilds the table of imported addresses from every import held, called with the importing lock held
func (m *Manager) refreshRemoteAddrsLocked() {
	table := map[string]remoteTarget{}
	for exporter, list := range m.imports {
		_, federated := m.exporters[exporter]
		for _, imp := range list {
			for _, addr := range imp.addrs {
				table[addr] = remoteTarget{node: exporter, federated: federated}
			}
		}
	}
	m.mu.Lock()
	m.remoteAddrs = table
	m.mu.Unlock()
}

// Dials an address, over TLS to the exporter's relay when the address is an imported endpoint's, plainly otherwise
func (m *Manager) DialEndpoint(ctx context.Context, network, addr string) (net.Conn, error) {
	m.mu.Lock()
	target, imported := m.remoteAddrs[addr]
	m.mu.Unlock()
	d := &net.Dialer{Timeout: dialTimeout}
	if !imported {
		return d.DialContext(ctx, network, addr)
	}
	var cfg *tls.Config
	if target.federated {
		cfg = m.exporterDialConfig(target.node)
	} else {
		cfg = m.memberDialConfig(target.node)
	}
	cfg = cfg.Clone()
	// The bytes are piped to the runtime unchanged, so no application protocol is negotiated
	cfg.NextProtos = nil
	return (&tls.Dialer{NetDialer: d, Config: cfg}).DialContext(ctx, network, addr)
}
