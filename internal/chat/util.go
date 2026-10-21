package chat

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"time"
)

func ptr[T any](v T) *T { return &v }

// Reads tool arguments as a JSON object, empty or null meaning none
func jsonArgs(s string) (json.RawMessage, bool) {
	s = strings.TrimSpace(s)
	if s == "" || s == "null" {
		return json.RawMessage("{}"), true
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal([]byte(s), &fields) != nil {
		return nil, false
	}
	return json.RawMessage(s), true
}

// Gathers streamed text and thinking into a whole answer
type textGather struct {
	text, thinking strings.Builder
}

func (t *textGather) add(ev Event) {
	switch ev.Kind {
	case Said:
		t.text.WriteString(ev.Text)
	case Thought:
		t.thinking.WriteString(ev.Text)
	}
}

// Gathers streamed tool fragments into whole calls, in index order
type toolGather struct {
	calls map[int]*ToolCall
	order []int
}

func (t *toolGather) add(ev Event) {
	if t.calls == nil {
		t.calls = map[int]*ToolCall{}
	}
	c, ok := t.calls[ev.Index]
	if !ok {
		c = &ToolCall{}
		t.calls[ev.Index] = c
		t.order = append(t.order, ev.Index)
	}
	if ev.Tool.ID != "" {
		c.ID = ev.Tool.ID
	}
	if ev.Tool.Name != "" {
		c.Name = ev.Tool.Name
	}
	c.Args += ev.Tool.Args
}

func (t *toolGather) list() []ToolCall {
	out := make([]ToolCall, 0, len(t.order))
	for _, i := range t.order {
		c := *t.calls[i]
		if c.Args == "" {
			c.Args = "{}"
		}
		out = append(out, c)
	}
	return out
}

// Joins the text parts of a message
func TextOf(parts []Part) string {
	var b strings.Builder
	for _, p := range parts {
		if p.Type == "text" {
			b.WriteString(p.Text)
		}
	}
	return b.String()
}

// Joins the thinking parts of a message
func thinkingOf(parts []Part) string {
	var b strings.Builder
	for _, p := range parts {
		if p.Type == "thinking" {
			if b.Len() > 0 {
				b.WriteString("\n\n")
			}
			b.WriteString(p.Text)
		}
	}
	return b.String()
}

// The id an answer carries, a fresh one under the prefix when the runtime gave none
func resultID(r *Result, prefix string) string {
	if r.ID != "" {
		return r.ID
	}
	return newID(prefix)
}

func newID(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}

// The json names each wire struct maps, read from its tags once
var knownFields sync.Map

func fieldNames(known any) map[string]bool {
	t := reflect.TypeOf(known)
	if names, ok := knownFields.Load(t); ok {
		return names.(map[string]bool)
	}
	names := map[string]bool{}
	for i := 0; i < t.NumField(); i++ {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
		if name == "" {
			name = t.Field(i).Name
		}
		names[name] = true
	}
	knownFields.Store(t, names)
	return names
}

// Fields of a JSON object that a wire struct has no tag for
func extraFields(raw []byte, known any) map[string]json.RawMessage {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return nil
	}
	for name := range fieldNames(known) {
		delete(fields, name)
	}
	if len(fields) == 0 {
		return nil
	}
	return fields
}

// Marshals v and adds the extra fields v does not set itself
func withExtra(v any, extra map[string]json.RawMessage) ([]byte, error) {
	data, err := json.Marshal(v)
	if err != nil || len(extra) == 0 {
		return data, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, err
	}
	for k, raw := range extra {
		if _, taken := fields[k]; !taken {
			fields[k] = raw
		}
	}
	return json.Marshal(fields)
}

// Adds from's fields to into, later values winning
func mergeExtra(into, from map[string]json.RawMessage) map[string]json.RawMessage {
	if len(from) == 0 {
		return into
	}
	if into == nil {
		into = map[string]json.RawMessage{}
	}
	for k, v := range from {
		into[k] = v
	}
	return into
}

// Reads a string or a list of strings
func strs(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var one string
	if json.Unmarshal(raw, &one) == nil {
		if one == "" {
			return nil
		}
		return []string{one}
	}
	var many []string
	json.Unmarshal(raw, &many)
	return many
}

// Splits a data URL into media type and base64 payload
func DataURL(u string) (string, string, bool) {
	if !strings.HasPrefix(u, "data:") {
		return "", "", false
	}
	meta, data, ok := strings.Cut(strings.TrimPrefix(u, "data:"), ",")
	if !ok || !strings.HasSuffix(meta, ";base64") {
		return "", "", false
	}
	return strings.TrimSuffix(meta, ";base64"), data, true
}

// The image type of raw bytes, the declared type when the bytes say nothing, png failing both
func MediaTypeOf(raw []byte, header string) string {
	if mt := http.DetectContentType(raw); strings.HasPrefix(mt, "image/") {
		return mt
	}
	if header != "" && strings.HasPrefix(header, "image/") {
		return strings.TrimSpace(strings.Split(header, ";")[0])
	}
	return "image/png"
}

// The image type of a base64 payload by its leading bytes
func mediaTypeOfBase64(data string) string {
	head := data
	if len(head) > 1024 {
		head = head[:1024]
	}
	raw, err := base64.StdEncoding.DecodeString(head[:len(head)-len(head)%4])
	if err != nil {
		return "image/png"
	}
	return MediaTypeOf(raw, "")
}

// Tools and turns flattened into text for token counting
func promptText(req *Request) string {
	var b strings.Builder
	for _, t := range req.Tools {
		b.WriteString(t.Name)
		b.WriteString(" ")
		b.WriteString(t.Description)
		b.WriteString(" ")
		b.WriteString(string(t.Schema))
		b.WriteString("\n")
	}
	for _, m := range req.Messages {
		b.WriteString(m.Role)
		b.WriteString(": ")
		b.WriteString(TextOf(m.Parts))
		for _, t := range m.ToolCalls {
			b.WriteString(" ")
			b.WriteString(t.Name)
			b.WriteString(" ")
			b.WriteString(t.Args)
		}
		b.WriteString("\n")
	}
	for _, in := range req.Inputs {
		b.WriteString(in)
		b.WriteString("\n")
	}
	return b.String()
}

const imageTokensMax = 1600

// Estimates tokens at four bytes each
func TextTokens(s string) int { return (len(s) + 3) / 4 }

// Estimates tokens at four bytes each, plus turn overhead and image area
func EstimateTokens(req *Request) int {
	n := TextTokens(promptText(req))
	for range req.Messages {
		n += 3
	}
	return n + ImageTokens(req)
}

// Estimates image tokens by area across every turn
func ImageTokens(req *Request) int {
	n := 0
	for _, m := range req.Messages {
		for _, p := range m.Parts {
			if p.Type == "image" {
				n += imageTokens(p)
			}
		}
	}
	return n
}

// Estimates one image's tokens as area over 750, the cap for one that cannot be decoded
func imageTokens(p Part) int {
	raw, err := base64.StdEncoding.DecodeString(p.Data)
	if err != nil {
		return imageTokensMax
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		return imageTokensMax
	}
	return min(imageTokensMax, max(1, (cfg.Width*cfg.Height+749)/750))
}
