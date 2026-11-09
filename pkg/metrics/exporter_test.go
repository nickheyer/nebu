package metrics

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/nickheyer/nebu/pkg/datalayer"
	"github.com/nickheyer/nebu/pkg/flowcontrol"
)

// An endpoint with a fixed spec and mutable attributes
type fakeEndpoint struct {
	spec  datalayer.EndpointSpec
	attrs map[string]any
}

func (e *fakeEndpoint) ID() datalayer.EndpointID       { return e.spec.ID }
func (e *fakeEndpoint) Pool() datalayer.PoolID         { return e.spec.Pool }
func (e *fakeEndpoint) Address() datalayer.Address     { return e.spec.Address }
func (e *fakeEndpoint) Labels() map[string]string      { return e.spec.Labels }
func (e *fakeEndpoint) Ready() bool                    { return e.spec.Ready }
func (e *fakeEndpoint) Attribute(c string) (any, bool) { v, ok := e.attrs[c]; return v, ok }

// A store answering the reads the exporter makes
type fakeStore struct {
	datalayer.Store
	mu    sync.Mutex
	pools []datalayer.Pool
	eps   map[datalayer.EndpointID]*fakeEndpoint
}

func (s *fakeStore) Pools() []datalayer.Pool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]datalayer.Pool(nil), s.pools...)
}

func (s *fakeStore) Endpoints(pool datalayer.PoolID, keep func(datalayer.Endpoint) bool) []datalayer.Endpoint {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []datalayer.Endpoint
	for _, e := range s.eps {
		if (pool == "" || e.spec.Pool == pool) && keep(e) {
			out = append(out, e)
		}
	}
	return out
}

func (s *fakeStore) Endpoint(id datalayer.EndpointID) (datalayer.Endpoint, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.eps[id]
	return e, ok
}

func (s *fakeStore) put(e *fakeEndpoint) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.eps[e.spec.ID] = e
}

func (s *fakeStore) remove(id datalayer.EndpointID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.eps, id)
}

// A controller answering depth per pool
type fakeFlow struct {
	flowcontrol.Controller
	mu    sync.Mutex
	depth map[datalayer.PoolID]map[int]int
}

func (f *fakeFlow) Depth(pool datalayer.PoolID) map[int]int {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[int]int{}
	for k, v := range f.depth[pool] {
		out[k] = v
	}
	return out
}

func (f *fakeFlow) set(pool datalayer.PoolID, d map[int]int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.depth[pool] = d
}

// A prefix index of a fixed size
type fakeIndex int

func (n fakeIndex) Len() int { return int(n) }

func TestExporterMirrorsStore(t *testing.T) {
	r, preg := newRegistry(t)
	now := time.Now()
	store := &fakeStore{
		pools: []datalayer.Pool{{ID: "p1"}, {ID: "p2"}},
		eps: map[datalayer.EndpointID]*fakeEndpoint{
			"a": {spec: datalayer.EndpointSpec{ID: "a", Pool: "p1", Ready: true}, attrs: map[string]any{
				datalayer.CollectorMetrics: &datalayer.ServerMetrics{QueuedRequests: 3, RunningRequests: 5, KVCacheUtilization: 0.25, UpdatedAt: now},
				datalayer.CollectorAssumed: 2,
				datalayer.CollectorHealth:  &datalayer.HealthState{Healthy: true},
			}},
			"b": {spec: datalayer.EndpointSpec{ID: "b", Pool: "p1", Ready: true}, attrs: map[string]any{
				datalayer.CollectorHealth: &datalayer.HealthState{Healthy: false},
			}},
			"c": {spec: datalayer.EndpointSpec{ID: "c", Pool: "p3", Ready: true}, attrs: map[string]any{}},
			"d": {spec: datalayer.EndpointSpec{ID: "d", Pool: "p1", Ready: true}, attrs: map[string]any{
				datalayer.CollectorMetrics: &datalayer.ServerMetrics{QueuedRequests: 1, RunningRequests: 1, KVCacheUtilization: 0.75, UpdatedAt: now},
				datalayer.CollectorHealth:  &datalayer.HealthState{Healthy: true},
			}},
		},
	}
	flow := &fakeFlow{depth: map[datalayer.PoolID]map[int]int{"p1": {0: 4, 1: 1}}}
	e, err := NewExporter(r, store, flow, fakeIndex(42), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	e.Mirror()

	expect := func(name string, want map[string]float64) {
		t.Helper()
		got := values(t, preg, name)
		if len(got) != len(want) {
			t.Fatalf("%s = %v, want %v", name, got, want)
		}
		for k, v := range want {
			if g, ok := got[k]; !ok || g != v {
				t.Fatalf("%s = %v, want %v", name, got, want)
			}
		}
	}
	expect("nebu_endpoint_queued_requests", map[string]float64{"endpoint=a,pool=p1": 3, "endpoint=d,pool=p1": 1})
	expect("nebu_endpoint_running_requests", map[string]float64{"endpoint=a,pool=p1": 5, "endpoint=d,pool=p1": 1})
	expect("nebu_endpoint_kv_cache_utilization", map[string]float64{"endpoint=a,pool=p1": 0.25, "endpoint=d,pool=p1": 0.75})
	expect("nebu_endpoint_assumed_requests", map[string]float64{"endpoint=a,pool=p1": 2, "endpoint=b,pool=p1": 0, "endpoint=c,pool=p3": 0, "endpoint=d,pool=p1": 0})
	expect("nebu_endpoint_ready", map[string]float64{"endpoint=a,pool=p1": 1, "endpoint=b,pool=p1": 0, "endpoint=c,pool=p3": 1, "endpoint=d,pool=p1": 1})
	expect("nebu_pool_ready_endpoints", map[string]float64{"pool=p1": 2, "pool=p2": 0, "pool=p3": 1})
	expect("nebu_pool_kv_cache_utilization", map[string]float64{"pool=p1": 0.5})
	expect("nebu_pool_queued_requests", map[string]float64{"pool=p1": 4, "pool=p2": 0, "pool=p3": 0})
	expect("nebu_flow_queue_depth", map[string]float64{"pool=p1,priority=0": 4, "pool=p1,priority=1": 1, "pool=p2,priority=0": 0, "pool=p3,priority=0": 0})
	expect("nebu_prefix_cache_entries", map[string]float64{"": 42})

	r.OnScrape("p1", "a", 20*time.Millisecond, nil)
	r.OnScrape("p1", "b", 30*time.Millisecond, errors.New("connection refused"))
	expect("nebu_endpoint_scrape_duration_seconds", map[string]float64{"pool=p1": 2})
	expect("nebu_endpoint_scrape_errors_total", map[string]float64{"endpoint=b,pool=p1": 1})

	// Endpoints b and d depart, a moves pools, band 1 empties and p3 loses its only endpoint and pool
	store.remove("b")
	store.remove("c")
	store.remove("d")
	store.put(&fakeEndpoint{spec: datalayer.EndpointSpec{ID: "a", Pool: "p2", Ready: false}, attrs: map[string]any{
		datalayer.CollectorMetrics: &datalayer.ServerMetrics{QueuedRequests: 9, UpdatedAt: now},
	}})
	flow.set("p1", map[int]int{0: 2})
	e.Mirror()

	expect("nebu_endpoint_queued_requests", map[string]float64{"endpoint=a,pool=p2": 9})
	expect("nebu_endpoint_running_requests", map[string]float64{"endpoint=a,pool=p2": 0})
	expect("nebu_endpoint_assumed_requests", map[string]float64{"endpoint=a,pool=p2": 0})
	expect("nebu_endpoint_ready", map[string]float64{"endpoint=a,pool=p2": 0})
	expect("nebu_endpoint_scrape_errors_total", map[string]float64{})
	expect("nebu_pool_ready_endpoints", map[string]float64{"pool=p1": 0, "pool=p2": 0})
	expect("nebu_pool_kv_cache_utilization", map[string]float64{"pool=p2": 0})
	expect("nebu_pool_queued_requests", map[string]float64{"pool=p1": 0, "pool=p2": 9})
	expect("nebu_flow_queue_depth", map[string]float64{"pool=p1,priority=0": 2, "pool=p1,priority=1": 0, "pool=p2,priority=0": 0})
}

// A scrape error series recorded before the first mirror pass goes with its endpoint
func TestExporterScrapeSeriesFollowEndpoint(t *testing.T) {
	r, preg := newRegistry(t)
	store := &fakeStore{eps: map[datalayer.EndpointID]*fakeEndpoint{
		"n": {spec: datalayer.EndpointSpec{ID: "n", Pool: "p"}},
	}}
	e, err := NewExporter(r, store, &fakeFlow{depth: map[datalayer.PoolID]map[int]int{}}, fakeIndex(0), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	r.OnScrape("p", "n", time.Millisecond, errors.New("timeout"))
	if got := values(t, preg, "nebu_endpoint_scrape_errors_total"); got["endpoint=n,pool=p"] != 1 {
		t.Fatalf("errors %v", got)
	}
	e.Mirror()
	store.remove("n")
	e.Mirror()
	if got := values(t, preg, "nebu_endpoint_scrape_errors_total"); len(got) != 0 {
		t.Fatalf("series of a departed endpoint survived: %v", got)
	}
}

func TestExporterRunTicks(t *testing.T) {
	r, preg := newRegistry(t)
	store := &fakeStore{pools: []datalayer.Pool{{ID: "p"}}, eps: map[datalayer.EndpointID]*fakeEndpoint{}}
	e, err := NewExporter(r, store, &fakeFlow{depth: map[datalayer.PoolID]map[int]int{}}, fakeIndex(0), 5*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()

	store.put(&fakeEndpoint{spec: datalayer.EndpointSpec{ID: "x", Pool: "p", Ready: true}, attrs: map[string]any{}})
	deadline := time.Now().Add(5 * time.Second)
	for values(t, preg, "nebu_pool_ready_endpoints")["pool=p"] != 1 {
		if time.Now().After(deadline) {
			t.Fatal("ticker never mirrored the new endpoint")
		}
		time.Sleep(2 * time.Millisecond)
	}
	if _, err := NewExporter(r, store, &fakeFlow{}, fakeIndex(0), 0); err == nil {
		t.Fatal("zero interval accepted")
	}
	if _, err := NewExporter(r, store, &fakeFlow{}, nil, time.Second); err == nil {
		t.Fatal("missing index accepted")
	}
}
