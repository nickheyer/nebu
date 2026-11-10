package gateway

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nickheyer/nebu/internal/pools"
	"github.com/nickheyer/nebu/pkg/datalayer"
	"github.com/nickheyer/nebu/pkg/datalayer/scrape"
	"github.com/nickheyer/nebu/pkg/scheduling/framework"
)

// A vLLM shaped runtime counting the reads of its metrics and holding chat completions while gated
type scrapedRuntime struct {
	srv     *httptest.Server
	scrapes atomic.Int64
	running atomic.Int32
	served  atomic.Int64
	// Metric reads that preceded the latest chat completion
	scrapedBefore atomic.Int64
	mu            sync.Mutex
	gate          chan struct{}
}

func newScrapedRuntime(t *testing.T) *scrapedRuntime {
	r := &scrapedRuntime{}
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"object":"list","data":[{"id":"demand","root":"demand","parent":null}]}`)
	})
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, _ *http.Request) {
		r.scrapes.Add(1)
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		fmt.Fprintf(w, "# TYPE vllm:num_requests_waiting gauge\nvllm:num_requests_waiting 0\n# TYPE vllm:num_requests_running gauge\nvllm:num_requests_running %d\n", r.running.Load())
		fmt.Fprintf(w, "# TYPE vllm:kv_cache_usage_perc gauge\nvllm:kv_cache_usage_perc 0\n# TYPE vllm:cache_config_info gauge\nvllm:cache_config_info{block_size=\"16\",num_gpu_blocks=\"1024\"} 1\n")
		fmt.Fprintf(w, "# TYPE vllm:prompt_tokens_total counter\nvllm:prompt_tokens_total %d\n", 3*r.served.Load())
	})
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, req *http.Request) {
		r.scrapedBefore.Store(r.scrapes.Load())
		r.running.Add(1)
		r.mu.Lock()
		gate := r.gate
		r.mu.Unlock()
		if gate != nil {
			select {
			case <-gate:
			case <-req.Context().Done():
			}
		}
		r.served.Add(1)
		r.running.Add(-1)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"c","object":"chat.completion","model":"demand","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}`)
	})
	r.srv = httptest.NewServer(mux)
	t.Cleanup(r.srv.Close)
	return r
}

// Holds every chat completion until the release
func (r *scrapedRuntime) hold() (release func()) {
	gate := make(chan struct{})
	r.mu.Lock()
	r.gate = gate
	r.mu.Unlock()
	return sync.OnceFunc(func() {
		r.mu.Lock()
		r.gate = nil
		r.mu.Unlock()
		close(gate)
	})
}

// Fails when the runtime's metrics are read during the window
func (r *scrapedRuntime) unscraped(t *testing.T, window time.Duration, what string) {
	t.Helper()
	before := r.scrapes.Load()
	time.Sleep(window)
	if got := r.scrapes.Load() - before; got != 0 {
		t.Fatalf("%s: scraped %d times in %s", what, got, window)
	}
}

// A gateway that scrapes only on demand over one published runtime, with the runtime's pool and endpoint
func demandHarness(t *testing.T, pace scrape.Pace) (*harness, *scrapedRuntime, datalayer.PoolID, datalayer.EndpointID) {
	t.Helper()
	h := newHarness(t, func(s *setup) { s.scrape, s.pace = true, pace })
	rt := newScrapedRuntime(t)
	ctx := context.Background()
	owner := pools.OwnerRef{Kind: pools.OwnerInstance, ID: "demand"}
	addr, err := datalayer.ParseAddress(hostOf(rt.srv))
	if err != nil {
		t.Fatal(err)
	}
	if err := h.mgr.Publish(ctx, owner, pools.Membership{Model: key("demand"), Runtime: "vllm", Protocol: datalayer.AppProtocolHTTP, Concurrency: 8}, []pools.Published{{InstanceID: owner.ID, NodeID: testNode, Address: addr, Ready: true}}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.mgr.NameRoute(ctx, owner, "demand", pools.RouteSpec{}); err != nil {
		t.Fatal(err)
	}
	pool := pools.PoolIDOf(key("demand"), "vllm", "")
	id := h.endpointID(pool)
	waitFor(t, "the first sample of the runtime", func() bool { return sampleOf(h, id) != nil })
	return h, rt, pool, id
}

func sampleOf(h *harness, id datalayer.EndpointID) *datalayer.ServerMetrics {
	ep, ok := h.store.Endpoint(id)
	if !ok {
		return nil
	}
	m, _ := datalayer.Attribute[*datalayer.ServerMetrics](ep, datalayer.CollectorMetrics)
	return m
}

// An idle runtime is scraped once, a request samples it before it is routed and while it runs, and the scraping ends with it
func TestRuntimeIsScrapedOnlyWhileRequestsNeedIt(t *testing.T) {
	pace := scrape.Pace{Deciding: 10 * time.Millisecond, Working: 60 * time.Millisecond, Idle: time.Hour}
	h, rt, _, id := demandHarness(t, pace)
	if got := rt.scrapes.Load(); got != 1 {
		t.Fatalf("%d scrapes of a runtime nothing was sent to", got)
	}
	rt.unscraped(t, 5*pace.Working, "an idle runtime")

	release := rt.hold()
	defer release()
	began := time.Now()
	answered := make(chan reply, 1)
	go func() { answered <- h.do(http.MethodPost, "/v1/chat/completions", modelBody("demand")) }()
	waitFor(t, "the runtime running the request", func() bool { return rt.running.Load() == 1 })
	if got := rt.scrapedBefore.Load(); got < 2 {
		t.Fatalf("the request reached the runtime after %d scrapes, want a fresh one taken for its decision", got)
	}
	held := rt.scrapes.Load()
	waitFor(t, "scrapes while the request is in flight", func() bool { return rt.scrapes.Load() >= held+3 })
	if m := sampleOf(h, id); m.RunningRequests != 1 || m.ActiveAt.Before(began) {
		t.Fatalf("the sample during the request holds %+v", m)
	}
	release()
	if r := <-answered; r.status != http.StatusOK {
		t.Fatalf("status %d: %s", r.status, r.body)
	}
	waitFor(t, "the sample that sees the request end", func() bool { return sampleOf(h, id).RunningRequests == 0 })
	time.Sleep(3 * pace.Working)
	rt.unscraped(t, 5*pace.Working, "a runtime whose request ended")
	if m := sampleOf(h, id); m.Work != 3 {
		t.Fatalf("the work counter reads %v after one request of three prompt tokens", m.Work)
	}
}

// A member's schedule call samples the candidates it names and picks among those samples
func TestScheduleHandlerSamplesItsCandidates(t *testing.T) {
	pace := scrape.Pace{Deciding: 10 * time.Millisecond, Working: 60 * time.Millisecond, Idle: time.Hour}
	h, rt, _, id := demandHarness(t, pace)
	rt.unscraped(t, 3*pace.Working, "an idle runtime")
	before := sampleOf(h, id).UpdatedAt
	ep, _ := h.store.Endpoint(id)
	res, err := (&poolScheduler{g: h.g}).Schedule(context.Background(), &framework.Request{ID: "r1", TargetModel: "demand"}, []datalayer.Endpoint{ep})
	if err != nil {
		t.Fatal(err)
	}
	picked := res.Endpoints()
	if len(picked) != 1 || picked[0].ID() != id {
		t.Fatalf("picked %v", picked)
	}
	m, _ := datalayer.Attribute[*datalayer.ServerMetrics](picked[0], datalayer.CollectorMetrics)
	if rt.scrapes.Load() < 2 || !m.UpdatedAt.After(before) {
		t.Fatalf("the pick read a sample from %v after %d scrapes, want one taken for it", m.UpdatedAt, rt.scrapes.Load())
	}
	time.Sleep(3 * pace.Working)
	rt.unscraped(t, 3*pace.Working, "a runtime after a member's pick")
}
