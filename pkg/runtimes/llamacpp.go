package runtimes

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/nickheyer/nebu/pkg/archs"
	"github.com/nickheyer/nebu/pkg/estimate"
	"github.com/nickheyer/nebu/pkg/host"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/text"
	"github.com/nickheyer/nebu/pkg/triage"
)

// llama.cpp's server, which runs GGUF models on the CPU and on any GPU through CUDA, ROCm, Vulkan, or Metal
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

// llama.cpp publishes no CUDA build for Linux, so an NVIDIA host takes Vulkan from the releases or builds from source
func (LlamaCpp) Methods() []Method {
	linux := func(arch string, more func(*v1.HostProfile) bool) func(*v1.HostProfile) bool {
		return func(h *v1.HostProfile) bool { return host.Is(h, "linux", arch) && (more == nil || more(h)) }
	}
	windows := func(arch string, more func(*v1.HostProfile) bool) func(*v1.HostProfile) bool {
		return func(h *v1.HostProfile) bool { return host.Is(h, "windows", arch) && (more == nil || more(h)) }
	}
	amd := func(h *v1.HostProfile) bool { return host.HasVendor(h, "amd") }
	nvidia := func(h *v1.HostProfile) bool { return host.HasVendor(h, "nvidia") }
	// The CUDA 13 build needs a 580 driver
	nvidia580 := func(h *v1.HostProfile) bool {
		return nvidia(h) && text.CompareVersions(driverVersion(h, "nvidia"), "580") >= 0
	}
	tar := func(contains string) Asset { return Asset{Prefix: "llama-b", Contains: contains, Suffix: ".tar.gz"} }
	zip := func(contains, suffix string) Asset {
		return Asset{Prefix: "llama-b", Contains: contains, Suffix: suffix}
	}
	return []Method{
		{ID: "adopt", Description: "Records a llama-server already on this host; nothing is downloaded or built", Kind: v1.InstallKind_INSTALL_KIND_ADOPTED, Binaries: []string{"llama-server"}},
		{
			ID: "release", Description: "A prebuilt llama-server from the GitHub releases of ggml-org/llama.cpp", Kind: v1.InstallKind_INSTALL_KIND_PREBUILT, Releases: "ggml-org/llama.cpp",
			Rules: []PrebuiltRule{
				{ID: "linux-rocm", Applies: linux("amd64", amd), Assets: []Asset{{Prefix: "llama-b", Contains: "-bin-ubuntu-rocm-", Suffix: "-x64.tar.gz"}}, Binary: "llama-server"},
				{ID: "linux-vulkan", Applies: linux("amd64", host.HasGPU), Assets: []Asset{tar("-bin-ubuntu-vulkan-x64")}, Binary: "llama-server"},
				{ID: "linux-cpu", Applies: linux("amd64", nil), Assets: []Asset{tar("-bin-ubuntu-x64")}, Binary: "llama-server"},
				{ID: "linux-arm64-vulkan", Applies: linux("arm64", host.HasGPU), Assets: []Asset{tar("-bin-ubuntu-vulkan-arm64")}, Binary: "llama-server"},
				{ID: "linux-arm64-cpu", Applies: linux("arm64", nil), Assets: []Asset{tar("-bin-ubuntu-arm64")}, Binary: "llama-server"},
				{ID: "macos-arm64", Applies: func(h *v1.HostProfile) bool { return host.Is(h, "darwin", "arm64") }, Assets: []Asset{tar("-bin-macos-arm64")}, Binary: "llama-server"},
				{ID: "macos-x64", Applies: func(h *v1.HostProfile) bool { return host.Is(h, "darwin", "amd64") }, Assets: []Asset{tar("-bin-macos-x64")}, Binary: "llama-server"},
				{ID: "windows-cuda13", Applies: windows("amd64", nvidia580), Assets: []Asset{zip("-bin-win-cuda-13.", "-x64.zip"), {Prefix: "cudart-llama-bin-win-cuda-13.", Suffix: "-x64.zip"}}, Binary: "llama-server.exe"},
				{ID: "windows-cuda12", Applies: windows("amd64", nvidia), Assets: []Asset{zip("-bin-win-cuda-12.", "-x64.zip"), {Prefix: "cudart-llama-bin-win-cuda-12.", Suffix: "-x64.zip"}}, Binary: "llama-server.exe"},
				{ID: "windows-arm64-cuda", Applies: windows("arm64", nvidia), Assets: []Asset{zip("-bin-win-cuda-", "-arm64.zip"), {Prefix: "cudart-llama-bin-win-cuda-", Suffix: "-arm64.zip"}}, Binary: "llama-server.exe"},
				{ID: "windows-rocm", Applies: windows("amd64", amd), Assets: []Asset{zip("-bin-win-rocm-", "-x64.zip")}, Binary: "llama-server.exe"},
				{ID: "windows-vulkan", Applies: windows("amd64", host.HasGPU), Assets: []Asset{zip("-bin-win-vulkan-x64", ".zip")}, Binary: "llama-server.exe"},
				{ID: "windows-cpu", Applies: windows("amd64", nil), Assets: []Asset{zip("-bin-win-cpu-x64", ".zip")}, Binary: "llama-server.exe"},
				{ID: "windows-arm64-cpu", Applies: windows("arm64", nil), Assets: []Asset{zip("-bin-win-cpu-arm64", ".zip")}, Binary: "llama-server.exe"},
			},
		},
		{ID: "source", Description: "Compiles llama-server with the backend for the devices on this host", Kind: v1.InstallKind_INSTALL_KIND_BUILT, RecipeID: "llamacpp"},
	}
}

// The element types the cache can be kept in, with the bytes each takes per element
var llamaCacheTypes = []string{"f32", "f16", "bf16", "q8_0", "q5_1", "q5_0", "q4_1", "q4_0"}

var llamaCacheBytes = map[string]float64{"f32": 4, "f16": 2, "bf16": 2, "q8_0": 1.0625, "q5_1": 0.75, "q5_0": 0.6875, "q4_1": 0.625, "q4_0": 0.5625}

// The quantized types, which the value cache can only take with flash attention on
var llamaQuantizedCache = []string{"q8_0", "q5_1", "q5_0", "q4_1", "q4_0"}

func (LlamaCpp) Params() []*v1.Param {
	return []*v1.Param{
		{Name: "n_ctx", Label: "Context length", Type: v1.ParamType_PARAM_TYPE_INT, Default: Auto, Solved: true, Unit: "tokens", Min: 256, Step: 256, Group: "Context", Flag: "--ctx-size",
			Rule: contextRule},
		{Name: "n_parallel", Label: "Parallel sequences", Type: v1.ParamType_PARAM_TYPE_INT, Default: "1", Unit: "sequences", Min: 1, Max: 256, Step: 1, Group: "Context", Flag: "--parallel"},
		{Name: "n_gpu_layers", Label: "GPU layers", Type: v1.ParamType_PARAM_TYPE_INT, Default: Auto, Solved: true, Unit: "layers", Min: 0, Step: 1, Group: "Placement", Flag: "--n-gpu-layers",
			Rule: "as many layers as fit on the device, the rest in system memory"},
		{Name: "n_cpu_moe", Label: "Expert layers on CPU", Type: v1.ParamType_PARAM_TYPE_INT, Default: Auto, Solved: true, Unit: "layers", Min: 0, Step: 1, Group: "Placement", Flag: "--n-cpu-moe",
			Rule: "the expert layers the device cannot hold once the layers are placed"},
		{Name: "device", Label: "Devices", Type: v1.ParamType_PARAM_TYPE_STRING, Group: "Placement", Advanced: true, Flag: "--device",
			Description: "CUDA0,CUDA1, or none"},
		{Name: "threads", Label: "CPU threads", Type: v1.ParamType_PARAM_TYPE_INT, Default: "-1", Unit: "threads", Min: -1, Step: 1, Group: "Placement", Advanced: true, Flag: "--threads",
			Description: "-1 for the core count"},
		{Name: "cache_type_k", Label: "Key cache type", Type: v1.ParamType_PARAM_TYPE_STRING, Default: Auto, Solved: true, Choices: llamaCacheTypes, Group: "Cache", Flag: "--cache-type-k",
			Rule: "q8_0 for quantized weights, f16 for weights kept at 16 bits or more"},
		{Name: "cache_type_v", Label: "Value cache type", Type: v1.ParamType_PARAM_TYPE_STRING, Default: Auto, Solved: true, Choices: llamaCacheTypes, Group: "Cache", Flag: "--cache-type-v",
			Rule: "q8_0 for quantized weights with flash attention on, f16 otherwise"},
		{Name: "flash_attn", Label: "Flash attention", Type: v1.ParamType_PARAM_TYPE_STRING, Default: "auto", Choices: []string{"auto", "on", "off"}, Group: "Cache", Flag: "--flash-attn"},
		{Name: "n_batch", Label: "Batch size", Type: v1.ParamType_PARAM_TYPE_INT, Default: "2048", Unit: "tokens", Min: 32, Step: 32, Group: "Batching", Advanced: true, Flag: "--batch-size"},
		{Name: "n_ubatch", Label: "Micro-batch size", Type: v1.ParamType_PARAM_TYPE_INT, Default: "512", Unit: "tokens", Min: 32, Step: 32, Group: "Batching", Advanced: true, Flag: "--ubatch-size"},
		{Name: "alias", Label: "Served name", Type: v1.ParamType_PARAM_TYPE_STRING, Group: "Identity", Flag: "--alias"},
		{Name: "mmproj", Label: "Projector", Type: v1.ParamType_PARAM_TYPE_PATH, Group: "Identity", Advanced: true, Flag: "--mmproj", Picks: "projector"},
		{Name: "log_verbosity", Label: "Log level", Type: v1.ParamType_PARAM_TYPE_INT, Default: "4", Min: 0, Max: 5, Step: 1, Group: "Diagnostics", Advanced: true, Flag: "--log-verbosity"},
	}
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
	if p.Str("mmproj") == "" {
		if proj := in.Artifacts["projector"]; proj != "" {
			p["mmproj"] = proj
		}
	}
	// A slot that keeps the model in host memory offloads to no device and hides every accelerator from the process
	hostOnly := in.Placement == v1.Placement_PLACEMENT_HOST
	if hostOnly {
		switch p.Str("device") {
		case "", "none":
			p["device"] = "none"
		default:
			return nil, fmt.Errorf("%w: device %s names an accelerator, but the slot keeps the model in host memory", ErrParam, p.Str("device"))
		}
	}
	args := []string{"--model", weights, "--host", in.Host, "--port", strconv.Itoa(in.Port)}
	flags, env, emitted := Flags(r.Params(), p)
	if hostOnly {
		hideDevices(env)
	} else {
		// A slot pins the process to its devices, no slot means every device
		setEnv(env, "CUDA_VISIBLE_DEVICES", visible(in.Devices, "nvidia", false))
		setEnv(env, "ROCR_VISIBLE_DEVICES", visible(in.Devices, "amd", true))
		setEnv(env, "GGML_VK_VISIBLE_DEVICES", visible(in.Devices, "", true))
	}
	return &Command{Command: in.Install.Path, Args: append(args, flags...), Env: env, Params: emitted}, nil
}

func (LlamaCpp) Prepares(string) bool             { return false }
func (LlamaCpp) Prepare(Launch) (*Command, error) { return nil, nil }
func (LlamaCpp) PrepareTimeout() time.Duration    { return 0 }
func (LlamaCpp) Health() Health {
	return Health{Path: "/health", Interval: time.Second, Timeout: 10 * time.Minute}
}
func (LlamaCpp) StopGrace() time.Duration { return 15 * time.Second }
func (LlamaCpp) Triage() []triage.Set     { return []triage.Set{triage.LlamaCpp{}} }

func (LlamaCpp) Probes() []Probe {
	return []Probe{
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
				// Each device is an indented line naming the backend device, CUDA0: NVIDIA ... (12345 MiB)
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
	}
}

// llama.cpp logs one line per backend buffer: the model, the KV cache, and the compute buffer, each sized
// on a device backend such as CUDA0 or on the host, CPU or a pinned CUDA_Host buffer
func (LlamaCpp) Measure(lines []string) []*v1.Measurement {
	var m measurements
	for _, line := range lines {
		for phrase, what := range map[string]string{" model buffer size = ": "weights", " KV buffer size = ": "cache", " compute buffer size = ": "compute"} {
			i := strings.Index(line, phrase)
			if i < 0 {
				continue
			}
			fields := strings.Fields(line[:i])
			if len(fields) == 0 {
				continue
			}
			backend := fields[len(fields)-1]
			n, ok := bytesAfter(line, phrase)
			if !ok {
				continue
			}
			side := "device"
			if strings.HasPrefix(backend, "CPU") || strings.HasSuffix(backend, "_Host") {
				side = "host"
			}
			m.add(side+"."+what, n, line)
		}
	}
	return m.list
}

// The cache follows the weights: quantized weights take an 8 bit cache, which loses nothing they kept,
// and full width weights keep a full width cache; the value cache only quantizes with flash attention on
func llamaCacheType(s *estimate.Scope, value bool) string {
	if s.Descriptor.GetBitsPerWeight() > 8 || (value && s.Params.Str("flash_attn") == "off") {
		return "f16"
	}
	return "q8_0"
}

func (LlamaCpp) Policy() *estimate.Policy {
	device := v1.PoolKind_POOL_KIND_DEVICE
	return &estimate.Policy{
		Groups: []estimate.GroupRule{
			{Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_EMBEDDING, Pool: v1.PoolKind_POOL_KIND_HOST},
			{Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_LAYER, Pool: device, Param: "n_gpu_layers", SpillPriority: 10},
			{Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_OUTPUT, Pool: device, Param: "n_gpu_layers", SpillPriority: 10},
			{Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_EXPERTS, Pool: device, Param: "n_cpu_moe", ParamCountsHost: true, SpillPriority: 1, Requires: v1.TensorGroupKind_TENSOR_GROUP_KIND_LAYER},
			// The projector loads whole on device beside the layers
			{Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_VISION, Pool: device},
			{Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_AUDIO, Pool: device},
			// Prediction head tensors are skipped at load, no decoding path drafts with them
			{Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_DRAFT, Pool: device, Loaded: func(*estimate.Scope) bool { return false }},
			{Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_OTHER, Pool: device},
		},
		CacheBytes: func(s *estimate.Scope) uint64 {
			k, v := llamaCacheBytes[s.Params.Str("cache_type_k")], llamaCacheBytes[s.Params.Str("cache_type_v")]
			return uint64(float64(s.Params.Int("n_ctx")) * s.CachePerToken * (k + v) / 2)
		},
		// The device context plus the compute buffer, fitted to measured CUDA runs with flash attention on
		OverheadBytes: func(s *estimate.Scope) uint64 {
			ctx, ubatch := float64(s.Params.Int("n_ctx")), float64(s.Params.Int("n_ubatch"))
			return uint64(384*(1<<20) + ctx*ubatch*10 + ubatch*s.Model.Embedding*16)
		},
		Margin:       0.05,
		ContextParam: "n_ctx",
		ContextMin:   256,
		ContextStep:  256,
		ContextMax:   func(s *estimate.Scope) int64 { return int64(s.Model.ContextTrain) },
		Shape: func(p estimate.Params) archs.Run {
			return archs.Run{Context: float64(p.Int("n_ctx")), UBatch: float64(p.Int("n_ubatch"))}
		},
		Solve: func(s *estimate.Scope) {
			if s.Params.IsAuto("cache_type_k") {
				s.Params["cache_type_k"] = llamaCacheType(s, false)
			}
			if s.Params.IsAuto("cache_type_v") {
				s.Params["cache_type_v"] = llamaCacheType(s, true)
			}
		},
		States: func(s *estimate.Scope) ([]*v1.ParamState, string) {
			threads := cpuThreads(s.Host)
			states := []*v1.ParamState{
				bounds("n_ctx", 256, s.Model.ContextTrain, 256),
				bounds("n_parallel", 1, 256, 1),
				bounds("n_gpu_layers", 0, s.Model.Layers, 1),
				bounds("n_cpu_moe", 0, s.Model.Layers, 1),
				bounds("threads", -1, threads, 1),
				bounds("n_batch", 32, float64(s.Params.Int("n_ctx")), 32),
				bounds("n_ubatch", 32, float64(s.Params.Int("n_batch")), 32),
				bounds("log_verbosity", 0, 5, 1),
			}
			value := &v1.ParamState{Name: "cache_type_v"}
			refusal := ""
			if s.Params.Str("flash_attn") == "off" {
				const why = "a quantized value cache needs flash attention on"
				for _, q := range llamaQuantizedCache {
					value.Disabled = append(value.Disabled, &v1.DisabledChoice{Value: q, Message: why})
					if s.Params.Str("cache_type_v") == q {
						refusal = "value cache type " + q + ": " + why
					}
				}
			}
			return append(states, value), refusal
		},
	}
}
