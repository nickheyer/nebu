package triage

import (
	"strings"
	"testing"
)

type fakeSet struct{ rules []Rule }

func (fakeSet) ID() string          { return "x" }
func (fakeSet) Description() string { return "fake" }
func (s fakeSet) Rules() []Rule     { return s.rules }

func TestScan(t *testing.T) {
	set := fakeSet{rules: []Rule{
		{ID: "arch", Summary: "no ${arch}", Hint: "build for ${arch}", Match: func(line string) (map[string]string, bool) {
			arch, ok := quotedAfter(line, "unknown model architecture: '", "'")
			if !ok {
				return nil, false
			}
			return map[string]string{"arch": arch}, true
		}},
		{ID: "oom", Summary: "oom", Hint: "lower ctx", Match: anyOf("out of memory")},
	}}
	lines := []string{"loading", "unknown model architecture: 'qwen9'", "CUDA out of memory", "unknown model architecture: 'other'"}
	hits := Scan([]Set{set}, lines)
	if len(hits) != 2 || hits[0].GetId() != "arch" || hits[0].GetSummary() != "no qwen9" || hits[0].GetHint() != "build for qwen9" {
		t.Fatalf("hits %v", hits)
	}
	if hits[1].GetId() != "oom" || hits[1].GetLine() != "CUDA out of memory" {
		t.Fatalf("hits %v", hits)
	}
	if len(Scan([]Set{set}, []string{"fine"})) != 0 {
		t.Fatal("no hits expected")
	}
}

func TestProjectorTriageLeadsWithTheType(t *testing.T) {
	lines := []string{
		"0.16.588.512 E clip_init: failed to load model '/m/mmproj.gguf': load_hparams: unknown projector type: nemotron_omni",
		"0.16.588.583 E mtmd_init_from_file: error: Failed to load CLIP model from /m/mmproj.gguf",
		"0.16.588.589 E srv    load_model: failed to load multimodal model, '/m/mmproj.gguf'",
	}
	hits := Scan([]Set{LlamaCpp{}}, lines)
	if len(hits) == 0 || hits[0].GetId() != "unknown-projector" || hits[0].GetSummary() != "this build does not know projector type nemotron_omni" {
		t.Fatalf("hits %v", hits)
	}
}

func TestShippedRules(t *testing.T) {
	cases := []struct {
		set  Set
		line string
		id   string
	}{
		{LlamaCpp{}, "ggml_backend_cuda_buffer_type_alloc_buffer: allocating 5000.00 MiB on device 0: cudaMalloc failed: out of memory", "device-oom"},
		{LlamaCpp{}, "llama_model_load: error loading model: unknown model architecture: 'qwen9'", "unknown-arch"},
		{LlamaCpp{}, "V cache quantization requires flash_attn", "cache-quant-needs-flash"},
		{LlamaCpp{}, `error while handling argument "--nope": unknown`, "bad-flag"},
		{LlamaCpp{}, "error: unknown device: Metal0", "unknown-device"},
		{LlamaCpp{}, `error while handling argument "--device": invalid device: Metal0`, "unknown-device"},
		{LlamaCpp{}, "llama_model_load: error loading model: error loading model hyperparameters: key qwen35.rope.dimension_sections has wrong array length; expected 4, got 3", "gguf-hparams"},
		{LlamaCpp{}, "0.16.588.512 E clip_init: failed to load model '/m/mmproj.gguf': load_hparams: unknown projector type: nemotron_omni", "unknown-projector"},
		{LlamaCpp{}, "0.16.588.589 E srv    load_model: failed to load multimodal model, '/m/mmproj.gguf'", "projector-load"},
		{VLLM{}, "torch.OutOfMemoryError: CUDA out of memory", "device-oom"},
		{VLLM{}, "ValueError: Model architectures ['FooForCausalLM'] are not supported for now", "unsupported-arch"},
		{SGLang{}, "RuntimeError: Not enough memory. Please try to increase --mem-fraction-static", "pool-too-small"},
		{SGLang{}, "ImportError: sgl_kernel undefined symbol", "kernel-mismatch"},
		{NeMo{}, "RuntimeError: Missing key(s) in state_dict", "state-dict-mismatch"},
		{NeMo{}, "ValueError: model_id foo not in MODEL_CONFIG_MAPPING", "convert-model-id"},
	}
	for _, c := range cases {
		hits := Scan([]Set{c.set}, []string{c.line})
		found := false
		for _, h := range hits {
			if h.GetId() == c.id {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: %q should hit %s, got %v", c.set.ID(), c.line, c.id, hits)
		}
	}
	for _, r := range (LlamaCpp{}).Rules() {
		if r.ID != "unknown-arch" {
			continue
		}
		if names, ok := r.Match("unknown model architecture: 'x'"); !ok || names["arch"] != "x" {
			t.Fatalf("arch capture %v %v", names, ok)
		}
	}
}

// An allocation that failed is named by what was being allocated, the hint naming the settings that size it and the generic account giving way
func TestLlamaCppNamesWhatDidNotFit(t *testing.T) {
	compute := []string{
		"0.01.114.178 W llama_model_loader: tensor overrides to CPU are used with mmap enabled - consider using --load-mode none for better performance",
		"0.16.151.840 E ggml_backend_cuda_buffer_type_alloc_buffer: allocating 301196.26 MiB on device 0: cudaMalloc failed: out of memory",
		"0.16.151.846 E ggml_gallocr_reserve_n_impl: failed to allocate CUDA0 buffer of size 315827167360",
		"0.16.151.847 E graph_reserve: failed to allocate compute buffers",
		"0.16.493.571 E llama_init_from_model: failed to initialize the context: failed to allocate compute pp buffers",
		"0.16.493.603 E srv    load_model: failed to create_context with model '/m/model.gguf'",
		"0.16.508.452 E srv  server_run_i: exiting due to model loading error",
	}
	hits := Scan([]Set{LlamaCpp{}}, compute)
	if len(hits) != 1 || hits[0].GetId() != "compute-oom" || !strings.Contains(hits[0].GetHint(), "n_ubatch") {
		t.Fatalf("compute buffers: %v", hits)
	}
	weights := []string{
		"0.00.536.830 E llama_init_from_model: model does not support a quantized V cache (q4_0)",
		"0.00.579.032 E common_fit_params: encountered an error while trying to fit params to free device memory: failed to create llama_context from model",
		"0.11.630.235 E ggml_backend_cuda_buffer_type_alloc_buffer: allocating 139689.51 MiB on device 0: cudaMalloc failed: out of memory",
		"0.11.630.241 E ggml_backend_buft_alloc_buffer_n_default: failed to allocate CUDA0 buffer of size 146475068032",
		"0.15.909.608 E llama_model_load: error loading model: unable to allocate CUDA0 buffer",
		"0.15.909.620 E llama_model_load_from_file_impl: failed to load model",
	}
	hits = Scan([]Set{LlamaCpp{}}, weights)
	if len(hits) != 2 || hits[0].GetId() != "cache-type-unsupported" || hits[0].GetSummary() != "this model cannot use a quantized V cache" || !strings.Contains(hits[0].GetHint(), "cache_type_v to f16") || hits[1].GetId() != "weights-oom" {
		t.Fatalf("weights: %v", hits)
	}
	cache := Scan([]Set{LlamaCpp{}}, []string{"ggml_backend_cuda_buffer_type_alloc_buffer: allocating 9000.00 MiB on device 0: cudaMalloc failed: out of memory", "llama_kv_cache: failed to allocate buffer for kv cache"})
	if len(cache) != 1 || cache[0].GetId() != "cache-oom" || !strings.Contains(cache[0].GetHint(), "kv_offload") {
		t.Fatalf("cache: %v", cache)
	}
}

// A rejected device name outranks the bad flag
func TestLlamaCppUnknownDevice(t *testing.T) {
	hits := Scan([]Set{LlamaCpp{}}, []string{`error while handling argument "--device": invalid device: Metal0`, "available devices:", "  MTL0: Apple M1 Max (53084 MiB, 53083 MiB free)"})
	if len(hits) < 2 || hits[0].GetId() != "unknown-device" || hits[0].GetSummary() != "this build has no device named Metal0" || hits[1].GetId() != "bad-flag" {
		t.Fatalf("hits %v", hits)
	}
	if len(Scan([]Set{LlamaCpp{}}, []string{"  MTL0: Apple M1 Max (53084 MiB, 53083 MiB free)", "ggml_metal_device_init: GPU name:   MTL0 (Apple M1 Max)"})) != 0 {
		t.Fatal("device listings are not failures")
	}
}

func TestLlamaCppHparams(t *testing.T) {
	lines := []string{
		"llama_model_load: error loading model: error loading model hyperparameters: key qwen35.rope.dimension_sections has wrong array length; expected 4, got 3",
		"llama_model_load_from_file_impl: failed to load model",
	}
	hits := Scan([]Set{LlamaCpp{}}, lines)
	if len(hits) < 2 || hits[0].GetId() != "gguf-hparams" || hits[1].GetId() != "model-load" {
		t.Fatalf("hits %v", hits)
	}
	if want := "this build cannot read the model's hyperparameters, qwen35.rope.dimension_sections holds 3 values where it wants 4"; hits[0].GetSummary() != want {
		t.Fatalf("summary %q", hits[0].GetSummary())
	}
	plain := Scan([]Set{LlamaCpp{}}, []string{"llama_model_load: error loading model: error loading model hyperparameters: key foo has wrong type"})
	if len(plain) == 0 || plain[0].GetId() != "gguf-hparams" || plain[0].GetSummary() != "this build cannot read the model's hyperparameters" {
		t.Fatalf("plain hits %v", plain)
	}
}

func TestLlamaCppBadFlag(t *testing.T) {
	for _, tc := range []struct {
		line string
		flag string
	}{
		{`error while handling argument "--ctx-size": invalid value`, "--ctx-size"},
		{`error: invalid argument: --missing`, "--missing"},
		{`error: unknown argument: '--missing'`, "--missing"},
		{`error: unrecognized argument '-x'`, "-x"},
		{`error: invalid argument`, "a launch argument"},
	} {
		hits := Scan([]Set{LlamaCpp{}}, []string{tc.line})
		if len(hits) != 1 || hits[0].GetId() != "bad-flag" || !strings.Contains(hits[0].GetSummary(), tc.flag) {
			t.Errorf("%q: expected rejected argument %q, got %v", tc.line, tc.flag, hits)
			continue
		}
		if hits[0].GetHint() == "" || strings.Contains(hits[0].GetSummary()+hits[0].GetHint(), "${") {
			t.Errorf("%q: incomplete diagnostic: %v", tc.line, hits[0])
		}
	}

	line := `error while handling argument "--draft-max": the argument has been removed. use --spec-draft-n-max or --spec-ngram-mod-n-max`
	hits := Scan([]Set{LlamaCpp{}}, []string{line})
	if len(hits) != 1 || hits[0].GetId() != "bad-flag" {
		t.Fatalf("removed argument: %v", hits)
	}
	if !strings.Contains(hits[0].GetSummary(), "--draft-max") || !strings.Contains(hits[0].GetHint(), "--spec-draft-n-max") || !strings.Contains(hits[0].GetHint(), "--spec-ngram-mod-n-max") {
		t.Fatalf("removed argument or replacements missing: %v", hits[0])
	}
	if hits[0].GetLine() != line {
		t.Fatalf("original runtime error was not preserved: %v", hits[0])
	}
}

// The head's mmap failure while streaming a stage's share is the direct io signature; allocation
// failures elsewhere are not
func TestLlamaCppDirectIO(t *testing.T) {
	for _, line := range []string{
		"llama_model_load: error loading model: mmap failed: Cannot allocate memory",
		"llama_model_load: error loading model: MapViewOfFile failed: Not enough memory resources are available to process this command.",
	} {
		hits := Scan([]Set{LlamaCpp{}}, []string{line})
		found := false
		for _, h := range hits {
			if h.GetId() == "rpc-share-mmap" && strings.Contains(h.GetHint(), "load_mode") {
				found = true
			}
		}
		if !found {
			t.Errorf("%q should hit rpc-share-mmap naming load_mode, got %v", line, hits)
		}
	}
	for _, line := range []string{
		"ggml_backend_cuda_buffer_type_alloc_buffer: allocating 5000.00 MiB on device 0: cudaMalloc failed: out of memory",
		"terminate called after throwing an instance of 'std::bad_alloc'",
		"llama_kv_cache: failed to allocate buffer for kv cache",
		"warning: munmap failed: Invalid argument",
	} {
		for _, h := range Scan([]Set{LlamaCpp{}}, []string{line}) {
			if h.GetId() == "rpc-share-mmap" {
				t.Errorf("%q is not the mmap signature", line)
			}
		}
	}
}
