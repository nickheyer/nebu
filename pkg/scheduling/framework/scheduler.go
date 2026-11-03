package framework

import (
	"context"
	"fmt"
	"sort"

	"github.com/nickheyer/nebu/pkg/datalayer"
)

// The reference scheduler, running profiles the handler picks until it picks none
type scheduler struct {
	handler  ProfileHandler
	profiles map[string]*Profile
}

// Builds a scheduler from one handler and its profiles, refusing a profile without a picker
func NewScheduler(handler ProfileHandler, profiles map[string]*Profile) (Scheduler, error) {
	if handler == nil {
		return nil, fmt.Errorf("a scheduler needs a profile handler")
	}
	for name, p := range profiles {
		if p.Picker == nil {
			return nil, fmt.Errorf("profile %s: %w", name, ErrNoPicker)
		}
		if p.Name == "" {
			p.Name = name
		}
	}
	return &scheduler{handler: handler, profiles: profiles}, nil
}

func (s *scheduler) Profiles() map[string]*Profile { return s.profiles }

// The profile handler of a scheduler NewScheduler built, false for any other scheduler
func HandlerOf(s Scheduler) (ProfileHandler, bool) {
	if sc, ok := s.(*scheduler); ok {
		return sc.handler, true
	}
	return nil, false
}

// Runs the profiles the handler picks, releasing the reservation of any profile the handler does not route to
func (s *scheduler) Schedule(ctx context.Context, req *Request, candidates []datalayer.Endpoint) (res *Result, err error) {
	state := NewCycleState()
	results := map[string]*ProfileRunResult{}
	failed := map[string]error{}
	defer func() {
		for name, r := range results {
			if err != nil || res.ProfileResults[name] == nil || res.ProfileResults[name].Endpoints[0].ID() != r.Endpoints[0].ID() {
				release(ctx, state, req, s.profiles[name], r.Endpoints[0])
			}
		}
	}()
	for pass := 0; ; pass++ {
		next := s.handler.Pick(ctx, state, req, s.profiles, results)
		if len(next) == 0 {
			if pass == 0 {
				return nil, ErrNoProfile
			}
			break
		}
		for name, p := range next {
			if err, again := failed[name]; again {
				return nil, fmt.Errorf("profile %s picked again after it failed: %w", name, err)
			}
			r, err := RunProfile(ctx, state, req, p, candidates)
			if err != nil {
				if fh, ok := s.handler.(ProfileFailureHandler); ok && fh.ProfileFailed(ctx, state, req, name, err) {
					failed[name] = err
					continue
				}
				return nil, fmt.Errorf("profile %s: %w", name, err)
			}
			results[name] = r
		}
	}
	res, err = s.handler.ProcessResults(ctx, state, req, results)
	if err != nil {
		return nil, err
	}
	if res == nil {
		return nil, fmt.Errorf("%s returned no result: %w", s.handler.TypedName(), ErrNoEndpoints)
	}
	res.State = state
	return res, nil
}

// Gives back the head reservation of one profile run
func release(ctx context.Context, state *CycleState, req *Request, p *Profile, ep datalayer.Endpoint) {
	if p == nil {
		return
	}
	for _, r := range p.Reservers {
		r.Release(ctx, state, req, ep)
	}
}

// Runs one profile's filters, scorers and picker over the candidates, then reserves the first picked endpoint with room
func RunProfile(ctx context.Context, state *CycleState, req *Request, p *Profile, candidates []datalayer.Endpoint) (*ProfileRunResult, error) {
	var pool datalayer.PoolID
	if len(candidates) > 0 {
		pool = candidates[0].Pool()
	}
	eps := candidates
	filtered := make([]FilterOutcome, 0, len(p.Filters))
	for _, f := range p.Filters {
		before := len(eps)
		eps = f.Filter(ctx, state, req, eps)
		filtered = append(filtered, FilterOutcome{Filter: f.TypedName().String(), Kept: idsOf(eps)})
		if p.Observer != nil {
			p.Observer.FilterDropped(pool, p.Name, f.TypedName().String(), before-len(eps))
		}
		if len(eps) == 0 {
			if why := takeRefusal(state); why != nil {
				return nil, fmt.Errorf("%s: %w", f.TypedName(), why)
			}
			return nil, fmt.Errorf("%s: %w", f.TypedName(), ErrNoEndpoints)
		}
	}
	scored := make([]ScoredEndpoint, len(eps))
	for i, ep := range eps {
		scored[i] = ScoredEndpoint{Endpoint: ep}
	}
	total := 0.0
	for _, ws := range p.Scorers {
		total += ws.Weight
	}
	raw := make(map[string]map[datalayer.EndpointID]float64, len(p.Scorers))
	for _, ws := range p.Scorers {
		if ws.Weight == 0 {
			continue
		}
		scores := ws.Score(ctx, state, req, eps)
		name := ws.TypedName().String()
		raw[name] = scores
		for i := range scored {
			score := clamp(scores[scored[i].Endpoint.ID()])
			scored[i].Score += ws.Weight / total * score
			if p.Observer != nil {
				p.Observer.ObserveScore(pool, p.Name, name, score)
			}
		}
	}
	sort.SliceStable(scored, func(i, j int) bool { return scored[i].Score > scored[j].Score })
	out := p.Picker.Pick(ctx, state, scored)
	if out == nil || len(out.Endpoints) == 0 {
		return nil, fmt.Errorf("%s picked nothing: %w", p.Picker.TypedName(), ErrNoEndpoints)
	}
	if len(p.Reservers) > 0 {
		var err error
		if out.Endpoints, err = reserve(ctx, state, req, p.Reservers, out.Endpoints, scored); err != nil {
			return nil, err
		}
	}
	if out.Scores == nil {
		out.Scores = map[datalayer.EndpointID]float64{}
		for _, s := range scored {
			out.Scores[s.Endpoint.ID()] = s.Score
		}
	}
	out.Filtered = filtered
	out.Scored = raw
	return out, nil
}

// The first endpoint every reserver accepts, picked before scored order, followed by as many fallbacks with room as the picker chose
func reserve(ctx context.Context, state *CycleState, req *Request, reservers []Reserver, picked []datalayer.Endpoint, scored []ScoredEndpoint) ([]datalayer.Endpoint, error) {
	order := make([]datalayer.Endpoint, 0, len(scored))
	order = append(order, picked...)
	for _, s := range scored {
		if !contains(picked, s.Endpoint.ID()) {
			order = append(order, s.Endpoint)
		}
	}
	var refused Reserver
	for i, ep := range order {
		r, ok := claim(ctx, state, req, reservers, ep)
		if !ok {
			refused = r
			continue
		}
		out := []datalayer.Endpoint{ep}
		for _, next := range order[i+1:] {
			if len(out) == len(picked) {
				break
			}
			if _, fits := claim(ctx, state, req, reservers, next); fits {
				release(ctx, state, req, &Profile{Reservers: reservers}, next)
				out = append(out, next)
			}
		}
		return out, nil
	}
	return nil, fmt.Errorf("%s: %w", refused.TypedName(), ErrSaturated)
}

// Asks every reserver for the endpoint, undoing the earlier ones when a later one refuses, naming the refuser
func claim(ctx context.Context, state *CycleState, req *Request, reservers []Reserver, ep datalayer.Endpoint) (Reserver, bool) {
	for i, r := range reservers {
		if r.Reserve(ctx, state, req, ep) {
			continue
		}
		for _, done := range reservers[:i] {
			done.Release(ctx, state, req, ep)
		}
		return r, false
	}
	return nil, true
}

func contains(eps []datalayer.Endpoint, id datalayer.EndpointID) bool {
	for _, ep := range eps {
		if ep.ID() == id {
			return true
		}
	}
	return false
}

func idsOf(eps []datalayer.Endpoint) []datalayer.EndpointID {
	out := make([]datalayer.EndpointID, len(eps))
	for i, ep := range eps {
		out[i] = ep.ID()
	}
	return out
}

func clamp(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
