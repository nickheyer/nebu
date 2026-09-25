package auth

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"
	"strings"
	"sync"
)

// Answers whether a bearer token is a mesh session another member holds, and which member
type Peers interface {
	Peer(token string) (nodeID string, ok bool)
}

// Checks the credentials API and gateway requests carry: the daemon token from
// auth.token, an API token a user made, or the browser session cookie. Mesh members carry a
// session token instead, which opens node to node calls and nothing else.
type Guard struct {
	token    string
	sessions *Sessions
	tokens   *Tokens
	err      error

	mu    sync.RWMutex
	peers Peers
}

// Builds the guard. An empty token with nil sessions is auth.disabled, where every request passes.
func NewGuard(token string, sessions *Sessions, tokens *Tokens) *Guard {
	g := &Guard{token: token, sessions: sessions, tokens: tokens}
	switch {
	case sessions != nil:
		g.err = errors.New("sign in, or send an API token from Settings or the daemon's api.token as a bearer token")
	case token != "":
		g.err = errors.New("missing or invalid token, set auth.token or NEBU_AUTH_TOKEN")
	default:
		g.err = errors.New("missing credentials")
	}
	return g
}

// Installs the mesh session table, so members' tokens are recognized
func (g *Guard) SetPeers(p Peers) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.peers = p
}

// Whether requests need credentials at all
func (g *Guard) Enabled() bool { return g.token != "" || g.sessions != nil }

// The error refused requests are answered with
func (g *Guard) Err() error { return g.err }

// Whether the headers carry a valid credential. Everything passes when auth is off.
func (g *Guard) Authenticated(h http.Header) bool {
	if !g.Enabled() {
		return true
	}
	_, ok := g.Identify(h)
	return ok
}

// Who sent the request: the account behind a session cookie or an API token,
// or nil and true for the daemon token. False when nothing valid was sent.
func (g *Guard) Identify(h http.Header) (*Session, bool) {
	bearer := Bearer(h)
	if g.token != "" && bearer != "" && subtle.ConstantTimeCompare([]byte(bearer), []byte(g.token)) == 1 {
		return nil, true
	}
	if g.tokens != nil && bearer != "" {
		if sess, ok := g.tokens.Identify(context.Background(), bearer); ok {
			return sess, true
		}
	}
	if g.sessions != nil {
		return g.sessions.Session(h)
	}
	return nil, false
}

// The mesh member behind the request's bearer token, false for every other credential
func (g *Guard) PeerOf(h http.Header) (string, bool) {
	bearer := Bearer(h)
	if bearer == "" {
		return "", false
	}
	g.mu.RLock()
	peers := g.peers
	g.mu.RUnlock()
	if peers == nil {
		return "", false
	}
	return peers.Peer(bearer)
}

// The token in the Authorization bearer header, or the X-Api-Key header Anthropic clients send
func Bearer(h http.Header) string {
	const scheme = "bearer "
	v := h.Get("Authorization")
	if len(v) >= len(scheme) && strings.EqualFold(v[:len(scheme)], scheme) {
		return strings.TrimSpace(v[len(scheme):])
	}
	return strings.TrimSpace(h.Get("X-Api-Key"))
}
