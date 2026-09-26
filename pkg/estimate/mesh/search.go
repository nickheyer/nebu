package mesh

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/nickheyer/nebu/pkg/estimate"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/runtimes"
	"github.com/nickheyer/nebu/pkg/text"
)

// A prefill cost as fixed seconds plus seconds per prompt token
type piece struct {
	fixed, perToken float64
}

func (p piece) at(prompt float64) float64 { return p.fixed + p.perToken*prompt }

func (p piece) add(q piece) piece { return piece{p.fixed + q.fixed, p.perToken + q.perToken} }

// The prefill of a shape at a prompt length: the largest of its pieces there
func evalPieces(pieces []piece, prompt float64) float64 {
	var out float64
	for i, p := range pieces {
		if v := p.at(prompt); i == 0 || v > out {
			out = v
		}
	}
	return out
}

// One shape on one node set, priced
type candidate struct {
	shape v1.Shape
	seats []*seat
	head  *seat
	// Node ids of an assignment that laid out no seats
	nodes   []string
	prefill float64
	decode  float64
	rps     float64
	// The cost model's prefill by prompt length and its decode per token, before the ratio
	pieces      []piece
	modelDecode float64
	reason      string
	verdict     v1.FitVerdict
	// Prompt length above which a relay pays, zero when never
	breakEven uint32
	class     v1.LinkClass
	// Draft tokens per round and the acceptance priced
	draftTokens uint32
	acceptance  float64
	score       float64
	// Formation runs the applied ratios were learned from
	ratioSamples uint32
}

func (c *candidate) nodeIDs() []string {
	if len(c.seats) == 0 {
		return c.nodes
	}
	var out []string
	for _, s := range c.seats {
		out = append(out, s.node.ID)
	}
	return out
}

func (c *candidate) tps() float64 {
	if c.decode <= 0 {
		return 0
	}
	return 1 / c.decode
}

// Whether the request allows a shape
func (pl *planner) wants(shape v1.Shape) bool {
	return pl.req.Shape == v1.Shape_SHAPE_UNSPECIFIED || pl.req.Shape == shape
}

// Nodes that support a shape
func (pl *planner) supporting(shape v1.Shape) []*Node {
	var out []*Node
	for _, n := range pl.nodes {
		if n.Supports(shape) {
			out = append(out, n)
		}
	}
	return out
}

// The nodes of the span whose install does not advertise a shape, as one reason line
func (pl *planner) lacking(shape v1.Shape) string {
	var out []string
	for _, n := range pl.nodes {
		if !n.Supports(shape) {
			out = append(out, fmt.Sprintf("%s's %s %s does not advertise %s", n.label(), pl.req.Runtime.ID(), n.Install.GetVersion(), shapeName(shape)))
		}
	}
	return strings.Join(out, "; ")
}

// Lists a shape fewer than two nodes support, naming the nodes that lack it
func (pl *planner) short(shape v1.Shape) []*candidate {
	if len(pl.nodes) < 2 {
		return nil
	}
	reason := pl.lacking(shape)
	if reason == "" {
		return nil
	}
	return []*candidate{{shape: shape, nodes: nodeIDs(pl.nodes), verdict: v1.FitVerdict_FIT_VERDICT_NO, reason: reason}}
}

// Records an assignment a shape could not make, once per node set and reason
func (pl *planner) reject(shape v1.Shape, nodes []*Node, reason string) {
	ids := nodeIDs(nodes)
	key := fmt.Sprintf("%d|%s|%s", shape, strings.Join(ids, ","), reason)
	if pl.rejectedSeen[key] {
		return
	}
	pl.rejectedSeen[key] = true
	pl.rejected = append(pl.rejected, &candidate{shape: shape, nodes: ids, verdict: v1.FitVerdict_FIT_VERDICT_NO, reason: reason})
}

func nodeIDs(nodes []*Node) []string {
	out := make([]string, len(nodes))
	for i, n := range nodes {
		out[i] = n.ID
	}
	return out
}

// Fills a candidate's prices at the reference prompt from its cost model
func (pl *planner) price(c *candidate) {
	c.prefill = evalPieces(c.pieces, pl.prompt())
	c.modelDecode = c.decode
	if total := c.prefill + pl.completion()*c.decode; total > 0 {
		c.rps = 1 / total
	}
}

// Every candidate of every shape the request allows at the pass's context, the assignments no
// shape could make listed after them
func (pl *planner) search() []*candidate {
	pl.rejected, pl.rejectedSeen = nil, map[string]bool{}
	var out []*candidate
	if pl.wants(v1.Shape_SHAPE_SOLO) {
		out = append(out, pl.solos()...)
	}
	if pl.parts.layerCount() > 0 {
		if pl.wants(v1.Shape_SHAPE_CHAIN) {
			out = append(out, pl.chains()...)
		}
		if pl.wants(v1.Shape_SHAPE_LOCKSTEP) {
			out = append(out, pl.locksteps()...)
		}
		if pl.wants(v1.Shape_SHAPE_RELAY) {
			out = append(out, pl.relays()...)
		}
		if pl.wants(v1.Shape_SHAPE_DRAFT) {
			out = append(out, pl.drafts()...)
		}
	}
	if pl.wants(v1.Shape_SHAPE_REPLICAS) {
		out = append(out, pl.replicas()...)
	}
	if pl.wants(v1.Shape_SHAPE_STAGES) && len(pl.parts.denoiser) > 0 {
		out = append(out, pl.stages()...)
	}
	return append(out, pl.rejected...)
}

// Solo on every node that supports it
func (pl *planner) solos() []*candidate {
	nodes := pl.supporting(v1.Shape_SHAPE_SOLO)
	role, _, ok := pl.roles(v1.Shape_SHAPE_SOLO)
	if !ok {
		pl.reject(v1.Shape_SHAPE_SOLO, nodes, fmt.Sprintf("%s declares no solo role", pl.req.Runtime.ID()))
		return nil
	}
	var out []*candidate
	for _, n := range nodes {
		s := pl.solo(n, role)
		c := &candidate{shape: v1.Shape_SHAPE_SOLO, seats: []*seat{s}, head: s, verdict: s.verdict}
		if s.verdict != v1.FitVerdict_FIT_VERDICT_FITS {
			c.reason = shortfall(s)
		}
		if len(pl.parts.denoiser) > 0 {
			// A pipeline is priced per request, every part on the one seat.
			cost, note, err := pl.pipeline(s, s, s, nil)
			if err != nil {
				c.verdict, c.reason = v1.FitVerdict_FIT_VERDICT_NO, err.Error()
				out = append(out, c)
				continue
			}
			c.pieces = cost
			pl.price(c)
			if c.reason == "" {
				c.reason = note
			}
			out = append(out, c)
			continue
		}
		c.decode = s.decode()
		c.pieces = []piece{s.prefillCost()}
		pl.price(c)
		if c.reason == "" {
			c.reason = fmt.Sprintf("everything on %s, %s per token", n.label(), seconds(c.decode))
		}
		out = append(out, c)
	}
	return out
}

// Chains: nodes with a device pool ordered by bandwidth, filled with whole layers to their capacity
// at their place in the pipeline, every subset up to eight nodes with every choice of head
func (pl *planner) chains() []*candidate {
	var nodes []*Node
	for _, n := range pl.supporting(v1.Shape_SHAPE_CHAIN) {
		if len(n.Devices) == 0 && n.CPU == nil {
			pl.reject(v1.Shape_SHAPE_CHAIN, []*Node{n}, fmt.Sprintf("%s has no memory pool, a chain seat needs one", n.label()))
			continue
		}
		nodes = append(nodes, n)
	}
	if len(nodes) < 2 {
		return pl.short(v1.Shape_SHAPE_CHAIN)
	}
	sort.SliceStable(nodes, func(i, j int) bool { return nodes[i].beta() > nodes[j].beta() })
	if len(nodes) > MaxChainNodes {
		for _, n := range nodes[MaxChainNodes:] {
			pl.reject(v1.Shape_SHAPE_CHAIN, []*Node{n}, fmt.Sprintf("%s is beyond the %d fastest nodes of the span, which a chain keeps", n.label(), MaxChainNodes))
		}
		nodes = nodes[:MaxChainNodes]
	}
	headRole, stageRole, ok := pl.roles(v1.Shape_SHAPE_CHAIN)
	if !ok {
		pl.reject(v1.Shape_SHAPE_CHAIN, nodes, fmt.Sprintf("%s declares no chain roles", pl.req.Runtime.ID()))
		return nil
	}
	compose := pl.req.Draft != nil && pl.facts.ChainSpeculative
	var out []*candidate
	for mask := 1; mask < 1<<len(nodes); mask++ {
		var members []*Node
		for i := range nodes {
			if mask&(1<<i) != 0 {
				members = append(members, nodes[i])
			}
		}
		if len(members) < 2 {
			continue
		}
		for hi := range members {
			c := pl.chain(members, hi, headRole, stageRole)
			if c == nil {
				continue
			}
			out = append(out, c)
			if compose {
				if d := pl.chainWithDraft(c); d != nil {
					out = append(out, d)
				}
			}
		}
	}
	return out
}

// Lays a chain out over members with one of them as head. The pipeline runs stages then head on
// a star runtime and head then ranks on a ring. Counts are decided fastest first, each node
// fitted at its own place in the pipeline, until the layout is stable.
func (pl *planner) chain(members []*Node, hi int, headRole, stageRole runtimes.Role) *candidate {
	L := pl.parts.layerCount()
	head := members[hi]
	ids := nodeIDs(members)
	ring := pl.facts.Ring
	if ring {
		pl.noteRanks(members)
	} else {
		pl.noteBuilds(members)
	}
	var order []*Node
	if ring {
		order = append(order, head)
	}
	for _, n := range members {
		if n != head {
			order = append(order, n)
		}
	}
	if !ring {
		order = append(order, head)
	}
	firstExtras, lastExtras := pl.parts.splitExtras()
	extrasOf := func(n *Node) []*v1.TensorGroup {
		if !ring {
			if n == head {
				return pl.parts.extras
			}
			return nil
		}
		var out []*v1.TensorGroup
		if n == order[0] {
			out = append(out, firstExtras...)
		}
		if n == order[len(order)-1] {
			out = append(out, lastExtras...)
		}
		return out
	}
	counts := map[*Node]int{}
	capacities := map[*Node]int{}
	for iter := 0; iter < 3; iter++ {
		offsets := map[*Node]int{}
		off := 0
		for _, n := range order {
			offsets[n] = off
			off += counts[n]
		}
		next := map[*Node]int{}
		remaining := L
		for _, n := range members {
			var capacity int
			var err error
			if !ring && n == head {
				capacity, _, err = pl.capacity(n, 0, L, true, L, extrasOf(n), pl.req.Placement)
			} else {
				capacity, _, err = pl.capacity(n, offsets[n], L, false, L, extrasOf(n), pl.req.Placement)
			}
			if err != nil {
				return &candidate{shape: v1.Shape_SHAPE_CHAIN, nodes: ids, verdict: v1.FitVerdict_FIT_VERDICT_NO, reason: fmt.Sprintf("%s cannot seat a chain: %v", n.label(), err)}
			}
			capacities[n] = capacity
			next[n] = min(capacity, remaining)
			remaining -= next[n]
		}
		same := len(counts) == len(next)
		for n, k := range next {
			if counts[n] != k {
				same = false
			}
		}
		counts = next
		if same {
			break
		}
	}
	for _, n := range members {
		if capacities[n] > 0 {
			continue
		}
		why := fmt.Sprintf("%s holds no layer: not one fits on it with its cache at %d context", n.label(), pl.context)
		if n == head {
			why = fmt.Sprintf("%s cannot head: not one layer fits on it beside the embedding and output", head.label())
		}
		return &candidate{shape: v1.Shape_SHAPE_CHAIN, nodes: ids, verdict: v1.FitVerdict_FIT_VERDICT_NO, reason: why}
	}
	pl.spread(members, counts, capacities, L)
	remaining := L
	for _, n := range members {
		remaining -= counts[n]
	}
	var seats []*seat
	off, rank := 0, 1
	for _, n := range order {
		s := &seat{node: n, from: off, to: off + counts[n]}
		off += counts[n]
		if n == head {
			s.role, s.phase, s.exposed, s.rank = headRole.Name, headRole.Phase, headRole.Rendezvous, 0
		} else {
			s.role, s.phase, s.exposed, s.rank = stageRole.Name, stageRole.Phase, stageRole.Rendezvous, uint32(rank)
			rank++
		}
		seats = append(seats, s)
	}
	c := &candidate{shape: v1.Shape_SHAPE_CHAIN, seats: seats}
	for _, s := range seats {
		if s.node == head {
			c.head = s
		}
	}
	if remaining > 0 {
		c.verdict = v1.FitVerdict_FIT_VERDICT_NO
		c.reason = fmt.Sprintf("%d of %d layers have no seat across %s", remaining, L, pl.names(seats))
	}
	pl.chainSeats(c, extrasOf)
	// A seat's layers land on its devices: a runtime renders a seat from its device list, and a seat
	// whose plan spills every layer to its host memory has none to render.
	for _, s := range seats {
		if s.verdict != v1.FitVerdict_FIT_VERDICT_FITS || len(s.devices) > 0 {
			continue
		}
		if s != c.head && s.node.CPU != nil && s.hostBytes > 0 {
			s.devices, s.deviceBytes = []*Device{s.node.CPU}, []uint64{s.hostBytes}
			continue
		}
		s.verdict = v1.FitVerdict_FIT_VERDICT_NO
		s.detail = fmt.Sprintf("layers %d-%d land on no device of %s", s.from, s.to-1, s.node.label())
		c.verdict = v1.FitVerdict_FIT_VERDICT_NO
		if c.reason == "" {
			c.reason = shortfall(s)
		}
	}
	if !ring {
		for _, s := range seats {
			if s == c.head {
				continue
			}
			for i, d := range s.devices {
				c.head.stageDevices = append(c.head.stageDevices, s.node.ID+"/"+d.ID())
				c.head.stageBytes = append(c.head.stageBytes, s.deviceBytes[i])
			}
		}
	}
	pl.priceChain(c)
	return c
}

// Gives every seat the faster ones left empty its share of the layers by bandwidth, at least
// one, taken from the seats holding the most, within its capacity
func (pl *planner) spread(members []*Node, counts, capacities map[*Node]int, L int) {
	var total float64
	for _, n := range members {
		total += n.beta()
	}
	for _, n := range members {
		if counts[n] > 0 {
			continue
		}
		want := 1
		if total > 0 {
			want = max(1, int(math.Round(float64(L)*n.beta()/total)))
		}
		want = min(want, capacities[n])
		for want > 0 {
			var donor *Node
			for _, d := range members {
				if d != n && counts[d] > 1 && (donor == nil || counts[d] > counts[donor]) {
					donor = d
				}
			}
			if donor == nil {
				break
			}
			counts[donor]--
			counts[n]++
			want--
		}
	}
}

// Plans every seat of a chain exactly over its own range under the runtime's rules
func (pl *planner) chainSeats(c *candidate, extrasOf func(*Node) []*v1.TensorGroup) {
	for _, s := range c.seats {
		extras := extrasOf(s.node)
		plan, err := pl.fit(s.node, s.from, s.to, extras, pl.req.Placement)
		if err != nil {
			pl.refused(s, err)
		} else {
			s.plan = plan
			_, extraParams := groupTotals(extras)
			s.params = pl.parts.readParams(s.from, s.to) + extraParams
			if err := pl.finish(s, pl.parts.expertShare); err != nil {
				pl.refused(s, err)
			}
		}
		if s.verdict != v1.FitVerdict_FIT_VERDICT_FITS && c.reason == "" {
			c.reason = shortfall(s)
		}
	}
	if c.verdict == v1.FitVerdict_FIT_VERDICT_UNSPECIFIED {
		c.verdict = worstVerdict(c.seats)
	}
}

// What an unmeasured link is priced as until a probe runs
const (
	unmeasuredRTT       = 0.010
	unmeasuredBandwidth = 100e6 / 8
)

// Round trip between two nodes, the slower direction, a slow guess when unmeasured
func (pl *planner) linkRTT(a, b string) float64 {
	if rtt, ok := pl.mesh.rtt(a, b); ok {
		return rtt
	}
	pl.noteUnmeasured(a, b)
	return unmeasuredRTT
}

// Bandwidth from one node to another, a slow guess when unmeasured
func (pl *planner) linkBandwidth(from, to string, aggregate bool) float64 {
	if w, ok := pl.mesh.bandwidth(from, to, aggregate); ok {
		return w
	}
	pl.noteUnmeasured(from, to)
	return unmeasuredBandwidth
}

func (pl *planner) noteUnmeasured(a, b string) {
	pl.note(fmt.Sprintf("link %s to %s unmeasured, priced at %s round trip and %s until a probe runs", pl.mesh.Node(a).label(), pl.mesh.Node(b).label(), seconds(unmeasuredRTT), gbits(unmeasuredBandwidth)))
}

// The link cost of one crossing: per token, and per prompt token as fixed seconds plus seconds
// per token, over the bandwidth the runtime's transport takes
func (pl *planner) crossing(from, to *seat, bytesPerToken float64) (float64, piece) {
	rtt := pl.linkRTT(from.node.ID, to.node.ID)
	w := pl.linkBandwidth(from.node.ID, to.node.ID, pl.facts.ChainAggregate)
	chunks := pl.facts.ChunksInFlight
	if chunks < 1 {
		chunks = 1
	}
	return rtt + bytesPerToken/w, piece{fixed: rtt, perToken: bytesPerToken / w / chunks}
}

// Prices a chain: every seat's own pass plus the links crossed per token and per prompt
func (pl *planner) priceChain(c *candidate) {
	d := pl.parts.model.Embedding
	act := pl.facts.ActivationBytes
	if act == 0 {
		act = 4
	}
	var decode float64
	var step piece
	for _, s := range c.seats {
		decode += s.decode()
		step = step.add(s.prefillCost())
	}
	var stages []*seat
	for _, s := range c.seats {
		if s != c.head {
			stages = append(stages, s)
		}
	}
	var linkDecode float64
	var linkPrefill piece
	var notes []string
	if pl.facts.Ring {
		order := append([]*seat{}, c.seats...)
		sort.SliceStable(order, func(i, j int) bool { return order[i].from < order[j].from })
		for i := 0; i+1 < len(order); i++ {
			from, to := order[i], order[i+1]
			dec, pre := pl.crossing(from, to, act*d)
			linkDecode += dec
			linkPrefill = linkPrefill.add(pre)
		}
		notes = append(notes, fmt.Sprintf("ring chain, %d boundaries each one hop", len(order)-1))
	} else {
		for _, s := range stages {
			out, outPre := pl.crossing(c.head, s, act*d)
			back, backPre := pl.crossing(s, c.head, act*d)
			rtt := pl.linkRTT(c.head.node.ID, s.node.ID)
			linkDecode += out + back - rtt
			linkPrefill = linkPrefill.add(piece{fixed: rtt, perToken: outPre.perToken + backPre.perToken})
		}
		notes = append(notes, "star chain, one round trip per stage per token")
	}
	c.decode = decode + linkDecode
	c.pieces = []piece{step.add(linkPrefill)}
	pl.price(c)
	for _, s := range stages {
		notes = append(notes, fmt.Sprintf("%s holds layers %d-%d at %s", s.node.label(), s.from, s.to-1, rate(s.node.beta())))
	}
	notes = append(notes, fmt.Sprintf("head %s holds layers %d-%d, links add %s per token", c.head.node.label(), c.head.from, c.head.to-1, seconds(linkDecode)))
	if !pl.facts.ChainSpeculative && pl.req.speculativeAsked() {
		notes = append(notes, fmt.Sprintf("speculative settings are off in chain, %s does not compose them with pipeline parallel", pl.req.Runtime.ID()))
	}
	c.class, _, _ = pl.mesh.worstClass(c.nodeIDs())
	if c.reason == "" {
		c.reason = strings.Join(notes, "; ")
	} else if c.verdict == v1.FitVerdict_FIT_VERDICT_NO {
		c.reason += "; " + strings.Join(notes, "; ")
	}
}

// A chain with the draft model beside the head: the draft fits into what the head's layers leave
// on its node, and a round of draft tokens is verified in one pass through the chain, so the
// chain's link cost is paid once per round
func (pl *planner) chainWithDraft(c *candidate) *candidate {
	if c.verdict != v1.FitVerdict_FIT_VERDICT_FITS {
		return nil
	}
	head := c.head
	gamma := pl.draftTokens()
	key := fmt.Sprintf("draft-beside|%d|%d|%s", head.from, head.to, groupIDs(pl.parts.extras))
	plan, err := pl.fitDraft(head.node, key, remaining(head.node, head.plan))
	d := &candidate{shape: v1.Shape_SHAPE_CHAIN, draftTokens: uint32(gamma), class: c.class}
	if err != nil || plan.GetVerdict() != v1.FitVerdict_FIT_VERDICT_FITS {
		d.nodes = c.nodeIDs()
		d.verdict = v1.FitVerdict_FIT_VERDICT_NO
		d.reason = fmt.Sprintf("the draft model does not fit beside the head's layers %d-%d on %s", head.from, head.to-1, head.node.label())
		if err != nil {
			d.reason += ": " + err.Error()
		} else if plan.GetDetail() != "" {
			d.reason += ": " + plan.GetDetail()
		}
		return d
	}
	dp := split(pl.req.Draft)
	local := &seat{node: head.node, role: head.role, plan: plan, from: 0, to: dp.layerCount()}
	_, extraParams := groupTotals(dp.extras)
	local.params = dp.readParams(0, dp.layerCount()) + extraParams
	if err := pl.finish(local, dp.expertShare); err != nil {
		d.nodes = c.nodeIDs()
		d.verdict = v1.FitVerdict_FIT_VERDICT_NO
		d.reason = fmt.Sprintf("the draft model beside the head on %s: %v", head.node.label(), err)
		return d
	}
	merged := *head
	merged.plan = mergePlans(head.plan, plan)
	for _, s := range c.seats {
		if s == head {
			d.seats = append(d.seats, &merged)
			d.head = &merged
		} else {
			d.seats = append(d.seats, s)
		}
	}
	alpha := head.node.acceptance()
	perRound := tokensPerRound(alpha, gamma)
	draftStep := local.decode()
	d.decode = (gamma*draftStep + c.modelDecode) / perRound
	d.pieces = c.pieces
	d.verdict = v1.FitVerdict_FIT_VERDICT_FITS
	d.acceptance = alpha
	pl.price(d)
	d.reason = fmt.Sprintf("%s; draft beside the head on %s, %.0f tokens per round each a %s draft step, verified in one %s pass through the chain at acceptance %.2f: %.1f tokens per round", c.reason, head.node.label(), gamma, seconds(draftStep), seconds(c.modelDecode), alpha, perRound)
	return d
}

// Draft tokens per round: the resolved param, else the default
func (pl *planner) draftTokens() float64 {
	gamma := float64(pl.params.Int("draft_max"))
	if gamma <= 0 {
		gamma = defaultDraftTokens
	}
	return gamma
}

// Tokens a round of gamma draft tokens yields at an acceptance per token, gamma plus one when every
// token is accepted
func tokensPerRound(alpha, gamma float64) float64 {
	switch {
	case alpha >= 1-1e-9:
		return gamma + 1
	case alpha <= 0:
		return 1
	}
	return (1 - math.Pow(alpha, gamma+1)) / (1 - alpha)
}

// Lockstep: matching nodes on fabric links, every layer divided by the seat count
func (pl *planner) locksteps() []*candidate {
	nodes := pl.supporting(v1.Shape_SHAPE_LOCKSTEP)
	if len(nodes) < 2 {
		return pl.short(v1.Shape_SHAPE_LOCKSTEP)
	}
	headRole, rankRole, ok := pl.roles(v1.Shape_SHAPE_LOCKSTEP)
	if !ok {
		pl.reject(v1.Shape_SHAPE_LOCKSTEP, nodes, fmt.Sprintf("%s declares no lockstep roles", pl.req.Runtime.ID()))
		return nil
	}
	var out []*candidate
	L := float64(pl.parts.layerCount())
	reductions := pl.facts.ReductionsPerLayer
	if reductions == 0 {
		reductions = 2
	}
	d := pl.parts.model.Embedding
	act := pl.facts.ActivationBytes
	if act == 0 {
		act = 2
	}
	for mask := 1; mask < 1<<len(nodes); mask++ {
		var members []*Node
		for i := range nodes {
			if mask&(1<<i) != 0 {
				members = append(members, nodes[i])
			}
		}
		k := len(members)
		if k < 2 {
			continue
		}
		ids := nodeIDs(members)
		c := &candidate{shape: v1.Shape_SHAPE_LOCKSTEP, nodes: ids}
		pl.noteRanks(members)
		class, slowest, _ := pl.mesh.worstClass(ids)
		c.class = class
		var linkNote string
		if a, b, unmeasured := pl.mesh.unmeasured(ids); unmeasured {
			pl.noteUnmeasured(a, b)
			linkNote = fmt.Sprintf("link %s to %s unmeasured, priced at %s round trip until a probe runs, every reduction pays it", pl.mesh.Node(a).label(), pl.mesh.Node(b).label(), seconds(unmeasuredRTT))
		} else if class != v1.LinkClass_LINK_CLASS_FABRIC {
			linkNote = fmt.Sprintf("link class %s, %s round trip between %s and %s, every reduction pays it", className(class), seconds(float64(slowest.GetRttUs())/1e6), pl.mesh.Node(slowest.GetFrom()).label(), pl.mesh.Node(slowest.GetTo()).label())
		}
		sliced := pl.parts.slice(k)
		for rank, n := range members {
			role := rankRole
			if rank == 0 {
				role = headRole
			}
			s := &seat{node: n, role: role.Name, rank: uint32(rank), phase: role.Phase, from: 0, to: pl.parts.layerCount(), exposed: role.Rendezvous}
			plan, err := pl.fitDescriptor(n, fmt.Sprintf("slice%d", k), sliced, v1.Placement_PLACEMENT_DEVICE)
			if err != nil {
				pl.refused(s, err)
			} else {
				s.plan = plan
				_, extraParams := groupTotals(pl.parts.extras)
				s.params = (pl.parts.readParams(0, pl.parts.layerCount()) + extraParams) / float64(k)
				if err := pl.finish(s, pl.parts.expertShare); err != nil {
					pl.refused(s, err)
				}
			}
			c.seats = append(c.seats, s)
			if rank == 0 {
				c.head = s
			}
		}
		if c.verdict == v1.FitVerdict_FIT_VERDICT_UNSPECIFIED {
			c.verdict = worstVerdict(c.seats)
			if c.verdict != v1.FitVerdict_FIT_VERDICT_FITS {
				for _, s := range c.seats {
					if s.verdict != v1.FitVerdict_FIT_VERDICT_FITS {
						c.reason = fmt.Sprintf("%d seats leave %s over capacity: %s", k, s.node.label(), s.detail)
						break
					}
				}
			}
		}
		// The slowest reduction sets the pace: a fabric figure, or twice the round trip on sockets.
		var a, wWorst float64
		for i, x := range ids {
			for _, y := range ids[i+1:] {
				rtt := pl.linkRTT(x, y)
				lat := 2 * rtt
				if pl.mesh.rdma(x, y) && class == v1.LinkClass_LINK_CLASS_FABRIC {
					lat = fabricReduction
				}
				a = math.Max(a, lat)
				for _, pair := range [][2]string{{x, y}, {y, x}} {
					w := pl.linkBandwidth(pair[0], pair[1], true)
					if wWorst == 0 || w < wWorst {
						wWorst = w
					}
				}
			}
		}
		var decode float64
		for _, s := range c.seats {
			decode = math.Max(decode, s.decode())
			pc := s.prefillCost()
			c.pieces = append(c.pieces, piece{fixed: pc.fixed + reductions*L*a, perToken: pc.perToken + reductions*L*act*d/wWorst})
		}
		c.decode = decode + reductions*L*a
		pl.price(c)
		summary := fmt.Sprintf("%d seats each read %s per token, %.0f reductions at %s", k, human(uint64(c.seats[0].read)), reductions*L, seconds(a))
		if linkNote != "" {
			summary += "; " + linkNote
		}
		if c.reason == "" {
			c.reason = summary
		} else {
			c.reason += "; " + summary
		}
		out = append(out, c)
	}
	return out
}

// Notes ranks that differ in vendor or device count, so the plan says so and the runtime decides
func (pl *planner) noteRanks(nodes []*Node) {
	first := nodes[0]
	for _, n := range nodes[1:] {
		if n.Vendor != first.Vendor {
			pl.note(fmt.Sprintf("%s has %s devices and %s has %s, the ranks differ in vendor", first.label(), first.Vendor, n.label(), n.Vendor))
		}
		if len(n.Devices) != len(first.Devices) {
			pl.note(fmt.Sprintf("%s has %d devices and %s has %d, the ranks differ in device count", first.label(), len(first.Devices), n.label(), len(n.Devices)))
		}
	}
	pl.noteBuilds(nodes)
}

// Notes seats on different builds, so the plan says so and the runtime decides
func (pl *planner) noteBuilds(nodes []*Node) {
	first := nodes[0]
	for _, n := range nodes[1:] {
		if n.Install.GetVersion() != first.Install.GetVersion() {
			pl.note(fmt.Sprintf("%s runs %s %s and %s runs %s, the seats run different builds", first.label(), pl.req.Runtime.ID(), first.Install.GetVersion(), n.label(), n.Install.GetVersion()))
		}
	}
}

// Relay: every ordered pair that fits solo, the prefill seat computing the prompt and the decode
// seat continuing it with the cache moved between them
func (pl *planner) relays() []*candidate {
	nodes := pl.supporting(v1.Shape_SHAPE_RELAY)
	if len(nodes) < 2 {
		return pl.short(v1.Shape_SHAPE_RELAY)
	}
	decodeRole, prefillRole, ok := pl.roles(v1.Shape_SHAPE_RELAY)
	if !ok {
		pl.reject(v1.Shape_SHAPE_RELAY, nodes, fmt.Sprintf("%s declares no relay roles", pl.req.Runtime.ID()))
		return nil
	}
	solos := map[string]*seat{}
	for _, n := range nodes {
		solos[n.ID] = pl.solo(n, decodeRole)
	}
	var out []*candidate
	P := pl.prompt()
	for _, p := range nodes {
		for _, q := range nodes {
			if p == q {
				continue
			}
			pre := *solos[p.ID]
			pre.role, pre.phase, pre.rank, pre.exposed = prefillRole.Name, prefillRole.Phase, 0, prefillRole.Rendezvous
			dec := *solos[q.ID]
			dec.role, dec.phase, dec.rank, dec.exposed = decodeRole.Name, decodeRole.Phase, 1, decodeRole.Rendezvous
			c := &candidate{shape: v1.Shape_SHAPE_RELAY, seats: []*seat{&pre, &dec}, head: &dec}
			c.class, _, _ = pl.mesh.worstClass([]string{p.ID, q.ID})
			c.decode = dec.decode()
			c.pieces = []piece{pre.prefillCost()}
			pl.price(c)
			if pre.verdict != v1.FitVerdict_FIT_VERDICT_FITS || dec.verdict != v1.FitVerdict_FIT_VERDICT_FITS {
				c.verdict = v1.FitVerdict_FIT_VERDICT_NO
				which := &pre
				if pre.verdict == v1.FitVerdict_FIT_VERDICT_FITS {
					which = &dec
				}
				c.reason = fmt.Sprintf("%s does not fit the model alone", which.node.label())
				if which.detail != "" {
					c.reason += ": " + which.detail
				}
				out = append(out, c)
				continue
			}
			pl.noteBuilds([]*Node{p, q})
			if why := cacheTypesDiffer(&pre, &dec); why != "" {
				c.verdict, c.reason = v1.FitVerdict_FIT_VERDICT_NO, why
				out = append(out, c)
				continue
			}
			if pl.context <= 0 {
				c.verdict, c.reason = v1.FitVerdict_FIT_VERDICT_NO, "the planned context is zero, so the cache a prompt token adds is unknown"
				out = append(out, c)
				continue
			}
			w := pl.relayBandwidth(p.ID, q.ID)
			perToken := pre.cache / float64(pl.context)
			move := piece{perToken: perToken / w}
			if pl.facts.RelayDisk {
				pl.useDisk(p)
				pl.useDisk(q)
				if p.Disk.Stream <= 0 || q.Disk.Stream <= 0 {
					c.verdict, c.reason = v1.FitVerdict_FIT_VERDICT_NO, fmt.Sprintf("the disk throughput of %s or %s is unknown, and this runtime's relay writes the cache through both disks", p.label(), q.label())
					out = append(out, c)
					continue
				}
				move.perToken += perToken/p.Disk.Stream + perToken/q.Disk.Stream
			}
			overlap := 0.0
			if pl.facts.RelayOverlap && !pl.facts.RelayDisk {
				overlap = 1
			}
			pf := pre.prefillCost()
			// The prompt's cost is the prefill, or the move with what the prefill does not hide.
			c.pieces = []piece{pf, {fixed: (1 - overlap) * pf.fixed, perToken: move.perToken + (1-overlap)*pf.perToken}}
			pl.price(c)
			c.verdict = v1.FitVerdict_FIT_VERDICT_FITS
			// The break even prompt: where the pair beats the better node alone.
			bestSolo := func(prompt float64) float64 {
				best := math.Inf(1)
				for _, s := range solos {
					if s.verdict != v1.FitVerdict_FIT_VERDICT_FITS {
						continue
					}
					best = math.Min(best, s.prefill(prompt)+pl.completion()*s.decode())
				}
				return best
			}
			pays := func(prompt float64) bool {
				return evalPieces(c.pieces, prompt)+pl.completion()*c.decode < bestSolo(prompt)
			}
			// Doubling finds the first length that pays, bisection the exact crossing below it.
			for lo, hi := 0.0, 256.0; hi <= relayMaxPrompt; lo, hi = hi, hi*2 {
				if !pays(hi) {
					continue
				}
				for hi-lo > 1 {
					mid := math.Floor((lo + hi) / 2)
					if pays(mid) {
						hi = mid
					} else {
						lo = mid
					}
				}
				c.breakEven = uint32(hi)
				break
			}
			same := p.gamma() == q.gamma() && p.beta() == q.beta()
			switch {
			case same:
				c.reason = fmt.Sprintf("%s and %s prefill and decode alike, so a relay gains nothing over solo", p.label(), q.label())
			case p.gamma() <= q.gamma():
				c.reason = fmt.Sprintf("%s prefills no faster than %s, the pair costs the cache move %s for nothing", p.label(), q.label(), seconds(move.at(P)))
			case c.breakEven == 0:
				c.reason = fmt.Sprintf("the cache move at %s costs more than %s's prefill saves at every prompt length up to %d", gbits(w), p.label(), relayMaxPrompt)
			default:
				c.reason = fmt.Sprintf("%s prefills at %s and %s decodes at %s, cache %s per token moved at %s, pays above %d prompt tokens", p.label(), seconds(pre.prefill(P)), q.label(), seconds(dec.decode()), human(uint64(perToken)), gbits(w), c.breakEven)
			}
			out = append(out, c)
		}
	}
	return out
}

// Why two relay seats cannot exchange a cache: their plans resolved different cache types
func cacheTypesDiffer(a, b *seat) string {
	for _, k := range []string{"cache_type_k", "cache_type_v"} {
		x, y := a.plan.GetParams()[k], b.plan.GetParams()[k]
		if x != y {
			return fmt.Sprintf("%s resolves %s %s and %s resolves %s, a relay's seats share one cache type", a.node.label(), k, x, b.node.label(), y)
		}
	}
	return ""
}

// Bandwidth a relay's cache moves at: the aggregate over an RDMA device when the runtime takes
// it, the stream otherwise
func (pl *planner) relayBandwidth(from, to string) float64 {
	return pl.linkBandwidth(from, to, pl.facts.RelayRDMA && pl.mesh.rdma(from, to))
}

// Replicas: every node that fits solo, throughput summed, latency the conductor's seat's
func (pl *planner) replicas() []*candidate {
	nodes := pl.supporting(v1.Shape_SHAPE_REPLICAS)
	role, _, ok := pl.roles(v1.Shape_SHAPE_REPLICAS)
	if !ok {
		pl.reject(v1.Shape_SHAPE_REPLICAS, nodes, fmt.Sprintf("%s declares no replica role", pl.req.Runtime.ID()))
		return nil
	}
	var seats []*seat
	var rejected []string
	for _, n := range nodes {
		s := pl.solo(n, role)
		if s.verdict != v1.FitVerdict_FIT_VERDICT_FITS {
			why := fmt.Sprintf("%s does not fit the model", n.label())
			if s.detail != "" {
				why += ": " + s.detail
			}
			rejected = append(rejected, why)
			continue
		}
		s.rank = uint32(len(seats))
		seats = append(seats, s)
	}
	if len(seats) < 2 {
		if len(nodes) >= 2 {
			c := &candidate{shape: v1.Shape_SHAPE_REPLICAS, nodes: nodeIDs(nodes), verdict: v1.FitVerdict_FIT_VERDICT_NO, reason: strings.Join(rejected, "; ")}
			if c.reason == "" {
				c.reason = "fewer than two nodes fit the model"
			}
			c.seats = seats
			if len(seats) == 1 {
				c.head = seats[0]
			}
			return []*candidate{c}
		}
		return nil
	}
	c := &candidate{shape: v1.Shape_SHAPE_REPLICAS, seats: seats, verdict: v1.FitVerdict_FIT_VERDICT_FITS}
	c.head = seats[0]
	for _, s := range seats {
		if s.node.ID == pl.mesh.Self {
			c.head = s
		}
	}
	P := pl.prompt()
	c.decode = c.head.decode()
	c.pieces = []piece{c.head.prefillCost()}
	pl.price(c)
	c.rps = 0
	for _, s := range seats {
		c.rps += 1 / (s.prefill(P) + pl.completion()*s.decode())
	}
	c.class, _, _ = pl.mesh.worstClass(c.nodeIDs())
	c.reason = fmt.Sprintf("%d seats answer %.2f requests per second together, each request as fast as its seat alone", len(seats), c.rps)
	if len(rejected) > 0 {
		c.reason += "; " + strings.Join(rejected, "; ")
	}
	return []*candidate{c}
}

// Draft: the target on a node that fits it, the draft model on another node, token ids crossing
func (pl *planner) drafts() []*candidate {
	nodes := pl.supporting(v1.Shape_SHAPE_DRAFT)
	if len(nodes) < 2 {
		return pl.short(v1.Shape_SHAPE_DRAFT)
	}
	if pl.req.Draft == nil {
		return []*candidate{{shape: v1.Shape_SHAPE_DRAFT, nodes: nodeIDs(nodes), verdict: v1.FitVerdict_FIT_VERDICT_NO, reason: "no draft model named: set draft_model to a stored companion as store://source/repo#group to draft on another node"}}
	}
	headRole, stageRole, ok := pl.roles(v1.Shape_SHAPE_DRAFT)
	if !ok {
		pl.reject(v1.Shape_SHAPE_DRAFT, nodes, fmt.Sprintf("%s declares no draft roles", pl.req.Runtime.ID()))
		return nil
	}
	gamma := pl.draftTokens()
	dp := split(pl.req.Draft)
	_, draftExtraParams := groupTotals(dp.extras)
	draftParams := dp.readParams(0, dp.layerCount()) + draftExtraParams
	var out []*candidate
	for _, h := range nodes {
		head := pl.solo(h, headRole)
		if head.verdict != v1.FitVerdict_FIT_VERDICT_FITS {
			why := fmt.Sprintf("%s does not fit the target alone, so it cannot head a draft", h.label())
			if head.detail != "" {
				why += ": " + head.detail
			}
			pl.reject(v1.Shape_SHAPE_DRAFT, []*Node{h}, why)
			continue
		}
		for _, s := range nodes {
			if s == h {
				continue
			}
			stage := &seat{node: s, role: stageRole.Name, rank: 1, phase: stageRole.Phase, exposed: stageRole.Rendezvous, to: dp.layerCount()}
			c := &candidate{shape: v1.Shape_SHAPE_DRAFT, seats: []*seat{stage, head}, head: head, draftTokens: uint32(gamma)}
			c.class, _, _ = pl.mesh.worstClass([]string{h.ID, s.ID})
			pl.noteBuilds([]*Node{h, s})
			plan, err := pl.fitDraft(s, "draft", s.Profile)
			if err != nil || plan.GetVerdict() != v1.FitVerdict_FIT_VERDICT_FITS {
				stage.plan = plan
				if plan == nil {
					stage.plan = &v1.MemoryPlan{Verdict: v1.FitVerdict_FIT_VERDICT_NO}
				}
				stage.verdict = v1.FitVerdict_FIT_VERDICT_NO
				c.verdict = v1.FitVerdict_FIT_VERDICT_NO
				c.reason = fmt.Sprintf("%s does not fit the draft model", s.label())
				if err != nil {
					c.reason += ": " + err.Error()
				} else if plan.GetDetail() != "" {
					c.reason += ": " + plan.GetDetail()
				}
				out = append(out, c)
				continue
			}
			stage.plan = plan
			stage.params = draftParams
			if err := pl.finish(stage, dp.expertShare); err != nil {
				pl.refused(stage, err)
				c.verdict, c.reason = v1.FitVerdict_FIT_VERDICT_NO, shortfall(stage)
				out = append(out, c)
				continue
			}
			rtt := pl.linkRTT(h.ID, s.ID)
			alpha := head.node.acceptance()
			c.acceptance = alpha
			round := gamma*(rtt+stage.decode()) + head.decode()
			perRound := tokensPerRound(alpha, gamma)
			c.decode = round / perRound
			c.pieces = []piece{head.prefillCost()}
			pl.price(c)
			c.verdict = v1.FitVerdict_FIT_VERDICT_FITS
			c.reason = fmt.Sprintf("draft on %s, %.0f tokens per round each a %s round trip and %s draft step, verified in one %s target step on %s at acceptance %.2f: %.1f tokens per round", s.label(), gamma, seconds(rtt), seconds(stage.decode()), seconds(head.decode()), h.label(), alpha, perRound)
			out = append(out, c)
		}
	}
	return out
}

// Plans the draft model on a node's profile under the runtime's rules, once per distinct request
func (pl *planner) fitDraft(n *Node, key string, profile *v1.HostProfile) (*v1.MemoryPlan, error) {
	key = fmt.Sprintf("%s|%s|%d|%d", n.ID, key, pl.req.Placement, pl.context)
	if r, ok := pl.fits[key]; ok {
		return r.plan, r.err
	}
	in := estimate.Input{
		Descriptor: pl.req.Draft,
		Family:     pl.req.DraftFamily,
		Host:       profile,
		Params:     pl.fixed(),
		Free:       pl.req.Free,
		Placement:  pl.req.Placement,
		Companions: pl.req.Companions,
		Repo:       pl.req.DraftRepo,
	}
	if pl.req.Calibration != nil {
		in.OverheadDelta = pl.req.Calibration(n.ID)
	}
	plan, err := pl.policy.Plan(in)
	pl.fits[key] = fitResult{plan, err}
	return plan, err
}

// The acceptance the node's fastest device learned, or the default
func (n *Node) acceptance() float64 {
	if len(n.Devices) > 0 && n.Devices[0].Numbers.Acceptance > 0 {
		return n.Devices[0].Numbers.Acceptance
	}
	if n.CPU != nil && n.CPU.Numbers.Acceptance > 0 {
		return n.CPU.Numbers.Acceptance
	}
	return 0.7
}

// The crossings of a stages request: the embedding to the denoiser's node and the latent back
type crossings struct {
	embeddingPerToken, latent float64
	toDenoiser, back          float64
	rtt                       float64
}

// Sizes of a pipeline request from the resolved params: the denoiser's tokens at its 16 pixel
// patch and the positions the decoder upsamples from at the VAE's 8 pixel patch
type pipelineSizes struct {
	steps, latentTokens, latentPositions, embeddingPerToken, latent float64
}

func (pl *planner) sizes() pipelineSizes {
	steps := float64(pl.params.Int("steps"))
	if steps <= 0 {
		steps = 30
	}
	width, height := float64(pl.params.Int("width")), float64(pl.params.Int("height"))
	if width <= 0 {
		width = 1024
	}
	if height <= 0 {
		height = 1024
	}
	frames := math.Max(1, float64(pl.params.Int("video_frames")))
	out := pipelineSizes{steps: steps, latentTokens: width * height / 256 * frames, latentPositions: width * height / 64 * frames}
	out.embeddingPerToken = pl.parts.model.Embedding * 2
	if pl.parts.model.Embedding == 0 {
		out.embeddingPerToken = 4096 * 2
	}
	out.latent = out.latentTokens * 16 * 2
	return out
}

// The compute a seat runs a part with: its CPU when the part landed in host memory, its device otherwise
func partCompute(s *seat, kind v1.TensorGroupKind) (float64, error) {
	host := text.Enum(v1.PoolKind_POOL_KIND_HOST)
	for _, p := range s.plan.GetPlacements() {
		if p.GetKind() != kind {
			continue
		}
		if p.GetPoolId() == host {
			if s.node.CPU == nil {
				return 0, fmt.Errorf("%s holds %s in host memory and has no CPU numbers", s.node.label(), text.Enum(kind))
			}
			return s.node.CPU.Numbers.Compute, nil
		}
		if d := s.primaryDevice(); d != nil {
			return d.Numbers.Compute, nil
		}
		return 0, fmt.Errorf("%s holds %s on a device it has no numbers for", s.node.label(), text.Enum(kind))
	}
	if d := s.primaryDevice(); d != nil {
		return d.Numbers.Compute, nil
	}
	if s.node.CPU != nil {
		return s.node.CPU.Numbers.Compute, nil
	}
	return 0, fmt.Errorf("%s has no compute numbers for %s", s.node.label(), text.Enum(kind))
}

// Per request cost of a diffusion pipeline: encoder parameters times prompt tokens over the
// compute of the seat holding the encoders, denoiser parameters times steps times latent tokens
// over the seat holding the denoiser, decoder parameters times the latent positions it decodes
// from over the seat holding the decoder, plus the crossings when the denoiser sits on another node
func (pl *planner) pipeline(enc, den, dec *seat, x *crossings) ([]piece, string, error) {
	sz := pl.sizes()
	_, denParams := groupTotals(pl.parts.denoiser)
	_, encParams := groupTotals(pl.parts.encoders)
	_, decParams := groupTotals(pl.parts.decoder)
	encGamma, err := partCompute(enc, v1.TensorGroupKind_TENSOR_GROUP_KIND_TEXT_ENCODER)
	if err != nil {
		return nil, "", err
	}
	denGamma, err := partCompute(den, v1.TensorGroupKind_TENSOR_GROUP_KIND_DIFFUSION)
	if err != nil {
		return nil, "", err
	}
	decGamma, err := partCompute(dec, v1.TensorGroupKind_TENSOR_GROUP_KIND_VAE)
	if err != nil {
		return nil, "", err
	}
	P := pl.prompt()
	encodePerToken := 2 * encParams / encGamma
	denoise := 2 * denParams * sz.steps * sz.latentTokens / denGamma
	decode := 2 * decParams * sz.latentPositions / decGamma
	cost := piece{fixed: denoise + decode, perToken: encodePerToken}
	notes := []string{fmt.Sprintf("encoding %s on %s", seconds(encodePerToken*P), enc.node.label())}
	if x != nil {
		cost.fixed += 2*x.rtt + x.latent/x.back
		cost.perToken += x.embeddingPerToken / x.toDenoiser
		notes = append(notes, fmt.Sprintf("%s embedding across in %s", human(uint64(x.embeddingPerToken*P)), seconds(x.embeddingPerToken*P/x.toDenoiser)))
	}
	notes = append(notes, fmt.Sprintf("%.0f denoising steps on %s in %s", sz.steps, den.node.label(), seconds(denoise)))
	if x != nil {
		notes = append(notes, fmt.Sprintf("%s latent back in %s", human(uint64(x.latent)), seconds(x.latent/x.back)))
	}
	notes = append(notes, fmt.Sprintf("decoding %s on %s", seconds(decode), dec.node.label()))
	return []piece{cost}, strings.Join(notes, ", "), nil
}

// Stages: the denoiser on the node with the fastest device that fits it, the encoders and
// decoder on a node that fits them in host memory, priced per request
func (pl *planner) stages() []*candidate {
	nodes := pl.supporting(v1.Shape_SHAPE_STAGES)
	if len(nodes) < 2 {
		return nil
	}
	headRole, denRole, ok := pl.roles(v1.Shape_SHAPE_STAGES)
	if !ok {
		pl.reject(v1.Shape_SHAPE_STAGES, nodes, fmt.Sprintf("%s declares no stages roles", pl.req.Runtime.ID()))
		return nil
	}
	denoiser := pl.parts.only(pl.parts.denoiser)
	rest := pl.parts.only(append(append(append([]*v1.TensorGroup{}, pl.parts.encoders...), pl.parts.decoder...), pl.parts.extras...))
	_, denParams := groupTotals(pl.parts.denoiser)
	_, encParams := groupTotals(pl.parts.encoders)
	_, decParams := groupTotals(pl.parts.decoder)
	sz := pl.sizes()
	var out []*candidate
	for _, dn := range nodes {
		if len(dn.Devices) == 0 {
			pl.reject(v1.Shape_SHAPE_STAGES, []*Node{dn}, fmt.Sprintf("%s has no device pool, the denoiser runs on a device", dn.label()))
			continue
		}
		den := &seat{node: dn, role: denRole.Name, rank: 1, phase: denRole.Phase, exposed: denRole.Rendezvous}
		plan, err := pl.fitDescriptor(dn, "denoiser", denoiser, v1.Placement_PLACEMENT_DEVICE)
		if err != nil {
			pl.reject(v1.Shape_SHAPE_STAGES, []*Node{dn}, fmt.Sprintf("the denoiser cannot be planned on %s: %v", dn.label(), err))
			continue
		}
		den.plan = plan
		den.params = denParams
		if err := pl.finish(den, 1); err != nil {
			pl.reject(v1.Shape_SHAPE_STAGES, []*Node{dn}, fmt.Sprintf("the denoiser on %s: %v", dn.label(), err))
			continue
		}
		if len(den.devices) == 0 && den.verdict == v1.FitVerdict_FIT_VERDICT_FITS {
			pl.reject(v1.Shape_SHAPE_STAGES, []*Node{dn}, fmt.Sprintf("the denoiser lands on no device of %s", dn.label()))
			continue
		}
		for _, hn := range nodes {
			if hn == dn {
				continue
			}
			// The head keeps the encoders and the decoder in host memory beside the denoiser's stage.
			head := &seat{node: hn, role: headRole.Name, rank: 0, phase: headRole.Phase, exposed: headRole.Rendezvous}
			hplan, err := pl.fitDescriptor(hn, "stages-head", rest, v1.Placement_PLACEMENT_HOST)
			if err != nil {
				pl.reject(v1.Shape_SHAPE_STAGES, []*Node{hn}, fmt.Sprintf("the encoders and decoder cannot be planned on %s: %v", hn.label(), err))
				continue
			}
			head.plan = hplan
			head.params = encParams + decParams
			if err := pl.finish(head, 1); err != nil {
				pl.reject(v1.Shape_SHAPE_STAGES, []*Node{hn}, fmt.Sprintf("the encoders and decoder on %s: %v", hn.label(), err))
				continue
			}
			c := &candidate{shape: v1.Shape_SHAPE_STAGES, seats: []*seat{den, head}, head: head}
			c.class, _, _ = pl.mesh.worstClass([]string{dn.ID, hn.ID})
			c.verdict = worstVerdict(c.seats)
			if c.verdict != v1.FitVerdict_FIT_VERDICT_FITS {
				if den.verdict != v1.FitVerdict_FIT_VERDICT_FITS {
					c.reason = fmt.Sprintf("the denoiser does not fit %s's device: %s", dn.label(), den.detail)
				} else {
					c.reason = fmt.Sprintf("the encoders and decoder do not fit %s's host memory: %s", hn.label(), head.detail)
				}
			}
			w := pl.linkBandwidth(hn.ID, dn.ID, false)
			wBack := pl.linkBandwidth(dn.ID, hn.ID, false)
			rtt := pl.linkRTT(hn.ID, dn.ID)
			cost, note, err := pl.pipeline(head, den, head, &crossings{embeddingPerToken: sz.embeddingPerToken, latent: sz.latent, toDenoiser: w, back: wBack, rtt: rtt})
			if err != nil {
				c.verdict, c.reason = v1.FitVerdict_FIT_VERDICT_NO, err.Error()
				out = append(out, c)
				continue
			}
			c.pieces = cost
			c.decode = 0
			pl.price(c)
			if c.reason == "" {
				c.reason = note
			}
			out = append(out, c)
		}
	}
	return out
}

func (pl *planner) names(seats []*seat) string {
	var out []string
	for _, s := range seats {
		out = append(out, s.node.label())
	}
	return strings.Join(out, ", ")
}
