package triage

// The failures NeMo Export-Deploy in framework serving prints
type NeMo struct{}

func (NeMo) ID() string { return "nemo" }
func (NeMo) Description() string {
	return "Failure patterns of NeMo Export-Deploy in framework serving"
}

func (NeMo) Rules() []Rule {
	return []Rule{
		{
			ID:      "device-oom",
			Summary: "device ran out of memory",
			Hint:    "lower n_ctx or max_batch_size, or shard with tensor_model_parallel_size and num_gpus",
			Match:   anyOf("torch.OutOfMemoryError", "CUDA out of memory", "out of memory"),
		},
		{
			ID:      "not-nemo2",
			Summary: "the checkpoint is not a NeMo 2 directory",
			Hint:    "this serving mode requires NeMo 2 checkpoints with context/ and weights/. Convert packed .nemo files first",
			Match: func(line string) (map[string]string, bool) {
				if _, ok := anyOf("not a valid NeMo 2", "missing context director", "missing weights director")(line); ok {
					return nil, true
				}
				if _, ok := anyOf("context", "weights")(line); !ok {
					return nil, false
				}
				return anyOf("does not exist", "not found", "no such file")(line)
			},
		},
		{
			ID:      "state-dict-mismatch",
			Summary: "the checkpoint keys do not match the model the config builds",
			Hint:    "turn on legacy_ckpt for checkpoints written before Megatron Bridge",
			Fix:     map[string]string{"legacy_ckpt": "true"},
			Match:   anyOf("Missing key(s) in state_dict", "Unexpected key(s) in state_dict", "legacy checkpoint"),
		},
		{
			ID:      "convert-model-id",
			Summary: "the converter does not know this checkpoint's base model",
			Hint:    "set model_id to the Hugging Face id of the base model the converter lists, such as meta-llama/Meta-Llama-3-8B",
			Match: func(line string) (map[string]string, bool) {
				if _, ok := anyOf("is not a valid model_id", "Available model listed in MODEL_CONFIG_MAPPING", "needs the base model's Hugging Face id")(line); ok {
					return nil, true
				}
				if _, ok := allOf("model_id", "not in")(line); ok {
					return nil, true
				}
				return allOf("model_id", "not supported")(line)
			},
		},
		{
			ID:      "convert-failed",
			Summary: "converting the packed checkpoint failed",
			Hint:    "check the converter output for supported model families",
			Match: func(line string) (map[string]string, bool) {
				if _, ok := anyOf("convert_nemo1_to_nemo2", "nemo-serve: converting")(line); !ok {
					return nil, false
				}
				return anyOf("Traceback", "Error")(line)
			},
		},
		{
			ID:      "missing-module",
			Summary: "the install is missing a Python package",
			Hint:    "rebuild the nemo recipe, the virtual environment is incomplete",
			Match:   anyOf("ModuleNotFoundError", "No module named"),
		},
		{
			ID:      "ray-start",
			Summary: "Ray could not start or lost a worker",
			Hint:    "stop other Ray instances on the host and free its ports, then run again",
			Match:   anyOf("Failed to start Ray", "Could not connect to Ray", "ray.exceptions", "RayTaskError"),
		},
		{
			ID:      "port-in-use",
			Summary: "the port is taken",
			Hint:    "free the port or rerun the model to use another port",
			Match:   anyOf("address already in use"),
		},
	}
}
