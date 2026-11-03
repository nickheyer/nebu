package scorer

import (
	"container/list"
	"context"
	"sync"
	"time"

	"github.com/nickheyer/nebu/pkg/datalayer"
	"github.com/nickheyer/nebu/pkg/latency"
	"github.com/nickheyer/nebu/pkg/prefixcache"
	"github.com/nickheyer/nebu/pkg/scheduling/framework"
	"github.com/nickheyer/nebu/pkg/scheduling/plugins"
	"github.com/nickheyer/nebu/pkg/scheduling/registry"
)

// Scores predicted TTFT and TPOT against the request objective and teaches the predictor each completed request
type LatencyScorer struct {
	plugins.Named
	pred  latency.Predictor
	idx   prefixcache.Indexer
	clock registry.Clock
	ttl   time.Duration

	mu      sync.Mutex
	pending map[string]*dispatch
	order   *list.List
}

// The features read for one request's endpoints, kept until the request completes or the ttl passes
type dispatch struct {
	id       string
	at       time.Time
	features map[datalayer.EndpointID]latency.Features
	elem     *list.Element
}

// A scorer forgetting features of requests that never complete after ttl
func NewLatencyScorer(pred latency.Predictor, idx prefixcache.Indexer, clock registry.Clock, ttl time.Duration) *LatencyScorer {
	return &LatencyScorer{
		Named:   plugins.NewNamed(LatencyScorerType),
		pred:    pred,
		idx:     idx,
		clock:   clock,
		ttl:     ttl,
		pending: map[string]*dispatch{},
		order:   list.New(),
	}
}

// Scores one inside both objectives falling linearly to zero at twice an objective, and without objectives the lowest predicted total time one
func (s *LatencyScorer) Score(_ context.Context, state *framework.CycleState, req *framework.Request, eps []datalayer.Endpoint) map[datalayer.EndpointID]float64 {
	feats := s.features(state, req, eps)
	defer s.remember(req.ID, feats, false)
	if req.Objective.TTFT <= 0 && req.Objective.TPOT <= 0 {
		decode := time.Duration(max(req.ExpectedOutputTokens-1, 0))
		totals := make(map[datalayer.EndpointID]float64, len(eps))
		for _, ep := range eps {
			id := ep.ID()
			totals[id] = float64(s.pred.PredictTTFT(id, feats[id]).Mean + s.pred.PredictTPOT(id, feats[id]).Mean*decode)
		}
		return lowerIsBetter(eps, totals)
	}
	out := make(map[datalayer.EndpointID]float64, len(eps))
	for _, ep := range eps {
		id := ep.ID()
		ttft, tpot := 1.0, 1.0
		if req.Objective.TTFT > 0 {
			ttft = within(s.pred.PredictTTFT(id, feats[id]).Mean, req.Objective.TTFT)
		}
		if req.Objective.TPOT > 0 {
			tpot = within(s.pred.PredictTPOT(id, feats[id]).Mean, req.Objective.TPOT)
		}
		out[id] = min(ttft, tpot)
	}
	return out
}

// Keeps the features of the endpoints the result routes to, reading at dispatch any that no profile scored
func (s *LatencyScorer) PreRequest(_ context.Context, req *framework.Request, res *framework.Result) {
	routed := plugins.ResultEndpoints(res)
	keep := make(map[datalayer.EndpointID]latency.Features, len(routed))
	var unscored []datalayer.Endpoint
	s.mu.Lock()
	d := s.pending[req.ID]
	for _, ep := range routed {
		if f, ok := d.lookup(ep.ID()); ok {
			keep[ep.ID()] = f
		} else {
			unscored = append(unscored, ep)
		}
	}
	s.mu.Unlock()
	if len(unscored) > 0 {
		var state *framework.CycleState
		if res != nil {
			state = res.State
		}
		for id, f := range s.features(state, req, unscored) {
			keep[id] = f
		}
	}
	s.remember(req.ID, keep, true)
}

// Teaches the predictor the measured latencies with the features read at dispatch, then forgets the request
func (s *LatencyScorer) ResponseComplete(_ context.Context, req *framework.Request, resp *framework.Response) {
	s.mu.Lock()
	d, ok := s.pending[req.ID]
	var f latency.Features
	if ok {
		s.drop(d)
		if resp != nil && resp.Endpoint != nil {
			f, ok = d.features[resp.Endpoint.ID()]
		}
	}
	s.mu.Unlock()
	if !ok || resp == nil || resp.Endpoint == nil || resp.Err != nil || resp.TTFT() <= 0 {
		return
	}
	s.pred.Observe(resp.Endpoint.ID(), f, resp.TTFT(), resp.TPOT())
}

// Requests whose features are held, for tests and debugging
func (s *LatencyScorer) Pending() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.pending)
}

func (s *LatencyScorer) features(state *framework.CycleState, req *framework.Request, eps []datalayer.Endpoint) map[datalayer.EndpointID]latency.Features {
	return plugins.LatencyFeatures(s.idx, state, req, eps)
}

// Stores features for a request, merging with what an earlier profile of the cycle stored unless replace is set
func (s *LatencyScorer) remember(id string, feats map[datalayer.EndpointID]latency.Features, replace bool) {
	now := s.clock.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for e := s.order.Front(); e != nil; e = s.order.Front() {
		d := e.Value.(*dispatch)
		if now.Sub(d.at) <= s.ttl {
			break
		}
		s.drop(d)
	}
	if d, ok := s.pending[id]; ok && !replace {
		for k, v := range feats {
			d.features[k] = v
		}
		return
	} else if ok {
		s.drop(d)
	}
	d := &dispatch{id: id, at: now, features: feats}
	d.elem = s.order.PushBack(d)
	s.pending[id] = d
}

func (d *dispatch) lookup(id datalayer.EndpointID) (latency.Features, bool) {
	if d == nil {
		return latency.Features{}, false
	}
	f, ok := d.features[id]
	return f, ok
}

func (s *LatencyScorer) drop(d *dispatch) {
	s.order.Remove(d.elem)
	delete(s.pending, d.id)
}

// One at or under the objective, zero at or over twice it, linear between
func within(pred, objective time.Duration) float64 {
	switch {
	case pred <= objective:
		return 1
	case pred >= 2*objective:
		return 0
	default:
		return 1 - float64(pred-objective)/float64(objective)
	}
}
