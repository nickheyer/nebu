// Package archs calculates cache requirements by attention family.
package archs

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/nickheyer/nebu/pkg/formats"
)

// Run dimensions used for cache sizing.
type Run struct {
	// Tokens of context the run holds, zero while unknown
	Context float64
	// Tokens per compute pass, which a sliding window keeps beside its window
	UBatch float64
}

// Attention family and cache formula.
type Arch interface {
	ID() string
	Description() string
	// Families claim architectures in priority order, highest first, the default last
	Priority() int
	// Whether the family covers an architecture as a header names it
	Matches(architecture string) bool
	// Cache elements per token of context on each layer, in layer order, an error naming what
	// the header lacked. A layer without a per token cache, one holding a recurrent state, is zero
	CacheLayers(p formats.Params, run Run) ([]float64, error)
}

// Cache elements per token of context over every layer of a family's model
func CachePerToken(a Arch, p formats.Params, run Run) (float64, error) {
	layers, err := a.CacheLayers(p, run)
	if err != nil {
		return 0, err
	}
	var total float64
	for _, l := range layers {
		total += l
	}
	return total, nil
}

// Every family, in priority order
type Registry struct {
	list []Arch
	byID map[string]Arch
}

func New(archs []Arch) (*Registry, error) {
	r := &Registry{byID: map[string]Arch{}}
	for _, a := range archs {
		if a.ID() == "" {
			return nil, fmt.Errorf("arch without id")
		}
		if _, dup := r.byID[a.ID()]; dup {
			return nil, fmt.Errorf("arch %s: duplicate id", a.ID())
		}
		r.byID[a.ID()] = a
		r.list = append(r.list, a)
	}
	sort.SliceStable(r.list, func(i, j int) bool {
		if r.list[i].Priority() != r.list[j].Priority() {
			return r.list[i].Priority() > r.list[j].Priority()
		}
		return r.list[i].ID() < r.list[j].ID()
	})
	return r, nil
}

func (r *Registry) List() []Arch { return r.list }

// Returns a family by id, nil when unknown
func (r *Registry) Get(id string) Arch { return r.byID[id] }

// Selects the first matching family with sufficient parameters, falling back to the last match if
// none can be sized.
func (r *Registry) Pick(architecture string, p formats.Params) Arch {
	var last Arch
	for _, a := range r.list {
		if !a.Matches(architecture) {
			continue
		}
		last = a
		if _, err := a.CacheLayers(p, Run{}); err == nil {
			return a
		}
	}
	return last
}

// Every family nebu knows
func All() []Arch {
	return []Arch{Default{}, MLA{}, Gemma2{}, Gemma3{}, Gemma3n{}, GPTOSS{}, Llama4{}, Cohere2{}, Phi3{}}
}

// Whether an architecture name is the id, case insensitively
func is(architecture, id string) bool { return strings.EqualFold(architecture, id) }

// Says which parameters a family needed that the header did not give
type Needs struct {
	Names []string
}

func (e *Needs) Error() string { return "needs " + strings.Join(e.Names, ", ") }

// The parameters among the value and name pairs that the header left at zero, nil when none did
func missing(pairs ...any) error {
	var names []string
	for i := 0; i+1 < len(pairs); i += 2 {
		if pairs[i].(float64) <= 0 {
			names = append(names, pairs[i+1].(string))
		}
	}
	if len(names) == 0 {
		return nil
	}
	return &Needs{Names: names}
}

// Cache elements per token on each layer of alternating attention: every pattern-th layer, the
// last of each group as llama.cpp lays the pattern out, keeps the full context, the others the
// window plus the prefill batch. A pattern of zero means every layer keeps a sliding window
func slidingWindow(p formats.Params, run Run, pattern float64) ([]float64, error) {
	if needs := missing(p.Layers, "n_layer", p.HeadsKV, "n_head_kv", p.HeadDim, "head_dim"); needs != nil {
		return nil, needs
	}
	if p.SlidingPattern > 0 {
		pattern = p.SlidingPattern
	}
	ctx := math.Max(run.Context, 1)
	swaTokens := ctx
	if p.SlidingWindow > 0 && run.Context > 0 {
		swaTokens = math.Min(run.Context, p.SlidingWindow+run.UBatch)
	}
	full := p.HeadsKV * (p.HeadDim + p.HeadDimV)
	return eachLayer(p, func(il int) float64 {
		if pattern > 0 && (il+1)%int(pattern) == 0 {
			return full
		}
		return full * swaTokens / ctx
	}), nil
}

// The cache of every layer of a model by its index
func eachLayer(p formats.Params, cache func(il int) float64) []float64 {
	out := make([]float64, int(p.Layers))
	for il := range out {
		out[il] = cache(il)
	}
	return out
}
