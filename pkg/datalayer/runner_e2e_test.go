package datalayer_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nickheyer/nebu/pkg/datalayer"
	"github.com/nickheyer/nebu/pkg/datalayer/catalog"
	"github.com/nickheyer/nebu/pkg/datalayer/probe"
	"github.com/nickheyer/nebu/pkg/datalayer/scrape"
)

const vllmMetrics = `# TYPE vllm:num_requests_waiting gauge
vllm:num_requests_waiting{model_name="m"} 2
# TYPE vllm:num_requests_running gauge
vllm:num_requests_running{model_name="m"} 1
# TYPE vllm:kv_cache_usage_perc gauge
vllm:kv_cache_usage_perc{model_name="m"} 0.5
# TYPE vllm:cache_config_info gauge
vllm:cache_config_info{block_size="16",num_gpu_blocks="1000"} 1
`

// A vLLM shaped endpoint counting the metrics scrapes it answers
type runtime struct {
	srv     *httptest.Server
	scrapes atomic.Int64
	first   atomic.Int64
}

func newRuntime(t *testing.T) *runtime {
	r := &runtime{}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/metrics":
			if r.scrapes.Add(1) == 1 {
				r.first.Store(time.Now().UnixNano())
			}
			_, _ = io.WriteString(w, vllmMetrics)
		case "/health":
		case "/v1/models":
			_, _ = io.WriteString(w, `{"data":[{"id":"m"},{"id":"a1","parent":"m"}]}`)
		default:
			http.NotFound(w, req)
		}
	}))
	t.Cleanup(r.srv.Close)
	return r
}

func (r *runtime) spec(t *testing.T, id string) datalayer.EndpointSpec {
	addr, err := datalayer.ParseAddress(strings.TrimPrefix(r.srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	return datalayer.EndpointSpec{ID: datalayer.EndpointID(id), Pool: "p", Address: addr, Ready: true}
}

func TestRunnerDrivesRealSources(t *testing.T) {
	const interval = 100 * time.Millisecond
	store := datalayer.NewStore()
	caps, _ := scrape.Capabilities(scrape.RuntimeVLLM)
	store.PutPool(datalayer.Pool{ID: "p", Model: "m", Runtime: scrape.RuntimeVLLM, Metrics: caps})

	metrics := scrape.New(store, scrape.StoreResolver(store), scrape.Options{Pace: scrape.Pace{Deciding: interval, Working: interval, Idle: interval}})
	health := probe.New(store, probe.StoreResolver(store), probe.Options{Interval: interval})
	models := catalog.New(store, catalog.StoreResolver(store), catalog.Options{Interval: interval})
	sources := datalayer.Sources{}
	for _, reg := range []struct {
		src datalayer.DataSource
		col datalayer.Collector
	}{{metrics, scrape.Collector{}}, {health, probe.NewCollector(3)}, {models, catalog.Collector{}}} {
		if err := reg.src.Subscribe(reg.col); err != nil {
			t.Fatal(err)
		}
		if err := sources.Register(reg.src); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- datalayer.NewRunner(store, sources).Run(ctx) }()

	a, b := newRuntime(t), newRuntime(t)
	store.UpsertEndpoint(a.spec(t, "a"))
	time.Sleep(interval / 2)
	added := time.Now()
	store.UpsertEndpoint(b.spec(t, "b"))

	waitUntil(t, func() bool { return b.scrapes.Load() > 0 })
	if lag := time.Duration(b.first.Load() - added.UnixNano()); lag > interval {
		t.Fatalf("first scrape %v after the endpoint appeared, interval %v", lag, interval)
	}
	waitUntil(t, func() bool {
		ep, ok := store.Endpoint("b")
		if !ok {
			return false
		}
		_, m := datalayer.Attribute[*datalayer.ServerMetrics](ep, datalayer.CollectorMetrics)
		_, h := datalayer.Attribute[*datalayer.HealthState](ep, datalayer.CollectorHealth)
		_, c := datalayer.Attribute[*datalayer.Catalog](ep, datalayer.CollectorCatalog)
		return m && h && c
	})
	ep, _ := store.Endpoint("b")
	if !datalayer.All(datalayer.ReadyAndHealthy, datalayer.FreshMetrics(time.Now(), time.Second), datalayer.HasAdapter("a1"))(ep) {
		t.Fatal("fully sampled endpoint filtered out")
	}

	store.DeleteEndpoint("a")
	time.Sleep(interval / 2)
	stopped := a.scrapes.Load()
	time.Sleep(3 * interval)
	if a.scrapes.Load() != stopped {
		t.Fatal("deleted endpoint still scraped")
	}
	before := b.scrapes.Load()
	waitUntil(t, func() bool { return b.scrapes.Load() > before })

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run returned %v", err)
	}
	final := b.scrapes.Load()
	time.Sleep(2 * interval)
	if b.scrapes.Load() != final {
		t.Fatal("sources kept scraping after Run returned")
	}
}

func waitUntil(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition never held")
		}
		time.Sleep(2 * time.Millisecond)
	}
}
