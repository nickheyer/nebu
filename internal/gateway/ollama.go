package gateway

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"time"
)

const (
	ollamaPrefix     = "/api/"
	ollamaChat       = "/api/chat"
	ollamaGenerate   = "/api/generate"
	ollamaEmbed      = "/api/embed"
	ollamaEmbeddings = "/api/embeddings"
	ollamaTags       = "/api/tags"
	ollamaPs         = "/api/ps"
	ollamaShow       = "/api/show"
	ollamaVersion    = "/api/version"
)

// The Ollama wire format, newline delimited JSON when streaming
type ollama struct{}

type olMessage struct {
	Role      string       `json:"role"`
	Content   string       `json:"content"`
	Images    []string     `json:"images,omitempty"`
	ToolCalls []olToolCall `json:"tool_calls,omitempty"`
}

type olToolCall struct {
	Function struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	} `json:"function"`
}

type olOptions struct {
	Temperature *float64        `json:"temperature,omitempty"`
	TopP        *float64        `json:"top_p,omitempty"`
	TopK        *int            `json:"top_k,omitempty"`
	NumPredict  int             `json:"num_predict,omitempty"`
	Stop        json.RawMessage `json:"stop,omitempty"`
}

type olRequest struct {
	Model    string          `json:"model"`
	Messages []olMessage     `json:"messages,omitempty"`
	Prompt   string          `json:"prompt,omitempty"`
	System   string          `json:"system,omitempty"`
	Images   []string        `json:"images,omitempty"`
	Input    json.RawMessage `json:"input,omitempty"`
	Stream   *bool           `json:"stream,omitempty"`
	Options  *olOptions      `json:"options,omitempty"`
	Tools    []oaiTool       `json:"tools,omitempty"`
}

type olResponse struct {
	Model           string      `json:"model"`
	CreatedAt       string      `json:"created_at"`
	Message         *olMessage  `json:"message,omitempty"`
	Response        *string     `json:"response,omitempty"`
	Done            bool        `json:"done"`
	DoneReason      string      `json:"done_reason,omitempty"`
	PromptEvalCount int         `json:"prompt_eval_count,omitempty"`
	EvalCount       int         `json:"eval_count,omitempty"`
	Embeddings      [][]float64 `json:"embeddings,omitempty"`
	Embedding       []float64   `json:"embedding,omitempty"`
}

func (ollama) ParseRequest(path string, body []byte) (*Chat, error) {
	var req olRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, bad("%v", err)
	}
	// Ollama streams unless told not to
	c := &Chat{Kind: "chat", Model: req.Model, Stream: req.Stream == nil || *req.Stream}
	if o := req.Options; o != nil {
		c.Temperature, c.TopP, c.TopK, c.MaxTokens, c.Stop = o.Temperature, o.TopP, o.TopK, o.NumPredict, strs(o.Stop)
	}
	images := func(data []string) []Part {
		var out []Part
		for _, d := range data {
			part := Part{Type: "image", Data: d}
			if mt, raw, ok := dataURL(d); ok {
				part.MediaType, part.Data = mt, raw
			} else {
				part.MediaType = mediaTypeOfBase64(d)
			}
			out = append(out, part)
		}
		return out
	}
	switch path {
	case ollamaEmbed, ollamaEmbeddings:
		c.Kind, c.Stream = "embed", false
		c.Inputs = strs(req.Input)
		if len(c.Inputs) == 0 && req.Prompt != "" {
			c.Inputs = []string{req.Prompt}
		}
		return c, nil
	case ollamaGenerate:
		c.Kind = "generate"
		if req.System != "" {
			c.Messages = append(c.Messages, Message{Role: "system", Parts: []Part{{Type: "text", Text: req.System}}})
		}
		c.Messages = append(c.Messages, Message{Role: "user", Parts: append([]Part{{Type: "text", Text: req.Prompt}}, images(req.Images)...)})
		return c, nil
	case ollamaChat:
	default:
		return nil, bad("%s is not an endpoint the ollama flavor serves", path)
	}
	// Ollama names no call ids, so tool turns answer the last assistant's calls in order
	var pending []string
	for _, m := range req.Messages {
		msg := Message{Role: m.Role}
		if m.Content != "" {
			msg.Parts = append(msg.Parts, Part{Type: "text", Text: m.Content})
		}
		msg.Parts = append(msg.Parts, images(m.Images)...)
		if len(m.ToolCalls) > 0 {
			pending = pending[:0]
		}
		for i, t := range m.ToolCalls {
			id := newID("call") + "-" + string(rune('a'+i))
			pending = append(pending, id)
			msg.ToolCalls = append(msg.ToolCalls, ToolCall{ID: id, Name: t.Function.Name, Args: string(t.Function.Arguments)})
		}
		if m.Role == "tool" && len(pending) > 0 {
			msg.ToolID, pending = pending[0], pending[1:]
		}
		c.Messages = append(c.Messages, msg)
	}
	for _, t := range req.Tools {
		c.Tools = append(c.Tools, Tool{Name: t.Function.Name, Description: t.Function.Description, Schema: t.Function.Parameters})
	}
	return c, nil
}

func (ollama) RenderRequest(c *Chat) (string, []byte, error) {
	if c.Kind == "count" {
		return "", nil, bad("the ollama flavor has no token count endpoint")
	}
	req := olRequest{Model: c.Model, Stream: ptr(c.Stream)}
	if c.Temperature != nil || c.TopP != nil || c.TopK != nil || c.MaxTokens > 0 || len(c.Stop) > 0 {
		req.Options = &olOptions{Temperature: c.Temperature, TopP: c.TopP, TopK: c.TopK, NumPredict: c.MaxTokens}
		if len(c.Stop) > 0 {
			req.Options.Stop, _ = json.Marshal(c.Stop)
		}
	}
	if c.Kind == "embed" {
		req.Stream = nil
		req.Input, _ = json.Marshal(c.Inputs)
		data, err := json.Marshal(req)
		return ollamaEmbed, data, err
	}
	for _, m := range c.Messages {
		msg := olMessage{Role: m.Role, Content: textOf(m.Parts)}
		for _, p := range m.Parts {
			if p.Type == "image" && p.Data != "" {
				msg.Images = append(msg.Images, p.Data)
			}
		}
		for _, t := range m.ToolCalls {
			tc := olToolCall{}
			tc.Function.Name = t.Name
			tc.Function.Arguments = json.RawMessage(t.Args)
			if !json.Valid(tc.Function.Arguments) {
				tc.Function.Arguments = json.RawMessage("{}")
			}
			msg.ToolCalls = append(msg.ToolCalls, tc)
		}
		req.Messages = append(req.Messages, msg)
	}
	for _, t := range c.Tools {
		tool := oaiTool{Type: "function"}
		tool.Function.Name, tool.Function.Description, tool.Function.Parameters = t.Name, t.Description, t.Schema
		req.Tools = append(req.Tools, tool)
	}
	data, err := json.Marshal(req)
	return ollamaChat, data, err
}

func olStop(reason string) string {
	if reason == "length" {
		return "length"
	}
	return "stop"
}

func olReason(stop string) string {
	if stop == "length" {
		return "length"
	}
	return "stop"
}

func olCalls(calls []olToolCall) []ToolCall {
	var out []ToolCall
	for _, t := range calls {
		out = append(out, ToolCall{ID: newID("call"), Name: t.Function.Name, Args: string(t.Function.Arguments)})
	}
	return out
}

func (ollama) ParseResult(c *Chat, body []byte) (*Result, error) {
	var resp olResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	r := &Result{Model: resp.Model, In: resp.PromptEvalCount, Out: resp.EvalCount, Vectors: resp.Embeddings, Stop: olStop(resp.DoneReason)}
	if len(resp.Embedding) > 0 {
		r.Vectors = [][]float64{resp.Embedding}
	}
	if resp.Message != nil {
		r.Text = resp.Message.Content
		r.ToolCalls = olCalls(resp.Message.ToolCalls)
	} else if resp.Response != nil {
		r.Text = *resp.Response
	}
	r.Stop = stopOf(r.Stop, len(r.ToolCalls))
	return r, nil
}

func olNow() string { return time.Now().UTC().Format(time.RFC3339Nano) }

func olToolCalls(calls []ToolCall) []olToolCall {
	var out []olToolCall
	for _, t := range calls {
		tc := olToolCall{}
		tc.Function.Name = t.Name
		tc.Function.Arguments = json.RawMessage(t.Args)
		if !json.Valid(tc.Function.Arguments) {
			tc.Function.Arguments = json.RawMessage("{}")
		}
		out = append(out, tc)
	}
	return out
}

func (ollama) RenderResult(c *Chat, r *Result) ([]byte, error) {
	if c.Kind == "embed" {
		resp := olResponse{Model: r.Model, Embeddings: r.Vectors, PromptEvalCount: r.In}
		if len(r.Vectors) > 0 {
			resp.Embedding = r.Vectors[0]
		}
		return json.Marshal(resp)
	}
	resp := olResponse{Model: r.Model, CreatedAt: olNow(), Done: true, DoneReason: olReason(r.Stop), PromptEvalCount: r.In, EvalCount: r.Out}
	if c.Kind == "generate" {
		resp.Response = ptr(r.Text)
	} else {
		resp.Message = &olMessage{Role: "assistant", Content: r.Text, ToolCalls: olToolCalls(r.ToolCalls)}
	}
	return json.Marshal(resp)
}

func (ollama) ParseStream(rd io.Reader, emit func(Event) error) error {
	sc := bufio.NewScanner(rd)
	sc.Buffer(make([]byte, 64<<10), 16<<20)
	final := &Result{}
	started := false
	index := 0
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var chunk olResponse
		if err := json.Unmarshal(line, &chunk); err != nil {
			return err
		}
		if !started {
			started = true
			final.Model = chunk.Model
			if err := emit(Event{Kind: "start", Res: &Result{Model: chunk.Model}}); err != nil {
				return err
			}
		}
		text := ""
		if chunk.Message != nil {
			text = chunk.Message.Content
			for _, t := range olCalls(chunk.Message.ToolCalls) {
				call := t
				final.ToolCalls = append(final.ToolCalls, call)
				if err := emit(Event{Kind: "tool", Index: index, Tool: &call}); err != nil {
					return err
				}
				index++
			}
		} else if chunk.Response != nil {
			text = *chunk.Response
		}
		if text != "" {
			if err := emit(Event{Kind: "text", Text: text}); err != nil {
				return err
			}
		}
		if chunk.Done {
			final.In, final.Out, final.Stop = chunk.PromptEvalCount, chunk.EvalCount, olStop(chunk.DoneReason)
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	final.Stop = stopOf(final.Stop, len(final.ToolCalls))
	return emit(Event{Kind: "stop", Res: final})
}

type olStream struct {
	w     http.ResponseWriter
	c     *Chat
	model string
	tools toolGather
	// Tool fragments finish when the next event moves on, calls go out whole
	open *int
}

func (ollama) Stream(w http.ResponseWriter, c *Chat) StreamWriter {
	streamHeaders(w, "application/x-ndjson")
	return &olStream{w: w, c: c, model: c.Model}
}

func (s *olStream) line(resp olResponse) error {
	resp.Model, resp.CreatedAt = s.model, olNow()
	data, err := json.Marshal(resp)
	if err != nil {
		return err
	}
	if _, err := s.w.Write(append(data, '\n')); err != nil {
		return err
	}
	flush(s.w)
	return nil
}

// Sends the tool call gathered so far when a fragment run ended
func (s *olStream) flushTool() error {
	if s.open == nil {
		return nil
	}
	call := *s.tools.calls[*s.open]
	if call.Args == "" {
		call.Args = "{}"
	}
	s.open = nil
	return s.line(olResponse{Message: &olMessage{Role: "assistant", ToolCalls: olToolCalls([]ToolCall{call})}})
}

func (s *olStream) Write(ev Event) error {
	switch ev.Kind {
	case "start":
		if ev.Res != nil && ev.Res.Model != "" {
			s.model = ev.Res.Model
		}
	case "text":
		if err := s.flushTool(); err != nil {
			return err
		}
		if s.c.Kind == "generate" {
			return s.line(olResponse{Response: ptr(ev.Text)})
		}
		return s.line(olResponse{Message: &olMessage{Role: "assistant", Content: ev.Text}})
	case "tool":
		if s.open != nil && *s.open != ev.Index {
			if err := s.flushTool(); err != nil {
				return err
			}
		}
		s.tools.add(ev)
		s.open = ptr(ev.Index)
	case "stop":
		if err := s.flushTool(); err != nil {
			return err
		}
		res := ev.Res
		if res == nil {
			res = &Result{}
		}
		resp := olResponse{Done: true, DoneReason: olReason(res.Stop), PromptEvalCount: res.In, EvalCount: res.Out}
		if s.c.Kind == "generate" {
			resp.Response = ptr("")
		} else {
			resp.Message = &olMessage{Role: "assistant"}
		}
		return s.line(resp)
	case "error":
		data, err := json.Marshal(map[string]any{"error": ev.Text, "model": s.model, "created_at": olNow(), "done": true})
		if err != nil {
			return err
		}
		_, err = s.w.Write(append(data, '\n'))
		flush(s.w)
		return err
	}
	return nil
}

func (s *olStream) Close() error { return nil }

func (ollama) InlineImages() bool { return true }

func (ollama) ErrorMessage(body []byte) string {
	var e struct {
		Error string `json:"error"`
	}
	json.Unmarshal(body, &e)
	return e.Error
}

func (ollama) Error(w http.ResponseWriter, status int, message, kind string) {
	writeJSON(w, status, map[string]any{"error": message})
}
