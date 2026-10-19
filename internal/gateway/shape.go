package gateway

import (
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/nickheyer/nebu/internal/chat"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// Separator for merged system messages
const systemJoin = "\n\n"

// Folds later system turns as the route says, merged into the first or turned into user turns
func foldSystem(c *chat.Request, mode v1.SystemMessages) {
	switch mode {
	case v1.SystemMessages_SYSTEM_MESSAGES_MERGE:
		var kept []chat.Message
		var late []string
		for i, m := range c.Messages {
			if i > 0 && m.Role == "system" {
				late = append(late, chat.TextOf(m.Parts))
				continue
			}
			kept = append(kept, m)
		}
		if len(late) == 0 {
			return
		}
		text := strings.Join(late, systemJoin)
		if len(kept) > 0 && kept[0].Role == "system" {
			kept[0].Parts = chat.AppendText(kept[0].Parts, text)
		} else {
			kept = append([]chat.Message{{Role: "system", Parts: []chat.Part{{Type: "text", Text: text}}}}, kept...)
		}
		c.Messages = kept
	case v1.SystemMessages_SYSTEM_MESSAGES_USER:
		for i := range c.Messages {
			if i > 0 && c.Messages[i].Role == "system" {
				c.Messages[i].Role = "user"
			}
		}
	}
}

// Reports whether a profile sets anything, an empty one inheriting everything
func ProfileSet(p *v1.Profile) bool {
	return p.GetSystemMessages() != v1.SystemMessages_SYSTEM_MESSAGES_UNSPECIFIED || p.GetAllowAll() || len(p.GetToolServers()) > 0 || p.GetMutatingTools() ||
		p.GetSystemPrompt() != v1.SystemPrompt_SYSTEM_PROMPT_UNSPECIFIED || p.GetOverrides() != "" || len(p.GetHiddenTools()) > 0 || p != nil && (p.AllowAll != nil || p.StripExtras != nil)
}

// The system prompt mode a profile resolves to, facts for a harness and keep for any other client
func systemPromptOf(p *v1.Profile, harness bool) v1.SystemPrompt {
	if mode := p.GetSystemPrompt(); mode != v1.SystemPrompt_SYSTEM_PROMPT_UNSPECIFIED {
		return mode
	}
	if harness {
		return v1.SystemPrompt_SYSTEM_PROMPT_FACTS
	}
	return v1.SystemPrompt_SYSTEM_PROMPT_KEEP
}

// Whether permission checks are answered as allowed, which a profile that says nothing does
func AllowsAll(p *v1.Profile) bool {
	if p == nil || p.AllowAll == nil {
		return true
	}
	return *p.AllowAll
}

// Whether a client's extras stay out, which a profile that says nothing does for an Anthropic client
func StripsExtras(p *v1.Profile, api v1.ApiFlavor) bool {
	if p == nil || p.StripExtras == nil {
		return api == v1.ApiFlavor_API_FLAVOR_ANTHROPIC
	}
	return *p.StripExtras
}

// Refuses a profile whose shaping cannot be applied as written
func ValidateProfile(p *v1.Profile) error {
	if p.GetSystemPrompt() == v1.SystemPrompt_SYSTEM_PROMPT_REPLACE && strings.TrimSpace(p.GetSystemText()) == "" {
		return errors.New("a profile that replaces the system prompt needs system text")
	}
	if p.GetSystemPrompt() != v1.SystemPrompt_SYSTEM_PROMPT_REPLACE && p.GetSystemText() != "" {
		return errors.New("system text is set, so the system prompt must be replace")
	}
	if o := p.GetOverrides(); o != "" {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal([]byte(o), &fields); err != nil {
			return fmt.Errorf("overrides must be a JSON object: %w", err)
		}
	}
	for _, pattern := range p.GetHiddenTools() {
		if strings.TrimSpace(pattern) == "" {
			return errors.New("a hidden tool pattern is empty")
		}
	}
	return nil
}

// Merges an alias profile over its slot's, every alias field that is set winning
func EffectiveProfile(alias, slot *v1.Profile) *v1.Profile {
	if !ProfileSet(alias) {
		return slot
	}
	if slot == nil {
		slot = &v1.Profile{}
	}
	out := &v1.Profile{
		SystemMessages: slot.GetSystemMessages(), AllowAll: slot.AllowAll,
		ToolServers: append([]string(nil), slot.GetToolServers()...), MutatingTools: slot.GetMutatingTools(),
		SystemPrompt: slot.GetSystemPrompt(), SystemText: slot.GetSystemText(), StripExtras: slot.StripExtras, Overrides: slot.GetOverrides(),
		HiddenTools: append([]string(nil), slot.GetHiddenTools()...),
	}
	if len(alias.GetHiddenTools()) > 0 {
		out.HiddenTools = append([]string(nil), alias.GetHiddenTools()...)
	}
	if alias.AllowAll != nil {
		out.AllowAll = alias.AllowAll
	}
	if alias.StripExtras != nil {
		out.StripExtras = alias.StripExtras
	}
	if alias.GetSystemMessages() != v1.SystemMessages_SYSTEM_MESSAGES_UNSPECIFIED {
		out.SystemMessages = alias.GetSystemMessages()
	}
	if len(alias.GetToolServers()) > 0 {
		out.ToolServers, out.MutatingTools = append([]string(nil), alias.GetToolServers()...), alias.GetMutatingTools()
	}
	if alias.GetSystemPrompt() != v1.SystemPrompt_SYSTEM_PROMPT_UNSPECIFIED {
		out.SystemPrompt, out.SystemText = alias.GetSystemPrompt(), alias.GetSystemText()
	}
	if alias.GetOverrides() != "" {
		out.Overrides = alias.GetOverrides()
	}
	return out
}

// Whether a profile changes what the runtime gets beyond what passing the body through would
func Shaped(p *v1.Profile, api v1.ApiFlavor, harness bool) bool {
	return systemPromptOf(p, harness) != v1.SystemPrompt_SYSTEM_PROMPT_KEEP || StripsExtras(p, api) || p.GetOverrides() != "" || len(p.GetHiddenTools()) > 0
}

// Applies the profile's system prompt, tool hiding, and field stripping before rendering
func shape(c *chat.Request, p *v1.Profile, api v1.ApiFlavor, harness bool) {
	switch systemPromptOf(p, harness) {
	case v1.SystemPrompt_SYSTEM_PROMPT_DROP:
		c.Messages = withoutSystem(c.Messages)
	case v1.SystemPrompt_SYSTEM_PROMPT_REPLACE:
		c.Messages = append([]chat.Message{{Role: "system", Parts: []chat.Part{{Type: "text", Text: p.GetSystemText()}}}}, withoutSystem(c.Messages)...)
	case v1.SystemPrompt_SYSTEM_PROMPT_FACTS:
		c.Messages = append([]chat.Message{{Role: "system", Parts: []chat.Part{{Type: "text", Text: factsPrompt(c.Messages)}}}}, withoutSystem(c.Messages)...)
	}
	if patterns := p.GetHiddenTools(); len(patterns) > 0 {
		kept := c.Tools[:0]
		for _, t := range c.Tools {
			if !hidden(t.Name, patterns) {
				kept = append(kept, t)
			}
		}
		c.Tools = kept
		if len(c.Tools) == 0 {
			c.ToolChoice = ""
		}
	}
	if StripsExtras(p, api) {
		stripExtras(c)
	}
}

// Whether a tool name matches any pattern, a star standing for any run of characters
func hidden(name string, patterns []string) bool {
	for _, pattern := range patterns {
		if matched, err := path.Match(strings.TrimSpace(pattern), name); err == nil && matched {
			return true
		}
	}
	return false
}

// The turns that are not system turns
func withoutSystem(messages []chat.Message) []chat.Message {
	out := make([]chat.Message, 0, len(messages))
	for _, m := range messages {
		if m.Role != "system" {
			out = append(out, m)
		}
	}
	return out
}

// Removes every unmapped field and the attribution block a harness prepends to its system prompt
func stripExtras(c *chat.Request) {
	chat.DropExtras(c)
	kept := c.Messages[:0]
	for _, m := range c.Messages {
		parts := m.Parts[:0]
		for _, p := range m.Parts {
			if m.Role == "system" && p.Type == "text" && strings.HasPrefix(strings.TrimSpace(p.Text), attributionMarker) {
				continue
			}
			parts = append(parts, p)
		}
		m.Parts = parts
		if m.Role == "system" && len(m.Parts) == 0 {
			continue
		}
		kept = append(kept, m)
	}
	c.Messages = kept
}

// Folds later system turns in a JSON body as sent, every other field kept as it was
func rewriteSystem(body []byte, mode v1.SystemMessages) ([]byte, error) {
	if mode != v1.SystemMessages_SYSTEM_MESSAGES_MERGE && mode != v1.SystemMessages_SYSTEM_MESSAGES_USER {
		return body, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return nil, fmt.Errorf("body is not a JSON object: %w", err)
	}
	raw, ok := fields["messages"]
	if !ok {
		return body, nil
	}
	var messages []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &messages); err != nil {
		return nil, fmt.Errorf("messages is not a list: %w", err)
	}
	role := func(m map[string]json.RawMessage) string {
		var s string
		json.Unmarshal(m["role"], &s)
		return s
	}
	late := false
	for i, m := range messages {
		late = late || i > 0 && role(m) == "system"
	}
	if !late {
		return body, nil
	}
	if mode == v1.SystemMessages_SYSTEM_MESSAGES_USER {
		for i, m := range messages {
			if i > 0 && role(m) == "system" {
				m["role"], _ = json.Marshal("user")
			}
		}
	} else {
		var kept []map[string]json.RawMessage
		var texts []string
		for i, m := range messages {
			if i > 0 && role(m) == "system" {
				texts = append(texts, contentText(m["content"]))
				continue
			}
			kept = append(kept, m)
		}
		text := strings.Join(texts, systemJoin)
		if len(kept) > 0 && role(kept[0]) == "system" {
			kept[0]["content"] = appendContent(kept[0]["content"], text)
		} else {
			content, _ := json.Marshal(text)
			lead := map[string]json.RawMessage{"role": json.RawMessage(`"system"`), "content": content}
			kept = append([]map[string]json.RawMessage{lead}, kept...)
		}
		messages = kept
	}
	var err error
	if fields["messages"], err = json.Marshal(messages); err != nil {
		return nil, err
	}
	return json.Marshal(fields)
}

// The text of a string or content part list
func contentText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	json.Unmarshal(raw, &parts)
	var b strings.Builder
	for _, p := range parts {
		if p.Type == "text" {
			b.WriteString(p.Text)
		}
	}
	return b.String()
}

// Appends text to wire content, joined onto plain text and added as a part beside anything else
func appendContent(raw json.RawMessage, text string) json.RawMessage {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if s != "" {
			text = s + systemJoin + text
		}
		out, _ := json.Marshal(text)
		return out
	}
	var parts []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &parts); err != nil {
		out, _ := json.Marshal(text)
		return out
	}
	allText := true
	for _, p := range parts {
		var kind string
		json.Unmarshal(p["type"], &kind)
		allText = allText && kind == "text"
	}
	if allText {
		if have := contentText(raw); have != "" {
			text = have + systemJoin + text
		}
		out, _ := json.Marshal(text)
		return out
	}
	part, _ := json.Marshal(systemJoin + text)
	parts = append(parts, map[string]json.RawMessage{"type": json.RawMessage(`"text"`), "text": part})
	out, _ := json.Marshal(parts)
	return out
}
