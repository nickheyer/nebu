package gateway

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nickheyer/nebu/internal/db"
	"github.com/nickheyer/nebu/pkg/events"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

func TestTableLifecycle(t *testing.T) {
	store, err := db.Open(t.TempDir() + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	bus := events.New()
	sub := bus.Subscribe(context.Background(), []v1.EventKind{v1.EventKind_EVENT_KIND_ROUTE})
	table, err := OpenTable(context.Background(), store, bus, nil)
	if err != nil {
		t.Fatal(err)
	}
	r := table.Pending("main", "s1", "repo:q4", nil, nil)
	if r.GetState() != v1.RouteState_ROUTE_STATE_PENDING || r.GetSlotId() != "s1" {
		t.Fatalf("pending %v", r)
	}
	if _, _, _, err := table.Acquire("main", true); !errors.Is(err, ErrPending) {
		t.Fatalf("pending acquire %v", err)
	}
	if _, _, _, err := table.Acquire("nope", true); !errors.Is(err, ErrNoRoute) {
		t.Fatalf("missing acquire %v", err)
	}
	r = table.Set("main", "i1", "s1", "http://a", "repo:q4", "", v1.ApiFlavor_API_FLAVOR_OPENAI, nil, nil)
	if r.GetState() != v1.RouteState_ROUTE_STATE_READY || r.GetInstanceId() != "i1" {
		t.Fatalf("ready %v", r)
	}
	table.Set("alias", "i1", "", "http://a", "repo:q4", "", v1.ApiFlavor_API_FLAVOR_OPENAI, nil, nil)
	route, _, release, err := table.Acquire("main", true)
	if err != nil || route.GetEndpoint() != "http://a" {
		t.Fatalf("acquire %v %v", route, err)
	}
	if table.InFlight("i1") != 1 || table.Requests() != 1 {
		t.Fatal("counters")
	}
	if got, _ := table.Lookup("main"); got.GetInFlight() != 1 || got.GetRequests() != 1 {
		t.Fatalf("lookup counters %v", got)
	}
	// Token counts occupy an in-flight slot without incrementing served requests.
	if _, _, releaseCount, err := table.Acquire("main", false); err != nil {
		t.Fatalf("count acquire %v", err)
	} else if table.InFlight("i1") != 2 || table.Requests() != 1 {
		t.Fatal("a count changed the served tally")
	} else {
		releaseCount()
	}
	if got, _ := table.Lookup("main"); got.GetInFlight() != 1 || got.GetRequests() != 1 {
		t.Fatalf("counters after a count %v", got)
	}
	table.Drain("i1")
	if _, _, _, err := table.Acquire("main", true); !errors.Is(err, ErrDraining) {
		t.Fatalf("draining acquire %v", err)
	}
	if table.WaitDrained(context.Background(), "i1", 30*time.Millisecond) {
		t.Fatal("still in flight")
	}
	release()
	if !table.WaitDrained(context.Background(), "i1", time.Second) {
		t.Fatal("drained after release")
	}
	table.RemoveInstance("i1")
	main, _ := table.Lookup("main")
	if main.GetState() != v1.RouteState_ROUTE_STATE_PENDING || main.GetInstanceId() != "" {
		t.Fatalf("slot route should stay pending %v", main)
	}
	if _, ok := table.Lookup("alias"); ok {
		t.Fatal("alias should be deleted with its instance")
	}
	if len(table.Ready()) != 0 || len(table.List()) != 1 {
		t.Fatal("ready and list")
	}
	if _, ok := table.Delete("main"); !ok {
		t.Fatal("delete")
	}
	reopened, err := OpenTable(context.Background(), store, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(reopened.List()) != 0 {
		t.Fatal("deleted route should not persist")
	}
	table.Set("keep", "i2", "s2", "http://b", "m", "", v1.ApiFlavor_API_FLAVOR_OPENAI, nil, nil)
	reopened, _ = OpenTable(context.Background(), store, nil, nil)
	kept, ok := reopened.Lookup("keep")
	if !ok || kept.GetState() != v1.RouteState_ROUTE_STATE_PENDING || kept.GetInstanceId() != "" || kept.GetSlotId() != "s2" {
		t.Fatalf("persisted route comes back pending %v", kept)
	}
	seen := 0
	for seen < 6 {
		select {
		case ev := <-sub.Events():
			if ev.GetRoute() == nil {
				t.Fatal("route event without payload")
			}
			seen++
		case <-time.After(time.Second):
			t.Fatalf("only %d route events", seen)
		}
	}
}

func TestGatewayAuthAndStates(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"ok":true}`)) }))
	defer upstream.Close()
	table, _ := OpenTable(context.Background(), nil, nil, nil)
	table.Pending("starting", "s1", "m", nil, nil)
	table.Set("ready", "i1", "", upstream.URL, "m", "", v1.ApiFlavor_API_FLAVOR_OPENAI, nil, nil)
	table.Set("draining", "i2", "", upstream.URL, "m", "", v1.ApiFlavor_API_FLAVOR_OPENAI, nil, nil)
	table.Drain("i2")
	g := New(table, []string{"k1"}, nil, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	g.SetListeners([]*v1.Listener{{Addr: "127.0.0.1:1", Shared: true}}, false)
	srv := httptest.NewServer(g.Handler())
	defer srv.Close()
	post := func(model, key string) (int, string, string) {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/chat/completions", strings.NewReader(`{"model":"`+model+`"}`))
		req.Header.Set("Content-Type", "application/json")
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp.StatusCode, string(body), resp.Header.Get("Retry-After")
	}
	if code, body, _ := post("ready", ""); code != 401 || !strings.Contains(body, "authentication_error") {
		t.Fatalf("no key %d %s", code, body)
	}
	if code, _, _ := post("ready", "wrong"); code != 401 {
		t.Fatal("wrong key")
	}
	if code, body, _ := post("ready", "k1"); code != 200 || body != `{"ok":true}` {
		t.Fatalf("ready %d %s", code, body)
	}
	if code, body, retry := post("starting", "k1"); code != 503 || retry == "" || !strings.Contains(body, "model_starting") {
		t.Fatalf("starting %d %s %q", code, body, retry)
	}
	if code, body, retry := post("draining", "k1"); code != 503 || retry == "" || !strings.Contains(body, "model_swapping") {
		t.Fatalf("draining %d %s", code, body)
	}
	if code, body, _ := post("missing", "k1"); code != 404 || !strings.Contains(body, "model_not_found") {
		t.Fatalf("missing %d %s", code, body)
	}
	resp, _ := http.Get(srv.URL + "/health")
	if resp.StatusCode != 200 {
		t.Fatal("health should be open")
	}
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/models", nil)
	req.Header.Set("X-Api-Key", "k1")
	resp, _ = http.DefaultClient.Do(req)
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || !strings.Contains(string(body), `"id":"starting"`) || strings.Contains(string(body), `"id":"draining"`) || !strings.Contains(string(body), `"ready":false`) {
		t.Fatalf("models %d %s", resp.StatusCode, body)
	}
	st := g.Status()
	if !st.GetAuth() || st.GetRequests() != 1 || len(st.GetListeners()) != 1 || len(st.GetRoutes()) != 3 {
		t.Fatalf("status %v", st)
	}
}
