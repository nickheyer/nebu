// Package inspect resolves, describes, and plans models before download.
package inspect

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/nickheyer/nebu/internal/calibrate"
	"github.com/nickheyer/nebu/pkg/blueprint"
	"github.com/nickheyer/nebu/pkg/cache"
	"github.com/nickheyer/nebu/pkg/descriptor"
	"github.com/nickheyer/nebu/pkg/estimate"
	"github.com/nickheyer/nebu/pkg/formats"
	"github.com/nickheyer/nebu/pkg/host"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/runtimes"
	"github.com/nickheyer/nebu/pkg/sources"
	"golang.org/x/sync/errgroup"
	"google.golang.org/protobuf/proto"
)

const (
	resolveTTL  = 10 * time.Minute
	describeMax = 8
	// Header key and value identifying model weights.
	typeKey   = "general.type"
	modelType = "model"
)

// Slot constraints: host resources, runtime, default params, and placement.
type Constraint struct {
	Profile   *v1.HostProfile
	RuntimeID string
	Params    map[string]string
	Placement v1.Placement
}

// Narrows a plan to a slot
type Constrainer interface {
	Constrain(ctx context.Context, slotID string, profile *v1.HostProfile) (*Constraint, error)
}

// Resolves models and plans memory use.
type Inspector struct {
	Sources     *sources.Registry
	Formats     *formats.Registry
	Builder     *descriptor.Builder
	Stored      func() ([]*v1.StoredModel, error)
	Runtimes    *runtimes.Registry
	Host        *host.Prober
	Cache       *cache.Store
	Calibration *calibrate.Table
	Contexts    []uint32
	// Set by the daemon once slots exist, nil until then
	Constrain Constrainer
	// Reports installed runtimes. Nil treats all runtimes as installed.
	Installed func(runtimeID string) bool
	Log       *slog.Logger
}

func (i *Inspector) installed(rt runtimes.Runtime) bool {
	return i.Installed == nil || i.Installed(rt.ID())
}

// Requested context lengths capped at the model's limit, with that limit appended.
func planContexts(contexts []uint32, d *v1.Descriptor, named bool) []uint32 {
	limit := formats.ParamsOf(d.GetParams()).ContextTrain
	if named || limit < 1 || limit > math.MaxUint32 {
		return contexts
	}
	max := uint32(limit)
	out := make([]uint32, 0, len(contexts)+1)
	for _, n := range contexts {
		if n < max {
			out = append(out, n)
		}
	}
	return append(out, max)
}

// Prefers installed runtimes that support the format. Explicit requests and
// formats with no installed runtime include all compatible runtimes.
func (i *Inspector) planners(list []runtimes.Runtime, formatID string, kind v1.ModelKind, named bool) []runtimes.Runtime {
	var accepting, installed []runtimes.Runtime
	for _, rt := range list {
		if !runtimes.Accepts(rt, formatID, kind) || rt.Policy() == nil {
			continue
		}
		accepting = append(accepting, rt)
		if i.installed(rt) {
			installed = append(installed, rt)
		}
	}
	if named || len(installed) == 0 {
		return accepting
	}
	return installed
}

// Returns slot constraints, or the full host when no slot is specified.
func (i *Inspector) constraint(ctx context.Context, slotID string) (*Constraint, error) {
	profile, err := i.Host.Profile(ctx, false)
	if err != nil {
		return nil, err
	}
	if slotID == "" {
		return &Constraint{Profile: profile}, nil
	}
	if i.Constrain == nil {
		return nil, fmt.Errorf("slots are not available")
	}
	return i.Constrain.Constrain(ctx, slotID, profile)
}

// Selects a compatible runtime, preferring installed ones.
func (i *Inspector) DefaultRuntime(profile *v1.HostProfile, formatID string, kind v1.ModelKind) (runtimes.Runtime, error) {
	var first runtimes.Runtime
	for _, rt := range i.Runtimes.List() {
		if ok, _ := runtimes.Compatible(rt, profile); !ok || !runtimes.Accepts(rt, formatID, kind) {
			continue
		}
		if i.installed(rt) {
			return rt, nil
		}
		if first == nil {
			first = rt
		}
	}
	if first == nil {
		if kind == v1.ModelKind_MODEL_KIND_COMPONENT {
			return nil, fmt.Errorf("%w: a component is loaded beside a diffusion model, not served on its own", runtimes.ErrParam)
		}
		return nil, fmt.Errorf("%w: no compatible runtime accepts %s", runtimes.ErrParam, formatID)
	}
	return first, nil
}

// Other stored models available as companion parts.
func (i *Inspector) Companions(sourceID, repo, group string) ([]*v1.StoredModel, error) {
	if i.Stored == nil {
		return nil, nil
	}
	list, err := i.Stored()
	if err != nil {
		return nil, err
	}
	var out []*v1.StoredModel
	for _, m := range list {
		if m.GetSourceId() == sourceID && m.GetRepo() == repo && m.GetGroup() == group {
			continue
		}
		out = append(out, m)
	}
	return out, nil
}

// Resolves and classifies a model, caching the listing briefly
func (i *Inspector) Resolve(ctx context.Context, sourceID, repo, revision string) (sources.Source, *v1.Model, error) {
	src, err := i.Sources.Get(sourceID)
	if err != nil {
		return nil, nil, err
	}
	key := resolveKey(src, repo, revision)
	model := &v1.Model{}
	if data, ok := i.Cache.Get(key, resolveTTL); ok && proto.Unmarshal(data, model) == nil {
		i.Formats.Classify(model)
		return src, model, nil
	}
	model, err = src.Resolve(ctx, repo, revision)
	if err != nil {
		return nil, nil, err
	}
	// Read pipeline declarations before classifying files into groups.
	open := func(ctx context.Context, a *v1.Artifact) (sources.Blob, error) { return src.Open(ctx, model, a) }
	if err := i.Formats.Lay(ctx, open, model); err != nil {
		return nil, nil, err
	}
	i.Formats.Classify(model)
	i.remember(key, model)
	return src, model, nil
}

func resolveKey(src sources.Source, repo, revision string) string {
	return strings.Join([]string{"resolve", src.Spec().GetId(), repo, revision}, "\x00")
}

// Caches a message under key, a failed write only logged
func (i *Inspector) remember(key string, msg proto.Message) {
	data, err := proto.Marshal(msg)
	if err != nil {
		return
	}
	if err := i.Cache.Put(key, data); err != nil {
		i.Log.Warn("cache write failed", "err", err)
	}
}

// Reads raw header facts, caching by content identity
func (i *Inspector) Raw(ctx context.Context, src sources.Source, model *v1.Model, g *formats.Group) (*v1.RawModel, error) {
	f := i.Formats.Get(g.FormatID)
	if f == nil {
		return nil, fmt.Errorf("no format reads %q", g.FormatID)
	}
	if missing := formats.Missing(f, g); len(missing) > 0 {
		return nil, fmt.Errorf("group %s missing %v", g.Name, missing)
	}
	key := rawKey(model, g)
	raw := &v1.RawModel{}
	if data, ok := i.Cache.Get(key, 0); ok && proto.Unmarshal(data, raw) == nil {
		raw.FormatId, raw.Group = g.FormatID, g.Name
		return raw, nil
	}
	open := func(ctx context.Context, a *v1.Artifact) (sources.Blob, error) { return src.Open(ctx, model, a) }
	raw, err := f.Read(ctx, open, g)
	if err != nil {
		return nil, err
	}
	i.remember(key, raw)
	return raw, nil
}

// Builds the descriptor for one group
func (i *Inspector) Describe(ctx context.Context, src sources.Source, model *v1.Model, g *formats.Group) (*v1.Descriptor, error) {
	raw, err := i.Raw(ctx, src, model, g)
	if err != nil {
		return nil, err
	}
	return i.Builder.Build(raw)
}

// Builds planning input from overrides, available or total memory, and calibration.
func (i *Inspector) input(rt runtimes.Runtime, d *v1.Descriptor, profile *v1.HostProfile, overrides map[string]string, free bool, placement v1.Placement, repo string, companions []*v1.StoredModel) (estimate.Input, error) {
	if rt.Policy() == nil {
		return estimate.Input{}, fmt.Errorf("runtime %s has no estimate policy", rt.ID())
	}
	params, err := runtimes.Resolve(rt, overrides)
	if err != nil {
		return estimate.Input{}, err
	}
	return estimate.Input{
		Descriptor:    d,
		Family:        i.Builder.Family(d),
		Host:          profile,
		Params:        params,
		Free:          free,
		OverheadDelta: i.Calibration.Delta(rt.ID(), d.GetArchitecture()),
		Placement:     placement,
		Companions:    companions,
		Repo:          repo,
	}, nil
}

// Estimates memory fit without rejecting unsupported runtime params.
func (i *Inspector) fit(rt runtimes.Runtime, d *v1.Descriptor, profile *v1.HostProfile, overrides map[string]string, free bool, placement v1.Placement, repo string, companions []*v1.StoredModel) (*v1.MemoryPlan, error) {
	in, err := i.input(rt, d, profile, overrides, free, placement, repo, companions)
	if err != nil {
		return nil, err
	}
	in.SkipRules = true
	return rt.Policy().Plan(in)
}

// Plans memory use and validates runtime params.
func (i *Inspector) Plan(rt runtimes.Runtime, d *v1.Descriptor, profile *v1.HostProfile, overrides map[string]string, free bool, placement v1.Placement, repo string, companions []*v1.StoredModel) (*v1.MemoryPlan, error) {
	in, err := i.input(rt, d, profile, overrides, free, placement, repo, companions)
	if err != nil {
		return nil, err
	}
	return rt.Policy().Plan(in)
}

// Builds the full fit table for a model
func (i *Inspector) Inspect(ctx context.Context, req *v1.InspectRequest) (*v1.InspectResponse, error) {
	src, model, err := i.Resolve(ctx, req.GetSourceId(), req.GetRepo(), req.GetRevision())
	if err != nil {
		return nil, err
	}
	c, err := i.constraint(ctx, req.GetSlotId())
	if err != nil {
		return nil, err
	}
	profile := c.Profile
	groups := selectGroups(i.Formats.Groups(model), req.GetGroups())
	// Use requested runtimes, the slot's runtime, or all compatible runtimes.
	ids := req.GetRuntimeIds()
	if len(ids) == 0 && c.RuntimeID != "" {
		ids = []string{c.RuntimeID}
	}
	named := len(ids) > 0
	list := i.selectRuntimes(ids, profile)
	contexts := req.GetContexts()
	if len(contexts) == 0 {
		contexts = i.Contexts
	}
	resp := &v1.InspectResponse{Model: model}
	// Stored parts available for diffusion file params.
	companions, err := i.Companions(req.GetSourceId(), req.GetRepo(), "")
	if err != nil {
		return nil, err
	}
	// Deduplicate warnings across contexts.
	seen := map[string]bool{}
	warn := func(format string, args ...any) {
		w := fmt.Sprintf(format, args...)
		if !seen[w] {
			seen[w] = true
			resp.Warnings = append(resp.Warnings, w)
		}
	}
	// Request params override slot defaults.
	layered := runtimes.Merge(c.Params, req.GetParams())
	descriptors := make([]*v1.Descriptor, len(groups))
	warnings := make([]string, len(groups))
	failures := make([]error, len(groups))
	eg, gctx := errgroup.WithContext(ctx)
	eg.SetLimit(describeMax)
	for idx, g := range groups {
		eg.Go(func() error {
			d, err := i.Describe(gctx, src, model, g)
			if err != nil {
				warnings[idx] = fmt.Sprintf("%s: the header could not be read, %v", g.Name, err)
				failures[idx] = err
				return nil
			}
			descriptors[idx] = d
			return nil
		})
	}
	if err := eg.Wait(); err != nil {
		return nil, err
	}
	// If every weight file is inaccessible, return the access error.
	if denied := deniedAll(failures); denied != nil {
		return nil, denied
	}
	for idx, d := range descriptors {
		if d == nil {
			warn("%s", warnings[idx])
			continue
		}
		// Headers identifying adapters or importance matrices override weight filenames.
		if kind := d.GetMetadata()[typeKey]; kind != "" && kind != modelType {
			for _, a := range groups[idx].Weights {
				a.Role, a.Group = v1.ArtifactRole_ARTIFACT_ROLE_OTHER, ""
			}
			continue
		}
		resp.Descriptors = append(resp.Descriptors, d)
		// Prefer installed runtimes that support the format.
		for _, rt := range i.planners(list, d.GetFormatId(), d.GetKind(), named) {
			// Show the largest fitting context before the requested context grid.
			rows := []uint32{}
			if rt.Policy().ContextParam != "" && len(req.GetContexts()) == 0 {
				rows = append(rows, 0)
			}
			for _, n := range append(rows, planContexts(contexts, d, len(req.GetContexts()) > 0)...) {
				overrides := withContext(layered, rt.Policy().ContextParam, n)
				// Report fit against both available and total memory.
				plan, err := i.fit(rt, d, profile, overrides, false, c.Placement, req.GetRepo(), companions)
				var now *v1.MemoryPlan
				if err == nil {
					now, err = i.fit(rt, d, profile, overrides, true, c.Placement, req.GetRepo(), companions)
				}
				if err != nil {
					warn("%s", planFailure(d, rt, err))
					i.Log.Debug("plan failed", "group", d.GetGroup(), "runtime", rt.ID(), "err", err)
					continue
				}
				resp.Rows = append(resp.Rows, &v1.FitRow{Group: d.GetGroup(), RuntimeId: rt.ID(), Context: n, Plan: plan, Free: now})
			}
		}
	}
	return resp, nil
}

// Returns a planning error message and logs the underlying error.
func planFailure(d *v1.Descriptor, rt runtimes.Runtime, err error) string {
	var missing *estimate.MissingError
	if errors.As(err, &missing) {
		return fmt.Sprintf("%s: %v, so memory on %s cannot be planned", d.GetGroup(), missing, rt.Name())
	}
	return fmt.Sprintf("%s: memory on %s cannot be planned", d.GetGroup(), rt.Name())
}

// Plans one group on one runtime
func (i *Inspector) Estimate(ctx context.Context, req *v1.EstimateRequest) (*v1.EstimateResponse, error) {
	src, model, err := i.Resolve(ctx, req.GetSourceId(), req.GetRepo(), req.GetRevision())
	if err != nil {
		return nil, err
	}
	g, err := formats.FindGroup(i.Formats.Groups(model), req.GetGroup())
	if err != nil {
		return nil, err
	}
	c, err := i.constraint(ctx, req.GetSlotId())
	if err != nil {
		return nil, err
	}
	profile := c.Profile
	// Use the slot's runtime unless the request specifies one.
	runtimeID := req.GetRuntimeId()
	if runtimeID == "" {
		runtimeID = c.RuntimeID
	}
	d, err := i.Describe(ctx, src, model, g)
	if err != nil {
		return nil, err
	}
	if runtimeID == "" {
		fallback, err := i.DefaultRuntime(profile, g.FormatID, d.GetKind())
		if err != nil {
			return nil, err
		}
		runtimeID = fallback.ID()
	}
	rt, err := i.Runtimes.Get(runtimeID)
	if err != nil {
		return nil, err
	}
	overrides := runtimes.Merge(c.Params, req.GetParams())
	companions, err := i.Companions(req.GetSourceId(), req.GetRepo(), req.GetGroup())
	if err != nil {
		return nil, err
	}
	// Return estimates with param errors for the form to display.
	in, err := i.input(rt, d, profile, overrides, req.GetFree(), c.Placement, req.GetRepo(), companions)
	if err != nil {
		return nil, err
	}
	in.SkipRules = true
	plan, err := rt.Policy().Plan(in)
	if err != nil {
		return nil, err
	}
	states, refusal := rt.Policy().ParamStates(in, plan)
	missing, err := i.missing(ctx, src, model, g, d, rt.Policy().MissingParts(in, plan))
	if err != nil {
		return nil, err
	}
	return &v1.EstimateResponse{Plan: plan, Descriptor_: d, Params: states, Refusal: refusal, Missing: missing}, nil
}

// Resolves missing slots and associates each part with its runtime param.
func (i *Inspector) missing(ctx context.Context, src sources.Source, model *v1.Model, g *formats.Group, d *v1.Descriptor, parts []estimate.Missing) ([]*v1.Part, error) {
	if len(parts) == 0 {
		return nil, nil
	}
	plan, err := i.Parts(ctx, src, model, g, d)
	if err != nil {
		return nil, err
	}
	var out []*v1.Part
	for _, part := range parts {
		found := false
		for _, pick := range plan.Picks {
			if pick.Fill.Slot != part.Slot {
				continue
			}
			p := pick.Proto()
			p.Param = part.Param
			out = append(out, p)
			found = true
		}
		if !found {
			out = append(out, &v1.Part{Slot: part.Slot, Label: blueprint.Label(part.Slot), Param: part.Param, Required: true, Error: "slot not defined for this model"})
		}
	}
	return out, nil
}

func (i *Inspector) selectRuntimes(ids []string, profile *v1.HostProfile) []runtimes.Runtime {
	if len(ids) > 0 {
		var out []runtimes.Runtime
		for _, id := range ids {
			if rt, err := i.Runtimes.Get(id); err == nil {
				out = append(out, rt)
			}
		}
		return out
	}
	var compatible []runtimes.Runtime
	for _, rt := range i.Runtimes.List() {
		if ok, _ := runtimes.Compatible(rt, profile); ok {
			compatible = append(compatible, rt)
		}
	}
	if len(compatible) == 0 {
		return i.Runtimes.List()
	}
	return compatible
}

// Returns the first error if all failures denied access.
func deniedAll(failures []error) error {
	var first error
	for _, err := range failures {
		if err == nil || !(sources.IsStatus(err, http.StatusUnauthorized) || sources.IsStatus(err, http.StatusForbidden)) {
			return nil
		}
		if first == nil {
			first = err
		}
	}
	return first
}

func selectGroups(groups []*formats.Group, names []string) []*formats.Group {
	if len(names) == 0 {
		return groups
	}
	var out []*formats.Group
	for _, g := range groups {
		if slices.Contains(names, g.Name) {
			out = append(out, g)
		}
	}
	return out
}

// Sets the context length, using auto when the planner should solve it.
func withContext(params map[string]string, name string, n uint32) map[string]string {
	out := make(map[string]string, len(params)+1)
	for k, v := range params {
		out[k] = v
	}
	if name == "" {
		return out
	}
	if n == 0 {
		out[name] = estimate.Auto
	} else {
		out[name] = fmt.Sprint(n)
	}
	return out
}

// Keys cached headers by weight, config, and projector digests. Uses source paths
// when digests are unavailable.
func rawKey(model *v1.Model, g *formats.Group) string {
	parts := []string{"raw", g.FormatID}
	add := func(a *v1.Artifact) {
		if a.GetSha256() != "" {
			parts = append(parts, a.GetSha256())
			return
		}
		parts = append(parts, model.GetSourceId(), model.GetRepo(), model.GetCommit(), a.GetPath(), fmt.Sprint(a.GetSizeBytes()))
	}
	for _, a := range g.Weights {
		add(a)
	}
	for _, a := range g.Files[v1.ArtifactRole_ARTIFACT_ROLE_CONFIG] {
		add(a)
	}
	if files := g.Files[v1.ArtifactRole_ARTIFACT_ROLE_PROJECTOR]; len(files) > 0 {
		add(files[0])
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return "raw\x00" + g.FormatID + "\x00" + hex.EncodeToString(sum[:])
}
