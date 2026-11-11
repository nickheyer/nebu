package gateway

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nickheyer/nebu/internal/pools"
	"github.com/nickheyer/nebu/pkg/datalayer"
)

// An agent's later system turn reaches a runtime whose chat template takes a system turn only first
func TestAnthropicLateSystemReachesStrictTemplate(t *testing.T) {
	h := newHarness(t)
	_, stubSrv := h.stub("stub-model")
	strict := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var sent struct {
			Messages []struct {
				Role string `json:"role"`
			} `json:"messages"`
		}
		json.Unmarshal(raw, &sent)
		for i, m := range sent.Messages {
			if i > 0 && m.Role == "system" {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusInternalServerError)
				io.WriteString(w, `{"error":{"code":500,"message":"Jinja Exception: System message must be at the beginning.","type":"server_error"}}`)
				return
			}
		}
		r.Body = io.NopCloser(bytes.NewReader(raw))
		stubSrv.Config.Handler.ServeHTTP(w, r)
	}))
	defer strict.Close()
	h.publish("chat", "stub", "llamacpp", datalayer.AppProtocolHTTP, true, pools.RouteSpec{ServedName: "stub-model"}, strict.URL)
	body := `{"model":"chat","max_tokens":8,"system":[{"type":"text","text":"You are an agent.","cache_control":{"type":"ephemeral"}}],
		"messages":[{"role":"user","content":"hello there"},{"role":"system","content":[{"type":"text","text":"# Environment"}]}],
		"thinking":{"type":"adaptive"},"output_config":{"effort":"xhigh"},"metadata":{"user_id":"u-1"}}`
	r := h.do(http.MethodPost, "/v1/messages", body, "anthropic-version", "2023-06-01")
	if r.status != http.StatusOK {
		t.Fatalf("status %d: %s", r.status, r.body)
	}
	upstream := h.trace(r).GetUpstreamRequest()
	for _, want := range []string{`{"role":"user","content":"# Environment"}`, `"reasoning_effort":"xhigh"`, `"user":"u-1"`} {
		if !strings.Contains(upstream, want) {
			t.Fatalf("upstream request lacks %s: %s", want, upstream)
		}
	}
	for _, gone := range []string{"cache_control", `"thinking"`, "output_config", "metadata"} {
		if strings.Contains(upstream, gone) {
			t.Fatalf("upstream request carries %s: %s", gone, upstream)
		}
	}
}

// Claude Code's billing header stays out of the prompt a chat completions runtime gets, unless the instance keeps it
func TestAnthropicBillingHeaderIsStripped(t *testing.T) {
	h := newHarness(t)
	_, stubSrv := h.stub("stub-model")
	h.publish("chat", "stub", "llamacpp", datalayer.AppProtocolHTTP, true, pools.RouteSpec{ServedName: "stub-model"}, stubSrv.URL)
	body := `{"model":"chat","max_tokens":8,"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.101.e51; cc_entrypoint=cli; cch=a5145;"},{"type":"text","text":"You are an agent."}],
		"messages":[{"role":"user","content":"hello there"}]}`
	r := h.do(http.MethodPost, "/v1/messages", body, "anthropic-version", "2023-06-01")
	if r.status != http.StatusOK {
		t.Fatalf("status %d: %s", r.status, r.body)
	}
	upstream := h.trace(r).GetUpstreamRequest()
	if strings.Contains(upstream, "x-anthropic-billing-header") || !strings.Contains(upstream, "You are an agent.") {
		t.Fatalf("upstream request keeps the billing header or lost the prompt: %s", upstream)
	}
	h.inst.mu.Lock()
	h.inst.keepBilling = map[string]bool{"chat-" + strings.TrimPrefix(stubSrv.URL, "http://"): true}
	h.inst.mu.Unlock()
	r = h.do(http.MethodPost, "/v1/messages", body, "anthropic-version", "2023-06-01")
	if r.status != http.StatusOK {
		t.Fatalf("status %d: %s", r.status, r.body)
	}
	if upstream := h.trace(r).GetUpstreamRequest(); !strings.Contains(upstream, "cch=a5145") {
		t.Fatalf("upstream request lost the billing header the instance keeps: %s", upstream)
	}
}
