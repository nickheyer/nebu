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

	"github.com/nickheyer/nebu/pkg/estimate"
	"github.com/nickheyer/nebu/pkg/formats"
	"github.com/nickheyer/nebu/pkg/formats/diffusion"
	"github.com/nickheyer/nebu/pkg/host"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/text"
	"github.com/nickheyer/nebu/pkg/triage"
	"google.golang.org/protobuf/proto"
)

// stable-diffusion.cpp's server, which samples image and video models on the CPU and on any GPU through CUDA, ROCm, Vulkan, or Metal
//
// A run names one denoiser, the stored group, and the parts its family loads beside it, each a file
// param the planner solves to what the store holds, the way llama.cpp's projector rides with its
// weights. Every flag sd-server takes is a param here, in the groups its own help lists them in.
type SDCpp struct{}

func (SDCpp) ID() string   { return "sdcpp" }
func (SDCpp) Name() string { return "stable-diffusion.cpp" }
func (SDCpp) Description() string {
	return "Generates images and video from diffusion checkpoints, safetensors or GGUF, on CPU, CUDA, ROCm, Vulkan, and Metal"
}
func (SDCpp) Formats() []string  { return []string{"gguf", "diffusion", "safetensors"} }
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

// stable-diffusion.cpp publishes no CUDA build for Linux, so an NVIDIA host takes Vulkan from the releases or builds from source
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
		{ID: "adopt", Description: "Records an sd-server already on this host; nothing is downloaded or built", Kind: v1.InstallKind_INSTALL_KIND_ADOPTED, Binaries: []string{"sd-server"}},
		{
			ID: "release", Description: "A prebuilt sd-server from the GitHub releases of leejet/stable-diffusion.cpp", Kind: v1.InstallKind_INSTALL_KIND_PREBUILT, Releases: "leejet/stable-diffusion.cpp",
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
		{ID: "source", Description: "Compiles sd-server with the backend for the devices on this host", Kind: v1.InstallKind_INSTALL_KIND_BUILT, RecipeID: "sdcpp"},
	}
}

// The file params the planner solves to what the store holds, each by the part it picks and the words for it
var sdParts = map[string]struct{ label, picks, flag string }{
	"vae":                   {"a VAE", "vae", "--vae"},
	"clip_l":                {"a CLIP-L text encoder", "clip_l", "--clip_l"},
	"clip_g":                {"a CLIP-G text encoder", "clip_g", "--clip_g"},
	"t5xxl":                 {"a T5 text encoder", "t5", "--t5xxl"},
	"llm":                   {"a language model text encoder", "llm", "--llm"},
	"llm_vision":            {"the language model's vision projector", "projector", "--llm_vision"},
	"clip_vision":           {"a CLIP vision encoder", "clip_vision", "--clip_vision"},
	"high_noise_model":      {"the high noise half of the denoiser", "diffusion", "--high-noise-diffusion-model"},
	"uncond_model":          {"the unconditional denoiser", "diffusion", "--uncond-diffusion-model"},
	"audio_encoder":         {"an audio encoder", "audio_encoder", "--audio-encoder"},
	"audio_vae":             {"an audio VAE", "audio_vae", "--audio-vae"},
	"embeddings_connectors": {"the embeddings connectors", "embeddings_connectors", "--embeddings-connectors"},
	"tokenizer":             {"a tokenizer", "tokenizer", "--tokenizer"},
}

// The directory params that hold every adapter of a kind, solved to a directory of links to what the store holds of it
var sdDirs = map[string]struct{ label, picks, flag, sub string }{
	"lora_dir":      {"LoRAs", "lora", "--lora-model-dir", "loras"},
	"embd_dir":      {"textual inversion embeddings", "embedding", "--embd-dir", "embeddings"},
	"upscalers_dir": {"highres fix upscalers", "upscaler", "--hires-upscalers-dir", "upscalers"},
}

// Where the parts each family needs are published, as stable-diffusion.cpp's docs list them: the file in
// a repository when one file is the part, the repository alone when it holds several to choose from,
// a GGUF at every quantization say. The family's own entry comes first, then the one any family takes.
var sdSources = map[string]map[string][]estimate.PartSource{
	"vae": {
		"flux": {{Repo: "black-forest-labs/FLUX.1-dev", Path: "ae.safetensors"}}, "chroma": {{Repo: "black-forest-labs/FLUX.1-dev", Path: "ae.safetensors"}}, "longcat": {{Repo: "black-forest-labs/FLUX.1-dev", Path: "ae.safetensors"}}, "boogu_image": {{Repo: "black-forest-labs/FLUX.1-dev", Path: "ae.safetensors"}},
		"z_image": {{Repo: "black-forest-labs/FLUX.1-schnell", Path: "ae.safetensors"}}, "ovis_image": {{Repo: "black-forest-labs/FLUX.1-schnell", Path: "ae.safetensors"}},
		"flux2": {{Repo: "black-forest-labs/FLUX.2-dev"}}, "flux2_klein": {{Repo: "black-forest-labs/FLUX.2-dev"}}, "lens": {{Repo: "black-forest-labs/FLUX.2-dev"}}, "ideogram4": {{Repo: "black-forest-labs/FLUX.2-dev"}}, "sefi_image": {{Repo: "black-forest-labs/FLUX.2-dev"}}, "ernie_image": {{Repo: "Comfy-Org/ERNIE-Image"}},
		"wan":   {{Repo: "Comfy-Org/Wan_2.1_ComfyUI_repackaged", Path: "split_files/vae/wan_2.1_vae.safetensors"}, {Repo: "Comfy-Org/Wan_2.2_ComfyUI_Repackaged", Path: "split_files/vae/wan2.2_vae.safetensors"}},
		"krea2": {{Repo: "Comfy-Org/Wan_2.1_ComfyUI_repackaged", Path: "split_files/vae/wan_2.1_vae.safetensors"}}, "lingbot_video": {{Repo: "Comfy-Org/Wan_2.1_ComfyUI_repackaged", Path: "split_files/vae/wan_2.1_vae.safetensors"}},
		"qwen_image": {{Repo: "Comfy-Org/Qwen-Image_ComfyUI", Path: "split_files/vae/qwen_image_vae.safetensors"}}, "anima": {{Repo: "Comfy-Org/Qwen-Image_ComfyUI", Path: "split_files/vae/qwen_image_vae.safetensors"}},
		"hunyuan_video": {{Repo: "Comfy-Org/HunyuanVideo_1.5_repackaged"}}, "ltx2": {{Repo: "Lightricks/LTX-2.5", Path: "vae/ltx-2.5-video-vae-conv-bf16.safetensors"}}, "minimax_h3": {{Repo: "Comfy-Org/MiniMax-H3"}}, "mage_flow": {{Repo: "microsoft/Mage-Flow"}},
		"sdxl": {{Repo: "madebyollin/sdxl-vae-fp16-fix"}}, "sd3": {{Repo: "stabilityai/stable-diffusion-3.5-large"}}, "pid": {{Repo: "nvidia/PiD"}}, "svd": {{Repo: "stabilityai/stable-video-diffusion-img2vid-xt"}},
	},
	"t5xxl": {
		"wan":           {{Repo: "Comfy-Org/Wan_2.1_ComfyUI_repackaged", Path: "split_files/text_encoders/umt5_xxl_fp16.safetensors"}, {Repo: "city96/umt5-xxl-encoder-gguf"}},
		"hunyuan_video": {{Repo: "Comfy-Org/HunyuanVideo_1.5_repackaged"}}, "minit2i": {{Repo: "google/flan-t5-large"}}, "sd3": {{Repo: "Comfy-Org/stable-diffusion-3.5-fp8", Path: "text_encoders/t5xxl_fp16.safetensors"}},
		"": {{Repo: "comfyanonymous/flux_text_encoders", Path: "t5xxl_fp16.safetensors"}},
	},
	"clip_l": {"sd3": {{Repo: "Comfy-Org/stable-diffusion-3.5-fp8", Path: "text_encoders/clip_l.safetensors"}}, "": {{Repo: "comfyanonymous/flux_text_encoders", Path: "clip_l.safetensors"}}},
	"clip_g": {"": {{Repo: "Comfy-Org/stable-diffusion-3.5-fp8", Path: "text_encoders/clip_g.safetensors"}}},
	"llm": {
		"qwen_image": {{Repo: "Comfy-Org/Qwen-Image_ComfyUI"}, {Repo: "mradermacher/Qwen2.5-VL-7B-Instruct-GGUF"}}, "longcat": {{Repo: "Comfy-Org/Qwen-Image_ComfyUI"}, {Repo: "mradermacher/Qwen2.5-VL-7B-Instruct-GGUF"}}, "hunyuan_video": {{Repo: "Comfy-Org/Qwen-Image_ComfyUI"}, {Repo: "mradermacher/Qwen2.5-VL-7B-Instruct-GGUF"}},
		"flux2": {{Repo: "unsloth/Mistral-Small-3.2-24B-Instruct-2506-GGUF"}}, "flux2_klein": {{Repo: "Comfy-Org/flux2-klein-4B"}, {Repo: "unsloth/Qwen3-4B-GGUF"}}, "z_image": {{Repo: "Comfy-Org/z_image_turbo"}, {Repo: "unsloth/Qwen3-4B-Instruct-2507-GGUF"}},
		"krea2": {{Repo: "Comfy-Org/Krea-2"}, {Repo: "Qwen/Qwen3-VL-4B-Instruct-GGUF"}}, "lingbot_video": {{Repo: "Comfy-Org/Krea-2"}, {Repo: "Qwen/Qwen3-VL-4B-Instruct-GGUF"}}, "mage_flow": {{Repo: "Comfy-Org/Krea-2"}, {Repo: "Qwen/Qwen3-VL-4B-Instruct-GGUF"}},
		"boogu_image": {{Repo: "unsloth/Qwen3-VL-8B-Instruct-GGUF"}}, "ideogram4": {{Repo: "unsloth/Qwen3-VL-8B-Instruct-GGUF"}}, "ernie_image": {{Repo: "Comfy-Org/ERNIE-Image"}, {Repo: "unsloth/Ministral-3-3B-Instruct-2512-GGUF"}}, "anima": {{Repo: "circlestone-labs/Anima"}, {Repo: "mradermacher/Qwen3-0.6B-Base-GGUF"}},
		"ltx2": {{Repo: "Lightricks/LTX-2.5", Path: "text_encoders/gemma4-12b-with-proj-ltx-2.5-bf16.safetensors"}, {Repo: "unsloth/gemma-3-12b-it-GGUF"}}, "minimax_h3": {{Repo: "Comfy-Org/MiniMax-H3"}, {Repo: "leejet/MiniMax-H3-GGUF"}}, "lens": {{Repo: "unsloth/gpt-oss-20b-GGUF"}}, "pid": {{Repo: "Comfy-Org/PixelDiT"}}, "ovis_image": {{Repo: "Comfy-Org/Ovis-Image"}}, "sefi_image": {{Repo: "SeFi-Image"}},
	},
	"clip_vision":           {"": {{Repo: "Comfy-Org/Wan_2.1_ComfyUI_repackaged", Path: "split_files/clip_vision/clip_vision_h.safetensors"}}},
	"audio_encoder":         {"": {{Repo: "Comfy-Org/Wan_2.2_ComfyUI_Repackaged", Path: "split_files/audio_encoders/wav2vec2_large_english_fp16.safetensors"}}},
	"audio_vae":             {"ltx2": {{Repo: "Lightricks/LTX-2.5", Path: "vae/ltx-2.5-audio-vae-bf16.safetensors"}}, "minimax_h3": {{Repo: "Comfy-Org/MiniMax-H3"}}},
	"embeddings_connectors": {"": {{Repo: "unsloth/LTX-2.3-GGUF"}}},
	"uncond_model":          {"": {{Repo: "ideogram-ai/ideogram-4-fp8"}}},
	"tokenizer":             {"lens": {{Repo: "openai/gpt-oss-20b", Path: "tokenizer.json"}}, "pid": {{Repo: "google/gemma-2-2b", Path: "tokenizer.json"}}},
	"llm_vision":            {"": {{Repo: "unsloth/Qwen3-VL-8B-Instruct-GGUF"}, {Repo: "mradermacher/Qwen2.5-VL-7B-Instruct-GGUF"}}},
	"high_noise_model":      {"": {{Repo: "Comfy-Org/Wan_2.2_ComfyUI_Repackaged"}}},
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
			Rule: strings.TrimPrefix(strings.TrimPrefix(part.label, "a "), "an ") + " stored beside the model, from its repository first, then any repository"}
	}
	dir := func(name, label string) *v1.Param {
		d := sdDirs[name]
		return &v1.Param{Name: name, Label: label, Type: v1.ParamType_PARAM_TYPE_PATH, Default: Auto, Solved: true, Group: groupAdapters, Flag: d.flag, Picks: d.picks, Advanced: true,
			Rule: "a directory of links to every " + strings.TrimSuffix(d.label, "s") + " in the store, so a request names any by its file name"}
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
	// A sampling setting the planner gives the family's own value while it is left at auto
	sampling := func(name, label, group, flag string, typ v1.ParamType, unit string, choices []string, min, max, step float64, rule string) *v1.Param {
		return &v1.Param{Name: name, Label: label, Type: typ, Default: Auto, Solved: true, Unit: unit, Choices: choices, Min: min, Max: max, Step: step, Group: group, Flag: flag, Rule: rule}
	}
	pairs := "key=value, comma separated"
	return []*v1.Param{
		// Model files: the parts a family loads beside its denoiser, solved from the store
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

		// Adapters and helpers: files a run may load beside the pipeline, and the directories requests name adapters under
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

		// Placement: what the device holds and where the rest goes
		{Name: "on_device", Label: "Parts on device", Type: v1.ParamType_PARAM_TYPE_INT, Default: Auto, Solved: true, Unit: "parts", Min: 0, Step: 1, Group: groupPlacement,
			Rule: "every part of the pipeline that fits in device memory, the denoiser first, then the VAE, then the text encoders"},
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

		// Compute: how the parts are run
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
		floatParam("linear_scale", "Linear input scale", groupCompute, "--linear-scale", "0", "0 keeps the model's own", 0, 0, 0.05, true),
		floatParam("attn_scale", "Attention K/V scale", groupCompute, "--attn-scale", "0", "0 keeps the model's own", 0, 0, 0.05, true),

		// Generation: what a request takes when it names nothing
		intParam("width", "Width", groupDefaults, "--width", "1024", "px", "", 64, 4096, 16, false),
		intParam("height", "Height", groupDefaults, "--height", "1024", "px", "", 64, 4096, 16, false),
		sampling("steps", "Steps", groupDefaults, "--steps", v1.ParamType_PARAM_TYPE_INT, "steps", nil, 1, 150, 1,
			"the step count the family samples well at, fewer for a distilled model such as a turbo, schnell, or lightning release"),
		sampling("sampling_method", "Sampler", groupDefaults, "--sampling-method", v1.ParamType_PARAM_TYPE_STRING, "", sdSamplers, 0, 0, 0,
			"euler for a transformer family, lcm for PiD, euler_a for a UNet family, as stable-diffusion.cpp picks them"),
		sampling("scheduler", "Scheduler", groupDefaults, "--scheduler", v1.ParamType_PARAM_TYPE_STRING, "", sdSchedulers, 0, 0, 0,
			"the schedule stable-diffusion.cpp picks for the family: flux, flux2, ltx2, logit_normal, lcm, or discrete"),
		intParam("seed", "Seed", groupDefaults, "--seed", "-1", "", "-1 for random", -1, 0, 1, true),
		intParam("batch_count", "Images per request", groupDefaults, "--batch-count", "1", "images", "", 1, 64, 1, true),
		intParam("clip_skip", "CLIP skip", groupDefaults, "--clip-skip", "-1", "", "-1 for the family's own", -1, 12, 1, true),
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
			"the guidance the family is documented at, 1 for a distilled model that takes no negative prompt"),
		floatParam("img_cfg_scale", "Image guidance scale", groupGuidance, "--img-cfg-scale", "0", "0 matches the guidance scale", 0, 30, 0.5, true),
		sampling("guidance", "Distilled guidance", groupGuidance, "--guidance", v1.ParamType_PARAM_TYPE_FLOAT, "", nil, 0, 30, 0.5,
			"4 for FLUX.2, 3.5 for every other family with a guidance embedding"),
		floatParam("slg_scale", "Skip layer guidance scale", groupGuidance, "--slg-scale", "0", "0 off", 0, 10, 0.5, true),
		strParam("skip_layers", "Skipped layers", groupGuidance, "--skip-layers", "", "7,8,9", nil, true),
		floatParam("skip_layer_start", "Skip layer start", groupGuidance, "--skip-layer-start", "0.01", "", 0, 1, 0.01, true),
		floatParam("skip_layer_end", "Skip layer end", groupGuidance, "--skip-layer-end", "0.2", "", 0, 1, 0.01, true),
		floatParam("eta", "Eta", groupGuidance, "--eta", "0", "0 keeps the sampler's own", 0, 2, 0.05, true),
		sampling("flow_shift", "Flow shift", groupGuidance, "--flow-shift", v1.ParamType_PARAM_TYPE_FLOAT, "", nil, 0, 20, 0.05,
			"the shift stable-diffusion.cpp gives the family, 5 for Wan, 1.15 for FLUX and Krea 2, 3 for Qwen Image, 0 for a family that samples without one"),

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
		floatParam("high_noise_eta", "High noise eta", groupHighNoise, "--high-noise-eta", "0", "0 keeps the sampler's own", 0, 2, 0.05, true),

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

		// ADetailer, a second pass over what a detector finds
		pathParam("ad_model", "ADetailer detector", groupDetailer, "--ad-model", "detector", true),
		strParam("ad_prompt", "ADetailer prompt", groupDetailer, "--ad-prompt", "", "[PROMPT], [SEP], [SKIP]", nil, true),
		strParam("ad_negative_prompt", "ADetailer negative prompt", groupDetailer, "--ad-negative-prompt", "", "", nil, true),
		strParam("extra_ad_args", "Extra ADetailer arguments", groupDetailer, "--extra-ad-args", "", pairs, nil, true),

		// Diagnostics
		strParam("log_level", "Log level", groupDiagnostics, "--log-level", "debug", "", []string{"debug", "verbose", "info", "warn", "error"}, true),
		boolParam("color", "Colored log", groupDiagnostics, "--color", true),
	}
}

// A checkpoint that bundles its VAE or text encoders loads as a full model, a lone denoiser as a diffusion model
func bundled(d *v1.Descriptor) bool {
	p := diffusion.ProfileOf(d)
	return p.VAE || p.TextEncoder
}

// The parts of the pipeline the device may hold, the denoiser, the autoencoder, and the text encoders,
// counted over the model's own groups and the parts solved beside it
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
	// A checkpoint in shards loads through its index, the way sd-server reads a safetensors index file
	if _, _, count, ok := formats.Shard(strings.TrimSuffix(path.Base(weights), path.Ext(weights))); ok && count > 1 {
		index := in.Artifacts[text.Enum(v1.ArtifactRole_ARTIFACT_ROLE_INDEX)]
		if index == "" {
			return nil, fmt.Errorf("%w: this group is split into %d shards and the store holds no index naming them; pull a single file checkpoint or a GGUF instead", ErrParam, count)
		}
		weights = index
	}
	if in.Descriptor.GetKind() == v1.ModelKind_MODEL_KIND_COMPONENT {
		return nil, fmt.Errorf("%w: %s is %s, a part loaded beside a diffusion model rather than one served on its own", ErrParam, in.Name, diffusion.Describe(in.Descriptor.GetArchitecture()))
	}
	p := in.Params.Clone()
	args := []string{"--listen-ip", in.Host, "--listen-port", strconv.Itoa(in.Port)}
	if bundled(in.Descriptor) {
		args = append(args, "--model", weights)
	} else {
		args = append(args, "--diffusion-model", weights)
	}
	// The adapter directories are laid out under the group's prepared directory, one link per adapter the store holds
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
	// Offload follows the plan and the parts asked onto the device: every part on the device needs none, a
	// part planned into host memory or left off the device by on_device needs it
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

// Lays out a directory of links to every stored adapter of one kind, named by group so a request names
// one by its file name the way sd-server scans a directory; the directory is remade on every launch
// so it says what the store holds now, and stays empty when the store holds none
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
		if diffusion.Canonical(m.GetDescriptor_().GetArchitecture()) != picks || m.GetDescriptor_().GetKind() != v1.ModelKind_MODEL_KIND_COMPONENT {
			continue
		}
		file := companionWeights(m)
		if file == "" {
			continue
		}
		name := m.GetGroup() + filepath.Ext(file)
		if err := os.Symlink(file, filepath.Join(dir, name)); err != nil {
			return "", err
		}
	}
	return dir, nil
}

// The parts the pipeline loads, the denoiser, the autoencoder, and the text encoders: what the plan placed,
// the solved parts included, else the descriptor's own groups
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

// Whether the plan put any part of the pipeline in host memory, which sd-server serves by offloading
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

// The server loads every part before it listens, so the capabilities endpoint answering means the model is ready
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
				// One device per line, its name before a tab and its description after
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

// A byte count printed as a number and unit with no space between and a note in parentheses after, 1234.56 MB(VRAM) or 6702.86MB
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

// The stored weights file of a companion, its first weights artifact
func companionWeights(c *v1.StoredModel) string {
	for _, sa := range c.GetArtifacts() {
		if sa.GetArtifact().GetRole() == v1.ArtifactRole_ARTIFACT_ROLE_WEIGHTS {
			return sa.GetPath()
		}
	}
	return ""
}

// A stored file of a companion by role, empty when it carries none
func companionFile(c *v1.StoredModel, role v1.ArtifactRole) string {
	for _, sa := range c.GetArtifacts() {
		if sa.GetArtifact().GetRole() == role {
			return sa.GetPath()
		}
	}
	return ""
}

// The tokenizer.json a stored group carries, the file sd-server reads a tokenizer from
func companionTokenizer(c *v1.StoredModel) string {
	for _, sa := range c.GetArtifacts() {
		if sa.GetArtifact().GetRole() == v1.ArtifactRole_ARTIFACT_ROLE_TOKENIZER && path.Base(sa.GetArtifact().GetPath()) == "tokenizer.json" {
			return sa.GetPath()
		}
	}
	return ""
}

// Whether a companion is the part a param names: a component of that architecture, any language model for the llm,
// and for the high noise half a denoiser of the same family named like this one with high in place of low
func companionFits(param, family, group string, c *v1.StoredModel) bool {
	d := c.GetDescriptor_()
	arch := diffusion.Canonical(d.GetArchitecture())
	switch param {
	case "llm":
		return d.GetKind() == v1.ModelKind_MODEL_KIND_LANGUAGE || arch == "llm"
	case "llm_vision":
		return (d.GetKind() == v1.ModelKind_MODEL_KIND_LANGUAGE || arch == "llm") && companionFile(c, v1.ArtifactRole_ARTIFACT_ROLE_PROJECTOR) != ""
	case "tokenizer":
		return (d.GetKind() == v1.ModelKind_MODEL_KIND_LANGUAGE || arch == "llm") && companionTokenizer(c) != ""
	case "high_noise_model":
		if d.GetKind() != v1.ModelKind_MODEL_KIND_DIFFUSION || arch != family {
			return false
		}
		return highOf(group) == strings.ToLower(c.GetGroup())
	case "uncond_model":
		return d.GetKind() == v1.ModelKind_MODEL_KIND_DIFFUSION && arch == family && strings.Contains(strings.ToLower(c.GetGroup()), "uncond") && !strings.Contains(strings.ToLower(group), "uncond")
	}
	return d.GetKind() == v1.ModelKind_MODEL_KIND_COMPONENT && arch == sdParts[param].picks
}

// The name of the high noise half beside a low noise half, empty for a name that is neither
func highOf(group string) string {
	lower := strings.ToLower(group)
	for _, pair := range [][2]string{{"low_noise", "high_noise"}, {"lownoise", "highnoise"}, {"low-noise", "high-noise"}} {
		if strings.Contains(lower, pair[0]) {
			return strings.Replace(lower, pair[0], pair[1], 1)
		}
	}
	return ""
}

// The file a param names in a companion: the projector for the vision tower, the tokenizer for the tokenizer, the weights for the rest
func companionPath(param string, c *v1.StoredModel) string {
	switch param {
	case "llm_vision":
		return companionFile(c, v1.ArtifactRole_ARTIFACT_ROLE_PROJECTOR)
	case "tokenizer":
		return companionTokenizer(c)
	}
	return companionWeights(c)
}

// The kind of tensor group a file param's part loads as, so the plan sizes it beside the model's own groups
var sdPartKinds = map[string]v1.TensorGroupKind{
	"vae": v1.TensorGroupKind_TENSOR_GROUP_KIND_VAE, "audio_vae": v1.TensorGroupKind_TENSOR_GROUP_KIND_VAE,
	"clip_l": v1.TensorGroupKind_TENSOR_GROUP_KIND_TEXT_ENCODER, "clip_g": v1.TensorGroupKind_TENSOR_GROUP_KIND_TEXT_ENCODER, "t5xxl": v1.TensorGroupKind_TENSOR_GROUP_KIND_TEXT_ENCODER, "llm": v1.TensorGroupKind_TENSOR_GROUP_KIND_TEXT_ENCODER, "embeddings_connectors": v1.TensorGroupKind_TENSOR_GROUP_KIND_TEXT_ENCODER,
	"llm_vision": v1.TensorGroupKind_TENSOR_GROUP_KIND_VISION, "clip_vision": v1.TensorGroupKind_TENSOR_GROUP_KIND_VISION,
	"audio_encoder":    v1.TensorGroupKind_TENSOR_GROUP_KIND_AUDIO,
	"high_noise_model": v1.TensorGroupKind_TENSOR_GROUP_KIND_DIFFUSION, "uncond_model": v1.TensorGroupKind_TENSOR_GROUP_KIND_DIFFUSION,
}

// The bytes a companion's file takes: the artifact the param names, the whole group when its size is not listed
func companionBytes(param string, c *v1.StoredModel) uint64 {
	file := companionPath(param, c)
	for _, sa := range c.GetArtifacts() {
		if sa.GetPath() == file && sa.GetArtifact().GetSizeBytes() > 0 {
			return sa.GetArtifact().GetSizeBytes()
		}
	}
	return c.GetBytes()
}

// Solves the file params to what the store holds: for each part left at auto, the fitting companion from
// the model's own repository first, then one whose name shares a word with the family, then any; the
// vision projector and tokenizer follow the language model picked. Every part picked joins the scope's
// descriptor as a group of its own, so the plan places and counts it beside the model's own groups.
func sdSolve(s *estimate.Scope) {
	d := s.Descriptor
	family := diffusion.Canonical(d.GetArchitecture())
	group := strings.ToLower(d.GetGroup())
	repo := s.Repo
	rank := func(c *v1.StoredModel) int {
		switch {
		case repo != "" && c.GetRepo() == repo:
			return 0
		case strings.Contains(strings.ToLower(c.GetGroup()), family) || strings.Contains(strings.ToLower(c.GetRepo()), family):
			return 1
		}
		return 2
	}
	pick := func(param string) *v1.StoredModel {
		var fits []*v1.StoredModel
		for _, c := range s.Companions {
			if companionFits(param, family, group, c) {
				fits = append(fits, c)
			}
		}
		if len(fits) == 0 {
			return nil
		}
		sort.SliceStable(fits, func(i, j int) bool { return rank(fits[i]) < rank(fits[j]) })
		return fits[0]
	}
	widened := proto.Clone(d).(*v1.Descriptor)
	var llm *v1.StoredModel
	for _, param := range sortedParts() {
		if !s.Params.IsAuto(param) {
			// A file named by hand is sized off the disk, so the plan counts it like a solved one
			if kind, ok := sdPartKinds[param]; ok && s.Params.Str(param) != "" {
				if info, err := os.Stat(s.Params.Str(param)); err == nil && !info.IsDir() {
					widened.Groups = append(widened.Groups, &v1.TensorGroup{Id: param, Kind: kind, Layer: -1, Bytes: uint64(info.Size())})
					widened.TotalBytes += uint64(info.Size())
				}
			}
			continue
		}
		var chosen *v1.StoredModel
		switch param {
		case "llm_vision", "tokenizer":
			// The projector and tokenizer ride with the language model when it carries them
			if llm != nil && companionFits(param, family, group, llm) {
				chosen = llm
			} else {
				chosen = pick(param)
			}
		default:
			chosen = pick(param)
		}
		if chosen == nil {
			continue
		}
		if param == "llm" {
			llm = chosen
		}
		file := companionPath(param, chosen)
		if file == "" {
			continue
		}
		s.Params[param] = file
		if kind, ok := sdPartKinds[param]; ok {
			widened.Groups = append(widened.Groups, &v1.TensorGroup{Id: param, Kind: kind, Layer: -1, Bytes: companionBytes(param, chosen)})
			widened.TotalBytes += companionBytes(param, chosen)
		}
	}
	s.Descriptor = widened
	sdSampling(s)
}

// The file params in a fixed order, the language model before the parts that follow it
func sortedParts() []string {
	out := make([]string, 0, len(sdParts))
	for name := range sdParts {
		if name != "llm" && name != "llm_vision" && name != "tokenizer" {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return append([]string{"llm"}, append(out, "llm_vision", "tokenizer")...)
}

// Why a run would be refused: a part the family needs left unset, or a component run on its own; the
// refusal names the part and where its docs say it is published
func sdRefusal(s *estimate.Scope) string {
	d := s.Descriptor
	if d.GetKind() == v1.ModelKind_MODEL_KIND_COMPONENT {
		return fmt.Sprintf("%s is %s, a part loaded beside a diffusion model rather than one served on its own", d.GetGroup(), diffusion.Describe(d.GetArchitecture()))
	}
	if msg := sdUpscalerProblem(s.Params.Str("hires_upscaler"), s.Companions); msg != "" {
		return msg
	}
	profile := diffusion.ProfileOf(d)
	var missing []string
	for _, part := range sdMissing(s) {
		var where []string
		for _, src := range part.Sources {
			if src.Path != "" {
				where = append(where, src.Repo+" "+src.Path)
			} else {
				where = append(where, src.Repo)
			}
		}
		missing = append(missing, fmt.Sprintf("%s (%s, published at %s)", part.Param, part.Label, strings.Join(where, " or ")))
	}
	if len(missing) == 0 {
		return ""
	}
	return fmt.Sprintf("%s needs %s: pull it so it is found in the store, or pick the file in the model files params", profile.Family, strings.Join(missing, "; "))
}

// highres upscaler
func sdUpscalerProblem(value string, stored []*v1.StoredModel) string {
	if value == "" || slices.Contains(sdUpscalers, value) {
		return ""
	}
	for _, m := range stored {
		if m.GetGroup() == value && m.GetDescriptor_().GetKind() == v1.ModelKind_MODEL_KIND_COMPONENT && diffusion.Canonical(m.GetDescriptor_().GetArchitecture()) == "upscaler" && companionWeights(m) != "" {
			return ""
		}
	}
	return fmt.Sprintf("hires_upscaler %q is not a latent mode, Lanczos, Nearest, or an upscaler in the store", value)
}

// The required parts a run needs that nothing solved, each with where its family's docs publish it
func sdMissing(s *estimate.Scope) []estimate.Missing {
	d := s.Descriptor
	if d.GetKind() != v1.ModelKind_MODEL_KIND_DIFFUSION {
		return nil
	}
	profile := diffusion.ProfileOf(d)
	var out []estimate.Missing
	for _, part := range diffusion.Needs(profile) {
		if !part.Required || !(s.Params.IsAuto(part.Param) || s.Params.Str(part.Param) == "") {
			continue
		}
		out = append(out, estimate.Missing{Param: part.Param, Label: sdParts[part.Param].label, Sources: sdSource(part.Param, profile.Family)})
	}
	return out
}

// Where a part is published for a family, as the runtime's docs list it: the family's own entries, then the ones any family takes
func sdSource(param, family string) []estimate.PartSource {
	by := sdSources[param]
	out := append([]estimate.PartSource(nil), by[family]...)
	if family != "" {
		for _, src := range by[""] {
			if !slices.Contains(out, src) {
				out = append(out, src)
			}
		}
	}
	return out
}

// Compute memory at the default size: the denoiser's activations over its latent tokens, then the VAE's decode, whichever peaks higher
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
			// The pipeline's parts go on the device while they fit, the denoiser first, and the rest are offloaded
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
