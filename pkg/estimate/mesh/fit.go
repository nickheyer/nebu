package mesh

import (
	"fmt"
	"sort"
	"strings"

	"github.com/nickheyer/nebu/pkg/estimate"
	"github.com/nickheyer/nebu/pkg/formats"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/runtimes"
	"github.com/nickheyer/nebu/pkg/text"
	"google.golang.org/protobuf/proto"
)

// The descriptor split into what every shape assigns: the layers in order with their expert
// groups, and everything else
type parts struct {
	descriptor *v1.Descriptor
	model      formats.Params
	// Layer groups by layer index, each with its expert group when the layer has one
	layers [][]*v1.TensorGroup
	// Groups that belong to no layer: embedding, output, vision, audio, other
	extras []*v1.TensorGroup
	// Share of expert bytes a token reads
	expertShare float64
	// Diffusion parts
	denoiser, encoders, decoder []*v1.TensorGroup
}

func split(d *v1.Descriptor) *parts {
	p := &parts{descriptor: d, model: formats.ParamsOf(d.GetParams()), expertShare: 1}
	if p.model.Experts > 0 && p.model.ExpertsUsed > 0 {
		p.expertShare = p.model.ExpertsUsed / p.model.Experts
	}
	byLayer := map[int32][]*v1.TensorGroup{}
	var indexes []int32
	for _, g := range d.GetGroups() {
		switch g.GetKind() {
		case v1.TensorGroupKind_TENSOR_GROUP_KIND_LAYER, v1.TensorGroupKind_TENSOR_GROUP_KIND_EXPERTS:
			if g.GetLayer() >= 0 {
				if _, ok := byLayer[g.GetLayer()]; !ok {
					indexes = append(indexes, g.GetLayer())
				}
				byLayer[g.GetLayer()] = append(byLayer[g.GetLayer()], g)
				continue
			}
		case v1.TensorGroupKind_TENSOR_GROUP_KIND_DIFFUSION:
			p.denoiser = append(p.denoiser, g)
			continue
		case v1.TensorGroupKind_TENSOR_GROUP_KIND_TEXT_ENCODER:
			p.encoders = append(p.encoders, g)
			continue
		case v1.TensorGroupKind_TENSOR_GROUP_KIND_VAE:
			p.decoder = append(p.decoder, g)
			continue
		}
		p.extras = append(p.extras, g)
	}
	sort.Slice(indexes, func(i, j int) bool { return indexes[i] < indexes[j] })
	for _, i := range indexes {
		p.layers = append(p.layers, byLayer[i])
	}
	return p
}

func (p *parts) layerCount() int { return len(p.layers) }

// Parameters a token touches through a layer range
func (p *parts) readParams(from, to int) float64 {
	var out float64
	for _, layer := range p.layers[from:to] {
		for _, g := range layer {
			if g.GetKind() == v1.TensorGroupKind_TENSOR_GROUP_KIND_EXPERTS {
				out += float64(g.GetElements()) * p.expertShare
			} else {
				out += float64(g.GetElements())
			}
		}
	}
	return out
}

// The extras without the output group, and the output group alone: what a ring's first and last
// ranks hold beside their layers
func (p *parts) splitExtras() (first, last []*v1.TensorGroup) {
	for _, g := range p.extras {
		if g.GetKind() == v1.TensorGroupKind_TENSOR_GROUP_KIND_OUTPUT {
			last = append(last, g)
		} else {
			first = append(first, g)
		}
	}
	return first, last
}

// Bytes and parameters of groups that belong to no layer, the embedding counted with the rest:
// a seat reads its non expert groups whole
func groupTotals(groups []*v1.TensorGroup) (bytes, params float64) {
	for _, g := range groups {
		bytes += float64(g.GetBytes())
		params += float64(g.GetElements())
	}
	return bytes, params
}

// A descriptor holding a layer range and extras, its layer count adjusted so the cache the
// family computes covers the range alone
func (p *parts) sub(from, to int, extras []*v1.TensorGroup) *v1.Descriptor {
	d := proto.Clone(p.descriptor).(*v1.Descriptor)
	d.Groups = nil
	d.TotalBytes = 0
	for _, g := range extras {
		d.Groups = append(d.Groups, g)
		d.TotalBytes += g.GetBytes()
	}
	for _, layer := range p.layers[from:to] {
		for _, g := range layer {
			d.Groups = append(d.Groups, g)
			d.TotalBytes += g.GetBytes()
		}
	}
	if d.Params == nil {
		d.Params = map[string]float64{}
	}
	d.Params["n_layer"] = float64(to - from)
	return d
}

// A descriptor with every group's bytes divided by the seat count, the cache following the key
// value heads a tensor parallel rank holds
func (p *parts) slice(k int) *v1.Descriptor {
	d := proto.Clone(p.descriptor).(*v1.Descriptor)
	d.TotalBytes = 0
	for _, g := range d.Groups {
		g.Bytes /= uint64(k)
		g.Elements /= uint64(k)
		d.TotalBytes += g.GetBytes()
	}
	if d.Params == nil {
		d.Params = map[string]float64{}
	}
	if p.model.KVLoraRank == 0 && p.model.HeadsKV > 0 {
		d.Params["n_head_kv"] = max(1, float64(int(p.model.HeadsKV)/k))
	}
	return d
}

// A descriptor holding only the given groups
func (p *parts) only(groups []*v1.TensorGroup) *v1.Descriptor {
	d := proto.Clone(p.descriptor).(*v1.Descriptor)
	d.Groups = nil
	d.TotalBytes = 0
	for _, g := range groups {
		d.Groups = append(d.Groups, g)
		d.TotalBytes += g.GetBytes()
	}
	return d
}

// One solver answer, kept so the same range on the same node is planned once per pass
type fitResult struct {
	plan *v1.MemoryPlan
	err  error
}

// The planner's working state
type planner struct {
	req   Request
	mesh  *Mesh
	parts *parts
	// Nodes in the span with the runtime installed
	nodes  []*Node
	policy *estimate.Policy
	facts  estimate.ShapeFacts
	// Resolved params, the context set for the pass
	params  estimate.Params
	context int64
	// Why the request cannot use a node
	excluded map[string]string
	// Every number the plan priced with, and the devices whose numbers came from the profile table
	sources  []string
	seen     map[string]bool
	profiled []string
	// Assignments a shape could not make, each once
	rejected     []*candidate
	rejectedSeen map[string]bool
	// Solver answers by node, range, extras, placement, and context
	fits map[string]fitResult
}

// Plans a descriptor on one node with the node's own profile and correction, under the runtime's
// rules: a refused param is an error naming the rule
func (pl *planner) plan(n *Node, d *v1.Descriptor, params estimate.Params, placement v1.Placement) (*v1.MemoryPlan, error) {
	in := estimate.Input{
		Descriptor: d,
		Family:     pl.req.Family,
		Host:       n.Profile,
		Params:     params,
		Free:       pl.req.Free,
		Placement:  placement,
		Companions: pl.req.Companions,
		Repo:       pl.req.Repo,
	}
	if pl.req.Calibration != nil {
		in.OverheadDelta = pl.req.Calibration(n.ID)
	}
	return pl.policy.Plan(in)
}

// Plans a layer range with extras on a node at the pass's context, once per distinct request
func (pl *planner) fit(n *Node, from, to int, extras []*v1.TensorGroup, placement v1.Placement) (*v1.MemoryPlan, error) {
	key := fmt.Sprintf("%s|%d|%d|%s|%d|%d", n.ID, from, to, groupIDs(extras), placement, pl.context)
	if r, ok := pl.fits[key]; ok {
		return r.plan, r.err
	}
	plan, err := pl.plan(n, pl.parts.sub(from, to, extras), pl.fixed(), placement)
	pl.fits[key] = fitResult{plan, err}
	return plan, err
}

// Plans any descriptor on a node at the pass's context, once per distinct request
func (pl *planner) fitDescriptor(n *Node, key string, d *v1.Descriptor, placement v1.Placement) (*v1.MemoryPlan, error) {
	key = fmt.Sprintf("%s|%s|%d|%d", n.ID, key, placement, pl.context)
	if r, ok := pl.fits[key]; ok {
		return r.plan, r.err
	}
	plan, err := pl.plan(n, d, pl.fixed(), placement)
	pl.fits[key] = fitResult{plan, err}
	return plan, err
}

func groupIDs(groups []*v1.TensorGroup) string {
	ids := make([]string, len(groups))
	for i, g := range groups {
		ids[i] = g.GetId()
	}
	return strings.Join(ids, ",")
}

// Params for a pass with the context fixed
func (pl *planner) fixed() estimate.Params {
	out := pl.params.Clone()
	if pl.policy.ContextParam != "" && pl.context > 0 {
		out[pl.policy.ContextParam] = pl.context
	}
	return out
}

// The most consecutive layers that fit a node beside the extras, up to a limit, by bisection over
// the solver: forward from a start, or backward from an end for a seat holding the last layers.
// Zero means not even one layer fits. An error means the solver refuses the node under the
// runtime's rules, at any count.
func (pl *planner) capacity(n *Node, start, end int, tail bool, limit int, extras []*v1.TensorGroup, placement v1.Placement) (int, *v1.MemoryPlan, error) {
	total := min(end-start, limit)
	if total <= 0 {
		return 0, nil, nil
	}
	lo, hi := 0, total
	var best *v1.MemoryPlan
	for lo < hi {
		mid := (lo + hi + 1) / 2
		from, to := start, start+mid
		if tail {
			from, to = end-mid, end
		}
		plan, err := pl.fit(n, from, to, extras, placement)
		if err != nil {
			return 0, nil, err
		}
		if plan.GetVerdict() == v1.FitVerdict_FIT_VERDICT_FITS {
			lo, best = mid, plan
		} else {
			hi = mid - 1
		}
	}
	return lo, best, nil
}

// A seat as the planner lays it out
type seat struct {
	node  *Node
	role  string
	rank  uint32
	phase int
	// Layer range held, from inclusive to exclusive
	from, to int
	plan     *v1.MemoryPlan
	// Bytes and parameters a token reads on the seat, and the cache it holds
	read, params float64
	cache        float64
	weights      uint64
	// Reads split by the side holding them: device pools and the host pool
	readDevice, readHost     float64
	paramsDevice, paramsHost float64
	// Devices in tensor order with the bytes each holds, and bytes left on the host pool
	devices     []*Device
	deviceBytes []uint64
	hostBytes   uint64
	gpuLayers   int
	exposed     bool
	verdict     v1.FitVerdict
	detail      string
	// Stage devices a head carries as devices of its own, listed before its own in the seat record
	stageDevices []string
	stageBytes   []uint64
}

// Lays a plan's pools out over the node's devices: bytes per device pool in the node's device
// order, and the rest on the host pool
func (pl *planner) layout(n *Node, plan *v1.MemoryPlan) ([]*Device, []uint64, uint64) {
	used := map[string]uint64{}
	var host uint64
	for _, pu := range plan.GetPools() {
		switch pu.GetKind() {
		case v1.PoolKind_POOL_KIND_HOST:
			host += pu.GetUsedBytes()
		default:
			used[pu.GetPoolId()] += pu.GetUsedBytes()
		}
	}
	var devices []*Device
	var bytes []uint64
	for _, d := range n.Devices {
		b := used[d.Pool.GetId()]
		if b == 0 {
			continue
		}
		devices = append(devices, d)
		bytes = append(bytes, b)
		// A unified pool holds host and device bytes alike, counted once on the device.
		if d.Pool.GetKind() == v1.PoolKind_POOL_KIND_UNIFIED {
			delete(used, d.Pool.GetId())
		}
	}
	return devices, bytes, host
}

// Fills a seat's numbers from its plan on its node: what each side holds, and what a token reads
// on each side, weights at the share a token touches and the cache following its layers, and
// records the numbers of every device the seat is priced with. Layers spilled to host memory are
// read and computed by the CPU. The embedding table, which runtimes keep in host memory and look
// up by row, counts with the seat's reads and parameters at the seat's own numbers, as the cost
// lines price a seat: no layer of it runs on the CPU.
func (pl *planner) finish(s *seat, expertShare float64) error {
	s.verdict = s.plan.GetVerdict()
	s.detail = s.plan.GetDetail()
	s.devices, s.deviceBytes, s.hostBytes = pl.layout(s.node, s.plan)
	s.weights = s.plan.GetWeightsBytes()
	s.cache = float64(s.plan.GetCacheBytes())
	var weightsDevice, weightsHost float64
	var layersDevice, layersAll uint32
	for _, p := range s.plan.GetPlacements() {
		share := 1.0
		if p.GetKind() == v1.TensorGroupKind_TENSOR_GROUP_KIND_EXPERTS {
			share = expertShare
		}
		b := float64(p.GetBytes()) * share
		host := p.GetPoolId() == text.Enum(v1.PoolKind_POOL_KIND_HOST) && p.GetKind() != v1.TensorGroupKind_TENSOR_GROUP_KIND_EMBEDDING
		if host {
			weightsHost += b
		} else {
			weightsDevice += b
		}
		if p.GetKind() == v1.TensorGroupKind_TENSOR_GROUP_KIND_LAYER {
			layersAll += p.GetCount()
			if !host {
				layersDevice += p.GetCount()
			}
		}
	}
	s.readDevice, s.readHost = weightsDevice, weightsHost
	if layersAll > 0 {
		s.readDevice += s.cache * float64(layersDevice) / float64(layersAll)
		s.readHost += s.cache * float64(layersAll-layersDevice) / float64(layersAll)
	} else {
		s.readDevice += s.cache
	}
	s.read = s.readDevice + s.readHost
	if total := weightsDevice + weightsHost; total > 0 {
		s.paramsDevice = s.params * weightsDevice / total
		s.paramsHost = s.params * weightsHost / total
	}
	if v := s.plan.GetParams()["n_gpu_layers"]; v != "" {
		n, err := text.ParseNumber(v)
		if err != nil {
			return fmt.Errorf("n_gpu_layers %q in the plan of %s: %w", v, s.node.label(), err)
		}
		s.gpuLayers = int(n)
	} else {
		s.gpuLayers = s.to - s.from
	}
	for _, d := range s.devices {
		pl.use(s.node, d)
	}
	if len(s.devices) == 0 && s.readDevice > 0 {
		if d := s.primaryDevice(); d != nil {
			pl.use(s.node, d)
		} else if s.node.CPU != nil {
			pl.use(s.node, s.node.CPU)
		}
	}
	if (s.readHost > 0 || len(s.devices) == 0) && s.node.CPU != nil {
		pl.use(s.node, s.node.CPU)
	}
	return nil
}

// Records a device's numbers among the plan's sources
func (pl *planner) use(n *Node, d *Device) {
	line := fmt.Sprintf("%s %s: %s", n.label(), d.label(), d.Numbers.Describe())
	if pl.note(line) && d.Numbers.FromProfile() {
		pl.profiled = append(pl.profiled, n.label()+" "+d.label())
	}
}

// Records a node's disk numbers among the plan's sources
func (pl *planner) useDisk(n *Node) {
	line := fmt.Sprintf("%s disk: bandwidth %s %s", n.label(), rate(n.Disk.Stream), n.Disk.StreamSource)
	if pl.note(line) && n.Disk.FromProfile() {
		pl.profiled = append(pl.profiled, n.label()+" disk")
	}
}

// Adds a source line once, reporting whether it was new
func (pl *planner) note(line string) bool {
	if pl.seen[line] {
		return false
	}
	pl.seen[line] = true
	pl.sources = append(pl.sources, line)
	return true
}

// The share of the seat's device bytes each device holds, one device taking everything
func (s *seat) deviceShares() []float64 {
	var total float64
	for _, b := range s.deviceBytes {
		total += float64(b)
	}
	out := make([]float64, len(s.deviceBytes))
	for i, b := range s.deviceBytes {
		if total > 0 {
			out[i] = float64(b) / total
		}
	}
	if len(out) > 0 && total == 0 {
		out[0] = 1
	}
	return out
}

// The device a seat's device reads go through when its plan lists none: the node's fastest
func (s *seat) primaryDevice() *Device {
	if len(s.devices) > 0 {
		return s.devices[0]
	}
	if len(s.node.Devices) > 0 {
		return s.node.Devices[0]
	}
	return nil
}

// Seconds one token's decode pass takes on the seat: the bytes a token reads on each side, weights
// at the share it touches plus the cache, streamed from the pools holding them, plus the fixed cost
func (s *seat) decode() float64 {
	var t float64
	if s.readDevice > 0 {
		if len(s.devices) > 0 {
			for i, share := range s.deviceShares() {
				t += share * s.readDevice / s.devices[i].Numbers.Stream
			}
		} else if d := s.primaryDevice(); d != nil {
			t += s.readDevice / d.Numbers.Stream
		} else if s.node.CPU != nil {
			t += s.readDevice / s.node.CPU.Numbers.Stream
		}
	}
	if s.readHost > 0 && s.node.CPU != nil {
		t += s.readHost / s.node.CPU.Numbers.Stream
	}
	return t + s.fixed()
}

// Seconds prefilling a prompt takes on the seat: two operations per parameter per token, on the
// compute of the side holding the parameters
func (s *seat) prefill(prompt float64) float64 {
	return s.prefillCost().at(prompt)
}

// The seat's prefill as fixed seconds plus seconds per prompt token
func (s *seat) prefillCost() piece {
	var perToken float64
	if s.paramsDevice > 0 {
		if len(s.devices) > 0 {
			for i, share := range s.deviceShares() {
				perToken += 2 * s.paramsDevice * share / s.devices[i].Numbers.Compute
			}
		} else if d := s.primaryDevice(); d != nil {
			perToken += 2 * s.paramsDevice / d.Numbers.Compute
		} else if s.node.CPU != nil {
			perToken += 2 * s.paramsDevice / s.node.CPU.Numbers.Compute
		}
	}
	if s.paramsHost > 0 && s.node.CPU != nil {
		perToken += 2 * s.paramsHost / s.node.CPU.Numbers.Compute
	}
	return piece{fixed: s.fixed(), perToken: perToken}
}

// Fixed cost of a pass on the seat's fastest device
func (s *seat) fixed() float64 {
	if len(s.devices) > 0 {
		return s.devices[0].Numbers.Fixed
	}
	if s.node.CPU != nil {
		return s.node.CPU.Numbers.Fixed
	}
	return 0
}

// Turns a seat into its record
func (s *seat) proto() *v1.Seat {
	out := &v1.Seat{
		NodeId:      s.node.ID,
		NodeName:    s.node.Name,
		Role:        s.role,
		Rank:        s.rank,
		Placements:  s.plan.GetPlacements(),
		LayerFrom:   uint32(s.from),
		LayerTo:     uint32(s.to),
		ReadBytes:   uint64(s.read),
		CacheBytes:  uint64(s.cache),
		WeightBytes: s.weights,
		Memory:      s.plan,
		InstallId:   s.node.Install.GetId(),
		Exposed:     s.exposed,
		Phase:       uint32(s.phase),
		State:       v1.InstanceState_INSTANCE_STATE_UNSPECIFIED,
		GpuLayers:   uint32(s.gpuLayers),
	}
	out.DeviceIds = append(out.DeviceIds, s.stageDevices...)
	out.DeviceBytes = append(out.DeviceBytes, s.stageBytes...)
	for i, d := range s.devices {
		out.DeviceIds = append(out.DeviceIds, d.ID())
		out.DeviceBytes = append(out.DeviceBytes, s.deviceBytes[i])
	}
	return out
}

// Whether every seat fits, the worst verdict among them
func worstVerdict(seats []*seat) v1.FitVerdict {
	worst := v1.FitVerdict_FIT_VERDICT_FITS
	for _, s := range seats {
		if s.verdict > worst {
			worst = s.verdict
		}
	}
	return worst
}

// The plan's memory: pools of every seat with node qualified ids, weights, cache, and overhead summed
func memoryOf(seats []*seat) *v1.MemoryPlan {
	out := &v1.MemoryPlan{Verdict: worstVerdict(seats)}
	for _, s := range seats {
		out.WeightsBytes += s.plan.GetWeightsBytes()
		out.CacheBytes += s.plan.GetCacheBytes()
		out.OverheadBytes += s.plan.GetOverheadBytes()
		for _, pu := range s.plan.GetPools() {
			q := proto.Clone(pu).(*v1.PoolUsage)
			q.PoolId = s.node.ID + "/" + pu.GetPoolId()
			out.Pools = append(out.Pools, q)
		}
		for _, pl := range s.plan.GetPlacements() {
			q := proto.Clone(pl).(*v1.GroupPlacement)
			q.PoolId = s.node.ID + "/" + pl.GetPoolId()
			out.Placements = append(out.Placements, q)
		}
	}
	return out
}

// Two plans on one node as one: weights, cache, and overhead summed, each pool's use summed, the
// placements of both, the worse verdict
func mergePlans(a, b *v1.MemoryPlan) *v1.MemoryPlan {
	out := proto.Clone(a).(*v1.MemoryPlan)
	out.WeightsBytes += b.GetWeightsBytes()
	out.CacheBytes += b.GetCacheBytes()
	out.OverheadBytes += b.GetOverheadBytes()
	byPool := map[string]*v1.PoolUsage{}
	for _, pu := range out.Pools {
		byPool[pu.GetPoolId()] = pu
	}
	for _, pu := range b.GetPools() {
		if have, ok := byPool[pu.GetPoolId()]; ok {
			have.UsedBytes += pu.GetUsedBytes()
			continue
		}
		q := proto.Clone(pu).(*v1.PoolUsage)
		out.Pools = append(out.Pools, q)
		byPool[q.GetPoolId()] = q
	}
	for _, p := range b.GetPlacements() {
		out.Placements = append(out.Placements, proto.Clone(p).(*v1.GroupPlacement))
	}
	if b.GetVerdict() > out.GetVerdict() {
		out.Verdict, out.Detail = b.GetVerdict(), b.GetDetail()
	}
	return out
}

// A node's profile with a plan's use taken out of each pool, what a second model on the node fits into
func remaining(n *Node, plan *v1.MemoryPlan) *v1.HostProfile {
	out := proto.Clone(n.Profile).(*v1.HostProfile)
	used := map[string]uint64{}
	for _, pu := range plan.GetPools() {
		used[pu.GetPoolId()] += pu.GetUsedBytes()
	}
	for _, p := range out.Pools {
		u := used[p.GetId()]
		if u == 0 {
			continue
		}
		p.TotalBytes -= min(p.GetTotalBytes(), u)
		if p.GetFreeBytes() > 0 {
			p.FreeBytes -= min(p.GetFreeBytes(), u)
		}
	}
	return out
}

// Names why a seat does not fit
func shortfall(s *seat) string {
	if s.detail != "" {
		return fmt.Sprintf("%s does not fit on %s: %s", s.role, s.node.label(), s.detail)
	}
	return fmt.Sprintf("%s does not fit on %s", s.role, s.node.label())
}

// A seat that the solver could not plan at all: the runtime's rule or the header it lacks
func (pl *planner) refused(s *seat, err error) {
	s.verdict, s.detail = v1.FitVerdict_FIT_VERDICT_NO, err.Error()
	s.plan = &v1.MemoryPlan{Verdict: v1.FitVerdict_FIT_VERDICT_NO, Detail: err.Error()}
}

// A solo plan of the whole model on a node, as a seat with a role
func (pl *planner) solo(n *Node, r runtimes.Role) *seat {
	s := &seat{node: n, role: r.Name, phase: r.Phase, exposed: r.Rendezvous, from: 0, to: pl.parts.layerCount()}
	plan, err := pl.fitDescriptor(n, "solo", pl.parts.descriptor, pl.req.Placement)
	if err != nil {
		pl.refused(s, err)
		return s
	}
	s.plan = plan
	_, extraParams := groupTotals(pl.parts.extras)
	_, partParams := groupTotals(append(append(append([]*v1.TensorGroup{}, pl.parts.denoiser...), pl.parts.encoders...), pl.parts.decoder...))
	s.params = pl.parts.readParams(0, pl.parts.layerCount()) + extraParams + partParams
	if err := pl.finish(s, pl.parts.expertShare); err != nil {
		pl.refused(s, err)
	}
	return s
}

// The head role of a shape and its other role, from the runtime's roles: the head answers the
// route, the other is what every remaining seat plays
func (pl *planner) roles(shape v1.Shape) (head, other runtimes.Role, ok bool) {
	var haveHead, haveOther bool
	for _, r := range pl.req.Runtime.Roles(shape) {
		switch {
		case r.Head && !haveHead:
			head, haveHead = r, true
		case !r.Head && !haveOther:
			other, haveOther = r, true
		}
	}
	return head, other, haveHead
}
