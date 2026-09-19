package runtimes

import (
	"fmt"
	"math"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nickheyer/nebu/pkg/blueprint"
	"github.com/nickheyer/nebu/pkg/estimate"
	"github.com/nickheyer/nebu/pkg/formats"
	"github.com/nickheyer/nebu/pkg/formats/diffusers"
	"github.com/nickheyer/nebu/pkg/formats/diffusion"
	"github.com/nickheyer/nebu/pkg/host"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/text"
	"github.com/nickheyer/nebu/pkg/triage"
	"google.golang.org/protobuf/proto"
)

// SDCpp serves image and video models through stable-diffusion.cpp. The planner resolves component
// files from the store.
type SDCpp struct{}

func (SDCpp) ID() string   { return "sdcpp" }
func (SDCpp) Name() string { return "stable-diffusion.cpp" }
func (SDCpp) Description() string {
	return "Generates images and video from diffusion checkpoints, safetensors or GGUF, on CPU, CUDA, ROCm, Vulkan, and Metal"
}
func (SDCpp) Formats() []string  { return []string{"gguf", "diffusion", "safetensors", "diffusers"} }
func (SDCpp) Kind() v1.ModelKind { return v1.ModelKind_MODEL_KIND_DIFFUSION }
func (SDCpp) API() v1.ApiFlavor  { return v1.ApiFlavor_API_FLAVOR_SDCPP }
func (SDCpp) Requirements() []string {
	return []string{"at least one probed device"}
}

func (SDCpp) Unmet(h *v1.HostProfile) []string {
	if len(h.GetDevices()) == 0 {
		return []string{"at least one probed device"}
	}
	return nil
}

// Linux CUDA requires a source build. Published Linux GPU builds use Vulkan or ROCm.
func (SDCpp) Methods() []Method {
	linux := func(more func(*v1.HostProfile) bool) func(*v1.HostProfile) bool {
		return func(h *v1.HostProfile) bool { return host.Is(h, "linux", "amd64") && (more == nil || more(h)) }
	}
	windows := func(more func(*v1.HostProfile) bool) func(*v1.HostProfile) bool {
		return func(h *v1.HostProfile) bool { return host.Is(h, "windows", "amd64") && (more == nil || more(h)) }
	}
	amd := func(h *v1.HostProfile) bool { return host.HasVendor(h, "amd") }
	nvidia := func(h *v1.HostProfile) bool { return host.HasVendor(h, "nvidia") }
	asset := func(contains, suffix string) Asset { return Asset{Prefix: "sd-", Contains: contains, Suffix: suffix} }
	return []Method{
		{ID: "adopt", Description: "Use an installed sd-server", Kind: v1.InstallKind_INSTALL_KIND_ADOPTED, Binaries: []string{"sd-server"}},
		{
			ID: "release", Description: "Download sd-server from GitHub releases", Kind: v1.InstallKind_INSTALL_KIND_PREBUILT, Releases: "leejet/stable-diffusion.cpp",
			Rules: []PrebuiltRule{
				{ID: "linux-rocm", Applies: linux(amd), Assets: []Asset{{Prefix: "sd-", Contains: "-x86_64-rocm-", Suffix: ".zip"}}, Binary: "sd-server"},
				{ID: "linux-vulkan", Applies: linux(host.HasGPU), Assets: []Asset{asset("-bin-Linux-Ubuntu-", "-x86_64-vulkan.zip")}, Binary: "sd-server"},
				{ID: "linux-cpu", Applies: linux(nil), Assets: []Asset{asset("-bin-Linux-Ubuntu-", "-x86_64.zip")}, Binary: "sd-server"},
				{ID: "macos-arm64", Applies: func(h *v1.HostProfile) bool { return host.Is(h, "darwin", "arm64") }, Assets: []Asset{asset("-bin-Darwin-", "-arm64.zip")}, Binary: "sd-server"},
				{ID: "windows-cuda12", Applies: windows(nvidia), Assets: []Asset{asset("-bin-win-cuda12-", "-x64.zip"), {Prefix: "cudart-sd-bin-win-cu12", Suffix: "-x64.zip"}}, Binary: "sd-server.exe"},
				{ID: "windows-rocm", Applies: windows(amd), Assets: []Asset{asset("-bin-win-rocm-", "-x64.zip")}, Binary: "sd-server.exe"},
				{ID: "windows-vulkan", Applies: windows(host.HasGPU), Assets: []Asset{asset("-bin-win-vulkan-", "-x64.zip")}, Binary: "sd-server.exe"},
				{ID: "windows-cpu", Applies: windows(nil), Assets: []Asset{asset("-bin-win-cpu-", "-x64.zip")}, Binary: "sd-server.exe"},
			},
		},
		{ID: "source", Description: "Build sd-server for this host", Kind: v1.InstallKind_INSTALL_KIND_BUILT, RecipeID: "sdcpp"},
	}
}

// Maps component parameters to sd-server flags, store kinds, and blueprint slots.
var sdParts = map[string]struct {
	label, flag, picks string
	slots              []string
}{
	"vae":                   {"a VAE", "--vae", "vae", []string{blueprint.SlotVAE}},
	"clip_l":                {"a CLIP-L text encoder", "--clip_l", "clip_l", []string{blueprint.SlotTextEncoderClipL}},
	"clip_g":                {"a CLIP-G text encoder", "--clip_g", "clip_g", []string{blueprint.SlotTextEncoderClipG}},
	"t5xxl":                 {"a T5 text encoder", "--t5xxl", "t5", []string{blueprint.SlotTextEncoderT5, blueprint.SlotTextEncoderGlyph}},
	"llm":                   {"a language model text encoder", "--llm", "llm", []string{blueprint.SlotTextEncoderLLM}},
	"llm_vision":            {"the language model's vision projector", "--llm_vision", "projector", []string{blueprint.SlotTextEncoderVision}},
	"clip_vision":           {"a CLIP vision encoder", "--clip_vision", "clip_vision", []string{blueprint.SlotImageClipVision}},
	"high_noise_model":      {"the high noise half of the denoiser", "--high-noise-diffusion-model", "diffusion", []string{blueprint.SlotDenoiserHighNoise}},
	"uncond_model":          {"the unconditional denoiser", "--uncond-diffusion-model", "diffusion", []string{blueprint.SlotDenoiserUncond}},
	"audio_encoder":         {"an audio encoder", "--audio-encoder", "audio_encoder", []string{blueprint.SlotAudioEncoder}},
	"audio_vae":             {"an audio VAE", "--audio-vae", "audio_vae", []string{blueprint.SlotVAEAudio}},
	"embeddings_connectors": {"the embeddings connectors", "--embeddings-connectors", "embeddings_connectors", []string{blueprint.SlotConnector}},
	"tokenizer":             {"a tokenizer", "--tokenizer", "tokenizer", []string{blueprint.SlotTokenizer}},
}

// Returns the sd-server parameter for a slot, or empty if unsupported.
func sdParam(slot string) string {
	for name, part := range sdParts {
		if slices.Contains(part.slots, slot) {
			return name
		}
	}
	return ""
}

// Adapter directories populated with links to stored models.
var sdDirs = map[string]struct{ label, picks, flag, sub string }{
	"lora_dir":      {"LoRAs", "lora", "--lora-model-dir", "loras"},
	"embd_dir":      {"textual inversion embeddings", "embedding", "--embd-dir", "embeddings"},
	"upscalers_dir": {"highres fix upscalers", "upscaler", "--hires-upscalers-dir", "upscalers"},
}

var (
	sdSamplers   = []string{"euler", "euler_a", "heun", "dpm2", "dpm++2s_a", "dpm++2m", "dpm++2mv2", "ipndm", "ipndm_v", "lcm", "ddim_trailing", "tcd", "res_multistep", "res_2s", "er_sde", "euler_cfg_pp", "euler_a_cfg_pp", "euler_ge", "dpm++2m_sde", "dpm++2m_sde_bt", "lms"}
	sdSchedulers = []string{"discrete", "karras", "exponential", "ays", "gits", "sgm_uniform", "simple", "smoothstep", "kl_optimal", "lcm", "bong_tangent", "ltx2", "logit_normal", "flux2", "flux", "beta"}
	sdTypes      = []string{"", "f32", "f16", "bf16", "q8_0", "q6_K", "q5_K", "q5_1", "q5_0", "q4_K", "q4_1", "q4_0", "q3_K", "q2_K"}
	sdPrediction = []string{"", "eps", "v", "edm_v", "sd3_flow", "flux_flow", "sefi_flow", "minit2i_flow", "sensenova_u1_flow"}
	sdRNG        = []string{"std_default", "cuda", "cpu"}
	sdCacheModes = []string{"", "disabled", "easycache", "ucache", "dbcache", "taylorseer", "cache-dit", "spectrum"}
	sdUpscalers  = []string{"Latent", "Lanczos", "Nearest", "Latent (nearest)", "Latent (nearest-exact)", "Latent (antialiased)", "Latent (bicubic)", "Latent (bicubic antialiased)"}
)

const (
	groupFiles       = "Model files"
	groupAdapters    = "Adapters and helpers"
	groupPlacement   = "Placement"
	groupCompute     = "Compute"
	groupDefaults    = "Generation"
	groupGuidance    = "Guidance"
	groupVideo       = "Video"
	groupHighNoise   = "High noise"
	groupVAE         = "VAE decoding"
	groupHires       = "Highres fix"
	groupUpscale     = "Upscaling"
	groupCache       = "Caching"
	groupDetailer    = "ADetailer"
	groupDiagnostics = "Diagnostics"
)

func (SDCpp) Params() []*v1.Param {
	pathParam := func(name, label, group, flag, picks string, advanced bool) *v1.Param {
		return &v1.Param{Name: name, Label: label, Type: v1.ParamType_PARAM_TYPE_PATH, Group: group, Flag: flag, Picks: picks, Advanced: advanced}
	}
	solved := func(name, label string) *v1.Param {
		part := sdParts[name]
		return &v1.Param{Name: name, Label: label, Type: v1.ParamType_PARAM_TYPE_PATH, Default: Auto, Solved: true, Group: groupFiles, Flag: part.flag, Picks: part.picks,
			Rule: strings.TrimPrefix(strings.TrimPrefix(part.label, "a "), "an ") + " from the store, preferring the model repository, then blueprint sources, then matching names"}
	}
	dir := func(name, label string) *v1.Param {
		d := sdDirs[name]
		return &v1.Param{Name: name, Label: label, Type: v1.ParamType_PARAM_TYPE_PATH, Default: Auto, Solved: true, Group: groupAdapters, Flag: d.flag, Picks: d.picks, Advanced: true,
			Rule: "stored " + d.label + ", available to requests by file name"}
	}
	intParam := func(name, label, group, flag, def, unit, description string, min, max, step float64, advanced bool) *v1.Param {
		return &v1.Param{Name: name, Label: label, Type: v1.ParamType_PARAM_TYPE_INT, Default: def, Unit: unit, Min: min, Max: max, Step: step, Group: group, Flag: flag, Advanced: advanced, Description: description}
	}
	floatParam := func(name, label, group, flag, def, description string, min, max, step float64, advanced bool) *v1.Param {
		return &v1.Param{Name: name, Label: label, Type: v1.ParamType_PARAM_TYPE_FLOAT, Default: def, Min: min, Max: max, Step: step, Group: group, Flag: flag, Advanced: advanced, Description: description}
	}
	boolParam := func(name, label, group, flag string, advanced bool) *v1.Param {
		return &v1.Param{Name: name, Label: label, Type: v1.ParamType_PARAM_TYPE_BOOL, Default: "false", Group: group, Flag: flag, Advanced: advanced}
	}
	strParam := func(name, label, group, flag, def, description string, choices []string, advanced bool) *v1.Param {
		return &v1.Param{Name: name, Label: label, Type: v1.ParamType_PARAM_TYPE_STRING, Default: def, Choices: choices, Group: group, Flag: flag, Advanced: advanced, Description: description}
	}
	// Sampling defaults resolved from the model family.
	sampling := func(name, label, group, flag string, typ v1.ParamType, unit string, choices []string, min, max, step float64, rule string) *v1.Param {
		return &v1.Param{Name: name, Label: label, Type: typ, Default: Auto, Solved: true, Unit: unit, Choices: choices, Min: min, Max: max, Step: step, Group: group, Flag: flag, Rule: rule}
	}
	pairs := "key=value, comma separated"
	return []*v1.Param{
		// Model files.
		solved("vae", "VAE"),
		solved("t5xxl", "T5 text encoder"),
		solved("clip_l", "CLIP-L text encoder"),
		solved("clip_g", "CLIP-G text encoder"),
		solved("llm", "Language model text encoder"),
		solved("llm_vision", "Language model vision projector"),
		solved("clip_vision", "CLIP vision encoder"),
		solved("high_noise_model", "High noise denoiser"),
		solved("uncond_model", "Unconditional denoiser"),
		solved("audio_encoder", "Audio encoder"),
		solved("audio_vae", "Audio VAE"),
		solved("embeddings_connectors", "Embeddings connectors"),
		solved("tokenizer", "Tokenizer"),

		// Adapters and helpers.
		dir("lora_dir", "LoRA directory"),
		dir("embd_dir", "Embeddings directory"),
		dir("upscalers_dir", "Upscalers directory"),
		pathParam("taesd", "Tiny autoencoder", groupAdapters, "--taesd", "taesd", false),
		pathParam("control_net", "ControlNet", groupAdapters, "--control-net", "controlnet", false),
		pathParam("ip_adapter", "IP-Adapter", groupAdapters, "--ip-adapter", "ip_adapter", false),
		pathParam("photo_maker", "PhotoMaker", groupAdapters, "--photo-maker", "photo_maker", true),
		pathParam("pulid_weights", "PuLID weights", groupAdapters, "--pulid-weights", "pulid", true),
		pathParam("motion_module", "AnimateDiff motion module", groupAdapters, "--motion-module", "motion_module", true),
		pathParam("upscale_model", "ESRGAN upscaler", groupAdapters, "--upscale-model", "upscaler", false),

		// Memory placement.
		{Name: "on_device", Label: "Parts on device", Type: v1.ParamType_PARAM_TYPE_INT, Default: Auto, Solved: true, Unit: "parts", Min: 0, Step: 1, Group: groupPlacement,
			Rule: "fit components on device in order: denoiser, VAE, text encoders"},
		strParam("offload", "Offload to system memory", groupPlacement, "", "auto", "", []string{"auto", "on", "off"}, false),
		strParam("backend", "Compute backend", groupPlacement, "--backend", "", "cpu, cuda0, or diffusion=cuda0,te=cpu", nil, true),
		strParam("params_backend", "Weights backend", groupPlacement, "--params-backend", "", "disk, cpu, or diffusion=disk,te=cpu", nil, true),
		strParam("max_vram", "Device memory budget", groupPlacement, "--max-vram", "", "GiB, or cuda0=6,vulkan0=4", nil, true),
		intParam("threads", "CPU threads", groupPlacement, "--threads", "-1", "threads", "-1 for every physical core", -1, 0, 1, true),
		strParam("split_mode", "Split mode", groupPlacement, "--split-mode", "", "layer, row, or diffusion=row,te=layer", nil, true),
		strParam("rpc_servers", "RPC servers", groupPlacement, "--rpc-servers", "", "host:port, comma separated", nil, true),
		boolParam("mmap", "Memory map weights", groupPlacement, "--mmap", true),
		boolParam("eager_load", "Load every part at start", groupPlacement, "--eager-load", true),
		strParam("auto_fit", "Automatic fit", groupPlacement, "--auto-fit", "on", "", []string{"on", "off"}, true),
		boolParam("disable_prefetch", "Disable weight prefetch", groupPlacement, "--disable-prefetch", true),
		boolParam("disable_segmented_compute", "Disable segmented compute", groupPlacement, "--disable-segmented-compute", true),

		// Compute settings.
		{Name: "diffusion_fa", Label: "Flash attention in the denoiser", Type: v1.ParamType_PARAM_TYPE_BOOL, Default: "true", Group: groupCompute, Flag: "--diffusion-fa"},
		boolParam("fa", "Flash attention everywhere", groupCompute, "--fa", true),
		strParam("weight_type", "Weight type", groupCompute, "--type", "", "", sdTypes, false),
		strParam("tensor_type_rules", "Tensor type rules", groupCompute, "--tensor-type-rules", "", `^vae\.=f16,model\.=q8_0`, nil, true),
		strParam("vae_format", "VAE latent format", groupCompute, "--vae-format", "auto", "", []string{"auto", "flux", "sd3", "flux2", "wan"}, true),
		boolParam("diffusion_conv_direct", "Direct convolution in the denoiser", groupCompute, "--diffusion-conv-direct", true),
		boolParam("vae_conv_direct", "Direct convolution in the VAE", groupCompute, "--vae-conv-direct", true),
		boolParam("force_sdxl_vae_conv_scale", "Force SDXL VAE conv scale", groupCompute, "--force-sdxl-vae-conv-scale", true),
		strParam("model_args", "Model arguments", groupCompute, "--model-args", "", pairs, nil, true),
		strParam("lora_apply_mode", "LoRA apply mode", groupCompute, "--lora-apply-mode", "auto", "", []string{"auto", "immediately", "at_runtime"}, true),
		strParam("rng", "Random number generator", groupCompute, "--rng", "cuda", "", sdRNG, true),
		strParam("sampler_rng", "Sampler random number generator", groupCompute, "--sampler-rng", "", "", append([]string{""}, sdRNG...), true),
		strParam("prediction", "Prediction type", groupCompute, "--prediction", "", "", sdPrediction, true),
		floatParam("linear_scale", "Linear input scale", groupCompute, "--linear-scale", "0", "0 uses the model default", 0, 0, 0.05, true),
		floatParam("attn_scale", "Attention K/V scale", groupCompute, "--attn-scale", "0", "0 uses the model default", 0, 0, 0.05, true),

		// Generation defaults.
		intParam("width", "Width", groupDefaults, "--width", "1024", "px", "", 64, 4096, 16, false),
		intParam("height", "Height", groupDefaults, "--height", "1024", "px", "", 64, 4096, 16, false),
		sampling("steps", "Steps", groupDefaults, "--steps", v1.ParamType_PARAM_TYPE_INT, "steps", nil, 1, 150, 1,
			"family step count, reduced for distilled variants such as Turbo, schnell, and Lightning"),
		sampling("sampling_method", "Sampler", groupDefaults, "--sampling-method", v1.ParamType_PARAM_TYPE_STRING, "", sdSamplers, 0, 0, 0,
			"euler for transformers, lcm for PiD, euler_a for UNets"),
		sampling("scheduler", "Scheduler", groupDefaults, "--scheduler", v1.ParamType_PARAM_TYPE_STRING, "", sdSchedulers, 0, 0, 0,
			"family scheduler: flux, flux2, ltx2, logit_normal, lcm, or discrete"),
		intParam("seed", "Seed", groupDefaults, "--seed", "-1", "", "-1 for random", -1, 0, 1, true),
		intParam("batch_count", "Images per request", groupDefaults, "--batch-count", "1", "images", "", 1, 64, 1, true),
		intParam("clip_skip", "CLIP skip", groupDefaults, "--clip-skip", "-1", "", "-1 uses the family default", -1, 12, 1, true),
		floatParam("strength", "Strength", groupDefaults, "--strength", "0.75", "", 0, 1, 0.05, true),
		strParam("negative_prompt", "Negative prompt", groupDefaults, "--negative-prompt", "", "", nil, true),
		strParam("sigmas", "Custom sigmas", groupDefaults, "--sigmas", "", "14.61,7.8,3.5,0.0", nil, true),
		intParam("timestep_shift", "Timestep shift", groupDefaults, "--timestep-shift", "0", "", "", 0, 1000, 1, true),
		intParam("qwen_image_layers", "Qwen Image layers", groupDefaults, "--qwen-image-layers", "3", "layers", "", 1, 16, 1, true),
		floatParam("control_strength", "ControlNet strength", groupDefaults, "--control-strength", "0.9", "", 0, 1, 0.05, true),
		floatParam("ip_adapter_strength", "IP-Adapter strength", groupDefaults, "--ip-adapter-strength", "1", "", 0, 2, 0.05, true),
		floatParam("pm_style_strength", "PhotoMaker style strength", groupDefaults, "--pm-style-strength", "20", "", 0, 100, 1, true),
		floatParam("pulid_id_weight", "PuLID identity weight", groupDefaults, "--pulid-id-weight", "1", "", 0, 2, 0.05, true),
		strParam("extra_sample_args", "Extra sampler arguments", groupDefaults, "--extra-sample-args", "", pairs, nil, true),
		strParam("ref_image_args", "Reference image arguments", groupDefaults, "--ref-image-args", "", pairs, nil, true),
		boolParam("increase_ref_index", "Number reference images", groupDefaults, "--increase-ref-index", true),
		boolParam("disable_auto_resize_ref_image", "Keep reference image size", groupDefaults, "--disable-auto-resize-ref-image", true),
		boolParam("circular", "Circular padding", groupDefaults, "--circular", true),
		boolParam("circularx", "Circular padding on x", groupDefaults, "--circularx", true),
		boolParam("circulary", "Circular padding on y", groupDefaults, "--circulary", true),
		boolParam("disable_image_metadata", "Leave metadata out of images", groupDefaults, "--disable-image-metadata", true),

		// Guidance
		sampling("cfg_scale", "Guidance scale", groupGuidance, "--cfg-scale", v1.ParamType_PARAM_TYPE_FLOAT, "", nil, 0, 30, 0.5,
			"documented family guidance, or 1 for distilled models without negative prompts"),
		floatParam("img_cfg_scale", "Image guidance scale", groupGuidance, "--img-cfg-scale", "0", "0 matches the guidance scale", 0, 30, 0.5, true),
		sampling("guidance", "Distilled guidance", groupGuidance, "--guidance", v1.ParamType_PARAM_TYPE_FLOAT, "", nil, 0, 30, 0.5,
			"4 for FLUX.2, 3.5 for every other family with a guidance embedding"),
		floatParam("slg_scale", "Skip layer guidance scale", groupGuidance, "--slg-scale", "0", "0 off", 0, 10, 0.5, true),
		strParam("skip_layers", "Skipped layers", groupGuidance, "--skip-layers", "", "7,8,9", nil, true),
		floatParam("skip_layer_start", "Skip layer start", groupGuidance, "--skip-layer-start", "0.01", "", 0, 1, 0.01, true),
		floatParam("skip_layer_end", "Skip layer end", groupGuidance, "--skip-layer-end", "0.2", "", 0, 1, 0.01, true),
		floatParam("eta", "Eta", groupGuidance, "--eta", "0", "0 uses the sampler default", 0, 2, 0.05, true),
		sampling("flow_shift", "Flow shift", groupGuidance, "--flow-shift", v1.ParamType_PARAM_TYPE_FLOAT, "", nil, 0, 20, 0.05,
			"family flow shift: 5 for Wan, 1.15 for FLUX and Krea 2, 3 for Qwen Image, otherwise 0"),

		// Video
		intParam("video_frames", "Video frames", groupVideo, "--video-frames", "1", "frames", "4n+1", 1, 401, 4, false),
		intParam("fps", "Frame rate", groupVideo, "--fps", "16", "fps", "", 1, 60, 1, false),
		floatParam("moe_boundary", "MoE boundary", groupVideo, "--moe-boundary", "0.875", "", 0, 1, 0.005, true),
		floatParam("vace_strength", "VACE strength", groupVideo, "--vace-strength", "1", "", 0, 2, 0.05, true),

		// High noise defaults, the first stage of a Wan 2.2 A14B pair
		intParam("high_noise_steps", "High noise steps", groupHighNoise, "--high-noise-steps", "-1", "steps", "-1 splits at the MoE boundary", -1, 150, 1, true),
		floatParam("high_noise_cfg_scale", "High noise guidance scale", groupHighNoise, "--high-noise-cfg-scale", "7", "", 0, 30, 0.5, true),
		floatParam("high_noise_img_cfg_scale", "High noise image guidance scale", groupHighNoise, "--high-noise-img-cfg-scale", "0", "0 matches the guidance scale", 0, 30, 0.5, true),
		floatParam("high_noise_guidance", "High noise distilled guidance", groupHighNoise, "--high-noise-guidance", "3.5", "", 0, 30, 0.5, true),
		strParam("high_noise_sampling_method", "High noise sampler", groupHighNoise, "--high-noise-sampling-method", "", "", append([]string{""}, sdSamplers...), true),
		floatParam("high_noise_slg_scale", "High noise skip layer guidance scale", groupHighNoise, "--high-noise-slg-scale", "0", "0 off", 0, 10, 0.5, true),
		strParam("high_noise_skip_layers", "High noise skipped layers", groupHighNoise, "--high-noise-skip-layers", "", "7,8,9", nil, true),
		floatParam("high_noise_skip_layer_start", "High noise skip layer start", groupHighNoise, "--high-noise-skip-layer-start", "0.01", "", 0, 1, 0.01, true),
		floatParam("high_noise_skip_layer_end", "High noise skip layer end", groupHighNoise, "--high-noise-skip-layer-end", "0.2", "", 0, 1, 0.01, true),
		floatParam("high_noise_eta", "High noise eta", groupHighNoise, "--high-noise-eta", "0", "0 uses the sampler default", 0, 2, 0.05, true),

		// VAE decoding
		boolParam("vae_tiling", "VAE tiling", groupVAE, "--vae-tiling", false),
		boolParam("temporal_tiling", "Temporal tiling", groupVAE, "--temporal-tiling", true),
		strParam("vae_tile_size", "VAE tile size", groupVAE, "--vae-tile-size", "", "32x32", nil, true),
		strParam("vae_relative_tile_size", "VAE relative tile size", groupVAE, "--vae-relative-tile-size", "", "0.5x0.5", nil, true),
		floatParam("vae_tile_overlap", "VAE tile overlap", groupVAE, "--vae-tile-overlap", "0.5", "", 0, 1, 0.05, true),
		strParam("extra_tiling_args", "Extra tiling arguments", groupVAE, "--extra-tiling-args", "", pairs, nil, true),

		// Highres fix
		boolParam("hires", "Highres fix", groupHires, "--hires", true),
		{Name: "hires_upscaler", Label: "Highres upscaler", Type: v1.ParamType_PARAM_TYPE_STRING, Default: "Latent", Choices: sdUpscalers, Picks: "upscaler", Group: groupHires, Flag: "--hires-upscaler", Advanced: true},
		floatParam("hires_scale", "Highres scale", groupHires, "--hires-scale", "2", "", 1, 4, 0.25, true),
		intParam("hires_width", "Highres width", groupHires, "--hires-width", "0", "px", "0 uses the scale", 0, 8192, 16, true),
		intParam("hires_height", "Highres height", groupHires, "--hires-height", "0", "px", "0 uses the scale", 0, 8192, 16, true),
		intParam("hires_steps", "Highres steps", groupHires, "--hires-steps", "0", "steps", "0 uses the main steps", 0, 150, 1, true),
		floatParam("hires_denoising_strength", "Highres denoising strength", groupHires, "--hires-denoising-strength", "0.7", "", 0, 1, 0.05, true),
		strParam("hires_sigmas", "Highres sigmas", groupHires, "--hires-sigmas", "", "", nil, true),
		intParam("hires_upscale_tile_size", "Highres upscale tile size", groupHires, "--hires-upscale-tile-size", "128", "px", "", 32, 1024, 32, true),

		// Upscaling with ESRGAN after generation
		intParam("upscale_repeats", "Upscale repeats", groupUpscale, "--upscale-repeats", "1", "times", "", 1, 4, 1, true),
		intParam("upscale_tile_size", "Upscale tile size", groupUpscale, "--upscale-tile-size", "128", "px", "", 32, 1024, 32, true),

		// Caching across steps
		strParam("cache_mode", "Cache mode", groupCache, "--cache-mode", "", "", sdCacheModes, false),
		strParam("cache_option", "Cache options", groupCache, "--cache-option", "", pairs, nil, true),
		strParam("scm_mask", "SCM steps mask", groupCache, "--scm-mask", "", "1,1,0,1,0", nil, true),
		strParam("scm_policy", "SCM policy", groupCache, "--scm-policy", "dynamic", "", []string{"dynamic", "static"}, true),

		// ADetailer second pass.
		pathParam("ad_model", "ADetailer detector", groupDetailer, "--ad-model", "detector", true),
		strParam("ad_prompt", "ADetailer prompt", groupDetailer, "--ad-prompt", "", "[PROMPT], [SEP], [SKIP]", nil, true),
		strParam("ad_negative_prompt", "ADetailer negative prompt", groupDetailer, "--ad-negative-prompt", "", "", nil, true),
		strParam("extra_ad_args", "Extra ADetailer arguments", groupDetailer, "--extra-ad-args", "", pairs, nil, true),

		// Diagnostics
		strParam("log_level", "Log level", groupDiagnostics, "--log-level", "debug", "", []string{"debug", "verbose", "info", "warn", "error"}, true),
		boolParam("color", "Colored log", groupDiagnostics, "--color", true),
	}
}

// Bundled checkpoints use --model. Standalone denoisers use --diffusion-model.
func bundled(d *v1.Descriptor) bool {
	p := diffusion.ProfileOf(d)
	return p.VAE || p.TextEncoder
}

// Counts device-eligible pipeline components, including resolved companions.
func sdPartCount(d *v1.Descriptor) int {
	n := 0
	for _, g := range d.GetGroups() {
		switch g.GetKind() {
		case v1.TensorGroupKind_TENSOR_GROUP_KIND_DIFFUSION, v1.TensorGroupKind_TENSOR_GROUP_KIND_VAE, v1.TensorGroupKind_TENSOR_GROUP_KIND_TEXT_ENCODER:
			n++
		}
	}
	return n
}

func (r SDCpp) Launch(in Launch) (*Command, error) {
	weights := in.Artifacts["weights"]
	if weights == "" {
		return nil, fmt.Errorf("%w: the stored group has no weights file", ErrParam)
	}
	if in.Descriptor.GetKind() == v1.ModelKind_MODEL_KIND_COMPONENT {
		return nil, fmt.Errorf("%w: %s is %s, a part loaded beside a diffusion model rather than one served on its own", ErrParam, in.Name, diffusion.Describe(in.Descriptor.GetArchitecture()))
	}
	p := in.Params.Clone()
	args := []string{"--listen-ip", in.Host, "--listen-port", strconv.Itoa(in.Port)}
	if diffusers.Pipeline(in.Descriptor) {
		// Resolve auto file parameters from declared pipeline subfolders.
		denoiser, err := sdPipeline(in, p)
		if err != nil {
			return nil, err
		}
		args = append(args, "--diffusion-model", denoiser)
	} else {
		// Load sharded checkpoints through their index.
		if _, _, count, ok := formats.Shard(strings.TrimSuffix(path.Base(weights), path.Ext(weights))); ok && count > 1 {
			index := in.Artifacts[text.Enum(v1.ArtifactRole_ARTIFACT_ROLE_INDEX)]
			if index == "" {
				return nil, fmt.Errorf("%w: no index for this group's %d shards. Pull a single checkpoint file or GGUF", ErrParam, count)
			}
			weights = index
		}
		if bundled(in.Descriptor) {
			args = append(args, "--model", weights)
		} else {
			args = append(args, "--diffusion-model", weights)
		}
	}
	// Create adapter links under the prepared directory.
	for name, d := range sdDirs {
		if !p.IsAuto(name) {
			continue
		}
		dir, err := linkDir(in.Artifacts["prepared_dir"], d.sub, in.Stored, d.picks)
		if err != nil {
			return nil, err
		}
		p[name] = dir
	}
	if msg := sdUpscalerProblem(p.Str("hires_upscaler"), in.Stored); msg != "" {
		return nil, fmt.Errorf("%w: %s", ErrParam, msg)
	}
	// Enable offload when the plan or on_device leaves components in host memory.
	offload := p.Str("offload") == "on"
	if p.Str("offload") == "auto" {
		offload = planOffloads(in.Plan) || (!p.IsAuto("on_device") && int(p.Int("on_device")) < sdPipelineParts(in.Plan, in.Descriptor))
	}
	p["offload"] = ""
	hostOnly := in.Placement == v1.Placement_PLACEMENT_HOST
	if hostOnly {
		switch p.Str("backend") {
		case "", "cpu":
			p["backend"] = "cpu"
		default:
			return nil, fmt.Errorf("%w: backend %s names an accelerator, but the slot keeps the model in host memory", ErrParam, p.Str("backend"))
		}
	}
	flags, env, emitted := Flags(r.Params(), p)
	if offload {
		flags = append(flags, "--offload-to-cpu")
		emitted["offload"] = "on"
	} else {
		emitted["offload"] = "off"
	}
	if hostOnly {
		hideDevices(env)
	} else {
		setEnv(env, "CUDA_VISIBLE_DEVICES", visible(in.Devices, "nvidia", false))
		setEnv(env, "ROCR_VISIBLE_DEVICES", visible(in.Devices, "amd", true))
		setEnv(env, "GGML_VK_VISIBLE_DEVICES", visible(in.Devices, "", true))
	}
	return &Command{Command: in.Install.Path, Args: append(args, flags...), Env: env, Params: emitted}, nil
}

// Resolves pipeline component paths and returns the denoiser path. Sharded components use index
// files. Embedded vision towers load with their text encoder.
func sdPipeline(in Launch, p estimate.Params) (string, error) {
	if in.Model == nil {
		return "", fmt.Errorf("%w: the run has no stored model to read the pipeline's files from", ErrParam)
	}
	d := in.Descriptor
	fills := map[string]bool{}
	for _, fill := range blueprint.Fills(diffusion.ProfileOf(d), d.GetGroup()) {
		fills[fill.Slot] = true
	}
	file := func(slot, sub string) (string, error) {
		weights, index, tokenizer := diffusers.Files(in.Model, sub)
		switch {
		case slot == blueprint.SlotTokenizer:
			if tokenizer == "" {
				return "", fmt.Errorf("%w: %s holds no tokenizer.json", ErrParam, sub)
			}
			return tokenizer, nil
		case len(weights) == 0:
			return "", fmt.Errorf("%w: %s holds no weights", ErrParam, sub)
		case len(weights) > 1 && index == "":
			return "", fmt.Errorf("%w: %s is split into %d shards and holds no index naming them", ErrParam, sub, len(weights))
		case len(weights) > 1:
			return index, nil
		}
		return weights[0], nil
	}
	declared := diffusers.Slots(d)
	slots := make([]string, 0, len(declared))
	for slot := range declared {
		slots = append(slots, slot)
	}
	sort.Strings(slots)
	var denoiser string
	for _, slot := range slots {
		sub := declared[slot]
		switch slot {
		case blueprint.SlotDenoiser:
			var err error
			if denoiser, err = file(slot, sub); err != nil {
				return "", err
			}
			continue
		case blueprint.SlotDenoiserHighNoise, blueprint.SlotDenoiserUncond:
		case blueprint.SlotTextEncoderVision:
			if sub == declared[blueprint.SlotTextEncoderLLM] {
				continue
			}
		default:
			if !fills[slot] {
				continue
			}
		}
		param := sdParam(slot)
		if param == "" || !p.IsAuto(param) {
			continue
		}
		path, err := file(slot, sub)
		if err != nil {
			return "", err
		}
		p[param] = path
	}
	if denoiser == "" {
		return "", fmt.Errorf("%w: the pipeline declares no denoiser", ErrParam)
	}
	return denoiser, nil
}

// Rebuilds links to stored adapters, named for request lookup.
func linkDir(prepared, sub string, stored []*v1.StoredModel, picks string) (string, error) {
	if prepared == "" {
		return "", fmt.Errorf("%w: the store gave no prepared directory to lay the %s links out in", ErrParam, sub)
	}
	dir := filepath.Join(prepared, sub)
	if err := os.RemoveAll(dir); err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	for _, m := range stored {
		if diffusion.PartOf(m.GetDescriptor_()) != picks || m.GetDescriptor_().GetKind() != v1.ModelKind_MODEL_KIND_COMPONENT {
			continue
		}
		file := companionWeights(m)
		if file == "" {
			continue
		}
		if err := os.Symlink(file, filepath.Join(dir, linkName(m)+filepath.Ext(file))); err != nil {
			return "", err
		}
	}
	return dir, nil
}

// Uses the adapter group name, or the repository name for root-level default groups.
func linkName(m *v1.StoredModel) string {
	name := m.GetGroup()
	if name == "default" || name == "" {
		name = path.Base(m.GetRepo())
	}
	return strings.ReplaceAll(name, "/", "_")
}

// Returns pipeline components from the plan, falling back to descriptor groups.
func sdPipelineParts(plan *v1.MemoryPlan, d *v1.Descriptor) int {
	if plan == nil {
		return sdPartCount(d)
	}
	n := 0
	for _, pl := range plan.GetPlacements() {
		switch pl.GetKind() {
		case v1.TensorGroupKind_TENSOR_GROUP_KIND_DIFFUSION, v1.TensorGroupKind_TENSOR_GROUP_KIND_VAE, v1.TensorGroupKind_TENSOR_GROUP_KIND_TEXT_ENCODER:
			n += int(pl.GetCount())
		}
	}
	return n
}

// Reports whether any pipeline component uses host memory.
func planOffloads(plan *v1.MemoryPlan) bool {
	host := text.Enum(v1.PoolKind_POOL_KIND_HOST)
	for _, pl := range plan.GetPlacements() {
		switch pl.GetKind() {
		case v1.TensorGroupKind_TENSOR_GROUP_KIND_DIFFUSION, v1.TensorGroupKind_TENSOR_GROUP_KIND_VAE, v1.TensorGroupKind_TENSOR_GROUP_KIND_TEXT_ENCODER:
			if pl.GetPoolId() == host && pl.GetBytes() > 0 {
				return true
			}
		}
	}
	return false
}

func (SDCpp) Prepares(string) bool             { return false }
func (SDCpp) Prepare(Launch) (*Command, error) { return nil, nil }
func (SDCpp) PrepareTimeout() time.Duration    { return 0 }

// The capabilities endpoint responds after all components load.
func (SDCpp) Health() Health {
	return Health{Path: "/sdcpp/v1/capabilities", Interval: 2 * time.Second, Timeout: 30 * time.Minute}
}
func (SDCpp) StopGrace() time.Duration { return 20 * time.Second }
func (SDCpp) Triage() []triage.Set     { return []triage.Set{triage.SDCpp{}} }

func (SDCpp) Probes() []Probe {
	return []Probe{
		{Key: "version", Args: []string{"--version"}, Parse: func(out string) (string, bool) {
			// stable-diffusion.cpp version unknown, commit cc515a0
			line := strings.TrimSpace(strings.SplitN(out, "\n", 2)[0])
			if i := strings.Index(line, "version "); i >= 0 {
				v := strings.TrimRight(strings.Fields(line[i+len("version "):])[0], ",")
				if v != "" && v != "unknown" {
					return v, true
				}
			}
			if i := strings.Index(line, "commit "); i >= 0 {
				if f := strings.Fields(line[i+len("commit "):]); len(f) > 0 {
					return f[0], true
				}
			}
			return "", false
		}},
		{Key: "devices", Args: []string{"--list-devices"}, Parse: func(out string) (string, bool) {
			var names []string
			for _, line := range strings.Split(out, "\n") {
				// Device name and description are separated by a tab.
				name, _, ok := strings.Cut(line, "\t")
				if ok && name != "" && !strings.ContainsAny(name, " :") {
					names = append(names, name)
				}
			}
			return strings.Join(names, ","), len(names) > 0
		}},
	}
}

// sd-server logs the weights once loaded, split between device and host memory, and one compute buffer per part on the device it ran on:
//
//	total params memory size = 6702.86MB (VRAM 6702.86MB, RAM 0.00MB): text_encoders 1595.65MB(VRAM), diffusion_model 4947.47MB(VRAM), vae 159.68MB(VRAM), ...
//	flux compute buffer size: 650.00 MB(VRAM) on CUDA0 (peak across 1 segment)
func (SDCpp) Measure(lines []string) []*v1.Measurement {
	var m measurements
	for _, line := range lines {
		if strings.Contains(line, "total params memory size") {
			if n, ok := sdBytesAfter(line, "(VRAM "); ok && n > 0 {
				m.add("device.weights", n, line)
			}
			if n, ok := sdBytesAfter(line, ", RAM "); ok && n > 0 {
				m.add("host.weights", n, line)
			}
			continue
		}
		if i := strings.Index(line, " compute buffer size: "); i >= 0 {
			n, ok := sdBytesAfter(line, " compute buffer size: ")
			if !ok {
				continue
			}
			side := "device"
			if strings.Contains(line, "(RAM)") || strings.Contains(line, " on CPU") {
				side = "host"
			}
			m.add(side+".compute", n, line)
		}
	}
	return m.list
}

// Parses byte counts such as 1234.56 MB(VRAM) and 6702.86MB.
func sdBytesAfter(line, phrase string) (uint64, bool) {
	i := strings.Index(line, phrase)
	if i < 0 {
		return 0, false
	}
	rest := strings.TrimSpace(line[i+len(phrase):])
	end := 0
	for end < len(rest) && (rest[end] >= '0' && rest[end] <= '9' || rest[end] == '.') {
		end++
	}
	n, err := strconv.ParseFloat(rest[:end], 64)
	if err != nil {
		return 0, false
	}
	unit := strings.ToLower(strings.TrimSpace(rest[end:]))
	if j := strings.IndexAny(unit, "(),: "); j >= 0 {
		unit = unit[:j]
	}
	mult := map[string]float64{"b": 1, "kb": 1 << 10, "kib": 1 << 10, "mb": 1 << 20, "mib": 1 << 20, "gb": 1 << 30, "gib": 1 << 30}[unit]
	if mult == 0 {
		return 0, false
	}
	return uint64(n * mult), true
}

// Returns the first stored weights path.
func companionWeights(c *v1.StoredModel) string {
	for _, sa := range c.GetArtifacts() {
		if sa.GetArtifact().GetRole() == v1.ArtifactRole_ARTIFACT_ROLE_WEIGHTS {
			return sa.GetPath()
		}
	}
	return ""
}

// Returns the first stored path with the role, or empty.
func companionFile(c *v1.StoredModel, role v1.ArtifactRole) string {
	for _, sa := range c.GetArtifacts() {
		if sa.GetArtifact().GetRole() == role {
			return sa.GetPath()
		}
	}
	return ""
}

// Returns the stored tokenizer.json path.
func companionTokenizer(c *v1.StoredModel) string {
	for _, sa := range c.GetArtifacts() {
		if sa.GetArtifact().GetRole() == v1.ArtifactRole_ARTIFACT_ROLE_TOKENIZER && path.Base(sa.GetArtifact().GetPath()) == "tokenizer.json" {
			return sa.GetPath()
		}
	}
	return ""
}

// Converts a stored model to a blueprint candidate.
func candidate(c *v1.StoredModel) blueprint.Candidate {
	out := blueprint.Candidate{Repo: c.GetRepo(), Group: c.GetGroup(), Roles: map[v1.ArtifactRole]bool{}, Descriptor: c.GetDescriptor_()}
	for _, sa := range c.GetArtifacts() {
		out.Paths = append(out.Paths, sa.GetArtifact().GetPath())
		out.Roles[sa.GetArtifact().GetRole()] = true
	}
	return out
}

// Selects projector, tokenizer.json, or weights according to the slot.
func companionPath(slot string, c *v1.StoredModel) string {
	switch slot {
	case blueprint.SlotTextEncoderVision:
		return companionFile(c, v1.ArtifactRole_ARTIFACT_ROLE_PROJECTOR)
	case blueprint.SlotTokenizer:
		return companionTokenizer(c)
	}
	return companionWeights(c)
}

// Maps component parameters to memory planning kinds.
var sdPartKinds = map[string]v1.TensorGroupKind{
	"vae": v1.TensorGroupKind_TENSOR_GROUP_KIND_VAE, "audio_vae": v1.TensorGroupKind_TENSOR_GROUP_KIND_VAE,
	"clip_l": v1.TensorGroupKind_TENSOR_GROUP_KIND_TEXT_ENCODER, "clip_g": v1.TensorGroupKind_TENSOR_GROUP_KIND_TEXT_ENCODER, "t5xxl": v1.TensorGroupKind_TENSOR_GROUP_KIND_TEXT_ENCODER, "llm": v1.TensorGroupKind_TENSOR_GROUP_KIND_TEXT_ENCODER, "embeddings_connectors": v1.TensorGroupKind_TENSOR_GROUP_KIND_TEXT_ENCODER,
	"llm_vision": v1.TensorGroupKind_TENSOR_GROUP_KIND_VISION, "clip_vision": v1.TensorGroupKind_TENSOR_GROUP_KIND_VISION,
	"audio_encoder":    v1.TensorGroupKind_TENSOR_GROUP_KIND_AUDIO,
	"high_noise_model": v1.TensorGroupKind_TENSOR_GROUP_KIND_DIFFUSION, "uncond_model": v1.TensorGroupKind_TENSOR_GROUP_KIND_DIFFUSION,
}

// Returns artifact bytes, falling back to the group size.
func companionBytes(slot string, c *v1.StoredModel) uint64 {
	file := companionPath(slot, c)
	for _, sa := range c.GetArtifacts() {
		if sa.GetPath() == file && sa.GetArtifact().GetSizeBytes() > 0 {
			return sa.GetArtifact().GetSizeBytes()
		}
	}
	return c.GetBytes()
}

// Blueprint slot and its sd-server parameter, empty if unsupported.
type sdSlot struct {
	fill  blueprint.Fill
	param string
}

// Returns required companion slots in blueprint order.
func sdSlots(d *v1.Descriptor) []sdSlot {
	var out []sdSlot
	for _, fill := range blueprint.Needs(diffusion.ProfileOf(d), d.GetGroup()) {
		out = append(out, sdSlot{fill: fill, param: sdParam(fill.Slot)})
	}
	return out
}

func sdTarget(s *estimate.Scope) blueprint.Target {
	d := s.Descriptor
	return blueprint.Target{Repo: s.Repo, Group: d.GetGroup(), Family: blueprint.Of(d), Bits: d.GetPrecision().GetBits()}
}

// Resolves auto components by blueprint rank, then precision distance. Reuses the selected language
// model's vision tower when available and adds component sizes to the descriptor.
func sdSolve(s *estimate.Scope) {
	d := s.Descriptor
	target := sdTarget(s)
	widened := proto.Clone(d).(*v1.Descriptor)
	widen := func(param string, bytes uint64) {
		if kind, ok := sdPartKinds[param]; ok {
			widened.Groups = append(widened.Groups, &v1.TensorGroup{Id: param, Kind: kind, Layer: -1, Bytes: bytes})
			widened.TotalBytes += bytes
		}
	}
	// Include explicit file sizes in the memory plan.
	for param := range sdParts {
		if s.Params.IsAuto(param) || s.Params.Str(param) == "" {
			continue
		}
		if info, err := os.Stat(s.Params.Str(param)); err == nil && !info.IsDir() {
			widen(param, uint64(info.Size()))
		}
	}
	bits := d.GetPrecision().GetBits()
	pick := func(fill blueprint.Fill) *v1.StoredModel {
		var fits []*v1.StoredModel
		for _, c := range s.Companions {
			if blueprint.Fits(fill, target, candidate(c)) && companionPath(fill.Slot, c) != "" {
				fits = append(fits, c)
			}
		}
		if len(fits) == 0 {
			return nil
		}
		sort.SliceStable(fits, func(i, j int) bool {
			ri, rj := blueprint.Rank(fill, target, candidate(fits[i])), blueprint.Rank(fill, target, candidate(fits[j]))
			if ri != rj {
				return ri < rj
			}
			return bitsDistance(fits[i], bits) < bitsDistance(fits[j], bits)
		})
		return fits[0]
	}
	var llm *v1.StoredModel
	for _, slot := range sdSlots(d) {
		if slot.param == "" || !s.Params.IsAuto(slot.param) {
			continue
		}
		var chosen *v1.StoredModel
		if slot.fill.Slot == blueprint.SlotTextEncoderVision && llm != nil && blueprint.Fits(slot.fill, target, candidate(llm)) {
			chosen = llm
		} else {
			chosen = pick(slot.fill)
		}
		if chosen == nil {
			continue
		}
		if slot.fill.Slot == blueprint.SlotTextEncoderLLM {
			llm = chosen
		}
		s.Params[slot.param] = companionPath(slot.fill.Slot, chosen)
		widen(slot.param, companionBytes(slot.fill.Slot, chosen))
	}
	s.Descriptor = widened
	sdSampling(s)
}

// Absolute precision difference in bits.
func bitsDistance(c *v1.StoredModel, bits uint32) int {
	have := c.GetDescriptor_().GetPrecision().GetBits()
	if bits == 0 || have == 0 {
		return 0
	}
	if have > bits {
		return int(have - bits)
	}
	return int(bits - have)
}

// Reports unsupported models or slots and missing required components.
func sdRefusal(s *estimate.Scope) string {
	d := s.Descriptor
	if d.GetKind() == v1.ModelKind_MODEL_KIND_COMPONENT {
		return fmt.Sprintf("%s is %s, a part loaded beside a diffusion model rather than one served on its own", d.GetGroup(), diffusion.Describe(d.GetArchitecture()))
	}
	if msg := sdUpscalerProblem(s.Params.Str("hires_upscaler"), s.Companions); msg != "" {
		return msg
	}
	family := blueprint.Of(d)
	var missing, unsupported []string
	for _, slot := range sdUnfilled(s) {
		if slot.param == "" {
			unsupported = append(unsupported, fmt.Sprintf("%s (%s)", slot.fill.Slot, slot.fill.Name))
			continue
		}
		where := blueprint.Where(family, slot.fill)
		missing = append(missing, fmt.Sprintf("%s (%s: %s, published at %s)", slot.param, slot.fill.Slot, slot.fill.Name, strings.Join(where, " or ")))
	}
	var out []string
	if len(unsupported) > 0 {
		out = append(out, fmt.Sprintf("stable-diffusion.cpp does not support %s required by %s", strings.Join(unsupported, ", "), family.Name))
	}
	if len(missing) > 0 {
		out = append(out, fmt.Sprintf("%s needs %s. Pull the missing components or set their paths under Model files", family.Name, strings.Join(missing, ", ")))
	}
	return strings.Join(out, ". ")
}

// Accepts built-in upscaler modes or stored upscalers.
func sdUpscalerProblem(value string, stored []*v1.StoredModel) string {
	if value == "" || slices.Contains(sdUpscalers, value) {
		return ""
	}
	for _, m := range stored {
		if m.GetGroup() == value && m.GetDescriptor_().GetKind() == v1.ModelKind_MODEL_KIND_COMPONENT && diffusion.PartOf(m.GetDescriptor_()) == "upscaler" && companionWeights(m) != "" {
			return ""
		}
	}
	return fmt.Sprintf("hires_upscaler %q is not a latent mode, Lanczos, Nearest, or an upscaler in the store", value)
}

// Returns unfilled required slots.
func sdUnfilled(s *estimate.Scope) []sdSlot {
	d := s.Descriptor
	if d.GetKind() != v1.ModelKind_MODEL_KIND_DIFFUSION || blueprint.Of(d) == nil {
		return nil
	}
	var out []sdSlot
	for _, slot := range sdSlots(d) {
		if !slot.fill.Required {
			continue
		}
		if slot.param != "" && !(s.Params.IsAuto(slot.param) || s.Params.Str(slot.param) == "") {
			continue
		}
		out = append(out, slot)
	}
	return out
}

// Returns missing slots and their runtime parameters.
func sdMissing(s *estimate.Scope) []estimate.Missing {
	var out []estimate.Missing
	for _, slot := range sdUnfilled(s) {
		out = append(out, estimate.Missing{Param: slot.param, Slot: slot.fill.Slot})
	}
	return out
}

// Estimates peak activation memory across denoising and VAE decoding.
func sdOverhead(s *estimate.Scope) uint64 {
	width, height := float64(s.Params.Int("width")), float64(s.Params.Int("height"))
	frames := math.Max(float64(s.Params.Int("video_frames")), 1)
	hidden := s.Model.Embedding
	if hidden == 0 {
		hidden = 3072
	}
	// Eight pixels to a latent, two latents to a token, four frames to a latent frame
	tokens := math.Ceil(width/16) * math.Ceil(height/16) * math.Ceil((frames+3)/4)
	activations := tokens * hidden * 48
	if !s.Params.Bool("diffusion_fa") && !s.Params.Bool("fa") {
		activations += tokens * tokens * math.Max(hidden/128, 1) * 2
	}
	decode := width * height * frames * 1024
	if s.Params.Bool("vae_tiling") {
		decode = 512 * 512 * frames * 1024
	}
	return uint64(384*(1<<20) + math.Max(activations, decode))
}

func (SDCpp) Policy() *estimate.Policy {
	device := v1.PoolKind_POOL_KIND_DEVICE
	return &estimate.Policy{
		Groups: []estimate.GroupRule{
			// Place components on device while they fit, starting with the denoiser.
			{Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_DIFFUSION, Pool: device, Param: "on_device", SpillPriority: 10},
			{Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_VAE, Pool: device, Param: "on_device", SpillPriority: 10},
			{Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_TEXT_ENCODER, Pool: device, Param: "on_device", SpillPriority: 10},
			{Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_VISION, Pool: device},
			{Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_AUDIO, Pool: device},
			{Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_OTHER, Pool: device},
			{Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_EMBEDDING, Pool: device},
			{Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_LAYER, Pool: device},
			{Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_OUTPUT, Pool: device},
			{Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_EXPERTS, Pool: device},
			{Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_DRAFT, Pool: device},
		},
		OverheadBytes: sdOverhead,
		Margin:        0.05,
		Solve:         sdSolve,
		Missing:       sdMissing,
		States: func(s *estimate.Scope) ([]*v1.ParamState, string) {
			states := []*v1.ParamState{
				bounds("on_device", 0, float64(sdPartCount(s.Descriptor)), 1),
				bounds("threads", -1, cpuThreads(s.Host), 1),
			}
			return states, sdRefusal(s)
		},
	}
}
