// Package estimate plans model memory onto host pools.
package estimate

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/nickheyer/nebu/pkg/eval"
	"github.com/nickheyer/nebu/pkg/host"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// Value a solved param takes before planning
const Auto = "auto"

// Returned when the params name a choice the runtime rules out under the other params
var ErrRefused = errors.New("params refused")

// A solved param with the expression that gives it its value
type autoParam struct {
	name string
	expr *eval.Expr
}

// Choices of one param that only stand while an expression holds
type choiceRule struct {
	param   string
	choices []string
	when    *eval.Expr
	message string
}

// A numeric param with the bound the model or host gives it
type boundParam struct {
	spec *v1.Param
	max  *eval.Expr
}

// Compiled estimate policy of one runtime
type Policy struct {
	spec     *v1.EstimatePolicy
	groups   map[v1.TensorGroupKind]*v1.GroupPolicy
	when     map[v1.TensorGroupKind]*eval.Expr
	cache    *eval.Expr
	overhead *eval.Expr
	// Params solved by expression, in manifest order so one may read another
	autos []autoParam
	rules []choiceRule
	// Every param whose bounds or choices the plan reports, in manifest order
	stated []boundParam
	// The context param and the ceiling an auto context solves under
	context    *v1.Param
	contextMax *eval.Expr
}

// Everything a plan needs
type Input struct {
	Descriptor    *v1.Descriptor
	Formulas      map[string]*eval.Expr
	Host          *v1.HostProfile
	Params        map[string]any
	Free          bool
	OverheadDelta float64
	// Plans through a choice the rules refuse instead of failing, for an estimate that reports the refusal
	SkipRules bool
}

// Compiles policy expressions, with the params the runtime takes for the ones the plan solves or bounds
func NewPolicy(spec *v1.EstimatePolicy, params []*v1.Param) (*Policy, error) {
	p := &Policy{spec: spec, groups: map[v1.TensorGroupKind]*v1.GroupPolicy{}, when: map[v1.TensorGroupKind]*eval.Expr{}}
	for _, prm := range params {
		if prm.GetAuto() != "" {
			if !prm.GetSolved() {
				return nil, fmt.Errorf("param %s: auto needs solved", prm.GetName())
			}
			e, err := eval.Compile(prm.GetAuto())
			if err != nil {
				return nil, fmt.Errorf("param %s auto: %w", prm.GetName(), err)
			}
			p.autos = append(p.autos, autoParam{name: prm.GetName(), expr: e})
		}
		for _, r := range prm.GetChoiceRules() {
			if len(r.GetChoices()) == 0 || r.GetWhen() == "" || r.GetMessage() == "" {
				return nil, fmt.Errorf("param %s: every choice rule needs choices, when, and message", prm.GetName())
			}
			for _, ch := range r.GetChoices() {
				if !containsString(prm.GetChoices(), ch) {
					return nil, fmt.Errorf("param %s: choice rule names %q, not one of its choices", prm.GetName(), ch)
				}
			}
			e, err := eval.Compile(r.GetWhen())
			if err != nil {
				return nil, fmt.Errorf("param %s choice rule: %w", prm.GetName(), err)
			}
			p.rules = append(p.rules, choiceRule{param: prm.GetName(), choices: r.GetChoices(), when: e, message: r.GetMessage()})
		}
		var max *eval.Expr
		if prm.GetMaxExpr() != "" {
			numeric := prm.GetType() == v1.ParamType_PARAM_TYPE_INT || prm.GetType() == v1.ParamType_PARAM_TYPE_FLOAT
			if !numeric {
				return nil, fmt.Errorf("param %s: max_expr applies to int and float params only", prm.GetName())
			}
			var err error
			if max, err = eval.Compile(prm.GetMaxExpr()); err != nil {
				return nil, fmt.Errorf("param %s max_expr: %w", prm.GetName(), err)
			}
		}
		if max != nil || len(prm.GetChoiceRules()) > 0 || prm.GetMin() != 0 || prm.GetMax() != 0 || prm.GetStep() != 0 {
			p.stated = append(p.stated, boundParam{spec: prm, max: max})
		}
		if prm.GetName() == spec.GetContextParam() {
			p.context = prm
		}
	}
	if spec.GetContextMax() != "" {
		e, err := eval.Compile(spec.GetContextMax())
		if err != nil {
			return nil, fmt.Errorf("context_max: %w", err)
		}
		p.contextMax = e
	}
	if spec.GetContextParam() != "" && p.context == nil {
		return nil, fmt.Errorf("context_param %s is not a param", spec.GetContextParam())
	}
	for _, g := range spec.GetGroups() {
		p.groups[g.GetKind()] = g
		if g.GetWhen() != "" {
			e, err := eval.Compile(g.GetWhen())
			if err != nil {
				return nil, fmt.Errorf("group %s when: %w", eval.EnumShort(g.GetKind()), err)
			}
			p.when[g.GetKind()] = e
		}
	}
	var err error
	if spec.GetCacheBytes() != "" {
		if p.cache, err = eval.Compile(spec.GetCacheBytes()); err != nil {
			return nil, err
		}
	}
	if spec.GetOverheadBytes() != "" {
		if p.overhead, err = eval.Compile(spec.GetOverheadBytes()); err != nil {
			return nil, err
		}
	}
	return p, nil
}

// Returns the param name holding context length
func (p *Policy) ContextParam() string { return p.spec.GetContextParam() }

// One tensor group as the planner moves it: its weights, and the share of the cache that follows it
type item struct {
	kind    v1.TensorGroupKind
	layer   int32
	weights uint64
	cache   uint64
}

func (it item) bytes() uint64 { return it.weights + it.cache }

type bucket struct {
	policy *v1.GroupPolicy
	items  []item
	prefix []uint64
	kinds  map[v1.TensorGroupKind][]int
	fixed  int
	count  int
}

type solver struct {
	buckets  []*bucket
	byKind   map[v1.TensorGroupKind]*bucket
	policies map[v1.TensorGroupKind]*v1.GroupPolicy
	free     bool
	overhead uint64
	// Groups the policy pins to one side, never offloaded
	pinned    []item
	hosted    []item
	fixedDev  uint64
	fixedHost uint64
	devCap    uint64
	hostCap   uint64
}

// Plans the descriptor on the host, solving offload params, the params an expression
// solves, and an auto context length, which takes the largest that still fits
func (p *Policy) Plan(in Input) (*v1.MemoryPlan, error) {
	env, err := p.env(in)
	if err != nil {
		return nil, err
	}
	params := cloneParams(in.Params)
	for _, a := range p.autos {
		if params[a.name] != Auto {
			continue
		}
		v, err := a.expr.Value(env)
		if err != nil {
			return nil, eval.Explain(a.expr, a.name, env, err)
		}
		params[a.name] = fmt.Sprint(v)
		env[a.name] = params[a.name]
	}
	if !in.SkipRules {
		if _, refusal, err := p.states(env, params, false); err != nil {
			return nil, err
		} else if refusal != "" {
			return nil, fmt.Errorf("%w: %s", ErrRefused, refusal)
		}
	}
	if name := p.spec.GetContextParam(); name != "" && params[name] == Auto {
		return p.solveContext(in, env, params)
	}
	return p.plan(in, env, params)
}

// Picks the largest context on the param's grid, up to the model's own, that keeps the verdict the
// smallest context earns: a model that fits whole stays whole, one that spills stays on the host
func (p *Policy) solveContext(in Input, env map[string]any, params map[string]any) (*v1.MemoryPlan, error) {
	name := p.spec.GetContextParam()
	step := int64(p.context.GetStep())
	if step <= 0 {
		step = 1
	}
	lo := int64(p.context.GetMin())
	if lo <= 0 {
		lo = step
	}
	hi := lo
	if p.contextMax != nil {
		v, err := p.contextMax.Float(env)
		if err != nil {
			return nil, eval.Explain(p.contextMax, "context_max", env, err)
		}
		hi = int64(v)
	}
	if p.context.GetMax() > 0 && int64(p.context.GetMax()) < hi {
		hi = int64(p.context.GetMax())
	}
	if hi < lo {
		hi = lo
	}
	at := func(n int64) (*v1.MemoryPlan, error) {
		ps := cloneParams(params)
		ps[name] = n
		e := cloneParams(env)
		e[name] = n
		return p.plan(in, e, ps)
	}
	best, err := at(lo)
	if err != nil || best.GetVerdict() == v1.FitVerdict_FIT_VERDICT_NO {
		return best, err
	}
	target := best.GetVerdict()
	steps := (hi - lo) / step
	l, r := int64(0), steps
	for l < r {
		mid := (l + r + 1) / 2
		plan, err := at(lo + mid*step)
		if err != nil {
			return nil, err
		}
		if plan.GetVerdict() <= target {
			best, l = plan, mid
		} else {
			r = mid - 1
		}
	}
	// The ceiling itself when the grid stops short of it
	if l == steps && lo+steps*step < hi {
		if plan, err := at(hi); err == nil && plan.GetVerdict() <= target {
			best = plan
		}
	}
	return best, nil
}

// Every param's bounds and ruled out choices under the params a plan solved, and why a run with them
// would be refused, from the scope the plan read with the solved values in place
func (p *Policy) States(in Input, plan *v1.MemoryPlan) ([]*v1.ParamState, string, error) {
	env, err := p.env(in)
	if err != nil {
		return nil, "", err
	}
	params := cloneParams(in.Params)
	for k, v := range plan.GetParams() {
		if n, err := strconv.ParseFloat(v, 64); err == nil {
			params[k] = n
		} else {
			params[k] = v
		}
		env[k] = params[k]
	}
	return p.states(env, params, true)
}

// The states of every param the policy speaks for; bounds are only read once every param is concrete,
// since a bound may follow a param the plan has yet to solve
func (p *Policy) states(env map[string]any, params map[string]any, bounds bool) ([]*v1.ParamState, string, error) {
	var out []*v1.ParamState
	var refusal string
	for _, b := range p.stated {
		st := &v1.ParamState{Name: b.spec.GetName(), Min: b.spec.GetMin(), Max: b.spec.GetMax(), Step: b.spec.GetStep()}
		if b.max != nil && bounds {
			v, err := b.max.Float(env)
			if err != nil {
				return nil, "", eval.Explain(b.max, b.spec.GetName()+" max", env, err)
			}
			if v > 0 {
				st.Max = v
			}
		}
		current := fmt.Sprint(params[b.spec.GetName()])
		for _, r := range p.rules {
			if r.param != b.spec.GetName() {
				continue
			}
			ok, err := r.when.Bool(env)
			if err != nil {
				return nil, "", eval.Explain(r.when, b.spec.GetName()+" choice rule", env, err)
			}
			if ok {
				continue
			}
			for _, ch := range r.choices {
				st.Disabled = append(st.Disabled, &v1.DisabledChoice{Value: ch, Message: r.message})
				if ch == current && refusal == "" {
					refusal = fmt.Sprintf("%s %s: %s", label(b.spec), ch, r.message)
				}
			}
		}
		out = append(out, st)
	}
	return out, refusal, nil
}

func label(p *v1.Param) string {
	if p.GetLabel() != "" {
		return strings.ToLower(p.GetLabel())
	}
	return p.GetName()
}

func containsString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func cloneParams(m map[string]any) map[string]any {
	out := make(map[string]any, len(m)+1)
	for k, v := range m {
		out[k] = v
	}
	return out
}

// Plans with every param concrete
func (p *Policy) plan(in Input, env map[string]any, params map[string]any) (*v1.MemoryPlan, error) {
	cacheTotal, err := p.eval(p.cache, "cache_bytes", env)
	if err != nil {
		return nil, err
	}
	overhead, err := p.eval(p.overhead, "overhead_bytes", env)
	if err != nil {
		return nil, err
	}
	if in.OverheadDelta != 0 {
		overhead = uint64(max(float64(overhead)+in.OverheadDelta, 0))
	}
	unloaded, err := p.unloaded(env)
	if err != nil {
		return nil, err
	}
	primary, host := pools(in.Host)
	primary = p.spanned(primary, params)
	margin := 1 - p.spec.GetMargin()
	s := &solver{byKind: map[v1.TensorGroupKind]*bucket{}, policies: p.groups, free: in.Free, overhead: overhead}
	for _, pl := range primary {
		s.devCap += uint64(float64(capacity(pl, in.Free)) * margin)
	}
	for _, pl := range host {
		s.hostCap += uint64(float64(capacity(pl, in.Free)) * margin)
	}
	s.fixedDev = overhead
	layers := 0
	for _, g := range in.Descriptor.GetGroups() {
		if g.GetKind() == v1.TensorGroupKind_TENSOR_GROUP_KIND_LAYER && !unloaded[g.GetKind()] {
			layers++
		}
	}
	// The cache follows the layers it serves, or sits on device whole when nothing is layered
	var cachePerLayer uint64
	if layers > 0 {
		cachePerLayer = cacheTotal / uint64(layers)
	} else {
		s.fixedDev += cacheTotal
	}
	byParam := map[string]*bucket{}
	skipped := newPlacements()
	var weights uint64
	for _, g := range in.Descriptor.GetGroups() {
		if unloaded[g.GetKind()] {
			skipped.add(g.GetKind(), v1.PoolKind_POOL_KIND_UNSPECIFIED, g.GetBytes(), 1)
			continue
		}
		weights += g.GetBytes()
		it := item{kind: g.GetKind(), layer: g.GetLayer(), weights: g.GetBytes()}
		if g.GetKind() == v1.TensorGroupKind_TENSOR_GROUP_KIND_LAYER {
			it.cache = cachePerLayer
		}
		gp := p.groups[g.GetKind()]
		if gp == nil || gp.GetParam() == "" {
			if poolOf(gp) == v1.PoolKind_POOL_KIND_HOST {
				s.hosted = append(s.hosted, it)
				s.fixedHost += it.bytes()
			} else {
				s.pinned = append(s.pinned, it)
				s.fixedDev += it.bytes()
			}
			continue
		}
		b, ok := byParam[gp.GetParam()]
		if !ok {
			b = &bucket{policy: gp, kinds: map[v1.TensorGroupKind][]int{}, fixed: -1}
			byParam[gp.GetParam()] = b
			s.buckets = append(s.buckets, b)
		}
		s.byKind[g.GetKind()] = b
		b.items = append(b.items, it)
	}
	for _, b := range s.buckets {
		b.prepare(params)
	}
	sort.SliceStable(s.buckets, func(i, j int) bool {
		return s.buckets[i].policy.GetSpillPriority() > s.buckets[j].policy.GetSpillPriority()
	})
	plan := &v1.MemoryPlan{
		WeightsBytes:  weights,
		CacheBytes:    cacheTotal,
		OverheadBytes: overhead,
		OverheadDelta: in.OverheadDelta,
		Params:        stringParams(params),
		Skipped:       skipped.list(),
		AgainstFree:   in.Free,
	}
	if len(primary) > 0 && s.solve(0) {
		plan.Verdict = v1.FitVerdict_FIT_VERDICT_FITS
		for _, b := range s.buckets {
			if b.count < len(b.items) {
				plan.Verdict = v1.FitVerdict_FIT_VERDICT_PARTIAL
			}
		}
		s.fill(plan, primary, host)
		return plan, nil
	}
	plan.Verdict = v1.FitVerdict_FIT_VERDICT_NO
	// The least the solver could ask of each side, read before the layout below moves the counts
	for _, b := range s.buckets {
		b.count = max(b.fixed, 0)
	}
	least, most := s.devNeed(), s.hostNeed()
	s.waterfall(plan, primary, host)
	switch {
	case len(primary) == 0:
		plan.Detail = "no device memory pools probed"
	case overflow(plan) > 0:
		plan.Detail = fmt.Sprintf("needs %s, %s more than the %s of memory on this host", Human(need(plan)), Human(overflow(plan)), Human(held(plan)))
	case s.hostCap > 0 && most > s.hostCap:
		plan.Detail = fmt.Sprintf("host need %s exceeds capacity %s", Human(most), Human(s.hostCap))
	default:
		plan.Detail = fmt.Sprintf("device need %s exceeds capacity %s", Human(least), Human(s.devCap))
	}
	return plan, nil
}

// Keeps the largest pools a run spans when the policy names a param counting them
func (p *Policy) spanned(primary []*v1.MemoryPool, params map[string]any) []*v1.MemoryPool {
	name := p.spec.GetDevicesParam()
	if name == "" {
		return primary
	}
	n, err := eval.Number(params[name])
	if err != nil || n < 1 {
		n = 1
	}
	if int(n) < len(primary) {
		return primary[:int(n)]
	}
	return primary
}

// Builds the expression scope: the host's facts and devices, descriptor params, the weights'
// width, run params, cache element sizes, then the arch formulas solved over them
func (p *Policy) env(in Input) (map[string]any, error) {
	env := host.Env(in.Host)
	for k, v := range in.Descriptor.GetParams() {
		env[k] = v
	}
	env["bits_per_weight"] = in.Descriptor.GetBitsPerWeight()
	for k, v := range in.Params {
		env[k] = v
	}
	cache := map[string]any{}
	for k, v := range p.spec.GetCacheElementBytes() {
		cache[k] = v
	}
	env["cache_bytes"] = cache
	if err := eval.Solve(in.Formulas, env); err != nil {
		return nil, err
	}
	return env, nil
}

func (p *Policy) eval(e *eval.Expr, name string, env map[string]any) (uint64, error) {
	if e == nil {
		return 0, nil
	}
	v, err := e.Float(env)
	if err != nil {
		return 0, eval.Explain(e, name, env, err)
	}
	if math.IsNaN(v) || v < 0 {
		return 0, fmt.Errorf("%s: invalid result %v", e.Source(), v)
	}
	return uint64(v), nil
}

// The kinds these params leave on disk: every group whose when clause does not hold
func (p *Policy) unloaded(env map[string]any) (map[v1.TensorGroupKind]bool, error) {
	out := map[v1.TensorGroupKind]bool{}
	for kind, e := range p.when {
		ok, err := e.Bool(env)
		if err != nil {
			return nil, eval.Explain(e, eval.EnumShort(kind)+" when", env, err)
		}
		if !ok {
			out[kind] = true
		}
	}
	return out, nil
}

func (b *bucket) prepare(params map[string]any) {
	sort.SliceStable(b.items, func(i, j int) bool { return rank(b.items[i]) > rank(b.items[j]) })
	b.prefix = make([]uint64, len(b.items)+1)
	for i, it := range b.items {
		b.prefix[i+1] = b.prefix[i] + it.bytes()
		b.kinds[it.kind] = append(b.kinds[it.kind], i)
	}
	if v, ok := params[b.policy.GetParam()]; ok {
		if n, err := eval.Number(v); err == nil {
			count := int(n)
			if b.policy.GetParamCountsHost() {
				count = len(b.items) - count
			}
			b.fixed = min(max(count, 0), len(b.items))
		}
	}
}

// Counts items of a kind among the first n items
func (b *bucket) kindCount(kind v1.TensorGroupKind, n int) int {
	c := 0
	for _, idx := range b.kinds[kind] {
		if idx < n {
			c++
		}
	}
	return c
}

// The value the param reports for a count of items on device
func (b *bucket) solved(count int) string {
	if b.policy.GetParamCountsHost() {
		count = len(b.items) - count
	}
	return strconv.Itoa(count)
}

func rank(it item) int64 {
	if it.layer < 0 {
		return math.MaxInt32
	}
	return int64(it.layer)
}

func (s *solver) solve(bi int) bool {
	if bi == len(s.buckets) {
		return s.fits()
	}
	b := s.buckets[bi]
	upper := len(b.items)
	if req := b.policy.GetRequires(); req != v1.TensorGroupKind_TENSOR_GROUP_KIND_UNSPECIFIED {
		if parent, ok := s.byKind[req]; ok && parent != b {
			upper = min(upper, parent.kindCount(req, parent.count))
		}
	}
	lower := 0
	if b.fixed >= 0 {
		upper, lower = min(b.fixed, upper), min(b.fixed, upper)
	}
	for n := upper; n >= lower; n-- {
		b.count = n
		if s.solve(bi + 1) {
			return true
		}
	}
	return false
}

func (s *solver) devNeed() uint64 {
	need := s.fixedDev
	for _, b := range s.buckets {
		need += b.prefix[b.count]
	}
	return need
}

func (s *solver) hostNeed() uint64 {
	need := s.fixedHost
	for _, b := range s.buckets {
		need += b.prefix[len(b.items)] - b.prefix[b.count]
	}
	return need
}

func (s *solver) fits() bool {
	if s.devNeed() > s.devCap {
		return false
	}
	return s.hostCap == 0 || s.hostNeed() <= s.hostCap
}

// Writes a solved plan: each side's need spread over its pools by size, and every group on the side the solver put it
func (s *solver) fill(plan *v1.MemoryPlan, primary, host []*v1.MemoryPool) {
	plan.Pools = append(distribute(s.devNeed(), primary, s.free), distribute(s.hostNeed(), host, s.free)...)
	for _, b := range s.buckets {
		plan.Params[b.policy.GetParam()] = b.solved(b.count)
	}
	agg := newPlacements()
	for _, it := range s.pinned {
		agg.add(it.kind, v1.PoolKind_POOL_KIND_DEVICE, it.weights, 1)
	}
	for _, it := range s.hosted {
		agg.add(it.kind, v1.PoolKind_POOL_KIND_HOST, it.weights, 1)
	}
	for _, b := range s.buckets {
		for i, it := range b.items {
			pool := v1.PoolKind_POOL_KIND_DEVICE
			if i >= b.count {
				pool = v1.PoolKind_POOL_KIND_HOST
			}
			agg.add(it.kind, pool, it.weights, 1)
		}
	}
	plan.Placements = agg.list()
}

// One pool as the layout fills it
type slot struct {
	pool *v1.MemoryPool
	side v1.PoolKind
	cap  uint64
	used uint64
}

// Lays a plan out the way the host would take it when no fit exists: device pools fill to the
// brim in order, the rest flows on into host memory, and whatever no pool holds overflows past
// the last pool's capacity, so the pools themselves say how far short the host falls. Groups
// go in the order the solver protects them, overhead and pinned kinds first, then offloadable
// kinds by spill priority, with kinds the policy keeps in host memory taking host pools alone.
func (s *solver) waterfall(plan *v1.MemoryPlan, primary, host []*v1.MemoryPool) {
	var chain []*slot
	seen := map[string]bool{}
	for _, pl := range primary {
		if !seen[pl.GetId()] {
			seen[pl.GetId()] = true
			chain = append(chain, &slot{pool: pl, side: v1.PoolKind_POOL_KIND_DEVICE, cap: capacity(pl, s.free)})
		}
	}
	hostStart := len(chain)
	for _, pl := range host {
		if !seen[pl.GetId()] {
			seen[pl.GetId()] = true
			chain = append(chain, &slot{pool: pl, side: v1.PoolKind_POOL_KIND_HOST, cap: capacity(pl, s.free)})
		}
	}
	// A unified pool is both sides at once, so host kinds start where every kind does
	if hostStart == len(chain) {
		hostStart = 0
	}
	agg := newPlacements()
	cur := 0
	// Places one group from the cursor onward, saying which side its first byte landed on
	place := func(it item, from int) v1.PoolKind {
		if cur < from {
			cur = from
		}
		remaining := it.bytes()
		first := v1.PoolKind_POOL_KIND_UNSPECIFIED
		count := uint32(1)
		left := it.weights
		// A group split across pools places its weights in the same shares, the last piece taking the rounding
		record := func(side v1.PoolKind, taken uint64) {
			if first == v1.PoolKind_POOL_KIND_UNSPECIFIED {
				first = side
			}
			share := left
			if taken < remaining {
				share = it.weights * taken / it.bytes()
			}
			left -= share
			if it.kind != v1.TensorGroupKind_TENSOR_GROUP_KIND_UNSPECIFIED {
				agg.add(it.kind, side, share, count)
			}
			count = 0
		}
		for remaining > 0 && len(chain) > 0 {
			if cur >= len(chain) {
				last := chain[len(chain)-1]
				last.used += remaining
				record(last.side, remaining)
				break
			}
			sl := chain[cur]
			if sl.used >= sl.cap {
				cur++
				continue
			}
			take := min(sl.cap-sl.used, remaining)
			sl.used += take
			record(sl.side, take)
			remaining -= take
		}
		return first
	}
	place(item{cache: s.overhead}, 0)
	for _, it := range s.pinned {
		place(it, 0)
	}
	for _, b := range s.buckets {
		b.count = 0
		for _, it := range b.items {
			if place(it, 0) == v1.PoolKind_POOL_KIND_DEVICE {
				b.count++
			}
		}
		plan.Params[b.policy.GetParam()] = b.solved(b.count)
	}
	for _, it := range s.hosted {
		place(it, hostStart)
	}
	for _, sl := range chain {
		plan.Pools = append(plan.Pools, &v1.PoolUsage{PoolId: sl.pool.GetId(), Kind: sl.pool.GetKind(), UsedBytes: sl.used, CapacityBytes: sl.cap})
	}
	plan.Placements = agg.list()
}

// Placements summed by kind and side, in the order they were first seen then sorted
type placements struct {
	agg   map[[2]int32]*v1.Placement
	order [][2]int32
}

func newPlacements() *placements {
	return &placements{agg: map[[2]int32]*v1.Placement{}}
}

func (p *placements) add(kind v1.TensorGroupKind, pool v1.PoolKind, bytes uint64, count uint32) {
	k := [2]int32{int32(kind), int32(pool)}
	pl, ok := p.agg[k]
	if !ok {
		pl = &v1.Placement{Kind: kind}
		if pool != v1.PoolKind_POOL_KIND_UNSPECIFIED {
			pl.PoolId = eval.EnumShort(pool)
		}
		p.agg[k] = pl
		p.order = append(p.order, k)
	}
	pl.Bytes += bytes
	pl.Count += count
}

func (p *placements) list() []*v1.Placement {
	sort.SliceStable(p.order, func(i, j int) bool {
		if p.order[i][0] != p.order[j][0] {
			return p.order[i][0] < p.order[j][0]
		}
		return p.order[i][1] < p.order[j][1]
	})
	out := make([]*v1.Placement, 0, len(p.order))
	for _, k := range p.order {
		out = append(out, p.agg[k])
	}
	return out
}

func distribute(need uint64, into []*v1.MemoryPool, free bool) []*v1.PoolUsage {
	var out []*v1.PoolUsage
	remaining := need
	var total uint64
	for _, pl := range into {
		total += capacity(pl, free)
	}
	total = max(total, 1)
	for _, pl := range into {
		share := min(uint64(float64(need)*float64(capacity(pl, free))/float64(total)), remaining)
		remaining -= share
		out = append(out, &v1.PoolUsage{PoolId: pl.GetId(), Kind: pl.GetKind(), UsedBytes: share, CapacityBytes: capacity(pl, free)})
	}
	if remaining > 0 && len(out) > 0 {
		out[len(out)-1].UsedBytes += remaining
	}
	return out
}

// Uses free bytes when asked and known, else total
func capacity(pl *v1.MemoryPool, free bool) uint64 {
	if free && pl.GetFreeBytes() > 0 {
		return pl.GetFreeBytes()
	}
	return pl.GetTotalBytes()
}

// Measurement key for device memory taken by a running instance
const DeviceUsedKey = "device.used"

// Sums bytes planned onto device class pools
func PlannedDevice(plan *v1.MemoryPlan) uint64 {
	var total uint64
	for _, pu := range plan.GetPools() {
		if pu.GetKind() == v1.PoolKind_POOL_KIND_DEVICE || pu.GetKind() == v1.PoolKind_POOL_KIND_UNIFIED {
			total += pu.GetUsedBytes()
		}
	}
	return total
}

// Sums bytes planned into every pool
func need(plan *v1.MemoryPlan) uint64 {
	var total uint64
	for _, pu := range plan.GetPools() {
		total += pu.GetUsedBytes()
	}
	return total
}

// Sums the capacity of every pool the plan touches
func held(plan *v1.MemoryPlan) uint64 {
	var total uint64
	for _, pu := range plan.GetPools() {
		total += pu.GetCapacityBytes()
	}
	return total
}

// Sums bytes planned past what their pools hold, the shortfall a host cannot make up
func overflow(plan *v1.MemoryPlan) uint64 {
	var total uint64
	for _, pu := range plan.GetPools() {
		if pu.GetUsedBytes() > pu.GetCapacityBytes() {
			total += pu.GetUsedBytes() - pu.GetCapacityBytes()
		}
	}
	return total
}

func pools(h *v1.HostProfile) (primary, host []*v1.MemoryPool) {
	var unified []*v1.MemoryPool
	for _, pl := range h.GetPools() {
		switch pl.GetKind() {
		case v1.PoolKind_POOL_KIND_DEVICE:
			primary = append(primary, pl)
		case v1.PoolKind_POOL_KIND_HOST:
			host = append(host, pl)
		case v1.PoolKind_POOL_KIND_UNIFIED:
			unified = append(unified, pl)
		}
	}
	if len(primary) == 0 {
		primary = unified
	}
	if len(host) == 0 {
		host = unified
	}
	sort.SliceStable(primary, func(i, j int) bool { return primary[i].GetTotalBytes() > primary[j].GetTotalBytes() })
	return primary, host
}

func poolOf(gp *v1.GroupPolicy) v1.PoolKind {
	if gp == nil || gp.GetPool() == v1.PoolKind_POOL_KIND_UNSPECIFIED {
		return v1.PoolKind_POOL_KIND_DEVICE
	}
	return gp.GetPool()
}

func stringParams(params map[string]any) map[string]string {
	out := make(map[string]string, len(params))
	for k, v := range params {
		out[k] = fmt.Sprint(v)
	}
	return out
}

// Formats bytes for humans
func Human(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGTPE"[exp])
}
