package runtimes

import (
	"fmt"
	"maps"
	"os"
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

// Linux CUDA requires a source build. Published Linux GPU builds use Vulkan or ROCm.
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
		{ID: "adopt", Description: "Use an installed llama-server", Kind: v1.InstallKind_INSTALL_KIND_ADOPTED, Binaries: []string{"llama-server"}},
		{
			ID: "release", Description: "Download llama-server from GitHub releases", Kind: v1.InstallKind_INSTALL_KIND_PREBUILT, Releases: "ggml-org/llama.cpp",
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
		{ID: "source", Description: "Build llama-server for this host", Kind: v1.InstallKind_INSTALL_KIND_BUILT, RecipeID: "llamacpp"},
	}
}

// Supported cache types and bytes per element.
var llamaCacheTypes = []string{"f32", "f16", "bf16", "q8_0", "q5_1", "q5_0", "q4_1", "q4_0"}

// Cache type choices, empty leaving llama.cpp's own f16
var llamaCacheChoices = append([]string{""}, llamaCacheTypes...)

// Speculative methods --spec-type takes, none being llama.cpp's own default
var llamaSpecTypes = []string{"none", "draft-mtp", "draft-simple", "draft-eagle3", "draft-dflash", "draft-dspark", "ngram-mod", "ngram-simple", "ngram-map-k", "ngram-map-k4v", "ngram-cache"}

// Speculative methods that draft with a separate draft model file
var llamaDraftFileTypes = []string{"draft-simple", "draft-eagle3", "draft-dflash", "draft-dspark"}

// Draft architectures whose layers read the target's cache instead of keeping their own
var llamaSharedCacheArchs = []string{"gemma4-assistant"}

// Draft architectures that are MTP head files without nextn tensors, drafting only with draft-mtp
var llamaHeadArchs = []string{"gemma4-assistant"}

// Draft architecture llama.cpp reads draft-dflash or draft-dspark from on its own
const llamaDFlashArch = "dflash"

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
const llamaContextRule = "llama.cpp's own choice, the trained length for each parallel sequence cut to fit free device memory but not below 4096 tokens"

var llamaCacheBytes = map[string]float64{"f32": 4, "f16": 2, "bf16": 2, "q8_0": 1.0625, "q5_1": 0.75, "q5_0": 0.6875, "q4_1": 0.625, "q4_0": 0.5625}

// Quantized value caches require flash attention.
var llamaQuantizedCache = []string{"q8_0", "q5_1", "q5_0", "q4_1", "q4_0"}

func (LlamaCpp) Params() []*v1.Param {
	return []*v1.Param{
		{Name: "n_ctx", Label: "Context length", Type: v1.ParamType_PARAM_TYPE_INT, Default: Auto, Solved: true, Unit: "tokens", Min: 256, Step: 256, Group: "Context", Flag: "--ctx-size",
			Rule: llamaContextRule},
		{Name: "n_parallel", Label: "Parallel sequences", Type: v1.ParamType_PARAM_TYPE_INT, Default: "1", Unit: "sequences", Min: 1, Max: 256, Step: 1, Group: "Context", Flag: "--parallel"},
		{Name: "n_gpu_layers", Label: "GPU layers", Type: v1.ParamType_PARAM_TYPE_INT, Default: Auto, Solved: true, Unit: "layers", Min: 0, Step: 1, Group: "Placement", Flag: "--n-gpu-layers",
			Rule: "fit layers on device and place the rest in system memory"},
		{Name: "n_cpu_moe", Label: "Expert layers on CPU", Type: v1.ParamType_PARAM_TYPE_INT, Default: Auto, Solved: true, Unit: "layers", Min: 0, Step: 1, Group: "Placement", Flag: "--n-cpu-moe",
			Rule: "offload expert layers that exceed remaining device memory"},
		{Name: "device", Label: "Devices", Type: v1.ParamType_PARAM_TYPE_STRING, Group: "Placement", Advanced: true, Flag: "--device",
			Description: "CUDA0,CUDA1, or none"},
		{Name: "threads", Label: "CPU threads", Type: v1.ParamType_PARAM_TYPE_INT, Default: "-1", Unit: "threads", Min: -1, Step: 1, Group: "Placement", Advanced: true, Flag: "--threads",
			Description: "-1 for the core count"},
		{Name: "cache_type_k", Label: "Key cache type", Type: v1.ParamType_PARAM_TYPE_STRING, Choices: llamaCacheChoices, Group: "Cache", Flag: "--cache-type-k",
			Description: "empty keeps llama.cpp's f16"},
		{Name: "cache_type_v", Label: "Value cache type", Type: v1.ParamType_PARAM_TYPE_STRING, Choices: llamaCacheChoices, Group: "Cache", Flag: "--cache-type-v",
			Description: "empty keeps llama.cpp's f16, a quantized type needs flash attention"},
		{Name: "flash_attn", Label: "Flash attention", Type: v1.ParamType_PARAM_TYPE_STRING, Default: "auto", Choices: []string{"auto", "on", "off"}, Group: "Cache", Flag: "--flash-attn"},
		{Name: "n_batch", Label: "Batch size", Type: v1.ParamType_PARAM_TYPE_INT, Default: "2048", Unit: "tokens", Min: 32, Step: 32, Group: "Batching", Advanced: true, Flag: "--batch-size"},
		{Name: "n_ubatch", Label: "Micro-batch size", Type: v1.ParamType_PARAM_TYPE_INT, Default: "512", Unit: "tokens", Min: 32, Step: 32, Group: "Batching", Advanced: true, Flag: "--ubatch-size"},
		{Name: "alias", Label: "Served name", Type: v1.ParamType_PARAM_TYPE_STRING, Group: "Identity", Flag: "--alias"},
		{Name: "mmproj", Label: "Projector", Type: v1.ParamType_PARAM_TYPE_PATH, Group: "Identity", Advanced: true, Flag: "--mmproj", Picks: "projector",
			Description: "a stored projector or GGUF path for image and audio input, empty serves text only"},
		{Name: "log_verbosity", Label: "Log level", Type: v1.ParamType_PARAM_TYPE_INT, Default: "4", Min: 0, Max: 5, Step: 1, Group: "Diagnostics", Advanced: true, Flag: "--log-verbosity"},
		{Name: "spec_type", Label: "Speculative method", Type: v1.ParamType_PARAM_TYPE_STRING, Default: Auto, Solved: true, Choices: llamaSpecTypes, Group: "Speculative decoding", Advanced: true,
			Description: "draft-mtp uses the model's MTP heads or a head file, other draft methods a draft model, ngram methods the prompt",
			Rule:        "the method the draft model is made for, draft-mtp for an MTP head file, draft-simple for a plain model, llama.cpp's own pick for a DFlash draft, none without a draft model"},
		{Name: "draft_model", Label: "Draft model", Type: v1.ParamType_PARAM_TYPE_PATH, Group: "Speculative decoding", Advanced: true, Picks: "draft",
			Description: "a smaller model of the same family, or an MTP head file such as mtp-*.gguf, as store://source/repo#group or a GGUF path"},
		{Name: "draft_max", Label: "Draft tokens per round", Type: v1.ParamType_PARAM_TYPE_INT, Unit: "tokens", Min: 1, Max: 64, Step: 1, Group: "Speculative decoding", Advanced: true,
			Description: "empty keeps llama.cpp's 3"},
		{Name: "direct_io", Label: "Direct file reads", Type: v1.ParamType_PARAM_TYPE_BOOL, Default: "false", Group: "Placement", Advanced: true,
			Description: "read weights without the page cache, for shares over 100 GiB on unified memory"},
		{Name: "cuda_disable_graphs", Label: "Disable CUDA graphs", Type: v1.ParamType_PARAM_TYPE_BOOL, Default: "false", Group: "Diagnostics", Advanced: true, Env: "GGML_CUDA_DISABLE_GRAPHS",
			Description: "for a driver that leaks compute graphs under the rpc server"},
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
	// Host placement disables GPU offload and hides accelerators.
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
	// Tools need the jinja renderer, which older builds turn on by this flag alone
	if has(in.InstallRecord, factJinja) {
		args = append(args, in.InstallRecord.GetFacts()[factJinja])
	}
	spec, specEmitted, err := llamaSpeculative(in, p)
	if err != nil {
		return nil, err
	}
	flags, env, emitted := Flags(r.Params(), p)
	flags = append(flags, spec...)
	maps.Copy(emitted, specEmitted)
	// Reading without the page cache has two spellings across builds, recorded by the install's facts.
	if p.Bool("direct_io") {
		if !has(in.InstallRecord, factDirectIO) {
			return nil, fmt.Errorf("%w: this llama-server lists no direct io flag, so it cannot read weights without the page cache", ErrParam)
		}
		flags = append(flags, directIOArgs(in.InstallRecord.GetFacts()[factDirectIO])...)
	}
	if hostOnly {
		hideDevices(env)
	} else {
		// Restrict devices only when a slot assigns them.
		setEnv(env, "CUDA_VISIBLE_DEVICES", visible(in.Devices, "nvidia", false))
		setEnv(env, "ROCR_VISIBLE_DEVICES", visible(in.Devices, "amd", true))
		setEnv(env, "GGML_VK_VISIBLE_DEVICES", visible(in.Devices, "", true))
	}
	return &Command{Command: in.Install.Path, Args: append(args, flags...), Env: env, Params: emitted}, nil
}

// Speculative flags as this install spells them, refusing settings llama-server would reject or leave idle unless forced
func llamaSpeculative(in Launch, p estimate.Params) ([]string, map[string]string, error) {
	method, draft, tokens := p.Str("spec_type"), llamaFile(p, "draft_model"), p["draft_max"]
	if method == "" {
		method = Auto
	}
	delete(p, "spec_type")
	delete(p, "draft_model")
	delete(p, "draft_max")
	if !in.Force {
		if refusal := llamaSpecRefusal(in.Descriptor, method, draft != "" || in.Draft != "", tokens != nil); refusal != "" {
			return nil, nil, fmt.Errorf("%w: %s", ErrParam, refusal)
		}
	}
	facts := in.InstallRecord.GetFacts()
	listed := has(in.InstallRecord, factSpecTypes)
	var args []string
	emitted := map[string]string{}
	switch {
	case method == Auto || method == "none":
		// None is llama.cpp's default, and auto leaves it to read the method from the draft file
	case listed:
		if types := strings.Split(facts[factSpecTypes], ","); !slices.Contains(types, method) {
			return nil, nil, fmt.Errorf("%w: this llama-server's --spec-type takes %s, not %s", ErrParam, strings.Join(types, ", "), method)
		}
		args = append(args, "--spec-type", method)
		emitted["spec_type"] = method
	case method == "draft-simple" && (draft != "" || in.Draft != ""):
		// A build before --spec-type drafts plainly with any draft model it loads
		emitted["spec_type"] = method
	default:
		return nil, nil, fmt.Errorf("%w: this llama-server lists no --spec-type, so it cannot run %s", ErrParam, method)
	}
	if draft != "" {
		if !has(in.InstallRecord, factDraftModel) {
			return nil, nil, fmt.Errorf("%w: this llama-server lists no draft model flag, so it cannot run speculative decoding", ErrParam)
		}
		args = append(args, facts[factDraftModel], draft)
		emitted["draft_model"] = draft
	}
	if tokens != nil {
		if !has(in.InstallRecord, factDraftMax) {
			return nil, nil, fmt.Errorf("%w: this llama-server lists no draft token limit flag", ErrParam)
		}
		n := fmt.Sprint(tokens)
		args = append(args, facts[factDraftMax], n)
		emitted["draft_max"] = n
	}
	return args, emitted, nil
}

// Why speculative settings cannot run on the model, empty when they can
func llamaSpecRefusal(d *v1.Descriptor, method string, drafted, tokens bool) string {
	switch {
	case slices.Contains(llamaDraftFileTypes, method) && !drafted:
		return method + " drafts with a draft model, so draft_model needs one"
	case method == "draft-mtp" && !drafted && !llamaHeads(d):
		return "the model's file carries no MTP heads, so draft-mtp needs an MTP head file such as mtp-*.gguf as draft_model"
	case (method == "none" || strings.HasPrefix(method, "ngram-")) && drafted:
		return method + " leaves a draft model idle, so clear draft_model or set spec_type to a draft method"
	case tokens && !strings.HasPrefix(method, "draft-") && !(method == Auto && drafted):
		return "draft tokens per round sets how far a draft model or MTP heads look ahead, so spec_type needs a draft method"
	}
	return ""
}

// The method an unset spec_type takes, the one the draft file is made for, auto where llama.cpp reads it
func llamaAutoMethod(s *estimate.Scope) string {
	switch d := s.Draft; {
	case llamaFile(s.Params, "draft_model") == "":
		return "none"
	case d == nil || d.GetArchitecture() == llamaDFlashArch:
		return Auto
	case llamaHeads(d) || slices.Contains(llamaHeadArchs, d.GetArchitecture()):
		return "draft-mtp"
	}
	return "draft-simple"
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
	return Health{Path: "/health", Interval: time.Second, Timeout: 10 * time.Minute}
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
func llamaMTP(p estimate.Params) bool { return p.Str("spec_type") == "draft-mtp" }

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
		ContextStep:  256,
		ContextMax:   func(s *estimate.Scope) int64 { return int64(s.Model.ContextTrain) },
		// Parallel sequences each get the trained length, as llama.cpp splits an unset context over them
		ContextScale: func(s *estimate.Scope) int64 { return s.Params.Int("n_parallel") },
		Deferred:     map[string]func([]string) (string, bool){"n_ctx": llamaContext},
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
			// The trained length for each parallel sequence, the most llama.cpp keeps per sequence
			trained := s.Model.ContextTrain * float64(max(s.Params.Int("n_parallel"), 1))
			states := []*v1.ParamState{
				bounds("n_ctx", 256, trained, 256),
				bounds("n_parallel", 1, 256, 1),
				bounds("n_gpu_layers", 0, s.Model.Layers, 1),
				bounds("n_cpu_moe", 0, s.Model.Layers, 1),
				bounds("threads", -1, threads, 1),
				bounds("n_batch", 32, float64(s.Params.Int("n_ctx")), 32),
				bounds("n_ubatch", 32, float64(s.Params.Int("n_batch")), 32),
				bounds("log_verbosity", 0, 5, 1),
				bounds("draft_max", 1, 64, 1),
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
			if refusal == "" && !s.Part {
				refusal = llamaFileRefusal(s)
			}
			return append(states, value), refusal
		},
	}
}

// Whether a vision or audio group is the projector file the param names
func llamaProjectorLoads(_ *estimate.Scope, g *v1.TensorGroup) bool {
	return g.GetId() == llamaProjectorGroup
}

// Why the files or speculative settings the params name cannot run, empty when they can
func llamaFileRefusal(s *estimate.Scope) string {
	if path := llamaFile(s.Params, "mmproj"); path != "" {
		if info, err := os.Stat(path); err != nil {
			return "projector " + err.Error()
		} else if info.IsDir() {
			return "projector " + path + " is a directory, not a GGUF file"
		}
	}
	return llamaSpecRefusal(s.Descriptor, s.Params.Str("spec_type"), llamaFile(s.Params, "draft_model") != "", s.Params["draft_max"] != nil)
}

// Context size llama.cpp logs for the target, the first context it builds past the fit's debug level trials
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
	return "", false
}
