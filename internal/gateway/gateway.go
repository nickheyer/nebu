// Package gateway exposes ready instances behind one endpoint in the OpenAI, Anthropic, and Ollama protocols
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
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/nickheyer/nebu/internal/auth"
	"github.com/nickheyer/nebu/internal/chat"
	"github.com/nickheyer/nebu/internal/db"
	"github.com/nickheyer/nebu/internal/tools"
	"github.com/nickheyer/nebu/pkg/events"
	"github.com/nickheyer/nebu/pkg/outbound"
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
	// The root answer the Ollama CLI expects
	heartbeat = "Ollama is running"
)

// Answers whether requests need credentials and whether one carries a valid one, a member's session apart
type Credentials interface {
	Enabled() bool
	Authenticated(http.Header) bool
	PeerOf(http.Header) (nodeID string, ok bool)
}

// Marks requests made in process, which pass without credentials
type localKey struct{}

// A request as the client sent it and, when the path is a chat, parsed once for every step
type inbound struct {
	body []byte
	req  *chat.Request
	// Why there is no request, for the steps that need one
	err error
}

// Routes requests by model and translates between the protocols clients and runtimes speak
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
	// Active and completed video jobs by id
	videos *videoStore
	// Connections to other members, for routes conducted elsewhere and relay slot moves
	peers Peers
	// Which seat a conversation's prefix was last sent to, so its cache answers again
	affinity *affinityTable
	// Video jobs forwarded to other members, by job id
	jobs *forwardedJobs
	// Sequence slots in use on llama.cpp relay seats
	slots *slotPool
	// The tool servers routes, personas, and requests attach
	tools *tools.Registry
}

// Creates a gateway with bearer keys, default route policy, and allowed origins
func New(table *Table, keys, origins []string, policy *v1.Policy, bus *events.Bus, log *slog.Logger) *Gateway {
	table.SetDefaults(policy)
	return &Gateway{table: table, keys: keys, origins: origins, log: log, traces: NewRecorder(bus, traceRing, countRing), transports: map[uint32]*http.Transport{}, videos: newVideoStore(), affinity: newAffinity(), jobs: newForwardedJobs(), slots: newSlotPool(), tools: tools.New(log, bus)}
}

// Returns the request recorder
func (g *Gateway) Traces() *Recorder { return g.traces }

// Sets gateway identity for clients
func (g *Gateway) SetVersion(v string) { g.version = v }

// Requires credentials whenever they are enabled, and accepts them where a gateway key would do
func (g *Gateway) SetCredentials(c Credentials) { g.creds = c }

// Installs the tool servers, so routes, personas, and requests can attach them
func (g *Gateway) SetTools(r *tools.Registry) { g.tools = r }

// Whether requests must carry a gateway key or a daemon credential
func (g *Gateway) required() bool {
	return len(g.keys) > 0 || (g.creds != nil && g.creds.Enabled())
}

// Caches transports by response header timeout
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

// Records listener addresses and TLS status
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
	mux.HandleFunc(chat.OllamaTags, g.cors(g.auth(g.models)))
	mux.HandleFunc(chat.OllamaPs, g.cors(g.auth(g.models)))
	mux.HandleFunc(chat.OllamaShow, g.cors(g.auth(g.show)))
	mux.HandleFunc(chat.OllamaVersion, g.cors(g.about))
	mux.HandleFunc(prefix, g.cors(g.auth(g.proxy)))
	mux.HandleFunc(chat.OllamaPrefix, g.cors(g.auth(g.proxy)))
	mux.HandleFunc(chat.SDCPPPrefix, g.cors(g.auth(g.proxy)))
}

// Handles CORS headers and preflight requests
func (g *Gateway) cors(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" && g.originAllowed(origin) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
			if g.credentialed(origin, r) {
				w.Header().Set("Access-Control-Allow-Credentials", "true")
			}
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

// Whether browsers at origin may send cookies, the request's own host on any port or an origin listed by name
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

// Returns the gateway handler with an Ollama heartbeat at root
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

// Reads a bounded body, an oversized one refused with 413 in the client's protocol
func readBody(w http.ResponseWriter, r *http.Request, client chat.Client) ([]byte, bool) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		var over *http.MaxBytesError
		if errors.As(err, &over) {
			client.Error(w, failure(http.StatusRequestEntityTooLarge, fmt.Sprintf("request body is over %d bytes", over.Limit), chat.CodeTooLarge))
		} else {
			client.Error(w, failure(http.StatusBadRequest, "could not read request body", chat.CodeInvalidRequest))
		}
		return nil, false
	}
	return body, true
}

// A failure with its status, message, and code
func failure(status int, message, code string) chat.Failure {
	return chat.Failure{Status: status, Code: code, Message: message}
}

// A refusal of the request as the client wrote it
func bad(format string, args ...any) error {
	return fmt.Errorf("bad request: "+format, args...)
}

// Rejects requests without a gateway key or a daemon credential whenever either is configured
func (g *Gateway) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if g.required() && !g.authorized(r) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="nebu"`)
			chat.ClientOf(chat.Detect(r)).Error(w, failure(http.StatusUnauthorized, "missing or invalid api key", chat.CodeAuthentication))
			return
		}
		next(w, r)
	}
}

// Whether a request passes, in process, by key, by credential, or by the member session that forwarded it
func (g *Gateway) authorized(r *http.Request) bool {
	if r.Context().Value(localKey{}) != nil {
		return true
	}
	token := auth.Bearer(r.Header)
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

// A route as a model in the client's protocol
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

// Reports chat, tool, image, and video capabilities for a route
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

// Lists routes as models in the client's protocol
func (g *Gateway) models(w http.ResponseWriter, r *http.Request) {
	api := chat.Detect(r)
	var routes []*v1.Route
	data := []map[string]any{}
	for _, rt := range g.table.List() {
		if rt.GetState() == v1.RouteState_ROUTE_STATE_DRAINING || r.URL.Path == chat.OllamaPs && rt.GetState() != v1.RouteState_ROUTE_STATE_READY {
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

// Returns one model in the client's protocol
func (g *Gateway) model(w http.ResponseWriter, r *http.Request) {
	api := chat.Detect(r)
	name, _ := url.PathUnescape(strings.TrimPrefix(r.URL.Path, modelsPath+"/"))
	rt, ok := g.table.Lookup(name)
	if !ok || rt.GetState() == v1.RouteState_ROUTE_STATE_DRAINING {
		chat.ClientOf(api).Error(w, failure(http.StatusNotFound, "model "+name+" not found", chat.CodeModelNotFound))
		return
	}
	writeJSON(w, http.StatusOK, modelEntry(api, rt))
}

// Builds Ollama's show response from route metadata
func (g *Gateway) show(w http.ResponseWriter, r *http.Request) {
	client := chat.ClientOf(v1.ApiFlavor_API_FLAVOR_OLLAMA)
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
		client.Error(w, failure(http.StatusNotFound, "model "+name+" not found", chat.CodeModelNotFound))
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

// Parses the body when the path is a chat, prompt, embedding, or count
func (g *Gateway) read(r *http.Request, client chat.Client, body []byte) *inbound {
	in := &inbound{body: body}
	switch client.Endpoint(r.URL.Path) {
	case chat.Chat, chat.Generate, chat.Embed, chat.Count:
		in.req, in.err = client.Parse(r.URL.Path, body)
	default:
		in.err = bad("%s is not an endpoint the gateway translates", r.URL.Path)
	}
	return in
}

// The trace kind of an endpoint, other for a path proxied unparsed
func traceKind(kind chat.Kind) v1.TraceKind {
	switch kind {
	case chat.Chat:
		return v1.TraceKind_TRACE_KIND_CHAT
	case chat.Generate:
		return v1.TraceKind_TRACE_KIND_GENERATE
	case chat.Embed:
		return v1.TraceKind_TRACE_KIND_EMBED
	case chat.Count:
		return v1.TraceKind_TRACE_KIND_COUNT
	case chat.Image:
		return v1.TraceKind_TRACE_KIND_IMAGE
	case chat.Video:
		return v1.TraceKind_TRACE_KIND_VIDEO
	}
	return v1.TraceKind_TRACE_KIND_OTHER
}

// Starts a request trace with caller, model, protocol, and the body whole
func (g *Gateway) trace(r *http.Request, name string, in *inbound, api v1.ApiFlavor, kind v1.TraceKind) *v1.Trace {
	t := &v1.Trace{
		Id:           db.NewID(),
		Route:        name,
		ClientApi:    api,
		Path:         r.URL.Path,
		Kind:         kind,
		StartedAt:    timestamppb.Now(),
		RequestBytes: uint64(len(in.body)),
		Request:      string(in.body),
		Remote:       r.RemoteAddr,
		Stream:       in.req != nil && in.req.Stream,
	}
	g.traces.Start(t)
	return t
}

// Answers an error in the client's protocol and records it on the trace
func (g *Gateway) refuse(w *traceWriter, client chat.Client, status int, message, code string) {
	w.t.Error = message
	client.Error(w, failure(status, message, code))
}

func (g *Gateway) proxy(rw http.ResponseWriter, r *http.Request) {
	api := chat.Detect(r)
	client := chat.ClientOf(api)
	if strings.HasPrefix(r.URL.Path, chat.OpenAIVideos) && r.Method != http.MethodPost {
		g.video(rw, r, client)
		return
	}
	body, ok := readBody(rw, r, client)
	if !ok {
		return
	}
	in := g.read(r, client, body)
	name := modelName(r, in)
	kind := traceKind(client.Endpoint(r.URL.Path))
	t := g.trace(r, name, in, api, kind)
	rw.Header().Set(traceHeader, t.GetId())
	w := &traceWriter{ResponseWriter: rw, t: t}
	defer func() {
		if w.detached {
			return
		}
		t.Status, t.ResponseBytes = uint32(w.status), w.bytes
		g.traces.Finish(t)
	}()
	served := kind != v1.TraceKind_TRACE_KIND_COUNT
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
		g.refuse(w, client, http.StatusServiceUnavailable, "model "+name+" is starting, retry shortly", chat.CodeModelStarting)
		return
	case errors.Is(err, ErrDraining):
		w.Header().Set("Retry-After", retryAfter)
		g.refuse(w, client, http.StatusServiceUnavailable, "model "+name+" is being replaced, retry shortly", chat.CodeModelSwapping)
		return
	case errors.Is(err, ErrBusy):
		w.Header().Set("Retry-After", retryAfter)
		g.refuse(w, client, http.StatusTooManyRequests, "model "+name+" has every allowed request in flight, retry shortly", chat.CodeRateLimit)
		return
	case errors.Is(err, ErrThrottled):
		w.Header().Set("Retry-After", "1")
		g.refuse(w, client, http.StatusTooManyRequests, "model "+name+" is over its request rate, retry shortly", chat.CodeRateLimit)
		return
	case err != nil:
		g.refuse(w, client, http.StatusNotFound, "model "+name+" is not running, run it with nebu run", chat.CodeModelNotFound)
		return
	}
	t.InstanceId, t.SlotId, t.UpstreamApi = route.GetInstanceId(), route.GetSlotId(), route.GetApi()
	entry := r.Header.Get(entryHeader)
	if route.GetForwarded() {
		defer release()
		if entry != "" {
			g.refuse(w, client, http.StatusBadGateway, "model "+name+" is forwarded here from "+entry+" and forwarded on from here, so no node answers it", chat.CodeServer)
			return
		}
		g.forward(w, r, in, name, api, route, policy, client)
		return
	}
	if g.peers != nil && route.GetFormationId() != "" {
		t.NodeId = g.peers.Self()
	}
	// A media job holds the route until it ends, one job per seat for replicas
	if kind == v1.TraceKind_TRACE_KIND_IMAGE || kind == v1.TraceKind_TRACE_KIND_VIDEO {
		targets, _, err := g.targets(route, in, entry, 1)
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
	x := &exchange{g: g, w: w, r: r, in: in, name: name, api: api, client: client, route: route, policy: policy}
	if route.GetShape() == v1.Shape_SHAPE_RELAY && len(route.GetSeats()) >= 2 && (kind == v1.TraceKind_TRACE_KIND_CHAT || kind == v1.TraceKind_TRACE_KIND_GENERATE) {
		x.serveRelay()
		return
	}
	targets, key, err := g.targets(route, in, entry, policy.GetMaxInFlight())
	if err != nil {
		g.refuseSeats(w, client, name, err)
		return
	}
	hold := g.holder(route, targets)
	defer hold.done()
	x.targets, x.hold = targets, hold
	x.open = g.opener(targets, hold, policy, r.Header, name)
	g.served(t, route, targets, x.serve(), key)
}

// Answers a request no seat can take, busy seats as 429, none ready as 503, no pick as 502
func (g *Gateway) refuseSeats(w *traceWriter, client chat.Client, name string, err error) {
	switch {
	case errors.Is(err, errSeatsBusy):
		w.Header().Set("Retry-After", retryAfter)
		g.refuse(w, client, http.StatusTooManyRequests, "model "+name+" has every allowed request in flight on every seat, retry shortly", chat.CodeRateLimit)
	case errors.Is(err, errNoSeat):
		w.Header().Set("Retry-After", retryAfter)
		g.refuse(w, client, http.StatusServiceUnavailable, "model "+name+" has no seat ready, retry shortly", chat.CodeModelStarting)
	default:
		g.refuse(w, client, http.StatusBadGateway, err.Error(), chat.CodeServer)
	}
}

// Opens the runtime's answer to one rendered request, answering the index of the seat that took it
type opener func(ctx context.Context, req *chat.Request, path string, body []byte) (*http.Response, context.CancelFunc, int, error)

// An opener over a route's targets with the client's headers, the count following the request between seats
func (g *Gateway) opener(targets []target, hold *seatHold, policy *v1.Policy, from http.Header, name string) opener {
	return func(ctx context.Context, req *chat.Request, path string, body []byte) (*http.Response, context.CancelFunc, int, error) {
		return g.open(ctx, targets, hold, path, body, req.Stream, policy, from, name)
	}
}

// Sends a rendered request with policy timeouts and the client's headers as a runtime gets them
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
	req.Header = runtimeHeaders(from)
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

// Opens a rendered request at the first target that takes it, a refusal or failure passing it on once
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

// Headers a runtime never gets, credentials, forwarding marks, and what the connection itself sets
var clientOnlyHeaders = map[string]bool{
	"Authorization": true, "X-Api-Key": true, "Cookie": true,
	"Forwarded": true, "X-Forwarded-For": true, "X-Forwarded-Host": true, "X-Forwarded-Proto": true,
	"Content-Length": true, "Accept-Encoding": true, "Expect": true, "Connection": true, "Keep-Alive": true,
	"Proxy-Authenticate": true, "Proxy-Authorization": true, "Proxy-Connection": true, "Te": true, "Trailer": true, "Transfer-Encoding": true, "Upgrade": true,
}

// Removes the headers a runtime never gets, the gateway's own among them
func dropClientOnly(h http.Header) {
	for name := range h {
		if clientOnlyHeaders[name] || strings.HasPrefix(name, "X-Nebu-") {
			delete(h, name)
		}
	}
}

// The client's headers as a runtime gets them
func runtimeHeaders(from http.Header) http.Header {
	out := from.Clone()
	if out == nil {
		out = http.Header{}
	}
	dropClientOnly(out)
	return out
}

// Answers a runtime that could not be reached, timeouts as 504 and the rest as 502
func (g *Gateway) upstreamError(w http.ResponseWriter, t *v1.Trace, client chat.Client, name string, err error) {
	g.log.Warn("gateway upstream", "model", name, "err", err)
	f := upstreamFailure(name, err)
	if t.Error == "" {
		t.Error = f.Message
	}
	client.Error(w, f)
}

// The failure a client gets when the runtime could not be reached
func upstreamFailure(name string, err error) chat.Failure {
	if errors.Is(err, errSlotsBusy) {
		return failure(http.StatusTooManyRequests, "model "+name+" cannot relay now: "+err.Error()+", retry shortly", chat.CodeRateLimit)
	}
	if errors.Is(err, context.DeadlineExceeded) || isTimeout(err) {
		return failure(http.StatusGatewayTimeout, fmt.Sprintf("model %s did not answer within its timeout", name), chat.CodeTimeout)
	}
	return failure(http.StatusBadGateway, "upstream error: "+err.Error(), chat.CodeServer)
}

// Fetches image URLs into bytes for runtimes that take images inline
func (g *Gateway) inlineImages(ctx context.Context, req *chat.Request, policy *v1.Policy) error {
	// Image links come from the client, so the fetch never reaches this host's own services
	transport := outbound.Transport()
	transport.ResponseHeaderTimeout = time.Duration(policy.GetUpstreamTimeoutMs()) * time.Millisecond
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}
	for mi := range req.Messages {
		for pi, p := range req.Messages[mi].Parts {
			if p.Type != "image" || p.Data != "" || p.URL == "" {
				continue
			}
			if mt, raw, ok := chat.DataURL(p.URL); ok {
				req.Messages[mi].Parts[pi].MediaType, req.Messages[mi].Parts[pi].Data = mt, raw
				continue
			}
			hr, err := http.NewRequestWithContext(ctx, http.MethodGet, p.URL, nil)
			if err != nil {
				return fmt.Errorf("image %s: %w", p.URL, err)
			}
			resp, err := client.Do(hr)
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
			req.Messages[mi].Parts[pi].Data = base64.StdEncoding.EncodeToString(raw)
			req.Messages[mi].Parts[pi].MediaType = chat.MediaTypeOf(raw, resp.Header.Get("Content-Type"))
		}
	}
	return nil
}

// Rewrites the JSON model field, preserving other fields
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

// Finds the model in the header, query, the parsed request, or the body
func modelName(r *http.Request, in *inbound) string {
	if v := r.Header.Get(modelHeader); v != "" {
		return v
	}
	if v := r.URL.Query().Get("model"); v != "" {
		return v
	}
	if in.req != nil {
		return in.req.Model
	}
	body := in.body
	if len(body) > 0 && strings.HasPrefix(strings.TrimSpace(r.Header.Get("Content-Type")), "application/json") || bytes.HasPrefix(bytes.TrimSpace(body), []byte("{")) {
		var probe struct {
			Model string `json:"model"`
		}
		if json.Unmarshal(body, &probe) == nil {
			return probe.Model
		}
	}
	// Image edits carry the model in a multipart field
	if form, err := parseForm(r, body); err == nil {
		return form.value("model")
	}
	return ""
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	chat.WriteJSON(w, status, v)
}

// Follows same host redirects, trailing slash ones included, keeping backend URLs out of client answers
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
