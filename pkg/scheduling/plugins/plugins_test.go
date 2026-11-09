package plugins_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/nickheyer/nebu/pkg/datalayer"
	"github.com/nickheyer/nebu/pkg/scheduling/framework"
	"github.com/nickheyer/nebu/pkg/scheduling/internal/schedtest"
	"github.com/nickheyer/nebu/pkg/scheduling/plugins"
	"github.com/nickheyer/nebu/pkg/scheduling/registry"
)

func TestResultWalks(t *testing.T) {
	a, b, c, p := schedtest.EP("a"), schedtest.EP("b"), schedtest.EP("c"), schedtest.EP("p")
	res := &framework.Result{
		PrimaryProfile: "decode",
		ProfileResults: map[string]*framework.ProfileRunResult{
			"decode":  {Endpoints: schedtest.Eps(b, a)},
			"prefill": {Endpoints: schedtest.Eps(p, a)},
			"alpha":   {Endpoints: schedtest.Eps(b, c)},
		},
	}
	tests := []struct {
		name string
		got  []datalayer.Endpoint
		want []string
	}{
		{"heads primary first then by profile name, each once", plugins.ResultHeads(res), []string{"b", "p"}},
		{"every routed endpoint once in order", plugins.ResultEndpoints(res), []string{"b", "a", "c", "p"}},
		{"nil result heads", plugins.ResultHeads(nil), []string{}},
		{"nil result endpoints", plugins.ResultEndpoints(nil), []string{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := schedtest.IDs(tc.got); !slices.Equal(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPrefixFractions(t *testing.T) {
	idx := schedtest.NewIndexer(2)
	h := idx.Hash("m", "", "aabbccdd", nil)
	idx.Record("all", h)
	idx.Record("half", h[:2])
	eps := schedtest.Eps(schedtest.EP("all"), schedtest.EP("half"), schedtest.EP("none"))
	got := plugins.PrefixFractions(idx, h, eps)
	for id, w := range map[datalayer.EndpointID]float64{"all": 1, "half": 0.5, "none": 0} {
		if got[id] != w {
			t.Fatalf("%s = %v, want %v", id, got[id], w)
		}
	}
	if got := plugins.PrefixFractions(idx, nil, eps); got["all"] != 0 || len(got) != 3 {
		t.Fatalf("empty prompt fractions %v", got)
	}
}

func TestPlainRefusesParams(t *testing.T) {
	f := plugins.Plain(func() *named { return &named{Named: plugins.NewNamed("t")} })
	p, err := f("n", nil, registry.Deps{})
	if err != nil || p.TypedName() != (framework.TypedName{Type: "t", Name: "n"}) {
		t.Fatalf("built %v %v", p, err)
	}
	if _, err := f("n", map[string]any{"x": 1}, registry.Deps{}); !errors.Is(err, registry.ErrInvalidParams) {
		t.Fatalf("err %v", err)
	}
}

type named struct{ plugins.Named }
