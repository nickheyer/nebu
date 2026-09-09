package triage

// The failures llama-server prints
type LlamaCpp struct{}

func (LlamaCpp) ID() string          { return "llamacpp" }
func (LlamaCpp) Description() string { return "Failure patterns of llama-server" }

func (LlamaCpp) Rules() []Rule {
	return []Rule{
		{
			ID:      "device-oom",
			Summary: "device ran out of memory while loading",
			Hint:    "lower n_ctx, quantize the cache with cache_type_k and cache_type_v q8_0, or keep experts on the host with n_cpu_moe",
			Match:   anyOf("out of memory", "failed to allocate", "cudaMalloc failed", "alloc_buffer: allocating", "not enough space in the buffer"),
		},
		{
			ID:      "unknown-arch",
			Summary: "this build does not know architecture ${arch}",
			Hint:    "adopt or build a newer runtime that supports ${arch}",
			Match: func(line string) (map[string]string, bool) {
				arch, ok := quotedAfter(line, "unknown model architecture: '", "'")
				if !ok {
					return nil, false
				}
				return map[string]string{"arch": arch}, true
			},
		},
		{
			ID:      "unknown-pretokenizer",
			Summary: "the tokenizer is newer than this build",
			Hint:    "adopt or build a newer runtime",
			Match:   anyOf("unknown pre-tokenizer type"),
		},
		{
			ID:      "assert",
			Summary: "runtime assertion failed",
			Hint:    "keep the log line and check the runtime issue tracker, then try different params",
			Match: func(line string) (map[string]string, bool) {
				return nil, contains(line, "GGML_ASSERT")
			},
		},
		{
			ID:      "cache-quant-needs-flash",
			Summary: "a quantized V cache needs flash attention",
			Hint:    "set flash_attn on or use cache_type_v f16",
			Fix:     map[string]string{"flash_attn": "on"},
			Match:   anyOf("V cache quantization requires flash_attn", "quantized V cache requires flash attention"),
		},
		{
			ID:      "port-in-use",
			Summary: "the port is taken",
			Hint:    "stop whatever holds the port or run the model again to get a fresh port",
			Match:   anyOf("address already in use", "couldn't bind to server socket", "couldn’t bind to server socket"),
		},
		{
			ID:      "model-load",
			Summary: "the model failed to load",
			Hint:    "verify the store with nebu store verify, then check the lines above this one",
			Match:   anyOf("error loading model", "failed to load model", "unable to load model"),
		},
		{
			ID:      "bad-flag",
			Summary: "this build rejected a flag nebu passed",
			Hint:    "check the line for the flag, override that param with a value this build accepts, or adopt a newer runtime",
			Match: func(line string) (map[string]string, bool) {
				if flag, ok := quotedAfter(line, `error while handling argument "`, `"`); ok {
					return map[string]string{"flag": flag}, true
				}
				_, ok := anyOf("error: invalid argument", "error: unknown argument", "error: unrecognized argument")(line)
				return nil, ok
			},
		},
		{
			ID:      "missing-file",
			Summary: "a file the runtime needs is missing",
			Hint:    "run nebu store verify and pull again",
			Match:   anyOf("no such file or directory", "failed to open"),
		},
	}
}
