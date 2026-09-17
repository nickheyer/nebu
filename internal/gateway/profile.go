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

// Between two system texts folded into one
const systemJoin = "\n\n"

// How many bytes of a runtime's refusal the probe keeps
const refusalCap = 512

// Shapes a chat the way a route asks before a flavor renders it
//
// A system message after the first is folded into the first, made when the
// chat has none, or turned into a user turn where it stood. Keep leaves the
// chat as it came.
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

// Adds text to parts as one text part when every part is text, since a template may read only a string
// or the first part, and as one more part otherwise
func appendText(parts []Part, text string) []Part {
	for _, p := range parts {
		if p.Type != "text" {
			return append(parts, Part{Type: "text", Text: systemJoin + text})
		}
	}
	if have := textOf(parts); have != "" {
		text = have + systemJoin + text
	}
	return []Part{{Type: "text", Text: text}}
}

// Shapes a body the gateway passes through, touching only the messages and only when the mode says to
//
// The body keeps every field the client set, which is why it is not read into a
// chat and rendered again. The messages array is the OpenAI and Ollama shape, a
// role and a content that is a string or a list of parts.
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

// The text of a wire content, a string or the text parts of a list
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

// Adds text to a wire content the way appendText does to parts
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

// Learns whether a runtime's chat template renders a system message after the first
//
// Two one-token chats go to the runtime in its own flavor. The first has the
// system message where every template takes it and must succeed, or the probe
// cannot tell and says why. The second adds a system message after an
// assistant turn; a refusal is the template's, in the runtime's own words.
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

// Sends one one-token chat and reads the status and, past success, the runtime's message
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
