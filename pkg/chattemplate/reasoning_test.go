package chattemplate

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// The template text of a fixture here or of the renderer's corpus
func fixture(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.FromSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// The renderer's corpus of real model templates
const corpus = "../../internal/gateway/jinja/testdata/templates/"

// A template taking xhigh, medium and low, xhigh by default, and raising on any other level
const effortTemplate = "testdata/effort_xhigh_medium_low.jinja"

// A chatml template that reads no reasoning variable at all
const plainTemplate = `{%- for message in messages %}{{ '<|im_start|>' + message.role + '\n' + message.content + '<|im_end|>\n' }}{%- endfor %}{%- if add_generation_prompt %}{{ '<|im_start|>assistant\n' }}{%- endif %}`

// A template that renders an unknown level as its medium default, naming medium and the levels it renders apart
const lenientTemplate = `{%- set e = reasoning_effort | default('medium') %}{%- for message in messages %}{{ message.content }}{%- endfor %}{%- if e == 'high' %}H{%- elif e == 'low' %}L{%- else %}M{%- endif %}`

// The probe of a template text
func probed(text string) *v1.ChatTemplate {
	out := &v1.ChatTemplate{}
	Probe(&Template{Text: text, Source: "test"}, out)
	return out
}

// A template raising on all but xhigh, medium and low takes those three, enable_thinking turning thinking off
func TestProbeRaisingTemplate(t *testing.T) {
	got := probed(fixture(t, effortTemplate))
	if got.GetReasoningError() != "" || !slices.Equal(got.GetEfforts(), []string{"low", "medium", "xhigh"}) || !got.GetReadsEffort() {
		t.Fatalf("efforts %v reads %v error %q", got.GetEfforts(), got.GetReadsEffort(), got.GetReasoningError())
	}
	if got.GetThinking() != v1.Thinking_THINKING_SWITCHED || got.GetThinkingSwitch() != "enable_thinking" {
		t.Fatalf("thinking %v switch %q", got.GetThinking(), got.GetThinkingSwitch())
	}
}

// A template never reading a reasoning effort takes no level and has no thinking to turn off
func TestProbeTemplateWithoutEffort(t *testing.T) {
	for name, text := range map[string]string{"chatml": plainTemplate, "llama31": fixture(t, corpus+"llama31.jinja"), "qwen25": fixture(t, corpus+"qwen25.jinja")} {
		got := probed(text)
		if got.GetReasoningError() != "" || got.GetReadsEffort() || len(got.GetEfforts()) != 0 || got.GetThinking() != v1.Thinking_THINKING_NONE {
			t.Errorf("%s: efforts %v reads %v thinking %v error %q", name, got.GetEfforts(), got.GetReadsEffort(), got.GetThinking(), got.GetReasoningError())
		}
	}
}

// Levels rendering as an unknown one count only when the template names them, so a silent default stays a level
func TestProbeLenientTemplate(t *testing.T) {
	got := probed(lenientTemplate)
	if !slices.Equal(got.GetEfforts(), []string{"low", "medium", "high"}) || !got.GetReadsEffort() || got.GetThinking() != v1.Thinking_THINKING_ALWAYS {
		t.Fatalf("efforts %v reads %v thinking %v", got.GetEfforts(), got.GetReadsEffort(), got.GetThinking())
	}
}

// Real templates show which variable turns thinking off, and which think with no way to stop
func TestProbeThinkingSwitches(t *testing.T) {
	for name, want := range map[string]struct {
		thinking v1.Thinking
		sw       string
		efforts  []string
	}{
		"qwen3":         {v1.Thinking_THINKING_SWITCHED, "enable_thinking", nil},
		"glm45":         {v1.Thinking_THINKING_SWITCHED, "enable_thinking", nil},
		"deepseek_v31":  {v1.Thinking_THINKING_SWITCHED, "thinking", nil},
		"granite33":     {v1.Thinking_THINKING_SWITCHED, "thinking", nil},
		"deepseek_r1":   {v1.Thinking_THINKING_ALWAYS, "", nil},
		"qwq":           {v1.Thinking_THINKING_ALWAYS, "", nil},
		"intellect3":    {v1.Thinking_THINKING_ALWAYS, "", nil},
		"phi4reasoning": {v1.Thinking_THINKING_ALWAYS, "", nil},
		"gptoss":        {v1.Thinking_THINKING_ALWAYS, "", []string{"minimal", "low", "medium", "high", "xhigh", "max"}},
		"mistralnemo":   {v1.Thinking_THINKING_NONE, "", nil},
		"gemma3":        {v1.Thinking_THINKING_NONE, "", nil},
	} {
		got := probed(fixture(t, corpus+name+".jinja"))
		if got.GetReasoningError() != "" || got.GetThinking() != want.thinking || got.GetThinkingSwitch() != want.sw || !slices.Equal(got.GetEfforts(), want.efforts) {
			t.Errorf("%s: thinking %v switch %q efforts %v error %q, want %v %q %v", name, got.GetThinking(), got.GetThinkingSwitch(), got.GetEfforts(), got.GetReasoningError(), want.thinking, want.sw, want.efforts)
		}
	}
}

// A template that cannot render a plain turn records why and claims nothing about reasoning
func TestProbeUnrenderable(t *testing.T) {
	for name, text := range map[string]string{"raises": `{{ raise_exception('always') }}`, "syntax": `{% if %}`} {
		got := probed(text)
		if got.GetReasoningError() == "" || got.GetThinking() != v1.Thinking_THINKING_UNSPECIFIED || got.GetReadsEffort() || len(got.GetEfforts()) != 0 {
			t.Errorf("%s: %v", name, got)
		}
	}
}

// A tool use variant that reads efforts narrows the levels to those both variants take
func TestProbeToolUseVariant(t *testing.T) {
	variant := `{%- if reasoning_effort is defined and reasoning_effort not in ('low', 'xhigh') %}{{ raise_exception('no') }}{%- endif %}{{ tools | length }}{%- for message in messages %}{{ message.content }}{%- endfor %}{{ reasoning_effort | default('xhigh') }}`
	out := &v1.ChatTemplate{}
	Probe(&Template{Text: fixture(t, effortTemplate), ToolUse: variant, Source: "gguf"}, out)
	if !slices.Equal(out.GetEfforts(), []string{"low", "xhigh"}) || out.GetThinking() != v1.Thinking_THINKING_ALWAYS {
		t.Fatalf("efforts %v thinking %v", out.GetEfforts(), out.GetThinking())
	}
}

// Inspecting a stored model records the probe beside the tool support
func TestInspectRecordsReasoning(t *testing.T) {
	dir := t.TempDir()
	jinja := file(t, dir, "chat_template.jinja", fixture(t, effortTemplate), v1.ArtifactRole_ARTIFACT_ROLE_TEMPLATE)
	tpl := Inspect(stored("safetensors", jinja))
	if tpl.GetError() != "" || !slices.Equal(tpl.GetEfforts(), []string{"low", "medium", "xhigh"}) || tpl.GetThinking() != v1.Thinking_THINKING_SWITCHED {
		t.Fatalf("%v", tpl)
	}
}

// Endpoint labels carry the probe whole, and an endpoint without them names no template
func TestReasoningLabels(t *testing.T) {
	for _, tp := range []*v1.ChatTemplate{
		{Efforts: []string{"low", "medium", "xhigh"}, ReadsEffort: true, Thinking: v1.Thinking_THINKING_SWITCHED, ThinkingSwitch: "enable_thinking"},
		{ReadsEffort: true, Thinking: v1.Thinking_THINKING_ALWAYS},
		{Thinking: v1.Thinking_THINKING_NONE},
		{ReasoningError: "'NoneType' object is not iterable"},
	} {
		labels := map[string]string{}
		SetLabels(labels, tp)
		got := FromLabels(labels)
		if !slices.Equal(got.GetEfforts(), tp.GetEfforts()) || got.GetReadsEffort() != tp.GetReadsEffort() || got.GetThinking() != tp.GetThinking() || got.GetThinkingSwitch() != tp.GetThinkingSwitch() || got.GetReasoningError() != tp.GetReasoningError() {
			t.Errorf("labels %v read back %v, want %v", labels, got, tp)
		}
	}
	for _, tp := range []*v1.ChatTemplate{nil, {Error: "no chat template among the model's files"}, {Source: "chat_template.jinja"}} {
		labels := map[string]string{}
		SetLabels(labels, tp)
		if len(labels) != 0 || FromLabels(labels) != nil {
			t.Errorf("%v gave labels %v", tp, labels)
		}
	}
	if got := FromLabels(map[string]string{LabelThinking: "sometimes"}); !strings.Contains(got.GetReasoningError(), "sometimes") {
		t.Fatalf("an unknown thinking label read as %v", got)
	}
}
