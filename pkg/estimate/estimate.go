// Package estimate plans model memory onto host pools.
package estimate

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"

	"github.com/nickheyer/nebu/pkg/archs"
	"github.com/nickheyer/nebu/pkg/formats"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/text"
)

// Value a solved param takes before planning
const Auto = "auto"

// Returned when the params name a choice the runtime rules out under the other params
var ErrRefused = errors.New("params refused")

// Run params as a run resolved them, each an int64, float64, bool, string, or Auto
type Params map[string]any

// The whole number a param holds, zero while absent or auto
func (p Params) Int(name string) int64 {
	switch v := p[name].(type) {
	case int64:
		return v
	case float64:
		return int64(v)
	}
	return 0
}

// The number a param holds, zero while absent or auto
func (p Params) Float(name string) float64 {
	switch v := p[name].(type) {
	case int64:
		return float64(v)
	case float64:
		return v
	}
	return 0
}

// The text a param holds, empty while absent
func (p Params) Str(name string) string {
	if v, ok := p[name].(string); ok {
		return v
	}
	return ""
}

// The flag a param holds, false while absent
func (p Params) Bool(name string) bool {
	v, _ := p[name].(bool)
	return v
}

// Whether a param waits for the planner to pick it
func (p Params) IsAuto(name string) bool {
	return p[name] == Auto
}

// A copy the caller may change
func (p Params) Clone() Params {
	out := make(Params, len(p)+1)
	for k, v := range p {
		out[k] = v
	}
	return out
}

// What a policy's rules read: the model, the host, and the run params with the autos solved so far
type Scope struct {
	Descriptor *v1.Descriptor
	Model      formats.Params
	Host       *v1.HostProfile
	Params     Params
	// Cache elements per token of context, from the model's attention family at this run's shape
	CachePerToken float64
}

// Placement rule for one tensor group kind
type GroupRule struct {
	Kind v1.TensorGroupKind
	Pool v1.PoolKind
	// The run param counting how many of the kind sit on device, empty when the kind is pinned to its pool
	Param string
	// Whether the param counts the ones on the host instead
	ParamCountsHost bool
	// Higher spills to the host first when the device is short
	SpillPriority uint32
	// A kind that cannot outnumber another on device, experts following their layers
	Requires v1.TensorGroupKind
	// Whether the kind loads under these params, nil meaning always; a draft head loads only once a run drafts with it
	Loaded func(s *Scope) bool
}

// Memory planning rules of one runtime
type Policy struct {
	Groups []GroupRule
	// Bytes of cache at this run's context, from the family's cost per token
	CacheBytes func(s *Scope) uint64
	// Bytes the runtime holds beside weights and cache
	OverheadBytes func(s *Scope) uint64
	// Share of every pool kept free
	Margin float64
	// The param holding context length, empty for a runtime whose context the planner never solves
	ContextParam string
	// The grid an auto context is solved on
	ContextMin, ContextStep int64
	// The most context the model was trained for, the ceiling an auto context solves under, zero for none
	ContextMax func(s *Scope) int64
	// The param counting the devices a run spans, so device capacity is that many of the largest pools; empty means every device
	DevicesParam string
	// The context and batch shape the family sizes the cache by
	Shape func(p Params) archs.Run
	// Gives every auto param but the context its value, in order, before the context is solved
	Solve func(s *Scope)
	// Every param's bounds and ruled out choices under the params, and why a run would be refused
	States func(s *Scope) ([]*v1.ParamState, string)
}

// Everything a plan needs
type Input struct {
	Descriptor    *v1.Descriptor
	Family        archs.Arch
	Host          *v1.HostProfile
	Params        Params
	Free          bool
	OverheadDelta float64
	// Plans through a choice the rules refuse instead of failing, for an estimate that reports the refusal
	SkipRules bool
}

// Says which facts the header lacked, the one failure that means the model rather than the policy is short
type MissingError struct {
	Names []string
}

func (e *MissingError) Error() string {
	return fmt.Sprintf("the header gives no %s", joinOr(e.Names))
}

func joinOr(names []string) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	}
	out := ""
	for i, n := range names {
		switch {
		case i == 0:
			out = n
		case i == len(names)-1:
			out += " or " + n
		default:
			out += ", " + n
		}
	}
	return out
}

func (p *Policy) scope(in Input, params Params) *Scope {
	return &Scope{Descriptor: in.Descriptor, Model: formats.ParamsOf(in.Descriptor.GetParams()), Host: in.Host, Params: params}
}

// Plans the descriptor on the host, solving offload params, the params the runtime solves, and an
// auto context length, which takes the largest that still fits
func (p *Policy) Plan(in Input) (*v1.MemoryPlan, error) {
	s := p.scope(in, in.Params.Clone())
	if p.Solve != nil {
		p.Solve(s)
	}
	if !in.SkipRules && p.States != nil {
		if _, refusal := p.States(s); refusal != "" {
			return nil, fmt.Errorf("%w: %s", ErrRefused, refusal)
		}
	}
	if p.ContextParam != "" && s.Params.IsAuto(p.ContextParam) {
		return p.solveContext(in, s)
	}
	return p.plan(in, s)
}

// Picks the largest context on the param's grid, up to the model's own, that keeps the verdict the
// smallest context earns: a model that fits whole stays whole, one that spills stays on the host
func (p *Policy) solveContext(in Input, s *Scope) (*v1.MemoryPlan, error) {
	step := p.ContextStep
	if step <= 0 {
		step = 1
	}
	lo := p.ContextMin
	if lo <= 0 {
		lo = step
	}
	hi := lo
	if p.ContextMax != nil {
		hi = p.ContextMax(s)
	}
	if hi < lo {
		hi = lo
	}
	at := func(n int64) (*v1.MemoryPlan, error) {
		ps := s.Params.Clone()
		ps[p.ContextParam] = n
		return p.plan(in, &Scope{Descriptor: s.Descriptor, Model: s.Model, Host: s.Host, Params: ps})
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
// would be refused, read with the solved values in place
func (p *Policy) ParamStates(in Input, plan *v1.MemoryPlan) ([]*v1.ParamState, string) {
	if p.States == nil {
		return nil, ""
	}
	params := in.Params.Clone()
	for k, v := range plan.GetParams() {
		params[k] = parsed(v)
	}
	return p.States(p.scope(in, params))
}

// A solved param value read back into the type it was planned as
func parsed(v string) any {
	switch v {
	case Auto:
		return Auto
	case "true":
		return true
	case "false":
		return false
	}
	if n, err := strconv.ParseInt(v, 10, 64); err == nil {
		return n
	}
	if f, err := strconv.ParseFloat(v, 64); err == nil {
		return f
	}
	return v
}

// Sizes the cache the family keeps per token at this run's shape
func (p *Policy) perToken(in Input, s *Scope) error {
	if in.Family == nil {
		return &MissingError{Names: []string{"an attention family"}}
	}
	var run archs.Run
	if p.Shape != nil {
		run = p.Shape(s.Params)
	}
	per, err := in.Family.CachePerToken(s.Model, run)
	if err != nil {
		var needs *archs.Needs
		if errors.As(err, &needs) {
			return &MissingError{Names: needs.Names}
		}
		return err
	}
	s.CachePerToken = per
	return nil
}

// One tensor group as the planner moves it: its weights, and the share of the cache that follows it
type item struct {
	kind    v1.TensorGroupKind
	layer   int32
	weights uint64
	cache   uint64
}

func (it item) bytes() uint64 { return it.weights + it.cache }

type bucket struct {
	rule   GroupRule
	items  []item
	prefix []uint64
	kinds  map[v1.TensorGroupKind][]int
	fixed  int
	count  int
}

type solver struct {
	buckets  []*bucket
	byKind   map[v1.TensorGroupKind]*bucket
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

func (p *Policy) rule(kind v1.TensorGroupKind) (GroupRule, bool) {
	for _, g := range p.Groups {
		if g.Kind == kind {
			return g, true
		}
	}
	return GroupRule{}, false
}

// Plans with every param concrete
func (p *Policy) plan(in Input, s *Scope) (*v1.MemoryPlan, error) {
	if err := p.perToken(in, s); err != nil {
		return nil, err
	}
	var cacheTotal, overhead uint64
	if p.CacheBytes != nil {
		cacheTotal = p.CacheBytes(s)
	}
	if p.OverheadBytes != nil {
		overhead = p.OverheadBytes(s)
	}
	if in.OverheadDelta != 0 {
		overhead = uint64(max(float64(overhead)+in.OverheadDelta, 0))
	}
	unloaded := map[v1.TensorGroupKind]bool{}
	for _, g := range p.Groups {
		if g.Loaded != nil && !g.Loaded(s) {
			unloaded[g.Kind] = true
		}
	}
	primary, host := pools(in.Host)
	primary = p.spanned(primary, s.Params)
	margin := 1 - p.Margin
	sv := &solver{byKind: map[v1.TensorGroupKind]*bucket{}, free: in.Free, overhead: overhead}
	for _, pl := range primary {
		sv.devCap += uint64(float64(capacity(pl, in.Free)) * margin)
	}
	for _, pl := range host {
		sv.hostCap += uint64(float64(capacity(pl, in.Free)) * margin)
	}
	sv.fixedDev = overhead
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
		sv.fixedDev += cacheTotal
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
		rule, known := p.rule(g.GetKind())
		if !known || rule.Param == "" {
			if known && rule.Pool == v1.PoolKind_POOL_KIND_HOST {
				sv.hosted = append(sv.hosted, it)
				sv.fixedHost += it.bytes()
			} else {
				sv.pinned = append(sv.pinned, it)
				sv.fixedDev += it.bytes()
			}
			continue
		}
		b, ok := byParam[rule.Param]
		if !ok {
			b = &bucket{rule: rule, kinds: map[v1.TensorGroupKind][]int{}, fixed: -1}
			byParam[rule.Param] = b
			sv.buckets = append(sv.buckets, b)
		}
		sv.byKind[g.GetKind()] = b
		b.items = append(b.items, it)
	}
	for _, b := range sv.buckets {
		b.prepare(s.Params)
	}
	sort.SliceStable(sv.buckets, func(i, j int) bool {
		return sv.buckets[i].rule.SpillPriority > sv.buckets[j].rule.SpillPriority
	})
	plan := &v1.MemoryPlan{
		WeightsBytes:  weights,
		CacheBytes:    cacheTotal,
		OverheadBytes: overhead,
		OverheadDelta: in.OverheadDelta,
		Params:        stringParams(s.Params),
		Skipped:       skipped.list(),
		AgainstFree:   in.Free,
	}
	if len(primary) > 0 && sv.solve(0) {
		plan.Verdict = v1.FitVerdict_FIT_VERDICT_FITS
		for _, b := range sv.buckets {
			if b.count < len(b.items) {
				plan.Verdict = v1.FitVerdict_FIT_VERDICT_PARTIAL
			}
		}
		sv.fill(plan, primary, host)
		return plan, nil
	}
	plan.Verdict = v1.FitVerdict_FIT_VERDICT_NO
	// The least the solver could ask of each side, read before the layout below moves the counts
	for _, b := range sv.buckets {
		b.count = max(b.fixed, 0)
	}
	least, most := sv.devNeed(), sv.hostNeed()
	sv.waterfall(plan, primary, host)
	switch {
	case len(primary) == 0:
		plan.Detail = "no device memory pools probed"
	case overflow(plan) > 0:
		plan.Detail = fmt.Sprintf("needs %s, %s more than the %s of memory on this host", Human(need(plan)), Human(overflow(plan)), Human(held(plan)))
	case sv.hostCap > 0 && most > sv.hostCap:
		plan.Detail = fmt.Sprintf("host need %s exceeds capacity %s", Human(most), Human(sv.hostCap))
	default:
		plan.Detail = fmt.Sprintf("device need %s exceeds capacity %s", Human(least), Human(sv.devCap))
	}
	return plan, nil
}

// Keeps the largest pools a run spans when the policy names a param counting them
func (p *Policy) spanned(primary []*v1.MemoryPool, params Params) []*v1.MemoryPool {
	if p.DevicesParam == "" {
		return primary
	}
	n := params.Int(p.DevicesParam)
	if n < 1 {
		n = 1
	}
	if int(n) < len(primary) {
		return primary[:int(n)]
	}
	return primary
}

func (b *bucket) prepare(params Params) {
	sort.SliceStable(b.items, func(i, j int) bool { return rank(b.items[i]) > rank(b.items[j]) })
	b.prefix = make([]uint64, len(b.items)+1)
	for i, it := range b.items {
		b.prefix[i+1] = b.prefix[i] + it.bytes()
		b.kinds[it.kind] = append(b.kinds[it.kind], i)
	}
	if v, ok := params[b.rule.Param]; ok && v != Auto {
		if n, err := text.Number(v); err == nil {
			count := int(n)
			if b.rule.ParamCountsHost {
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
	if b.rule.ParamCountsHost {
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
	if req := b.rule.Requires; req != v1.TensorGroupKind_TENSOR_GROUP_KIND_UNSPECIFIED {
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
		plan.Params[b.rule.Param] = b.solved(b.count)
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
		plan.Params[b.rule.Param] = b.solved(b.count)
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
			pl.PoolId = text.Enum(pool)
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

func stringParams(params Params) map[string]string {
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
