package flowcontrol

import (
	"maps"
	"sync"
	"time"

	"github.com/nickheyer/nebu/pkg/datalayer"
	"github.com/nickheyer/nebu/pkg/latency"
)

type fakeEndpoint struct {
	id    datalayer.EndpointID
	pool  datalayer.PoolID
	ready bool
	attrs map[string]any
}

func (e *fakeEndpoint) ID() datalayer.EndpointID { return e.id }
func (e *fakeEndpoint) Pool() datalayer.PoolID   { return e.pool }
func (e *fakeEndpoint) Address() datalayer.Address {
	return datalayer.Address{Host: "127.0.0.1", Port: 8000}
}
func (e *fakeEndpoint) Labels() map[string]string { return nil }
func (e *fakeEndpoint) Ready() bool               { return e.ready }

func (e *fakeEndpoint) Attribute(c string) (any, bool) {
	v, ok := e.attrs[c]
	return v, ok
}

func endpoint(id string, ready bool, attrs map[string]any) *fakeEndpoint {
	if attrs == nil {
		attrs = map[string]any{}
	}
	return &fakeEndpoint{id: datalayer.EndpointID(id), pool: testPool, ready: ready, attrs: attrs}
}

// Serves endpoint snapshots, every other store method panicking through the nil embedded interface
type fakeStore struct {
	datalayer.Store
	mu  sync.Mutex
	eps []*fakeEndpoint
}

func (s *fakeStore) Endpoints(pool datalayer.PoolID, keep func(datalayer.Endpoint) bool) []datalayer.Endpoint {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []datalayer.Endpoint
	for _, e := range s.eps {
		snap := &fakeEndpoint{id: e.id, pool: e.pool, ready: e.ready, attrs: maps.Clone(e.attrs)}
		if (pool == "" || snap.pool == pool) && (keep == nil || keep(snap)) {
			out = append(out, snap)
		}
	}
	return out
}

func (s *fakeStore) set(id datalayer.EndpointID, collector string, value any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.eps {
		if e.id == id {
			e.attrs[collector] = value
		}
	}
}

// Returns configured predictions per endpoint and records the features it was asked about
type fakePredictor struct {
	latency.Predictor
	mu   sync.Mutex
	ttft map[datalayer.EndpointID]latency.Prediction
	tpot map[datalayer.EndpointID]latency.Prediction
	seen map[datalayer.EndpointID]latency.Features
}

func newFakePredictor() *fakePredictor {
	return &fakePredictor{
		ttft: map[datalayer.EndpointID]latency.Prediction{},
		tpot: map[datalayer.EndpointID]latency.Prediction{},
		seen: map[datalayer.EndpointID]latency.Features{},
	}
}

func (p *fakePredictor) PredictTTFT(ep datalayer.EndpointID, f latency.Features) latency.Prediction {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.seen[ep] = f
	return p.ttft[ep]
}

func (p *fakePredictor) PredictTPOT(ep datalayer.EndpointID, f latency.Features) latency.Prediction {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.seen[ep] = f
	return p.tpot[ep]
}

func (p *fakePredictor) setTTFT(ep datalayer.EndpointID, mean, std time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ttft[ep] = latency.Prediction{Mean: mean, StdDev: std, Samples: 100, Confident: true}
}

func (p *fakePredictor) setTPOT(ep datalayer.EndpointID, mean, std time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.tpot[ep] = latency.Prediction{Mean: mean, StdDev: std, Samples: 100, Confident: true}
}

// Sets an unlearned TTFT prediction whose prior mean alone decides, its spread the prior's ignorance
func (p *fakePredictor) setPriorTTFT(ep datalayer.EndpointID, mean time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ttft[ep] = latency.Prediction{Mean: mean, StdDev: time.Hour}
}

func (p *fakePredictor) features(ep datalayer.EndpointID) latency.Features {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.seen[ep]
}

// Saturates a pool at a fixed count of dispatched requests and judges objectives from predictions
type staticDetector struct {
	objectives
	limit int

	mu       sync.Mutex
	inflight map[datalayer.PoolID]int
}

func newStaticDetector(limit int, store datalayer.Store, predictor latency.Predictor, staleness time.Duration) (*staticDetector, error) {
	o, err := newObjectives(store, predictor, staleness)
	if err != nil {
		return nil, err
	}
	return &staticDetector{objectives: o, limit: limit, inflight: map[datalayer.PoolID]int{}}, nil
}

func (d *staticDetector) HardSaturated(pool datalayer.PoolID, _ *Item) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.inflight[pool] >= d.limit
}

func (d *staticDetector) Dispatched(pool datalayer.PoolID, _ *Item) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.inflight[pool]++
}

func (d *staticDetector) Released(pool datalayer.PoolID, _ *Item) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.inflight[pool]--; d.inflight[pool] <= 0 {
		delete(d.inflight, pool)
	}
}

type admission struct {
	pool    datalayer.PoolID
	band    int
	waited  time.Duration
	outcome string
}

type recorder struct {
	mu     sync.Mutex
	events []admission
}

func (r *recorder) OnAdmit(pool datalayer.PoolID, band int, waited time.Duration, outcome string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, admission{pool, band, waited, outcome})
}

func (r *recorder) count(outcome string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, e := range r.events {
		if e.outcome == outcome {
			n++
		}
	}
	return n
}

func (r *recorder) last(outcome string) (admission, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := len(r.events) - 1; i >= 0; i-- {
		if r.events[i].outcome == outcome {
			return r.events[i], true
		}
	}
	return admission{}, false
}
