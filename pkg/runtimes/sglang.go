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

var sglangCacheBytes = map[string]float64{"auto": 2, "fp8_e5m2": 1, "fp8_e4m3": 1}

func (SGLang) Params() []*v1.Param {
	return []*v1.Param{
		{Name: "n_ctx", Label: "Context length", Type: v1.ParamType_PARAM_TYPE_INT, Default: Auto, Solved: true, Unit: "tokens", Min: 256, Step: 256, Group: "Context", Flag: "--context-length",
			Rule: contextRule},
		{Name: "chunked_prefill_size", Label: "Chunked prefill", Type: v1.ParamType_PARAM_TYPE_INT, Default: "8192", Unit: "tokens", Min: -1, Step: 512, Group: "Context", Advanced: true, Flag: "--chunked-prefill-size",
			Description: "-1 off"},
		{Name: "mem_fraction_static", Label: "Static memory fraction", Type: v1.ParamType_PARAM_TYPE_FLOAT, Default: "0.88", Min: 0.05, Max: 1, Step: 0.01, Group: "Memory", Flag: "--mem-fraction-static"},
		{Name: "kv_cache_dtype", Label: "KV cache type", Type: v1.ParamType_PARAM_TYPE_STRING, Default: "auto", Choices: []string{"auto", "fp8_e5m2", "fp8_e4m3"}, Group: "Memory", Flag: "--kv-cache-dtype"},
		{Name: "tp_size", Label: "Tensor parallel", Type: v1.ParamType_PARAM_TYPE_INT, Default: "1", Unit: "devices", Min: 1, Step: 1, Group: "Parallelism", Flag: "--tp-size"},
		{Name: "served_model_name", Label: "Served name", Type: v1.ParamType_PARAM_TYPE_STRING, Group: "Identity", Flag: "--served-model-name"},
		{Name: "speculative_algorithm", Label: "Draft method", Type: v1.ParamType_PARAM_TYPE_STRING, Choices: []string{"", "NEXTN", "EAGLE", "EAGLE3"}, Group: "Speculative decoding", Advanced: true, Flag: "--speculative-algorithm"},
		{Name: "speculative_draft_model_path", Label: "Draft model", Type: v1.ParamType_PARAM_TYPE_PATH, Group: "Speculative decoding", Advanced: true, Flag: "--speculative-draft-model-path", Picks: "checkpoint"},
		{Name: "speculative_num_steps", Label: "Draft steps", Type: v1.ParamType_PARAM_TYPE_INT, Min: 1, Step: 1, Group: "Speculative decoding", Advanced: true, Flag: "--speculative-num-steps"},
		{Name: "speculative_eagle_topk", Label: "Draft branches", Type: v1.ParamType_PARAM_TYPE_INT, Min: 1, Step: 1, Group: "Speculative decoding", Advanced: true, Flag: "--speculative-eagle-topk"},
		{Name: "speculative_num_draft_tokens", Label: "Draft tokens", Type: v1.ParamType_PARAM_TYPE_INT, Min: 1, Step: 1, Group: "Speculative decoding", Advanced: true, Flag: "--speculative-num-draft-tokens"},
	}
}

func (r SGLang) Launch(in Launch) (*Command, error) {
	if err := deviceBound(in, r.Name()); err != nil {
		return nil, err
	}
	dir := in.Artifacts["weights_dir"]
	if dir == "" {
		return nil, fmt.Errorf("%w: the stored group has no directory", ErrParam)
	}
	p := in.Params.Clone()
	if p.Str("served_model_name") == "" {
		p["served_model_name"] = in.Name
	}
	args := []string{"-m", "sglang.launch_server", "--model-path", dir, "--host", in.Host, "--port", strconv.Itoa(in.Port)}
	flags, env, emitted := Flags(r.Params(), p)
	setEnv(env, "CUDA_VISIBLE_DEVICES", visible(in.Devices, "nvidia", false))
	return &Command{Command: in.Install.Path, Args: append(args, flags...), Env: env, Params: emitted}, nil
}

func (SGLang) Prepares(string) bool             { return false }
func (SGLang) Prepare(Launch) (*Command, error) { return nil, nil }
func (SGLang) PrepareTimeout() time.Duration    { return 0 }
func (SGLang) Health() Health {
	return Health{Path: "/health", Interval: 2 * time.Second, Timeout: 30 * time.Minute}
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
	return []Probe{
		{Key: "version", Args: []string{"-c", "import sglang; print(sglang.__version__)"}, Timeout: time.Minute, Parse: versionField},
		nnodes, disaggregation, nixl,
	}
}

// SGLang logs the weights it loaded, Load weight end. ... mem usage=12.34 GiB, and NCCL names the
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
			{Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_DRAFT, Pool: device, Loaded: func(s *estimate.Scope) bool { return s.Params.Str("speculative_algorithm") != "" }},
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
		Shape:         func(p estimate.Params) archs.Run { return archs.Run{Context: float64(p.Int("n_ctx"))} },
		States: func(s *estimate.Scope) ([]*v1.ParamState, string) {
			gpus := float64(len(host.Kind(s.Host, v1.DeviceKind_DEVICE_KIND_GPU)))
			return []*v1.ParamState{
				bounds("n_ctx", 256, s.Model.ContextTrain, 256),
				bounds("chunked_prefill_size", -1, float64(s.Params.Int("n_ctx")), 512),
				bounds("mem_fraction_static", 0.05, 1, 0.01),
				bounds("tp_size", 1, gpus, 1),
			}, ""
		},
	}
}
