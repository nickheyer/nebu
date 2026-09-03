// Package estimate plans model memory onto host pools.
package estimate

import (
	"fmt"
	"math"
	"sort"
	"strconv"

	"github.com/nickheyer/nebu/pkg/eval"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// Compiled estimate policy of one runtime
type Policy struct {
	spec     *v1.EstimatePolicy
	groups   map[v1.TensorGroupKind]*v1.GroupPolicy
	cache    *eval.Expr
	overhead *eval.Expr
}

// Everything a plan needs
type Input struct {
	Descriptor *v1.Descriptor
	Formulas   map[string]*eval.Expr
	Host       *v1.HostProfile
	Params     map[string]any
}

// Compiles policy expressions
func NewPolicy(spec *v1.EstimatePolicy) (*Policy, error) {
	p := &Policy{spec: spec, groups: map[v1.TensorGroupKind]*v1.GroupPolicy{}}
	for _, g := range spec.GetGroups() {
		p.groups[g.GetKind()] = g
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

type item struct {
	kind  v1.TensorGroupKind
	layer int32
	bytes uint64
}

type bucket struct {
	policy *v1.GroupPolicy
	items  []item
	prefix []uint64
	kinds  map[v1.TensorGroupKind][]int
	fixed  int
	count  int
}

type solver struct {
	buckets   []*bucket
	byKind    map[v1.TensorGroupKind]*bucket
	fixedDev  uint64
	fixedHost uint64
	devCap    uint64
	hostCap   uint64
}

// Plans the descriptor on the host, solving offload params
func (p *Policy) Plan(in Input) (*v1.MemoryPlan, error) {
	env, err := p.env(in)
	if err != nil {
		return nil, err
	}
	cacheTotal, err := p.eval(p.cache, env)
	if err != nil {
		return nil, err
	}
	overhead, err := p.eval(p.overhead, env)
	if err != nil {
		return nil, err
	}
	primary, host := pools(in.Host)
	margin := 1 - p.spec.GetMargin()
	s := &solver{byKind: map[v1.TensorGroupKind]*bucket{}}
	for _, pl := range primary {
		s.devCap += uint64(float64(pl.GetTotalBytes()) * margin)
	}
	for _, pl := range host {
		s.hostCap += uint64(float64(pl.GetTotalBytes()) * margin)
	}
	s.fixedDev = overhead
	layers := countKind(in.Descriptor, v1.TensorGroupKind_TENSOR_GROUP_KIND_LAYER)
	var cachePerLayer uint64
	if layers > 0 {
		cachePerLayer = cacheTotal / uint64(layers)
	}
	byParam := map[string]*bucket{}
	var weights uint64
	for _, g := range in.Descriptor.GetGroups() {
		weights += g.GetBytes()
		bytes := g.GetBytes()
		if g.GetKind() == v1.TensorGroupKind_TENSOR_GROUP_KIND_LAYER {
			bytes += cachePerLayer
		}
		gp := p.groups[g.GetKind()]
		if gp == nil || gp.GetParam() == "" {
			if poolOf(gp) == v1.PoolKind_POOL_KIND_HOST {
				s.fixedHost += bytes
			} else {
				s.fixedDev += bytes
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
		b.items = append(b.items, item{kind: g.GetKind(), layer: g.GetLayer(), bytes: bytes})
	}
	for _, b := range s.buckets {
		b.prepare(in.Params)
	}
	sort.SliceStable(s.buckets, func(i, j int) bool {
		return s.buckets[i].policy.GetSpillPriority() > s.buckets[j].policy.GetSpillPriority()
	})
	plan := &v1.MemoryPlan{
		WeightsBytes:  weights,
		CacheBytes:    cacheTotal,
		OverheadBytes: overhead,
		Params:        stringParams(in.Params),
	}
	if len(primary) == 0 {
		plan.Verdict = v1.FitVerdict_FIT_VERDICT_NO
		plan.Detail = "no device memory pools probed"
		return plan, nil
	}
	if !s.solve(0) {
		plan.Verdict = v1.FitVerdict_FIT_VERDICT_NO
		plan.Detail = fmt.Sprintf("fixed device need %s exceeds capacity %s", human(s.fixedDev), human(s.devCap))
		if s.hostCap > 0 && s.hostNeed() > s.hostCap {
			plan.Detail = fmt.Sprintf("host need %s exceeds capacity %s", human(s.hostNeed()), human(s.hostCap))
		}
		for _, b := range s.buckets {
			b.count = 0
		}
	} else {
		plan.Verdict = v1.FitVerdict_FIT_VERDICT_FITS
		for _, b := range s.buckets {
			if b.count < len(b.items) {
				plan.Verdict = v1.FitVerdict_FIT_VERDICT_PARTIAL
			}
		}
	}
	s.fill(plan, primary, host, in.Descriptor, p.groups)
	return plan, nil
}

func (p *Policy) env(in Input) (map[string]any, error) {
	env := map[string]any{}
	for k, v := range in.Descriptor.GetParams() {
		env[k] = v
	}
	for k, v := range in.Params {
		env[k] = v
	}
	cache := map[string]any{}
	for k, v := range p.spec.GetCacheElementBytes() {
		cache[k] = v
	}
	env["cache_bytes"] = cache
	names := make([]string, 0, len(in.Formulas))
	for name := range in.Formulas {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		v, err := in.Formulas[name].Float(env)
		if err != nil {
			return nil, fmt.Errorf("formula %s: %w", name, err)
		}
		env[name] = v
	}
	return env, nil
}

func (p *Policy) eval(e *eval.Expr, env map[string]any) (uint64, error) {
	if e == nil {
		return 0, nil
	}
	v, err := e.Float(env)
	if err != nil {
		return 0, err
	}
	if math.IsNaN(v) || v < 0 {
		return 0, fmt.Errorf("%s: invalid result %v", e.Source(), v)
	}
	return uint64(v), nil
}

func (b *bucket) prepare(params map[string]any) {
	sort.SliceStable(b.items, func(i, j int) bool { return rank(b.items[i]) > rank(b.items[j]) })
	b.prefix = make([]uint64, len(b.items)+1)
	for i, it := range b.items {
		b.prefix[i+1] = b.prefix[i] + it.bytes
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

func (s *solver) fill(plan *v1.MemoryPlan, primary, host []*v1.MemoryPool, d *v1.Descriptor, policies map[v1.TensorGroupKind]*v1.GroupPolicy) {
	plan.Pools = append(distribute(s.devNeed(), primary), distribute(s.hostNeed(), host)...)
	for _, b := range s.buckets {
		solved := b.count
		if b.policy.GetParamCountsHost() {
			solved = len(b.items) - b.count
		}
		plan.Params[b.policy.GetParam()] = strconv.Itoa(solved)
	}
	type key struct {
		kind v1.TensorGroupKind
		pool v1.PoolKind
	}
	agg := map[key]*v1.Placement{}
	var order []key
	add := func(kind v1.TensorGroupKind, pool v1.PoolKind, bytes uint64) {
		k := key{kind, pool}
		pl, ok := agg[k]
		if !ok {
			pl = &v1.Placement{Kind: kind, PoolId: eval.EnumShort(pool)}
			agg[k] = pl
			order = append(order, k)
		}
		pl.Bytes += bytes
		pl.Count++
	}
	for _, g := range d.GetGroups() {
		gp := policies[g.GetKind()]
		if gp == nil || gp.GetParam() == "" {
			add(g.GetKind(), poolOf(gp), g.GetBytes())
		}
	}
	for _, b := range s.buckets {
		for i, it := range b.items {
			pool := v1.PoolKind_POOL_KIND_DEVICE
			if i >= b.count {
				pool = v1.PoolKind_POOL_KIND_HOST
			}
			add(it.kind, pool, it.bytes)
		}
	}
	sort.SliceStable(order, func(i, j int) bool {
		if order[i].kind != order[j].kind {
			return order[i].kind < order[j].kind
		}
		return order[i].pool < order[j].pool
	})
	for _, k := range order {
		plan.Placements = append(plan.Placements, agg[k])
	}
}

func distribute(need uint64, into []*v1.MemoryPool) []*v1.PoolUsage {
	var out []*v1.PoolUsage
	remaining := need
	total := sumTotal(into)
	for _, pl := range into {
		share := min(uint64(float64(need)*float64(pl.GetTotalBytes())/float64(total)), remaining)
		remaining -= share
		out = append(out, &v1.PoolUsage{PoolId: pl.GetId(), Kind: pl.GetKind(), UsedBytes: share, CapacityBytes: pl.GetTotalBytes()})
	}
	if remaining > 0 && len(out) > 0 {
		out[len(out)-1].UsedBytes += remaining
	}
	return out
}

func sumTotal(pools []*v1.MemoryPool) uint64 {
	var t uint64
	for _, pl := range pools {
		t += pl.GetTotalBytes()
	}
	return max(t, 1)
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

func countKind(d *v1.Descriptor, kind v1.TensorGroupKind) int {
	n := 0
	for _, g := range d.GetGroups() {
		if g.GetKind() == kind {
			n++
		}
	}
	return n
}

func stringParams(params map[string]any) map[string]string {
	out := make(map[string]string, len(params))
	for k, v := range params {
		out[k] = fmt.Sprint(v)
	}
	return out
}

// Formats bytes for humans
func human(b uint64) string {
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

// Formats bytes for humans
func Human(b uint64) string { return human(b) }
