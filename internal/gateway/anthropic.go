package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

const (
	anthropicMessages  = "/v1/messages"
	anthropicCount     = "/v1/messages/count_tokens"
	anthropicMaxTokens = 4096
)

// The Anthropic Messages wire format
type anthropic struct{}

type antBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
	Source    *struct {
		Type      string `json:"type"`
		MediaType string `json:"media_type"`
		Data      string `json:"data"`
		URL       string `json:"url"`
	} `json:"source,omitempty"`
}

type antMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type antRequest struct {
	Model         string          `json:"model"`
	System        json.RawMessage `json:"system,omitempty"`
	Messages      []antMessage    `json:"messages"`
	MaxTokens     int             `json:"max_tokens,omitempty"`
	Temperature   *float64        `json:"temperature,omitempty"`
	TopP          *float64        `json:"top_p,omitempty"`
	TopK          *int            `json:"top_k,omitempty"`
	StopSequences []string        `json:"stop_sequences,omitempty"`
	Stream        bool            `json:"stream,omitempty"`
	Tools         []antTool       `json:"tools,omitempty"`
	ToolChoice    *antToolChoice  `json:"tool_choice,omitempty"`
}

type antTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema,omitempty"`
}

type antToolChoice struct {
	Type string `json:"type"`
	Name string `json:"name,omitempty"`
}

type antUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

type antResponse struct {
	ID           string     `json:"id"`
	Type         string     `json:"type"`
	Role         string     `json:"role"`
	Model        string     `json:"model"`
	Content      []antBlock `json:"content"`
	StopReason   *string    `json:"stop_reason"`
	StopSequence *string    `json:"stop_sequence"`
	Usage        antUsage   `json:"usage"`
}

// Reads blocks, a bare string being one text block
func antBlocks(raw json.RawMessage) []antBlock {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		if text == "" {
			return nil
		}
		return []antBlock{{Type: "text", Text: text}}
	}
	var blocks []antBlock
	json.Unmarshal(raw, &blocks)
	return blocks
}

func (anthropic) ParseRequest(path string, body []byte) (*Chat, error) {
	if path != anthropicMessages && path != anthropicCount {
		return nil, bad("%s is not an endpoint the anthropic flavor serves", path)
	}
	var req antRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, bad("%v", err)
	}
	c := &Chat{Kind: "chat", Model: req.Model, MaxTokens: req.MaxTokens, Temperature: req.Temperature, TopP: req.TopP, TopK: req.TopK, Stop: req.StopSequences, Stream: req.Stream}
	if path == anthropicCount {
		c.Kind, c.Stream = "count", false
	}
	if system := textOf(antParts(antBlocks(req.System))); system != "" {
		c.Messages = append(c.Messages, Message{Role: "system", Parts: []Part{{Type: "text", Text: system}}})
	}
	for _, m := range req.Messages {
		blocks := antBlocks(m.Content)
		msg := Message{Role: m.Role}
		for _, b := range blocks {
			switch b.Type {
			case "tool_use":
				msg.ToolCalls = append(msg.ToolCalls, ToolCall{ID: b.ID, Name: b.Name, Args: string(b.Input)})
			case "tool_result":
				// Each result is its own tool turn, the way every other flavor carries them
				if len(msg.Parts) > 0 || len(msg.ToolCalls) > 0 {
					c.Messages = append(c.Messages, msg)
					msg = Message{Role: m.Role}
				}
				c.Messages = append(c.Messages, Message{Role: "tool", ToolID: b.ToolUseID, Parts: []Part{{Type: "text", Text: textOf(antParts(antBlocks(b.Content)))}}})
			default:
				msg.Parts = append(msg.Parts, antParts([]antBlock{b})...)
			}
		}
		if len(msg.Parts) > 0 || len(msg.ToolCalls) > 0 {
			c.Messages = append(c.Messages, msg)
		}
	}
	for _, t := range req.Tools {
		c.Tools = append(c.Tools, Tool{Name: t.Name, Description: t.Description, Schema: t.InputSchema})
	}
	if req.ToolChoice != nil {
		switch req.ToolChoice.Type {
		case "any":
			c.ToolChoice = "required"
		case "tool":
			c.ToolChoice = req.ToolChoice.Name
		case "none", "auto":
			c.ToolChoice = req.ToolChoice.Type
		}
	}
	return c, nil
}

// Turns text and image blocks into parts
func antParts(blocks []antBlock) []Part {
	var out []Part
	for _, b := range blocks {
		switch b.Type {
		case "text":
			out = append(out, Part{Type: "text", Text: b.Text})
		case "image":
			if b.Source != nil {
				out = append(out, Part{Type: "image", MediaType: b.Source.MediaType, Data: b.Source.Data, URL: b.Source.URL})
			}
		}
	}
	return out
}

func antContent(parts []Part) []antBlock {
	var out []antBlock
	for _, p := range parts {
		switch p.Type {
		case "text":
			out = append(out, antBlock{Type: "text", Text: p.Text})
		case "image":
			b := antBlock{Type: "image", Source: &struct {
				Type      string `json:"type"`
				MediaType string `json:"media_type"`
				Data      string `json:"data"`
				URL       string `json:"url"`
			}{}}
			if p.URL != "" {
				b.Source.Type, b.Source.URL = "url", p.URL
			} else {
				b.Source.Type, b.Source.MediaType, b.Source.Data = "base64", p.MediaType, p.Data
			}
			out = append(out, b)
		}
	}
	return out
}

func (anthropic) RenderRequest(c *Chat) (string, []byte, error) {
	if c.Kind == "embed" {
		return "", nil, bad("the anthropic flavor has no embeddings endpoint")
	}
	req := antRequest{Model: c.Model, MaxTokens: c.MaxTokens, Temperature: c.Temperature, TopP: c.TopP, TopK: c.TopK, StopSequences: c.Stop, Stream: c.Stream}
	if req.MaxTokens == 0 {
		req.MaxTokens = anthropicMaxTokens
	}
	var system []string
	for _, m := range c.Messages {
		if m.Role == "system" {
			system = append(system, textOf(m.Parts))
		}
	}
	if len(system) > 0 {
		req.System, _ = json.Marshal(strings.Join(system, "\n\n"))
	}
	// Turns alternate, so neighbours with one role merge and tool results ride on a user turn
	var last *antMessage
	push := func(role string, blocks []antBlock) {
		if last != nil && last.Role == role {
			var have []antBlock
			json.Unmarshal(last.Content, &have)
			last.Content, _ = json.Marshal(append(have, blocks...))
			return
		}
		data, _ := json.Marshal(blocks)
		req.Messages = append(req.Messages, antMessage{Role: role, Content: data})
		last = &req.Messages[len(req.Messages)-1]
	}
	for _, m := range c.Messages {
		switch m.Role {
		case "system":
		case "tool":
			content, _ := json.Marshal([]antBlock{{Type: "text", Text: textOf(m.Parts)}})
			push("user", []antBlock{{Type: "tool_result", ToolUseID: m.ToolID, Content: content}})
		case "assistant":
			blocks := antContent(m.Parts)
			for _, t := range m.ToolCalls {
				args := json.RawMessage(t.Args)
				if !json.Valid(args) {
					args = json.RawMessage("{}")
				}
				blocks = append(blocks, antBlock{Type: "tool_use", ID: t.ID, Name: t.Name, Input: args})
			}
			if len(blocks) > 0 {
				push("assistant", blocks)
			}
		default:
			if blocks := antContent(m.Parts); len(blocks) > 0 {
				push("user", blocks)
			}
		}
	}
	for _, t := range c.Tools {
		schema := t.Schema
		if len(schema) == 0 {
			schema = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		req.Tools = append(req.Tools, antTool{Name: t.Name, Description: t.Description, InputSchema: schema})
	}
	switch c.ToolChoice {
	case "":
	case "required":
		req.ToolChoice = &antToolChoice{Type: "any"}
	case "auto", "none":
		req.ToolChoice = &antToolChoice{Type: c.ToolChoice}
	default:
		req.ToolChoice = &antToolChoice{Type: "tool", Name: c.ToolChoice}
	}
	if c.Kind == "count" {
		// The count endpoint takes the prompt alone and refuses sampling fields
		req.MaxTokens, req.Temperature, req.TopP, req.TopK, req.StopSequences, req.Stream = 0, nil, nil, nil, nil, false
		data, err := json.Marshal(req)
		return anthropicCount, data, err
	}
	data, err := json.Marshal(req)
	return anthropicMessages, data, err
}

func antStop(reason string) string {
	switch reason {
	case "max_tokens":
		return "length"
	case "tool_use":
		return "tool"
	}
	return "stop"
}

func antReason(stop string) string {
	switch stop {
	case "length":
		return "max_tokens"
	case "tool":
		return "tool_use"
	}
	return "end_turn"
}

func (anthropic) ParseResult(c *Chat, body []byte) (*Result, error) {
	if c.Kind == "count" {
		var count antUsage
		if err := json.Unmarshal(body, &count); err != nil {
			return nil, err
		}
		return &Result{Model: c.Model, In: count.InputTokens}, nil
	}
	var resp antResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	r := &Result{ID: resp.ID, Model: resp.Model, In: resp.Usage.InputTokens, Out: resp.Usage.OutputTokens}
	for _, b := range resp.Content {
		switch b.Type {
		case "text":
			r.Text += b.Text
		case "tool_use":
			r.ToolCalls = append(r.ToolCalls, ToolCall{ID: b.ID, Name: b.Name, Args: string(b.Input)})
		}
	}
	if resp.StopReason != nil {
		r.Stop = antStop(*resp.StopReason)
	}
	r.Stop = stopOf(r.Stop, len(r.ToolCalls))
	return r, nil
}

func (anthropic) RenderResult(c *Chat, r *Result) ([]byte, error) {
	if c.Kind == "count" {
		return json.Marshal(map[string]any{"input_tokens": r.In})
	}
	id := r.ID
	if id == "" {
		id = newID("msg")
	}
	resp := antResponse{ID: id, Type: "message", Role: "assistant", Model: r.Model, Content: []antBlock{}, StopReason: ptr(antReason(r.Stop)), Usage: antUsage{InputTokens: r.In, OutputTokens: r.Out}}
	if r.Text != "" || len(r.ToolCalls) == 0 {
		resp.Content = append(resp.Content, antBlock{Type: "text", Text: r.Text})
	}
	for _, t := range r.ToolCalls {
		args := json.RawMessage(t.Args)
		if !json.Valid(args) {
			args = json.RawMessage("{}")
		}
		resp.Content = append(resp.Content, antBlock{Type: "tool_use", ID: t.ID, Name: t.Name, Input: args})
	}
	return json.Marshal(resp)
}

func (anthropic) ParseStream(rd io.Reader, emit func(Event) error) error {
	final := &Result{}
	var tools toolGather
	blocks := map[int]string{}
	err := readSSE(rd, func(event string, data []byte) error {
		var ev struct {
			Type         string       `json:"type"`
			Index        int          `json:"index"`
			Message      *antResponse `json:"message"`
			ContentBlock *antBlock    `json:"content_block"`
			Delta        *struct {
				Type        string  `json:"type"`
				Text        string  `json:"text"`
				PartialJSON string  `json:"partial_json"`
				StopReason  *string `json:"stop_reason"`
			} `json:"delta"`
			Usage *antUsage `json:"usage"`
		}
		if err := json.Unmarshal(data, &ev); err != nil {
			return err
		}
		switch ev.Type {
		case "message_start":
			if ev.Message != nil {
				final.ID, final.Model, final.In = ev.Message.ID, ev.Message.Model, ev.Message.Usage.InputTokens
			}
			return emit(Event{Kind: "start", Res: &Result{ID: final.ID, Model: final.Model}})
		case "content_block_start":
			if ev.ContentBlock != nil && ev.ContentBlock.Type == "tool_use" {
				blocks[ev.Index] = "tool"
				e := Event{Kind: "tool", Index: ev.Index, Tool: &ToolCall{ID: ev.ContentBlock.ID, Name: ev.ContentBlock.Name}}
				tools.add(e)
				return emit(e)
			}
			blocks[ev.Index] = "text"
		case "content_block_delta":
			if ev.Delta == nil {
				return nil
			}
			if ev.Delta.Type == "input_json_delta" {
				e := Event{Kind: "tool", Index: ev.Index, Tool: &ToolCall{Args: ev.Delta.PartialJSON}}
				tools.add(e)
				return emit(e)
			}
			if ev.Delta.Text != "" {
				return emit(Event{Kind: "text", Text: ev.Delta.Text})
			}
		case "message_delta":
			if ev.Delta != nil && ev.Delta.StopReason != nil {
				final.Stop = antStop(*ev.Delta.StopReason)
			}
			if ev.Usage != nil {
				final.Out = ev.Usage.OutputTokens
				if ev.Usage.InputTokens > 0 {
					final.In = ev.Usage.InputTokens
				}
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	final.ToolCalls = tools.list()
	final.Stop = stopOf(final.Stop, len(final.ToolCalls))
	return emit(Event{Kind: "stop", Res: final})
}

type antStream struct {
	w     http.ResponseWriter
	id    string
	model string
	// The open content block, -1 for none, and its kind
	index int
	kind  string
	// Tool fragments by stream index map onto our own block indices
	toolBlock map[int]int
	toolIndex int
	tools     toolGather
	started   bool
	in        int
}

func (anthropic) Stream(w http.ResponseWriter, c *Chat) StreamWriter {
	streamHeaders(w, "text/event-stream")
	return &antStream{w: w, id: newID("msg"), model: c.Model, index: -1, toolBlock: map[int]int{}}
}

func (s *antStream) start() error {
	if s.started {
		return nil
	}
	s.started = true
	msg := antResponse{ID: s.id, Type: "message", Role: "assistant", Model: s.model, Content: []antBlock{}, Usage: antUsage{InputTokens: s.in}}
	return writeSSE(s.w, "message_start", map[string]any{"type": "message_start", "message": msg})
}

func (s *antStream) closeBlock() error {
	if s.index < 0 {
		return nil
	}
	err := writeSSE(s.w, "content_block_stop", map[string]any{"type": "content_block_stop", "index": s.index})
	s.kind = ""
	return err
}

func (s *antStream) openBlock(block antBlock) error {
	if err := s.closeBlock(); err != nil {
		return err
	}
	s.index++
	s.kind = block.Type
	return writeSSE(s.w, "content_block_start", map[string]any{"type": "content_block_start", "index": s.index, "content_block": block})
}

func (s *antStream) Write(ev Event) error {
	switch ev.Kind {
	case "start":
		if ev.Res != nil {
			if ev.Res.ID != "" {
				s.id = ev.Res.ID
			}
			if ev.Res.Model != "" {
				s.model = ev.Res.Model
			}
			s.in = ev.Res.In
		}
		return s.start()
	case "text":
		if err := s.start(); err != nil {
			return err
		}
		if s.kind != "text" {
			if err := s.openBlock(antBlock{Type: "text", Text: ""}); err != nil {
				return err
			}
		}
		return writeSSE(s.w, "content_block_delta", map[string]any{"type": "content_block_delta", "index": s.index, "delta": map[string]any{"type": "text_delta", "text": ev.Text}})
	case "tool":
		if err := s.start(); err != nil {
			return err
		}
		s.tools.add(ev)
		if _, open := s.toolBlock[ev.Index]; !open || s.kind != "tool_use" || s.toolBlock[ev.Index] != s.index {
			if _, seen := s.toolBlock[ev.Index]; seen && s.toolBlock[ev.Index] == s.index {
				// The block is ours already
			} else {
				id, name := ev.Tool.ID, ev.Tool.Name
				if id == "" {
					id = newID("toolu")
				}
				if err := s.openBlock(antBlock{Type: "tool_use", ID: id, Name: name, Input: json.RawMessage("{}")}); err != nil {
					return err
				}
				s.toolBlock[ev.Index] = s.index
			}
		}
		if ev.Tool.Args == "" {
			return nil
		}
		return writeSSE(s.w, "content_block_delta", map[string]any{"type": "content_block_delta", "index": s.index, "delta": map[string]any{"type": "input_json_delta", "partial_json": ev.Tool.Args}})
	case "stop":
		if err := s.start(); err != nil {
			return err
		}
		if err := s.closeBlock(); err != nil {
			return err
		}
		res := ev.Res
		if res == nil {
			res = &Result{}
		}
		reason := antReason(stopOf(res.Stop, len(s.tools.order)))
		// Input tokens ride on the final delta too, an OpenAI runtime only reports them at the end
		if err := writeSSE(s.w, "message_delta", map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": reason, "stop_sequence": nil}, "usage": map[string]any{"input_tokens": res.In, "output_tokens": res.Out}}); err != nil {
			return err
		}
		return writeSSE(s.w, "message_stop", map[string]any{"type": "message_stop"})
	}
	return nil
}

func (s *antStream) Close() error { return nil }

func (anthropic) ErrorMessage(body []byte) string {
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	json.Unmarshal(body, &e)
	return e.Error.Message
}

func (anthropic) Error(w http.ResponseWriter, status int, message, kind string) {
	writeJSON(w, status, map[string]any{"type": "error", "error": map[string]any{"type": kind, "message": message}})
}
