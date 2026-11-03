package picker_test

import (
	"context"
	"math"
	"slices"
	"testing"

	"github.com/nickheyer/nebu/pkg/scheduling/framework"
	"github.com/nickheyer/nebu/pkg/scheduling/internal/schedtest"
	"github.com/nickheyer/nebu/pkg/scheduling/plugins/picker"
)

const draws = 100_000

// Six standard deviations of a proportion over draws, so a correct sampler fails about once in a billion runs
var bound = 6 * math.Sqrt(0.25/draws)

func scored(pairs ...any) []framework.ScoredEndpoint {
	var out []framework.ScoredEndpoint
	for i := 0; i < len(pairs); i += 2 {
		out = append(out, framework.ScoredEndpoint{Endpoint: schedtest.EP(pairs[i].(string)), Score: pairs[i+1].(float64)})
	}
	return out
}

func pick(p framework.Picker, s []framework.ScoredEndpoint) []string {
	return schedtest.IDs(p.Pick(context.Background(), framework.NewCycleState(), s).Endpoints)
}

func TestMaxScorePickerOrder(t *testing.T) {
	tests := []struct {
		name  string
		count int
		in    []framework.ScoredEndpoint
		want  []string
	}{
		{"top two as primary and fallback", 2, scored("a", 0.9, "b", 0.5, "c", 0.7), []string{"a", "c"}},
		{"count beyond candidates returns all in order", 5, scored("a", 0.1, "b", 0.5, "c", 0.3), []string{"b", "c", "a"}},
		{"one", 1, scored("a", 0.2, "b", 0.8), []string{"b"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := pick(picker.NewMaxScorePicker(tc.count), tc.in); !slices.Equal(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestMaxScorePickerBreaksTiesAtRandom(t *testing.T) {
	p := picker.NewMaxScorePicker(3)
	in := scored("a", 0.5, "b", 0.5, "c", 0.1)
	first := map[string]int{}
	for range draws {
		got := pick(p, in)
		if got[2] != "c" {
			t.Fatalf("lower score not last: %v", got)
		}
		first[got[0]]++
	}
	for _, id := range []string{"a", "b"} {
		if f := float64(first[id]) / draws; math.Abs(f-0.5) > bound {
			t.Fatalf("%s first in %.4f of draws, want 0.5", id, f)
		}
	}
}

func TestWeightedRandomPickerDistribution(t *testing.T) {
	in := scored("a", 0.6, "b", 0.3, "c", 0.1)
	w := map[string]float64{"a": 0.6 + picker.Epsilon, "b": 0.3 + picker.Epsilon, "c": 0.1 + picker.Epsilon}
	sum := w["a"] + w["b"] + w["c"]
	// Probability b is second, the sum over each other first pick of its share times b's share of the rest
	secondB := w["a"]/sum*w["b"]/(sum-w["a"]) + w["c"]/sum*w["b"]/(sum-w["c"])

	p := picker.NewWeightedRandomPicker(2)
	first := map[string]int{}
	second := map[string]int{}
	for range draws {
		got := pick(p, in)
		if len(got) != 2 || got[0] == got[1] {
			t.Fatalf("sample with replacement or wrong size: %v", got)
		}
		first[got[0]]++
		second[got[1]]++
	}
	for id, wi := range w {
		if f := float64(first[id]) / draws; math.Abs(f-wi/sum) > bound {
			t.Fatalf("%s first in %.4f of draws, want %.4f", id, f, wi/sum)
		}
	}
	if f := float64(second["b"]) / draws; math.Abs(f-secondB) > bound {
		t.Fatalf("b second in %.4f of draws, want %.4f", f, secondB)
	}
}

func TestWeightedRandomPickerZeroScoresStayReachable(t *testing.T) {
	p := picker.NewWeightedRandomPicker(1)
	in := scored("a", 0.0, "b", 0.0)
	first := map[string]int{}
	for range draws {
		first[pick(p, in)[0]]++
	}
	if f := float64(first["a"]) / draws; math.Abs(f-0.5) > bound {
		t.Fatalf("a first in %.4f of draws, want 0.5", f)
	}
}

func TestRandomPickerUniform(t *testing.T) {
	p := picker.NewRandomPicker(2)
	in := scored("a", 0.9, "b", 0.1, "c", 0.0)
	first := map[string]int{}
	for range draws {
		got := pick(p, in)
		if len(got) != 2 || got[0] == got[1] {
			t.Fatalf("sample with replacement or wrong size: %v", got)
		}
		first[got[0]]++
	}
	for _, id := range []string{"a", "b", "c"} {
		if f := float64(first[id]) / draws; math.Abs(f-1.0/3) > bound {
			t.Fatalf("%s first in %.4f of draws, want 1/3", id, f)
		}
	}
}

func TestPickerNames(t *testing.T) {
	type named interface {
		framework.Picker
		SetName(string)
	}
	tests := []struct {
		p   named
		typ string
	}{
		{picker.NewMaxScorePicker(1), picker.MaxScorePickerType},
		{picker.NewWeightedRandomPicker(1), picker.WeightedRandomPickerType},
		{picker.NewRandomPicker(1), picker.RandomPickerType},
	}
	for _, tc := range tests {
		t.Run(tc.typ, func(t *testing.T) {
			if got := tc.p.TypedName(); got.Type != tc.typ || got.Name != tc.typ {
				t.Fatalf("default name %+v", got)
			}
			tc.p.SetName("custom")
			if got := tc.p.TypedName(); got.Name != "custom" {
				t.Fatalf("renamed %+v", got)
			}
		})
	}
}
