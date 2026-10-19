package chat

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

// OpenAI endpoints, the media ones served by the gateway itself
const (
	OpenAIChat        = "/v1/chat/completions"
	OpenAICompletions = "/v1/completions"
	OpenAIEmbeddings  = "/v1/embeddings"
	OpenAIImages      = "/v1/images/generations"
	OpenAIEdits       = "/v1/images/edits"
	OpenAIVideos      = "/v1/videos"
	// The tokenizer endpoint llama.cpp, vLLM, and SGLang share
	openaiTokenize = "/tokenize"
)

// The OpenAI chat completions protocol
type openai struct{}

type oaiMessage struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content,omitempty"`
	Name       string          `json:"name,omitempty"`
	ToolCalls  []oaiToolCall   `json:"tool_calls,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
	// Reasoning under the llama.cpp name and the newer vLLM name
	ReasoningContent string                     `json:"reasoning_content,omitempty"`
	Reasoning        string                     `json:"reasoning,omitempty"`
	Extra            map[string]json.RawMessage `json:"-"`
}

// The reasoning a message carries under either name
func (m *oaiMessage) reasoning() string {
	if m.ReasoningContent != "" {
		return m.ReasoningContent
	}
	return m.Reasoning
}

func (m *oaiMessage) UnmarshalJSON(data []byte) error {
	type plain oaiMessage
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	*m = oaiMessage(p)
	m.Extra = extraFields(data, plain{})
	return nil
}

func (m oaiMessage) MarshalJSON() ([]byte, error) {
	type plain oaiMessage
	return withExtra(plain(m), m.Extra)
}

type oaiToolCall struct {
	Index    *int   `json:"index,omitempty"`
	ID       string `json:"id,omitempty"`
	Type     string `json:"type,omitempty"`
	Function struct {
		Name      string `json:"name,omitempty"`
		Arguments string `json:"arguments,omitempty"`
	} `json:"function"`
}

// A content part, text or an image URL
type oaiPart struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL *struct {
		URL string `json:"url"`
	} `json:"image_url,omitempty"`
	Extra map[string]json.RawMessage `json:"-"`
}

func (p *oaiPart) UnmarshalJSON(data []byte) error {
	type plain oaiPart
	var v plain
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	*p = oaiPart(v)
	p.Extra = extraFields(data, plain{})
	return nil
}

// Writes the field its type calls for, so a text part always carries text
func (p oaiPart) MarshalJSON() ([]byte, error) {
	fields := map[string]any{"type": p.Type}
	switch p.Type {
	case "text":
		fields["text"] = p.Text
	case "image_url":
		fields["image_url"] = p.ImageURL
	}
	return withExtra(fields, p.Extra)
}

type oaiRequest struct {
	Model               string          `json:"model"`
	Messages            []oaiMessage    `json:"messages,omitempty"`
	Prompt              json.RawMessage `json:"prompt,omitempty"`
	Input               json.RawMessage `json:"input,omitempty"`
	MaxTokens           int             `json:"max_tokens,omitempty"`
	MaxCompletionTokens int             `json:"max_completion_tokens,omitempty"`
	Temperature         *float64        `json:"temperature,omitempty"`
	TopP                *float64        `json:"top_p,omitempty"`
	Stop                json.RawMessage `json:"stop,omitempty"`
	Stream              bool            `json:"stream,omitempty"`
	StreamOptions       *struct {
		IncludeUsage bool `json:"include_usage"`
	} `json:"stream_options,omitempty"`
	Tools      []oaiTool                  `json:"tools,omitempty"`
	ToolChoice json.RawMessage            `json:"tool_choice,omitempty"`
	Extra      map[string]json.RawMessage `json:"-"`
}

func (r *oaiRequest) UnmarshalJSON(data []byte) error {
	type plain oaiRequest
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	*r = oaiRequest(p)
	r.Extra = extraFields(data, plain{})
	return nil
}

func (r oaiRequest) MarshalJSON() ([]byte, error) {
	type plain oaiRequest
	return withExtra(plain(r), r.Extra)
}

type oaiFunction struct {
	Name        string                     `json:"name"`
	Description string                     `json:"description,omitempty"`
	Parameters  json.RawMessage            `json:"parameters,omitempty"`
	Strict      *bool                      `json:"strict,omitempty"`
	Extra       map[string]json.RawMessage `json:"-"`
}

func (f *oaiFunction) UnmarshalJSON(data []byte) error {
	type plain oaiFunction
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	*f = oaiFunction(p)
	f.Extra = extraFields(data, plain{})
	return nil
}

func (f oaiFunction) MarshalJSON() ([]byte, error) {
	type plain oaiFunction
	return withExtra(plain(f), f.Extra)
}

type oaiTool struct {
	Type     string                     `json:"type"`
	Function oaiFunction                `json:"function"`
	Extra    map[string]json.RawMessage `json:"-"`
}

func (t *oaiTool) UnmarshalJSON(data []byte) error {
	type plain oaiTool
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	*t = oaiTool(p)
	t.Extra = extraFields(data, plain{})
	return nil
}

func (t oaiTool) MarshalJSON() ([]byte, error) {
	type plain oaiTool
	return withExtra(plain(t), t.Extra)
}

type oaiUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type oaiChoice struct {
	Index        int         `json:"index"`
	Message      *oaiMessage `json:"message,omitempty"`
	Delta        *oaiMessage `json:"delta,omitempty"`
	Text         string      `json:"text,omitempty"`
	FinishReason *string     `json:"finish_reason"`
}

// Per request timings a llama.cpp server adds, with the speculative decoding counters
type oaiTimings struct {
	DraftN         int `json:"draft_n"`
	DraftNAccepted int `json:"draft_n_accepted"`
}

type oaiResponse struct {
	ID      string      `json:"id"`
	Object  string      `json:"object"`
	Created int64       `json:"created"`
	Model   string      `json:"model"`
	Choices []oaiChoice `json:"choices"`
	Usage   *oaiUsage   `json:"usage,omitempty"`
	Timings *oaiTimings `json:"timings,omitempty"`
	Data    []struct {
		Embedding []float64 `json:"embedding"`
	} `json:"data,omitempty"`
	Extra map[string]json.RawMessage `json:"-"`
}

func (r *oaiResponse) UnmarshalJSON(data []byte) error {
	type plain oaiResponse
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	*r = oaiResponse(p)
	r.Extra = extraFields(data, plain{})
	return nil
}

func (r oaiResponse) MarshalJSON() ([]byte, error) {
	type plain oaiResponse
	return withExtra(plain(r), r.Extra)
}

func (openai) headers() []string { return []string{"Openai-"} }

func (openai) Endpoint(path string) Kind {
	switch path {
	case OpenAIChat:
		return Chat
	case OpenAICompletions:
		return Generate
	case OpenAIEmbeddings:
		return Embed
	case OpenAIImages, OpenAIEdits:
		return Image
	case OpenAIVideos:
		return Video
	}
	return None
}

func (openai) Parse(path string, body []byte) (*Request, error) {
	var req oaiRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, bad("%v", err)
	}
	c := &Request{Kind: Chat, Model: req.Model, MaxTokens: max(req.MaxTokens, req.MaxCompletionTokens), Temperature: req.Temperature, TopP: req.TopP, Stop: strs(req.Stop), Stream: req.Stream, Extra: req.Extra}
	switch path {
	case OpenAIEmbeddings:
		c.Kind, c.Inputs = Embed, strs(req.Input)
		return c, nil
	case OpenAICompletions:
		c.Kind = Generate
		c.Messages = []Message{{Role: "user", Parts: []Part{{Type: "text", Text: strings.Join(strs(req.Prompt), "")}}}}
		return c, nil
	}
	for _, m := range req.Messages {
		msg := Message{Role: m.Role, ToolID: m.ToolCallID, Extra: m.Extra}
		if reasoning := m.reasoning(); reasoning != "" {
			if m.Role != "assistant" {
				return nil, bad("reasoning_content belongs in assistant turns, not %s turns", m.Role)
			}
			msg.Parts = append(msg.Parts, Part{Type: "thinking", Text: reasoning})
		}
		parts, err := oaiParts(m.Content)
		if err != nil {
			return nil, err
		}
		msg.Parts = append(msg.Parts, parts...)
		for _, t := range m.ToolCalls {
			msg.ToolCalls = append(msg.ToolCalls, ToolCall{ID: t.ID, Name: t.Function.Name, Args: t.Function.Arguments})
		}
		c.Messages = append(c.Messages, msg)
	}
	c.Tools = toolsFromOAI(req.Tools)
	if len(req.ToolChoice) > 0 {
		var s string
		if json.Unmarshal(req.ToolChoice, &s) == nil {
			c.ToolChoice = s
		} else {
			var named struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			}
			json.Unmarshal(req.ToolChoice, &named)
			c.ToolChoice = named.Function.Name
		}
	}
	return c, nil
}

// Reads message content as a string or a list of text and image parts, any other part refused by name
func oaiParts(raw json.RawMessage) ([]Part, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		if text == "" {
			return nil, nil
		}
		return []Part{{Type: "text", Text: text}}, nil
	}
	var list []oaiPart
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, bad("message content must be a string or a list of parts")
	}
	var out []Part
	for _, p := range list {
		switch p.Type {
		case "text":
			out = append(out, Part{Type: "text", Text: p.Text, Extra: p.Extra})
		case "image_url":
			part := Part{Type: "image", Extra: p.Extra}
			if p.ImageURL != nil {
				part.URL = p.ImageURL.URL
			}
			if mt, data, ok := DataURL(part.URL); ok {
				part.MediaType, part.Data, part.URL = mt, data, ""
			}
			out = append(out, part)
		default:
			return nil, bad("%s parts are not content the gateway can pass to a model", p.Type)
		}
	}
	return out, nil
}

// Renders parts, a bare string for one plain text part and a part list otherwise
func oaiContent(parts []Part) json.RawMessage {
	var list []oaiPart
	for _, p := range parts {
		switch p.Type {
		case "text":
			list = append(list, oaiPart{Type: "text", Text: p.Text, Extra: p.Extra})
		case "image":
			url := p.URL
			if url == "" {
				url = "data:" + p.MediaType + ";base64," + p.Data
			}
			list = append(list, oaiPart{Type: "image_url", ImageURL: &struct {
				URL string `json:"url"`
			}{url}, Extra: p.Extra})
		}
	}
	if len(list) == 0 {
		return json.RawMessage(`""`)
	}
	if len(list) == 1 && list[0].Type == "text" && len(list[0].Extra) == 0 {
		out, _ := json.Marshal(list[0].Text)
		return out
	}
	out, _ := json.Marshal(list)
	return out
}

// OpenAI tool definitions, which Ollama shares
func toolsFromOAI(tools []oaiTool) []Tool {
	var out []Tool
	for _, t := range tools {
		tool := Tool{Name: t.Function.Name, Description: t.Function.Description, Schema: t.Function.Parameters, Strict: t.Function.Strict, Extra: mergeExtra(t.Extra, t.Function.Extra)}
		if t.Type != "function" {
			tool.Type = t.Type
		}
		out = append(out, tool)
	}
	return out
}

// Tools in the OpenAI shape, which has room only for tools defined by schema
func toolsToOAI(tools []Tool) ([]oaiTool, error) {
	var out []oaiTool
	for _, t := range tools {
		if t.Type != "" && t.Type != "custom" {
			return nil, bad("Input tag '%s' found using 'type' does not match any of the expected tags: 'custom'", t.Type)
		}
		tool := oaiTool{Type: "function", Extra: t.Extra}
		tool.Function = oaiFunction{Name: t.Name, Description: t.Description, Parameters: t.Schema, Strict: t.Strict}
		out = append(out, tool)
	}
	return out, nil
}

// Calls in the OpenAI shape
func oaiCalls(calls []ToolCall) []oaiToolCall {
	var out []oaiToolCall
	for _, t := range calls {
		tc := oaiToolCall{ID: t.ID, Type: "function"}
		tc.Function.Name, tc.Function.Arguments = t.Name, t.Args
		out = append(out, tc)
	}
	return out
}

// Reads an error a runtime put in a 200 body or stream chunk, an object, a string, or vLLM's shape
func oaiFailure(data []byte) string {
	var body struct {
		Error   json.RawMessage `json:"error"`
		Object  string          `json:"object"`
		Message string          `json:"message"`
	}
	if json.Unmarshal(data, &body) != nil {
		return ""
	}
	if len(body.Error) > 0 && string(body.Error) != "null" {
		var text string
		if json.Unmarshal(body.Error, &text) == nil && text != "" {
			return text
		}
		if message := errorField(data); message != "" {
			return message
		}
		return strings.TrimSpace(string(body.Error))
	}
	if body.Object == "error" {
		if body.Message != "" {
			return body.Message
		}
		return strings.TrimSpace(string(data))
	}
	return ""
}

func (openai) Render(c *Request) (string, []byte, error) {
	if c.Kind == Count {
		text := promptText(c)
		data, err := json.Marshal(map[string]any{"model": c.Model, "content": text, "prompt": text})
		return openaiTokenize, data, err
	}
	req := oaiRequest{Model: c.Model, MaxTokens: c.MaxTokens, Temperature: c.Temperature, TopP: c.TopP, Stream: c.Stream, Extra: c.Extra}
	if len(c.Stop) > 0 {
		req.Stop, _ = json.Marshal(c.Stop)
	}
	if c.Kind == Embed {
		req.Input, _ = json.Marshal(c.Inputs)
		req.Stream = false
		data, err := json.Marshal(req)
		return OpenAIEmbeddings, data, err
	}
	if c.Stream {
		req.StreamOptions = &struct {
			IncludeUsage bool `json:"include_usage"`
		}{true}
	}
	// Tool turns carry text only, their images following the run of tool turns as a user turn
	var held []Part
	for i, m := range c.Messages {
		msg := oaiMessage{Role: m.Role, ToolCallID: m.ToolID, Extra: m.Extra}
		if m.Role == "tool" {
			msg.Content, _ = json.Marshal(TextOf(m.Parts))
			req.Messages = append(req.Messages, msg)
			for _, p := range m.Parts {
				if p.Type == "image" {
					held = append(held, p)
				}
			}
			if len(held) > 0 && (i+1 == len(c.Messages) || c.Messages[i+1].Role != "tool") {
				req.Messages = append(req.Messages, oaiMessage{Role: "user", Content: oaiContent(held)})
				held = nil
			}
			continue
		}
		msg.Content = oaiContent(m.Parts)
		if m.Role == "assistant" {
			msg.ReasoningContent = thinkingOf(m.Parts)
		}
		msg.ToolCalls = oaiCalls(m.ToolCalls)
		req.Messages = append(req.Messages, msg)
	}
	tools, err := toolsToOAI(c.Tools)
	if err != nil {
		return "", nil, err
	}
	req.Tools = tools
	switch c.ToolChoice {
	case "":
	case "auto", "none", "required":
		req.ToolChoice, _ = json.Marshal(c.ToolChoice)
	default:
		req.ToolChoice, _ = json.Marshal(map[string]any{"type": "function", "function": map[string]any{"name": c.ToolChoice}})
	}
	data, err := json.Marshal(req)
	return OpenAIChat, data, err
}

func oaiStop(reason string) string {
	switch reason {
	case "length":
		return StopLength
	case "tool_calls", "function_call":
		return StopTool
	case "content_filter":
		return StopFilter
	}
	return StopEnd
}

func oaiReason(stop string) string {
	switch stop {
	case StopLength:
		return "length"
	case StopTool:
		return "tool_calls"
	case StopFilter:
		return "content_filter"
	}
	return "stop"
}

func (openai) ParseResult(c *Request, body []byte) (*Result, error) {
	if c.Kind == Count {
		var count struct {
			Count  int               `json:"count"`
			Tokens []json.RawMessage `json:"tokens"`
		}
		if err := json.Unmarshal(body, &count); err != nil {
			return nil, err
		}
		if count.Count == 0 && count.Tokens == nil {
			return nil, bad("the tokenizer answered without a count")
		}
		return &Result{Model: c.Model, Usage: Usage{In: max(count.Count, len(count.Tokens))}}, nil
	}
	if message := oaiFailure(body); message != "" {
		return nil, &Failure{Status: http.StatusBadGateway, Message: message}
	}
	var resp oaiResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	r := &Result{ID: resp.ID, Model: resp.Model, Extra: resp.Extra}
	if resp.Usage != nil {
		r.Usage.In, r.Usage.Out = resp.Usage.PromptTokens, resp.Usage.CompletionTokens
	}
	if resp.Timings != nil {
		r.Usage.DraftOffered, r.Usage.DraftAccepted = resp.Timings.DraftN, resp.Timings.DraftNAccepted
	}
	for _, d := range resp.Data {
		r.Vectors = append(r.Vectors, d.Embedding)
	}
	if len(resp.Choices) > 0 {
		ch := resp.Choices[0]
		r.Text = ch.Text
		if ch.Message != nil {
			json.Unmarshal(ch.Message.Content, &r.Text)
			r.Thinking = ch.Message.reasoning()
			for _, t := range ch.Message.ToolCalls {
				r.ToolCalls = append(r.ToolCalls, ToolCall{ID: t.ID, Name: t.Function.Name, Args: t.Function.Arguments})
			}
		}
		if ch.FinishReason != nil {
			r.Stop = oaiStop(*ch.FinishReason)
		}
	}
	r.Stop = StopOf(r.Stop, len(r.ToolCalls))
	return r, nil
}

// A whole answer in the OpenAI shape
func (openai) renderResult(c *Request, r *Result) ([]byte, error) {
	if c.Kind == Embed {
		data := make([]map[string]any, 0, len(r.Vectors))
		for i, v := range r.Vectors {
			data = append(data, map[string]any{"object": "embedding", "index": i, "embedding": v})
		}
		return withExtra(map[string]any{"object": "list", "data": data, "model": r.Model, "usage": oaiUsage{PromptTokens: r.Usage.In, TotalTokens: r.Usage.In}}, r.Extra)
	}
	resp := oaiResponse{ID: resultID(r, "chatcmpl"), Object: "chat.completion", Created: time.Now().Unix(), Model: r.Model, Usage: &oaiUsage{PromptTokens: r.Usage.In, CompletionTokens: r.Usage.Out, TotalTokens: r.Usage.In + r.Usage.Out}, Extra: r.Extra}
	reason := oaiReason(r.Stop)
	if c.Kind == Generate {
		resp.Object = "text_completion"
		resp.Choices = []oaiChoice{{Text: r.Text, FinishReason: &reason}}
		return json.Marshal(resp)
	}
	msg := &oaiMessage{Role: "assistant", ReasoningContent: r.Thinking, ToolCalls: oaiCalls(r.ToolCalls)}
	msg.Content, _ = json.Marshal(r.Text)
	resp.Choices = []oaiChoice{{Message: msg, FinishReason: &reason}}
	return json.Marshal(resp)
}

func (openai) ParseStream(rd io.Reader, emit func(Event) error) error {
	started := false
	final := &Result{}
	var tools toolGather
	err := ReadSSE(rd, func(_ string, data []byte) error {
		if string(data) == "[DONE]" {
			return nil
		}
		if message := oaiFailure(data); message != "" {
			return errors.New(message)
		}
		var chunk oaiResponse
		if err := json.Unmarshal(data, &chunk); err != nil {
			return err
		}
		final.Extra = mergeExtra(final.Extra, chunk.Extra)
		if !started {
			started = true
			final.ID, final.Model = chunk.ID, chunk.Model
			if err := emit(Event{Kind: Started, Res: &Result{ID: chunk.ID, Model: chunk.Model}}); err != nil {
				return err
			}
		}
		if chunk.Usage != nil {
			final.Usage.In, final.Usage.Out = chunk.Usage.PromptTokens, chunk.Usage.CompletionTokens
		}
		if chunk.Timings != nil {
			final.Usage.DraftOffered, final.Usage.DraftAccepted = chunk.Timings.DraftN, chunk.Timings.DraftNAccepted
		}
		for _, ch := range chunk.Choices {
			if ch.Text != "" {
				if err := emit(Event{Kind: Said, Text: ch.Text}); err != nil {
					return err
				}
			}
			if ch.Delta != nil {
				if reasoning := ch.Delta.reasoning(); reasoning != "" {
					if err := emit(Event{Kind: Thought, Text: reasoning}); err != nil {
						return err
					}
				}
				var text string
				if json.Unmarshal(ch.Delta.Content, &text) == nil && text != "" {
					if err := emit(Event{Kind: Said, Text: text}); err != nil {
						return err
					}
				}
				for i, t := range ch.Delta.ToolCalls {
					idx := i
					if t.Index != nil {
						idx = *t.Index
					}
					ev := Event{Kind: Called, Index: idx, Tool: &ToolCall{ID: t.ID, Name: t.Function.Name, Args: t.Function.Arguments}}
					tools.add(ev)
					if err := emit(ev); err != nil {
						return err
					}
				}
			}
			if ch.FinishReason != nil && *ch.FinishReason != "" {
				final.Stop = oaiStop(*ch.FinishReason)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	final.ToolCalls = tools.list()
	final.Stop = StopOf(final.Stop, len(final.ToolCalls))
	return emit(Event{Kind: Stopped, Res: final})
}

type oaiStream struct {
	live     *liveWriter
	req      *Request
	id       string
	model    string
	created  int64
	tools    toolGather
	sentRole bool
}

func (o openai) Answer(w http.ResponseWriter, req *Request, model string) Writer {
	if !req.Stream {
		return &whole{w: w, req: req, model: model, client: o, render: o.renderResult}
	}
	streamHeaders(w, "text/event-stream")
	return &oaiStream{live: newLiveWriter(w, pingComment), req: req, id: newID("chatcmpl"), model: model, created: time.Now().Unix()}
}

func (s *oaiStream) chunk(delta *oaiMessage, text string, finish *string, usage *oaiUsage, extra map[string]json.RawMessage) error {
	resp := oaiResponse{ID: s.id, Object: "chat.completion.chunk", Created: s.created, Model: s.model, Usage: usage, Extra: extra}
	if s.req.Kind == Generate {
		resp.Object = "text_completion"
		resp.Choices = []oaiChoice{{Text: text, FinishReason: finish}}
	} else {
		if delta == nil {
			delta = &oaiMessage{}
		}
		if !s.sentRole {
			delta.Role, s.sentRole = "assistant", true
		}
		resp.Choices = []oaiChoice{{Delta: delta, FinishReason: finish}}
	}
	return s.live.sse("", resp)
}

func (s *oaiStream) Write(ev Event) error {
	switch ev.Kind {
	case Started:
		if ev.Res != nil && ev.Res.ID != "" {
			s.id = ev.Res.ID
		}
		return nil
	case Said:
		delta := &oaiMessage{}
		delta.Content, _ = json.Marshal(ev.Text)
		return s.chunk(delta, ev.Text, nil, nil, nil)
	case Thought:
		if s.req.Kind == Generate {
			return nil
		}
		return s.chunk(&oaiMessage{ReasoningContent: ev.Text}, "", nil, nil, nil)
	case Called:
		s.tools.add(ev)
		tc := oaiToolCall{Index: ptr(ev.Index), ID: ev.Tool.ID, Type: "function"}
		tc.Function.Name, tc.Function.Arguments = ev.Tool.Name, ev.Tool.Args
		if tc.ID == "" && tc.Function.Name == "" {
			tc.Type = ""
		}
		return s.chunk(&oaiMessage{ToolCalls: []oaiToolCall{tc}}, "", nil, nil, nil)
	case Stopped:
		res := ev.Res
		if res == nil {
			res = &Result{}
		}
		reason := oaiReason(StopOf(res.Stop, len(s.tools.order)))
		return s.chunk(&oaiMessage{}, "", &reason, &oaiUsage{PromptTokens: res.Usage.In, CompletionTokens: res.Usage.Out, TotalTokens: res.Usage.In + res.Usage.Out}, res.Extra)
	case Failed:
		code := ev.Failure.Code
		if code == "" {
			code = CodeUpstream
		}
		return s.live.sse("", map[string]any{"error": map[string]any{"message": ev.Failure.Message, "type": code, "code": code}})
	}
	return nil
}

func (s *oaiStream) Close() error {
	err := s.live.write(func(w http.ResponseWriter) error {
		_, err := io.WriteString(w, "data: [DONE]\n\n")
		flush(w)
		return err
	})
	s.live.close()
	return err
}

func (openai) Traits() Traits { return Traits{} }

func (openai) ErrorMessage(body []byte) string { return errorField(body) }

func (openai) Error(w http.ResponseWriter, f Failure) {
	WriteJSON(w, f.Status, map[string]any{"error": map[string]any{"message": f.Message, "type": f.Code, "code": f.Code}})
}
