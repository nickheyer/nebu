package mesh

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/proto/nebu/v1/nebuv1connect"
	"golang.org/x/net/http2"
)

const dialTimeout = 5 * time.Second

// A connection to one member under its session credential
type Client struct {
	NodeID  string
	Address string
	// http or https and the address
	Base string
	// Carries the session credential on every request
	HTTP *http.Client
	Mesh nebuv1connect.MeshServiceClient
	// The credential alone, for requests built elsewhere
	Header http.Header
	// The configuration the member is dialed with, nil for a plain listener
	TLS *tls.Config
}

// Adds the bearer token to every request
type bearer struct {
	next  http.RoundTripper
	token string
}

func (b bearer) RoundTrip(req *http.Request) (*http.Response, error) {
	if b.token != "" {
		req = req.Clone(req.Context())
		req.Header.Set("Authorization", "Bearer "+b.token)
	}
	return b.next.RoundTrip(req)
}

// An HTTP/2 transport to an address: prior knowledge over plain TCP, or TLS
func transport(cfg *tls.Config) http.RoundTripper {
	d := &net.Dialer{Timeout: dialTimeout}
	if cfg == nil {
		return &http2.Transport{
			AllowHTTP: true,
			DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
				return d.DialContext(ctx, network, addr)
			},
			ReadIdleTimeout: 30 * time.Second,
			PingTimeout:     10 * time.Second,
		}
	}
	return &http2.Transport{
		TLSClientConfig: cfg,
		DialTLSContext: func(ctx context.Context, network, addr string, c *tls.Config) (net.Conn, error) {
			c = c.Clone()
			if c.ServerName == "" {
				host, _, _ := net.SplitHostPort(addr)
				c.ServerName = host
			}
			return (&tls.Dialer{NetDialer: d, Config: c}).DialContext(ctx, network, addr)
		},
		ReadIdleTimeout: 30 * time.Second,
		PingTimeout:     10 * time.Second,
	}
}

// A transport to an address, shared across calls while the way to trust it stays the same
func (m *Manager) roundTripper(address, fingerprint string, cfg *tls.Config) http.RoundTripper {
	key := address + "|plain"
	if cfg != nil {
		key = address + "|tls|" + fingerprint
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if rt, ok := m.h2[key]; ok {
		return rt
	}
	rt := transport(cfg)
	m.h2[key] = rt
	return rt
}

// A client with no credential, for the handshake and the admission calls. The fingerprint keys
// the transport, so a member whose certificate changed is dialed afresh.
func (m *Manager) anonymous(address string, cfg *tls.Config, fingerprint string) *Client {
	if cfg == nil {
		fingerprint = ""
	}
	base := "http://" + address
	if cfg != nil {
		base = "https://" + address
	}
	client := &http.Client{Transport: m.roundTripper(address, fingerprint, cfg)}
	return &Client{Address: address, Base: base, HTTP: client, Mesh: nebuv1connect.NewMeshServiceClient(client, base), Header: http.Header{}, TLS: cfg}
}

// The member's address, how to trust it, and the session token it takes, empty when none is held
func (m *Manager) dialFacts(nodeID string) (address string, useTLS bool, fingerprint string, cfg *tls.Config, token string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	mb, ok := m.members[nodeID]
	if !ok {
		return "", false, "", nil, "", fmt.Errorf("%w %q", ErrUnknownNode, nodeID)
	}
	rec := mb.rec
	address, useTLS, fingerprint = rec.GetAddress(), rec.GetTls(), rec.GetTlsFingerprint()
	if useTLS {
		cfg = m.dialTLSLocked(fingerprint)
	}
	if s := m.sessions[nodeID]; s != nil && s.send != "" && time.Now().Before(s.sendExpires) {
		token = s.send
	}
	return address, useTLS, fingerprint, cfg, token, nil
}

// A client to a member under a live session, handshaking first when none is held. Callers
// wanting the same member at once share one handshake, since a second one opened while the
// first is under way replaces its nonce on the peer and fails it.
func (m *Manager) Client(ctx context.Context, nodeID string) (*Client, error) {
	address, useTLS, fingerprint, cfg, token, err := m.dialFacts(nodeID)
	if err != nil {
		return nil, err
	}
	if address == "" {
		return nil, fmt.Errorf("%w: %s has no mesh address yet", ErrMesh, nodeID)
	}
	if token == "" {
		m.mu.Lock()
		dial := m.dialing[nodeID]
		if dial == nil {
			dial = &sync.Mutex{}
			m.dialing[nodeID] = dial
		}
		m.mu.Unlock()
		dial.Lock()
		defer dial.Unlock()
		if _, _, _, _, token, err = m.dialFacts(nodeID); err != nil {
			return nil, err
		}
	}
	if token == "" {
		resp, err := m.handshake(ctx, address, useTLS, fingerprint, nil, nil)
		if err != nil {
			return nil, err
		}
		if resp.GetNodeId() != nodeID {
			return nil, fmt.Errorf("%w: %s answered as %s, not %s", ErrMesh, address, resp.GetNodeId(), nodeID)
		}
		// Forget member until handshake happens again
		m.mu.Lock()
		_, still := m.members[nodeID]
		dropped := false
		if s := m.sessions[nodeID]; !still && s != nil && s.accept == "" {
			delete(m.sessions, nodeID)
			dropped = true
		}
		m.mu.Unlock()
		if !still {
			if dropped {
				m.DB.DeleteSession(context.Background(), nodeID)
			}
			return nil, fmt.Errorf("%w %q", ErrUnknownNode, nodeID)
		}
		m.absorb(resp.GetNode(), resp.GetMembers(), true)
		token = resp.GetSessionToken()
	}
	base := "http://" + address
	if cfg != nil {
		base = "https://" + address
	}
	client := &http.Client{Transport: bearer{next: m.roundTripper(address, fingerprint, cfg), token: token}}
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

// The member record behind a client, for callers that need its facts
func (m *Manager) Member(nodeID string) (*v1.Node, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	mb, ok := m.members[nodeID]
	if !ok {
		return nil, false
	}
	return mb.rec, true
}
