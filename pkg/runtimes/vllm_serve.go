package runtimes

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/nickheyer/nebu/pkg/estimate"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/triage"
)

// Queue, running and KV gauges, the cache config and LoRA info gauges with their labels
func (VLLM) Metrics() *v1.MetricNames {
	return &v1.MetricNames{
		QueuedRequests:       "vllm:num_requests_waiting",
		RunningRequests:      "vllm:num_requests_running",
		KvCacheUtilization:   "vllm:kv_cache_usage_perc",
		CacheConfigInfo:      "vllm:cache_config_info",
		BlockSizeLabel:       "block_size",
		NumBlocksLabel:       "num_gpu_blocks",
		LoraInfo:             "vllm:lora_requests_info",
		MaxLoraLabel:         "max_lora",
		RunningAdaptersLabel: "running_lora_adapters",
		WaitingAdaptersLabel: "waiting_lora_adapters",
		WorkCounter:          "vllm:prompt_tokens_total",
		MetricsPath:          "/metrics",
		HealthPath:           "/health",
	}
}

func (VLLM) MetricsPort(_ map[string]string, port int) int { return port }

// gRPC when the launch serves the Rust frontend's services, HTTP otherwise
func (VLLM) AppProtocol(params map[string]string) v1.AppProtocol { return grpcProtocol(params) }

// The port after the HTTP port when the launch serves gRPC
func (VLLM) GRPCPort(params map[string]string, port int) int { return grpcPort(params, port) }

// Every rank's HTTP and gRPC ports, the coordinator port when the ranks share experts, then every rank's KV event and replay ports
func (VLLM) PortSpan(in Launch) int {
	ranks := launchRanks(in.Params, "data_parallel_size")
	span := ranks * rankSpan(in.Params)
	if ranks > 1 && mixtureOfExperts(in.Descriptor) {
		span++
	}
	return span + 2*ranks
}

// Whether the recorded launch runs its ranks as one data parallel group behind a coordinator
func vllmCoordinated(params map[string]string) bool { return params["data_parallel_rpc_port"] != "" }

// The KV event and replay addresses of a rank, after every rank's serving ports and the coordinator, none without prefix caching
func (VLLM) KVEvents(params map[string]string, host string, port, rank int) (string, string) {
	if params["enable_prefix_caching"] == "off" {
		return "", ""
	}
	extra := 0
	if vllmCoordinated(params) {
		extra = 1
	}
	return kvEventAddresses(host, port, rank, recordedRanks(params, "data_parallel_size"), recordedRankSpan(params), extra)
}

// The publisher config of a rank, vLLM itself adding its data parallel rank to the ports of a coordinated group
func vllmKVEventsConfig(in Launch, p estimate.Params, emitted map[string]string, ranks int, coordinated bool) string {
	if emitted["enable_prefix_caching"] == "off" {
		return ""
	}
	extra, rank := 0, in.Rank
	if coordinated {
		extra, rank = 1, 0
	}
	events, replay := kvEventPorts(in.Port, ranks, rankSpan(p), extra, rank)
	return kvEventsConfig("*", events, replay, map[string]any{"enable_kv_cache_events": true})
}

// One process per data parallel rank
func (VLLM) Ranks(params map[string]string) int { return recordedRanks(params, "data_parallel_size") }

// The tensor parallel devices of every rank together when expert parallel is on, one otherwise
func (VLLM) ExpertParallel(params map[string]string) int {
	if !paramOn(params, "enable_expert_parallel") {
		return 1
	}
	return int(max(paramInt(params, "tensor_parallel_size"), 1)) * recordedRanks(params, "data_parallel_size")
}

// Sequences one rank runs at once, each rank its own endpoint
func (VLLM) Capacity(params map[string]string) uint32 {
	return perEngine(params, "max_num_seqs")
}

// Zero since vLLM reports its blocks through the cache config gauge
func (VLLM) KVCapacity(map[string]string, *v1.MemoryPlan) uint32 { return 0 }

// Named loads and unloads when the launch turned LoRA on
func (VLLM) Adapters(endpoint string, params map[string]string) AdapterClient {
	if !paramOn(params, "enable_lora") {
		return nil
	}
	return namedAdapters{base: endpoint, load: "/v1/load_lora_adapter", unload: "/v1/unload_lora_adapter"}
}

// Level one sleep and wake when the launch turned sleep mode on
func (VLLM) Sleeper(endpoint string, params map[string]string) SleepClient {
	if !paramOn(params, "enable_sleep_mode") {
		return nil
	}
	return vllmSleeper{base: endpoint}
}

// Context from the model length param or the trained length, offload from the plan or cpu_offload_gb, modalities the encoders allow
func (VLLM) Inventory(in Launch, params map[string]string) (*v1.RuntimeInventory, error) {
	inv, err := baseInventory(in, params)
	if err != nil {
		return nil, err
	}
	inv.ContextLength = contextOf(params, "n_ctx", trained(in))
	if gb, err := strconv.ParseFloat(strings.TrimSpace(params["cpu_offload_gb"]), 64); err == nil && gb > 0 && inv.ThroughputClass == ThroughputDevice {
		inv.ThroughputClass = ThroughputOffload
	}
	if inv.Modalities, err = VLLMModalities(in.Descriptor, params); err != nil {
		return nil, err
	}
	return inv, nil
}

// Params vLLM resolves itself when left empty, and the tasks its server reports, read back from its log
const (
	vllmRunner  = "runner"
	vllmConvert = "convert"
	// The tasks vLLM's server says it supports, comma separated, as its log names them at start
	VLLMSupportedTasks = "supported_tasks"
)

// The lines vLLM logs as it resolves an auto runner and conversion and as its server starts
var (
	vllmResolvedRunner  = regexp.MustCompile("Resolved `--runner auto` to `--runner (\\w+)`")
	vllmResolvedConvert = regexp.MustCompile("Resolved `--convert auto` to `--convert (\\w+)`")
	vllmTasksLine       = regexp.MustCompile(`Supported tasks: \((.*)\)`)
	vllmTaskName        = regexp.MustCompile(`'([\w&]+)'`)
)

// Readers of what vLLM chose for an empty runner and conversion and the tasks its server supports
func VLLMResolved() map[string]func([]string) (string, bool) {
	return map[string]func([]string) (string, bool){
		vllmRunner:         func(lines []string) (string, bool) { return vllmResolved(lines, vllmResolvedRunner, "generate") },
		vllmConvert:        func(lines []string) (string, bool) { return vllmResolved(lines, vllmResolvedConvert, "none") },
		VLLMSupportedTasks: vllmTasks,
	}
}

// The value a resolved line names, or the common value vLLM leaves unlogged once its server reported its tasks
func vllmResolved(lines []string, re *regexp.Regexp, common string) (string, bool) {
	for i := len(lines) - 1; i >= 0; i-- {
		if m := re.FindStringSubmatch(lines[i]); m != nil {
			return m[1], true
		}
	}
	if _, started := vllmTasks(lines); started {
		return common, true
	}
	return "", false
}

// The tasks the last start line names, comma separated
func vllmTasks(lines []string) (string, bool) {
	for i := len(lines) - 1; i >= 0; i-- {
		m := vllmTasksLine.FindStringSubmatch(lines[i])
		if m == nil {
			continue
		}
		var tasks []string
		for _, t := range vllmTaskName.FindAllStringSubmatch(m[1], -1) {
			tasks = append(tasks, t[1])
		}
		if len(tasks) == 0 {
			return "", false
		}
		return strings.Join(tasks, ","), true
	}
	return "", false
}

// The modalities a vLLM launch serves, from the tasks its server named, else from its runner and conversion
func VLLMModalities(d *v1.Descriptor, params map[string]string) ([]string, error) {
	off, err := vllmModalitiesOff(params["limit_mm_per_prompt"])
	if err != nil {
		return nil, err
	}
	generating := slices.DeleteFunc(encoderModalities(d), func(m string) bool { return off[m] })
	if tasks := strings.TrimSpace(params[VLLMSupportedTasks]); tasks != "" {
		list := strings.Split(tasks, ",")
		switch {
		case slices.ContainsFunc(list, func(t string) bool { return t == "generate" || t == "transcription" || t == "realtime" }):
			return generating, nil
		case slices.Contains(list, "embed"):
			return []string{ModalityEmbedding}, nil
		case slices.Contains(list, "classify"):
			return []string{ModalityClassify}, nil
		}
		return []string{ModalityPooling}, nil
	}
	switch convert, runner := strings.TrimSpace(params[vllmConvert]), strings.TrimSpace(params[vllmRunner]); {
	case convert == "embed":
		return []string{ModalityEmbedding}, nil
	case convert == "classify":
		return []string{ModalityClassify}, nil
	case runner == "pooling":
		return []string{ModalityPooling}, nil
	}
	return generating, nil
}

// Modalities a media limit sets to zero items, by count alone or by a count field
func vllmModalitiesOff(limit string) (map[string]bool, error) {
	off := map[string]bool{}
	if strings.TrimSpace(limit) == "" {
		return off, nil
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(limit), &raw); err != nil {
		return nil, fmt.Errorf("%w: limit_mm_per_prompt is not a JSON object: %v", ErrParam, err)
	}
	for name, value := range raw {
		var count int
		if json.Unmarshal(value, &count) == nil {
			off[name] = count == 0
			continue
		}
		var item struct {
			Count *int `json:"count"`
		}
		if err := json.Unmarshal(value, &item); err != nil {
			return nil, fmt.Errorf("%w: limit_mm_per_prompt %s is neither a count nor an object: %v", ErrParam, name, err)
		}
		off[name] = item.Count != nil && *item.Count == 0
	}
	return off, nil
}

func (VLLM) PortRule() triage.Rule { return pythonPortRule("vLLM") }

// gRPC when the recorded params turned the gRPC server on
func grpcProtocol(params map[string]string) v1.AppProtocol {
	if paramOn(params, "grpc") {
		return v1.AppProtocol_APP_PROTOCOL_GRPC
	}
	return v1.AppProtocol_APP_PROTOCOL_HTTP
}

// The port after the HTTP port when the recorded params turned the gRPC server on, zero otherwise
func grpcPort(params map[string]string, port int) int {
	if paramOn(params, "grpc") {
		return port + 1
	}
	return 0
}
