// Package gateway exposes ready instances behind one endpoint in the OpenAI, Anthropic, and Ollama formats.
package gateway

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

const (
	prefix      = "/v1/"
	modelsPath  = "/v1/models"
	healthPath  = "/health"
	maxBody     = 64 << 20
	modelHeader = "X-Nebu-Model"
	retryAfter  = "2"
	// What the Ollama CLI expects from the root before it talks to a server
	heartbeat = "Ollama is running"
)

// Reverse proxy keyed by the model field of each request, answering in
// the OpenAI, Anthropic, and Ollama wire formats whatever the runtime speaks
type Gateway struct {
	table     *Table
	keys      []string
	origins   []string
	listeners []*v1.Listener
	tls       bool
	version   string
	log       *slog.Logger

	mu         sync.Mutex
	transports map[uint32]*http.Transport
}

// Builds the gateway, requiring a bearer key when keys exist, with the policy routes inherit and the origins browsers may call from
func New(table *Table, keys, origins []string, policy *v1.Policy, log *slog.Logger) *Gateway {
	table.SetDefaults(policy)
	return &Gateway{table: table, keys: keys, origins: origins, log: log, transports: map[uint32]*http.Transport{}}
}

// Records what the gateway says it is, for clients that ask
func (g *Gateway) SetVersion(v string) { g.version = v }

// Returns a transport that gives the runtime this long to start answering, one per timeout
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

// Records the addresses the gateway answers on and whether they speak TLS
func (g *Gateway) SetListeners(listeners []*v1.Listener, tls bool) {
	g.listeners, g.tls = listeners, tls
}

// Returns the route table
func (g *Gateway) Table() *Table { return g.table }

// Reports listeners, routes, and counters
func (g *Gateway) Status() *v1.GatewayStatus {
	return &v1.GatewayStatus{Listeners: g.listeners, Routes: g.table.List(), Auth: len(g.keys) > 0, Requests: g.table.Requests(), Policy: g.table.Defaults(), Tls: g.tls}
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
}

// Lets browsers on other origins call the gateway, answering preflights itself
func (g *Gateway) cors(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" && g.originAllowed(origin) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			// Whatever headers the client's SDK asks to send, the key is what guards the gateway
			if asked := r.Header.Get("Access-Control-Request-Headers"); asked != "" {
				w.Header().Set("Access-Control-Allow-Headers", asked)
			}
			w.Header().Set("Access-Control-Expose-Headers", "Retry-After")
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

// Returns a handler serving only the gateway, its root answering the Ollama CLI's heartbeat
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

// Reads a body up to the cap, answering 413 in the caller's flavor past it and false
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

// Rejects requests without a configured key when keys are set
func (g *Gateway) auth(next http.HandlerFunc) http.HandlerFunc {
	if len(g.keys) == 0 {
		return next
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if !g.authorized(r) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="nebu"`)
			flavorOf(clientFlavor(r)).Error(w, http.StatusUnauthorized, "missing or invalid api key", "authentication_error")
			return
		}
		next(w, r)
	}
}

func (g *Gateway) authorized(r *http.Request) bool {
	token := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer"))
	if token == "" {
		token = r.Header.Get("X-Api-Key")
	}
	if token == "" {
		return false
	}
	for _, k := range g.keys {
		if subtle.ConstantTimeCompare([]byte(k), []byte(token)) == 1 {
			return true
		}
	}
	return false
}

func (g *Gateway) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "models": len(g.table.Ready())})
}

// Describes one route as a model in the shape the flavor expects
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
		"id":       rt.GetName(),
		"object":   "model",
		"created":  rt.GetUpdatedAt().AsTime().Unix(),
		"owned_by": "nebu",
		"ready":    rt.GetState() == v1.RouteState_ROUTE_STATE_READY,
	}
}

// Lists routes as models in the shape the caller's flavor expects
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

// Answers one model by the name after /v1/models/ in the caller's flavor
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

// Answers Ollama's show with what the route table knows
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
		"modelfile":  "",
		"parameters": "",
		"template":   "",
		"details":    map[string]any{"family": rt.GetModel(), "format": "", "parameter_size": "", "quantization_level": ""},
		"model_info": map[string]any{"nebu.instance": rt.GetInstanceId(), "nebu.slot": rt.GetSlotId(), "nebu.model": rt.GetModel()},
		// Every flavor crosses tool calls over, so every route takes tools
		"capabilities": []string{"completion", "tools"},
	})
}

func (g *Gateway) about(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"version": g.version})
}

func (g *Gateway) proxy(w http.ResponseWriter, r *http.Request) {
	client := flavorOf(clientFlavor(r))
	body, ok := readBody(w, r, client)
	if !ok {
		return
	}
	name := modelName(r, body)
	endpoint, api, policy, release, err := g.table.Acquire(name)
	if errors.Is(err, ErrNoRoute) && name == "" {
		if ready := g.table.Ready(); len(ready) == 1 {
			name = ready[0].GetName()
			endpoint, api, policy, release, err = g.table.Acquire(name)
		}
	}
	switch {
	case errors.Is(err, ErrPending):
		w.Header().Set("Retry-After", retryAfter)
		client.Error(w, http.StatusServiceUnavailable, "model "+name+" is starting, retry shortly", "model_starting")
		return
	case errors.Is(err, ErrDraining):
		w.Header().Set("Retry-After", retryAfter)
		client.Error(w, http.StatusServiceUnavailable, "model "+name+" is being replaced, retry shortly", "model_swapping")
		return
	case errors.Is(err, ErrBusy):
		w.Header().Set("Retry-After", retryAfter)
		client.Error(w, http.StatusTooManyRequests, "model "+name+" has every allowed request in flight, retry shortly", "rate_limit_error")
		return
	case errors.Is(err, ErrThrottled):
		w.Header().Set("Retry-After", "1")
		client.Error(w, http.StatusTooManyRequests, "model "+name+" is over its request rate, retry shortly", "rate_limit_error")
		return
	case err != nil:
		client.Error(w, http.StatusNotFound, "model "+name+" is not running, run it with nebu run", "model_not_found")
		return
	}
	defer release()
	target, err := url.Parse(endpoint)
	if err != nil {
		client.Error(w, http.StatusBadGateway, err.Error(), "server_error")
		return
	}
	// A request in the runtime's own format passes through untouched, any other is translated both ways
	if clientFlavor(r) != api {
		g.translate(w, r, body, name, target, policy, client, flavorOf(api))
		return
	}
	// The whole exchange ends at the request timeout, so a hung runtime never holds a request in flight forever
	ctx := r.Context()
	if d := policy.GetRequestTimeoutMs(); d > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(d)*time.Millisecond)
		defer cancel()
	}
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.URL.Path = r.URL.Path
			pr.Out.URL.RawPath = r.URL.RawPath
			pr.Out.Host = target.Host
		},
		Transport:     &sameHostRedirects{next: g.transport(policy.GetUpstreamTimeoutMs()), body: body},
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			g.log.Warn("gateway upstream", "model", name, "err", err)
			if errors.Is(err, context.DeadlineExceeded) || isTimeout(err) {
				client.Error(w, http.StatusGatewayTimeout, fmt.Sprintf("model %s did not answer within its timeout", name), "timeout_error")
				return
			}
			client.Error(w, http.StatusBadGateway, "upstream error: "+err.Error(), "server_error")
		},
	}
	r = r.WithContext(ctx)
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	rp.ServeHTTP(w, r)
}

// Posts a rendered request to the runtime under the policy's timeouts
func (g *Gateway) send(ctx context.Context, target *url.URL, path string, out []byte, stream bool, policy *v1.Policy) (*http.Response, context.CancelFunc, error) {
	cancel := context.CancelFunc(func() {})
	if d := policy.GetRequestTimeoutMs(); d > 0 {
		ctx, cancel = context.WithTimeout(ctx, time.Duration(d)*time.Millisecond)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target.ResolveReference(&url.URL{Path: path}).String(), bytes.NewReader(out))
	if err != nil {
		cancel()
		return nil, nil, err
	}
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

// Serves a request written in one flavor from a runtime that speaks another
func (g *Gateway) translate(w http.ResponseWriter, r *http.Request, body []byte, name string, target *url.URL, policy *v1.Policy, client, upstream Flavor) {
	chat, err := client.ParseRequest(r.URL.Path, body)
	if err != nil {
		client.Error(w, http.StatusBadRequest, err.Error(), "invalid_request_error")
		return
	}
	if chat.Kind == "count" {
		g.count(w, r, chat, name, target, policy, client, upstream)
		return
	}
	path, out, err := upstream.RenderRequest(chat)
	if err != nil {
		client.Error(w, http.StatusBadRequest, err.Error(), "invalid_request_error")
		return
	}
	resp, cancel, err := g.send(r.Context(), target, path, out, chat.Stream, policy)
	if err != nil {
		g.log.Warn("gateway upstream", "model", name, "err", err)
		if errors.Is(err, context.DeadlineExceeded) || isTimeout(err) {
			client.Error(w, http.StatusGatewayTimeout, fmt.Sprintf("model %s did not answer within its timeout", name), "timeout_error")
			return
		}
		client.Error(w, http.StatusBadGateway, "upstream error: "+err.Error(), "server_error")
		return
	}
	defer cancel()
	defer resp.Body.Close()
	if resp.StatusCode >= http.StatusMultipleChoices {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxBody))
		message := upstream.ErrorMessage(raw)
		if message == "" {
			message = strings.TrimSpace(string(raw))
		}
		client.Error(w, resp.StatusCode, "upstream: "+message, "upstream_error")
		return
	}
	if chat.Stream {
		sw := client.Stream(w, chat)
		if err := upstream.ParseStream(resp.Body, sw.Write); err != nil {
			g.log.Warn("gateway stream", "model", name, "err", err)
		}
		sw.Close()
		return
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		client.Error(w, http.StatusBadGateway, "upstream error: "+err.Error(), "server_error")
		return
	}
	res, err := upstream.ParseResult(chat, raw)
	if err != nil {
		client.Error(w, http.StatusBadGateway, "upstream answered in a shape the gateway could not read: "+err.Error(), "server_error")
		return
	}
	if res.Model == "" {
		res.Model = chat.Model
	}
	answer, err := client.RenderResult(chat, res)
	if err != nil {
		client.Error(w, http.StatusInternalServerError, err.Error(), "server_error")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(answer)
}

// Answers a token count from the runtime's tokenizer when it has one, an estimate otherwise
func (g *Gateway) count(w http.ResponseWriter, r *http.Request, chat *Chat, name string, target *url.URL, policy *v1.Policy, client, upstream Flavor) {
	res := &Result{Model: chat.Model}
	n, err := g.countUpstream(r.Context(), chat, target, policy, upstream)
	if err != nil {
		g.log.Debug("gateway count estimated", "model", name, "err", err)
		n = estimateTokens(chat)
	}
	res.In = n
	answer, err := client.RenderResult(chat, res)
	if err != nil {
		client.Error(w, http.StatusInternalServerError, err.Error(), "server_error")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(answer)
}

func (g *Gateway) countUpstream(ctx context.Context, chat *Chat, target *url.URL, policy *v1.Policy, upstream Flavor) (int, error) {
	path, out, err := upstream.RenderRequest(chat)
	if err != nil {
		return 0, err
	}
	resp, cancel, err := g.send(ctx, target, path, out, false, policy)
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
	return ""
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// Follows redirects that stay on the upstream, so a backend that answers a
// path with a redirect to its trailing slash twin, as FastAPI does, is served
// instead of handing the client a Location on a loopback port
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
