// Package gateway exposes ready instances behind one endpoint in the OpenAI, Anthropic, and Ollama formats.
package gateway

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/nickheyer/nebu/internal/db"
	"github.com/nickheyer/nebu/pkg/events"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	prefix      = "/v1/"
	modelsPath  = "/v1/models"
	healthPath  = "/health"
	maxBody     = 64 << 20
	modelHeader = "X-Nebu-Model"
	retryAfter  = "2"
	// Root response expected by the Ollama CLI.
	heartbeat = "Ollama is running"
)

// Answers whether requests need credentials at all and whether one request carries a valid one:
// the daemon token, a user's API token, or a signed-in browser session. A mesh member's session
// is recognized apart, since it opens forwarded requests alone
type Credentials interface {
	Enabled() bool
	Authenticated(http.Header) bool
	PeerOf(http.Header) (nodeID string, ok bool)
}

// Marks requests made in process, which pass without credentials
type localKey struct{}

// Routes requests by model and translates OpenAI, Anthropic, and Ollama protocols.
type Gateway struct {
	table     *Table
	keys      []string
	creds     Credentials
	origins   []string
	listeners []*v1.Listener
	tls       bool
	version   string
	log       *slog.Logger
	traces    *Recorder

	mu         sync.Mutex
	transports map[uint32]*http.Transport
	// Active and completed video jobs by ID.
	videos *videoStore
	// Connections to other members, for routes conducted elsewhere and relay slot moves
	peers Peers
	// Which seat a conversation's prefix was last sent to, so its cache answers again
	affinity *affinityTable
	// Video jobs forwarded to other members, by job id
	jobs *forwardedJobs
	// Sequence slots in use on llama.cpp relay seats
	slots *slotPool
}

// Creates a gateway with bearer keys, default route policy, and allowed origins.
// Records requests and publishes recent traces.
func New(table *Table, keys, origins []string, policy *v1.Policy, bus *events.Bus, log *slog.Logger) *Gateway {
	table.SetDefaults(policy)
	return &Gateway{table: table, keys: keys, origins: origins, log: log, traces: NewRecorder(bus, traceRing, countRing), transports: map[uint32]*http.Transport{}, videos: newVideoStore(), affinity: newAffinity(), jobs: newForwardedJobs(), slots: newSlotPool()}
}

// Returns the request recorder
func (g *Gateway) Traces() *Recorder { return g.traces }

// Sets gateway identity for clients.
func (g *Gateway) SetVersion(v string) { g.version = v }

// Requires credentials whenever they are enabled, and accepts them where a gateway key would do.
func (g *Gateway) SetCredentials(c Credentials) { g.creds = c }

// Whether requests must carry a gateway key or a daemon credential
func (g *Gateway) required() bool {
	return len(g.keys) > 0 || (g.creds != nil && g.creds.Enabled())
}

// Caches transports by response header timeout.
func (g *Gateway) transport(upstreamMs uint32) *http.Transport {
	g.mu.Lock()
	defer g.mu.Unlock()
	t, ok := g.transports[upstreamMs]
	if !ok {
		t = http.DefaultTransport.(*http.Transport).Clone()
		t.ResponseHeaderTimeout = time.Duration(upstreamMs) * time.Millisecond
		g.transports[upstreamMs] = t
	}
	return t
}

// Records listener addresses and TLS status.
func (g *Gateway) SetListeners(listeners []*v1.Listener, tls bool) {
	g.listeners, g.tls = listeners, tls
}

// Returns the route table
func (g *Gateway) Table() *Table { return g.table }

// Reports listeners, routes, and counters
func (g *Gateway) Status() *v1.GatewayStatus {
	return &v1.GatewayStatus{Listeners: g.listeners, Routes: g.table.List(), Auth: g.required(), Requests: g.table.Requests(), Policy: g.table.Defaults(), Tls: g.tls}
}

// Registers gateway routes on a mux
func (g *Gateway) Mount(mux *http.ServeMux) {
	mux.HandleFunc(healthPath, g.cors(g.health))
	mux.HandleFunc(modelsPath, g.cors(g.auth(g.models)))
	mux.HandleFunc(modelsPath+"/", g.cors(g.auth(g.model)))
	mux.HandleFunc(ollamaTags, g.cors(g.auth(g.models)))
	mux.HandleFunc(ollamaPs, g.cors(g.auth(g.models)))
	mux.HandleFunc(ollamaShow, g.cors(g.auth(g.show)))
	mux.HandleFunc(ollamaVersion, g.cors(g.about))
	mux.HandleFunc(prefix, g.cors(g.auth(g.proxy)))
	mux.HandleFunc(ollamaPrefix, g.cors(g.auth(g.proxy)))
	mux.HandleFunc(sdcppPrefix, g.cors(g.auth(g.proxy)))
}

// Handles CORS headers and preflight requests.
func (g *Gateway) cors(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" && g.originAllowed(origin) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
			// Session cookies ride along only from the daemon's own host or a listed origin.
			if g.credentialed(origin, r) {
				w.Header().Set("Access-Control-Allow-Credentials", "true")
			}
			// Allow requested headers. Bearer keys enforce authentication.
			if asked := r.Header.Get("Access-Control-Request-Headers"); asked != "" {
				w.Header().Set("Access-Control-Allow-Headers", asked)
			}
			w.Header().Set("Access-Control-Expose-Headers", "Retry-After, "+traceHeader)
			w.Header().Set("Access-Control-Max-Age", "600")
			w.Header().Add("Vary", "Origin")
			w.Header().Add("Vary", "Access-Control-Request-Headers")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next(w, r)
	}
}

// Whether browsers at origin may send cookies: the request's own host on any port, or an origin listed by name
func (g *Gateway) credentialed(origin string, r *http.Request) bool {
	for _, o := range g.origins {
		if o != "*" && strings.EqualFold(o, origin) {
			return true
		}
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return strings.EqualFold(u.Hostname(), strings.Trim(host, "[]"))
}

func (g *Gateway) originAllowed(origin string) bool {
	if len(g.origins) == 0 {
		return true
	}
	for _, o := range g.origins {
		if o == "*" || strings.EqualFold(o, origin) {
			return true
		}
	}
	return false
}

// Returns the gateway handler with an Ollama heartbeat at root.
func (g *Gateway) Handler() http.Handler {
	mux := http.NewServeMux()
	g.Mount(mux)
	mux.HandleFunc("/", g.cors(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" || r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		io.WriteString(w, heartbeat)
	}))
	return mux
}

// Reads a bounded body. Oversized requests receive 413 in the client's format.
func readBody(w http.ResponseWriter, r *http.Request, client Flavor) ([]byte, bool) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		var over *http.MaxBytesError
		if errors.As(err, &over) {
			client.Error(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("request body is over %d bytes", over.Limit), "request_too_large")
		} else {
			client.Error(w, http.StatusBadRequest, "could not read request body", "invalid_request_error")
		}
		return nil, false
	}
	return body, true
}

// Rejects requests without a gateway key or a daemon credential whenever either is configured
func (g *Gateway) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if g.required() && !g.authorized(r) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="nebu"`)
			flavorOf(clientFlavor(r)).Error(w, http.StatusUnauthorized, "missing or invalid api key", "authentication_error")
			return
		}
		next(w, r)
	}
}

// In-process callers pass. Others need a gateway key, or a credential the daemon accepts. A
// request another member forwarded here carries that member's session, which passes on its own
func (g *Gateway) authorized(r *http.Request) bool {
	if r.Context().Value(localKey{}) != nil {
		return true
	}
	token := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer"))
	if token == "" {
		token = r.Header.Get("X-Api-Key")
	}
	for _, k := range g.keys {
		if token != "" && subtle.ConstantTimeCompare([]byte(k), []byte(token)) == 1 {
			return true
		}
	}
	if g.creds == nil {
		return false
	}
	if g.creds.Authenticated(r.Header) {
		return true
	}
	if r.Header.Get(entryHeader) != "" {
		_, ok := g.creds.PeerOf(r.Header)
		return ok
	}
	return false
}

func (g *Gateway) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "models": len(g.table.Ready())})
}

// Formats a route as a model for the client's protocol.
func modelEntry(api v1.ApiFlavor, rt *v1.Route) map[string]any {
	switch api {
	case v1.ApiFlavor_API_FLAVOR_OLLAMA:
		return map[string]any{
			"name":        rt.GetName(),
			"model":       rt.GetName(),
			"modified_at": rt.GetUpdatedAt().AsTime().UTC().Format(time.RFC3339Nano),
			"size":        0,
			"digest":      "",
			"details":     map[string]any{"family": rt.GetModel(), "format": "", "parameter_size": "", "quantization_level": ""},
		}
	case v1.ApiFlavor_API_FLAVOR_ANTHROPIC:
		return map[string]any{"id": rt.GetName(), "type": "model", "display_name": rt.GetName(), "created_at": rt.GetUpdatedAt().AsTime().UTC().Format(time.RFC3339)}
	}
	return map[string]any{
		"id":           rt.GetName(),
		"object":       "model",
		"created":      rt.GetUpdatedAt().AsTime().Unix(),
		"owned_by":     "nebu",
		"ready":        rt.GetState() == v1.RouteState_ROUTE_STATE_READY,
		"capabilities": capabilitiesOf(rt),
	}
}

// Reports chat, tool, image, and video capabilities for a route.
func capabilitiesOf(rt *v1.Route) []string {
	if rt.GetApi() != v1.ApiFlavor_API_FLAVOR_SDCPP {
		return []string{"completion", "tools"}
	}
	var out []string
	for _, mode := range rt.GetModes() {
		switch mode {
		case "img_gen":
			out = append(out, "images")
		case "vid_gen":
			out = append(out, "videos")
		}
	}
	if out == nil {
		out = []string{}
	}
	return out
}

// Lists routes as models in the client's format.
func (g *Gateway) models(w http.ResponseWriter, r *http.Request) {
	api := clientFlavor(r)
	var routes []*v1.Route
	data := []map[string]any{}
	for _, rt := range g.table.List() {
		if rt.GetState() == v1.RouteState_ROUTE_STATE_DRAINING || r.URL.Path == ollamaPs && rt.GetState() != v1.RouteState_ROUTE_STATE_READY {
			continue
		}
		routes = append(routes, rt)
		data = append(data, modelEntry(api, rt))
	}
	switch api {
	case v1.ApiFlavor_API_FLAVOR_OLLAMA:
		writeJSON(w, http.StatusOK, map[string]any{"models": data})
	case v1.ApiFlavor_API_FLAVOR_ANTHROPIC:
		resp := map[string]any{"data": data, "has_more": false}
		if len(routes) > 0 {
			resp["first_id"], resp["last_id"] = routes[0].GetName(), routes[len(routes)-1].GetName()
		}
		writeJSON(w, http.StatusOK, resp)
	default:
		writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": data})
	}
}

// Returns a model from /v1/models/ in the client's format.
func (g *Gateway) model(w http.ResponseWriter, r *http.Request) {
	api := clientFlavor(r)
	name, _ := url.PathUnescape(strings.TrimPrefix(r.URL.Path, modelsPath+"/"))
	rt, ok := g.table.Lookup(name)
	if !ok || rt.GetState() == v1.RouteState_ROUTE_STATE_DRAINING {
		flavorOf(api).Error(w, http.StatusNotFound, "model "+name+" not found", "model_not_found")
		return
	}
	writeJSON(w, http.StatusOK, modelEntry(api, rt))
}

// Builds Ollama's show response from route metadata.
func (g *Gateway) show(w http.ResponseWriter, r *http.Request) {
	client := flavors[v1.ApiFlavor_API_FLAVOR_OLLAMA]
	body, ok := readBody(w, r, client)
	if !ok {
		return
	}
	var req struct {
		Model string `json:"model"`
		Name  string `json:"name"`
	}
	json.Unmarshal(body, &req)
	name := req.Model
	if name == "" {
		name = req.Name
	}
	rt, ok := g.table.Lookup(name)
	if !ok {
		client.Error(w, http.StatusNotFound, "model "+name+" not found", "model_not_found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"modelfile":    "",
		"parameters":   "",
		"template":     "",
		"details":      map[string]any{"family": rt.GetModel(), "format": "", "parameter_size": "", "quantization_level": ""},
		"model_info":   map[string]any{"nebu.instance": rt.GetInstanceId(), "nebu.slot": rt.GetSlotId(), "nebu.model": rt.GetModel()},
		"capabilities": capabilitiesOf(rt),
	})
}

func (g *Gateway) about(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"version": g.version})
}

// Starts a request trace with caller, model, and protocol.
func (g *Gateway) trace(r *http.Request, name string, body []byte, api v1.ApiFlavor) *v1.Trace {
	t := &v1.Trace{
		Id:           db.NewID(),
		Route:        name,
		ClientApi:    api,
		Path:         r.URL.Path,
		Kind:         kindOf(api, r.URL.Path),
		StartedAt:    timestamppb.Now(),
		RequestBytes: uint64(len(body)),
		Request:      capped(body),
		Remote:       r.RemoteAddr,
	}
	if t.Kind != v1.TraceKind_TRACE_KIND_OTHER {
		if chat, err := flavorOf(api).ParseRequest(r.URL.Path, body); err == nil {
			t.Stream = chat.Stream
		}
	}
	g.traces.Start(t)
	return t
}

// Returns an error in the client's format and closes the trace.
func (g *Gateway) refuse(w *traceWriter, client Flavor, status int, message, kind string) {
	w.t.Error = message
	client.Error(w, status, message, kind)
}

func (g *Gateway) proxy(rw http.ResponseWriter, r *http.Request) {
	client := flavorOf(clientFlavor(r))
	// Video operations use the job ID.
	if strings.HasPrefix(r.URL.Path, videosPath) && r.Method != http.MethodPost {
		g.video(rw, r, client)
		return
	}
	body, ok := readBody(rw, r, client)
	if !ok {
		return
	}
	name := modelName(r, body)
	t := g.trace(r, name, body, clientFlavor(r))
	rw.Header().Set(traceHeader, t.GetId())
	w := &traceWriter{ResponseWriter: rw, t: t}
	defer func() {
		if w.detached {
			return
		}
		t.Status, t.ResponseBytes = uint32(w.status), w.bytes
		g.traces.Finish(t)
	}()
	// Exclude token counts from served request counters.
	served := t.GetKind() != v1.TraceKind_TRACE_KIND_COUNT
	route, policy, release, err := g.table.Acquire(name, served)
	if errors.Is(err, ErrNoRoute) && name == "" {
		if ready := g.table.Ready(); len(ready) == 1 {
			name = ready[0].GetName()
			t.Route = name
			route, policy, release, err = g.table.Acquire(name, served)
		}
	}
	switch {
	case errors.Is(err, ErrPending):
		w.Header().Set("Retry-After", retryAfter)
		g.refuse(w, client, http.StatusServiceUnavailable, "model "+name+" is starting, retry shortly", "model_starting")
		return
	case errors.Is(err, ErrDraining):
		w.Header().Set("Retry-After", retryAfter)
		g.refuse(w, client, http.StatusServiceUnavailable, "model "+name+" is being replaced, retry shortly", "model_swapping")
		return
	case errors.Is(err, ErrBusy):
		w.Header().Set("Retry-After", retryAfter)
		g.refuse(w, client, http.StatusTooManyRequests, "model "+name+" has every allowed request in flight, retry shortly", "rate_limit_error")
		return
	case errors.Is(err, ErrThrottled):
		w.Header().Set("Retry-After", "1")
		g.refuse(w, client, http.StatusTooManyRequests, "model "+name+" is over its request rate, retry shortly", "rate_limit_error")
		return
	case err != nil:
		g.refuse(w, client, http.StatusNotFound, "model "+name+" is not running, run it with nebu run", "model_not_found")
		return
	}
	t.InstanceId, t.SlotId, t.UpstreamApi = route.GetInstanceId(), route.GetSlotId(), route.GetApi()
	// A route conducted elsewhere is answered by that node's gateway.
	entry := r.Header.Get(entryHeader)
	if route.GetForwarded() {
		defer release()
		if entry != "" {
			g.refuse(w, client, http.StatusBadGateway, "model "+name+" is forwarded here from "+entry+" and forwarded on from here, so no node answers it", "server_error")
			return
		}
		g.forward(w, r, body, name, route, policy, client)
		return
	}
	if g.peers != nil && route.GetFormationId() != "" {
		t.NodeId = g.peers.Self()
	}
	// Hold the route while the native media job runs, one job per seat for replicas.
	if t.GetKind() == v1.TraceKind_TRACE_KIND_IMAGE || t.GetKind() == v1.TraceKind_TRACE_KIND_VIDEO {
		targets, _, err := g.targets(route, r.URL.Path, body, client, entry, 1)
		if err != nil {
			release()
			g.refuseSeats(w, client, name, err)
			return
		}
		hold := g.holder(route, targets)
		g.media(w, r, body, name, route, policy, targets, hold, func() {
			hold.done()
			release()
		})
		return
	}
	defer release()
	if route.GetShape() == v1.Shape_SHAPE_RELAY && len(route.GetSeats()) >= 2 && (t.GetKind() == v1.TraceKind_TRACE_KIND_CHAT || t.GetKind() == v1.TraceKind_TRACE_KIND_GENERATE) {
		g.relay(w, r, body, name, route, policy, client)
		return
	}
	targets, key, err := g.targets(route, r.URL.Path, body, client, entry, policy.GetMaxInFlight())
	if err != nil {
		g.refuseSeats(w, client, name, err)
		return
	}
	hold := g.holder(route, targets)
	defer hold.done()
	g.dispatch(w, r, body, name, route, policy, client, targets, hold, key)
}

// Answers a request no seat can take: busy seats with 429, none ready with 503, and a route
// whose seats cannot be picked with 502
func (g *Gateway) refuseSeats(w *traceWriter, client Flavor, name string, err error) {
	switch {
	case errors.Is(err, errSeatsBusy):
		w.Header().Set("Retry-After", retryAfter)
		g.refuse(w, client, http.StatusTooManyRequests, "model "+name+" has every allowed request in flight on every seat, retry shortly", "rate_limit_error")
	case errors.Is(err, errNoSeat):
		w.Header().Set("Retry-After", retryAfter)
		g.refuse(w, client, http.StatusServiceUnavailable, "model "+name+" has no seat ready, retry shortly", "model_starting")
	default:
		g.refuse(w, client, http.StatusBadGateway, err.Error(), "server_error")
	}
}

// Sends a request to the first target that takes it, translating when the client's protocol
// differs from the runtime's, and records the seat that served it
func (g *Gateway) dispatch(w *traceWriter, r *http.Request, body []byte, name string, route *v1.Route, policy *v1.Policy, client Flavor, targets []target, hold *seatHold, key string) {
	t := w.t
	mode := g.table.SystemMode(route)
	// Translate when client and runtime protocols differ.
	if clientFlavor(r) != route.GetApi() || r.URL.Path == anthropicCount {
		t.Translated = true
		used := g.translate(w, r, body, name, route.GetServed(), mode, targets, hold, policy, client, flavorOf(route.GetApi()))
		g.served(t, route, targets, used, key)
		return
	}
	var err error
	// Replace the route alias with the runtime's model name.
	if served := route.GetServed(); served != "" && served != name {
		if body, err = renameModel(body, served); err != nil {
			g.refuse(w, client, http.StatusBadRequest, err.Error(), "invalid_request_error")
			return
		}
	}
	// Apply the route's system message policy.
	if bytes.HasPrefix(bytes.TrimSpace(body), []byte("{")) {
		shaped, err := rewriteSystem(body, mode)
		if err != nil {
			g.refuse(w, client, http.StatusBadRequest, err.Error(), "invalid_request_error")
			return
		}
		if !bytes.Equal(shaped, body) {
			t.UpstreamRequest = capped(shaped)
		}
		body = shaped
	}
	// Parse tokens and text into the trace during proxying. Keep a response prefix
	// for unsupported paths or unreadable bodies.
	upstream := flavorOf(route.GetApi())
	chat, perr := (*Chat)(nil), error(nil)
	if t.Kind != v1.TraceKind_TRACE_KIND_OTHER {
		chat, perr = upstream.ParseRequest(r.URL.Path, body)
	}
	if chat != nil && perr == nil {
		reader := readPassthrough(t, upstream, chat)
		w.tee = reader.pw
		defer reader.close()
	} else {
		raw := &rawCapture{}
		w.tee = raw
		defer func() { t.Response = raw.buf.String() }()
	}
	// Apply the request timeout to the entire exchange.
	ctx := r.Context()
	if d := policy.GetRequestTimeoutMs(); d > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(d)*time.Millisecond)
		defer cancel()
	}
	first := targets[0].url
	hops := &failover{targets: targets, hold: hold, next: &sameHostRedirects{next: g.transport(policy.GetUpstreamTimeoutMs()), body: body}, log: g.log}
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(first)
			pr.Out.URL.Path = r.URL.Path
			pr.Out.URL.RawPath = r.URL.RawPath
			pr.Out.Host = first.Host
			// The credential headers stay with the gateway.
			pr.Out.Header.Del("Authorization")
			pr.Out.Header.Del("X-Api-Key")
			pr.Out.Header.Del("Cookie")
			pr.Out.Header.Del(entryHeader)
		},
		Transport:     hops,
		FlushInterval: -1,
		ErrorHandler:  func(w http.ResponseWriter, _ *http.Request, err error) { g.upstreamError(w, t, client, name, err) },
	}
	r = r.WithContext(ctx)
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	rp.ServeHTTP(w, r)
	g.served(t, route, targets, hops.used, key)
}

// Sends a rendered request with policy timeouts. The client's capability and
// session headers go with it.
func (g *Gateway) send(ctx context.Context, target *url.URL, path string, out []byte, stream bool, policy *v1.Policy, from http.Header) (*http.Response, context.CancelFunc, error) {
	cancel := context.CancelFunc(func() {})
	if d := policy.GetRequestTimeoutMs(); d > 0 {
		ctx, cancel = context.WithTimeout(ctx, time.Duration(d)*time.Millisecond)
	}
	ref, err := url.Parse(path)
	if err != nil {
		cancel()
		return nil, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target.ResolveReference(ref).String(), bytes.NewReader(out))
	if err != nil {
		cancel()
		return nil, nil, err
	}
	forwardHeaders(req.Header, from)
	req.Header.Set("Content-Type", "application/json")
	if stream {
		req.Header.Set("Accept", "text/event-stream, application/x-ndjson, application/json")
	}
	resp, err := (&sameHostRedirects{next: g.transport(policy.GetUpstreamTimeoutMs()), body: out}).RoundTrip(req)
	if err != nil {
		cancel()
		return nil, nil, err
	}
	return resp, cancel, nil
}

// Copies the headers a client uses to announce capabilities and identify its
// session, as they are. The credential headers stay with the gateway.
func forwardHeaders(to, from http.Header) {
	for name, values := range from {
		if strings.HasPrefix(name, "Anthropic-") || strings.HasPrefix(name, "X-Claude-Code-") {
			to[name] = append([]string(nil), values...)
		}
	}
}

// Copies the headers a client reads for retries and usage limits
func relayHeaders(to, from http.Header) {
	for name, values := range from {
		if strings.HasPrefix(name, "Anthropic-") || name == "Retry-After" || name == "X-Should-Retry" {
			to[name] = append([]string(nil), values...)
		}
	}
}

// Translates requests and responses between protocols, returning the index of the target that
// served, or -1 when none did
func (g *Gateway) translate(w *traceWriter, r *http.Request, body []byte, name, served string, mode v1.SystemMessages, targets []target, hold *seatHold, policy *v1.Policy, client, upstream Flavor) int {
	t := w.t
	chat, err := client.ParseRequest(r.URL.Path, body)
	if err != nil {
		g.refuse(w, client, http.StatusBadRequest, err.Error(), "invalid_request_error")
		return -1
	}
	// Use the runtime model name upstream and the requested alias in responses.
	if served != "" {
		chat.Model = served
	}
	foldSystem(chat, mode)
	if chat.Kind == "count" {
		hold.to(0)
		g.count(w, r, chat, name, targets[0].url, policy, client, upstream)
		return 0
	}
	if upstream.InlineImages() {
		if err := g.inlineImages(r.Context(), chat, policy); err != nil {
			g.refuse(w, client, http.StatusBadRequest, err.Error(), "invalid_request_error")
			return -1
		}
	}
	path, out, err := upstream.RenderRequest(chat)
	if err != nil {
		g.refuse(w, client, http.StatusBadRequest, err.Error(), "invalid_request_error")
		return -1
	}
	t.UpstreamRequest = capped(out)
	chat.Model = name
	return g.exchange(w, r, chat, name, path, out, targets, hold, policy, client, upstream)
}

// Opens a rendered request at the first target that takes it: a seat that refuses or fails
// before answering passes the request to the next, once, the count following the request.
// Returns the answer, its cancel, and the index of the target that answered
func (g *Gateway) open(ctx context.Context, targets []target, hold *seatHold, path string, out []byte, stream bool, policy *v1.Policy, from http.Header, name string) (*http.Response, context.CancelFunc, int, error) {
	for i, tg := range targets {
		hold.to(i)
		resp, cancel, err := g.send(ctx, tg.url, path, out, stream, policy, from)
		last := i == len(targets)-1
		switch {
		case err != nil && !last && retryable(err):
			g.log.Warn("seat did not answer, passing the request to the next", "model", name, "seat", tg.url.Host, "err", err)
			continue
		case err != nil:
			return nil, nil, i, err
		case !last && refused(resp.StatusCode):
			io.Copy(io.Discard, io.LimitReader(resp.Body, maxBody))
			resp.Body.Close()
			cancel()
			g.log.Warn("seat refused, passing the request to the next", "model", name, "seat", tg.url.Host, "status", resp.StatusCode)
			continue
		}
		return resp, cancel, i, nil
	}
	return nil, nil, -1, errors.New("no seat to send to")
}

// Sends a rendered request to the first target that takes it and returns its answer in the
// client's format, streaming when the client asked to. Returns the index of the target that
// answered, or -1 when none did
func (g *Gateway) exchange(w *traceWriter, r *http.Request, chat *Chat, name, path string, out []byte, targets []target, hold *seatHold, policy *v1.Policy, client, upstream Flavor) int {
	resp, cancel, used, err := g.open(r.Context(), targets, hold, path, out, chat.Stream, policy, r.Header, name)
	if err != nil {
		g.upstreamError(w, w.t, client, name, err)
		return -1
	}
	g.deliver(w, r, chat, name, resp, cancel, client, upstream)
	return used
}

// Returns an opened answer to the client in its format, streaming when it asked to
func (g *Gateway) deliver(w *traceWriter, r *http.Request, chat *Chat, name string, resp *http.Response, cancel context.CancelFunc, client, upstream Flavor) {
	t := w.t
	defer cancel()
	defer resp.Body.Close()
	t.FirstByteAt = timestamppb.Now()
	relayHeaders(w.Header(), resp.Header)
	if resp.StatusCode >= http.StatusMultipleChoices {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxBody))
		message := upstream.ErrorMessage(raw)
		if message == "" {
			message = strings.TrimSpace(string(raw))
		}
		// The runtime's own words reach the client, which matches on them to recover.
		t.Error = "upstream: " + message
		client.Error(w, resp.StatusCode, message, errorKind(resp.StatusCode, raw))
		return
	}
	sink := &traceSink{t: t}
	defer sink.close()
	if chat.Stream {
		sw := tracedStream{StreamWriter: client.Stream(w, chat), sink: sink}
		if err := upstream.ParseStream(resp.Body, sw.Write); err != nil {
			switch {
			case r.Context().Err() != nil:
				// The client hung up. There is no one left to tell.
				t.Stop, t.Error = "cancelled", "client went away"
				g.log.Info("gateway stream", "model", name, "err", t.Error)
			case errors.Is(err, context.DeadlineExceeded):
				message := fmt.Sprintf("model %s did not answer within its timeout", name)
				g.log.Warn("gateway stream", "model", name, "err", message)
				sw.Write(Event{Kind: "error", Text: message})
			default:
				g.log.Warn("gateway stream", "model", name, "err", err)
				t.Error = "upstream: " + err.Error()
				sw.Write(Event{Kind: "error", Text: err.Error()})
			}
		}
		sw.Close()
		return
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		g.refuse(w, client, http.StatusBadGateway, "upstream error: "+err.Error(), "server_error")
		return
	}
	res, err := upstream.ParseResult(chat, raw)
	if err != nil {
		if refusal, ok := errors.AsType[*upstreamRefusal](err); ok {
			t.Error = "upstream: " + refusal.message
			client.Error(w, refusal.status, refusal.message, errorKind(refusal.status, raw))
			return
		}
		g.refuse(w, client, http.StatusBadGateway, "upstream answered in a shape the gateway could not read: "+err.Error(), "server_error")
		return
	}
	res.Model = name
	sink.first()
	sink.result(res)
	g.answer(w, client, chat, res)
}

// Writes a complete response in the client's format
func (g *Gateway) answer(w *traceWriter, client Flavor, chat *Chat, res *Result) {
	answer, err := client.RenderResult(chat, res)
	if err != nil {
		var args *toolArgsError
		if errors.As(err, &args) {
			g.refuse(w, client, http.StatusBadGateway, "upstream: "+err.Error(), "upstream_error")
			return
		}
		g.refuse(w, client, http.StatusInternalServerError, err.Error(), "server_error")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(answer)
}

// Maps runtime exchange timeouts to 504 and other failures to 502.
func (g *Gateway) upstreamError(w http.ResponseWriter, t *v1.Trace, client Flavor, name string, err error) {
	g.log.Warn("gateway upstream", "model", name, "err", err)
	message := "upstream error: " + err.Error()
	status := http.StatusBadGateway
	kind := "server_error"
	if errors.Is(err, context.DeadlineExceeded) || isTimeout(err) {
		message, status, kind = fmt.Sprintf("model %s did not answer within its timeout", name), http.StatusGatewayTimeout, "timeout_error"
	}
	if t.Error == "" {
		t.Error = message
	}
	client.Error(w, status, message, kind)
}

// Uses the runtime tokenizer when available, otherwise estimates tokens.
func (g *Gateway) count(w *traceWriter, r *http.Request, chat *Chat, name string, target *url.URL, policy *v1.Policy, client, upstream Flavor) {
	res := &Result{Model: name}
	n, err := g.countUpstream(r.Context(), chat, target, policy, upstream, r.Header)
	if err != nil {
		g.log.Debug("gateway count estimated", "model", name, "err", err)
		n = estimateTokens(chat)
	} else {
		n = withImageTokens(upstream, chat, n)
	}
	res.In = n
	w.t.PromptTokens = uint32(n)
	g.answer(w, client, chat, res)
}

func (g *Gateway) countUpstream(ctx context.Context, chat *Chat, target *url.URL, policy *v1.Policy, upstream Flavor, from http.Header) (int, error) {
	path, out, err := upstream.RenderRequest(chat)
	if err != nil {
		return 0, err
	}
	resp, cancel, err := g.send(ctx, target, path, out, false, policy, from)
	if err != nil {
		return 0, err
	}
	defer cancel()
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return 0, err
	}
	if resp.StatusCode >= http.StatusMultipleChoices {
		return 0, fmt.Errorf("tokenizer answered %d", resp.StatusCode)
	}
	res, err := upstream.ParseResult(chat, raw)
	if err != nil {
		return 0, err
	}
	return res.In, nil
}

// Fetches image URLs for protocols that require image bytes.
func (g *Gateway) inlineImages(ctx context.Context, chat *Chat, policy *v1.Policy) error {
	client := &http.Client{Transport: g.transport(policy.GetUpstreamTimeoutMs())}
	for mi := range chat.Messages {
		for pi, p := range chat.Messages[mi].Parts {
			if p.Type != "image" || p.Data != "" || p.URL == "" {
				continue
			}
			if mt, raw, ok := dataURL(p.URL); ok {
				chat.Messages[mi].Parts[pi].MediaType, chat.Messages[mi].Parts[pi].Data = mt, raw
				continue
			}
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.URL, nil)
			if err != nil {
				return fmt.Errorf("image %s: %w", p.URL, err)
			}
			resp, err := client.Do(req)
			if err != nil {
				return fmt.Errorf("image %s: %w", p.URL, err)
			}
			raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
			resp.Body.Close()
			if err != nil {
				return fmt.Errorf("image %s: %w", p.URL, err)
			}
			if resp.StatusCode >= http.StatusMultipleChoices {
				return fmt.Errorf("image %s answered %d", p.URL, resp.StatusCode)
			}
			chat.Messages[mi].Parts[pi].Data = base64.StdEncoding.EncodeToString(raw)
			chat.Messages[mi].Parts[pi].MediaType = mediaTypeOf(raw, resp.Header.Get("Content-Type"))
		}
	}
	return nil
}

// Rewrites the JSON model field, preserving other fields.
func renameModel(body []byte, model string) ([]byte, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return nil, fmt.Errorf("body is not a JSON object: %w", err)
	}
	if _, ok := fields["model"]; !ok {
		return body, nil
	}
	fields["model"], _ = json.Marshal(model)
	return json.Marshal(fields)
}

func isTimeout(err error) bool {
	var t interface{ Timeout() bool }
	return errors.As(err, &t) && t.Timeout()
}

// Finds the model in the header, query, or body
func modelName(r *http.Request, body []byte) string {
	if v := r.Header.Get(modelHeader); v != "" {
		return v
	}
	if v := r.URL.Query().Get("model"); v != "" {
		return v
	}
	if len(body) > 0 && strings.HasPrefix(strings.TrimSpace(r.Header.Get("Content-Type")), "application/json") || bytes.HasPrefix(bytes.TrimSpace(body), []byte("{")) {
		var probe struct {
			Model string `json:"model"`
		}
		if json.Unmarshal(body, &probe) == nil {
			return probe.Model
		}
	}
	// Image edits carry the model in a multipart field.
	if form, err := parseForm(r, body); err == nil {
		return form.value("model")
	}
	return ""
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// Follows same-upstream redirects, including trailing-slash redirects, to keep
// backend loopback URLs out of client responses.
type sameHostRedirects struct {
	next http.RoundTripper
	body []byte
}

const maxRedirects = 3

func (t *sameHostRedirects) RoundTrip(req *http.Request) (*http.Response, error) {
	for hop := 0; ; hop++ {
		resp, err := t.next.RoundTrip(req)
		if err != nil {
			return nil, err
		}
		if hop >= maxRedirects || (resp.StatusCode != http.StatusTemporaryRedirect && resp.StatusCode != http.StatusPermanentRedirect && resp.StatusCode != http.StatusMovedPermanently && resp.StatusCode != http.StatusFound) {
			return resp, nil
		}
		location, err := resp.Location()
		if err != nil || location.Host != req.URL.Host {
			return resp, nil
		}
		resp.Body.Close()
		next := req.Clone(req.Context())
		next.URL = location
		next.Body = io.NopCloser(bytes.NewReader(t.body))
		next.ContentLength = int64(len(t.body))
		req = next
	}
}
