package requestcontrol

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/nickheyer/nebu/pkg/datalayer"
	"github.com/nickheyer/nebu/pkg/flowcontrol"
	"github.com/nickheyer/nebu/pkg/scheduling/framework"
)

// A scheduler that ignores the subset only ever sees the endpoints inside it, none of them answering 503 unscheduled
func TestSubsetFilteredBeforeScheduler(t *testing.T) {
	cases := []struct {
		name   string
		subset string
		seen   []string
		err    error
	}{
		{"one of two", "10.0.0.2:8000", []string{"e2"}, nil},
		{"both", "10.0.0.1:8000,10.0.0.2:8000", []string{"e1", "e2"}, nil},
		{"outside the pool", "10.9.9.9:1", nil, ErrNoReadyEndpoints},
		{"empty", "", nil, ErrNoReadyEndpoints},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			var seen []string
			e.sched.fn = func(_ context.Context, _ *framework.Request, cands []datalayer.Endpoint) (*framework.Result, error) {
				for _, ep := range cands {
					seen = append(seen, string(ep.ID()))
				}
				return &framework.Result{PrimaryProfile: "p", ProfileResults: map[string]*framework.ProfileRunResult{"p": {Endpoints: cands}}}, nil
			}
			dec, err := e.dir.Decide(context.Background(), inbound(t, chat("llama"), HeaderSubset, c.subset))
			if c.err != nil {
				if code, _ := Status(err); !errors.Is(err, c.err) || code != 503 || e.sched.calls.Load() != 0 {
					t.Fatalf("err %v status %d scheduler calls %d", err, code, e.sched.calls.Load())
				}
				e.settled(t)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if fmt.Sprint(seen) != fmt.Sprint(c.seen) {
				t.Fatalf("scheduler saw %v, want %v", seen, c.seen)
			}
			e.dir.ResponseComplete(context.Background(), dec, nil)
			e.settled(t)
		})
	}
}

// A saturated answer from the scheduler sends the request back through flow control until an endpoint has room
func TestSaturatedSchedulerRequeues(t *testing.T) {
	cases := []struct {
		name      string
		saturated int
		mode      datalayer.FailureMode
	}{
		{"once under fail close", 1, datalayer.FailClose},
		{"three times under fail open", 3, datalayer.FailOpen},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			e.store.PutPool(datalayer.Pool{ID: "p1", Model: "llama", FailureMode: c.mode})
			e.sched.fn = func(ctx context.Context, req *framework.Request, cands []datalayer.Endpoint) (*framework.Result, error) {
				if e.sched.calls.Load() <= int64(c.saturated) {
					return nil, fmt.Errorf("lora-affinity-filter: %w", framework.ErrSaturated)
				}
				return pickAll(ctx, req, cands)
			}
			dec, err := e.dir.Decide(context.Background(), inbound(t, chat("llama")))
			if err != nil {
				t.Fatal(err)
			}
			if dec.FailedOpen || e.flow.requeued.Load() != int64(c.saturated) || e.sched.calls.Load() != int64(c.saturated)+1 {
				t.Fatalf("failed open %v requeued %d scheduler calls %d", dec.FailedOpen, e.flow.requeued.Load(), e.sched.calls.Load())
			}
			e.dir.ResponseComplete(context.Background(), dec, nil)
			e.settled(t)
		})
	}
	t.Run("requeue refusal is shed", func(t *testing.T) {
		e := newEnv(t)
		e.sched.fn = func(context.Context, *framework.Request, []datalayer.Endpoint) (*framework.Result, error) {
			e.flow.mu.Lock()
			e.flow.err = flowcontrol.ErrQueueTimeout
			e.flow.mu.Unlock()
			return nil, framework.ErrSaturated
		}
		_, err := e.dir.Decide(context.Background(), inbound(t, chat("llama")))
		if code, _ := Status(err); !errors.Is(err, ErrShed) || code != 429 {
			t.Fatalf("err %v status %d", err, code)
		}
		e.settled(t)
	})
}

// A pool with no ready endpoint answers 503 before flow control sees the request, whatever its band
func TestNoReadyEndpointRefusedBeforeAdmission(t *testing.T) {
	cases := []struct {
		name    string
		headers []string
		flowErr error
	}{
		{"default band", nil, nil},
		{"sheddable band flow control would shed", []string{HeaderObjective, "-5"}, flowcontrol.ErrShed},
		{"band flow control would refuse as full", []string{HeaderObjective, "3"}, flowcontrol.ErrQueueFull},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			e.store.UpsertEndpoint(endpoint("e1", "p1", "10.0.0.1", false))
			e.store.UpsertEndpoint(endpoint("e2", "p1", "10.0.0.2", false))
			e.flow.err = c.flowErr
			start := time.Now()
			_, err := e.dir.Decide(context.Background(), inbound(t, chat("llama"), c.headers...))
			code, retry := Status(err)
			if !errors.Is(err, ErrNoReadyEndpoints) || code != 503 || retry == "" {
				t.Fatalf("err %v status %d retry %q", err, code, retry)
			}
			if time.Since(start) > 100*time.Millisecond || len(e.flow.items) != 0 || e.sched.calls.Load() != 0 {
				t.Fatalf("refusal took %s, flow saw %d items, scheduler calls %d", time.Since(start), len(e.flow.items), e.sched.calls.Load())
			}
			e.settled(t)
		})
	}
}

// The route and its cap reach flow control on the item so the cap is waited for rather than refused
func TestRouteCapReachesFlowControl(t *testing.T) {
	e := newEnv(t)
	in := inbound(t, chat("llama"))
	in.Route, in.MaxInFlight = "chat", 3
	dec, err := e.dir.Decide(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if it := e.flow.lastItem(t); it.Route != "chat" || it.RouteLimit != 3 {
		t.Fatalf("item route %q limit %d", it.Route, it.RouteLimit)
	}
	e.dir.ResponseComplete(context.Background(), dec, nil)
	e.settled(t)
}

// Completion settles the tokens the response used against the permit before releasing it
func TestCompleteSettlesActualTokens(t *testing.T) {
	cases := []struct {
		name           string
		resp           *framework.Response
		prompt, output int64
	}{
		{"counted usage", &framework.Response{PromptTokens: 7, OutputTokens: 9}, 7, 9},
		{"no usage", nil, 0, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			dec, err := e.dir.Decide(context.Background(), inbound(t, chat("llama")))
			if err != nil {
				t.Fatal(err)
			}
			e.dir.ResponseComplete(context.Background(), dec, c.resp)
			if e.flow.prompt.Load() != c.prompt || e.flow.output.Load() != c.output {
				t.Fatalf("settled %d and %d, want %d and %d", e.flow.prompt.Load(), e.flow.output.Load(), c.prompt, c.output)
			}
			e.settled(t)
		})
	}
}

// A fairness id over its quota is refused as shed with 429
func TestQuotaRefusalIsShed(t *testing.T) {
	e := newEnv(t)
	e.flow.err = &flowcontrol.QuotaError{FairnessID: "tenant", Retry: time.Minute}
	_, err := e.dir.Decide(context.Background(), inbound(t, chat("llama")))
	code, retry := Status(err)
	if !errors.Is(err, ErrShed) || !errors.Is(err, flowcontrol.ErrQuotaExceeded) || code != 429 || retry == "" {
		t.Fatalf("err %v status %d retry %q", err, code, retry)
	}
	e.settled(t)
}
