package chattemplate

import (
	"strings"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// Endpoint labels carrying how the serving instance's template takes reasoning settings, wherever the endpoint is published or imported
const (
	// How the template thinks, none, switched or always
	LabelThinking = "thinking"
	// The variable a request sets false in chat_template_kwargs to turn thinking off
	LabelThinkingSwitch = "thinking-switch"
	// The effort levels the template honors joined by commas, present when it reads an effort
	LabelEfforts = "reasoning-efforts"
	// Why the template could not be rendered to learn its reasoning settings
	LabelReasoningError = "reasoning-error"
)

// Label values of the ways a template thinks
var thinkingLabels = map[v1.Thinking]string{
	v1.Thinking_THINKING_NONE:     "none",
	v1.Thinking_THINKING_SWITCHED: "switched",
	v1.Thinking_THINKING_ALWAYS:   "always",
}

// Whether the template record holds what a render of the template found about reasoning
func Probed(tp *v1.ChatTemplate) bool {
	return tp.GetReasoningError() != "" || tp.GetThinking() != v1.Thinking_THINKING_UNSPECIFIED
}

// Writes the reasoning labels of an endpoint serving the template, none when no template was read
func SetLabels(labels map[string]string, tp *v1.ChatTemplate) {
	switch {
	case tp.GetError() != "" || !Probed(tp):
		return
	case tp.GetReasoningError() != "":
		labels[LabelReasoningError] = tp.GetReasoningError()
		return
	}
	labels[LabelThinking] = thinkingLabels[tp.GetThinking()]
	if tp.GetThinking() == v1.Thinking_THINKING_SWITCHED {
		labels[LabelThinkingSwitch] = tp.GetThinkingSwitch()
	}
	if tp.GetReadsEffort() {
		labels[LabelEfforts] = strings.Join(tp.GetEfforts(), ",")
	}
}

// The reasoning settings an endpoint's labels carry, nil when the endpoint names none
func FromLabels(labels map[string]string) *v1.ChatTemplate {
	if e, ok := labels[LabelReasoningError]; ok {
		return &v1.ChatTemplate{ReasoningError: e}
	}
	name, ok := labels[LabelThinking]
	if !ok {
		return nil
	}
	tp := &v1.ChatTemplate{ThinkingSwitch: labels[LabelThinkingSwitch]}
	for t, n := range thinkingLabels {
		if n == name {
			tp.Thinking = t
		}
	}
	if tp.Thinking == v1.Thinking_THINKING_UNSPECIFIED {
		tp.ReasoningError = "the endpoint names thinking " + name + ", which this gateway does not know"
	}
	if efforts, ok := labels[LabelEfforts]; ok {
		tp.ReadsEffort = true
		if efforts != "" {
			tp.Efforts = strings.Split(efforts, ",")
		}
	}
	return tp
}
