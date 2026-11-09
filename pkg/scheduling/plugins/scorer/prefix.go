package scorer

import (
	"container/list"
	"context"
	"maps"
	"math"
	"sync"
	"time"

	"github.com/nickheyer/nebu/pkg/datalayer"
	"github.com/nickheyer/nebu/pkg/prefixcache"
	"github.com/nickheyer/nebu/pkg/scheduling/framework"
	"github.com/nickheyer/nebu/pkg/scheduling/plugins"
	"github.com/nickheyer/nebu/pkg/scheduling/registry"
)

// Load factor of the ring's bounded load when none is configured
const DefaultRingLoadFactor = 1.25

// Scores the share of prompt blocks an endpoint holds, the ring's endpoint when none holds any, and pins prompts to endpoints that answered
type PrefixCacheScorer struct {
	plugins.Named
	idx        prefixcache.Indexer
	ring       prefixcache.Ring
	loadFactor float64
	clock      registry.Clock
	ttl        time.Duration

	mu      sync.Mutex
	pending map[string]*pin
	order   *list.List
}

// The endpoints one request's prompt was recorded on at pick, kept until the request completes or the ttl passes
type pin struct {
	id    string
	at    time.Time
	heads []datalayer.EndpointID
	elem  *list.Element
}

// A scorer falling back to ring with bounded load factor, forgetting pins of requests that never complete after ttl
func NewPrefixCacheScorer(idx prefixcache.Indexer, ring prefixcache.Ring, loadFactor float64, clock registry.Clock, ttl time.Duration) *PrefixCacheScorer {
	if loadFactor <= 1 {
		loadFactor = DefaultRingLoadFactor
	}
	return &PrefixCacheScorer{
		Named:      plugins.NewNamed(PrefixCacheScorerType),
		idx:        idx,
		ring:       ring,
		loadFactor: loadFactor,
		clock:      clock,
		ttl:        ttl,
		pending:    map[string]*pin{},
		order:      list.New(),
	}
}

// Hashes the prompt once per cycle into the cycle state and scores matched over total blocks, the ring's pick one when no block matches
func (s *PrefixCacheScorer) Score(_ context.Context, state *framework.CycleState, req *framework.Request, eps []datalayer.Endpoint) map[datalayer.EndpointID]float64 {
	hashes := plugins.PromptHashes(state, s.idx, req)
	out := plugins.PrefixFractions(s.idx, hashes, eps)
	state.Write(plugins.PrefixFractionsKey, maps.Clone(out))
	if len(hashes) == 0 || s.ring == nil {
		return out
	}
	for _, v := range out {
		if v > 0 {
			return out
		}
	}
	ids := make([]datalayer.EndpointID, len(eps))
	total := 0
	loads := make(map[datalayer.EndpointID]int, len(eps))
	for i, ep := range eps {
		ids[i] = ep.ID()
		loads[ids[i]], _ = datalayer.Attribute[int](ep, datalayer.CollectorAssumed)
		total += loads[ids[i]]
	}
	maxLoad := int(math.Floor(float64(total)/float64(len(eps))*s.loadFactor)) + 1
	if id, ok := s.ring.Pick(hashes[0], ids, func(id datalayer.EndpointID) int { return loads[id] }, maxLoad); ok {
		out[id] = 1
	}
	return out
}

// Records the cycle's prompt hashes on the first endpoint of every profile the result routes to and remembers them
func (s *PrefixCacheScorer) PreRequest(_ context.Context, req *framework.Request, res *framework.Result) {
	heads := plugins.ResultHeads(res)
	if len(heads) == 0 {
		return
	}
	hashes := plugins.PromptHashes(res.State, s.idx, req)
	if len(hashes) == 0 {
		return
	}
	ids := make([]datalayer.EndpointID, len(heads))
	for i, ep := range heads {
		ids[i] = ep.ID()
		s.idx.Record(ep.ID(), hashes)
	}
	s.remember(req.ID, ids)
}

// Unpins the prompt from the endpoint that answered with an error or that the request fell back from, pinning the one that served
func (s *PrefixCacheScorer) ResponseComplete(_ context.Context, req *framework.Request, resp *framework.Response) {
	heads := s.forget(req.ID)
	var served datalayer.EndpointID
	if resp != nil && resp.Endpoint != nil {
		served = resp.Endpoint.ID()
	}
	failed := resp == nil || resp.Err != nil || resp.Status >= 400
	if len(heads) == 0 && (failed || served == "") {
		return
	}
	hashes := s.idx.Hash(req.TargetModel, req.Adapter, req.Prompt, req.PromptTokenIDs)
	if len(hashes) == 0 {
		return
	}
	switch {
	case failed && served == "":
		for _, id := range heads {
			s.idx.Unrecord(id, hashes)
		}
	case failed:
		s.idx.Unrecord(served, hashes)
		if len(heads) > 0 && heads[0] != served {
			s.idx.Unrecord(heads[0], hashes)
		}
	case len(heads) == 0 || !containsID(heads, served):
		s.idx.Record(served, hashes)
		if len(heads) > 0 {
			s.idx.Unrecord(heads[0], hashes)
		}
	}
}

// Requests whose pins are held, for tests and debugging
func (s *PrefixCacheScorer) Pending() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.pending)
}

// Stores the heads of a request, dropping pins of requests older than the ttl
func (s *PrefixCacheScorer) remember(id string, heads []datalayer.EndpointID) {
	now := s.clock.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for e := s.order.Front(); e != nil; e = s.order.Front() {
		p := e.Value.(*pin)
		if now.Sub(p.at) <= s.ttl {
			break
		}
		s.drop(p)
	}
	if p, ok := s.pending[id]; ok {
		s.drop(p)
	}
	p := &pin{id: id, at: now, heads: heads}
	p.elem = s.order.PushBack(p)
	s.pending[id] = p
}

// Takes the heads of a request out of the pending set
func (s *PrefixCacheScorer) forget(id string) []datalayer.EndpointID {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.pending[id]
	if !ok {
		return nil
	}
	s.drop(p)
	return p.heads
}

func (s *PrefixCacheScorer) drop(p *pin) {
	s.order.Remove(p.elem)
	delete(s.pending, p.id)
}

func containsID(ids []datalayer.EndpointID, id datalayer.EndpointID) bool {
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}
