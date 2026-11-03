// Package picker holds the reference pickers that turn scored endpoints into an ordered primary and fallbacks
package picker

import (
	"context"
	"math"
	"math/rand/v2"
	"slices"
	"sort"

	"github.com/nickheyer/nebu/pkg/datalayer"
	"github.com/nickheyer/nebu/pkg/scheduling/framework"
	"github.com/nickheyer/nebu/pkg/scheduling/plugins"
	"github.com/nickheyer/nebu/pkg/scheduling/registry"
)

// Plugin type names
const (
	MaxScorePickerType       = "max-score-picker"
	WeightedRandomPickerType = "weighted-random-picker"
	RandomPickerType         = "random-picker"
)

// Added to every score so an endpoint scoring zero keeps a chance in weighted sampling
const Epsilon = 1e-3

func endpoints(scored []framework.ScoredEndpoint, n int) *framework.ProfileRunResult {
	n = min(n, len(scored))
	out := make([]datalayer.Endpoint, n)
	for i := range n {
		out[i] = scored[i].Endpoint
	}
	return &framework.ProfileRunResult{Endpoints: out}
}

// Picks the count highest scored endpoints in score order, breaking ties at random
type MaxScorePicker struct {
	plugins.Named
	count int
}

func NewMaxScorePicker(count int) *MaxScorePicker {
	return &MaxScorePicker{Named: plugins.NewNamed(MaxScorePickerType), count: count}
}

func (p *MaxScorePicker) Pick(_ context.Context, _ *framework.CycleState, scored []framework.ScoredEndpoint) *framework.ProfileRunResult {
	s := slices.Clone(scored)
	rand.Shuffle(len(s), func(i, j int) { s[i], s[j] = s[j], s[i] })
	sort.SliceStable(s, func(i, j int) bool { return s[i].Score > s[j].Score })
	return endpoints(s, p.count)
}

// Samples count endpoints without replacement, each draw proportional to score plus Epsilon
type WeightedRandomPicker struct {
	plugins.Named
	count int
}

func NewWeightedRandomPicker(count int) *WeightedRandomPicker {
	return &WeightedRandomPicker{Named: plugins.NewNamed(WeightedRandomPickerType), count: count}
}

// Ranks by the Efraimidis Spirakis key log u over weight, whose top count is a weighted sample without replacement
func (p *WeightedRandomPicker) Pick(_ context.Context, _ *framework.CycleState, scored []framework.ScoredEndpoint) *framework.ProfileRunResult {
	type keyed struct {
		ep  framework.ScoredEndpoint
		key float64
	}
	ks := make([]keyed, len(scored))
	for i, s := range scored {
		u := 1 - rand.Float64()
		ks[i] = keyed{ep: s, key: math.Log(u) / (max(s.Score, 0) + Epsilon)}
	}
	sort.Slice(ks, func(i, j int) bool { return ks[i].key > ks[j].key })
	out := make([]framework.ScoredEndpoint, len(ks))
	for i, k := range ks {
		out[i] = k.ep
	}
	return endpoints(out, p.count)
}

// Picks count endpoints uniformly at random without replacement
type RandomPicker struct {
	plugins.Named
	count int
}

func NewRandomPicker(count int) *RandomPicker {
	return &RandomPicker{Named: plugins.NewNamed(RandomPickerType), count: count}
}

func (p *RandomPicker) Pick(_ context.Context, _ *framework.CycleState, scored []framework.ScoredEndpoint) *framework.ProfileRunResult {
	s := slices.Clone(scored)
	rand.Shuffle(len(s), func(i, j int) { s[i], s[j] = s[j], s[i] })
	return endpoints(s, p.count)
}

type settablePicker interface {
	framework.Picker
	SetName(string)
}

// Registers every picker with a count parameter of at least one, one when absent
func init() {
	builders := map[string]func(int) settablePicker{
		MaxScorePickerType:       func(n int) settablePicker { return NewMaxScorePicker(n) },
		WeightedRandomPickerType: func(n int) settablePicker { return NewWeightedRandomPicker(n) },
		RandomPickerType:         func(n int) settablePicker { return NewRandomPicker(n) },
	}
	for typ, build := range builders {
		registry.Register(typ, func(name string, params map[string]any, _ registry.Deps) (framework.Plugin, error) {
			p := struct {
				Count int `json:"count"`
			}{Count: 1}
			if err := registry.Decode(params, &p); err != nil {
				return nil, err
			}
			if p.Count < 1 {
				return nil, registry.Invalid("count must be at least 1")
			}
			pk := build(p.Count)
			pk.SetName(name)
			return pk, nil
		})
	}
}
