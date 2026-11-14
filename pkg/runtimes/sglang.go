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

// SGLang serves safetensors models with prefix caching on NVIDIA GPUs.
type SGLang struct{}

func (SGLang) ID() string   { return "sglang" }
func (SGLang) Name() string { return "SGLang" }
func (SGLang) Description() string {
	return "Serves safetensors checkpoints with prefix caching on NVIDIA GPUs"
}
func (SGLang) Formats() []string  { return []string{"safetensors"} }
func (SGLang) Kind() v1.ModelKind { return v1.ModelKind_MODEL_KIND_LANGUAGE }
func (SGLang) API() v1.ApiFlavor  { return v1.ApiFlavor_API_FLAVOR_OPENAI }
func (SGLang) Requirements() []string {
	return []string{"an NVIDIA GPU"}
}

func (SGLang) Unmet(h *v1.HostProfile) []string {
	if host.HasVendor(h, "nvidia") {
		return nil
	}
	return []string{"an NVIDIA GPU"}
}

func (SGLang) Methods() []Method {
	return []Method{
		{ID: "source", Description: "Install sglang from PyPI in a virtual environment", Kind: v1.InstallKind_INSTALL_KIND_BUILT, RecipeID: "sglang"},
	}
}

// The cache data types --kv-cache-dtype takes, as SGLang lists them
var sglangCacheDTypes = []string{"auto", "fp8_e5m2", "fp8_e4m3", "mxfp8", "bf16", "bfloat16", "nvfp4", "fp4_mx_block16"}

var sglangCacheBytes = map[string]float64{"auto": 2, "bf16": 2, "bfloat16": 2, "fp8_e5m2": 1, "fp8_e4m3": 1, "mxfp8": 1, "nvfp4": 0.5625, "fp4_mx_block16": 0.5625}

// The speculative algorithms SGLang builds in, with NEXTN as its alias of EAGLE for MTP heads
var sglangSpecAlgorithms = []string{"", "EAGLE", "EAGLE3", "NEXTN", "STANDALONE", "NGRAM", "DFLASH", "DSPARK", "UNO"}

func (SGLang) Params() []*v1.Param {
	const (
		groupContext     = "Context"
		groupMemory      = "Memory"
		groupParallel    = "Parallelism"
		groupModel       = "Model"
		groupIdentity    = "Identity"
		groupChat        = "Chat"
		groupSpeculative = GroupSpeculative
		groupDiagnostics = "Diagnostics"
		groupAdapters    = "Adapters"
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
	return []*v1.Param{
		{Name: "n_ctx", Label: "Context length", Type: v1.ParamType_PARAM_TYPE_INT, Default: Auto, Solved: true, Unit: "tokens", Min: 256, Max: contextSpan, Step: 256, Group: groupContext, Flag: "--context-length",
			Rule: contextRule},
		placeholder("by device memory", intParam("chunked_prefill_size", "Chunked prefill", groupContext, "--chunked-prefill-size", "", "tokens", "tokens in one prefill chunk, -1 off", -1, 0, 512, true)),
		placeholder("by cache size", intParam("max_running_requests", "Max running requests", groupContext, "--max-running-requests", "", "requests", "read back from its log as the route's capacity", 1, 0, 1, false)),
		placeholder("no limit", intParam("max_queued_requests", "Max queued requests", groupContext, "--max-queued-requests", "", "requests", "", 1, 0, 1, true)),
		placeholder("by memory fraction", intParam("max_total_tokens", "Cache pool tokens", groupContext, "--max-total-tokens", "", "tokens", "tokens the cache pool holds", 256, 0, 256, true)),
		placeholder("runtime's own", intParam("max_prefill_tokens", "Max prefill tokens", groupContext, "--max-prefill-tokens", "", "tokens", "tokens one prefill batch holds, at least the context length", 256, 0, 256, true)),
		placeholder("runtime's own", strParam("schedule_policy", "Schedule policy", groupContext, "--schedule-policy", "", "order requests are served in", []string{"", "lpm", "random", "fcfs", "dfs-weight", "lof", "priority", "routing-key", "hrrn"}, true)),
		boolParam("enable_mixed_chunk", "Mixed chunks", groupContext, "--enable-mixed-chunk", "batch decoding with prefill chunks"),
		boolParam("disable_radix_cache", "Disable prefix cache", groupContext, "--disable-radix-cache", "serve without the radix prefix cache that prefix aware routing counts on"),
		placeholder("1", intParam("num_continuous_decode_steps", "Decode steps per schedule", groupContext, "--num-continuous-decode-steps", "", "steps", "decode steps run between scheduler passes", 1, 0, 1, true)),
		{Name: "mem_fraction_static", Label: "Static memory fraction", Type: v1.ParamType_PARAM_TYPE_FLOAT, Min: 0.05, Max: 1, Step: 0.01, Group: groupMemory, Flag: "--mem-fraction-static",
			Placeholder: "by model and device", Description: "share of device memory for weights and the cache pool"},
		{Name: "kv_cache_dtype", Label: "KV cache type", Type: v1.ParamType_PARAM_TYPE_STRING, Default: "auto", Choices: sglangCacheDTypes, Group: groupMemory, Flag: "--kv-cache-dtype",
			Description: "auto keeps the model's type, the fp8 types need CUDA 11.8, mxfp8 the FA4 backend, the fp4 types CUDA 12.8"},
		placeholder("by backend", intParam("page_size", "Page size", groupMemory, "--page-size", "", "tokens", "tokens in one cache page", 1, 0, 1, true)),
		{Name: "tp_size", Label: "Tensor parallel", Type: v1.ParamType_PARAM_TYPE_INT, Default: "1", Unit: "devices", Min: 1, Step: 1, Group: groupParallel, Flag: "--tp-size"},
		placeholder("1", intParam("pp_size", "Pipeline parallel", groupParallel, "--pp-size", "", "stages", "a chain formation sets it per rank", 1, 0, 1, true)),
		placeholder("1", intParam("dp_size", "Data parallel", groupParallel, "--dp-size", "", "ranks", "ranks each serving its own port as a server of its own on tensor parallel devices of its own", 1, 0, 1, true)),
		placeholder("1", intParam("ep_size", "Expert parallel", groupParallel, "--ep-size", "", "devices", "", 1, 0, 1, true)),
		boolParam("enable_p2p_check", "Check peer access", groupParallel, "--enable-p2p-check", "verify device to device access before enabling it"),
		placeholder("auto", strParam("dtype", "Weight type", groupModel, "--dtype", "", "type weights and activations run in, auto takes bfloat16 for bfloat16 models and float16 otherwise", []string{"", "auto", "half", "float16", "bfloat16", "float", "float32"}, false)),
		placeholder("the checkpoint's", strParam("quantization", "Quantization", groupModel, "--quantization", "", "quantization method of the weights, such as awq, gptq, fp8", nil, true)),
		placeholder("auto", strParam("load_format", "Load format", groupModel, "--load-format", "", "weight file format to load", nil, true)),
		placeholder("runtime's choice", strParam("attention_backend", "Attention backend", groupModel, "--attention-backend", "", "kernel for attention, such as flashinfer, fa3, fa4, triton, trtllm_mha", nil, true)),
		boolParam("trust_remote_code", "Trust remote code", groupModel, "--trust-remote-code", "run the modeling code a checkpoint ships"),
		boolParam("is_embedding", "Embedding model", groupModel, "--is-embedding", "serve a causal model as an embedding model"),
		placeholder("random", intParam("random_seed", "Seed", groupModel, "--random-seed", "", "", "sampling seed", 0, 0, 1, true)),
		placeholder("runtime's choice", intParam("cuda_graph_max_bs", "CUDA graph batch", groupModel, "--cuda-graph-max-bs", "", "requests", "largest batch captured as a CUDA graph", 1, 0, 1, true)),
		boolParam("enable_torch_compile", "torch.compile", groupModel, "--enable-torch-compile", "compile the model with torch, slower starts and faster steps"),
		placeholder("32", intParam("torch_compile_max_bs", "Compiled batch", groupModel, "--torch-compile-max-bs", "", "requests", "largest batch torch compiles", 1, 0, 1, true)),
		boolParam("enable_lora", "LoRA adapters", groupAdapters, "--enable-lora", "accept LoRA adapters named by requests and loaded at runtime"),
		fileList(strParam("lora_paths", "Adapters at launch", groupAdapters, "", "", "name=path pairs, comma separated, served from the start and turning LoRA on", nil, true), v1.ParamList_PARAM_LIST_NAMED),
		placeholder("8", intParam("max_loras_per_batch", "Adapters per batch", groupAdapters, "--max-loras-per-batch", "", "adapters", "distinct adapters one batch runs, base requests counting as one", 1, 0, 1, true)),
		placeholder("no limit", intParam("max_loaded_loras", "Adapters in host memory", groupAdapters, "--max-loaded-loras", "", "adapters", "adapters held in host memory at once, at least the per batch count", 1, 0, 1, true)),
		placeholder("from launch adapters", intParam("max_lora_rank", "Max adapter rank", groupAdapters, "--max-lora-rank", "", "", "largest rank an adapter loaded at runtime may have", 1, 0, 8, true)),
		placeholder("from launch adapters", strParam("lora_target_modules", "Adapter target modules", groupAdapters, "", "", "modules adapters loaded at runtime may target, comma separated or all", nil, true)),
		boolParam("sleep_on_idle", "Sleep when idle", groupModel, "--sleep-on-idle", "lower CPU use while no request runs"),
		{Name: "served_model_name", Label: "Served name", Type: v1.ParamType_PARAM_TYPE_STRING, Group: groupIdentity, Flag: "--served-model-name", Placeholder: "the launch's name"},
		placeholder("the model's", strParam("chat_template", "Chat template", groupChat, "--chat-template", "", "a built in template name or a template file", nil, true)),
		strParam("default_chat_template_kwargs", "Template arguments", groupChat, "--default-chat-template-kwargs", "", "a JSON object of values the template reads on every request", nil, true),
		placeholder("reasoning in content", strParam("reasoning_parser", "Reasoning parser", groupChat, "--reasoning-parser", "", "parser that splits reasoning from the answer, auto detects it from the template", nil, true)),
		placeholder("by model family", strParam("tool_call_parser", "Tool call parser", groupChat, "", "", "parser for tool calls", nil, true)),
		placeholder("model", strParam("sampling_defaults", "Sampling defaults", groupChat, "--sampling-defaults", "", "where unset sampling values come from", []string{"", "openai", "model"}, true)),
		placeholder("1", intParam("stream_interval", "Stream interval", groupChat, "--stream-interval", "", "tokens", "tokens between streamed chunks", 1, 0, 1, true)),
		{Name: "speculative_algorithm", Label: "Draft method", Type: v1.ParamType_PARAM_TYPE_STRING, Choices: sglangSpecAlgorithms, Group: groupSpeculative, Advanced: true, Flag: "--speculative-algorithm",
			Description: "EAGLE and EAGLE3 take a draft model, NEXTN the model's own MTP heads, STANDALONE a plain draft model, NGRAM needs none"},
		{Name: "speculative_draft_model_path", Label: "Draft model", Type: v1.ParamType_PARAM_TYPE_PATH, Group: groupSpeculative, Advanced: true, Flag: "--speculative-draft-model-path", Picks: "checkpoint"},
		{Name: "speculative_num_steps", Label: "Draft steps", Type: v1.ParamType_PARAM_TYPE_INT, Min: 1, Step: 1, Group: groupSpeculative, Advanced: true, Flag: "--speculative-num-steps", Placeholder: "by algorithm"},
		{Name: "speculative_eagle_topk", Label: "Draft branches", Type: v1.ParamType_PARAM_TYPE_INT, Min: 1, Step: 1, Group: groupSpeculative, Advanced: true, Flag: "--speculative-eagle-topk", Placeholder: "by algorithm"},
		{Name: "speculative_num_draft_tokens", Label: "Draft tokens", Type: v1.ParamType_PARAM_TYPE_INT, Min: 1, Step: 1, Group: groupSpeculative, Advanced: true, Flag: "--speculative-num-draft-tokens", Placeholder: "by algorithm"},
		placeholder("info", strParam("log_level", "Log level", groupDiagnostics, "--log-level", "", "", []string{"", "debug", "info", "warning", "error"}, true)),
		{Name: "enable_metrics", Label: "Prometheus metrics", Type: v1.ParamType_PARAM_TYPE_BOOL, Default: "true", Group: groupDiagnostics, Flag: "--enable-metrics", Advanced: true,
			Description: "serve the metrics endpoint the router reads queue depth and cache use from"},
		{Name: "watchdog_timeout", Label: "Watchdog timeout", Type: v1.ParamType_PARAM_TYPE_FLOAT, Min: 1, Step: 30, Unit: "seconds", Group: groupDiagnostics, Advanced: true, Flag: "--watchdog-timeout",
			Placeholder: "300", Description: "a forward batch past this long crashes the server, a formation sets its stop grace"},
		boolParam("enable_dynamic_chunking", "Dynamic chunking", groupDiagnostics, "--enable-dynamic-chunking", "size pipeline chunks as they run, a chain formation turns it on"),
		boolParam("disable_overlap_schedule", "Disable overlap schedule", groupDiagnostics, "--disable-overlap-schedule", "run the scheduler and the workers in turn, a chain formation turns it on"),
		boolParam("grpc", "gRPC server", groupServing, "", "serve the native SglangService on the port after the HTTP port, beside HTTP"),
	}
}

func (r SGLang) Launch(in Launch) (*Command, error) {
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
		parser = sglangToolParser(in)
	}
	// The metrics endpoint the gateway reads load from stays on unless set off
	if p["enable_metrics"] == nil {
		p["enable_metrics"] = true
	}
	paths, err := loraPairs("lora_paths", p.Str("lora_paths"))
	if err != nil {
		return nil, err
	}
	if len(paths) > 0 {
		p["enable_lora"] = true
	}
	// Every rank is a server of its own with data parallel one, as SGLang's router launches its workers
	ranks := launchRanks(p, "dp_size")
	if ranks > 1 {
		delete(p, "dp_size")
	}
	port := in.Port + in.Rank*rankSpan(p)
	args := []string{"-m", "sglang.launch_server", "--model-path", dir, "--host", in.Host, "--port", strconv.Itoa(port), "--tool-call-parser", parser}
	flags, env, emitted := Flags(r.Params(), p)
	emitted["tool_call_parser"] = parser
	if ranks > 1 {
		emitted["dp_size"] = strconv.Itoa(ranks)
	}
	if len(paths) > 0 {
		flags = append(append(flags, "--lora-paths"), paths...)
	}
	if kv := sglangKVEventsConfig(in, p, ranks); kv != "" {
		flags = append(flags, "--kv-events-config", kv)
	}
	if modules := strings.FieldsFunc(p.Str("lora_target_modules"), func(r rune) bool { return r == ',' || r == ' ' }); len(modules) > 0 {
		flags = append(append(flags, "--lora-target-modules"), modules...)
	}
	if p.Bool("grpc") {
		flag := in.InstallRecord.GetFacts()[factGRPCPort]
		if flag == "" {
			return nil, fmt.Errorf("%w: grpc serves through the native --grpc-port, which this SGLang install's launch_server help does not list, so upgrade SGLang or turn grpc off", ErrParam)
		}
		grpc := strconv.Itoa(port + 1)
		flags = append(flags, flag, grpc)
		emitted["grpc_port"] = grpc
	}
	launched, err := launchDevices(in)
	if err != nil {
		return nil, err
	}
	devices, err := rankPins(in, launched, ranks, int(max(p.Int("tp_size"), 1)*max(p.Int("pp_size"), 1)))
	if err != nil {
		return nil, err
	}
	pinEnv(env, devices)
	return &Command{Command: in.Install.Path, Args: append(args, flags...), Env: env, Params: emitted, Devices: devices, Port: port}, nil
}

func (SGLang) Prepares(string) bool             { return false }
func (SGLang) Prepare(Launch) (*Command, error) { return nil, nil }
func (SGLang) PrepareTimeout() time.Duration    { return 0 }
func (SGLang) Health() Health {
	return Health{Path: "/health", Interval: 2 * time.Second, SlowAfter: 30 * time.Minute}
}
func (SGLang) StopGrace() time.Duration { return 30 * time.Second }
func (SGLang) Triage() []triage.Set     { return []triage.Set{triage.SGLang{}} }

func (SGLang) Probes() []Probe {
	help := []string{"-m", "sglang.launch_server", "--help"}
	nnodes := flagProbe(factNNodes, help, "--nnodes")
	nnodes.Timeout = time.Minute
	disaggregation := flagProbe(factDisaggregation, help, "--disaggregation-mode")
	disaggregation.Timeout = time.Minute
	nixl := sglangNIXLProbe()
	parsers := choicesProbe(factToolParsers, help, "--tool-call-parser")
	parsers.Timeout = time.Minute
	grpc := flagProbe(factGRPCPort, help, "--grpc-port")
	grpc.Timeout = time.Minute
	return []Probe{
		{Key: "version", Args: []string{"-c", "import sglang; print(sglang.__version__)"}, Timeout: time.Minute, Parse: versionField},
		nnodes, disaggregation, nixl, parsers, grpc,
	}
}

// SGLang logs the weights it loaded, Load weight end. ... mem usage=12.34 GB, and NCCL names the
// net its ranks took, NET/IB or NET/Socket, which is a measurement too
func (SGLang) Measure(lines []string) []*v1.Measurement {
	var m measurements
	for _, line := range lines {
		if !strings.Contains(line, "Load weight end") {
			continue
		}
		if n, ok := bytesAfter(line, "mem usage="); ok {
			m.add("device.weights", n, line)
		}
	}
	return append(m.list, transportMeasurement(lines)...)
}

func (SGLang) Policy() *estimate.Policy {
	device := v1.PoolKind_POOL_KIND_DEVICE
	return &estimate.Policy{
		States: func(s *estimate.Scope) []*v1.ParamState {
			gpus := float64(len(host.Kind(s.Host, v1.DeviceKind_DEVICE_KIND_GPU)))
			return []*v1.ParamState{
				bounds("n_ctx", 256, s.Model.ContextTrain, 256),
				bounds("chunked_prefill_size", -1, float64(s.Params.Int("n_ctx")), 512),
				bounds("mem_fraction_static", 0.05, 1, 0.01),
				bounds("tp_size", 1, gpus, 1),
				bounds("pp_size", 1, max(gpus, 1), 1),
				bounds("dp_size", 1, max(gpus, 1), 1),
				bounds("ep_size", 1, max(gpus, 1), 1),
			}
		},
		// Ranks pass half precision activations in a ring and pipeline prompt chunks, two in flight
		// with dynamic chunking, and the NIXL backend streams the cache as layers finish.
		Shapes: &estimate.ShapeFacts{Ring: true, ActivationBytes: 2, ChunksInFlight: 2, RelayOverlap: true, RelayRDMA: true, ReductionsPerLayer: 2, ChainAggregate: true, ChainSpeculative: false, Handoff: estimate.HandoffSGLangBootstrap},
		Groups: []estimate.GroupRule{
			{Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_EMBEDDING, Pool: device},
			{Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_LAYER, Pool: device},
			{Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_EXPERTS, Pool: device},
			{Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_OUTPUT, Pool: device},
			{Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_VISION, Pool: device},
			{Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_AUDIO, Pool: device},
			{Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_DRAFT, Pool: device, Loaded: func(s *estimate.Scope, _ *v1.TensorGroup) bool { return s.Params.Str("speculative_algorithm") != "" }},
			{Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_OTHER, Pool: device},
		},
		CacheBytes: func(s *estimate.Scope) uint64 {
			return uint64(float64(s.Params.Int("n_ctx")) * s.CachePerToken * sglangCacheBytes[s.Params.Str("kv_cache_dtype")])
		},
		OverheadBytes: func(*estimate.Scope) uint64 { return 2 << 30 },
		Margin:        0.1,
		ContextParam:  "n_ctx",
		ContextMin:    256,
		ContextStep:   256,
		ContextMax:    func(s *estimate.Scope) int64 { return int64(s.Model.ContextTrain) },
		DevicesParam:  "tp_size",
		RanksParam:    "dp_size",
		Deferred:      map[string]func([]string) (string, bool){"max_running_requests": sglangRunning},
		Shape:         func(p estimate.Params) archs.Run { return archs.Run{Context: float64(p.Int("n_ctx"))} },
	}
}
