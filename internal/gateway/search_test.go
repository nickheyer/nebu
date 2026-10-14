package gateway

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

const kittenTool = `{"type":"web_search_20250305","name":"web_search","max_uses":8,"user_location":{"type":"approximate","city":"Portland","country":"US"}}`

// The request Claude Code's web search makes, with the tool as given
func searchBody(stream bool, tool string) string {
	return fmt.Sprintf(`{"model":"m","max_tokens":200,"stream":%v,"system":"You are an assistant for performing a web search tool use","messages":[{"role":"user","content":"Perform a search for pictures of cute little kittens!"}],"tools":[%s],"tool_choice":{"type":"auto"}}`, stream, tool)
}

// A search engine as a test stands it up, remembering what it was asked
type engine struct {
	*httptest.Server
	mu    sync.Mutex
	asked []url.Values
	heads []http.Header
}

func (e *engine) query(i int) url.Values {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.asked[i]
}

func (e *engine) header(i int) http.Header {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.heads[i]
}

func newEngine(t *testing.T, answer func(w http.ResponseWriter, r *http.Request)) *engine {
	t.Helper()
	e := &engine{}
	e.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.mu.Lock()
		e.asked = append(e.asked, r.URL.Query())
		e.heads = append(e.heads, r.Header.Clone())
		e.mu.Unlock()
		answer(w, r)
	}))
	t.Cleanup(e.Close)
	return e
}

// A SearXNG that answers every search with the given results
func searxngWith(t *testing.T, results ...map[string]any) *engine {
	return newEngine(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search" || r.URL.Query().Get("format") != "json" {
			http.Error(w, "wrong path or format", http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"query": r.URL.Query().Get("q"), "results": results})
	})
}

var kittenPages = []map[string]any{
	{"url": "https://cats.example/kittens", "title": "Kittens", "content": "Very small cats.", "publishedDate": "2024-05-01"},
	{"url": "https://pics.cats.example/album", "title": "Kitten album", "content": "Pictures.", "publishedDate": nil},
	{"url": "https://dogs.example/puppies", "title": "Puppies", "content": "Not cats.", "publishedDate": "2023-01-01"},
}

// Whether the model has a search result to read
func hasToolTurn(body map[string]any) bool {
	msgs, _ := body["messages"].([]any)
	for _, m := range msgs {
		if mm, ok := m.(map[string]any); ok && mm["role"] == "tool" {
			return true
		}
	}
	return false
}

// A finished OpenAI answer calling tools
func toolAnswer(w http.ResponseWriter, text string, calls ...map[string]any) {
	writeJSON(w, http.StatusOK, map[string]any{"id": "c1", "object": "chat.completion", "model": "m", "choices": []map[string]any{{"index": 0, "message": map[string]any{"role": "assistant", "content": text, "tool_calls": calls}, "finish_reason": "tool_calls"}}, "usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 5}})
}

func searchCall(id, query string) map[string]any {
	return map[string]any{"id": id, "type": "function", "function": map[string]any{"name": "web_search", "arguments": fmt.Sprintf(`{"query":%q}`, query)}}
}

// A streamed OpenAI answer calling web_search, the arguments split over two chunks
func searchStream(w http.ResponseWriter, query string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	args, _ := json.Marshal(map[string]string{"query": query})
	fmt.Fprint(w, `data: {"id":"c1","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"Let me look."}}]}`+"\n\n")
	fmt.Fprint(w, `data: {"id":"c1","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"web_search","arguments":""}}]}}]}`+"\n\n")
	fmt.Fprintf(w, `data: {"id":"c1","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":%q}}]}}]}`+"\n\n", string(args[:8]))
	fmt.Fprintf(w, `data: {"id":"c1","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":%q}}]}}]}`+"\n\n", string(args[8:]))
	fmt.Fprint(w, `data: {"id":"c1","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`+"\n\n")
	fmt.Fprint(w, "data: [DONE]\n\n")
}

// A gateway serving one OpenAI seat as m, searching through the given provider settings
func searchGateway(t *testing.T, model *seat, cfg *v1.WebSearch) (*Gateway, string) {
	t.Helper()
	g, table := newGateway(t)
	table.Set("m", "i1", "", model.URL, "r:g", "m", v1.ApiFlavor_API_FLAVOR_OPENAI, nil, nil)
	if cfg != nil {
		g.SetWebSearch(func() *v1.WebSearch { return cfg })
	}
	srv := httptest.NewServer(g.Handler())
	t.Cleanup(srv.Close)
	return g, srv.URL
}

func searxngSettings(e *engine) *v1.WebSearch {
	return &v1.WebSearch{Provider: "searxng", SearxngUrl: e.URL}
}

func blocksOf(t *testing.T, body []byte) (map[string]any, []map[string]any) {
	t.Helper()
	var reply map[string]any
	if err := json.Unmarshal(body, &reply); err != nil {
		t.Fatalf("reply is not JSON: %v: %s", err, body)
	}
	raw, _ := reply["content"].([]any)
	var blocks []map[string]any
	for _, b := range raw {
		blocks = append(blocks, b.(map[string]any))
	}
	return reply, blocks
}

// The data objects of a server sent event stream in order
func eventsOf(t *testing.T, body []byte) []map[string]any {
	t.Helper()
	var out []map[string]any
	if err := readSSE(strings.NewReader(string(body)), func(_ string, data []byte) error {
		var ev map[string]any
		if err := json.Unmarshal(data, &ev); err != nil {
			return err
		}
		out = append(out, ev)
		return nil
	}); err != nil {
		t.Fatalf("stream: %v\n%s", err, body)
	}
	return out
}

// A client offering web_search to an OpenAI runtime gets the searches run by the gateway: the
// model is handed a web_search function and its results, and the client's message carries
// the search the way the API writes it, with the counts in usage and the trace
func TestWebSearchWhole(t *testing.T) {
	searx := searxngWith(t, kittenPages...)
	model := newSeat(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		if hasToolTurn(body) {
			chatAnswer(w, "Kittens are at https://cats.example/kittens")
			return
		}
		toolAnswer(w, "Let me look.", searchCall("call_1", "cute kittens"))
	})
	g, base := searchGateway(t, model, searxngSettings(searx))
	r, trace := ask(t, g, base, http.MethodPost, anthropicMessages, searchBody(false, kittenTool), nil)
	if r.status != http.StatusOK {
		t.Fatalf("status %d: %s", r.status, r.body)
	}
	reply, blocks := blocksOf(t, r.body)
	if len(blocks) != 4 || blocks[0]["type"] != "text" || blocks[1]["type"] != "server_tool_use" || blocks[2]["type"] != "web_search_tool_result" || blocks[3]["type"] != "text" {
		t.Fatalf("blocks: %s", r.body)
	}
	if blocks[0]["text"] != "Let me look." || blocks[3]["text"] != "Kittens are at https://cats.example/kittens" {
		t.Fatalf("texts: %s", r.body)
	}
	use := blocks[1]
	id, _ := use["id"].(string)
	if !strings.HasPrefix(id, "srvtoolu_") || use["name"] != "web_search" || use["input"].(map[string]any)["query"] != "cute kittens" {
		t.Fatalf("server_tool_use: %v", use)
	}
	if blocks[2]["tool_use_id"] != id {
		t.Fatalf("result belongs to %v, not %s", blocks[2]["tool_use_id"], id)
	}
	results := blocks[2]["content"].([]any)
	if len(results) != 3 {
		t.Fatalf("results: %v", results)
	}
	first := results[0].(map[string]any)
	if first["type"] != "web_search_result" || first["url"] != "https://cats.example/kittens" || first["title"] != "Kittens" || first["page_age"] != "2024-05-01" {
		t.Fatalf("first result: %v", first)
	}
	if snippet, ok := openSnippet(first["encrypted_content"].(string)); !ok || snippet != "Very small cats." {
		t.Fatalf("sealed snippet: %q %v", snippet, ok)
	}
	if results[1].(map[string]any)["page_age"] != nil {
		t.Fatalf("an unknown date is null: %v", results[1])
	}
	if reply["stop_reason"] != "end_turn" {
		t.Fatalf("stop_reason %v", reply["stop_reason"])
	}
	usage := reply["usage"].(map[string]any)
	if usage["input_tokens"] != 13.0 || usage["output_tokens"] != 6.0 || usage["server_tool_use"].(map[string]any)["web_search_requests"] != 1.0 {
		t.Fatalf("usage: %v", usage)
	}
	// The engine was asked the model's query in the client's country.
	if q := searx.query(0); q.Get("q") != "cute kittens" || q.Get("language") != "en-US" {
		t.Fatalf("engine asked %v", q)
	}
	// The model saw a function, never the server tool, and read the results as text.
	if model.count() != 2 {
		t.Fatalf("model answered %d times", model.count())
	}
	for i := range 2 {
		tools := model.call(i).body["tools"].([]any)
		if len(tools) != 1 || tools[0].(map[string]any)["type"] != "function" || tools[0].(map[string]any)["function"].(map[string]any)["name"] != "web_search" {
			t.Fatalf("call %d tools: %v", i, tools)
		}
	}
	msgs := model.call(1).body["messages"].([]any)
	assistant, result := msgs[len(msgs)-2].(map[string]any), msgs[len(msgs)-1].(map[string]any)
	if assistant["role"] != "assistant" || assistant["content"] != "Let me look." || assistant["tool_calls"].([]any)[0].(map[string]any)["id"] != "call_1" {
		t.Fatalf("assistant turn: %v", assistant)
	}
	text, _ := result["content"].(string)
	if result["role"] != "tool" || result["tool_call_id"] != "call_1" || !strings.Contains(text, "Search results:") || !strings.Contains(text, "1. Kittens\n   https://cats.example/kittens\n   Very small cats.\n   published 2024-05-01") {
		t.Fatalf("tool turn: %v", result)
	}
	if trace.GetWebSearches() != 1 || !trace.GetTranslated() || trace.GetStatus() != http.StatusOK || trace.GetPromptTokens() != 13 || trace.GetCompletionTokens() != 6 {
		t.Fatalf("trace %v", trace)
	}
	if len(trace.GetToolCalls()) != 1 || trace.GetToolCalls()[0].GetName() != "web_search" {
		t.Fatalf("trace tool calls %v", trace.GetToolCalls())
	}
	if got := trace.GetResponse(); got != "Let me look.Kittens are at https://cats.example/kittens" {
		t.Fatalf("trace response %q", got)
	}
	if !strings.Contains(trace.GetUpstreamRequest(), "Search results:") {
		t.Fatalf("the trace shows the runtime's last request: %s", trace.GetUpstreamRequest())
	}
}

// A streamed answer carries the search as its own blocks between the model's text, the
// server_tool_use input arriving as one json delta, and the counts in the closing delta
func TestWebSearchStream(t *testing.T) {
	searx := searxngWith(t, kittenPages[0])
	model := newSeat(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		if hasToolTurn(body) {
			chatStream(w, "Found them.")
			return
		}
		searchStream(w, "cute kittens")
	})
	g, base := searchGateway(t, model, searxngSettings(searx))
	later := strings.Replace(kittenTool, "web_search_20250305", "web_search_20260301", 1)
	r, trace := ask(t, g, base, http.MethodPost, anthropicMessages, searchBody(true, later), nil)
	if r.status != http.StatusOK || !strings.Contains(r.header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("status %d %s: %s", r.status, r.header.Get("Content-Type"), r.body)
	}
	events := eventsOf(t, r.body)
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
	want := []string{
		"message_start",
		"content_block_start:text", "content_block_delta:text_delta", "content_block_stop",
		"content_block_start:server_tool_use", "content_block_delta:input_json_delta", "content_block_stop",
		"content_block_start:web_search_tool_result", "content_block_stop",
		"content_block_start:text", "content_block_delta:text_delta", "content_block_stop",
		"message_delta", "message_stop",
	}
	if strings.Join(shape, " ") != strings.Join(want, " ") {
		t.Fatalf("events:\n%s\nwant:\n%s", strings.Join(shape, "\n"), strings.Join(want, "\n"))
	}
	use := events[4]["content_block"].(map[string]any)
	if use["name"] != "web_search" || len(use["input"].(map[string]any)) != 0 {
		t.Fatalf("server_tool_use starts empty: %v", use)
	}
	if partial := events[5]["delta"].(map[string]any)["partial_json"]; partial != `{"query":"cute kittens"}` {
		t.Fatalf("input delta %v", partial)
	}
	result := events[7]["content_block"].(map[string]any)
	if result["tool_use_id"] != use["id"] || len(result["content"].([]any)) != 1 {
		t.Fatalf("result block %v", result)
	}
	closing := events[len(events)-2]
	if closing["delta"].(map[string]any)["stop_reason"] != "end_turn" || closing["usage"].(map[string]any)["server_tool_use"].(map[string]any)["web_search_requests"] != 1.0 {
		t.Fatalf("closing delta %v", closing)
	}
	if trace.GetWebSearches() != 1 || trace.GetResponse() != "Let me look.Found them." {
		t.Fatalf("trace %v", trace)
	}
	if q := searx.query(0); q.Get("q") != "cute kittens" {
		t.Fatalf("engine asked %v", q)
	}
}

// Every dated version of the tool and its bare name are the same tool
func TestWebSearchVersions(t *testing.T) {
	for _, kind := range []string{"web_search", "web_search_20250305", "web_search_20260301", "web_search_20301231"} {
		chat := &Chat{Format: v1.ApiFlavor_API_FLAVOR_ANTHROPIC, Tools: []Tool{{Type: kind, Name: "web_search"}, {Name: "other"}}}
		tool, ok, err := takeWebSearch(chat)
		if err != nil || !ok || tool.maxUses != webSearchUses || len(chat.Tools) != 2 || chat.Tools[1].Name != webSearchName || chat.Tools[1].Type != "" {
			t.Fatalf("%s: %v %v %v %v", kind, tool, ok, err, chat.Tools)
		}
	}
	for _, kind := range []string{"web_fetch_20250910", "web_searching", "text_editor_20250124", "custom", ""} {
		chat := &Chat{Format: v1.ApiFlavor_API_FLAVOR_ANTHROPIC, Tools: []Tool{{Type: kind, Name: "fetch"}}}
		if _, ok, err := takeWebSearch(chat); ok || err != nil {
			t.Fatalf("%s taken as web search: %v", kind, err)
		}
	}
}

// Searches past max_uses are answered with the max_uses_exceeded error, to the client as an
// error block and to the model as text, and a call without a query with invalid_input
func TestWebSearchLimits(t *testing.T) {
	searx := searxngWith(t, kittenPages[0])
	model := newSeat(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		if hasToolTurn(body) {
			chatAnswer(w, "Done.")
			return
		}
		toolAnswer(w, "", searchCall("call_1", "cute kittens"), searchCall("call_2", "more kittens"), map[string]any{"id": "call_3", "type": "function", "function": map[string]any{"name": "web_search", "arguments": `{"q":"oops"}`}})
	})
	g, base := searchGateway(t, model, searxngSettings(searx))
	tool := `{"type":"web_search_20250305","name":"web_search","max_uses":1}`
	r, trace := ask(t, g, base, http.MethodPost, anthropicMessages, searchBody(false, tool), nil)
	if r.status != http.StatusOK {
		t.Fatalf("status %d: %s", r.status, r.body)
	}
	reply, blocks := blocksOf(t, r.body)
	if len(blocks) != 7 {
		t.Fatalf("blocks: %s", r.body)
	}
	if _, ok := blocks[1]["content"].([]any); !ok {
		t.Fatalf("the first search has results: %v", blocks[1])
	}
	second := blocks[3]["content"].(map[string]any)
	if second["type"] != "web_search_tool_result_error" || second["error_code"] != "max_uses_exceeded" {
		t.Fatalf("second search: %v", second)
	}
	if third := blocks[5]["content"].(map[string]any); third["error_code"] != "invalid_input" || blocks[4]["input"].(map[string]any)["query"] != "" {
		t.Fatalf("third search: %v %v", blocks[4], third)
	}
	if reply["usage"].(map[string]any)["server_tool_use"].(map[string]any)["web_search_requests"] != 1.0 || trace.GetWebSearches() != 1 {
		t.Fatalf("one search counted: %v %d", reply["usage"], trace.GetWebSearches())
	}
	if searx.query(0).Get("q") != "cute kittens" || len(searx.asked) != 1 {
		t.Fatalf("engine asked %v", searx.asked)
	}
	msgs := model.call(1).body["messages"].([]any)
	turns := map[string]string{}
	for _, m := range msgs {
		if mm := m.(map[string]any); mm["role"] == "tool" {
			turns[mm["tool_call_id"].(string)], _ = mm["content"].(string)
		}
	}
	if !strings.Contains(turns["call_1"], "Search results:") || !strings.Contains(turns["call_2"], "No more web searches") || !strings.Contains(turns["call_3"], "needs a query") {
		t.Fatalf("tool turns: %v", turns)
	}
}

// A round that calls the client's tool beside a search ends the turn with both: the search
// run and shown, the client's call as tool_use, so the next turn carries everything
func TestWebSearchBesideClientTool(t *testing.T) {
	searx := searxngWith(t, kittenPages[0])
	model := newSeat(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		toolAnswer(w, "", searchCall("call_1", "cute kittens"), map[string]any{"id": "call_2", "type": "function", "function": map[string]any{"name": "get_weather", "arguments": `{"city":"Portland"}`}})
	})
	g, base := searchGateway(t, model, searxngSettings(searx))
	tools := kittenTool + `,{"name":"get_weather","input_schema":{"type":"object","properties":{"city":{"type":"string"}}}}`
	r, _ := ask(t, g, base, http.MethodPost, anthropicMessages, searchBody(false, tools), nil)
	if r.status != http.StatusOK {
		t.Fatalf("status %d: %s", r.status, r.body)
	}
	reply, blocks := blocksOf(t, r.body)
	if len(blocks) != 3 || blocks[0]["type"] != "server_tool_use" || blocks[1]["type"] != "web_search_tool_result" || blocks[2]["type"] != "tool_use" {
		t.Fatalf("blocks: %s", r.body)
	}
	if blocks[2]["name"] != "get_weather" || blocks[2]["id"] != "call_2" || blocks[2]["input"].(map[string]any)["city"] != "Portland" {
		t.Fatalf("tool_use: %v", blocks[2])
	}
	if reply["stop_reason"] != "tool_use" || model.count() != 1 {
		t.Fatalf("stop %v after %d rounds", reply["stop_reason"], model.count())
	}
	if tools := model.call(0).body["tools"].([]any); len(tools) != 2 {
		t.Fatalf("the model had both tools: %v", tools)
	}
}

// Domain lists keep or drop results by host, covering subdomains, and cannot be combined
func TestWebSearchDomains(t *testing.T) {
	searx := searxngWith(t, kittenPages...)
	model := newSeat(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		if hasToolTurn(body) {
			chatAnswer(w, "Done.")
			return
		}
		toolAnswer(w, "", searchCall("call_1", "kittens"))
	})
	g, base := searchGateway(t, model, searxngSettings(searx))
	urls := func(tool string) []string {
		r, _ := ask(t, g, base, http.MethodPost, anthropicMessages, searchBody(false, tool), nil)
		if r.status != http.StatusOK {
			t.Fatalf("status %d: %s", r.status, r.body)
		}
		_, blocks := blocksOf(t, r.body)
		var out []string
		for _, res := range blocks[1]["content"].([]any) {
			out = append(out, res.(map[string]any)["url"].(string))
		}
		return out
	}
	if got := urls(`{"type":"web_search_20250305","name":"web_search","allowed_domains":["cats.example"]}`); strings.Join(got, " ") != "https://cats.example/kittens https://pics.cats.example/album" {
		t.Fatalf("allowed: %v", got)
	}
	if got := urls(`{"type":"web_search_20250305","name":"web_search","blocked_domains":["cats.example"]}`); strings.Join(got, " ") != "https://dogs.example/puppies" {
		t.Fatalf("blocked: %v", got)
	}
	if got := urls(`{"type":"web_search_20250305","name":"web_search","allowed_domains":["cats.example/kittens"]}`); strings.Join(got, " ") != "https://cats.example/kittens" {
		t.Fatalf("allowed path: %v", got)
	}
	r, _ := ask(t, g, base, http.MethodPost, anthropicMessages, searchBody(false, `{"type":"web_search_20250305","name":"web_search","allowed_domains":["a.example"],"blocked_domains":["b.example"]}`), nil)
	if r.status != http.StatusBadRequest || !strings.Contains(string(r.body), "not both") {
		t.Fatalf("both lists: %d %s", r.status, r.body)
	}
}

// Without a provider the tool is refused up front, naming the setting to make
func TestWebSearchUnset(t *testing.T) {
	model := newSeat(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		chatAnswer(w, "never")
	})
	for _, cfg := range []*v1.WebSearch{nil, {}} {
		g, base := searchGateway(t, model, cfg)
		r, trace := ask(t, g, base, http.MethodPost, anthropicMessages, searchBody(true, kittenTool), nil)
		if r.status != http.StatusBadRequest || !strings.Contains(string(r.body), "web search provider is chosen in Settings") {
			t.Fatalf("status %d: %s", r.status, r.body)
		}
		if trace.GetError() == "" || model.count() != 0 {
			t.Fatalf("trace %v after %d model calls", trace, model.count())
		}
	}
}

// An engine that fails leaves the model an unavailable answer and the client an error block,
// and a rate limited one names too_many_requests
func TestWebSearchEngineDown(t *testing.T) {
	var status atomic.Int32
	searx := newEngine(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(int(status.Load())) })
	model := newSeat(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		if hasToolTurn(body) {
			chatAnswer(w, "Could not search.")
			return
		}
		toolAnswer(w, "", searchCall("call_1", "kittens"))
	})
	g, base := searchGateway(t, model, searxngSettings(searx))
	for _, c := range []struct {
		status int
		code   string
	}{{http.StatusForbidden, "unavailable"}, {http.StatusTooManyRequests, "too_many_requests"}, {http.StatusBadGateway, "unavailable"}} {
		status.Store(int32(c.status))
		r, trace := ask(t, g, base, http.MethodPost, anthropicMessages, searchBody(false, kittenTool), nil)
		if r.status != http.StatusOK {
			t.Fatalf("status %d: %s", r.status, r.body)
		}
		_, blocks := blocksOf(t, r.body)
		if got := blocks[1]["content"].(map[string]any); got["type"] != "web_search_tool_result_error" || got["error_code"] != c.code {
			t.Fatalf("%d: %v", c.status, got)
		}
		if trace.GetWebSearches() != 1 || trace.GetError() != "" {
			t.Fatalf("the search was attempted and the request succeeded: %v", trace)
		}
		text, _ := model.call(model.count() - 1).body["messages"].([]any)[len(model.call(model.count() - 1).body["messages"].([]any))-1].(map[string]any)["content"].(string)
		if c.code == "too_many_requests" && !strings.Contains(text, "rate limiting") || c.code == "unavailable" && !strings.Contains(text, "unavailable") {
			t.Fatalf("%d: the model read %q", c.status, text)
		}
	}
}

// Brave is asked under its token with the client's country, and its snippets reach the model
func TestWebSearchBrave(t *testing.T) {
	api := newEngine(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Subscription-Token") != "tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"web": map[string]any{"results": []map[string]any{{"title": "Kittens", "url": "https://cats.example/kittens", "description": "Small cats.", "extra_snippets": []string{"Very fluffy."}, "page_age": "2024-05-01T00:00:00"}}}})
	})
	model := newSeat(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		if hasToolTurn(body) {
			chatAnswer(w, "Done.")
			return
		}
		toolAnswer(w, "", searchCall("call_1", "cute kittens"))
	})
	real := braveEndpoint
	braveEndpoint = api.URL
	t.Cleanup(func() { braveEndpoint = real })
	g, base := searchGateway(t, model, &v1.WebSearch{Provider: "brave", BraveKey: "tok"})
	r, trace := ask(t, g, base, http.MethodPost, anthropicMessages, searchBody(false, kittenTool), nil)
	if r.status != http.StatusOK {
		t.Fatalf("status %d: %s", r.status, r.body)
	}
	_, blocks := blocksOf(t, r.body)
	results := blocks[1]["content"].([]any)
	first := results[0].(map[string]any)
	if len(results) != 1 || first["url"] != "https://cats.example/kittens" || first["page_age"] != "2024-05-01T00:00:00" {
		t.Fatalf("results %v", results)
	}
	if snippet, _ := openSnippet(first["encrypted_content"].(string)); snippet != "Small cats. Very fluffy." {
		t.Fatalf("snippet %q", snippet)
	}
	if q := api.query(0); q.Get("q") != "cute kittens" || q.Get("country") != "US" || q.Get("count") != "20" || api.header(0).Get("X-Subscription-Token") != "tok" {
		t.Fatalf("brave asked %v %v", q, api.header(0))
	}
	if trace.GetWebSearches() != 1 {
		t.Fatalf("trace %v", trace)
	}
	b := brave{key: "tok", client: &http.Client{}}
	if _, err := b.search(t.Context(), searchAsk{Query: "x", Country: "AQ"}); err != nil {
		t.Fatalf("a country Brave does not list is left out: %v", err)
	} else if q := api.query(1); q.Has("country") {
		t.Fatalf("brave asked %v", q)
	}
	b.key = "wrong"
	if _, err := b.search(t.Context(), searchAsk{Query: "x"}); err == nil || !strings.Contains(err.Error(), "refuses the subscription token") {
		t.Fatalf("bad token: %v", err)
	}
	long := strings.Repeat("kittens ", 60)
	model2 := newSeat(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		if hasToolTurn(body) {
			chatAnswer(w, "Done.")
			return
		}
		toolAnswer(w, "", searchCall("call_1", long))
	})
	g, base = searchGateway(t, model2, &v1.WebSearch{Provider: "brave", BraveKey: "tok"})
	r, _ = ask(t, g, base, http.MethodPost, anthropicMessages, searchBody(false, kittenTool), nil)
	_, blocks = blocksOf(t, r.body)
	if got := blocks[1]["content"].(map[string]any); got["error_code"] != "query_too_long" {
		t.Fatalf("long query: %v", got)
	}
}

// A conversation carrying earlier searches reads back into the model as its calls and their
// results, and content another service wrote is refused
func TestWebSearchHistory(t *testing.T) {
	model := newSeat(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		chatAnswer(w, "Still kittens.")
	})
	g, base := searchGateway(t, model, nil)
	sealed := sealSnippet("Very small cats.")
	history := fmt.Sprintf(`{"model":"m","max_tokens":50,"messages":[
		{"role":"user","content":"Find kittens"},
		{"role":"assistant","content":[
			{"type":"text","text":"Looking."},
			{"type":"server_tool_use","id":"srvtoolu_1","name":"web_search","input":{"query":"kittens"}},
			{"type":"web_search_tool_result","tool_use_id":"srvtoolu_1","content":[{"type":"web_search_result","title":"Kittens","url":"https://cats.example/kittens","encrypted_content":%q,"page_age":"2024-05-01"}]},
			{"type":"text","text":"Found them."}
		]},
		{"role":"user","content":"And now?"}
	]}`, sealed)
	r, _ := ask(t, g, base, http.MethodPost, anthropicMessages, history, nil)
	if r.status != http.StatusOK {
		t.Fatalf("status %d: %s", r.status, r.body)
	}
	msgs := model.call(0).body["messages"].([]any)
	if len(msgs) != 5 {
		t.Fatalf("turns: %v", msgs)
	}
	assistant := msgs[1].(map[string]any)
	call := assistant["tool_calls"].([]any)[0].(map[string]any)
	if assistant["content"] != "Looking." || call["id"] != "srvtoolu_1" || call["function"].(map[string]any)["name"] != "web_search" {
		t.Fatalf("assistant turn: %v", assistant)
	}
	result := msgs[2].(map[string]any)
	text, _ := result["content"].(string)
	if result["role"] != "tool" || result["tool_call_id"] != "srvtoolu_1" || !strings.Contains(text, "https://cats.example/kittens") || !strings.Contains(text, "Very small cats.") {
		t.Fatalf("tool turn: %v", result)
	}
	if after := msgs[3].(map[string]any); after["role"] != "assistant" || after["content"] != "Found them." {
		t.Fatalf("trailing text: %v", after)
	}
	foreign := strings.Replace(history, sealed, "EqwOTQ3NjI1YWY1ZGVjNjNmY2E5Y2E1OWJkNmU2OWVjZj", 1)
	r, _ = ask(t, g, base, http.MethodPost, anthropicMessages, foreign, nil)
	if r.status != http.StatusBadRequest || !strings.Contains(string(r.body), "did not write") {
		t.Fatalf("foreign content: %d %s", r.status, r.body)
	}
	failed := strings.Replace(history, fmt.Sprintf(`[{"type":"web_search_result","title":"Kittens","url":"https://cats.example/kittens","encrypted_content":%q,"page_age":"2024-05-01"}]`, sealed), `{"type":"web_search_tool_result_error","error_code":"unavailable"}`, 1)
	r, _ = ask(t, g, base, http.MethodPost, anthropicMessages, failed, nil)
	if r.status != http.StatusOK {
		t.Fatalf("failed search in history: %d %s", r.status, r.body)
	}
	if text, _ := model.call(model.count() - 1).body["messages"].([]any)[2].(map[string]any)["content"].(string); !strings.Contains(text, "unavailable") {
		t.Fatalf("failed search text: %q", text)
	}
}

// A runtime that fails after a search has started the stream ends it with an error event,
// and one that fails before any answer is passed through as its own status
func TestWebSearchRuntimeFails(t *testing.T) {
	searx := searxngWith(t, kittenPages[0])
	model := newSeat(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		if hasToolTurn(body) {
			refuse(w, http.StatusInternalServerError)
			return
		}
		searchStream(w, "kittens")
	})
	g, base := searchGateway(t, model, searxngSettings(searx))
	r, trace := ask(t, g, base, http.MethodPost, anthropicMessages, searchBody(true, kittenTool), nil)
	if r.status != http.StatusOK {
		t.Fatalf("status %d: %s", r.status, r.body)
	}
	events := eventsOf(t, r.body)
	last := events[len(events)-1]
	if last["type"] != "error" || !strings.Contains(last["error"].(map[string]any)["message"].(string), "seat refuses") {
		t.Fatalf("last event %v", last)
	}
	if trace.GetError() != "upstream: seat refuses" || trace.GetWebSearches() != 1 {
		t.Fatalf("trace %v", trace)
	}
	broken := newSeat(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		refuse(w, http.StatusInternalServerError)
	})
	g, base = searchGateway(t, broken, searxngSettings(searx))
	r, _ = ask(t, g, base, http.MethodPost, anthropicMessages, searchBody(false, kittenTool), nil)
	if r.status != http.StatusInternalServerError || !strings.Contains(string(r.body), "seat refuses") {
		t.Fatalf("first round failure: %d %s", r.status, r.body)
	}
}

// Tool definitions that clash with the search tool are refused by name
func TestWebSearchToolClash(t *testing.T) {
	model := newSeat(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) { chatAnswer(w, "never") })
	searx := searxngWith(t)
	g, base := searchGateway(t, model, searxngSettings(searx))
	for body, want := range map[string]string{
		searchBody(false, kittenTool+`,{"name":"web_search","input_schema":{"type":"object"}}`): "belongs to the web search tool",
		searchBody(false, kittenTool+`,`+kittenTool):                                            "one web search tool",
		searchBody(false, kittenTool+`,{"type":"web_search_20260301","name":"web_search"}`):     "one web search tool",
		searchBody(false, `{"type":"web_search_20250305","name":"search"}`):                     "named web_search",
		searchBody(false, `{"type":"web_search_20250305","name":"web_search","max_uses":0}`):    "at least 1",
		searchBody(false, `{"type":"text_editor_20250124","name":"str_replace_editor"}`):        "expected tags: 'custom'",
	} {
		r, _ := ask(t, g, base, http.MethodPost, anthropicMessages, body, nil)
		if r.status != http.StatusBadRequest || !strings.Contains(string(r.body), want) {
			t.Fatalf("%s: %d %s", want, r.status, r.body)
		}
	}
	if model.count() != 0 {
		t.Fatalf("the model was asked %d times", model.count())
	}
}

func TestUnderDomains(t *testing.T) {
	for page, want := range map[string]bool{
		"https://cats.example/kittens":    true,
		"https://pics.cats.example/album": true,
		"https://notcats.example/":        false,
		"https://cats.example.evil/":      false,
		"https://docs.example/blog/post":  true,
		"https://docs.example/blogs/post": false,
		"https://docs.example/blog":       true,
		"https://Docs.Example/other":      false,
		"not a url":                       false,
	} {
		if got := underDomains(page, []string{"cats.example", "https://docs.example/blog/"}); got != want {
			t.Errorf("%s: %v", page, got)
		}
	}
}
