package gateway

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLocalClientReachesTheGatewayInProcess(t *testing.T) {
	var seenAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"x","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]}`))
	}))
	defer upstream.Close()
	g := New(tableOf(t, map[string]string{"m1": upstream.URL}), []string{"k1"}, nil, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	client := g.Client("bot:test")
	resp, err := client.Post(g.LocalBase()+"/v1/chat/completions", "application/json", strings.NewReader(`{"model":"m1","messages":[{"role":"user","content":"hello"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), `"content":"hi"`) {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	trace := resp.Header.Get(traceHeader)
	if trace == "" {
		t.Fatal("no trace header")
	}
	kept, ok := g.Traces().Get(trace)
	if !ok || kept.GetRemote() != "bot:test" || kept.GetRoute() != "m1" {
		t.Fatalf("trace %v", kept)
	}
	_ = seenAuth
	// Without the key the same request over the network is refused, so the local client's key is what let it through
	srv := httptest.NewServer(g.Handler())
	defer srv.Close()
	resp, _ = http.Post(srv.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{"model":"m1","messages":[]}`))
	if resp.StatusCode != 401 {
		t.Fatalf("network request without a key answered %d", resp.StatusCode)
	}
}
