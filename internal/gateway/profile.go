package gateway

import (
	"context"
	"encoding/json"
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

// Probes support for later system messages with two one-token requests.
// The baseline starts with a system message. The second adds one after an
// assistant turn. A baseline failure makes the probe inconclusive.
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
	if status >= http.StatusMultipleChoices {
		return &v1.TemplateProbe{LateSystem: false, Refusal: message}
	}
	return &v1.TemplateProbe{LateSystem: true}
}

// Sends a one-token chat and returns its status and any runtime error.
func renderOnce(ctx context.Context, client *http.Client, target *url.URL, upstream Flavor, model string, messages []Message) (int, string, error) {
	path, out, err := upstream.RenderRequest(&Chat{Kind: "chat", Model: model, MaxTokens: 1, Messages: messages})
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
