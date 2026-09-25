// Package estimate plans model memory across host and device pools.
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
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Placeholder for parameters resolved by the planner.
const Auto = "auto"

// Invalid parameter combination.
var ErrRefused = errors.New("params refused")

// Resolved parameters: int64, float64, bool, string, or Auto.
type Params map[string]any

// Integer value, zero if absent or auto.
func (p Params) Int(name string) int64 {
	switch v := p[name].(type) {
	case int64:
		return v
	case float64:
		return int64(v)
	}
	return 0
}

// Numeric value, zero if absent or auto.
func (p Params) Float(name string) float64 {
	switch v := p[name].(type) {
	case int64:
		return float64(v)
	case float64:
		return v
	}
	return 0
}

// String value, empty if absent.
func (p Params) Str(name string) string {
	if v, ok := p[name].(string); ok {
		return v
	}
	return ""
}

// Boolean value, false if absent.
func (p Params) Bool(name string) bool {
	v, _ := p[name].(bool)
	return v
}

// Reports whether a parameter needs resolution.
func (p Params) IsAuto(name string) bool {
	return p[name] == Auto
}

func (p Params) Clone() Params {
	out := make(Params, len(p)+1)
	for k, v := range p {
		out[k] = v
	}
	return out
}

// Inputs to policy rules, including parameters resolved so far.
type Scope struct {
	Descriptor *v1.Descriptor
	Model      formats.Params
	Host       *v1.HostProfile
	Params     Params
	// Cache elements per context token for this run.
	CachePerToken float64
	// Other stored models available as companions.
	Companions []*v1.StoredModel
	// Source repository, preferred when selecting companions.
	Repo string
}

// Placement rule for one tensor group kind
type GroupRule struct {
	Kind v1.TensorGroupKind
	Pool v1.PoolKind
	// Parameter counting groups on device. Empty pins the kind to its pool.
	Param string
	// Whether the parameter counts groups on host instead.
	ParamCountsHost bool
	// Higher values offload first.
	SpillPriority uint32
	// Kind whose device count limits this kind, such as layers limiting experts.
	Requires v1.TensorGroupKind
	// Loading condition, nil for unconditional loading.
	Loaded func(s *Scope) bool
}

// Per shape cost facts a runtime declares, what the formation planner prices links with
type ShapeFacts struct {
	// Whether a chain passes activations rank to rank in a ring, or the head carries every crossing in a star
	Ring bool
	// Bytes per hidden unit an activation moves across a link, 4 for float32 and 2 for half precision
	ActivationBytes float64
	// Prompt chunks a chain keeps in flight, dividing the boundary term of prefill
	ChunksInFlight float64
	// Whether relay streams the cache layer by layer while later layers compute
	RelayOverlap bool
	// Whether a relay moves the cache over the RDMA device when the link has one, taking the aggregate bandwidth
	RelayRDMA bool
	// Whether relay writes the cache to disk on one seat and reads it on the other
	RelayDisk bool
	// Reductions per layer per token in lockstep
	ReductionsPerLayer float64
	// Whether a chain's transport takes a link's aggregate bandwidth, as a collective over many connections or
	// the RDMA device does, or one stream, as one TCP connection per stage does
	ChainAggregate bool
	// Whether a chain head keeps the request's speculative settings: a draft beside the head composes with the
	// chain when true, and the settings are off in chain when the runtime does not compose them with pipeline parallel
	ChainSpeculative bool
	// The relay handoff the gateway drives between the prefill and decode seats, one of the Handoff constants
	Handoff string
}

// Relay handoffs a runtime declares: vLLM's NIXL connector parameters, SGLang's bootstrap room,
// and llama.cpp's saved slot file
const (
	HandoffNIXL            = "nixl"
	HandoffSGLangBootstrap = "sglang-bootstrap"
	HandoffLlamaSlot       = "llamacpp-slot"
)

// Memory planning rules of one runtime
type Policy struct {
	// Cost facts per shape, nil for a runtime that runs solo only
	Shapes *ShapeFacts
	Groups []GroupRule
	// Cache bytes at the resolved context length.
	CacheBytes func(s *Scope) uint64
	// Memory overhead excluding weights and cache.
	OverheadBytes func(s *Scope) uint64
	// Share of every pool kept free
	Margin float64
	// Context length parameter, empty if not resolved by the planner.
	ContextParam string
	// Allowed context increments.
	ContextMin, ContextStep int64
	// Maximum trained context length, zero if unknown.
	ContextMax func(s *Scope) int64
	// Device count parameter. Limits capacity to the largest selected pools. Empty uses all devices.
	DevicesParam string
	// Context and batch dimensions for cache sizing.
	Shape func(p Params) archs.Run
	// Resolves auto parameters before context sizing. May extend the descriptor with companion groups.
	Solve func(s *Scope)
	// Parameter bounds, excluded choices, and launch refusal under resolved values.
	States func(s *Scope) ([]*v1.ParamState, string)
	// Required companion slots left unresolved. Nil when the runtime has no companion files.
	Missing func(s *Scope) []Missing
}

// Unfilled required companion slot.
type Missing struct {
	// Runtime file parameter.
	Param string
	// Blueprint slot ID.
	Slot string
}

// Planning inputs.
type Input struct {
	Descriptor    *v1.Descriptor
	Family        archs.Arch
	Host          *v1.HostProfile
	Params        Params
	Free          bool
	OverheadDelta float64
	// Returns an estimate with the refusal instead of failing immediately.
	SkipRules bool
	// Allowed memory placement: shared, device only, or host only.
	Placement v1.Placement
	// Other stored models available as companions.
	Companions []*v1.StoredModel
	// Source repository, preferred when selecting companions.
	Repo string
}

// Reports header fields required for memory planning.
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
	return &Scope{Descriptor: in.Descriptor, Model: formats.ParamsOf(in.Descriptor.GetParams()), Host: in.Host, Params: params, Companions: in.Companions, Repo: in.Repo}
}

// Plans model placement and resolves auto parameters, using the largest context that fits.
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

// Finds the largest allowed context that preserves the minimum context's placement verdict, capped
// at the trained length.
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
		return p.plan(in, &Scope{Descriptor: s.Descriptor, Model: s.Model, Host: s.Host, Params: ps, Companions: s.Companions, Repo: s.Repo})
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
	// Also try the ceiling when it falls between increments.
	if l == steps && lo+steps*step < hi {
		if plan, err := at(hi); err == nil && plan.GetVerdict() <= target {
			best = plan
		}
	}
	return best, nil
}

// Parameter bounds, excluded choices, and launch refusal under resolved values.
func (p *Policy) ParamStates(in Input, plan *v1.MemoryPlan) ([]*v1.ParamState, string) {
	if p.States == nil {
		return nil, ""
	}
	return p.States(p.solvedScope(in, plan))
}

// Returns companion slots left unresolved by the plan.
func (p *Policy) MissingParts(in Input, plan *v1.MemoryPlan) []Missing {
	if p.Missing == nil {
		return nil
	}
	return p.Missing(p.solvedScope(in, plan))
}

// Builds the scope with resolved parameter values.
func (p *Policy) solvedScope(in Input, plan *v1.MemoryPlan) *Scope {
	params := in.Params.Clone()
	for k, v := range plan.GetParams() {
		params[k] = parsed(v)
	}
	return p.scope(in, params)
}

// Converts a resolved value to its original parameter type.
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

// Calculates cache elements per token for the run shape.
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

// Tensor group weights and associated cache for placement.
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
	// Allowed memory placement: shared, device only, or host only.
	placement v1.Placement
	// Groups pinned to host or device.
	pinned    []item
	hosted    []item
	fixedDev  uint64
	fixedHost uint64
	devCap    uint64
	hostCap   uint64
}

func (s *solver) hostOnly() bool   { return s.placement == v1.Placement_PLACEMENT_HOST }
func (s *solver) deviceOnly() bool { return s.placement == v1.Placement_PLACEMENT_DEVICE }

// Places overhead on device unless the entire model uses host memory.
func (s *solver) beside(n uint64) {
	if s.hostOnly() {
		s.fixedHost += n
	} else {
		s.fixedDev += n
	}
}

// Applies pinned placement, overridden by host-only placement.
func (s *solver) pin(it item, toHost bool) {
	if toHost || s.hostOnly() {
		s.hosted = append(s.hosted, it)
		s.fixedHost += it.bytes()
		return
	}
	s.pinned = append(s.pinned, it)
	s.fixedDev += it.bytes()
}

func (p *Policy) rule(kind v1.TensorGroupKind) (GroupRule, bool) {
	for _, g := range p.Groups {
		if g.Kind == kind {
			return g, true
		}
	}
	return GroupRule{}, false
}

// Plans resolved parameters. Skips cache sizing when the policy has no cache.
func (p *Policy) plan(in Input, s *Scope) (*v1.MemoryPlan, error) {
	if p.CacheBytes != nil {
		if err := p.perToken(in, s); err != nil {
			return nil, err
		}
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
	// A host with no device memory holds everything in host memory
	placement := in.Placement
	if placement == v1.Placement_PLACEMENT_UNSPECIFIED && len(primary) == 0 {
		placement = v1.Placement_PLACEMENT_HOST
	}
	if placement == v1.Placement_PLACEMENT_HOST {
		primary = nil
	}
	margin := 1 - p.Margin
	sv := &solver{byKind: map[v1.TensorGroupKind]*bucket{}, free: in.Free, overhead: overhead, placement: placement}
	for _, pl := range primary {
		sv.devCap += uint64(float64(capacity(pl, in.Free)) * margin)
	}
	for _, pl := range host {
		sv.hostCap += uint64(float64(capacity(pl, in.Free)) * margin)
	}
	sv.beside(overhead)
	layers := 0
	for _, g := range s.Descriptor.GetGroups() {
		if g.GetKind() == v1.TensorGroupKind_TENSOR_GROUP_KIND_LAYER && !unloaded[g.GetKind()] {
			layers++
		}
	}
	// Cache follows its layers, or the full weight group for unlayered models.
	var cachePerLayer uint64
	if layers > 0 {
		cachePerLayer = cacheTotal / uint64(layers)
	} else {
		sv.beside(cacheTotal)
	}
	byParam := map[string]*bucket{}
	skipped := newPlacements()
	var weights uint64
	for _, g := range s.Descriptor.GetGroups() {
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
			sv.pin(it, known && rule.Pool == v1.PoolKind_POOL_KIND_HOST)
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
		PlannedAt:     plannedAt(in.Host),
	}
	// Preserve invalid explicit placement in the estimate and report its refusal.
	if detail := sv.contradiction(); detail != "" {
		plan.Verdict = v1.FitVerdict_FIT_VERDICT_NO
		plan.Detail = detail
		typed := plan.Params
		plan.Params = stringParams(s.Params)
		sv.waterfall(plan, primary, host)
		plan.Params = typed
		return plan, nil
	}
	if sv.hostOnly() && len(sv.buckets) == 0 {
		plan.Verdict = v1.FitVerdict_FIT_VERDICT_NO
		plan.Detail = "the runtime keeps the whole model in device memory and cannot run in host memory"
		sv.waterfall(plan, primary, host)
		return plan, nil
	}
	if (len(primary) > 0 || sv.hostOnly()) && sv.solve(0) {
		plan.Verdict = v1.FitVerdict_FIT_VERDICT_FITS
		for _, b := range sv.buckets {
			if !sv.hostOnly() && b.count < len(b.items) {
				plan.Verdict = v1.FitVerdict_FIT_VERDICT_PARTIAL
			}
		}
		sv.fill(plan, primary, host)
		return plan, nil
	}
	plan.Verdict = v1.FitVerdict_FIT_VERDICT_NO
	// Record minimum memory requirements before changing group counts.
	for _, b := range sv.buckets {
		switch {
		case sv.hostOnly():
			b.count = 0
		case sv.deviceOnly():
			b.count = len(b.items)
		default:
			b.count = max(b.fixed, 0)
		}
	}
	least, most := sv.devNeed(), sv.hostNeed()
	sv.waterfall(plan, primary, host)
	switch {
	case sv.hostOnly() && sv.hostCap == 0:
		plan.Detail = "no host memory pools probed"
	case sv.hostOnly():
		plan.Detail = fmt.Sprintf("needs %s in host memory, %s more than the %s it holds", Human(most), Human(most-sv.hostCap), Human(sv.hostCap))
	case len(primary) == 0:
		plan.Detail = "no device memory pools probed"
	case sv.deviceOnly() && least > sv.devCap:
		plan.Detail = fmt.Sprintf("needs %s in device memory, %s more than the %s it holds", Human(least), Human(least-sv.devCap), Human(sv.devCap))
	case overflow(plan) > 0:
		plan.Detail = fmt.Sprintf("needs %s, %s more than the %s of memory on this host", Human(need(plan)), Human(overflow(plan)), Human(held(plan)))
	case sv.hostCap > 0 && most > sv.hostCap:
		plan.Detail = fmt.Sprintf("host need %s exceeds capacity %s", Human(most), Human(sv.hostCap))
	default:
		plan.Detail = fmt.Sprintf("device need %s exceeds capacity %s", Human(least), Human(sv.devCap))
	}
	return plan, nil
}

// Reports explicit parameters that conflict with placement.
func (s *solver) contradiction() string {
	for _, b := range s.buckets {
		if b.fixed < 0 {
			continue
		}
		switch {
		case s.hostOnly() && b.fixed > 0:
			return fmt.Sprintf("%s %s puts weights on the device, but the slot keeps the model in host memory", b.rule.Param, b.solved(b.fixed))
		case s.deviceOnly() && b.fixed < len(b.items):
			return fmt.Sprintf("%s %s leaves weights in host memory, but the slot keeps the model on the device", b.rule.Param, b.solved(b.fixed))
		}
	}
	return ""
}

// Selects the largest pools allowed by the device count parameter.
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

// Converts the device group count to the parameter value.
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

// Searches allowed device counts, largest first.
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
	switch {
	case s.hostOnly():
		upper = 0
	case s.deviceOnly():
		lower = upper
	}
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

// Checks host and device capacity. Unknown host capacity is unconstrained unless all weights use
// host memory.
func (s *solver) fits() bool {
	if s.devNeed() > s.devCap {
		return false
	}
	if s.hostCap == 0 {
		return !s.hostOnly()
	}
	return s.hostNeed() <= s.hostCap
}

// Builds the plan with groups assigned to pools in proportion to capacity.
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

// Builds an overflow layout when no fit exists. Fills device pools before host pools, preserving
// pinned kinds and spill priority. Excess bytes remain on the last allowed pool to report the
// shortfall.
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
	// Unified memory shares the starting pool for host and device groups.
	if hostStart == len(chain) {
		hostStart = 0
	}
	spillEnd := len(chain)
	if s.deviceOnly() && hostStart > 0 {
		spillEnd = hostStart
	}
	agg := newPlacements()
	cur := 0
	// Places a group across pools from the cursor and returns its starting side.
	place := func(it item, from, end int) v1.PoolKind {
		if cur < from {
			cur = from
		}
		remaining := it.bytes()
		first := v1.PoolKind_POOL_KIND_UNSPECIFIED
		count := uint32(1)
		left := it.weights
		// Split weights proportionally and assign rounding to the last piece.
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
		for remaining > 0 && end > 0 {
			if cur >= end {
				last := chain[end-1]
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
	place(item{cache: s.overhead}, 0, spillEnd)
	for _, it := range s.pinned {
		place(it, 0, spillEnd)
	}
	for _, b := range s.buckets {
		b.count = 0
		for _, it := range b.items {
			if place(it, 0, spillEnd) == v1.PoolKind_POOL_KIND_DEVICE {
				b.count++
			}
		}
		plan.Params[b.rule.Param] = b.solved(b.count)
	}
	for _, it := range s.hosted {
		place(it, hostStart, len(chain))
	}
	for _, sl := range chain {
		plan.Pools = append(plan.Pools, usage(sl.pool, sl.used, sl.cap))
	}
	plan.Placements = agg.list()
}

// Sums placements by kind and side, then sorts them.
type placements struct {
	agg   map[[2]int32]*v1.GroupPlacement
	order [][2]int32
}

func newPlacements() *placements {
	return &placements{agg: map[[2]int32]*v1.GroupPlacement{}}
}

func (p *placements) add(kind v1.TensorGroupKind, pool v1.PoolKind, bytes uint64, count uint32) {
	k := [2]int32{int32(kind), int32(pool)}
	pl, ok := p.agg[k]
	if !ok {
		pl = &v1.GroupPlacement{Kind: kind}
		if pool != v1.PoolKind_POOL_KIND_UNSPECIFIED {
			pl.PoolId = text.Enum(pool)
		}
		p.agg[k] = pl
		p.order = append(p.order, k)
	}
	pl.Bytes += bytes
	pl.Count += count
}

func (p *placements) list() []*v1.GroupPlacement {
	sort.SliceStable(p.order, func(i, j int) bool {
		if p.order[i][0] != p.order[j][0] {
			return p.order[i][0] < p.order[j][0]
		}
		return p.order[i][1] < p.order[j][1]
	})
	out := make([]*v1.GroupPlacement, 0, len(p.order))
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
		out = append(out, usage(pl, share, capacity(pl, free)))
	}
	if remaining > 0 && len(out) > 0 {
		out[len(out)-1].UsedBytes += remaining
	}
	return out
}

// Pool capacity, free memory, and planned usage for display.
func usage(pl *v1.MemoryPool, used, cap uint64) *v1.PoolUsage {
	return &v1.PoolUsage{PoolId: pl.GetId(), Kind: pl.GetKind(), UsedBytes: used, CapacityBytes: cap, TotalBytes: pl.GetTotalBytes(), FreeBytes: pl.GetFreeBytes()}
}

// Profile timestamp, defaulting to now if absent.
func plannedAt(h *v1.HostProfile) *timestamppb.Timestamp {
	if at := h.GetProbedAt(); at != nil {
		return at
	}
	return timestamppb.Now()
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

// Sums memory shortfalls across pools.
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
