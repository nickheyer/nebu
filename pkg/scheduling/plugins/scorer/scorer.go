// Package scorer holds the reference scorers, each answering a score from zero to one per endpoint
package scorer

import (
	"context"
	"math"
	"strconv"

	"github.com/nickheyer/nebu/pkg/datalayer"
	"github.com/nickheyer/nebu/pkg/scheduling/framework"
	"github.com/nickheyer/nebu/pkg/scheduling/plugins"
)

// Plugin type names
const (
	QueueScorerType              = "queue-scorer"
	KVCacheUtilizationScorerType = "kv-cache-utilization-scorer"
	LoRAAffinityScorerType       = "lora-affinity-scorer"
	PrefixCacheScorerType        = "prefix-cache-scorer"
	ThroughputScorerType         = "throughput-scorer"
	LatencyScorerType            = "latency-scorer"
	AssumedLoadScorerType        = "assumed-load-scorer"
	SessionAffinityScorerType    = "session-affinity-scorer"
	ExpertParallelScorerType     = "expert-parallel-scorer"
)

// The collector whose float64 attribute holds an endpoint's measured output tokens per second
const CollectorThroughput = "throughput"

// Scores one for the lowest value, zero for the highest, linear between, one for all when equal, zero when unknown
func lowerIsBetter(eps []datalayer.Endpoint, vals map[datalayer.EndpointID]float64) map[datalayer.EndpointID]float64 {
	lo, hi := math.Inf(1), math.Inf(-1)
	for _, v := range vals {
		lo, hi = min(lo, v), max(hi, v)
	}
	out := make(map[datalayer.EndpointID]float64, len(eps))
	for _, ep := range eps {
		v, ok := vals[ep.ID()]
		switch {
		case !ok:
			out[ep.ID()] = 0
		case hi == lo:
			out[ep.ID()] = 1
		default:
			out[ep.ID()] = (hi - v) / (hi - lo)
		}
	}
	return out
}

// Prefers endpoints with fewer queued requests, endpoints without metrics scoring zero
type QueueScorer struct{ plugins.Named }

func NewQueueScorer() *QueueScorer { return &QueueScorer{plugins.NewNamed(QueueScorerType)} }

func (s *QueueScorer) Score(_ context.Context, _ *framework.CycleState, _ *framework.Request, eps []datalayer.Endpoint) map[datalayer.EndpointID]float64 {
	vals := make(map[datalayer.EndpointID]float64, len(eps))
	for _, ep := range eps {
		if m, ok := plugins.Metrics(ep); ok {
			vals[ep.ID()] = float64(m.QueuedRequests)
		}
	}
	return lowerIsBetter(eps, vals)
}

// Scores one minus KV cache utilization, endpoints without metrics scoring zero
type KVCacheUtilizationScorer struct{ plugins.Named }

func NewKVCacheUtilizationScorer() *KVCacheUtilizationScorer {
	return &KVCacheUtilizationScorer{plugins.NewNamed(KVCacheUtilizationScorerType)}
}

func (s *KVCacheUtilizationScorer) Score(_ context.Context, _ *framework.CycleState, _ *framework.Request, eps []datalayer.Endpoint) map[datalayer.EndpointID]float64 {
	out := make(map[datalayer.EndpointID]float64, len(eps))
	for _, ep := range eps {
		if m, ok := plugins.Metrics(ep); ok {
			out[ep.ID()] = 1 - min(max(m.KVCacheUtilization, 0), 1)
		} else {
			out[ep.ID()] = 0
		}
	}
	return out
}

// Scores one with the adapter active, half with free adapter slots, a quarter with it waiting, else zero
type LoRAAffinityScorer struct{ plugins.Named }

func NewLoRAAffinityScorer() *LoRAAffinityScorer {
	return &LoRAAffinityScorer{plugins.NewNamed(LoRAAffinityScorerType)}
}

func (s *LoRAAffinityScorer) Score(_ context.Context, _ *framework.CycleState, req *framework.Request, eps []datalayer.Endpoint) map[datalayer.EndpointID]float64 {
	out := make(map[datalayer.EndpointID]float64, len(eps))
	for _, ep := range eps {
		if req.Adapter == "" {
			out[ep.ID()] = 1
			continue
		}
		out[ep.ID()] = 0
		m, ok := plugins.Metrics(ep)
		if !ok {
			continue
		}
		_, waiting := m.WaitingAdapters[req.Adapter]
		switch {
		case m.AdapterActive(req.Adapter):
			out[ep.ID()] = 1
		case len(m.RunningAdapters) < m.MaxLoRA:
			out[ep.ID()] = 0.5
		case waiting:
			out[ep.ID()] = 0.25
		}
	}
	return out
}

// Scores measured tokens per second divided by the highest among the endpoints, unknown scoring zero
type ThroughputScorer struct{ plugins.Named }

func NewThroughputScorer() *ThroughputScorer {
	return &ThroughputScorer{plugins.NewNamed(ThroughputScorerType)}
}

func (s *ThroughputScorer) Score(_ context.Context, _ *framework.CycleState, _ *framework.Request, eps []datalayer.Endpoint) map[datalayer.EndpointID]float64 {
	vals := make(map[datalayer.EndpointID]float64, len(eps))
	top := 0.0
	for _, ep := range eps {
		v, _ := datalayer.Attribute[float64](ep, CollectorThroughput)
		vals[ep.ID()] = max(v, 0)
		top = max(top, v)
	}
	out := make(map[datalayer.EndpointID]float64, len(eps))
	for id, v := range vals {
		if top > 0 {
			out[id] = v / top
		} else {
			out[id] = 0
		}
	}
	return out
}

// Prefers endpoints with fewer requests this gateway assumed in flight, an endpoint never counted holding none
type AssumedLoadScorer struct{ plugins.Named }

func NewAssumedLoadScorer() *AssumedLoadScorer {
	return &AssumedLoadScorer{plugins.NewNamed(AssumedLoadScorerType)}
}

func (s *AssumedLoadScorer) Score(_ context.Context, _ *framework.CycleState, _ *framework.Request, eps []datalayer.Endpoint) map[datalayer.EndpointID]float64 {
	vals := make(map[datalayer.EndpointID]float64, len(eps))
	for _, ep := range eps {
		n, _ := datalayer.Attribute[int](ep, datalayer.CollectorAssumed)
		vals[ep.ID()] = float64(n)
	}
	return lowerIsBetter(eps, vals)
}

// Prefers the lowest load per expert parallel rank, so a wide seat fills its all to all batch before a narrow one takes more
type ExpertParallelScorer struct{ plugins.Named }

func NewExpertParallelScorer() *ExpertParallelScorer {
	return &ExpertParallelScorer{plugins.NewNamed(ExpertParallelScorerType)}
}

// Load is queued plus running plus assumed requests, width the expert-parallel label or one
func (s *ExpertParallelScorer) Score(_ context.Context, _ *framework.CycleState, _ *framework.Request, eps []datalayer.Endpoint) map[datalayer.EndpointID]float64 {
	vals := make(map[datalayer.EndpointID]float64, len(eps))
	for _, ep := range eps {
		load := 0
		if m, ok := plugins.Metrics(ep); ok {
			load = m.QueuedRequests + m.RunningRequests
		}
		assumed, _ := datalayer.Attribute[int](ep, datalayer.CollectorAssumed)
		load += assumed
		width, err := strconv.Atoi(ep.Labels()[datalayer.LabelExpertParallel])
		if err != nil || width < 1 {
			width = 1
		}
		vals[ep.ID()] = float64(load) / float64(width)
	}
	return lowerIsBetter(eps, vals)
}
