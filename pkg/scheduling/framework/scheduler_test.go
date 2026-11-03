package framework_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/nickheyer/nebu/pkg/datalayer"
	"github.com/nickheyer/nebu/pkg/scheduling/framework"
	"github.com/nickheyer/nebu/pkg/scheduling/internal/schedtest"
)

type firstPicker struct{}

func (firstPicker) TypedName() framework.TypedName { return framework.TypedName{Type: "first"} }
func (firstPicker) Pick(_ context.Context, _ *framework.CycleState, s []framework.ScoredEndpoint) *framework.ProfileRunResult {
	return &framework.ProfileRunResult{Endpoints: []datalayer.Endpoint{s[0].Endpoint}}
}

type dropAll struct{}

func (dropAll) TypedName() framework.TypedName { return framework.TypedName{Type: "drop"} }
func (dropAll) Filter(context.Context, *framework.CycleState, *framework.Request, []datalayer.Endpoint) []datalayer.Endpoint {
	return nil
}

// Runs every profile once on the first pass and tolerates failures of the names it lists
type allHandler struct {
	tolerate map[string]bool
	repick   bool
	failed   []string
}

func (h *allHandler) TypedName() framework.TypedName { return framework.TypedName{Type: "all"} }
func (h *allHandler) Pick(_ context.Context, _ *framework.CycleState, _ *framework.Request, profiles map[string]*framework.Profile, results map[string]*framework.ProfileRunResult) map[string]*framework.Profile {
	if len(results) > 0 && !h.repick {
		return nil
	}
	if len(h.failed) > 0 && h.repick {
		return map[string]*framework.Profile{h.failed[0]: profiles[h.failed[0]]}
	}
	return profiles
}
func (h *allHandler) ProcessResults(_ context.Context, _ *framework.CycleState, _ *framework.Request, results map[string]*framework.ProfileRunResult) (*framework.Result, error) {
	return &framework.Result{ProfileResults: results, PrimaryProfile: "ok"}, nil
}
func (h *allHandler) ProfileFailed(_ context.Context, _ *framework.CycleState, _ *framework.Request, profile string, _ error) bool {
	h.failed = append(h.failed, profile)
	return h.tolerate[profile]
}

func TestScheduleFailureHandlerAndState(t *testing.T) {
	cands := schedtest.Eps(schedtest.EP("a"), schedtest.EP("b"))
	profiles := func() map[string]*framework.Profile {
		return map[string]*framework.Profile{
			"ok":  {Picker: firstPicker{}},
			"bad": {Filters: []framework.Filter{dropAll{}}, Picker: firstPicker{}},
		}
	}
	tests := []struct {
		name     string
		handler  *allHandler
		wantErr  error
		wantRuns []string
	}{
		{name: "tolerated failure keeps the cycle", handler: &allHandler{tolerate: map[string]bool{"bad": true}}, wantRuns: []string{"ok"}},
		{name: "untolerated failure fails the cycle", handler: &allHandler{tolerate: map[string]bool{}}, wantErr: framework.ErrNoEndpoints},
		{name: "picking a failed profile again fails the cycle", handler: &allHandler{tolerate: map[string]bool{"bad": true}, repick: true}, wantErr: framework.ErrNoEndpoints},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, err := framework.NewScheduler(tc.handler, profiles())
			if err != nil {
				t.Fatal(err)
			}
			res, err := s.Schedule(context.Background(), &framework.Request{ID: "r"}, cands)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if res.State == nil {
				t.Fatal("result carries no cycle state")
			}
			if len(res.ProfileResults) != len(tc.wantRuns) {
				t.Fatalf("results %v, want %v", res.ProfileResults, tc.wantRuns)
			}
			for _, name := range tc.wantRuns {
				if _, ok := res.ProfileResults[name]; !ok {
					t.Fatalf("profile %s missing from results", name)
				}
			}
		})
	}
}

// A reserver with a room of one per endpoint
type roomOne struct {
	mu       sync.Mutex
	held     map[datalayer.EndpointID]int
	released []datalayer.EndpointID
}

func (r *roomOne) TypedName() framework.TypedName { return framework.TypedName{Type: "room"} }
func (r *roomOne) Reserve(_ context.Context, _ *framework.CycleState, _ *framework.Request, ep datalayer.Endpoint) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.held[ep.ID()] >= 1 {
		return false
	}
	r.held[ep.ID()]++
	return true
}
func (r *roomOne) Release(_ context.Context, _ *framework.CycleState, _ *framework.Request, ep datalayer.Endpoint) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.held[ep.ID()]--
	r.released = append(r.released, ep.ID())
}

// A reserver refusing every endpoint
type noRoom struct{}

func (noRoom) TypedName() framework.TypedName { return framework.TypedName{Type: "full"} }
func (noRoom) Reserve(context.Context, *framework.CycleState, *framework.Request, datalayer.Endpoint) bool {
	return false
}
func (noRoom) Release(context.Context, *framework.CycleState, *framework.Request, datalayer.Endpoint) {
}

type fixedScorer struct {
	name string
	v    map[datalayer.EndpointID]float64
}

func (f fixedScorer) TypedName() framework.TypedName { return framework.TypedName{Type: f.name} }
func (f fixedScorer) Score(context.Context, *framework.CycleState, *framework.Request, []datalayer.Endpoint) map[datalayer.EndpointID]float64 {
	return f.v
}

// Picks the two highest scored endpoints in order
type topTwo struct{}

func (topTwo) TypedName() framework.TypedName { return framework.TypedName{Type: "top2"} }
func (topTwo) Pick(_ context.Context, _ *framework.CycleState, s []framework.ScoredEndpoint) *framework.ProfileRunResult {
	out := &framework.ProfileRunResult{}
	for _, e := range s[:min(2, len(s))] {
		out.Endpoints = append(out.Endpoints, e.Endpoint)
	}
	return out
}

// Refuses every endpoint as saturated
type saturate struct{}

func (saturate) TypedName() framework.TypedName { return framework.TypedName{Type: "saturate"} }
func (saturate) Filter(_ context.Context, state *framework.CycleState, _ *framework.Request, _ []datalayer.Endpoint) []datalayer.Endpoint {
	framework.Refuse(state, framework.ErrSaturated)
	return nil
}

type recordingObserver struct {
	mu      sync.Mutex
	dropped map[string]int
	scores  map[string][]float64
}

func (o *recordingObserver) FilterDropped(_ datalayer.PoolID, profile, filter string, n int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.dropped[profile+"/"+filter] += n
}
func (o *recordingObserver) ObserveScore(_ datalayer.PoolID, profile, scorer string, s float64) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.scores[profile+"/"+scorer] = append(o.scores[profile+"/"+scorer], s)
}

func TestRunProfileReservesInsidePick(t *testing.T) {
	a, b, c := schedtest.EP("a"), schedtest.EP("b"), schedtest.EP("c")
	cands := schedtest.Eps(a, b, c)
	scores := fixedScorer{"fixed", map[datalayer.EndpointID]float64{"a": 1, "b": 0.5, "c": 0.1}}
	room := &roomOne{held: map[datalayer.EndpointID]int{}}
	p := &framework.Profile{Name: "p", Scorers: []framework.WeightedScorer{{Scorer: scores, Weight: 1}}, Picker: topTwo{}, Reservers: []framework.Reserver{room}}
	want := [][]string{{"a", "b"}, {"b", "c"}, {"c"}}
	// Each pick reserves its head alone, the fallback offered only while it has room
	for i, w := range want {
		r, err := framework.RunProfile(context.Background(), framework.NewCycleState(), &framework.Request{ID: "r"}, p, cands)
		if err != nil {
			t.Fatalf("pick %d: %v", i, err)
		}
		if got := schedtest.IDs(r.Endpoints); !slices.Equal(got, w) {
			t.Fatalf("pick %d routed %v, want %v", i, got, w)
		}
	}
	_, err := framework.RunProfile(context.Background(), framework.NewCycleState(), &framework.Request{ID: "r"}, p, cands)
	if !errors.Is(err, framework.ErrSaturated) {
		t.Fatalf("fourth pick err %v, want ErrSaturated", err)
	}
	if room.held["a"] != 1 || room.held["b"] != 1 || room.held["c"] != 1 {
		t.Fatalf("held %v", room.held)
	}

	t.Run("a later reserver refusing undoes the earlier one", func(t *testing.T) {
		room := &roomOne{held: map[datalayer.EndpointID]int{}}
		p := &framework.Profile{Name: "p", Picker: topTwo{}, Reservers: []framework.Reserver{room, noRoom{}}}
		_, err := framework.RunProfile(context.Background(), framework.NewCycleState(), &framework.Request{ID: "r"}, p, cands)
		if !errors.Is(err, framework.ErrSaturated) || !strings.Contains(err.Error(), "full") {
			t.Fatalf("err %v", err)
		}
		if room.held["a"] != 0 || room.held["b"] != 0 || room.held["c"] != 0 || len(room.released) != 3 {
			t.Fatalf("held %v released %v", room.held, room.released)
		}
	})
}

func TestScheduleReleasesUnroutedReservations(t *testing.T) {
	cands := schedtest.Eps(schedtest.EP("a"), schedtest.EP("b"))
	room := &roomOne{held: map[datalayer.EndpointID]int{}}
	profiles := map[string]*framework.Profile{
		"ok":    {Picker: firstPicker{}, Reservers: []framework.Reserver{room}},
		"other": {Picker: firstPicker{}, Reservers: []framework.Reserver{room}, Scorers: []framework.WeightedScorer{{Scorer: fixedScorer{"f", map[datalayer.EndpointID]float64{"b": 1}}, Weight: 1}}},
	}
	s, err := framework.NewScheduler(&allHandler{tolerate: map[string]bool{}}, profiles)
	if err != nil {
		t.Fatal(err)
	}
	res, err := s.Schedule(context.Background(), &framework.Request{ID: "r"}, cands)
	if err != nil {
		t.Fatal(err)
	}
	if got := schedtest.IDs(res.Endpoints()); !slices.Equal(got, []string{"a"}) {
		t.Fatalf("routed %v", got)
	}
	if room.held["a"] != 1 || room.held["b"] != 1 {
		t.Fatalf("both profiles routed by the handler keep their reservations, held %v", room.held)
	}

	t.Run("failed cycle releases every reservation", func(t *testing.T) {
		room := &roomOne{held: map[datalayer.EndpointID]int{}}
		profiles := map[string]*framework.Profile{
			"ok":  {Picker: firstPicker{}, Reservers: []framework.Reserver{room}},
			"bad": {Filters: []framework.Filter{dropAll{}}, Picker: firstPicker{}},
		}
		s, _ := framework.NewScheduler(&allHandler{tolerate: map[string]bool{}}, profiles)
		if _, err := s.Schedule(context.Background(), &framework.Request{ID: "r"}, cands); !errors.Is(err, framework.ErrNoEndpoints) {
			t.Fatalf("err %v", err)
		}
		if room.held["a"] != 0 && room.held["b"] != 0 {
			t.Fatalf("held %v after a failed cycle", room.held)
		}
	})
}

func TestRunProfileRefusalAndOutcomes(t *testing.T) {
	cands := schedtest.Eps(schedtest.EP("a"), schedtest.EP("b").NotReady())
	ready := readyOnly{}
	obs := &recordingObserver{dropped: map[string]int{}, scores: map[string][]float64{}}
	p := &framework.Profile{Name: "p", Filters: []framework.Filter{ready}, Scorers: []framework.WeightedScorer{{Scorer: fixedScorer{"fixed", map[datalayer.EndpointID]float64{"a": 0.25}}, Weight: 1}}, Picker: firstPicker{}, Observer: obs}
	r, err := framework.RunProfile(context.Background(), framework.NewCycleState(), &framework.Request{ID: "r"}, p, cands)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Filtered) != 1 || r.Filtered[0].Filter != "ready" || !slices.Equal(r.Filtered[0].Kept, []datalayer.EndpointID{"a"}) {
		t.Fatalf("filtered %+v", r.Filtered)
	}
	if r.Scored["fixed"]["a"] != 0.25 {
		t.Fatalf("scored %v", r.Scored)
	}
	if obs.dropped["p/ready"] != 1 || !slices.Equal(obs.scores["p/fixed"], []float64{0.25}) {
		t.Fatalf("observer saw dropped %v scores %v", obs.dropped, obs.scores)
	}

	p = &framework.Profile{Name: "p", Filters: []framework.Filter{saturate{}}, Picker: firstPicker{}}
	state := framework.NewCycleState()
	_, err = framework.RunProfile(context.Background(), state, &framework.Request{ID: "r"}, p, cands)
	if !errors.Is(err, framework.ErrSaturated) || errors.Is(err, framework.ErrNoEndpoints) {
		t.Fatalf("err %v, want the filter's refusal", err)
	}
	p = &framework.Profile{Name: "p", Filters: []framework.Filter{dropAll{}}, Picker: firstPicker{}}
	if _, err = framework.RunProfile(context.Background(), state, &framework.Request{ID: "r"}, p, cands); !errors.Is(err, framework.ErrNoEndpoints) || errors.Is(err, framework.ErrSaturated) {
		t.Fatalf("a refusal leaked into the next run: %v", err)
	}
}

type readyOnly struct{}

func (readyOnly) TypedName() framework.TypedName { return framework.TypedName{Type: "ready"} }
func (readyOnly) Filter(_ context.Context, _ *framework.CycleState, _ *framework.Request, eps []datalayer.Endpoint) []datalayer.Endpoint {
	var out []datalayer.Endpoint
	for _, ep := range eps {
		if ep.Ready() {
			out = append(out, ep)
		}
	}
	return out
}
