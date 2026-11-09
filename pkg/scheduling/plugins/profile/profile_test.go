package profile_test

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"

	"github.com/nickheyer/nebu/pkg/datalayer"
	"github.com/nickheyer/nebu/pkg/scheduling/framework"
	"github.com/nickheyer/nebu/pkg/scheduling/internal/schedtest"
	"github.com/nickheyer/nebu/pkg/scheduling/plugins/filter"
	"github.com/nickheyer/nebu/pkg/scheduling/plugins/picker"
	"github.com/nickheyer/nebu/pkg/scheduling/plugins/profile"
	"github.com/nickheyer/nebu/pkg/scheduling/plugins/scorer"
)

var ctx = context.Background()

func role(id, r string) *schedtest.Endpoint {
	return schedtest.EP(id).Label(datalayer.LabelRole, r)
}

func byRole(r string) *framework.Profile {
	return &framework.Profile{Filters: []framework.Filter{filter.NewRoleFilter(r)}, Picker: picker.NewMaxScorePicker(1)}
}

func TestSingleProfileHandler(t *testing.T) {
	cands := schedtest.Eps(role("d", datalayer.RoleDecode))
	tests := []struct {
		name     string
		profiles map[string]*framework.Profile
		wantErr  error
	}{
		{"runs its profile", map[string]*framework.Profile{"main": byRole(datalayer.RoleDecode)}, nil},
		{"absent profile picks nothing", map[string]*framework.Profile{"other": byRole(datalayer.RoleDecode)}, framework.ErrNoProfile},
		{"empty profile fails the cycle", map[string]*framework.Profile{"main": byRole(datalayer.RolePrefill)}, framework.ErrNoEndpoints},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, err := framework.NewScheduler(profile.NewSingleProfileHandler("main"), tc.profiles)
			if err != nil {
				t.Fatal(err)
			}
			res, err := s.Schedule(ctx, &framework.Request{ID: "r"}, cands)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err %v, want %v", err, tc.wantErr)
			}
			if tc.wantErr == nil && (res.PrimaryProfile != "main" || !slices.Equal(schedtest.IDs(res.Endpoints()), []string{"d"})) {
				t.Fatalf("result %+v", res)
			}
		})
	}
}

func TestShadowProfileHandler(t *testing.T) {
	cands := schedtest.Eps(role("d", datalayer.RoleDecode), role("p", datalayer.RolePrefill))
	var mu sync.Mutex
	var seen []profile.ShadowOutcome
	log := func(_ context.Context, _ *framework.Request, out profile.ShadowOutcome) {
		mu.Lock()
		defer mu.Unlock()
		seen = append(seen, out)
	}
	h := profile.NewShadowProfileHandler(log, "main", "works", "empty")
	s, err := framework.NewScheduler(h, map[string]*framework.Profile{
		"main":  byRole(datalayer.RoleDecode),
		"works": byRole(datalayer.RolePrefill),
		"empty": {Filters: []framework.Filter{filter.NewQueueThresholdFilter(0)}, Picker: picker.NewMaxScorePicker(1)},
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := s.Schedule(ctx, &framework.Request{ID: "r"}, cands)
	if err != nil {
		t.Fatal(err)
	}
	if res.PrimaryProfile != "main" || len(res.ProfileResults) != 1 || !slices.Equal(schedtest.IDs(res.Endpoints()), []string{"d"}) {
		t.Fatalf("result routes by more than the primary: %+v", res.ProfileResults)
	}
	if len(seen) != 2 {
		t.Fatalf("logged %d shadow outcomes, want 2", len(seen))
	}
	works, empty := seen[0], seen[1]
	if works.Profile != "works" || works.Err != nil || !slices.Equal(schedtest.IDs(works.Shadow.Endpoints), []string{"p"}) || schedtest.IDs(works.Primary.Endpoints)[0] != "d" {
		t.Fatalf("working shadow outcome %+v", works)
	}
	if empty.Profile != "empty" || !errors.Is(empty.Err, framework.ErrNoEndpoints) || empty.Shadow != nil {
		t.Fatalf("failing shadow outcome %+v", empty)
	}

	t.Run("primary failure fails the cycle", func(t *testing.T) {
		_, err := s.Schedule(ctx, &framework.Request{ID: "r"}, schedtest.Eps(role("p", datalayer.RolePrefill)))
		if !errors.Is(err, framework.ErrNoEndpoints) {
			t.Fatalf("err %v", err)
		}
	})
}

// Records the decode result the prefill profile sees in the cycle state
type probe struct {
	mu   sync.Mutex
	seen []string
}

func (p *probe) TypedName() framework.TypedName { return framework.TypedName{Type: "probe"} }
func (p *probe) Filter(_ context.Context, state *framework.CycleState, _ *framework.Request, eps []datalayer.Endpoint) []datalayer.Endpoint {
	r, ok := framework.ReadState[*framework.ProfileRunResult](state, profile.DecodeResultKey)
	p.mu.Lock()
	defer p.mu.Unlock()
	if ok {
		p.seen = schedtest.IDs(r.Endpoints)
	}
	return eps
}

func TestPDProfileHandler(t *testing.T) {
	cands := schedtest.Eps(role("d1", datalayer.RoleDecode), role("p1", datalayer.RolePrefill), role("b1", datalayer.RoleBoth))
	pd := func(sel profile.DecodeSelector, pr *probe) (framework.Scheduler, error) {
		pre := byRole(datalayer.RolePrefill)
		pre.Filters = append([]framework.Filter{pr}, pre.Filters...)
		pre.Scorers = []framework.WeightedScorer{{Scorer: scorer.NewQueueScorer(), Weight: 1}}
		return framework.NewScheduler(profile.NewPDProfileHandler("prefill", "decode", 256, sel), map[string]*framework.Profile{
			"prefill": pre,
			"decode":  {Filters: []framework.Filter{filter.NewRoleFilter(datalayer.RoleDecode)}, Picker: picker.NewMaxScorePicker(2)},
		})
	}
	tests := []struct {
		name        string
		tokens      int
		wantPrefill bool
	}{
		{"below threshold decode alone", 255, false},
		{"at threshold prefill joins", 256, true},
		{"above threshold prefill joins", 4096, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pr := &probe{}
			s, err := pd(profile.PromptTokens, pr)
			if err != nil {
				t.Fatal(err)
			}
			res, err := s.Schedule(ctx, &framework.Request{ID: "r", PromptTokens: tc.tokens}, cands)
			if err != nil {
				t.Fatal(err)
			}
			if res.PrimaryProfile != "decode" || len(res.Endpoints()) != 2 {
				t.Fatalf("primary %q endpoints %v", res.PrimaryProfile, schedtest.IDs(res.Endpoints()))
			}
			pre := res.Profile("prefill")
			if tc.wantPrefill != (pre != nil) {
				t.Fatalf("prefill endpoints %v, want prefill %v", schedtest.IDs(pre), tc.wantPrefill)
			}
			if !tc.wantPrefill {
				return
			}
			if id := pre[0].ID(); id != "p1" && id != "b1" {
				t.Fatalf("prefill picked %s", id)
			}
			if !slices.Equal(pr.seen, schedtest.IDs(res.Endpoints())) {
				t.Fatalf("prefill saw decode result %v, want %v", pr.seen, schedtest.IDs(res.Endpoints()))
			}
		})
	}

	t.Run("uncached tokens skip prefill when decode holds the prompt", func(t *testing.T) {
		idx := schedtest.NewIndexer(4)
		req := &framework.Request{ID: "r", TargetModel: "m", Prompt: "aaaabbbbccccdddd", PromptTokens: 1000}
		hashes := idx.Hash("m", "", req.Prompt, nil)
		idx.Record("d1", hashes)
		idx.Record("b1", hashes)
		s, err := pd(profile.UncachedTokens(idx), &probe{})
		if err != nil {
			t.Fatal(err)
		}
		res, err := s.Schedule(ctx, req, cands)
		if err != nil {
			t.Fatal(err)
		}
		if res.Profile("prefill") != nil {
			t.Fatal("prefill ran although decode caches the whole prompt")
		}
		cold := &framework.Request{ID: "r2", TargetModel: "m", Prompt: "zzzzyyyy", PromptTokens: 1000}
		res, err = s.Schedule(ctx, cold, cands)
		if err != nil {
			t.Fatal(err)
		}
		if res.Profile("prefill") == nil {
			t.Fatal("prefill skipped although decode caches nothing")
		}
	})

	t.Run("prefill wanted without its profile fails", func(t *testing.T) {
		s, err := framework.NewScheduler(profile.NewPDProfileHandler("prefill", "decode", 0, profile.PromptTokens), map[string]*framework.Profile{
			"decode": byRole(datalayer.RoleDecode),
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Schedule(ctx, &framework.Request{ID: "r"}, cands); !errors.Is(err, framework.ErrNoProfile) {
			t.Fatalf("err %v", err)
		}
	})

	t.Run("prefill failure fails the cycle", func(t *testing.T) {
		s, err := pd(profile.PromptTokens, &probe{})
		if err != nil {
			t.Fatal(err)
		}
		_, err = s.Schedule(ctx, &framework.Request{ID: "r", PromptTokens: 300}, schedtest.Eps(role("d1", datalayer.RoleDecode)))
		if !errors.Is(err, framework.ErrNoEndpoints) {
			t.Fatalf("err %v", err)
		}
	})
}

func TestProfileRefs(t *testing.T) {
	tests := []struct {
		refs []string
		want []string
	}{
		{profile.NewSingleProfileHandler("a").ProfileRefs(), []string{"a"}},
		{profile.NewShadowProfileHandler(nil, "a", "b", "c").ProfileRefs(), []string{"a", "b", "c"}},
		{profile.NewPDProfileHandler("p", "d", 1, profile.PromptTokens).ProfileRefs(), []string{"d", "p"}},
	}
	for _, tc := range tests {
		if !slices.Equal(tc.refs, tc.want) {
			t.Fatalf("refs %v, want %v", tc.refs, tc.want)
		}
	}
}
