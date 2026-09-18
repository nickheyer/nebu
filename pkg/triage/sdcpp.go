package triage

// The failures sd-server prints
type SDCpp struct{}

func (SDCpp) ID() string          { return "sdcpp" }
func (SDCpp) Description() string { return "Failure patterns of stable-diffusion.cpp's server" }

func (SDCpp) Rules() []Rule {
	return []Rule{
		{
			ID:      "device-oom",
			Summary: "device ran out of memory while loading or sampling",
			Hint:    "turn offload on to keep the weights in system memory, set a smaller width and height, turn vae_tiling on, or pick a smaller weight_type",
			Fix:     map[string]string{"offload": "on"},
			Match:   anyOf("out of memory", "failed to allocate", "cannot make enough memory available", "alloc params backend buffer failed", "cudaMalloc failed", "not enough space in the buffer", "needs", "is available under current VRAM limits"),
		},
		{
			ID:      "missing-part",
			Summary: "a part of the pipeline was not given",
			Hint:    "set the vae, text encoder, or encoder file the model loads beside itself in the model files params",
			Match:   anyOf("no text encoder", "no vae", "vae is required", "text encoder is required", "missing text encoder", "missing vae", "clip_vision is required", "requires a clip vision", "t5xxl is required", "llm is required"),
		},
		{
			ID:      "unknown-model",
			Summary: "this build cannot tell what model the file holds",
			Hint:    "the file may be a part rather than a checkpoint, or newer than this build; adopt or build a newer runtime, or check the file is a diffusion model",
			Match:   anyOf("unknown model", "unsupported model", "get sd version from file failed", "cannot identify updated diffusion model", "model type not supported", "unsupported sd version"),
		},
		{
			ID:      "model-load",
			Summary: "the model failed to load",
			Hint:    "verify the store with nebu store verify, then check the lines above this one",
			Match:   anyOf("new_sd_ctx_t failed", "load tensors from model loader failed", "init model loader from file failed", "failed to load model", "failed to load", "load weights from file failed"),
		},
		{
			ID:      "missing-file",
			Summary: "a file the runtime needs is missing",
			Hint:    "run nebu store verify and pull again, or point the model files params at files that exist",
			Match:   anyOf("no such file or directory", "failed to open", "cannot open file", "cannot inspect model source"),
		},
		{
			ID:      "bad-flag",
			Summary: "this build rejected a flag nebu passed",
			Hint:    "check the line for the flag, override that param with a value this build accepts, or adopt a newer runtime",
			Match:   anyOf("unknown argument", "invalid argument", "unrecognized argument", "error: unknown option", "requires an argument"),
		},
		{
			ID:      "assert",
			Summary: "runtime assertion failed",
			Hint:    "keep the log line and check the runtime issue tracker, then try different params",
			Match: func(line string) (map[string]string, bool) {
				return nil, contains(line, "GGML_ASSERT")
			},
		},
	}
}
