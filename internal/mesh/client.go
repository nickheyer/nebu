package mesh

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/nickheyer/nebu/internal/mesh/links"
	"github.com/nickheyer/nebu/pkg/proto/nebu/v1/nebuv1connect"
)

const dialTimeout = 5 * time.Second

// A connection to one member under mutual TLS, its API calls carrying the session bearer
type Client struct {
	NodeID  string
	Address string
	// https and the address
	Base string
	// Carries the session bearer on every request
	HTTP *http.Client
	Mesh nebuv1connect.MeshServiceClient
	// The bearer alone, for requests built elsewhere
	Header http.Header
	// The configuration the member is dialed with
	TLS *tls.Config
}

// Adds the member's current session bearer to every request, syncing for a fresh one when none is live
type bearer struct {
	next http.RoundTripper
	m    *Manager
	node string
}

func (b bearer) RoundTrip(req *http.Request) (*http.Response, error) {
	token, err := b.m.sessionToken(req.Context(), b.node)
	if err != nil {
		return nil, err
	}
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+token)
	return b.next.RoundTrip(req)
}

// An HTTP/2 transport over TLS to an address, pinged while idle
func transport(cfg *tls.Config) *http.Transport {
	d := &net.Dialer{Timeout: dialTimeout}
	return &http.Transport{
		Protocols:       links.ClientProtocols(),
		HTTP2:           &http.HTTP2Config{SendPingTimeout: 30 * time.Second, PingTimeout: 10 * time.Second},
		TLSClientConfig: cfg,
		DialTLSContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return (&tls.Dialer{NetDialer: d, Config: cfg.Clone()}).DialContext(ctx, network, addr)
		},
	}
}

// A transport per address and trust, shared across calls while the way to trust the address stays the same
func (m *Manager) roundTripper(key string, cfg *tls.Config) http.RoundTripper {
	m.mu.Lock()
	defer m.mu.Unlock()
	if rt, ok := m.h2[key]; ok {
		return rt
	}
	rt := transport(cfg)
	m.h2[key] = rt
	return rt
}

// Drops the transports held for an address, every one when the address is empty, closing idle connections
func (m *Manager) dropTransports(address string) {
	m.mu.Lock()
	var dropped []http.RoundTripper
	for key, rt := range m.h2 {
		if address == "" || strings.HasPrefix(key, address+"|") {
			dropped = append(dropped, rt)
			delete(m.h2, key)
		}
	}
	m.mu.Unlock()
	for _, rt := range dropped {
		if closer, ok := rt.(interface{ CloseIdleConnections() }); ok {
			closer.CloseIdleConnections()
		}
	}
}

// Transports held, one per address and trust
func (m *Manager) transports() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.h2)
}

// The membership client to a member at an address under mutual TLS
func (m *Manager) peerClient(nodeID, address string) nebuv1connect.MeshPeerServiceClient {
	rt := m.roundTripper(address+"|member|"+nodeID, m.memberDialConfig(nodeID))
	return nebuv1connect.NewMeshPeerServiceClient(&http.Client{Transport: rt}, "https://"+address)
}

// The membership client to a member by id, certificate alone authenticating its calls
func (m *Manager) peer(nodeID string) (nebuv1connect.MeshPeerServiceClient, error) {
	m.mu.Lock()
	mb, ok := m.members[nodeID]
	address := mb.GetAddress()
	m.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("%w %q", ErrUnknownNode, nodeID)
	}
	if address == "" {
		return nil, fmt.Errorf("%w: %s has no mesh address yet", ErrMesh, nodeID)
	}
	return m.peerClient(nodeID, address), nil
}

// The admission client to a node by its identity certificate, the id and pin checked when known
func (m *Manager) admissionClient(address, expectID, pin string) nebuv1connect.MeshPeerServiceClient {
	rt := m.roundTripper(address+"|admission|"+expectID+"|"+pin, m.admissionDialConfig(expectID, pin, nil))
	return nebuv1connect.NewMeshPeerServiceClient(&http.Client{Transport: rt}, "https://"+address)
}

// The bearer a member or an exporter of another mesh takes, syncing or pulling first when none is live
func (m *Manager) sessionToken(ctx context.Context, nodeID string) (string, error) {
	if token := m.liveToken(nodeID); token != "" {
		return token, nil
	}
	m.mu.Lock()
	_, federated := m.exporters[nodeID]
	m.mu.Unlock()
	refresh := m.syncWith
	if federated {
		refresh = m.pullExports
	}
	if err := refresh(ctx, nodeID); err != nil {
		return "", err
	}
	if token := m.liveToken(nodeID); token != "" {
		return token, nil
	}
	return "", fmt.Errorf("%w: %s answered the sync without a session token", ErrMesh, nodeID)
}

// The bearer held for a member while it is live, empty otherwise
func (m *Manager) liveToken(nodeID string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s := m.sessions[nodeID]; s != nil && s.send != "" && time.Now().Before(s.sendExpires) {
		return s.send
	}
	return ""
}

// A client to a member under mutual TLS, or to an exporter of another mesh by pinned certificate, carrying a live bearer
func (m *Manager) Client(ctx context.Context, nodeID string) (*Client, error) {
	m.mu.Lock()
	mb, ok := m.members[nodeID]
	address := mb.GetAddress()
	f, federated := m.exporters[nodeID]
	if federated {
		address = f.rec.GetFederation().GetAddress()
	}
	m.mu.Unlock()
	if !ok && !federated {
		return nil, fmt.Errorf("%w %q", ErrUnknownNode, nodeID)
	}
	if address == "" {
		return nil, fmt.Errorf("%w: %s has no mesh address yet", ErrMesh, nodeID)
	}
	token, err := m.sessionToken(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	cfg, key := m.memberDialConfig(nodeID), address+"|member|"+nodeID
	if federated {
		cfg, key = m.exporterDialConfig(nodeID), address+"|federated|"+nodeID
	}
	rt := m.roundTripper(key, cfg)
	base := "https://" + address
	client := &http.Client{Transport: bearer{next: rt, m: m, node: nodeID}}
	return &Client{
		NodeID:  nodeID,
		Address: address,
		Base:    base,
		HTTP:    client,
		Mesh:    nebuv1connect.NewMeshServiceClient(client, base),
		Header:  http.Header{"Authorization": {"Bearer " + token}},
		TLS:     cfg,
	}, nil
}
