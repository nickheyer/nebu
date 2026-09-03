// Package gateway exposes ready instances behind one OpenAI compatible endpoint.
package gateway

import (
	"bytes"
	"encoding/json"
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
)

// Resolves public model names to instance endpoints
type Router interface {
	Route(name string) (string, bool)
	Ready() []*v1.Instance
}

// Reverse proxy keyed by the model field of each request
type Gateway struct {
	router Router
	log    *slog.Logger
}

// Builds the gateway
func New(router Router, log *slog.Logger) *Gateway {
	return &Gateway{router: router, log: log}
}

// Registers gateway routes on a mux
func (g *Gateway) Mount(mux *http.ServeMux) {
	mux.HandleFunc(healthPath, g.health)
	mux.HandleFunc(modelsPath, g.models)
	mux.HandleFunc(prefix, g.proxy)
}

// Returns a handler serving only the gateway
func (g *Gateway) Handler() http.Handler {
	mux := http.NewServeMux()
	g.Mount(mux)
	return mux
}

func (g *Gateway) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "models": len(g.router.Ready())})
}

func (g *Gateway) models(w http.ResponseWriter, r *http.Request) {
	data := []map[string]any{}
	for _, in := range g.router.Ready() {
		data = append(data, map[string]any{
			"id":       in.GetName(),
			"object":   "model",
			"created":  in.GetReadyAt().AsTime().Unix(),
			"owned_by": "nebu",
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
	endpoint, ok := g.router.Route(name)
	if !ok {
		if ready := g.router.Ready(); name == "" && len(ready) == 1 {
			endpoint, ok = g.router.Route(ready[0].GetName())
		}
	}
	if !ok {
		writeError(w, http.StatusNotFound, "model "+name+" is not running, run it with nebu run", "model_not_found")
		return
	}
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

// Finds the requested model in the header, query, or JSON body
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
