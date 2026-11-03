package scrape

import "github.com/nickheyer/nebu/pkg/datalayer"

// Runtime names the capabilities table is keyed by
const (
	RuntimeVLLM         = "vllm"
	RuntimeSGLang       = "sglang"
	RuntimeTritonTRTLLM = "triton-trtllm"
	RuntimeTRTLLMServe  = "trtllm-serve"
	RuntimeLlamaCpp     = "llamacpp"
	RuntimeNeMo         = "nemo"
)

// The metric names and paths each supported runtime serves
var runtimes = map[string]datalayer.MetricCapabilities{
	RuntimeVLLM: {
		QueuedRequests:       "vllm:num_requests_waiting",
		RunningRequests:      "vllm:num_requests_running",
		KVCacheUtilization:   "vllm:kv_cache_usage_perc",
		CacheConfigInfo:      "vllm:cache_config_info",
		BlockSizeLabel:       "block_size",
		NumBlocksLabel:       "num_gpu_blocks",
		LoRAInfo:             "vllm:lora_requests_info",
		MaxLoRALabel:         "max_lora",
		RunningAdaptersLabel: "running_lora_adapters",
		WaitingAdaptersLabel: "waiting_lora_adapters",
		Path:                 "/metrics",
		HealthPath:           "/health",
	},
	RuntimeSGLang: {
		QueuedRequests:       "sglang:num_queue_reqs",
		QueuedRequestsMatch:  map[string]string{"priority": ""},
		RunningRequests:      "sglang:num_running_reqs",
		RunningRequestsMatch: map[string]string{"priority": ""},
		KVCacheUtilization:   "sglang:token_usage",
		BlockSize:            "sglang:page_size",
		NumBlocks:            "sglang:num_pages",
		ReplicaLabel:         "dp_rank",
		AdaptersPath:         "/v1/models",
		AdaptersFormat:       datalayer.AdaptersOpenAI,
		MaxLoRAPath:          "/server_info",
		MaxLoRAField:         "max_loras_per_batch",
		Path:                 "/metrics",
		HealthPath:           "/health",
	},
	RuntimeTritonTRTLLM: {
		QueuedRequests:          "nv_trt_llm_request_metrics",
		QueuedRequestsMatch:     map[string]string{"request_type": "waiting"},
		RunningRequests:         "nv_trt_llm_request_metrics",
		RunningRequestsMatch:    map[string]string{"request_type": "scheduled"},
		KVCacheUtilization:      "nv_trt_llm_kv_cache_block_metrics",
		KVCacheUtilizationMatch: map[string]string{"kv_cache_block_type": "fraction"},
		BlockSize:               "nv_trt_llm_kv_cache_block_metrics",
		BlockSizeMatch:          map[string]string{"kv_cache_block_type": "tokens_per"},
		NumBlocks:               "nv_trt_llm_kv_cache_block_metrics",
		NumBlocksMatch:          map[string]string{"kv_cache_block_type": "max"},
		Path:                    "/metrics",
		MetricsPort:             8002,
		HealthPath:              "/v2/health/ready",
	},
	RuntimeTRTLLMServe: {
		QueuedRequests:     "trtllm_num_requests_waiting",
		RunningRequests:    "trtllm_num_requests_running",
		KVCacheUtilization: "trtllm_kv_cache_utilization",
		BlockSize:          "trtllm_kv_cache_tokens_per_block",
		NumBlocks:          "trtllm_kv_cache_max_blocks",
		Path:               "/prometheus/metrics",
		HealthPath:         "/health",
	},
	RuntimeLlamaCpp: {
		QueuedRequests:  "llamacpp:requests_deferred",
		RunningRequests: "llamacpp:requests_processing",
		SlotsPath:       "/slots",
		AdaptersPath:    "/lora-adapters",
		AdaptersFormat:  datalayer.AdaptersLlamaCpp,
		Path:            "/metrics",
		HealthPath:      "/health",
	},
	RuntimeNeMo: {
		QueuedRequests:  "ray_serve_deployment_queued_queries",
		RunningRequests: "ray_serve_replica_processing_queries",
		StaticCache:     true,
		Path:            "/metrics",
		HealthPath:      "/v1/health",
	},
}

// A private copy of the metric names a runtime declares, false for a runtime the table lacks
func Capabilities(runtime string) (datalayer.MetricCapabilities, bool) {
	c, ok := runtimes[runtime]
	return c.Clone(), ok
}
