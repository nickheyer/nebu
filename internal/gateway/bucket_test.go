package gateway

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nickheyer/nebu/pkg/metrics"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/requestcontrol"
	"github.com/nickheyer/nebu/pkg/scheduling/framework"
	"github.com/prometheus/client_golang/prometheus"
)

// A clock that stands still until the test moves it
type stepClock struct{ now time.Time }

func (c *stepClock) Now() time.Time { return c.now }

func (c *stepClock) advance(d time.Duration) { c.now = c.now.Add(d) }

// One draw from a route's bucket after moving the clock, with the answer it must give
type draw struct {
	after time.Duration
	ok    bool
	wait  time.Duration
}

// A route bucket gives its burst at once, then one token per interval, and refills to the burst and no further
func TestRouteBucketRefills(t *testing.T) {
	cases := []struct {
		name  string
		rate  float64
		burst int
		draws []draw
	}{
		{"burst then one per interval", 2, 3, []draw{
			{0, true, 0}, {0, true, 0}, {0, true, 0},
			{0, false, 500 * time.Millisecond},
			{499 * time.Millisecond, false, time.Millisecond},
			{time.Millisecond, true, 0},
			{0, false, 500 * time.Millisecond},
		}},
		{"idle refills to the burst and no further", 2, 3, []draw{
			{0, true, 0}, {0, true, 0}, {0, true, 0},
			{10 * time.Second, true, 0}, {0, true, 0}, {0, true, 0},
			{0, false, 500 * time.Millisecond},
		}},
		{"rate below one per second", 0.5, 1, []draw{
			{0, true, 0},
			{0, false, 2 * time.Second},
			{1500 * time.Millisecond, false, 500 * time.Millisecond},
			{500 * time.Millisecond, true, 0},
			{0, false, 2 * time.Second},
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			clock := &stepClock{now: time.Unix(1_700_000_000, 0)}
			st := &routeState{}
			for i, d := range c.draws {
				clock.advance(d.after)
				ok, wait := st.take(clock.Now(), c.rate, c.burst)
				if ok != d.ok || wait != d.wait {
					t.Fatalf("draw %d after %s: ok %v wait %s, want ok %v wait %s", i, d.after, ok, wait, d.ok, d.wait)
				}
			}
		})
	}
}

// Refuses past the default burst with the wait for the next token and admits once the gateway clock reaches it
func TestRoutePolicyRefillsOnTheGatewayClock(t *testing.T) {
	reg, err := metrics.New(prometheus.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}
	clock := &stepClock{now: time.Unix(1_700_000_000, 0)}
	g := &Gateway{clock: clock, metrics: reg}
	p := &routePolicy{g: g}
	x := &exchange{g: g, model: "rated", route: &v1.Route{Name: "rated"}, policy: &v1.Policy{RequestsPerSecond: 2}}
	ctx := withExchange(context.Background(), x)
	admit := func() error {
		x.hold = nil
		return p.PreRequest(ctx, nil, &framework.Request{})
	}
	for i := range 2 {
		if err := admit(); err != nil || x.hold == nil {
			t.Fatalf("request %d of the burst: %v", i, err)
		}
	}
	err = admit()
	var refused *policyError
	if !errors.As(err, &refused) || !errors.Is(err, requestcontrol.ErrShed) || refused.wait != 500*time.Millisecond || x.hold != nil {
		t.Fatalf("past the burst: %v", err)
	}
	st := g.routeState("rated")
	if n := st.inflight.Load(); n != 2 {
		t.Fatalf("a refused request holds the route: %d in flight", n)
	}
	clock.advance(499 * time.Millisecond)
	if err := admit(); !errors.As(err, &refused) || refused.wait != time.Millisecond {
		t.Fatalf("a millisecond before the next token: %v", err)
	}
	clock.advance(time.Millisecond)
	if err := admit(); err != nil || x.hold == nil {
		t.Fatalf("once the next token is due: %v", err)
	}
	if n, total := st.inflight.Load(), st.requests.Load(); n != 3 || total != 5 {
		t.Fatalf("%d in flight of %d requests, want 3 of 5", n, total)
	}
}
