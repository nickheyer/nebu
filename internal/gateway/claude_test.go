package gateway

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nickheyer/nebu/internal/chat"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/proto"
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

// A classifier prompt in the first user turn is answered by the gateway by default without asking the runtime
func TestGuardInUserTurn(t *testing.T) {
	model := newSeat(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) { chatAnswer(w, "<block>yes</block>") })
	g, base := seatGateway(t, model, nil)
	prompt := guardPrompt + ". Decide whether the command below may run."
	body := func(stream bool) string {
		return fmt.Sprintf(`{"model":"m","max_tokens":50,"stream":%v,"messages":[{"role":"user","content":[{"type":"text","text":%q},{"type":"text","text":"rm -rf build"}]}]}`, stream, prompt)
	}
	r, trace := ask(t, g, base, http.MethodPost, chat.AnthropicMessages, body(false), nil)
	if r.status != http.StatusOK {
		t.Fatalf("status %d: %s", r.status, r.body)
	}
	_, blocks := blocksOf(t, r.body)
	if len(blocks) != 1 || blocks[0]["text"] != guardAllow {
		t.Fatalf("blocks %v", blocks)
	}
	if trace.GetResponse() != guardAllow || trace.GetInstanceId() != "i1" {
		t.Fatalf("trace response %q on instance %q", trace.GetResponse(), trace.GetInstanceId())
	}
	r, _ = ask(t, g, base, http.MethodPost, chat.AnthropicMessages, body(true), nil)
	if r.status != http.StatusOK {
		t.Fatalf("status %d: %s", r.status, r.body)
	}
	var said string
	for _, ev := range eventsOf(t, r.body) {
		if ev["type"] == "content_block_delta" {
			said += ev["delta"].(map[string]any)["text"].(string)
		}
	}
	if said != guardAllow {
		t.Fatalf("streamed %q", said)
	}
	if model.count() != 0 {
		t.Fatalf("the runtime was asked %d times", model.count())
	}
	g2, base2 := seatGateway(t, model, &v1.Profile{AllowAll: proto.Bool(false)})
	r, _ = ask(t, g2, base2, http.MethodPost, chat.AnthropicMessages, body(false), nil)
	_, blocks = blocksOf(t, r.body)
	if model.count() != 1 || blocks[0]["text"] != "<block>yes</block>" {
		t.Fatalf("with checks left to the model the runtime answers: asked %d, blocks %v", model.count(), blocks)
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

// A runtime refusing a prompt that outgrew its context is answered in the words clients compact on
func TestPromptTooLong(t *testing.T) {
	model := newSeat(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"code": 400, "message": "the request exceeds the available context size. try increasing the context size or enable context shift", "type": "invalid_request_error"}})
	})
	g, base := seatGateway(t, model, nil)
	for _, stream := range []bool{false, true} {
		r, trace := ask(t, g, base, http.MethodPost, chat.AnthropicMessages, fmt.Sprintf(`{"model":"m","max_tokens":50,"stream":%v,"messages":[{"role":"user","content":"hi"}]}`, stream), nil)
		if r.status != http.StatusBadRequest {
			t.Fatalf("status %d: %s", r.status, r.body)
		}
		var reply struct {
			Error struct {
				Type, Message string
			}
		}
		json.Unmarshal(r.body, &reply)
		if !strings.HasPrefix(reply.Error.Message, "prompt is too long: the request exceeds") || reply.Error.Type != "invalid_request_error" {
			t.Fatalf("stream %v: %s", stream, r.body)
		}
		if !strings.HasPrefix(trace.GetError(), "upstream: prompt is too long") {
			t.Fatalf("trace error %q", trace.GetError())
		}
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

// An OpenAI client gets the runtime's own answer id, streamed or whole, and a refusal's retry headers
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
	g, base := seatGateway(t, model, &v1.Profile{Overrides: `{"top_p":0.9}`})
	r, _ := ask(t, g, base, http.MethodPost, chat.OpenAIChat, `{"model":"m","messages":[{"role":"user","content":"hi"}]}`, nil)
	var reply struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(r.body, &reply) != nil || reply.ID != "c1" {
		t.Fatalf("whole id: %s", r.body)
	}
	r, _ = ask(t, g, base, http.MethodPost, chat.OpenAIChat, `{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`, nil)
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
