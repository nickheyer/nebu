package chat

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// The tags a model writes calls in and reads results from when tools live in the prompt
const (
	CallOpen      = "<tool_call>"
	CallClose     = "</tool_call>"
	ResponseOpen  = "<tool_response"
	ResponseClose = "</tool_response>"
)

// A runtime that refuses tool definitions, served by writing them into the prompt
type emulated struct {
	Runtime
}

// Wraps a runtime so its tools go in the prompt and the calls come out of the text
func Emulate(rt Runtime) Runtime { return emulated{rt} }

func (e emulated) Render(c *Request) (string, []byte, error) {
	return e.Runtime.Render(foldTools(c))
}

// Reads a whole answer, the calls the model wrote taken out of its text
func (e emulated) ParseResult(c *Request, body []byte) (*Result, error) {
	res, err := e.Runtime.ParseResult(c, body)
	if err != nil {
		return nil, err
	}
	var text strings.Builder
	var calls []ToolCall
	p := &CallParser{Emit: func(ev Event) error {
		switch ev.Kind {
		case Said:
			text.WriteString(ev.Text)
		case Called:
			calls = append(calls, *ev.Tool)
		}
		return nil
	}}
	p.Text(res.Text)
	p.Flush()
	res.Text = text.String()
	res.ToolCalls = append(res.ToolCalls, calls...)
	res.Stop = StopOf(res.Stop, len(res.ToolCalls))
	return res, nil
}

// Reads a streamed answer, text going out as it arrives and each call once its block closes
func (e emulated) ParseStream(r io.Reader, emit func(Event) error) error {
	var calls []ToolCall
	p := &CallParser{Emit: func(ev Event) error {
		if ev.Kind == Called {
			calls = append(calls, *ev.Tool)
		}
		return emit(ev)
	}}
	return e.Runtime.ParseStream(r, func(ev Event) error {
		switch ev.Kind {
		case Said:
			return p.Text(ev.Text)
		case Stopped:
			if err := p.Flush(); err != nil {
				return err
			}
			if ev.Res == nil {
				ev.Res = &Result{}
			}
			ev.Res.ToolCalls = append(ev.Res.ToolCalls, calls...)
			ev.Res.Stop = StopOf(ev.Res.Stop, len(ev.Res.ToolCalls))
		}
		return emit(ev)
	})
}

// A copy of the request with tools in the system prompt and calls and results written as text
func foldTools(c *Request) *Request {
	out := *c
	out.Tools, out.ToolChoice, out.Messages = nil, "", nil
	for _, m := range c.Messages {
		msg := Message{Role: m.Role, Parts: append([]Part(nil), m.Parts...), Extra: m.Extra}
		switch m.Role {
		case "assistant":
			if len(m.ToolCalls) > 0 {
				text := TextOf(m.Parts)
				for _, call := range m.ToolCalls {
					text = strings.TrimRight(text, "\n") + "\n" + callText(call) + "\n"
				}
				msg.Parts = []Part{{Type: "text", Text: text}}
				for _, p := range m.Parts {
					if p.Type != "text" {
						msg.Parts = append(msg.Parts, p)
					}
				}
			}
		case "tool":
			msg.Role = "user"
			msg.Parts = responseParts(m)
		}
		if n := len(out.Messages); n > 0 && out.Messages[n-1].Role == "user" && msg.Role == "user" {
			out.Messages[n-1].Parts = joinParts(out.Messages[n-1].Parts, msg.Parts)
			continue
		}
		out.Messages = append(out.Messages, msg)
	}
	if len(c.Tools) == 0 || c.ToolChoice == "none" {
		return &out
	}
	prompt := toolPrompt(c.Tools, c.ToolChoice)
	if len(out.Messages) > 0 && out.Messages[0].Role == "system" {
		out.Messages[0].Parts = AppendText(out.Messages[0].Parts, prompt)
		return &out
	}
	out.Messages = append([]Message{{Role: "system", Parts: []Part{{Type: "text", Text: prompt}}}}, out.Messages...)
	return &out
}

// A call as the model writes it
func callText(call ToolCall) string {
	args := json.RawMessage(strings.TrimSpace(call.Args))
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}
	body, _ := json.Marshal(map[string]any{"name": call.Name, "arguments": args})
	return CallOpen + "\n" + string(body) + "\n" + CallClose
}

// A tool turn as a user turn, the text in a response block with images beside it
func responseParts(m Message) []Part {
	var b strings.Builder
	fmt.Fprintf(&b, "%s id=%q>\n", ResponseOpen, m.ToolID)
	b.WriteString(TextOf(m.Parts))
	b.WriteString("\n" + ResponseClose)
	out := []Part{{Type: "text", Text: b.String()}}
	for _, p := range m.Parts {
		if p.Type == "image" {
			out = append(out, p)
		}
	}
	return out
}

// Joins two turns' parts, text with a blank line between
func joinParts(a, b []Part) []Part {
	out := append([]Part(nil), a...)
	for _, p := range b {
		if p.Type == "text" && len(out) > 0 && out[len(out)-1].Type == "text" {
			out[len(out)-1].Text += "\n\n" + p.Text
			continue
		}
		out = append(out, p)
	}
	return out
}

// Adds text to a run of parts, joined onto plain text and appended as a part beside anything else
func AppendText(parts []Part, text string) []Part {
	for _, p := range parts {
		if p.Type != "text" || len(p.Extra) > 0 {
			return append(parts, Part{Type: "text", Text: "\n\n" + text})
		}
	}
	if have := TextOf(parts); have != "" {
		text = have + "\n\n" + text
	}
	return []Part{{Type: "text", Text: text}}
}

// The system text that offers tools and says how to call them
func toolPrompt(tools []Tool, choice string) string {
	var b strings.Builder
	b.WriteString("# Tools\n\nYou can call the tools listed below. To call one, write a " + CallOpen + " block holding a JSON object with the tool's name and its arguments, one block per call, after any text you want to say first, then stop and wait:\n\n")
	b.WriteString(CallOpen + "\n{\"name\": \"tool_name\", \"arguments\": {\"argument\": \"value\"}}\n" + CallClose + "\n\n")
	b.WriteString("Each result comes back in a " + ResponseOpen + "> block. Never write " + ResponseOpen + "> blocks yourself.\n")
	switch choice {
	case "", "auto":
	case "required":
		b.WriteString("You must call at least one tool before answering.\n")
	default:
		fmt.Fprintf(&b, "You must call the tool %s now.\n", choice)
	}
	for _, t := range tools {
		fmt.Fprintf(&b, "\n## %s\n", t.Name)
		if t.Description != "" {
			b.WriteString(t.Description + "\n")
		}
		schema := strings.TrimSpace(string(t.Schema))
		if schema == "" {
			schema = `{"type":"object","properties":{}}`
		}
		b.WriteString("Arguments, as a JSON schema: " + schema + "\n")
	}
	return strings.TrimSpace(b.String())
}

// Splits a model's text into text and the calls it wrote, as the text arrives
type CallParser struct {
	Emit func(Event) error
	buf  string
	// Whether a call block is open, its body gathering in buf
	open bool
	n    int
}

func (p *CallParser) Text(s string) error {
	p.buf += s
	for {
		if p.open {
			i := strings.Index(p.buf, CallClose)
			if i < 0 {
				return nil
			}
			body := p.buf[:i]
			p.buf = p.buf[i+len(CallClose):]
			p.open = false
			if err := p.call(body); err != nil {
				return err
			}
			continue
		}
		i := strings.Index(p.buf, CallOpen)
		if i >= 0 {
			if err := p.say(p.buf[:i]); err != nil {
				return err
			}
			p.buf = p.buf[i+len(CallOpen):]
			p.open = true
			continue
		}
		keep := partialPrefix(p.buf, CallOpen)
		if err := p.say(p.buf[:len(p.buf)-keep]); err != nil {
			return err
		}
		p.buf = p.buf[len(p.buf)-keep:]
		return nil
	}
}

func (p *CallParser) say(s string) error {
	if s == "" {
		return nil
	}
	return p.Emit(Event{Kind: Said, Text: s})
}

// Emits a call from a block's body, or the block as text when it holds no call
func (p *CallParser) call(body string) error {
	call, ok := parseCall(body)
	if !ok {
		return p.say(CallOpen + body + CallClose)
	}
	call.ID = "call_" + randomID()
	ev := Event{Kind: Called, Index: p.n, Tool: &call}
	p.n++
	return p.Emit(ev)
}

// Emits what is left once the text ends, an unclosed block as a call when it parses
func (p *CallParser) Flush() error {
	rest := p.buf
	p.buf = ""
	if p.open {
		p.open = false
		if call, ok := parseCall(rest); ok {
			call.ID = "call_" + randomID()
			ev := Event{Kind: Called, Index: p.n, Tool: &call}
			p.n++
			return p.Emit(ev)
		}
		return p.say(CallOpen + rest)
	}
	return p.say(rest)
}

// A call from a block body, a JSON object naming the tool and its arguments, fenced or not
func parseCall(body string) (ToolCall, bool) {
	s := strings.TrimSpace(body)
	s = strings.TrimPrefix(strings.TrimPrefix(s, "```json"), "```")
	s = strings.TrimSuffix(strings.TrimSpace(s), "```")
	var raw struct {
		Name       string          `json:"name"`
		Arguments  json.RawMessage `json:"arguments"`
		Parameters json.RawMessage `json:"parameters"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(s)), &raw); err != nil || raw.Name == "" {
		return ToolCall{}, false
	}
	args := raw.Arguments
	if len(args) == 0 {
		args = raw.Parameters
	}
	var inner string
	if json.Unmarshal(args, &inner) == nil {
		args = json.RawMessage(inner)
	}
	parsed, ok := jsonArgs(string(args))
	if !ok {
		return ToolCall{}, false
	}
	return ToolCall{Name: raw.Name, Args: string(parsed)}, true
}

// How many bytes at the end of s could be the start of tag
func partialPrefix(s, tag string) int {
	for n := min(len(s), len(tag)-1); n > 0; n-- {
		if strings.HasSuffix(s, tag[:n]) {
			return n
		}
	}
	return 0
}
