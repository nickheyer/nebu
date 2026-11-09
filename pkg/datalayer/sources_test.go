package datalayer

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"
)

// Records every endpoint set it is handed
type fakeSource struct {
	typ     string
	tracks  func(Pool) bool
	mu      sync.Mutex
	started bool
	stopped bool
	last    []EndpointID
	updates chan []EndpointID
	failOn  int
	calls   int
}

func newFake(typ string, tracks func(Pool) bool) *fakeSource {
	return &fakeSource{typ: typ, tracks: tracks, updates: make(chan []EndpointID, 64)}
}

func (f *fakeSource) Type() string { return f.typ }

func (f *fakeSource) Start(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.started = true
	return nil
}

func (f *fakeSource) Stop() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopped = true
	return nil
}

func (f *fakeSource) Subscribe(Collector) error { return nil }

func (f *fakeSource) UpdateEndpoints(ids []EndpointID) error {
	f.mu.Lock()
	f.calls++
	fail := f.failOn != 0 && f.calls == f.failOn
	f.last = slices.Clone(ids)
	f.mu.Unlock()
	f.updates <- slices.Clone(ids)
	if fail {
		return errors.New("refused")
	}
	return nil
}

// A fake that scopes itself to pools, unlike the plain fake
type scopedFake struct{ *fakeSource }

func (s scopedFake) Tracks(p Pool) bool { return s.tracks(p) }

// Waits until the source has been handed exactly want
func await(t *testing.T, f *fakeSource, want []EndpointID) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case got := <-f.updates:
			if reflect.DeepEqual(got, want) || (len(got) == 0 && len(want) == 0) {
				return
			}
		case <-deadline:
			f.mu.Lock()
			defer f.mu.Unlock()
			t.Fatalf("%s last handed %v, want %v", f.typ, f.last, want)
		}
	}
}

func TestRunnerPropagation(t *testing.T) {
	store := NewStore()
	metricsPools := func(p Pool) bool { return p.Metrics.Complete() }
	scoped := scopedFake{newFake("scoped", metricsPools)}
	plain := newFake("plain", nil)
	sources := Sources{}
	if err := sources.Register(scoped); err != nil {
		t.Fatal(err)
	}
	if err := sources.Register(plain); err != nil {
		t.Fatal(err)
	}
	if err := sources.Register(newFake("plain", nil)); !errors.Is(err, ErrDuplicateSource) {
		t.Fatalf("duplicate register error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := NewRunner(store, sources)
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	await(t, scoped.fakeSource, nil)
	await(t, plain, nil)

	full := MetricCapabilities{QueuedRequests: "q", RunningRequests: "r", KVCacheUtilization: "k"}
	steps := []struct {
		name       string
		act        func()
		wantScoped []EndpointID
		wantPlain  []EndpointID
	}{
		{"endpoint before its pool", func() { store.UpsertEndpoint(spec("a1", "a", nil)) }, nil, []EndpointID{"a1"}},
		{"pool with metrics", func() { store.PutPool(Pool{ID: "a", Metrics: full}) }, []EndpointID{"a1"}, []EndpointID{"a1"}},
		{"pool without metrics", func() { store.PutPool(Pool{ID: "b"}) }, []EndpointID{"a1"}, []EndpointID{"a1"}},
		{"endpoint in bare pool", func() { store.UpsertEndpoint(spec("b1", "b", nil)) }, []EndpointID{"a1"}, []EndpointID{"a1", "b1"}},
		{"second scoped endpoint", func() { store.UpsertEndpoint(spec("a2", "a", nil)) }, []EndpointID{"a1", "a2"}, []EndpointID{"a1", "a2", "b1"}},
		{"bare pool gains metrics", func() { store.PutPool(Pool{ID: "b", Metrics: full}) }, []EndpointID{"a1", "a2", "b1"}, []EndpointID{"a1", "a2", "b1"}},
		{"endpoint deleted", func() { store.DeleteEndpoint("a1") }, []EndpointID{"a2", "b1"}, []EndpointID{"a2", "b1"}},
		{"pool deleted", func() { store.DeletePool("b") }, []EndpointID{"a2"}, []EndpointID{"a2"}},
	}
	for _, st := range steps {
		t.Run(st.name, func(t *testing.T) {
			st.act()
			await(t, scoped.fakeSource, st.wantScoped)
			await(t, plain, st.wantPlain)
		})
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run returned %v", err)
	}
	for _, f := range []*fakeSource{scoped.fakeSource, plain} {
		if !f.started || !f.stopped {
			t.Fatalf("%s started=%v stopped=%v", f.typ, f.started, f.stopped)
		}
	}
}

func TestRunnerReturnsUpdateErrors(t *testing.T) {
	store := NewStore()
	f := newFake("f", nil)
	f.failOn = 2
	r := NewRunner(store, Sources{"f": f})
	done := make(chan error, 1)
	go func() { done <- r.Run(context.Background()) }()
	await(t, f, nil)
	store.UpsertEndpoint(spec("e", "p", nil))
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Run swallowed the update error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run kept going after an update error")
	}
	if !f.stopped {
		t.Fatal("source not stopped after the error")
	}
}
