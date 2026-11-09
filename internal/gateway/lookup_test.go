package gateway

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nickheyer/nebu/internal/auth"
	"github.com/nickheyer/nebu/internal/db"
	"github.com/nickheyer/nebu/internal/pools"
	"github.com/nickheyer/nebu/pkg/datalayer"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// An in process answer leads to its finished trace, and the instance and route that served it count the request
func TestTraceOfAnAnswerAndItsCounts(t *testing.T) {
	h := newHarness(t)
	_, srv := h.stub("stub-model")
	h.publish("chat", "stub", "vllm", datalayer.AppProtocolHTTP, true, pools.RouteSpec{ServedName: "stub-model"}, srv.URL)
	req, err := http.NewRequest(http.MethodPost, h.g.LocalBase()+"/v1/chat/completions", strings.NewReader(chatBody))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := h.g.Client("test").Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	tr, ok := h.g.Trace(resp)
	if !ok || tr.GetId() != resp.Header.Get(TraceHeader) || tr.GetFinishedAt() == nil || tr.GetEndpoint() != hostOf(srv) || tr.GetStatus() != http.StatusOK {
		t.Fatalf("trace %v %v", tr, ok)
	}
	if _, ok := h.g.Trace(&http.Response{Header: http.Header{}}); ok {
		t.Fatal("an answer naming no trace has none")
	}
	instance := "chat-" + hostOf(srv)
	if h.g.Dispatched(instance) != 1 || h.g.InFlight(instance) != 0 || h.g.Dispatched("other") != 0 {
		t.Fatalf("dispatched %d in flight %d", h.g.Dispatched(instance), h.g.InFlight(instance))
	}
	rt, ok := h.g.Route("chat")
	if !ok || rt.GetStatus().GetRequests() != 1 || rt.GetStatus().GetInFlight() != 0 {
		t.Fatalf("route %v", rt)
	}
	if _, ok := h.g.Route("nope"); ok {
		t.Fatal("an unknown route was found")
	}
	if list := h.g.Routes(); len(list) != 1 || list[0].GetStatus().GetRequests() != 1 {
		t.Fatalf("routes %v", list)
	}
}

// Liveness, readiness, gRPC health and metrics mount on a mux of their own, the metrics behind the gateway's credentials
func TestHealthMountsAlone(t *testing.T) {
	h := newHarness(t, func(s *setup) { s.keys = []string{"k"} })
	mux := http.NewServeMux()
	h.g.MountHealth(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	get := func(path, key string) int {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+path, nil)
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if get("/livez", "") != http.StatusOK || get("/readyz", "") != http.StatusOK {
		t.Fatal("liveness and readiness answer without credentials")
	}
	if get("/metrics", "") != http.StatusUnauthorized || get("/metrics", "k") != http.StatusOK {
		t.Fatal("metrics answer the gateway key alone")
	}
	if get("/v1/models", "k") != http.StatusNotFound {
		t.Fatal("the health mount serves no inference path")
	}
}

// A scraper holding a read token reaches metrics while that token sends no inference
func TestMetricsTakeReadScope(t *testing.T) {
	store, err := db.Open(filepath.Join(t.TempDir(), "tokens.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	tokens := auth.NewTokens(store, slog.New(slog.DiscardHandler))
	admin := auth.Identity{Subject: "u1", Provider: auth.ProviderLocal, Role: v1.Role_ROLE_ADMIN, Scopes: auth.RoleScopes(v1.Role_ROLE_ADMIN)}
	read, err := tokens.Create(context.Background(), admin, "scraper", []v1.Scope{v1.Scope_SCOPE_READ})
	if err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, func(s *setup) { s.guard = auth.NewGuard("daemon", nil, tokens) })
	if r := h.do(http.MethodGet, "/metrics", "", "Authorization", "Bearer "+read.GetSecret()); r.status != http.StatusOK {
		t.Fatalf("metrics with a read token %d", r.status)
	}
	if r := h.do(http.MethodGet, "/metrics", ""); r.status != http.StatusUnauthorized {
		t.Fatalf("metrics without a credential %d", r.status)
	}
	if r := h.do(http.MethodPost, "/v1/chat/completions", chatBody, "Authorization", "Bearer "+read.GetSecret()); r.status != http.StatusUnauthorized {
		t.Fatalf("inference with a read token %d", r.status)
	}
}
