package triage

// The failures vllm serve prints
type VLLM struct{}

func (VLLM) ID() string          { return "vllm" }
func (VLLM) Description() string { return "Failure patterns of vllm serve" }

func (VLLM) Rules() []Rule {
	return []Rule{
		{
			ID:      "device-oom",
			Summary: "device ran out of memory",
			Hint:    "lower n_ctx or gpu_memory_utilization, or shard with tensor_parallel_size",
			Match:   anyOf("torch.OutOfMemoryError", "CUDA out of memory", "HIP out of memory"),
		},
		{
			ID:      "kv-cache-too-small",
			Summary: "the context does not fit in the cache budget",
			Hint:    "lower n_ctx or raise gpu_memory_utilization",
			Match:   anyOf("is larger than the maximum number of tokens that can be stored in KV cache", "No available memory for the cache blocks"),
		},
		{
			ID:      "unsupported-arch",
			Summary: "this build does not support the model architecture",
			Hint:    "upgrade the runtime",
			Match: func(line string) (map[string]string, bool) {
				if _, ok := allOf("Model architectures", "are not supported")(line); ok {
					return nil, true
				}
				return allOf("has no attribute", "ForCausalLM")(line)
			},
		},
		{
			ID:      "port-in-use",
			Summary: "the port is taken",
			Hint:    "stop whatever holds the port or run the model again to get a fresh port",
			Match:   anyOf("address already in use"),
		},
	}
}
