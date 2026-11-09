// Package schedtest holds fakes of endpoints, stores, indexes, predictors and clocks for scheduling tests
package schedtest

import (
	"hash/fnv"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/nickheyer/nebu/pkg/datalayer"
	"github.com/nickheyer/nebu/pkg/latency"
	"github.com/nickheyer/nebu/pkg/prefixcache"
	"github.com/nickheyer/nebu/pkg/scheduling/framework"
)

// An endpoint whose attributes are fixed at construction
type Endpoint struct {
	id     datalayer.EndpointID
	pool   datalayer.PoolID
	addr   datalayer.Address
	labels map[string]string
	ready  bool
	attrs  map[string]any
}

// A ready endpoint at host id port 8000 with no labels or attributes
func EP(id string) *Endpoint {
	return &Endpoint{
		id:     datalayer.EndpointID(id),
		pool:   "pool",
		addr:   datalayer.Address{Host: id, Port: 8000},
		labels: map[string]string{},
		ready:  true,
		attrs:  map[string]any{},
	}
}

// Stores an attribute under a collector name
func (e *Endpoint) With(collector string, v any) *Endpoint { e.attrs[collector] = v; return e }

// Stores scraped metrics under the metrics collector
func (e *Endpoint) Metrics(m datalayer.ServerMetrics) *Endpoint {
	return e.With(datalayer.CollectorMetrics, &m)
}

// Sets one label
func (e *Endpoint) Label(k, v string) *Endpoint { e.labels[k] = v; return e }

// Moves the endpoint to another address
func (e *Endpoint) At(host string, port int) *Endpoint {
	e.addr = datalayer.Address{Host: host, Port: port}
	return e
}

// Marks the endpoint as not ready
func (e *Endpoint) NotReady() *Endpoint { e.ready = false; return e }

func (e *Endpoint) ID() datalayer.EndpointID       { return e.id }
func (e *Endpoint) Pool() datalayer.PoolID         { return e.pool }
func (e *Endpoint) Address() datalayer.Address     { return e.addr }
func (e *Endpoint) Labels() map[string]string      { return e.labels }
func (e *Endpoint) Ready() bool                    { return e.ready }
func (e *Endpoint) Attribute(c string) (any, bool) { v, ok := e.attrs[c]; return v, ok }

// The ids of endpoints in order
func IDs(eps []datalayer.Endpoint) []string {
	out := make([]string, len(eps))
	for i, ep := range eps {
		out[i] = string(ep.ID())
	}
	return out
}

// Endpoints as the interface slice the framework takes
func Eps(eps ...*Endpoint) []datalayer.Endpoint {
	out := make([]datalayer.Endpoint, len(eps))
	for i, ep := range eps {
		out[i] = ep
	}
	return out
}

// A store that records attribute writes, every other method panicking through the nil embedded interface
type Store struct {
	datalayer.Store
	mu    sync.Mutex
	attrs map[datalayer.EndpointID]map[string]any
}

func NewStore() *Store { return &Store{attrs: map[datalayer.EndpointID]map[string]any{}} }

func (s *Store) SetAttribute(id datalayer.EndpointID, collector string, v any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.attrs[id] == nil {
		s.attrs[id] = map[string]any{}
	}
	s.attrs[id][collector] = v
}

// The last value written for an endpoint and collector
func (s *Store) Get(id datalayer.EndpointID, collector string) (any, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.attrs[id][collector]
	return v, ok
}

// An exact prefix index chunking text by a fixed character count
type Indexer struct {
	Chunk  int
	mu     sync.Mutex
	held   map[datalayer.EndpointID]map[prefixcache.BlockHash]bool
	hashes int
}

func NewIndexer(chunk int) *Indexer {
	return &Indexer{Chunk: chunk, held: map[datalayer.EndpointID]map[prefixcache.BlockHash]bool{}}
}

func (x *Indexer) Hash(model, adapter, prompt string, tokenIDs []int) []prefixcache.BlockHash {
	x.mu.Lock()
	x.hashes++
	x.mu.Unlock()
	h := fnv.New64a()
	h.Write([]byte(model + "\x00" + adapter + "\x00"))
	var out []prefixcache.BlockHash
	for i := 0; i+x.Chunk <= len(prompt); i += x.Chunk {
		h.Write([]byte(prompt[i : i+x.Chunk]))
		out = append(out, prefixcache.BlockHash(h.Sum64()))
	}
	return out
}

// How many times the prompt was hashed
func (x *Indexer) HashCalls() int {
	x.mu.Lock()
	defer x.mu.Unlock()
	return x.hashes
}

func (x *Indexer) Match(hashes []prefixcache.BlockHash, candidates []datalayer.EndpointID) []prefixcache.Match {
	x.mu.Lock()
	defer x.mu.Unlock()
	var out []prefixcache.Match
	for _, id := range candidates {
		n := 0
		for _, h := range hashes {
			if !x.held[id][h] {
				break
			}
			n++
		}
		if n > 0 {
			out = append(out, prefixcache.Match{Endpoint: id, Blocks: n})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Blocks > out[j].Blocks })
	return out
}

func (x *Indexer) Record(ep datalayer.EndpointID, hashes []prefixcache.BlockHash) {
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.held[ep] == nil {
		x.held[ep] = map[prefixcache.BlockHash]bool{}
	}
	for _, h := range hashes {
		x.held[ep][h] = true
	}
}

func (x *Indexer) Unrecord(ep datalayer.EndpointID, hashes []prefixcache.BlockHash) {
	x.mu.Lock()
	defer x.mu.Unlock()
	for _, h := range hashes {
		delete(x.held[ep], h)
	}
}

// The endpoints holding every given hash, sorted
func (x *Indexer) Holders(hashes []prefixcache.BlockHash) []string {
	x.mu.Lock()
	defer x.mu.Unlock()
	var out []string
	for id, held := range x.held {
		all := len(hashes) > 0
		for _, h := range hashes {
			all = all && held[h]
		}
		if all {
			out = append(out, string(id))
		}
	}
	slices.Sort(out)
	return out
}

func (x *Indexer) SetCapacity(datalayer.EndpointID, int)                     {}
func (x *Indexer) Report(ep datalayer.EndpointID, h []prefixcache.BlockHash) { x.Record(ep, h) }
func (x *Indexer) Forget(ep datalayer.EndpointID) {
	x.mu.Lock()
	defer x.mu.Unlock()
	delete(x.held, ep)
}
func (x *Indexer) Stats() prefixcache.Stats {
	x.mu.Lock()
	defer x.mu.Unlock()
	st := prefixcache.Stats{Endpoints: len(x.held)}
	distinct := map[prefixcache.BlockHash]bool{}
	for _, held := range x.held {
		st.Entries += len(held)
		for h := range held {
			distinct[h] = true
		}
	}
	st.Hashes = len(distinct)
	return st
}

func (x *Indexer) Len() int {
	x.mu.Lock()
	defer x.mu.Unlock()
	n := 0
	for _, held := range x.held {
		n += len(held)
	}
	return n
}

// One Observe call
type Observation struct {
	Endpoint   datalayer.EndpointID
	Features   latency.Features
	TTFT, TPOT time.Duration
}

// A predictor answering fixed means and deviations per endpoint and recording observations
type Predictor struct {
	TTFT, TPOT map[datalayer.EndpointID]time.Duration
	// Deviation of every prediction
	StdDev time.Duration
	// Whether every prediction counts as learned rather than prior based
	Confident bool
	mu        sync.Mutex
	seen      []Observation
	asked     []latency.Features
}

func (p *Predictor) PredictTTFT(ep datalayer.EndpointID, f latency.Features) latency.Prediction {
	p.mu.Lock()
	p.asked = append(p.asked, f)
	p.mu.Unlock()
	return latency.Prediction{Mean: p.TTFT[ep], StdDev: p.StdDev, Confident: p.Confident}
}

func (p *Predictor) PredictTPOT(ep datalayer.EndpointID, f latency.Features) latency.Prediction {
	return latency.Prediction{Mean: p.TPOT[ep], StdDev: p.StdDev, Confident: p.Confident}
}

func (p *Predictor) Observe(ep datalayer.EndpointID, f latency.Features, ttft, tpot time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.seen = append(p.seen, Observation{Endpoint: ep, Features: f, TTFT: ttft, TPOT: tpot})
}

func (p *Predictor) Forget(datalayer.EndpointID) {}

// Every observation so far
func (p *Predictor) Observed() []Observation {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.seen)
}

// Every feature set a TTFT prediction was asked for
func (p *Predictor) Asked() []latency.Features {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.asked)
}

// A clock moved only by Advance
type Clock struct {
	mu  sync.Mutex
	now time.Time
}

func NewClock() *Clock { return &Clock{now: time.Unix(1_700_000_000, 0)} }

func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *Clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// A single profile result of the given endpoints
func Result(profile string, eps ...datalayer.Endpoint) *framework.Result {
	return &framework.Result{
		ProfileResults: map[string]*framework.ProfileRunResult{profile: {Endpoints: eps}},
		PrimaryProfile: profile,
		State:          framework.NewCycleState(),
	}
}
