package chat

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
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

// Image parts from base64 payloads or data URLs, typed by their leading bytes
func olImages(data []string) []Part {
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

func (ollama) Parse(path string, body []byte) (*Request, error) {
	var req olRequest
	if err := decodeRequest(body, req.read); err != nil {
		return nil, bad("%v", err)
	}
	c := &Request{Kind: Chat, Model: req.Model, Stream: req.Stream == nil || *req.Stream, Raw: req.Raw, Extra: req.Extra, Origin: originOf(path, body, req.model, req.Model)}
	if o := req.Options; o != nil {
		c.Temperature, c.TopP, c.TopK, c.MaxTokens, c.Stop = o.Temperature, o.TopP, o.TopK, o.NumPredict, o.Stop
	}
	switch path {
	case OllamaEmbed, OllamaEmbeddings:
		c.Kind, c.Stream, c.Inputs = Embed, false, req.Input
		if len(c.Inputs) == 0 && req.Prompt != "" {
			c.Inputs = []Part{{Type: "text", Text: req.Prompt}}
		}
		return c, nil
	case OllamaGenerate:
		c.Kind = Generate
		if req.System != "" {
			c.Messages = append(c.Messages, Message{Role: "system", Parts: []Part{{Type: "text", Text: req.System}}})
		}
		c.Messages = append(c.Messages, Message{Role: "user", Parts: append([]Part{{Type: "text", Text: req.Prompt}}, olImages(req.Images)...)})
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
		msg.Parts = append(msg.Parts, olImages(m.Images)...)
		if len(m.ToolCalls) > 0 {
			pending = pending[:0]
		}
		for i, t := range m.ToolCalls {
			id := newID("call") + "-" + string(rune('a'+i))
			pending = append(pending, id)
			msg.ToolCalls = append(msg.ToolCalls, ToolCall{ID: id, Name: t.Name, Args: string(t.Arguments)})
		}
		if m.Role == "tool" && len(pending) > 0 {
			msg.ToolID, pending = pending[0], pending[1:]
		}
		c.Messages = append(c.Messages, msg)
	}
	c.Tools = toolsFromOAI(req.Tools)
	return c, nil
}

// The images of parts as base64 payloads
func olImagesOf(parts []Part) []string {
	var out []string
	for _, p := range parts {
		if p.Type == "image" && p.Data != "" {
			out = append(out, p.Data)
		}
	}
	return out
}

func (ollama) Render(c *Request) (string, []byte, error) {
	if err := translatable(c); err != nil {
		return "", nil, err
	}
	if c.Kind == Count {
		return "", nil, bad("the ollama protocol has no token count endpoint")
	}
	if err := choicesFit(ollama{}, c, "ollama"); err != nil {
		return "", nil, err
	}
	req := olRequest{Model: c.Model, Stream: ptr(c.Stream), Extra: c.Extra}
	if c.Temperature != nil || c.TopP != nil || c.TopK != nil || c.MaxTokens > 0 || len(c.Stop) > 0 {
		req.Options = &olOptions{Temperature: c.Temperature, TopP: c.TopP, TopK: c.TopK, NumPredict: c.MaxTokens, Stop: c.Stop}
	}
	b := renderBuffer(c)
	switch c.Kind {
	case Embed:
		if hasTokens(c.Inputs) {
			return "", nil, bad("the ollama protocol embeds text, not token ids")
		}
		req.Stream, req.Input = nil, c.Inputs
		if req.Input == nil {
			req.Input = []Part{}
		}
		req.appendTo(&b)
		return OllamaEmbed, b, nil
	case Generate:
		if err := olGenerate(c, &req); err != nil {
			return "", nil, err
		}
		req.appendTo(&b)
		return OllamaGenerate, b, nil
	}
	req.Messages = make([]olMessage, 0, len(c.Messages))
	for _, m := range c.Messages {
		if hasTokens(m.Parts) {
			return "", nil, bad("the ollama protocol takes turns as text, not token ids")
		}
		msg := olMessage{Role: m.Role, Content: TextOf(m.Parts), Images: olImagesOf(m.Parts), Extra: m.Extra}
		if m.Role == "assistant" {
			msg.Thinking = thinkingOf(m.Parts)
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
	req.appendTo(&b)
	return OllamaChat, b, nil
}

// Fills a generate request from system turns and the one prompt, raw when the client asked for no template
func olGenerate(c *Request, req *olRequest) error {
	if len(c.Tools) > 0 {
		return bad("the ollama generate endpoint takes no tools")
	}
	var system []string
	prompts := 0
	for _, m := range c.Messages {
		switch m.Role {
		case "system":
			system = append(system, TextOf(m.Parts))
		case "user":
			if prompts++; prompts > 1 {
				return bad("the ollama protocol takes one prompt a request, so each prompt takes a call of its own")
			}
			if hasTokens(m.Parts) {
				return bad("the ollama protocol takes prompts as text, not token ids")
			}
			req.Prompt, req.Images = TextOf(m.Parts), olImagesOf(m.Parts)
		default:
			return bad("a generate request holds system turns and a prompt, not %s turns", m.Role)
		}
	}
	req.System, req.Raw = strings.Join(system, "\n\n"), c.Raw
	return nil
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
		out = append(out, ToolCall{ID: newID("call"), Name: t.Name, Args: string(t.Arguments)})
	}
	return out
}

func (ollama) ParseResult(c *Request, body []byte) (*Result, error) {
	var resp olResponse
	if err := decode(body, resp.read); err != nil {
		return nil, malformed(err)
	}
	if resp.Error != "" {
		return nil, &Failure{Status: http.StatusBadGateway, Message: resp.Error}
	}
	r := &Result{Model: resp.Model, Usage: Usage{In: resp.PromptEvalCount, Out: resp.EvalCount, Cached: resp.PromptEvalCachedCount}, Vectors: resp.Embeddings, Stop: olStop(resp.DoneReason), Extra: resp.Extra}
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
		out = append(out, olToolCall{Name: t.Name, Arguments: args})
	}
	return out, ""
}

// A whole answer in the Ollama shape
func (ollama) renderResult(c *Request, r *Result) ([]byte, error) {
	b := make([]byte, 0, 256+len(r.Text))
	if c.Kind == Embed {
		o := begin(&b)
		o.str("model", r.Model)
		o.key("embeddings")
		b = append(b, '[')
		for i, v := range r.Vectors {
			if i > 0 {
				b = append(b, ',')
			}
			appendVector(&b, v)
		}
		b = append(b, ']')
		if len(r.Vectors) > 0 {
			o.key("embedding")
			appendVector(&b, r.Vectors[0])
		}
		o.numIf("prompt_eval_count", r.Usage.In)
		o.close(r.Extra)
		return b, nil
	}
	resp := olResponse{Model: r.Model, CreatedAt: olNow(), Done: true, DoneReason: olReason(r.Stop), PromptEvalCount: r.Usage.In, PromptEvalCachedCount: r.Usage.Cached, EvalCount: r.Usage.Out, Extra: r.Extra}
	if c.Kind == Generate {
		resp.Response = ptr(r.Text)
	} else {
		calls, broken := olToolCalls(r.ToolCalls)
		if broken != "" {
			return nil, &ToolArgsError{Name: broken}
		}
		resp.Message = &olMessage{Role: "assistant", Content: r.Text, Thinking: r.Thinking, ToolCalls: calls}
	}
	resp.appendTo(&b)
	return b, nil
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
		if err := decode(line, chunk.read); err != nil {
			return malformed(err)
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
			final.Usage.In, final.Usage.Out, final.Usage.Cached, final.Stop = chunk.PromptEvalCount, chunk.EvalCount, chunk.PromptEvalCachedCount, olStop(chunk.DoneReason)
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	final.Stop = StopOf(final.Stop, len(final.ToolCalls))
	return emit(Event{Kind: Stopped, Res: final})
}

type olStream struct {
	live  *liveWriter
	req   *Request
	model string
	tools toolGather
	// The index of the call gathering, written whole when the stream moves on
	open *int
}

// Streams NDJSON, an empty chunk that is not done going out after silence so clients and proxies hold on
func (o ollama) Answer(w http.ResponseWriter, req *Request, model string) Writer {
	if !req.Stream {
		return &whole{w: w, req: req, model: model, client: o, render: o.renderResult}
	}
	streamHeaders(w, "application/x-ndjson")
	s := &olStream{req: req, model: model}
	s.live = newLiveWriter(w, s.keepalive)
	return s
}

// A chunk with empty content and done false, which every Ollama client reads as nothing said yet
func (s *olStream) keepalive(w http.ResponseWriter) error {
	return writeLine(w, s.empty())
}

func (s *olStream) empty() []byte {
	resp := olResponse{Model: s.model, CreatedAt: olNow()}
	if s.req.Kind == Generate {
		resp.Response = ptr("")
	} else {
		resp.Message = &olMessage{Role: "assistant"}
	}
	return resp.json()
}

// Writes one NDJSON line and flushes it
func writeLine(w http.ResponseWriter, data []byte) error {
	if _, err := w.Write(append(data, '\n')); err != nil {
		return err
	}
	flush(w)
	return nil
}

func (s *olStream) line(resp olResponse) error {
	resp.Model, resp.CreatedAt = s.model, olNow()
	data := resp.json()
	return s.live.write(func(w http.ResponseWriter) error { return writeLine(w, data) })
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
	if err := oneChoice(ev, "ollama"); err != nil {
		return err
	}
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
		resp := olResponse{Done: true, DoneReason: olReason(res.Stop), PromptEvalCount: res.Usage.In, PromptEvalCachedCount: res.Usage.Cached, EvalCount: res.Usage.Out, Extra: res.Extra}
		if s.req.Kind == Generate {
			resp.Response = ptr("")
		} else {
			resp.Message = &olMessage{Role: "assistant"}
		}
		return s.line(resp)
	case Failed:
		data, err := olStreamFailure(s.model, *ev.Failure)
		if err != nil {
			return err
		}
		return s.live.write(func(w http.ResponseWriter) error { return writeLine(w, data) })
	}
	return nil
}

// The done line carrying the error that ends a chat or generate stream
func olStreamFailure(model string, f Failure) ([]byte, error) {
	return json.Marshal(map[string]any{"error": f.Message, "model": model, "created_at": olNow(), "done": true})
}

// Ends a chat or generate stream with its error line
func (ollama) StreamFailure(at StreamAt, f Failure) ([]byte, error) {
	data, err := olStreamFailure(at.Model, f)
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

func (s *olStream) Close() error {
	s.live.close()
	return nil
}

// Ollama takes image bytes and has no tokenizer endpoint
func (ollama) Traits() Traits { return Traits{InlineImages: true} }

func (o ollama) Native(req *Request) (string, []byte, error) { return native(o, req) }

func (ollama) ErrorMessage(body []byte) string {
	message := ""
	decode(body, func(s *scan) error {
		return s.object(func(k []byte) error {
			if string(k) != "error" || s.peek() != '"' {
				return s.skip()
			}
			var err error
			message, err = s.str()
			return err
		})
	})
	return message
}

func (ollama) Error(w http.ResponseWriter, f Failure) {
	retryHeader(w, f)
	WriteJSON(w, f.Status, map[string]any{"error": f.Message})
}
