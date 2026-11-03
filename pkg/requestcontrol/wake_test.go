package requestcontrol

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nickheyer/nebu/pkg/datalayer"
)

// A resolver reporting a wake under way for every pool while set
type wakingResolver struct {
	Resolver
	waking atomic.Bool
}

func (w *wakingResolver) Waking(datalayer.PoolID) bool { return w.waking.Load() }

// A woken request waits for its pool's first ready endpoint, or ends with the request timeout, the pool going away, or the wake lapsing
func TestWokenRequestHeldUntilReady(t *testing.T) {
	cases := []struct {
		name  string
		edit  func(e *env, w *wakingResolver)
		ctx   func() (context.Context, context.CancelFunc)
		want  error
		text  string
		admit int64
	}{
		{"ready endpoint joins", func(e *env, _ *wakingResolver) {
			go func() {
				time.Sleep(150 * time.Millisecond)
				e.store.UpsertEndpoint(endpoint("e1", "p1", "10.0.0.1", true))
			}()
		}, func() (context.Context, context.CancelFunc) { return context.WithCancel(context.Background()) }, nil, "", 1},
		{"request timeout passes", func(*env, *wakingResolver) {}, func() (context.Context, context.CancelFunc) {
			return context.WithTimeout(context.Background(), 300*time.Millisecond)
		}, ErrNoReadyEndpoints, "before the request timeout", 0},
		{"pool goes away", func(e *env, _ *wakingResolver) {
			go func() {
				time.Sleep(150 * time.Millisecond)
				e.store.DeletePool("p1")
			}()
		}, func() (context.Context, context.CancelFunc) { return context.WithCancel(context.Background()) }, ErrNoReadyEndpoints, "went away", 0},
		{"wake lapses with nothing", func(_ *env, w *wakingResolver) {
			go func() {
				time.Sleep(150 * time.Millisecond)
				w.waking.Store(false)
			}()
		}, func() (context.Context, context.CancelFunc) { return context.WithCancel(context.Background()) }, ErrNoReadyEndpoints, "woke nothing", 0},
		{"not woken answers at once", func(_ *env, w *wakingResolver) { w.waking.Store(false) }, func() (context.Context, context.CancelFunc) {
			return context.WithCancel(context.Background())
		}, ErrNoReadyEndpoints, "no ready endpoint", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var w *wakingResolver
			e := newEnv(t, func(d *Deps) {
				w = &wakingResolver{Resolver: d.Resolver}
				w.waking.Store(true)
				d.Resolver = w
			})
			e.store.DeleteEndpoint("e1")
			e.store.DeleteEndpoint("e2")
			c.edit(e, w)
			ctx, cancel := c.ctx()
			defer cancel()
			start := time.Now()
			dec, err := e.dir.Decide(ctx, inbound(t, chat("llama")))
			switch {
			case !errors.Is(err, c.want) || (c.text != "" && !strings.Contains(err.Error(), c.text)):
				t.Fatalf("err %v, want %v with %q", err, c.want, c.text)
			case c.want == nil && (dec == nil || time.Since(start) < 100*time.Millisecond):
				t.Fatalf("decided %v after %s without holding", dec, time.Since(start))
			case c.want != nil && c.name != "not woken answers at once" && time.Since(start) < 100*time.Millisecond:
				t.Fatalf("refused after %s without holding", time.Since(start))
			case e.flow.admitted.Load() != c.admit:
				t.Fatalf("flow admitted %d requests, want %d", e.flow.admitted.Load(), c.admit)
			}
			if dec != nil {
				e.dir.ResponseComplete(context.Background(), dec, nil)
			}
		})
	}
}
