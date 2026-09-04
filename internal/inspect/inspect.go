// Package inspect resolves, describes, and plans models before download.
package inspect

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/nickheyer/nebu/pkg/cache"
	"github.com/nickheyer/nebu/pkg/descriptor"
	"github.com/nickheyer/nebu/pkg/estimate"
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
)

// Narrows a profile to a slot and returns the slot's default params, set by the daemon
type Constrainer func(ctx context.Context, slotID string, profile *v1.HostProfile) (*v1.HostProfile, map[string]string, error)

// Returns the profile a run of a runtime starts from, named by id or name or
// the runtime's default, nil when it has none, set by the daemon
type Profiler func(runtimeID, ref string) (*v1.Profile, error)

// Returns the learned overhead correction for a runtime and architecture, set by the daemon
type Calibrator func(runtimeID, architecture string) float64

// Orchestrates sources, readers, descriptors, and planning
type Inspector struct {
	Sources    *sources.Registry
	Classifier *formats.Classifier
	Readers    formats.Readers
	Builder    *descriptor.Builder
	Runtimes   *runtime.Registry
	Host       *host.Prober
	Cache      *cache.Store
	Contexts   []uint32
	Constrain  Constrainer
	Profiles   Profiler
	Delta      Calibrator
	Log        *slog.Logger
}

// Returns the planning profile narrowed to the named slot, with the slot's default params
func (i *Inspector) profile(ctx context.Context, slotID string) (*v1.HostProfile, map[string]string, error) {
	profile, err := i.Host.Profile(ctx, false)
	if err != nil {
		return nil, nil, err
	}
	if slotID == "" {
		return profile, nil, nil
	}
	if i.Constrain == nil {
		return nil, nil, fmt.Errorf("slots are not available")
	}
	return i.Constrain(ctx, slotID, profile)
}

// Resolves the profile a run of a runtime starts from, nil when it has none
func (i *Inspector) Profile(runtimeID, ref string) (*v1.Profile, error) {
	if i.Profiles == nil {
		if ref != "" {
			return nil, fmt.Errorf("profiles are not available")
		}
		return nil, nil
	}
	return i.Profiles(runtimeID, ref)
}

// Layers a run's params the one way every plan does: the profile, then the
// slot's defaults, then the request, returning the profile used
func (i *Inspector) Layer(runtimeID, ref string, slot, params map[string]string) (*v1.Profile, map[string]string, error) {
	p, err := i.Profile(runtimeID, ref)
	if err != nil {
		return nil, nil, err
	}
	return p, runtime.Merge(p.GetParams(), slot, params), nil
}

// Resolves and classifies a model, caching the listing briefly
func (i *Inspector) Resolve(ctx context.Context, sourceID, repo, revision string) (sources.Source, *v1.Model, error) {
	src, err := i.Sources.Get(sourceID)
	if err != nil {
		return nil, nil, err
	}
	key := strings.Join([]string{"resolve", src.Spec().GetId(), repo, revision}, "\x00")
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
	if data, err := proto.Marshal(model); err == nil {
		if err := i.Cache.Put(key, data); err != nil {
			i.Log.Warn("cache write failed", "err", err)
		}
	}
	return src, model, nil
}

// Resolves a model again, ignoring the cached listing
func (i *Inspector) ResolveFresh(ctx context.Context, sourceID, repo, revision string) (sources.Source, *v1.Model, error) {
	src, err := i.Sources.Get(sourceID)
	if err != nil {
		return nil, nil, err
	}
	key := strings.Join([]string{"resolve", src.Spec().GetId(), repo, revision}, "\x00")
	if err := i.Cache.Delete(key); err != nil {
		i.Log.Warn("cache delete failed", "err", err)
	}
	return i.Resolve(ctx, sourceID, repo, revision)
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
	if data, err := proto.Marshal(raw); err == nil {
		if err := i.Cache.Put(key, data); err != nil {
			i.Log.Warn("cache write failed", "err", err)
		}
	}
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

// Plans one descriptor on one runtime with overrides, against the memory free
// right now or all of it, with the same learned correction a run applies
func (i *Inspector) Plan(rt *runtime.Runtime, d *v1.Descriptor, profile *v1.HostProfile, overrides map[string]string, free bool) (*v1.MemoryPlan, error) {
	if rt.Policy == nil {
		return nil, fmt.Errorf("runtime %s has no estimate policy", rt.Manifest.GetId())
	}
	params, err := rt.Params(overrides)
	if err != nil {
		return nil, err
	}
	var delta float64
	if i.Delta != nil {
		delta = i.Delta(rt.Manifest.GetId(), d.GetArchitecture())
	}
	return rt.Policy.Plan(estimate.Input{
		Descriptor:    d,
		Formulas:      i.Builder.Formulas(d.GetArchSpecId()),
		Host:          profile,
		Params:        params,
		Free:          free,
		OverheadDelta: delta,
	})
}

// Builds the full fit table for a model
func (i *Inspector) Inspect(ctx context.Context, req *v1.InspectRequest) (*v1.InspectResponse, error) {
	src, model, err := i.Resolve(ctx, req.GetSourceId(), req.GetRepo(), req.GetRevision())
	if err != nil {
		return nil, err
	}
	profile, slotParams, err := i.profile(ctx, req.GetSlotId())
	if err != nil {
		return nil, err
	}
	groups := selectGroups(i.Classifier.Groups(model), req.GetGroups())
	// A named profile plans its own runtime unless the request names runtimes itself
	ids := req.GetRuntimeIds()
	named, err := i.Profile("", req.GetProfileId())
	if err != nil {
		return nil, err
	}
	if named != nil && len(ids) == 0 {
		ids = []string{named.GetRuntimeId()}
	}
	runtimes := i.selectRuntimes(ids, profile)
	contexts := req.GetContexts()
	if len(contexts) == 0 {
		contexts = i.Contexts
	}
	resp := &v1.InspectResponse{Model: model}
	// The named profile sits under its runtime's rows, every other runtime's default under the rest
	layered := map[string]map[string]string{}
	for _, rt := range runtimes {
		ref := ""
		if named != nil && named.GetRuntimeId() == rt.Manifest.GetId() {
			ref = named.GetId()
		}
		_, params, err := i.Layer(rt.Manifest.GetId(), ref, slotParams, req.GetParams())
		if err != nil {
			resp.Warnings = append(resp.Warnings, fmt.Sprintf("%s: %v", rt.Manifest.GetId(), err))
			continue
		}
		layered[rt.Manifest.GetId()] = params
	}
	descriptors := make([]*v1.Descriptor, len(groups))
	warnings := make([]string, len(groups))
	eg, gctx := errgroup.WithContext(ctx)
	eg.SetLimit(describeMax)
	for idx, g := range groups {
		eg.Go(func() error {
			d, err := i.Describe(gctx, src, model, g)
			if err != nil {
				warnings[idx] = fmt.Sprintf("%s: %v", g.Name, err)
				return nil
			}
			descriptors[idx] = d
			return nil
		})
	}
	if err := eg.Wait(); err != nil {
		return nil, err
	}
	for idx, d := range descriptors {
		if d == nil {
			resp.Warnings = append(resp.Warnings, warnings[idx])
			continue
		}
		resp.Descriptors = append(resp.Descriptors, d)
		for _, rt := range runtimes {
			params, ok := layered[rt.Manifest.GetId()]
			if !ok || !rt.Accepts(d.GetFormatId()) || rt.Policy == nil {
				continue
			}
			for _, n := range contexts {
				overrides := withContext(params, rt.Policy.ContextParam(), n)
				plan, err := i.Plan(rt, d, profile, overrides, false)
				if err != nil {
					resp.Warnings = append(resp.Warnings, fmt.Sprintf("%s on %s: %v", d.GetGroup(), rt.Manifest.GetId(), err))
					continue
				}
				// A run plans around what is loaded now, so the table says both
				now, err := i.Plan(rt, d, profile, overrides, true)
				if err != nil {
					resp.Warnings = append(resp.Warnings, fmt.Sprintf("%s on %s: %v", d.GetGroup(), rt.Manifest.GetId(), err))
					continue
				}
				resp.Rows = append(resp.Rows, &v1.FitRow{Group: d.GetGroup(), RuntimeId: rt.Manifest.GetId(), Context: n, Plan: plan, Free: now})
			}
		}
	}
	return resp, nil
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
	runtimeID := req.GetRuntimeId()
	// A named profile picks its runtime when nothing else did, as a run does
	if runtimeID == "" && req.GetProfileId() != "" {
		named, err := i.Profile("", req.GetProfileId())
		if err != nil {
			return nil, err
		}
		runtimeID = named.GetRuntimeId()
	}
	rt, err := i.Runtimes.Get(runtimeID)
	if err != nil {
		return nil, err
	}
	profile, slotParams, err := i.profile(ctx, req.GetSlotId())
	if err != nil {
		return nil, err
	}
	_, overrides, err := i.Layer(rt.Manifest.GetId(), req.GetProfileId(), slotParams, req.GetParams())
	if err != nil {
		return nil, err
	}
	d, err := i.Describe(ctx, src, model, g)
	if err != nil {
		return nil, err
	}
	plan, err := i.Plan(rt, d, profile, overrides, req.GetFree())
	if err != nil {
		return nil, err
	}
	return &v1.EstimateResponse{Plan: plan, Descriptor_: d}, nil
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

func selectGroups(groups []*formats.Group, names []string) []*formats.Group {
	if len(names) == 0 {
		return groups
	}
	var out []*formats.Group
	for _, g := range groups {
		for _, n := range names {
			if g.Name == n {
				out = append(out, g)
				break
			}
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
	return strings.Join(parts, "\x00")
}
