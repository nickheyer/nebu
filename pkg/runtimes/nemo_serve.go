package runtimes

import (
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/triage"
)

// Export-Deploy's sequence length when the launch passes none
const nemoDefaultContext = 4096

// Ray Serve's queued and processing gauges from the launch's own Ray cluster, every running request holding a static cache
func (NeMo) Metrics() *v1.MetricNames {
	return &v1.MetricNames{
		QueuedRequests:  "ray_serve_deployment_queued_queries",
		RunningRequests: "ray_serve_replica_processing_queries",
		StaticCache:     true,
		WorkCounter:     "ray_serve_deployment_request_counter_total",
		MetricsPath:     "/metrics",
		HealthPath:      "/v1/health",
	}
}

// The port after the served port, where the launch pins Ray's metrics export
func (NeMo) MetricsPort(_ map[string]string, port int) int { return port + 1 }

func (NeMo) AppProtocol(map[string]string) v1.AppProtocol { return v1.AppProtocol_APP_PROTOCOL_HTTP }

func (NeMo) GRPCPort(map[string]string, int) int { return 0 }

// The served port and Ray's metrics export port after it
func (NeMo) PortSpan(Launch) int { return 2 }

// One rank, NeMo running no data parallel ranks of its own
func (NeMo) Ranks(map[string]string) int { return 1 }

// The expert tensor parallel size the launch recorded, one when it left Export-Deploy's default
func (NeMo) ExpertParallel(params map[string]string) int {
	return int(max(paramInt(params, "expert_tensor_parallel_size"), 1))
}

// Sequences every Ray Serve replica batches at once
func (NeMo) Capacity(params map[string]string) uint32 {
	return perReplica(params, "max_batch_size", "num_replicas")
}

// Cells of the static caches, one full context per sequence every replica batches at once
func (NeMo) KVCapacity(params map[string]string, _ *v1.MemoryPlan) uint32 {
	return contextOf(params, "n_ctx", nemoDefaultContext) * (NeMo{}).Capacity(params)
}

func (NeMo) Adapters(string, map[string]string) AdapterClient { return nil }

func (NeMo) Sleeper(string, map[string]string) SleepClient { return nil }

// Context from the sequence length param or Export-Deploy's 4096, text in and out
func (NeMo) Inventory(in Launch, params map[string]string) (*v1.RuntimeInventory, error) {
	inv, err := baseInventory(in, params)
	if err != nil {
		return nil, err
	}
	inv.ContextLength = contextOf(params, "n_ctx", nemoDefaultContext)
	inv.Modalities = []string{"text"}
	return inv, nil
}

func (NeMo) PortRule() triage.Rule { return pythonPortRule("NeMo") }
