package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/nickheyer/nebu/internal/auth"
	"github.com/nickheyer/nebu/internal/pools"
	"github.com/nickheyer/nebu/pkg/datalayer"
	"github.com/nickheyer/nebu/pkg/sources"
)

// What a ChatGPT plan's provider refuses a request for naming, https://developers.openai.com/siwc/token-sharing-open-source/preview-limitations
var planRefuses = []string{"background", "conversation", "max_output_tokens", "max_tool_calls", "metadata", "moderation", "prompt", "prompt_cache_retention",
	"safety_identifier", "temperature", "top_logprobs", "top_p", "truncation", "user", "previous_response_id", "messages", "max_tokens", "stream_options", "stop"}

// A provider serving a plan sign-in as a relay answers the gateway for it: the Responses API alone, streamed and stored
// nowhere, with a 401 for every other path and a 400 for a body naming what it refuses
type planProvider struct {
	mu     sync.Mutex
	paths  []string
	bodies []map[string]json.RawMessage
	srv    *httptest.Server
}

func newPlanProvider(t *testing.T) *planProvider {
	p := &planProvider{}
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]json.RawMessage
		json.NewDecoder(r.Body).Decode(&body)
		p.mu.Lock()
		p.paths, p.bodies = append(p.paths, r.URL.Path), append(p.bodies, body)
		p.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/v1/responses" {
			w.Header().Set(ProviderStatusHeader, "401")
			w.WriteHeader(http.StatusBadGateway)
			io.WriteString(w, `{"error":{"message":"Unauthorized","type":"rejected_by_access_enforcement","code":"no_matching_rule","param":null},"status":401}`)
			return
		}
		unsupported := func(param string) {
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, `{"error":{"message":"Unsupported capability","type":"invalid_request_error","code":"subscription_sharing_unsupported_capability","param":"`+param+`"}}`)
		}
		for _, k := range planRefuses {
			if _, named := body[k]; named {
				unsupported(k)
				return
			}
		}
		switch {
		case string(body["store"]) != "false":
			unsupported("store")
			return
		case string(body["stream"]) != "true":
			unsupported("stream")
			return
		case strings.Contains(string(body["input"]), `"role":"system"`):
			unsupported("input")
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, ev := range []string{
			`{"type":"response.created","response":{"id":"resp_1","object":"response","model":"gpt-6.1-terra","status":"in_progress","output":[]}}`,
			`{"type":"response.output_item.added","output_index":0,"item":{"id":"msg_1","type":"message","role":"assistant","status":"in_progress","content":[]}}`,
			`{"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":"sup"}`,
			`{"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":" bro"}`,
			`{"type":"response.completed","response":{"id":"resp_1","object":"response","model":"gpt-6.1-terra","status":"completed","usage":{"input_tokens":11,"output_tokens":4,"total_tokens":15}}}`,
		} {
			var kind struct {
				Type string `json:"type"`
			}
			json.Unmarshal([]byte(ev), &kind)
			io.WriteString(w, "event: "+kind.Type+"\ndata: "+ev+"\n\n")
		}
	}))
	t.Cleanup(p.srv.Close)
	return p
}

// The path and body of the last request the provider was sent, and how many it was sent
func (p *planProvider) last() (string, map[string]json.RawMessage, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.paths) == 0 {
		return "", nil, 0
	}
	return p.paths[len(p.paths)-1], p.bodies[len(p.bodies)-1], len(p.paths)
}

// A plan sign-in's provider serves the Responses API alone, so a chat in any client protocol is rendered for it, streamed
// and stored nowhere without the fields it refuses, and answered in the client's own protocol, whole when the client asked
// for no stream
func TestSignInServedOverResponsesAloneTakesEveryClientProtocol(t *testing.T) {
	creds := &fakeCreds{keys: map[string]string{auth.ProviderDaemon: "plan-token"}, responses: &sources.Responses{Namespace: "functions"}}
	h := newHarness(t, func(s *setup) { s.creds = creds })
	up := newPlanProvider(t)
	h.publish("chat", "gpt-6.1-terra", "vllm", datalayer.AppProtocolHTTP, true, pools.RouteSpec{ServedName: "gpt-6.1-terra"}, up.srv.URL)

	whole := `{"model":"chat","messages":[{"role":"system","content":"be brief"},{"role":"user","content":"sup bro"}],"max_tokens":8,"temperature":0.2,"top_p":0.9,"stop":["END"],"user":"u1"}`
	r := h.do(http.MethodPost, "/v1/chat/completions", whole)
	if r.status != http.StatusOK || !strings.Contains(string(r.body), `"content":"sup bro"`) || !strings.Contains(string(r.body), `"object":"chat.completion"`) || !strings.Contains(string(r.body), `"prompt_tokens":11`) {
		t.Fatalf("a whole chat completion: %d %s", r.status, r.body)
	}
	path, body, n := up.last()
	if path != "/v1/responses" || n != 1 || string(body["instructions"]) != `"be brief"` || string(body["model"]) != `"gpt-6.1-terra"` {
		t.Fatalf("the provider was sent %s %v, %d requests", path, body, n)
	}
	if string(body["input"]) != `[{"type":"message","role":"user","content":"sup bro"}]` {
		t.Fatalf("input %s", body["input"])
	}
	tr := traceOf(t, h, r.header.Get(TraceHeader))
	if tr.GetPromptTokens() != 11 || tr.GetCompletionTokens() != 4 || tr.GetStream() || len(tr.GetFallbacks()) != 0 || !strings.Contains(string(tr.GetUpstreamRequest()), `"store":false`) {
		t.Fatalf("trace %v", tr)
	}

	streamed := `{"model":"chat","messages":[{"role":"user","content":"sup bro"}],"stream":true,"stream_options":{"include_usage":true}}`
	r = h.do(http.MethodPost, "/v1/chat/completions", streamed)
	if r.status != http.StatusOK || !strings.Contains(string(r.body), `"object":"chat.completion.chunk"`) || !strings.Contains(string(r.body), `"content":"sup"`) || !strings.Contains(string(r.body), `"prompt_tokens":11`) || !strings.HasSuffix(string(r.body), "data: [DONE]\n\n") {
		t.Fatalf("a streamed chat completion: %d %s", r.status, r.body)
	}
	if _, _, n := up.last(); n != 2 {
		t.Fatalf("the provider was sent %d requests, want one a chat", n)
	}

	r = h.do(http.MethodPost, "/v1/messages", `{"model":"chat","max_tokens":64,"system":"be brief","messages":[{"role":"user","content":"sup bro"}]}`, "anthropic-version", "2023-06-01")
	if r.status != http.StatusOK || !strings.Contains(string(r.body), `"text":"sup bro"`) || !strings.Contains(string(r.body), `"input_tokens":11`) {
		t.Fatalf("an Anthropic client: %d %s", r.status, r.body)
	}
	if path, body, _ := up.last(); path != "/v1/responses" || string(body["instructions"]) != `"be brief"` {
		t.Fatalf("an Anthropic client's chat was sent as %s %v", path, body)
	}

	r = h.do(http.MethodPost, "/api/chat", `{"model":"chat","stream":false,"messages":[{"role":"user","content":"sup bro"}]}`)
	if r.status != http.StatusOK || !strings.Contains(string(r.body), `"content":"sup bro"`) {
		t.Fatalf("an Ollama client: %d %s", r.status, r.body)
	}
}

// A Responses request that already asks for the stream the provider answers with passes as the client wrote it, and one
// asking for a whole answer is rendered for a stream and answered whole
func TestSignInServedOverResponsesAloneTakesAResponsesClient(t *testing.T) {
	creds := &fakeCreds{keys: map[string]string{auth.ProviderDaemon: "plan-token"}, responses: &sources.Responses{}}
	h := newHarness(t, func(s *setup) { s.creds = creds })
	up := newPlanProvider(t)
	h.publish("chat", "gpt-6.1-terra", "vllm", datalayer.AppProtocolHTTP, true, pools.RouteSpec{ServedName: "gpt-6.1-terra"}, up.srv.URL)

	own := `{"model":"chat","input":[{"role":"user","content":"sup bro"}],"store":false,"stream":true,"include":["reasoning.encrypted_content"]}`
	r := h.do(http.MethodPost, "/v1/responses", own)
	if r.status != http.StatusOK || !strings.Contains(string(r.body), "response.output_text.delta") || !strings.Contains(string(r.body), "response.completed") {
		t.Fatalf("a streamed Responses request: %d %s", r.status, r.body)
	}
	if path, body, _ := up.last(); path != "/v1/responses" || string(body["include"]) != `["reasoning.encrypted_content"]` || string(body["model"]) != `"gpt-6.1-terra"` {
		t.Fatalf("the client's own body went on as %s %v", path, body)
	}

	r = h.do(http.MethodPost, "/v1/responses", `{"model":"chat","instructions":"be brief","input":"sup bro","max_output_tokens":9}`)
	if r.status != http.StatusOK || !strings.Contains(string(r.body), `"object":"response"`) || !strings.Contains(string(r.body), `"text":"sup bro"`) || strings.Contains(string(r.body), "event:") {
		t.Fatalf("a whole Responses request: %d %s", r.status, r.body)
	}
	if _, body, _ := up.last(); string(body["stream"]) != "true" || string(body["store"]) != "false" || string(body["instructions"]) != `"be brief"` {
		t.Fatalf("a request for a whole answer was sent as %v", body)
	}
}

// What the Responses API has no form for is refused before the provider is asked, in words naming what would serve it,
// and what the provider itself refuses reaches the client in the provider's words with the parameter it blames
func TestSignInServedOverResponsesAloneRefusals(t *testing.T) {
	creds := &fakeCreds{keys: map[string]string{auth.ProviderDaemon: "plan-token"}, responses: &sources.Responses{}}
	h := newHarness(t, func(s *setup) { s.creds = creds })
	up := newPlanProvider(t)
	h.publish("chat", "gpt-6.1-terra", "vllm", datalayer.AppProtocolHTTP, true, pools.RouteSpec{ServedName: "gpt-6.1-terra"}, up.srv.URL)

	r := h.do(http.MethodPost, "/v1/embeddings", `{"model":"chat","input":"a"}`)
	if r.status != http.StatusBadRequest || !strings.Contains(string(r.body), "embeddings take a key") {
		t.Fatalf("embeddings: %d %s", r.status, r.body)
	}
	if r = h.do(http.MethodPost, "/v1/completions", `{"model":"chat","prompt":"a"}`); r.status != http.StatusBadRequest || !strings.Contains(string(r.body), "completions take a key") {
		t.Fatalf("a raw prompt: %d %s", r.status, r.body)
	}
	if _, _, n := up.last(); n != 0 {
		t.Fatalf("the provider was asked %d times for what it does not serve", n)
	}
	// The client's own Responses body, streamed, names a field the provider refuses
	r = h.do(http.MethodPost, "/v1/responses", `{"model":"chat","input":"a","stream":true,"store":false,"temperature":0.5}`)
	if r.status != http.StatusBadRequest || !strings.Contains(string(r.body), "Unsupported capability") {
		t.Fatalf("a refused field: %d %s", r.status, r.body)
	}
}

// A key for the same provider is served every path as before, its chat going on as the client wrote it
func TestKeyBesideASignInServedOverResponsesAlone(t *testing.T) {
	creds := &fakeCreds{keys: map[string]string{auth.ProviderDaemon: "sk-key"}}
	h := newHarness(t, func(s *setup) { s.creds = creds })
	var paths []string
	var mu sync.Mutex
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, openaiAnswer)
	}))
	t.Cleanup(upstream.Close)
	h.publish("chat", "gpt", "vllm", datalayer.AppProtocolHTTP, true, pools.RouteSpec{ServedName: "gpt"}, upstream.URL)
	if r := h.do(http.MethodPost, "/v1/chat/completions", chatBody); r.status != http.StatusOK {
		t.Fatalf("status %d %s", r.status, r.body)
	}
	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(paths, []string{"/v1/chat/completions"}) {
		t.Fatalf("a key's chat went to %v", paths)
	}
}
