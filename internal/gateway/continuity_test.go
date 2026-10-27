package gateway

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/nickheyer/nebu/internal/chat"
	"github.com/nickheyer/nebu/pkg/interceptors"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// The roles of the turns a seat was sent, comma separated
func rolesOf(body map[string]any) string {
	var out []string
	for _, m := range body["messages"].([]any) {
		out = append(out, m.(map[string]any)["role"].(string))
	}
	return strings.Join(out, ",")
}

// A seat that searches once, then answers once it can read a result
func searchingSeat(t *testing.T) *seat {
	return newSeat(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
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
}

// An echoed answer gets its search put back, whole or streamed, a ghost answer the same
func TestRoundsReturnToTheConversation(t *testing.T) {
	searx := searxngWith(t, kittenPages[0])
	model := searchingSeat(t)
	g, table, base := toolGateway(t, model, searxngServer(searx))
	table.Set("m", "i1", "", model.URL, "r:g", "m", v1.ApiFlavor_API_FLAVOR_OPENAI, nil, &v1.Profile{ToolServers: []string{"web"}})
	r, _ := ask(t, g, base, http.MethodPost, chat.OpenAIChat, oaiBody(false, ""), nil)
	if msg, _ := oaiAnswer(t, r.body); r.status != http.StatusOK || msg["content"] != "Kittens found." {
		t.Fatalf("first answer %d %s", r.status, r.body)
	}
	echo := `{"model":"m","messages":[{"role":"user","content":"Find kittens"},{"role":"assistant","content":"Kittens found."},{"role":"user","content":"And now?"}]}`
	r, trace := ask(t, g, base, http.MethodPost, chat.OpenAIChat, echo, nil)
	if r.status != http.StatusOK {
		t.Fatalf("second answer %d %s", r.status, r.body)
	}
	sent := model.call(model.count() - 1).body
	if rolesOf(sent) != "user,assistant,tool,assistant,user" {
		t.Fatalf("the search went back behind the answer: %s", rolesOf(sent))
	}
	msgs := sent["messages"].([]any)
	call := msgs[1].(map[string]any)["tool_calls"].([]any)[0].(map[string]any)
	if call["id"] != "call_1" || call["function"].(map[string]any)["name"] != "web_search" {
		t.Fatalf("search call %v", call)
	}
	if result := msgs[2].(map[string]any); result["tool_call_id"] != "call_1" || !strings.Contains(result["content"].(string), "https://cats.example/kittens") {
		t.Fatalf("search result %v", result)
	}
	if msgs[3].(map[string]any)["content"] != "Kittens found." || msgs[4].(map[string]any)["content"] != "And now?" {
		t.Fatalf("the echoed turns stay: %v", msgs[3:])
	}
	if !strings.Contains(trace.GetUpstreamRequest(), "https://cats.example/kittens") {
		t.Fatal("the trace shows the request as the runtime got it")
	}
	// Streamed, the client's echo is every round's text run together
	r, _ = ask(t, g, base, http.MethodPost, chat.OpenAIChat, oaiBody(true, ""), nil)
	if r.status != http.StatusOK || !strings.Contains(string(r.body), `"content":"Let me look."`) || !strings.Contains(string(r.body), `"content":"Kittens found."`) {
		t.Fatalf("streamed %d %s", r.status, r.body)
	}
	echo = `{"model":"m","messages":[{"role":"user","content":"Find kittens"},{"role":"assistant","content":"Let me look.Kittens found."},{"role":"user","content":"And now?"}]}`
	r, _ = ask(t, g, base, http.MethodPost, chat.OpenAIChat, echo, nil)
	sent = model.call(model.count() - 1).body
	msgs = sent["messages"].([]any)
	if r.status != http.StatusOK || rolesOf(sent) != "user,assistant,tool,assistant,user" || msgs[1].(map[string]any)["content"] != "Let me look." || msgs[3].(map[string]any)["content"] != "Kittens found." {
		t.Fatalf("streamed echo: %d %s %v", r.status, rolesOf(sent), msgs)
	}
	// A bare echo is left as sent
	r, _ = ask(t, g, base, http.MethodPost, chat.OpenAIChat, echo, map[string]string{interceptors.BareHeader: "1"})
	if sent = model.call(model.count() - 1).body; r.status != http.StatusOK || rolesOf(sent) != "user,assistant,user" {
		t.Fatalf("bare echo: %d %s", r.status, rolesOf(sent))
	}
	// A ghost conversation leaves no trace and still carries on with its rounds
	ghost := `{"model":"m","messages":[{"role":"user","content":"Find kittens quietly"}]}`
	r, trace = ask(t, g, base, http.MethodPost, chat.OpenAIChat, ghost, map[string]string{interceptors.GhostHeader: "1"})
	if r.status != http.StatusOK || trace != nil {
		t.Fatalf("ghost %d %s trace %v", r.status, r.body, trace)
	}
	echo = `{"model":"m","messages":[{"role":"user","content":"Find kittens quietly"},{"role":"assistant","content":"Kittens found."},{"role":"user","content":"And now?"}]}`
	r, trace = ask(t, g, base, http.MethodPost, chat.OpenAIChat, echo, map[string]string{interceptors.GhostHeader: "1"})
	if sent = model.call(model.count() - 1).body; r.status != http.StatusOK || trace != nil || rolesOf(sent) != "user,assistant,tool,assistant,user" {
		t.Fatalf("ghost echo: %d %s", r.status, rolesOf(sent))
	}
}

// A search run beside a call handed back comes back with it, its result ahead of the client's
func TestRoundsReturnBesideHeldCalls(t *testing.T) {
	searx := searxngWith(t, kittenPages[0])
	fake := fakeMCP(t)
	model := newSeat(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		if hasToolTurn(body) {
			chatAnswer(w, "Done.")
			return
		}
		toolAnswer(w, "On it.", searchCall("call_1", "kittens"), toolCall("call_2", "fake__write_file", `{"path":"/x"}`))
	})
	g, table, base := toolGateway(t, model, searxngServer(searx), mcpServer("fake", fake.URL))
	table.Set("m", "i1", "", model.URL, "r:g", "m", v1.ApiFlavor_API_FLAVOR_OPENAI, nil, &v1.Profile{ToolServers: []string{"web"}})
	headers := map[string]string{ToolsHeader: "fake", MutatingHeader: "true", ApproveHeader: "mutating"}
	r, _ := ask(t, g, base, http.MethodPost, chat.OpenAIChat, oaiBody(false, ""), headers)
	msg, reason := oaiAnswer(t, r.body)
	if r.status != http.StatusOK || reason != "tool_calls" || msg["content"] != "On it." || len(msg["tool_calls"].([]any)) != 1 {
		t.Fatalf("held answer %d %v %s", r.status, msg, reason)
	}
	echo := `{"model":"m","messages":[{"role":"user","content":"Find kittens"},{"role":"assistant","content":"On it.","tool_calls":[{"id":"call_2","type":"function","function":{"name":"fake__write_file","arguments":"{\"path\": \"/x\"}"}}]},{"role":"tool","tool_call_id":"call_2","content":"wrote /x"}]}`
	r, _ = ask(t, g, base, http.MethodPost, chat.OpenAIChat, echo, headers)
	if msg, _ := oaiAnswer(t, r.body); r.status != http.StatusOK || msg["content"] != "Done." {
		t.Fatalf("after the held call %d %s", r.status, r.body)
	}
	sent := model.call(model.count() - 1).body
	if rolesOf(sent) != "user,assistant,tool,tool" {
		t.Fatalf("the search and its result come back beside the held call: %s", rolesOf(sent))
	}
	msgs := sent["messages"].([]any)
	calls := msgs[1].(map[string]any)["tool_calls"].([]any)
	if len(calls) != 2 || calls[0].(map[string]any)["id"] != "call_1" || calls[1].(map[string]any)["id"] != "call_2" || msgs[1].(map[string]any)["content"] != "On it." {
		t.Fatalf("assistant turn %v", msgs[1])
	}
	if first, second := msgs[2].(map[string]any), msgs[3].(map[string]any); first["tool_call_id"] != "call_1" || !strings.Contains(first["content"].(string), "cats.example") || second["tool_call_id"] != "call_2" || second["content"] != "wrote /x" {
		t.Fatalf("tool turns %v %v", first, second)
	}
}

// Ollama and Responses clients echo answers in their own shapes and get the rounds back the same way
func TestRoundsReturnInEveryProtocol(t *testing.T) {
	searx := searxngWith(t, kittenPages[0])
	model := searchingSeat(t)
	g, table, base := toolGateway(t, model, searxngServer(searx))
	table.Set("m", "i1", "", model.URL, "r:g", "m", v1.ApiFlavor_API_FLAVOR_OPENAI, nil, &v1.Profile{ToolServers: []string{"web"}})
	r, _ := ask(t, g, base, http.MethodPost, chat.OllamaChat, `{"model":"m","stream":false,"messages":[{"role":"user","content":"Find kittens"}]}`, nil)
	if r.status != http.StatusOK || !strings.Contains(string(r.body), "Kittens found.") {
		t.Fatalf("ollama %d %s", r.status, r.body)
	}
	echo := `{"model":"m","stream":false,"messages":[{"role":"user","content":"Find kittens"},{"role":"assistant","content":"Kittens found."},{"role":"user","content":"And now?"}]}`
	r, _ = ask(t, g, base, http.MethodPost, chat.OllamaChat, echo, nil)
	if sent := model.call(model.count() - 1).body; r.status != http.StatusOK || rolesOf(sent) != "user,assistant,tool,assistant,user" {
		t.Fatalf("ollama echo %d %s", r.status, rolesOf(sent))
	}
	r, _ = ask(t, g, base, http.MethodPost, chat.OpenAIResponses, `{"model":"m","input":"Find kittens"}`, nil)
	if r.status != http.StatusOK || !strings.Contains(string(r.body), `"type":"web_search_call"`) || !strings.Contains(string(r.body), "Kittens found.") {
		t.Fatalf("responses %d %s", r.status, r.body)
	}
	echo = fmt.Sprintf(`{"model":"m","input":[{"role":"user","content":"Find kittens"},%s,{"type":"message","role":"assistant","id":"msg_1","status":"completed","content":[{"type":"output_text","text":"Kittens found.","annotations":[]}]},{"role":"user","content":"And now?"}]}`,
		`{"type":"web_search_call","id":"ws_1","status":"completed","action":{"type":"search","query":"kittens","sources":[{"type":"url","url":"https://cats.example/kittens"}]}}`)
	r, _ = ask(t, g, base, http.MethodPost, chat.OpenAIResponses, echo, nil)
	if sent := model.call(model.count() - 1).body; r.status != http.StatusOK || rolesOf(sent) != "user,assistant,tool,assistant,user" {
		t.Fatalf("responses echo %d %s %s", r.status, rolesOf(sent), r.body)
	}
}

// The key reads a call's arguments the same whatever spacing and key order a protocol's echo gives them
func TestRoundKeyCanonicalArgs(t *testing.T) {
	user := chat.Message{Role: "user", Parts: []chat.Part{{Type: "text", Text: "hi"}}}
	a := roundKey("m", &user, "ok", []chat.ToolCall{{ID: "x", Name: "write", Args: `{"path":"/x","mode":1}`}})
	b := roundKey("m", &user, "ok ", []chat.ToolCall{{ID: "y", Name: "write", Args: `{ "mode": 1, "path": "/x" }`}})
	if a != b {
		t.Fatal("ids, spacing, and key order do not change the key")
	}
	if a == roundKey("m", &user, "ok", []chat.ToolCall{{Name: "write", Args: `{"path":"/y","mode":1}`}}) || a == roundKey("other", &user, "ok", nil) {
		t.Fatal("other arguments or routes make another key")
	}
}

// The oldest answers leave past the count bound
func TestRoundStoreBounds(t *testing.T) {
	s := newRoundStore()
	final := chat.Message{Role: "assistant", Parts: []chat.Part{{Type: "text", Text: "ok"}}}
	for i := 0; i <= roundsMax; i++ {
		s.put(fmt.Sprint(i), nil, final, nil, 0)
	}
	if _, ok := s.get("0"); ok {
		t.Fatal("the oldest answer left")
	}
	if _, ok := s.get(fmt.Sprint(roundsMax)); !ok || len(s.entries) != roundsMax {
		t.Fatalf("%d kept", len(s.entries))
	}
	s.put("1", nil, final, nil, 0)
	if len(s.order) != roundsMax || len(s.entries) != roundsMax {
		t.Fatalf("a key put again is counted once: %d %d", len(s.order), len(s.entries))
	}
}
