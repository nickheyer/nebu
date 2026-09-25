package mesh

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/nickheyer/nebu/pkg/estimate"
	"github.com/nickheyer/nebu/pkg/formats"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/runtimes"
	"github.com/nickheyer/nebu/pkg/text"
)

// What a profile weighs: time to first token, tokens per second over the completion, and
// throughput, at reference sizes
type weights struct {
	ttft, tps, rps float64
	prompt         float64
	completion     float64
}

// The weights of the request's profile, learned from traces for auto
func (pl *planner) weights() weights {
	st := pl.req.Stats
	switch pl.req.Profile {
	case v1.PlanProfile_PLAN_PROFILE_AGENT:
		prompt := float64(st.MedianPrompt)
		if prompt == 0 {
			prompt = AgentPrompt
		}
		return weights{ttft: 2, tps: 1, prompt: prompt, completion: ChatCompletion}
	case v1.PlanProfile_PLAN_PROFILE_BATCH:
		concurrency := float64(st.PeakConcurrency)
		if concurrency < 2 {
			concurrency = BatchConcurrency
		}
		return weights{ttft: 0.5, tps: 0.5, rps: concurrency, prompt: ChatPrompt, completion: ChatCompletion}
	case v1.PlanProfile_PLAN_PROFILE_AUTO:
		w := weights{ttft: 1, tps: 1, prompt: ChatPrompt, completion: ChatCompletion}
		if st.Traces > 0 {
			if st.MedianPrompt > 0 {
				w.prompt = float64(st.MedianPrompt)
			}
			if st.MedianCompletion > 0 {
				w.completion = float64(st.MedianCompletion)
			}
			if st.PeakConcurrency > 1 {
				w.rps = float64(st.PeakConcurrency)
			}
		}
		return w
	}
	return weights{ttft: 1, tps: 1, prompt: ChatPrompt, completion: ChatCompletion}
}

func (pl *planner) prompt() float64     { return pl.weights().prompt }
func (pl *planner) completion() float64 { return pl.weights().completion }

// Applies the learned ratio for the candidate's shape, runtime, and link class, then scores it
func (pl *planner) score(c *candidate) (ttftRatio, tptRatio float64) {
	ttftRatio, tptRatio = 1, 1
	if pl.req.Table != nil && c.shape != v1.Shape_SHAPE_SOLO {
		ttftRatio, tptRatio, c.ratioSamples = pl.req.Table.Ratio(c.shape, pl.req.Runtime.ID(), c.class)
	}
	c.prefill *= ttftRatio
	c.decode *= tptRatio
	if c.prefill > 0 || c.decode > 0 {
		c.rps = 1 / (c.prefill + pl.completion()*c.decode)
		if c.shape == v1.Shape_SHAPE_REPLICAS {
			c.rps = 0
			for _, s := range c.seats {
				c.rps += 1 / (s.prefill(pl.prompt())*ttftRatio + pl.completion()*s.decode()*tptRatio)
			}
		}
	}
	w := pl.weights()
	c.score = w.ttft*c.prefill + w.tps*w.completion*c.decode
	if w.rps > 0 {
		if c.rps > 0 {
			c.score += w.rps / c.rps
		} else {
			c.score = math.Inf(1)
		}
	}
	return ttftRatio, tptRatio
}

// Orders candidates: fitting first, then partial, then rejected, by score within each, an equal
// score going to the candidate whose head is the conductor, then to the fewer seats
func order(list []*candidate, self string) {
	conducts := func(c *candidate) bool { return c.head != nil && c.head.node.ID == self }
	sort.SliceStable(list, func(i, j int) bool {
		a, b := list[i], list[j]
		if a.verdict != b.verdict {
			return rank(a.verdict) < rank(b.verdict)
		}
		if a.score != b.score {
			return a.score < b.score
		}
		if conducts(a) != conducts(b) {
			return conducts(a)
		}
		return len(a.seats) < len(b.seats)
	})
}

func rank(v v1.FitVerdict) int {
	switch v {
	case v1.FitVerdict_FIT_VERDICT_FITS:
		return 0
	case v1.FitVerdict_FIT_VERDICT_PARTIAL:
		return 1
	}
	return 2
}

// Plans the request across the mesh
func Plan(req Request, m *Mesh) (*v1.FormationPlan, error) {
	if req.Runtime == nil || req.Runtime.Policy() == nil {
		return nil, fmt.Errorf("%w: the runtime has no estimate policy", ErrPlan)
	}
	if req.Descriptor == nil {
		return nil, fmt.Errorf("%w: no descriptor", ErrPlan)
	}
	pl := &planner{req: req, mesh: m, policy: req.Runtime.Policy(), excluded: map[string]string{}, seen: map[string]bool{}, fits: map[string]fitResult{}}
	if pl.policy.Shapes != nil {
		pl.facts = *pl.policy.Shapes
	}
	for id, why := range req.Excluded {
		pl.excluded[id] = why
	}
	span := map[string]bool{}
	for _, id := range req.Span {
		span[id] = true
	}
	for _, n := range m.Nodes {
		if len(span) > 0 && !span[n.ID] {
			continue
		}
		switch {
		case n.Install == nil:
			pl.excluded[n.ID] = fmt.Sprintf("%s has no %s install", n.label(), req.Runtime.ID())
		case len(n.Devices) == 0 && n.CPU == nil:
			pl.excluded[n.ID] = fmt.Sprintf("%s has no memory pool probed", n.label())
		default:
			pl.nodes = append(pl.nodes, n)
		}
	}
	if len(pl.nodes) == 0 {
		var why []string
		for _, id := range req.Span {
			if r, ok := pl.excluded[id]; ok {
				why = append(why, r)
			} else if m.Node(id) == nil {
				why = append(why, fmt.Sprintf("%s is not a member of the mesh", id))
			}
		}
		if len(why) == 0 {
			why = append(why, "no node in the span")
		}
		return nil, fmt.Errorf("%w: %s", ErrPlan, strings.Join(why, "; "))
	}
	// Resolve params over the runtime's defaults, stored companions filling path params.
	models := append([]*v1.StoredModel{}, req.Companions...)
	overrides, err := runtimes.ResolveStore(req.Runtime, req.Params, models)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrPlan, err)
	}
	if pl.params, err = runtimes.Resolve(req.Runtime, overrides); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrPlan, err)
	}
	// Companion parts widen a diffusion descriptor before the shapes split it.
	scope := &estimate.Scope{Descriptor: req.Descriptor, Model: formats.ParamsOf(req.Descriptor.GetParams()), Host: pl.nodes[0].Profile, Params: pl.params.Clone(), Companions: req.Companions, Repo: req.Repo}
	if pl.policy.Solve != nil {
		pl.policy.Solve(scope)
		pl.params = scope.Params
	}
	pl.parts = split(scope.Descriptor)
	// Try contexts largest first when the context is auto, keeping the first that fits.
	contexts := pl.contexts(scope)
	var best []*candidate
	var chosenContext int64
	for _, ctx := range contexts {
		pl.context = ctx
		list := pl.search()
		for _, c := range list {
			pl.score(c)
		}
		order(list, m.Self)
		best, chosenContext = list, ctx
		if len(list) > 0 && list[0].verdict == v1.FitVerdict_FIT_VERDICT_FITS {
			break
		}
	}
	return pl.result(best, chosenContext), nil
}

// The contexts a pass tries: the request's own, or the trained length halving down to the minimum
func (pl *planner) contexts(scope *estimate.Scope) []int64 {
	if pl.policy.ContextParam == "" {
		return []int64{0}
	}
	if !pl.params.IsAuto(pl.policy.ContextParam) {
		return []int64{pl.params.Int(pl.policy.ContextParam)}
	}
	step := pl.policy.ContextStep
	if step <= 0 {
		step = 1
	}
	lo := pl.policy.ContextMin
	if lo <= 0 {
		lo = step
	}
	hi := lo
	if pl.policy.ContextMax != nil {
		hi = pl.policy.ContextMax(scope)
	}
	if hi < lo {
		hi = lo
	}
	var out []int64
	for c := hi; c > lo; c /= 2 {
		out = append(out, c/step*step)
	}
	return append(out, lo)
}

// The affinity policy a replicas route's gateway applies: a conversation stays on the seat that
// answered its opening messages within the window, the hash covering the system message and
// the first user message
func affinity() *v1.AffinityPolicy {
	return &v1.AffinityPolicy{WindowSeconds: AffinityWindowSeconds, SystemMessage: true, FirstUserMessage: true}
}

// Builds the plan record from the ordered candidates
func (pl *planner) result(list []*candidate, context int64) *v1.FormationPlan {
	plan := &v1.FormationPlan{
		RuntimeId:           pl.req.Runtime.ID(),
		ReferencePrompt:     uint32(pl.prompt()),
		ReferenceCompletion: uint32(pl.completion()),
		Profile:             pl.req.Profile,
		Context:             uint32(context),
		Sources:             pl.sources,
	}
	var bestSolo float64
	for _, c := range list {
		if c.shape == v1.Shape_SHAPE_SOLO && c.verdict == v1.FitVerdict_FIT_VERDICT_FITS && c.decode > 0 && (bestSolo == 0 || c.decode < bestSolo) {
			bestSolo = c.decode
		}
	}
	for _, c := range list {
		out := &v1.Candidate{
			Shape:                 c.shape,
			NodeIds:               c.nodeIDs(),
			Verdict:               c.verdict,
			Score:                 c.score,
			PrefillSeconds:        c.prefill,
			DecodeSecondsPerToken: c.decode,
			Reason:                c.reason,
			RequestsPerSecond:     c.rps,
			TokensPerSecond:       c.tps(),
			DraftTokens:           c.draftTokens,
		}
		if c.head != nil {
			out.Head = c.head.node.ID
		}
		if bestSolo > 0 && c.decode > 0 {
			out.Speedup = bestSolo / c.decode
		}
		if math.IsInf(out.Score, 0) || math.IsNaN(out.Score) {
			out.Score = math.MaxFloat64
		}
		plan.Candidates = append(plan.Candidates, out)
	}
	var excludedIDs []string
	for id := range pl.excluded {
		excludedIDs = append(excludedIDs, id)
	}
	sort.Strings(excludedIDs)
	for _, id := range excludedIDs {
		plan.Candidates = append(plan.Candidates, &v1.Candidate{Shape: v1.Shape_SHAPE_UNSPECIFIED, NodeIds: []string{id}, Verdict: v1.FitVerdict_FIT_VERDICT_NO, Reason: pl.excluded[id], Score: math.MaxFloat64})
	}
	if len(list) == 0 {
		plan.Verdict = v1.FitVerdict_FIT_VERDICT_NO
		plan.Detail = "no shape any node's runtime supports fits the span"
		if pl.req.Shape != v1.Shape_SHAPE_UNSPECIFIED {
			plan.Detail = fmt.Sprintf("no node in the span runs %s with %s", shapeName(pl.req.Shape), pl.req.Runtime.ID())
		}
		return plan
	}
	chosen := list[0]
	plan.Shape = chosen.shape
	plan.Verdict = chosen.verdict
	plan.PrefillSeconds = chosen.prefill
	plan.DecodeSecondsPerToken = chosen.decode
	plan.RequestsPerSecond = chosen.rps
	plan.TokensPerSecond = chosen.tps()
	plan.RelayBreakEvenPrompt = chosen.breakEven
	plan.LinkClass = chosen.class
	plan.DraftTokens = chosen.draftTokens
	plan.Acceptance = chosen.acceptance
	plan.ModelDecodeSecondsPerToken = chosen.modelDecode
	for _, p := range chosen.pieces {
		plan.PrefillCost = append(plan.PrefillCost, &v1.PrefillCost{FixedSeconds: p.fixed, SecondsPerPromptToken: p.perToken})
	}
	if bestSolo > 0 && chosen.decode > 0 {
		plan.Speedup = bestSolo / chosen.decode
	}
	if chosen.head != nil {
		plan.Head = chosen.head.node.ID
	}
	if chosen.shape == v1.Shape_SHAPE_REPLICAS {
		plan.Affinity = affinity()
	}
	plan.TtftRatio, plan.TptRatio = 1, 1
	if pl.req.Table != nil && chosen.shape != v1.Shape_SHAPE_SOLO {
		plan.TtftRatio, plan.TptRatio, plan.RatioSamples = pl.req.Table.Ratio(chosen.shape, pl.req.Runtime.ID(), chosen.class)
	}
	for _, s := range chosen.seats {
		if s.plan == nil {
			s.plan = &v1.MemoryPlan{Verdict: s.verdict, Detail: s.detail}
		}
		plan.Seats = append(plan.Seats, s.proto())
	}
	plan.Memory = memoryOf(chosen.seats)
	plan.Memory.Params = stringParams(pl.fixed())
	if pl.policy.ContextParam != "" && context > 0 {
		plan.Memory.Params[pl.policy.ContextParam] = fmt.Sprint(context)
	}
	switch chosen.verdict {
	case v1.FitVerdict_FIT_VERDICT_FITS:
		plan.Detail = fmt.Sprintf("%s: %s; %s to first token at %d prompt tokens, %.1f tokens per second", shapeName(chosen.shape), chosen.reason, seconds(chosen.prefill), plan.ReferencePrompt, plan.TokensPerSecond)
	case v1.FitVerdict_FIT_VERDICT_PARTIAL:
		plan.Detail = fmt.Sprintf("%s fits only partially: %s", shapeName(chosen.shape), chosen.reason)
	default:
		plan.Detail = fmt.Sprintf("nothing fits: %s", chosen.reason)
	}
	if plan.RatioSamples > 0 {
		plan.Detail += fmt.Sprintf("; corrected by learned ratios %.2f and %.2f from %d runs", plan.TtftRatio, plan.TptRatio, plan.RatioSamples)
	}
	if len(pl.profiled) > 0 {
		plan.Detail += fmt.Sprintf("; numbers from the profile table, no run has taught them yet: %s", strings.Join(pl.profiled, ", "))
	}
	return plan
}

func stringParams(p estimate.Params) map[string]string {
	out := make(map[string]string, len(p))
	for k, v := range p {
		out[k] = fmt.Sprint(v)
	}
	return out
}

// The verdict of a plan as a word
func Verdict(v v1.FitVerdict) string { return text.Enum(v) }
