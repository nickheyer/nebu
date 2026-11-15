package pools

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nickheyer/nebu/internal/db"
	"github.com/nickheyer/nebu/pkg/datalayer"
	"github.com/nickheyer/nebu/pkg/datalayer/scrape"
	"github.com/nickheyer/nebu/pkg/events"
	"github.com/nickheyer/nebu/pkg/prefixcache"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// A vLLM shaped runtime counting the reads of its metrics and reporting a set number of running requests
type countedRuntime struct {
	addr    datalayer.Address
	scrapes atomic.Int64
	running atomic.Int32
}

func newCountedRuntime(t *testing.T) *countedRuntime {
	t.Helper()
	r := &countedRuntime{}
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, `{"object":"list","data":[]}`) })
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, _ *http.Request) {
		r.scrapes.Add(1)
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		fmt.Fprintf(w, "# TYPE vllm:num_requests_waiting gauge\nvllm:num_requests_waiting 0\n# TYPE vllm:num_requests_running gauge\nvllm:num_requests_running %d\n", r.running.Load())
		fmt.Fprintf(w, "# TYPE vllm:kv_cache_usage_perc gauge\nvllm:kv_cache_usage_perc 0\n# TYPE vllm:cache_config_info gauge\nvllm:cache_config_info{block_size=\"16\",num_gpu_blocks=\"1024\"} 1\n")
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	addr, err := datalayer.ParseAddress(strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	r.addr = addr
	return r
}

// A route balanced by free capacity samples its pools for every draw, one balanced by static weight samples none
func TestFreeCapacityDrawSamplesItsPools(t *testing.T) {
	ctx := context.Background()
	d, err := db.Open(filepath.Join(t.TempDir(), "nebu.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	pace := scrape.Pace{Deciding: 10 * time.Millisecond, Working: 60 * time.Millisecond, Idle: time.Hour}
	m, err := New(ctx, Options{DB: d, Events: events.New(), NodeID: node, Scrape: pace, ProbeInterval: time.Hour, CatalogInterval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	m.SetIndexer(prefixcache.New(prefixcache.Config{}), prefixcache.Config{})
	t.Cleanup(func() { m.Close() })
	busy, free := newCountedRuntime(t), newCountedRuntime(t)
	a, b := PoolIDOf(llama, "vllm", ""), PoolIDOf(llama, "sglang", "")
	for runtime, rt := range map[string]*countedRuntime{"vllm": busy, "sglang": free} {
		if err := m.Publish(ctx, OwnerRef{Kind: OwnerInstance, ID: runtime}, membership(runtime), []Published{{InstanceID: runtime, NodeID: node, Address: rt.addr, Ready: true}}); err != nil {
			t.Fatal(err)
		}
	}
	balanced := func(balance v1.RouteBalance) {
		t.Helper()
		if _, err := m.PutRoute(ctx, "split", &v1.RouteSpec{Rules: []*v1.RewriteRule{rewrite("chat", to(a, "a-model", 1), to(b, "b-model", 1))}, Balance: balance}); err != nil {
			t.Fatal(err)
		}
	}
	balanced(v1.RouteBalance_ROUTE_BALANCE_UNSPECIFIED)
	m.Store().MarkSynced()
	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	running := func(runtime string, rt *countedRuntime) (int, bool) {
		ep, ok := m.Store().Endpoint(endpointID(&Published{InstanceID: runtime, Address: rt.addr}))
		if !ok {
			return 0, false
		}
		sm, ok := datalayer.Attribute[*datalayer.ServerMetrics](ep, datalayer.CollectorMetrics)
		if !ok {
			return 0, false
		}
		return sm.RunningRequests, true
	}
	waitUntil(t, "the first sample of both runtimes", func() bool {
		_, a := running("vllm", busy)
		_, b := running("sglang", free)
		return a && b
	})
	busy.running.Store(4)
	if counts := drawCounts(t, m, "chat", 200); counts[a] < 60 || counts[a] > 140 {
		t.Fatalf("static weights drew %v", counts)
	}
	if busy.scrapes.Load() != 1 || free.scrapes.Load() != 1 {
		t.Fatalf("a statically balanced route scraped its pools %d and %d times", busy.scrapes.Load(), free.scrapes.Load())
	}
	balanced(v1.RouteBalance_ROUTE_BALANCE_FREE_CAPACITY)
	time.Sleep(2 * pace.Working)
	waitUntil(t, "a draw to sample the full pool", func() bool {
		start := time.Now()
		drawCounts(t, m, "chat", 1)
		waited := time.Since(start)
		n, _ := running("vllm", busy)
		if n != 4 && waited < pace.Deciding {
			t.Fatalf("a draw decided on the idle sample of a full pool %v after asking for a newer one", waited)
		}
		return n == 4
	})
	if counts := drawCounts(t, m, "chat", 2000); counts[b] < 1400 {
		t.Fatalf("free capacity over a full pool drew %v", counts)
	}
	if busy.scrapes.Load() < 2 || free.scrapes.Load() < 2 {
		t.Fatalf("the draws read samples taken before them, %d and %d scrapes", busy.scrapes.Load(), free.scrapes.Load())
	}
}
