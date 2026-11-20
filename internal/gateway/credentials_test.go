package gateway

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/nickheyer/nebu/internal/auth"
	"github.com/nickheyer/nebu/internal/pools"
	"github.com/nickheyer/nebu/pkg/datalayer"
	"github.com/nickheyer/nebu/pkg/sources"
)

// Keys by owner for the harness's source, every other source serving no models
type fakeCreds struct {
	keys map[string]string
	mu   sync.Mutex
	// Owners asked for, in order
	asked []string
}

func (f *fakeCreds) Headers(_ context.Context, owner, sourceID string, sent http.Header) (map[string]string, bool, error) {
	if sourceID != "hf" {
		return nil, false, nil
	}
	f.mu.Lock()
	f.asked = append(f.asked, owner)
	f.mu.Unlock()
	key, ok := f.keys[owner]
	if !ok {
		return nil, true, fmt.Errorf("%w for Lab, add one under Profile, Providers", sources.ErrNoCredential)
	}
	out := map[string]string{"Authorization": "Bearer " + key}
	if sent.Get("X-Version") == "" {
		out["X-Version"] = "fixed"
	}
	return out, true, nil
}

// The caller's key reaches the upstream in place of the client's own token, and a caller with none is refused before dispatch
func TestProviderCredentialSignsTheRequest(t *testing.T) {
	creds := &fakeCreds{keys: map[string]string{auth.ProviderDaemon: "sk-shared"}}
	h := newHarness(t, func(s *setup) { s.creds = creds })
	var seen sync.Map
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.Store("auth", r.Header.Get("Authorization"))
		seen.Store("version", r.Header.Get("X-Version"))
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"x","object":"chat.completion","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]}`)
	}))
	t.Cleanup(upstream.Close)
	h.publish("chat", "stub", "vllm", datalayer.AppProtocolHTTP, true, pools.RouteSpec{ServedName: "stub-model"}, upstream.URL)
	r := h.do(http.MethodPost, "/v1/chat/completions", chatBody, "Authorization", "Bearer client-token", "X-Version", "client")
	if r.status != http.StatusOK {
		t.Fatalf("status %d %s", r.status, r.body)
	}
	if got, _ := seen.Load("auth"); got != "Bearer sk-shared" {
		t.Fatalf("upstream saw %v, want the caller's stored key", got)
	}
	if got, _ := seen.Load("version"); got != "client" {
		t.Fatalf("a header the client sent stands over the provider's fixed one, got %v", got)
	}
	creds.keys = map[string]string{}
	r = h.do(http.MethodPost, "/v1/chat/completions", chatBody)
	if r.status != http.StatusForbidden || !strings.Contains(string(r.body), "no credential for Lab") || !strings.Contains(string(r.body), "Profile") {
		t.Fatalf("refusal %d %s", r.status, r.body)
	}
}

// A gateway key names no person, so its requests use the shared credentials
func TestProviderCredentialOwnerOfAGatewayKey(t *testing.T) {
	creds := &fakeCreds{keys: map[string]string{auth.ProviderDaemon: "sk-shared"}}
	h := newHarness(t, func(s *setup) { s.creds = creds; s.keys = []string{"gw-key"} })
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"x","object":"chat.completion","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]}`)
	}))
	t.Cleanup(upstream.Close)
	h.publish("chat", "stub", "vllm", datalayer.AppProtocolHTTP, true, pools.RouteSpec{ServedName: "stub-model"}, upstream.URL)
	if r := h.do(http.MethodPost, "/v1/chat/completions", chatBody, "Authorization", "Bearer gw-key"); r.status != http.StatusOK {
		t.Fatalf("status %d %s", r.status, r.body)
	}
	creds.mu.Lock()
	defer creds.mu.Unlock()
	if len(creds.asked) != 1 || creds.asked[0] != auth.ProviderDaemon {
		t.Fatalf("owners asked %v", creds.asked)
	}
}
