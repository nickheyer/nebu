// Package archs knows every attention family: how much cache each token of context costs.
package archs

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/nickheyer/nebu/pkg/formats"
)

// The shape of one run that the cache size follows
type Run struct {
	// Tokens of context the run holds, zero while unknown
	Context float64
	// Tokens per compute pass, which a sliding window keeps beside its window
	UBatch float64
}

// One attention family, the way it keeps a cache per token
type Arch interface {
	ID() string
	Description() string
	// Families claim architectures in priority order, highest first, the default last
	Priority() int
	// Whether the family covers an architecture as a header names it
	Matches(architecture string) bool
	// Cache elements per token of context, an error naming what the header lacked
	CachePerToken(p formats.Params, run Run) (float64, error)
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

// The first family covering an architecture whose formula the params satisfy, so a family never claims
// a checkpoint missing what it needs, the last match standing in when none is satisfied
func (r *Registry) Pick(architecture string, p formats.Params) Arch {
	var last Arch
	for _, a := range r.list {
		if !a.Matches(architecture) {
			continue
		}
		last = a
		if _, err := a.CachePerToken(p, Run{}); err == nil {
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

// The cache of a model whose layers alternate between full attention and a sliding window: every
// pattern-th layer keeps the whole context, the rest keep a window of tokens plus the batch being
// prefilled. A pattern of zero means every layer slides.
func slidingWindow(p formats.Params, run Run, pattern float64) (float64, error) {
	if needs := missing(p.Layers, "n_layer", p.HeadsKV, "n_head_kv", p.HeadDim, "head_dim"); needs != nil {
		return 0, needs
	}
	if p.SlidingPattern > 0 {
		pattern = p.SlidingPattern
	}
	swaLayers := p.Layers
	if pattern > 0 {
		swaLayers = p.Layers - math.Floor(p.Layers/pattern)
	}
	ctx := math.Max(run.Context, 1)
	swaTokens := ctx
	if p.SlidingWindow > 0 && run.Context > 0 {
		swaTokens = math.Min(run.Context, p.SlidingWindow+run.UBatch)
	}
	return p.HeadsKV * (p.HeadDim + p.HeadDimV) * ((p.Layers - swaLayers) + swaLayers*swaTokens/ctx), nil
}
