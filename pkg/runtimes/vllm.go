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
	"github.com/nickheyer/nebu/pkg/triage"
)

// vLLM, serving safetensors checkpoints at high throughput on NVIDIA and AMD GPUs
type VLLM struct{}

func (VLLM) ID() string   { return "vllm" }
func (VLLM) Name() string { return "vLLM" }
func (VLLM) Description() string {
	return "Serves safetensors checkpoints at high throughput on NVIDIA and AMD GPUs"
}
func (VLLM) Formats() []string  { return []string{"safetensors"} }
func (VLLM) Kind() v1.ModelKind { return v1.ModelKind_MODEL_KIND_LANGUAGE }
func (VLLM) API() v1.ApiFlavor  { return v1.ApiFlavor_API_FLAVOR_OPENAI }
func (VLLM) Requirements() []string {
	return []string{"an NVIDIA or AMD GPU"}
}

func (VLLM) Unmet(h *v1.HostProfile) []string {
	if host.HasVendor(h, "nvidia") || host.HasVendor(h, "amd") {
		return nil
	}
	return []string{"an NVIDIA or AMD GPU"}
}

func (VLLM) Methods() []Method {
	return []Method{
		{ID: "adopt", Description: "Use an installed vllm", Kind: v1.InstallKind_INSTALL_KIND_ADOPTED, Binaries: []string{"vllm"}},
		{ID: "source", Description: "Install vllm from its wheel index in a virtual environment", Kind: v1.InstallKind_INSTALL_KIND_BUILT, RecipeID: "vllm"},
	}
}

// The cache data types --kv-cache-dtype takes, as vLLM lists them
var vllmCacheDTypes = []string{"auto", "float16", "bfloat16", "fp8", "fp8_e4m3", "fp8_e5m2", "fp8_inc", "fp8_ds_mla", "nvfp4_ds_mla", "turboquant_k8v4", "turboquant_4bit_nc", "turboquant_k3v4_nc", "turboquant_3bit_nc", "octave_k3v4", "octave_k3v3", "octave_k3v3_compact", "ultraquant_4bit", "int4_per_token_head", "int8_per_token_head", "fp8_per_token_head", "nvfp4", "nvfp4_4over6"}

// Bytes per cache element of each type, the key and value averaged where they differ
var vllmCacheBytes = map[string]float64{
	"auto": 2, "float16": 2, "bfloat16": 2,
	"fp8": 1, "fp8_e4m3": 1, "fp8_e5m2": 1, "fp8_inc": 1, "fp8_ds_mla": 1, "fp8_per_token_head": 1, "int8_per_token_head": 1,
	"nvfp4": 0.5625, "nvfp4_ds_mla": 0.5625, "nvfp4_4over6": 0.5625, "int4_per_token_head": 0.5, "ultraquant_4bit": 0.5, "turboquant_4bit_nc": 0.5,
	"turboquant_k8v4": 0.75, "turboquant_k3v4_nc": 0.4375, "octave_k3v4": 0.4375, "turboquant_3bit_nc": 0.375, "octave_k3v3": 0.375, "octave_k3v3_compact": 0.375,
}

// The switches vllm serve spells as --flag and --no-flag, by param name
var vllmSwitches = map[string]string{
	"enable_prefix_caching":  "--enable-prefix-caching",
	"enable_chunked_prefill": "--enable-chunked-prefill",
	"async_scheduling":       "--async-scheduling",
}

func (VLLM) Params() []*v1.Param {
	const (
		groupContext     = "Context"
		groupMemory      = "Memory"
		groupParallel    = "Parallelism"
		groupModel       = "Model"
		groupIdentity    = "Identity"
		groupChat        = "Chat"
		groupAdapters    = "Adapters"
		groupSpeculative = GroupSpeculative
		groupDiagnostics = "Diagnostics"
		groupServing     = "Serving"
	)
	intParam := func(name, label, group, flag, def, unit, description string, min, max, step float64, advanced bool) *v1.Param {
		return &v1.Param{Name: name, Label: label, Type: v1.ParamType_PARAM_TYPE_INT, Default: def, Unit: unit, Min: min, Max: max, Step: step, Group: group, Flag: flag, Advanced: advanced, Description: description}
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
	mambaTypes := []string{"auto", "float32", "float16", "bfloat16"}
	return []*v1.Param{
		{Name: "n_ctx", Label: "Context length", Type: v1.ParamType_PARAM_TYPE_INT, Default: Auto, Solved: true, Unit: "tokens", Min: 256, Step: 256, Group: groupContext, Flag: "--max-model-len",
			Rule: contextRule},
		intParam("max_num_seqs", "Max sequences", groupContext, "--max-num-seqs", "", "sequences", "requests one scheduler step may run, empty keeps vLLM's own choice by the first device's memory", 1, 0, 1, false),
		intParam("max_num_batched_tokens", "Max batched tokens", groupContext, "--max-num-batched-tokens", "", "tokens", "tokens one scheduler step may hold, empty keeps vLLM's 2048, 8192 or 16384 on larger devices", 1, 0, 256, true),
		{Name: "enable_prefix_caching", Label: "Prefix caching", Type: v1.ParamType_PARAM_TYPE_STRING, Default: "on", Choices: switchChoices, Group: groupContext, Advanced: true,
			Description: "reuse cache blocks of a shared prompt prefix, which prefix aware routing counts on, empty keeps vLLM's on"},
		switchParam("enable_chunked_prefill", "Chunked prefill", groupContext, "split long prompts into chunks batched with decoding, empty keeps vLLM's on"),
		switchParam("async_scheduling", "Async scheduling", groupContext, "overlap scheduling with the model's forward pass, empty keeps vLLM's choice"),
		{Name: "gpu_memory_utilization", Label: "GPU memory fraction", Type: v1.ParamType_PARAM_TYPE_FLOAT, Min: 0.05, Max: 1, Step: 0.01, Group: groupMemory, Flag: "--gpu-memory-utilization",
			Description: "share of each device this instance takes for weights, cache, and activations, empty keeps vLLM's 0.92, or 1 when it sizes the cache from measured memory"},
		strParam("kv_cache_memory_bytes", "Cache memory", groupMemory, "--kv-cache-memory-bytes", "", "bytes of cache on each device, such as 4GiB, which replaces the memory fraction for the cache", nil, true),
		{Name: "kv_cache_dtype", Label: "KV cache type", Type: v1.ParamType_PARAM_TYPE_STRING, Default: "auto", Choices: vllmCacheDTypes, Group: groupMemory, Flag: "--kv-cache-dtype",
			Description: "auto keeps the model's type, fp8 needs CUDA 11.8 or ROCm, the 4 bit types need their kernels"},
		intParam("block_size", "Cache block size", groupMemory, "--block-size", "", "tokens", "tokens in one cache block, empty keeps vLLM's choice for the backend", 1, 0, 16, true),
		{Name: "cpu_offload_gb", Label: "Weights offloaded to host", Type: v1.ParamType_PARAM_TYPE_FLOAT, Min: 0, Step: 1, Group: groupMemory, Flag: "--cpu-offload-gb", Advanced: true, Unit: "GiB",
			Description: "weights kept in system memory and streamed each step, empty keeps vLLM's 0"},
		strParam("mamba_cache_dtype", "Mamba cache type", groupMemory, "--mamba-cache-dtype", "", "state type of Mamba layers, empty keeps vLLM's auto", append([]string{""}, mambaTypes...), true),
		strParam("mamba_ssm_cache_dtype", "Mamba SSM cache type", groupMemory, "--mamba-ssm-cache-dtype", "", "state type of the SSM state alone, empty follows the Mamba cache type", append([]string{""}, mambaTypes...), true),
		{Name: "tensor_parallel_size", Label: "Tensor parallel", Type: v1.ParamType_PARAM_TYPE_INT, Default: "1", Unit: "devices", Min: 1, Step: 1, Group: groupParallel, Flag: "--tensor-parallel-size"},
		intParam("pipeline_parallel_size", "Pipeline parallel", groupParallel, "--pipeline-parallel-size", "", "stages", "empty keeps vLLM's 1, a chain formation sets it per rank", 1, 0, 1, true),
		intParam("data_parallel_size", "Data parallel", groupParallel, "--data-parallel-size", "", "ranks", "ranks each serving its own port on tensor parallel devices of its own, sharing a mixture of experts' experts, empty keeps one", 1, 0, 1, true),
		boolParam("enable_expert_parallel", "Expert parallel", groupParallel, "--enable-expert-parallel", "shard experts over the tensor parallel devices instead of replicating them"),
		strParam("distributed_executor_backend", "Executor backend", groupParallel, "--distributed-executor-backend", "", "how workers are spawned, empty keeps vLLM's choice, a formation sets mp", []string{"", "mp", "ray", "uni", "external_launcher"}, true),
		strParam("dtype", "Weight type", groupModel, "--dtype", "", "type weights and activations run in, empty keeps vLLM's auto, bfloat16 for bfloat16 models and float16 otherwise", []string{"", "auto", "half", "float16", "bfloat16", "float", "float32"}, false),
		strParam("quantization", "Quantization", groupModel, "--quantization", "", "quantization method of the weights, such as awq, gptq, fp8, empty keeps the checkpoint's", nil, true),
		strParam("load_format", "Load format", groupModel, "--load-format", "", "weight file format to load, empty keeps vLLM's auto", nil, true),
		strParam("tokenizer_mode", "Tokenizer mode", groupModel, "--tokenizer-mode", "", "auto, hf, slow, mistral, or custom, empty keeps vLLM's auto", nil, true),
		strParam(vllmRunner, "Model runner", groupModel, "--runner", "", "generate, pooling or draft, empty keeps vLLM's choice by architecture, read back from its log", []string{"", "generate", "pooling", "draft"}, true),
		strParam(vllmConvert, "Model conversion", groupModel, "--convert", "", "none, embed or classify adapts the model to a pooling task, empty keeps vLLM's choice by architecture, read back from its log", []string{"", "none", "embed", "classify"}, true),
		boolParam("trust_remote_code", "Trust remote code", groupModel, "--trust-remote-code", "run the modeling code a checkpoint ships"),
		boolParam("enforce_eager", "Eager execution", groupModel, "--enforce-eager", "run without CUDA graphs, slower decoding and faster starts"),
		intParam("seed", "Seed", groupModel, "--seed", "", "", "sampling seed, empty keeps vLLM's 0", 0, 0, 1, true),
		intParam("max_logprobs", "Max logprobs", groupModel, "--max-logprobs", "", "", "most log probabilities a request may ask for, empty keeps vLLM's 20", 0, 0, 1, true),
		strParam("hf_overrides", "Config overrides", groupModel, "--hf-overrides", "", "a JSON object merged over the checkpoint's config", nil, true),
		strParam("limit_mm_per_prompt", "Media per prompt", groupModel, "--limit-mm-per-prompt", "", "a JSON object of how many items of each modality a prompt may carry, such as {\"image\":4}", nil, true),
		boolParam("enable_prompt_embeds", "Prompt embeddings", groupModel, "--enable-prompt-embeds", "accept prompts given as embeddings"),
		boolParam("disable_sliding_window", "Disable sliding window", groupModel, "--disable-sliding-window", "cap the context at the sliding window instead of using it"),
		boolParam("enable_sleep_mode", "Sleep mode", groupModel, "--enable-sleep-mode", "allow the engine to release device memory while asleep, serving the sleep and wake endpoints"),
		strParam("compilation_config", "Compilation config", groupModel, "--compilation-config", "", "a compilation level 0 to 3 or a JSON object for torch.compile", nil, true),
		{Name: "served_model_name", Label: "Served name", Type: v1.ParamType_PARAM_TYPE_STRING, Group: groupIdentity, Flag: "--served-model-name"},
		strParam("generation_config", "Generation config", groupChat, "--generation-config", "", "auto takes the checkpoint's generation config as sampling defaults, vllm takes vLLM's own, or a path to one, empty keeps vLLM's auto", nil, true),
		strParam("override_generation_config", "Generation overrides", groupChat, "--override-generation-config", "", "a JSON object of sampling defaults over the generation config", nil, true),
		strParam("reasoning_parser", "Reasoning parser", groupChat, "--reasoning-parser", "", "parser that splits reasoning from the answer, such as deepseek_r1 or qwen3, empty leaves reasoning in the content", nil, true),
		strParam("tool_call_parser", "Tool call parser", groupChat, "", "", "parser for tool calls, empty picks one for the model family", nil, true),
		boolParam("enable_lora", "LoRA adapters", groupAdapters, "--enable-lora", "accept LoRA adapters named by requests and loaded at runtime"),
		fileList(strParam("lora_modules", "Adapters at launch", groupAdapters, "", "", "name=path pairs, comma separated, served from the start and turning LoRA on", nil, true), v1.ParamList_PARAM_LIST_NAMED),
		intParam("max_loras", "Max adapters per batch", groupAdapters, "--max-loras", "", "adapters", "empty keeps vLLM's 1", 1, 0, 1, true),
		intParam("max_lora_rank", "Max adapter rank", groupAdapters, "--max-lora-rank", "", "", "empty keeps vLLM's 16", 1, 0, 8, true),
		intParam("max_cpu_loras", "Adapters held in host memory", groupAdapters, "--max-cpu-loras", "", "adapters", "empty keeps vLLM's same as max_loras", 1, 0, 1, true),
		strParam("speculative_config", "Speculative config", groupSpeculative, "--speculative-config", "", "a JSON object such as {\"method\":\"ngram\",\"num_speculative_tokens\":4}, methods ngram, suffix, draft_model, eagle, eagle3, dflash, dspark, or an MTP type", nil, true),
		boolParam("disable_log_stats", "Disable stats", groupDiagnostics, "--disable-log-stats", "stop recording engine statistics, emptying the queue and cache gauges of the metrics endpoint vLLM always serves"),
		boolParam("grpc", "gRPC server", groupServing, "", "serve the Rust frontend's gRPC Inference and Control services on the port after the HTTP port"),
	}
}

func (r VLLM) Launch(in Launch) (*Command, error) {
	dir := in.Artifacts["weights_dir"]
	if dir == "" {
		return nil, fmt.Errorf("%w: the stored group has no directory", ErrParam)
	}
	p := in.Params.Clone()
	if p.Str("served_model_name") == "" {
		p["served_model_name"] = in.Name
	}
	parser := p.Str("tool_call_parser")
	delete(p, "tool_call_parser")
	if parser == "" {
		parser = vllmToolParser(in)
	}
	ranks := launchRanks(p, "data_parallel_size")
	launched, err := launchDevices(in)
	if err != nil {
		return nil, err
	}
	devices, err := rankPins(in, launched, ranks, int(max(p.Int("tensor_parallel_size"), 1)*max(p.Int("pipeline_parallel_size"), 1)))
	if err != nil {
		return nil, err
	}
	port := in.Port + in.Rank*rankSpan(p)
	// Ranks of a dense model serve as independent servers, so vLLM is told of no data parallel group
	experts := ranks > 1 && mixtureOfExperts(in.Descriptor)
	if ranks > 1 && !experts {
		delete(p, "data_parallel_size")
	}
	modules, err := loraPairs("lora_modules", p.Str("lora_modules"))
	if err != nil {
		return nil, err
	}
	if len(modules) > 0 {
		p["enable_lora"] = true
	}
	args := []string{"serve", dir, "--host", in.Host, "--port", strconv.Itoa(port), "--enable-auto-tool-choice", "--tool-call-parser", parser}
	switches, switchEmitted := switchFlags(p, vllmSwitches)
	flags, env, emitted := Flags(r.Params(), p)
	flags = append(flags, switches...)
	for k, v := range switchEmitted {
		emitted[k] = v
	}
	emitted["tool_call_parser"] = parser
	if ranks > 1 {
		emitted["data_parallel_size"] = strconv.Itoa(ranks)
	}
	// Ranks sharing expert layers run vLLM's external load balancing, rank 0 hosting the coordinator after every rank's ports
	if experts {
		rpc := strconv.Itoa(in.Port + ranks*rankSpan(p))
		flags = append(flags, "--data-parallel-rank", strconv.Itoa(in.Rank), "--data-parallel-size-local", "1", "--data-parallel-address", in.Host, "--data-parallel-rpc-port", rpc)
		emitted["data_parallel_rpc_port"] = rpc
	}
	if len(modules) > 0 {
		flags = append(append(flags, "--lora-modules"), modules...)
	}
	if kv := vllmKVEventsConfig(in, p, emitted, ranks, experts); kv != "" {
		flags = append(flags, "--kv-events-config", kv)
	}
	if p.Bool("enable_lora") {
		env["VLLM_ALLOW_RUNTIME_LORA_UPDATING"] = "True"
	}
	// The sleep and wake endpoints exist only in dev mode
	if p.Bool("enable_sleep_mode") {
		env["VLLM_SERVER_DEV_MODE"] = "1"
	}
	if p.Bool("grpc") {
		flag := in.InstallRecord.GetFacts()[factGRPCPort]
		if flag == "" {
			return nil, fmt.Errorf("%w: grpc serves through the Rust frontend's --grpc-port, which this vLLM install's serve help does not list, so upgrade vLLM or turn grpc off", ErrParam)
		}
		grpc := strconv.Itoa(port + 1)
		flags = append(flags, flag, grpc)
		env["VLLM_USE_RUST_FRONTEND"] = "1"
		emitted["grpc_port"] = grpc
	}
	pinEnv(env, devices)
	return &Command{Command: in.Install.Path, Args: append(args, flags...), Env: env, Params: emitted, Devices: devices, Port: port}, nil
}

// Splits comma separated name=path pairs, each pair one argument
func loraPairs(param, value string) ([]string, error) {
	var out []string
	for _, pair := range strings.Split(value, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		name, path, ok := strings.Cut(pair, "=")
		if !ok || strings.TrimSpace(name) == "" || strings.TrimSpace(path) == "" {
			return nil, fmt.Errorf("%w: %s takes name=path pairs, got %q", ErrParam, param, pair)
		}
		out = append(out, strings.TrimSpace(name)+"="+strings.TrimSpace(path))
	}
	return out, nil
}

func (VLLM) Prepares(string) bool             { return false }
func (VLLM) Prepare(Launch) (*Command, error) { return nil, nil }
func (VLLM) PrepareTimeout() time.Duration    { return 0 }
func (VLLM) Health() Health {
	return Health{Path: "/health", Interval: 2 * time.Second, SlowAfter: 30 * time.Minute}
}
func (VLLM) StopGrace() time.Duration { return 30 * time.Second }
func (VLLM) Triage() []triage.Set     { return []triage.Set{triage.VLLM{}} }

// The shape probes import the engine, which takes longer than a binary's help
func (VLLM) Probes() []Probe {
	nnodes := flagProbe(factNNodes, []string{"serve", "--help"}, "--nnodes")
	nnodes.Timeout = time.Minute
	parsers := choicesProbe(factToolParsers, []string{"serve", "--help"}, "--tool-call-parser")
	parsers.Timeout = time.Minute
	grpc := flagProbe(factGRPCPort, []string{"serve", "--help"}, "--grpc-port")
	grpc.Timeout = time.Minute
	return []Probe{
		{Key: "version", Args: []string{"--version"}, Parse: versionField},
		nnodes,
		parsers,
		nixlConnectorProbe(),
		grpc,
	}
}

// vLLM logs the weights it loaded as one line, Model loading took 12.34 GiB, and NCCL names the net
// its ranks took, NET/IB or NET/Socket, which is a measurement too
func (VLLM) Measure(lines []string) []*v1.Measurement {
	var m measurements
	for _, line := range lines {
		if n, ok := bytesAfter(line, "Model loading took "); ok {
			m.add("device.weights", n, line)
		}
	}
	return append(m.list, transportMeasurement(lines)...)
}

func (VLLM) Policy() *estimate.Policy {
	device := v1.PoolKind_POOL_KIND_DEVICE
	return &estimate.Policy{
		// Ranks pass half precision activations rank to rank, and the NIXL connector streams the
		// cache layer by layer over the RDMA device when the link has one.
		Shapes: &estimate.ShapeFacts{Ring: true, ActivationBytes: 2, ChunksInFlight: 1, RelayOverlap: true, RelayRDMA: true, ReductionsPerLayer: 2, ChainAggregate: true, ChainSpeculative: false, Handoff: estimate.HandoffNIXL},
		Groups: []estimate.GroupRule{
			{Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_EMBEDDING, Pool: device},
			{Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_LAYER, Pool: device},
			{Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_EXPERTS, Pool: device},
			{Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_OUTPUT, Pool: device},
			{Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_VISION, Pool: device},
			{Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_AUDIO, Pool: device},
			{Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_DRAFT, Pool: device, Loaded: func(s *estimate.Scope, _ *v1.TensorGroup) bool { return s.Params.Str("speculative_config") != "" }},
			{Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_OTHER, Pool: device},
		},
		CacheBytes: func(s *estimate.Scope) uint64 {
			return uint64(float64(s.Params.Int("n_ctx")) * s.CachePerToken * vllmCacheBytes[s.Params.Str("kv_cache_dtype")])
		},
		OverheadBytes: func(*estimate.Scope) uint64 { return 2 << 30 },
		Margin:        0.1,
		ContextParam:  "n_ctx",
		ContextMin:    256,
		ContextStep:   256,
		ContextMax:    func(s *estimate.Scope) int64 { return int64(s.Model.ContextTrain) },
		Deferred:      VLLMResolved(),
		DevicesParam:  "tensor_parallel_size",
		RanksParam:    "data_parallel_size",
		// Ranks of a mixture of experts shard its experts over every rank's devices
		RankSplit: func(_ *estimate.Scope, g *v1.TensorGroup) bool {
			return g.GetKind() == v1.TensorGroupKind_TENSOR_GROUP_KIND_EXPERTS
		},
		Shape: func(p estimate.Params) archs.Run { return archs.Run{Context: float64(p.Int("n_ctx"))} },
	}
}
