package runtimes

import (
	"os"
	"strconv"
	"strings"

	"github.com/nickheyer/nebu/pkg/estimate"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/triage"
)

// Queue and running totals, token use and pages per data parallel rank, adapters from model listing and server info
func (SGLang) Metrics() *v1.MetricNames {
	return &v1.MetricNames{
		QueuedRequests:       "sglang:num_queue_reqs",
		QueuedRequestsMatch:  map[string]string{"priority": ""},
		RunningRequests:      "sglang:num_running_reqs",
		RunningRequestsMatch: map[string]string{"priority": ""},
		KvCacheUtilization:   "sglang:token_usage",
		BlockSize:            "sglang:page_size",
		NumBlocks:            "sglang:num_pages",
		ReplicaLabel:         "dp_rank",
		AdaptersPath:         "/v1/models",
		AdaptersFormat:       "openai",
		MaxLoraPath:          "/server_info",
		MaxLoraField:         "max_loras_per_batch",
		WorkCounter:          "sglang:num_requests_total",
		MetricsPath:          "/metrics",
		HealthPath:           "/health",
	}
}

func (SGLang) MetricsPort(_ map[string]string, port int) int { return port }

// gRPC when the launch serves the native SglangService, HTTP otherwise
func (SGLang) AppProtocol(params map[string]string) v1.AppProtocol { return grpcProtocol(params) }

// The port after the HTTP port when the launch serves gRPC
func (SGLang) GRPCPort(params map[string]string, port int) int { return grpcPort(params, port) }

// Every rank's HTTP and gRPC ports, then every rank's KV event and replay ports
func (SGLang) PortSpan(in Launch) int {
	ranks := launchRanks(in.Params, "dp_size")
	return ranks*rankSpan(in.Params) + 2*ranks
}

// The KV event and replay addresses of a rank, after every rank's serving ports, none without the radix cache
func (SGLang) KVEvents(params map[string]string, host string, port, rank int) (string, string) {
	if paramOn(params, "disable_radix_cache") {
		return "", ""
	}
	return kvEventAddresses(host, port, rank, recordedRanks(params, "dp_size"), recordedRankSpan(params), 0)
}

// The publisher config of a rank bound on the launch host, every rank a server of its own
func sglangKVEventsConfig(in Launch, p estimate.Params, ranks int) string {
	if p.Bool("disable_radix_cache") {
		return ""
	}
	events, replay := kvEventPorts(in.Port, ranks, rankSpan(p), 0, in.Rank)
	return kvEventsConfig(in.Host, events, replay, map[string]any{"bind": true})
}

// One server per data parallel rank
func (SGLang) Ranks(params map[string]string) int { return recordedRanks(params, "dp_size") }

// The ep_size the launch recorded, one when it left SGLang's default
func (SGLang) ExpertParallel(params map[string]string) int {
	return int(max(paramInt(params, "ep_size"), 1))
}

// Requests one rank runs at once, zero until SGLang's own cap is read from its log
func (SGLang) Capacity(params map[string]string) uint32 {
	return perEngine(params, "max_running_requests")
}

// Zero since SGLang reports its pages through its page gauges
func (SGLang) KVCapacity(map[string]string, *v1.MemoryPlan) uint32 { return 0 }

// Named loads and unloads when the launch turned LoRA on
func (SGLang) Adapters(endpoint string, params map[string]string) AdapterClient {
	if !paramOn(params, "enable_lora") {
		return nil
	}
	return namedAdapters{base: endpoint, load: "/load_lora_adapter", unload: "/unload_lora_adapter"}
}

func (SGLang) Sleeper(string, map[string]string) SleepClient { return nil }

// The template file the chat_template param names, or the built in template it names when it is no file
func (SGLang) TemplateOverride(params map[string]string) (string, string) {
	named := strings.TrimSpace(params["chat_template"])
	if info, err := os.Stat(named); named != "" && err == nil && !info.IsDir() {
		return "", named
	}
	return named, ""
}

// Context from the context length param or the trained length, embedding alone for an embedding launch, else the model's encoders
func (SGLang) Inventory(in Launch, params map[string]string) (*v1.RuntimeInventory, error) {
	inv, err := baseInventory(in, params)
	if err != nil {
		return nil, err
	}
	inv.ContextLength = contextOf(params, "n_ctx", trained(in))
	inv.Modalities = encoderModalities(in.Descriptor)
	if paramOn(params, "is_embedding") {
		inv.Modalities = []string{ModalityEmbedding}
	}
	return inv, nil
}

func (SGLang) PortRule() triage.Rule { return pythonPortRule("SGLang") }

// Running request cap SGLang's scheduler logs at start as max_running_requests=N
func sglangRunning(lines []string) (string, bool) {
	const key = "max_running_requests="
	for _, line := range lines {
		i := strings.Index(line, key)
		if i < 0 {
			continue
		}
		digits := line[i+len(key):]
		end := strings.IndexFunc(digits, func(r rune) bool { return r < '0' || r > '9' })
		if end >= 0 {
			digits = digits[:end]
		}
		if n, err := strconv.ParseUint(digits, 10, 32); err == nil && n > 0 {
			return strconv.FormatUint(n, 10), true
		}
	}
	return "", false
}
