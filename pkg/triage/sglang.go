package triage

// The failures sglang.launch_server prints
type SGLang struct{}

func (SGLang) ID() string          { return "sglang" }
func (SGLang) Description() string { return "Failure patterns of sglang.launch_server" }

func (SGLang) Rules() []Rule {
	return []Rule{
		{
			ID:      "device-oom",
			Summary: "device ran out of memory",
			Hint:    "lower n_ctx, chunked_prefill_size, or mem_fraction_static, or shard with tp_size",
			Match:   anyOf("torch.OutOfMemoryError", "CUDA out of memory"),
		},
		{
			ID:      "pool-too-small",
			Summary: "the cache pool cannot hold one full length request",
			Hint:    "raise mem_fraction_static or lower n_ctx",
			Match: func(line string) (map[string]string, bool) {
				if _, ok := anyOf("Not enough memory. Please try to increase --mem-fraction-static", "not enough memory for the KV cache")(line); ok {
					return nil, true
				}
				return allOf("KV cache pool", "too small")(line)
			},
		},
		{
			ID:      "unsupported-arch",
			Summary: "this build does not support the model architecture",
			Hint:    "upgrade the runtime",
			Match:   anyOf("Unsupported architectures", "is not supported by SGLang", "does not support model"),
		},
		{
			ID:      "kernel-mismatch",
			Summary: "the kernel package does not match the runtime",
			Hint:    "rebuild the sglang recipe so sglang and sgl-kernel come from one install",
			Match: func(line string) (map[string]string, bool) {
				if _, ok := anyOf("sgl_kernel", "sgl-kernel")(line); !ok {
					return nil, false
				}
				return anyOf("version", "not found", "ImportError", "undefined symbol")(line)
			},
		},
		{
			ID:      "missing-module",
			Summary: "the install is missing a Python package",
			Hint:    "rebuild the sglang recipe, the virtual environment is incomplete",
			Match:   anyOf("ModuleNotFoundError", "No module named"),
		},
		{
			ID:      "port-in-use",
			Summary: "the port is taken",
			Hint:    "stop whatever holds the port or run the model again to get a fresh port",
			Match:   anyOf("address already in use"),
		},
	}
}
