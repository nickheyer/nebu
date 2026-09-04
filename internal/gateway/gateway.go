// Package gateway exposes ready instances behind one OpenAI compatible endpoint.
package gateway

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

const (
	prefix      = "/v1/"
	modelsPath  = "/v1/models"
	healthPath  = "/health"
	maxBody     = 64 << 20
	modelHeader = "X-Nebu-Model"
	retryAfter  = "2"
)

// Reverse proxy keyed by the model field of each request
type Gateway struct {
	table     *Table
	keys      []string
	listeners []*v1.Listener
	log       *slog.Logger
}

// Builds the gateway, requiring a bearer key when keys exist
func New(table *Table, keys []string, log *slog.Logger) *Gateway {
	return &Gateway{table: table, keys: keys, log: log}
}

// Records the addresses the gateway answers on
func (g *Gateway) SetListeners(listeners []*v1.Listener) { g.listeners = listeners }

// Returns the route table
func (g *Gateway) Table() *Table { return g.table }

// Reports listeners, routes, and counters
func (g *Gateway) Status() *v1.GatewayStatus {
	return &v1.GatewayStatus{Listeners: g.listeners, Routes: g.table.List(), Auth: len(g.keys) > 0, Requests: g.table.Requests()}
}

// Registers gateway routes on a mux
func (g *Gateway) Mount(mux *http.ServeMux) {
	mux.HandleFunc(healthPath, g.health)
	mux.HandleFunc(modelsPath, g.auth(g.models))
	mux.HandleFunc(prefix, g.auth(g.proxy))
}

// Returns a handler serving only the gateway
func (g *Gateway) Handler() http.Handler {
	mux := http.NewServeMux()
	g.Mount(mux)
	return mux
}

// Rejects requests without a configured key when keys are set
func (g *Gateway) auth(next http.HandlerFunc) http.HandlerFunc {
	if len(g.keys) == 0 {
		return next
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if !g.authorized(r) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="nebu"`)
			writeError(w, http.StatusUnauthorized, "missing or invalid api key", "authentication_error")
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

func (g *Gateway) models(w http.ResponseWriter, r *http.Request) {
	data := []map[string]any{}
	for _, rt := range g.table.List() {
		if rt.GetState() == v1.RouteState_ROUTE_STATE_DRAINING {
			continue
		}
		data = append(data, map[string]any{
			"id":       rt.GetName(),
			"object":   "model",
			"created":  rt.GetUpdatedAt().AsTime().Unix(),
			"owned_by": "nebu",
			"ready":    rt.GetState() == v1.RouteState_ROUTE_STATE_READY,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": data})
}

func (g *Gateway) proxy(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody))
	if err != nil {
		writeError(w, http.StatusBadRequest, "could not read request body", "invalid_request_error")
		return
	}
	name := modelName(r, body)
	endpoint, release, err := g.table.Acquire(name)
	if errors.Is(err, ErrNoRoute) && name == "" {
		if ready := g.table.Ready(); len(ready) == 1 {
			name = ready[0].GetName()
			endpoint, release, err = g.table.Acquire(name)
		}
	}
	switch {
	case errors.Is(err, ErrPending):
		w.Header().Set("Retry-After", retryAfter)
		writeError(w, http.StatusServiceUnavailable, "model "+name+" is starting, retry shortly", "model_starting")
		return
	case errors.Is(err, ErrDraining):
		w.Header().Set("Retry-After", retryAfter)
		writeError(w, http.StatusServiceUnavailable, "model "+name+" is being replaced, retry shortly", "model_swapping")
		return
	case err != nil:
		writeError(w, http.StatusNotFound, "model "+name+" is not running, run it with nebu run", "model_not_found")
		return
	}
	defer release()
	target, err := url.Parse(endpoint)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error(), "server_error")
		return
	}
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.URL.Path = r.URL.Path
			pr.Out.URL.RawPath = r.URL.RawPath
			pr.Out.Host = target.Host
		},
		Transport:     &sameHostRedirects{next: http.DefaultTransport, body: body},
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			g.log.Warn("gateway upstream", "model", name, "err", err)
			writeError(w, http.StatusBadGateway, "upstream error: "+err.Error(), "server_error")
		},
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	rp.ServeHTTP(w, r)
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

func writeError(w http.ResponseWriter, status int, message, kind string) {
	writeJSON(w, status, map[string]any{"error": map[string]any{"message": message, "type": kind, "code": kind}})
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
