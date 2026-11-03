package flowcontrol

import (
	"testing"
	"time"

	"github.com/nickheyer/nebu/pkg/datalayer"
	"github.com/nickheyer/nebu/pkg/latency"
)

var testThresholds = Thresholds{QueueDepth: 5, KVUtilization: 0.9}

const testStaleness = time.Second

func fresh(queued int, kv float64) *datalayer.ServerMetrics {
	return &datalayer.ServerMetrics{QueuedRequests: queued, RunningRequests: 2, KVCacheUtilization: kv, UpdatedAt: time.Now()}
}

func stale(queued int, kv float64) *datalayer.ServerMetrics {
	m := fresh(queued, kv)
	m.UpdatedAt = time.Now().Add(-10 * testStaleness)
	return m
}

func newMetricsDetector(t *testing.T, eps ...*fakeEndpoint) (*MetricsDetector, *fakeStore, *fakePredictor) {
	t.Helper()
	store := &fakeStore{eps: eps}
	pred := newFakePredictor()
	d, err := NewMetricsDetector(store, pred, testStaleness, testThresholds)
	if err != nil {
		t.Fatal(err)
	}
	return d, store, pred
}

func TestDetectorConstructorsRejectInvalidInput(t *testing.T) {
	store, pred := &fakeStore{}, newFakePredictor()
	cases := []struct {
		name  string
		build func() error
	}{
		{"metrics nil store", func() error { _, err := NewMetricsDetector(nil, pred, time.Second, testThresholds); return err }},
		{"metrics nil predictor", func() error { _, err := NewMetricsDetector(store, nil, time.Second, testThresholds); return err }},
		{"metrics zero staleness", func() error { _, err := NewMetricsDetector(store, pred, 0, testThresholds); return err }},
		{"metrics zero queue depth", func() error {
			_, err := NewMetricsDetector(store, pred, time.Second, Thresholds{QueueDepth: 0, KVUtilization: 0.9})
			return err
		}},
		{"metrics KV above one", func() error {
			_, err := NewMetricsDetector(store, pred, time.Second, Thresholds{QueueDepth: 5, KVUtilization: 1.5})
			return err
		}},
		{"metrics zero KV", func() error {
			_, err := NewMetricsDetector(store, pred, time.Second, Thresholds{QueueDepth: 5})
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.build() == nil {
				t.Fatal("want an error")
			}
		})
	}
}

func TestMetricsDetectorHardSaturated(t *testing.T) {
	unhealthy := &datalayer.HealthState{Healthy: false}
	cases := []struct {
		name string
		eps  []*fakeEndpoint
		want bool
	}{
		{"fresh endpoint below both thresholds", []*fakeEndpoint{
			endpoint("a", true, map[string]any{datalayer.CollectorMetrics: fresh(5, 0.95)}),
			endpoint("b", true, map[string]any{datalayer.CollectorMetrics: fresh(2, 0.5)}),
		}, false},
		{"every fresh endpoint at queue threshold", []*fakeEndpoint{
			endpoint("a", true, map[string]any{datalayer.CollectorMetrics: fresh(5, 0.1)}),
			endpoint("b", true, map[string]any{datalayer.CollectorMetrics: fresh(9, 0.1)}),
		}, true},
		{"every fresh endpoint at KV threshold", []*fakeEndpoint{
			endpoint("a", true, map[string]any{datalayer.CollectorMetrics: fresh(0, 0.9)}),
			endpoint("b", true, map[string]any{datalayer.CollectorMetrics: fresh(0, 0.99)}),
		}, true},
		{"assumed load leaves the room fresh metrics report", []*fakeEndpoint{
			endpoint("a", true, map[string]any{datalayer.CollectorMetrics: fresh(3, 0.1), datalayer.CollectorAssumed: 200}),
		}, false},
		{"fresh metrics with known capacity add the free running slots", []*fakeEndpoint{
			endpoint("a", true, map[string]any{datalayer.CollectorMetrics: fresh(5, 0.1), CollectorCapacity: 3}),
		}, false},
		{"fresh metrics with running at capacity and the queue full", []*fakeEndpoint{
			endpoint("a", true, map[string]any{datalayer.CollectorMetrics: fresh(5, 0.1), CollectorCapacity: 2}),
		}, true},
		{"stale endpoint capacity room counts beside a saturated fresh one", []*fakeEndpoint{
			endpoint("a", true, map[string]any{datalayer.CollectorMetrics: fresh(5, 0.1)}),
			endpoint("b", true, map[string]any{datalayer.CollectorMetrics: stale(0, 0.1), CollectorCapacity: 10}),
		}, false},
		{"stale endpoint at capacity beside a saturated fresh one", []*fakeEndpoint{
			endpoint("a", true, map[string]any{datalayer.CollectorMetrics: fresh(5, 0.1)}),
			endpoint("b", true, map[string]any{datalayer.CollectorMetrics: stale(0, 0.1), datalayer.CollectorAssumed: 10, CollectorCapacity: 10}),
		}, true},
		{"not ready endpoint with room ignored", []*fakeEndpoint{
			endpoint("a", true, map[string]any{datalayer.CollectorMetrics: fresh(5, 0.1)}),
			endpoint("b", false, map[string]any{datalayer.CollectorMetrics: fresh(0, 0.1)}),
		}, true},
		{"unhealthy endpoint with room ignored", []*fakeEndpoint{
			endpoint("a", true, map[string]any{datalayer.CollectorMetrics: fresh(5, 0.1)}),
			endpoint("b", true, map[string]any{datalayer.CollectorMetrics: fresh(0, 0.1), datalayer.CollectorHealth: unhealthy}),
		}, true},
		{"no fresh metrics and assumed below capacity", []*fakeEndpoint{
			endpoint("a", true, map[string]any{datalayer.CollectorMetrics: stale(0, 0.1), datalayer.CollectorAssumed: 4, CollectorCapacity: 4}),
			endpoint("b", true, map[string]any{datalayer.CollectorAssumed: 3, CollectorCapacity: 4}),
		}, false},
		{"no fresh metrics and every endpoint at capacity", []*fakeEndpoint{
			endpoint("a", true, map[string]any{datalayer.CollectorMetrics: stale(0, 0.1), datalayer.CollectorAssumed: 4, CollectorCapacity: 4}),
			endpoint("b", true, map[string]any{datalayer.CollectorAssumed: 6, CollectorCapacity: 4}),
		}, true},
		{"no fresh metrics and no capacity known leaves the pool open", []*fakeEndpoint{
			endpoint("a", true, map[string]any{datalayer.CollectorAssumed: 0}),
		}, false},
		{"open endpoint beside a saturated fresh one", []*fakeEndpoint{
			endpoint("a", true, map[string]any{datalayer.CollectorMetrics: fresh(5, 0.95)}),
			endpoint("b", true, nil),
		}, false},
		{"not ready endpoint without inputs does not open the pool", []*fakeEndpoint{
			endpoint("a", true, map[string]any{datalayer.CollectorMetrics: fresh(5, 0.95)}),
			endpoint("b", false, nil),
		}, true},
		{"no endpoints", nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, _, _ := newMetricsDetector(t, tc.eps...)
			if got := d.HardSaturated(testPool, &Item{}); got != tc.want {
				t.Fatalf("hard saturated %v, want %v", got, tc.want)
			}
		})
	}
}

func TestMetricsDetectorCountsDispatchesInputsHaveNotSeen(t *testing.T) {
	cases := []struct {
		name    string
		attrs   map[string]any
		refresh func(*fakeStore)
	}{
		{"fresh metrics", map[string]any{datalayer.CollectorMetrics: fresh(3, 0.1)},
			func(s *fakeStore) { s.set("a", datalayer.CollectorMetrics, fresh(4, 0.1)) }},
		{"assumed load against capacity", map[string]any{datalayer.CollectorAssumed: 3, CollectorCapacity: 5},
			func(s *fakeStore) { s.set("a", datalayer.CollectorAssumed, 4) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, store, _ := newMetricsDetector(t, endpoint("a", true, tc.attrs))
			one, two, three := &Item{ID: "1"}, &Item{ID: "2"}, &Item{ID: "3"}
			steps := []struct {
				do   func()
				want bool
			}{
				{func() {}, false},
				{func() { d.Dispatched(testPool, one) }, false},
				{func() { d.Dispatched(testPool, two) }, true},
				{func() { d.Released(testPool, one) }, false},
				{func() { tc.refresh(store) }, false},
				{func() { d.Dispatched(testPool, three) }, true},
				{func() { d.Released(testPool, three) }, false},
			}
			for i, s := range steps {
				s.do()
				if got := d.HardSaturated(testPool, &Item{}); got != s.want {
					t.Fatalf("step %d hard saturated %v, want %v", i, got, s.want)
				}
			}
		})
	}
}

func TestSoftSaturated(t *testing.T) {
	ms := time.Millisecond
	cases := []struct {
		name string
		eps  []*fakeEndpoint
		ttft map[datalayer.EndpointID]latency.Prediction
		tpot map[datalayer.EndpointID]latency.Prediction
		item Item
		want bool
	}{
		{"no objective ignores slow predictions",
			[]*fakeEndpoint{endpoint("a", true, nil)},
			map[datalayer.EndpointID]latency.Prediction{"a": {Mean: time.Hour}}, nil,
			Item{}, false},
		{"TTFT met on one endpoint",
			[]*fakeEndpoint{endpoint("a", true, nil), endpoint("b", true, nil)},
			map[datalayer.EndpointID]latency.Prediction{"a": {Mean: 300 * ms}, "b": {Mean: 80 * ms, StdDev: 20 * ms}}, nil,
			Item{TTFT: 100 * ms}, false},
		{"TTFT mean met but mean plus deviation missed everywhere",
			[]*fakeEndpoint{endpoint("a", true, nil), endpoint("b", true, nil)},
			map[datalayer.EndpointID]latency.Prediction{"a": {Mean: 90 * ms, StdDev: 20 * ms}, "b": {Mean: 95 * ms, StdDev: 6 * ms}}, nil,
			Item{TTFT: 100 * ms}, true},
		{"TPOT missed everywhere",
			[]*fakeEndpoint{endpoint("a", true, nil)},
			nil, map[datalayer.EndpointID]latency.Prediction{"a": {Mean: 30 * ms}},
			Item{TPOT: 20 * ms}, true},
		{"TTFT met and TPOT missed on the same endpoint",
			[]*fakeEndpoint{endpoint("a", true, nil)},
			map[datalayer.EndpointID]latency.Prediction{"a": {Mean: 10 * ms}}, map[datalayer.EndpointID]latency.Prediction{"a": {Mean: 30 * ms}},
			Item{TTFT: 100 * ms, TPOT: 20 * ms}, true},
		{"both objectives met",
			[]*fakeEndpoint{endpoint("a", true, nil)},
			map[datalayer.EndpointID]latency.Prediction{"a": {Mean: 10 * ms}}, map[datalayer.EndpointID]latency.Prediction{"a": {Mean: 20 * ms}},
			Item{TTFT: 100 * ms, TPOT: 20 * ms}, false},
		{"fast endpoint that is not ready ignored",
			[]*fakeEndpoint{endpoint("a", true, nil), endpoint("b", false, nil)},
			map[datalayer.EndpointID]latency.Prediction{"a": {Mean: 300 * ms}, "b": {Mean: ms}}, nil,
			Item{TTFT: 100 * ms}, true},
		{"objective with no ready endpoint", nil, nil, nil, Item{TTFT: 100 * ms}, true},
		{"unlearned prediction missed by its prior mean despite a learned slow endpoint",
			[]*fakeEndpoint{endpoint("a", true, nil), endpoint("b", true, nil)},
			map[datalayer.EndpointID]latency.Prediction{"a": {Mean: 300 * ms, Confident: true}, "b": {Mean: 150 * ms}}, nil,
			Item{TTFT: 100 * ms}, true},
		{"unlearned prediction met by its prior mean whatever its spread",
			[]*fakeEndpoint{endpoint("a", true, nil)},
			map[datalayer.EndpointID]latency.Prediction{"a": {Mean: 50 * ms}}, nil,
			Item{TTFT: 100 * ms}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, _, pred := newMetricsDetector(t, tc.eps...)
			for ep, p := range tc.ttft {
				if p.Confident || p.StdDev > 0 {
					pred.setTTFT(ep, p.Mean, p.StdDev)
				} else {
					pred.setPriorTTFT(ep, p.Mean)
				}
			}
			for ep, p := range tc.tpot {
				pred.setTPOT(ep, p.Mean, p.StdDev)
			}
			item := tc.item
			if got := d.SoftSaturated(testPool, &item); got != tc.want {
				t.Fatalf("soft saturated %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSoftSaturatedFeatures(t *testing.T) {
	cases := []struct {
		name  string
		attrs map[string]any
		want  latency.Features
	}{
		{"fresh metrics and assumed load",
			map[string]any{datalayer.CollectorMetrics: fresh(3, 0.4), datalayer.CollectorAssumed: 2},
			latency.Features{PromptTokens: 700, QueuedRequests: 3, RunningRequests: 2, KVCacheUtilization: 0.4, AssumedRequests: 2}},
		{"stale metrics leave only assumed load",
			map[string]any{datalayer.CollectorMetrics: stale(3, 0.4), datalayer.CollectorAssumed: 5},
			latency.Features{PromptTokens: 700, AssumedRequests: 5}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, _, pred := newMetricsDetector(t, endpoint("a", true, tc.attrs))
			d.SoftSaturated(testPool, &Item{TTFT: time.Second, PromptTokens: 700})
			if got := pred.features("a"); got != tc.want {
				t.Fatalf("features %+v, want %+v", got, tc.want)
			}
		})
	}
}
