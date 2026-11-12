// Renders Hugging Face chat templates exactly as transformers renders them for vLLM
package jinja

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/nickheyer/nebu/pkg/chattemplate/source"
	"github.com/nickheyer/nebu/pkg/transcode/vllm"
)

// The clock strftime_now reads, the local wall time as Python's datetime.now gives it
var now = time.Now

// A compiled chat template, the tool use variant included when the source names one
type Template struct {
	text    *program
	toolUse *program
}

// One compiled template text with what vLLM derives from it before rendering
type program struct {
	body      []stmt
	openai    bool
	developer bool
}

// Compiles the template text and its tool use variant
func Compile(t *source.Template) (*Template, error) {
	if t == nil {
		return nil, errors.New("no chat template to compile")
	}
	text, err := compileProgram(t.Text)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", t.Source, err)
	}
	out := &Template{text: text}
	if t.ToolUse != "" {
		if out.toolUse, err = compileProgram(t.ToolUse); err != nil {
			return nil, fmt.Errorf("%s tool use variant: %w", t.Source, err)
		}
	}
	return out, nil
}

func compileProgram(source string) (*program, error) {
	body, err := parse(source)
	if err != nil {
		return nil, err
	}
	return &program{
		body:      body,
		openai:    detectOpenAIFormat(body),
		developer: strings.Contains(source, `"developer"`) || strings.Contains(source, `'developer'`),
	}, nil
}

// Renders with no template arguments, implementing vllm.Renderer
func (t *Template) Render(messages []vllm.Message, tools []vllm.Tool, addGenerationPrompt bool) (string, error) {
	return t.render(nil, messages, tools, addGenerationPrompt)
}

// A renderer that passes chat_template_kwargs to the template as top level variables
func (t *Template) With(kwargs map[string]any) vllm.Renderer {
	return &kwargsRenderer{t: t, kwargs: kwargs}
}

type kwargsRenderer struct {
	t      *Template
	kwargs map[string]any
}

func (k *kwargsRenderer) Render(messages []vllm.Message, tools []vllm.Tool, addGenerationPrompt bool) (string, error) {
	return k.t.render(k.kwargs, messages, tools, addGenerationPrompt)
}

// Keys vLLM or transformers refuse in chat_template_kwargs
var refusedKwargs = map[string]string{
	"chat_template": "Found unexpected chat template kwargs from request: {'chat_template'}",
	"tokenize":      "Found unexpected chat template kwargs from request: {'tokenize'}",
	"messages":      "render() got multiple values for keyword argument 'messages'",
	"conversation":  "safe_apply_chat_template() got multiple values for argument 'conversation'",
	"model_config":  "safe_apply_chat_template() got multiple values for argument 'model_config'",
	"tokenizer":     "safe_apply_chat_template() got multiple values for argument 'tokenizer'",
}

// Keys the request's own fields override, so the kwarg never reaches the template
var overriddenKwargs = map[string]bool{"tools": true, "add_generation_prompt": true, "continue_final_message": true}

// Keys apply_chat_template consumes for tokenization without passing them to the template
var tokenizationKwargs = map[string]bool{"padding": true, "truncation": true, "max_length": true, "return_tensors": true, "return_dict": true, "tokenizer_kwargs": true}

// Keys that change tokenization in a way rendered text cannot carry
var tokenOnlyKwargs = map[string]bool{"sanitize_control_tokens": true, "return_assistant_tokens_mask": true}

func (t *Template) render(kwargs map[string]any, messages []vllm.Message, tools []vllm.Tool, addGenerationPrompt bool) (string, error) {
	if len(messages) == 0 {
		return "", &Error{Kind: "ValueError", Msg: "Cannot apply chat template to an empty conversation. Provide at least one message."}
	}
	p := t.text
	if len(tools) > 0 && t.toolUse != nil {
		p = t.toolUse
	}
	conversation, err := p.conversation(messages)
	if err != nil {
		return "", err
	}
	toolValues, err := toolDicts(tools)
	if err != nil {
		return "", err
	}
	vars := map[string]Value{
		"bos_token": "",
		"eos_token": "",
		"documents": none,
	}
	for _, k := range sortedKeys(kwargs) {
		if msg, refused := refusedKwargs[k]; refused {
			return "", errors.New(msg)
		}
		if overriddenKwargs[k] || tokenizationKwargs[k] {
			continue
		}
		v, err := goValue(kwargs[k])
		if err != nil {
			return "", fmt.Errorf("chat_template_kwargs[%q]: %w", k, err)
		}
		if tokenOnlyKwargs[k] {
			if truthy(v) {
				return "", fmt.Errorf("chat_template_kwargs %s changes tokenization, which a rendered prompt cannot carry", k)
			}
			continue
		}
		vars[k] = v
	}
	if err := checkDocuments(vars["documents"]); err != nil {
		return "", err
	}
	vars["messages"] = conversation
	vars["tools"] = toolValues
	vars["add_generation_prompt"] = addGenerationPrompt
	return render(p.body, vars, now)
}

// Refuses documents that are not a list of dicts, as transformers does
func checkDocuments(docs Value) error {
	if _, isNone := docs.(NoneType); isNone {
		return nil
	}
	items, err := iterValues(docs)
	if err != nil {
		return err
	}
	for _, d := range items {
		if _, ok := d.(*Dict); !ok {
			return &Error{Kind: "TypeError", Msg: "Documents should be a list of dicts with 'title' and 'text' keys!"}
		}
	}
	return nil
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

const billingHeader = "x-anthropic-billing-header"

// The conversation vLLM hands transformers, content shaped by the template's detected format
func (p *program) conversation(messages []vllm.Message) (*List, error) {
	out := &List{}
	hasDeveloper := false
	for _, m := range messages {
		parts := m.Content
		if m.Role == "system" {
			parts = stripBillingHeader(parts)
		}
		d := newDict()
		d.put("role", m.Role)
		if p.openai {
			items := make([]Value, len(parts))
			for i, part := range parts {
				pd := newDict()
				pd.put("type", "text")
				pd.put("text", part.Text)
				items[i] = pd
			}
			d.put("content", &List{items: items})
		} else {
			var texts []string
			for _, part := range parts {
				if part.Text != "" {
					texts = append(texts, part.Text)
				}
			}
			d.put("content", strings.Join(texts, "\n"))
		}
		switch m.Role {
		case "assistant":
			if len(m.ToolCalls) > 0 {
				calls, err := toolCallDicts(m.ToolCalls)
				if err != nil {
					return nil, err
				}
				d.put("tool_calls", calls)
			}
		case "tool":
			if m.ToolCallID != "" {
				d.put("tool_call_id", m.ToolCallID)
			}
			if p.openai {
				texts := make([]string, len(parts))
				for i, part := range parts {
					texts[i] = part.Text
				}
				d.put("content", strings.Join(texts, "\n"))
			}
		case "developer":
			hasDeveloper = true
		}
		if m.Name != "" {
			d.put("name", m.Name)
		}
		if m.Role == "developer" {
			d.put("tools", none)
		}
		out.items = append(out.items, d)
	}
	if hasDeveloper && !p.developer {
		return consolidateSystem(out), nil
	}
	return out, nil
}

// Drops Claude Code's billing header line from system text parts as vLLM does
func stripBillingHeader(parts []vllm.ContentPart) []vllm.ContentPart {
	var out []vllm.ContentPart
	for _, part := range parts {
		if !strings.HasPrefix(part.Text, billingHeader) {
			out = append(out, part)
			continue
		}
		_, rest, _ := strings.Cut(part.Text, "\n")
		if rest == "" {
			continue
		}
		out = append(out, vllm.ContentPart{Type: part.Type, Text: rest})
	}
	return out
}

// Turns developer messages into system messages and merges all system messages into the first position
func consolidateSystem(conv *List) *List {
	converted := make([]Value, len(conv.items))
	for i, it := range conv.items {
		d := it.(*Dict)
		if role, _ := d.lookup("role"); role == "developer" {
			nd := newDict()
			for j, k := range d.keys {
				switch k {
				case "role":
					nd.put("role", "system")
				case "tools":
				default:
					_ = nd.set(k, d.vals[j])
				}
			}
			d = nd
		}
		converted[i] = d
	}
	var contents []string
	var rest []Value
	needed := false
	for i, it := range converted {
		d := it.(*Dict)
		if role, _ := d.lookup("role"); role != "system" {
			rest = append(rest, d)
			continue
		}
		if i > 0 || len(contents) > 0 {
			needed = true
		}
		content, _ := d.lookup("content")
		text := ""
		switch c := content.(type) {
		case string:
			text = c
		case *List:
			var parts []string
			for _, p := range c.items {
				if pd, ok := p.(*Dict); ok {
					if t, found := pd.lookup("text"); found {
						parts = append(parts, t.(string))
					}
				}
			}
			text = strings.Join(parts, "\n")
		}
		if text != "" {
			contents = append(contents, text)
		}
	}
	if !needed {
		return &List{items: converted}
	}
	merged := newDict()
	merged.put("role", "system")
	merged.put("content", strings.Join(contents, "\n\n"))
	return &List{items: append([]Value{merged}, rest...)}
}

// Tool calls as vLLM validates them, arguments decoded from their JSON text into a dict
func toolCallDicts(calls []vllm.ToolCall) (*List, error) {
	out := &List{}
	for _, c := range calls {
		if c.Type != "" && c.Type != "function" {
			return nil, &Error{Kind: "ValueError", Msg: "chat completions only support assistant tool_calls of type 'function'."}
		}
		var args Value = newDict()
		if c.Function.Arguments != "" {
			v, err := jsonLoads(c.Function.Arguments)
			if err != nil {
				return nil, fmt.Errorf("tool call %s arguments: %w", c.ID, err)
			}
			if _, ok := v.(*Dict); !ok {
				return nil, fmt.Errorf("tool call %s arguments decode to %s, not a JSON object", c.ID, typeName(v))
			}
			args = v
		}
		fn := newDict()
		fn.put("arguments", args)
		fn.put("name", c.Function.Name)
		d := newDict()
		d.put("id", c.ID)
		d.put("function", fn)
		d.put("type", "function")
		out.items = append(out.items, d)
	}
	return out, nil
}

// Tools as vLLM dumps them, None when the request offers none
func toolDicts(tools []vllm.Tool) (Value, error) {
	if len(tools) == 0 {
		return none, nil
	}
	out := &List{}
	for _, t := range tools {
		if t.Type != "" && t.Type != "function" {
			return nil, fmt.Errorf("tool %s has type %q, only function tools are supported", t.Function.Name, t.Type)
		}
		fn := newDict()
		fn.put("name", t.Function.Name)
		if t.Function.Description == "" {
			fn.put("description", none)
		} else {
			fn.put("description", t.Function.Description)
		}
		var params Value = none
		if raw := strings.TrimSpace(string(t.Function.Parameters)); raw != "" {
			v, err := jsonLoads(raw)
			if err != nil {
				return nil, fmt.Errorf("tool %s parameters: %w", t.Function.Name, err)
			}
			params = v
		}
		fn.put("parameters", params)
		d := newDict()
		d.put("type", "function")
		d.put("function", fn)
		out.items = append(out.items, d)
	}
	return out, nil
}

// Converts a Go value from decoded JSON or code into the template value Python would hold
func goValue(v any) (Value, error) {
	switch x := v.(type) {
	case nil:
		return none, nil
	case json.RawMessage:
		return jsonLoads(string(x))
	case json.Number:
		return jsonLoads(string(x))
	case string:
		return x, nil
	case bool:
		return x, nil
	case float64:
		if x == math.Trunc(x) && !math.IsInf(x, 0) && math.Abs(x) < 1<<63 {
			return int64(x), nil
		}
		return x, nil
	case float32:
		return goValue(float64(x))
	case *big.Int:
		return normInt(new(big.Int).Set(x)), nil
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.String:
		return rv.String(), nil
	case reflect.Bool:
		return rv.Bool(), nil
	case reflect.Float32, reflect.Float64:
		return goValue(rv.Float())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return rv.Int(), nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return normInt(new(big.Int).SetUint64(rv.Uint())), nil
	case reflect.Slice, reflect.Array:
		l := &List{}
		for i := 0; i < rv.Len(); i++ {
			item, err := goValue(rv.Index(i).Interface())
			if err != nil {
				return nil, err
			}
			l.items = append(l.items, item)
		}
		return l, nil
	case reflect.Map:
		if rv.Type().Key().Kind() != reflect.String {
			return nil, fmt.Errorf("map keys must be strings, not %s", rv.Type().Key())
		}
		keys := rv.MapKeys()
		sort.Slice(keys, func(i, j int) bool { return keys[i].String() < keys[j].String() })
		d := newDict()
		for _, k := range keys {
			item, err := goValue(rv.MapIndex(k).Interface())
			if err != nil {
				return nil, err
			}
			d.put(k.String(), item)
		}
		return d, nil
	case reflect.Pointer, reflect.Interface:
		if rv.IsNil() {
			return none, nil
		}
		return goValue(rv.Elem().Interface())
	}
	return nil, fmt.Errorf("cannot pass a %T to a template", v)
}
