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

// NeMo serves checkpoints through Export-Deploy and Ray Serve on NVIDIA GPUs.
type NeMo struct{}

func (NeMo) ID() string          { return "nemo" }
func (NeMo) Name() string        { return "NeMo" }
func (NeMo) Description() string { return "Serves NeMo checkpoints in framework on NVIDIA GPUs" }
func (NeMo) Formats() []string   { return []string{"nemo2", "nemo"} }
func (NeMo) Kind() v1.ModelKind  { return v1.ModelKind_MODEL_KIND_LANGUAGE }
func (NeMo) API() v1.ApiFlavor   { return v1.ApiFlavor_API_FLAVOR_OPENAI }
func (NeMo) Requirements() []string {
	return []string{"an NVIDIA GPU"}
}

func (NeMo) Unmet(h *v1.HostProfile) []string {
	if host.HasVendor(h, "nvidia") {
		return nil
	}
	return []string{"an NVIDIA GPU"}
}

func (NeMo) Methods() []Method {
	return []Method{
		{ID: "source", Description: "Install Export-Deploy and the checkpoint converter in separate virtual environments", Kind: v1.InstallKind_INSTALL_KIND_BUILT, RecipeID: "nemo"},
	}
}

func (NeMo) Params() []*v1.Param {
	return []*v1.Param{
		{Name: "n_ctx", Label: "Context length", Type: v1.ParamType_PARAM_TYPE_INT, Default: Auto, Solved: true, Unit: "tokens", Min: 256, Step: 256, Group: "Context", Flag: "--inference_max_seq_length",
			Rule: contextRule},
		{Name: "max_batch_size", Label: "Batch size", Type: v1.ParamType_PARAM_TYPE_INT, Default: "8", Unit: "sequences", Min: 1, Step: 1, Group: "Context", Flag: "--max_batch_size"},
		{Name: "num_gpus", Label: "Devices", Type: v1.ParamType_PARAM_TYPE_INT, Default: "1", Unit: "devices", Min: 1, Step: 1, Group: "Parallelism", Flag: "--num_gpus"},
		{Name: "tensor_model_parallel_size", Label: "Tensor parallel", Type: v1.ParamType_PARAM_TYPE_INT, Default: "1", Unit: "devices", Min: 1, Step: 1, Group: "Parallelism", Flag: "--tensor_model_parallel_size"},
		{Name: "pipeline_model_parallel_size", Label: "Pipeline parallel", Type: v1.ParamType_PARAM_TYPE_INT, Default: "1", Unit: "devices", Min: 1, Step: 1, Group: "Parallelism", Flag: "--pipeline_model_parallel_size"},
		{Name: "enable_cuda_graphs", Label: "CUDA graphs", Type: v1.ParamType_PARAM_TYPE_BOOL, Default: "false", Group: "Performance", Flag: "--enable_cuda_graphs"},
		{Name: "enable_flash_decode", Label: "Flash decode", Type: v1.ParamType_PARAM_TYPE_BOOL, Default: "false", Group: "Performance", Flag: "--enable_flash_decode"},
		{Name: "served_model_name", Label: "Served name", Type: v1.ParamType_PARAM_TYPE_STRING, Group: "Identity", Flag: "--model_id"},
		{Name: "model_id", Label: "Base model id", Type: v1.ParamType_PARAM_TYPE_STRING, Group: "Conversion", Advanced: true,
			Description: "Hugging Face id"},
		{Name: "legacy_ckpt", Label: "Legacy checkpoint", Type: v1.ParamType_PARAM_TYPE_BOOL, Default: "false", Group: "Conversion", Advanced: true, Flag: "--legacy_ckpt"},
		{Name: "cuda_visible_devices", Label: "Visible devices", Type: v1.ParamType_PARAM_TYPE_STRING, Group: "Parallelism", Advanced: true, Flag: "--cuda_visible_devices", Description: "0,1"},
	}
}

func (r NeMo) Launch(in Launch) (*Command, error) {
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
	if p.Str("cuda_visible_devices") == "" {
		p["cuda_visible_devices"] = visible(in.Devices, "nvidia", true)
	}
	args := []string{in.Install.Path, "--megatron_checkpoint", dir, "--host", in.Host, "--port", strconv.Itoa(in.Port)}
	flags, env, emitted := Flags(r.Params(), p)
	return &Command{Command: in.Install.Dir + "/venv/bin/python", Args: append(args, flags...), Env: env, Params: emitted}, nil
}

// Convert packed .nemo archives to NeMo 2 before first launch.
func (NeMo) Prepares(formatID string) bool { return formatID == "nemo" }

func (NeMo) Prepare(in Launch) (*Command, error) {
	weights, out := in.Artifacts["weights"], in.Artifacts["prepared_dir"]
	if weights == "" || out == "" {
		return nil, fmt.Errorf("%w: converting needs the packed checkpoint and a directory to write", ErrParam)
	}
	modelID := in.Params.Str("model_id")
	if modelID == "" {
		modelID = in.Descriptor.GetMetadata()["tokenizer.type"]
	}
	if modelID == "" {
		return nil, fmt.Errorf("%w: model_id is empty and the checkpoint names no tokenizer, set model_id to the base model's Hugging Face id", ErrParam)
	}
	return &Command{
		Command: in.Install.Dir + "/venv-convert/bin/python",
		Args:    []string{in.Install.Dir + "/convert_nemo1_to_nemo2.py", "--input_path", weights, "--output_path", out, "--model_id", modelID},
		Env:     map[string]string{},
		Params:  map[string]string{"model_id": modelID},
	}, nil
}

func (NeMo) PrepareTimeout() time.Duration { return 2 * time.Hour }
func (NeMo) Health() Health {
	return Health{Path: "/v1/health", Interval: 5 * time.Second, Timeout: time.Hour}
}
func (NeMo) StopGrace() time.Duration { return time.Minute }
func (NeMo) Triage() []triage.Set     { return []triage.Set{triage.NeMo{}} }

func (NeMo) Probes() []Probe {
	return []Probe{{
		Key:     "version",
		Command: func(in Install) string { return in.Dir + "/venv/bin/python" },
		Args:    []string{"-c", "from nemo_export_deploy_common.package_info import __version__; print(__version__)"},
		Timeout: time.Minute,
		Parse:   versionField,
	}}
}

// This serving mode reports no supported memory measurements.
func (NeMo) Measure([]string) []*v1.Measurement { return nil }

func (NeMo) Policy() *estimate.Policy {
	device := v1.PoolKind_POOL_KIND_DEVICE
	return &estimate.Policy{
		Groups: []estimate.GroupRule{
			{Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_EMBEDDING, Pool: device},
			{Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_LAYER, Pool: device},
			{Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_EXPERTS, Pool: device},
			{Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_OUTPUT, Pool: device},
			{Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_VISION, Pool: device},
			{Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_AUDIO, Pool: device},
			// This serving mode does not load prediction heads.
			{Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_DRAFT, Pool: device, Loaded: func(*estimate.Scope) bool { return false }},
			{Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_OTHER, Pool: device},
		},
		// Each batched sequence keeps a full-length KV cache.
		CacheBytes: func(s *estimate.Scope) uint64 {
			return uint64(float64(s.Params.Int("n_ctx")) * s.CachePerToken * 2 * float64(s.Params.Int("max_batch_size")))
		},
		OverheadBytes: func(*estimate.Scope) uint64 { return 3 << 30 },
		Margin:        0.1,
		ContextParam:  "n_ctx",
		ContextMin:    256,
		ContextStep:   256,
		ContextMax:    func(s *estimate.Scope) int64 { return int64(s.Model.ContextTrain) },
		DevicesParam:  "num_gpus",
		Shape:         func(p estimate.Params) archs.Run { return archs.Run{Context: float64(p.Int("n_ctx"))} },
		States: func(s *estimate.Scope) ([]*v1.ParamState, string) {
			gpus := float64(len(host.Kind(s.Host, v1.DeviceKind_DEVICE_KIND_GPU)))
			return []*v1.ParamState{
				bounds("n_ctx", 256, s.Model.ContextTrain, 256),
				bounds("max_batch_size", 1, 0, 1),
				bounds("num_gpus", 1, gpus, 1),
				bounds("tensor_model_parallel_size", 1, gpus, 1),
				bounds("pipeline_model_parallel_size", 1, gpus, 1),
			}, ""
		},
	}
}
