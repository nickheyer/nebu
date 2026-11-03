package scorer_test

import (
	"context"
	"math"
	"slices"
	"testing"
	"time"

	"github.com/nickheyer/nebu/pkg/datalayer"
	"github.com/nickheyer/nebu/pkg/latency"
	"github.com/nickheyer/nebu/pkg/prefixcache"
	"github.com/nickheyer/nebu/pkg/scheduling/framework"
	"github.com/nickheyer/nebu/pkg/scheduling/internal/schedtest"
	"github.com/nickheyer/nebu/pkg/scheduling/plugins"
	"github.com/nickheyer/nebu/pkg/scheduling/plugins/scorer"
)

var ctx = context.Background()

func set(names ...string) map[string]struct{} {
	m := map[string]struct{}{}
	for _, n := range names {
		m[n] = struct{}{}
	}
	return m
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func check(t *testing.T, got map[datalayer.EndpointID]float64, want map[string]float64) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for id, w := range want {
		if g, ok := got[datalayer.EndpointID(id)]; !ok || !near(g, w) {
			t.Fatalf("score of %s = %v, want %v (all %v)", id, g, w, got)
		}
	}
}

func TestMetricScorers(t *testing.T) {
	ep := schedtest.EP
	q := func(n int) datalayer.ServerMetrics { return datalayer.ServerMetrics{QueuedRequests: n} }
	kv := func(u float64) datalayer.ServerMetrics { return datalayer.ServerMetrics{KVCacheUtilization: u} }
	lora := func(max int, running, waiting []string) datalayer.ServerMetrics {
		return datalayer.ServerMetrics{MaxLoRA: max, RunningAdapters: set(running...), WaitingAdapters: set(waiting...)}
	}
	tests := []struct {
		name   string
		scorer framework.Scorer
		req    framework.Request
		eps    []*schedtest.Endpoint
		want   map[string]float64
	}{
		{
			name:   "queue linear from lowest to highest",
			scorer: scorer.NewQueueScorer(),
			eps:    []*schedtest.Endpoint{ep("q0").Metrics(q(0)), ep("q5").Metrics(q(5)), ep("q10").Metrics(q(10)), ep("none")},
			want:   map[string]float64{"q0": 1, "q5": 0.5, "q10": 0, "none": 0},
		},
		{
			name:   "queue equal scores one for all",
			scorer: scorer.NewQueueScorer(),
			eps:    []*schedtest.Endpoint{ep("a").Metrics(q(4)), ep("b").Metrics(q(4))},
			want:   map[string]float64{"a": 1, "b": 1},
		},
		{
			name:   "kv one minus utilization",
			scorer: scorer.NewKVCacheUtilizationScorer(),
			eps:    []*schedtest.Endpoint{ep("a").Metrics(kv(0.25)), ep("b").Metrics(kv(0.9)), ep("over").Metrics(kv(1.2)), ep("none")},
			want:   map[string]float64{"a": 0.75, "b": 0.1, "over": 0, "none": 0},
		},
		{
			name:   "lora tiers",
			scorer: scorer.NewLoRAAffinityScorer(),
			req:    framework.Request{Adapter: "x"},
			eps: []*schedtest.Endpoint{
				ep("active").Metrics(lora(1, []string{"x"}, nil)),
				ep("free").Metrics(lora(2, []string{"y"}, nil)),
				ep("waiting").Metrics(lora(1, []string{"y"}, []string{"x"})),
				ep("full").Metrics(lora(1, []string{"y"}, nil)),
				ep("none"),
			},
			want: map[string]float64{"active": 1, "free": 0.5, "waiting": 0.25, "full": 0, "none": 0},
		},
		{
			name:   "lora without adapter scores one",
			scorer: scorer.NewLoRAAffinityScorer(),
			eps:    []*schedtest.Endpoint{ep("a").Metrics(lora(1, []string{"y"}, nil)), ep("none")},
			want:   map[string]float64{"a": 1, "none": 1},
		},
		{
			name:   "throughput over the highest",
			scorer: scorer.NewThroughputScorer(),
			eps: []*schedtest.Endpoint{
				ep("fast").With(scorer.CollectorThroughput, 200.0),
				ep("half").With(scorer.CollectorThroughput, 100.0),
				ep("none"),
			},
			want: map[string]float64{"fast": 1, "half": 0.5, "none": 0},
		},
		{
			name:   "throughput all unknown scores zero",
			scorer: scorer.NewThroughputScorer(),
			eps:    []*schedtest.Endpoint{ep("a"), ep("b")},
			want:   map[string]float64{"a": 0, "b": 0},
		},
		{
			name:   "assumed fewer is better, never counted holding none",
			scorer: scorer.NewAssumedLoadScorer(),
			eps: []*schedtest.Endpoint{
				ep("never"),
				ep("two").With(datalayer.CollectorAssumed, 2),
				ep("four").With(datalayer.CollectorAssumed, 4),
			},
			want: map[string]float64{"never": 1, "two": 0.5, "four": 0},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			check(t, tc.scorer.Score(ctx, framework.NewCycleState(), &tc.req, schedtest.Eps(tc.eps...)), tc.want)
		})
	}
}

func TestPrefixCacheScorer(t *testing.T) {
	idx := schedtest.NewIndexer(4)
	req := &framework.Request{ID: "r", TargetModel: "m", Prompt: "aaaabbbbcccc"}
	hashes := idx.Hash("m", "", req.Prompt, nil)
	if len(hashes) != 3 {
		t.Fatalf("fake hashed %d blocks", len(hashes))
	}
	idx.Record("full", hashes)
	idx.Record("one", hashes[:1])
	s := scorer.NewPrefixCacheScorer(idx, nil, 0, schedtest.NewClock(), time.Minute)
	eps := schedtest.Eps(schedtest.EP("full"), schedtest.EP("one"), schedtest.EP("cold"))
	state := framework.NewCycleState()
	before := idx.HashCalls()
	check(t, s.Score(ctx, state, req, eps), map[string]float64{"full": 1, "one": 1.0 / 3, "cold": 0})
	check(t, s.Score(ctx, state, req, eps), map[string]float64{"full": 1, "one": 1.0 / 3, "cold": 0})
	if n := idx.HashCalls() - before; n != 1 {
		t.Fatalf("prompt hashed %d times in one cycle", n)
	}
	kept, ok := framework.ReadState[[]prefixcache.BlockHash](state, plugins.PrefixHashesKey)
	if !ok || len(kept) != 3 {
		t.Fatalf("cycle state holds %v", kept)
	}

	t.Run("pre request records on every routed head", func(t *testing.T) {
		res := &framework.Result{
			PrimaryProfile: "decode",
			ProfileResults: map[string]*framework.ProfileRunResult{
				"decode":  {Endpoints: schedtest.Eps(schedtest.EP("cold"), schedtest.EP("fallback"))},
				"prefill": {Endpoints: schedtest.Eps(schedtest.EP("pre"))},
			},
			State: state,
		}
		calls := idx.HashCalls()
		s.PreRequest(ctx, req, res)
		if idx.HashCalls() != calls {
			t.Fatal("pre request hashed again instead of reading the cycle state")
		}
		got := idx.Holders(hashes)
		want := []string{"cold", "full", "pre"}
		if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
			t.Fatalf("holders %v, want %v", got, want)
		}
	})

	t.Run("pre request hashes when another process scheduled", func(t *testing.T) {
		other := &framework.Request{ID: "r2", TargetModel: "m", Prompt: "zzzzyyyy"}
		s.PreRequest(ctx, other, schedtest.Result("p", schedtest.EP("remote")))
		if got := idx.Holders(idx.Hash("m", "", other.Prompt, nil)); len(got) != 1 || got[0] != "remote" {
			t.Fatalf("holders %v", got)
		}
	})

	t.Run("adapter scopes the hashes", func(t *testing.T) {
		scoped := &framework.Request{ID: "r3", TargetModel: "m", Adapter: "x", Prompt: req.Prompt}
		check(t, s.Score(ctx, framework.NewCycleState(), scoped, eps), map[string]float64{"full": 0, "one": 0, "cold": 0})
	})
}

func TestLatencyScorer(t *testing.T) {
	ms := time.Millisecond
	pred := &schedtest.Predictor{
		TTFT: map[datalayer.EndpointID]time.Duration{"in": 80 * ms, "ttft-half": 150 * ms, "ttft-out": 250 * ms, "tpot-half": 50 * ms},
		TPOT: map[datalayer.EndpointID]time.Duration{"in": 5 * ms, "ttft-half": 5 * ms, "ttft-out": 5 * ms, "tpot-half": 15 * ms},
	}
	eps := schedtest.Eps(schedtest.EP("in"), schedtest.EP("ttft-half"), schedtest.EP("ttft-out"), schedtest.EP("tpot-half"))
	tests := []struct {
		name string
		obj  framework.Objective
		want map[string]float64
	}{
		{"both objectives", framework.Objective{TTFT: 100 * ms, TPOT: 10 * ms}, map[string]float64{"in": 1, "ttft-half": 0.5, "ttft-out": 0, "tpot-half": 0.5}},
		{"ttft objective alone", framework.Objective{TTFT: 100 * ms}, map[string]float64{"in": 1, "ttft-half": 0.5, "ttft-out": 0, "tpot-half": 1}},
		{"no objective ranks predicted ttft plus tpot over the expected output", framework.Objective{}, map[string]float64{"in": 1, "ttft-half": 100.0 / 170, "ttft-out": 0, "tpot-half": 100.0 / 170}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := scorer.NewLatencyScorer(pred, schedtest.NewIndexer(4), schedtest.NewClock(), time.Minute)
			check(t, s.Score(ctx, framework.NewCycleState(), &framework.Request{ID: "r", Objective: tc.obj, ExpectedOutputTokens: 11}, eps), tc.want)
		})
	}
	t.Run("no objective and no expected output ranks ttft alone", func(t *testing.T) {
		s := scorer.NewLatencyScorer(pred, schedtest.NewIndexer(4), schedtest.NewClock(), time.Minute)
		check(t, s.Score(ctx, framework.NewCycleState(), &framework.Request{ID: "r"}, eps), map[string]float64{"in": 170.0 / 200, "ttft-half": 100.0 / 200, "ttft-out": 0, "tpot-half": 1})
	})
}

func TestExpertParallelScorer(t *testing.T) {
	ep := schedtest.EP
	busy := datalayer.ServerMetrics{QueuedRequests: 2, RunningRequests: 6}
	tests := []struct {
		name string
		eps  []*schedtest.Endpoint
		want map[string]float64
	}{
		{
			name: "load per rank, a wide seat carrying more at the same score",
			eps: []*schedtest.Endpoint{
				ep("narrow").Metrics(busy),
				ep("wide").Metrics(busy).Label(datalayer.LabelExpertParallel, "4"),
				ep("wider").Metrics(busy).Label(datalayer.LabelExpertParallel, "8").With(datalayer.CollectorAssumed, 8),
				ep("idle"),
			},
			want: map[string]float64{"narrow": 0, "wide": 0.75, "wider": 0.75, "idle": 1},
		},
		{
			name: "unparsable widths count as one",
			eps:  []*schedtest.Endpoint{ep("a").With(datalayer.CollectorAssumed, 4).Label(datalayer.LabelExpertParallel, "x"), ep("b").With(datalayer.CollectorAssumed, 4).Label(datalayer.LabelExpertParallel, "0")},
			want: map[string]float64{"a": 1, "b": 1},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			check(t, scorer.NewExpertParallelScorer().Score(ctx, nil, &framework.Request{}, schedtest.Eps(tc.eps...)), tc.want)
		})
	}
}

func TestPrefixCacheScorerRingFallback(t *testing.T) {
	idx := schedtest.NewIndexer(4)
	ring := prefixcache.NewHashRing(64)
	s := scorer.NewPrefixCacheScorer(idx, ring, 1.25, schedtest.NewClock(), time.Minute)
	other := scorer.NewPrefixCacheScorer(schedtest.NewIndexer(4), prefixcache.NewHashRing(64), 1.25, schedtest.NewClock(), time.Minute)
	eps := schedtest.Eps(schedtest.EP("a"), schedtest.EP("b"), schedtest.EP("c"))
	req := &framework.Request{ID: "r", TargetModel: "m", Prompt: "zzzzyyyyxxxx"}
	got := s.Score(ctx, framework.NewCycleState(), req, eps)
	var winner datalayer.EndpointID
	ones := 0
	for id, v := range got {
		if v == 1 {
			winner, ones = id, ones+1
		}
	}
	if ones != 1 {
		t.Fatalf("ring scored %v", got)
	}
	if again := other.Score(ctx, framework.NewCycleState(), req, []datalayer.Endpoint{eps[2], eps[0], eps[1]}); again[winner] != 1 {
		t.Fatalf("another gateway with the same candidates in another order scored %v, want %s", again, winner)
	}
	if far := s.Score(ctx, framework.NewCycleState(), &framework.Request{ID: "r2", TargetModel: "m", Prompt: "zzzzyyyyxxxx, continued"}, eps); far[winner] != 1 {
		t.Fatalf("a longer prompt with the same first block scored %v, want %s", far, winner)
	}

	t.Run("bounded load skips the loaded ring endpoint", func(t *testing.T) {
		loaded := make([]datalayer.Endpoint, 3)
		for i, ep := range eps {
			e := schedtest.EP(string(ep.ID()))
			if ep.ID() == winner {
				e.With(datalayer.CollectorAssumed, 9)
			}
			loaded[i] = e
		}
		got := s.Score(ctx, framework.NewCycleState(), req, loaded)
		if got[winner] != 0 {
			t.Fatalf("loaded ring endpoint still scored %v", got)
		}
		ones := 0
		for _, v := range got {
			if v == 1 {
				ones++
			}
		}
		if ones != 1 {
			t.Fatalf("no other endpoint took the prefix: %v", got)
		}
	})

	t.Run("an index match outranks the ring", func(t *testing.T) {
		idx.Record("c", idx.Hash("m", "", req.Prompt, nil))
		if got := s.Score(ctx, framework.NewCycleState(), req, eps); got["c"] != 1 || got["a"]+got["b"] != 0 {
			t.Fatalf("scored %v", got)
		}
	})
}

func TestPrefixPinFollowsTheAnswer(t *testing.T) {
	req := &framework.Request{ID: "r", TargetModel: "m", Prompt: "aaaabbbbcccc"}
	a, b, pre := schedtest.EP("a"), schedtest.EP("b"), schedtest.EP("pre")
	pd := func() *framework.Result {
		return &framework.Result{PrimaryProfile: "decode", ProfileResults: map[string]*framework.ProfileRunResult{
			"decode":  {Endpoints: schedtest.Eps(a, b)},
			"prefill": {Endpoints: schedtest.Eps(pre)},
		}, State: framework.NewCycleState()}
	}
	tests := []struct {
		name string
		resp *framework.Response
		want []string
	}{
		{"success keeps the pins", &framework.Response{Endpoint: a, Status: 200}, []string{"a", "pre"}},
		{"error status unpins the answering endpoint", &framework.Response{Endpoint: a, Status: 503}, []string{"pre"}},
		{"transport error unpins the answering endpoint", &framework.Response{Endpoint: a, Err: context.DeadlineExceeded}, []string{"pre"}},
		{"no endpoint answered unpins every head", &framework.Response{Err: context.DeadlineExceeded}, nil},
		{"fallback moves the pin to the endpoint that served", &framework.Response{Endpoint: b, Status: 200}, []string{"b", "pre"}},
		{"fallback that failed unpins both", &framework.Response{Endpoint: b, Status: 500}, []string{"pre"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			idx := schedtest.NewIndexer(4)
			s := scorer.NewPrefixCacheScorer(idx, nil, 0, schedtest.NewClock(), time.Minute)
			s.PreRequest(ctx, req, pd())
			s.ResponseComplete(ctx, req, tc.resp)
			if got := idx.Holders(idx.Hash("m", "", req.Prompt, nil)); !slices.Equal(got, tc.want) || s.Pending() != 0 {
				t.Fatalf("holders %v pending %d, want %v", got, s.Pending(), tc.want)
			}
		})
	}

	t.Run("a success scheduled elsewhere pins the endpoint that served", func(t *testing.T) {
		idx := schedtest.NewIndexer(4)
		s := scorer.NewPrefixCacheScorer(idx, nil, 0, schedtest.NewClock(), time.Minute)
		s.ResponseComplete(ctx, req, &framework.Response{Endpoint: b, Status: 200})
		if got := idx.Holders(idx.Hash("m", "", req.Prompt, nil)); !slices.Equal(got, []string{"b"}) {
			t.Fatalf("holders %v", got)
		}
	})

	t.Run("pins of requests that never complete are forgotten after the ttl", func(t *testing.T) {
		clock := schedtest.NewClock()
		s := scorer.NewPrefixCacheScorer(schedtest.NewIndexer(4), nil, 0, clock, time.Minute)
		s.PreRequest(ctx, &framework.Request{ID: "lost", TargetModel: "m", Prompt: "aaaabbbb"}, schedtest.Result("p", a))
		clock.Advance(2 * time.Minute)
		s.PreRequest(ctx, &framework.Request{ID: "next", TargetModel: "m", Prompt: "aaaabbbb"}, schedtest.Result("p", a))
		if s.Pending() != 1 {
			t.Fatalf("pending %d", s.Pending())
		}
	})
}

func TestLatencyScorerLearning(t *testing.T) {
	ms := time.Millisecond
	idx := schedtest.NewIndexer(4)
	clock := schedtest.NewClock()
	req := &framework.Request{ID: "r", TargetModel: "m", Prompt: "aaaabbbb", PromptTokens: 100, Objective: framework.Objective{TTFT: time.Second}}
	idx.Record("e1", idx.Hash("m", "", req.Prompt, nil)[:1])
	e1 := schedtest.EP("e1").
		Metrics(datalayer.ServerMetrics{QueuedRequests: 3, RunningRequests: 2, KVCacheUtilization: 0.4}).
		With(datalayer.CollectorAssumed, 1)
	e2 := schedtest.EP("e2")
	e3 := schedtest.EP("e3")
	want := latency.Features{PromptTokens: 100, CachedTokens: 50, QueuedRequests: 3, RunningRequests: 2, KVCacheUtilization: 0.4, AssumedRequests: 1}
	t0 := clock.Now()
	resp := &framework.Response{Endpoint: e1, StartedAt: t0, FirstTokenAt: t0.Add(90 * ms), FinishedAt: t0.Add(180 * ms), OutputTokens: 10}

	t.Run("observes the features read at pick", func(t *testing.T) {
		pred := &schedtest.Predictor{}
		s := scorer.NewLatencyScorer(pred, idx, clock, time.Minute)
		state := framework.NewCycleState()
		s.Score(ctx, state, req, schedtest.Eps(e1, e2, e3))
		if asked := pred.Asked(); len(asked) != 3 || asked[0] != want {
			t.Fatalf("predicted with %+v, want first %+v", asked, want)
		}
		res := schedtest.Result("p", e1, e2)
		res.State = state
		s.PreRequest(ctx, req, res)
		s.ResponseComplete(ctx, req, resp)
		obs := pred.Observed()
		if len(obs) != 1 || obs[0].Endpoint != "e1" || obs[0].Features != want || obs[0].TTFT != 90*ms || obs[0].TPOT != 10*ms {
			t.Fatalf("observed %+v", obs)
		}
		if s.Pending() != 0 {
			t.Fatalf("%d requests still held after completion", s.Pending())
		}
	})

	t.Run("failed response teaches nothing and is forgotten", func(t *testing.T) {
		pred := &schedtest.Predictor{}
		s := scorer.NewLatencyScorer(pred, idx, clock, time.Minute)
		s.Score(ctx, framework.NewCycleState(), req, schedtest.Eps(e1))
		failed := *resp
		failed.Err = context.DeadlineExceeded
		s.ResponseComplete(ctx, req, &failed)
		if len(pred.Observed()) != 0 || s.Pending() != 0 {
			t.Fatalf("observed %v pending %d", pred.Observed(), s.Pending())
		}
	})

	t.Run("captures features at dispatch when another process scheduled", func(t *testing.T) {
		pred := &schedtest.Predictor{}
		s := scorer.NewLatencyScorer(pred, idx, clock, time.Minute)
		s.PreRequest(ctx, req, schedtest.Result("p", e1))
		s.ResponseComplete(ctx, req, resp)
		if obs := pred.Observed(); len(obs) != 1 || obs[0].Features != want {
			t.Fatalf("observed %+v", obs)
		}
	})

	t.Run("captures features of a routed endpoint no profile scored", func(t *testing.T) {
		pred := &schedtest.Predictor{}
		s := scorer.NewLatencyScorer(pred, idx, clock, time.Minute)
		s.Score(ctx, framework.NewCycleState(), req, schedtest.Eps(e2, e3))
		s.PreRequest(ctx, req, schedtest.Result("p", e1, e2))
		s.ResponseComplete(ctx, req, resp)
		if obs := pred.Observed(); len(obs) != 1 || obs[0].Endpoint != "e1" || obs[0].Features != want {
			t.Fatalf("observed %+v", obs)
		}
	})

	t.Run("forgets requests that never complete after the ttl", func(t *testing.T) {
		pred := &schedtest.Predictor{}
		c := schedtest.NewClock()
		s := scorer.NewLatencyScorer(pred, idx, c, time.Minute)
		s.Score(ctx, framework.NewCycleState(), &framework.Request{ID: "lost"}, schedtest.Eps(e1))
		c.Advance(2 * time.Minute)
		s.Score(ctx, framework.NewCycleState(), &framework.Request{ID: "next"}, schedtest.Eps(e1))
		if s.Pending() != 1 {
			t.Fatalf("pending %d, want only the fresh request", s.Pending())
		}
	})
}

func TestSessionAffinityScorer(t *testing.T) {
	clock := schedtest.NewClock()
	eps := schedtest.Eps(schedtest.EP("a"), schedtest.EP("b"), schedtest.EP("c"))
	s := scorer.NewSessionAffinityScorer("X-Session-Id", time.Minute, 2, clock)
	req := func(sid string) *framework.Request {
		return &framework.Request{ID: sid, Headers: map[string]string{"x-session-id": sid}}
	}

	check(t, s.Score(ctx, nil, &framework.Request{}, eps), map[string]float64{"a": 0, "b": 0, "c": 0})
	check(t, s.Score(ctx, nil, req("s1"), eps), map[string]float64{"a": 0, "b": 0, "c": 0})
	s.PreRequest(ctx, req("s1"), schedtest.Result("p", eps[1], eps[0]))
	check(t, s.Score(ctx, nil, req("s1"), eps), map[string]float64{"a": 0, "b": 1, "c": 0})
	check(t, s.Score(ctx, nil, req("s2"), eps), map[string]float64{"a": 0, "b": 0, "c": 0})

	clock.Advance(59 * time.Second)
	check(t, s.Score(ctx, nil, req("s1"), eps), map[string]float64{"a": 0, "b": 1, "c": 0})
	clock.Advance(2 * time.Second)
	check(t, s.Score(ctx, nil, req("s1"), eps), map[string]float64{"a": 0, "b": 0, "c": 0})

	s.PreRequest(ctx, req("s1"), schedtest.Result("p", eps[0]))
	s.PreRequest(ctx, req("s2"), schedtest.Result("p", eps[1]))
	s.PreRequest(ctx, req("s3"), schedtest.Result("p", eps[2]))
	if s.Sessions() != 2 {
		t.Fatalf("holding %d sessions past the limit", s.Sessions())
	}
	check(t, s.Score(ctx, nil, req("s1"), eps), map[string]float64{"a": 0, "b": 0, "c": 0})
	check(t, s.Score(ctx, nil, req("s3"), eps), map[string]float64{"a": 0, "b": 0, "c": 1})

	s.ResponseComplete(ctx, req("s3"), &framework.Response{Endpoint: eps[0]})
	check(t, s.Score(ctx, nil, req("s3"), eps), map[string]float64{"a": 1, "b": 0, "c": 0})
	s.ResponseComplete(ctx, req("s3"), &framework.Response{Endpoint: eps[1], Err: context.Canceled})
	check(t, s.Score(ctx, nil, req("s3"), eps), map[string]float64{"a": 1, "b": 0, "c": 0})
}

func TestScorerNames(t *testing.T) {
	type named interface {
		framework.Scorer
		SetName(string)
	}
	clock := schedtest.NewClock()
	tests := []struct {
		s   named
		typ string
	}{
		{scorer.NewQueueScorer(), scorer.QueueScorerType},
		{scorer.NewKVCacheUtilizationScorer(), scorer.KVCacheUtilizationScorerType},
		{scorer.NewLoRAAffinityScorer(), scorer.LoRAAffinityScorerType},
		{scorer.NewPrefixCacheScorer(schedtest.NewIndexer(4), nil, 0, clock, time.Minute), scorer.PrefixCacheScorerType},
		{scorer.NewExpertParallelScorer(), scorer.ExpertParallelScorerType},
		{scorer.NewThroughputScorer(), scorer.ThroughputScorerType},
		{scorer.NewLatencyScorer(&schedtest.Predictor{}, schedtest.NewIndexer(4), clock, time.Minute), scorer.LatencyScorerType},
		{scorer.NewAssumedLoadScorer(), scorer.AssumedLoadScorerType},
		{scorer.NewSessionAffinityScorer("h", time.Minute, 1, clock), scorer.SessionAffinityScorerType},
	}
	for _, tc := range tests {
		t.Run(tc.typ, func(t *testing.T) {
			if got := tc.s.TypedName(); got.Type != tc.typ || got.Name != tc.typ {
				t.Fatalf("default name %+v", got)
			}
			tc.s.SetName("custom")
			if got := tc.s.TypedName(); got.Name != "custom" {
				t.Fatalf("renamed %+v", got)
			}
		})
	}
}
