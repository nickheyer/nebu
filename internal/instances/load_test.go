package instances

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/nickheyer/nebu/internal/pools"
	"github.com/nickheyer/nebu/pkg/datalayer"
	"github.com/nickheyer/nebu/pkg/events"
	"github.com/nickheyer/nebu/pkg/prefixcache"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// The idle clock resets while the fake runtime's own scraped gauge shows requests that never went through a gateway
func TestIdleWindowReadsTheRuntimeScrape(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n := newSeatNode(t, ctx)
	pm, err := pools.New(ctx, pools.Options{DB: n.db, Events: events.New(), NodeID: "n1", Log: slog.New(slog.NewTextHandler(io.Discard, nil)), ScrapeInterval: 50 * time.Millisecond, ProbeInterval: 50 * time.Millisecond, CatalogInterval: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	pm.SetIndexer(prefixcache.New(prefixcache.Config{}), prefixcache.Config{})
	pm.Store().MarkSynced()
	if err := pm.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pm.Close() })
	m := n.manager(t, ctx, nil)
	m.Registrar = pm
	m.Load = ScrapedLoad(pm.Store(), time.Second)
	run := soloRun(nil)
	run.IdleUnloadSeconds = 1
	in, _, err := m.Run(ctx, run)
	if err != nil {
		t.Fatal(err)
	}
	ready := waitState(t, m, in.GetId(), v1.InstanceState_INSTANCE_STATE_READY)
	waitFor(t, "the first scrape of the idle runtime", func() bool {
		running, queued, scraped := m.Load(in.GetId())
		return scraped && running == 0 && queued == 0
	})
	release := n.launcher.Proc("solo").Hold()
	defer release()
	answered := make(chan error, 1)
	go func() {
		resp, err := http.Post(ready.GetEndpoint()+"/v1/chat/completions", "application/json", strings.NewReader(`{"model":"solo","messages":[{"role":"user","content":"hi"}]}`))
		if err == nil {
			resp.Body.Close()
		}
		answered <- err
	}()
	waitFor(t, "the scrape showing the request the runtime runs", func() bool {
		running, _, scraped := m.Load(in.GetId())
		return scraped && running == 1
	})
	time.Sleep(2500 * time.Millisecond)
	if got := current(t, m, in.GetId()).GetState(); got != v1.InstanceState_INSTANCE_STATE_READY || n.traffic.Dispatched(in.GetId()) != 0 || n.traffic.InFlight(in.GetId()) != 0 {
		t.Fatalf("an instance running a request no gateway sent went %s", got)
	}
	release()
	if err := <-answered; err != nil {
		t.Fatal(err)
	}
	waitState(t, m, in.GetId(), v1.InstanceState_INSTANCE_STATE_IDLE)
	if eps := pm.Store().Endpoints("", func(ep datalayer.Endpoint) bool { return ep.Labels()[datalayer.LabelInstance] == in.GetId() }); len(eps) != 0 {
		t.Fatalf("the idle instance still publishes %d endpoints", len(eps))
	}
	if _, _, scraped := m.Load(in.GetId()); scraped {
		t.Fatal("an unpublished instance reads as scraped")
	}
	if _, err := m.Stop(ctx, in.GetId()); err != nil {
		t.Fatal(err)
	}
}

// Fresh samples are summed once per address over an instance's endpoints, stale ones and other instances left out
func TestScrapedLoadSumsFreshSamples(t *testing.T) {
	store := datalayer.NewStore()
	put := func(id datalayer.EndpointID, instance string, port int, m *datalayer.ServerMetrics) {
		store.UpsertEndpoint(datalayer.EndpointSpec{ID: id, Pool: "p", Address: datalayer.Address{Host: "127.0.0.1", Port: port}, Labels: map[string]string{datalayer.LabelInstance: instance}, Ready: true})
		if m != nil {
			store.SetAttribute(id, datalayer.CollectorMetrics, m)
		}
	}
	now := time.Now()
	put("a/1", "a", 1, &datalayer.ServerMetrics{RunningRequests: 2, QueuedRequests: 1, UpdatedAt: now})
	put("a/1/sql", "a", 1, &datalayer.ServerMetrics{RunningRequests: 2, QueuedRequests: 1, UpdatedAt: now})
	put("a/2", "a", 2, &datalayer.ServerMetrics{RunningRequests: 3, UpdatedAt: now})
	put("a/3", "a", 3, &datalayer.ServerMetrics{RunningRequests: 50, UpdatedAt: now.Add(-time.Minute)})
	put("b/4", "b", 4, &datalayer.ServerMetrics{RunningRequests: 9, UpdatedAt: now})
	put("c/5", "c", 5, nil)
	put("d/6", "d", 6, &datalayer.ServerMetrics{UpdatedAt: now.Add(-time.Minute)})
	load := ScrapedLoad(store, 10*time.Second)
	cases := []struct {
		instance        string
		running, queued int
		scraped         bool
	}{
		{"a", 5, 1, true},
		{"b", 9, 0, true},
		{"c", 0, 0, false},
		{"d", 0, 0, false},
		{"none", 0, 0, false},
	}
	for _, c := range cases {
		if running, queued, scraped := load(c.instance); running != c.running || queued != c.queued || scraped != c.scraped {
			t.Errorf("%s: %d running %d queued scraped %v, want %d %d %v", c.instance, running, queued, scraped, c.running, c.queued, c.scraped)
		}
	}
}
