package jinja

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nickheyer/nebu/pkg/chattemplate"
	"github.com/nickheyer/nebu/pkg/transcode/vllm"
)

func compileText(t *testing.T, text string) *Template {
	t.Helper()
	tmpl, err := Compile(&chattemplate.Template{Text: text, Source: "test"})
	if err != nil {
		t.Fatal(err)
	}
	return tmpl
}

func userMessage(text string) vllm.Message {
	return vllm.Message{Role: "user", Content: []vllm.ContentPart{{Type: "text", Text: text}}}
}

// The template's raise_exception message comes back as the render error
func TestRaiseExceptionSurfaces(t *testing.T) {
	tmpl := compileText(t, `{% if messages[0].role == 'system' %}{{ raise_exception('System role not supported') }}{% endif %}ok`)
	tests := []struct {
		name    string
		role    string
		want    string
		wantErr string
	}{
		{name: "raises", role: "system", wantErr: "System role not supported"},
		{name: "passes", role: "user", want: "ok"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			msg := userMessage("x")
			msg.Role = tc.role
			got, err := tmpl.Render([]vllm.Message{msg}, nil, true)
			if tc.wantErr != "" {
				if err == nil || err.Error() != tc.wantErr {
					t.Fatalf("error %v, want %q", err, tc.wantErr)
				}
				var te *Error
				if !errors.As(err, &te) || te.Kind != "TemplateError" {
					t.Fatalf("error %#v is not a TemplateError", err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("got %q, %v, want %q", got, err, tc.want)
			}
		})
	}
}

// chat_template_kwargs arrive as top level variables with vLLM's precedence rules
func TestKwargsReachTemplate(t *testing.T) {
	tmpl := compileText(t, `{{ bos_token }}{% if enable_thinking is defined and enable_thinking is false %}off{% else %}on{% endif %}|{{ add_generation_prompt }}|{{ tools is none }}|{{ documents }}|{{ extra|default(none)|tojson }}`)
	msgs := []vllm.Message{userMessage("hi")}
	tests := []struct {
		name    string
		kwargs  map[string]any
		want    string
		wantErr string
	}{
		{name: "none", want: "on|True|True|None|null"},
		{name: "bool", kwargs: map[string]any{"enable_thinking": false}, want: "off|True|True|None|null"},
		{name: "raw json", kwargs: map[string]any{"enable_thinking": json.RawMessage("false"), "extra": json.RawMessage(`{"b": 1, "a": [1.0, 2]}`)}, want: "off|True|True|None|{\"b\": 1, \"a\": [1.0, 2]}"},
		{name: "go values", kwargs: map[string]any{"extra": map[string]any{"z": 1.5, "a": []any{"x", nil, true, float64(3)}}}, want: "on|True|True|None|{\"a\": [\"x\", null, true, 3], \"z\": 1.5}"},
		{name: "number", kwargs: map[string]any{"extra": json.Number("2.0")}, want: "on|True|True|None|2.0"},
		{name: "special tokens", kwargs: map[string]any{"bos_token": "<s>"}, want: "<s>on|True|True|None|null"},
		{name: "documents", kwargs: map[string]any{"documents": []any{map[string]any{"title": "t", "text": "x"}}}, want: "on|True|True|[{'text': 'x', 'title': 't'}]|null"},
		{name: "request fields win", kwargs: map[string]any{"add_generation_prompt": false, "tools": []any{"x"}, "continue_final_message": true}, want: "on|True|True|None|null"},
		{name: "tokenization only", kwargs: map[string]any{"return_dict": true, "padding": true, "sanitize_control_tokens": false}, want: "on|True|True|None|null"},
		{name: "sanitize refused", kwargs: map[string]any{"sanitize_control_tokens": true}, wantErr: "sanitize_control_tokens"},
		{name: "chat_template refused", kwargs: map[string]any{"chat_template": "x"}, wantErr: "chat_template"},
		{name: "messages refused", kwargs: map[string]any{"messages": []any{}}, wantErr: "messages"},
		{name: "bad documents", kwargs: map[string]any{"documents": "text"}, wantErr: "Documents should be a list of dicts"},
		{name: "bad type", kwargs: map[string]any{"extra": make(chan int)}, wantErr: "cannot pass"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var r vllm.Renderer = tmpl
			if tc.kwargs != nil {
				r = tmpl.With(tc.kwargs)
			}
			got, err := r.Render(msgs, nil, true)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error %v, want one mentioning %q", err, tc.wantErr)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("got %q, %v, want %q", got, err, tc.want)
			}
		})
	}
}

// The tool use variant renders only when the request offers tools
func TestToolUseVariant(t *testing.T) {
	tool := vllm.Tool{Type: "function", Function: vllm.Function{Name: "f", Parameters: json.RawMessage(`{"type": "object"}`)}}
	tests := []struct {
		name    string
		toolUse string
		tools   []vllm.Tool
		want    string
	}{
		{name: "no tools", toolUse: "variant {{ tools|length }}", want: "default None"},
		{name: "tools", toolUse: "variant {{ tools|tojson }}", tools: []vllm.Tool{tool}, want: `variant [{"type": "function", "function": {"name": "f", "description": null, "parameters": {"type": "object"}}}]`},
		{name: "tools without variant", tools: []vllm.Tool{tool}, want: "default [{'type': 'function', 'function': {'name': 'f', 'description': None, 'parameters': {'type': 'object'}}}]"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tmpl, err := Compile(&chattemplate.Template{Text: "default {{ tools }}", ToolUse: tc.toolUse, Source: "gguf"})
			if err != nil {
				t.Fatal(err)
			}
			got, err := tmpl.Render([]vllm.Message{userMessage("x")}, tc.tools, false)
			if err != nil || got != tc.want {
				t.Fatalf("got %q, %v, want %q", got, err, tc.want)
			}
		})
	}
}

// Message content and tool calls take the shape vLLM gives transformers
func TestMessageShaping(t *testing.T) {
	const stringFormat = `{{ messages|tojson }}`
	const openaiFormat = `{% for m in messages %}{% for c in m.content %}[{{ c.text }}]{% endfor %}{% endfor %}{{ messages|tojson }}`
	parts := vllm.Message{Role: "user", Content: []vllm.ContentPart{{Type: "text", Text: "a"}, {Type: "text", Text: ""}, {Type: "text", Text: "b"}}}
	call := func(args string) vllm.Message {
		return vllm.Message{Role: "assistant", ToolCalls: []vllm.ToolCall{{ID: "c1", Type: "function", Function: vllm.FunctionCall{Name: "f", Arguments: args}}}}
	}
	tests := []struct {
		name     string
		template string
		messages []vllm.Message
		want     string
		wantErr  string
	}{
		{name: "string joins parts", template: stringFormat, messages: []vllm.Message{parts}, want: `[{"role": "user", "content": "a\nb"}]`},
		{name: "openai keeps parts", template: openaiFormat, messages: []vllm.Message{parts}, want: `[a][][b][{"role": "user", "content": [{"type": "text", "text": "a"}, {"type": "text", "text": ""}, {"type": "text", "text": "b"}]}]`},
		{name: "no content", template: stringFormat, messages: []vllm.Message{{Role: "assistant"}}, want: `[{"role": "assistant", "content": ""}]`},
		{name: "arguments decoded", template: stringFormat, messages: []vllm.Message{call(`{"x": 1, "a": [true]}`)}, want: `[{"role": "assistant", "content": "", "tool_calls": [{"id": "c1", "function": {"arguments": {"x": 1, "a": [true]}, "name": "f"}, "type": "function"}]}]`},
		{name: "empty arguments", template: stringFormat, messages: []vllm.Message{call("")}, want: `[{"role": "assistant", "content": "", "tool_calls": [{"id": "c1", "function": {"arguments": {}, "name": "f"}, "type": "function"}]}]`},
		{name: "arguments not json", template: stringFormat, messages: []vllm.Message{call("{oops")}, wantErr: "arguments"},
		{name: "arguments not object", template: stringFormat, messages: []vllm.Message{call("[1]")}, wantErr: "not a JSON object"},
		{name: "tool message", template: openaiFormat, messages: []vllm.Message{{Role: "tool", ToolCallID: "c1", Name: "f", Content: []vllm.ContentPart{{Type: "text", Text: "r1"}, {Type: "text", Text: "r2"}}}}, want: `[][][][][][{"role": "tool", "content": "r1\nr2", "tool_call_id": "c1", "name": "f"}]`},
		{name: "developer converted", template: stringFormat, messages: []vllm.Message{{Role: "developer", Content: []vllm.ContentPart{{Type: "text", Text: "d"}}}, userMessage("u")}, want: `[{"role": "system", "content": "d"}, {"role": "user", "content": "u"}]`},
		{name: "developer kept", template: `{{ messages|tojson }}{# 'developer' #}`, messages: []vllm.Message{{Role: "developer", Content: []vllm.ContentPart{{Type: "text", Text: "d"}}}}, want: `[{"role": "developer", "content": "d", "tools": null}]`},
		{name: "empty conversation", template: stringFormat, wantErr: "empty conversation"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := compileText(t, tc.template).Render(tc.messages, nil, false)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error %v, want one mentioning %q", err, tc.wantErr)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("got %q, %v\nwant %q", got, err, tc.want)
			}
		})
	}
}

// Compilation fails for templates jinja2 refuses
func TestCompileErrors(t *testing.T) {
	tests := []struct {
		name string
		src  *chattemplate.Template
	}{
		{name: "nil", src: nil},
		{name: "syntax", src: &chattemplate.Template{Text: "{% if %}"}},
		{name: "unclosed", src: &chattemplate.Template{Text: "{% for x in y %}"}},
		{name: "unknown filter", src: &chattemplate.Template{Text: "{{ x|nope }}"}},
		{name: "bad tool use variant", src: &chattemplate.Template{Text: "ok", ToolUse: "{{ x"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Compile(tc.src); err == nil {
				t.Fatal("compiled without error")
			}
		})
	}
}

// One template rendered from many goroutines gives every caller the single threaded output
func TestConcurrentRender(t *testing.T) {
	tmpl := compileText(t, string(readTestdata(t, "templates", "qwen3.jinja")))
	tool := vllm.Tool{Type: "function", Function: vllm.Function{Name: "f", Description: "d", Parameters: json.RawMessage(`{"type": "object", "properties": {"a": {"type": "string"}}}`)}}
	conv := func(i int) []vllm.Message {
		return []vllm.Message{userMessage(fmt.Sprintf("question %d", i)), {Role: "assistant", Content: []vllm.ContentPart{{Type: "text", Text: "<think>x</think>answer"}}}, userMessage("again")}
	}
	want := make([]string, 8)
	for i := range want {
		var err error
		if want[i], err = tmpl.With(map[string]any{"enable_thinking": i%2 == 0}).Render(conv(i), []vllm.Tool{tool}, true); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	errs := make(chan error, 64)
	for g := 0; g < 64; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for n := 0; n < 20; n++ {
				i := (g + n) % len(want)
				got, err := tmpl.With(map[string]any{"enable_thinking": i%2 == 0}).Render(conv(i), []vllm.Tool{tool}, true)
				if err != nil || got != want[i] {
					errs <- fmt.Errorf("goroutine %d render %d differs: %v", g, i, err)
					return
				}
			}
		}(g)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

// Concurrent Gets compile once, failures are retried and Forget makes the next Get load again
func TestCache(t *testing.T) {
	var loads atomic.Int32
	failFirst := map[string]bool{"flaky": true}
	var mu sync.Mutex
	cache := NewCache(func(id string) (*chattemplate.Template, error) {
		loads.Add(1)
		time.Sleep(20 * time.Millisecond)
		mu.Lock()
		defer mu.Unlock()
		if failFirst[id] {
			failFirst[id] = false
			return nil, errors.New("not ready")
		}
		return &chattemplate.Template{Text: "{{ messages[0].content }} from " + id, Source: id}, nil
	})
	var wg sync.WaitGroup
	got := make([]*Template, 64)
	for i := range got {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			tmpl, err := cache.Get("a")
			if err != nil {
				t.Error(err)
			}
			got[i] = tmpl
		}(i)
	}
	wg.Wait()
	if n := loads.Load(); n != 1 {
		t.Fatalf("loaded %d times, want 1", n)
	}
	for _, tmpl := range got {
		if tmpl != got[0] {
			t.Fatal("concurrent Gets returned different templates")
		}
	}
	out, err := got[0].Render([]vllm.Message{userMessage("hi")}, nil, false)
	if err != nil || out != "hi from a" {
		t.Fatalf("got %q, %v", out, err)
	}
	again, err := cache.Get("a")
	if err != nil || again != got[0] || loads.Load() != 1 {
		t.Fatalf("second Get reloaded or changed template: %v, loads %d", err, loads.Load())
	}
	cache.Forget("a")
	reloaded, err := cache.Get("a")
	if err != nil || reloaded == got[0] || loads.Load() != 2 {
		t.Fatalf("Get after Forget did not reload: %v, loads %d", err, loads.Load())
	}
	if _, err := cache.Get("flaky"); err == nil {
		t.Fatal("first load of flaky succeeded")
	}
	if _, err := cache.Get("flaky"); err != nil {
		t.Fatalf("failed load was cached: %v", err)
	}
}
