package runtimes

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nickheyer/nebu/pkg/archs"
	"github.com/nickheyer/nebu/pkg/estimate"
	"github.com/nickheyer/nebu/pkg/formats"
	"github.com/nickheyer/nebu/pkg/host"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/text"
	"github.com/nickheyer/nebu/pkg/triage"
	"google.golang.org/protobuf/proto"
)

// LlamaCpp serves GGUF models on CPU and supported GPU backends.
type LlamaCpp struct{}

func (LlamaCpp) ID() string   { return "llamacpp" }
func (LlamaCpp) Name() string { return "llama.cpp" }
func (LlamaCpp) Description() string {
	return "Serves GGUF models on CPU, CUDA, ROCm, Vulkan, and Metal"
}
func (LlamaCpp) Formats() []string  { return []string{"gguf"} }
func (LlamaCpp) Kind() v1.ModelKind { return v1.ModelKind_MODEL_KIND_LANGUAGE }
func (LlamaCpp) API() v1.ApiFlavor  { return v1.ApiFlavor_API_FLAVOR_OPENAI }
func (LlamaCpp) Requirements() []string {
	return []string{"at least one probed device"}
}

func (LlamaCpp) Unmet(h *v1.HostProfile) []string {
	if len(h.GetDevices()) == 0 {
		return []string{"at least one probed device"}
	}
	return nil
}

// Published builds by platform and device. CUDA builds ship with a cudart archive beside them,
// the CUDA 13 ones needing a 580 driver. SYCL builds run on Intel GPUs with the oneAPI runtime
func (LlamaCpp) Methods() []Method {
	linux := func(arch string, more func(*v1.HostProfile) bool) func(*v1.HostProfile) bool {
		return func(h *v1.HostProfile) bool { return host.Is(h, "linux", arch) && (more == nil || more(h)) }
	}
	windows := func(arch string, more func(*v1.HostProfile) bool) func(*v1.HostProfile) bool {
		return func(h *v1.HostProfile) bool { return host.Is(h, "windows", arch) && (more == nil || more(h)) }
	}
	amd := func(h *v1.HostProfile) bool { return host.HasVendor(h, "amd") }
	nvidia := func(h *v1.HostProfile) bool { return host.HasVendor(h, "nvidia") }
	intel := func(h *v1.HostProfile) bool { return host.HasVendor(h, "intel") }
	nvidia580 := func(h *v1.HostProfile) bool {
		return nvidia(h) && text.CompareVersions(driverVersion(h, "nvidia"), "580") >= 0
	}
	tar := func(contains string) Asset { return Asset{Prefix: "llama-b", Contains: contains, Suffix: ".tar.gz"} }
	zip := func(contains, suffix string) Asset {
		return Asset{Prefix: "llama-b", Contains: contains, Suffix: suffix}
	}
	cudart := func(contains, suffix string) Asset {
		return Asset{Prefix: "cudart-llama-b", Contains: contains, Suffix: suffix}
	}
	return []Method{
		{ID: "adopt", Description: "Use an installed llama-server", Kind: v1.InstallKind_INSTALL_KIND_ADOPTED, Binaries: []string{"llama-server"}},
		{
			ID: "release", Description: "Download llama-server from GitHub releases", Kind: v1.InstallKind_INSTALL_KIND_PREBUILT, Releases: "ggml-org/llama.cpp",
			Rules: []PrebuiltRule{
				{ID: "linux-cuda13", Applies: linux("amd64", nvidia580), Assets: []Asset{zip("-bin-ubuntu-cuda-13.", "-x64.tar.gz"), cudart("-bin-ubuntu-cuda-13.", "-x64.tar.gz")}, Binary: "llama-server"},
				{ID: "linux-cuda12", Applies: linux("amd64", nvidia), Assets: []Asset{zip("-bin-ubuntu-cuda-12.", "-x64.tar.gz"), cudart("-bin-ubuntu-cuda-12.", "-x64.tar.gz")}, Binary: "llama-server"},
				{ID: "linux-rocm", Applies: linux("amd64", amd), Assets: []Asset{zip("-bin-ubuntu-rocm-", "-x64.tar.gz")}, Binary: "llama-server"},
				{ID: "linux-sycl", Applies: linux("amd64", intel), Assets: []Asset{tar("-bin-ubuntu-sycl-fp16-x64")}, Binary: "llama-server"},
				{ID: "linux-vulkan", Applies: linux("amd64", host.HasGPU), Assets: []Asset{tar("-bin-ubuntu-vulkan-x64")}, Binary: "llama-server"},
				{ID: "linux-cpu", Applies: linux("amd64", nil), Assets: []Asset{tar("-bin-ubuntu-x64")}, Binary: "llama-server"},
				{ID: "linux-arm64-cuda13", Applies: linux("arm64", nvidia580), Assets: []Asset{zip("-bin-ubuntu-cuda-13.", "-arm64.tar.gz"), cudart("-bin-ubuntu-cuda-13.", "-arm64.tar.gz")}, Binary: "llama-server"},
				{ID: "linux-arm64-vulkan", Applies: linux("arm64", host.HasGPU), Assets: []Asset{tar("-bin-ubuntu-vulkan-arm64")}, Binary: "llama-server"},
				{ID: "linux-arm64-cpu", Applies: linux("arm64", nil), Assets: []Asset{tar("-bin-ubuntu-arm64")}, Binary: "llama-server"},
				{ID: "macos-arm64", Applies: func(h *v1.HostProfile) bool { return host.Is(h, "darwin", "arm64") }, Assets: []Asset{tar("-bin-macos-arm64")}, Binary: "llama-server"},
				{ID: "macos-x64", Applies: func(h *v1.HostProfile) bool { return host.Is(h, "darwin", "amd64") }, Assets: []Asset{tar("-bin-macos-x64")}, Binary: "llama-server"},
				{ID: "windows-cuda13", Applies: windows("amd64", nvidia580), Assets: []Asset{zip("-bin-win-cuda-13.", "-x64.zip"), {Prefix: "cudart-llama-bin-win-cuda-13.", Suffix: "-x64.zip"}}, Binary: "llama-server.exe"},
				{ID: "windows-cuda12", Applies: windows("amd64", nvidia), Assets: []Asset{zip("-bin-win-cuda-12.", "-x64.zip"), {Prefix: "cudart-llama-bin-win-cuda-12.", Suffix: "-x64.zip"}}, Binary: "llama-server.exe"},
				{ID: "windows-arm64-cuda", Applies: windows("arm64", nvidia), Assets: []Asset{zip("-bin-win-cuda-", "-arm64.zip"), {Prefix: "cudart-llama-bin-win-cuda-", Suffix: "-arm64.zip"}}, Binary: "llama-server.exe"},
				{ID: "windows-rocm", Applies: windows("amd64", amd), Assets: []Asset{zip("-bin-win-rocm-", "-x64.zip")}, Binary: "llama-server.exe"},
				{ID: "windows-sycl", Applies: windows("amd64", intel), Assets: []Asset{zip("-bin-win-sycl-x64", ".zip")}, Binary: "llama-server.exe"},
				{ID: "windows-vulkan", Applies: windows("amd64", host.HasGPU), Assets: []Asset{zip("-bin-win-vulkan-x64", ".zip")}, Binary: "llama-server.exe"},
				{ID: "windows-cpu", Applies: windows("amd64", nil), Assets: []Asset{zip("-bin-win-cpu-x64", ".zip")}, Binary: "llama-server.exe"},
				{ID: "windows-arm64-vulkan", Applies: windows("arm64", host.HasGPU), Assets: []Asset{zip("-bin-win-vulkan-arm64", ".zip")}, Binary: "llama-server.exe"},
				{ID: "windows-arm64-cpu", Applies: windows("arm64", nil), Assets: []Asset{zip("-bin-win-cpu-arm64", ".zip")}, Binary: "llama-server.exe"},
			},
		},
		{ID: "source", Description: "Build llama-server for this host", Kind: v1.InstallKind_INSTALL_KIND_BUILT, RecipeID: "llamacpp"},
	}
}

// The cache types llama.cpp takes for --cache-type-k and --cache-type-v
var llamaCacheTypes = []string{"f32", "f16", "bf16", "q8_0", "q4_0", "q4_1", "iq4_nl", "q5_0", "q5_1"}

// Cache type choices, empty leaving llama.cpp's own f16
var llamaCacheChoices = append([]string{""}, llamaCacheTypes...)

// The speculative methods --spec-type takes, as llama.cpp names them
var llamaSpecTypes = []string{"none", "draft-simple", "draft-eagle3", "draft-mtp", "draft-dflash", "draft-dspark", "ngram-simple", "ngram-map-k", "ngram-map-k4v", "ngram-mod", "ngram-cache"}

// Speculative methods that draft with the model's own MTP heads
var llamaMTPMethods = []string{"draft-mtp"}

// Draft architectures whose layers read the target's cache instead of keeping their own
var llamaSharedCacheArchs = []string{"gemma4-assistant"}

// Draft architectures that are MTP head files without nextn tensors, drafting only with draft-mtp
var llamaHeadArchs = []string{"gemma4-assistant"}

// Ids of the groups Solve adds for the projector and draft files the params name
const (
	llamaProjectorGroup = "mmproj"
	llamaDraftGroup     = "draft_model"
)

// Bytes per element of a draft or MTP context's cache, f16 as llama.cpp keeps it by default
const llamaDraftCacheBytes = 2.0

// Smallest context llama.cpp's fit cuts an unset context to per sequence, its --fit-ctx default
const llamaFitContext = 4096

// What an auto context resolves to, the choice llama.cpp makes when nebu passes none
const llamaContextRule = "llama.cpp's own choice, the trained length for each parallel sequence cut to fit free device memory but not below fit_ctx, 4096 tokens unless set"

// The modes --load-mode takes, empty leaving llama.cpp's auto
var llamaLoadModes = []string{"", "auto", "none", "mmap", "mlock", "mmap+mlock", "dio"}

var llamaCacheBytes = map[string]float64{"f32": 4, "f16": 2, "bf16": 2, "q8_0": 1.0625, "q5_1": 0.75, "q5_0": 0.6875, "q4_1": 0.625, "q4_0": 0.5625, "iq4_nl": 0.5625}

// The switches llama-server spells as --flag and --no-flag, by param name
var llamaSwitches = map[string]string{
	"kv_unified":             "--kv-unified",
	"cache_idle_slots":       "--cache-idle-slots",
	"context_shift":          "--context-shift",
	"kv_offload":             "--kv-offload",
	"repack":                 "--repack",
	"op_offload":             "--op-offload",
	"cont_batching":          "--cont-batching",
	"cache_prompt":           "--cache-prompt",
	"mmproj_auto":            "--mmproj-auto",
	"mmproj_offload":         "--mmproj-offload",
	"jinja":                  "--jinja",
	"reasoning_preserve":     "--reasoning-preserve",
	"skip_chat_parsing":      "--skip-chat-parsing",
	"prefill_assistant":      "--prefill-assistant",
	"draft_backend_sampling": "--spec-draft-backend-sampling",
	"log_prefix":             "--log-prefix",
	"log_timestamps":         "--log-timestamps",
}

// Draft model params whose flag spelling the install's help decides, with the fact naming it
var llamaDraftFlags = []struct{ param, fact string }{
	{"draft_max", factDraftMax},
	{"draft_min", factDraftMin},
	{"draft_p_min", factDraftPMin},
	{"draft_p_split", factDraftPSplit},
	{"draft_layers", factDraftLayers},
	{"draft_device", factDraftDevice},
	{"draft_cache_type_k", factDraftCacheK},
	{"draft_cache_type_v", factDraftCacheV},
	{"draft_threads", factDraftThreads},
	{"draft_threads_batch", factDraftThreadsBatch},
	{"draft_cpu_moe", factDraftCPUMoE},
	{"draft_n_cpu_moe", factDraftNCPUMoE},
	{"draft_override_tensor", factDraftOverride},
}

func (LlamaCpp) Params() []*v1.Param {
	const (
		groupContext     = "Context"
		groupPlacement   = "Placement"
		groupCache       = "Cache"
		groupBatching    = "Batching"
		groupIdentity    = "Identity"
		groupChat        = "Chat"
		groupEmbeddings  = "Embeddings"
		groupAdapters    = "Adapters"
		groupRope        = "RoPE"
		groupSpeculative = "Speculative decoding"
		groupServer      = "Server"
		groupDiagnostics = "Diagnostics"
	)
	intParam := func(name, label, group, flag, def, unit, description string, min, max, step float64, advanced bool) *v1.Param {
		return &v1.Param{Name: name, Label: label, Type: v1.ParamType_PARAM_TYPE_INT, Default: def, Unit: unit, Min: min, Max: max, Step: step, Group: group, Flag: flag, Advanced: advanced, Description: description}
	}
	floatParam := func(name, label, group, flag, description string, min, max, step float64) *v1.Param {
		return &v1.Param{Name: name, Label: label, Type: v1.ParamType_PARAM_TYPE_FLOAT, Min: min, Max: max, Step: step, Group: group, Flag: flag, Advanced: true, Description: description}
	}
	boolParam := func(name, label, group, flag, description string) *v1.Param {
		return &v1.Param{Name: name, Label: label, Type: v1.ParamType_PARAM_TYPE_BOOL, Default: "false", Group: group, Flag: flag, Advanced: true, Description: description}
	}
	strParam := func(name, label, group, flag, def, description string, choices []string, advanced bool) *v1.Param {
		return &v1.Param{Name: name, Label: label, Type: v1.ParamType_PARAM_TYPE_STRING, Default: def, Choices: choices, Group: group, Flag: flag, Advanced: advanced, Description: description}
	}
	switchParam := func(name, label, group, description string) *v1.Param {
		return &v1.Param{Name: name, Label: label, Type: v1.ParamType_PARAM_TYPE_STRING, Choices: switchChoices, Group: group, Advanced: true, Description: description}
	}
	ngram := func(name, label, flag, description string) *v1.Param {
		return intParam(name, label, groupSpeculative, flag, "", "", description, 1, 1024, 1, true)
	}
	return []*v1.Param{
		{Name: "n_ctx", Label: "Context length", Type: v1.ParamType_PARAM_TYPE_INT, Default: Auto, Solved: true, Unit: "tokens", Min: 256, Step: 256, Group: groupContext, Flag: "--ctx-size",
			Description: "the whole cache pool, each parallel sequence gets n_ctx / n_parallel of it unless kv_unified is on",
			Rule:        llamaContextRule},
		intParam("n_parallel", "Parallel sequences", groupContext, "--parallel", "1", "sequences", "server slots, a request past them waits for one to free, -1 lets llama.cpp choose the count and unifies the pool", -1, 256, 1, false),
		switchParam("kv_unified", "Unified cache pool", groupContext, "every sequence draws from one pool, so a sequence may use up to the whole n_ctx, empty keeps llama.cpp's off, on when n_parallel is -1"),
		intParam("kv_unified_per_slot", "Context per slot", groupContext, "--kv-unified-per-slot", "", "tokens", "limit for each parallel slot, the shared pool sized to n_parallel times it when n_ctx is unset, empty keeps llama.cpp's none", 0, 0, 256, true),
		{Name: "slot_prompt_similarity", Label: "Slot reuse similarity", Type: v1.ParamType_PARAM_TYPE_FLOAT, Min: 0, Max: 1, Step: 0.05, Group: groupContext, Flag: "--slot-prompt-similarity",
			Description: "prefix share a request must have with an idle slot's cache to take that slot over an empty one, empty keeps llama.cpp's 0.10, 0 always takes the empty or least recently used slot"},
		intParam("n_predict", "Generation limit", groupContext, "--n-predict", "", "tokens", "most tokens a request generates unless it asks for fewer, empty keeps llama.cpp's -1 for no limit", -1, 0, 1, true),
		switchParam("context_shift", "Context shift", groupContext, "drop the oldest tokens when a generation fills the context instead of stopping, empty keeps llama.cpp's off"),
		boolParam("swa_full", "Full sliding window cache", groupContext, "--swa-full", "keep the whole cache for sliding window attention layers, which costs memory and allows cache reuse past the window"),
		{Name: "n_gpu_layers", Label: "GPU layers", Type: v1.ParamType_PARAM_TYPE_INT, Default: Auto, Solved: true, Unit: "layers", Min: 0, Step: 1, Group: groupPlacement, Flag: "--n-gpu-layers",
			Rule: "fit layers on device and place the rest in system memory"},
		{Name: "n_cpu_moe", Label: "Expert layers on CPU", Type: v1.ParamType_PARAM_TYPE_INT, Default: Auto, Solved: true, Unit: "layers", Min: 0, Step: 1, Group: groupPlacement, Flag: "--n-cpu-moe",
			Rule: "offload expert layers that exceed remaining device memory"},
		boolParam("cpu_moe", "Every expert on CPU", groupPlacement, "--cpu-moe", "keep every mixture of experts weight in system memory"),
		intParam("n_cpu_ffn", "Dense FFN layers on CPU", groupPlacement, "--n-cpu-ffn", "", "layers", "keep the dense feed forward weights of the first N layers in system memory", 0, 0, 1, true),
		intParam("moe_cache_mib", "Expert cache on device", groupPlacement, "--moe-cache-mib", "", "MiB", "device cache for experts kept on the CPU, split across devices like the layers, empty keeps llama.cpp's 0 for none", 0, 0, 256, true),
		strParam("override_tensor", "Tensor buffer overrides", groupPlacement, "--override-tensor", "", "pattern=buffer type pairs, comma separated, such as blk\\.[0-9]+\\.ffn_.*_exps=CPU", nil, true),
		{Name: "device", Label: "Devices", Type: v1.ParamType_PARAM_TYPE_STRING, Group: groupPlacement, Advanced: true, Flag: "--device",
			Description: "CUDA0,CUDA1, or none"},
		strParam("split_mode", "Split mode", groupPlacement, "--split-mode", "", "how layers spread over several devices, empty keeps llama.cpp's layer", []string{"", "none", "layer", "row", "tensor"}, true),
		intParam("main_gpu", "Main device", groupPlacement, "--main-gpu", "", "", "device index that holds the model with split mode none and the cache with split mode row, empty keeps llama.cpp's 0", 0, 0, 1, true),
		strParam("tensor_split", "Tensor split", groupPlacement, "--tensor-split", "", "share of the layers each device takes, comma separated proportions such as 3,1", nil, true),
		{Name: "threads", Label: "CPU threads", Type: v1.ParamType_PARAM_TYPE_INT, Default: "-1", Unit: "threads", Min: -1, Step: 1, Group: groupPlacement, Advanced: true, Flag: "--threads",
			Description: "-1 for the core count"},
		intParam("threads_batch", "Batch threads", groupPlacement, "--threads-batch", "", "threads", "threads for prompt processing, empty keeps llama.cpp's same as threads", -1, 0, 1, true),
		strParam("numa", "NUMA strategy", groupPlacement, "--numa", "", "", []string{"", "distribute", "isolate", "numactl"}, true),
		strParam("load_mode", "Weight loading", groupPlacement, "", "", "how the weights are read, dio reads without the page cache, mlock pins them in memory, none reads them into private memory, empty keeps llama.cpp's auto", llamaLoadModes, true),
		strParam("lazy_mode", "Lazy tensor reads", groupPlacement, "--lazy-mode", "", "read per layer embedding rows from disk on demand, empty keeps llama.cpp's auto for tensors over 4 GiB", []string{"", "on", "auto", "off"}, true),
		switchParam("kv_offload", "Cache on device", groupPlacement, "keep the cache on the device, empty keeps llama.cpp's on"),
		switchParam("op_offload", "Offload host operations", groupPlacement, "run operations on host tensors on the device, empty keeps llama.cpp's on"),
		switchParam("repack", "Weight repacking", groupPlacement, "repack weights for the CPU backend's kernels, empty keeps llama.cpp's on"),
		boolParam("no_host", "Bypass host buffers", groupPlacement, "--no-host", "skip the host buffer so extra buffer types are used"),
		strParam("fit", "Fit to memory", groupPlacement, "--fit", "", "let llama.cpp cut unset context and layers to free device memory, empty keeps its on", []string{"", "on", "off"}, true),
		intParam("fit_ctx", "Fit context floor", groupPlacement, "--fit-ctx", "", "tokens", "smallest context the fit may cut to, empty keeps llama.cpp's 4096", 256, 0, 256, true),
		strParam("fit_target", "Fit margin", groupPlacement, "--fit-target", "", "MiB left free on each device by the fit, one value or one per device, empty keeps llama.cpp's 1024", nil, true),
		{Name: "cache_type_k", Label: "Key cache type", Type: v1.ParamType_PARAM_TYPE_STRING, Choices: llamaCacheChoices, Group: groupCache, Flag: "--cache-type-k",
			Description: "empty keeps llama.cpp's f16"},
		{Name: "cache_type_v", Label: "Value cache type", Type: v1.ParamType_PARAM_TYPE_STRING, Choices: llamaCacheChoices, Group: groupCache, Flag: "--cache-type-v",
			Description: "empty keeps llama.cpp's f16, a quantized type needs flash attention"},
		{Name: "flash_attn", Label: "Flash attention", Type: v1.ParamType_PARAM_TYPE_STRING, Default: "auto", Choices: []string{"auto", "on", "off"}, Group: groupCache, Flag: "--flash-attn"},
		{Name: "cache_ram", Label: "Prompt cache in RAM", Type: v1.ParamType_PARAM_TYPE_INT, Unit: "MiB", Min: -1, Step: 1024, Group: groupCache, Flag: "--cache-ram",
			Description: "host memory for the states of conversations evicted from a slot, empty keeps llama.cpp's 8192, -1 unlimited, 0 off"},
		switchParam("cache_idle_slots", "Save idle slots", groupCache, "copy every idle slot's state into the RAM cache when a request starts, empty keeps llama.cpp's on"),
		{Name: "cache_prompt", Label: "Prompt caching", Type: v1.ParamType_PARAM_TYPE_STRING, Default: "on", Choices: switchChoices, Group: groupCache, Advanced: true,
			Description: "reuse a slot's cache for a request sharing its prefix, which prefix aware routing counts on, empty keeps llama.cpp's on"},
		{Name: "ctx_checkpoints", Label: "Context checkpoints", Type: v1.ParamType_PARAM_TYPE_INT, Unit: "per slot", Min: 0, Max: 256, Step: 1, Group: groupCache, Advanced: true, Flag: "--ctx-checkpoints",
			Description: "state snapshots a slot keeps so a turn that rewrites earlier tokens re-prefills from the nearest one, empty keeps llama.cpp's 32, 0 off"},
		{Name: "checkpoint_min_step", Label: "Checkpoint spacing", Type: v1.ParamType_PARAM_TYPE_INT, Unit: "tokens", Min: 0, Step: 256, Group: groupCache, Advanced: true, Flag: "--checkpoint-min-step",
			Description: "fewest tokens between checkpoints, empty keeps llama.cpp's 8192, a smaller step costs memory and re-prefills less"},
		{Name: "cache_reuse", Label: "Cache reuse chunk", Type: v1.ParamType_PARAM_TYPE_INT, Default: "256", Unit: "tokens", Min: 0, Step: 32, Group: groupCache, Advanced: true, Flag: "--cache-reuse",
			Description: "smallest chunk reused by shifting the KV cache, 0 off, empty keeps llama.cpp's off, no effect on hybrid and recurrent models"},
		{Name: "n_batch", Label: "Batch size", Type: v1.ParamType_PARAM_TYPE_INT, Default: "2048", Unit: "tokens", Min: 32, Step: 32, Group: groupBatching, Advanced: true, Flag: "--batch-size"},
		{Name: "n_ubatch", Label: "Micro-batch size", Type: v1.ParamType_PARAM_TYPE_INT, Default: "512", Unit: "tokens", Min: 32, Step: 32, Group: groupBatching, Advanced: true, Flag: "--ubatch-size",
			Description: "tokens per forward pass, the chunk of one slot's prefill that every other slot's decoding waits behind"},
		switchParam("cont_batching", "Continuous batching", groupBatching, "decode every slot's sequence in one batch, empty keeps llama.cpp's on"),
		{Name: "alias", Label: "Served name", Type: v1.ParamType_PARAM_TYPE_STRING, Group: groupIdentity, Flag: "--alias", Description: "model names the API answers to, comma separated"},
		{Name: "mmproj", Label: "Projector", Type: v1.ParamType_PARAM_TYPE_PATH, Group: groupIdentity, Advanced: true, Flag: "--mmproj", Picks: "projector",
			Description: "image and audio input"},
		switchParam("mmproj_auto", "Projector lookup", groupIdentity, "load a projector found beside the model, empty keeps llama.cpp's on"),
		switchParam("mmproj_offload", "Projector on device", groupIdentity, "run the projector on the device, empty keeps llama.cpp's on"),
		strParam("mmproj_device", "Projector GPU", groupIdentity, "--mmproj-device", "", "the GPU that runs the projector, the part of the model that reads images and audio, named as llama.cpp lists devices such as CUDA0, none for the CPU, empty for the model's own GPU", nil, true),
		switchParam("jinja", "Jinja templates", groupChat, "render chat templates with the jinja engine that tools need, empty passes --jinja on builds that list it"),
		strParam("chat_template", "Chat template", groupChat, "--chat-template", "", "a built in template name or jinja text, empty keeps the model's", nil, true),
		{Name: "chat_template_file", Label: "Chat template file", Type: v1.ParamType_PARAM_TYPE_PATH, Group: groupChat, Advanced: true, Flag: "--chat-template-file"},
		strParam("chat_template_kwargs", "Template arguments", groupChat, "--chat-template-kwargs", "", "a JSON object of values the template reads", nil, true),
		strParam("reasoning", "Reasoning", groupChat, "--reasoning", "", "whether the model thinks before answering, empty keeps llama.cpp's auto from the template", []string{"", "auto", "on", "off"}, true),
		strParam("reasoning_format", "Reasoning format", groupChat, "--reasoning-format", "", "where thoughts go in the answer, empty keeps llama.cpp's auto", []string{"", "auto", "none", "deepseek", "deepseek-legacy"}, true),
		strParam("reasoning_effort", "Reasoning effort", groupChat, "--reasoning-effort", "", "level handed to the template, such as low, medium, high, empty keeps the template's default", nil, true),
		intParam("reasoning_budget", "Reasoning budget", groupChat, "--reasoning-budget", "", "tokens", "tokens the model may think for, 0 ends thinking at once, empty keeps llama.cpp's -1 for no limit", -1, 0, 1, true),
		strParam("reasoning_budget_message", "Budget message", groupChat, "--reasoning-budget-message", "", "text injected before the end of thinking when the budget runs out", nil, true),
		switchParam("reasoning_preserve", "Preserve reasoning", groupChat, "keep every turn's reasoning in the history on templates that support it, empty keeps llama.cpp's on"),
		switchParam("skip_chat_parsing", "Skip chat parsing", groupChat, "leave reasoning and tool calls unparsed in the content, empty keeps llama.cpp's off"),
		switchParam("prefill_assistant", "Prefill assistant", groupChat, "continue a trailing assistant message instead of answering it, empty keeps llama.cpp's on"),
		{Name: ParamStripBillingHeader, Label: "Strip Claude Code billing header", Type: v1.ParamType_PARAM_TYPE_BOOL, Default: "true", Group: groupChat, Advanced: true,
			Description: "drop the x-anthropic-billing-header line Claude Code leads its system prompt with, whose stamp changes on every request and ends the prompt cache at its first tokens"},
		boolParam("embedding", "Embeddings only", groupEmbeddings, "--embedding", "serve embeddings alone, for embedding models"),
		boolParam("reranking", "Reranking", groupEmbeddings, "--reranking", "serve the rerank endpoint, for reranker models"),
		strParam("pooling", "Pooling", groupEmbeddings, "--pooling", "", "how token states pool into one embedding, empty keeps the model's", []string{"", "none", "mean", "cls", "last", "rank"}, true),
		fileList(strParam("lora", "LoRA adapters", groupAdapters, "--lora", "", "adapter files, comma separated, empty loads every LoRA file beside the model unapplied so requests switch them on", nil, true), v1.ParamList_PARAM_LIST_FILES),
		fileList(strParam("lora_scaled", "Scaled LoRA adapters", groupAdapters, "--lora-scaled", "", "file:scale pairs, comma separated", nil, true), v1.ParamList_PARAM_LIST_SCALED),
		boolParam("lora_init_without_apply", "Load adapters unapplied", groupAdapters, "--lora-init-without-apply", "load adapters and apply them later through the API"),
		fileList(strParam("control_vector", "Control vectors", groupAdapters, "--control-vector", "", "control vector files, comma separated", nil, true), v1.ParamList_PARAM_LIST_FILES),
		fileList(strParam("control_vector_scaled", "Scaled control vectors", groupAdapters, "--control-vector-scaled", "", "file:scale pairs, comma separated", nil, true), v1.ParamList_PARAM_LIST_SCALED),
		strParam("control_vector_layer_range", "Control vector layers", groupAdapters, "", "", "first and last layer the control vectors apply to, as start,end", nil, true),
		strParam("rope_scaling", "RoPE scaling", groupRope, "--rope-scaling", "", "empty keeps the model's, linear unless it says", []string{"", "none", "linear", "yarn"}, true),
		floatParam("rope_scale", "RoPE scale", groupRope, "--rope-scale", "context scaling factor, the inverse of the frequency scale", 0, 0, 0.5),
		floatParam("rope_freq_base", "RoPE base", groupRope, "--rope-freq-base", "base frequency for NTK aware scaling, empty keeps the model's", 0, 0, 1000),
		floatParam("rope_freq_scale", "RoPE frequency scale", groupRope, "--rope-freq-scale", "frequency scaling factor, the inverse of the context scale", 0, 0, 0.05),
		intParam("yarn_orig_ctx", "YaRN original context", groupRope, "--yarn-orig-ctx", "", "tokens", "empty keeps the model's trained length", 0, 0, 256, true),
		floatParam("yarn_ext_factor", "YaRN extrapolation", groupRope, "--yarn-ext-factor", "extrapolation mix factor, 0 for full interpolation, empty keeps llama.cpp's -1 for the model's", -1, 1, 0.05),
		floatParam("yarn_attn_factor", "YaRN attention factor", groupRope, "--yarn-attn-factor", "attention magnitude scale, empty keeps llama.cpp's 1", 0, 0, 0.05),
		floatParam("yarn_beta_slow", "YaRN beta slow", groupRope, "--yarn-beta-slow", "high correction dimension, empty keeps llama.cpp's 1", 0, 0, 0.5),
		floatParam("yarn_beta_fast", "YaRN beta fast", groupRope, "--yarn-beta-fast", "low correction dimension, empty keeps llama.cpp's 32", 0, 0, 0.5),
		{Name: "spec_type", Label: "Speculative method", Type: v1.ParamType_PARAM_TYPE_STRING, Default: Auto, Solved: true, Choices: llamaSpecTypes, Group: groupSpeculative, Advanced: true,
			Description: "draft-mtp for MTP heads, draft-simple and draft-eagle3 for a draft model, the ngram methods need no draft model",
			Rule:        "draft-mtp for an MTP head file, otherwise llama.cpp's own pick"},
		{Name: "draft_model", Label: "Draft model", Type: v1.ParamType_PARAM_TYPE_PATH, Group: groupSpeculative, Advanced: true, Picks: "draft",
			Description: "a smaller model of the same family, or MTP heads"},
		{Name: "draft_max", Label: "Draft tokens per round", Type: v1.ParamType_PARAM_TYPE_INT, Unit: "tokens", Min: 1, Max: 64, Step: 1, Group: groupSpeculative, Advanced: true,
			Description: "empty keeps llama.cpp's 3"},
		intParam("draft_min", "Fewest draft tokens", groupSpeculative, "", "", "tokens", "fewest tokens a round drafts, empty keeps llama.cpp's 0", 0, 64, 1, true),
		floatParam("draft_p_min", "Draft probability floor", groupSpeculative, "", "probability under which drafting stops, empty keeps llama.cpp's 0", 0, 1, 0.05),
		floatParam("draft_p_split", "Draft split probability", groupSpeculative, "", "empty keeps llama.cpp's 0.1", 0, 1, 0.05),
		strParam("draft_layers", "Draft GPU layers", groupSpeculative, "", "", "layers of the draft model on the device, a number, auto, or all, empty keeps llama.cpp's auto", nil, true),
		strParam("draft_device", "Draft devices", groupSpeculative, "", "", "devices the draft model runs on, comma separated, none for the host, empty follows device", nil, true),
		strParam("draft_cache_type_k", "Draft key cache type", groupSpeculative, "", "", "empty keeps llama.cpp's f16", llamaCacheChoices, true),
		strParam("draft_cache_type_v", "Draft value cache type", groupSpeculative, "", "", "empty keeps llama.cpp's f16", llamaCacheChoices, true),
		intParam("draft_threads", "Draft threads", groupSpeculative, "", "", "threads", "empty keeps llama.cpp's same as threads", -1, 0, 1, true),
		intParam("draft_threads_batch", "Draft batch threads", groupSpeculative, "", "", "threads", "empty keeps llama.cpp's same as draft_threads", -1, 0, 1, true),
		intParam("draft_n_ubatch", "Draft micro-batch", groupSpeculative, "--spec-draft-n-ubatch", "", "tokens", "physical batch of the draft context, empty keeps llama.cpp's same as n_ubatch", 0, 0, 32, true),
		strParam("draft_sampling", "Draft sampling", groupSpeculative, "--spec-draft-sampling", "", "greedy takes the draft argmax, probabilistic samples it and verifies by rejection, empty keeps llama.cpp's greedy", []string{"", "greedy", "probabilistic"}, true),
		switchParam("draft_backend_sampling", "Draft sampling on device", groupSpeculative, "sample the draft on the backend, empty keeps llama.cpp's off"),
		boolParam("draft_cpu_moe", "Draft experts on CPU", groupSpeculative, "", "keep every expert weight of the draft model in system memory"),
		intParam("draft_n_cpu_moe", "Draft expert layers on CPU", groupSpeculative, "", "", "layers", "keep the expert weights of the draft model's first N layers in system memory", 0, 0, 1, true),
		strParam("draft_override_tensor", "Draft tensor overrides", groupSpeculative, "", "", "pattern=buffer type pairs for the draft model, comma separated", nil, true),
		ngram("ngram_mod_n_min", "N-gram mod fewest tokens", "--spec-ngram-mod-n-min", "fewest tokens ngram-mod drafts, empty keeps llama.cpp's 48"),
		ngram("ngram_mod_n_max", "N-gram mod most tokens", "--spec-ngram-mod-n-max", "most tokens ngram-mod drafts, empty keeps llama.cpp's 64"),
		ngram("ngram_mod_n_match", "N-gram mod lookup length", "--spec-ngram-mod-n-match", "lookup length of ngram-mod, empty keeps llama.cpp's"),
		ngram("ngram_simple_size_n", "N-gram simple lookup length", "--spec-ngram-simple-size-n", "lookup n-gram length of ngram-simple, empty keeps llama.cpp's"),
		ngram("ngram_simple_size_m", "N-gram simple draft length", "--spec-ngram-simple-size-m", "draft m-gram length of ngram-simple, empty keeps llama.cpp's"),
		ngram("ngram_simple_min_hits", "N-gram simple fewest hits", "--spec-ngram-simple-min-hits", "fewest hits before ngram-simple drafts, empty keeps llama.cpp's"),
		ngram("ngram_map_k_size_n", "N-gram map-k lookup length", "--spec-ngram-map-k-size-n", "lookup n-gram length of ngram-map-k, empty keeps llama.cpp's"),
		ngram("ngram_map_k_size_m", "N-gram map-k draft length", "--spec-ngram-map-k-size-m", "draft m-gram length of ngram-map-k, empty keeps llama.cpp's 48"),
		ngram("ngram_map_k_min_hits", "N-gram map-k fewest hits", "--spec-ngram-map-k-min-hits", "fewest hits before ngram-map-k drafts, empty keeps llama.cpp's"),
		ngram("ngram_map_k4v_size_n", "N-gram map-k4v lookup length", "--spec-ngram-map-k4v-size-n", "lookup n-gram length of ngram-map-k4v, empty keeps llama.cpp's"),
		ngram("ngram_map_k4v_size_m", "N-gram map-k4v draft length", "--spec-ngram-map-k4v-size-m", "draft m-gram length of ngram-map-k4v, empty keeps llama.cpp's 48"),
		ngram("ngram_map_k4v_min_hits", "N-gram map-k4v fewest hits", "--spec-ngram-map-k4v-min-hits", "fewest hits before ngram-map-k4v drafts, empty keeps llama.cpp's"),
		{Name: "lookup_cache_static", Label: "Static lookup cache", Type: v1.ParamType_PARAM_TYPE_PATH, Group: groupSpeculative, Advanced: true, Flag: "--lookup-cache-static", Description: "n-gram cache file ngram-cache reads and never updates"},
		{Name: "lookup_cache_dynamic", Label: "Dynamic lookup cache", Type: v1.ParamType_PARAM_TYPE_PATH, Group: groupSpeculative, Advanced: true, Flag: "--lookup-cache-dynamic", Description: "n-gram cache file ngram-cache reads and updates as it generates"},
		intParam("timeout", "Request timeout", groupServer, "--timeout", "", "seconds", "read and write timeout of the server, empty keeps llama.cpp's 600", 0, 0, 30, true),
		intParam("threads_http", "HTTP threads", groupServer, "--threads-http", "", "threads", "empty keeps llama.cpp's -1 for the core count", -1, 0, 1, true),
		intParam("sleep_idle_seconds", "Sleep when idle", groupServer, "--sleep-idle-seconds", "", "seconds", "idle time after which the server frees the model until the next request, empty keeps llama.cpp's -1 for never", -1, 0, 60, true),
		boolParam("props", "Settings changes over the API", groupServer, "--props", "let any client change server wide settings, such as the chat template and sampling defaults, by posting to /props"),
		{Name: "media_path", Label: "Media directory", Type: v1.ParamType_PARAM_TYPE_PATH, Group: groupServer, Advanced: true, Flag: "--media-path", Directory: true,
			Description: "directory requests may load local media from by relative file URL"},
		{Name: "log_verbosity", Label: "Log level", Type: v1.ParamType_PARAM_TYPE_INT, Default: "3", Min: 0, Max: 5, Step: 1, Group: groupDiagnostics, Advanced: true, Flag: "--log-verbosity",
			Description: "0 generic, 1 errors, 2 warnings, 3 info, 4 trace, 5 debug, 4 and up log the buffers the memory breakdown and cache calibration read"},
		strParam("log_colors", "Log colors", groupDiagnostics, "--log-colors", "", "empty keeps llama.cpp's auto", []string{"", "auto", "on", "off"}, true),
		switchParam("log_prefix", "Log prefix", groupDiagnostics, "prefix every log line with its level, empty keeps llama.cpp's on"),
		switchParam("log_timestamps", "Log timestamps", groupDiagnostics, "timestamp every log line, empty keeps llama.cpp's on"),
		boolParam("verbose_prompt", "Log prompts", groupDiagnostics, "--verbose-prompt", "print every prompt and its tokens"),
		boolParam("no_warmup", "Skip warmup", groupDiagnostics, "--no-warmup", "skip the empty run that warms the model up at start"),
		boolParam("no_perf", "Skip timings", groupDiagnostics, "--no-perf", "leave performance timings out of the log"),
		boolParam("check_tensors", "Check tensors", groupDiagnostics, "--check-tensors", "check every tensor for invalid values while loading"),
		strParam("override_kv", "Metadata overrides", groupDiagnostics, "--override-kv", "", "key=type:value pairs, comma separated, such as tokenizer.ggml.add_bos_token=bool:false", nil, true),
		intParam("seed", "Seed", groupDiagnostics, "--seed", "", "", "sampling seed, empty keeps llama.cpp's -1 for random", -1, 0, 1, true),
		boolParam("offline", "Offline", groupDiagnostics, "--offline", "never reach the network, every download comes from the cache"),
		{Name: "cuda_disable_graphs", Label: "Disable CUDA graphs", Type: v1.ParamType_PARAM_TYPE_BOOL, Default: "false", Group: groupDiagnostics, Advanced: true, Env: "GGML_CUDA_DISABLE_GRAPHS",
			Description: "runs every batch without ggml's CUDA graphs, slower decoding, only for a driver that fails cudaGraphLaunch under the rpc server"},
	}
}

// Params llama.cpp took once and the launch now sets itself
func (LlamaCpp) Retired() []string { return []string{"metrics"} }

// Refuses zero parallel sequences, which llama.cpp exits on, -1 leaving the count to it
func (LlamaCpp) Check(p estimate.Params) error {
	if n, set := p["n_parallel"].(int64); set && n == 0 {
		return errors.New("param n_parallel: 0 is neither -1 nor at least 1")
	}
	return nil
}

func (r LlamaCpp) Launch(in Launch) (*Command, error) {
	weights := in.Artifacts["weights"]
	if weights == "" {
		return nil, fmt.Errorf("%w: the stored group has no weights file", ErrParam)
	}
	p := in.Params.Clone()
	if p.Str("alias") == "" {
		p["alias"] = in.Name
	}
	// Host placement hides accelerators and leaves the device list to the params
	hostOnly := in.Placement == v1.Placement_PLACEMENT_HOST
	if hostOnly && p.Str("device") == "" {
		p["device"] = "none"
	}
	args := []string{"--model", weights, "--host", in.Host, "--port", strconv.Itoa(in.Port)}
	// Tools need the jinja renderer, which older builds turn on by this flag alone
	if strings.TrimSpace(p.Str("jinja")) == "" && has(in.InstallRecord, factJinja) {
		args = append(args, in.InstallRecord.GetFacts()[factJinja])
	}
	spec, specEmitted := llamaSpeculative(in, p)
	switches, switchEmitted := switchFlags(p, llamaSwitches)
	layers, layersEmitted, err := llamaControlLayers(p)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(p.Str("lora")) == "" && strings.TrimSpace(p.Str("lora_scaled")) == "" {
		found, err := llamaAdaptersBeside(weights)
		if err != nil {
			return nil, err
		}
		if len(found) > 0 {
			p["lora"] = strings.Join(found, ",")
			p["lora_init_without_apply"] = true
		}
	}
	flags, env, emitted := Flags(r.Params(), p)
	// The metrics endpoint the gateway reads queued and running requests from
	flags = append(flags, "--metrics")
	emitted["metrics"] = "true"
	flags = append(flags, spec...)
	flags = append(flags, switches...)
	flags = append(flags, layers...)
	maps.Copy(emitted, specEmitted)
	maps.Copy(emitted, switchEmitted)
	maps.Copy(emitted, layersEmitted)
	// A slot count llama.cpp picks itself is read back from its log, its pool unified as llama.cpp unifies it
	if p.Int("n_parallel") < 0 {
		delete(emitted, "n_parallel")
		if _, set := emitted["kv_unified"]; !set {
			emitted["kv_unified"] = "on"
		}
	}
	if mode := p.Str("load_mode"); mode != "" {
		flags = append(flags, loadModeArgs(in.InstallRecord, mode)...)
	}
	// The slot listing the router sums KV use from, on builds that serve it only when asked
	if has(in.InstallRecord, factSlotsFlag) {
		flags = append(flags, in.InstallRecord.GetFacts()[factSlotsFlag])
		emitted["slots"] = "on"
	}
	cmd := &Command{Command: in.Install.Path, Args: append(args, flags...), Env: env, Params: emitted}
	if hostOnly {
		hideDevices(env)
		return cmd, nil
	}
	if err := pinDevices(cmd, in); err != nil {
		return nil, err
	}
	return cmd, nil
}

// LoRA adapter files in the model's directory, told apart by the general and adapter type of each GGUF header
func llamaAdaptersBeside(weights string) ([]string, error) {
	dir := filepath.Dir(weights)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("%w: reading the model's directory for adapters: %v", ErrParam, err)
	}
	var out []string
	for _, e := range entries {
		path := filepath.Join(dir, e.Name())
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".gguf") || path == filepath.Clean(weights) {
			continue
		}
		meta, err := ggufMetadata(path)
		if err != nil {
			return nil, fmt.Errorf("%w: adapter candidate %v", ErrParam, err)
		}
		if meta["general.type"] == "adapter" && meta["adapter.type"] == "lora" {
			out = append(out, path)
		}
	}
	return out, nil
}

// The control vector layer range as its two arguments, start and end, from the one param
func llamaControlLayers(p estimate.Params) ([]string, map[string]string, error) {
	v := strings.TrimSpace(p.Str("control_vector_layer_range"))
	delete(p, "control_vector_layer_range")
	if v == "" {
		return nil, map[string]string{}, nil
	}
	ends := strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' })
	if len(ends) != 2 {
		return nil, nil, fmt.Errorf("%w: control_vector_layer_range needs a start and an end layer as start,end, got %q", ErrParam, v)
	}
	for _, e := range ends {
		if _, err := strconv.Atoi(e); err != nil {
			return nil, nil, fmt.Errorf("%w: control_vector_layer_range layer %q is not a number", ErrParam, e)
		}
	}
	return []string{"--control-vector-layer-range", ends[0], ends[1]}, map[string]string{"control_vector_layer_range": ends[0] + "," + ends[1]}, nil
}

// Speculative flags as the params say them and this install spells them, an unset method left to llama.cpp
func llamaSpeculative(in Launch, p estimate.Params) ([]string, map[string]string) {
	method, draft := p.Str("spec_type"), llamaFile(p, "draft_model")
	delete(p, "spec_type")
	delete(p, "draft_model")
	var args []string
	emitted := map[string]string{}
	if method != "" && method != Auto {
		args = append(args, "--spec-type", method)
		emitted["spec_type"] = method
	}
	if draft != "" {
		args = append(args, flagOf(in.InstallRecord, factDraftModel), draft)
		emitted["draft_model"] = draft
	}
	for _, f := range llamaDraftFlags {
		value, ok := p[f.param]
		delete(p, f.param)
		if !ok || value == nil {
			continue
		}
		switch v := value.(type) {
		case bool:
			if v {
				args = append(args, flagOf(in.InstallRecord, f.fact))
				emitted[f.param] = "true"
			}
		case string:
			if strings.TrimSpace(v) == "" {
				continue
			}
			args = append(args, flagOf(in.InstallRecord, f.fact), v)
			emitted[f.param] = v
		default:
			text := fmt.Sprint(v)
			args = append(args, flagOf(in.InstallRecord, f.fact), text)
			emitted[f.param] = text
		}
	}
	return args, emitted
}

// The method an unset spec_type takes, draft-mtp for a named head file, llama.cpp's own pick otherwise
func llamaAutoMethod(s *estimate.Scope) string {
	if d := s.Draft; d != nil && llamaFile(s.Params, "draft_model") != "" && (llamaHeads(d) || slices.Contains(llamaHeadArchs, d.GetArchitecture())) {
		return "draft-mtp"
	}
	return Auto
}

// Whether the model's file carries MTP heads, the nextn layers past its trunk
func llamaHeads(d *v1.Descriptor) bool {
	if formats.ParamsOf(d.GetParams()).DraftLayers <= 0 {
		return false
	}
	for _, g := range d.GetGroups() {
		if g.GetKind() == v1.TensorGroupKind_TENSOR_GROUP_KIND_DRAFT && g.GetId() != llamaDraftGroup {
			return true
		}
	}
	return false
}

// The file a path param names, empty when it names none
func llamaFile(p estimate.Params, name string) string {
	if v := strings.TrimSpace(p.Str(name)); !strings.EqualFold(v, None) {
		return v
	}
	return ""
}

func (LlamaCpp) Prepares(string) bool             { return false }
func (LlamaCpp) Prepare(Launch) (*Command, error) { return nil, nil }
func (LlamaCpp) PrepareTimeout() time.Duration    { return 0 }
func (LlamaCpp) Health() Health {
	return Health{Path: "/health", Interval: time.Second, SlowAfter: 10 * time.Minute}
}
func (LlamaCpp) StopGrace() time.Duration { return 15 * time.Second }
func (LlamaCpp) Triage() []triage.Set     { return []triage.Set{triage.LlamaCpp{}} }

func (LlamaCpp) Probes() []Probe {
	return append([]Probe{
		{Key: "version", Args: []string{"--version"}, Parse: func(out string) (string, bool) {
			for _, line := range strings.Split(out, "\n") {
				if i := strings.Index(line, "version:"); i >= 0 {
					if f := strings.Fields(line[i+len("version:"):]); len(f) > 0 {
						return f[0], true
					}
				}
			}
			return "", false
		}},
		{Key: "devices", Args: []string{"--list-devices"}, Parse: func(out string) (string, bool) {
			var names []string
			for _, line := range strings.Split(out, "\n") {
				// Indented device lines start with a backend ID, such as CUDA0.
				if !strings.HasPrefix(line, "  ") {
					continue
				}
				name, _, ok := strings.Cut(strings.TrimSpace(line), ": ")
				if ok && name != "" && !strings.ContainsAny(name, " \t") {
					names = append(names, name)
				}
			}
			return strings.Join(names, ","), len(names) > 0
		}},
	}, llamaShapeProbes()...)
}

// Parses model, KV cache, and compute buffer allocations for device and host backends. Buffers on
// RPC backends are the tensors the head streamed to stages. The transport a seat negotiated, when
// a line names it, is a measurement too.
// What a llama.cpp log says about memory: the weight, cache, and compute buffers it allocated on
// each side, summed per side; the allocations that failed, summed per side under <side>.failed;
// and from the memory breakdown it prints, each device side's total, free, and unaccounted bytes
// as of the latest print. The side is the process's own device, its host, or an rpc device
func (LlamaCpp) Measure(lines []string) []*v1.Measurement {
	var m measurements
	for _, line := range lines {
		for _, b := range llamaBuffers {
			i := strings.Index(line, b.phrase)
			if i < 0 {
				continue
			}
			fields := strings.Fields(line[:i])
			if len(fields) == 0 {
				continue
			}
			n, ok := bytesAfter(line, b.phrase)
			if !ok {
				continue
			}
			m.add(memorySide(fields[len(fields)-1])+"."+b.what, n, line)
		}
		if n, ok := bytesAfter(line, ": allocating "); ok && strings.Contains(line, " failed") {
			m.add("device.failed", n, line)
		}
		if i := strings.Index(line, "failed to allocate "); i >= 0 {
			fields := strings.Fields(line[i+len("failed to allocate "):])
			if len(fields) >= 5 && fields[1] == "buffer" && fields[2] == "of" && fields[3] == "size" {
				if n, err := strconv.ParseUint(strings.TrimRight(fields[4], ",.;"), 10, 64); err == nil {
					m.add(memorySide(fields[0])+".failed", n, line)
				}
			}
		}
		if side, numbers, ok := breakdownRow(line); ok {
			m.set(side+".total", numbers[0]<<20, line)
			m.set(side+".free", numbers[1]<<20, line)
			m.set(side+".unaccounted", numbers[6]<<20, line)
		}
	}
	return append(m.list, transportMeasurement(lines)...)
}

// The buffer allocation phrases llama.cpp logs and what each one measures
var llamaBuffers = []struct{ phrase, what string }{
	{" model buffer size = ", "weights"},
	{" KV buffer size = ", "cache"},
	{" RS buffer size = ", "cache"},
	{" compute buffer size = ", "compute"},
}

// The side a ggml backend or device name belongs to
func memorySide(backend string) string {
	switch {
	case strings.HasPrefix(backend, "CPU"), strings.HasSuffix(backend, "_Host"), backend == "Host":
		return "host"
	case strings.HasPrefix(backend, "RPC"):
		return "rpc"
	}
	return "device"
}

// A device row of llama.cpp's memory breakdown table, "| - <device> | total = free + (self = model
// + context + compute) + unaccounted |" in MiB: the side and the seven numbers, unaccounted read
// as zero when llama.cpp prints it negative. The host row carries four numbers and is no device row
func breakdownRow(line string) (string, []uint64, bool) {
	if !strings.Contains(line, "memory_breakdown") {
		return "", nil, false
	}
	cells := strings.Split(line, "|")
	if len(cells) < 3 {
		return "", nil, false
	}
	name := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(cells[1]), "-"))
	if name == "" {
		return "", nil, false
	}
	var numbers []uint64
	for _, f := range strings.FieldsFunc(cells[2], func(r rune) bool { return r == ' ' || r == '=' || r == '+' || r == '(' || r == ')' }) {
		if strings.HasPrefix(f, "-") {
			numbers = append(numbers, 0)
			continue
		}
		n, err := strconv.ParseUint(f, 10, 64)
		if err != nil {
			return "", nil, false
		}
		numbers = append(numbers, n)
	}
	if len(numbers) != 7 {
		return "", nil, false
	}
	return memorySide(strings.Fields(name)[0]), numbers, true
}

// A cache type param's value, llama.cpp's own f16 when it is empty
func llamaCacheType(p estimate.Params, name string) string {
	if t := p.Str(name); t != "" {
		return t
	}
	return "f16"
}

// Whether the speculative method drafts with MTP heads, which llama.cpp then loads
func llamaMTP(p estimate.Params) bool { return slices.Contains(llamaMTPMethods, p.Str("spec_type")) }

// Cache elements per token the draft model keeps, its trunk's layers or the heads of a heads only file
func llamaDraftElements(s *estimate.Scope, run archs.Run) (float64, error) {
	d := s.Draft
	if slices.Contains(llamaSharedCacheArchs, d.GetArchitecture()) {
		return 0, nil
	}
	if s.DraftFamily == nil {
		return 0, fmt.Errorf("draft model %s: no attention family sizes its cache", d.GetArchitecture())
	}
	layers, err := s.DraftFamily.CacheLayers(formats.ParamsOf(d.GetParams()), run)
	if err != nil {
		return 0, fmt.Errorf("draft model %s: %w", d.GetArchitecture(), err)
	}
	kind := v1.TensorGroupKind_TENSOR_GROUP_KIND_LAYER
	if !llamaTrunk(d) {
		kind = v1.TensorGroupKind_TENSOR_GROUP_KIND_DRAFT
	}
	var total float64
	for _, g := range d.GetGroups() {
		if g.GetKind() == kind && g.GetLayer() >= 0 && int(g.GetLayer()) < len(layers) {
			total += layers[g.GetLayer()]
		}
	}
	return total, nil
}

// Whether a draft file holds trunk layers, a model of its own rather than heads alone
func llamaTrunk(d *v1.Descriptor) bool {
	for _, g := range d.GetGroups() {
		if g.GetKind() == v1.TensorGroupKind_TENSOR_GROUP_KIND_LAYER {
			return true
		}
	}
	return false
}

// Bytes the draft model loads, its heads left on disk unless the method drafts with them
func llamaDraftWeights(s *estimate.Scope) uint64 {
	d := s.Draft
	trunk := llamaTrunk(d)
	var total uint64
	for _, g := range d.GetGroups() {
		if trunk && g.GetKind() == v1.TensorGroupKind_TENSOR_GROUP_KIND_DRAFT && !llamaMTP(s.Params) {
			continue
		}
		total += g.GetBytes()
	}
	return total
}

// Adds the projector and draft files the params name to the descriptor, each as one group
func llamaWiden(s *estimate.Scope) {
	widened := proto.Clone(s.Descriptor).(*v1.Descriptor)
	add := func(id string, kind v1.TensorGroupKind, bytes uint64) {
		widened.Groups = append(widened.Groups, &v1.TensorGroup{Id: id, Kind: kind, Layer: -1, Bytes: bytes})
		widened.TotalBytes += bytes
	}
	if path := llamaFile(s.Params, "mmproj"); path != "" {
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			add(llamaProjectorGroup, v1.TensorGroupKind_TENSOR_GROUP_KIND_VISION, uint64(info.Size()))
		}
	}
	if s.Draft != nil && llamaFile(s.Params, "draft_model") != "" {
		add(llamaDraftGroup, v1.TensorGroupKind_TENSOR_GROUP_KIND_DRAFT, llamaDraftWeights(s))
	}
	s.Descriptor = widened
}

func (LlamaCpp) Policy() *estimate.Policy {
	device := v1.PoolKind_POOL_KIND_DEVICE
	return &estimate.Policy{
		// The head carries every crossing itself in float32, the prompt goes through the stages in one
		// pass, and a relay writes the cache to disk on one seat and reads it on the other.
		Shapes: &estimate.ShapeFacts{Ring: false, ActivationBytes: 4, ChunksInFlight: 1, RelayOverlap: false, RelayDisk: true, ReductionsPerLayer: 2, ChainAggregate: false, ChainSpeculative: true, Handoff: estimate.HandoffLlamaSlot, DraftTokens: 3},
		Groups: []estimate.GroupRule{
			{Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_EMBEDDING, Pool: v1.PoolKind_POOL_KIND_HOST},
			{Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_LAYER, Pool: device, Param: "n_gpu_layers", SpillPriority: 10},
			{Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_OUTPUT, Pool: device, Param: "n_gpu_layers", SpillPriority: 10},
			{Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_EXPERTS, Pool: device, Param: "n_cpu_moe", ParamCountsHost: true, SpillPriority: 1, Requires: v1.TensorGroupKind_TENSOR_GROUP_KIND_LAYER},
			// Only the projector file the param names loads, whole and on device
			{Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_VISION, Pool: device, Loaded: llamaProjectorLoads},
			{Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_AUDIO, Pool: device, Loaded: llamaProjectorLoads},
			// MTP heads load with draft-mtp, a named draft model always
			{Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_DRAFT, Pool: device, Loaded: func(s *estimate.Scope, g *v1.TensorGroup) bool {
				return g.GetId() == llamaDraftGroup || llamaMTP(s.Params)
			}},
			{Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_OTHER, Pool: device},
		},
		CacheBytes: func(s *estimate.Scope) uint64 {
			k, v := llamaCacheBytes[llamaCacheType(s.Params, "cache_type_k")], llamaCacheBytes[llamaCacheType(s.Params, "cache_type_v")]
			return uint64(float64(s.Params.Int("n_ctx")) * s.CachePerToken * (k + v) / 2)
		},
		// A draft or MTP context holds the whole context in f16 over the layers it runs
		GroupCache: func(s *estimate.Scope, g *v1.TensorGroup) (uint64, error) {
			if g.GetKind() != v1.TensorGroupKind_TENSOR_GROUP_KIND_DRAFT {
				return 0, nil
			}
			run := archs.Run{Context: float64(s.Params.Int("n_ctx")), UBatch: float64(s.Params.Int("n_ubatch"))}
			if g.GetId() == llamaDraftGroup {
				elems, err := llamaDraftElements(s, run)
				return uint64(run.Context * elems * llamaDraftCacheBytes), err
			}
			if g.GetLayer() < 0 || int(g.GetLayer()) >= len(s.CacheLayers) {
				return 0, fmt.Errorf("MTP head layer %d is beyond the %d layers the header counts", g.GetLayer(), len(s.CacheLayers))
			}
			return uint64(run.Context * s.CacheLayers[g.GetLayer()] * llamaDraftCacheBytes), nil
		},
		// Device context and compute buffer estimate, fitted to CUDA runs with flash attention.
		OverheadBytes: func(s *estimate.Scope) uint64 {
			ctx, ubatch := float64(s.Params.Int("n_ctx")), float64(s.Params.Int("n_ubatch"))
			return uint64(384*(1<<20) + ctx*ubatch*10 + ubatch*s.Model.Embedding*16)
		},
		Margin:       0.05,
		ContextParam: "n_ctx",
		ContextMin:   llamaFitContext,
		ContextFloor: func(s *estimate.Scope) int64 { return s.Params.Int("fit_ctx") },
		ContextStep:  256,
		ContextMax:   func(s *estimate.Scope) int64 { return int64(s.Model.ContextTrain) },
		// Parallel sequences each get the trained length, as llama.cpp splits an unset context over them, one shared pool when unified
		ContextScale: llamaSequences,
		Deferred:     map[string]func([]string) (string, bool){"n_ctx": llamaContext, "n_parallel": llamaSlots},
		Shape: func(p estimate.Params) archs.Run {
			return archs.Run{Context: float64(p.Int("n_ctx")), UBatch: float64(p.Int("n_ubatch"))}
		},
		Solve: func(s *estimate.Scope) {
			if s.Params.IsAuto("spec_type") {
				s.Params["spec_type"] = llamaAutoMethod(s)
			}
			if !s.Part {
				llamaWiden(s)
			}
		},
		States: func(s *estimate.Scope) ([]*v1.ParamState, string) {
			threads := cpuThreads(s.Host)
			gpus := float64(len(host.Kind(s.Host, v1.DeviceKind_DEVICE_KIND_GPU)))
			// The trained length for each parallel sequence, the most llama.cpp keeps per sequence
			trained := s.Model.ContextTrain * float64(llamaSequences(s))
			ctx := float64(s.Params.Int("n_ctx"))
			states := []*v1.ParamState{
				bounds("n_ctx", 256, trained, 256),
				bounds("n_parallel", -1, 256, 1),
				bounds("kv_unified_per_slot", 0, s.Model.ContextTrain, 256),
				bounds("slot_prompt_similarity", 0, 1, 0.05),
				bounds("n_predict", -1, ctx, 1),
				bounds("cache_ram", -1, float64(hostMemory(s.Host)>>20), 1024),
				bounds("ctx_checkpoints", 0, 256, 1),
				bounds("checkpoint_min_step", 0, ctx, 256),
				bounds("cache_reuse", 0, ctx, 32),
				bounds("n_gpu_layers", 0, s.Model.Layers, 1),
				bounds("n_cpu_moe", 0, s.Model.Layers, 1),
				bounds("n_cpu_ffn", 0, s.Model.Layers, 1),
				bounds("main_gpu", 0, max(gpus-1, 0), 1),
				bounds("threads", -1, threads, 1),
				bounds("threads_batch", -1, threads, 1),
				bounds("threads_http", -1, threads, 1),
				bounds("fit_ctx", 256, s.Model.ContextTrain, 256),
				bounds("n_batch", 32, ctx, 32),
				bounds("n_ubatch", 32, float64(s.Params.Int("n_batch")), 32),
				bounds("draft_n_ubatch", 0, float64(s.Params.Int("n_batch")), 32),
				bounds("yarn_orig_ctx", 0, s.Model.ContextTrain, 256),
				bounds("reasoning_budget", -1, ctx, 1),
				bounds("log_verbosity", 0, 5, 1),
				bounds("draft_max", 1, 64, 1),
				bounds("draft_min", 0, 64, 1),
				bounds("draft_threads", -1, threads, 1),
				bounds("draft_threads_batch", -1, threads, 1),
			}
			return states, ""
		},
	}
}

// Bytes of system memory the host profile reports across its host pools
func hostMemory(h *v1.HostProfile) uint64 {
	var total uint64
	for _, p := range h.GetPools() {
		if p.GetKind() == v1.PoolKind_POOL_KIND_HOST {
			total += p.GetTotalBytes()
		}
	}
	return total
}

// Sequences the cache pool is split over, one when every sequence draws from a unified pool,
// which llama.cpp turns on itself when it picks the slot count
func llamaSequences(s *estimate.Scope) int64 {
	parallel := s.Params.Int("n_parallel")
	if switchOn(s.Params, "kv_unified", parallel < 0) {
		return 1
	}
	return max(parallel, 1)
}

// Whether a vision or audio group is the projector file the param names
func llamaProjectorLoads(_ *estimate.Scope, g *v1.TensorGroup) bool {
	return g.GetId() == llamaProjectorGroup
}

// Slot count llama.cpp logs as it initializes its slots
func llamaSlots(lines []string) (string, bool) {
	for _, line := range lines {
		if !strings.Contains(line, "initializing") {
			continue
		}
		if n, ok := llamaLogged(line, "n_slots"); ok && n > 0 {
			return strconv.FormatUint(n, 10), true
		}
	}
	return "", false
}

// The whole number a log line gives a name, written name = number
func llamaLogged(line, name string) (uint64, bool) {
	i := strings.Index(line, name+" =")
	if i < 0 {
		return 0, false
	}
	f := strings.Fields(line[i+len(name)+2:])
	if len(f) == 0 {
		return 0, false
	}
	n, err := strconv.ParseUint(strings.TrimRight(f[0], ","), 10, 64)
	return n, err == nil
}

// The context the slot line implies, a slot's uncapped context times the slots unless they share one pool
func llamaSlotPool(lines []string) (string, bool) {
	const capped = "the slot context ("
	var uncapped uint64
	for _, line := range lines {
		if i := strings.Index(line, capped); i >= 0 {
			if digits, _, ok := strings.Cut(line[i+len(capped):], ")"); ok {
				uncapped, _ = strconv.ParseUint(digits, 10, 64)
			}
			continue
		}
		unified, split := strings.Contains(line, "kv_unified = 'true'"), strings.Contains(line, "kv_unified = 'false'")
		if !strings.Contains(line, "initializing") || unified == split {
			continue
		}
		slots, counted := llamaLogged(line, "n_slots")
		per, sized := llamaLogged(line, "n_ctx_slot")
		if !counted || !sized || slots == 0 || per == 0 {
			continue
		}
		if uncapped > 0 {
			per = uncapped
		}
		if split {
			per *= slots
		}
		return strconv.FormatUint(per, 10), true
	}
	return "", false
}

// Context size llama.cpp built, from its context line past the fit's debug trials or else its slot line
func llamaContext(lines []string) (string, bool) {
	for _, line := range lines {
		for _, prefix := range []string{"llama_context:", "llama_new_context_with_model:"} {
			i := strings.Index(line, prefix)
			if i < 0 || slices.Contains(strings.Fields(line[:i]), "D") {
				continue
			}
			name, value, ok := strings.Cut(line[i+len(prefix):], "=")
			if !ok || strings.TrimSpace(name) != "n_ctx" {
				continue
			}
			if n, err := strconv.ParseUint(strings.TrimSpace(value), 10, 64); err == nil {
				return strconv.FormatUint(n, 10), true
			}
		}
	}
	return llamaSlotPool(lines)
}
