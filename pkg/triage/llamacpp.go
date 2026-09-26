package triage

import "strings"

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
			ID:      "unknown-tensor-type",
			Summary: "the GGUF uses a tensor type this build does not know${detail}",
			Hint:    "the file was quantized by a fork of llama.cpp with its own types. Run it with the build that made it, or pull a GGUF quantized with upstream types",
			Match: func(line string) (map[string]string, bool) {
				if !contains(line, "invalid ggml type") {
					return nil, false
				}
				names := map[string]string{"detail": ""}
				if tensor, ok := quotedAfter(line, "tensor '", "'"); ok {
					names["detail"] = ": " + tensor
					if kind, ok := quotedAfter(line, "invalid ggml type ", "."); ok {
						names["detail"] += " is type " + kind
					}
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
			ID:      "unknown-device",
			Summary: "this build has no device named ${device}",
			Hint:    "compare it with the names llama-server --list-devices prints for this install, and reprobe the runtime if they differ from its recorded device fact",
			Match: func(line string) (map[string]string, bool) {
				for _, phrase := range []string{"unknown device: ", "invalid device: "} {
					if device, ok := tailAfter(line, phrase); ok && device != "" {
						return map[string]string{"device": device}, true
					}
				}
				return nil, false
			},
		},
		{
			ID:      "bad-flag",
			Summary: "llama.cpp rejected ${flag}",
			Hint:    "${hint}",
			Match: func(line string) (map[string]string, bool) {
				names := map[string]string{
					"flag": "a launch argument",
					"hint": "Check the argument and its value against this runtime's supported options.",
				}
				if flag, ok := quotedAfter(line, `error while handling argument "`, `"`); ok {
					names["flag"] = flag
					if replacement, ok := tailAfter(line, "the argument has been removed. use "); ok && replacement != "" {
						names["hint"] = "Use " + strings.TrimSuffix(replacement, ".") + "."
					}
					return names, true
				}
				_, ok := anyOf("error: invalid argument", "error: unknown argument", "error: unrecognized argument")(line)
				if !ok {
					return nil, false
				}
				for _, word := range strings.Fields(line) {
					word = strings.Trim(word, `"':,.`)
					if strings.HasPrefix(word, "-") {
						names["flag"], _, _ = strings.Cut(word, "=")
						break
					}
				}
				return names, true
			},
		},
		{
			ID:      "missing-file",
			Summary: "a file the runtime needs is missing",
			Hint:    "run nebu store verify and pull again",
			Match:   anyOf("no such file or directory", "failed to open"),
		},
		{
			ID:      "rpc-graph-leak",
			Summary: "CUDA graph execution failed on a worker",
			Hint:    "enable cuda_disable_graphs on the affected worker",
			Fix:     map[string]string{"cuda_disable_graphs": "true"},
			Match:   anyOf("cudaGraphInstantiate", "cudaGraphLaunch", "cudaGraphExecUpdate", "CUDA graph update failed", "graph capture failed", "cudaErrorGraphExecUpdateFailure"),
		},
		{
			ID:      "rpc-share-mmap",
			Summary: "the primary node could not map the model file",
			Hint:    "enable direct_io on the primary node to read model weights without memory mapping",
			Fix:     map[string]string{"direct_io": "true"},
			Match:   anyOf("mmap failed:", "MapViewOfFile failed:", "CreateFileMappingA failed:"),
		},
	}
}
