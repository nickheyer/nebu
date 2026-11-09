package stub

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Cap on JSON request bodies
const maxBody = 64 << 20

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

// A stop field given as one string or a list of strings
type stopList []string

// Accepts null, a string, or a list of strings
func (l *stopList) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		*l = nil
		return nil
	}
	var one string
	if err := json.Unmarshal(b, &one); err == nil {
		*l = stopList{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(b, &many); err != nil {
		return err
	}
	*l = many
	return nil
}

// Options both completion endpoints share
type generation struct {
	Stream              bool           `json:"stream"`
	StreamOptions       *streamOptions `json:"stream_options"`
	MaxTokens           *int           `json:"max_tokens"`
	MaxCompletionTokens *int           `json:"max_completion_tokens"`
	N                   *int           `json:"n"`
	Stop                stopList       `json:"stop"`
	ReturnTokenIDs      bool           `json:"return_token_ids"`
}

type chatMessage struct {
	Role       string          `json:"role"`
	Name       string          `json:"name"`
	Content    json.RawMessage `json:"content"`
	ToolCalls  json.RawMessage `json:"tool_calls"`
	ToolCallID string          `json:"tool_call_id"`
}

type chatRequest struct {
	Model          string          `json:"model"`
	Messages       []chatMessage   `json:"messages"`
	Tools          json.RawMessage `json:"tools"`
	ToolChoice     json.RawMessage `json:"tool_choice"`
	ResponseFormat *struct {
		Type string `json:"type"`
	} `json:"response_format"`
	Logprobs bool `json:"logprobs"`
	// The prompt as token ids in place of the rendered messages, as SGLang takes it
	InputIDs []uint32 `json:"input_ids"`
	generation
}

type completionRequest struct {
	Model    string          `json:"model"`
	Prompt   json.RawMessage `json:"prompt"`
	Echo     bool            `json:"echo"`
	Logprobs *int            `json:"logprobs"`
	Suffix   *string         `json:"suffix"`
	generation
}

// Input to one completion as text with its token ids or as token ids alone
type prompt struct {
	text   string
	ids    []uint32
	isText bool
}

// A validated generation request ready for the engine
type job struct {
	chat     bool
	model    string
	prompts  []prompt
	n        int
	target   int
	limit    string
	stops    []string
	echo     bool
	stream   bool
	usage    bool
	tokenIDs bool
}

type usage struct {
	PromptTokens        int           `json:"prompt_tokens"`
	CompletionTokens    int           `json:"completion_tokens"`
	TotalTokens         int           `json:"total_tokens"`
	PromptTokensDetails promptDetails `json:"prompt_tokens_details"`
}

type promptDetails struct {
	CachedTokens int `json:"cached_tokens"`
}

type completion struct {
	ID             string   `json:"id"`
	Object         string   `json:"object"`
	Created        int64    `json:"created"`
	Model          string   `json:"model"`
	Choices        []any    `json:"choices"`
	Usage          usage    `json:"usage"`
	PromptTokenIDs []uint32 `json:"prompt_token_ids,omitempty"`
}

type chunk struct {
	ID             string          `json:"id"`
	Object         string          `json:"object"`
	Created        int64           `json:"created"`
	Model          string          `json:"model"`
	Choices        []any           `json:"choices"`
	Usage          json.RawMessage `json:"usage,omitempty"`
	PromptTokenIDs []uint32        `json:"prompt_token_ids,omitempty"`
}

type messageChoice struct {
	Index        int       `json:"index"`
	Message      message   `json:"message"`
	Logprobs     *struct{} `json:"logprobs"`
	FinishReason string    `json:"finish_reason"`
	TokenIDs     []uint32  `json:"token_ids,omitempty"`
}

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type deltaChoice struct {
	Index        int       `json:"index"`
	Delta        delta     `json:"delta"`
	Logprobs     *struct{} `json:"logprobs"`
	FinishReason *string   `json:"finish_reason"`
	TokenIDs     []uint32  `json:"token_ids,omitempty"`
}

type delta struct {
	Role    string  `json:"role,omitempty"`
	Content *string `json:"content,omitempty"`
}

type textChoice struct {
	Index          int       `json:"index"`
	Text           string    `json:"text"`
	Logprobs       *struct{} `json:"logprobs"`
	FinishReason   *string   `json:"finish_reason"`
	PromptTokenIDs []uint32  `json:"prompt_token_ids,omitempty"`
	TokenIDs       []uint32  `json:"token_ids,omitempty"`
}

// Reads a JSON body into v, answering 400 on malformed input
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody)).Decode(v); err != nil {
		writeError(w, badRequest("", "Invalid JSON body: %v", err))
		return false
	}
	return true
}

// Serves OpenAI chat completions over the emulated engine
func (s *Stub) handleChat(w http.ResponseWriter, r *http.Request) {
	if !s.gate(w) {
		return
	}
	var req chatRequest
	if !decode(w, r, &req) {
		return
	}
	text, err := renderChat(&req)
	if err != nil {
		writeError(w, err)
		return
	}
	first, perr := s.textPrompt(text)
	if perr != nil {
		writeError(w, perr)
		return
	}
	j := job{chat: true, model: req.Model, prompts: []prompt{first}}
	if s.cfg.Names == NamesSGLang && req.InputIDs != nil {
		j.prompts = []prompt{{ids: req.InputIDs}}
	}
	if err := s.options(req.generation, &j); err != nil {
		writeError(w, err)
		return
	}
	s.generate(w, r, j)
}

// Serves OpenAI text completions over the emulated engine
func (s *Stub) handleCompletions(w http.ResponseWriter, r *http.Request) {
	if !s.gate(w) {
		return
	}
	var req completionRequest
	if !decode(w, r, &req) {
		return
	}
	if req.Logprobs != nil {
		writeError(w, badRequest("logprobs", "logprobs are not supported by this server."))
		return
	}
	if req.Suffix != nil && *req.Suffix != "" {
		writeError(w, badRequest("suffix", "suffix is not supported by this server."))
		return
	}
	prompts, err := s.parsePrompts(req.Prompt)
	if err != nil {
		writeError(w, err)
		return
	}
	if req.Echo {
		for i, p := range prompts {
			if p.isText {
				continue
			}
			if s.cfg.Tokenizer == nil {
				writeError(w, badRequest("echo", "echo requires text prompts."))
				return
			}
			text, derr := s.cfg.Tokenizer.Decode(p.ids)
			if derr != nil {
				writeError(w, badRequest("prompt", "prompt token ids do not decode: %v", derr))
				return
			}
			prompts[i].text = text
		}
	}
	j := job{model: req.Model, prompts: prompts, echo: req.Echo}
	if err := s.options(req.generation, &j); err != nil {
		writeError(w, err)
		return
	}
	s.generate(w, r, j)
}

// Renders tools and messages into one prompt ending with the assistant turn opener
func renderChat(req *chatRequest) (string, *apiError) {
	if req.Logprobs {
		return "", badRequest("logprobs", "logprobs are not supported by this server.")
	}
	if f := req.ResponseFormat; f != nil && f.Type != "" && f.Type != "text" {
		return "", badRequest("response_format", "response_format %q is not supported by this server, which answers in plain text.", f.Type)
	}
	if c := bytes.TrimSpace(req.ToolChoice); len(c) > 0 && string(c) != "null" && string(c) != `"auto"` && string(c) != `"none"` {
		return "", badRequest("tool_choice", "tool_choice %s requires a tool call, which this server does not generate.", c)
	}
	if len(req.Messages) == 0 {
		return "", badRequest("messages", "messages must contain at least one message.")
	}
	var b strings.Builder
	if t := bytes.TrimSpace(req.Tools); len(t) > 0 && string(t) != "null" {
		b.WriteString("<|tools|>\n")
		b.Write(t)
		b.WriteString("\n")
	}
	for i, m := range req.Messages {
		if m.Role == "" {
			return "", badRequest("messages", "messages[%d] has no role.", i)
		}
		text, err := messageText(m.Content)
		if err != nil {
			return "", err
		}
		b.WriteString("<|" + m.Role + "|>")
		if m.Name != "" {
			b.WriteString(" " + m.Name)
		}
		if m.ToolCallID != "" {
			b.WriteString(" " + m.ToolCallID)
		}
		b.WriteString("\n" + text)
		if c := bytes.TrimSpace(m.ToolCalls); len(c) > 0 && string(c) != "null" {
			b.WriteString("\n")
			b.Write(c)
		}
		b.WriteString("\n")
	}
	b.WriteString("<|assistant|>\n")
	return b.String(), nil
}

// Text of a message content given as null, a string, or a list of text parts
func messageText(raw json.RawMessage) (string, *apiError) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s, nil
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &parts); err != nil {
		return "", badRequest("messages", "message content must be a string or a list of content parts.")
	}
	texts := make([]string, 0, len(parts))
	for _, p := range parts {
		if p.Type != "text" {
			return "", badRequest("messages", "content part type %q is not supported by this text only model.", p.Type)
		}
		texts = append(texts, p.Text)
	}
	return strings.Join(texts, "\n"), nil
}

// Prompts given as a string, strings, token ids, or lists of token ids
func (s *Stub) parsePrompts(raw json.RawMessage) ([]prompt, *apiError) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return nil, badRequest("prompt", "prompt is required.")
	}
	var one string
	if json.Unmarshal(raw, &one) == nil {
		p, err := s.textPrompt(one)
		if err != nil {
			return nil, err
		}
		return []prompt{p}, nil
	}
	var texts []string
	if json.Unmarshal(raw, &texts) == nil && len(texts) > 0 {
		out := make([]prompt, len(texts))
		for i, t := range texts {
			p, err := s.textPrompt(t)
			if err != nil {
				return nil, err
			}
			out[i] = p
		}
		return out, nil
	}
	var ids []uint32
	if json.Unmarshal(raw, &ids) == nil && len(ids) > 0 {
		return []prompt{{ids: ids}}, nil
	}
	var batch [][]uint32
	if json.Unmarshal(raw, &batch) == nil && len(batch) > 0 {
		out := make([]prompt, len(batch))
		for i, b := range batch {
			out[i] = prompt{ids: b}
		}
		return out, nil
	}
	return nil, badRequest("prompt", "prompt must be a string, a list of strings, a list of token ids, or a list of token id lists.")
}

// Tokenized input for a string, by the configured tokenizer or the hashed one, a tokenizer fault answered as a server error
func (s *Stub) textPrompt(text string) (prompt, *apiError) {
	ids, err := s.encode(text, true)
	if err != nil {
		return prompt{}, err
	}
	return prompt{text: text, ids: ids, isText: true}, nil
}

// The ids of a text, hashed words without a configured tokenizer
func (s *Stub) encode(text string, special bool) ([]uint32, *apiError) {
	if s.cfg.Tokenizer == nil {
		return tokenIDs(Tokenize(text)), nil
	}
	ids, err := s.cfg.Tokenizer.Encode(text, special)
	if err != nil {
		return nil, &apiError{status: http.StatusInternalServerError, message: fmt.Sprintf("the tokenizer could not encode the text: %v", err), kind: "server_error", code: "tokenizer_error"}
	}
	return ids, nil
}

// The ids of one generated piece of text, one hashed id without a configured tokenizer
func (s *Stub) pieceIDs(text string) ([]uint32, *apiError) {
	if s.cfg.Tokenizer == nil {
		return tokenIDs([]string{text}), nil
	}
	return s.encode(text, false)
}

// Reads streaming, length, choice count and stop options into the job
func (s *Stub) options(g generation, j *job) *apiError {
	if g.StreamOptions != nil && !g.Stream {
		return badRequest("stream_options", "stream_options is only allowed when stream is true.")
	}
	j.stream = g.Stream
	j.usage = g.StreamOptions != nil && g.StreamOptions.IncludeUsage
	j.tokenIDs = g.ReturnTokenIDs
	limit := g.MaxTokens
	if g.MaxCompletionTokens != nil {
		limit = g.MaxCompletionTokens
	}
	j.target, j.limit = s.cfg.DefaultMaxTokens, "stop"
	if limit != nil {
		if *limit < 1 {
			return badRequest("max_tokens", "max_tokens must be at least 1, got %d.", *limit)
		}
		j.target, j.limit = *limit, "length"
	}
	j.n = 1
	if g.N != nil {
		if *g.N < 1 || *g.N > 128 {
			return badRequest("n", "n must be between 1 and 128, got %d.", *g.N)
		}
		j.n = *g.N
	}
	for _, st := range g.Stop {
		if st == "" {
			return badRequest("stop", "stop strings must not be empty.")
		}
	}
	j.stops = g.Stop
	return nil
}

// Builds the sequences of a job, queues them, and answers as a stream or one JSON body
func (s *Stub) generate(w http.ResponseWriter, r *http.Request, j job) {
	if j.model == "" {
		j.model = s.cfg.Model
	}
	g := &group{id: s.requestID(r), model: j.model, chat: j.chat, limit: j.limit, stops: j.stops, notify: make(chan struct{}, 1)}
	for _, st := range j.stops {
		g.maxStop = max(g.maxStop, len(st))
	}
	limit := s.cfg.NumGPUBlocks * s.cfg.BlockSize
	var echo []string
	for _, p := range j.prompts {
		if len(p.ids) == 0 {
			writeError(w, badRequest("prompt", "The prompt must contain at least one token."))
			return
		}
		if ceilDiv(len(p.ids)+j.target, s.cfg.BlockSize) > s.cfg.NumGPUBlocks {
			writeError(w, badRequest("max_tokens", "This model's maximum context length is %d tokens. However, you requested %d tokens (%d in the prompt, %d in the completion). Please reduce the length of the prompt or completion.",
				limit, len(p.ids)+j.target, len(p.ids), j.target))
			return
		}
		hashes := blockHashes(j.model, p.ids, s.cfg.BlockSize)
		for c := 0; c < j.n; c++ {
			g.seqs = append(g.seqs, &seq{g: g, lead: c == 0, ids: p.ids, hashes: hashes, target: j.target})
			if j.echo {
				echo = append(echo, p.text)
			}
		}
	}
	g.arrival = time.Now()
	if err := s.eng.submit(g); err != nil {
		writeError(w, err)
		return
	}
	c := &chunker{id: "cmpl-" + g.id, object: "text_completion", created: g.arrival.Unix(), model: j.model, chat: j.chat, usage: j.usage, echo: echo}
	if j.tokenIDs {
		c.pieceIDs = s.pieceIDs
		for _, q := range g.seqs {
			c.promptIDs = append(c.promptIDs, q.ids)
		}
	}
	if j.chat {
		c.id, c.object = "chatcmpl-"+g.id, "chat.completion.chunk"
	}
	if j.stream {
		s.stream(w, r, g, c)
		return
	}
	s.respond(w, r, g, c)
}

// Waits for every sequence and answers with one completion body
func (s *Stub) respond(w http.ResponseWriter, r *http.Request, g *group, c *chunker) {
	n := len(g.seqs)
	sent := make([]int, n)
	texts := make([]strings.Builder, n)
	ids := make([][]uint32, n)
	for {
		select {
		case <-g.notify:
		case <-r.Context().Done():
			s.eng.cancel(g)
			return
		}
		v := s.eng.view(g, sent, 0)
		if v.err != nil {
			writeError(w, v.err)
			return
		}
		for i := range n {
			for _, p := range v.pieces[i] {
				texts[i].WriteString(p)
				if c.pieceIDs == nil {
					continue
				}
				more, err := c.pieceIDs(p)
				if err != nil {
					s.eng.cancel(g)
					writeError(w, err)
					return
				}
				ids[i] = append(ids[i], more...)
			}
		}
		if !v.done {
			continue
		}
		body := completion{ID: c.id, Object: "text_completion", Created: c.created, Model: c.model, Choices: make([]any, n), Usage: v.usage}
		for i := range n {
			reason := v.reasons[i]
			if c.chat {
				body.Choices[i] = messageChoice{Index: i, Message: message{Role: "assistant", Content: texts[i].String()}, FinishReason: reason, TokenIDs: ids[i]}
				continue
			}
			text := texts[i].String()
			if c.echo != nil {
				text = c.echo[i] + text
			}
			body.Choices[i] = textChoice{Index: i, Text: text, FinishReason: &reason, PromptTokenIDs: c.promptOf(i), TokenIDs: ids[i]}
		}
		if c.chat {
			body.Object = "chat.completion"
			if c.promptIDs != nil {
				body.PromptTokenIDs = c.promptOf(0)
			}
		}
		writeJSON(w, http.StatusOK, body)
		return
	}
}

// Streams one chunk per token as server sent events, then optional usage and the done marker
func (s *Stub) stream(w http.ResponseWriter, r *http.Request, g *group, c *chunker) {
	rc := http.NewResponseController(w)
	n := len(g.seqs)
	sent := make([]int, n)
	opened := make([]bool, n)
	closed := make([]bool, n)
	started := false
	hold := max(0, g.maxStop-1)
	for {
		select {
		case <-g.notify:
		case <-r.Context().Done():
			s.eng.cancel(g)
			return
		}
		v := s.eng.view(g, sent, hold)
		if v.err != nil {
			if !started {
				writeError(w, v.err)
				return
			}
			writeEvent(w, v.err.body())
			io.WriteString(w, "data: [DONE]\n\n")
			rc.Flush()
			return
		}
		var events []any
		for i := range n {
			if len(v.pieces[i]) == 0 && (!v.finished[i] || closed[i]) {
				continue
			}
			if !opened[i] {
				if ev := c.open(i); ev != nil {
					events = append(events, ev)
				}
				opened[i] = true
			}
			for _, p := range v.pieces[i] {
				ev, err := c.piece(i, p)
				if err != nil {
					s.eng.cancel(g)
					if !started {
						writeError(w, err)
						return
					}
					writeEvent(w, err.body())
					io.WriteString(w, "data: [DONE]\n\n")
					rc.Flush()
					return
				}
				events = append(events, ev)
			}
			if v.finished[i] && !closed[i] {
				events = append(events, c.finish(i, v.reasons[i]))
				closed[i] = true
			}
		}
		if v.done && c.usage {
			events = append(events, c.final(v.usage))
		}
		if !started && (len(events) > 0 || v.done) {
			h := w.Header()
			h.Set("Content-Type", "text/event-stream")
			h.Set("Cache-Control", "no-cache")
			w.WriteHeader(http.StatusOK)
			started = true
		}
		for _, ev := range events {
			if err := writeEvent(w, ev); err != nil {
				s.eng.cancel(g)
				return
			}
		}
		if v.done {
			io.WriteString(w, "data: [DONE]\n\n")
		}
		rc.Flush()
		if v.done {
			return
		}
	}
}

// Writes one server sent event carrying a JSON value
func writeEvent(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = w.Write(append(append([]byte("data: "), b...), '\n', '\n'))
	return err
}

// Builds the bodies of one response for chat or text completions
type chunker struct {
	id      string
	object  string
	created int64
	model   string
	chat    bool
	usage   bool
	echo    []string
	// The prompt ids per sequence and the ids of each piece, set when the request asked for token ids
	promptIDs  [][]uint32
	pieceIDs   func(string) ([]uint32, *apiError)
	sentPrompt []bool
}

// The prompt ids of a sequence once, nil after they went out or when none were asked for
func (c *chunker) promptOf(i int) []uint32 {
	if c.promptIDs == nil || i >= len(c.promptIDs) {
		return nil
	}
	if c.sentPrompt == nil {
		c.sentPrompt = make([]bool, len(c.promptIDs))
	}
	if c.sentPrompt[i] {
		return nil
	}
	c.sentPrompt[i] = true
	return c.promptIDs[i]
}

// Streaming envelope around choices with a null usage when usage arrives at the end
func (c *chunker) chunk(choices ...any) chunk {
	ch := chunk{ID: c.id, Object: c.object, Created: c.created, Model: c.model, Choices: choices}
	if c.usage {
		ch.Usage = json.RawMessage("null")
	}
	return ch
}

// The role chunk for chat or the echoed prompt for text, nil when a text choice opens with nothing
func (c *chunker) open(i int) any {
	if c.chat {
		empty := ""
		ch := c.chunk(deltaChoice{Index: i, Delta: delta{Role: "assistant", Content: &empty}})
		if i == 0 {
			ch.PromptTokenIDs = c.promptOf(0)
		}
		return ch
	}
	if c.echo != nil {
		return c.chunk(textChoice{Index: i, Text: c.echo[i], PromptTokenIDs: c.promptOf(i)})
	}
	return nil
}

// A chunk carrying one token of text, a tokenizer fault spelling its ids answered as a server error
func (c *chunker) piece(i int, text string) (any, *apiError) {
	var ids []uint32
	if c.pieceIDs != nil {
		var err *apiError
		if ids, err = c.pieceIDs(text); err != nil {
			return nil, err
		}
	}
	if c.chat {
		return c.chunk(deltaChoice{Index: i, Delta: delta{Content: &text}, TokenIDs: ids}), nil
	}
	return c.chunk(textChoice{Index: i, Text: text, PromptTokenIDs: c.promptOf(i), TokenIDs: ids}), nil
}

// A chunk closing a choice with its finish reason
func (c *chunker) finish(i int, reason string) any {
	if c.chat {
		return c.chunk(deltaChoice{Index: i, FinishReason: &reason})
	}
	return c.chunk(textChoice{Index: i, FinishReason: &reason})
}

// The last chunk with no choices and the request usage
func (c *chunker) final(u usage) any {
	b, _ := json.Marshal(u)
	return chunk{ID: c.id, Object: c.object, Created: c.created, Model: c.model, Choices: []any{}, Usage: b}
}
