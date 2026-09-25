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
		{ID: "arch", Summary: "no ${arch}", Hint: "build for ${arch}", Fix: map[string]string{"a": "b"}, Match: func(line string) (map[string]string, bool) {
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
	if len(hits) != 2 || hits[0].GetId() != "arch" || hits[0].GetSummary() != "no qwen9" || hits[0].GetHint() != "build for qwen9" || hits[0].GetFix()["a"] != "b" {
		t.Fatalf("hits %v", hits)
	}
	if hits[1].GetId() != "oom" || hits[1].GetLine() != "CUDA out of memory" {
		t.Fatalf("hits %v", hits)
	}
	if len(Scan([]Set{set}, []string{"fine"})) != 0 {
		t.Fatal("no hits expected")
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
		{LlamaCpp{}, "llama_model_load: error loading model: error loading model hyperparameters: key qwen35.rope.dimension_sections has wrong array length; expected 4, got 3", "gguf-hparams"},
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
	unknown := LlamaCpp{}.Rules()[1]
	if names, ok := unknown.Match("unknown model architecture: 'x'"); !ok || names["arch"] != "x" {
		t.Fatalf("arch capture %v %v", names, ok)
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
			if h.GetId() == "rpc-share-mmap" && h.GetFix()["direct_io"] == "true" {
				found = true
			}
		}
		if !found {
			t.Errorf("%q should hit rpc-share-mmap with the direct_io fix, got %v", line, hits)
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
