package chat

import (
	"encoding/json"
	"strconv"
	"strings"
)

// A chat, completions, or embeddings request as OpenAI clients write it
type oaiRequest struct {
	Model       string
	Messages    []oaiMessage
	Prompt      []Part
	Input       []Part
	MaxTokens   int
	Temperature *float64
	TopP        *float64
	TopK        *int
	N           int
	Stop        []string
	Stream      bool
	// The include_usage flag of stream_options
	IncludeUsage bool
	Tools        []oaiTool
	// The choice in the neutral form, a mode or a tool name
	ToolChoice string
	Extra      map[string]json.RawMessage
	model      [2]int
}

func (r *oaiRequest) read(s *scan) error {
	return s.object(func(k []byte) error {
		var err error
		switch string(k) {
		case "model":
			at := s.i
			r.Model, err = s.str()
			r.model = [2]int{at, s.i}
		case "messages":
			err = s.array(func() error {
				r.Messages = append(r.Messages, oaiMessage{})
				return r.Messages[len(r.Messages)-1].read(s)
			})
		case "prompt":
			r.Prompt, err = s.prompts()
		case "input":
			r.Input, err = s.prompts()
		case "max_tokens", "max_completion_tokens":
			var n int
			n, err = s.integer()
			r.MaxTokens = max(r.MaxTokens, n)
		case "temperature":
			r.Temperature, err = s.optFloat()
		case "top_p":
			r.TopP, err = s.optFloat()
		case "top_k":
			r.TopK, err = s.optInt()
		case "n":
			r.N, err = s.integer()
		case "stop":
			r.Stop, err = s.strs()
		case "stream":
			r.Stream, err = s.boolean()
		case "stream_options":
			err = s.object(func(k []byte) error {
				if string(k) != "include_usage" {
					return s.skip()
				}
				var err error
				r.IncludeUsage, err = s.boolean()
				return err
			})
		case "tools":
			err = s.array(func() error {
				r.Tools = append(r.Tools, oaiTool{})
				return r.Tools[len(r.Tools)-1].read(s)
			})
		case "tool_choice":
			var tc toolChoiceWire
			if tc, err = readToolChoice(s); err == nil {
				r.ToolChoice = tc.chat()
			}
		default:
			err = s.extra(&r.Extra, k)
		}
		return err
	})
}

func (r *oaiRequest) appendTo(b *[]byte) {
	o := begin(b)
	o.str("model", r.Model)
	if r.Messages != nil {
		o.key("messages")
		appendList(b, r.Messages, (*oaiMessage).appendTo)
	}
	if r.Prompt != nil {
		o.key("prompt")
		appendPrompts(b, r.Prompt)
	}
	if r.Input != nil {
		o.key("input")
		appendList(b, r.Input, func(p *Part, b *[]byte) {
			if p.Type == "tokens" {
				appendTokens(b, p.Tokens)
				return
			}
			*b = appendStr(*b, p.Text)
		})
	}
	o.numIf("max_tokens", r.MaxTokens)
	o.float("temperature", r.Temperature)
	o.float("top_p", r.TopP)
	if r.TopK != nil {
		o.num("top_k", *r.TopK)
	}
	o.numIf("n", r.N)
	o.strList("stop", r.Stop)
	if r.Stream {
		o.boolean("stream", true)
		if r.IncludeUsage {
			o.raw("stream_options", []byte(`{"include_usage":true}`))
		}
	}
	if len(r.Tools) > 0 {
		o.key("tools")
		appendList(b, r.Tools, (*oaiTool).appendTo)
	}
	switch r.ToolChoice {
	case "":
	case "auto", "none", "required":
		o.str("tool_choice", r.ToolChoice)
	default:
		o.key("tool_choice")
		*b = append(*b, `{"type":"function","function":{"name":`...)
		*b = appendStr(*b, r.ToolChoice)
		*b = append(*b, '}', '}')
	}
	o.close(r.Extra)
}

// A tool choice as either API writes it, a mode or an object naming a tool or allowed tools
type toolChoiceWire struct {
	mode, kind, name, allowed string
	object                    bool
}

func readToolChoice(s *scan) (toolChoiceWire, error) {
	var tc toolChoiceWire
	if s.peek() == '"' {
		var err error
		tc.mode, err = s.str()
		return tc, err
	}
	tc.object = s.peek() == '{'
	err := s.object(func(k []byte) error {
		var err error
		switch string(k) {
		case "type":
			tc.kind, err = s.str()
		case "name":
			tc.name, err = s.str()
		case "mode":
			tc.allowed, err = s.str()
		case "function", "custom":
			err = s.object(func(k []byte) error {
				if string(k) != "name" {
					return s.skip()
				}
				var err error
				tc.name, err = s.str()
				return err
			})
		case "allowed_tools":
			err = s.object(func(k []byte) error {
				if string(k) != "mode" {
					return s.skip()
				}
				var err error
				tc.allowed, err = s.str()
				return err
			})
		default:
			err = s.skip()
		}
		return err
	})
	return tc, err
}

// The neutral choice a chat completions tool_choice makes, any mode string passing as it is
func (tc toolChoiceWire) chat() string {
	switch {
	case !tc.object:
		return tc.mode
	case tc.kind == "function" || tc.kind == "custom":
		return tc.name
	case tc.kind == "allowed_tools" && tc.allowed == "required":
		return "required"
	case tc.kind == "allowed_tools":
		return "auto"
	}
	return ""
}

// One chat message, from a client, in a runtime's answer, or as a stream delta
type oaiMessage struct {
	Role string
	// Absent or null when nil
	Content    *oaiContent
	ToolCalls  []oaiToolCall
	ToolCallID string
	// Reasoning under the llama.cpp name and the newer vLLM name
	ReasoningContent string
	Reasoning        string
	Extra            map[string]json.RawMessage
}

// Message content, a bare string or a list of parts
type oaiContent struct {
	Text  string
	Parts []oaiPart
	List  bool
}

// The reasoning a message carries under either name
func (m *oaiMessage) reasoning() string {
	if m.ReasoningContent != "" {
		return m.ReasoningContent
	}
	return m.Reasoning
}

// The words content holds, a string as it is or the text parts joined
func (c *oaiContent) text() string {
	if c == nil {
		return ""
	}
	if !c.List {
		return c.Text
	}
	var b strings.Builder
	for _, p := range c.Parts {
		if p.Type == "text" {
			b.WriteString(p.Text)
		}
	}
	return b.String()
}

func (m *oaiMessage) read(s *scan) error {
	return s.object(func(k []byte) error {
		var err error
		switch string(k) {
		case "role":
			m.Role, err = s.str()
		case "content":
			m.Content, err = readContent(s)
		case "tool_calls":
			err = s.array(func() error {
				m.ToolCalls = append(m.ToolCalls, oaiToolCall{})
				return m.ToolCalls[len(m.ToolCalls)-1].read(s)
			})
		case "tool_call_id":
			m.ToolCallID, err = s.str()
		case "reasoning_content":
			m.ReasoningContent, err = s.str()
		case "reasoning":
			m.Reasoning, err = s.str()
		default:
			err = s.extra(&m.Extra, k)
		}
		return err
	})
}

// Reads content as null, a string, or a list of parts
func readContent(s *scan) (*oaiContent, error) {
	switch s.peek() {
	case 'n':
		s.null()
		return nil, nil
	case '"':
		t, err := s.str()
		return &oaiContent{Text: t}, err
	case '[':
		c := &oaiContent{List: true}
		err := s.array(func() error {
			c.Parts = append(c.Parts, oaiPart{})
			return c.Parts[len(c.Parts)-1].read(s)
		})
		return c, err
	}
	return nil, s.fail("message content must be a string or a list of parts")
}

func (m *oaiMessage) appendTo(b *[]byte) {
	o := begin(b)
	o.strIf("role", m.Role)
	if m.Content != nil {
		o.key("content")
		if m.Content.List {
			appendList(b, m.Content.Parts, (*oaiPart).appendTo)
		} else {
			*b = appendStr(*b, m.Content.Text)
		}
	}
	if len(m.ToolCalls) > 0 {
		o.key("tool_calls")
		appendList(b, m.ToolCalls, (*oaiToolCall).appendTo)
	}
	o.strIf("tool_call_id", m.ToolCallID)
	o.strIf("reasoning_content", m.ReasoningContent)
	o.strIf("reasoning", m.Reasoning)
	o.close(m.Extra)
}

// A content part, text or an image URL with its detail
type oaiPart struct {
	Type   string
	Text   string
	URL    string
	Detail string
	Extra  map[string]json.RawMessage
}

func (p *oaiPart) read(s *scan) error {
	return s.object(func(k []byte) error {
		var err error
		switch string(k) {
		case "type":
			p.Type, err = s.str()
		case "text":
			p.Text, err = s.str()
		case "image_url":
			if s.peek() == '"' {
				p.URL, err = s.str()
				return err
			}
			err = s.object(func(k []byte) error {
				var err error
				switch string(k) {
				case "url":
					p.URL, err = s.str()
				case "detail":
					p.Detail, err = s.str()
				default:
					err = s.skip()
				}
				return err
			})
		default:
			err = s.extra(&p.Extra, k)
		}
		return err
	})
}

// Writes the field its type calls for, so a text part always carries text
func (p *oaiPart) appendTo(b *[]byte) {
	o := begin(b)
	o.str("type", p.Type)
	switch p.Type {
	case "text":
		o.str("text", p.Text)
	case "image_url":
		o.key("image_url")
		img := begin(b)
		img.str("url", p.URL)
		img.strIf("detail", p.Detail)
		img.close(nil)
	}
	o.close(p.Extra)
}

type oaiToolCall struct {
	Index     *int
	ID        string
	Type      string
	Name      string
	Arguments string
}

func (t *oaiToolCall) read(s *scan) error {
	return s.object(func(k []byte) error {
		var err error
		switch string(k) {
		case "index":
			t.Index, err = s.optInt()
		case "id":
			t.ID, err = s.str()
		case "type":
			t.Type, err = s.str()
		case "function":
			err = s.object(func(k []byte) error {
				var err error
				switch string(k) {
				case "name":
					t.Name, err = s.str()
				case "arguments":
					t.Arguments, err = readArgs(s)
				default:
					err = s.skip()
				}
				return err
			})
		default:
			err = s.skip()
		}
		return err
	})
}

// Reads tool arguments as a string of JSON, an object written in place being taken as its text
func readArgs(s *scan) (string, error) {
	if s.peek() == '{' {
		raw, err := s.raw()
		return string(raw), err
	}
	return s.str()
}

func (t *oaiToolCall) appendTo(b *[]byte) {
	o := begin(b)
	if t.Index != nil {
		o.num("index", *t.Index)
	}
	o.strIf("id", t.ID)
	o.strIf("type", t.Type)
	o.key("function")
	fn := begin(b)
	fn.strIf("name", t.Name)
	fn.strIf("arguments", t.Arguments)
	fn.close(nil)
	o.close(nil)
}

type oaiTool struct {
	Type     string
	Function oaiFunction
	Extra    map[string]json.RawMessage
}

func (t *oaiTool) read(s *scan) error {
	return s.object(func(k []byte) error {
		var err error
		switch string(k) {
		case "type":
			t.Type, err = s.str()
		case "function":
			err = t.Function.read(s)
		default:
			err = s.extra(&t.Extra, k)
		}
		return err
	})
}

func (t *oaiTool) appendTo(b *[]byte) {
	o := begin(b)
	o.str("type", t.Type)
	o.key("function")
	t.Function.appendTo(b)
	o.close(t.Extra)
}

type oaiFunction struct {
	Name        string
	Description string
	Parameters  json.RawMessage
	Strict      *bool
	Extra       map[string]json.RawMessage
}

func (f *oaiFunction) read(s *scan) error {
	return s.object(func(k []byte) error {
		var err error
		switch string(k) {
		case "name":
			f.Name, err = s.str()
		case "description":
			f.Description, err = s.str()
		case "parameters":
			f.Parameters, err = s.raw()
		case "strict":
			f.Strict, err = s.optBool()
		default:
			err = s.extra(&f.Extra, k)
		}
		return err
	})
}

func (f *oaiFunction) appendTo(b *[]byte) {
	o := begin(b)
	o.str("name", f.Name)
	o.strIf("description", f.Description)
	o.raw("parameters", f.Parameters)
	if f.Strict != nil {
		o.boolean("strict", *f.Strict)
	}
	o.close(f.Extra)
}

// Prompt and completion tokens as either API reports them, with the share the cache served
type oaiUsage struct {
	In, Out, Cached int
}

func (u *oaiUsage) read(s *scan) error {
	return s.object(func(k []byte) error {
		var err error
		switch string(k) {
		case "prompt_tokens", "input_tokens":
			u.In, err = s.integer()
		case "completion_tokens", "output_tokens":
			u.Out, err = s.integer()
		case "prompt_tokens_details", "input_tokens_details":
			err = s.object(func(k []byte) error {
				if string(k) != "cached_tokens" {
					return s.skip()
				}
				var err error
				u.Cached, err = s.integer()
				return err
			})
		default:
			err = s.skip()
		}
		return err
	})
}

// Writes chat completions usage, the details object only when the cache served part of the prompt
func appendUsage(b *[]byte, u Usage) {
	o := begin(b)
	o.num("prompt_tokens", u.In)
	o.num("completion_tokens", u.Out)
	o.num("total_tokens", u.In+u.Out)
	if u.Cached > 0 {
		o.key("prompt_tokens_details")
		d := begin(b)
		d.num("cached_tokens", u.Cached)
		d.close(nil)
	}
	o.close(nil)
}

// Per request timings a llama.cpp server adds, with the prompt cache and speculative decoding counters
type oaiTimings struct {
	CacheN, DraftN, DraftNAccepted int
}

func (t *oaiTimings) read(s *scan) error {
	return s.object(func(k []byte) error {
		var err error
		switch string(k) {
		case "cache_n":
			t.CacheN, err = s.integer()
		case "draft_n":
			t.DraftN, err = s.integer()
		case "draft_n_accepted":
			t.DraftNAccepted, err = s.integer()
		default:
			err = s.skip()
		}
		return err
	})
}

type oaiChoice struct {
	Index        int
	Message      *oaiMessage
	Delta        *oaiMessage
	Text         string
	FinishReason *string
	// The ids of the choice's tokens and of its prompt, as vLLM and SGLang answer return_token_ids
	TokenIDs       []int
	PromptTokenIDs []int
}

func (c *oaiChoice) read(s *scan) error {
	return s.object(func(k []byte) error {
		var err error
		switch string(k) {
		case "index":
			c.Index, err = s.integer()
		case "message":
			c.Message = &oaiMessage{}
			err = c.Message.read(s)
		case "delta":
			c.Delta = &oaiMessage{}
			err = c.Delta.read(s)
		case "text":
			c.Text, err = s.str()
		case "finish_reason":
			c.FinishReason, err = s.optStr()
		case "token_ids", "response_token_ids":
			c.TokenIDs, err = s.tokens()
		case "prompt_token_ids":
			c.PromptTokenIDs, err = s.tokens()
		default:
			err = s.skip()
		}
		return err
	})
}

// Anything an OpenAI runtime answers, a completion, a chunk, embeddings, an error, or a Responses object or event
type oaiAnswer struct {
	ID      string
	Object  string
	Model   string
	Choices []oaiChoice
	Usage   *oaiUsage
	Timings *oaiTimings
	Data    [][]float64
	Extra   map[string]json.RawMessage
	// The prompt ids a chat completion carries at its top level when the request asked for token ids
	PromptTokenIDs []int
	// The error member's message, or its raw text when it names none, and whether the member was set
	Error    string
	HasError bool
	// The message of a vLLM error object or a Responses error event
	Message string
	// The type of a Responses event, empty for the chat shapes
	Type     string
	Response *respAnswer
	Item     *respOutItem
	// Members of Responses events
	Delta       string
	ItemID      string
	OutputIndex int
	Arguments   string
	Code        string
	// Members of a Responses object read at the top level
	whole respAnswer
}

func (a *oaiAnswer) read(s *scan) error {
	return s.object(func(k []byte) error {
		var err error
		switch string(k) {
		case "id":
			a.ID, err = s.str()
		case "object":
			a.Object, err = s.str()
		case "model":
			a.Model, err = s.str()
		case "created", "created_at", "sequence_number", "content_index", "summary_index", "logprobs", "obfuscation", "param", "part":
			err = s.skip()
		case "choices":
			a.Choices = []oaiChoice{}
			err = s.array(func() error {
				a.Choices = append(a.Choices, oaiChoice{})
				return a.Choices[len(a.Choices)-1].read(s)
			})
		case "usage":
			a.Usage = &oaiUsage{}
			err = a.Usage.read(s)
		case "timings":
			a.Timings = &oaiTimings{}
			err = a.Timings.read(s)
		case "prompt_token_ids":
			a.PromptTokenIDs, err = s.tokens()
		case "data":
			err = s.array(func() error {
				var vec []float64
				err := s.object(func(k []byte) error {
					if string(k) != "embedding" {
						return s.skip()
					}
					var err error
					vec, err = s.vector()
					return err
				})
				a.Data = append(a.Data, vec)
				return err
			})
		case "error":
			a.Error, a.HasError, err = readFailure(s)
		case "message":
			a.Message, err = s.str()
		case "type":
			a.Type, err = s.str()
		case "response":
			a.Response = &respAnswer{}
			err = a.Response.read(s)
		case "item":
			a.Item = &respOutItem{}
			err = a.Item.read(s)
		case "delta":
			a.Delta, err = s.str()
		case "item_id":
			a.ItemID, err = s.str()
		case "output_index":
			a.OutputIndex, err = s.integer()
		case "arguments":
			a.Arguments, err = readArgs(s)
		case "code":
			a.Code, err = readCode(s)
		case "status", "output", "incomplete_details":
			err = a.whole.member(s, k)
		default:
			err = s.extra(&a.Extra, k)
		}
		return err
	})
}

// Reads an error member, a string, an object whose message it takes, or anything else as its raw text
func readFailure(s *scan) (string, bool, error) {
	switch s.peek() {
	case 'n':
		s.null()
		return "", false, nil
	case '"':
		t, err := s.str()
		return t, true, err
	case '{':
		start := s.i
		message := ""
		err := s.object(func(k []byte) error {
			if string(k) != "message" {
				return s.skip()
			}
			var err error
			message, err = s.str()
			return err
		})
		if message == "" {
			message = strings.TrimSpace(string(s.b[start:s.i]))
		}
		return message, true, err
	}
	raw, err := s.raw()
	return strings.TrimSpace(string(raw)), true, err
}

// Reads an error code written as a string or a number
func readCode(s *scan) (string, error) {
	if s.peek() == '"' || s.peek() == 'n' {
		return s.str()
	}
	lit, err := s.number()
	return string(lit), err
}

// The message of an error a runtime put in a 200 body or chunk, empty for an answer
func (a *oaiAnswer) failure(body []byte) string {
	switch {
	case a.HasError && a.Type != "error":
		return a.Error
	case a.Object == "error" && a.Message != "":
		return a.Message
	case a.Object == "error":
		return strings.TrimSpace(string(body))
	}
	return ""
}

// The Responses object the answer is when its object says so, nil otherwise
func (a *oaiAnswer) responseObject() *respAnswer {
	if a.Object != "response" {
		return nil
	}
	r := a.whole
	r.ID, r.Model, r.Usage = a.ID, a.Model, a.Usage
	if a.HasError {
		r.Error = a.Error
	}
	return &r
}

// A whole chat or completions answer written to a client
type oaiReply struct {
	ID      string
	Object  string
	Created int64
	Model   string
	Choices []oaiReplyChoice
	Usage   *Usage
	Extra   map[string]json.RawMessage
	// The prompt token ids, written when the request asked for token ids
	PromptTokenIDs []int
}

type oaiReplyChoice struct {
	Index   int
	Message *oaiMessage
	Delta   *oaiMessage
	// Set for completions, whose choices always carry their text
	Text         *string
	FinishReason *string
	// The ids of the choice's tokens and, on a completions choice, of its prompt, written when asked for token ids
	TokenIDs       []int
	PromptTokenIDs []int
}

// Writes an int list member
func appendInts(b *[]byte, ids []int) {
	*b = append(*b, '[')
	for i, id := range ids {
		if i > 0 {
			*b = append(*b, ',')
		}
		*b = strconv.AppendInt(*b, int64(id), 10)
	}
	*b = append(*b, ']')
}

func (r *oaiReply) appendTo(b *[]byte) {
	o := begin(b)
	o.str("id", r.ID)
	o.str("object", r.Object)
	o.num("created", int(r.Created))
	o.str("model", r.Model)
	o.key("choices")
	appendList(b, r.Choices, (*oaiReplyChoice).appendTo)
	if r.Usage != nil {
		o.key("usage")
		appendUsage(b, *r.Usage)
	}
	if r.PromptTokenIDs != nil {
		o.key("prompt_token_ids")
		appendInts(b, r.PromptTokenIDs)
	}
	o.close(r.Extra)
}

func (r *oaiReply) json() []byte {
	b := make([]byte, 0, 256)
	r.appendTo(&b)
	return b
}

func (c *oaiReplyChoice) appendTo(b *[]byte) {
	o := begin(b)
	o.num("index", c.Index)
	if c.Message != nil {
		o.key("message")
		c.Message.appendTo(b)
	}
	if c.Delta != nil {
		o.key("delta")
		c.Delta.appendTo(b)
	}
	if c.Text != nil {
		o.str("text", *c.Text)
	}
	o.key("finish_reason")
	if c.FinishReason == nil {
		*b = append(*b, "null"...)
	} else {
		*b = appendStr(*b, *c.FinishReason)
	}
	if c.TokenIDs != nil {
		o.key("token_ids")
		appendInts(b, c.TokenIDs)
	}
	if c.PromptTokenIDs != nil {
		o.key("prompt_token_ids")
		appendInts(b, c.PromptTokenIDs)
	}
	o.close(nil)
}

// Writes a vector of floats
func appendVector(b *[]byte, v []float64) {
	*b = append(*b, '[')
	for i, f := range v {
		if i > 0 {
			*b = append(*b, ',')
		}
		*b = appendFloat(*b, f)
	}
	*b = append(*b, ']')
}
