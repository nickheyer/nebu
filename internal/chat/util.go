package chat

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"net/http"
	"slices"
	"strings"
	"time"
)

func ptr[T any](v T) *T { return &v }

// Reads tool arguments as a JSON object, empty or null meaning none
func jsonArgs(s string) (json.RawMessage, bool) {
	s = strings.TrimSpace(s)
	if s == "" || s == "null" {
		return json.RawMessage("{}"), true
	}
	if !isObject([]byte(s)) {
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

// The text, thinking, calls and token ids of one choice as they arrive
type choiceGather struct {
	said   textGather
	tools  toolGather
	tokens []int
}

// Gathers every choice of an answer apart, by index
type answerGather map[int]*choiceGather

func (a answerGather) of(choice int) *choiceGather {
	g, ok := a[choice]
	if !ok {
		g = &choiceGather{}
		a[choice] = g
	}
	return g
}

func (a answerGather) add(ev Event) {
	switch ev.Kind {
	case Said, Thought:
		g := a.of(ev.Choice)
		g.said.add(ev)
		g.tokens = append(g.tokens, ev.TokenIDs...)
	case Called:
		a.of(ev.Choice).tools.add(ev)
	}
}

// The whole answer, each choice's gathered text and calls on its own entry, answer fields on the first
func (a answerGather) result(res *Result, model string) *Result {
	if res == nil {
		res = &Result{}
	}
	byIndex := map[int]*Result{}
	for _, r := range res.All() {
		c := *r
		c.More = nil
		byIndex[c.Choice] = &c
	}
	for choice := range a {
		if _, ok := byIndex[choice]; !ok {
			byIndex[choice] = &Result{Choice: choice}
		}
	}
	for choice, r := range byIndex {
		g := a.of(choice)
		r.Model, r.Text, r.Thinking, r.ToolCalls = model, g.said.text.String(), g.said.thinking.String(), g.tools.list()
		if len(g.tokens) > 0 {
			r.TokenIDs = g.tokens
		}
		r.Stop = StopOf(r.Stop, len(r.ToolCalls))
	}
	top := byIndex[res.Choice]
	first := choices(byIndex)
	if first != top {
		first.ID, first.Usage, first.Extra, first.Vectors, first.PromptTokenIDs = top.ID, top.Usage, top.Extra, top.Vectors, top.PromptTokenIDs
		top.ID, top.Usage, top.Extra, top.Vectors, top.PromptTokenIDs = "", Usage{}, nil, nil, nil
	}
	return first
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

// Whether any part holds token ids rather than text
func hasTokens(parts []Part) bool {
	return slices.ContainsFunc(parts, func(p Part) bool { return p.Type == "tokens" })
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

// Tools and turns flattened into text for a tokenizer
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
		b.WriteString(in.Text)
		b.WriteString("\n")
	}
	return b.String()
}

const (
	imageTokensMax = 1600
	// Tokens a chat template spends on each turn around its content
	turnTokens = 4
)

// Estimates tokens at four bytes each, rounded up
func TextTokens(s string) int { return (len(s) + 3) / 4 }

// Estimates the tokens of one part, text at four bytes each, token ids as they are, images by area
func partTokens(p Part) int {
	switch p.Type {
	case "text", "thinking":
		return TextTokens(p.Text)
	case "tokens":
		return len(p.Tokens)
	case "image":
		return imageTokens(p)
	}
	return 0
}

// Estimates prompt tokens at four bytes a token per text part, four per turn, and images by area
func EstimateTokens(req *Request) int {
	n := 0
	for _, t := range req.Tools {
		n += TextTokens(t.Name) + TextTokens(t.Description) + TextTokens(string(t.Schema))
	}
	for _, m := range req.Messages {
		n += turnTokens
		for _, p := range m.Parts {
			n += partTokens(p)
		}
		for _, c := range m.ToolCalls {
			n += TextTokens(c.Name) + TextTokens(c.Args)
		}
	}
	for _, in := range req.Inputs {
		n += partTokens(in)
	}
	return n
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

// Estimates one image's tokens as area over 750, reading only the header, the cap for one that cannot be decoded
func imageTokens(p Part) int {
	if p.Data == "" {
		return imageTokensMax
	}
	cfg, _, err := image.DecodeConfig(base64.NewDecoder(base64.StdEncoding, strings.NewReader(p.Data)))
	if err != nil {
		return imageTokensMax
	}
	return min(imageTokensMax, max(1, (cfg.Width*cfg.Height+749)/750))
}
