package chat

import (
	"encoding/json"
	"slices"
	"strconv"
	"strings"

	"github.com/nickheyer/nebu/pkg/chattemplate"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// Thinking budgets at or above which an anthropic request asks for low, medium and high effort
const (
	effortLowBudget    = 1024
	effortMediumBudget = 2048
	effortHighBudget   = 4096
)

// What a request asks of the model's reasoning
type reasoningAsk struct {
	// The effort level asked, empty when none is
	effort string
	// Thinking asked off, or asked on by an explicit enable
	off, on bool
}

// What an anthropic thinking setting and output effort ask, an effort only when named or budgeted
func antAsk(thinking, output json.RawMessage) reasoningAsk {
	var th struct {
		Type   string  `json:"type"`
		Budget float64 `json:"budget_tokens"`
	}
	var out struct {
		Effort string `json:"effort"`
	}
	json.Unmarshal(thinking, &th)
	json.Unmarshal(output, &out)
	if th.Type == "disabled" {
		return reasoningAsk{off: true}
	}
	ask := reasoningAsk{effort: out.Effort, on: th.Type == "enabled"}
	if ask.on && ask.effort == "" && th.Budget > 0 {
		ask.effort = budgetEffort(th.Budget)
	}
	return ask
}

// The effort level a thinking budget asks for
func budgetEffort(budget float64) string {
	switch {
	case budget >= effortHighBudget:
		return "high"
	case budget >= effortMediumBudget:
		return "medium"
	case budget >= effortLowBudget:
		return "low"
	}
	return "minimal"
}

// Chat completions fields an anthropic reasoning ask becomes for the serving template, as asked when none was read
func antReasoning(req *Request, tpl *v1.ChatTemplate) (map[string]json.RawMessage, error) {
	ask := antAsk(req.Extra["thinking"], req.Extra["output_config"])
	mapped := map[string]json.RawMessage{}
	if tpl == nil {
		if ask.off {
			ask.effort = "none"
		}
		if ask.effort != "" {
			mapped["reasoning_effort"] = appendStr(nil, ask.effort)
		}
		return mapped, nil
	}
	if !ask.off && !ask.on && ask.effort == "" {
		return mapped, nil
	}
	if !rendered(tpl) {
		return nil, unrendered(req.Model, tpl)
	}
	switch {
	case ask.off && tpl.GetThinking() == v1.Thinking_THINKING_ALWAYS:
		return nil, bad("thinking is disabled, and model %s thinks on every request, its chat template reads no variable that turns thinking off", req.Model)
	case ask.off && tpl.GetThinking() == v1.Thinking_THINKING_SWITCHED:
		return mapped, setSwitch(req.Extra, mapped, tpl.GetThinkingSwitch(), false)
	case ask.off:
		return mapped, nil
	case ask.on && tpl.GetThinking() == v1.Thinking_THINKING_SWITCHED:
		if err := setSwitch(req.Extra, mapped, tpl.GetThinkingSwitch(), true); err != nil {
			return nil, err
		}
	}
	if ask.effort == "" {
		return mapped, nil
	}
	level, err := nearestEffort(req.Model, ask.effort, tpl)
	if err != nil {
		return nil, err
	}
	if level != "" {
		mapped["reasoning_effort"] = appendStr(nil, level)
	}
	return mapped, nil
}

// The taken level nearest an asked one, the next higher first, else the highest below, empty when none
func nearestEffort(model, asked string, tpl *v1.ChatTemplate) (string, error) {
	taken := tpl.GetEfforts()
	switch {
	case !tpl.GetReadsEffort():
		return "", nil
	case slices.Contains(taken, asked):
		return asked, nil
	}
	levels := chattemplate.EffortLevels
	at := slices.Index(levels, asked)
	if at < 0 {
		return "", bad("effort %s is not a reasoning effort level, the levels are %s, and model %s takes %s", asked, strings.Join(levels, ", "), model, levelList(taken))
	}
	for _, l := range levels[at+1:] {
		if slices.Contains(taken, l) {
			return l, nil
		}
	}
	for i := at - 1; i >= 0; i-- {
		if slices.Contains(taken, levels[i]) {
			return levels[i], nil
		}
	}
	return "", nil
}

// Refuses a reasoning_effort the template behind the endpoint does not take, turning none into the template's thinking switch
func FitEffort(req *Request, tpl *v1.ChatTemplate) (bool, error) {
	raw, ok := req.Extra["reasoning_effort"]
	var effort string
	if !ok || tpl == nil || req.Kind != Chat && req.Kind != Count || json.Unmarshal(raw, &effort) != nil || effort == "" {
		return false, nil
	}
	if !rendered(tpl) {
		return false, unrendered(req.Model, tpl)
	}
	if effort == "none" {
		switch tpl.GetThinking() {
		case v1.Thinking_THINKING_ALWAYS:
			return false, bad("reasoning_effort none asks thinking off, and model %s thinks on every request, its chat template reads no variable that turns thinking off", req.Model)
		case v1.Thinking_THINKING_SWITCHED:
			mapped := map[string]json.RawMessage{}
			if err := setSwitch(req.Extra, mapped, tpl.GetThinkingSwitch(), false); err != nil {
				return false, err
			}
			delete(req.Extra, "reasoning_effort")
			req.Extra = mergeExtra(req.Extra, mapped)
			return true, nil
		}
		return false, nil
	}
	if !tpl.GetReadsEffort() || slices.Contains(tpl.GetEfforts(), effort) {
		return false, nil
	}
	return false, bad("reasoning_effort %s is not a level model %s takes, it takes %s", effort, req.Model, levelList(tpl.GetEfforts()))
}

// Whether a render of the template found how it takes reasoning settings
func rendered(tpl *v1.ChatTemplate) bool {
	return tpl.GetReasoningError() == "" && tpl.GetThinking() != v1.Thinking_THINKING_UNSPECIFIED
}

// The refusal of a reasoning setting the gateway could not check because the template did not render
func unrendered(model string, tpl *v1.ChatTemplate) error {
	why := tpl.GetReasoningError()
	if why == "" {
		why = "the endpoint carries no render of it"
	}
	return bad("model %s has a chat template the gateway could not render to check reasoning settings against, %s", model, why)
}

// The levels a template takes as a list, or that it takes none of them
func levelList(taken []string) string {
	if len(taken) == 0 {
		return "none of " + strings.Join(chattemplate.EffortLevels, ", ")
	}
	return strings.Join(taken, ", ")
}

// Writes chat_template_kwargs with the switch variable set, keeping the variables the client set
func setSwitch(extra, mapped map[string]json.RawMessage, name string, value bool) error {
	kwargs := map[string]json.RawMessage{}
	if raw, ok := extra["chat_template_kwargs"]; ok && string(raw) != "null" {
		if err := json.Unmarshal(raw, &kwargs); err != nil {
			return bad("chat_template_kwargs must be an object to carry %s", name)
		}
	}
	kwargs[name] = json.RawMessage(strconv.FormatBool(value))
	b, err := json.Marshal(kwargs)
	if err != nil {
		return bad("chat_template_kwargs: %v", err)
	}
	mapped["chat_template_kwargs"] = b
	return nil
}
