package gateway

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nickheyer/nebu/internal/auth"
	"github.com/nickheyer/nebu/internal/pools"
	"github.com/nickheyer/nebu/pkg/datalayer"
	"github.com/nickheyer/nebu/pkg/flowcontrol"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/requestcontrol"
)

// Mesh session tokens the guard recognizes as members
type fakeAuthPeers map[string]string

func (p fakeAuthPeers) Peer(token string) (string, bool) {
	id, ok := p[token]
	return id, ok
}

// Members reachable at one base, every dial answering the same server
type fakePeers struct {
	base   string
	client *http.Client
}

func (p *fakePeers) Self() string { return testNode }

func (p *fakePeers) Dial(context.Context, string) (string, *http.Client, error) {
	return p.base, p.client, nil
}

func (p *fakePeers) MoveSlotTo(context.Context, string, string, string, string) error { return nil }

func (p *fakePeers) DropSlotOn(context.Context, string, string, string) error { return nil }

func modelBody(model string) string {
	return strings.Replace(chatBody, `"model":"chat"`, `"model":"`+model+`"`, 1)
}

// The finished trace of a reply
func (h *harness) trace(r reply) *v1.Trace {
	h.t.Helper()
	id := r.header.Get(TraceHeader)
	var tr *v1.Trace
	waitFor(h.t, "trace "+id, func() bool {
		got, ok, _ := h.g.Traces().Get(context.Background(), Viewer{All: true}, id)
		tr = got
		return ok && got.GetFinishedAt() != nil
	})
	return tr
}

// A route cap holds the next request in flow control until a slot frees instead of answering 429
func TestRouteCapQueuesInsteadOfRefusing(t *testing.T) {
	h := newHarness(t)
	st, srv := h.stub("stub-model")
	pool := h.publish("capped", "capped", "vllm", datalayer.AppProtocolHTTP, true, pools.RouteSpec{ServedName: "stub-model", Policy: &v1.Policy{MaxInFlight: 1}}, srv.URL)
	st.Pause()
	replies := make(chan reply, 2)
	go func() { replies <- h.do(http.MethodPost, "/v1/chat/completions", modelBody("capped")) }()
	waitFor(t, "first request dispatched", func() bool { return h.g.flow.InFlight(pool) == 1 })
	go func() { replies <- h.do(http.MethodPost, "/v1/chat/completions", modelBody("capped")) }()
	waitFor(t, "second request queued behind the cap", func() bool { return h.g.flow.Depth(pool)[0] == 1 })
	time.Sleep(100 * time.Millisecond)
	if h.g.flow.InFlight(pool) != 1 || h.g.flow.Depth(pool)[0] != 1 {
		t.Fatalf("in flight %d depth %v while capped", h.g.flow.InFlight(pool), h.g.flow.Depth(pool))
	}
	st.Resume()
	var waits []uint32
	for range 2 {
		r := <-replies
		if r.status != http.StatusOK || r.header.Get("Retry-After") != "" {
			t.Fatalf("status %d retry-after %q: %s", r.status, r.header.Get("Retry-After"), r.body)
		}
		waits = append(waits, h.trace(r).GetQueueWaitMs())
	}
	if slices.Max(waits) < 100 {
		t.Fatalf("queue waits %v ms, the second request must have waited for the slot", waits)
	}
	if got := st.Stats().Served; got != 2 {
		t.Fatalf("stub served %d, want 2", got)
	}
}

// A forwarded request is counted once across the mesh, at the dispatching node, and recognized as a peer's without the guard
func TestForwardedRequestCountedOnce(t *testing.T) {
	t.Run("entry node forwards without applying the cap", func(t *testing.T) {
		var inside, peak atomic.Int64
		release := make(chan struct{})
		conductor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			n := inside.Add(1)
			for p := peak.Load(); n > p && !peak.CompareAndSwap(p, n); p = peak.Load() {
			}
			defer inside.Add(-1)
			if r.Header.Get(EntryHeader) != testNode || r.Header.Get(requestcontrol.HeaderModelName) != "stub-model" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			<-release
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"id":"x","object":"chat.completion","model":"stub-model","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
		}))
		defer conductor.Close()
		h := newHarness(t, func(s *setup) { s.peers = &fakePeers{base: conductor.URL, client: conductor.Client()} })
		h.publish("remote", "remote", "vllm", datalayer.AppProtocolHTTP, true, pools.RouteSpec{ServedName: "stub-model", NodeID: "n2", Policy: &v1.Policy{MaxInFlight: 1}}, "127.0.0.1:1")
		replies := make(chan reply, 2)
		for range 2 {
			go func() { replies <- h.do(http.MethodPost, "/v1/chat/completions", modelBody("remote")) }()
		}
		waitFor(t, "both requests at the conductor", func() bool { return inside.Load() == 2 })
		if got := h.g.routeState("remote").inflight.Load(); got != 0 {
			t.Fatalf("entry node holds %d route slots for forwarded requests", got)
		}
		close(release)
		for range 2 {
			if r := <-replies; r.status != http.StatusOK {
				t.Fatalf("forwarded request %d: %s", r.status, r.body)
			}
		}
		if peak.Load() != 2 {
			t.Fatalf("peak at the conductor %d, the entry node capped a forwarded request", peak.Load())
		}
	})
	t.Run("conductor counts the forwarded request under the peer identity with the guard off", func(t *testing.T) {
		guard := auth.NewGuard("", nil, nil)
		guard.SetPeers(fakeAuthPeers{"session-n2": "n2"})
		h := newHarness(t, func(s *setup) { s.guard = guard })
		st, srv := h.stub("stub-model")
		pool := h.publish("chat", "stub", "vllm", datalayer.AppProtocolHTTP, true, pools.RouteSpec{ServedName: "stub-model", Policy: &v1.Policy{MaxInFlight: 1}}, srv.URL)
		st.Pause()
		replies := make(chan reply, 2)
		go func() { replies <- h.do(http.MethodPost, "/v1/chat/completions", chatBody) }()
		waitFor(t, "local request dispatched", func() bool { return h.g.flow.InFlight(pool) == 1 })
		var forwarded reply
		var done sync.WaitGroup
		done.Go(func() {
			forwarded = h.do(http.MethodPost, "/v1/chat/completions", chatBody, "Authorization", "Bearer session-n2", EntryHeader, "n2", OwnerHeader, "alice")
		})
		waitFor(t, "forwarded request queued behind the cap", func() bool { return h.g.flow.Depth(pool)[0] == 1 })
		st.Resume()
		done.Wait()
		if r := <-replies; r.status != http.StatusOK {
			t.Fatalf("local request %d: %s", r.status, r.body)
		}
		if forwarded.status != http.StatusOK {
			t.Fatalf("forwarded request %d: %s", forwarded.status, forwarded.body)
		}
		tr := h.trace(forwarded)
		if tr.GetEntryNode() != "n2" || tr.GetFairnessId() != "alice" || tr.GetQueueWaitMs() == 0 {
			t.Fatalf("forwarded trace entry %q fairness %q queue wait %d", tr.GetEntryNode(), tr.GetFairnessId(), tr.GetQueueWaitMs())
		}
		if r := h.do(http.MethodPost, "/v1/chat/completions", chatBody, "Authorization", "Bearer stranger", EntryHeader, "n9", OwnerHeader, "mallory"); r.status != http.StatusOK || h.trace(r).GetEntryNode() != "" {
			t.Fatalf("unknown session %d entry %q, the entry header alone must not make a peer", r.status, h.trace(r).GetEntryNode())
		}
	})
}

// Two hundred requests at forty per second over four idle scraped stubs wait under twenty milliseconds at the median
func TestIdleScrapedStubsQueueWait(t *testing.T) {
	h := newHarness(t, func(s *setup) { s.scrape = true })
	ctx := context.Background()
	const stubs = 4
	for i := range stubs {
		_, srv := h.stub("stub-model")
		owner := pools.OwnerRef{Kind: pools.OwnerInstance, ID: fmt.Sprint("idle", i)}
		addr, _ := datalayer.ParseAddress(hostOf(srv))
		if err := h.mgr.Publish(ctx, owner, pools.Membership{Model: key("idle"), Runtime: "vllm", Protocol: datalayer.AppProtocolHTTP, Concurrency: 256}, []pools.Published{{InstanceID: owner.ID, NodeID: testNode, Address: addr, Ready: true}}); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			if _, err := h.mgr.NameRoute(ctx, owner, "idle", pools.RouteSpec{ServedName: "stub-model"}); err != nil {
				t.Fatal(err)
			}
		}
	}
	var pool datalayer.PoolID
	for _, p := range h.store.Pools() {
		if p.Model == key("idle").String() {
			pool = p.ID
		}
	}
	waitFor(t, "every stub scraped", func() bool {
		eps := h.store.Endpoints(pool, datalayer.ReadyAndHealthy)
		if len(eps) != stubs {
			return false
		}
		for _, ep := range eps {
			if m, ok := datalayer.Attribute[*datalayer.ServerMetrics](ep, datalayer.CollectorMetrics); !ok || m.NumGPUBlocks == 0 {
				return false
			}
		}
		return true
	})
	const requests, interval = 200, 25 * time.Millisecond
	replies := make(chan reply, requests)
	var wg sync.WaitGroup
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for range requests {
		<-tick.C
		wg.Go(func() { replies <- h.do(http.MethodPost, "/v1/chat/completions", modelBody("idle")) })
	}
	wg.Wait()
	close(replies)
	var waits []uint32
	for r := range replies {
		if r.status != http.StatusOK {
			t.Fatalf("status %d: %s", r.status, r.body)
		}
		waits = append(waits, h.trace(r).GetQueueWaitMs())
	}
	slices.Sort(waits)
	if p50 := waits[len(waits)/2]; p50 >= 20 {
		t.Fatalf("queue wait p50 %d ms, max %d ms over idle stubs", p50, waits[len(waits)-1])
	}
}

// A user pool of static endpoints with no runtime, metric names or concurrency serves at once
func TestStaticPoolWithoutRuntimeServes(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	st, srv := h.stub("stub-model")
	addr, _ := datalayer.ParseAddress(hostOf(srv))
	const pool = "static-pool"
	if _, err := h.mgr.PutPool(ctx, pool, &v1.PoolSpec{Model: key("static").String(), TargetPorts: []uint32{uint32(addr.Port)}, StaticEndpoints: []*v1.StaticEndpoint{{Address: addr.String()}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.mgr.PutRoute(ctx, "static", &v1.RouteSpec{Rules: []*v1.RewriteRule{{Match: "static", Targets: []*v1.RewriteTarget{{PoolId: pool, Model: "stub-model", Weight: 100}}}}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "static endpoint ready", func() bool { return len(h.store.Endpoints(pool, datalayer.ReadyAndHealthy)) == 1 })
	ep := h.store.Endpoints(pool, nil)[0]
	if _, ok := ep.Attribute(datalayer.CollectorMetrics); ok {
		t.Fatal("static endpoint carries metrics")
	}
	if c, ok := datalayer.Attribute[int](ep, datalayer.CollectorCapacity); ok && c > 0 {
		t.Fatalf("static endpoint carries capacity %d", c)
	}
	for i := range 5 {
		start := time.Now()
		r := h.do(http.MethodPost, "/v1/chat/completions", modelBody("static"))
		if r.status != http.StatusOK || time.Since(start) > time.Second {
			t.Fatalf("request %d: %d after %s: %s", i, r.status, time.Since(start), r.body)
		}
		if w := h.trace(r).GetQueueWaitMs(); w > 50 {
			t.Fatalf("request %d waited %d ms in flow control", i, w)
		}
	}
	if st.Stats().Served != 5 {
		t.Fatalf("stub served %d, want 5", st.Stats().Served)
	}
}

// A fairness id over its request or token quota answers 429 with the wait until its window resets
func TestQuotaAnswers429(t *testing.T) {
	flow := flowcontrol.Config{QueueCapacity: 64, QueueTimeout: 2 * time.Second, SheddableBelow: -1, Tick: 10 * time.Millisecond,
		Quota: flowcontrol.Quota{Requests: 2, Window: time.Hour}, Quotas: map[string]flowcontrol.Quota{"vip": {Tokens: 5, Window: time.Hour}}}
	h := newHarness(t, func(s *setup) { s.flow = &flow })
	_, srv := h.stub("stub-model")
	h.publish("chat", "stub", "vllm", datalayer.AppProtocolHTTP, true, pools.RouteSpec{ServedName: "stub-model"}, srv.URL)
	for i := range 2 {
		if r := h.do(http.MethodPost, "/v1/chat/completions", chatBody); r.status != http.StatusOK {
			t.Fatalf("request %d under the default quota: %d %s", i, r.status, r.body)
		}
	}
	r := h.do(http.MethodPost, "/v1/chat/completions", chatBody)
	if r.status != http.StatusTooManyRequests || r.header.Get("Retry-After") == "" || !strings.Contains(string(r.body), "quota") {
		t.Fatalf("third request %d retry-after %q: %s", r.status, r.header.Get("Retry-After"), r.body)
	}
	if r := h.do(http.MethodPost, "/v1/chat/completions", chatBody, requestcontrol.HeaderFairnessID, "vip"); r.status != http.StatusOK {
		t.Fatalf("first vip request %d: %s", r.status, r.body)
	}
	waitFor(t, "vip tokens metered", func() bool { u := h.g.flow.Usage("vip"); return u.PromptTokens+u.OutputTokens > 5 })
	if r := h.do(http.MethodPost, "/v1/chat/completions", chatBody, requestcontrol.HeaderFairnessID, "vip"); r.status != http.StatusTooManyRequests {
		t.Fatalf("vip over its token quota %d: %s", r.status, r.body)
	}
	if r := h.do(http.MethodPost, "/v1/chat/completions", chatBody, requestcontrol.HeaderFairnessID, "other"); r.status != http.StatusOK {
		t.Fatalf("another id %d: %s", r.status, r.body)
	}
}
