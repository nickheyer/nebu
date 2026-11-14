package runtimes

import (
	"encoding/json"
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
	const (
		groupContext     = "Context"
		groupParallel    = "Parallelism"
		groupPerformance = "Performance"
		groupIdentity    = "Identity"
		groupServing     = "Serving"
		groupConversion  = "Conversion"
	)
	intParam := func(name, label, group, flag, def, unit, description string, min, max, step float64, advanced bool) *v1.Param {
		return &v1.Param{Name: name, Label: label, Type: v1.ParamType_PARAM_TYPE_INT, Default: def, Unit: unit, Min: min, Max: max, Step: step, Group: group, Flag: flag, Advanced: advanced, Description: description}
	}
	boolParam := func(name, label, group, flag, description string, advanced bool) *v1.Param {
		return &v1.Param{Name: name, Label: label, Type: v1.ParamType_PARAM_TYPE_BOOL, Default: "false", Group: group, Flag: flag, Advanced: advanced, Description: description}
	}
	strParam := func(name, label, group, flag, description string) *v1.Param {
		return &v1.Param{Name: name, Label: label, Type: v1.ParamType_PARAM_TYPE_STRING, Group: group, Flag: flag, Advanced: true, Description: description}
	}
	return []*v1.Param{
		{Name: "n_ctx", Label: "Context length", Type: v1.ParamType_PARAM_TYPE_INT, Default: Auto, Solved: true, Unit: "tokens", Min: 256, Max: contextSpan, Step: 256, Group: groupContext, Flag: "--inference_max_seq_length",
			Rule: contextRule},
		intParam("max_batch_size", "Batch size", groupContext, "--max_batch_size", "32", "sequences", "requests served in one batch, each keeping a full length cache", 1, 0, 1, false),
		placeholder("runtime's choice", intParam("micro_batch_size", "Micro-batch size", groupContext, "--micro_batch_size", "", "sequences", "sequences in one forward pass", 1, 0, 1, true)),
		{Name: "batch_wait_timeout_s", Label: "Batch wait", Type: v1.ParamType_PARAM_TYPE_FLOAT, Min: 0, Max: 10, Step: 0.05, Unit: "seconds", Group: groupContext, Flag: "--batch_wait_timeout_s", Advanced: true,
			Placeholder: "0.1", Description: "how long a request waits for others to fill its batch"},
		intParam("num_gpus", "Devices", groupParallel, "--num_gpus", "1", "devices", "", 1, 0, 1, false),
		intParam("tensor_model_parallel_size", "Tensor parallel", groupParallel, "--tensor_model_parallel_size", "1", "devices", "", 1, 0, 1, false),
		intParam("pipeline_model_parallel_size", "Pipeline parallel", groupParallel, "--pipeline_model_parallel_size", "1", "devices", "", 1, 0, 1, false),
		placeholder("split evenly", intParam("num_layers_in_first_pipeline_stage", "First stage layers", groupParallel, "--num_layers_in_first_pipeline_stage", "", "layers", "layers the first pipeline stage holds", 1, 0, 1, true)),
		placeholder("split evenly", intParam("num_layers_in_last_pipeline_stage", "Last stage layers", groupParallel, "--num_layers_in_last_pipeline_stage", "", "layers", "layers the last pipeline stage holds", 1, 0, 1, true)),
		boolParam("account_for_embedding_in_pipeline_split", "Count embedding as a stage layer", groupParallel, "--account_for_embedding_in_pipeline_split", "weigh the embedding like a layer when splitting the pipeline", true),
		boolParam("account_for_loss_in_pipeline_split", "Count output as a stage layer", groupParallel, "--account_for_loss_in_pipeline_split", "weigh the output head like a layer when splitting the pipeline", true),
		placeholder("1", intParam("expert_model_parallel_size", "Expert parallel", groupParallel, "--expert_model_parallel_size", "", "devices", "", 1, 0, 1, true)),
		placeholder("1", intParam("expert_tensor_parallel_size", "Expert tensor parallel", groupParallel, "--expert_tensor_parallel_size", "", "devices", "", 1, 0, 1, true)),
		placeholder("1", intParam("context_parallel_size", "Context parallel", groupParallel, "--context_parallel_size", "", "devices", "", 1, 0, 1, true)),
		boolParam("sequence_parallel", "Sequence parallel", groupParallel, "--sequence_parallel", "split sequence activations over the tensor parallel devices", true),
		placeholder("1", intParam("num_replicas", "Replicas", groupParallel, "--num_replicas", "", "replicas", "model copies Ray Serve runs", 1, 0, 1, true)),
		placeholder("every core", intParam("num_cpus", "Ray CPUs", groupParallel, "--num_cpus", "", "cores", "cores the Ray cluster takes", 1, 0, 1, true)),
		{Name: "num_cpus_per_replica", Label: "CPUs per replica", Type: v1.ParamType_PARAM_TYPE_FLOAT, Min: 0.5, Step: 0.5, Unit: "cores", Group: groupParallel, Flag: "--num_cpus_per_replica", Advanced: true,
			Placeholder: "8"},
		{Name: "cuda_visible_devices", Label: "Visible devices", Type: v1.ParamType_PARAM_TYPE_STRING, Group: groupParallel, Advanced: true, Flag: "--cuda_visible_devices", Description: "0,1", Placeholder: "the launch's devices"},
		{Name: "enable_cuda_graphs", Label: "CUDA graphs", Type: v1.ParamType_PARAM_TYPE_BOOL, Default: "false", Group: groupPerformance, Flag: "--enable_cuda_graphs"},
		{Name: "enable_flash_decode", Label: "Flash decode", Type: v1.ParamType_PARAM_TYPE_BOOL, Default: "false", Group: groupPerformance, Flag: "--enable_flash_decode"},
		boolParam("legacy_model_format", "Legacy inference engine", groupPerformance, "--legacy_model_format", "use the static inference engine path", true),
		placeholder("random", intParam("random_seed", "Seed", groupPerformance, "--random_seed", "", "", "sampling seed", 0, 0, 1, true)),
		{Name: "served_model_name", Label: "Served name", Type: v1.ParamType_PARAM_TYPE_STRING, Group: groupIdentity, Flag: "--model_id", Placeholder: "the launch's name"},
		placeholder("gpt", strParam("model_type", "Model type", groupServing, "--model_type", "model family the checkpoint loads as")),
		boolParam("include_dashboard", "Ray dashboard", groupServing, "--include_dashboard", "serve the Ray dashboard beside the model", true),
		strParam("runtime_env", "Ray runtime environment", groupServing, "--runtime_env", "a JSON object Ray gives every replica, such as environment variables"),
		{Name: "model_id", Label: "Base model id", Type: v1.ParamType_PARAM_TYPE_STRING, Group: groupConversion, Advanced: true,
			Description: "Hugging Face id the converter builds the NeMo 2 config from"},
		{Name: "legacy_ckpt", Label: "Legacy checkpoint", Type: v1.ParamType_PARAM_TYPE_BOOL, Default: "false", Group: groupConversion, Advanced: true, Flag: "--legacy_ckpt"},
		{Name: "tokenizer_path", Label: "Converter tokenizer", Type: v1.ParamType_PARAM_TYPE_PATH, Group: groupConversion, Advanced: true, Placeholder: "checkpoint or base model", Description: "tokenizer file for a packed checkpoint"},
		placeholder("sentencepiece", strParam("tokenizer_library", "Converter tokenizer library", groupConversion, "", "sentencepiece, megatron, or huggingface")),
		{Name: "tokenizer_vocab_file", Label: "Converter vocab file", Type: v1.ParamType_PARAM_TYPE_PATH, Group: groupConversion, Advanced: true, Description: "vocabulary file for a megatron tokenizer"},
		placeholder("TiktokenTokenizer", strParam("tokenizer_model_name", "Converter tokenizer model", groupConversion, "", "tokenizer model name")),
	}
}

// The converter options the conversion params carry, each by its flag
var nemoConverterFlags = []struct{ param, flag string }{
	{"tokenizer_path", "--tokenizer_path"},
	{"tokenizer_library", "--tokenizer_library"},
	{"tokenizer_vocab_file", "--tokenizer_vocab_file"},
	{"tokenizer_model_name", "--tokenizer_model_name"},
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
	devices, err := launchDevices(in)
	if err != nil {
		return nil, err
	}
	if p.Str("cuda_visible_devices") == "" {
		p["cuda_visible_devices"] = visible(devices, "nvidia", true)
	}
	for _, f := range nemoConverterFlags {
		delete(p, f.param)
	}
	metrics := in.Port + 1
	ray, err := nemoRayInit(p, metrics)
	if err != nil {
		return nil, err
	}
	args := []string{"-c", nemoBootstrap, ray, in.Install.Path, "--megatron_checkpoint", dir, "--host", in.Host, "--port", strconv.Itoa(in.Port)}
	flags, env, emitted := Flags(r.Params(), p)
	emitted["metrics_port"] = strconv.Itoa(metrics)
	pinEnv(env, devices)
	return &Command{Command: in.Install.Dir + "/venv/bin/python", Args: append(args, flags...), Env: env, Params: emitted, Devices: devices}, nil
}

// Starts a Ray cluster of the launch's own with its metrics on a fixed port, then runs the deploy script, whose own init joins it
const nemoBootstrap = `import json, runpy, sys
import ray
ray.init(**json.loads(sys.argv[1]))
sys.argv = sys.argv[2:]
runpy.run_path(sys.argv[0], run_name="__main__")
`

// The init arguments the deploy script would give Ray, a cluster of the launch's own, and the metrics export port
func nemoRayInit(p estimate.Params, metrics int) (string, error) {
	options := map[string]any{"address": "local", "_metrics_export_port": metrics, "include_dashboard": p.Bool("include_dashboard")}
	if n := p.Int("num_gpus"); n > 0 {
		options["num_gpus"] = n
	}
	if n := p.Int("num_cpus"); n > 0 {
		options["num_cpus"] = n
	}
	if env := strings.TrimSpace(p.Str("runtime_env")); env != "" {
		var parsed map[string]any
		if err := json.Unmarshal([]byte(env), &parsed); err != nil {
			return "", fmt.Errorf("%w: runtime_env is not a JSON object: %v", ErrParam, err)
		}
		options["runtime_env"] = parsed
	}
	data, err := json.Marshal(options)
	return string(data), err
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
	args := []string{in.Install.Dir + "/convert_nemo1_to_nemo2.py", "--input_path", weights, "--output_path", out, "--model_id", modelID}
	params := map[string]string{"model_id": modelID}
	for _, f := range nemoConverterFlags {
		if v := in.Params.Str(f.param); v != "" {
			args = append(args, f.flag, v)
			params[f.param] = v
		}
	}
	return &Command{
		Command: in.Install.Dir + "/venv-convert/bin/python",
		Args:    args,
		Env:     map[string]string{},
		Params:  params,
	}, nil
}

func (NeMo) PrepareTimeout() time.Duration { return 2 * time.Hour }
func (NeMo) Health() Health {
	return Health{Path: "/v1/health", Interval: 5 * time.Second, SlowAfter: time.Hour}
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
		States: func(s *estimate.Scope) []*v1.ParamState {
			gpus := float64(len(host.Kind(s.Host, v1.DeviceKind_DEVICE_KIND_GPU)))
			return []*v1.ParamState{
				bounds("n_ctx", 256, s.Model.ContextTrain, 256),
				bounds("micro_batch_size", 1, float64(s.Params.Int("max_batch_size")), 1),
				bounds("num_gpus", 1, gpus, 1),
				bounds("tensor_model_parallel_size", 1, gpus, 1),
				bounds("pipeline_model_parallel_size", 1, gpus, 1),
				bounds("num_layers_in_first_pipeline_stage", 1, s.Model.Layers, 1),
				bounds("num_layers_in_last_pipeline_stage", 1, s.Model.Layers, 1),
				bounds("expert_model_parallel_size", 1, gpus, 1),
				bounds("expert_tensor_parallel_size", 1, gpus, 1),
				bounds("context_parallel_size", 1, gpus, 1),
				bounds("num_cpus", 1, cpuThreads(s.Host), 1),
			}
		},
		Groups: []estimate.GroupRule{
			{Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_EMBEDDING, Pool: device},
			{Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_LAYER, Pool: device},
			{Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_EXPERTS, Pool: device},
			{Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_OUTPUT, Pool: device},
			{Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_VISION, Pool: device},
			{Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_AUDIO, Pool: device},
			// This serving mode does not load prediction heads.
			{Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_DRAFT, Pool: device, Loaded: func(*estimate.Scope, *v1.TensorGroup) bool { return false }},
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
	}
}
