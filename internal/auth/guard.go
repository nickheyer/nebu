package auth

import (
	"cmp"
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"connectrpc.com/connect"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/proto/nebu/v1/nebuv1connect"
)

// Answers whether a bearer token is a mesh session another member holds, and which member
type Peers interface {
	Peer(token string) (nodeID string, ok bool)
}

// Checks the daemon token, user API tokens, and session cookies requests carry, member sessions opening node calls alone
type Guard struct {
	token    string
	sessions *Sessions
	tokens   *Tokens
	err      error

	mu    sync.RWMutex
	peers Peers
}

// Builds the guard, an empty token with nil sessions being auth.disabled where every request passes
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

// Whether the headers carry a credential opening the scope, everything passing when auth is off
func (g *Guard) Scoped(h http.Header, scope v1.Scope) bool {
	if !g.Enabled() {
		return true
	}
	id, ok := g.Identify(h)
	return ok && id.Allows(scope)
}

// Who sent the request and what the credential opens, false when nothing valid was sent or auth is off
func (g *Guard) Identify(h http.Header) (Identity, bool) {
	bearer := Bearer(h)
	if g.token != "" && bearer != "" && subtle.ConstantTimeCompare([]byte(bearer), []byte(g.token)) == 1 {
		return Identity{Provider: ProviderDaemon, Role: v1.Role_ROLE_ADMIN, Scopes: RoleScopes(v1.Role_ROLE_ADMIN)}, true
	}
	if g.tokens != nil && bearer != "" {
		if id, ok := g.tokens.Identify(context.Background(), bearer); ok {
			return id, true
		}
	}
	if g.sessions != nil {
		if sess, ok := g.sessions.Session(h); ok {
			return Identity{Subject: sess.Subject, Provider: sess.Provider, Name: sess.Name, Email: sess.Email, Role: sess.Role, Scopes: RoleScopes(sess.Role)}, true
		}
	}
	return Identity{}, false
}

// The procedures a member calls on behalf of a caller of its own, naming the owner whose provider credentials the work goes
// under: a pull it sends on. No other call of a member's reaches an owner it names.
var forwardsOwner = map[string]bool{nebuv1connect.MeshServicePullSeatProcedure: true}

// The owner a call's provider credentials are kept under: as CredentialOwner has it, but for a member calling a procedure
// it forwards an owner on, which goes under the owner it names
func (g *Guard) CallOwner(procedure string, h http.Header) string {
	if _, peer := g.PeerOf(h); peer && forwardsOwner[procedure] {
		return cmp.Or(h.Get(CredentialOwnerHeader), ProviderDaemon)
	}
	return g.CredentialOwner(h)
}

// The owner a request's provider credentials are kept under: its account's, the shared one for a member, the daemon token
// and auth.disabled, and none for a request no credential opens
func (g *Guard) CredentialOwner(h http.Header) string {
	if _, peer := g.PeerOf(h); peer {
		return ProviderDaemon
	}
	id, ok := g.Identify(h)
	if !ok && g.Enabled() {
		return ""
	}
	return CredentialOwner(id)
}

// Refuses a call the procedure's rule does not open, unauthenticated without a credential and denied without the scope
func (g *Guard) Check(procedure string, h http.Header) error {
	rule, ok := procedures[procedure]
	if !ok {
		return connect.NewError(connect.CodePermissionDenied, fmt.Errorf("%s has no access rule", procedure))
	}
	if rule.open || !g.Enabled() {
		return nil
	}
	if rule.peer {
		if _, ok := g.PeerOf(h); ok {
			return nil
		}
	}
	id, ok := g.Identify(h)
	if !ok {
		return connect.NewError(connect.CodeUnauthenticated, g.err)
	}
	if !id.Allows(rule.scope) {
		return connect.NewError(connect.CodePermissionDenied, fmt.Errorf("%s needs the %s scope, which this credential does not hold", procedure, ScopeName(rule.scope)))
	}
	return nil
}

// Applies Check to every unary and streaming call a handler serves
func (g *Guard) Interceptor() connect.Interceptor { return interceptor{g} }

type interceptor struct{ g *Guard }

func (i interceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		if err := i.g.Check(req.Spec().Procedure, req.Header()); err != nil {
			return nil, err
		}
		return next(ctx, req)
	}
}

func (i interceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (i interceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		if err := i.g.Check(conn.Spec().Procedure, conn.RequestHeader()); err != nil {
			return err
		}
		if conn.Spec().Procedure == nebuv1connect.EventServiceWatchEventsProcedure {
			conn = eventFilter{StreamingHandlerConn: conn, guard: i.g, header: conn.RequestHeader()}
		}
		return next(ctx, conn)
	}
}

// Signs every call a handler serves with its caller's provider credentials, so each provider source a handler reaches answers for the caller
func (c *Credentials) Interceptor(g *Guard) connect.Interceptor { return signing{c, g} }

type signing struct {
	c *Credentials
	g *Guard
}

func (s signing) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		return next(s.c.signAs(ctx, sync.OnceValue(func() string { return s.g.CallOwner(req.Spec().Procedure, req.Header()) })), req)
	}
}

func (s signing) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (s signing) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		return next(s.c.signAs(ctx, sync.OnceValue(func() string { return s.g.CallOwner(conn.Spec().Procedure, conn.RequestHeader()) })), conn)
	}
}

// Event kinds a subscriber needs more than the read scope to receive, matching the calls that read the same records
var eventScopes = map[v1.EventKind]v1.Scope{
	v1.EventKind_EVENT_KIND_SETTINGS: v1.Scope_SCOPE_ADMIN,
}

// Drops events whose kind needs a scope the subscriber lacks and other accounts' traces from non admins
type eventFilter struct {
	connect.StreamingHandlerConn
	guard  *Guard
	header http.Header
}

func (f eventFilter) Send(msg any) error {
	if r, ok := msg.(*v1.WatchEventsResponse); ok && !f.guard.receives(f.header, r.GetEvent()) {
		return nil
	}
	return f.StreamingHandlerConn.Send(msg)
}

// Whether the credential receives an event, a trace reaching its owner and admins alone while auth is on
func (g *Guard) receives(h http.Header, ev *v1.Event) bool {
	if need, gated := eventScopes[ev.GetKind()]; gated && !g.Scoped(h, need) {
		return false
	}
	if ev.GetKind() != v1.EventKind_EVENT_KIND_TRACE || !g.Enabled() {
		return true
	}
	id, ok := g.Identify(h)
	return ok && (id.Admin() || id.Owner() == ev.GetTrace().GetOwner())
}

// The host the browser addressed, X-Forwarded-Host behind a proxy the sessions trust
func (g *Guard) Host(r *http.Request) string {
	if g.sessions == nil {
		return r.Host
	}
	return g.sessions.Proxies().Host(r)
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
