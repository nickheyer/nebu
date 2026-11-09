package flowcontrol

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nickheyer/nebu/pkg/datalayer"
)

const testPool datalayer.PoolID = "p"

type harness struct {
	c     *FlowController
	store *fakeStore
	pred  *fakePredictor
	obs   *recorder
}

func baseConfig() Config {
	return Config{QueueCapacity: 4096, QueueTimeout: 10 * time.Second, SheddableBelow: -100, Tick: 2 * time.Millisecond}
}

func newHarness(t *testing.T, cfg Config, limit int) *harness {
	t.Helper()
	store := &fakeStore{eps: []*fakeEndpoint{endpoint("e1", true, nil)}}
	pred := newFakePredictor()
	det, err := newStaticDetector(limit, store, pred, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	obs := &recorder{}
	c, err := NewController(cfg, det, obs)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return &harness{c: c, store: store, pred: pred, obs: obs}
}

func newItem(id, fairness string, priority, cost int) *Item {
	return &Item{ID: id, Flow: FlowKey{FairnessID: fairness, Priority: priority}, Pool: testPool, Priority: priority, Cost: cost}
}

type result struct {
	item   *Item
	permit Permit
	err    error
}

func (h *harness) admitAsync(ctx context.Context, item *Item, out chan<- result) {
	go func() {
		pm, err := h.c.Admit(ctx, item)
		out <- result{item: item, permit: pm, err: err}
	}()
}

// Takes the only slot of a limit one pool so later admissions queue
func (h *harness) block(t *testing.T) Permit {
	t.Helper()
	pm, err := h.c.Admit(context.Background(), newItem("blocker", "blocker", 1000, 1))
	if err != nil {
		t.Fatalf("blocker: %v", err)
	}
	return pm
}

func (h *harness) waiting() int {
	n := 0
	for _, v := range h.c.Depth(testPool) {
		n += v
	}
	return n
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

func receive(t *testing.T, out <-chan result) result {
	t.Helper()
	select {
	case r := <-out:
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for an admission to resolve")
		return result{}
	}
}

func TestNewControllerRejectsInvalidConfig(t *testing.T) {
	det, err := newStaticDetector(1, &fakeStore{}, newFakePredictor(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		cfg  func(*Config)
		det  SaturationDetector
	}{
		{"zero capacity", func(c *Config) { c.QueueCapacity = 0 }, det},
		{"zero timeout", func(c *Config) { c.QueueTimeout = 0 }, det},
		{"zero tick", func(c *Config) { c.Tick = 0 }, det},
		{"nil detector", func(*Config) {}, nil},
		{"negative quota", func(c *Config) { c.Quota = Quota{Tokens: -1, Window: time.Hour} }, det},
		{"limited quota without a window", func(c *Config) { c.Quotas = map[string]Quota{"a": {Requests: 1}} }, det},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := baseConfig()
			tc.cfg(&cfg)
			if _, err := NewController(cfg, tc.det, nil); err == nil {
				t.Fatal("want an error")
			}
		})
	}
}

func TestAdmitDispatchesImmediatelyWhenIdle(t *testing.T) {
	cases := []struct {
		name string
		item *Item
		ttft time.Duration
	}{
		{"default band", newItem("a", "", 0, 10), 0},
		{"high band", newItem("b", "user-1", 9, 500), 0},
		{"sheddable band while not saturated", newItem("c", "user-2", -200, 10), 0},
		{"objective met", &Item{ID: "d", Pool: testPool, Priority: 1, Cost: 10, TTFT: 200 * time.Millisecond}, 50 * time.Millisecond},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, baseConfig(), 4)
			h.pred.setTTFT("e1", tc.ttft, 0)
			start := time.Now()
			pm, err := h.c.Admit(context.Background(), tc.item)
			if err != nil {
				t.Fatalf("admit: %v", err)
			}
			if el := time.Since(start); el > 100*time.Millisecond {
				t.Fatalf("admit took %s", el)
			}
			if pm.Waited() > 100*time.Millisecond {
				t.Fatalf("waited %s", pm.Waited())
			}
			if got := h.c.InFlight(testPool); got != 1 {
				t.Fatalf("in flight %d, want 1", got)
			}
			if got := h.obs.count(OutcomeDispatched); got != 1 {
				t.Fatalf("dispatched reports %d, want 1", got)
			}
			pm.Release()
			pm.Release()
			if got := h.c.InFlight(testPool); got != 0 {
				t.Fatalf("in flight after double release %d, want 0", got)
			}
		})
	}
}

func TestStrictPriorityUnderSaturation(t *testing.T) {
	cases := []struct {
		name  string
		bands []int
	}{
		{"three bands interleaved", []int{1, 3, 2, 3, 1, 2, 2, 1, 3}},
		{"negative and positive bands", []int{-5, 0, 7, -5, 7, 0, 3}},
		{"single band", []int{4, 4, 4, 4}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, baseConfig(), 1)
			blocker := h.block(t)
			out := make(chan result, len(tc.bands))
			for i, b := range tc.bands {
				h.admitAsync(context.Background(), newItem(fmt.Sprint(i), fmt.Sprint("f", i%2), b, 10), out)
			}
			waitFor(t, "all items queued", func() bool { return h.waiting() == len(tc.bands) })
			blocker.Release()
			var got []int
			for range tc.bands {
				r := receive(t, out)
				if r.err != nil {
					t.Fatalf("admit: %v", r.err)
				}
				got = append(got, r.item.Priority)
				r.permit.Release()
			}
			want := slices.Clone(tc.bands)
			slices.Sort(want)
			slices.Reverse(want)
			if !slices.Equal(got, want) {
				t.Fatalf("dispatch order %v, want %v", got, want)
			}
		})
	}
}

func TestFairShareBetweenUnequalCostFlows(t *testing.T) {
	cases := []struct {
		name       string
		cheap, big int
	}{
		{"ten against one hundred", 10, 100},
		{"one against sixty four", 1, 64},
		{"odd costs", 37, 251},
	}
	const dispatched = 1000
	const queued = 1200
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, baseConfig(), 1)
			blocker := h.block(t)
			out := make(chan result, 2*queued)
			for i := range queued {
				h.admitAsync(context.Background(), newItem(fmt.Sprint("cheap", i), "cheap", 1, tc.cheap), out)
				h.admitAsync(context.Background(), newItem(fmt.Sprint("big", i), "big", 1, tc.big), out)
			}
			waitFor(t, "both flows queued", func() bool { return h.waiting() == 2*queued })
			blocker.Release()
			tokens := map[string]int{}
			var last Permit
			for i := range dispatched {
				r := receive(t, out)
				if r.err != nil {
					t.Fatalf("admit: %v", r.err)
				}
				tokens[r.item.Flow.FairnessID] += r.item.Cost
				if i < dispatched-1 {
					r.permit.Release()
				}
				last = r.permit
			}
			h.c.Close()
			last.Release()
			for range 2*queued - dispatched {
				if r := receive(t, out); !errors.Is(r.err, context.Canceled) {
					t.Fatalf("leftover admission ended with %v, want context canceled", r.err)
				}
			}
			share := float64(tokens["cheap"]) / float64(tokens["cheap"]+tokens["big"])
			if math.Abs(share-0.5) > 0.05 {
				t.Fatalf("cheap flow token share %.3f (cheap %d, big %d), want within 10%% of an equal share", share, tokens["cheap"], tokens["big"])
			}
		})
	}
}

func TestShedBelowThresholdUnderSaturation(t *testing.T) {
	cases := []struct {
		name     string
		priority int
		shed     bool
	}{
		{"below threshold", 0, true},
		{"at threshold", 1, true},
		{"far below threshold", -50, true},
		{"above threshold", 2, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := baseConfig()
			cfg.SheddableBelow = 1
			h := newHarness(t, cfg, 1)
			h.block(t)
			if tc.shed {
				start := time.Now()
				_, err := h.c.Admit(context.Background(), newItem("x", "f", tc.priority, 10))
				if !errors.Is(err, ErrShed) {
					t.Fatalf("got %v, want ErrShed", err)
				}
				if el := time.Since(start); el > 100*time.Millisecond {
					t.Fatalf("shedding took %s", el)
				}
				if h.waiting() != 0 || h.obs.count(OutcomeShed) != 1 {
					t.Fatalf("shed item left depth %v and %d shed reports", h.c.Depth(testPool), h.obs.count(OutcomeShed))
				}
				return
			}
			out := make(chan result, 1)
			h.admitAsync(context.Background(), newItem("x", "f", tc.priority, 10), out)
			waitFor(t, "item queued", func() bool { return h.c.Depth(testPool)[tc.priority] == 1 })
			h.c.Close()
			if r := receive(t, out); !errors.Is(r.err, context.Canceled) {
				t.Fatalf("queued item ended with %v, want context canceled", r.err)
			}
		})
	}
}

func TestQueueCapacityDisplacesLowestBand(t *testing.T) {
	cases := []struct {
		name     string
		capacity int
		queued   []int
		arrive   int
		// Index of the queued item shed to make room, -1 when the arrival is refused instead
		displaced int
	}{
		{"higher band sheds the newest item of the lowest band", 3, []int{1, 0, 0}, 2, 2},
		{"equal band is refused", 2, []int{1, 1}, 1, -1},
		{"arrival below every band is refused", 2, []int{3, 2}, 1, -1},
		{"arrival between bands sheds from the lowest only", 3, []int{5, 2, 2}, 3, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := baseConfig()
			cfg.QueueCapacity = tc.capacity
			h := newHarness(t, cfg, 1)
			h.block(t)
			out := make(chan result, len(tc.queued)+1)
			for i, prio := range tc.queued {
				h.admitAsync(context.Background(), newItem(fmt.Sprint(i), fmt.Sprint("f", i%2), prio, 10), out)
				waitFor(t, "item queued", func() bool { return h.waiting() == i+1 })
			}
			if tc.displaced < 0 {
				if _, err := h.c.Admit(context.Background(), newItem("probe", "g", tc.arrive, 10)); !errors.Is(err, ErrQueueFull) {
					t.Fatalf("got %v, want ErrQueueFull", err)
				}
				if h.obs.count(OutcomeQueueFull) != 1 || h.waiting() != tc.capacity {
					t.Fatalf("queue full reports %d, depth %v", h.obs.count(OutcomeQueueFull), h.c.Depth(testPool))
				}
			} else {
				h.admitAsync(context.Background(), newItem("probe", "g", tc.arrive, 10), out)
				r := receive(t, out)
				if !errors.Is(r.err, ErrShed) || r.item.ID != fmt.Sprint(tc.displaced) {
					t.Fatalf("displaced item %s ended with %v, want %d shed", r.item.ID, r.err, tc.displaced)
				}
				waitFor(t, "arrival queued in place of the shed item", func() bool { return h.c.Depth(testPool)[tc.arrive] >= 1 && h.waiting() == tc.capacity })
				if h.obs.count(OutcomeShed) != 1 {
					t.Fatal("displacement not reported as shed")
				}
			}
			h.c.Close()
			for range tc.capacity {
				if r := receive(t, out); !errors.Is(r.err, context.Canceled) {
					t.Fatalf("waiter ended with %v, want context canceled", r.err)
				}
			}
		})
	}
}

// The highest band is never refused while a lower band holds items, and refused only once every item is its own
func TestHighestBandNeverRefusedWhileLowerBandHoldsItems(t *testing.T) {
	cfg := baseConfig()
	cfg.QueueCapacity = 2
	h := newHarness(t, cfg, 1)
	h.block(t)
	low := make(chan result, 2)
	for i := range 2 {
		h.admitAsync(context.Background(), newItem(fmt.Sprint("low", i), "f", 0, 10), low)
		waitFor(t, "low item queued", func() bool { return h.waiting() == i+1 })
	}
	high := make(chan result, 2)
	for i := range 2 {
		h.admitAsync(context.Background(), newItem(fmt.Sprint("high", i), "g", 9, 10), high)
		if r := receive(t, low); !errors.Is(r.err, ErrShed) {
			t.Fatalf("low item ended with %v, want ErrShed", r.err)
		}
		waitFor(t, "high item queued", func() bool { return h.c.Depth(testPool)[9] == i+1 })
	}
	if _, err := h.c.Admit(context.Background(), newItem("high2", "g", 9, 10)); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("third high item got %v, want ErrQueueFull once no lower band holds anything", err)
	}
	if d := h.c.Depth(testPool); d[0] != 0 || d[9] != 2 {
		t.Fatalf("depth %v", d)
	}
	h.c.Close()
	for range 2 {
		if r := receive(t, high); !errors.Is(r.err, context.Canceled) {
			t.Fatalf("high waiter ended with %v", r.err)
		}
	}
}

func TestQueueTimeout(t *testing.T) {
	cases := []struct {
		name    string
		timeout time.Duration
	}{
		{"short", 30 * time.Millisecond},
		{"longer", 80 * time.Millisecond},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := baseConfig()
			cfg.QueueTimeout = tc.timeout
			h := newHarness(t, cfg, 1)
			h.block(t)
			start := time.Now()
			_, err := h.c.Admit(context.Background(), newItem("x", "f", 1, 10))
			if !errors.Is(err, ErrQueueTimeout) {
				t.Fatalf("got %v, want ErrQueueTimeout", err)
			}
			if el := time.Since(start); el < tc.timeout {
				t.Fatalf("timed out after %s, before %s", el, tc.timeout)
			}
			if h.waiting() != 0 {
				t.Fatalf("timed out item still queued, depth %v", h.c.Depth(testPool))
			}
			ev, ok := h.obs.last(OutcomeTimeout)
			if !ok || ev.waited < tc.timeout || ev.band != 1 || ev.pool != testPool {
				t.Fatalf("timeout report %+v, present %v", ev, ok)
			}
		})
	}
}

func TestContextEndRemovesQueuedItem(t *testing.T) {
	cases := []struct {
		name string
		ctx  func() (context.Context, context.CancelFunc)
		want error
	}{
		{"canceled while queued", func() (context.Context, context.CancelFunc) { return context.WithCancel(context.Background()) }, context.Canceled},
		{"deadline while queued", func() (context.Context, context.CancelFunc) {
			return context.WithTimeout(context.Background(), 100*time.Millisecond)
		}, context.DeadlineExceeded},
		{"canceled before admit", func() (context.Context, context.CancelFunc) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			return ctx, cancel
		}, context.Canceled},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, baseConfig(), 1)
			blocker := h.block(t)
			ctx, cancel := tc.ctx()
			defer cancel()
			out := make(chan result, 1)
			h.admitAsync(ctx, newItem("x", "f", 1, 10), out)
			if ctx.Err() == nil {
				waitFor(t, "item queued", func() bool { return h.waiting() == 1 })
				if tc.want == context.Canceled {
					cancel()
				}
			}
			r := receive(t, out)
			if !errors.Is(r.err, tc.want) {
				t.Fatalf("got %v, want %v", r.err, tc.want)
			}
			if h.waiting() != 0 {
				t.Fatalf("item still queued, depth %v", h.c.Depth(testPool))
			}
			blocker.Release()
			waitFor(t, "no capacity handed to the removed item", func() bool { return h.c.InFlight(testPool) == 0 })
			if h.obs.count(OutcomeCanceled) != 1 {
				t.Fatal("cancellation not reported")
			}
		})
	}
}

func TestSoftSaturationHoldsObjectiveItems(t *testing.T) {
	cases := []struct {
		name    string
		item    *Item
		slow    func(*fakePredictor)
		fast    func(*fakePredictor)
		timeout time.Duration
		// Band of an item without objective that must pass the held one
		plainBand int
		want      error
	}{
		{
			name:      "TTFT held until predictions recover",
			plainBand: 1,
			item:      &Item{ID: "x", Flow: FlowKey{FairnessID: "f"}, Pool: testPool, Priority: 1, Cost: 10, TTFT: 100 * time.Millisecond},
			slow:      func(p *fakePredictor) { p.setTTFT("e1", 80*time.Millisecond, 30*time.Millisecond) },
			fast:      func(p *fakePredictor) { p.setTTFT("e1", 50*time.Millisecond, 10*time.Millisecond) },
		},
		{
			name:      "TPOT held until predictions recover while a lower band dispatches",
			plainBand: 0,
			item:      &Item{ID: "x", Flow: FlowKey{FairnessID: "f"}, Pool: testPool, Priority: 1, Cost: 10, TPOT: 20 * time.Millisecond},
			slow:      func(p *fakePredictor) { p.setTPOT("e1", 25*time.Millisecond, 0) },
			fast:      func(p *fakePredictor) { p.setTPOT("e1", 15*time.Millisecond, 2*time.Millisecond) },
		},
		{
			name:      "held until queue timeout",
			plainBand: 1,
			item:      &Item{ID: "x", Flow: FlowKey{FairnessID: "f"}, Pool: testPool, Priority: 1, Cost: 10, TTFT: 100 * time.Millisecond},
			slow:      func(p *fakePredictor) { p.setTTFT("e1", 200*time.Millisecond, 0) },
			timeout:   60 * time.Millisecond,
			want:      ErrQueueTimeout,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := baseConfig()
			if tc.timeout > 0 {
				cfg.QueueTimeout = tc.timeout
			}
			h := newHarness(t, cfg, 8)
			tc.slow(h.pred)
			out := make(chan result, 1)
			h.admitAsync(context.Background(), tc.item, out)
			waitFor(t, "objective item held", func() bool { return h.waiting() == 1 })

			plain, err := h.c.Admit(context.Background(), newItem("plain", "f", tc.plainBand, 10))
			if err != nil {
				t.Fatalf("item without objective: %v", err)
			}
			plain.Release()
			if h.waiting() != 1 {
				t.Fatalf("held item left the queue early, depth %v", h.c.Depth(testPool))
			}

			const hold = 30 * time.Millisecond
			if tc.fast != nil {
				time.Sleep(hold)
				tc.fast(h.pred)
			}
			r := receive(t, out)
			if !errors.Is(r.err, tc.want) {
				t.Fatalf("got %v, want %v", r.err, tc.want)
			}
			if tc.want == nil {
				if r.permit.Waited() < hold {
					t.Fatalf("dispatched after %s, before the hold ended", r.permit.Waited())
				}
				r.permit.Release()
			}
			if h.waiting() != 0 || h.c.InFlight(testPool) != 0 {
				t.Fatalf("depth %v in flight %d after resolution", h.c.Depth(testPool), h.c.InFlight(testPool))
			}
		})
	}
}

func TestCloseFailsWaiters(t *testing.T) {
	cases := []struct {
		name  string
		bands []int
	}{
		{"one band", []int{1, 1, 1}},
		{"several bands", []int{1, 5, -3, 9, 5}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, baseConfig(), 1)
			blocker := h.block(t)
			out := make(chan result, len(tc.bands))
			for i, b := range tc.bands {
				h.admitAsync(context.Background(), newItem(fmt.Sprint(i), fmt.Sprint("f", i), b, 10), out)
			}
			waitFor(t, "all queued", func() bool { return h.waiting() == len(tc.bands) })
			h.c.Close()
			for range tc.bands {
				if r := receive(t, out); !errors.Is(r.err, context.Canceled) {
					t.Fatalf("waiter ended with %v, want context canceled", r.err)
				}
			}
			if got := h.obs.count(OutcomeClosed); got != len(tc.bands) {
				t.Fatalf("closed reports %d, want %d", got, len(tc.bands))
			}
			if _, err := h.c.Admit(context.Background(), newItem("late", "f", 1, 1)); !errors.Is(err, context.Canceled) {
				t.Fatalf("admit after close got %v, want context canceled", err)
			}
			if h.waiting() != 0 {
				t.Fatalf("depth after close %v", h.c.Depth(testPool))
			}
			blocker.Release()
			if got := h.c.InFlight(testPool); got != 0 {
				t.Fatalf("in flight after release %d, want 0", got)
			}
		})
	}
}

func TestConcurrentAdmitAndRelease(t *testing.T) {
	cases := []struct {
		name        string
		limit       int
		cancelEvery int
	}{
		{"no cancellation", 4, 0},
		{"with cancellation", 3, 7},
	}
	const goroutines = 100
	const rounds = 50
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, baseConfig(), tc.limit)
			var current, peak, dispatched atomic.Int64
			var wg sync.WaitGroup
			errs := make(chan error, goroutines)
			for g := range goroutines {
				wg.Go(func() {
					rng := rand.New(rand.NewPCG(uint64(g), 1))
					for r := range rounds {
						ctx, cancel := context.Background(), context.CancelFunc(func() {})
						if tc.cancelEvery > 0 && (g*rounds+r)%tc.cancelEvery == 0 {
							ctx, cancel = context.WithTimeout(ctx, time.Duration(rng.IntN(500))*time.Microsecond)
						}
						it := newItem(fmt.Sprint(g, "-", r), fmt.Sprint("f", rng.IntN(5)), 1+rng.IntN(3), 1+rng.IntN(200))
						pm, err := h.c.Admit(ctx, it)
						cancel()
						if err != nil {
							if tc.cancelEvery > 0 && (errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)) {
								continue
							}
							errs <- err
							return
						}
						dispatched.Add(1)
						now := current.Add(1)
						for p := peak.Load(); now > p && !peak.CompareAndSwap(p, now); p = peak.Load() {
						}
						if rng.IntN(4) == 0 {
							time.Sleep(time.Duration(rng.IntN(200)) * time.Microsecond)
						}
						current.Add(-1)
						pm.Release()
					}
				})
			}
			wg.Wait()
			close(errs)
			for err := range errs {
				t.Fatalf("admit: %v", err)
			}
			if p := peak.Load(); p > int64(tc.limit) {
				t.Fatalf("peak in flight %d above limit %d", p, tc.limit)
			}
			if got := h.c.InFlight(testPool); got != 0 {
				t.Fatalf("in flight after all releases %d", got)
			}
			if h.waiting() != 0 {
				t.Fatalf("depth after all admissions %v", h.c.Depth(testPool))
			}
			if got := int64(h.obs.count(OutcomeDispatched)); got != dispatched.Load() {
				t.Fatalf("dispatched reports %d, want %d", got, dispatched.Load())
			}
			if tc.cancelEvery == 0 && dispatched.Load() != goroutines*rounds {
				t.Fatalf("dispatched %d, want %d", dispatched.Load(), goroutines*rounds)
			}
		})
	}
}

func TestMetricsRefreshDispatchesOnlyHeadroom(t *testing.T) {
	cases := []struct {
		name   string
		queued int
		want   int
	}{
		{"room for two", 3, 2},
		{"room for four", 1, 4},
	}
	const waiting = 6
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeStore{eps: []*fakeEndpoint{endpoint("a", true, map[string]any{datalayer.CollectorMetrics: fresh(5, 0.1)})}}
			det, err := NewMetricsDetector(store, newFakePredictor(), testStaleness, testThresholds)
			if err != nil {
				t.Fatal(err)
			}
			c, err := NewController(baseConfig(), det, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			out := make(chan result, waiting)
			h := &harness{c: c}
			for i := range waiting {
				h.admitAsync(context.Background(), newItem(fmt.Sprint(i), "f", 1, 10), out)
			}
			waitFor(t, "all queued while saturated", func() bool { return h.waiting() == waiting })
			store.set("a", datalayer.CollectorMetrics, fresh(tc.queued, 0.1))
			var permits []Permit
			for range tc.want {
				permits = append(permits, receive(t, out).permit)
			}
			time.Sleep(20 * baseConfig().Tick)
			if got := h.waiting(); got != waiting-tc.want {
				t.Fatalf("waiting %d after refresh, want %d", got, waiting-tc.want)
			}
			permits[0].Release()
			receive(t, out)
			if got := h.waiting(); got != waiting-tc.want-1 {
				t.Fatalf("waiting %d after one release, want %d", got, waiting-tc.want-1)
			}
		})
	}
}

// A route cap waits in flow control across pools and frees in order, a sheddable band shed at once
func TestRouteLimitQueuesAcrossPools(t *testing.T) {
	const other datalayer.PoolID = "q"
	routed := func(id, fairness string, prio int, pool datalayer.PoolID) *Item {
		it := newItem(id, fairness, prio, 10)
		it.Pool, it.Route, it.RouteLimit = pool, "r", 1
		return it
	}
	h := newHarness(t, baseConfig(), 8)
	first, err := h.c.Admit(context.Background(), routed("a", "f", 1, testPool))
	if err != nil {
		t.Fatalf("first routed item: %v", err)
	}
	out := make(chan result, 1)
	h.admitAsync(context.Background(), routed("b", "f", 1, other), out)
	waitFor(t, "second routed item queued in the other pool", func() bool { return h.c.Depth(other)[1] == 1 })
	if pm, err := h.c.Admit(context.Background(), newItem("free", "f", 1, 10)); err != nil {
		t.Fatalf("item of an uncapped route: %v", err)
	} else {
		pm.Release()
	}
	if _, err := h.c.Admit(context.Background(), routed("shed", "f", -200, other)); !errors.Is(err, ErrShed) {
		t.Fatalf("sheddable item at the route cap got %v, want ErrShed", err)
	}
	if h.c.Depth(other)[1] != 1 {
		t.Fatalf("depth %v after refusals", h.c.Depth(other))
	}
	first.Release()
	r := receive(t, out)
	if r.err != nil {
		t.Fatalf("second routed item: %v", r.err)
	}
	if r.permit.Waited() <= 0 || h.c.InFlight(other) != 1 || h.c.InFlight(testPool) != 0 {
		t.Fatalf("waited %s, in flight %d and %d", r.permit.Waited(), h.c.InFlight(other), h.c.InFlight(testPool))
	}
	r.permit.Release()
	if pm, err := h.c.Admit(context.Background(), routed("c", "f", 1, testPool)); err != nil || pm.Waited() > 50*time.Millisecond {
		t.Fatalf("routed item after release err %v waited %v", err, pm)
	}
}

// Settled tokens charge the flow for its overrun so the other flow goes first on the next round
func TestSettleChargesActualTokens(t *testing.T) {
	cases := []struct {
		name string
		// Tokens flow a settles its first dispatch with, zero settling nothing
		prompt, output int
		// Whether a and b queue before the settle lands on the live flow
		live bool
		want string
	}{
		{"overrun on a live flow", 5000, 5000, true, "b"},
		{"overrun kept as debt after the flow left", 5000, 5000, false, "b"},
		{"no settle keeps the estimate", 0, 0, true, "a"},
		{"underrun refunds the flow", 1, 1, true, "a"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, baseConfig(), 1)
			blocker := h.block(t)
			out := make(chan result, 3)
			h.admitAsync(context.Background(), newItem("a1", "a", 1, 10), out)
			waitFor(t, "a1 queued", func() bool { return h.waiting() == 1 })
			blocker.Release()
			a1 := receive(t, out)
			if a1.err != nil {
				t.Fatal(a1.err)
			}
			queue := func() {
				h.admitAsync(context.Background(), newItem("a2", "a", 1, 10), out)
				waitFor(t, "a2 queued", func() bool { return h.waiting() == 1 })
				h.admitAsync(context.Background(), newItem("b1", "b", 1, 10), out)
				waitFor(t, "b1 queued", func() bool { return h.waiting() == 2 })
			}
			if tc.live {
				queue()
			}
			if tc.prompt+tc.output > 0 {
				a1.permit.Settle(tc.prompt, tc.output)
			}
			if !tc.live {
				queue()
			}
			a1.permit.Release()
			next := receive(t, out)
			if next.err != nil || next.item.Flow.FairnessID != tc.want {
				t.Fatalf("next dispatched %s with %v, want flow %s", next.item.ID, next.err, tc.want)
			}
			next.permit.Release()
			last := receive(t, out)
			if last.err != nil {
				t.Fatal(last.err)
			}
			last.permit.Release()
		})
	}
}

// A fairness id over its request or token quota is refused with the wait until its window resets
func TestQuotaRefusesOverUsedFairnessID(t *testing.T) {
	cfg := baseConfig()
	cfg.Quota = Quota{Requests: 2, Window: time.Hour}
	cfg.Quotas = map[string]Quota{"vip": {Tokens: 100, Window: time.Hour}}
	h := newHarness(t, cfg, 8)
	for i := range 2 {
		pm, err := h.c.Admit(context.Background(), newItem(fmt.Sprint(i), "f", 1, 10))
		if err != nil {
			t.Fatalf("admit %d: %v", i, err)
		}
		pm.Settle(3, 4)
		pm.Release()
	}
	_, err := h.c.Admit(context.Background(), newItem("over", "f", 1, 10))
	qe, ok := errors.AsType[*QuotaError](err)
	if !ok || !errors.Is(err, ErrQuotaExceeded) || qe.FairnessID != "f" || qe.Retry <= 0 || qe.Retry > time.Hour {
		t.Fatalf("third request got %v", err)
	}
	if got := h.c.Usage("f"); got != (Usage{Requests: 2, PromptTokens: 6, OutputTokens: 8}) {
		t.Fatalf("usage %+v", got)
	}
	if pm, err := h.c.Admit(context.Background(), newItem("g", "g", 1, 10)); err != nil {
		t.Fatalf("another id under the default quota: %v", err)
	} else {
		pm.Release()
	}
	vip, err := h.c.Admit(context.Background(), newItem("v1", "vip", 1, 10))
	if err != nil {
		t.Fatal(err)
	}
	vip.Settle(60, 50)
	vip.Release()
	if _, err := h.c.Admit(context.Background(), newItem("v2", "vip", 1, 10)); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("vip over its token quota got %v", err)
	}
	if got := h.obs.count(OutcomeQuota); got != 2 {
		t.Fatalf("quota reports %d, want 2", got)
	}
	bad := baseConfig()
	bad.Quota = Quota{Tokens: 10}
	if _, err := NewController(bad, h.c.detector, nil); err == nil {
		t.Fatal("a limited quota without a window must be refused")
	}
}

// A requeued item gives its capacity back, lets the next waiter go, and keeps its original queue timeout
func TestRequeueReturnsItemToQueue(t *testing.T) {
	t.Run("dispatches again on the next pass", func(t *testing.T) {
		h := newHarness(t, baseConfig(), 2)
		x := newItem("x", "f", 1, 10)
		pm, err := h.c.Admit(context.Background(), x)
		if err != nil {
			t.Fatal(err)
		}
		again, err := h.c.Requeue(context.Background(), pm, x)
		if err != nil {
			t.Fatalf("requeue: %v", err)
		}
		pm.Release()
		if h.c.InFlight(testPool) != 1 || h.waiting() != 0 {
			t.Fatalf("in flight %d waiting %d after requeue", h.c.InFlight(testPool), h.waiting())
		}
		if _, err := h.c.Requeue(context.Background(), pm, x); err == nil {
			t.Fatal("a consumed permit must not requeue")
		}
		again.Release()
		if h.c.InFlight(testPool) != 0 || h.obs.count(OutcomeDispatched) != 2 {
			t.Fatalf("in flight %d dispatched reports %d", h.c.InFlight(testPool), h.obs.count(OutcomeDispatched))
		}
	})
	t.Run("waiter of another flow goes first", func(t *testing.T) {
		h := newHarness(t, baseConfig(), 1)
		x := newItem("x", "f", 1, 10)
		pm, err := h.c.Admit(context.Background(), x)
		if err != nil {
			t.Fatal(err)
		}
		out := make(chan result, 2)
		h.admitAsync(context.Background(), newItem("y", "g", 1, 10), out)
		waitFor(t, "y queued", func() bool { return h.waiting() == 1 })
		go func() {
			again, err := h.c.Requeue(context.Background(), pm, x)
			out <- result{item: x, permit: again, err: err}
		}()
		y := receive(t, out)
		if y.err != nil || y.item.ID != "y" {
			t.Fatalf("first dispatched %s with %v, want y", y.item.ID, y.err)
		}
		y.permit.Release()
		back := receive(t, out)
		if back.err != nil || back.item.ID != "x" || back.permit.Waited() <= 0 {
			t.Fatalf("requeued item %s ended with %v after %s", back.item.ID, back.err, back.permit.Waited())
		}
		back.permit.Release()
	})
	t.Run("timeout counts from the first admission", func(t *testing.T) {
		cfg := baseConfig()
		cfg.QueueTimeout = 80 * time.Millisecond
		h := newHarness(t, cfg, 1)
		x := newItem("x", "f", 1, 10)
		pm, err := h.c.Admit(context.Background(), x)
		if err != nil {
			t.Fatal(err)
		}
		out := make(chan result, 1)
		h.admitAsync(context.Background(), newItem("y", "g", 1, 10), out)
		waitFor(t, "y queued", func() bool { return h.waiting() == 1 })
		time.Sleep(50 * time.Millisecond)
		start := time.Now()
		_, err = h.c.Requeue(context.Background(), pm, x)
		if !errors.Is(err, ErrQueueTimeout) {
			t.Fatalf("requeue got %v, want ErrQueueTimeout", err)
		}
		if el := time.Since(start); el >= cfg.QueueTimeout {
			t.Fatalf("timed out %s after the requeue, the timeout must count from the first admission", el)
		}
		y := receive(t, out)
		if y.err != nil {
			t.Fatal(y.err)
		}
		y.permit.Release()
	})
}

// A pool whose endpoints report neither metrics nor capacity dispatches every request at once
func TestOpenPoolDispatchesWithoutMetricsOrCapacity(t *testing.T) {
	store := &fakeStore{eps: []*fakeEndpoint{endpoint("static", true, nil)}}
	det, err := NewMetricsDetector(store, newFakePredictor(), testStaleness, testThresholds)
	if err != nil {
		t.Fatal(err)
	}
	c, err := NewController(baseConfig(), det, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	for i := range 50 {
		pm, err := c.Admit(context.Background(), newItem(fmt.Sprint(i), "f", 1, 10))
		if err != nil {
			t.Fatalf("admit %d: %v", i, err)
		}
		if pm.Waited() > 20*time.Millisecond {
			t.Fatalf("admit %d waited %s", i, pm.Waited())
		}
		defer pm.Release()
	}
	if got := c.InFlight(testPool); got != 50 {
		t.Fatalf("in flight %d, want 50", got)
	}
}
