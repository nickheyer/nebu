package slots

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nickheyer/nebu/internal/instances"
	"github.com/nickheyer/nebu/internal/instances/fake"
	"github.com/nickheyer/nebu/internal/pools"
	"github.com/nickheyer/nebu/pkg/datalayer"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/proto"
)

// A clock that moves only when a test advances it
type fakeClock struct {
	mu      sync.Mutex
	now     time.Duration
	waiters []clockWaiter
}

type clockWaiter struct {
	at time.Duration
	ch chan time.Time
}

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan time.Time, 1)
	c.waiters = append(c.waiters, clockWaiter{at: c.now + d, ch: ch})
	return ch
}

// Moves the clock forward and fires every wait that came due
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now += d
	keep := c.waiters[:0]
	for _, w := range c.waiters {
		if w.at <= c.now {
			w.ch <- time.Unix(0, 0).Add(c.now)
			continue
		}
		keep = append(keep, w)
	}
	c.waiters = keep
}

// Waits not yet due
func (c *fakeClock) Waiting() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.waiters)
}

type outcomeKey struct {
	pool  datalayer.PoolID
	model string
}

// Request counters a test adds to by pool and target model
type fakeOutcomes struct {
	mu sync.Mutex
	by map[outcomeKey]pools.Outcome
}

func (f *fakeOutcomes) Outcome(pool datalayer.PoolID, model string) pools.Outcome {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.by[outcomeKey{pool, model}]
}

// Adds counts to a target's counters
func (f *fakeOutcomes) add(t pools.WeightedTarget, o pools.Outcome) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.by == nil {
		f.by = map[outcomeKey]pools.Outcome{}
	}
	k := outcomeKey{t.Pool, t.Model}
	cur := f.by[k]
	f.by[k] = pools.Outcome{
		Requests: cur.Requests + o.Requests, Errors: cur.Errors + o.Errors, Judged: cur.Judged + o.Judged, Met: cur.Met + o.Met,
		TTFT: cur.TTFT + o.TTFT, TTFTs: cur.TTFTs + o.TTFTs, TPOT: cur.TPOT + o.TPOT, TPOTs: cur.TPOTs + o.TPOTs,
	}
}

// Counters of n requests with errs failing, judged of them carrying an objective and met meeting it, at mean latencies
func traffic(n, errs, judged, met uint64, ttft, tpot time.Duration) pools.Outcome {
	ok := n - errs
	return pools.Outcome{Requests: n, Errors: errs, Judged: judged, Met: met, TTFT: ttft * time.Duration(ok), TTFTs: ok, TPOT: tpot * time.Duration(ok), TPOTs: ok}
}

// Healthy traffic at the latencies every case compares against
func steady(n uint64) pools.Outcome {
	return traffic(n, 0, n, n, 100*time.Millisecond, 20*time.Millisecond)
}

// Every measure of the canary is compared with the occupant's within the bound, and missing evidence on either side regresses
func TestRegressed(t *testing.T) {
	ms := time.Millisecond
	cases := []struct {
		name     string
		occ, can pools.Outcome
		slack    uint32
		want     string
	}{
		{"equal", steady(100), steady(10), 10, ""},
		{"error rate inside the bound", traffic(100, 0, 0, 0, 100*ms, 20*ms), traffic(20, 1, 0, 0, 100*ms, 20*ms), 10, ""},
		{"error rate past the bound", traffic(100, 0, 0, 0, 100*ms, 20*ms), traffic(10, 2, 0, 0, 100*ms, 20*ms), 10, "error rate 20.0%"},
		{"attainment inside the bound", traffic(100, 0, 100, 95, 100*ms, 20*ms), traffic(10, 0, 10, 9, 100*ms, 20*ms), 10, ""},
		{"attainment past the bound", traffic(100, 0, 100, 95, 100*ms, 20*ms), traffic(10, 0, 10, 5, 100*ms, 20*ms), 10, "met 50.0% of objectives"},
		{"ttft inside the bound", steady(100), traffic(10, 0, 10, 10, 108*ms, 20*ms), 10, ""},
		{"ttft past the bound", steady(100), traffic(10, 0, 10, 10, 200*ms, 20*ms), 10, "mean time to first token 200ms"},
		{"tpot past the bound", steady(100), traffic(10, 0, 10, 10, 100*ms, 30*ms), 10, "mean time per output token 30ms"},
		{"strict bound", steady(100), traffic(10, 0, 10, 10, 101*ms, 20*ms), 0, "time to first token"},
		{"wide bound", steady(100), traffic(10, 0, 10, 10, 140*ms, 20*ms), 50, ""},
		{"faster canary", steady(100), traffic(10, 0, 10, 10, 50*ms, 10*ms), 0, ""},
		{"silent canary", steady(100), pools.Outcome{}, 10, "the canary served no requests"},
		{"silent occupant", pools.Outcome{}, steady(10), 10, "the occupant served no requests"},
		{"no objectives on the canary", steady(100), traffic(10, 0, 0, 0, 100*ms, 20*ms), 10, "the canary served no requests carrying an objective while the occupant served 100"},
		{"no objectives on the occupant", traffic(100, 0, 0, 0, 100*ms, 20*ms), steady(10), 10, "the occupant served no requests carrying an objective"},
		{"no objectives anywhere", traffic(100, 0, 0, 0, 100*ms, 20*ms), traffic(10, 0, 0, 0, 100*ms, 20*ms), 10, ""},
		{"no tokens timed on the canary", steady(100), pools.Outcome{Requests: 10, Judged: 10, Met: 10}, 10, "the canary served no time to first token samples"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := regressed(c.occ, c.can, c.slack)
			if c.want == "" && got != "" || c.want != "" && !strings.Contains(got, c.want) {
				t.Fatalf("regressed %q, want %q", got, c.want)
			}
		})
	}
}

// A canary must answer under a pool and served name of its own for the split to tell it apart
func TestBlended(t *testing.T) {
	cases := []struct {
		name     string
		was, now pools.WeightedTarget
		want     string
	}{
		{"same pool other name", pools.WeightedTarget{Pool: "p", Model: "main"}, pools.WeightedTarget{Pool: "p", Model: "main~3"}, ""},
		{"other pool same name", pools.WeightedTarget{Pool: "p", Model: "main"}, pools.WeightedTarget{Pool: "q", Model: "main"}, ""},
		{"same pool same name", pools.WeightedTarget{Pool: "p", Model: "main"}, pools.WeightedTarget{Pool: "p", Model: "main"}, "serves as main in the occupant's pool p"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := blended(c.was, c.now); c.want == "" && got != "" || !strings.Contains(got, c.want) {
				t.Fatalf("blended %q, want %q", got, c.want)
			}
		})
	}
}

// A swap's canary options turn into shares per step and a bound, options without a share refused
func TestCanaryPlan(t *testing.T) {
	cases := []struct {
		name   string
		req    *v1.SwapRequest
		canary bool
		shares []uint32
		slack  uint32
		bad    bool
	}{
		{"no canary", &v1.SwapRequest{}, false, nil, 0, false},
		{"growing steps", &v1.SwapRequest{CanaryPercent: 10, CanaryStepPercent: 30, CanarySeconds: 60}, true, []uint32{10, 40, 70}, 10, false},
		{"one step", &v1.SwapRequest{CanaryPercent: 25, CanarySeconds: 60}, true, []uint32{25}, 10, false},
		{"steps up to 99", &v1.SwapRequest{CanaryPercent: 1, CanaryStepPercent: 49, CanarySeconds: 1}, true, []uint32{1, 50, 99}, 10, false},
		{"a last step past 99 completes", &v1.SwapRequest{CanaryPercent: 99, CanaryStepPercent: 5, CanarySeconds: 1}, true, []uint32{99}, 10, false},
		{"strict bound", &v1.SwapRequest{CanaryPercent: 10, CanarySeconds: 1, CanaryMaxRegressionPercent: proto.Uint32(0)}, true, []uint32{10}, 0, false},
		{"window without a share", &v1.SwapRequest{CanarySeconds: 60}, false, nil, 0, true},
		{"step without a share", &v1.SwapRequest{CanaryStepPercent: 10}, false, nil, 0, true},
		{"bound without a share", &v1.SwapRequest{CanaryMaxRegressionPercent: proto.Uint32(5)}, false, nil, 0, true},
		{"share without a window", &v1.SwapRequest{CanaryPercent: 10}, false, nil, 0, true},
		{"whole share", &v1.SwapRequest{CanaryPercent: 100, CanarySeconds: 1}, false, nil, 0, true},
		{"whole step", &v1.SwapRequest{CanaryPercent: 10, CanaryStepPercent: 100, CanarySeconds: 1}, false, nil, 0, true},
		{"bound past 100", &v1.SwapRequest{CanaryPercent: 10, CanarySeconds: 1, CanaryMaxRegressionPercent: proto.Uint32(101)}, false, nil, 0, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p, canary, err := canaryOf(c.req)
			if c.bad {
				if !errors.Is(err, ErrSlot) {
					t.Fatalf("accepted %v", c.req)
				}
				return
			}
			if err != nil || canary != c.canary {
				t.Fatalf("canary %v %v", canary, err)
			}
			if canary && (!slices.Equal(p.shares(), c.shares) || p.slack != c.slack) {
				t.Fatalf("shares %v slack %d, want %v %d", p.shares(), p.slack, c.shares, c.slack)
			}
		})
	}
}

// Fails unless the slot's condition of the type has the status and reason and its message holds every part
func wantCondition(t *testing.T, s *v1.Slot, typ string, status bool, reason string, parts ...string) {
	t.Helper()
	c := instances.ConditionOf(s.GetConditions(), typ)
	if c == nil || instances.ConditionTrue(s.GetConditions(), typ) != status || c.GetReason() != reason {
		t.Fatalf("condition %s is %v, want %v %s", typ, c, status, reason)
	}
	for _, p := range parts {
		if !strings.Contains(c.GetMessage(), p) {
			t.Fatalf("condition %s message %q lacks %q", typ, c.GetMessage(), p)
		}
	}
}

// Waits for every slot name to send share percent to the canary with its step pending, returning both targets
func (n *slotNode) atStep(t *testing.T, id string, share uint32) (occ, can pools.WeightedTarget) {
	t.Helper()
	waitFor(t, fmt.Sprintf("a %d%% split", share), func() bool {
		for _, name := range []string{"main", "chat"} {
			rt, ok := n.registrar.Named(name)
			if !ok || !rt.Split || len(rt.Targets) != 2 || rt.Targets[1].Weight != share || rt.Targets[0].Weight != 100-share {
				return false
			}
		}
		c := instances.ConditionOf(n.slot(t, id).GetConditions(), CondCanary)
		return n.clock.Waiting() == 1 && c.GetReason() == CanaryShifting && strings.Contains(c.GetMessage(), fmt.Sprintf(" %d%% ", share))
	})
	s := n.slot(t, id)
	wantCondition(t, s, CondCanary, true, CanaryShifting, fmt.Sprintf("sends %d%% of traffic", share))
	wantCondition(t, s, CondSwapping, true, ModeCanary)
	wantCondition(t, s, CondReady, true, "OldOccupantServing")
	rt, _ := n.registrar.Named("main")
	return rt.Targets[0], rt.Targets[1]
}

// Moves the clock one step, proving the step holds until its full length passes
func (n *slotNode) pass(t *testing.T, every time.Duration) {
	t.Helper()
	n.clock.Advance(every - time.Millisecond)
	time.Sleep(3 * canaryPoll)
	if n.clock.Waiting() != 1 {
		t.Fatalf("the step ended %s early", time.Millisecond)
	}
	n.clock.Advance(time.Millisecond)
}

// Starts a canary swap of the filled slot, to q8 unless the request names a run, returning its task
func (n *slotNode) startCanary(t *testing.T, ctx context.Context, s *v1.Slot, req *v1.SwapRequest) *v1.Task {
	t.Helper()
	req.SlotId = s.GetId()
	if req.Run == nil {
		req.Run = slotRun(s.GetId(), "q8")
	}
	_, _, task, err := n.slots.Swap(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	return task
}

// The instance in the slot other than the occupant
func (n *slotNode) canaryOf(t *testing.T, s *v1.Slot, old *v1.Instance) *v1.Instance {
	t.Helper()
	for _, in := range n.instances.InSlot(s.GetId()) {
		if in.GetId() != old.GetId() {
			return in
		}
	}
	t.Fatal("no canary beside the occupant")
	return nil
}

// Records each change of the reason the slot's Canary condition carries, read from the slot's events
func (n *slotNode) phases(ctx context.Context, id string) func() []string {
	sub := n.slots.Events.Subscribe(ctx, []v1.EventKind{v1.EventKind_EVENT_KIND_SLOT})
	var mu sync.Mutex
	var seen []string
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case ev := <-sub.Events():
				c := instances.ConditionOf(ev.GetSlot().GetConditions(), CondCanary)
				if ev.GetId() != id || c == nil {
					continue
				}
				mu.Lock()
				if len(seen) == 0 || seen[len(seen)-1] != c.GetReason() {
					seen = append(seen, c.GetReason())
				}
				mu.Unlock()
			}
		}
	}()
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(seen)
	}
}

// Fails unless the recorded phases come to want within thirty seconds
func wantPhases(t *testing.T, seen func() []string, want ...string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for !slices.Equal(seen(), want) {
		if time.Now().After(deadline) {
			t.Fatalf("canary phases %v, want %v", seen(), want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func fastCanaryPoll(t *testing.T) {
	poll := canaryPoll
	canaryPoll = 20 * time.Millisecond
	t.Cleanup(func() { canaryPoll = poll })
}

// A canary holding up through every scheduled step takes all traffic and the old occupant drains
func TestCanaryShiftsInStepsAndPromotes(t *testing.T) {
	fastCanaryPoll(t)
	ms := time.Millisecond
	cases := []struct {
		name   string
		req    *v1.SwapRequest
		shares []uint32
		can    pools.Outcome
	}{
		{"three steps", &v1.SwapRequest{CanaryPercent: 10, CanaryStepPercent: 30, CanarySeconds: 60}, []uint32{10, 40, 70}, steady(10)},
		{"one step", &v1.SwapRequest{CanaryPercent: 25, CanarySeconds: 30}, []uint32{25}, steady(10)},
		{"slower inside the default bound", &v1.SwapRequest{CanaryPercent: 50, CanarySeconds: 30}, []uint32{50}, traffic(20, 1, 20, 19, 108*ms, 21*ms)},
		{"slower inside a wide bound", &v1.SwapRequest{CanaryPercent: 50, CanarySeconds: 30, CanaryMaxRegressionPercent: proto.Uint32(50)}, []uint32{50}, traffic(20, 0, 20, 20, 140*ms, 28*ms)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			n := newSlotNode(t, ctx)
			s := n.filled(t, ctx)
			old := n.occupant(t, s.GetId())
			every := time.Duration(c.req.GetCanarySeconds()) * time.Second
			seen := n.phases(ctx, s.GetId())
			task := n.startCanary(t, ctx, s, c.req)
			for i, share := range c.shares {
				occ, can := n.atStep(t, s.GetId(), share)
				if i == 0 {
					canary := n.canaryOf(t, s, old)
					if occ.Model != instances.ServedName(old) || can.Model != instances.ServedName(canary) || !strings.HasPrefix(canary.GetName(), "main~") {
						t.Fatalf("split %+v %+v, old %s canary %s", occ, can, instances.ServedName(old), canary.GetName())
					}
					if got := n.slot(t, s.GetId()); got.GetInstanceId() != old.GetId() || got.GetState() != v1.SlotState_SLOT_STATE_SWAPPING {
						t.Fatalf("during the canary the slot is %s with %s", got.GetState(), got.GetInstanceId())
					}
				}
				n.outcomes.add(occ, steady(100))
				n.outcomes.add(can, c.can)
				n.pass(t, every)
			}
			final, err := n.instances.Tasks.Wait(ctx, task.GetId())
			if err != nil || final.GetState() != v1.TaskState_TASK_STATE_SUCCEEDED {
				t.Fatalf("swap %s: %s %v", final.GetState(), final.GetError(), err)
			}
			canary := n.occupant(t, s.GetId())
			settled := n.slot(t, s.GetId())
			if canary.GetId() == old.GetId() || settled.GetState() != v1.SlotState_SLOT_STATE_READY || settled.GetRequest().GetGroup() != "q8" || settled.GetError() != "" {
				t.Fatalf("slot %s occupant %s group %s error %q", settled.GetState(), canary.GetId(), settled.GetRequest().GetGroup(), settled.GetError())
			}
			wantCondition(t, settled, CondCanary, false, CanaryPromoted, canary.GetName(), fmt.Sprintf("through step %d", len(c.shares)))
			wantPhases(t, seen, CanaryLaunching, CanaryShifting, CanaryPromoting, CanaryPromoted)
			wantCondition(t, settled, CondSwapping, false, "Settled")
			for _, name := range []string{"main", "chat"} {
				if rt, _ := n.registrar.Named(name); rt.Split || rt.Spec.ServedName != instances.ServedName(canary) {
					t.Fatalf("the promoted route %s %+v", name, rt)
				}
			}
			waitFor(t, "the old occupant to stop", func() bool {
				gone, _ := n.instances.Get(old.GetId())
				return gone.GetState() == v1.InstanceState_INSTANCE_STATE_STOPPED
			})
		})
	}
}

// A canary trailing the occupant, falling silent, failing, or cancelled goes back to the occupant at once and stops
func TestCanaryRollsBack(t *testing.T) {
	fastCanaryPoll(t)
	ms := time.Millisecond
	cases := []struct {
		name string
		req  *v1.SwapRequest
		// Counts added to the occupant before the swap starts
		before pools.Outcome
		// Canary counts per step, the last step being the one that rolls back
		steps []pools.Outcome
		// Breaks the canary during the last step in place of counting it
		breaks func(t *testing.T, n *slotNode, canary *v1.Instance, task *v1.Task)
		state  v1.TaskState
		why    string
	}{
		{name: "canary fails to start", req: &v1.SwapRequest{CanaryPercent: 10, CanarySeconds: 60, Run: &v1.RunRequest{SourceId: "src", Repo: "org/model", Group: "q8", RuntimeId: fake.RuntimeID, Params: map[string]string{fake.ParamFail: "true"}}}, why: "the canary did not start"},
		{name: "error rate", req: &v1.SwapRequest{CanaryPercent: 10, CanarySeconds: 60}, steps: []pools.Outcome{traffic(10, 5, 10, 5, 100*ms, 20*ms)}, why: "step 1: error rate 50.0%"},
		{name: "objective attainment", req: &v1.SwapRequest{CanaryPercent: 10, CanarySeconds: 60}, steps: []pools.Outcome{traffic(10, 0, 10, 6, 100*ms, 20*ms)}, why: "met 60.0% of objectives"},
		{name: "time to first token", req: &v1.SwapRequest{CanaryPercent: 10, CanarySeconds: 60}, steps: []pools.Outcome{traffic(10, 0, 10, 10, 300*ms, 20*ms)}, why: "mean time to first token 300ms"},
		{name: "strict bound", req: &v1.SwapRequest{CanaryPercent: 10, CanarySeconds: 60, CanaryMaxRegressionPercent: proto.Uint32(0)}, steps: []pools.Outcome{traffic(10, 0, 10, 10, 100*ms, 21*ms)}, why: "mean time per output token 21ms"},
		{name: "silent canary", req: &v1.SwapRequest{CanaryPercent: 10, CanarySeconds: 60}, steps: []pools.Outcome{{}}, why: "the canary served no requests"},
		{name: "later step", req: &v1.SwapRequest{CanaryPercent: 10, CanaryStepPercent: 40, CanarySeconds: 60}, steps: []pools.Outcome{steady(10), traffic(40, 30, 40, 10, 100*ms, 20*ms)}, why: "step 2: error rate"},
		{name: "counted from the canary's start", req: &v1.SwapRequest{CanaryPercent: 10, CanarySeconds: 60}, before: traffic(1000, 1000, 0, 0, 0, 0), steps: []pools.Outcome{traffic(10, 3, 10, 7, 100*ms, 20*ms)}, why: "error rate 30.0% against the occupant's 0.0%"},
		{name: "canary stops serving", req: &v1.SwapRequest{CanaryPercent: 10, CanarySeconds: 60}, steps: []pools.Outcome{{}}, breaks: func(t *testing.T, n *slotNode, canary *v1.Instance, _ *v1.Task) {
			if _, err := n.instances.Stop(context.Background(), canary.GetId()); err != nil {
				t.Fatal(err)
			}
		}, why: "step 1: the canary stopped serving"},
		{name: "cancelled", req: &v1.SwapRequest{CanaryPercent: 10, CanarySeconds: 60}, steps: []pools.Outcome{{}}, breaks: func(t *testing.T, n *slotNode, _ *v1.Instance, task *v1.Task) {
			if _, err := n.instances.Tasks.Cancel(task.GetId()); err != nil {
				t.Fatal(err)
			}
		}, state: v1.TaskState_TASK_STATE_CANCELED, why: "the swap was cancelled"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			n := newSlotNode(t, ctx)
			s := n.filled(t, ctx)
			old := n.occupant(t, s.GetId())
			rt, _ := n.registrar.Named("main")
			n.outcomes.add(pools.WeightedTarget{Pool: pools.PoolIDOf(pools.ModelKey{SourceID: old.GetSourceId(), Repo: old.GetRepo(), Group: old.GetGroup()}, old.GetRuntimeId(), n.instances.AcceleratorOf(ctx, old)), Model: rt.Spec.ServedName}, c.before)
			every := time.Duration(c.req.GetCanarySeconds()) * time.Second
			share := c.req.GetCanaryPercent()
			seen := n.phases(ctx, s.GetId())
			task := n.startCanary(t, ctx, s, c.req)
			var canary *v1.Instance
			for i, can := range c.steps {
				occT, canT := n.atStep(t, s.GetId(), share)
				canary = n.canaryOf(t, s, old)
				if i == len(c.steps)-1 && c.breaks != nil {
					c.breaks(t, n, canary, task)
					break
				}
				n.outcomes.add(occT, steady(100))
				n.outcomes.add(canT, can)
				n.pass(t, every)
				share += c.req.GetCanaryStepPercent()
			}
			final, err := n.instances.Tasks.Wait(ctx, task.GetId())
			want := cmp.Or(c.state, v1.TaskState_TASK_STATE_FAILED)
			if err != nil || final.GetState() != want || !strings.Contains(final.GetError(), c.why) {
				t.Fatalf("swap %s: %q %v, want %s with %q", final.GetState(), final.GetError(), err, want, c.why)
			}
			settled := n.slot(t, s.GetId())
			if settled.GetInstanceId() != old.GetId() || settled.GetState() != v1.SlotState_SLOT_STATE_READY || !strings.Contains(settled.GetError(), "canary rolled back") || settled.GetRequest().GetGroup() != "q4" {
				t.Fatalf("slot %s occupant %s error %q group %s", settled.GetState(), settled.GetInstanceId(), settled.GetError(), settled.GetRequest().GetGroup())
			}
			wantCondition(t, settled, CondCanary, false, CanaryRolledBack, c.why)
			if len(c.steps) == 0 {
				wantPhases(t, seen, CanaryLaunching, CanaryRollingBack, CanaryRolledBack)
			} else {
				wantPhases(t, seen, CanaryLaunching, CanaryShifting, CanaryRollingBack, CanaryRolledBack)
			}
			for _, name := range []string{"main", "chat"} {
				if rt, _ := n.registrar.Named(name); rt.Split || rt.Spec.ServedName != instances.ServedName(old) {
					t.Fatalf("the rolled back route %s %+v", name, rt)
				}
			}
			waitFor(t, "the occupant alone in the slot", func() bool {
				left := n.instances.InSlot(s.GetId())
				return len(left) == 1 && left[0].GetId() == old.GetId()
			})
			if canary != nil {
				if gone, _ := n.instances.Get(canary.GetId()); gone.GetState() != v1.InstanceState_INSTANCE_STATE_STOPPED {
					t.Fatalf("the canary is %s", gone.GetState())
				}
			}
		})
	}
}

// A canary the daemon died during is stopped on recovery and the occupant keeps every name
func TestCanaryAbandonedOnRestart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n := newSlotNode(t, ctx)
	s := n.filled(t, ctx)
	old := n.occupant(t, s.GetId())
	run := slotRun(s.GetId(), "q8")
	run.Name = "main~9"
	stray, task, err := n.instances.Run(instances.WithSwap(ctx), run)
	if err != nil {
		t.Fatal(err)
	}
	if err := n.instances.Tasks.WaitOK(ctx, task.GetId()); err != nil {
		t.Fatal(err)
	}
	stray, _ = n.instances.Get(stray.GetId())
	if err := n.slots.splitRoutes(ctx, s, []pools.WeightedTarget{n.slots.target(ctx, occupant{in: old}, 90), n.slots.target(ctx, occupant{in: stray}, 10)}); err != nil {
		t.Fatal(err)
	}
	n.slots.update(s.GetId(), func(sl *v1.Slot) {
		sl.State = v1.SlotState_SLOT_STATE_SWAPPING
		if n.slots.modes == nil {
			n.slots.modes = map[string]string{}
		}
		n.slots.modes[sl.GetId()] = ModeCanary
		n.slots.phaseLocked(sl.GetId(), phase{active: true, reason: CanaryShifting, message: "step 1 of 1 sends 10% of traffic to main~9"})
	})
	n.instances.Close()
	n.start(t, ctx)
	if err := n.instances.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	if err := n.slots.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the abandoned canary to stop", func() bool {
		gone, err := n.instances.Get(stray.GetId())
		return err == nil && gone.GetState() == v1.InstanceState_INSTANCE_STATE_STOPPED
	})
	settled := n.slot(t, s.GetId())
	if settled.GetInstanceId() != old.GetId() || settled.GetState() == v1.SlotState_SLOT_STATE_SWAPPING {
		t.Fatalf("slot %s occupant %s", settled.GetState(), settled.GetInstanceId())
	}
	wantCondition(t, settled, CondCanary, false, CanaryAbandoned, "stopped main~9")
	wantCondition(t, settled, CondSwapping, false, "Settled")
	for _, name := range []string{"main", "chat"} {
		if rt, _ := n.registrar.Named(name); rt.Split || rt.Spec.ServedName != instances.ServedName(old) {
			t.Fatalf("route %s after recovery %+v", name, rt)
		}
	}
	saved, err := n.db.ListSlots(ctx)
	if err != nil || len(saved) != 1 || instances.ConditionTrue(saved[0].GetConditions(), CondCanary) {
		t.Fatalf("the stored slot still runs a canary %v %v", saved, err)
	}
}

// A canary needs a share, a step length and counters, runs only beside an occupant, and never drains first
func TestCanaryRefusals(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n := newSlotNode(t, ctx)
	empty, err := n.slots.Create(ctx, &v1.CreateSlotRequest{Name: "spare"})
	if err != nil {
		t.Fatal(err)
	}
	s := n.filled(t, ctx)
	for _, c := range []struct {
		name string
		slot *v1.Slot
		req  *v1.SwapRequest
	}{
		{"share without a window", s, &v1.SwapRequest{CanaryPercent: 10}},
		{"window without a share", s, &v1.SwapRequest{CanarySeconds: 10}},
		{"drain first", s, &v1.SwapRequest{CanaryPercent: 10, CanarySeconds: 1, DrainFirst: true}},
		{"empty slot", empty, &v1.SwapRequest{CanaryPercent: 10, CanarySeconds: 1}},
	} {
		c.req.SlotId, c.req.Run = c.slot.GetId(), slotRun(c.slot.GetId(), "q8")
		if _, _, _, err := n.slots.Swap(ctx, c.req); !errors.Is(err, ErrSlot) {
			t.Fatalf("%s: %v", c.name, err)
		}
	}
	n.slots.Outcomes = nil
	if _, _, _, err := n.slots.Swap(ctx, &v1.SwapRequest{SlotId: s.GetId(), Run: slotRun(s.GetId(), "q8"), CanaryPercent: 10, CanarySeconds: 1}); !errors.Is(err, ErrSlot) {
		t.Fatalf("a canary without counters: %v", err)
	}
	if got := n.slot(t, s.GetId()); got.GetState() != v1.SlotState_SLOT_STATE_READY || got.GetInstanceId() != n.occupant(t, s.GetId()).GetId() {
		t.Fatalf("a refused canary changed the slot to %s", got.GetState())
	}
}

// A swap without a canary clears the condition an earlier canary left, beside an occupant or into an empty slot
func TestSwapClearsCanaryCondition(t *testing.T) {
	for _, evict := range []bool{false, true} {
		t.Run(fmt.Sprintf("evicted %v", evict), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			n := newSlotNode(t, ctx)
			s := n.filled(t, ctx)
			if evict {
				if _, err := n.slots.Evict(ctx, s.GetId()); err != nil {
					t.Fatal(err)
				}
			}
			n.slots.setPhase(s.GetId(), phase{reason: CanaryRolledBack, message: "error rate"})
			if instances.ConditionOf(n.slot(t, s.GetId()).GetConditions(), CondCanary) == nil {
				t.Fatal("the canary phase is not on the slot")
			}
			if _, _, _, err := n.slots.Swap(ctx, &v1.SwapRequest{SlotId: s.GetId(), Run: slotRun(s.GetId(), "q8")}); err != nil {
				t.Fatal(err)
			}
			waitFor(t, "the swap", func() bool {
				got := n.slot(t, s.GetId())
				return got.GetState() == v1.SlotState_SLOT_STATE_READY && got.GetRequest().GetGroup() == "q8"
			})
			if c := instances.ConditionOf(n.slot(t, s.GetId()).GetConditions(), CondCanary); c != nil {
				t.Fatalf("a plain swap kept %v", c)
			}
		})
	}
}
