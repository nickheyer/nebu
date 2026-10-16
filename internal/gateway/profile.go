package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// Separator for merged system messages.
const systemJoin = "\n\n"

// Maximum probe error body size.
const refusalCap = 512

// Applies the route's system message policy before rendering. Merge combines
// system messages at the start. User converts later ones to user turns. Keep
// preserves the chat.
func foldSystem(c *Chat, mode v1.SystemMessages) {
	switch mode {
	case v1.SystemMessages_SYSTEM_MESSAGES_MERGE:
		var kept []Message
		var late []string
		for i, m := range c.Messages {
			if i > 0 && m.Role == "system" {
				late = append(late, textOf(m.Parts))
				continue
			}
			kept = append(kept, m)
		}
		if len(late) == 0 {
			return
		}
		text := strings.Join(late, systemJoin)
		if len(kept) > 0 && kept[0].Role == "system" {
			kept[0].Parts = appendText(kept[0].Parts, text)
		} else {
			kept = append([]Message{{Role: "system", Parts: []Part{{Type: "text", Text: text}}}}, kept...)
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
		p.GetSystemPrompt() != v1.SystemPrompt_SYSTEM_PROMPT_UNSPECIFIED || p.GetOverrides() != "" || p != nil && (p.AllowAll != nil || p.StripExtras != nil)
}

// Whether permission checks are answered as allowed, which a profile that says nothing does
func AllowsAll(p *v1.Profile) bool {
	if p == nil || p.AllowAll == nil {
		return true
	}
	return *p.AllowAll
}

// Whether a client's extras stay out, which a profile that says nothing does for an Anthropic client
func StripsExtras(p *v1.Profile, format v1.ApiFlavor) bool {
	if p == nil || p.StripExtras == nil {
		return format == v1.ApiFlavor_API_FLAVOR_ANTHROPIC
	}
	return *p.StripExtras
}

// The opening of the block a coding agent harness prepends to its system prompt for attribution
const attributionMarker = "x-anthropic-billing-header"

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
func Shaped(p *v1.Profile, format v1.ApiFlavor) bool {
	return p.GetSystemPrompt() == v1.SystemPrompt_SYSTEM_PROMPT_DROP || p.GetSystemPrompt() == v1.SystemPrompt_SYSTEM_PROMPT_REPLACE || StripsExtras(p, format) || p.GetOverrides() != ""
}

// Applies the profile's system prompt and field stripping to a chat before rendering
func shape(c *Chat, p *v1.Profile) {
	switch p.GetSystemPrompt() {
	case v1.SystemPrompt_SYSTEM_PROMPT_DROP:
		c.Messages = withoutSystem(c.Messages)
	case v1.SystemPrompt_SYSTEM_PROMPT_REPLACE:
		c.Messages = append([]Message{{Role: "system", Parts: []Part{{Type: "text", Text: p.GetSystemText()}}}}, withoutSystem(c.Messages)...)
	}
	if StripsExtras(p, c.Format) {
		stripExtras(c)
	}
}

// The turns that are not system turns
func withoutSystem(messages []Message) []Message {
	out := make([]Message, 0, len(messages))
	for _, m := range messages {
		if m.Role != "system" {
			out = append(out, m)
		}
	}
	return out
}

// Removes every field the gateway has no mapping for at every level, and the attribution block
func stripExtras(c *Chat) {
	c.Extra = nil
	kept := c.Messages[:0]
	for _, m := range c.Messages {
		m.Extra = nil
		parts := m.Parts[:0]
		for _, p := range m.Parts {
			if m.Role == "system" && p.Type == "text" && strings.HasPrefix(strings.TrimSpace(p.Text), attributionMarker) {
				continue
			}
			p.Extra = nil
			parts = append(parts, p)
		}
		m.Parts = parts
		if m.Role == "system" && len(m.Parts) == 0 {
			continue
		}
		kept = append(kept, m)
	}
	c.Messages = kept
	for i := range c.Tools {
		c.Tools[i].Extra = nil
	}
}

// Renders requests through a flavor with the profile's overrides written over chats and prompts
type shaped struct {
	Flavor
	overrides string
}

// The upstream flavor with the profile's overrides, the flavor itself when it has none
func shapeFlavor(f Flavor, p *v1.Profile) Flavor {
	if p.GetOverrides() == "" {
		return f
	}
	return shaped{Flavor: f, overrides: p.GetOverrides()}
}

func (s shaped) RenderRequest(c *Chat) (string, []byte, error) {
	path, body, err := s.Flavor.RenderRequest(c)
	if err != nil || c.Kind != "chat" && c.Kind != "generate" {
		return path, body, err
	}
	body, err = withOverrides(body, s.overrides)
	return path, body, err
}

// Writes the fields of a JSON object over a JSON body's, the overrides winning
func withOverrides(body []byte, overrides string) ([]byte, error) {
	var fields, over map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return nil, fmt.Errorf("request is not a JSON object: %w", err)
	}
	if err := json.Unmarshal([]byte(overrides), &over); err != nil {
		return nil, fmt.Errorf("overrides are not a JSON object: %w", err)
	}
	for k, v := range over {
		fields[k] = v
	}
	return json.Marshal(fields)
}

// Combines plain text parts for templates
func appendText(parts []Part, text string) []Part {
	for _, p := range parts {
		if p.Type != "text" || len(p.Extra) > 0 {
			return append(parts, Part{Type: "text", Text: systemJoin + text})
		}
	}
	if have := textOf(parts); have != "" {
		text = have + systemJoin + text
	}
	return []Part{{Type: "text", Text: text}}
}

// Applies system message policy directly to OpenAI or Ollama JSON messages.
// Preserves all other client fields without a full parse-and-render cycle.
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

// Extracts text from a string or content part list.
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

// Appends text to wire content using appendText semantics.
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

// Probes later system messages and tools with three one token requests, a baseline failure being inconclusive
func ProbeTemplate(ctx context.Context, client *http.Client, endpoint string, api v1.ApiFlavor, model string) *v1.TemplateProbe {
	target, err := url.Parse(endpoint)
	if err != nil {
		return &v1.TemplateProbe{Error: "endpoint: " + err.Error()}
	}
	upstream := flavorOf(api)
	turns := []Message{
		{Role: "system", Parts: []Part{{Type: "text", Text: "You answer briefly."}}},
		{Role: "user", Parts: []Part{{Type: "text", Text: "Say ok."}}},
		{Role: "assistant", Parts: []Part{{Type: "text", Text: "ok"}}},
	}
	control := append(append([]Message{}, turns...), Message{Role: "user", Parts: []Part{{Type: "text", Text: "Say ok again."}}})
	probe := append(append([]Message{}, turns...),
		Message{Role: "system", Parts: []Part{{Type: "text", Text: "Answer in lower case."}}},
		Message{Role: "user", Parts: []Part{{Type: "text", Text: "Say ok again."}}})
	if status, message, err := renderOnce(ctx, client, target, upstream, model, control); err != nil {
		return &v1.TemplateProbe{Error: "a plain chat request failed: " + err.Error()}
	} else if status >= http.StatusMultipleChoices {
		return &v1.TemplateProbe{Error: fmt.Sprintf("the runtime answered %d to a plain chat request: %s", status, message)}
	}
	status, message, err := renderOnce(ctx, client, target, upstream, model, probe)
	if err != nil {
		return &v1.TemplateProbe{Error: "the chat request with a late system message failed: " + err.Error()}
	}
	out := &v1.TemplateProbe{LateSystem: status < http.StatusMultipleChoices}
	if !out.LateSystem {
		out.Refusal = message
	}
	status, message, err = renderTool(ctx, client, target, upstream, model, control)
	switch {
	case err != nil:
		out.Error = "the chat request with a tool failed: " + err.Error()
	case status >= http.StatusMultipleChoices:
		out.Tools, out.ToolsRefusal = v1.ToolSupport_TOOL_SUPPORT_EMULATED, message
	default:
		out.Tools = v1.ToolSupport_TOOL_SUPPORT_NATIVE
	}
	return out
}

// The tool the probe offers
var probeTool = Tool{Name: "probe", Description: "Reports whether the runtime takes tools", Schema: json.RawMessage(`{"type":"object","properties":{}}`)}

// Sends a one-token chat offering a tool and returns its status and any runtime error
func renderTool(ctx context.Context, client *http.Client, target *url.URL, upstream Flavor, model string, messages []Message) (int, string, error) {
	return sendOnce(ctx, client, target, upstream, &Chat{Kind: "chat", Model: model, MaxTokens: 1, Messages: messages, Tools: []Tool{probeTool}, ToolChoice: "auto"})
}

// Sends a one-token chat and returns its status and any runtime error.
func renderOnce(ctx context.Context, client *http.Client, target *url.URL, upstream Flavor, model string, messages []Message) (int, string, error) {
	return sendOnce(ctx, client, target, upstream, &Chat{Kind: "chat", Model: model, MaxTokens: 1, Messages: messages})
}

// Sends one chat and returns its status and any runtime error
func sendOnce(ctx context.Context, client *http.Client, target *url.URL, upstream Flavor, chat *Chat) (int, string, error) {
	path, out, err := upstream.RenderRequest(chat)
	if err != nil {
		return 0, "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target.ResolveReference(&url.URL{Path: path}).String(), strings.NewReader(string(out)))
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if resp.StatusCode < http.StatusMultipleChoices {
		return resp.StatusCode, "", nil
	}
	message := upstream.ErrorMessage(raw)
	if message == "" {
		message = strings.TrimSpace(string(raw))
	}
	if len(message) > refusalCap {
		message = message[:refusalCap]
	}
	return resp.StatusCode, message, nil
}
