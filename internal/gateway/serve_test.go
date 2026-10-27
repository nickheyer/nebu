package gateway

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nickheyer/nebu/internal/chat"
	"github.com/nickheyer/nebu/pkg/interceptors"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// A gateway serving one OpenAI seat as m under a profile, at a test server
func seatGateway(t *testing.T, model *seat, profile *v1.Profile) (*Gateway, string) {
	t.Helper()
	g, table := newGateway(t)
	table.Set("m", "i1", "", model.URL, "r:g", "m", v1.ApiFlavor_API_FLAVOR_OPENAI, nil, profile)
	srv := httptest.NewServer(g.Handler())
	t.Cleanup(srv.Close)
	return g, srv.URL
}

// The system text the runtime got, its parts joined by a bar, empty when it got no system turn
func systemSent(t *testing.T, body map[string]any) string {
	t.Helper()
	msgs, _ := body["messages"].([]any)
	if len(msgs) == 0 {
		return ""
	}
	first := msgs[0].(map[string]any)
	if first["role"] != "system" {
		return ""
	}
	switch c := first["content"].(type) {
	case string:
		return c
	case []any:
		var out []string
		for _, p := range c {
			out = append(out, p.(map[string]any)["text"].(string))
		}
		return strings.Join(out, "|")
	}
	return ""
}

// The roles of the turns the runtime got
func rolesSent(body map[string]any) []string {
	var out []string
	msgs, _ := body["messages"].([]any)
	for _, m := range msgs {
		out = append(out, m.(map[string]any)["role"].(string))
	}
	return out
}

// An Anthropic request the way a coding agent sends one, with its identity line, attribution block, unmapped fields, and a check
const agentRequest = `{"model":"m","max_tokens":50,"temperature":1,"metadata":{"user_id":"u"},"thinking":{"type":"adaptive"},"safeguards":[{"type":"dangerous_tool_use","classifier_context":{"v":1}}],"system":[{"type":"text","text":"x-anthropic-billing-header: agent"},{"type":"text","text":"You are Claude Code, a cautious agent.","cache_control":{"type":"ephemeral"}}],"messages":[{"role":"user","content":[{"type":"text","text":"You are a security monitor for autonomous AI coding agents. May rm -rf build run?","cache_control":{"type":"ephemeral"}}]}],"tools":[{"name":"Read","description":"Reads","input_schema":{"type":"object"},"input_examples":[{"path":"a"}]}]}`

// An Anthropic client's request reaches an OpenAI runtime as sent, system prompt, unmapped fields, and headers included,
// and the model's own answer comes back, credentials alone staying at the gateway
func TestTranslatedRequestAsSent(t *testing.T) {
	model := newSeat(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) { chatAnswer(w, "<block>yes</block>") })
	g, base := seatGateway(t, model, nil)
	headers := map[string]string{"X-Claude-Code-Session-Id": "s1", "anthropic-beta": "x", "anthropic-version": "2023-06-01", "Authorization": "Bearer secret", "X-Api-Key": "key", "Cookie": "sid=1", "X-Nebu-Model": "m"}
	r, trace := ask(t, g, base, http.MethodPost, chat.AnthropicMessages, agentRequest, headers)
	if r.status != http.StatusOK {
		t.Fatalf("status %d: %s", r.status, r.body)
	}
	if model.count() != 1 {
		t.Fatalf("the runtime was asked %d times", model.count())
	}
	call := model.call(0)
	if got := systemSent(t, call.body); got != "x-anthropic-billing-header: agent|You are Claude Code, a cautious agent." {
		t.Fatalf("system %q", got)
	}
	raw, _ := json.Marshal(call.body)
	for _, field := range []string{`"metadata":{"user_id":"u"}`, `"thinking":{"type":"adaptive"}`, `"safeguards":[{"classifier_context":{"v":1},"type":"dangerous_tool_use"}]`, `"cache_control":{"type":"ephemeral"}`, `"input_examples":[{"path":"a"}]`, "You are a security monitor"} {
		if !strings.Contains(string(raw), field) {
			t.Fatalf("%s did not reach the runtime: %s", field, raw)
		}
	}
	for name, want := range map[string]string{"X-Claude-Code-Session-Id": "s1", "Anthropic-Beta": "x", "Anthropic-Version": "2023-06-01", "Authorization": "", "X-Api-Key": "", "Cookie": "", "X-Nebu-Model": ""} {
		if got := call.header.Get(name); got != want {
			t.Fatalf("header %s reached the runtime as %q, want %q", name, got, want)
		}
	}
	_, blocks := blocksOf(t, r.body)
	if len(blocks) != 1 || blocks[0]["text"] != "<block>yes</block>" {
		t.Fatalf("blocks %v", blocks)
	}
	if !trace.GetTranslated() || trace.GetResponse() != "<block>yes</block>" {
		t.Fatalf("trace translated %v response %q", trace.GetTranslated(), trace.GetResponse())
	}
}

// A same protocol request passes through as sent, the model renamed to what the runtime serves, with the client's headers minus credentials
func TestPassthroughAsSent(t *testing.T) {
	model := newSeat(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) { chatAnswer(w, "ok") })
	g, table := newGateway(t)
	table.Set("m", "i1", "", model.URL, "r:g", "real", v1.ApiFlavor_API_FLAVOR_OPENAI, nil, nil)
	srv := httptest.NewServer(g.Handler())
	t.Cleanup(srv.Close)
	body := `{"model":"m","min_p":0.05,"messages":[{"role":"system","content":"be brief"},{"role":"user","content":"hi"},{"role":"system","content":"now be long"}]}`
	headers := map[string]string{"anthropic-beta": "x", "Authorization": "Bearer secret", "X-Nebu-Model": "m"}
	r, trace := ask(t, g, srv.URL, http.MethodPost, chat.OpenAIChat, body, headers)
	if r.status != http.StatusOK || trace.GetTranslated() {
		t.Fatalf("status %d translated %v: %s", r.status, trace.GetTranslated(), r.body)
	}
	call := model.call(0)
	if call.body["model"] != "real" || call.body["min_p"] != 0.05 || strings.Join(rolesSent(call.body), ",") != "system,user,system" {
		t.Fatalf("the runtime got %v", call.body)
	}
	if call.header.Get("Anthropic-Beta") != "x" || call.header.Get("Authorization") != "" || call.header.Get("X-Nebu-Model") != "" {
		t.Fatalf("headers %v", call.header)
	}
	if !json.Valid([]byte(trace.GetUpstreamRequest())) || !strings.Contains(trace.GetUpstreamRequest(), `"model":"real"`) {
		t.Fatalf("upstream request %q", trace.GetUpstreamRequest())
	}
}

// Later system turns fold as the route's mode says and the request renders, passing through otherwise
func TestLaterSystemTurns(t *testing.T) {
	model := newSeat(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) { chatAnswer(w, "ok") })
	body := `{"model":"m","messages":[{"role":"system","content":"be brief"},{"role":"user","content":"hi"},{"role":"system","content":"now be long"},{"role":"user","content":"go"}]}`
	for name, tc := range map[string]struct {
		mode     v1.SystemMessages
		roles    string
		system   string
		rendered bool
	}{
		"keep":  {v1.SystemMessages_SYSTEM_MESSAGES_KEEP, "system,user,system,user", "be brief", false},
		"merge": {v1.SystemMessages_SYSTEM_MESSAGES_MERGE, "system,user,user", "be brief\n\nnow be long", true},
		"user":  {v1.SystemMessages_SYSTEM_MESSAGES_USER, "system,user,user", "be brief", true},
	} {
		t.Run(name, func(t *testing.T) {
			g, base := seatGateway(t, model, &v1.Profile{SystemMessages: tc.mode})
			before := model.count()
			r, trace := ask(t, g, base, http.MethodPost, chat.OpenAIChat, body, nil)
			if r.status != http.StatusOK || trace.GetTranslated() || (trace.GetUpstreamRequest() != "") != tc.rendered {
				t.Fatalf("status %d translated %v upstream %q: %s", r.status, trace.GetTranslated(), trace.GetUpstreamRequest(), r.body)
			}
			sent := model.call(before).body
			if got := strings.Join(rolesSent(sent), ","); got != tc.roles {
				t.Fatalf("roles %s, want %s", got, tc.roles)
			}
			if got := systemSent(t, sent); got != tc.system {
				t.Fatalf("system %q, want %q", got, tc.system)
			}
			if tc.mode == v1.SystemMessages_SYSTEM_MESSAGES_USER {
				if first := sent["messages"].([]any)[1].(map[string]any)["content"]; first != "hi\n\nnow be long" {
					t.Fatalf("the late system turn joins the user turn before it: %v", first)
				}
			}
		})
	}
}

// A runtime's refusal reaches the client in the runtime's own words, asked once, streamed or whole
func TestRefusalAsWorded(t *testing.T) {
	message := "This model's maximum context length is 32768 tokens. However, you requested 40000 tokens (30000 in the messages, 10000 in the completion)."
	model := newSeat(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"code": 400, "message": message, "type": "invalid_request_error"}})
	})
	g, base := seatGateway(t, model, nil)
	for i, stream := range []bool{false, true} {
		r, trace := ask(t, g, base, http.MethodPost, chat.AnthropicMessages, fmt.Sprintf(`{"model":"m","max_tokens":10000,"stream":%v,"messages":[{"role":"user","content":"hi"}]}`, stream), nil)
		if r.status != http.StatusBadRequest {
			t.Fatalf("status %d: %s", r.status, r.body)
		}
		var reply struct {
			Error struct {
				Type, Message string
			}
		}
		json.Unmarshal(r.body, &reply)
		if reply.Error.Message != message || reply.Error.Type != "invalid_request_error" {
			t.Fatalf("stream %v: %s", stream, r.body)
		}
		if trace.GetError() != "upstream: "+message {
			t.Fatalf("trace error %q", trace.GetError())
		}
		if model.count() != i+1 {
			t.Fatalf("the runtime was asked %d times", model.count())
		}
	}
}

// The kinds of a stream's events, block starts and deltas named by their block or delta type
func shapeOf(events []map[string]any) []string {
	var shape []string
	for _, ev := range events {
		kind, _ := ev["type"].(string)
		switch kind {
		case "content_block_start":
			kind += ":" + ev["content_block"].(map[string]any)["type"].(string)
		case "content_block_delta":
			kind += ":" + ev["delta"].(map[string]any)["type"].(string)
		case "ping":
			continue
		}
		shape = append(shape, kind)
	}
	return shape
}

// A streamed OpenAI answer that reasons before it speaks
func reasoningStream(w http.ResponseWriter, thinking, text string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"reasoning_content\":%q}}]}\n\n", thinking)
	fmt.Fprintf(w, "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"content\":%q}}]}\n\n", text)
	fmt.Fprint(w, "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2}}\n\n")
	fmt.Fprint(w, "data: [DONE]\n\n")
}

// A finished OpenAI answer that reasons before it speaks
func reasoningAnswer(w http.ResponseWriter, thinking, text string) {
	writeJSON(w, http.StatusOK, map[string]any{"id": "c1", "object": "chat.completion", "model": "m", "choices": []map[string]any{{"index": 0, "message": map[string]any{"role": "assistant", "reasoning_content": thinking, "content": text}, "finish_reason": "stop"}}, "usage": map[string]any{"prompt_tokens": 3, "completion_tokens": 2}})
}

// A trace keeps the request, the upstream request, and the answer whole however large
func TestTraceKeepsWholeBodies(t *testing.T) {
	model := newSeat(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) { chatAnswer(w, "ok") })
	g, base := seatGateway(t, model, nil)
	filler := strings.Repeat("x", 200<<10)
	body := `{"model":"m","max_tokens":10,"messages":[{"role":"user","content":"` + filler + `"}]}`
	r, trace := ask(t, g, base, http.MethodPost, chat.AnthropicMessages, body, nil)
	if r.status != http.StatusOK {
		t.Fatalf("status %d: %s", r.status, r.body)
	}
	if trace.GetRequest() != body {
		t.Fatalf("request kept %d of %d bytes", len(trace.GetRequest()), len(body))
	}
	if !json.Valid([]byte(trace.GetUpstreamRequest())) || !strings.Contains(trace.GetUpstreamRequest(), filler) {
		t.Fatalf("upstream request is cut: %d bytes, valid %v", len(trace.GetUpstreamRequest()), json.Valid([]byte(trace.GetUpstreamRequest())))
	}
	if trace.GetResponse() != "ok" {
		t.Fatalf("response %q", trace.GetResponse())
	}
}

// Bodies held stay under the budget, the oldest inference traces leaving and the newest staying
func TestTraceBudget(t *testing.T) {
	rec := NewRecorder(nil, 10, 5)
	rec.budget = 1000
	for i := range 5 {
		rec.Start(&v1.Trace{Id: fmt.Sprint(i), Request: strings.Repeat("a", 400)})
	}
	if len(rec.main.items) != 2 || rec.held != 800 {
		t.Fatalf("kept %d traces holding %d bytes", len(rec.main.items), rec.held)
	}
	if _, ok := rec.Get("2"); ok {
		t.Fatal("an evicted trace is still indexed")
	}
	if _, ok := rec.Get("4"); !ok {
		t.Fatal("the newest trace left")
	}
	rec.Finish(&v1.Trace{Id: "4", Request: strings.Repeat("a", 400), Response: strings.Repeat("b", 900)})
	if len(rec.main.items) != 1 || rec.main.items[0].GetId() != "4" || rec.held != 1300 {
		t.Fatalf("after a finish over budget: %d traces holding %d bytes", len(rec.main.items), rec.held)
	}
	rec.Start(&v1.Trace{Id: "c", Kind: v1.TraceKind_TRACE_KIND_COUNT, Request: "n"})
	if len(rec.counts.items) != 1 || len(rec.main.items) != 1 {
		t.Fatalf("a count trace moved inference traces: %d counts, %d main", len(rec.counts.items), len(rec.main.items))
	}
}

// Reasoning reaches an Anthropic client as a thinking block before the text and an OpenAI client as reasoning_content
func TestThinkingPassthrough(t *testing.T) {
	model := newSeat(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		if body["stream"] == true {
			reasoningStream(w, "Let me think.", "Answer.")
			return
		}
		reasoningAnswer(w, "Let me think.", "Answer.")
	})
	g, base := seatGateway(t, model, nil)
	r, trace := ask(t, g, base, http.MethodPost, chat.AnthropicMessages, `{"model":"m","max_tokens":50,"stream":true,"messages":[{"role":"user","content":"hi"}]}`, nil)
	if r.status != http.StatusOK {
		t.Fatalf("status %d: %s", r.status, r.body)
	}
	events := eventsOf(t, r.body)
	want := []string{
		"message_start",
		"content_block_start:thinking", "content_block_delta:thinking_delta", "content_block_stop",
		"content_block_start:text", "content_block_delta:text_delta", "content_block_stop",
		"message_delta", "message_stop",
	}
	if got := shapeOf(events); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("events:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if delta := events[2]["delta"].(map[string]any)["thinking"]; delta != "Let me think." {
		t.Fatalf("thinking delta %v", delta)
	}
	if trace.GetResponse() != "Answer." || trace.GetFirstTokenAt() == nil {
		t.Fatalf("trace response %q, first token %v", trace.GetResponse(), trace.GetFirstTokenAt())
	}
	r, _ = ask(t, g, base, http.MethodPost, chat.AnthropicMessages, `{"model":"m","max_tokens":50,"messages":[{"role":"user","content":"hi"}]}`, nil)
	_, blocks := blocksOf(t, r.body)
	if len(blocks) != 2 || blocks[0]["type"] != "thinking" || blocks[0]["thinking"] != "Let me think." || blocks[1]["text"] != "Answer." {
		t.Fatalf("blocks %v", blocks)
	}
	if _, has := blocks[0]["signature"]; !has {
		t.Fatalf("a thinking block carries its signature field: %v", blocks[0])
	}
	r, _ = ask(t, g, base, http.MethodPost, chat.OpenAIChat, `{"model":"m","messages":[{"role":"user","content":"hi"}]}`, nil)
	msg, _ := oaiAnswer(t, r.body)
	if msg["reasoning_content"] != "Let me think." || msg["content"] != "Answer." {
		t.Fatalf("openai message %v", msg)
	}
	r, _ = ask(t, g, base, http.MethodPost, chat.OpenAIChat, `{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`, nil)
	var reasoned, said string
	for _, ev := range eventsOf(t, r.body) {
		delta := ev["choices"].([]any)[0].(map[string]any)["delta"].(map[string]any)
		if s, ok := delta["reasoning_content"].(string); ok {
			reasoned += s
		}
		if s, ok := delta["content"].(string); ok {
			said += s
		}
	}
	if reasoned != "Let me think." || said != "Answer." {
		t.Fatalf("openai stream reasoned %q said %q", reasoned, said)
	}
}

// A document block becomes its text under its title, an unreadable format named, without failing the request
func TestDocumentBlocks(t *testing.T) {
	model := newSeat(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) { chatAnswer(w, "read") })
	g, base := seatGateway(t, model, nil)
	body := `{"model":"m","max_tokens":50,"messages":[
		{"role":"user","content":"read my notes"},
		{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Read","input":{"path":"notes.txt"}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":[
			{"type":"document","title":"notes.txt","source":{"type":"text","media_type":"text/plain","data":"buy milk"}},
			{"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"AAAA"}},
			{"type":"redacted_thinking","data":"x"}]}]}]}`
	r, _ := ask(t, g, base, http.MethodPost, chat.AnthropicMessages, body, nil)
	if r.status != http.StatusOK {
		t.Fatalf("status %d: %s", r.status, r.body)
	}
	msgs := model.call(0).body["messages"].([]any)
	tool := msgs[len(msgs)-1].(map[string]any)
	if tool["role"] != "tool" {
		t.Fatalf("last turn %v", tool)
	}
	text, _ := tool["content"].(string)
	if !strings.Contains(text, "notes.txt\nbuy milk") || !strings.Contains(text, "[a application/pdf document the model cannot read]") {
		t.Fatalf("tool text %q", text)
	}
}

// Deferred tools are offered upfront and the tool search tool is left out
func TestDeferredTools(t *testing.T) {
	model := newSeat(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) { chatAnswer(w, "ok") })
	g, base := seatGateway(t, model, nil)
	body := `{"model":"m","max_tokens":50,"messages":[{"role":"user","content":[{"type":"text","text":"hi"},{"type":"tool_reference","tool_name":"Read"}]}],
		"tools":[{"type":"tool_search_tool_regex_20251119","name":"tool_search_tool_regex"},
		{"name":"Read","description":"Reads a file","input_schema":{"type":"object","properties":{"path":{"type":"string"}}},"defer_loading":true}]}`
	r, _ := ask(t, g, base, http.MethodPost, chat.AnthropicMessages, body, nil)
	if r.status != http.StatusOK {
		t.Fatalf("status %d: %s", r.status, r.body)
	}
	sent := model.call(0).body
	if names := toolNames(sent); len(names) != 1 || names[0] != "Read" {
		t.Fatalf("tools offered %v", names)
	}
	tool := sent["tools"].([]any)[0].(map[string]any)
	if _, has := tool["defer_loading"]; has {
		t.Fatalf("defer_loading reached the runtime: %v", tool)
	}
	if _, has := tool["function"].(map[string]any)["defer_loading"]; has {
		t.Fatalf("defer_loading reached the runtime: %v", tool)
	}
}

// A token count is answered by the runtime's tokenizer when it has one and estimated otherwise
func TestCountTokens(t *testing.T) {
	tokenizer := true
	model := newSeat(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		if r.URL.Path != "/tokenize" || !tokenizer {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"count": 42})
	})
	g, base := seatGateway(t, model, nil)
	body := `{"model":"m","messages":[{"role":"user","content":"hello there"}],"tools":[{"name":"Read","input_schema":{"type":"object"}}]}`
	r, trace := ask(t, g, base, http.MethodPost, chat.AnthropicCount, body, nil)
	if r.status != http.StatusOK || strings.TrimSpace(string(r.body)) != `{"input_tokens":42}` {
		t.Fatalf("status %d: %s", r.status, r.body)
	}
	if trace.GetKind() != v1.TraceKind_TRACE_KIND_COUNT || trace.GetPromptTokens() != 42 || !trace.GetTranslated() {
		t.Fatalf("trace %v", trace)
	}
	if sent := model.call(0).body; sent["content"] == nil || sent["model"] != "m" {
		t.Fatalf("tokenizer request %v", sent)
	}
	tokenizer = false
	r, _ = ask(t, g, base, http.MethodPost, chat.AnthropicCount, body, nil)
	var reply struct {
		InputTokens int `json:"input_tokens"`
	}
	if json.Unmarshal(r.body, &reply) != nil || r.status != http.StatusOK || reply.InputTokens < 10 {
		t.Fatalf("estimated count: %d %s", r.status, r.body)
	}
	if rt, _ := g.table.Lookup("m"); rt.GetRequests() != 0 {
		t.Fatalf("counts served %d requests", rt.GetRequests())
	}
}

// An OpenAI client gets the runtime's own answer id on a rendered request, streamed or whole, and a refusal's retry header
func TestRuntimeIdsAndRetryHeaders(t *testing.T) {
	busy := false
	model := newSeat(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		if busy {
			w.Header().Set("Retry-After", "7")
			refuse(w, http.StatusTooManyRequests)
			return
		}
		if body["stream"] == true {
			chatStream(w, "hi")
			return
		}
		chatAnswer(w, "hi")
	})
	g, base := seatGateway(t, model, &v1.Profile{SystemMessages: v1.SystemMessages_SYSTEM_MESSAGES_MERGE})
	late := `[{"role":"system","content":"a"},{"role":"user","content":"hi"},{"role":"system","content":"b"}]`
	r, trace := ask(t, g, base, http.MethodPost, chat.OpenAIChat, `{"model":"m","messages":`+late+`}`, nil)
	var reply struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(r.body, &reply) != nil || reply.ID != "c1" || trace.GetUpstreamRequest() == "" || trace.GetTranslated() {
		t.Fatalf("whole id: %s", r.body)
	}
	r, _ = ask(t, g, base, http.MethodPost, chat.OpenAIChat, `{"model":"m","stream":true,"messages":`+late+`}`, nil)
	for _, ev := range eventsOf(t, r.body) {
		if ev["id"] != "c1" {
			t.Fatalf("stream id: %v", ev)
		}
	}
	busy = true
	r, _ = ask(t, g, base, http.MethodPost, chat.AnthropicMessages, `{"model":"m","max_tokens":5,"messages":[{"role":"user","content":"hi"}]}`, nil)
	if r.status != http.StatusTooManyRequests || r.header.Get("Retry-After") != "7" {
		t.Fatalf("refusal: %d retry after %q", r.status, r.header.Get("Retry-After"))
	}
}

// Waits until a route and its seats have nothing in flight, which is after the gateway is done with every request
func settled(t *testing.T, table *Table, name string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		rt, _ := table.Lookup(name)
		busy := rt.GetInFlight()
		for _, s := range rt.GetSeats() {
			busy += s.GetInFlight()
		}
		if busy == 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("route %s never settled", name)
}

// A system prompt header replaces every system and developer turn the client sent in any protocol, never reaching
// the runtime itself, and a header that is not base64 is refused before the runtime is asked
func TestSystemHeader(t *testing.T) {
	model := newSeat(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) { chatAnswer(w, "ok") })
	g, base := seatGateway(t, model, &v1.Profile{SystemMessages: v1.SystemMessages_SYSTEM_MESSAGES_USER})
	pinned := "line one\nline two"
	headers := map[string]string{interceptors.SystemHeader: base64.StdEncoding.EncodeToString([]byte(pinned))}
	body := `{"model":"m","messages":[{"role":"system","content":"be brief"},{"role":"developer","content":"rules"},{"role":"user","content":"hi"},{"role":"system","content":"now be long"},{"role":"user","content":"go"}]}`
	r, trace := ask(t, g, base, http.MethodPost, chat.OpenAIChat, body, headers)
	if r.status != http.StatusOK || trace.GetUpstreamRequest() == "" {
		t.Fatalf("status %d upstream %q: %s", r.status, trace.GetUpstreamRequest(), r.body)
	}
	call := model.call(0)
	if got := strings.Join(rolesSent(call.body), ","); got != "system,user,user" {
		t.Fatalf("roles %s", got)
	}
	if got := systemSent(t, call.body); got != pinned {
		t.Fatalf("system %q", got)
	}
	if call.header.Get(interceptors.SystemHeader) != "" {
		t.Fatal("the system prompt header reached the runtime")
	}
	r, _ = ask(t, g, base, http.MethodPost, chat.AnthropicMessages, `{"model":"m","max_tokens":5,"system":"be brief","messages":[{"role":"user","content":"hi"}]}`, headers)
	if r.status != http.StatusOK || systemSent(t, model.call(1).body) != pinned {
		t.Fatalf("anthropic: %d system %q", r.status, systemSent(t, model.call(1).body))
	}
	r, _ = ask(t, g, base, http.MethodPost, chat.OpenAIChat, body, map[string]string{interceptors.SystemHeader: "rules!"})
	if r.status != http.StatusBadRequest || !strings.Contains(string(r.body), interceptors.SystemHeader) || model.count() != 2 {
		t.Fatalf("not base64: %d %s, runtime asked %d times", r.status, r.body, model.count())
	}
}

// A ghost request is answered with no trace kept or named and no seat pinned to its prefix, a ghost header that is
// neither on nor off is refused before anything is kept, and the same request without the header is traced and pinned
func TestGhostHeader(t *testing.T) {
	a := newSeat(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) { chatAnswer(w, "ok") })
	g, table := newGateway(t)
	replicaRoute(table, []*v1.RouteSeat{readySeat("ia", a.URL, "", 0)}, &v1.AffinityPolicy{WindowSeconds: 600, SystemMessage: true, FirstUserMessage: true}, nil)
	srv := httptest.NewServer(g.Handler())
	defer srv.Close()
	pins := func() int {
		g.affinity.mu.Lock()
		defer g.affinity.mu.Unlock()
		return len(g.affinity.entries)
	}
	for _, stream := range []string{"false", "true"} {
		body := strings.Replace(chatBody, `"model":"m"`, `"model":"m","stream":`+stream, 1)
		r, trace := ask(t, g, srv.URL, http.MethodPost, chat.OpenAIChat, body, map[string]string{interceptors.GhostHeader: "1"})
		settled(t, table, "m")
		if r.status != http.StatusOK || trace != nil || r.header.Get(traceHeader) != "" {
			t.Fatalf("stream %s: status %d trace header %q: %s", stream, r.status, r.header.Get(traceHeader), r.body)
		}
		if n := len(g.Traces().List("", 0)); n != 0 || pins() != 0 {
			t.Fatalf("stream %s: %d traces and %d pins kept", stream, n, pins())
		}
	}
	r, _ := ask(t, g, srv.URL, http.MethodPost, chat.OpenAIChat, chatBody, map[string]string{interceptors.GhostHeader: "maybe"})
	if r.status != http.StatusBadRequest || !strings.Contains(string(r.body), interceptors.GhostHeader) || a.count() != 2 || len(g.Traces().List("", 0)) != 0 {
		t.Fatalf("maybe: %d %s, runtime asked %d times", r.status, r.body, a.count())
	}
	if _, trace := ask(t, g, srv.URL, http.MethodPost, chat.OpenAIChat, chatBody, nil); trace == nil || pins() != 1 {
		t.Fatalf("without the header: trace %v, %d pins", trace, pins())
	}
}

// A ghost video job is accepted and read by its id, kept out of the listing, and leaves no trace even once it ends
func TestGhostVideoJob(t *testing.T) {
	done := make(chan struct{})
	model := newSeat(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		switch {
		case r.URL.Path == sdcppVideoJob:
			writeJSON(w, http.StatusAccepted, map[string]any{"id": "job1", "status": "queued"})
		case strings.HasSuffix(r.URL.Path, "/cancel"):
			close(done)
			writeJSON(w, http.StatusOK, map[string]any{"id": "job1", "status": "cancelled"})
		default:
			select {
			case <-done:
				writeJSON(w, http.StatusOK, map[string]any{"id": "job1", "status": "cancelled"})
			default:
				writeJSON(w, http.StatusOK, map[string]any{"id": "job1", "status": "processing"})
			}
		}
	})
	g, table := newGateway(t)
	table.SetModes("i1", []string{"vid_gen"})
	table.Set("v", "i1", "", model.URL, "r:g", "v", v1.ApiFlavor_API_FLAVOR_SDCPP, nil, nil)
	srv := httptest.NewServer(g.Handler())
	defer srv.Close()
	r, trace := ask(t, g, srv.URL, http.MethodPost, chat.OpenAIVideos, `{"model":"v","prompt":"a cat","seconds":1}`, map[string]string{interceptors.GhostHeader: "true"})
	var job struct {
		ID    string `json:"id"`
		Trace string `json:"trace"`
	}
	if r.status != http.StatusAccepted || trace != nil || json.Unmarshal(r.body, &job) != nil || job.ID == "" || job.Trace != "" {
		t.Fatalf("ghost video: %d trace header %q: %s", r.status, r.header.Get(traceHeader), r.body)
	}
	if r, _ = ask(t, nil, srv.URL, http.MethodGet, chat.OpenAIVideos, "", nil); r.status != http.StatusOK || strings.Contains(string(r.body), job.ID) {
		t.Fatalf("listing: %d %s", r.status, r.body)
	}
	if r, _ = ask(t, nil, srv.URL, http.MethodGet, chat.OpenAIVideos+"/"+job.ID, "", nil); r.status != http.StatusOK {
		t.Fatalf("read by id: %d %s", r.status, r.body)
	}
	if r, _ = ask(t, nil, srv.URL, http.MethodDelete, chat.OpenAIVideos+"/"+job.ID, "", nil); r.status != http.StatusOK {
		t.Fatalf("cancel: %d %s", r.status, r.body)
	}
	settled(t, table, "v")
	if n := len(g.Traces().List("", 0)); n != 0 {
		t.Fatalf("%d traces kept", n)
	}
}
