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
			Hint:    "check the runtime issue tracker for this error",
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
			Hint:    "free the port or rerun the model to use another port",
			Match:   anyOf("address already in use", "couldn't bind to server socket", "couldn’t bind to server socket"),
		},
		{
			ID:      "gguf-hparams",
			Summary: "this build cannot read the model's hyperparameters${detail}",
			Hint:    "this GGUF targets a different llama.cpp version. Use a compatible runtime or pull an upstream-compatible GGUF, for example from Hugging Face",
			Match: func(line string) (map[string]string, bool) {
				if !contains(line, "error loading model hyperparameters") {
					return nil, false
				}
				names := map[string]string{"detail": ""}
				key, hasKey := quotedAfter(line, "key ", " has wrong array length")
				want, hasWant := quotedAfter(line, "expected ", ",")
				got, hasGot := tailAfter(line, "got ")
				if hasKey && hasWant && hasGot {
					names["detail"] = ", " + key + " holds " + got + " values where it wants " + want
				}
				return names, true
			},
		},
		{
			ID:      "model-load",
			Summary: "the model failed to load",
			Hint:    "run nebu store verify and check the preceding log lines",
			Match:   anyOf("error loading model", "failed to load model", "unable to load model"),
		},
		{
			ID:      "bad-flag",
			Summary: "this build rejected a flag nebu passed",
			Hint:    "set the rejected parameter to a supported value or update the runtime",
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
