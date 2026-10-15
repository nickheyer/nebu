package runtimes

import (
	"strings"
	"testing"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// Every launch names the parser its model family writes, the first the install lists
func TestToolParserRules(t *testing.T) {
	for _, c := range []struct {
		arch, repo, listed string
		vllm, sglang       string
	}{
		{"Qwen3ForCausalLM", "Qwen/Qwen3-8B", "", "hermes", "qwen25"},
		{"Qwen3MoeForCausalLM", "Qwen/Qwen3-Coder-480B-A35B-Instruct", "", "qwen3_coder", "qwen3_coder"},
		{"Qwen3MoeForCausalLM", "Qwen/Qwen3-Coder-480B-A35B-Instruct", "hermes,llama3_json", "hermes", "qwen3_coder"},
		{"LlamaForCausalLM", "meta-llama/Llama-3.1-8B-Instruct", "", "llama3_json", "llama3"},
		{"Llama4ForConditionalGeneration", "meta-llama/Llama-4-Scout", "", "llama4_pythonic", "llama4"},
		{"MistralForCausalLM", "mistralai/Mistral-7B-Instruct-v0.3", "", "mistral", "mistral"},
		{"DeepseekV3ForCausalLM", "deepseek-ai/DeepSeek-V3", "", "deepseek_v3", "deepseekv3"},
		{"DeepseekV3ForCausalLM", "deepseek-ai/DeepSeek-V3.1", "", "deepseek_v31", "deepseekv31"},
		{"DeepseekV3ForCausalLM", "moonshotai/Kimi-K2-Instruct", "", "kimi_k2", "kimi_k2"},
		{"Glm4MoeForCausalLM", "zai-org/GLM-4.5", "", "glm45", "glm45"},
		{"GptOssForCausalLM", "openai/gpt-oss-20b", "", "openai", "gpt-oss"},
		{"Gemma3ForConditionalGeneration", "google/gemma-3-12b-it", "", "hermes", "qwen25"},
		{"", "", "", "hermes", "qwen25"},
	} {
		in := Launch{Descriptor: &v1.Descriptor{Architecture: c.arch}, Model: &v1.StoredModel{Repo: c.repo}, InstallRecord: &v1.Install{Facts: map[string]string{factToolParsers: c.listed}}}
		if got := vllmToolParser(in); got != c.vllm {
			t.Errorf("%s %s vllm: %s", c.arch, c.repo, got)
		}
		if got := sglangToolParser(in); got != c.sglang {
			t.Errorf("%s %s sglang: %s", c.arch, c.repo, got)
		}
	}
}

// The help's brace list of parsers is recorded, nothing when the flag is missing
func TestChoicesProbe(t *testing.T) {
	p := choicesProbe(factToolParsers, []string{"serve", "--help"}, "--tool-call-parser")
	got, ok := p.Parse("  --tool-call-parser {deepseek_v3,hermes, llama3_json,\n mistral}\n   Select the parser")
	if !ok || got != "deepseek_v3,hermes,llama3_json,mistral" {
		t.Fatalf("%q %v", got, ok)
	}
	if _, ok := p.Parse("--tool-call-parser TOOL_CALL_PARSER\n"); ok {
		t.Fatal("no braces is no list")
	}
	if _, ok := p.Parse("--other {a,b}"); ok {
		t.Fatal("another flag's list is not this one")
	}
}

// vLLM and SGLang launch with their tool call parser, and llama.cpp with the jinja flag its build lists
func TestLaunchToolFlags(t *testing.T) {
	gpu := &v1.Device{Id: "GPU-0", Kind: v1.DeviceKind_DEVICE_KIND_GPU, Vendor: "nvidia", Facts: map[string]string{"index": "0"}}
	in := Launch{Name: "m", Params: map[string]any{}, Artifacts: map[string]string{"weights_dir": "/w", "weights": "/w.gguf"}, Host: "127.0.0.1", Port: 9, Install: Install{Path: "/venv/bin/vllm"}, Devices: []*v1.Device{gpu}, Descriptor: &v1.Descriptor{Architecture: "LlamaForCausalLM"}, Model: &v1.StoredModel{Repo: "meta-llama/Llama-3.1-8B"}, InstallRecord: &v1.Install{Facts: map[string]string{}}}
	cmd, err := VLLM{}.Launch(in)
	if err != nil {
		t.Fatal(err)
	}
	if args := strings.Join(cmd.Args, " "); !strings.Contains(args, "--enable-auto-tool-choice --tool-call-parser llama3_json") {
		t.Fatalf("vllm args %s", args)
	}
	cmd, err = SGLang{}.Launch(in)
	if err != nil {
		t.Fatal(err)
	}
	if args := strings.Join(cmd.Args, " "); !strings.Contains(args, "--tool-call-parser llama3") {
		t.Fatalf("sglang args %s", args)
	}
	in.Install = Install{Path: "/bin/llama-server"}
	cmd, err = LlamaCpp{}.Launch(in)
	if err != nil {
		t.Fatal(err)
	}
	if args := strings.Join(cmd.Args, " "); strings.Contains(args, "--jinja") {
		t.Fatalf("a build without the flag does not get it: %s", args)
	}
	in.InstallRecord = &v1.Install{Facts: map[string]string{factJinja: "--jinja"}}
	cmd, err = LlamaCpp{}.Launch(in)
	if err != nil {
		t.Fatal(err)
	}
	if args := strings.Join(cmd.Args, " "); !strings.Contains(args, "--jinja") {
		t.Fatalf("llama.cpp args %s", args)
	}
}
