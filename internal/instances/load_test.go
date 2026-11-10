package instances

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nickheyer/nebu/internal/pools"
	"github.com/nickheyer/nebu/pkg/datalayer"
	"github.com/nickheyer/nebu/pkg/datalayer/scrape"
	"github.com/nickheyer/nebu/pkg/events"
	"github.com/nickheyer/nebu/pkg/prefixcache"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// A pools manager over the node's database whose metrics source samples only on demand, started and closed with the test
func demandPools(t *testing.T, ctx context.Context, n *seatNode) *pools.Manager {
	t.Helper()
	pace := scrape.Pace{Deciding: 50 * time.Millisecond, Working: 50 * time.Millisecond, Idle: time.Hour}
	pm, err := pools.New(ctx, pools.Options{DB: n.db, Events: events.New(), NodeID: "n1", Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Scrape: pace, ProbeInterval: 50 * time.Millisecond, CatalogInterval: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	pm.SetIndexer(prefixcache.New(prefixcache.Config{}), prefixcache.Config{})
	pm.Store().MarkSynced()
	if err := pm.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pm.Close() })
	return pm
}

// The idle clock resets while a sample taken as the window ends shows a request that never went through a gateway
func TestIdleWindowReadsTheRuntimeScrape(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n := newSeatNode(t, ctx)
	pm := demandPools(t, ctx, n)
	m := n.manager(t, ctx, nil)
	m.Registrar = pm
	m.Load = ScrapedLoad(pm.Store(), pm.Sampler(), time.Second)
	run := soloRun(nil)
	run.IdleUnloadSeconds = 1
	in, _, err := m.Run(ctx, run)
	if err != nil {
		t.Fatal(err)
	}
	ready := waitState(t, m, in.GetId(), v1.InstanceState_INSTANCE_STATE_READY)
	waitFor(t, "the first sample of the idle runtime", func() bool {
		at, err := m.Load(ctx, in.GetId())
		return err == nil && at.IsZero()
	})
	release := n.launcher.Proc("solo").Hold()
	defer release()
	held := time.Now()
	answered := make(chan error, 1)
	go func() {
		resp, err := http.Post(ready.GetEndpoint()+"/v1/chat/completions", "application/json", strings.NewReader(`{"model":"solo","messages":[{"role":"user","content":"hi"}]}`))
		if err == nil {
			resp.Body.Close()
		}
		answered <- err
	}()
	waitFor(t, "a sample showing the request the runtime runs", func() bool {
		at, err := m.Load(ctx, in.GetId())
		return err == nil && at.After(held)
	})
	time.Sleep(2500 * time.Millisecond)
	if got := current(t, m, in.GetId()).GetState(); got != v1.InstanceState_INSTANCE_STATE_READY || n.traffic.Dispatched(in.GetId()) != 0 || n.traffic.InFlight(in.GetId()) != 0 {
		t.Fatalf("an instance running a request no gateway sent went %s", got)
	}
	release()
	if err := <-answered; err != nil {
		t.Fatal(err)
	}
	waitState(t, m, in.GetId(), v1.InstanceState_INSTANCE_STATE_IDLE)
	if eps := pm.Store().Endpoints("", func(ep datalayer.Endpoint) bool { return ep.Labels()[datalayer.LabelInstance] == in.GetId() }); len(eps) != 0 {
		t.Fatalf("the idle instance still publishes %d endpoints", len(eps))
	}
	if at, err := m.Load(ctx, in.GetId()); err != nil || !at.IsZero() {
		t.Fatalf("an unpublished instance reads work at %v, err %v", at, err)
	}
	if _, err := m.Stop(ctx, in.GetId()); err != nil {
		t.Fatal(err)
	}
}

// A request no gateway sent that starts and ends between two samples still resets the idle clock through the work counter
func TestIdleWindowCountsWorkBetweenSamples(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n := newSeatNode(t, ctx)
	pm := demandPools(t, ctx, n)
	m := n.manager(t, ctx, nil)
	m.Registrar = pm
	m.Load = ScrapedLoad(pm.Store(), pm.Sampler(), time.Second)
	run := soloRun(nil)
	run.IdleUnloadSeconds = 2
	in, _, err := m.Run(ctx, run)
	if err != nil {
		t.Fatal(err)
	}
	ready := waitState(t, m, in.GetId(), v1.InstanceState_INSTANCE_STATE_READY)
	began := time.Now()
	waitFor(t, "the first sample of the idle runtime", func() bool {
		eps := pm.Store().Endpoints("", func(ep datalayer.Endpoint) bool { return ep.Labels()[datalayer.LabelInstance] == in.GetId() })
		return len(eps) == 1 && slices.ContainsFunc(eps, func(ep datalayer.Endpoint) bool { _, ok := ep.Attribute(datalayer.CollectorMetrics); return ok })
	})
	resp, err := http.Post(ready.GetEndpoint()+"/v1/chat/completions", "application/json", strings.NewReader(`{"model":"solo","messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	time.Sleep(time.Until(began.Add(3 * time.Second)))
	if got := current(t, m, in.GetId()).GetState(); got != v1.InstanceState_INSTANCE_STATE_READY || n.traffic.Dispatched(in.GetId()) != 0 {
		t.Fatalf("an instance that served a request inside its idle window went %s", got)
	}
	waitState(t, m, in.GetId(), v1.InstanceState_INSTANCE_STATE_IDLE)
	if _, err := m.Stop(ctx, in.GetId()); err != nil {
		t.Fatal(err)
	}
}

// Records the endpoints it was asked to sample
type askedSampler struct{ asked [][]datalayer.EndpointID }

func (*askedSampler) Deciding(context.Context, []datalayer.EndpointID) func() { return func() {} }
func (*askedSampler) Serving(datalayer.EndpointID) func()                     { return func() {} }
func (s *askedSampler) Sample(_ context.Context, ids []datalayer.EndpointID) {
	s.asked = append(s.asked, slices.Clone(ids))
}

// An instance whose runtime cannot be sampled as its idle window ends stays loaded until a sample answers
func TestIdleWindowKeepsAnUnansweredRuntimeLoaded(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n := newSeatNode(t, ctx)
	m := n.manager(t, ctx, nil)
	var answering atomic.Bool
	m.Load = func(context.Context, string) (time.Time, error) {
		if !answering.Load() {
			return time.Time{}, ErrUnanswered
		}
		return time.Time{}, nil
	}
	run := soloRun(nil)
	run.IdleUnloadSeconds = 1
	in, _, err := m.Run(ctx, run)
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, m, in.GetId(), v1.InstanceState_INSTANCE_STATE_READY)
	time.Sleep(2500 * time.Millisecond)
	if got := current(t, m, in.GetId()).GetState(); got != v1.InstanceState_INSTANCE_STATE_READY {
		t.Fatalf("an instance whose runtime did not answer went %s", got)
	}
	answering.Store(true)
	waitState(t, m, in.GetId(), v1.InstanceState_INSTANCE_STATE_IDLE)
	if _, err := m.Stop(ctx, in.GetId()); err != nil {
		t.Fatal(err)
	}
}

// The latest work among an instance's scraped endpoints is read after they are sampled, one without a fresh sample reading as unanswered
func TestScrapedLoadReadsTheLatestWork(t *testing.T) {
	store := datalayer.NewStore()
	caps, _ := scrape.Capabilities(scrape.RuntimeVLLM)
	store.PutPool(datalayer.Pool{ID: "p", Metrics: caps})
	store.PutPool(datalayer.Pool{ID: "unscraped"})
	put := func(id datalayer.EndpointID, pool datalayer.PoolID, instance string, port int, m *datalayer.ServerMetrics) {
		store.UpsertEndpoint(datalayer.EndpointSpec{ID: id, Pool: pool, Address: datalayer.Address{Host: "127.0.0.1", Port: port}, Labels: map[string]string{datalayer.LabelInstance: instance}, Ready: true})
		if m != nil {
			store.SetAttribute(id, datalayer.CollectorMetrics, m)
		}
	}
	now := time.Now()
	early, late := now.Add(-8*time.Second), now.Add(-3*time.Second)
	put("a/1", "p", "a", 1, &datalayer.ServerMetrics{ActiveAt: early, UpdatedAt: now})
	put("a/2", "p", "a", 2, &datalayer.ServerMetrics{ActiveAt: late, UpdatedAt: now})
	put("a/3", "unscraped", "a", 3, nil)
	put("b/4", "p", "b", 4, &datalayer.ServerMetrics{UpdatedAt: now})
	put("c/5", "p", "c", 5, nil)
	put("d/6", "p", "d", 6, &datalayer.ServerMetrics{ActiveAt: now, UpdatedAt: now.Add(-time.Minute)})
	put("e/7", "p", "e", 7, &datalayer.ServerMetrics{ActiveAt: late, UpdatedAt: now})
	put("e/8", "p", "e", 8, &datalayer.ServerMetrics{ActiveAt: now, UpdatedAt: now.Add(-time.Minute)})
	put("f/9", "unscraped", "f", 9, nil)
	cases := []struct {
		instance string
		active   time.Time
		err      error
		asked    []datalayer.EndpointID
	}{
		{"a", late, nil, []datalayer.EndpointID{"a/1", "a/2"}},
		{"b", time.Time{}, nil, []datalayer.EndpointID{"b/4"}},
		{"c", time.Time{}, ErrUnanswered, []datalayer.EndpointID{"c/5"}},
		{"d", time.Time{}, ErrUnanswered, []datalayer.EndpointID{"d/6"}},
		{"e", time.Time{}, ErrUnanswered, []datalayer.EndpointID{"e/7", "e/8"}},
		{"f", time.Time{}, nil, nil},
		{"none", time.Time{}, nil, nil},
	}
	for _, c := range cases {
		sampler := &askedSampler{}
		active, err := ScrapedLoad(store, sampler, 10*time.Second)(context.Background(), c.instance)
		if !active.Equal(c.active) || !errors.Is(err, c.err) {
			t.Errorf("%s: active %v err %v, want %v %v", c.instance, active, err, c.active, c.err)
		}
		if len(sampler.asked) != 1 || !slices.Equal(sampler.asked[0], c.asked) {
			t.Errorf("%s: sampled %v, want one sample of %v", c.instance, sampler.asked, c.asked)
		}
	}
}
