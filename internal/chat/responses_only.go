package chat

import (
	"cmp"
	"encoding/json"
	"slices"
	"strings"
)

// What a provider serving the Responses API alone asks of the requests rendered for it
type ResponsesRules struct {
	// The namespace function tools go grouped under, empty for a provider taking them at the top level
	Namespace string
	// The instructions a request bringing none goes with, empty for a provider taking a request without any
	Instructions string
}

// The OpenAI protocol toward a provider serving chats over the Responses API alone, as ChatGPT plans are served. Every
// request is rendered for /v1/responses streamed and stored nowhere, whatever the client spoke and whether or not it asked
// for a stream, its leading system turns as instructions and later ones as developer turns. What such a provider refuses
// stays out: max_output_tokens, temperature, top_p, and what chat completions alone defines, stop and seed among it.
type responsesOnly struct {
	openai
	rules ResponsesRules
}

// The runtime side of the OpenAI protocol for a provider serving the Responses API alone
func ResponsesOnly(rules ResponsesRules) Runtime { return responsesOnly{rules: rules} }

// One choice a call and no tokenizer, the provider serving neither n nor a count
func (responsesOnly) Traits() Traits { return Traits{Responses: true, ResponsesOnly: true} }

// A Responses request of the client's own passes as it is, any other needing rendering
func (r responsesOnly) Native(req *Request) (string, []byte, error) {
	if o := req.Origin; o != nil && o.Path != OpenAIResponses {
		return "", nil, bad("the provider serves %s alone, so the request needs translating", OpenAIResponses)
	}
	return native(r, req)
}

// The message of an error object with the parameter it blames, or the detail a refusal ahead of the stream carries instead
func (responsesOnly) ErrorMessage(body []byte) string {
	var message, param, detail string
	decode(body, func(s *scan) error {
		return s.object(func(k []byte) error {
			switch {
			case string(k) == "detail" && s.peek() == '"':
				var err error
				detail, err = s.str()
				return err
			case string(k) != "error" || s.peek() != '{':
				return s.skip()
			}
			return s.object(func(k []byte) error {
				var err error
				switch {
				case string(k) == "message" && s.peek() == '"':
					message, err = s.str()
				case string(k) == "param" && s.peek() == '"':
					param, err = s.str()
				default:
					err = s.skip()
				}
				return err
			})
		})
	})
	if message != "" && param != "" && !strings.Contains(message, param) {
		message += ", param " + param
	}
	return cmp.Or(message, detail)
}

// Fields beside the mapped ones a provider serving the Responses API alone still takes as the client wrote them
var responsesKept = []string{"parallel_tool_calls", "prompt_cache_key"}

func (r responsesOnly) Render(c *Request) (string, []byte, error) {
	if err := translatable(c); err != nil {
		return "", nil, err
	}
	if err := noAudio(c, "the Responses API"); err != nil {
		return "", nil, err
	}
	switch {
	case c.Kind == Embed:
		return "", nil, bad("this sign-in is served chats alone, over the Responses API, so embeddings take a key for the provider")
	case c.Kind == Count:
		return "", nil, bad("this sign-in is served chats alone, over the Responses API, which counts no tokens")
	case c.Kind == Generate && c.Raw:
		return "", nil, bad("this sign-in is served chats alone, over the Responses API, which takes no raw prompt, so completions take a key for the provider")
	case c.N > 1:
		return "", nil, bad("the Responses API answers one choice a call, so n=%d takes a call per choice", c.N)
	}
	b := renderBuffer(c)
	o := begin(&b)
	o.str("model", c.Model)
	instructions, turns := r.lead(c.Messages)
	o.strIf("instructions", instructions)
	o.key("input")
	if err := r.appendInput(&b, turns); err != nil {
		return "", nil, err
	}
	if len(c.Tools) > 0 {
		if i := slices.IndexFunc(c.Tools, func(t Tool) bool { return t.Type != "" && t.Type != "custom" }); i >= 0 {
			return "", nil, bad("tool %s has type %s, and the provider is offered only tools defined by a schema", c.Tools[i].Name, c.Tools[i].Type)
		}
		o.key("tools")
		r.appendTools(&b, c.Tools)
	}
	switch c.ToolChoice {
	case "":
	case "auto", "none", "required":
		o.str("tool_choice", c.ToolChoice)
	default:
		o.key("tool_choice")
		b = append(b, `{"type":"function","name":`...)
		b = appendStr(b, c.ToolChoice)
		b = append(b, '}')
	}
	// The summary is what a client is shown of the reasoning, which the provider keeps to itself unless asked
	if effort := extraString(c.Extra, "reasoning_effort"); effort != "" {
		o.key("reasoning")
		w := begin(&b)
		w.str("effort", effort)
		w.str("summary", "auto")
		w.close(nil)
	}
	format, err := textFormat(c.Extra["response_format"])
	if err != nil {
		return "", nil, err
	}
	if verbosity := extraString(c.Extra, "verbosity"); format != nil || verbosity != "" {
		o.key("text")
		w := begin(&b)
		w.raw("format", format)
		w.strIf("verbosity", verbosity)
		w.close(nil)
	}
	for _, k := range responsesKept {
		if v := c.Extra[k]; present(v) {
			o.raw(k, v)
		}
	}
	o.boolean("store", false)
	o.boolean("stream", true)
	o.close(nil)
	return OpenAIResponses, b, nil
}

// The system turns a chat leads with as instructions, the provider's own when it leads with none, and the turns after them
func (r responsesOnly) lead(msgs []Message) (string, []Message) {
	var texts []string
	i := 0
	for ; i < len(msgs) && (msgs[i].Role == "system" || msgs[i].Role == "developer"); i++ {
		if text := TextOf(msgs[i].Parts); text != "" {
			texts = append(texts, text)
		}
	}
	if len(texts) == 0 {
		return r.rules.Instructions, msgs[i:]
	}
	return strings.Join(texts, "\n\n"), msgs[i:]
}

// Writes turns as input items: a message by role, a later system turn as a developer's, an assistant's calls as function
// calls after its text, and a tool turn as the output of its call. Reasoning stays behind, the provider taking back only
// reasoning items of its own.
func (r responsesOnly) appendInput(b *[]byte, msgs []Message) error {
	*b = append(*b, '[')
	n := 0
	item := func(kind string) obj {
		if n > 0 {
			*b = append(*b, ',')
		}
		n++
		o := begin(b)
		o.str("type", kind)
		return o
	}
	for _, m := range msgs {
		if hasTokens(m.Parts) {
			return bad("a chat turn carries text, not token ids")
		}
		switch m.Role {
		case "tool":
			o := item("function_call_output")
			o.str("call_id", m.ToolID)
			o.key("output")
			appendInputContent(b, m.Parts)
			o.close(nil)
		case "assistant":
			if text := TextOf(m.Parts); text != "" {
				o := item("message")
				o.str("role", "assistant")
				o.str("content", text)
				o.close(nil)
			}
			for _, call := range m.ToolCalls {
				o := item("function_call")
				o.str("call_id", call.ID)
				o.str("name", call.Name)
				o.strIf("namespace", r.rules.Namespace)
				o.str("arguments", cmp.Or(call.Args, "{}"))
				o.close(nil)
			}
		default:
			role := m.Role
			if role == "system" {
				role = "developer"
			}
			o := item("message")
			o.str("role", role)
			o.key("content")
			appendInputContent(b, m.Parts)
			o.close(nil)
		}
	}
	*b = append(*b, ']')
	return nil
}

// Writes a turn's text and images as input content, a bare string for text alone
func appendInputContent(b *[]byte, parts []Part) {
	if !slices.ContainsFunc(parts, func(p Part) bool { return p.Type == "image" }) {
		*b = appendStr(*b, TextOf(parts))
		return
	}
	*b = append(*b, '[')
	n := 0
	for _, p := range parts {
		if p.Type != "text" && p.Type != "image" {
			continue
		}
		if n > 0 {
			*b = append(*b, ',')
		}
		n++
		o := begin(b)
		if p.Type == "text" {
			o.str("type", "input_text")
			o.str("text", p.Text)
		} else {
			o.str("type", "input_image")
			if p.URL != "" {
				o.str("image_url", p.URL)
			} else {
				o.str("image_url", "data:"+p.MediaType+";base64,"+p.Data)
			}
			o.strIf("detail", p.Detail)
		}
		o.close(nil)
	}
	*b = append(*b, ']')
}

// Writes tools as functions, flat as the Responses API lists them and under the provider's namespace when it groups them.
// A tool with no schema takes any object, and one the client did not mark strict is not held to its schema, as chat
// completions has it.
func (r responsesOnly) appendTools(b *[]byte, tools []Tool) {
	*b = append(*b, '[')
	var group obj
	if r.rules.Namespace != "" {
		group = begin(b)
		group.str("type", "namespace")
		group.str("name", r.rules.Namespace)
		group.str("description", "Functions the client offers")
		group.key("tools")
		*b = append(*b, '[')
	}
	for i, t := range tools {
		if i > 0 {
			*b = append(*b, ',')
		}
		o := begin(b)
		o.str("type", "function")
		o.str("name", t.Name)
		o.strIf("description", t.Description)
		if present(t.Schema) {
			o.raw("parameters", t.Schema)
		} else {
			o.raw("parameters", []byte(`{"type":"object","properties":{}}`))
		}
		o.boolean("strict", t.Strict != nil && *t.Strict)
		o.close(nil)
	}
	if r.rules.Namespace != "" {
		*b = append(*b, ']')
		group.close(nil)
	}
	*b = append(*b, ']')
}

// A string among a request's unmapped fields, empty when the field is absent or no string
func extraString(extra map[string]json.RawMessage, key string) string {
	var s string
	json.Unmarshal(extra[key], &s)
	return s
}

// The Responses text format a chat completions response_format asks for, nil for plain text
func textFormat(raw json.RawMessage) (json.RawMessage, error) {
	if !present(raw) {
		return nil, nil
	}
	var format struct {
		Type   string `json:"type"`
		Schema *struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			Schema      json.RawMessage `json:"schema"`
			Strict      *bool           `json:"strict"`
		} `json:"json_schema"`
	}
	if err := json.Unmarshal(raw, &format); err != nil {
		return nil, bad("response_format must be an object naming its type")
	}
	switch format.Type {
	case "", "text":
		return nil, nil
	case "json_object":
		return json.RawMessage(`{"type":"json_object"}`), nil
	case "json_schema":
		if format.Schema == nil || !present(format.Schema.Schema) {
			return nil, bad("response_format of type json_schema needs a schema")
		}
		var b []byte
		o := begin(&b)
		o.str("type", "json_schema")
		o.str("name", cmp.Or(format.Schema.Name, "response"))
		o.strIf("description", format.Schema.Description)
		o.raw("schema", format.Schema.Schema)
		if format.Schema.Strict != nil {
			o.boolean("strict", *format.Schema.Strict)
		}
		o.close(nil)
		return b, nil
	}
	return nil, bad("response_format type %q is not text, json_object, or json_schema", format.Type)
}
