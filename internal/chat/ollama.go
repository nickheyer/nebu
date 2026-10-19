package chat

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"
)

// Ollama endpoints, the listing ones served by the gateway itself
const (
	OllamaPrefix     = "/api/"
	OllamaChat       = "/api/chat"
	OllamaGenerate   = "/api/generate"
	OllamaEmbed      = "/api/embed"
	OllamaEmbeddings = "/api/embeddings"
	OllamaTags       = "/api/tags"
	OllamaPs         = "/api/ps"
	OllamaShow       = "/api/show"
	OllamaVersion    = "/api/version"
	ollamaPrefix     = OllamaPrefix
)

// The Ollama protocol
type ollama struct{}

type olMessage struct {
	Role      string                     `json:"role"`
	Content   string                     `json:"content"`
	Thinking  string                     `json:"thinking,omitempty"`
	Images    []string                   `json:"images,omitempty"`
	ToolCalls []olToolCall               `json:"tool_calls,omitempty"`
	Extra     map[string]json.RawMessage `json:"-"`
}

func (m *olMessage) UnmarshalJSON(data []byte) error {
	type plain olMessage
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	*m = olMessage(p)
	m.Extra = extraFields(data, plain{})
	return nil
}

func (m olMessage) MarshalJSON() ([]byte, error) {
	type plain olMessage
	return withExtra(plain(m), m.Extra)
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
	Model    string                     `json:"model"`
	Messages []olMessage                `json:"messages,omitempty"`
	Prompt   string                     `json:"prompt,omitempty"`
	System   string                     `json:"system,omitempty"`
	Images   []string                   `json:"images,omitempty"`
	Input    json.RawMessage            `json:"input,omitempty"`
	Stream   *bool                      `json:"stream,omitempty"`
	Options  *olOptions                 `json:"options,omitempty"`
	Tools    []oaiTool                  `json:"tools,omitempty"`
	Extra    map[string]json.RawMessage `json:"-"`
}

func (r *olRequest) UnmarshalJSON(data []byte) error {
	type plain olRequest
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	*r = olRequest(p)
	r.Extra = extraFields(data, plain{})
	return nil
}

func (r olRequest) MarshalJSON() ([]byte, error) {
	type plain olRequest
	return withExtra(plain(r), r.Extra)
}

type olResponse struct {
	Model           string                     `json:"model"`
	CreatedAt       string                     `json:"created_at"`
	Message         *olMessage                 `json:"message,omitempty"`
	Response        *string                    `json:"response,omitempty"`
	Done            bool                       `json:"done"`
	DoneReason      string                     `json:"done_reason,omitempty"`
	PromptEvalCount int                        `json:"prompt_eval_count,omitempty"`
	EvalCount       int                        `json:"eval_count,omitempty"`
	Embeddings      [][]float64                `json:"embeddings,omitempty"`
	Embedding       []float64                  `json:"embedding,omitempty"`
	Error           string                     `json:"error,omitempty"`
	Extra           map[string]json.RawMessage `json:"-"`
}

func (r *olResponse) UnmarshalJSON(data []byte) error {
	type plain olResponse
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	*r = olResponse(p)
	r.Extra = extraFields(data, plain{})
	return nil
}

func (r olResponse) MarshalJSON() ([]byte, error) {
	type plain olResponse
	return withExtra(plain(r), r.Extra)
}

func (ollama) headers() []string { return nil }

func (ollama) Endpoint(path string) Kind {
	switch path {
	case OllamaChat:
		return Chat
	case OllamaGenerate:
		return Generate
	case OllamaEmbed, OllamaEmbeddings:
		return Embed
	}
	return None
}

func (ollama) Parse(path string, body []byte) (*Request, error) {
	var req olRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, bad("%v", err)
	}
	c := &Request{Kind: Chat, Model: req.Model, Stream: req.Stream == nil || *req.Stream, Extra: req.Extra}
	if o := req.Options; o != nil {
		c.Temperature, c.TopP, c.TopK, c.MaxTokens, c.Stop = o.Temperature, o.TopP, o.TopK, o.NumPredict, strs(o.Stop)
	}
	images := func(data []string) []Part {
		var out []Part
		for _, d := range data {
			part := Part{Type: "image", Data: d}
			if mt, raw, ok := DataURL(d); ok {
				part.MediaType, part.Data = mt, raw
			} else {
				part.MediaType = mediaTypeOfBase64(d)
			}
			out = append(out, part)
		}
		return out
	}
	switch path {
	case OllamaEmbed, OllamaEmbeddings:
		c.Kind, c.Stream = Embed, false
		c.Inputs = strs(req.Input)
		if len(c.Inputs) == 0 && req.Prompt != "" {
			c.Inputs = []string{req.Prompt}
		}
		return c, nil
	case OllamaGenerate:
		c.Kind = Generate
		if req.System != "" {
			c.Messages = append(c.Messages, Message{Role: "system", Parts: []Part{{Type: "text", Text: req.System}}})
		}
		c.Messages = append(c.Messages, Message{Role: "user", Parts: append([]Part{{Type: "text", Text: req.Prompt}}, images(req.Images)...)})
		return c, nil
	case OllamaChat:
	default:
		return nil, bad("%s is not an endpoint the ollama protocol serves", path)
	}
	// Ollama has no call ids, so tool results match the preceding calls in order
	var pending []string
	for _, m := range req.Messages {
		msg := Message{Role: m.Role, Extra: m.Extra}
		if m.Thinking != "" {
			if m.Role != "assistant" {
				return nil, bad("thinking belongs in assistant turns, not %s turns", m.Role)
			}
			msg.Parts = append(msg.Parts, Part{Type: "thinking", Text: m.Thinking})
		}
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
	c.Tools = toolsFromOAI(req.Tools)
	return c, nil
}

func (ollama) Render(c *Request) (string, []byte, error) {
	if c.Kind == Count {
		return "", nil, bad("the ollama protocol has no token count endpoint")
	}
	req := olRequest{Model: c.Model, Stream: ptr(c.Stream), Extra: c.Extra}
	if c.Temperature != nil || c.TopP != nil || c.TopK != nil || c.MaxTokens > 0 || len(c.Stop) > 0 {
		req.Options = &olOptions{Temperature: c.Temperature, TopP: c.TopP, TopK: c.TopK, NumPredict: c.MaxTokens}
		if len(c.Stop) > 0 {
			req.Options.Stop, _ = json.Marshal(c.Stop)
		}
	}
	if c.Kind == Embed {
		req.Stream = nil
		req.Input, _ = json.Marshal(c.Inputs)
		data, err := json.Marshal(req)
		return OllamaEmbed, data, err
	}
	for _, m := range c.Messages {
		msg := olMessage{Role: m.Role, Content: TextOf(m.Parts), Extra: m.Extra}
		if m.Role == "assistant" {
			msg.Thinking = thinkingOf(m.Parts)
		}
		for _, p := range m.Parts {
			if p.Type == "image" && p.Data != "" {
				msg.Images = append(msg.Images, p.Data)
			}
		}
		calls, broken := olToolCalls(m.ToolCalls)
		if broken != "" {
			return "", nil, bad("tool call %s has arguments that are not a JSON object", broken)
		}
		msg.ToolCalls = calls
		req.Messages = append(req.Messages, msg)
	}
	tools, err := toolsToOAI(c.Tools)
	if err != nil {
		return "", nil, err
	}
	req.Tools = tools
	data, err := json.Marshal(req)
	return OllamaChat, data, err
}

func olStop(reason string) string {
	if reason == "length" {
		return StopLength
	}
	return StopEnd
}

func olReason(stop string) string {
	if stop == StopLength {
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

func (ollama) ParseResult(c *Request, body []byte) (*Result, error) {
	var resp olResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	if resp.Error != "" {
		return nil, &Failure{Status: http.StatusBadGateway, Message: resp.Error}
	}
	r := &Result{Model: resp.Model, Usage: Usage{In: resp.PromptEvalCount, Out: resp.EvalCount}, Vectors: resp.Embeddings, Stop: olStop(resp.DoneReason), Extra: resp.Extra}
	if len(resp.Embedding) > 0 {
		r.Vectors = [][]float64{resp.Embedding}
	}
	if resp.Message != nil {
		r.Text, r.Thinking = resp.Message.Content, resp.Message.Thinking
		r.ToolCalls = olCalls(resp.Message.ToolCalls)
	} else if resp.Response != nil {
		r.Text = *resp.Response
	}
	r.Stop = StopOf(r.Stop, len(r.ToolCalls))
	return r, nil
}

func olNow() string { return time.Now().UTC().Format(time.RFC3339Nano) }

// Calls in the Ollama shape, or the name of the first whose arguments are not a JSON object
func olToolCalls(calls []ToolCall) ([]olToolCall, string) {
	var out []olToolCall
	for _, t := range calls {
		args, ok := jsonArgs(t.Args)
		if !ok {
			return nil, t.Name
		}
		tc := olToolCall{}
		tc.Function.Name, tc.Function.Arguments = t.Name, args
		out = append(out, tc)
	}
	return out, ""
}

// A whole answer in the Ollama shape
func (ollama) renderResult(c *Request, r *Result) ([]byte, error) {
	if c.Kind == Embed {
		resp := olResponse{Model: r.Model, Embeddings: r.Vectors, PromptEvalCount: r.Usage.In, Extra: r.Extra}
		if len(r.Vectors) > 0 {
			resp.Embedding = r.Vectors[0]
		}
		return json.Marshal(resp)
	}
	resp := olResponse{Model: r.Model, CreatedAt: olNow(), Done: true, DoneReason: olReason(r.Stop), PromptEvalCount: r.Usage.In, EvalCount: r.Usage.Out, Extra: r.Extra}
	if c.Kind == Generate {
		resp.Response = ptr(r.Text)
	} else {
		calls, broken := olToolCalls(r.ToolCalls)
		if broken != "" {
			return nil, &ToolArgsError{Name: broken}
		}
		resp.Message = &olMessage{Role: "assistant", Content: r.Text, Thinking: r.Thinking, ToolCalls: calls}
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
		if chunk.Error != "" {
			return errors.New(chunk.Error)
		}
		final.Extra = mergeExtra(final.Extra, chunk.Extra)
		if !started {
			started = true
			final.Model = chunk.Model
			if err := emit(Event{Kind: Started, Res: &Result{Model: chunk.Model}}); err != nil {
				return err
			}
		}
		text := ""
		if chunk.Message != nil {
			text = chunk.Message.Content
			if chunk.Message.Thinking != "" {
				if err := emit(Event{Kind: Thought, Text: chunk.Message.Thinking}); err != nil {
					return err
				}
			}
			for _, t := range olCalls(chunk.Message.ToolCalls) {
				call := t
				final.ToolCalls = append(final.ToolCalls, call)
				if err := emit(Event{Kind: Called, Index: index, Tool: &call}); err != nil {
					return err
				}
				index++
			}
		} else if chunk.Response != nil {
			text = *chunk.Response
		}
		if text != "" {
			if err := emit(Event{Kind: Said, Text: text}); err != nil {
				return err
			}
		}
		if chunk.Done {
			final.Usage.In, final.Usage.Out, final.Stop = chunk.PromptEvalCount, chunk.EvalCount, olStop(chunk.DoneReason)
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	final.Stop = StopOf(final.Stop, len(final.ToolCalls))
	return emit(Event{Kind: Stopped, Res: final})
}

type olStream struct {
	w     http.ResponseWriter
	req   *Request
	model string
	tools toolGather
	// The index of the call gathering, written whole when the stream moves on
	open *int
}

func (o ollama) Answer(w http.ResponseWriter, req *Request, model string) Writer {
	if !req.Stream {
		return &whole{w: w, req: req, model: model, client: o, render: o.renderResult}
	}
	streamHeaders(w, "application/x-ndjson")
	return &olStream{w: w, req: req, model: model}
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

// Writes the call gathered so far as one line
func (s *olStream) flushTool() error {
	if s.open == nil {
		return nil
	}
	call := *s.tools.calls[*s.open]
	s.open = nil
	calls, broken := olToolCalls([]ToolCall{call})
	if broken != "" {
		return &ToolArgsError{Name: broken}
	}
	return s.line(olResponse{Message: &olMessage{Role: "assistant", ToolCalls: calls}})
}

func (s *olStream) Write(ev Event) error {
	switch ev.Kind {
	case Said:
		if err := s.flushTool(); err != nil {
			return err
		}
		if s.req.Kind == Generate {
			return s.line(olResponse{Response: ptr(ev.Text)})
		}
		return s.line(olResponse{Message: &olMessage{Role: "assistant", Content: ev.Text}})
	case Thought:
		if s.req.Kind == Generate {
			return nil
		}
		if err := s.flushTool(); err != nil {
			return err
		}
		return s.line(olResponse{Message: &olMessage{Role: "assistant", Thinking: ev.Text}})
	case Called:
		if s.open != nil && *s.open != ev.Index {
			if err := s.flushTool(); err != nil {
				return err
			}
		}
		s.tools.add(ev)
		s.open = ptr(ev.Index)
	case Stopped:
		if err := s.flushTool(); err != nil {
			return err
		}
		res := ev.Res
		if res == nil {
			res = &Result{}
		}
		resp := olResponse{Done: true, DoneReason: olReason(res.Stop), PromptEvalCount: res.Usage.In, EvalCount: res.Usage.Out, Extra: res.Extra}
		if s.req.Kind == Generate {
			resp.Response = ptr("")
		} else {
			resp.Message = &olMessage{Role: "assistant"}
		}
		return s.line(resp)
	case Failed:
		data, err := json.Marshal(map[string]any{"error": ev.Failure.Message, "model": s.model, "created_at": olNow(), "done": true})
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

// Ollama takes image bytes and has no count endpoint
func (ollama) Traits() Traits { return Traits{InlineImages: true} }

func (ollama) ErrorMessage(body []byte) string {
	var e struct {
		Error string `json:"error"`
	}
	json.Unmarshal(body, &e)
	return e.Error
}

func (ollama) Error(w http.ResponseWriter, f Failure) {
	WriteJSON(w, f.Status, map[string]any{"error": f.Message})
}
