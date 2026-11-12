// Package filter holds the reference filters that drop endpoints unfit for a request
package filter

import (
	"context"
	"time"

	"github.com/nickheyer/nebu/pkg/datalayer"
	"github.com/nickheyer/nebu/pkg/latency"
	"github.com/nickheyer/nebu/pkg/prefixcache"
	"github.com/nickheyer/nebu/pkg/scheduling/framework"
	"github.com/nickheyer/nebu/pkg/scheduling/plugins"
	"github.com/nickheyer/nebu/pkg/scheduling/registry"
)

// Plugin type names
const (
	ReadyFilterType          = "ready-filter"
	FreshMetricsFilterType   = "fresh-metrics-filter"
	SubsetFilterType         = "subset-filter"
	RoleFilterType           = "role-filter"
	KVThresholdFilterType    = "kv-threshold-filter"
	QueueThresholdFilterType = "queue-threshold-filter"
	LoRAAffinityFilterType   = "lora-affinity-filter"
	LeastQueueFilterType     = "least-queue-filter"
	CapacityFilterType       = "capacity-filter"
	ObjectiveFilterType      = "objective-filter"
)

func keep(eps []datalayer.Endpoint, ok func(datalayer.Endpoint) bool) []datalayer.Endpoint {
	out := make([]datalayer.Endpoint, 0, len(eps))
	for _, ep := range eps {
		if ok(ep) {
			out = append(out, ep)
		}
	}
	return out
}

// Keeps endpoints the check finds room on, refusing as saturated when it finds none
func room(state *framework.CycleState, eps []datalayer.Endpoint, ok func(datalayer.Endpoint) bool) []datalayer.Endpoint {
	out := keep(eps, ok)
	if len(out) == 0 && len(eps) > 0 {
		framework.Refuse(state, framework.ErrSaturated)
	}
	return out
}

// Keeps endpoints that are ready and that the health collector last found healthy
type ReadyFilter struct{ plugins.Named }

func NewReadyFilter() *ReadyFilter { return &ReadyFilter{plugins.NewNamed(ReadyFilterType)} }

func (f *ReadyFilter) Filter(_ context.Context, _ *framework.CycleState, _ *framework.Request, eps []datalayer.Endpoint) []datalayer.Endpoint {
	return keep(eps, datalayer.ReadyAndHealthy)
}

// Keeps endpoints with metrics no older than the threshold, refusing as saturated while none has such a sample
type FreshMetricsFilter struct {
	plugins.Named
	threshold time.Duration
	clock     registry.Clock
}

func NewFreshMetricsFilter(threshold time.Duration, clock registry.Clock) *FreshMetricsFilter {
	return &FreshMetricsFilter{Named: plugins.NewNamed(FreshMetricsFilterType), threshold: threshold, clock: clock}
}

func (f *FreshMetricsFilter) Filter(_ context.Context, state *framework.CycleState, _ *framework.Request, eps []datalayer.Endpoint) []datalayer.Endpoint {
	now := f.clock.Now()
	return room(state, eps, func(ep datalayer.Endpoint) bool {
		m, ok := plugins.Metrics(ep)
		return ok && !m.Stale(now, f.threshold)
	})
}

// Keeps endpoints whose address the request subset lists, a subset port of zero matching every port on the host
type SubsetFilter struct{ plugins.Named }

func NewSubsetFilter() *SubsetFilter { return &SubsetFilter{plugins.NewNamed(SubsetFilterType)} }

func (f *SubsetFilter) Filter(_ context.Context, _ *framework.CycleState, req *framework.Request, eps []datalayer.Endpoint) []datalayer.Endpoint {
	if !req.SubsetSet && len(req.Subset) == 0 {
		return eps
	}
	exact := make(map[datalayer.Address]bool, len(req.Subset))
	hosts := map[string]bool{}
	for _, a := range req.Subset {
		if a.Port == 0 {
			hosts[a.Host] = true
		} else {
			exact[a] = true
		}
	}
	return keep(eps, func(ep datalayer.Endpoint) bool {
		a := ep.Address()
		return exact[a] || hosts[a.Host]
	})
}

// Keeps endpoints whose role label equals the role or marks them as serving both roles
type RoleFilter struct {
	plugins.Named
	role string
}

func NewRoleFilter(role string) *RoleFilter {
	return &RoleFilter{Named: plugins.NewNamed(RoleFilterType), role: role}
}

func (f *RoleFilter) Filter(_ context.Context, _ *framework.CycleState, _ *framework.Request, eps []datalayer.Endpoint) []datalayer.Endpoint {
	return keep(eps, func(ep datalayer.Endpoint) bool {
		r := ep.Labels()[datalayer.LabelRole]
		return r == f.role || r == datalayer.RoleBoth
	})
}

// Drops endpoints over the KV cache utilization maximum or without metrics, refusing as saturated when none remain
type KVThresholdFilter struct {
	plugins.Named
	max float64
}

func NewKVThresholdFilter(max float64) *KVThresholdFilter {
	return &KVThresholdFilter{Named: plugins.NewNamed(KVThresholdFilterType), max: max}
}

func (f *KVThresholdFilter) Filter(_ context.Context, state *framework.CycleState, _ *framework.Request, eps []datalayer.Endpoint) []datalayer.Endpoint {
	return room(state, eps, func(ep datalayer.Endpoint) bool {
		m, ok := plugins.Metrics(ep)
		return ok && m.KVCacheUtilization <= f.max
	})
}

// Drops endpoints over the queued request maximum or without metrics, refusing as saturated when none remain
type QueueThresholdFilter struct {
	plugins.Named
	max int
}

func NewQueueThresholdFilter(max int) *QueueThresholdFilter {
	return &QueueThresholdFilter{Named: plugins.NewNamed(QueueThresholdFilterType), max: max}
}

func (f *QueueThresholdFilter) Filter(_ context.Context, state *framework.CycleState, _ *framework.Request, eps []datalayer.Endpoint) []datalayer.Endpoint {
	return room(state, eps, func(ep datalayer.Endpoint) bool {
		m, ok := plugins.Metrics(ep)
		return ok && m.QueuedRequests <= f.max
	})
}

// Keeps endpoints serving the adapter, else with free adapter slots, else with it waiting, refusing as saturated when every slot is taken
type LoRAAffinityFilter struct{ plugins.Named }

func NewLoRAAffinityFilter() *LoRAAffinityFilter {
	return &LoRAAffinityFilter{plugins.NewNamed(LoRAAffinityFilterType)}
}

// An endpoint serves the adapter by label, catalog or running gauge, an endpoint without LoRA gauges only that way
func (f *LoRAAffinityFilter) Filter(_ context.Context, state *framework.CycleState, req *framework.Request, eps []datalayer.Endpoint) []datalayer.Endpoint {
	if req.Adapter == "" {
		return eps
	}
	serves := datalayer.HasAdapter(req.Adapter)
	var active, free, waiting []datalayer.Endpoint
	full := 0
	for _, ep := range eps {
		m, ok := plugins.Metrics(ep)
		gauged := ok && m.MaxLoRA > 0
		w := false
		if gauged {
			_, w = m.WaitingAdapters[req.Adapter]
		}
		switch {
		case serves(ep):
			active = append(active, ep)
		case gauged && len(m.RunningAdapters) < m.MaxLoRA:
			free = append(free, ep)
		case gauged && w:
			waiting = append(waiting, ep)
		case gauged:
			full++
		}
	}
	switch {
	case len(active) > 0:
		return active
	case len(free) > 0:
		return free
	case len(waiting) > 0:
		return waiting
	case full > 0:
		framework.Refuse(state, framework.ErrSaturated)
	}
	return nil
}

// Keeps endpoints whose queued request count is within an absolute margin of the lowest
type LeastQueueFilter struct {
	plugins.Named
	margin int
}

func NewLeastQueueFilter(margin int) *LeastQueueFilter {
	return &LeastQueueFilter{Named: plugins.NewNamed(LeastQueueFilterType), margin: margin}
}

func (f *LeastQueueFilter) Filter(_ context.Context, _ *framework.CycleState, _ *framework.Request, eps []datalayer.Endpoint) []datalayer.Endpoint {
	lowest := -1
	for _, ep := range eps {
		if m, ok := plugins.Metrics(ep); ok && (lowest < 0 || m.QueuedRequests < lowest) {
			lowest = m.QueuedRequests
		}
	}
	return keep(eps, func(ep datalayer.Endpoint) bool {
		m, ok := plugins.Metrics(ep)
		return ok && m.QueuedRequests <= lowest+f.margin
	})
}

// Drops endpoints at their request capacity or whose assumed tokens plus the request's cost exceed their KV tokens, refusing as saturated when none remain
type CapacityFilter struct{ plugins.Named }

func NewCapacityFilter() *CapacityFilter {
	return &CapacityFilter{plugins.NewNamed(CapacityFilterType)}
}

// An endpoint holding no tokens always fits, the runtime judging a prompt against its own context length
func (f *CapacityFilter) Filter(_ context.Context, state *framework.CycleState, req *framework.Request, eps []datalayer.Endpoint) []datalayer.Endpoint {
	cost := plugins.Cost(req)
	return room(state, eps, func(ep datalayer.Endpoint) bool {
		n, _ := datalayer.Attribute[int](ep, datalayer.CollectorAssumed)
		if capacity, _ := datalayer.Attribute[int](ep, datalayer.CollectorCapacity); capacity > 0 && n >= capacity {
			return false
		}
		tokens, _ := datalayer.Attribute[int](ep, datalayer.CollectorAssumedTokens)
		kv, ok := plugins.KVTokens(ep)
		return !ok || tokens <= 0 || tokens+cost <= kv
	})
}

// Drops endpoints whose predicted TTFT or TPOT misses the request's objective, refusing as saturated when every one misses
type ObjectiveFilter struct {
	plugins.Named
	pred latency.Predictor
	idx  prefixcache.Indexer
}

func NewObjectiveFilter(pred latency.Predictor, idx prefixcache.Indexer) *ObjectiveFilter {
	return &ObjectiveFilter{Named: plugins.NewNamed(ObjectiveFilterType), pred: pred, idx: idx}
}

// A confident prediction misses by mean plus one deviation, an unmeasured endpoint by the prior's mean
func (f *ObjectiveFilter) Filter(_ context.Context, state *framework.CycleState, req *framework.Request, eps []datalayer.Endpoint) []datalayer.Endpoint {
	if req.Objective.TTFT <= 0 && req.Objective.TPOT <= 0 {
		return eps
	}
	feats := plugins.LatencyFeatures(f.idx, state, req, eps)
	return room(state, eps, func(ep datalayer.Endpoint) bool {
		id := ep.ID()
		if req.Objective.TTFT > 0 && plugins.Misses(f.pred.PredictTTFT(id, feats[id]), req.Objective.TTFT) {
			return false
		}
		return req.Objective.TPOT <= 0 || !plugins.Misses(f.pred.PredictTPOT(id, feats[id]), req.Objective.TPOT)
	})
}
