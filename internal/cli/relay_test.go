package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// A relay in front of a provider that echoes what it was sent, refusing every key but good-key
func relayFixture(t *testing.T) (*httptest.Server, *bytes.Buffer) {
	t.Helper()
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer good-key" {
			w.WriteHeader(http.StatusUnauthorized)
			io.WriteString(w, `{"error":{"message":"Incorrect API key provided"}}`)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var doc map[string]any
		json.Unmarshal(body, &doc)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("X-Path", r.URL.Path+"?"+r.URL.RawQuery)
		io.WriteString(w, "data: "+doc["model"].(string)+"\n\n")
		w.(http.Flusher).Flush()
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(provider.Close)
	base, _ := url.Parse(provider.URL + "/api/v1")
	var logs bytes.Buffer
	rs := &relayServer{upstream: base, model: "vendor/model-7", served: "seven", client: provider.Client(), log: slog.New(slog.NewTextHandler(&logs, nil))}
	srv := httptest.NewServer(rs)
	t.Cleanup(srv.Close)
	return srv, &logs
}

func TestRelayForwardsRenamedAndStreamed(t *testing.T) {
	srv, logs := relayFixture(t)
	if resp, err := http.Get(srv.URL + "/health"); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("health %v %v", resp, err)
	}
	if resp, err := http.Get(srv.URL + "/lora-adapters"); err != nil || resp.StatusCode != http.StatusNotFound {
		t.Fatalf("a path outside the version path stays here %v %v", resp, err)
	}
	resp, err := http.Get(srv.URL + "/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	var list struct {
		Data []struct{ ID string } `json:"data"`
	}
	json.NewDecoder(resp.Body).Decode(&list)
	if len(list.Data) != 1 || list.Data[0].ID != "seven" {
		t.Fatalf("models %+v", list)
	}
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL+"/v1/chat/completions?x=1", strings.NewReader(`{"model":"seven","stream":true,"messages":[]}`))
	req.Header.Set("Authorization", "Bearer good-key")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || resp.Header.Get("X-Path") != "/api/v1/chat/completions?x=1" || string(body) != "data: vendor/model-7\n\ndata: [DONE]\n\n" {
		t.Fatalf("relayed %d %s %q", resp.StatusCode, resp.Header.Get("X-Path"), body)
	}
	if strings.Contains(logs.String(), "good-key") || !strings.Contains(logs.String(), "path=/v1/chat/completions") {
		t.Fatalf("logs %s", logs)
	}
}

func TestRelayAnswersAProviderRefusalAsBadGateway(t *testing.T) {
	srv, logs := relayFixture(t)
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL+"/v1/messages", strings.NewReader(`{"model":"seven"}`))
	req.Header.Set("Authorization", "Bearer stale-key")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusBadGateway || !strings.Contains(string(body), "Incorrect API key") {
		t.Fatalf("refusal %d %s", resp.StatusCode, body)
	}
	if strings.Contains(logs.String(), "stale-key") {
		t.Fatalf("a header value reached the log: %s", logs)
	}
}
