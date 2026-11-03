package formations

import (
	"context"
	"fmt"
	"path"
	"sort"
	"strings"

	"connectrpc.com/connect"
	"github.com/nickheyer/nebu/internal/db"
	"github.com/nickheyer/nebu/internal/instances"
	"github.com/nickheyer/nebu/internal/mesh"
	planner "github.com/nickheyer/nebu/pkg/estimate/mesh"
	"github.com/nickheyer/nebu/pkg/host"
	"github.com/nickheyer/nebu/pkg/perf"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/runtimes"
	"github.com/nickheyer/nebu/pkg/store"
	"github.com/nickheyer/nebu/pkg/text"
	"google.golang.org/protobuf/proto"
)

// Traces the route's profile is learned from
const statsTraces = 200

// What a plan resolved beside the plan itself
type planned struct {
	plan       *v1.FormationPlan
	descriptor *v1.Descriptor
	stored     *v1.StoredModel
	rt         runtimes.Runtime
	name       string
	// Run params over the slot's, what every seat launches with
	params map[string]string
	// The slot's and the run's extra words for every seat that runs the runtime's server
	args string
	// The draft model's store key, for a draft head
	draftKey string
	draft    *v1.StoredModel
}

// Previews a formation plan without launching, free memory for slot or free requests so swaps see occupants, total otherwise
func (m *Manager) Plan(ctx context.Context, run *v1.RunRequest) (*v1.FormationPlan, *v1.Descriptor, error) {
	p, err := m.planWith(ctx, run, run.GetFree() || run.GetSlotId() != "", nil)
	if err != nil {
		return nil, nil, err
	}
	return p.plan, p.descriptor, nil
}

// Prices a launch against free memory as a solo launch does on its node
func (m *Manager) plan(ctx context.Context, run *v1.RunRequest) (*planned, error) {
	return m.planWith(ctx, run, true, nil)
}

// Prices a launch with each node held to the install a pin names, so a relaunch never moves a seat to a newer install
func (m *Manager) planPinned(ctx context.Context, run *v1.RunRequest, pinned map[string]string) (*planned, error) {
	if m.planner != nil {
		return m.planner(ctx, run)
	}
	return m.planWith(ctx, run, true, pinned)
}

func (m *Manager) planWith(ctx context.Context, run *v1.RunRequest, free bool, pinned map[string]string) (*planned, error) {
	if !m.Mesh.Joined() {
		return nil, mesh.ErrNoMesh
	}
	if run.GetRepo() == "" || run.GetGroup() == "" {
		return nil, fmt.Errorf("%w: a formation names a repo and a weight group", ErrFormation)
	}
	if err := runtimes.CheckArgs(run.GetArgs()); err != nil {
		return nil, err
	}
	nodes := m.Mesh.Nodes()
	span := map[string]bool{}
	// Devices a node may use, every device when the span names the node alone
	allowed := map[string]map[string]bool{}
	var spanIDs []string
	for _, entry := range run.GetSpan() {
		nodeRef, device, _ := strings.Cut(entry, "/")
		rec, err := m.Mesh.Node(nodeRef)
		if err != nil {
			return nil, fmt.Errorf("%w: span: %v", ErrFormation, err)
		}
		if !span[rec.GetId()] {
			spanIDs = append(spanIDs, rec.GetId())
		}
		span[rec.GetId()] = true
		if device != "" {
			d := host.Device(rec.GetProfile(), device)
			if d == nil {
				return nil, fmt.Errorf("%w: span: %s has no device %q, see nebu mesh nodes", ErrFormation, nodeName(rec), device)
			}
			if !host.OwnMemory(rec.GetProfile(), d) {
				return nil, fmt.Errorf("%w: span: device %q (%s) on %s has no memory of its own, so a seat cannot pin it", ErrFormation, device, d.GetName(), nodeName(rec))
			}
			if allowed[rec.GetId()] == nil {
				allowed[rec.GetId()] = map[string]bool{}
			}
			allowed[rec.GetId()][device] = true
		} else {
			allowed[rec.GetId()] = nil
		}
	}
	stored, err := m.manifest(ctx, run.GetSourceId(), run.GetRepo(), run.GetGroup(), nodes)
	if err != nil {
		return nil, err
	}
	descriptor := stored.GetDescriptor_()
	if descriptor == nil || descriptor.GetKind() == v1.ModelKind_MODEL_KIND_UNSPECIFIED {
		return nil, fmt.Errorf("%w: %s %s has no descriptor yet, pull it again or open it once on the node holding it", ErrFormation, run.GetRepo(), run.GetGroup())
	}
	rt, err := m.runtime(run, stored, descriptor, nodes, span)
	if err != nil {
		return nil, err
	}
	name := run.GetName()
	if name == "" {
		name = path.Base(stored.GetRepo()) + ":" + stored.GetGroup()
	}
	companions, err := m.Inspector.Companions(stored.GetSourceId(), stored.GetRepo(), stored.GetGroup())
	if err != nil {
		return nil, err
	}
	stats, err := m.stats(ctx, name)
	if err != nil {
		return nil, err
	}
	params := run.GetParams()
	var budget uint64
	var slotArgs string
	placement := v1.Placement_PLACEMENT_UNSPECIFIED
	if run.GetSlotId() != "" {
		if m.SlotView == nil {
			return nil, fmt.Errorf("%w: slot %s: no slots are wired to the conductor", ErrFormation, run.GetSlotId())
		}
		slot, _, err := m.SlotView.Get(run.GetSlotId())
		if err != nil {
			return nil, fmt.Errorf("%w: slot %s: %v", ErrFormation, run.GetSlotId(), err)
		}
		params = runtimes.Merge(slot.GetParams(), run.GetParams())
		budget, placement, slotArgs = slot.GetMemoryBytes(), slot.GetPlacement(), slot.GetArgs()
	}
	req := planner.Request{
		Descriptor: descriptor,
		Family:     m.Inspector.Builder.Family(descriptor),
		Runtime:    rt,
		Params:     params,
		Placement:  placement,
		Shape:      run.GetShape(),
		Profile:    run.GetProfile(),
		Span:       spanIDs,
		Free:       free,
		Repo:       stored.GetRepo(),
		Companions: companions,
		Stats:      stats,
		Table:      m.Perf,
		Excluded:   map[string]string{},
		Calibration: func(nodeID string) float64 {
			if nodeID == m.self() && m.Calibration != nil {
				return m.Calibration.Delta(rt.ID(), descriptor.GetArchitecture())
			}
			return 0
		},
	}
	out := &planned{descriptor: descriptor, stored: stored, rt: rt, name: name, params: params, args: runtimes.JoinArgs(slotArgs, run.GetArgs())}
	draft, err := m.draft(ctx, params, rt, stored, companions, nodes)
	if err != nil {
		return nil, err
	}
	if draft != nil {
		req.Draft, req.DraftFamily, req.DraftRepo = draft.GetDescriptor_(), m.Inspector.Builder.Family(draft.GetDescriptor_()), draft.GetRepo()
		out.draft = draft
		out.draftKey = store.Key(draft.GetSourceId(), draft.GetRepo(), draft.GetGroup())
	}
	var members []*planner.Node
	for _, rec := range nodes {
		if len(span) > 0 && !span[rec.GetId()] {
			continue
		}
		if rec.GetId() != m.self() && rec.GetState() != v1.NodeState_NODE_STATE_READY {
			req.Excluded[rec.GetId()] = fmt.Sprintf("%s is %s since %s", nodeName(rec), text.Enum(rec.GetState()), rec.GetSeenAt().AsTime().Format("2006-01-02 15:04:05"))
			continue
		}
		caps := map[string][]v1.Shape{}
		for _, c := range rec.GetCapabilities() {
			caps[c.GetInstallId()] = c.GetShapes()
		}
		var pins []string
		for id := range allowed[rec.GetId()] {
			pins = append(pins, id)
		}
		sort.Strings(pins)
		numbers := m.numbersFor(rec)
		// A slot's budget, placement and device pins bound every seat as they bound a solo run
		if len(pins) > 0 || budget > 0 {
			rec = proto.Clone(rec).(*v1.Node)
			rec.Profile = instances.Constrain(rec.GetProfile(), pins, budget, placement)
		}
		installID := run.GetInstallId()
		if held, ok := pinned[rec.GetId()]; ok {
			installID = held
		}
		members = append(members, planner.NodeOf(rec, rt.ID(), installID, caps, numbers))
	}
	plan, err := planner.Plan(req, planner.New(members, m.Mesh.Links(), m.self()))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrFormation, err)
	}
	plan.RuntimeId = rt.ID()
	for _, s := range plan.GetSeats() {
		if rec, err := m.Mesh.Node(s.GetNodeId()); err == nil {
			s.NodeName = rec.GetName()
		}
	}
	out.plan = plan
	return out, nil
}

func nodeName(rec *v1.Node) string {
	if rec.GetName() != "" {
		return rec.GetName()
	}
	return rec.GetId()
}

// The device numbers a node plans with, this node's learned table or the throughput rows its record carries
func (m *Manager) numbersFor(rec *v1.Node) func(*v1.Device) perf.Numbers {
	if rec.GetId() == m.self() {
		return m.Perf.Numbers
	}
	learned := rec.GetThroughput()
	return func(d *v1.Device) perf.Numbers { return m.Perf.NumbersFor(d, learned) }
}

// The manifest of the model, from this node's store or a member holding it
func (m *Manager) manifest(ctx context.Context, sourceID, repo, group string, nodes []*v1.Node) (*v1.StoredModel, error) {
	if stored, err := m.Store.ReadManifest(sourceID, repo, group); err == nil {
		return stored, nil
	}
	for _, rec := range nodes {
		if rec.GetId() == m.self() || rec.GetState() != v1.NodeState_NODE_STATE_READY {
			continue
		}
		if !holds(rec, sourceID, repo, group) {
			continue
		}
		cl, err := m.Mesh.Client(ctx, rec.GetId())
		if err != nil {
			continue
		}
		cctx, cancel := context.WithTimeout(ctx, callTimeout)
		resp, err := cl.Mesh.GetStored(cctx, connect.NewRequest(&v1.GetStoredRequest{SourceId: sourceID, Repo: repo, Group: group}))
		cancel()
		if err != nil {
			m.Log.Warn("manifest fetch failed", "node", rec.GetName(), "err", err)
			continue
		}
		return resp.Msg.GetModel(), nil
	}
	return nil, fmt.Errorf("%w: %s %s is stored on no member of the mesh, pull it first", store.ErrNotStored, repo, group)
}

// Whether a member's record lists a stored model
func holds(rec *v1.Node, sourceID, repo, group string) bool {
	for _, s := range rec.GetStored() {
		if s.GetRepo() == repo && s.GetGroup() == group && (sourceID == "" || s.GetSourceId() == sourceID) {
			return true
		}
	}
	return false
}

// The runtime planned with, the one named or the first registered accepting the model and installed in the span
func (m *Manager) runtime(run *v1.RunRequest, stored *v1.StoredModel, d *v1.Descriptor, nodes []*v1.Node, span map[string]bool) (runtimes.Runtime, error) {
	if run.GetRuntimeId() != "" {
		return m.Runtimes.Get(run.GetRuntimeId())
	}
	installed := map[string]bool{}
	for _, rec := range nodes {
		if len(span) > 0 && !span[rec.GetId()] {
			continue
		}
		for _, in := range rec.GetInstalls() {
			installed[in.GetRuntimeId()] = true
		}
	}
	for _, rt := range m.Runtimes.List() {
		if installed[rt.ID()] && runtimes.Accepts(rt, stored.GetFormatId(), d.GetKind()) {
			return rt, nil
		}
	}
	var names []string
	for id := range installed {
		names = append(names, id)
	}
	sort.Strings(names)
	return nil, fmt.Errorf("%w: no runtime installed on the span serves %s %s models, installed: %s", ErrFormation, stored.GetFormatId(), strings.ToLower(strings.TrimPrefix(d.GetKind().String(), "MODEL_KIND_")), strings.Join(names, ", "))
}

// The draft model the draft_model param names, a stored companion or the same group on a member, nil when unnamed
func (m *Manager) draft(ctx context.Context, params map[string]string, rt runtimes.Runtime, target *v1.StoredModel, companions []*v1.StoredModel, nodes []*v1.Node) (*v1.StoredModel, error) {
	if !supportsDraft(rt) {
		return nil, nil
	}
	ref := strings.TrimSpace(params["draft_model"])
	if ref == "" || strings.EqualFold(ref, runtimes.None) || strings.EqualFold(ref, runtimes.Auto) {
		return nil, nil
	}
	rest, ok := strings.CutPrefix(ref, runtimes.StoreScheme)
	if !ok {
		return nil, fmt.Errorf("%w: a draft for a formation is named as %ssource/repo#group, so every node finds it, not as the file %q", ErrFormation, runtimes.StoreScheme, ref)
	}
	repoPath, group, hasGroup := strings.Cut(rest, "#")
	source, repo, hasRepo := strings.Cut(repoPath, "/")
	if !hasGroup || !hasRepo || source == "" || repo == "" || group == "" {
		return nil, fmt.Errorf("%w: draft_model %q is not a store reference of the form %ssource/repo#group", ErrFormation, ref, runtimes.StoreScheme)
	}
	for _, c := range append(append([]*v1.StoredModel{}, companions...), target) {
		if c.GetSourceId() == source && c.GetRepo() == repo && c.GetGroup() == group {
			return c, nil
		}
	}
	stored, err := m.manifest(ctx, source, repo, group, nodes)
	if err != nil {
		return nil, fmt.Errorf("draft_model: %w", err)
	}
	return stored, nil
}

// Whether the runtime has a draft shape at all
func supportsDraft(rt runtimes.Runtime) bool {
	return len(rt.Roles(v1.Shape_SHAPE_DRAFT)) > 0
}

// Median prompt and completion tokens and peak concurrency of the route's recent stored traces
func (m *Manager) stats(ctx context.Context, route string) (planner.Stats, error) {
	var out planner.Stats
	traces, err := m.DB.ListTraces(ctx, db.TraceQuery{AllOwners: true, Route: route, Limit: statsTraces})
	if err != nil {
		return out, fmt.Errorf("traces of %s: %w", route, err)
	}
	var prompts, completions []int
	type edge struct {
		at    int64
		delta int
	}
	var edges []edge
	for _, t := range traces {
		if t.GetFinishedAt() == nil || t.GetError() != "" {
			continue
		}
		if t.GetKind() != v1.TraceKind_TRACE_KIND_CHAT && t.GetKind() != v1.TraceKind_TRACE_KIND_GENERATE {
			continue
		}
		out.Traces++
		if t.GetPromptTokens() > 0 {
			prompts = append(prompts, int(t.GetPromptTokens()))
		}
		if t.GetCompletionTokens() > 0 {
			completions = append(completions, int(t.GetCompletionTokens()))
		}
		edges = append(edges, edge{t.GetStartedAt().AsTime().UnixNano(), 1}, edge{t.GetFinishedAt().AsTime().UnixNano(), -1})
	}
	median := func(xs []int) uint32 {
		if len(xs) == 0 {
			return 0
		}
		sort.Ints(xs)
		return uint32(xs[len(xs)/2])
	}
	out.MedianPrompt, out.MedianCompletion = median(prompts), median(completions)
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].at != edges[j].at {
			return edges[i].at < edges[j].at
		}
		return edges[i].delta < edges[j].delta
	})
	var open, peak int
	for _, e := range edges {
		open += e.delta
		if open > peak {
			peak = open
		}
	}
	out.PeakConcurrency = uint32(peak)
	return out, nil
}
