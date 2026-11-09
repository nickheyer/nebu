package config_test

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/nickheyer/nebu/pkg/datalayer"
	"github.com/nickheyer/nebu/pkg/latency"
	"github.com/nickheyer/nebu/pkg/prefixcache"
	"github.com/nickheyer/nebu/pkg/scheduling/config"
	"github.com/nickheyer/nebu/pkg/scheduling/framework"
	"github.com/nickheyer/nebu/pkg/scheduling/internal/schedtest"
	"github.com/nickheyer/nebu/pkg/scheduling/registry"
)

// A real store, index and predictor with n fresh endpoints in pool p
func realDeps(n int) (registry.Deps, func()) {
	store := datalayer.NewStore()
	store.PutPool(datalayer.Pool{ID: "p", Model: "m"})
	for i := range n {
		id := datalayer.EndpointID(fmt.Sprintf("e%02d", i))
		store.UpsertEndpoint(datalayer.EndpointSpec{ID: id, Pool: "p", Address: datalayer.Address{Host: "10.0.0.1", Port: 8000 + i}, Ready: true})
		store.SetAttribute(id, datalayer.CollectorMetrics, &datalayer.ServerMetrics{QueuedRequests: i % 5, RunningRequests: i % 7, KVCacheUtilization: float64(i%10) / 10, BlockSize: 16, NumGPUBlocks: 4096, UpdatedAt: time.Now().Add(time.Hour)})
	}
	idx := prefixcache.New(prefixcache.Config{})
	return registry.Deps{Store: store, Indexer: idx, Predictor: latency.New(latency.Config{})}, idx.Close
}

func preRequest(life []framework.Plugin, req *framework.Request, res *framework.Result) {
	for _, p := range life {
		if h, ok := p.(framework.PreRequestPlugin); ok {
			h.PreRequest(context.Background(), req, res)
		}
	}
}

func complete(life []framework.Plugin, req *framework.Request, resp *framework.Response) {
	for _, p := range life {
		if h, ok := p.(framework.ResponseCompletePlugin); ok {
			h.ResponseComplete(context.Background(), req, resp)
		}
	}
}

func assumedOn(t *testing.T, store datalayer.Store, id datalayer.EndpointID) int {
	t.Helper()
	ep, ok := store.Endpoint(id)
	if !ok {
		t.Fatalf("endpoint %s gone", id)
	}
	n, _ := datalayer.Attribute[int](ep, datalayer.CollectorAssumed)
	return n
}

// Two picks made before either request's hooks run land on different seats of capacity one, a third is refused
func TestPickAndCountAtomic(t *testing.T) {
	deps, done := realDeps(0)
	defer done()
	store := deps.Store
	for i, q := range []int{0, 9} {
		id := datalayer.EndpointID(fmt.Sprintf("s%d", i))
		store.UpsertEndpoint(datalayer.EndpointSpec{ID: id, Pool: "p", Address: datalayer.Address{Host: "h", Port: 9000 + i}, Ready: true})
		store.SetAttribute(id, datalayer.CollectorMetrics, &datalayer.ServerMetrics{QueuedRequests: q, UpdatedAt: time.Now().Add(time.Hour)})
		store.SetAttribute(id, datalayer.CollectorCapacity, 1)
	}
	eps := store.Endpoints("p", nil)
	sched, life, err := config.Load([]byte(config.DefaultConfig), deps)
	if err != nil {
		t.Fatal(err)
	}
	reqs := []*framework.Request{{ID: "a", TargetModel: "m", Prompt: "x"}, {ID: "b", TargetModel: "m", Prompt: "y"}}
	var results []*framework.Result
	for _, r := range reqs {
		res, err := sched.Schedule(context.Background(), r, eps)
		if err != nil {
			t.Fatal(err)
		}
		results = append(results, res)
	}
	if results[0].Endpoints()[0].ID() != "s0" || results[1].Endpoints()[0].ID() != "s1" {
		t.Fatalf("picks %s and %s, want s0 then s1", results[0].Endpoints()[0].ID(), results[1].Endpoints()[0].ID())
	}
	if len(results[1].Endpoints()) != 1 {
		t.Fatalf("second pick offered %v as fallbacks past a full seat", schedtest.IDs(results[1].Endpoints()))
	}
	_, err = sched.Schedule(context.Background(), &framework.Request{ID: "c", TargetModel: "m", Prompt: "z"}, eps)
	if !errors.Is(err, framework.ErrSaturated) {
		t.Fatalf("third pick err %v, want ErrSaturated", err)
	}
	for i, r := range reqs {
		preRequest(life, r, results[i])
	}
	if assumedOn(t, store, "s0") != 1 || assumedOn(t, store, "s1") != 1 {
		t.Fatalf("hooks counted again: s0 %d s1 %d", assumedOn(t, store, "s0"), assumedOn(t, store, "s1"))
	}
	complete(life, reqs[0], &framework.Response{Endpoint: results[0].Endpoints()[0], Status: 200})
	res, err := sched.Schedule(context.Background(), &framework.Request{ID: "d", TargetModel: "m", Prompt: "w"}, store.Endpoints("p", nil))
	if err != nil || res.Endpoints()[0].ID() != "s0" {
		t.Fatalf("after completion pick %v err %v, want s0", res, err)
	}
}

// Two schedulers built from one configuration over one store spread load with no seat over its capacity
func TestCooperativeSchedulers(t *testing.T) {
	deps, done := realDeps(4)
	defer done()
	store := deps.Store
	const capacity = 3
	for _, ep := range store.Endpoints("p", nil) {
		store.SetAttribute(ep.ID(), datalayer.CollectorCapacity, capacity)
	}
	type sched struct {
		s    framework.Scheduler
		life []framework.Plugin
	}
	var scheds []sched
	for range 2 {
		s, life, err := config.Load([]byte(config.DefaultConfig), deps)
		if err != nil {
			t.Fatal(err)
		}
		scheds = append(scheds, sched{s, life})
	}
	const workers, per, inFlight = 8, 60, 10
	sem := make(chan struct{}, inFlight)
	var picks sync.Map
	var over, requeued atomic.Int64
	var wg sync.WaitGroup
	for w := range workers {
		sc := scheds[w%2]
		wg.Go(func() {
			for i := 0; i < per; {
				sem <- struct{}{}
				req := &framework.Request{ID: fmt.Sprintf("%d-%d", w, i), TargetModel: "m", Prompt: fmt.Sprintf("worker %d prompt %d", w, i%3), PromptTokens: 20, ExpectedOutputTokens: 20}
				res, err := sc.s.Schedule(context.Background(), req, store.Endpoints("p", nil))
				if errors.Is(err, framework.ErrSaturated) {
					requeued.Add(1)
					<-sem
					continue
				}
				if err != nil {
					t.Error(err)
					<-sem
					return
				}
				head := res.Endpoints()[0].ID()
				if n := assumedOn(t, store, head); n > capacity {
					over.Add(1)
				}
				c, _ := picks.LoadOrStore(head, new(atomic.Int64))
				c.(*atomic.Int64).Add(1)
				preRequest(sc.life, req, res)
				time.Sleep(200 * time.Microsecond)
				complete(sc.life, req, &framework.Response{Endpoint: res.Endpoints()[0], Status: 200})
				<-sem
				i++
			}
		})
	}
	wg.Wait()
	if over.Load() > 0 {
		t.Fatalf("%d picks landed on a seat past its capacity", over.Load())
	}
	spread := 0
	picks.Range(func(k, v any) bool {
		if v.(*atomic.Int64).Load() > 0 {
			spread++
		}
		return true
	})
	if spread != 4 {
		t.Fatalf("load reached %d of 4 seats", spread)
	}
	for _, ep := range store.Endpoints("p", nil) {
		if n := assumedOn(t, store, ep.ID()); n != 0 {
			t.Fatalf("%s holds %d after every completion", ep.ID(), n)
		}
	}
	t.Logf("requeued %d of %d", requeued.Load(), workers*per)
}

// The real default profile decides concurrently with its hooks without races or refusals
func TestConcurrentDefaultProfile(t *testing.T) {
	deps, done := realDeps(6)
	defer done()
	sched, life, err := config.Load([]byte(config.DefaultConfig), deps)
	if err != nil {
		t.Fatal(err)
	}
	eps := deps.Store.Endpoints("p", nil)
	var wg sync.WaitGroup
	var picks [6]atomic.Int64
	for w := range 16 {
		wg.Go(func() {
			for i := range 100 {
				req := &framework.Request{ID: fmt.Sprintf("%d-%d", w, i), TargetModel: "m", Prompt: fmt.Sprintf("shared system prompt, user %d", w%4), PromptTokens: 30, ExpectedOutputTokens: 30, Objective: framework.Objective{TTFT: time.Second}}
				res, err := sched.Schedule(context.Background(), req, eps)
				if err != nil {
					t.Error(err)
					return
				}
				var slot int
				fmt.Sscanf(string(res.Endpoints()[0].ID()), "e%02d", &slot)
				picks[slot].Add(1)
				preRequest(life, req, res)
				start := time.Now()
				complete(life, req, &framework.Response{Endpoint: res.Endpoints()[0], Status: 200, StartedAt: start, FirstTokenAt: start.Add(time.Millisecond), FinishedAt: start.Add(20 * time.Millisecond), OutputTokens: 30})
			}
		})
	}
	wg.Wait()
	total := int64(0)
	for i := range picks {
		total += picks[i].Load()
	}
	if total != 1600 {
		t.Fatalf("decided %d of 1600", total)
	}
}

// A seat holding the prompt's prefix wins while busier but under its caps, and loses once over any of them
func TestPinnedSeatWinsUnderCap(t *testing.T) {
	clock := schedtest.NewClock()
	d := deps(clock)
	idx := d.Indexer.(*schedtest.Indexer)
	s, _, err := config.Load([]byte(config.DefaultReplicasConfig), d)
	if err != nil {
		t.Fatal(err)
	}
	req := &framework.Request{ID: "r", TargetModel: "m", Prompt: "aaaabbbbcccc", PromptTokens: 12}
	idx.Record("pinned", idx.Hash("m", "", req.Prompt, nil))
	metrics := func(q int, kv float64) datalayer.ServerMetrics {
		return datalayer.ServerMetrics{QueuedRequests: q, KVCacheUtilization: kv, UpdatedAt: clock.Now()}
	}
	tests := []struct {
		name   string
		pinned *schedtest.Endpoint
		want   string
	}{
		{"busier under the caps", schedtest.EP("pinned").Metrics(metrics(2, 0.5)).With(datalayer.CollectorCapacity, 4).With(datalayer.CollectorAssumed, 3), "pinned"},
		{"at its request capacity", schedtest.EP("pinned").Metrics(metrics(2, 0.5)).With(datalayer.CollectorCapacity, 4).With(datalayer.CollectorAssumed, 4), "other"},
		{"over the kv threshold", schedtest.EP("pinned").Metrics(metrics(2, 0.96)).With(datalayer.CollectorCapacity, 4), "other"},
		{"stale metrics", schedtest.EP("pinned").Metrics(datalayer.ServerMetrics{QueuedRequests: 2, UpdatedAt: clock.Now().Add(-3 * time.Second)}), "other"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			other := schedtest.EP("other").Metrics(metrics(0, 0))
			res, err := s.Schedule(context.Background(), req, schedtest.Eps(tc.pinned, other))
			if err != nil {
				t.Fatal(err)
			}
			if got := res.Endpoints()[0].ID(); string(got) != tc.want {
				t.Fatalf("picked %s with scores %v, want %s", got, res.ProfileResults["replicas"].Scores, tc.want)
			}
		})
	}
}

// The objective filter keeps the request off an endpoint predicted to miss, even one holding its prefix
func TestObjectiveRoutesToEndpointsPredictedToMeet(t *testing.T) {
	ms := time.Millisecond
	clock := schedtest.NewClock()
	d := deps(clock)
	d.Predictor = &schedtest.Predictor{TTFT: map[datalayer.EndpointID]time.Duration{"slow": 800 * ms, "fast": 100 * ms}}
	idx := d.Indexer.(*schedtest.Indexer)
	s, _, err := config.Load([]byte(config.DefaultConfig), d)
	if err != nil {
		t.Fatal(err)
	}
	prompt := "aaaabbbbcccc"
	idx.Record("slow", idx.Hash("m", "", prompt, nil))
	fresh := datalayer.ServerMetrics{UpdatedAt: clock.Now()}
	eps := schedtest.Eps(schedtest.EP("slow").Metrics(fresh), schedtest.EP("fast").Metrics(fresh))
	tests := []struct {
		name string
		obj  framework.Objective
		want string
	}{
		{"without an objective the prefix hit wins", framework.Objective{}, "slow"},
		{"a ttft objective drops the endpoint predicted to miss", framework.Objective{TTFT: 500 * ms}, "fast"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res, err := s.Schedule(context.Background(), &framework.Request{ID: tc.name, TargetModel: "m", Prompt: prompt, PromptTokens: 12, ExpectedOutputTokens: 10, Objective: tc.obj}, eps)
			if err != nil {
				t.Fatal(err)
			}
			if got := res.Endpoints()[0].ID(); string(got) != tc.want {
				t.Fatalf("picked %s with scores %v, want %s", got, res.ProfileResults["default"].Scores, tc.want)
			}
		})
	}
	_, err = s.Schedule(context.Background(), &framework.Request{ID: "hopeless", TargetModel: "m", Prompt: prompt, Objective: framework.Objective{TTFT: 50 * ms}}, eps)
	if !errors.Is(err, framework.ErrSaturated) {
		t.Fatalf("every endpoint missing err %v, want ErrSaturated", err)
	}
}

// An adapter request finding every endpoint at its adapter limit is refused as saturated, not as unservable
func TestLoRAEverySlotTakenSaturates(t *testing.T) {
	clock := schedtest.NewClock()
	s, _, err := config.Load([]byte(config.DefaultConfig), deps(clock))
	if err != nil {
		t.Fatal(err)
	}
	full := datalayer.ServerMetrics{UpdatedAt: clock.Now(), MaxLoRA: 1, RunningAdapters: map[string]struct{}{"other": {}}}
	_, err = s.Schedule(context.Background(), &framework.Request{ID: "r", TargetModel: "m", Adapter: "sql"}, schedtest.Eps(schedtest.EP("a").Metrics(full), schedtest.EP("b").Metrics(full)))
	if !errors.Is(err, framework.ErrSaturated) || !strings.Contains(err.Error(), "lora-affinity-filter") {
		t.Fatalf("err %v, want the lora filter's ErrSaturated", err)
	}
}

// The CPU time the process has used so far, user and system together
func cpuTime(t *testing.T) time.Duration {
	t.Helper()
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		t.Fatal(err)
	}
	return time.Duration(ru.Utime.Nano() + ru.Stime.Nano())
}

// One full decision of the default configuration with its hooks costs under ten milliseconds of CPU on average under load
func TestDecideLatencyUnderLoad(t *testing.T) {
	for _, n := range []int{8, 64} {
		deps, done := realDeps(n)
		sched, life, err := config.Load([]byte(config.DefaultConfig), deps)
		if err != nil {
			t.Fatal(err)
		}
		eps := deps.Store.Endpoints("p", nil)
		base := strings.Repeat("You are a helpful assistant that answers in detail. ", 40)
		workers, per := runtime.GOMAXPROCS(0), 200
		var mu sync.Mutex
		var total time.Duration
		var count int
		var wg sync.WaitGroup
		cpuStart := cpuTime(t)
		for w := range workers {
			wg.Go(func() {
				var local time.Duration
				for i := range per {
					req := &framework.Request{ID: fmt.Sprintf("r%d-%d", w, i), TargetModel: "m", Prompt: base + fmt.Sprintf("user %d question %d", w%8, i), PromptTokens: 600, ExpectedOutputTokens: 128, Objective: framework.Objective{TTFT: 500 * time.Millisecond}}
					start := time.Now()
					res, err := sched.Schedule(context.Background(), req, eps)
					if err != nil {
						t.Error(err)
						return
					}
					preRequest(life, req, res)
					local += time.Since(start)
					resp := &framework.Response{Endpoint: res.Endpoints()[0], Status: 200, StartedAt: start, FirstTokenAt: start.Add(80 * time.Millisecond), FinishedAt: start.Add(2 * time.Second), OutputTokens: 100}
					complete(life, req, resp)
				}
				mu.Lock()
				total += local
				count += per
				mu.Unlock()
			})
		}
		wg.Wait()
		cpu := cpuTime(t) - cpuStart
		done()
		if count == 0 {
			t.Fatal("no decision completed")
		}
		mean, cpuMean := total/time.Duration(count), cpu/time.Duration(count)
		t.Logf("endpoints=%d workers=%d decisions=%d wall=%s cpu=%s", n, workers, count, mean, cpuMean)
		if cpuMean > 10*time.Millisecond {
			t.Fatalf("endpoints=%d mean decision cost %s of CPU over ten milliseconds", n, cpuMean)
		}
	}
}
