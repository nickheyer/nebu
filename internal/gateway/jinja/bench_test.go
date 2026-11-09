package jinja

import (
	"encoding/json"
	"testing"

	"github.com/nickheyer/nebu/pkg/chattemplate"
	"github.com/nickheyer/nebu/pkg/transcode/vllm"
)

// The three largest corpus templates by source size
var benchTemplates = []string{"gptoss.jinja", "commanda.jinja", "mistralsmall32.jinja"}

// Renders each of the largest templates, compiled once, over the fixture conversation with parallel tool calls
func BenchmarkRender(b *testing.B) {
	var fx fixtures
	readJSON(b, &fx, "fixtures.json")
	messages := fx.Conversations["multi_tool_calls"]
	for _, name := range benchTemplates {
		b.Run(name, func(b *testing.B) {
			tmpl, err := Compile(&chattemplate.Template{Text: string(readTestdata(b, "templates", name)), Source: name})
			if err != nil {
				b.Fatal(err)
			}
			out, err := tmpl.Render(messages, fx.Tools["tools"], true)
			if err != nil {
				b.Fatal(err)
			}
			b.SetBytes(int64(len(out)))
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if _, err := tmpl.Render(messages, fx.Tools["tools"], true); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// Lexes, parses and compiles each of the largest templates
func BenchmarkParse(b *testing.B) {
	for _, name := range benchTemplates {
		b.Run(name, func(b *testing.B) {
			src := string(readTestdata(b, "templates", name))
			b.SetBytes(int64(len(src)))
			b.ReportAllocs()
			for b.Loop() {
				if _, err := parse(src); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// Renders Qwen2.5's template over a 20 message conversation with tools through the public API
func BenchmarkQwen25Render20(b *testing.B) {
	tmpl, err := Compile(&chattemplate.Template{Text: string(readTestdata(b, "templates", "qwen25.jinja")), Source: "qwen25.jinja"})
	if err != nil {
		b.Fatal(err)
	}
	var fx fixtures
	readJSON(b, &fx, "fixtures.json")
	messages := benchConversation()
	if len(messages) != 20 {
		b.Fatalf("%d messages, want 20", len(messages))
	}
	out, err := tmpl.Render(messages, fx.Tools["tools"], true)
	if err != nil {
		b.Fatal(err)
	}
	b.SetBytes(int64(len(out)))
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if _, err := tmpl.Render(messages, fx.Tools["tools"], true); err != nil {
			b.Fatal(err)
		}
	}
}

// A system prompt then user, assistant and tool turns, every third assistant turn calling a tool
func benchConversation() []vllm.Message {
	text := func(s string) []vllm.ContentPart { return []vllm.ContentPart{{Type: "text", Text: s}} }
	messages := []vllm.Message{{Role: "system", Content: text("You are a helpful assistant that answers questions about the weather.")}}
	for i := 0; len(messages) < 20; i++ {
		messages = append(messages, vllm.Message{Role: "user", Content: text("What is the weather like in city number " + string(rune('A'+i)) + " today?")})
		if i%3 == 2 && len(messages) <= 17 {
			args, _ := json.Marshal(map[string]any{"city": string(rune('A' + i)), "unit": "celsius"})
			id := "call0000" + string(rune('0'+i%10))
			messages = append(messages,
				vllm.Message{Role: "assistant", ToolCalls: []vllm.ToolCall{{ID: id, Type: "function", Function: vllm.FunctionCall{Name: "get_weather", Arguments: string(args)}}}},
				vllm.Message{Role: "tool", ToolCallID: id, Content: text(`{"temperature": 21, "condition": "cloudy"}`)},
			)
			continue
		}
		if len(messages) < 20 {
			messages = append(messages, vllm.Message{Role: "assistant", Content: text("It is mild and partly cloudy with a light breeze from the west.")})
		}
	}
	return messages
}
