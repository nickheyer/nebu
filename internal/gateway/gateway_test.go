package gateway

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

func tableOf(t *testing.T, routes map[string]string) *Table {
	t.Helper()
	table, err := OpenTable(context.Background(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for name, ep := range routes {
		table.Set(name, "inst-"+name, "", ep, "repo:"+name, v1.ApiFlavor_API_FLAVOR_OPENAI)
	}
	return table
}

func TestGateway(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"path":"` + r.URL.Path + `","body":` + string(body) + `}`))
	}))
	defer upstream.Close()
	g := New(tableOf(t, map[string]string{"m1": upstream.URL}), nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	srv := httptest.NewServer(g.Handler())
	defer srv.Close()
	resp, err := http.Post(srv.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{"model":"m1","messages":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || !strings.Contains(string(body), `"path":"/v1/chat/completions"`) || !strings.Contains(string(body), `"model":"m1"`) {
		t.Fatalf("proxy %d %s", resp.StatusCode, body)
	}
	resp, _ = http.Post(srv.URL+"/v1/completions", "application/json", strings.NewReader(`{"prompt":"x"}`))
	if resp.StatusCode != 200 {
		t.Fatalf("single ready fallback %d", resp.StatusCode)
	}
	resp, _ = http.Post(srv.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{"model":"nope"}`))
	body, _ = io.ReadAll(resp.Body)
	if resp.StatusCode != 404 || !strings.Contains(string(body), "model_not_found") {
		t.Fatalf("unknown model %d %s", resp.StatusCode, body)
	}
	resp, _ = http.Get(srv.URL + "/v1/models")
	var list struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	json.NewDecoder(resp.Body).Decode(&list)
	if len(list.Data) != 1 || list.Data[0].ID != "m1" {
		t.Fatalf("models %+v", list)
	}
	resp, _ = http.Get(srv.URL + "/health")
	if resp.StatusCode != 200 {
		t.Fatal("health")
	}
}
