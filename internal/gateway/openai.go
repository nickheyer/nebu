package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	openaiChat        = "/v1/chat/completions"
	openaiCompletions = "/v1/completions"
	openaiEmbeddings  = "/v1/embeddings"
	// The tokenizer llama.cpp, vLLM, and SGLang expose beside their OpenAI routes
	openaiTokenize = "/tokenize"
)

// The OpenAI wire format, what every shipped runtime speaks
type openai struct{}

type oaiMessage struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content,omitempty"`
	Name       string          `json:"name,omitempty"`
	ToolCalls  []oaiToolCall   `json:"tool_calls,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
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
	Tools      []oaiTool       `json:"tools,omitempty"`
	ToolChoice json.RawMessage `json:"tool_choice,omitempty"`
}

type oaiTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description,omitempty"`
		Parameters  json.RawMessage `json:"parameters,omitempty"`
	} `json:"function"`
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

type oaiResponse struct {
	ID      string      `json:"id"`
	Object  string      `json:"object"`
	Created int64       `json:"created"`
	Model   string      `json:"model"`
	Choices []oaiChoice `json:"choices"`
	Usage   *oaiUsage   `json:"usage,omitempty"`
	Data    []struct {
		Embedding []float64 `json:"embedding"`
	} `json:"data,omitempty"`
}

func (openai) ParseRequest(path string, body []byte) (*Chat, error) {
	var req oaiRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, bad("%v", err)
	}
	c := &Chat{Kind: "chat", Model: req.Model, MaxTokens: max(req.MaxTokens, req.MaxCompletionTokens), Temperature: req.Temperature, TopP: req.TopP, Stop: strs(req.Stop), Stream: req.Stream}
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
		msg := Message{Role: m.Role, ToolID: m.ToolCallID}
		var text string
		if json.Unmarshal(m.Content, &text) == nil {
			if text != "" {
				msg.Parts = []Part{{Type: "text", Text: text}}
			}
		} else {
			var parts []struct {
				Type     string `json:"type"`
				Text     string `json:"text"`
				ImageURL struct {
					URL string `json:"url"`
				} `json:"image_url"`
			}
			json.Unmarshal(m.Content, &parts)
			for _, p := range parts {
				switch p.Type {
				case "text":
					msg.Parts = append(msg.Parts, Part{Type: "text", Text: p.Text})
				case "image_url":
					part := Part{Type: "image", URL: p.ImageURL.URL}
					if mt, data, ok := dataURL(p.ImageURL.URL); ok {
						part.MediaType, part.Data, part.URL = mt, data, ""
					}
					msg.Parts = append(msg.Parts, part)
				}
			}
		}
		for _, t := range m.ToolCalls {
			msg.ToolCalls = append(msg.ToolCalls, ToolCall{ID: t.ID, Name: t.Function.Name, Args: t.Function.Arguments})
		}
		c.Messages = append(c.Messages, msg)
	}
	for _, t := range req.Tools {
		c.Tools = append(c.Tools, Tool{Name: t.Function.Name, Description: t.Function.Description, Schema: t.Function.Parameters})
	}
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

func (openai) RenderRequest(c *Chat) (string, []byte, error) {
	if c.Kind == "count" {
		// Both field names, one per server, each ignores the other's
		text := promptText(c)
		data, err := json.Marshal(map[string]any{"model": c.Model, "content": text, "prompt": text})
		return openaiTokenize, data, err
	}
	req := oaiRequest{Model: c.Model, MaxTokens: c.MaxTokens, Temperature: c.Temperature, TopP: c.TopP, Stream: c.Stream}
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
	for _, m := range c.Messages {
		msg := oaiMessage{Role: m.Role, ToolCallID: m.ToolID}
		if m.Role == "tool" || len(m.Parts) == 1 && m.Parts[0].Type == "text" || len(m.Parts) == 0 {
			msg.Content, _ = json.Marshal(textOf(m.Parts))
		} else {
			var parts []map[string]any
			for _, p := range m.Parts {
				if p.Type == "image" {
					url := p.URL
					if url == "" {
						url = "data:" + p.MediaType + ";base64," + p.Data
					}
					parts = append(parts, map[string]any{"type": "image_url", "image_url": map[string]any{"url": url}})
				} else {
					parts = append(parts, map[string]any{"type": "text", "text": p.Text})
				}
			}
			msg.Content, _ = json.Marshal(parts)
		}
		for _, t := range m.ToolCalls {
			tc := oaiToolCall{ID: t.ID, Type: "function"}
			tc.Function.Name, tc.Function.Arguments = t.Name, t.Args
			msg.ToolCalls = append(msg.ToolCalls, tc)
		}
		req.Messages = append(req.Messages, msg)
	}
	for _, t := range c.Tools {
		tool := oaiTool{Type: "function"}
		tool.Function.Name, tool.Function.Description, tool.Function.Parameters = t.Name, t.Description, t.Schema
		req.Tools = append(req.Tools, tool)
	}
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
		// A count field, or the token list's length when the server gives only that
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
	var resp oaiResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	r := &Result{ID: resp.ID, Model: resp.Model}
	if resp.Usage != nil {
		r.In, r.Out = resp.Usage.PromptTokens, resp.Usage.CompletionTokens
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
		return json.Marshal(map[string]any{"object": "list", "data": data, "model": r.Model, "usage": oaiUsage{PromptTokens: r.In, TotalTokens: r.In}})
	}
	id := r.ID
	if id == "" {
		id = newID("chatcmpl")
	}
	resp := oaiResponse{ID: id, Object: "chat.completion", Created: time.Now().Unix(), Model: r.Model, Usage: &oaiUsage{PromptTokens: r.In, CompletionTokens: r.Out, TotalTokens: r.In + r.Out}}
	reason := oaiReason(r.Stop)
	if c.Kind == "generate" {
		resp.Object = "text_completion"
		resp.Choices = []oaiChoice{{Text: r.Text, FinishReason: &reason}}
		return json.Marshal(resp)
	}
	msg := &oaiMessage{Role: "assistant"}
	msg.Content, _ = json.Marshal(r.Text)
	for _, t := range r.ToolCalls {
		tc := oaiToolCall{ID: t.ID, Type: "function"}
		tc.Function.Name, tc.Function.Arguments = t.Name, t.Args
		msg.ToolCalls = append(msg.ToolCalls, tc)
	}
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
		var chunk oaiResponse
		if err := json.Unmarshal(data, &chunk); err != nil {
			return err
		}
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
	w        http.ResponseWriter
	c        *Chat
	id       string
	model    string
	created  int64
	tools    toolGather
	sentRole bool
}

func (openai) Stream(w http.ResponseWriter, c *Chat) StreamWriter {
	streamHeaders(w, "text/event-stream")
	return &oaiStream{w: w, c: c, id: newID("chatcmpl"), model: c.Model, created: time.Now().Unix()}
}

func (s *oaiStream) chunk(delta *oaiMessage, text string, finish *string, usage *oaiUsage) error {
	resp := oaiResponse{ID: s.id, Object: "chat.completion.chunk", Created: s.created, Model: s.model, Usage: usage}
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
	return writeSSE(s.w, "", resp)
}

func (s *oaiStream) Write(ev Event) error {
	switch ev.Kind {
	case "start":
		if ev.Res != nil && ev.Res.Model != "" {
			s.model = ev.Res.Model
		}
		if ev.Res != nil && ev.Res.ID != "" {
			s.id = ev.Res.ID
		}
		return nil
	case "text":
		delta := &oaiMessage{}
		delta.Content, _ = json.Marshal(ev.Text)
		return s.chunk(delta, ev.Text, nil, nil)
	case "tool":
		s.tools.add(ev)
		tc := oaiToolCall{Index: ptr(ev.Index), ID: ev.Tool.ID, Type: "function"}
		tc.Function.Name, tc.Function.Arguments = ev.Tool.Name, ev.Tool.Args
		if tc.ID == "" && tc.Function.Name == "" {
			tc.Type = ""
		}
		return s.chunk(&oaiMessage{ToolCalls: []oaiToolCall{tc}}, "", nil, nil)
	case "stop":
		res := ev.Res
		if res == nil {
			res = &Result{}
		}
		reason := oaiReason(stopOf(res.Stop, len(s.tools.order)))
		if err := s.chunk(&oaiMessage{}, "", &reason, &oaiUsage{PromptTokens: res.In, CompletionTokens: res.Out, TotalTokens: res.In + res.Out}); err != nil {
			return err
		}
	}
	return nil
}

func (s *oaiStream) Close() error {
	_, err := io.WriteString(s.w, "data: [DONE]\n\n")
	flush(s.w)
	return err
}

func (openai) ErrorMessage(body []byte) string {
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	json.Unmarshal(body, &e)
	return e.Error.Message
}

func (openai) Error(w http.ResponseWriter, status int, message, kind string) {
	writeJSON(w, status, map[string]any{"error": map[string]any{"message": message, "type": kind, "code": kind}})
}
