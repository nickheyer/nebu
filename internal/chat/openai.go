package chat

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
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
	// The tokenizer endpoint llama.cpp and vLLM share
	openaiTokenize = "/tokenize"
)

// The OpenAI chat completions protocol
type openai struct{}

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
	case OpenAIResponses:
		return Chat
	}
	return None
}

func (openai) Parse(path string, body []byte) (*Request, error) {
	if path == OpenAIResponses {
		return parseResponses(body)
	}
	var req oaiRequest
	if err := decodeRequest(body, req.read); err != nil {
		return nil, bad("%v", err)
	}
	c := &Request{Kind: Chat, Model: req.Model, MaxTokens: req.MaxTokens, Temperature: req.Temperature, TopP: req.TopP, TopK: req.TopK, N: req.N, Stop: req.Stop, Stream: req.Stream, Extra: req.Extra, Origin: originOf(path, body, req.model, req.Model)}
	c.IncludeUsage = req.Stream && req.IncludeUsage
	switch path {
	case OpenAIEmbeddings:
		c.Kind, c.Inputs = Embed, req.Input
		return c, nil
	case OpenAICompletions:
		c.Kind, c.Raw = Generate, true
		prompts := req.Prompt
		if len(prompts) == 0 {
			prompts = []Part{{Type: "text"}}
		}
		for _, p := range prompts {
			c.Messages = append(c.Messages, Message{Role: "user", Parts: []Part{p}})
		}
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
			msg.ToolCalls = append(msg.ToolCalls, ToolCall{ID: t.ID, Name: t.Name, Args: t.Arguments})
		}
		c.Messages = append(c.Messages, msg)
	}
	c.Tools = toolsFromOAI(req.Tools)
	c.ToolChoice = req.ToolChoice
	return c, nil
}

// Reads message content as text and image parts, any other part refused by name
func oaiParts(content *oaiContent) ([]Part, error) {
	if content == nil {
		return nil, nil
	}
	if !content.List {
		if content.Text == "" {
			return nil, nil
		}
		return []Part{{Type: "text", Text: content.Text}}, nil
	}
	var out []Part
	for _, p := range content.Parts {
		switch p.Type {
		case "text":
			out = append(out, Part{Type: "text", Text: p.Text, Extra: p.Extra})
		case "image_url":
			part := Part{Type: "image", URL: p.URL, Detail: p.Detail, Extra: p.Extra}
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
func oaiContentOf(parts []Part) *oaiContent {
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
			list = append(list, oaiPart{Type: "image_url", URL: url, Detail: p.Detail, Extra: p.Extra})
		}
	}
	if len(list) == 0 {
		return &oaiContent{}
	}
	if len(list) == 1 && list[0].Type == "text" && len(list[0].Extra) == 0 {
		return &oaiContent{Text: list[0].Text}
	}
	return &oaiContent{Parts: list, List: true}
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
			return nil, bad("tool %s has type %s, and the runtime accepts only tools defined by a schema", t.Name, t.Type)
		}
		out = append(out, oaiTool{Type: "function", Function: oaiFunction{Name: t.Name, Description: t.Description, Parameters: t.Schema, Strict: t.Strict}, Extra: t.Extra})
	}
	return out, nil
}

// Calls in the OpenAI shape
func oaiCalls(calls []ToolCall) []oaiToolCall {
	var out []oaiToolCall
	for _, t := range calls {
		out = append(out, oaiToolCall{ID: t.ID, Type: "function", Name: t.Name, Arguments: t.Args})
	}
	return out
}

// The body a request renders to, sized to the client's body when there is one
func renderBuffer(c *Request) []byte {
	n := 512
	if c.Origin != nil {
		n += len(c.Origin.Body) + len(c.Origin.Body)/8
	}
	return make([]byte, 0, n)
}

func (openai) Render(c *Request) (string, []byte, error) {
	if err := translatable(c); err != nil {
		return "", nil, err
	}
	b := renderBuffer(c)
	if c.Kind == Count {
		text := promptText(c)
		o := begin(&b)
		o.str("model", c.Model)
		o.str("content", text)
		o.str("prompt", text)
		o.close(nil)
		return openai{}.Traits().TokenizePath, b, nil
	}
	req := oaiRequest{Model: c.Model, MaxTokens: c.MaxTokens, Temperature: c.Temperature, TopP: c.TopP, TopK: c.TopK, N: c.N, Stop: c.Stop, Stream: c.Stream, Extra: c.Extra}
	switch {
	case c.Kind == Embed:
		req.Input, req.Stream = c.Inputs, false
		if req.Input == nil {
			req.Input = []Part{}
		}
		req.appendTo(&b)
		return OpenAIEmbeddings, b, nil
	case c.Kind == Generate && c.Raw:
		prompts, err := rawPrompts(c)
		if err != nil {
			return "", nil, err
		}
		req.Prompt, req.IncludeUsage = prompts, c.Stream
		req.appendTo(&b)
		return OpenAICompletions, b, nil
	}
	req.IncludeUsage = c.Stream
	req.Messages = make([]oaiMessage, 0, len(c.Messages))
	// Tool turns carry text only, their images following the run of tool turns as a user turn
	var held []Part
	for i, m := range c.Messages {
		if hasTokens(m.Parts) {
			return "", nil, bad("a chat turn carries text, not token ids")
		}
		msg := oaiMessage{Role: m.Role, ToolCallID: m.ToolID, Extra: m.Extra}
		if m.Role == "tool" {
			msg.Content = &oaiContent{Text: TextOf(m.Parts)}
			req.Messages = append(req.Messages, msg)
			for _, p := range m.Parts {
				if p.Type == "image" {
					held = append(held, p)
				}
			}
			if len(held) > 0 && (i+1 == len(c.Messages) || c.Messages[i+1].Role != "tool") {
				req.Messages = append(req.Messages, oaiMessage{Role: "user", Content: oaiContentOf(held)})
				held = nil
			}
			continue
		}
		msg.Content = oaiContentOf(m.Parts)
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
	req.Tools, req.ToolChoice = tools, c.ToolChoice
	req.appendTo(&b)
	return OpenAIChat, b, nil
}

// The prompts of a raw Generate request, one per user turn, each its own element
func rawPrompts(c *Request) ([]Part, error) {
	if len(c.Tools) > 0 {
		return nil, bad("a raw prompt cannot offer tools")
	}
	out := make([]Part, 0, len(c.Messages))
	for _, m := range c.Messages {
		if m.Role != "user" {
			return nil, bad("a raw prompt is written as is, with no %s turn around it", m.Role)
		}
		switch {
		case len(m.Parts) == 1 && m.Parts[0].Type == "tokens":
			out = append(out, m.Parts[0])
		case slices.ContainsFunc(m.Parts, func(p Part) bool { return p.Type != "text" }):
			return nil, bad("a raw prompt holds text or one list of token ids")
		default:
			out = append(out, Part{Type: "text", Text: TextOf(m.Parts)})
		}
	}
	return out, nil
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

// Reads a tokenizer's answer, a count or the list of tokens
func countOf(body []byte) (int, error) {
	count, tokens, listed := 0, 0, false
	err := decode(body, func(s *scan) error {
		return s.object(func(k []byte) error {
			var err error
			switch string(k) {
			case "count":
				count, err = s.integer()
			case "tokens":
				listed = s.peek() == '['
				err = s.array(func() error {
					tokens++
					return s.skip()
				})
			default:
				err = s.skip()
			}
			return err
		})
	})
	if err != nil {
		return 0, malformed(err)
	}
	if count == 0 && !listed {
		return 0, bad("the tokenizer answered without a count")
	}
	return max(count, tokens), nil
}

func (openai) ParseResult(c *Request, body []byte) (*Result, error) {
	if c.Kind == Count {
		n, err := countOf(body)
		if err != nil {
			return nil, err
		}
		return &Result{Model: c.Model, Usage: Usage{In: n}}, nil
	}
	var a oaiAnswer
	if err := decode(body, a.read); err != nil {
		return nil, malformed(err)
	}
	if message := a.failure(body); message != "" {
		return nil, &Failure{Status: http.StatusBadGateway, Message: message}
	}
	if resp := a.responseObject(); resp != nil {
		return resp.result(), nil
	}
	byIndex := map[int]*Result{}
	for _, ch := range a.Choices {
		r := &Result{Choice: ch.Index, Text: ch.Text, TokenIDs: ch.TokenIDs, PromptTokenIDs: ch.PromptTokenIDs}
		if m := ch.Message; m != nil {
			r.Text, r.Thinking = m.Content.text(), m.reasoning()
			for _, t := range m.ToolCalls {
				r.ToolCalls = append(r.ToolCalls, ToolCall{ID: t.ID, Name: t.Name, Args: t.Arguments})
			}
		}
		if ch.FinishReason != nil {
			r.Stop = oaiStop(*ch.FinishReason)
		}
		r.Stop = StopOf(r.Stop, len(r.ToolCalls))
		byIndex[ch.Index] = r
	}
	r := choices(byIndex)
	r.ID, r.Model, r.Vectors, r.Extra, r.Usage = a.ID, a.Model, a.Data, a.Extra, a.usage()
	if a.PromptTokenIDs != nil {
		r.PromptTokenIDs = a.PromptTokenIDs
	}
	r.Stop = StopOf(r.Stop, len(r.ToolCalls))
	return r, nil
}

// The tokens an answer reports, cached ones from the usage details or llama.cpp's timings
func (a *oaiAnswer) usage() Usage {
	var u Usage
	if a.Usage != nil {
		u.In, u.Out, u.Cached = a.Usage.In, a.Usage.Out, a.Usage.Cached
	}
	if t := a.Timings; t != nil {
		u.DraftOffered, u.DraftAccepted = t.DraftN, t.DraftNAccepted
		if u.Cached == 0 {
			u.Cached = t.CacheN
		}
	}
	return u
}

// Adds a chunk's usage to what the stream has reported, later counts replacing earlier ones
func (u *Usage) update(from Usage, usage, timings bool) {
	if usage {
		u.In, u.Out = from.In, from.Out
		if from.Cached > 0 {
			u.Cached = from.Cached
		}
	}
	if timings {
		u.DraftOffered, u.DraftAccepted = from.DraftOffered, from.DraftAccepted
		if from.Cached > 0 {
			u.Cached = from.Cached
		}
	}
}

// A whole answer in the OpenAI shape, every choice in its slot
func (openai) renderResult(c *Request, r *Result) ([]byte, error) {
	b := make([]byte, 0, 512+len(r.Text))
	if c.Kind == Embed {
		o := begin(&b)
		o.str("object", "list")
		o.key("data")
		b = append(b, '[')
		for i, v := range r.Vectors {
			if i > 0 {
				b = append(b, ',')
			}
			e := begin(&b)
			e.str("object", "embedding")
			e.num("index", i)
			e.key("embedding")
			appendVector(&b, v)
			e.close(nil)
		}
		b = append(b, ']')
		o.str("model", r.Model)
		o.key("usage")
		u := begin(&b)
		u.num("prompt_tokens", r.Usage.In)
		u.num("completion_tokens", 0)
		u.num("total_tokens", r.Usage.In)
		u.close(nil)
		o.close(r.Extra)
		return b, nil
	}
	reply := oaiReply{ID: resultID(r, "chatcmpl"), Object: "chat.completion", Created: time.Now().Unix(), Model: r.Model, Usage: &r.Usage, Extra: r.Extra, PromptTokenIDs: r.PromptTokenIDs}
	if c.Kind == Generate {
		reply.Object, reply.PromptTokenIDs = "text_completion", nil
	}
	for _, ch := range r.All() {
		reason := oaiReason(ch.Stop)
		choice := oaiReplyChoice{Index: ch.Choice, FinishReason: &reason, TokenIDs: ch.TokenIDs}
		if c.Kind == Generate {
			choice.Text, choice.PromptTokenIDs = &ch.Text, ch.PromptTokenIDs
		} else {
			choice.Message = &oaiMessage{Role: "assistant", Content: &oaiContent{Text: ch.Text}, ReasoningContent: ch.Thinking, ToolCalls: oaiCalls(ch.ToolCalls)}
		}
		reply.Choices = append(reply.Choices, choice)
	}
	reply.appendTo(&b)
	return b, nil
}

// Reads a chat completions or Responses stream into events, each choice of the first kept apart
type oaiStreamRead struct {
	emit    func(Event) error
	started bool
	final   Result
	// Per choice tool calls and stop reasons of a chat stream
	tools map[int]*toolGather
	stops map[int]string
	// A Responses stream's tool calls by item id and output index, and which have had arguments
	calls   toolGather
	byItem  map[string]int
	byIndex map[int]int
	argued  map[int]bool
	native  bool
}

func (openai) ParseStream(rd io.Reader, emit func(Event) error) error {
	p := &oaiStreamRead{emit: emit, tools: map[int]*toolGather{}, stops: map[int]string{}, byItem: map[string]int{}, byIndex: map[int]int{}, argued: map[int]bool{}}
	if err := ReadSSE(rd, p.event); err != nil {
		return err
	}
	return p.stop()
}

func (p *oaiStreamRead) event(_ string, data []byte) error {
	if string(data) == "[DONE]" {
		return nil
	}
	var a oaiAnswer
	if err := decode(data, a.read); err != nil {
		return malformed(err)
	}
	if message := a.failure(data); message != "" {
		return errors.New(message)
	}
	if a.Type == "error" || strings.HasPrefix(a.Type, "response.") {
		p.native = true
		return p.responses(&a)
	}
	p.final.Extra = mergeExtra(p.final.Extra, a.Extra)
	promptIDs := a.PromptTokenIDs
	for _, ch := range a.Choices {
		if ch.PromptTokenIDs != nil {
			promptIDs = ch.PromptTokenIDs
		}
	}
	if err := p.start(a.ID, a.Model, promptIDs); err != nil {
		return err
	}
	if promptIDs != nil {
		p.final.PromptTokenIDs = promptIDs
	}
	p.final.Usage.update(a.usage(), a.Usage != nil, a.Timings != nil)
	for _, ch := range a.Choices {
		tools := p.toolsOf(ch.Index)
		ids := ch.TokenIDs
		if ch.Text != "" || (ch.Delta == nil && len(ids) > 0) {
			if err := p.emit(Event{Kind: Said, Text: ch.Text, Choice: ch.Index, TokenIDs: ids}); err != nil {
				return err
			}
			ids = nil
		}
		if d := ch.Delta; d != nil {
			if reasoning := d.reasoning(); reasoning != "" {
				if err := p.emit(Event{Kind: Thought, Text: reasoning, Choice: ch.Index}); err != nil {
					return err
				}
			}
			if text := d.Content.text(); text != "" || len(ids) > 0 {
				if err := p.emit(Event{Kind: Said, Text: text, Choice: ch.Index, TokenIDs: ids}); err != nil {
					return err
				}
			}
			for i, t := range d.ToolCalls {
				idx := i
				if t.Index != nil {
					idx = *t.Index
				}
				ev := Event{Kind: Called, Index: idx, Choice: ch.Index, Tool: &ToolCall{ID: t.ID, Name: t.Name, Args: t.Arguments}}
				tools.add(ev)
				if err := p.emit(ev); err != nil {
					return err
				}
			}
		}
		if ch.FinishReason != nil && *ch.FinishReason != "" {
			p.stops[ch.Index] = oaiStop(*ch.FinishReason)
		}
	}
	return nil
}

func (p *oaiStreamRead) toolsOf(choice int) *toolGather {
	t, ok := p.tools[choice]
	if !ok {
		t = &toolGather{}
		p.tools[choice] = t
	}
	return t
}

// Emits the answer's id, model and prompt ids once, ahead of anything else
func (p *oaiStreamRead) start(id, model string, promptIDs []int) error {
	if p.started {
		return nil
	}
	p.started = true
	p.final.ID, p.final.Model = id, model
	return p.emit(Event{Kind: Started, Res: &Result{ID: id, Model: model, PromptTokenIDs: promptIDs}})
}

// Reads one event of a Responses stream from a runtime serving the API natively
func (p *oaiStreamRead) responses(a *oaiAnswer) error {
	id, model := "", ""
	if a.Response != nil {
		id, model = a.Response.ID, a.Response.Model
	}
	if err := p.start(id, model, nil); err != nil {
		return err
	}
	switch a.Type {
	case "error":
		return fmt.Errorf("%s: %s", a.Code, a.Message)
	case "response.failed":
		message := "the response failed"
		if a.Response != nil && a.Response.Error != "" {
			message = a.Response.Error
		}
		return errors.New(message)
	case "response.output_text.delta", "response.refusal.delta":
		return p.emit(Event{Kind: Said, Text: a.Delta})
	case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
		return p.emit(Event{Kind: Thought, Text: a.Delta})
	case "response.output_item.added", "response.output_item.done":
		if a.Item == nil || a.Item.Type != "function_call" {
			return nil
		}
		n, known := p.callIndex(a.Item.ID, a.OutputIndex)
		ev := Event{Kind: Called, Index: n, Tool: &ToolCall{}}
		if !known {
			ev.Tool.ID, ev.Tool.Name = a.Item.CallID, a.Item.Name
		}
		if !p.argued[n] && a.Item.Arguments != "" {
			ev.Tool.Args = a.Item.Arguments
			p.argued[n] = true
		}
		if known && ev.Tool.Args == "" {
			return nil
		}
		p.calls.add(ev)
		return p.emit(ev)
	case "response.function_call_arguments.delta", "response.function_call_arguments.done":
		n, _ := p.callIndex(a.ItemID, a.OutputIndex)
		args := a.Delta
		if a.Type == "response.function_call_arguments.done" {
			if p.argued[n] {
				return nil
			}
			args = a.Arguments
		}
		if args == "" {
			return nil
		}
		p.argued[n] = true
		ev := Event{Kind: Called, Index: n, Tool: &ToolCall{Args: args}}
		p.calls.add(ev)
		return p.emit(ev)
	case "response.completed", "response.incomplete":
		if r := a.Response; r != nil {
			if r.Usage != nil {
				p.final.Usage.In, p.final.Usage.Out, p.final.Usage.Cached = r.Usage.In, r.Usage.Out, r.Usage.Cached
			}
			p.stops[0] = respStop(r.Status, r.Incomplete)
		}
	}
	return nil
}

// The call index of a Responses function call item, numbered in order of appearance, and whether it was seen before
func (p *oaiStreamRead) callIndex(item string, output int) (int, bool) {
	if n, ok := p.byItem[item]; ok && item != "" {
		return n, true
	}
	if n, ok := p.byIndex[output]; ok {
		return n, true
	}
	n := len(p.byIndex)
	p.byIndex[output] = n
	if item != "" {
		p.byItem[item] = n
	}
	return n, false
}

// Emits Stopped with every choice's calls and stop reason, the answer's fields on the first
func (p *oaiStreamRead) stop() error {
	if p.native {
		res := p.final
		res.ToolCalls = p.calls.list()
		res.Stop = StopOf(p.stops[0], len(res.ToolCalls))
		return p.emit(Event{Kind: Stopped, Res: &res})
	}
	byIndex := map[int]*Result{}
	for choice, tools := range p.tools {
		byIndex[choice] = &Result{Choice: choice, ToolCalls: tools.list()}
	}
	for choice, stop := range p.stops {
		if _, ok := byIndex[choice]; !ok {
			byIndex[choice] = &Result{Choice: choice}
		}
		byIndex[choice].Stop = stop
	}
	for _, r := range byIndex {
		r.Stop = StopOf(r.Stop, len(r.ToolCalls))
	}
	first := choices(byIndex)
	first.ID, first.Model, first.Usage, first.Extra = p.final.ID, p.final.Model, p.final.Usage, p.final.Extra
	first.Stop = StopOf(first.Stop, len(first.ToolCalls))
	return p.emit(Event{Kind: Stopped, Res: first})
}

// One choice's state in a stream written to a client
type oaiChoiceOut struct {
	sentRole bool
	tools    toolGather
}

type oaiStream struct {
	live    *liveWriter
	req     *Request
	id      string
	model   string
	created int64
	choices map[int]*oaiChoiceOut
	// Prompt token ids announced at the start, written on the first chunk
	promptIDs []int
}

func (o openai) Answer(w http.ResponseWriter, req *Request, model string) Writer {
	if req.Responses {
		return o.answerResponses(w, req, model)
	}
	if !req.Stream {
		return &whole{w: w, req: req, model: model, client: o, render: o.renderResult, choices: true}
	}
	streamHeaders(w, "text/event-stream")
	return &oaiStream{live: newLiveWriter(w, pingComment), req: req, id: newID("chatcmpl"), model: model, created: time.Now().Unix(), choices: map[int]*oaiChoiceOut{}}
}

func (s *oaiStream) choice(i int) *oaiChoiceOut {
	c, ok := s.choices[i]
	if !ok {
		c = &oaiChoiceOut{}
		s.choices[i] = c
	}
	return c
}

// Writes one delta for a choice, the role on the choice's first
func (s *oaiStream) chunk(choice int, delta *oaiMessage, text string, finish *string, extra map[string]json.RawMessage, tokens []int) error {
	reply := oaiReply{ID: s.id, Object: "chat.completion.chunk", Created: s.created, Model: s.model, Extra: extra, PromptTokenIDs: s.promptIDs}
	s.promptIDs = nil
	out := oaiReplyChoice{Index: choice, FinishReason: finish, TokenIDs: tokens}
	if s.req.Kind == Generate {
		reply.Object = "text_completion"
		out.Text, out.PromptTokenIDs, reply.PromptTokenIDs = &text, reply.PromptTokenIDs, nil
	} else {
		if delta == nil {
			delta = &oaiMessage{}
		}
		if c := s.choice(choice); !c.sentRole {
			delta.Role, c.sentRole = "assistant", true
		}
		out.Delta = delta
	}
	reply.Choices = []oaiReplyChoice{out}
	return s.live.sseData("", reply.json())
}

func (s *oaiStream) Write(ev Event) error {
	switch ev.Kind {
	case Started:
		if ev.Res != nil && ev.Res.ID != "" {
			s.id = ev.Res.ID
		}
		if ev.Res != nil && ev.Res.PromptTokenIDs != nil {
			s.promptIDs = ev.Res.PromptTokenIDs
		}
		return nil
	case Said:
		return s.chunk(ev.Choice, &oaiMessage{Content: &oaiContent{Text: ev.Text}}, ev.Text, nil, nil, ev.TokenIDs)
	case Thought:
		if s.req.Kind == Generate {
			return nil
		}
		return s.chunk(ev.Choice, &oaiMessage{ReasoningContent: ev.Text}, "", nil, nil, nil)
	case Called:
		s.choice(ev.Choice).tools.add(ev)
		tc := oaiToolCall{Index: ptr(ev.Index), ID: ev.Tool.ID, Type: "function", Name: ev.Tool.Name, Arguments: ev.Tool.Args}
		if tc.ID == "" && tc.Name == "" {
			tc.Type = ""
		}
		return s.chunk(ev.Choice, &oaiMessage{ToolCalls: []oaiToolCall{tc}}, "", nil, nil, nil)
	case Stopped:
		res := ev.Res
		if res == nil {
			res = &Result{}
		}
		stops := map[int]string{}
		for _, r := range res.All() {
			stops[r.Choice] = r.Stop
		}
		for choice := range s.choices {
			if _, ok := stops[choice]; !ok {
				stops[choice] = ""
			}
		}
		order := make([]int, 0, len(stops))
		for choice := range stops {
			order = append(order, choice)
		}
		slices.Sort(order)
		for _, choice := range order {
			reason := oaiReason(StopOf(stops[choice], len(s.choice(choice).tools.order)))
			if err := s.chunk(choice, &oaiMessage{}, "", &reason, res.Extra, nil); err != nil {
				return err
			}
		}
		if !s.req.IncludeUsage {
			return nil
		}
		// The usage goes last in a chunk of its own with no choices, as the API sends it
		last := oaiReply{ID: s.id, Object: "chat.completion.chunk", Created: s.created, Model: s.model, Choices: []oaiReplyChoice{}, Usage: &res.Usage}
		if s.req.Kind == Generate {
			last.Object = "text_completion"
		}
		return s.live.sseData("", last.json())
	case Failed:
		return s.live.sse("", oaiStreamFailure(*ev.Failure))
	}
	return nil
}

// The data event carrying the error object that ends a chat or completions stream
func oaiStreamFailure(f Failure) map[string]any {
	code := f.Code
	if code == "" {
		code = CodeUpstream
	}
	return map[string]any{"error": map[string]any{"message": f.Message, "type": code, "code": code}}
}

// Ends a chat or completions stream with the error object, a Responses stream with the error event numbered next
func (openai) StreamFailure(at StreamAt, f Failure) ([]byte, error) {
	if at.Path == OpenAIResponses {
		fields := respFailure(f)
		fields["type"], fields["sequence_number"] = "error", at.Seq
		return sseJSON("error", fields)
	}
	return sseJSON("", oaiStreamFailure(f))
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

// Serves Responses itself, choices by n, and counts tokens at /tokenize as llama.cpp and vLLM do
func (openai) Traits() Traits {
	return Traits{Responses: true, Choices: true, TokenizePath: openaiTokenize}
}

func (o openai) Native(req *Request) (string, []byte, error) { return native(o, req) }

func (openai) ErrorMessage(body []byte) string { return errorField(body) }

func (openai) Error(w http.ResponseWriter, f Failure) {
	retryHeader(w, f)
	WriteJSON(w, f.Status, map[string]any{"error": map[string]any{"message": f.Message, "type": f.Code, "code": f.Code}})
}
