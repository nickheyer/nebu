package chat

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/nickheyer/nebu/pkg/chattemplate"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// The reasoning settings a template text renders, probed as an instance records them
func probedTemplate(t *testing.T, text string) *v1.ChatTemplate {
	t.Helper()
	out := &v1.ChatTemplate{}
	chattemplate.Probe(&chattemplate.Template{Text: text, Source: "test"}, out)
	if out.GetReasoningError() != "" {
		t.Fatalf("probe: %s", out.GetReasoningError())
	}
	return out
}

// The production template, taking xhigh, medium and low with xhigh by default and raising on any other level
func xhighTemplate(t *testing.T) *v1.ChatTemplate {
	t.Helper()
	raw, err := os.ReadFile("../../pkg/chattemplate/testdata/effort_xhigh_medium_low.jinja")
	if err != nil {
		t.Fatal(err)
	}
	return probedTemplate(t, string(raw))
}

// A template reading no reasoning variable
const chatmlTemplate = `{%- for message in messages %}{{ '<|im_start|>' + message.role + '\n' + message.content + '<|im_end|>\n' }}{%- endfor %}{%- if add_generation_prompt %}{{ '<|im_start|>assistant\n' }}{%- endif %}`

// A template that always thinks and takes low and high, reading no variable that turns thinking off
const alwaysTemplate = `{%- set e = reasoning_effort | default('high') %}{%- if e not in ('low', 'high') %}{{ raise_exception('effort ' ~ e) }}{%- endif %}{%- for message in messages %}{{ message.content }}{%- endfor %}{{ '<think>' + e }}`

// The reasoning_effort and chat_template_kwargs an anthropic request reaches the runtime with
func crossedReasoning(t *testing.T, settings string, tpl *v1.ChatTemplate) (string, string, error) {
	t.Helper()
	sent, _, _, err := crossedFor(t, `{"model":"m","max_tokens":64,"messages":[{"role":"user","content":"hi"}]`+settings+`}`, tpl)
	return string(sent["reasoning_effort"]), string(sent["chat_template_kwargs"]), err
}

// A taken effort passes, another becomes the next taken level up or else the highest below, high becoming xhigh
func TestCrossNearestEffort(t *testing.T) {
	tpl := xhighTemplate(t)
	for settings, want := range map[string]string{
		`,"thinking":{"type":"adaptive"},"output_config":{"effort":"high"}`:   `"xhigh"`,
		`,"output_config":{"effort":"high"}`:                                  `"xhigh"`,
		`,"thinking":{"type":"adaptive"},"output_config":{"effort":"medium"}`: `"medium"`,
		`,"output_config":{"effort":"low"}`:                                   `"low"`,
		`,"output_config":{"effort":"xhigh"}`:                                 `"xhigh"`,
		`,"output_config":{"effort":"max"}`:                                   `"xhigh"`,
		`,"output_config":{"effort":"minimal"}`:                               `"low"`,
	} {
		effort, _, err := crossedReasoning(t, settings, tpl)
		if err != nil || effort != want {
			t.Errorf("%s gave effort %s err %v, want %s", settings, effort, err, want)
		}
	}
}

// Adaptive thinking without a named effort sends none, a budget maps to a level only when the client set one
func TestCrossInventsNoEffort(t *testing.T) {
	tpl := xhighTemplate(t)
	for settings, want := range map[string][2]string{
		`,"thinking":{"type":"adaptive"}`:                        {``, ``},
		`,"thinking":{"type":"adaptive","display":"summarized"}`: {``, ``},
		``:                               {``, ``},
		`,"thinking":{"type":"enabled"}`: {``, `{"enable_thinking":true}`},
		`,"thinking":{"type":"enabled","budget_tokens":4096}`:                                     {`"xhigh"`, `{"enable_thinking":true}`},
		`,"thinking":{"type":"enabled","budget_tokens":2048}`:                                     {`"medium"`, `{"enable_thinking":true}`},
		`,"thinking":{"type":"enabled","budget_tokens":1024}`:                                     {`"low"`, `{"enable_thinking":true}`},
		`,"thinking":{"type":"enabled","budget_tokens":512}`:                                      {`"low"`, `{"enable_thinking":true}`},
		`,"thinking":{"type":"enabled","budget_tokens":9000},"output_config":{"effort":"medium"}`: {`"medium"`, `{"enable_thinking":true}`},
	} {
		effort, kwargs, err := crossedReasoning(t, settings, tpl)
		if err != nil || effort != want[0] || kwargs != want[1] {
			t.Errorf("%q gave effort %q kwargs %q err %v, want %q %q", settings, effort, kwargs, err, want[0], want[1])
		}
	}
}

// Disabled thinking turns the template's switch off with no effort, keeping the client's own template variables
func TestCrossDisabledThinking(t *testing.T) {
	tpl := xhighTemplate(t)
	effort, kwargs, err := crossedReasoning(t, `,"thinking":{"type":"disabled"},"output_config":{"effort":"high"}`, tpl)
	if err != nil || effort != "" || kwargs != `{"enable_thinking":false}` {
		t.Fatalf("effort %q kwargs %q err %v", effort, kwargs, err)
	}
	_, kwargs, err = crossedReasoning(t, `,"thinking":{"type":"disabled"},"chat_template_kwargs":{"preserve_thinking":true}`, tpl)
	if err != nil || kwargs != `{"enable_thinking":false,"preserve_thinking":true}` {
		t.Fatalf("kwargs %q err %v", kwargs, err)
	}
	_, _, err = crossedReasoning(t, `,"thinking":{"type":"disabled"},"chat_template_kwargs":"x"`, tpl)
	if f := failure(err); f.Status != http.StatusBadRequest {
		t.Fatalf("kwargs that are not an object: %v", err)
	}
	switched := &v1.ChatTemplate{Thinking: v1.Thinking_THINKING_SWITCHED, ThinkingSwitch: "thinking"}
	if _, kwargs, err := crossedReasoning(t, `,"thinking":{"type":"disabled"}`, switched); err != nil || kwargs != `{"thinking":false}` {
		t.Fatalf("a template switched by thinking: kwargs %q err %v", kwargs, err)
	}
}

// A template with no way to turn thinking off refuses disabled thinking, one that never thinks takes it as is
func TestCrossDisabledWithoutSwitch(t *testing.T) {
	_, _, err := crossedReasoning(t, `,"thinking":{"type":"disabled"}`, probedTemplate(t, alwaysTemplate))
	if f := failure(err); f.Status != http.StatusBadRequest || !strings.Contains(f.Message, "no variable that turns thinking off") {
		t.Fatalf("always thinking: %v", err)
	}
	effort, kwargs, err := crossedReasoning(t, `,"thinking":{"type":"disabled"}`, probedTemplate(t, chatmlTemplate))
	if err != nil || effort != "" || kwargs != "" {
		t.Fatalf("never thinking: effort %q kwargs %q err %v", effort, kwargs, err)
	}
}

// A template reading no effort gets none, an unknown level and an unrendered template are refused
func TestCrossEffortRefusals(t *testing.T) {
	effort, kwargs, err := crossedReasoning(t, `,"thinking":{"type":"enabled","budget_tokens":4096},"output_config":{"effort":"high"}`, probedTemplate(t, chatmlTemplate))
	if err != nil || effort != "" || kwargs != "" {
		t.Fatalf("no effort read: effort %q kwargs %q err %v", effort, kwargs, err)
	}
	_, _, err = crossedReasoning(t, `,"output_config":{"effort":"turbo"}`, xhighTemplate(t))
	if f := failure(err); f.Status != http.StatusBadRequest || !strings.Contains(f.Message, "low, medium, xhigh") {
		t.Fatalf("unknown level: %v", err)
	}
	unrendered := &v1.ChatTemplate{ReasoningError: "'NoneType' object is not iterable"}
	_, _, err = crossedReasoning(t, `,"output_config":{"effort":"high"}`, unrendered)
	if f := failure(err); f.Status != http.StatusBadRequest || !strings.Contains(f.Message, "NoneType") {
		t.Fatalf("unrendered: %v", err)
	}
	if effort, _, err := crossedReasoning(t, `,"thinking":{"type":"adaptive"}`, unrendered); err != nil || effort != "" {
		t.Fatalf("adaptive needs no check: effort %q err %v", effort, err)
	}
}

// The failure an error carries, zero when it is none
func failure(err error) Failure {
	var f *Failure
	if errors.As(err, &f) {
		return *f
	}
	return Failure{}
}

// A chat completions request's own effort the template rejects is refused naming the levels, none turns the switch off
func TestFitEffort(t *testing.T) {
	tpl := xhighTemplate(t)
	parse := func(body string) *Request {
		req, err := (openai{}).Parse(OpenAIChat, []byte(body))
		if err != nil {
			t.Fatal(err)
		}
		return req
	}
	req := parse(`{"model":"qwen","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"high"}`)
	changed, err := FitEffort(req, tpl)
	if f := failure(err); changed || f.Status != http.StatusBadRequest || !strings.Contains(f.Message, "reasoning_effort high is not a level model qwen takes, it takes low, medium, xhigh") {
		t.Fatalf("rejected level: changed %v err %v", changed, err)
	}
	for _, level := range []string{"low", "medium", "xhigh"} {
		req := parse(`{"model":"qwen","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"` + level + `"}`)
		if changed, err := FitEffort(req, tpl); changed || err != nil {
			t.Fatalf("%s: changed %v err %v", level, changed, err)
		}
	}
	req = parse(`{"model":"qwen","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"none","chat_template_kwargs":{"a":1}}`)
	changed, err = FitEffort(req, tpl)
	if !changed || err != nil || req.Extra["reasoning_effort"] != nil || string(req.Extra["chat_template_kwargs"]) != `{"a":1,"enable_thinking":false}` {
		t.Fatalf("none: changed %v err %v extra %s", changed, err, req.Extra["chat_template_kwargs"])
	}
	req = parse(`{"model":"qwen","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"none"}`)
	if _, err := FitEffort(req, probedTemplate(t, alwaysTemplate)); failure(err).Status != http.StatusBadRequest {
		t.Fatalf("none on a template that always thinks: %v", err)
	}
	for _, tp := range []*v1.ChatTemplate{probedTemplate(t, chatmlTemplate), nil} {
		req = parse(`{"model":"qwen","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"high"}`)
		if changed, err := FitEffort(req, tp); changed || err != nil || string(req.Extra["reasoning_effort"]) != `"high"` {
			t.Fatalf("a template reading no effort or none read: changed %v err %v", changed, err)
		}
	}
	req = parse(`{"model":"qwen","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"low"}`)
	if _, err := FitEffort(req, &v1.ChatTemplate{ReasoningError: "boom"}); failure(err).Status != http.StatusBadRequest {
		t.Fatalf("unrendered: %v", err)
	}
	var sent map[string]json.RawMessage
	req = parse(`{"model":"qwen","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"none"}`)
	FitEffort(req, tpl)
	_, out, err := (openai{}).Render(req)
	if err != nil || json.Unmarshal(out, &sent) != nil || sent["reasoning_effort"] != nil || string(sent["chat_template_kwargs"]) != `{"enable_thinking":false}` {
		t.Fatalf("rendered %s err %v", out, err)
	}
}
