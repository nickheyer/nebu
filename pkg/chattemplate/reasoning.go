package chattemplate

import (
	"slices"
	"strings"

	"github.com/nickheyer/nebu/internal/gateway/jinja"
	"github.com/nickheyer/nebu/pkg/chattemplate/source"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/transcode/vllm"
)

// Reasoning effort levels a template is rendered with, lowest first, the levels llama.cpp probes
var EffortLevels = []string{"minimal", "low", "medium", "high", "xhigh", "max"}

// A level no template names, rendering as the template renders any level it does not know
const unknownEffort = "REASONING_EFFORT_UNKNOWN"

// Variables a request sets false in chat_template_kwargs to turn thinking off, in the order tried
var thinkingSwitches = []string{"enable_thinking", "thinking"}

// Delimiters thinking models wrap their reasoning in, found in a render only when the template writes them
var thinkingMarkers = []string{"<think>", "</think>", "<seed:think>", "[THINK]", "\u25c1think\u25b7"}

// The tool a tool use variant is probed with, a variant unable to render it serving no request with tools
var probeTools = []vllm.Tool{{Type: "function", Function: vllm.Function{Name: "probe", Description: "Probe tool", Parameters: []byte(`{"type":"object","properties":{"query":{"type":"string","description":"Probe query"}},"required":["query"]}`)}}}

// The texts the probe conversation carries, the trace marked so its removal from history shows
const (
	probeUser   = "User message"
	probeTrace  = "PROBE_REASONING_TRACE"
	probeAnswer = "Assistant message"
)

// How one template variant renders the probe's reasoning settings
type variantReasoning struct {
	efforts  []string
	reads    bool
	switches []string
	thinks   bool
}

// Records the effort levels the template honors and how its thinking turns off, rendering it once per setting
func Probe(t *Template, out *v1.ChatTemplate) {
	tpl, err := jinja.Compile(t)
	if err != nil {
		out.ReasoningError = err.Error()
		return
	}
	vr, err := probeVariant(t.Text, tpl, nil)
	if err != nil {
		out.ReasoningError = err.Error()
		return
	}
	found := []variantReasoning{vr}
	if t.ToolUse != "" {
		if vr, err := probeVariant(t.ToolUse, tpl, probeTools); err == nil {
			found = append(found, vr)
		}
	}
	var reading, thinking []variantReasoning
	for _, vr := range found {
		if vr.reads {
			reading = append(reading, vr)
		}
		if vr.thinks {
			thinking = append(thinking, vr)
		}
	}
	out.ReadsEffort = len(reading) > 0
	if out.ReadsEffort {
		out.Efforts = reading[0].efforts
		for _, vr := range reading[1:] {
			out.Efforts = slices.DeleteFunc(out.Efforts, func(l string) bool { return !slices.Contains(vr.efforts, l) })
		}
	}
	if len(thinking) == 0 {
		out.Thinking = v1.Thinking_THINKING_NONE
		return
	}
	switches := thinking[0].switches
	for _, vr := range thinking[1:] {
		switches = slices.DeleteFunc(switches, func(s string) bool { return !slices.Contains(vr.switches, s) })
	}
	out.Thinking = v1.Thinking_THINKING_ALWAYS
	if len(switches) > 0 {
		out.Thinking, out.ThinkingSwitch = v1.Thinking_THINKING_SWITCHED, switches[0]
	}
}

// Renders one variant with each effort level and switch value as llama.cpp hands them over, tools picking the variant
func probeVariant(text string, tpl *jinja.Template, tools []vllm.Tool) (variantReasoning, error) {
	var vr variantReasoning
	render := func(messages []vllm.Message, vars map[string]any) (string, error) {
		withVars := map[string]any{"enable_thinking": true}
		for k, v := range vars {
			withVars[k] = v
		}
		return tpl.With(withVars).Render(messages, tools, true)
	}
	user := []vllm.Message{message("user", probeUser)}
	effort := func(level string) (string, error) {
		return render(user, map[string]any{"reasoning_effort": level, "reasoning_strength": level})
	}
	if _, err := render(user, nil); err != nil {
		return vr, err
	}
	unknown, unknownErr := effort(unknownEffort)
	vr.reads = unknownErr != nil
	var alike []string
	for _, level := range EffortLevels {
		got, err := effort(level)
		switch {
		case err != nil:
			vr.reads = true
		case unknownErr != nil || got != unknown:
			vr.reads = true
			vr.efforts = append(vr.efforts, level)
		default:
			alike = append(alike, level)
		}
	}
	if vr.reads {
		for _, level := range alike {
			if source.Quotes(text, level) {
				vr.efforts = append(vr.efforts, level)
			}
		}
		slices.SortFunc(vr.efforts, func(a, b string) int { return slices.Index(EffortLevels, a) - slices.Index(EffortLevels, b) })
	}
	for _, name := range thinkingSwitches {
		on, onErr := render(user, map[string]any{name: true})
		off, offErr := render(user, map[string]any{name: false})
		if onErr == nil && offErr == nil && on != off {
			vr.switches = append(vr.switches, name)
		}
	}
	traced, tracedErr := render([]vllm.Message{user[0], message("assistant", "<think>\n"+probeTrace+"\n</think>\n\n"+probeAnswer), user[0]}, nil)
	stripped := tracedErr == nil && strings.Contains(traced, probeAnswer) && !strings.Contains(traced, probeTrace)
	plain, plainErr := render([]vllm.Message{user[0], message("assistant", probeAnswer), user[0]}, nil)
	marked := plainErr == nil && slices.ContainsFunc(thinkingMarkers, func(m string) bool { return strings.Contains(plain, m) })
	vr.thinks = vr.reads || len(vr.switches) > 0 || stripped || marked
	return vr, nil
}

// A message of one text part
func message(role, s string) vllm.Message {
	return vllm.Message{Role: role, Content: []vllm.ContentPart{{Type: "text", Text: s}}}
}
