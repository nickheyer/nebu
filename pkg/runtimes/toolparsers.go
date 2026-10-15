package runtimes

import (
	"strings"
)

// The install fact listing the tool call parsers a runtime's help names
const factToolParsers = "tool_parsers"

// One model family's parser on each runtime, matched by architecture or repo name, first match winning
type parserRule struct {
	// Lower case substrings of the architecture, any matching
	archs []string
	// Lower case substrings of the repo name, any matching
	repos []string
	// Parser names in order of preference on each runtime
	vllm, sglang []string
}

var parserRules = []parserRule{
	{repos: []string{"kimi-k2", "kimi_k2", "kimi"}, vllm: []string{"kimi_k2"}, sglang: []string{"kimi_k2"}},
	{repos: []string{"qwen3-coder", "qwen3_coder", "qwen3coder"}, vllm: []string{"qwen3_coder", "qwen3_xml", "hermes"}, sglang: []string{"qwen3_coder", "qwen25"}},
	{archs: []string{"qwen"}, vllm: []string{"hermes"}, sglang: []string{"qwen25"}},
	{archs: []string{"llama4"}, vllm: []string{"llama4_pythonic", "llama4_json", "llama3_json"}, sglang: []string{"llama4", "llama3"}},
	{archs: []string{"llama"}, vllm: []string{"llama3_json"}, sglang: []string{"llama3"}},
	{archs: []string{"mistral", "mixtral", "pixtral"}, vllm: []string{"mistral"}, sglang: []string{"mistral"}},
	{archs: []string{"deepseekv3", "deepseek_v3"}, repos: []string{"v3.1", "v31", "v3_1"}, vllm: []string{"deepseek_v31", "deepseek_v3"}, sglang: []string{"deepseekv31", "deepseekv3"}},
	{archs: []string{"deepseek"}, vllm: []string{"deepseek_v3"}, sglang: []string{"deepseekv3"}},
	{archs: []string{"glm4"}, vllm: []string{"glm45"}, sglang: []string{"glm45"}},
	{archs: []string{"gptoss", "gpt_oss"}, vllm: []string{"openai"}, sglang: []string{"gpt-oss"}},
	{archs: []string{"phi4"}, vllm: []string{"phi4_mini_json"}, sglang: []string{"qwen25"}},
	{archs: []string{"internlm"}, vllm: []string{"internlm"}, sglang: []string{"qwen25"}},
	{archs: []string{"jamba"}, vllm: []string{"jamba"}, sglang: []string{"qwen25"}},
	{archs: []string{"granite"}, vllm: []string{"granite", "hermes"}, sglang: []string{"qwen25"}},
	{archs: []string{"hunyuan"}, vllm: []string{"hunyuan_a13b"}, sglang: []string{"qwen25"}},
	{archs: []string{"minimax"}, vllm: []string{"minimax"}, sglang: []string{"minimax-m2", "qwen25"}},
	{archs: []string{"step3"}, vllm: []string{"step3"}, sglang: []string{"step3"}},
	{archs: []string{"xlam"}, repos: []string{"xlam"}, vllm: []string{"xlam"}, sglang: []string{"qwen25"}},
	{archs: []string{"seedoss", "seed_oss"}, repos: []string{"seed-oss"}, vllm: []string{"seed_oss"}, sglang: []string{"qwen25"}},
}

// The parsers used when no rule matches, the hermes shape most models write
var defaultParsers = parserRule{vllm: []string{"hermes"}, sglang: []string{"qwen25"}}

// The rule for a launch, by its descriptor's architecture and its model's repo
func parserRuleFor(in Launch) parserRule {
	arch := strings.ToLower(in.Descriptor.GetArchitecture())
	repo := strings.ToLower(in.Model.GetRepo())
	for _, r := range parserRules {
		matched := false
		for _, a := range r.archs {
			matched = matched || arch != "" && strings.Contains(arch, a)
		}
		for _, p := range r.repos {
			matched = matched || repo != "" && strings.Contains(repo, p)
		}
		if matched && (len(r.repos) == 0 || len(r.archs) == 0 || anyIn(repo, r.repos) || anyIn(arch, r.archs)) {
			if len(r.archs) > 0 && len(r.repos) > 0 && !(anyIn(arch, r.archs) && anyIn(repo, r.repos)) {
				continue
			}
			return r
		}
	}
	return defaultParsers
}

func anyIn(s string, subs []string) bool {
	for _, sub := range subs {
		if s != "" && strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// The first preferred parser the install lists, the first preferred one when it lists none
func toolParserFor(in Launch, prefer []string) string {
	listed := strings.Split(in.InstallRecord.GetFacts()[factToolParsers], ",")
	known := map[string]bool{}
	for _, p := range listed {
		if p = strings.TrimSpace(p); p != "" {
			known[p] = true
		}
	}
	for _, p := range prefer {
		if len(known) == 0 || known[p] {
			return p
		}
	}
	return prefer[0]
}

// The parser vLLM takes for a launch
func vllmToolParser(in Launch) string { return toolParserFor(in, parserRuleFor(in).vllm) }

// The parser SGLang takes for a launch
func sglangToolParser(in Launch) string { return toolParserFor(in, parserRuleFor(in).sglang) }

// A probe recording the choices a flag's help lists in braces, comma separated
func choicesProbe(key string, args []string, flag string) Probe {
	return Probe{Key: key, Args: args, Parse: func(out string) (string, bool) {
		i := strings.Index(out, flag)
		if i < 0 {
			return "", false
		}
		rest := out[i+len(flag):]
		open := strings.Index(rest, "{")
		if open < 0 {
			return "", false
		}
		closing := strings.Index(rest[open:], "}")
		if closing < 0 {
			return "", false
		}
		var names []string
		for _, n := range strings.Split(rest[open+1:open+closing], ",") {
			if n = strings.TrimSpace(n); n != "" {
				names = append(names, n)
			}
		}
		return strings.Join(names, ","), len(names) > 0
	}}
}
