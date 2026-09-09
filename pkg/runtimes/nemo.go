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

// NeMo Export-Deploy, serving NeMo checkpoints in framework over Ray Serve on NVIDIA GPUs, converting packed archives first
type NeMo struct{}

func (NeMo) ID() string          { return "nemo" }
func (NeMo) Name() string        { return "NeMo" }
func (NeMo) Description() string { return "Serves NeMo checkpoints in framework on NVIDIA GPUs" }
func (NeMo) Formats() []string   { return []string{"nemo2", "nemo"} }
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
		{ID: "source", Description: "Install Export-Deploy and the checkpoint converter from PyPI into virtual environments", Kind: v1.InstallKind_INSTALL_KIND_BUILT, RecipeID: "nemo"},
	}
}

func (NeMo) Params() []*v1.Param {
	return []*v1.Param{
		{Name: "n_ctx", Label: "Context length", Type: v1.ParamType_PARAM_TYPE_INT, Default: Auto, Solved: true, Unit: "tokens", Min: 256, Step: 256, Group: "Context", Flag: "--inference_max_seq_length",
			Description: "Tokens the model can hold in one sequence. Auto takes the largest that fits in memory, up to the length the model was trained for."},
		{Name: "max_batch_size", Label: "Batch size", Type: v1.ParamType_PARAM_TYPE_INT, Default: "8", Unit: "sequences", Min: 1, Step: 1, Group: "Context", Flag: "--max_batch_size",
			Description: "Sequences batched together. Each holds a full-length cache."},
		{Name: "num_gpus", Label: "Devices", Type: v1.ParamType_PARAM_TYPE_INT, Default: "1", Unit: "devices", Min: 1, Step: 1, Group: "Parallelism", Flag: "--num_gpus",
			Description: "Devices the deployment claims."},
		{Name: "tensor_model_parallel_size", Label: "Tensor parallel", Type: v1.ParamType_PARAM_TYPE_INT, Default: "1", Unit: "devices", Min: 1, Step: 1, Group: "Parallelism", Flag: "--tensor_model_parallel_size",
			Description: "Devices each layer is sharded across."},
		{Name: "pipeline_model_parallel_size", Label: "Pipeline parallel", Type: v1.ParamType_PARAM_TYPE_INT, Default: "1", Unit: "devices", Min: 1, Step: 1, Group: "Parallelism", Flag: "--pipeline_model_parallel_size",
			Description: "Devices the layer stack is split across."},
		{Name: "enable_cuda_graphs", Label: "CUDA graphs", Type: v1.ParamType_PARAM_TYPE_BOOL, Default: "false", Group: "Performance", Flag: "--enable_cuda_graphs",
			Description: "Capture CUDA graphs for faster decoding."},
		{Name: "enable_flash_decode", Label: "Flash decode", Type: v1.ParamType_PARAM_TYPE_BOOL, Default: "false", Group: "Performance", Flag: "--enable_flash_decode",
			Description: "Flash attention during decoding."},
		{Name: "served_model_name", Label: "Served name", Type: v1.ParamType_PARAM_TYPE_STRING, Group: "Identity", Flag: "--model_id",
			Description: "Model name the runtime reports. Empty takes the instance name."},
		{Name: "model_id", Label: "Base model id", Type: v1.ParamType_PARAM_TYPE_STRING, Group: "Conversion", Advanced: true,
			Description: "Hugging Face id of the base model the converter builds a packed checkpoint as. Empty takes the checkpoint's tokenizer id."},
		{Name: "legacy_ckpt", Label: "Legacy checkpoint", Type: v1.ParamType_PARAM_TYPE_BOOL, Default: "false", Group: "Conversion", Advanced: true, Flag: "--legacy_ckpt",
			Description: "Load a checkpoint written before Megatron Bridge."},
		{Name: "cuda_visible_devices", Label: "Visible devices", Type: v1.ParamType_PARAM_TYPE_STRING, Group: "Parallelism", Advanced: true, Flag: "--cuda_visible_devices",
			Description: "Device indexes the deployment is pinned to. Empty takes the slot's devices."},
	}
}

func (r NeMo) Launch(in Launch) (*Command, error) {
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

// A packed .nemo archive is converted to a NeMo 2 directory before its first launch
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

// In framework serving reports no allocations nebu reads
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
			// In framework inference drafts with nothing, prediction heads stay on disk
			{Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_DRAFT, Pool: device, Loaded: func(*estimate.Scope) bool { return false }},
			{Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_OTHER, Pool: device},
		},
		// In framework inference keeps a full length key and value cache per batched sequence
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
