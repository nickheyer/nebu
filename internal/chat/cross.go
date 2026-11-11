package chat

import (
	"encoding/json"
	"strings"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// Request fields only the anthropic protocol defines, read here and kept from a runtime speaking another protocol
var antOnly = []string{"thinking", "output_config", "output_format", "metadata", "context_management", "safeguards"}

// Thinking budgets at or above which a chat completions runtime is asked for low, medium and high effort
const (
	effortLowBudget    = 1024
	effortMediumBudget = 2048
	effortHighBudget   = 4096
)

// Rewrites what only the client's protocol defines into what a runtime speaking another protocol reads
func Cross(req *Request, from v1.ApiFlavor) {
	if from != v1.ApiFlavor_API_FLAVOR_ANTHROPIC {
		return
	}
	req.Messages = leadSystem(req.Messages)
	for i := range req.Messages {
		delete(req.Messages[i].Extra, "cache_control")
		for j := range req.Messages[i].Parts {
			delete(req.Messages[i].Parts[j].Extra, "cache_control")
		}
	}
	for i := range req.Tools {
		delete(req.Tools[i].Extra, "cache_control")
	}
	mapped := map[string]json.RawMessage{}
	if effort := antEffort(req.Extra["thinking"], req.Extra["output_config"]); effort != "" {
		mapped["reasoning_effort"] = appendStr(nil, effort)
	}
	if format := antFormat(req.Extra["output_format"], req.Extra["output_config"]); format != nil {
		mapped["response_format"] = format
	}
	var metadata struct {
		UserID string `json:"user_id"`
	}
	json.Unmarshal(req.Extra["metadata"], &metadata)
	if metadata.UserID != "" {
		mapped["user"] = appendStr(nil, metadata.UserID)
	}
	for _, k := range antOnly {
		delete(req.Extra, k)
	}
	req.Extra = mergeExtra(req.Extra, mapped)
}

// The line Claude Code leads its system prompt with, its cch field stamped anew on every request
const billingHeader = "x-anthropic-billing-header"

// Drops Claude Code's billing header from the system turns, whose changing stamp ends a prompt cache at its first tokens
func StripBillingHeader(req *Request) {
	kept := req.Messages[:0]
	for _, m := range req.Messages {
		if m.Role != "system" {
			kept = append(kept, m)
			continue
		}
		parts := make([]Part, 0, len(m.Parts))
		for _, p := range m.Parts {
			if p.Type == "text" && strings.HasPrefix(p.Text, billingHeader) {
				if p.Text = afterBillingHeader(p.Text); p.Text == "" {
					continue
				}
			}
			parts = append(parts, p)
		}
		if len(parts) == 0 && len(m.Parts) > 0 {
			continue
		}
		m.Parts = parts
		kept = append(kept, m)
	}
	req.Messages = kept
}

// The text past a billing header, which ends at its cch field or else with its line
func afterBillingHeader(text string) string {
	line, rest, _ := strings.Cut(text, "\n")
	if _, tail, ok := strings.Cut(line, "cch="); ok {
		if _, after, ok := strings.Cut(tail, ";"); ok && strings.TrimSpace(after) != "" {
			return strings.TrimSuffix(strings.TrimSpace(after)+"\n"+rest, "\n")
		}
	}
	return rest
}

// Keeps only the first turn as system, later ones becoming user turns placed after the tool results they precede
func leadSystem(msgs []Message) []Message {
	out := make([]Message, 0, len(msgs))
	var held []Message
	for i, m := range msgs {
		if i > 0 && m.Role == "system" {
			if !hasContent(m.Parts) {
				continue
			}
			m.Role = "user"
			if toolNext(msgs, i) {
				held = append(held, m)
			} else {
				out = append(out, m)
			}
			continue
		}
		out = append(out, m)
		if m.Role == "tool" && len(held) > 0 && !toolNext(msgs, i) {
			out = append(out, held...)
			held = nil
		}
	}
	return out
}

// Whether the next turn that is not a system turn is a tool result
func toolNext(msgs []Message, i int) bool {
	for _, m := range msgs[i+1:] {
		if m.Role != "system" {
			return m.Role == "tool"
		}
	}
	return false
}

// Whether any part is an image or text that is not blank
func hasContent(parts []Part) bool {
	for _, p := range parts {
		if p.Type == "image" || p.Type == "text" && strings.TrimSpace(p.Text) != "" {
			return true
		}
	}
	return false
}

// The reasoning effort a thinking setting and an output effort ask for, empty when the request sets neither
func antEffort(thinking, output json.RawMessage) string {
	var th struct {
		Type   string  `json:"type"`
		Budget float64 `json:"budget_tokens"`
	}
	var out struct {
		Effort string `json:"effort"`
	}
	json.Unmarshal(thinking, &th)
	json.Unmarshal(output, &out)
	switch {
	case th.Type == "disabled":
		return "none"
	case out.Effort != "":
		return out.Effort
	case th.Type == "adaptive":
		return "high"
	case th.Type != "enabled":
		return ""
	case th.Budget >= effortHighBudget:
		return "high"
	case th.Budget >= effortMediumBudget:
		return "medium"
	case th.Budget >= effortLowBudget:
		return "low"
	}
	return "minimal"
}

// The chat completions response_format of an output format naming a JSON schema, the older top level field first
func antFormat(legacy, output json.RawMessage) json.RawMessage {
	var out struct {
		Format json.RawMessage `json:"format"`
	}
	json.Unmarshal(output, &out)
	for _, raw := range []json.RawMessage{legacy, out.Format} {
		var f struct {
			Type   string          `json:"type"`
			Schema json.RawMessage `json:"schema"`
		}
		if json.Unmarshal(raw, &f) != nil || f.Type != "json_schema" || len(f.Schema) == 0 {
			continue
		}
		b := []byte(`{"type":"json_schema","json_schema":`)
		o := begin(&b)
		o.str("name", "structured_output")
		o.raw("schema", f.Schema)
		o.close(nil)
		return append(b, '}')
	}
	return nil
}
