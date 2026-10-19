package chat

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// The request Claude Code sends under server side review, with the check it asks for
const reviewBody = `{"model":"m","max_tokens":50,"messages":[{"role":"user","content":"list the dir"}],"tools":[{"name":"Bash","description":"Runs","input_schema":{"type":"object"}}],"safeguards":[{"type":"dangerous_tool_use","classifier_context":{"v":1,"permission_mode":"auto"}}]}`

// A check goes on to an Anthropic runtime only while the gateway has not answered it, and never to an OpenAI one
func TestChecksForwarded(t *testing.T) {
	req, err := (anthropic{}).Parse(AnthropicMessages, []byte(reviewBody))
	if err != nil {
		t.Fatal(err)
	}
	if len(req.Checks) != 1 || req.Checks[0].Type != DangerousToolUse {
		t.Fatalf("parsed %v", req.Checks)
	}
	_, body, err := (anthropic{}).Render(req)
	if err != nil || !strings.Contains(string(body), `"safeguards":[{"type":"dangerous_tool_use"`) {
		t.Fatalf("forwarded %s: %v", body, err)
	}
	req.Checks[0].Verdict = Allowed
	_, body, _ = (anthropic{}).Render(req)
	if strings.Contains(string(body), "safeguards") {
		t.Fatalf("an answered check was forwarded: %s", body)
	}
	_, body, _ = (openai{}).Render(req)
	if strings.Contains(string(body), "safeguards") {
		t.Fatalf("the check reached an OpenAI body: %s", body)
	}
}

// Server tools parse as provided tools with their limits, MCP endpoints as sources, and the API's rules hold
func TestAnthropicProvidedTools(t *testing.T) {
	body := func(tools string) []byte {
		return []byte(fmt.Sprintf(`{"model":"m","max_tokens":10,"messages":[{"role":"user","content":"hi"}],"tools":[%s],"mcp_servers":[{"type":"url","url":"http://127.0.0.1:1/mcp","name":"tools","authorization_token":"tok","tool_configuration":{"allowed_tools":["echo"]}}]}`, tools))
	}
	req, err := (anthropic{}).Parse(AnthropicMessages, body(`{"type":"web_search_20250305","name":"web_search","max_uses":8,"allowed_domains":["a.example"],"user_location":{"type":"approximate","country":"US"}},{"type":"web_fetch_20250910","name":"web_fetch","max_content_tokens":100},{"name":"Read","input_schema":{"type":"object"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(req.Tools) != 3 || req.Tools[2].Provided != nil {
		t.Fatalf("tools %v", req.Tools)
	}
	if s := req.Tools[0].Provided; s == nil || s.Kind != v1.ToolServerKind_TOOL_SERVER_KIND_SEARCH || s.MaxUses != 8 || s.Country != "US" || strings.Join(s.Allowed, ",") != "a.example" {
		t.Fatalf("search limits %v", s)
	}
	if f := req.Tools[1].Provided; f == nil || f.Kind != v1.ToolServerKind_TOOL_SERVER_KIND_FETCH || f.MaxChars != 400 || f.MaxUses != ProvidedUses {
		t.Fatalf("fetch limits %v", f)
	}
	if len(req.Sources) != 1 || req.Sources[0].Name != "tools" || req.Sources[0].Token != "tok" || strings.Join(req.Sources[0].Allowed, ",") != "echo" {
		t.Fatalf("sources %v", req.Sources)
	}
	for tools, want := range map[string]string{
		`{"type":"web_search_20250305","name":"web_search"},{"name":"web_search","input_schema":{"type":"object"}}`: "belongs to the web search tool",
		`{"type":"web_search_20250305","name":"web_search"},{"type":"web_search_20260301","name":"web_search"}`:     "one web search tool",
		`{"type":"web_search_20250305","name":"search"}`:                                                            "named web_search",
		`{"type":"web_search_20250305","name":"web_search","max_uses":0}`:                                           "at least 1",
		`{"type":"web_search_20250305","name":"web_search","allowed_domains":["a"],"blocked_domains":["b"]}`:        "not both",
	} {
		if _, err := (anthropic{}).Parse(AnthropicMessages, body(tools)); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%s: %v", want, err)
		}
	}
}

// Context overflow refusals are reworded the way agent clients compact on, and sized ones give the room left
func TestPromptTooLong(t *testing.T) {
	for message, reworded := range map[string]bool{
		"This model's maximum context length is 32768 tokens. However, you requested 40000 tokens.":   true,
		"request (50000 tokens) exceeds the available context size (32768 tokens), try increasing it": true,
		"prompt is too long: 213462 tokens > 200000 maximum":                                          false,
		"unknown model": false,
	} {
		out := PromptTooLong(message)
		if reworded && out != "prompt is too long: "+message || !reworded && out != message {
			t.Fatalf("%q became %q", message, out)
		}
	}
	if room, ok := CompletionRoom("This model's maximum context length is 32768 tokens. However, you requested 40000 tokens (30000 in the messages, 10000 in the completion)."); !ok || room != 2768 {
		t.Fatalf("room %d %v", room, ok)
	}
	if _, ok := CompletionRoom("request (50000 tokens) exceeds the available context size (32768 tokens)"); ok {
		t.Fatal("a prompt that alone overflows leaves no room")
	}
}

// The call parser splits text from calls as pieces arrive, takes fences and the parameters
// spelling, and leaves a block that holds no call as text
func TestCallParser(t *testing.T) {
	run := func(pieces ...string) (string, []ToolCall) {
		var text strings.Builder
		var calls []ToolCall
		p := &CallParser{Emit: func(ev Event) error {
			if ev.Kind == Said {
				text.WriteString(ev.Text)
			} else {
				calls = append(calls, *ev.Tool)
			}
			return nil
		}}
		for _, s := range pieces {
			p.Text(s)
		}
		p.Flush()
		return text.String(), calls
	}
	text, calls := run("Hi <", "tool_cal", `l>{"name":"a","arguments":{"x":1}}</tool_call> bye <tool_call>`, "```json\n{\"name\":\"b\",\"parameters\":\"{\\\"y\\\":2}\"}\n```", "</tool_call>")
	if text != "Hi  bye " || len(calls) != 2 || calls[0].Name != "a" || calls[0].Args != `{"x":1}` || calls[1].Name != "b" || calls[1].Args != `{"y":2}` {
		t.Fatalf("%q %v", text, calls)
	}
	text, calls = run("<tool_call>not json</tool_call> and <tool_call>{\"name\":\"c\"}")
	if text != "<tool_call>not json</tool_call> and " || len(calls) != 1 || calls[0].Name != "c" || calls[0].Args != "{}" {
		t.Fatalf("%q %v", text, calls)
	}
	text, calls = run("<tool_call>never closed")
	if text != "<tool_call>never closed" || len(calls) != 0 {
		t.Fatalf("%q %v", text, calls)
	}
	text, _ = run("a < b and <tool", "s> are fine")
	if text != "a < b and <tools> are fine" {
		t.Fatalf("%q", text)
	}
}

// An Anthropic request renders to OpenAI and back without losing turns, calls, or tool results
func TestRoundTrip(t *testing.T) {
	body := `{"model":"m","max_tokens":50,"system":"be brief","messages":[
		{"role":"user","content":"read my notes"},
		{"role":"assistant","content":[{"type":"thinking","thinking":"hm","signature":"s"},{"type":"text","text":"Reading."},{"type":"tool_use","id":"t1","name":"Read","input":{"path":"notes.txt"}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"buy milk"},{"type":"text","text":"thanks"}]}],
		"tools":[{"name":"Read","description":"Reads a file","input_schema":{"type":"object","properties":{"path":{"type":"string"}}}}],"tool_choice":{"type":"any"}}`
	req, err := (anthropic{}).Parse(AnthropicMessages, []byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if len(req.Messages) != 5 || req.Messages[0].Role != "system" || req.Messages[3].Role != "tool" || req.Messages[3].ToolID != "t1" || req.ToolChoice != "required" {
		t.Fatalf("parsed %+v", req.Messages)
	}
	path, out, err := (openai{}).Render(req)
	if err != nil || path != OpenAIChat {
		t.Fatalf("render %s: %v", path, err)
	}
	var sent struct {
		Messages []struct {
			Role       string          `json:"role"`
			Content    json.RawMessage `json:"content"`
			Reasoning  string          `json:"reasoning_content"`
			ToolCallID string          `json:"tool_call_id"`
			ToolCalls  []struct {
				ID string `json:"id"`
			} `json:"tool_calls"`
		} `json:"messages"`
		ToolChoice string `json:"tool_choice"`
	}
	if err := json.Unmarshal(out, &sent); err != nil {
		t.Fatal(err)
	}
	if len(sent.Messages) != 5 || sent.Messages[2].Reasoning != "hm" || sent.Messages[2].ToolCalls[0].ID != "t1" || sent.Messages[3].ToolCallID != "t1" || string(sent.Messages[3].Content) != `"buy milk"` || sent.ToolChoice != "required" {
		t.Fatalf("sent %s", out)
	}
	back, err := (openai{}).Parse(OpenAIChat, out)
	if err != nil {
		t.Fatal(err)
	}
	if _, rendered, err := (anthropic{}).Render(back); err != nil || !strings.Contains(string(rendered), `"tool_use_id":"t1"`) || !strings.Contains(string(rendered), `"thinking":"hm"`) {
		t.Fatalf("back %s: %v", rendered, err)
	}
}

// A request the size a coding agent sends, with fields the gateway has no mapping for
func agentBody(turns int) []byte {
	var tools []map[string]any
	for i := range 20 {
		tools = append(tools, map[string]any{"name": fmt.Sprintf("tool_%d", i), "description": strings.Repeat("Does a thing. ", 40), "input_schema": map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string", "description": strings.Repeat("where ", 50)}}}})
	}
	var messages []map[string]any
	messages = append(messages, map[string]any{"role": "user", "content": "start"})
	for i := range turns {
		messages = append(messages,
			map[string]any{"role": "assistant", "content": []map[string]any{{"type": "text", "text": "Reading."}, {"type": "tool_use", "id": fmt.Sprintf("t%d", i), "name": "tool_1", "input": map[string]any{"path": "a/b.go"}}}},
			map[string]any{"role": "user", "content": []map[string]any{{"type": "tool_result", "tool_use_id": fmt.Sprintf("t%d", i), "content": []map[string]any{{"type": "text", "text": strings.Repeat("line of code\n", 200), "cache_control": map[string]any{"type": "ephemeral"}}}}}})
	}
	body, _ := json.Marshal(map[string]any{
		"model": "m", "max_tokens": 32000, "stream": true,
		"system":   []map[string]any{{"type": "text", "text": "x-anthropic-billing-header"}, {"type": "text", "text": strings.Repeat("You are an agent. ", 2000), "cache_control": map[string]any{"type": "ephemeral"}}},
		"messages": messages, "tools": tools, "metadata": map[string]any{"user_id": "u"}, "thinking": map[string]any{"type": "adaptive"},
	})
	return body
}

func BenchmarkParseAgentRequest(b *testing.B) {
	body := agentBody(200)
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	for b.Loop() {
		if _, err := (anthropic{}).Parse(AnthropicMessages, body); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRenderAgentRequest(b *testing.B) {
	req, err := (anthropic{}).Parse(AnthropicMessages, agentBody(200))
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		if _, _, err := (openai{}).Render(req); err != nil {
			b.Fatal(err)
		}
	}
}
