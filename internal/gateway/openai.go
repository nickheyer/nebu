package gateway

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

const (
	openaiChat        = "/v1/chat/completions"
	openaiCompletions = "/v1/completions"
	openaiEmbeddings  = "/v1/embeddings"
	// Tokenizer endpoint shared by llama.cpp, vLLM, and SGLang.
	openaiTokenize = "/tokenize"
)

// OpenAI protocol adapter
type openai struct{}

type oaiMessage struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content,omitempty"`
	Name       string          `json:"name,omitempty"`
	ToolCalls  []oaiToolCall   `json:"tool_calls,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
	// Reasoning in assistant turns, as llama.cpp and vLLM name it.
	ReasoningContent string                     `json:"reasoning_content,omitempty"`
	Extra            map[string]json.RawMessage `json:"-"`
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

// A content part: text or an image URL
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

// Writes the field its type calls for, so a text part always carries text.
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

func (openai) ParseRequest(path string, body []byte) (*Chat, error) {
	var req oaiRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, bad("%v", err)
	}
	c := &Chat{Kind: "chat", Format: v1.ApiFlavor_API_FLAVOR_OPENAI, Model: req.Model, MaxTokens: max(req.MaxTokens, req.MaxCompletionTokens), Temperature: req.Temperature, TopP: req.TopP, Stop: strs(req.Stop), Stream: req.Stream, Extra: req.Extra}
	switch path {
	case openaiEmbeddings:
		c.Kind, c.Inputs = "embed", strs(req.Input)
		return c, nil
	case openaiCompletions:
		c.Kind = "generate"
		c.Messages = []Message{{Role: "user", Parts: []Part{{Type: "text", Text: strings.Join(strs(req.Prompt), "")}}}}
		return c, nil
	}
	for _, m := range req.Messages {
		msg := Message{Role: m.Role, ToolID: m.ToolCallID, Extra: m.Extra}
		if m.ReasoningContent != "" {
			if m.Role != "assistant" {
				return nil, bad("reasoning_content belongs in assistant turns, not %s turns", m.Role)
			}
			msg.Parts = append(msg.Parts, Part{Type: "thinking", Text: m.ReasoningContent})
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

// Reads message content as a string or a list of text and image parts. Any
// other part type is refused by name.
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
			if mt, data, ok := dataURL(part.URL); ok {
				part.MediaType, part.Data, part.URL = mt, data, ""
			}
			out = append(out, part)
		default:
			return nil, bad("%s parts are not content the gateway can pass to a model", p.Type)
		}
	}
	return out, nil
}

// Renders text and image parts: a bare string for one plain text part, otherwise a part list.
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

// Reads an error a runtime put in a 200 body or a stream chunk: an error object
// with a message, a bare error string, or vLLM's object of type error.
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

func (openai) RenderRequest(c *Chat) (string, []byte, error) {
	if c.Kind == "count" {
		// Send both server-specific field names. Servers ignore the other field.
		text := promptText(c)
		data, err := json.Marshal(map[string]any{"model": c.Model, "content": text, "prompt": text})
		return openaiTokenize, data, err
	}
	// OpenAI-format runtimes skip fields they dont know
	req := oaiRequest{Model: c.Model, MaxTokens: c.MaxTokens, Temperature: c.Temperature, TopP: c.TopP, Stream: c.Stream, Extra: c.Extra}
	if len(c.Stop) > 0 {
		req.Stop, _ = json.Marshal(c.Stop)
	}
	if c.Kind == "embed" {
		req.Input, _ = json.Marshal(c.Inputs)
		req.Stream = false
		data, err := json.Marshal(req)
		return openaiEmbeddings, data, err
	}
	if c.Stream {
		req.StreamOptions = &struct {
			IncludeUsage bool `json:"include_usage"`
		}{true}
	}
	// Tool turns carry text only. Their images follow the run of tool turns as a user turn.
	var held []Part
	for i, m := range c.Messages {
		msg := oaiMessage{Role: m.Role, ToolCallID: m.ToolID, Extra: m.Extra}
		if m.Role == "tool" {
			msg.Content, _ = json.Marshal(textOf(m.Parts))
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
	return openaiChat, data, err
}

func oaiStop(reason string) string {
	switch reason {
	case "length":
		return "length"
	case "tool_calls", "function_call":
		return "tool"
	case "content_filter":
		return "filter"
	}
	return "stop"
}

func oaiReason(stop string) string {
	switch stop {
	case "length":
		return "length"
	case "tool":
		return "tool_calls"
	case "filter":
		return "content_filter"
	}
	return "stop"
}

func (openai) ParseResult(c *Chat, body []byte) (*Result, error) {
	if c.Kind == "count" {
		// Use the count field or token list length.
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
		return &Result{Model: c.Model, In: max(count.Count, len(count.Tokens))}, nil
	}
	if message := oaiFailure(body); message != "" {
		return nil, &upstreamRefusal{status: http.StatusBadGateway, message: message}
	}
	var resp oaiResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	r := &Result{ID: resp.ID, Model: resp.Model, Extra: resp.Extra}
	if resp.Usage != nil {
		r.In, r.Out = resp.Usage.PromptTokens, resp.Usage.CompletionTokens
	}
	if resp.Timings != nil {
		r.DraftOffered, r.DraftAccepted = resp.Timings.DraftN, resp.Timings.DraftNAccepted
	}
	for _, d := range resp.Data {
		r.Vectors = append(r.Vectors, d.Embedding)
	}
	if len(resp.Choices) > 0 {
		ch := resp.Choices[0]
		r.Text = ch.Text
		if ch.Message != nil {
			json.Unmarshal(ch.Message.Content, &r.Text)
			for _, t := range ch.Message.ToolCalls {
				r.ToolCalls = append(r.ToolCalls, ToolCall{ID: t.ID, Name: t.Function.Name, Args: t.Function.Arguments})
			}
		}
		if ch.FinishReason != nil {
			r.Stop = oaiStop(*ch.FinishReason)
		}
	}
	r.Stop = stopOf(r.Stop, len(r.ToolCalls))
	return r, nil
}

func (openai) RenderResult(c *Chat, r *Result) ([]byte, error) {
	if c.Kind == "embed" {
		data := make([]map[string]any, 0, len(r.Vectors))
		for i, v := range r.Vectors {
			data = append(data, map[string]any{"object": "embedding", "index": i, "embedding": v})
		}
		return withExtra(map[string]any{"object": "list", "data": data, "model": r.Model, "usage": oaiUsage{PromptTokens: r.In, TotalTokens: r.In}}, r.Extra)
	}
	resp := oaiResponse{ID: resultID(r, "chatcmpl"), Object: "chat.completion", Created: time.Now().Unix(), Model: r.Model, Usage: &oaiUsage{PromptTokens: r.In, CompletionTokens: r.Out, TotalTokens: r.In + r.Out}, Extra: r.Extra}
	reason := oaiReason(r.Stop)
	if c.Kind == "generate" {
		resp.Object = "text_completion"
		resp.Choices = []oaiChoice{{Text: r.Text, FinishReason: &reason}}
		return json.Marshal(resp)
	}
	msg := &oaiMessage{Role: "assistant", ToolCalls: oaiCalls(r.ToolCalls)}
	msg.Content, _ = json.Marshal(r.Text)
	resp.Choices = []oaiChoice{{Message: msg, FinishReason: &reason}}
	return json.Marshal(resp)
}

func (openai) ParseStream(rd io.Reader, emit func(Event) error) error {
	started := false
	final := &Result{}
	var tools toolGather
	err := readSSE(rd, func(_ string, data []byte) error {
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
			if err := emit(Event{Kind: "start", Res: &Result{ID: chunk.ID, Model: chunk.Model}}); err != nil {
				return err
			}
		}
		if chunk.Usage != nil {
			final.In, final.Out = chunk.Usage.PromptTokens, chunk.Usage.CompletionTokens
		}
		if chunk.Timings != nil {
			final.DraftOffered, final.DraftAccepted = chunk.Timings.DraftN, chunk.Timings.DraftNAccepted
		}
		for _, ch := range chunk.Choices {
			if ch.Text != "" {
				if err := emit(Event{Kind: "text", Text: ch.Text}); err != nil {
					return err
				}
			}
			if ch.Delta != nil {
				var text string
				if json.Unmarshal(ch.Delta.Content, &text) == nil && text != "" {
					if err := emit(Event{Kind: "text", Text: text}); err != nil {
						return err
					}
				}
				for i, t := range ch.Delta.ToolCalls {
					idx := i
					if t.Index != nil {
						idx = *t.Index
					}
					ev := Event{Kind: "tool", Index: idx, Tool: &ToolCall{ID: t.ID, Name: t.Function.Name, Args: t.Function.Arguments}}
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
	final.Stop = stopOf(final.Stop, len(final.ToolCalls))
	return emit(Event{Kind: "stop", Res: final})
}

type oaiStream struct {
	live     *liveWriter
	c        *Chat
	id       string
	model    string
	created  int64
	tools    toolGather
	sentRole bool
}

// Opens the stream. Comment lines keep it alive while the model works.
func (openai) Stream(w http.ResponseWriter, c *Chat) StreamWriter {
	streamHeaders(w, "text/event-stream")
	return &oaiStream{live: newLiveWriter(w, pingComment), c: c, id: newID("chatcmpl"), model: c.Model, created: time.Now().Unix()}
}

func (s *oaiStream) chunk(delta *oaiMessage, text string, finish *string, usage *oaiUsage, extra map[string]json.RawMessage) error {
	resp := oaiResponse{ID: s.id, Object: "chat.completion.chunk", Created: s.created, Model: s.model, Usage: usage, Extra: extra}
	if s.c.Kind == "generate" {
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
	case "start":
		if ev.Res != nil && ev.Res.ID != "" {
			s.id = ev.Res.ID
		}
		return nil
	case "text":
		delta := &oaiMessage{}
		delta.Content, _ = json.Marshal(ev.Text)
		return s.chunk(delta, ev.Text, nil, nil, nil)
	case "tool":
		s.tools.add(ev)
		tc := oaiToolCall{Index: ptr(ev.Index), ID: ev.Tool.ID, Type: "function"}
		tc.Function.Name, tc.Function.Arguments = ev.Tool.Name, ev.Tool.Args
		if tc.ID == "" && tc.Function.Name == "" {
			tc.Type = ""
		}
		return s.chunk(&oaiMessage{ToolCalls: []oaiToolCall{tc}}, "", nil, nil, nil)
	case "stop":
		res := ev.Res
		if res == nil {
			res = &Result{}
		}
		reason := oaiReason(stopOf(res.Stop, len(s.tools.order)))
		// The closing chunk carries every top-level key the runtime added.
		if err := s.chunk(&oaiMessage{}, "", &reason, &oaiUsage{PromptTokens: res.In, CompletionTokens: res.Out, TotalTokens: res.In + res.Out}, res.Extra); err != nil {
			return err
		}
	case "error":
		return s.live.sse("", map[string]any{"error": map[string]any{"message": ev.Text, "type": "upstream_error", "code": "upstream_error"}})
	}
	return nil
}

func (openai) InlineImages() bool { return false }

// Add image tokens to the text-only tokenizer count.
func (openai) CountsImages() bool { return false }

func (s *oaiStream) Close() error {
	err := s.live.write(func(w http.ResponseWriter) error {
		_, err := io.WriteString(w, "data: [DONE]\n\n")
		flush(w)
		return err
	})
	s.live.close()
	return err
}

func (openai) ErrorMessage(body []byte) string { return errorField(body) }

func (openai) Error(w http.ResponseWriter, status int, message, kind string) {
	writeJSON(w, status, map[string]any{"error": map[string]any{"message": message, "type": kind, "code": kind}})
}
