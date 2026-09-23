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
			Hint:    "set the missing VAE or encoder path under Model files",
			Match:   anyOf("no text encoder", "no vae", "vae is required", "text encoder is required", "missing text encoder", "missing vae", "clip_vision is required", "requires a clip vision", "t5xxl is required", "llm is required"),
		},
		{
			ID:      "unknown-model",
			Summary: "sd-server recognized none of the model's tensor names",
			Hint:    "sd-server identifies a model by its tensor names, not its config. Pull the checkpoint in the layout stable-diffusion.cpp loads, published at the family's sources, or update the runtime when the family is newer than this build",
			Match:   anyOf("unknown model", "unsupported model", "get sd version from file failed", "cannot identify updated diffusion model", "model type not supported", "unsupported sd version"),
		},
		{
			ID:      "model-load",
			Summary: "the model failed to load",
			Hint:    "run nebu store verify and check the preceding log lines",
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
			Hint:    "set the rejected parameter to a supported value or update the runtime",
			Match:   anyOf("unknown argument", "invalid argument", "unrecognized argument", "error: unknown option", "requires an argument"),
		},
		{
			ID:      "assert",
			Summary: "runtime assertion failed",
			Hint:    "check the runtime issue tracker for this error",
			Match: func(line string) (map[string]string, bool) {
				return nil, contains(line, "GGML_ASSERT")
			},
		},
	}
}
