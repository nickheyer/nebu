package gateway

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/nickheyer/nebu/internal/auth"
	"github.com/nickheyer/nebu/internal/chat"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/requestcontrol"
	"github.com/nickheyer/nebu/pkg/telemetry"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

const (
	healthPath = "/health"
	modelsPath = "/v1/models"
	// The root answer the Ollama CLI expects
	heartbeat = "Ollama is running"
	// In process request base, recorded as the trace's remote address
	localBase = "http://nebu.gateway"
)

// The headers the gateway reads from a member that forwarded a request here
const (
	// The node a forwarded request entered the mesh at
	EntryHeader = "X-Nebu-Entry"
	// The subject the entry node authenticated, whose traces the request joins
	OwnerHeader = "X-Nebu-Owner"
	// The header a gateway answer carries its trace id in
	TraceHeader = "X-Nebu-Trace"
)

// Marks requests made in process, which pass without credentials
type localKey struct{}

// Every gateway route behind CORS, auth and a server span
func (g *Gateway) routesMux() http.Handler {
	mux := http.NewServeMux()
	g.Mount(mux)
	mux.Handle("/", traced(g.cors(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" || r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		io.WriteString(w, heartbeat)
	})))
	return mux
}

// Wraps a handler in a server span, continuing any trace the client's headers carry
func traced(h http.HandlerFunc) http.Handler {
	return telemetry.Middleware(h, requestcontrol.HeaderModelName)
}

// Registers every gateway route on a mux shared with other services, each HTTP route in a server span and gRPC in its own
func (g *Gateway) Mount(mux *http.ServeMux) {
	mux.Handle(inferencePrefix, g.grpc)
	mux.Handle(sglangPrefix, g.grpc)
	g.MountHealth(mux)
	mux.Handle(healthPath, traced(g.cors(g.healthz)))
	mux.Handle(modelsPath, traced(g.cors(g.auth(g.models))))
	mux.Handle(modelsPath+"/", traced(g.cors(g.auth(g.model))))
	mux.Handle(chat.OllamaTags, traced(g.cors(g.auth(g.models))))
	mux.Handle(chat.OllamaPs, traced(g.cors(g.auth(g.models))))
	mux.Handle(chat.OllamaShow, traced(g.cors(g.auth(g.show))))
	mux.Handle(chat.OllamaVersion, traced(g.cors(g.version)))
	for _, p := range []string{chat.OpenAIChat, chat.OpenAICompletions, chat.OpenAIEmbeddings, chat.OpenAIResponses, chat.AnthropicMessages, chat.AnthropicCount, chat.OllamaChat, chat.OllamaGenerate, chat.OllamaEmbed, chat.OllamaEmbeddings, tokenizePath, detokenizePath} {
		mux.Handle(p, traced(g.cors(g.auth(g.serve))))
	}
	mux.Handle(chat.OpenAIImages, traced(g.cors(g.auth(g.serve))))
	mux.Handle(chat.OpenAIEdits, traced(g.cors(g.auth(g.serve))))
	mux.Handle(chat.OpenAIVideos, traced(g.cors(g.auth(g.videoRoot))))
	mux.Handle(chat.OpenAIVideos+"/", traced(g.cors(g.auth(g.video))))
	mux.Handle(chat.SDCPPPrefix, traced(g.cors(g.auth(g.serve))))
}

// Registers liveness, readiness, the gRPC health service and the metrics behind the gateway's credentials
func (g *Gateway) MountHealth(mux *http.ServeMux) {
	mux.Handle("/"+healthpb.Health_ServiceDesc.ServiceName+"/", g.grpc)
	mux.Handle("/livez", g.health.LivezHandler())
	mux.Handle("/readyz", g.health.ReadyzHandler())
	mux.Handle("/metrics", g.metricsAuth(g.metrics.Handler().ServeHTTP))
}

// Lets metrics through for a daemon credential holding the read scope or any credential the gateway itself takes
func (g *Gateway) metricsAuth(next http.HandlerFunc) http.HandlerFunc {
	gated := g.auth(next)
	return func(w http.ResponseWriter, r *http.Request) {
		if g.guard.Enabled() && g.guard.Scoped(r.Header, v1.Scope_SCOPE_READ) {
			next(w, r)
			return
		}
		gated(w, r)
	}
}

// The base URL in process clients address
func (g *Gateway) LocalBase() string { return localBase }

// An in process client labeled with its origin, trusted without credentials under normal limits and tracing
func (g *Gateway) Client(origin string) *http.Client {
	return &http.Client{Transport: &localTransport{handler: g.mux, origin: origin}}
}

// Handles CORS headers and preflight requests
func (g *Gateway) cors(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" && g.originAllowed(origin) {
			h := w.Header()
			h.Set("Access-Control-Allow-Origin", origin)
			h.Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
			if g.credentialed(origin, r) {
				h.Set("Access-Control-Allow-Credentials", "true")
			}
			if asked := r.Header.Get("Access-Control-Request-Headers"); asked != "" {
				h.Set("Access-Control-Allow-Headers", asked)
			}
			h.Set("Access-Control-Expose-Headers", "Retry-After, "+TraceHeader+", "+requestcontrol.HeaderServed+", "+requestcontrol.HeaderRequestID)
			h.Set("Access-Control-Max-Age", "600")
			h.Add("Vary", "Origin")
			h.Add("Vary", "Access-Control-Request-Headers")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next(w, r)
	}
}

// Whether browsers at origin may send cookies, a listed origin, the request's own, or another local listener
func (g *Gateway) credentialed(origin string, r *http.Request) bool {
	for _, o := range g.cfg.Origins {
		if o != "*" && strings.EqualFold(o, origin) {
			return true
		}
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	ownHost, ownPort := splitHost(g.guard.Host(r))
	if !strings.EqualFold(u.Hostname(), ownHost) {
		return false
	}
	if port := u.Port(); port == ownPort || port == "" && ownPort == "" {
		return true
	}
	for _, l := range g.listening().list {
		if _, port := splitHost(l.GetAddr()); port != "" && port == u.Port() {
			return true
		}
	}
	return false
}

// A host and its port, the port empty when the address names none
func splitHost(addr string) (string, string) {
	if h, p, err := net.SplitHostPort(addr); err == nil {
		return strings.Trim(h, "[]"), p
	}
	return strings.Trim(addr, "[]"), ""
}

func (g *Gateway) originAllowed(origin string) bool {
	if len(g.cfg.Origins) == 0 {
		return true
	}
	for _, o := range g.cfg.Origins {
		if o == "*" || strings.EqualFold(o, origin) {
			return true
		}
	}
	return false
}

// Whether requests need a credential at all
func (g *Gateway) required() bool {
	return len(g.keys()) > 0 || g.guard.Enabled()
}

type identityKey struct{}

// The identity the auth layer attached to a request
func identityOf(r *http.Request) identity {
	id, _ := r.Context().Value(identityKey{}).(identity)
	return id
}

// Refuses requests carrying neither the inference scope nor a gateway key, attaching the caller's identity
func (g *Gateway) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := g.identify(r)
		if !ok {
			w.Header().Set("WWW-Authenticate", `Bearer realm="nebu"`)
			chat.ClientOf(chat.Detect(r)).Error(w, chat.Failure{Status: http.StatusUnauthorized, Code: chat.CodeAuthentication, Message: "missing or invalid api key, or a credential without the inference scope"})
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), identityKey{}, id)))
	}
}

// Who sent the request, in process, by gateway key, by the member session that forwarded it, or by credential
func (g *Gateway) identify(r *http.Request) (identity, bool) {
	if r.Context().Value(localKey{}) != nil {
		return identity{admin: true}, true
	}
	token := auth.Bearer(r.Header)
	for _, k := range g.keys() {
		if token != "" && subtle.ConstantTimeCompare([]byte(k), []byte(token)) == 1 {
			owner := keyOwner(k)
			return identity{owner: owner, fairness: owner}, true
		}
	}
	if r.Header.Get(EntryHeader) != "" {
		if _, ok := g.guard.PeerOf(r.Header); ok {
			owner := r.Header.Get(OwnerHeader)
			return identity{owner: owner, fairness: owner, peer: true}, true
		}
	}
	if !g.guard.Enabled() {
		return identity{admin: true}, len(g.keys()) == 0
	}
	if id, ok := g.guard.Identify(r.Header); ok {
		if !id.Allows(v1.Scope_SCOPE_INFERENCE) {
			return identity{}, false
		}
		owner := firstOf(id.Subject, id.TokenPrefix, id.Provider)
		return identity{owner: owner, fairness: owner, admin: id.Admin()}, true
	}
	return identity{}, false
}

// The subject a gateway key's requests are kept under, a digest prefix that never reveals the key
func keyOwner(key string) string {
	sum := sha256.Sum256([]byte(key))
	return "key:" + hex.EncodeToString(sum[:4])
}

func firstOf(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// Serves handler responses through a pipe for streaming, every request marked in process
type localTransport struct {
	handler http.Handler
	origin  string
}

func (t *localTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(context.WithValue(req.Context(), localKey{}, true))
	req.RemoteAddr = t.origin
	pr, pw := io.Pipe()
	w := &pipeWriter{header: http.Header{}, pw: pw, ready: make(chan struct{})}
	go func() {
		defer pw.Close()
		defer w.WriteHeader(http.StatusOK)
		t.handler.ServeHTTP(w, req)
	}()
	select {
	case <-w.ready:
	case <-req.Context().Done():
		pr.Close()
		return nil, req.Context().Err()
	}
	return &http.Response{
		Status:     fmt.Sprintf("%d %s", w.status, http.StatusText(w.status)),
		StatusCode: w.status,
		Proto:      "HTTP/1.1",
		ProtoMajor: 1,
		ProtoMinor: 1,
		Header:     w.snapshot,
		Body:       pr,
		Request:    req,
	}, nil
}

// Streams handler output through a pipe, fixing headers on the first write
type pipeWriter struct {
	header   http.Header
	snapshot http.Header
	pw       *io.PipeWriter
	status   int
	ready    chan struct{}
	once     sync.Once
}

func (w *pipeWriter) Header() http.Header { return w.header }

func (w *pipeWriter) WriteHeader(code int) {
	w.once.Do(func() {
		w.status = code
		w.snapshot = w.header.Clone()
		close(w.ready)
	})
}

func (w *pipeWriter) Write(p []byte) (int, error) {
	w.WriteHeader(http.StatusOK)
	return w.pw.Write(p)
}

func (w *pipeWriter) Flush() { w.WriteHeader(http.StatusOK) }
