package gateway

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/nickheyer/nebu/internal/db"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

const (
	anthropicMessages  = "/v1/messages"
	anthropicCount     = "/v1/messages/count_tokens"
	anthropicMaxTokens = 4096
)

// The Anthropic Messages wire format
type anthropic struct{}

type antSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type,omitempty"`
	Data      string `json:"data,omitempty"`
	URL       string `json:"url,omitempty"`
}

type antBlock struct {
	Type      string                     `json:"type"`
	Text      string                     `json:"text,omitempty"`
	ID        string                     `json:"id,omitempty"`
	Name      string                     `json:"name,omitempty"`
	Input     json.RawMessage            `json:"input,omitempty"`
	ToolUseID string                     `json:"tool_use_id,omitempty"`
	Content   json.RawMessage            `json:"content,omitempty"`
	Source    *antSource                 `json:"source,omitempty"`
	Thinking  string                     `json:"thinking,omitempty"`
	Signature string                     `json:"signature,omitempty"`
	Extra     map[string]json.RawMessage `json:"-"`
}

func (b *antBlock) UnmarshalJSON(data []byte) error {
	type plain antBlock
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	*b = antBlock(p)
	b.Extra = extraFields(data, plain{})
	return nil
}

func (b antBlock) MarshalJSON() ([]byte, error) {
	type plain antBlock
	return withExtra(plain(b), b.Extra)
}

// A text block written to clients, which always carries its text field.
type antText struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type antMessage struct {
	Role    string                     `json:"role"`
	Content json.RawMessage            `json:"content"`
	Extra   map[string]json.RawMessage `json:"-"`
}

func (m *antMessage) UnmarshalJSON(data []byte) error {
	type plain antMessage
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	*m = antMessage(p)
	m.Extra = extraFields(data, plain{})
	return nil
}

func (m antMessage) MarshalJSON() ([]byte, error) {
	type plain antMessage
	return withExtra(plain(m), m.Extra)
}

type antRequest struct {
	Model         string                     `json:"model"`
	System        json.RawMessage            `json:"system,omitempty"`
	Messages      []antMessage               `json:"messages"`
	MaxTokens     int                        `json:"max_tokens,omitempty"`
	Temperature   *float64                   `json:"temperature,omitempty"`
	TopP          *float64                   `json:"top_p,omitempty"`
	TopK          *int                       `json:"top_k,omitempty"`
	StopSequences []string                   `json:"stop_sequences,omitempty"`
	Stream        bool                       `json:"stream,omitempty"`
	Tools         []antTool                  `json:"tools,omitempty"`
	ToolChoice    *antToolChoice             `json:"tool_choice,omitempty"`
	Extra         map[string]json.RawMessage `json:"-"`
}

func (r *antRequest) UnmarshalJSON(data []byte) error {
	type plain antRequest
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	*r = antRequest(p)
	r.Extra = extraFields(data, plain{})
	return nil
}

func (r antRequest) MarshalJSON() ([]byte, error) {
	type plain antRequest
	return withExtra(plain(r), r.Extra)
}

type antTool struct {
	Type        string                     `json:"type,omitempty"`
	Name        string                     `json:"name"`
	Description string                     `json:"description,omitempty"`
	InputSchema json.RawMessage            `json:"input_schema,omitempty"`
	Strict      *bool                      `json:"strict,omitempty"`
	Extra       map[string]json.RawMessage `json:"-"`
}

func (t *antTool) UnmarshalJSON(data []byte) error {
	type plain antTool
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	*t = antTool(p)
	t.Extra = extraFields(data, plain{})
	return nil
}

func (t antTool) MarshalJSON() ([]byte, error) {
	type plain antTool
	return withExtra(plain(t), t.Extra)
}

type antToolChoice struct {
	Type string `json:"type"`
	Name string `json:"name,omitempty"`
}

type antUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// A message read from a runtime
type antResponse struct {
	ID           string                     `json:"id"`
	Type         string                     `json:"type"`
	Role         string                     `json:"role"`
	Model        string                     `json:"model"`
	Content      []antBlock                 `json:"content"`
	StopReason   *string                    `json:"stop_reason"`
	StopSequence *string                    `json:"stop_sequence"`
	Usage        antUsage                   `json:"usage"`
	Extra        map[string]json.RawMessage `json:"-"`
}

func (r *antResponse) UnmarshalJSON(data []byte) error {
	type plain antResponse
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	*r = antResponse(p)
	r.Extra = extraFields(data, plain{})
	return nil
}

// A message written to a client
type antReply struct {
	ID           string                     `json:"id"`
	Type         string                     `json:"type"`
	Role         string                     `json:"role"`
	Model        string                     `json:"model"`
	Content      []any                      `json:"content"`
	StopReason   *string                    `json:"stop_reason"`
	StopSequence *string                    `json:"stop_sequence"`
	Usage        antUsage                   `json:"usage"`
	Extra        map[string]json.RawMessage `json:"-"`
}

func (r antReply) MarshalJSON() ([]byte, error) {
	type plain antReply
	return withExtra(plain(r), r.Extra)
}

// The delta of a message_delta or content_block_delta event
type antDelta struct {
	Type        string                     `json:"type"`
	Text        string                     `json:"text"`
	PartialJSON string                     `json:"partial_json"`
	StopReason  *string                    `json:"stop_reason"`
	Extra       map[string]json.RawMessage `json:"-"`
}

func (d *antDelta) UnmarshalJSON(data []byte) error {
	type plain antDelta
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	*d = antDelta(p)
	d.Extra = extraFields(data, plain{})
	return nil
}

// Message IDs the gateway hands out, in the Anthropic shape
func antID() string { return "msg_" + db.NewID() }

// Parses blocks, treating a bare string as one text block.
func antBlocks(raw json.RawMessage) ([]antBlock, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		if text == "" {
			return nil, nil
		}
		return []antBlock{{Type: "text", Text: text}}, nil
	}
	var blocks []antBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil, bad("content must be a string or a list of blocks")
	}
	return blocks, nil
}

func (anthropic) ParseRequest(path string, body []byte) (*Chat, error) {
	if path != anthropicMessages && path != anthropicCount {
		return nil, bad("%s is not an endpoint the anthropic flavor serves", path)
	}
	var req antRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, bad("%v", err)
	}
	c := &Chat{Kind: "chat", Format: v1.ApiFlavor_API_FLAVOR_ANTHROPIC, Model: req.Model, MaxTokens: req.MaxTokens, Temperature: req.Temperature, TopP: req.TopP, TopK: req.TopK, Stop: req.StopSequences, Stream: req.Stream, Extra: req.Extra}
	if path == anthropicCount {
		c.Kind, c.Stream = "count", false
	}
	blocks, err := antBlocks(req.System)
	if err != nil {
		return nil, err
	}
	system, err := antParts(blocks, false)
	if err != nil {
		return nil, err
	}
	for _, p := range system {
		if p.Type != "text" {
			return nil, bad("system takes text blocks, not %s", p.Type)
		}
	}
	// One part per block keeps each block's own fields.
	if textOf(system) != "" {
		c.Messages = append(c.Messages, Message{Role: "system", Parts: system})
	}
	for _, m := range req.Messages {
		blocks, err := antBlocks(m.Content)
		if err != nil {
			return nil, err
		}
		// Fields on the message object go with the first turn made from it.
		extra := m.Extra
		msg := Message{Role: m.Role}
		for _, b := range blocks {
			switch b.Type {
			case "tool_use":
				msg.ToolCalls = append(msg.ToolCalls, ToolCall{ID: b.ID, Name: b.Name, Args: string(b.Input)})
			case "tool_result":
				// Convert each result to a separate tool turn.
				inner, err := antBlocks(b.Content)
				if err != nil {
					return nil, err
				}
				parts, err := antParts(inner, false)
				if err != nil {
					return nil, err
				}
				if len(msg.Parts) > 0 || len(msg.ToolCalls) > 0 {
					msg.Extra, extra = extra, nil
					c.Messages = append(c.Messages, msg)
					msg = Message{Role: m.Role}
				}
				c.Messages = append(c.Messages, Message{Role: "tool", ToolID: b.ToolUseID, Parts: parts, Extra: extra})
				extra = nil
			default:
				parts, err := antParts([]antBlock{b}, m.Role == "assistant")
				if err != nil {
					return nil, err
				}
				msg.Parts = append(msg.Parts, parts...)
			}
		}
		if len(msg.Parts) > 0 || len(msg.ToolCalls) > 0 {
			msg.Extra = extra
			c.Messages = append(c.Messages, msg)
		}
	}
	for _, t := range req.Tools {
		c.Tools = append(c.Tools, Tool{Type: t.Type, Name: t.Name, Description: t.Description, Schema: t.InputSchema, Strict: t.Strict, Extra: t.Extra})
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

// Turns text and image blocks into parts, plus thinking blocks in assistant
// turns. Any other block type is refused by name.
func antParts(blocks []antBlock, assistant bool) ([]Part, error) {
	var out []Part
	for _, b := range blocks {
		switch b.Type {
		case "text":
			out = append(out, Part{Type: "text", Text: b.Text, Extra: b.Extra})
		case "image":
			if b.Source == nil {
				return nil, bad("an image block needs a source")
			}
			out = append(out, Part{Type: "image", MediaType: b.Source.MediaType, Data: b.Source.Data, URL: b.Source.URL, Extra: b.Extra})
		case "thinking":
			if !assistant {
				return nil, bad("thinking blocks belong in assistant turns")
			}
			out = append(out, Part{Type: "thinking", Text: b.Thinking, Data: b.Signature, Extra: b.Extra})
		default:
			return nil, bad("%s blocks are not content the gateway can pass to a model", b.Type)
		}
	}
	return out, nil
}

// Renders parts as blocks. Blank text is left out because the API refuses it.
// The fields the gateway does not map go along when extras is set.
func antContent(parts []Part, extras bool) []antBlock {
	var out []antBlock
	for _, p := range parts {
		var extra map[string]json.RawMessage
		if extras {
			extra = p.Extra
		}
		switch p.Type {
		case "text":
			if strings.TrimSpace(p.Text) != "" {
				out = append(out, antBlock{Type: "text", Text: p.Text, Extra: extra})
			}
		case "image":
			src := &antSource{}
			if p.URL != "" {
				src.Type, src.URL = "url", p.URL
			} else {
				src.Type, src.MediaType, src.Data = "base64", p.MediaType, p.Data
			}
			out = append(out, antBlock{Type: "image", Source: src, Extra: extra})
		case "thinking":
			out = append(out, antBlock{Type: "thinking", Thinking: p.Text, Signature: p.Data, Extra: extra})
		}
	}
	return out
}

func (anthropic) RenderRequest(c *Chat) (string, []byte, error) {
	if c.Kind == "embed" {
		return "", nil, bad("the anthropic flavor has no embeddings endpoint")
	}
	// The API refuses fields it does not know, so fields the gateway does not
	// map go through only when a client wrote them for this API.
	native := c.Format == v1.ApiFlavor_API_FLAVOR_ANTHROPIC
	keep := func(extra map[string]json.RawMessage) map[string]json.RawMessage {
		if native {
			return extra
		}
		return nil
	}
	req := antRequest{Model: c.Model, MaxTokens: c.MaxTokens, Temperature: c.Temperature, TopP: c.TopP, TopK: c.TopK, StopSequences: c.Stop, Stream: c.Stream, Extra: keep(c.Extra)}
	if req.MaxTokens == 0 {
		req.MaxTokens = anthropicMaxTokens
	}
	// The API samples with temperature or top_p, never both. A top_p of 1 changes nothing and is dropped.
	if req.Temperature != nil && req.TopP != nil {
		if *req.TopP != 1 {
			return "", nil, bad("this model samples with temperature or top_p, not both")
		}
		req.TopP = nil
	}
	var system []antBlock
	for _, m := range c.Messages {
		if m.Role == "system" {
			for _, p := range m.Parts {
				if p.Type == "text" && strings.TrimSpace(p.Text) != "" {
					system = append(system, antBlock{Type: "text", Text: p.Text, Extra: keep(p.Extra)})
				}
			}
		}
	}
	if len(system) > 0 {
		req.System, _ = json.Marshal(system)
	}
	// Merge adjacent same-role turns and put tool results in user turns.
	var last *antMessage
	push := func(role string, blocks []antBlock, extra map[string]json.RawMessage) {
		if last != nil && last.Role == role {
			var have []antBlock
			json.Unmarshal(last.Content, &have)
			last.Content, _ = json.Marshal(append(have, blocks...))
			for k, v := range extra {
				if _, taken := last.Extra[k]; !taken {
					last.Extra = mergeExtra(last.Extra, map[string]json.RawMessage{k: v})
				}
			}
			return
		}
		data, _ := json.Marshal(blocks)
		req.Messages = append(req.Messages, antMessage{Role: role, Content: data, Extra: extra})
		last = &req.Messages[len(req.Messages)-1]
	}
	for _, m := range c.Messages {
		switch m.Role {
		case "system":
		case "tool":
			block := antBlock{Type: "tool_result", ToolUseID: m.ToolID}
			if blocks := antContent(m.Parts, native); len(blocks) > 0 {
				block.Content, _ = json.Marshal(blocks)
			}
			push("user", []antBlock{block}, keep(m.Extra))
		case "assistant":
			blocks := antContent(m.Parts, native)
			for _, t := range m.ToolCalls {
				input, ok := jsonArgs(t.Args)
				if !ok {
					return "", nil, bad("tool call %s has arguments that are not a JSON object", t.Name)
				}
				blocks = append(blocks, antBlock{Type: "tool_use", ID: t.ID, Name: t.Name, Input: input})
			}
			if len(blocks) > 0 {
				push("assistant", blocks, keep(m.Extra))
			}
		default:
			if blocks := antContent(m.Parts, native); len(blocks) > 0 {
				push("user", blocks, keep(m.Extra))
			}
		}
	}
	for _, t := range c.Tools {
		tool := antTool{Type: t.Type, Name: t.Name, Description: t.Description, InputSchema: t.Schema, Strict: t.Strict, Extra: keep(t.Extra)}
		// Tools defined by schema need one. Tools the API provides carry none.
		if len(tool.InputSchema) == 0 && (t.Type == "" || t.Type == "custom") {
			tool.InputSchema = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		req.Tools = append(req.Tools, tool)
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
		// Exclude sampling fields from token count requests.
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
	r := &Result{ID: resp.ID, Model: resp.Model, In: resp.Usage.InputTokens, Out: resp.Usage.OutputTokens, Extra: resp.Extra}
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

// Tool call IDs from the runtime are kept so tool results match them. Missing ones are made up.
func antToolID(id string) string {
	if id != "" {
		return id
	}
	return "toolu_" + db.NewID()
}

func (anthropic) RenderResult(c *Chat, r *Result) ([]byte, error) {
	if c.Kind == "count" {
		return json.Marshal(map[string]any{"input_tokens": r.In})
	}
	content := []any{}
	if r.Text != "" {
		content = append(content, antText{Type: "text", Text: r.Text})
	}
	for _, t := range r.ToolCalls {
		input, ok := jsonArgs(t.Args)
		if !ok {
			return nil, &toolArgsError{name: t.Name}
		}
		content = append(content, antBlock{Type: "tool_use", ID: antToolID(t.ID), Name: t.Name, Input: input})
	}
	resp := antReply{ID: antID(), Type: "message", Role: "assistant", Model: r.Model, Content: content, StopReason: ptr(antReason(r.Stop)), Usage: antUsage{InputTokens: r.In, OutputTokens: r.Out}, Extra: r.Extra}
	return json.Marshal(resp)
}

func (anthropic) ParseStream(rd io.Reader, emit func(Event) error) error {
	final := &Result{}
	var tools toolGather
	// Tool calls numbered in order of appearance, since content block indexes also count text blocks.
	order := map[int]int{}
	err := readSSE(rd, func(event string, data []byte) error {
		var ev struct {
			Type         string       `json:"type"`
			Index        int          `json:"index"`
			Message      *antResponse `json:"message"`
			ContentBlock *antBlock    `json:"content_block"`
			Delta        *antDelta    `json:"delta"`
			Usage        *antUsage    `json:"usage"`
			Error        *struct {
				Type    string `json:"type"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(data, &ev); err != nil {
			return err
		}
		switch ev.Type {
		case "error":
			message := ""
			if ev.Error != nil {
				message = ev.Error.Message
			}
			if message == "" {
				message = strings.TrimSpace(string(data))
			}
			return errors.New(message)
		case "message_start":
			if ev.Message != nil {
				final.ID, final.Model, final.In = ev.Message.ID, ev.Message.Model, ev.Message.Usage.InputTokens
				final.Extra = mergeExtra(final.Extra, ev.Message.Extra)
			}
			return emit(Event{Kind: "start", Res: &Result{ID: final.ID, Model: final.Model, In: final.In}})
		case "content_block_start":
			if ev.ContentBlock != nil && ev.ContentBlock.Type == "tool_use" {
				n := len(order)
				order[ev.Index] = n
				e := Event{Kind: "tool", Index: n, Tool: &ToolCall{ID: ev.ContentBlock.ID, Name: ev.ContentBlock.Name}}
				tools.add(e)
				return emit(e)
			}
		case "content_block_delta":
			if ev.Delta == nil {
				return nil
			}
			if ev.Delta.Type == "input_json_delta" {
				n, ok := order[ev.Index]
				if !ok {
					return fmt.Errorf("tool input arrived for content block %d, which is not a tool call", ev.Index)
				}
				e := Event{Kind: "tool", Index: n, Tool: &ToolCall{Args: ev.Delta.PartialJSON}}
				tools.add(e)
				return emit(e)
			}
			if ev.Delta.Text != "" {
				return emit(Event{Kind: "text", Text: ev.Delta.Text})
			}
		case "message_delta":
			if ev.Delta != nil {
				if ev.Delta.StopReason != nil {
					final.Stop = antStop(*ev.Delta.StopReason)
				}
				final.Extra = mergeExtra(final.Extra, ev.Delta.Extra)
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
	live *liveWriter
	// Open content block index and kind. Kind is empty between blocks.
	index int
	kind  string
	// Stream index of the tool call in the open block, and stream indexes whose blocks are already closed.
	tool int
	done map[int]bool
	// Text that arrived while a tool block was open. It is written as its own block once the tool block closes.
	held  strings.Builder
	tools toolGather
}

// Opens the stream with message_start at once, as the API does, so the client
// sees the message before the model has produced anything. Pings follow while
// it works. Input tokens arrive in the closing message_delta.
func (anthropic) Stream(w http.ResponseWriter, c *Chat) StreamWriter {
	streamHeaders(w, "text/event-stream")
	s := &antStream{live: newLiveWriter(w, antPing), index: -1, done: map[int]bool{}}
	msg := antReply{ID: antID(), Type: "message", Role: "assistant", Model: c.Model, Content: []any{}}
	s.live.sse("message_start", map[string]any{"type": "message_start", "message": msg})
	return s
}

func antPing(w http.ResponseWriter) error {
	return writeSSE(w, "ping", map[string]any{"type": "ping"})
}

func (s *antStream) open(block any, kind string) error {
	s.index++
	s.kind = kind
	return s.live.sse("content_block_start", map[string]any{"type": "content_block_start", "index": s.index, "content_block": block})
}

func (s *antStream) close() error {
	if s.kind == "" {
		return nil
	}
	if s.kind == "tool_use" {
		s.done[s.tool] = true
	}
	s.kind = ""
	return s.live.sse("content_block_stop", map[string]any{"type": "content_block_stop", "index": s.index})
}

func (s *antStream) delta(delta map[string]any) error {
	return s.live.sse("content_block_delta", map[string]any{"type": "content_block_delta", "index": s.index, "delta": delta})
}

// Closes the open block, then writes text held back behind a tool block as a block of its own.
func (s *antStream) settle() error {
	if err := s.close(); err != nil {
		return err
	}
	if s.held.Len() == 0 {
		return nil
	}
	text := s.held.String()
	s.held.Reset()
	if err := s.open(antText{Type: "text"}, "text"); err != nil {
		return err
	}
	if err := s.delta(map[string]any{"type": "text_delta", "text": text}); err != nil {
		return err
	}
	return s.close()
}

func (s *antStream) Write(ev Event) error {
	switch ev.Kind {
	case "start":
		return nil
	case "text":
		if s.kind == "tool_use" {
			s.held.WriteString(ev.Text)
			return nil
		}
		if s.kind != "text" {
			if err := s.settle(); err != nil {
				return err
			}
			if err := s.open(antText{Type: "text"}, "text"); err != nil {
				return err
			}
		}
		return s.delta(map[string]any{"type": "text_delta", "text": ev.Text})
	case "tool":
		s.tools.add(ev)
		if s.kind != "tool_use" || s.tool != ev.Index {
			if s.done[ev.Index] {
				return fmt.Errorf("upstream sent more of tool call %d after moving on from it", ev.Index)
			}
			call := s.tools.calls[ev.Index]
			if call.Name == "" {
				return fmt.Errorf("upstream started tool call %d without naming the tool", ev.Index)
			}
			call.ID = antToolID(call.ID)
			if err := s.settle(); err != nil {
				return err
			}
			if err := s.open(antBlock{Type: "tool_use", ID: call.ID, Name: call.Name, Input: json.RawMessage("{}")}, "tool_use"); err != nil {
				return err
			}
			s.tool = ev.Index
		}
		if ev.Tool.Args == "" {
			return nil
		}
		return s.delta(map[string]any{"type": "input_json_delta", "partial_json": ev.Tool.Args})
	case "stop":
		if err := s.settle(); err != nil {
			return err
		}
		for _, call := range s.tools.list() {
			if _, ok := jsonArgs(call.Args); !ok {
				return &toolArgsError{name: call.Name}
			}
		}
		res := ev.Res
		if res == nil {
			res = &Result{}
		}
		reason := antReason(stopOf(res.Stop, len(s.tools.order)))
		// The delta carries the message's closing fields: the stop reason, input
		// tokens the runtime counted late, and every other top-level key it added.
		delta := map[string]any{"stop_reason": reason, "stop_sequence": nil}
		for k, v := range res.Extra {
			if _, taken := delta[k]; !taken {
				delta[k] = v
			}
		}
		if err := s.live.sse("message_delta", map[string]any{"type": "message_delta", "delta": delta, "usage": map[string]any{"input_tokens": res.In, "output_tokens": res.Out}}); err != nil {
			return err
		}
		return s.live.sse("message_stop", map[string]any{"type": "message_stop"})
	case "error":
		return s.live.sse("error", map[string]any{"type": "error", "error": map[string]any{"type": "api_error", "message": ev.Text}})
	}
	return nil
}

func (s *antStream) Close() error {
	s.live.close()
	return nil
}

func (anthropic) InlineImages() bool { return false }

// The count endpoint sizes image blocks itself
func (anthropic) CountsImages() bool { return true }

func (anthropic) ErrorMessage(body []byte) string { return errorField(body) }

func (anthropic) Error(w http.ResponseWriter, status int, message, kind string) {
	writeJSON(w, status, map[string]any{"type": "error", "error": map[string]any{"type": kind, "message": message}})
}
