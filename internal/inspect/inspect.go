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
	"github.com/nickheyer/nebu/pkg/cache"
	"github.com/nickheyer/nebu/pkg/descriptor"
	"github.com/nickheyer/nebu/pkg/estimate"
	"github.com/nickheyer/nebu/pkg/eval"
	"github.com/nickheyer/nebu/pkg/formats"
	"github.com/nickheyer/nebu/pkg/host"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/runtime"
	"github.com/nickheyer/nebu/pkg/sources"
	"golang.org/x/sync/errgroup"
	"google.golang.org/protobuf/encoding/prototext"
	"google.golang.org/protobuf/proto"
)

const (
	resolveTTL  = 10 * time.Minute
	describeMax = 8
	// The header key saying what a file holds, and the one value that is weights to load
	typeKey   = "general.type"
	modelType = "model"
)

// Narrows a plan to a slot, its devices, budget, runtime, and default params
type Constrainer interface {
	Constrain(ctx context.Context, slotID string, profile *v1.HostProfile) (*v1.HostProfile, string, map[string]string, error)
}

// Orchestrates sources, readers, descriptors, and planning
type Inspector struct {
	Sources     *sources.Registry
	Classifier  *formats.Classifier
	Readers     formats.Readers
	Builder     *descriptor.Builder
	Runtimes    *runtime.Registry
	Host        *host.Prober
	Cache       *cache.Store
	Calibration *calibrate.Table
	Contexts    []uint32
	// Set by the daemon once slots exist, nil until then
	Constrain Constrainer
	// Reports whether a runtime has an install, set by the daemon, every runtime counted as installed when nil
	Installed func(runtimeID string) bool
	Log       *slog.Logger
}

func (i *Inspector) installed(rt *runtime.Runtime) bool {
	return i.Installed == nil || i.Installed(rt.Manifest.GetId())
}

// The context lengths a group is planned at: the ones given, capped at the model's own and ending on it
func planContexts(contexts []uint32, d *v1.Descriptor, named bool) []uint32 {
	limit := d.GetParams()["n_ctx_train"]
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

// Narrows runtimes to the ones accepting a format that have an install, every accepting one
// when none is installed or the request named the runtimes itself
func (i *Inspector) planners(runtimes []*runtime.Runtime, formatID string, named bool) []*runtime.Runtime {
	var accepting, installed []*runtime.Runtime
	for _, rt := range runtimes {
		if !rt.Accepts(formatID) || rt.Policy == nil {
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

// Returns the planning profile narrowed to the named slot, with the slot's runtime and default params
func (i *Inspector) profile(ctx context.Context, slotID string) (*v1.HostProfile, string, map[string]string, error) {
	profile, err := i.Host.Profile(ctx, false)
	if err != nil {
		return nil, "", nil, err
	}
	if slotID == "" {
		return profile, "", nil, nil
	}
	if i.Constrain == nil {
		return nil, "", nil, fmt.Errorf("slots are not available")
	}
	return i.Constrain.Constrain(ctx, slotID, profile)
}

// Picks the first compatible runtime accepting a format, an installed one before the rest
func (i *Inspector) DefaultRuntime(profile *v1.HostProfile, formatID string) (*runtime.Runtime, error) {
	var first *runtime.Runtime
	for _, rt := range i.Runtimes.List() {
		if ok, _ := rt.Compatible(profile); !ok || !rt.Accepts(formatID) {
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
		return nil, fmt.Errorf("%w: no compatible runtime accepts %s", runtime.ErrParam, formatID)
	}
	return first, nil
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
		i.Classifier.Classify(model)
		return src, model, nil
	}
	model, err = src.Resolve(ctx, repo, revision)
	if err != nil {
		return nil, nil, err
	}
	i.Classifier.Classify(model)
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
	reader, ok := i.Readers[g.FormatID]
	if !ok {
		return nil, fmt.Errorf("no reader for format %q", g.FormatID)
	}
	if missing := formats.Missing(i.Classifier.Spec(g.FormatID), g); len(missing) > 0 {
		return nil, fmt.Errorf("group %s missing %v", g.Name, missing)
	}
	key := rawKey(model, g, i.Classifier.Spec(g.FormatID))
	raw := &v1.RawModel{}
	if data, ok := i.Cache.Get(key, 0); ok && proto.Unmarshal(data, raw) == nil {
		raw.FormatId, raw.Group = g.FormatID, g.Name
		return raw, nil
	}
	open := func(ctx context.Context, a *v1.Artifact) (sources.Blob, error) { return src.Open(ctx, model, a) }
	raw, err := reader.Read(ctx, open, g)
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

// Everything a plan of one descriptor on one runtime reads
func (i *Inspector) input(rt *runtime.Runtime, d *v1.Descriptor, profile *v1.HostProfile, overrides map[string]string, free bool) (estimate.Input, error) {
	if rt.Policy == nil {
		return estimate.Input{}, fmt.Errorf("runtime %s has no estimate policy", rt.Manifest.GetId())
	}
	params, err := rt.Params(overrides)
	if err != nil {
		return estimate.Input{}, err
	}
	return estimate.Input{
		Descriptor:    d,
		Formulas:      i.Builder.Formulas(d.GetArchSpecId()),
		Host:          profile,
		Params:        params,
		Free:          free,
		OverheadDelta: i.Calibration.Delta(rt.Manifest.GetId(), d.GetArchitecture()),
	}, nil
}

func (i *Inspector) Plan(rt *runtime.Runtime, d *v1.Descriptor, profile *v1.HostProfile, overrides map[string]string, free bool) (*v1.MemoryPlan, error) {
	in, err := i.input(rt, d, profile, overrides, free)
	if err != nil {
		return nil, err
	}
	return rt.Policy.Plan(in)
}

// Builds the full fit table for a model
func (i *Inspector) Inspect(ctx context.Context, req *v1.InspectRequest) (*v1.InspectResponse, error) {
	src, model, err := i.Resolve(ctx, req.GetSourceId(), req.GetRepo(), req.GetRevision())
	if err != nil {
		return nil, err
	}
	profile, slotRuntime, slotParams, err := i.profile(ctx, req.GetSlotId())
	if err != nil {
		return nil, err
	}
	groups := selectGroups(i.Classifier.Groups(model), req.GetGroups())
	// The runtimes the request names, else the slot's runtime, else every compatible one
	ids := req.GetRuntimeIds()
	if len(ids) == 0 && slotRuntime != "" {
		ids = []string{slotRuntime}
	}
	named := len(ids) > 0
	runtimes := i.selectRuntimes(ids, profile)
	contexts := req.GetContexts()
	if len(contexts) == 0 {
		contexts = i.Contexts
	}
	resp := &v1.InspectResponse{Model: model}
	// The same failure repeats per context, so every warning is kept once
	seen := map[string]bool{}
	warn := func(format string, args ...any) {
		w := fmt.Sprintf(format, args...)
		if !seen[w] {
			seen[w] = true
			resp.Warnings = append(resp.Warnings, w)
		}
	}
	// The slot's defaults sit under the request's params, the same layering a run uses
	layered := runtime.Merge(slotParams, req.GetParams())
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
	// A repository whose every weight file the source refuses is gated or private, not a warning
	if denied := deniedAll(failures); denied != nil {
		return nil, denied
	}
	for idx, d := range descriptors {
		if d == nil {
			warn("%s", warnings[idx])
			continue
		}
		// A header that says it holds something other than a model, an importance matrix or an adapter,
		// is not weights whatever its name looks like, so its files go back to being ordinary files
		if kind := d.GetMetadata()[typeKey]; kind != "" && kind != modelType {
			for _, a := range groups[idx].Weights {
				a.Role, a.Group = v1.ArtifactRole_ARTIFACT_ROLE_OTHER, ""
			}
			continue
		}
		resp.Descriptors = append(resp.Descriptors, d)
		// What you have installed answers first, what you could install only when nothing installed serves the format
		for _, rt := range i.planners(runtimes, d.GetFormatId(), named) {
			for _, n := range planContexts(contexts, d, len(req.GetContexts()) > 0) {
				overrides := withContext(layered, rt.Policy.ContextParam(), n)
				// A run plans around what is loaded now, so the table says both
				plan, err := i.Plan(rt, d, profile, overrides, false)
				var now *v1.MemoryPlan
				if err == nil {
					now, err = i.Plan(rt, d, profile, overrides, true)
				}
				if err != nil {
					warn("%s", planFailure(d, rt, err))
					i.Log.Debug("plan failed", "group", d.GetGroup(), "runtime", rt.Manifest.GetId(), "err", err)
					continue
				}
				resp.Rows = append(resp.Rows, &v1.FitRow{Group: d.GetGroup(), RuntimeId: rt.Manifest.GetId(), Context: n, Plan: plan, Free: now})
			}
		}
	}
	return resp, nil
}

// Says why a group could not be planned in words a person can act on, the failure itself kept for the log
func planFailure(d *v1.Descriptor, rt *runtime.Runtime, err error) string {
	var missing *eval.MissingError
	if errors.As(err, &missing) {
		return fmt.Sprintf("%s: the header gives no %s, so memory on %s cannot be planned", d.GetGroup(), strings.Join(missing.Names, " or "), rt.Manifest.GetName())
	}
	return fmt.Sprintf("%s: memory on %s cannot be planned", d.GetGroup(), rt.Manifest.GetName())
}

// Plans one group on one runtime
func (i *Inspector) Estimate(ctx context.Context, req *v1.EstimateRequest) (*v1.EstimateResponse, error) {
	src, model, err := i.Resolve(ctx, req.GetSourceId(), req.GetRepo(), req.GetRevision())
	if err != nil {
		return nil, err
	}
	g, err := formats.FindGroup(i.Classifier.Groups(model), req.GetGroup())
	if err != nil {
		return nil, err
	}
	profile, slotRuntime, slotParams, err := i.profile(ctx, req.GetSlotId())
	if err != nil {
		return nil, err
	}
	// The slot's runtime picks the runtime when the request did not, as a run does
	runtimeID := req.GetRuntimeId()
	if runtimeID == "" {
		runtimeID = slotRuntime
	}
	if runtimeID == "" {
		fallback, err := i.DefaultRuntime(profile, g.FormatID)
		if err != nil {
			return nil, err
		}
		runtimeID = fallback.Manifest.GetId()
	}
	rt, err := i.Runtimes.Get(runtimeID)
	if err != nil {
		return nil, err
	}
	overrides := runtime.Merge(slotParams, req.GetParams())
	d, err := i.Describe(ctx, src, model, g)
	if err != nil {
		return nil, err
	}
	// An estimate plans through params a run would refuse and says so, so a form can show what to change
	in, err := i.input(rt, d, profile, overrides, req.GetFree())
	if err != nil {
		return nil, err
	}
	in.SkipRules = true
	plan, err := rt.Policy.Plan(in)
	if err != nil {
		return nil, err
	}
	states, refusal, err := rt.Policy.States(in, plan)
	if err != nil {
		return nil, err
	}
	return &v1.EstimateResponse{Plan: plan, Descriptor_: d, Params: states, Refusal: refusal}, nil
}

func (i *Inspector) selectRuntimes(ids []string, profile *v1.HostProfile) []*runtime.Runtime {
	if len(ids) > 0 {
		var out []*runtime.Runtime
		for _, id := range ids {
			if rt, err := i.Runtimes.Get(id); err == nil {
				out = append(out, rt)
			}
		}
		return out
	}
	var compatible []*runtime.Runtime
	for _, rt := range i.Runtimes.List() {
		if ok, _ := rt.Compatible(profile); ok {
			compatible = append(compatible, rt)
		}
	}
	if len(compatible) == 0 {
		return i.Runtimes.List()
	}
	return compatible
}

// The first failure when there were some and each one was the source refusing access
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

func withContext(params map[string]string, name string, n uint32) map[string]string {
	out := make(map[string]string, len(params)+1)
	for k, v := range params {
		out[k] = v
	}
	if name != "" {
		out[name] = fmt.Sprint(n)
	}
	return out
}

func rawKey(model *v1.Model, g *formats.Group, spec *v1.FormatSpec) string {
	sum := sha256.Sum256([]byte(prototext.Format(spec)))
	parts := []string{"raw", g.FormatID, hex.EncodeToString(sum[:8])}
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
	return strings.Join(parts, "\x00")
}
