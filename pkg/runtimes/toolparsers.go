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
	{repos: []string{"kimi-k3", "kimi_k3"}, vllm: []string{"kimi_k3"}, sglang: []string{"kimi_k3"}},
	{repos: []string{"kimi-k2", "kimi_k2", "kimi"}, vllm: []string{"kimi_k2"}, sglang: []string{"kimi_k2"}},
	{repos: []string{"k2-horizon", "k2_horizon"}, vllm: []string{"k2_horizon"}, sglang: []string{"k2_horizon"}},
	{repos: []string{"qwen3-coder", "qwen3_coder", "qwen3coder"}, vllm: []string{"qwen3_coder", "qwen3_xml", "hermes"}, sglang: []string{"qwen3_coder", "qwen25"}},
	{archs: []string{"qwen"}, vllm: []string{"hermes"}, sglang: []string{"qwen25"}},
	{archs: []string{"llama4"}, vllm: []string{"llama4_pythonic", "llama4_json", "llama3_json"}, sglang: []string{"llama3"}},
	{archs: []string{"llama"}, vllm: []string{"llama3_json"}, sglang: []string{"llama3"}},
	{archs: []string{"mistral", "mixtral", "pixtral", "ministral"}, vllm: []string{"mistral"}, sglang: []string{"mistral"}},
	{archs: []string{"deepseekv4", "deepseek_v4"}, repos: []string{"v4.1", "v41", "v4_1"}, vllm: []string{"deepseek_v41", "deepseek_v4"}, sglang: []string{"deepseekv41", "deepseekv4"}},
	{archs: []string{"deepseekv4", "deepseek_v4"}, vllm: []string{"deepseek_v4"}, sglang: []string{"deepseekv4"}},
	{archs: []string{"deepseekv3", "deepseek_v3"}, repos: []string{"v3.2", "v32", "v3_2"}, vllm: []string{"deepseek_v32", "deepseek_v31"}, sglang: []string{"deepseekv32", "deepseekv31"}},
	{archs: []string{"deepseekv3", "deepseek_v3"}, repos: []string{"v3.1", "v31", "v3_1"}, vllm: []string{"deepseek_v31", "deepseek_v3"}, sglang: []string{"deepseekv31", "deepseekv3"}},
	{archs: []string{"deepseek"}, vllm: []string{"deepseek_v3"}, sglang: []string{"deepseekv3"}},
	{archs: []string{"glm4"}, repos: []string{"glm-4.7", "glm4.7", "glm_4.7", "glm-4-7"}, vllm: []string{"glm47", "glm45"}, sglang: []string{"glm47", "glm45"}},
	{archs: []string{"glm4", "glm5"}, vllm: []string{"glm45"}, sglang: []string{"glm45"}},
	{archs: []string{"gptoss", "gpt_oss"}, vllm: []string{"openai"}, sglang: []string{"gpt-oss"}},
	{archs: []string{"functiongemma"}, repos: []string{"functiongemma"}, vllm: []string{"functiongemma", "gemma4"}, sglang: []string{"gemma4"}},
	{archs: []string{"gemma4"}, vllm: []string{"gemma4"}, sglang: []string{"gemma4"}},
	{archs: []string{"phi4"}, vllm: []string{"phi4_mini_json"}, sglang: []string{"hermes", "qwen25"}},
	{archs: []string{"internlm", "interns1"}, vllm: []string{"internlm"}, sglang: []string{"interns1", "qwen25"}},
	{archs: []string{"jamba"}, vllm: []string{"jamba"}, sglang: []string{"hermes", "qwen25"}},
	{archs: []string{"granitemoehybrid", "granite4"}, repos: []string{"granite-4", "granite4"}, vllm: []string{"granite4", "granite", "hermes"}, sglang: []string{"hermes", "qwen25"}},
	{archs: []string{"granite"}, vllm: []string{"granite", "hermes"}, sglang: []string{"hermes", "qwen25"}},
	{archs: []string{"hunyuan"}, repos: []string{"a13b"}, vllm: []string{"hunyuan_a13b"}, sglang: []string{"hunyuan"}},
	{archs: []string{"hunyuan"}, vllm: []string{"hy_v4", "hy_v3", "hunyuan_a13b"}, sglang: []string{"hunyuan"}},
	{archs: []string{"minimax"}, repos: []string{"m3"}, vllm: []string{"minimax_m3", "minimax_m2"}, sglang: []string{"minimax-m3", "minimax-m2"}},
	{archs: []string{"minimax"}, vllm: []string{"minimax_m2"}, sglang: []string{"minimax-m2", "qwen25"}},
	{archs: []string{"step3"}, repos: []string{"3.5", "step3p5"}, vllm: []string{"step3p5", "step3"}, sglang: []string{"step3p5", "step3"}},
	{archs: []string{"step3"}, vllm: []string{"step3"}, sglang: []string{"step3"}},
	{archs: []string{"step5"}, vllm: []string{"step5"}, sglang: []string{"hermes", "qwen25"}},
	{archs: []string{"xlam"}, repos: []string{"xlam"}, vllm: []string{"xlam"}, sglang: []string{"hermes", "qwen25"}},
	{archs: []string{"seedoss", "seed_oss"}, repos: []string{"seed-oss"}, vllm: []string{"seed_oss"}, sglang: []string{"hermes", "qwen25"}},
	{archs: []string{"lfm2"}, vllm: []string{"lfm2"}, sglang: []string{"lfm2"}},
	{archs: []string{"mimo"}, vllm: []string{"mimo"}, sglang: []string{"mimo"}},
	{archs: []string{"minicpm"}, vllm: []string{"minicpm5", "hermes"}, sglang: []string{"minicpm5", "qwen25"}},
	{archs: []string{"dots"}, vllm: []string{"dots", "hermes"}, sglang: []string{"dots", "qwen25"}},
	{archs: []string{"ernie4"}, vllm: []string{"ernie45"}, sglang: []string{"hermes", "qwen25"}},
	{archs: []string{"olmo3"}, vllm: []string{"olmo3"}, sglang: []string{"hermes", "qwen25"}},
	{archs: []string{"apertus"}, vllm: []string{"apertus"}, sglang: []string{"apertus2509", "qwen25"}},
	{archs: []string{"longcat"}, vllm: []string{"longcat"}, sglang: []string{"hermes", "qwen25"}},
	{archs: []string{"ling"}, repos: []string{"ling-3", "ling3"}, vllm: []string{"ling3"}, sglang: []string{"ling3"}},
	{archs: []string{"gigachat"}, vllm: []string{"gigachat3"}, sglang: []string{"gigachat35", "gigachat3"}},
	{archs: []string{"inkling"}, vllm: []string{"inkling"}, sglang: []string{"inkling"}},
	{archs: []string{"poolside"}, vllm: []string{"poolside_v1"}, sglang: []string{"poolside_v1"}},
	{archs: []string{"beam"}, vllm: []string{"beam"}, sglang: []string{"beam"}},
	{archs: []string{"muse"}, vllm: []string{"muse", "muse_glimmer"}, sglang: []string{"muse"}},
	{archs: []string{"plamo"}, vllm: []string{"plamo3"}, sglang: []string{"hermes", "qwen25"}},
}

// The parsers used when no rule matches, hermes for the tool call shape most models write, and
// SGLang's own detection ahead of it on a build that lists auto
var defaultParsers = parserRule{vllm: []string{"hermes"}, sglang: []string{"auto", "qwen25"}}

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

// The first preferred parser the install lists, the first preferred one that is not a runtime's
// own auto detection when it lists none
func toolParserFor(in Launch, prefer []string) string {
	listed := strings.Split(in.InstallRecord.GetFacts()[factToolParsers], ",")
	known := map[string]bool{}
	for _, p := range listed {
		if p = strings.TrimSpace(p); p != "" {
			known[p] = true
		}
	}
	for _, p := range prefer {
		if len(known) == 0 && p != "auto" || known[p] {
			return p
		}
	}
	for _, p := range prefer {
		if p != "auto" {
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
