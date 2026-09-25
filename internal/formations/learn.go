package formations

import (
	"context"
	"fmt"
	"strconv"

	"github.com/nickheyer/nebu/pkg/formats"
	"github.com/nickheyer/nebu/pkg/perf"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/text"
)

const (
	// Completion tokens a trace needs before it teaches decode bandwidth
	learnCompletion = 16
	// Prompt tokens a trace needs before it teaches prefill compute
	learnPrompt = 256
	// The relay trace mark the gateway writes when the decode seat answered alone, beside relayBoth
	relayDecode = "decode-only"
)

// Learns node numbers from the gateway's traces: streaming bandwidth and prefill compute from solo
// instances, the ratio of predicted to measured timings from formations, and draft acceptance from both
func (m *Manager) learn(ctx context.Context) {
	sub := m.Events.Subscribe(ctx, []v1.EventKind{v1.EventKind_EVENT_KIND_TRACE})
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-sub.Events():
			t := ev.GetTrace()
			if t == nil || t.GetFinishedAt() == nil || t.GetError() != "" || t.GetForwarded() {
				continue
			}
			if t.GetKind() != v1.TraceKind_TRACE_KIND_CHAT && t.GetKind() != v1.TraceKind_TRACE_KIND_GENERATE {
				continue
			}
			m.learnTrace(ctx, t)
		}
	}
}

func (m *Manager) learnTrace(ctx context.Context, t *v1.Trace) {
	route, ok := m.Routes.Lookup(t.GetRoute())
	if !ok || m.Perf == nil {
		return
	}
	ttft, tpt := timings(t)
	if t.GetCompletionTokens() < learnCompletion {
		tpt = 0
	}
	if route.GetFormationId() != "" {
		f, err := m.Get(route.GetFormationId())
		if err != nil || f.GetConductor() != m.self() {
			return
		}
		m.learnFormation(ctx, f, t, ttft, tpt)
		return
	}
	if route.GetInstanceId() == "" {
		return
	}
	in, err := m.Instances.Get(route.GetInstanceId())
	if err != nil || in.GetSeat() != nil || in.GetPlan() == nil {
		return
	}
	device := m.primaryDevice(in.GetPlan())
	if device == "" {
		return
	}
	if t.GetDraftOffered() > 0 {
		if err := m.Perf.RecordAcceptance(ctx, device, t.GetDraftOffered(), t.GetDraftAccepted()); err != nil {
			m.Log.Warn("acceptance write failed", "err", err)
		}
	}
	stored, err := m.Store.ReadManifest(in.GetSourceId(), in.GetRepo(), in.GetGroup())
	if err != nil {
		return
	}
	rt, err := m.Runtimes.Get(in.GetRuntimeId())
	if err != nil {
		return
	}
	m.learnSolo(ctx, device, in.GetPlan(), rt.Policy().ContextParam, stored.GetDescriptor_(), t, ttft, tpt)
}

// Learns from a formation's trace: the ratio of predicted to measured timings for its shape, and
// the acceptance of its draft. A relay request the decode seat answered alone is a solo request
// of that seat and teaches its node's numbers instead.
func (m *Manager) learnFormation(ctx context.Context, f *v1.Formation, t *v1.Trace, ttft, tpt float64) {
	plan := f.GetPlan()
	if t.GetDraftOffered() > 0 {
		if head := m.headSeat(f); head != nil && len(head.GetDeviceIds()) > 0 {
			if err := m.Perf.RecordAcceptance(ctx, head.GetDeviceIds()[0], t.GetDraftOffered(), t.GetDraftAccepted()); err != nil {
				m.Log.Warn("acceptance write failed", "err", err)
			}
		}
	}
	switch relayPath(f.GetShape(), t) {
	case pathFormation:
		predictedTTFT, predictedTPT := predicted(plan, t.GetPromptTokens())
		if err := m.Perf.RecordRatio(ctx, f.GetShape(), f.GetRuntimeId(), plan.GetLinkClass(), predictedTTFT, ttft, predictedTPT, tpt); err != nil {
			m.Log.Warn("ratio write failed", "err", err)
		}
	case pathDecodeAlone:
		head := m.headSeat(f)
		if head == nil || head.GetNodeId() != m.self() || len(head.GetDeviceIds()) == 0 {
			return
		}
		stored, err := m.Store.ReadManifest(f.GetSourceId(), f.GetRepo(), f.GetGroup())
		if err != nil {
			return
		}
		rt, err := m.Runtimes.Get(f.GetRuntimeId())
		if err != nil {
			return
		}
		m.learnSolo(ctx, head.GetDeviceIds()[0], head.GetMemory(), rt.Policy().ContextParam, stored.GetDescriptor_(), t, ttft, tpt)
	}
}

// What a formation's trace teaches
type learnPath int

const (
	// Nothing: the request went a way the model does not price
	pathNone learnPath = iota
	// The formation's ratio: the request went through every seat the plan prices
	pathFormation
	// The decode seat's own numbers: a relay request the decode seat answered alone
	pathDecodeAlone
)

// Which numbers a formation's trace feeds: relay traces say which seats they went through, and
// only the ones through both seats measure the relay the plan priced
func relayPath(shape v1.Shape, t *v1.Trace) learnPath {
	if shape != v1.Shape_SHAPE_RELAY {
		return pathFormation
	}
	switch t.GetRelay() {
	case relayBoth:
		return pathFormation
	case relayDecode:
		return pathDecodeAlone
	}
	return pathNone
}

// The plan's own prediction for a request: its prefill re-priced by the cost model at the
// request's prompt length, and its decode per token, both before the learned ratio
func predicted(plan *v1.FormationPlan, prompt uint32) (ttft, tpt float64) {
	for i, p := range plan.GetPrefillCost() {
		if v := p.GetFixedSeconds() + p.GetSecondsPerPromptToken()*float64(prompt); i == 0 || v > ttft {
			ttft = v
		}
	}
	return ttft, plan.GetModelDecodeSecondsPerToken()
}

// Learns a node's numbers from a request one seat or instance answered alone
func (m *Manager) learnSolo(ctx context.Context, device string, plan *v1.MemoryPlan, ctxParam string, d *v1.Descriptor, t *v1.Trace, ttft, tpt float64) {
	sample, err := soloSample(device, plan, ctxParam, d, t, ttft, tpt)
	if err != nil {
		m.Log.Warn("throughput sample skipped", "route", t.GetRoute(), "err", err)
		return
	}
	if err := m.Perf.Record(ctx, sample); err != nil {
		m.Log.Warn("throughput write failed", "err", err)
	}
}

// The sample a request teaches: bytes a token read, the device weights at the share a token
// touches plus the cache at the request's context, over the time per completion token; and the
// parameters a token touched, experts at their share, over the time to the first token
func soloSample(device string, plan *v1.MemoryPlan, ctxParam string, d *v1.Descriptor, t *v1.Trace, ttft, tpt float64) (perf.Sample, error) {
	share := expertShare(d)
	// A placement names the kind of pool it landed in, device class or host.
	deviceKind, unifiedKind := text.Enum(v1.PoolKind_POOL_KIND_DEVICE), text.Enum(v1.PoolKind_POOL_KIND_UNIFIED)
	var deviceWeights float64
	for _, p := range plan.GetPlacements() {
		if p.GetPoolId() != deviceKind && p.GetPoolId() != unifiedKind {
			continue
		}
		b := float64(p.GetBytes())
		if p.GetKind() == v1.TensorGroupKind_TENSOR_GROUP_KIND_EXPERTS {
			b *= share
		}
		deviceWeights += b
	}
	var perToken float64
	if plan.GetCacheBytes() > 0 {
		if ctxParam == "" {
			return perf.Sample{}, fmt.Errorf("the plan holds %d cache bytes but the runtime names no context param, so the cache per token is unknown", plan.GetCacheBytes())
		}
		ctx, err := strconv.ParseFloat(plan.GetParams()[ctxParam], 64)
		if err != nil || ctx <= 0 {
			return perf.Sample{}, fmt.Errorf("the plan's %s %q is not a context length", ctxParam, plan.GetParams()[ctxParam])
		}
		perToken = float64(plan.GetCacheBytes()) / ctx
	}
	context := float64(t.GetPromptTokens() + t.GetCompletionTokens())
	return perf.Sample{
		DeviceID:      device,
		BytesPerToken: deviceWeights + perToken*context,
		TimePerToken:  tpt,
		Parameters:    activeParams(d, share),
		PromptTokens:  float64(t.GetPromptTokens()),
		TimeToFirst:   ttft,
		Decode:        t.GetCompletionTokens() >= learnCompletion && tpt > 0,
		Prefill:       t.GetPromptTokens() >= learnPrompt && ttft > 0,
	}, nil
}

// Share of expert parameters a token reads, one for a dense model
func expertShare(d *v1.Descriptor) float64 {
	p := formats.ParamsOf(d.GetParams())
	if p.Experts > 0 && p.ExpertsUsed > 0 {
		return p.ExpertsUsed / p.Experts
	}
	return 1
}

// Parameters a token touches: every group whole but the experts at their share, what the planner
// prices prefill with; the header's count when the descriptor lists no groups
func activeParams(d *v1.Descriptor, share float64) float64 {
	if len(d.GetGroups()) == 0 {
		return float64(d.GetParameterCount())
	}
	var out float64
	for _, g := range d.GetGroups() {
		n := float64(g.GetElements())
		if g.GetKind() == v1.TensorGroupKind_TENSOR_GROUP_KIND_EXPERTS {
			n *= share
		}
		out += n
	}
	return out
}

// Seconds to the first token and seconds per later token, from the trace's stamps
func timings(t *v1.Trace) (ttft, tpt float64) {
	if t.GetStartedAt() != nil && t.GetFirstTokenAt() != nil {
		ttft = t.GetFirstTokenAt().AsTime().Sub(t.GetStartedAt().AsTime()).Seconds()
	}
	if t.GetFirstTokenAt() != nil && t.GetFinishedAt() != nil && t.GetCompletionTokens() > 1 {
		tpt = t.GetFinishedAt().AsTime().Sub(t.GetFirstTokenAt().AsTime()).Seconds() / float64(t.GetCompletionTokens()-1)
	}
	return ttft, tpt
}

// The device an instance's plan puts the most bytes on, by the pool's device id
func (m *Manager) primaryDevice(plan *v1.MemoryPlan) string {
	var best *v1.PoolUsage
	for _, p := range plan.GetPools() {
		if p.GetKind() != v1.PoolKind_POOL_KIND_DEVICE && p.GetKind() != v1.PoolKind_POOL_KIND_UNIFIED {
			continue
		}
		if best == nil || p.GetUsedBytes() > best.GetUsedBytes() {
			best = p
		}
	}
	if best == nil {
		return ""
	}
	profile, err := m.Instances.Host.Profile(context.Background(), false)
	if err != nil {
		return best.GetPoolId()
	}
	for _, pool := range profile.GetPools() {
		if pool.GetId() == best.GetPoolId() {
			if pool.GetDeviceId() != "" {
				return pool.GetDeviceId()
			}
			// A unified pool belongs to the accelerator sharing it.
			for _, d := range profile.GetDevices() {
				if d.GetKind() != v1.DeviceKind_DEVICE_KIND_CPU {
					return d.GetId()
				}
			}
		}
	}
	return best.GetPoolId()
}
