package runtimes

import (
	"regexp"
	"strings"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/triage"
)

// Queue and running gauges from the metrics endpoint, KV use and cells from the slot listing, adapters from their listing
func (LlamaCpp) Metrics() *v1.MetricNames {
	return &v1.MetricNames{
		QueuedRequests:  "llamacpp:requests_deferred",
		RunningRequests: "llamacpp:requests_processing",
		SlotsPath:       "/slots",
		AdaptersPath:    "/lora-adapters",
		AdaptersFormat:  "llamacpp",
		MetricsPath:     "/metrics",
		HealthPath:      "/health",
	}
}

func (LlamaCpp) MetricsPort(_ map[string]string, port int) int { return port }

func (LlamaCpp) AppProtocol(map[string]string) v1.AppProtocol {
	return v1.AppProtocol_APP_PROTOCOL_HTTP
}

func (LlamaCpp) GRPCPort(map[string]string, int) int { return 0 }

func (LlamaCpp) PortSpan(Launch) int { return 1 }

// One rank, llama.cpp serving its slots from one process
func (LlamaCpp) Ranks(map[string]string) int { return 1 }

// One, llama.cpp keeping every expert of a layer on the devices the layer splits over
func (LlamaCpp) ExpertParallel(map[string]string) int { return 1 }

// Server slots, zero until the count llama.cpp picked for n_parallel -1 is read from its log
func (LlamaCpp) Capacity(params map[string]string) uint32 {
	if n := paramInt(params, "n_parallel"); n > 0 {
		return uint32(n)
	}
	return 0
}

// Cells of the whole pool, unified or split into slot shares, which no llama.cpp response states
func (LlamaCpp) KVCapacity(params map[string]string, plan *v1.MemoryPlan) uint32 {
	if n := paramInt(params, "n_ctx"); n > 0 {
		return uint32(n)
	}
	if n := paramInt(plan.GetParams(), "n_ctx"); n > 0 {
		return uint32(n)
	}
	if per, slots := paramInt(params, "kv_unified_per_slot"), paramInt(params, "n_parallel"); paramOn(params, "kv_unified") && per > 0 && slots > 0 {
		return uint32(per * slots)
	}
	return 0
}

// Switches the adapters loaded at launch when the launch loaded any
func (LlamaCpp) Adapters(endpoint string, params map[string]string) AdapterClient {
	if strings.TrimSpace(params["lora"]) == "" && strings.TrimSpace(params["lora_scaled"]) == "" {
		return nil
	}
	return llamaAdapters{base: endpoint}
}

func (LlamaCpp) Sleeper(string, map[string]string) SleepClient { return nil }

// Context one slot holds, the whole pool when unified, rerank or embedding alone for those modes, else the projector's modalities
func (LlamaCpp) Inventory(in Launch, params map[string]string) (*v1.RuntimeInventory, error) {
	inv, err := baseInventory(in, params)
	if err != nil {
		return nil, err
	}
	inv.ContextLength = llamaSlotContext(in, params)
	switch {
	case paramOn(params, "reranking"), paramOn(params, "embedding") && strings.TrimSpace(params["pooling"]) == "rank":
		inv.Modalities = []string{ModalityRerank}
		return inv, nil
	case paramOn(params, "embedding"):
		inv.Modalities = []string{ModalityEmbedding}
		return inv, nil
	}
	inv.Modalities = []string{"text"}
	if mmproj := strings.TrimSpace(params["mmproj"]); mmproj != "" {
		more, err := projectorModalities(mmproj)
		if err != nil {
			return nil, err
		}
		inv.Modalities = append(inv.Modalities, more...)
	}
	return inv, nil
}

// Tokens one slot holds, the trained length per slot until llama.cpp's own context is recorded
func llamaSlotContext(in Launch, params map[string]string) uint32 {
	pool := paramInt(params, "n_ctx")
	if pool <= 0 {
		return uint32(trained(in))
	}
	if paramOn(params, "kv_unified") {
		if per := paramInt(params, "kv_unified_per_slot"); per > 0 {
			return uint32(min(per, pool))
		}
		return uint32(pool)
	}
	return uint32(pool / max(paramInt(params, "n_parallel"), 1))
}

// llama-server names the port it could not bind as port N
var llamaBindRe = regexp.MustCompile(`port[:=]?\s*(\d+)`)

func (LlamaCpp) PortRule() triage.Rule {
	return triage.Rule{
		ID:      portRuleID,
		Summary: "llama.cpp found its port taken by another process",
		Hint:    "reserve another port and launch again",
		Match: func(line string) (map[string]string, bool) {
			lower := strings.ToLower(line)
			if !strings.Contains(lower, "address already in use") && !strings.Contains(lower, "couldn't bind") && !strings.Contains(lower, "couldn’t bind") {
				return nil, false
			}
			if m := llamaBindRe.FindStringSubmatch(lower); m != nil {
				return map[string]string{"port": m[1]}, true
			}
			return nil, true
		},
	}
}
