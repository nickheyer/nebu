package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/nickheyer/nebu/internal/chat"
	"github.com/nickheyer/nebu/internal/tools"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/proto"
)

const kittenTool = `{"type":"web_search_20250305","name":"web_search","max_uses":8,"user_location":{"type":"approximate","city":"Portland","country":"US"}}`

// The request Claude Code's web search makes, with the tool as given
func searchBody(stream bool, tool string) string {
	return fmt.Sprintf(`{"model":"m","max_tokens":200,"stream":%v,"system":"You are an assistant for performing a web search tool use","messages":[{"role":"user","content":"Perform a search for pictures of cute little kittens!"}],"tools":[%s],"tool_choice":{"type":"auto"}}`, stream, tool)
}

// An OpenAI chat request, with tools when given
func oaiBody(stream bool, tools string) string {
	body := fmt.Sprintf(`{"model":"m","stream":%v,"messages":[{"role":"user","content":"Find kittens"}]`, stream)
	if tools != "" {
		body += `,"tools":` + tools
	}
	return body + "}"
}

// A search engine as a test stands it up, remembering what it was asked
type engine struct {
	*httptest.Server
	mu    sync.Mutex
	asked []url.Values
}

func (e *engine) query(i int) url.Values {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.asked[i]
}

func (e *engine) count() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.asked)
}

func newEngine(t *testing.T, answer func(w http.ResponseWriter, r *http.Request)) *engine {
	t.Helper()
	e := &engine{}
	e.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.mu.Lock()
		e.asked = append(e.asked, r.URL.Query())
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

// Whether the model has a tool result to read
func hasToolTurn(body map[string]any) bool {
	msgs, _ := body["messages"].([]any)
	for _, m := range msgs {
		if mm, ok := m.(map[string]any); ok && mm["role"] == "tool" {
			return true
		}
	}
	return false
}

// The names of the tools a request offered the model
func toolNames(body map[string]any) []string {
	var out []string
	list, _ := body["tools"].([]any)
	for _, t := range list {
		out = append(out, t.(map[string]any)["function"].(map[string]any)["name"].(string))
	}
	return out
}

// A finished OpenAI answer calling tools
func toolAnswer(w http.ResponseWriter, text string, calls ...map[string]any) {
	writeJSON(w, http.StatusOK, map[string]any{"id": "c1", "object": "chat.completion", "model": "m", "choices": []map[string]any{{"index": 0, "message": map[string]any{"role": "assistant", "content": text, "tool_calls": calls}, "finish_reason": "tool_calls"}}, "usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 5}})
}

func toolCall(id, name, args string) map[string]any {
	return map[string]any{"id": id, "type": "function", "function": map[string]any{"name": name, "arguments": args}}
}

func searchCall(id, query string) map[string]any {
	return toolCall(id, "web_search", fmt.Sprintf(`{"query":%q}`, query))
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

// A gateway serving one OpenAI seat as m with the given tool servers in Settings
func toolGateway(t *testing.T, model *seat, servers ...*v1.ToolServer) (*Gateway, *Table, string) {
	t.Helper()
	g, table := newGateway(t)
	table.Set("m", "i1", "", model.URL, "r:g", "m", v1.ApiFlavor_API_FLAVOR_OPENAI, nil, nil)
	g.tools.Apply(context.Background(), servers)
	wait, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	g.tools.Wait(wait)
	t.Cleanup(g.tools.Close)
	srv := httptest.NewServer(g.Handler())
	t.Cleanup(srv.Close)
	return g, table, srv.URL
}

func searxngServer(e *engine) *v1.ToolServer {
	return &v1.ToolServer{Name: "web", Enabled: true, Kind: v1.ToolServerKind_TOOL_SERVER_KIND_SEARCH, Provider: "searxng", SearxngUrl: e.URL}
}

func fetchServer() *v1.ToolServer {
	return &v1.ToolServer{Name: "fetch", Enabled: true, Kind: v1.ToolServerKind_TOOL_SERVER_KIND_FETCH}
}

// An MCP server over HTTP with a read only echo and a mutating write
func fakeMCP(t *testing.T) *httptest.Server {
	t.Helper()
	s := mcp.NewServer(&mcp.Implementation{Name: "fake", Version: "1"}, nil)
	type echoIn struct {
		Text string `json:"text"`
	}
	mcp.AddTool(s, &mcp.Tool{Name: "echo", Description: "Says it back", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, func(ctx context.Context, req *mcp.CallToolRequest, in echoIn) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "echo: " + in.Text}}}, nil, nil
	})
	type pathIn struct {
		Path string `json:"path"`
	}
	mcp.AddTool(s, &mcp.Tool{Name: "write_file", Description: "Writes"}, func(ctx context.Context, req *mcp.CallToolRequest, in pathIn) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "wrote " + in.Path}}}, nil, nil
	})
	srv := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s }, &mcp.StreamableHTTPOptions{DisableLocalhostProtection: true}))
	t.Cleanup(srv.Close)
	return srv
}

func mcpServer(name, url string) *v1.ToolServer {
	return &v1.ToolServer{Name: name, Enabled: true, Kind: v1.ToolServerKind_TOOL_SERVER_KIND_HTTP, Url: url}
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
	if err := chat.ReadSSE(strings.NewReader(string(body)), func(_ string, data []byte) error {
		if string(data) == "[DONE]" {
			return nil
		}
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

// The OpenAI answer's message and finish reason
func oaiAnswer(t *testing.T, body []byte) (map[string]any, string) {
	t.Helper()
	var reply map[string]any
	if err := json.Unmarshal(body, &reply); err != nil {
		t.Fatalf("reply is not JSON: %v: %s", err, body)
	}
	choice := reply["choices"].([]any)[0].(map[string]any)
	reason, _ := choice["finish_reason"].(string)
	return choice["message"].(map[string]any), reason
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
	g, _, base := toolGateway(t, model, searxngServer(searx))
	r, trace := ask(t, g, base, http.MethodPost, chat.AnthropicMessages, searchBody(false, kittenTool), nil)
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
	if snippet, ok := chat.OpenSnippet(first["encrypted_content"].(string)); !ok || snippet != "Very small cats." {
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
		if names := toolNames(model.call(i).body); strings.Join(names, ",") != "web_search" {
			t.Fatalf("call %d tools: %v", i, names)
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
	if !trace.GetTranslated() || trace.GetStatus() != http.StatusOK || trace.GetPromptTokens() != 13 || trace.GetCompletionTokens() != 6 {
		t.Fatalf("trace %v", trace)
	}
	runs := trace.GetToolCalls()
	if len(runs) != 1 || runs[0].GetName() != "web_search" || runs[0].GetServer() != "web" || runs[0].GetFailed() || !strings.Contains(runs[0].GetResult(), "Search results:") {
		t.Fatalf("trace tool calls %v", runs)
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
	g, _, base := toolGateway(t, model, searxngServer(searx))
	later := strings.Replace(kittenTool, "web_search_20250305", "web_search_20260301", 1)
	r, trace := ask(t, g, base, http.MethodPost, chat.AnthropicMessages, searchBody(true, later), nil)
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
	if len(trace.GetToolCalls()) != 1 || trace.GetResponse() != "Let me look.Found them." {
		t.Fatalf("trace %v", trace)
	}
	if q := searx.query(0); q.Get("q") != "cute kittens" {
		t.Fatalf("engine asked %v", q)
	}
}

// Every dated version of the server tools and their bare names are the same tool, and the
// model sees them as functions beside the client's own
func TestServerToolVersions(t *testing.T) {
	searx := searxngWith(t)
	model := newSeat(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) { chatAnswer(w, "never") })
	g, table, _ := toolGateway(t, model, searxngServer(searx), fetchServer())
	route, _ := table.Lookup("m")
	parse := func(tools string) *chat.Request {
		t.Helper()
		req, err := chat.ClientOf(v1.ApiFlavor_API_FLAVOR_ANTHROPIC).Parse(chat.AnthropicMessages, []byte(searchBody(false, tools)))
		if err != nil {
			t.Fatal(err)
		}
		return req
	}
	for _, kind := range []string{"web_search", "web_search_20250305", "web_search_20260301", "web_search_20301231"} {
		req := parse(fmt.Sprintf(`{"type":%q,"name":"web_search"},{"name":"other"}`, kind))
		plan, err := g.plan(context.Background(), req, route, http.Header{})
		if err != nil || !plan.active() || len(req.Tools) != 2 || req.Tools[0].Name != "other" || req.Tools[1].Name != tools.SearchTool || req.Tools[1].Type != "" {
			t.Fatalf("%s: %v %v", kind, err, req.Tools)
		}
		if l := plan.tools[tools.SearchTool].limits; l == nil || l.MaxUses != chat.ProvidedUses {
			t.Fatalf("%s: default limits %v", kind, l)
		}
	}
	req := parse(`{"type":"web_fetch_20250910","name":"web_fetch","max_content_tokens":100}`)
	plan, err := g.plan(context.Background(), req, route, http.Header{})
	if err != nil || len(req.Tools) != 1 || req.Tools[0].Name != tools.FetchTool || plan.tools[tools.FetchTool].limits.MaxChars != 400 {
		t.Fatalf("fetch: %v %v", err, req.Tools)
	}
	for _, kind := range []string{"web_searching", "text_editor_20250124", "custom", ""} {
		req := parse(fmt.Sprintf(`{"type":%q,"name":"editor","input_schema":{"type":"object"}}`, kind))
		if plan, err := g.plan(context.Background(), req, route, http.Header{}); err != nil || plan.active() {
			t.Fatalf("%s taken as a server tool: %v", kind, err)
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
		toolAnswer(w, "", searchCall("call_1", "cute kittens"), searchCall("call_2", "more kittens"), toolCall("call_3", "web_search", `{"q":"oops"}`))
	})
	g, _, base := toolGateway(t, model, searxngServer(searx))
	tool := `{"type":"web_search_20250305","name":"web_search","max_uses":1}`
	r, trace := ask(t, g, base, http.MethodPost, chat.AnthropicMessages, searchBody(false, tool), nil)
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
	if reply["usage"].(map[string]any)["server_tool_use"].(map[string]any)["web_search_requests"] != 1.0 {
		t.Fatalf("one search counted: %v", reply["usage"])
	}
	runs := trace.GetToolCalls()
	if len(runs) != 3 || runs[0].GetFailed() || !runs[1].GetFailed() || !runs[2].GetFailed() {
		t.Fatalf("trace runs %v", runs)
	}
	if searx.query(0).Get("q") != "cute kittens" || searx.count() != 1 {
		t.Fatalf("engine asked %d times", searx.count())
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
		toolAnswer(w, "", searchCall("call_1", "cute kittens"), toolCall("call_2", "get_weather", `{"city":"Portland"}`))
	})
	g, _, base := toolGateway(t, model, searxngServer(searx))
	withWeather := kittenTool + `,{"name":"get_weather","input_schema":{"type":"object","properties":{"city":{"type":"string"}}}}`
	r, trace := ask(t, g, base, http.MethodPost, chat.AnthropicMessages, searchBody(false, withWeather), nil)
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
	if names := toolNames(model.call(0).body); len(names) != 2 {
		t.Fatalf("the model had both tools: %v", names)
	}
	if calls := trace.GetToolCalls(); len(calls) != 2 || calls[0].GetServer() != "web" || calls[1].GetServer() != "" || calls[1].GetName() != "get_weather" {
		t.Fatalf("trace calls %v", calls)
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
	g, _, base := toolGateway(t, model, searxngServer(searx))
	urls := func(tool string) []string {
		r, _ := ask(t, g, base, http.MethodPost, chat.AnthropicMessages, searchBody(false, tool), nil)
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
	r, _ := ask(t, g, base, http.MethodPost, chat.AnthropicMessages, searchBody(false, `{"type":"web_search_20250305","name":"web_search","allowed_domains":["a.example"],"blocked_domains":["b.example"]}`), nil)
	if r.status != http.StatusBadRequest || !strings.Contains(string(r.body), "not both") {
		t.Fatalf("both lists: %d %s", r.status, r.body)
	}
}

// Without a search server the tool is refused up front, naming the setting to make
func TestWebSearchUnset(t *testing.T) {
	model := newSeat(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		chatAnswer(w, "never")
	})
	g, _, base := toolGateway(t, model)
	r, trace := ask(t, g, base, http.MethodPost, chat.AnthropicMessages, searchBody(true, kittenTool), nil)
	if r.status != http.StatusBadRequest || !strings.Contains(string(r.body), "web search tool server is added in Settings") {
		t.Fatalf("status %d: %s", r.status, r.body)
	}
	if trace.GetError() == "" || model.count() != 0 {
		t.Fatalf("trace %v after %d model calls", trace, model.count())
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
	g, _, base := toolGateway(t, model, searxngServer(searx))
	for _, c := range []struct {
		status int
		code   string
	}{{http.StatusForbidden, "unavailable"}, {http.StatusTooManyRequests, "too_many_requests"}, {http.StatusBadGateway, "unavailable"}} {
		status.Store(int32(c.status))
		r, trace := ask(t, g, base, http.MethodPost, chat.AnthropicMessages, searchBody(false, kittenTool), nil)
		if r.status != http.StatusOK {
			t.Fatalf("status %d: %s", r.status, r.body)
		}
		_, blocks := blocksOf(t, r.body)
		if got := blocks[1]["content"].(map[string]any); got["type"] != "web_search_tool_result_error" || got["error_code"] != c.code {
			t.Fatalf("%d: %v", c.status, got)
		}
		if runs := trace.GetToolCalls(); len(runs) != 1 || !runs[0].GetFailed() || trace.GetError() != "" {
			t.Fatalf("the search was attempted and the request succeeded: %v", trace)
		}
		last := model.call(model.count() - 1).body["messages"].([]any)
		text, _ := last[len(last)-1].(map[string]any)["content"].(string)
		if c.code == "too_many_requests" && !strings.Contains(text, "rate limiting") || c.code == "unavailable" && !strings.Contains(text, "unavailable") {
			t.Fatalf("%d: the model read %q", c.status, text)
		}
	}
}

// A conversation carrying earlier searches, fetches, and MCP calls reads back into the model as
// its calls and their results, and search content another service wrote is refused
func TestToolHistory(t *testing.T) {
	model := newSeat(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		chatAnswer(w, "Still kittens.")
	})
	g, _, base := toolGateway(t, model)
	sealed := chat.SealSnippet("Very small cats.")
	history := fmt.Sprintf(`{"model":"m","max_tokens":50,"messages":[
		{"role":"user","content":"Find kittens"},
		{"role":"assistant","content":[
			{"type":"text","text":"Looking."},
			{"type":"server_tool_use","id":"srvtoolu_1","name":"web_search","input":{"query":"kittens"}},
			{"type":"web_search_tool_result","tool_use_id":"srvtoolu_1","content":[{"type":"web_search_result","title":"Kittens","url":"https://cats.example/kittens","encrypted_content":%q,"page_age":"2024-05-01"}]},
			{"type":"server_tool_use","id":"srvtoolu_2","name":"web_fetch","input":{"url":"https://cats.example/kittens"}},
			{"type":"web_fetch_tool_result","tool_use_id":"srvtoolu_2","content":{"type":"web_fetch_result","url":"https://cats.example/kittens","retrieved_at":"2025-01-01T00:00:00Z","content":{"type":"document","title":"Kittens","source":{"type":"text","media_type":"text/plain","data":"All about kittens."}}}},
			{"type":"mcp_tool_use","id":"mcptoolu_3","name":"echo","server_name":"fake","input":{"text":"hi"}},
			{"type":"mcp_tool_result","tool_use_id":"mcptoolu_3","is_error":false,"content":[{"type":"text","text":"echo: hi"}]},
			{"type":"text","text":"Found them."}
		]},
		{"role":"user","content":"And now?"}
	]}`, sealed)
	r, _ := ask(t, g, base, http.MethodPost, chat.AnthropicMessages, history, nil)
	if r.status != http.StatusOK {
		t.Fatalf("status %d: %s", r.status, r.body)
	}
	msgs := model.call(0).body["messages"].([]any)
	if len(msgs) != 9 {
		t.Fatalf("turns: %d %v", len(msgs), msgs)
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
	if fetched, _ := msgs[4].(map[string]any)["content"].(string); !strings.Contains(fetched, "Kittens\nhttps://cats.example/kittens\n\nAll about kittens.") {
		t.Fatalf("fetch turn: %v", msgs[4])
	}
	echo := msgs[5].(map[string]any)["tool_calls"].([]any)[0].(map[string]any)["function"].(map[string]any)
	if echo["name"] != "fake__echo" || echo["arguments"] != `{"text":"hi"}` {
		t.Fatalf("mcp call: %v", echo)
	}
	if said, _ := msgs[6].(map[string]any)["content"].(string); said != "echo: hi" {
		t.Fatalf("mcp result: %v", msgs[6])
	}
	if after := msgs[7].(map[string]any); after["role"] != "assistant" || after["content"] != "Found them." {
		t.Fatalf("trailing text: %v", after)
	}
	foreign := strings.Replace(history, sealed, "EqwOTQ3NjI1YWY1ZGVjNjNmY2E5Y2E1OWJkNmU2OWVjZj", 1)
	r, _ = ask(t, g, base, http.MethodPost, chat.AnthropicMessages, foreign, nil)
	if r.status != http.StatusBadRequest || !strings.Contains(string(r.body), "did not write") {
		t.Fatalf("foreign content: %d %s", r.status, r.body)
	}
	failed := strings.Replace(history, fmt.Sprintf(`[{"type":"web_search_result","title":"Kittens","url":"https://cats.example/kittens","encrypted_content":%q,"page_age":"2024-05-01"}]`, sealed), `{"type":"web_search_tool_result_error","error_code":"unavailable"}`, 1)
	r, _ = ask(t, g, base, http.MethodPost, chat.AnthropicMessages, failed, nil)
	if r.status != http.StatusOK {
		t.Fatalf("failed search in history: %d %s", r.status, r.body)
	}
	if text, _ := model.call(model.count() - 1).body["messages"].([]any)[2].(map[string]any)["content"].(string); !strings.Contains(text, "unavailable") {
		t.Fatalf("failed search text: %q", text)
	}
}

// A runtime that fails after a search has started the stream ends it with an error event,
// and one that fails before any answer is passed through as its own status
func TestToolRuntimeFails(t *testing.T) {
	searx := searxngWith(t, kittenPages[0])
	model := newSeat(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		if hasToolTurn(body) {
			refuse(w, http.StatusInternalServerError)
			return
		}
		searchStream(w, "kittens")
	})
	g, _, base := toolGateway(t, model, searxngServer(searx))
	r, trace := ask(t, g, base, http.MethodPost, chat.AnthropicMessages, searchBody(true, kittenTool), nil)
	if r.status != http.StatusOK {
		t.Fatalf("status %d: %s", r.status, r.body)
	}
	events := eventsOf(t, r.body)
	last := events[len(events)-1]
	if last["type"] != "error" || !strings.Contains(last["error"].(map[string]any)["message"].(string), "seat refuses") {
		t.Fatalf("last event %v", last)
	}
	if trace.GetError() != "upstream: seat refuses" || len(trace.GetToolCalls()) != 1 {
		t.Fatalf("trace %v", trace)
	}
	broken := newSeat(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		refuse(w, http.StatusInternalServerError)
	})
	g, _, base = toolGateway(t, broken, searxngServer(searx))
	r, _ = ask(t, g, base, http.MethodPost, chat.AnthropicMessages, searchBody(false, kittenTool), nil)
	if r.status != http.StatusInternalServerError || !strings.Contains(string(r.body), "seat refuses") {
		t.Fatalf("first round failure: %d %s", r.status, r.body)
	}
}

// Tool definitions that clash with the gateway's tools are refused by name
func TestToolClash(t *testing.T) {
	model := newSeat(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) { chatAnswer(w, "never") })
	searx := searxngWith(t)
	g, table, base := toolGateway(t, model, searxngServer(searx))
	for body, want := range map[string]string{
		searchBody(false, kittenTool+`,{"name":"web_search","input_schema":{"type":"object"}}`): "belongs to the web search tool",
		searchBody(false, kittenTool+`,`+kittenTool):                                            "one web search tool",
		searchBody(false, kittenTool+`,{"type":"web_search_20260301","name":"web_search"}`):     "one web search tool",
		searchBody(false, `{"type":"web_search_20250305","name":"search"}`):                     "named web_search",
		searchBody(false, `{"type":"web_search_20250305","name":"web_search","max_uses":0}`):    "at least 1",
		searchBody(false, `{"type":"text_editor_20250124","name":"str_replace_editor"}`):        "expected tags: 'custom'",
	} {
		r, _ := ask(t, g, base, http.MethodPost, chat.AnthropicMessages, body, nil)
		if r.status != http.StatusBadRequest || !strings.Contains(string(r.body), want) {
			t.Fatalf("%s: %d %s", want, r.status, r.body)
		}
	}
	// A client's own function cannot take a name a route's tool server offers.
	table.Set("m", "i1", "", model.URL, "r:g", "m", v1.ApiFlavor_API_FLAVOR_OPENAI, nil, &v1.Profile{ToolServers: []string{"web"}})
	r, _ := ask(t, g, base, http.MethodPost, chat.OpenAIChat, oaiBody(false, `[{"type":"function","function":{"name":"web_search","parameters":{"type":"object"}}}]`), nil)
	if r.status != http.StatusBadRequest || !strings.Contains(string(r.body), "belongs to tool server web") {
		t.Fatalf("route clash: %d %s", r.status, r.body)
	}
	r, _ = ask(t, g, base, http.MethodPost, chat.OpenAIChat, oaiBody(false, ""), map[string]string{ApproveHeader: "sometimes"})
	if r.status != http.StatusBadRequest || !strings.Contains(string(r.body), "mutating, all, or unset") {
		t.Fatalf("bad approval header: %d %s", r.status, r.body)
	}
	if model.count() != 0 {
		t.Fatalf("the model was asked %d times", model.count())
	}
}

// A client offering web_fetch gets pages read by the gateway and written as documents
func TestWebFetch(t *testing.T) {
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte("<html><head><title>Kittens</title></head><body><p>All about kittens.</p></body></html>"))
	}))
	t.Cleanup(site.Close)
	model := newSeat(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		if hasToolTurn(body) {
			chatAnswer(w, "Read it.")
			return
		}
		toolAnswer(w, "", toolCall("call_1", "web_fetch", fmt.Sprintf(`{"url":%q}`, site.URL+"/kittens")), toolCall("call_2", "web_fetch", `{"url":"ftp://nope"}`))
	})
	g, _, base := toolGateway(t, model, fetchServer())
	tool := `{"type":"web_fetch_20250910","name":"web_fetch","max_uses":5}`
	r, trace := ask(t, g, base, http.MethodPost, chat.AnthropicMessages, searchBody(false, tool), nil)
	if r.status != http.StatusOK {
		t.Fatalf("status %d: %s", r.status, r.body)
	}
	reply, blocks := blocksOf(t, r.body)
	if len(blocks) != 5 || blocks[0]["type"] != "server_tool_use" || blocks[0]["name"] != "web_fetch" || blocks[1]["type"] != "web_fetch_tool_result" {
		t.Fatalf("blocks: %s", r.body)
	}
	page := blocks[1]["content"].(map[string]any)
	doc := page["content"].(map[string]any)
	if page["type"] != "web_fetch_result" || page["url"] != site.URL+"/kittens" || page["retrieved_at"] == "" || doc["title"] != "Kittens" || doc["source"].(map[string]any)["data"] != "All about kittens." {
		t.Fatalf("page: %v", page)
	}
	if failure := blocks[3]["content"].(map[string]any); failure["type"] != "web_fetch_tool_result_error" || failure["error_code"] != "invalid_input" {
		t.Fatalf("failure: %v", failure)
	}
	if reply["usage"].(map[string]any)["server_tool_use"].(map[string]any)["web_fetch_requests"] != 1.0 {
		t.Fatalf("usage %v", reply["usage"])
	}
	if names := toolNames(model.call(0).body); strings.Join(names, ",") != "web_fetch" {
		t.Fatalf("model tools %v", names)
	}
	if runs := trace.GetToolCalls(); len(runs) != 2 || runs[0].GetServer() != "fetch" || !strings.Contains(runs[0].GetResult(), "All about kittens") {
		t.Fatalf("trace %v", runs)
	}
}

// A route whose profile attaches a tool server hands every client the tools, whatever its
// format: an OpenAI or Ollama client gets the final answer, the trace shows what ran
func TestRouteTools(t *testing.T) {
	searx := searxngWith(t, kittenPages[0])
	model := newSeat(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		streaming := body["stream"] == true
		switch {
		case hasToolTurn(body) && streaming:
			chatStream(w, "Kittens found.")
		case hasToolTurn(body):
			chatAnswer(w, "Kittens found.")
		case streaming:
			searchStream(w, "kittens")
		default:
			toolAnswer(w, "", searchCall("call_1", "kittens"))
		}
	})
	g, table, base := toolGateway(t, model, searxngServer(searx))
	table.Set("m", "i1", "", model.URL, "r:g", "m", v1.ApiFlavor_API_FLAVOR_OPENAI, nil, &v1.Profile{ToolServers: []string{"web"}})
	r, trace := ask(t, g, base, http.MethodPost, chat.OpenAIChat, oaiBody(false, ""), nil)
	if r.status != http.StatusOK {
		t.Fatalf("status %d: %s", r.status, r.body)
	}
	msg, reason := oaiAnswer(t, r.body)
	if msg["content"] != "Kittens found." || msg["tool_calls"] != nil || reason != "stop" {
		t.Fatalf("answer %v %s", msg, reason)
	}
	if names := toolNames(model.call(0).body); strings.Join(names, ",") != "web_search" {
		t.Fatalf("the model was offered %v", names)
	}
	if runs := trace.GetToolCalls(); len(runs) != 1 || runs[0].GetServer() != "web" || runs[0].GetTookMs() > 60000 {
		t.Fatalf("trace %v", runs)
	}
	if trace.GetTranslated() || trace.GetPromptTokens() != 13 {
		t.Fatalf("trace %v", trace)
	}
	r, _ = ask(t, g, base, http.MethodPost, chat.OpenAIChat, oaiBody(true, ""), nil)
	if r.status != http.StatusOK || !strings.Contains(string(r.body), `"content":"Kittens found."`) || strings.Contains(string(r.body), "tool_calls") {
		t.Fatalf("streamed: %d %s", r.status, r.body)
	}
	r, _ = ask(t, g, base, http.MethodPost, chat.OllamaChat, `{"model":"m","stream":false,"messages":[{"role":"user","content":"Find kittens"}]}`, nil)
	if r.status != http.StatusOK || !strings.Contains(string(r.body), `"content":"Kittens found."`) {
		t.Fatalf("ollama: %d %s", r.status, r.body)
	}
	// A client's own tools stay beside the route's.
	r, _ = ask(t, g, base, http.MethodPost, chat.OpenAIChat, oaiBody(false, `[{"type":"function","function":{"name":"get_weather","parameters":{"type":"object"}}}]`), nil)
	if r.status != http.StatusOK {
		t.Fatalf("status %d: %s", r.status, r.body)
	}
	if names := toolNames(model.call(model.count() - 2).body); strings.Join(names, ",") != "get_weather,web_search" {
		t.Fatalf("the model was offered %v", names)
	}
	// A route naming a server Settings no longer hold serves without it.
	table.Set("m", "i1", "", model.URL, "r:g", "m", v1.ApiFlavor_API_FLAVOR_OPENAI, nil, &v1.Profile{ToolServers: []string{"gone"}})
	r, _ = ask(t, g, base, http.MethodPost, chat.OpenAIChat, oaiBody(false, ""), nil)
	if r.status != http.StatusOK || toolNames(model.call(model.count()-1).body) != nil {
		t.Fatalf("missing server: %d %s %v", r.status, r.body, toolNames(model.call(model.count()-1).body))
	}
}

// A request attaching MCP servers by header gets their read only tools, their mutating ones too
// when it says so, and the calls run over MCP with the answers handed to the model
func TestHeaderTools(t *testing.T) {
	fake := fakeMCP(t)
	model := newSeat(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		if hasToolTurn(body) {
			chatAnswer(w, "Done.")
			return
		}
		toolAnswer(w, "", toolCall("call_1", "fake__echo", `{"text":"hi"}`), toolCall("call_2", "fake__write_file", `{"path":"/x"}`))
	})
	g, _, base := toolGateway(t, model, mcpServer("fake", fake.URL))
	r, trace := ask(t, g, base, http.MethodPost, chat.OpenAIChat, oaiBody(false, ""), map[string]string{ToolsHeader: "fake"})
	if r.status != http.StatusOK {
		t.Fatalf("status %d: %s", r.status, r.body)
	}
	if names := toolNames(model.call(0).body); strings.Join(names, ",") != "fake__echo" {
		t.Fatalf("read only offer %v", names)
	}
	// The mutating call is not the gateway's to run, so it goes back to the client.
	msg, reason := oaiAnswer(t, r.body)
	if reason != "tool_calls" || len(msg["tool_calls"].([]any)) != 1 || msg["tool_calls"].([]any)[0].(map[string]any)["function"].(map[string]any)["name"] != "fake__write_file" {
		t.Fatalf("answer %v %s", msg, reason)
	}
	if runs := trace.GetToolCalls(); len(runs) != 2 || runs[0].GetServer() != "fake" || runs[0].GetResult() != "echo: hi" || runs[1].GetServer() != "" {
		t.Fatalf("trace %v", runs)
	}
	r, trace = ask(t, g, base, http.MethodPost, chat.OpenAIChat, oaiBody(false, ""), map[string]string{ToolsHeader: "fake", MutatingHeader: "true"})
	if r.status != http.StatusOK {
		t.Fatalf("status %d: %s", r.status, r.body)
	}
	if names := toolNames(model.call(model.count() - 2).body); strings.Join(names, ",") != "fake__echo,fake__write_file" {
		t.Fatalf("mutating offer %v", names)
	}
	if msg, reason := oaiAnswer(t, r.body); msg["content"] != "Done." || reason != "stop" {
		t.Fatalf("answer %v %s", msg, reason)
	}
	if runs := trace.GetToolCalls(); len(runs) != 2 || runs[1].GetResult() != "wrote /x" {
		t.Fatalf("trace %v", runs)
	}
	r, _ = ask(t, g, base, http.MethodPost, chat.OpenAIChat, oaiBody(false, ""), map[string]string{ToolsHeader: "nope"})
	if r.status != http.StatusBadRequest || !strings.Contains(string(r.body), "unknown tool server") {
		t.Fatalf("unknown server: %d %s", r.status, r.body)
	}
}

// With approval asked, the gateway hands matching calls back to the client as its own tool
// calls instead of running them, and runs the rest
func TestApproval(t *testing.T) {
	fake := fakeMCP(t)
	model := newSeat(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		if hasToolTurn(body) {
			chatAnswer(w, "Done.")
			return
		}
		toolAnswer(w, "", toolCall("call_1", "fake__echo", `{"text":"hi"}`), toolCall("call_2", "fake__write_file", `{"path":"/x"}`))
	})
	g, _, base := toolGateway(t, model, mcpServer("fake", fake.URL))
	headers := map[string]string{ToolsHeader: "fake", MutatingHeader: "true", ApproveHeader: "mutating"}
	r, trace := ask(t, g, base, http.MethodPost, chat.OpenAIChat, oaiBody(false, ""), headers)
	if r.status != http.StatusOK {
		t.Fatalf("status %d: %s", r.status, r.body)
	}
	msg, reason := oaiAnswer(t, r.body)
	calls := msg["tool_calls"].([]any)
	if reason != "tool_calls" || len(calls) != 1 || calls[0].(map[string]any)["id"] != "call_2" {
		t.Fatalf("held calls %v %s", msg, reason)
	}
	if runs := trace.GetToolCalls(); len(runs) != 2 || runs[0].GetServer() != "fake" || runs[0].GetResult() != "echo: hi" || runs[1].GetServer() != "" || runs[1].GetName() != "fake__write_file" {
		t.Fatalf("trace %v", runs)
	}
	headers[ApproveHeader] = "all"
	r, _ = ask(t, g, base, http.MethodPost, chat.OpenAIChat, oaiBody(false, ""), headers)
	if msg, _ := oaiAnswer(t, r.body); len(msg["tool_calls"].([]any)) != 2 {
		t.Fatalf("all held %v", msg)
	}
}

// An Anthropic request naming MCP endpoints gets them connected for the request, their tools
// run by the gateway and written as MCP blocks, and the endpoints closed after
func TestMCPServersRequest(t *testing.T) {
	fake := fakeMCP(t)
	model := newSeat(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		if hasToolTurn(body) {
			chatAnswer(w, "Done.")
			return
		}
		toolAnswer(w, "Calling.", toolCall("call_1", "tools__echo", `{"text":"hi"}`))
	})
	g, _, base := toolGateway(t, model)
	body := fmt.Sprintf(`{"model":"m","max_tokens":50,"messages":[{"role":"user","content":"hi"}],"mcp_servers":[{"type":"url","url":%q,"name":"tools","authorization_token":"tok","tool_configuration":{"allowed_tools":["echo"]}}]}`, fake.URL)
	r, trace := ask(t, g, base, http.MethodPost, chat.AnthropicMessages, body, nil)
	if r.status != http.StatusOK {
		t.Fatalf("status %d: %s", r.status, r.body)
	}
	_, blocks := blocksOf(t, r.body)
	if len(blocks) != 4 || blocks[1]["type"] != "mcp_tool_use" || blocks[2]["type"] != "mcp_tool_result" {
		t.Fatalf("blocks: %s", r.body)
	}
	use := blocks[1]
	if use["name"] != "echo" || use["server_name"] != "tools" || use["input"].(map[string]any)["text"] != "hi" || !strings.HasPrefix(use["id"].(string), "mcptoolu_") {
		t.Fatalf("mcp_tool_use %v", use)
	}
	res := blocks[2]
	if res["tool_use_id"] != use["id"] || res["is_error"] != false || res["content"].([]any)[0].(map[string]any)["text"] != "echo: hi" {
		t.Fatalf("mcp_tool_result %v", res)
	}
	if names := toolNames(model.call(0).body); strings.Join(names, ",") != "tools__echo" {
		t.Fatalf("model tools %v", names)
	}
	if _, has := model.call(0).body["mcp_servers"]; has {
		t.Fatal("the runtime never sees mcp_servers")
	}
	if runs := trace.GetToolCalls(); len(runs) != 1 || runs[0].GetServer() != "tools" {
		t.Fatalf("trace %v", runs)
	}
	r, _ = ask(t, g, base, http.MethodPost, chat.AnthropicMessages, strings.Replace(body, fake.URL, "http://127.0.0.1:1/mcp", 1), nil)
	if r.status != http.StatusBadRequest || !strings.Contains(string(r.body), "could not be reached") {
		t.Fatalf("unreachable: %d %s", r.status, r.body)
	}
}

// A model that keeps calling tools is cut off after the round limit: the limit is explained to
// it, its tools withdrawn, and the answer it gives then is the client's
func TestRoundCap(t *testing.T) {
	searx := searxngWith(t, kittenPages[0])
	model := newSeat(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		if _, has := body["tools"]; !has {
			chatAnswer(w, "Giving up.")
			return
		}
		toolAnswer(w, "", searchCall(fmt.Sprintf("call_%d", len(body["messages"].([]any))), "kittens"))
	})
	g, table, base := toolGateway(t, model, searxngServer(searx))
	table.Set("m", "i1", "", model.URL, "r:g", "m", v1.ApiFlavor_API_FLAVOR_OPENAI, nil, &v1.Profile{ToolServers: []string{"web"}})
	r, trace := ask(t, g, base, http.MethodPost, chat.OpenAIChat, oaiBody(false, ""), nil)
	if r.status != http.StatusOK {
		t.Fatalf("status %d: %s", r.status, r.body)
	}
	if msg, _ := oaiAnswer(t, r.body); msg["content"] != "Giving up." {
		t.Fatalf("answer %v", msg)
	}
	if model.count() != maxToolRounds+2 || searx.count() != maxToolRounds {
		t.Fatalf("%d rounds, %d searches", model.count(), searx.count())
	}
	runs := trace.GetToolCalls()
	if len(runs) != maxToolRounds+1 || !runs[len(runs)-1].GetFailed() || !strings.Contains(runs[len(runs)-1].GetResult(), "limit") {
		t.Fatalf("trace runs %d: %v", len(runs), runs[len(runs)-1])
	}
}

// A runtime that refuses tool definitions gets them written into the prompt, the calls the
// model writes come back as tool calls, and the results go in as text turns
func TestEmulated(t *testing.T) {
	model := newSeat(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		if _, has := body["tools"]; has {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"message": "this template takes no tools"}})
			return
		}
		msgs := body["messages"].([]any)
		last, _ := msgs[len(msgs)-1].(map[string]any)["content"].(string)
		if strings.Contains(last, chat.ResponseOpen) {
			chatAnswer(w, "It is sunny in Portland.")
			return
		}
		if body["stream"] == true {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			for _, piece := range []string{"Checking.", " <tool_", `call>{"name":"get_weather","arg`, `uments":{"city":"Portland"}}</tool_call>`} {
				fmt.Fprintf(w, "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"content\":%q}}]}\n\n", piece)
			}
			fmt.Fprint(w, "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":1}}\n\n")
			fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}
		chatAnswer(w, "Checking.\n<tool_call>\n{\"name\": \"get_weather\", \"arguments\": {\"city\": \"Portland\"}}\n</tool_call>")
	})
	g, table, base := toolGateway(t, model)
	table.Serve("m", &v1.Instance{Id: "i1", Endpoint: model.URL, Name: "m", Template: &v1.TemplateProbe{Tools: v1.ToolSupport_TOOL_SUPPORT_EMULATED}}, v1.ApiFlavor_API_FLAVOR_OPENAI, "", nil, nil)
	if route, _ := table.Lookup("m"); route.GetTools() != v1.ToolSupport_TOOL_SUPPORT_EMULATED {
		t.Fatalf("route tools %v", route.GetTools())
	}
	weather := `[{"type":"function","function":{"name":"get_weather","description":"Reads the weather","parameters":{"type":"object","properties":{"city":{"type":"string"}}}}}]`
	r, trace := ask(t, g, base, http.MethodPost, chat.OpenAIChat, oaiBody(false, weather), nil)
	if r.status != http.StatusOK {
		t.Fatalf("status %d: %s", r.status, r.body)
	}
	msg, reason := oaiAnswer(t, r.body)
	calls, _ := msg["tool_calls"].([]any)
	if reason != "tool_calls" || len(calls) != 1 || msg["content"] != "Checking.\n" {
		t.Fatalf("answer %v %s", msg, reason)
	}
	call := calls[0].(map[string]any)
	if call["function"].(map[string]any)["name"] != "get_weather" || call["function"].(map[string]any)["arguments"] != `{"city": "Portland"}` || !strings.HasPrefix(call["id"].(string), "call_") {
		t.Fatalf("call %v", call)
	}
	sent := model.call(0).body
	if _, has := sent["tools"]; has {
		t.Fatal("the runtime saw tools")
	}
	system, _ := sent["messages"].([]any)[0].(map[string]any)["content"].(string)
	if !strings.Contains(system, "# Tools") || !strings.Contains(system, "## get_weather") || !strings.Contains(system, "Reads the weather") || !strings.Contains(system, `"city"`) {
		t.Fatalf("system prompt %q", system)
	}
	if len(trace.GetToolCalls()) != 1 || trace.GetToolCalls()[0].GetName() != "get_weather" {
		t.Fatalf("trace %v", trace.GetToolCalls())
	}
	// Streamed, the call arrives once its block closes, the text before it as it came.
	r, _ = ask(t, g, base, http.MethodPost, chat.OpenAIChat, oaiBody(true, weather), nil)
	if r.status != http.StatusOK {
		t.Fatalf("status %d: %s", r.status, r.body)
	}
	var texts []string
	var streamed []string
	for _, ev := range eventsOf(t, r.body) {
		for _, c := range ev["choices"].([]any) {
			delta := c.(map[string]any)["delta"].(map[string]any)
			if s, _ := delta["content"].(string); s != "" {
				texts = append(texts, s)
			}
			calls, _ := delta["tool_calls"].([]any)
			for _, tc := range calls {
				streamed = append(streamed, tc.(map[string]any)["function"].(map[string]any)["name"].(string))
			}
		}
	}
	if strings.Join(texts, "|") != "Checking.| " || strings.Join(streamed, ",") != "get_weather" {
		t.Fatalf("streamed %v %v", texts, streamed)
	}
	// The result turn reaches the runtime as a user turn carrying the response block.
	followup := `{"model":"m","messages":[{"role":"user","content":"Weather?"},{"role":"assistant","content":"Checking.","tool_calls":[{"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"Portland\"}"}}]},{"role":"tool","tool_call_id":"call_1","content":"sunny"}],"tools":` + weather + `}`
	r, _ = ask(t, g, base, http.MethodPost, chat.OpenAIChat, followup, nil)
	if r.status != http.StatusOK {
		t.Fatalf("status %d: %s", r.status, r.body)
	}
	if msg, _ := oaiAnswer(t, r.body); msg["content"] != "It is sunny in Portland." {
		t.Fatalf("answer %v", msg)
	}
	msgs := model.call(model.count() - 1).body["messages"].([]any)
	assistant, _ := msgs[2].(map[string]any)["content"].(string)
	user := msgs[3].(map[string]any)
	userText, _ := user["content"].(string)
	if !strings.Contains(assistant, chat.CallOpen) || !strings.Contains(assistant, `"get_weather"`) || user["role"] != "user" || !strings.Contains(userText, `<tool_response id="call_1">`) || !strings.Contains(userText, "sunny") {
		t.Fatalf("folded turns %v", msgs)
	}
}

// The template probe reports whether a runtime takes tools, and names its refusal otherwise
func TestProbeTemplateTools(t *testing.T) {
	refusing := newSeat(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		if _, has := body["tools"]; has {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"message": "tools need --jinja"}})
			return
		}
		chatAnswer(w, "ok")
	})
	probe := ProbeTemplate(context.Background(), &http.Client{}, refusing.URL, v1.ApiFlavor_API_FLAVOR_OPENAI, "m")
	if probe.GetError() != "" || !probe.GetLateSystem() || probe.GetTools() != v1.ToolSupport_TOOL_SUPPORT_EMULATED || probe.GetToolsRefusal() != "tools need --jinja" {
		t.Fatalf("probe %v", probe)
	}
	taking := newSeat(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) { chatAnswer(w, "ok") })
	probe = ProbeTemplate(context.Background(), &http.Client{}, taking.URL, v1.ApiFlavor_API_FLAVOR_OPENAI, "m")
	if probe.GetTools() != v1.ToolSupport_TOOL_SUPPORT_NATIVE || probe.GetToolsRefusal() != "" {
		t.Fatalf("probe %v", probe)
	}
	if calls := taking.count(); calls != 3 {
		t.Fatalf("%d probe requests", calls)
	}
}

// Profiles merge their tool servers the way they merge the rest: an alias naming any replaces
// the slot's, an alias naming none inherits them
func TestEffectiveProfileTools(t *testing.T) {
	slot := &v1.Profile{SystemMessages: v1.SystemMessages_SYSTEM_MESSAGES_KEEP, ToolServers: []string{"web"}, MutatingTools: true}
	got := EffectiveProfile(&v1.Profile{AllowAll: proto.Bool(true)}, slot)
	if strings.Join(got.GetToolServers(), ",") != "web" || !got.GetMutatingTools() || !got.GetAllowAll() {
		t.Fatalf("inherited %v", got)
	}
	got = EffectiveProfile(&v1.Profile{ToolServers: []string{"fs"}}, slot)
	if strings.Join(got.GetToolServers(), ",") != "fs" || got.GetMutatingTools() || got.GetSystemMessages() != v1.SystemMessages_SYSTEM_MESSAGES_KEEP {
		t.Fatalf("replaced %v", got)
	}
	if !ProfileSet(&v1.Profile{MutatingTools: true}) || ProfileSet(&v1.Profile{}) {
		t.Fatal("set")
	}
	if _, err := errors.New("x"), error(nil); err != nil {
		t.Fatal(err)
	}
}
