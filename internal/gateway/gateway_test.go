package gateway

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type fakeRouter map[string]string

func (f fakeRouter) Route(name string) (string, bool) {
	ep, ok := f[name]
	return ep, ok
}

func (f fakeRouter) Ready() []*v1.Instance {
	var out []*v1.Instance
	for name, ep := range f {
		out = append(out, &v1.Instance{Name: name, Endpoint: ep, ReadyAt: timestamppb.Now()})
	}
	return out
}

func TestGateway(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"path":"` + r.URL.Path + `","body":` + string(body) + `}`))
	}))
	defer upstream.Close()
	g := New(fakeRouter{"m1": upstream.URL}, slog.New(slog.NewTextHandler(io.Discard, nil)))
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
