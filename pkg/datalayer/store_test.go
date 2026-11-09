package datalayer

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"
)

func spec(id, pool string, labels map[string]string) EndpointSpec {
	return EndpointSpec{ID: EndpointID(id), Pool: PoolID(pool), Address: Address{Host: "10.0.0.1", Port: 8000}, Labels: labels, Ready: true}
}

func ids(eps []Endpoint) []EndpointID {
	out := make([]EndpointID, 0, len(eps))
	for _, ep := range eps {
		out = append(out, ep.ID())
	}
	return out
}

func TestPools(t *testing.T) {
	s := NewStore()
	sel := map[string]string{"model": "a"}
	match := map[string]string{"request_type": "waiting"}
	s.PutPool(Pool{ID: "b", Model: "mb"})
	s.PutPool(Pool{ID: "a", Model: "ma", Selector: sel, Metrics: MetricCapabilities{QueuedRequestsMatch: match}})
	sel["model"] = "mutated"
	match["request_type"] = "mutated"

	tests := []struct {
		name string
		id   PoolID
		ok   bool
	}{
		{"present", "a", true},
		{"second", "b", true},
		{"absent", "c", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, ok := s.Pool(tc.id)
			if ok != tc.ok {
				t.Fatalf("Pool(%s) ok = %v, want %v", tc.id, ok, tc.ok)
			}
		})
	}
	p, _ := s.Pool("a")
	if p.Selector["model"] != "a" || p.Metrics.QueuedRequestsMatch["request_type"] != "waiting" {
		t.Fatalf("caller mutation reached the store: %+v", p)
	}
	var got []PoolID
	for _, p := range s.Pools() {
		got = append(got, p.ID)
	}
	if !reflect.DeepEqual(got, []PoolID{"a", "b"}) {
		t.Fatalf("Pools order = %v", got)
	}
	s.DeletePool("a")
	if _, ok := s.Pool("a"); ok {
		t.Fatal("deleted pool still present")
	}
}

func TestEndpointSnapshotIsolation(t *testing.T) {
	tests := []struct {
		name  string
		write func(s Store)
		check func(t *testing.T, before Endpoint)
	}{
		{
			name:  "attribute set after read",
			write: func(s Store) { s.SetAttribute("e1", "c", 2) },
			check: func(t *testing.T, before Endpoint) {
				if v, _ := before.Attribute("c"); v != 1 {
					t.Fatalf("old view attribute = %v, want 1", v)
				}
			},
		},
		{
			name:  "attribute removed after read",
			write: func(s Store) { s.SetAttribute("e1", "c", nil) },
			check: func(t *testing.T, before Endpoint) {
				if _, ok := before.Attribute("c"); !ok {
					t.Fatal("old view lost its attribute")
				}
			},
		},
		{
			name:  "spec replaced after read",
			write: func(s Store) { s.UpsertEndpoint(spec("e1", "p", map[string]string{"role": "decode"})) },
			check: func(t *testing.T, before Endpoint) {
				if before.Labels()["role"] != "prefill" {
					t.Fatalf("old view labels = %v", before.Labels())
				}
			},
		},
		{
			name:  "endpoint deleted after read",
			write: func(s Store) { s.DeleteEndpoint("e1") },
			check: func(t *testing.T, before Endpoint) {
				if before.ID() != "e1" || !before.Ready() {
					t.Fatal("old view changed after delete")
				}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := NewStore()
			labels := map[string]string{"role": "prefill"}
			s.UpsertEndpoint(spec("e1", "p", labels))
			labels["role"] = "mutated"
			s.SetAttribute("e1", "c", 1)
			before, ok := s.Endpoint("e1")
			if !ok {
				t.Fatal("endpoint missing")
			}
			tc.write(s)
			tc.check(t, before)
		})
	}
}

func TestUpsertKeepsAttributes(t *testing.T) {
	tests := []struct {
		name     string
		next     EndpointSpec
		wantPool PoolID
	}{
		{"same pool", spec("e1", "p", map[string]string{"x": "1"}), "p"},
		{"moved pool", spec("e1", "q", nil), "q"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := NewStore()
			s.UpsertEndpoint(spec("e1", "p", nil))
			s.SetAttribute("e1", CollectorMetrics, &ServerMetrics{QueuedRequests: 4})
			s.UpsertEndpoint(tc.next)
			ep, _ := s.Endpoint("e1")
			m, ok := Attribute[*ServerMetrics](ep, CollectorMetrics)
			if !ok || m.QueuedRequests != 4 {
				t.Fatalf("attribute lost across upsert: %v %v", m, ok)
			}
			if ep.Pool() != tc.wantPool {
				t.Fatalf("pool = %s, want %s", ep.Pool(), tc.wantPool)
			}
			if got := ids(s.Endpoints(tc.wantPool, nil)); !reflect.DeepEqual(got, []EndpointID{"e1"}) {
				t.Fatalf("pool membership = %v", got)
			}
			if tc.wantPool != "p" && len(s.Endpoints("p", nil)) != 0 {
				t.Fatal("endpoint still listed under its old pool")
			}
		})
	}
}

func TestEndpoints(t *testing.T) {
	s := NewStore()
	for _, sp := range []EndpointSpec{spec("c", "p1", nil), spec("a", "p1", nil), spec("b", "p2", nil), spec("d", "p2", nil)} {
		s.UpsertEndpoint(sp)
	}
	notD := func(ep Endpoint) bool { return ep.ID() != "d" }
	tests := []struct {
		name string
		pool PoolID
		keep func(Endpoint) bool
		want []EndpointID
	}{
		{"all pools", "", nil, []EndpointID{"a", "b", "c", "d"}},
		{"one pool", "p1", nil, []EndpointID{"a", "c"}},
		{"predicate", "p2", notD, []EndpointID{"b"}},
		{"predicate over all", "", notD, []EndpointID{"a", "b", "c"}},
		{"unknown pool", "zz", nil, []EndpointID{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ids(s.Endpoints(tc.pool, tc.keep)); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSetAttribute(t *testing.T) {
	tests := []struct {
		name     string
		preset   any
		id       EndpointID
		value    any
		wantHas  bool
		wantSeen int
	}{
		{"stores", nil, "e1", 7, true, 1},
		{"replaces", 1, "e1", 7, true, 1},
		{"nil removes", 1, "e1", nil, false, 1},
		{"nil on absent is silent", nil, "e1", nil, false, 0},
		{"unknown endpoint is silent", nil, "zz", 7, false, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := NewStore()
			s.UpsertEndpoint(spec("e1", "p", nil))
			if tc.preset != nil {
				s.SetAttribute("e1", "c", tc.preset)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			w := s.Watch(ctx)
			s.SetAttribute(tc.id, "c", tc.value)
			drain(t, w, tc.wantSeen)
			ep, _ := s.Endpoint("e1")
			v, has := ep.Attribute("c")
			if has != tc.wantHas || (has && v != tc.value) {
				t.Fatalf("attribute = %v %v", v, has)
			}
		})
	}
}

// Reads exactly n changes and fails on any extra change arriving shortly after
func drain(t *testing.T, w <-chan Change, n int) []Change {
	t.Helper()
	var out []Change
	for len(out) < n {
		select {
		case c := <-w:
			out = append(out, c)
		case <-time.After(2 * time.Second):
			t.Fatalf("got %d of %d changes: %v", len(out), n, out)
		}
	}
	select {
	case c := <-w:
		t.Fatalf("unexpected extra change %+v", c)
	case <-time.After(20 * time.Millisecond):
	}
	return out
}

func TestDeletePoolCascades(t *testing.T) {
	s := NewStore()
	s.PutPool(Pool{ID: "p"})
	s.UpsertEndpoint(spec("b", "p", nil))
	s.UpsertEndpoint(spec("a", "p", nil))
	s.UpsertEndpoint(spec("z", "q", nil))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := s.Watch(ctx)
	s.DeletePool("p")
	s.DeletePool("p")
	want := []Change{
		{Kind: ChangeEndpointDeleted, Pool: "p", Endpoint: "a"},
		{Kind: ChangeEndpointDeleted, Pool: "p", Endpoint: "b"},
		{Kind: ChangePoolDeleted, Pool: "p"},
	}
	if got := drain(t, w, len(want)); !reflect.DeepEqual(got, want) {
		t.Fatalf("changes = %+v", got)
	}
	if got := ids(s.Endpoints("", nil)); !reflect.DeepEqual(got, []EndpointID{"z"}) {
		t.Fatalf("remaining = %v", got)
	}
	s.SetAttribute("a", "c", 1)
	if _, ok := s.Endpoint("a"); ok {
		t.Fatal("cascaded endpoint came back")
	}
}

func TestWatchOrdering(t *testing.T) {
	s := NewStore()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := s.Watch(ctx)
	s.PutPool(Pool{ID: "p"})
	s.UpsertEndpoint(spec("e1", "p", nil))
	s.SetAttribute("e1", "m", 1)
	s.UpsertEndpoint(spec("e1", "q", nil))
	s.SetAttribute("e1", "m", nil)
	s.DeleteEndpoint("e1")
	s.DeletePool("p")
	want := []Change{
		{Kind: ChangePoolPut, Pool: "p"},
		{Kind: ChangeEndpointPut, Pool: "p", Endpoint: "e1"},
		{Kind: ChangeAttribute, Pool: "p", Endpoint: "e1", Collector: "m"},
		{Kind: ChangeEndpointPut, Pool: "q", Endpoint: "e1"},
		{Kind: ChangeAttribute, Pool: "q", Endpoint: "e1", Collector: "m"},
		{Kind: ChangeEndpointDeleted, Pool: "q", Endpoint: "e1"},
		{Kind: ChangePoolDeleted, Pool: "p"},
	}
	if got := drain(t, w, len(want)); !reflect.DeepEqual(got, want) {
		t.Fatalf("changes =\n%+v\nwant\n%+v", got, want)
	}
}

func TestWatchUnboundedAndShared(t *testing.T) {
	s := NewStore()
	const writers, each = 8, 500
	for i := range writers {
		s.UpsertEndpoint(spec(fmt.Sprintf("e%d", i), "p", nil))
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w1, w2 := s.Watch(ctx), s.Watch(ctx)
	var wg sync.WaitGroup
	for i := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := range each {
				s.SetAttribute(EndpointID(fmt.Sprintf("e%d", i)), "m", n)
			}
		}()
	}
	wg.Wait()
	a := drain(t, w1, writers*each)
	b := drain(t, w2, writers*each)
	if !reflect.DeepEqual(a, b) {
		t.Fatal("watchers saw different orders")
	}
	last := map[EndpointID]int{}
	for _, c := range a {
		last[c.Endpoint]++
	}
	for i := range writers {
		id := EndpointID(fmt.Sprintf("e%d", i))
		if last[id] != each {
			t.Fatalf("%s delivered %d changes, want %d", id, last[id], each)
		}
		ep, _ := s.Endpoint(id)
		if v, _ := ep.Attribute("m"); v != each-1 {
			t.Fatalf("%s final value %v", id, v)
		}
	}
}

func TestWatchClosesOnCancel(t *testing.T) {
	s := NewStore()
	ctx, cancel := context.WithCancel(context.Background())
	w := s.Watch(ctx)
	s.PutPool(Pool{ID: "p"})
	cancel()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case _, open := <-w:
			if !open {
				return
			}
		case <-deadline:
			t.Fatal("watch channel never closed")
		}
	}
}

func TestNoLostAttributeAcrossConcurrentUpserts(t *testing.T) {
	s := NewStore()
	s.UpsertEndpoint(spec("e1", "p0", nil))
	const writes = 2000
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for n := range writes {
			s.SetAttribute("e1", "m", n)
		}
	}()
	go func() {
		defer wg.Done()
		for n := range 200 {
			s.UpsertEndpoint(spec("e1", fmt.Sprintf("p%d", n%3), nil))
			_ = s.Endpoints("", nil)
		}
	}()
	wg.Wait()
	ep, _ := s.Endpoint("e1")
	if v, _ := ep.Attribute("m"); v != writes-1 {
		t.Fatalf("final attribute %v, want %d", v, writes-1)
	}
	total := 0
	for _, p := range []PoolID{"p0", "p1", "p2"} {
		total += len(s.Endpoints(p, nil))
	}
	if total != 1 {
		t.Fatalf("endpoint listed under %d pools", total)
	}
}

func TestSynced(t *testing.T) {
	s := NewStore()
	if s.Synced() {
		t.Fatal("synced before mark")
	}
	s.MarkSynced()
	s.MarkSynced()
	if !s.Synced() {
		t.Fatal("not synced after mark")
	}
}
