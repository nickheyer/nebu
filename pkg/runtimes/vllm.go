package runtimes

import (
	"fmt"
	"strconv"
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
		{ID: "source", Description: "Install vllm from PyPI in a virtual environment", Kind: v1.InstallKind_INSTALL_KIND_BUILT, RecipeID: "vllm"},
	}
}

var vllmCacheBytes = map[string]float64{"auto": 2, "fp8": 1, "fp8_e5m2": 1, "fp8_e4m3": 1}

func (VLLM) Params() []*v1.Param {
	return []*v1.Param{
		{Name: "n_ctx", Label: "Context length", Type: v1.ParamType_PARAM_TYPE_INT, Default: Auto, Solved: true, Unit: "tokens", Min: 256, Step: 256, Group: "Context", Flag: "--max-model-len",
			Rule: contextRule},
		{Name: "max_num_seqs", Label: "Max sequences", Type: v1.ParamType_PARAM_TYPE_INT, Default: "256", Unit: "sequences", Min: 1, Step: 1, Group: "Context", Flag: "--max-num-seqs"},
		{Name: "gpu_memory_utilization", Label: "GPU memory fraction", Type: v1.ParamType_PARAM_TYPE_FLOAT, Default: "0.9", Min: 0.05, Max: 1, Step: 0.01, Group: "Memory", Flag: "--gpu-memory-utilization"},
		{Name: "kv_cache_dtype", Label: "KV cache type", Type: v1.ParamType_PARAM_TYPE_STRING, Default: "auto", Choices: []string{"auto", "fp8", "fp8_e5m2", "fp8_e4m3"}, Group: "Memory", Flag: "--kv-cache-dtype"},
		{Name: "tensor_parallel_size", Label: "Tensor parallel", Type: v1.ParamType_PARAM_TYPE_INT, Default: "1", Unit: "devices", Min: 1, Step: 1, Group: "Parallelism", Flag: "--tensor-parallel-size"},
		{Name: "served_model_name", Label: "Served name", Type: v1.ParamType_PARAM_TYPE_STRING, Group: "Identity", Flag: "--served-model-name"},
		{Name: "speculative_config", Label: "Speculative config", Type: v1.ParamType_PARAM_TYPE_STRING, Group: "Speculative decoding", Advanced: true, Flag: "--speculative-config",
			Description: `{"method":"mtp","num_speculative_tokens":1}`},
	}
}

func (r VLLM) Launch(in Launch) (*Command, error) {
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
	args := []string{"serve", dir, "--host", in.Host, "--port", strconv.Itoa(in.Port)}
	flags, env, emitted := Flags(r.Params(), p)
	setEnv(env, "CUDA_VISIBLE_DEVICES", visible(in.Devices, "nvidia", false))
	setEnv(env, "ROCR_VISIBLE_DEVICES", visible(in.Devices, "amd", true))
	return &Command{Command: in.Install.Path, Args: append(args, flags...), Env: env, Params: emitted}, nil
}

func (VLLM) Prepares(string) bool             { return false }
func (VLLM) Prepare(Launch) (*Command, error) { return nil, nil }
func (VLLM) PrepareTimeout() time.Duration    { return 0 }
func (VLLM) Health() Health {
	return Health{Path: "/health", Interval: 2 * time.Second, Timeout: 30 * time.Minute}
}
func (VLLM) StopGrace() time.Duration { return 30 * time.Second }
func (VLLM) Triage() []triage.Set     { return []triage.Set{triage.VLLM{}} }

func (VLLM) Probes() []Probe {
	return []Probe{{Key: "version", Args: []string{"--version"}, Parse: versionField}}
}

// vLLM logs the weights it loaded as one line, Model loading took 12.34 GiB
func (VLLM) Measure(lines []string) []*v1.Measurement {
	var m measurements
	for _, line := range lines {
		if n, ok := bytesAfter(line, "Model loading took "); ok {
			m.add("device.weights", n, line)
		}
	}
	return m.list
}

func (VLLM) Policy() *estimate.Policy {
	device := v1.PoolKind_POOL_KIND_DEVICE
	return &estimate.Policy{
		Groups: []estimate.GroupRule{
			{Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_EMBEDDING, Pool: device},
			{Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_LAYER, Pool: device},
			{Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_EXPERTS, Pool: device},
			{Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_OUTPUT, Pool: device},
			{Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_VISION, Pool: device},
			{Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_AUDIO, Pool: device},
			{Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_DRAFT, Pool: device, Loaded: func(s *estimate.Scope) bool { return s.Params.Str("speculative_config") != "" }},
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
		DevicesParam:  "tensor_parallel_size",
		Shape:         func(p estimate.Params) archs.Run { return archs.Run{Context: float64(p.Int("n_ctx"))} },
		States: func(s *estimate.Scope) ([]*v1.ParamState, string) {
			gpus := float64(len(host.Kind(s.Host, v1.DeviceKind_DEVICE_KIND_GPU)))
			return []*v1.ParamState{
				bounds("n_ctx", 256, s.Model.ContextTrain, 256),
				bounds("max_num_seqs", 1, 0, 1),
				bounds("gpu_memory_utilization", 0.05, 1, 0.01),
				bounds("tensor_parallel_size", 1, gpus, 1),
			}, ""
		},
	}
}
