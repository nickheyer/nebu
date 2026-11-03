package gateway

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/nickheyer/nebu/internal/chat"
	"github.com/nickheyer/nebu/internal/gateway/jinja"
	"github.com/nickheyer/nebu/internal/pools"
	"github.com/nickheyer/nebu/pkg/chattemplate"
	"github.com/nickheyer/nebu/pkg/datalayer"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/stub"
	"github.com/nickheyer/nebu/pkg/tokenizer"
)

// Tokenizer package fixtures, encoding and decoding as the Python tokenizers package does
const (
	tokenizerFixture = "../../pkg/tokenizer/testdata/llama3_bytelevel.json"
	wordLevelFixture = "../../pkg/tokenizer/testdata/wordlevel.json"
)

// The prompt roleTemplate renders for chatBody
const renderedChat = "<system>be brief<user>hello there<assistant>"

// The fixture tokenizer as the stub's own, so the stub decodes the ids it is handed
type stubTokens struct{ t *tokenizer.Tokenizer }

func (s stubTokens) Encode(text string, special bool) ([]uint32, error) {
	return s.t.Encode(text, special)
}
func (s stubTokens) Decode(ids []uint32) (string, error) { return s.t.Decode(ids, false) }

func loadFixture(t *testing.T) *tokenizer.Tokenizer {
	t.Helper()
	tok, err := tokenizer.Load(tokenizerFixture)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func encode(t *testing.T, tok *tokenizer.Tokenizer, text string, special bool) []uint32 {
	t.Helper()
	ids, err := tok.Encode(text, special)
	if err != nil || len(ids) == 0 {
		t.Fatalf("encode %q: %v %v", text, ids, err)
	}
	return ids
}

// Publishes a one instance pool switched to tokens in tokens out, the instance carrying the fixture and the role template
func (h *harness) tokensPool(route, model, runtime string, proto datalayer.AppProtocol, addr string, files bool) datalayer.PoolID {
	h.t.Helper()
	ctx := h.t.Context()
	pool := h.publish(route, model, runtime, proto, true, pools.RouteSpec{ServedName: "served"}, addr)
	host := strings.TrimPrefix(addr, "http://")
	instance := route + "-" + host
	h.inst.mu.Lock()
	h.inst.templates[instance] = &chattemplate.Template{Text: roleTemplate}
	if files {
		h.inst.files[instance] = tokenizerFixture
	}
	h.inst.mu.Unlock()
	a, err := datalayer.ParseAddress(host)
	if err != nil {
		h.t.Fatal(err)
	}
	if _, err := h.mgr.PutPool(ctx, string(pool), &v1.PoolSpec{Model: key(model).String(), RuntimeId: runtime, TargetPorts: []uint32{uint32(a.Port)}, TokensInOut: true}); err != nil {
		h.t.Fatal(err)
	}
	waitFor(h.t, "pool in tokens in tokens out mode", func() bool { p, ok := h.store.Pool(pool); return ok && p.TokensInOut })
	return pool
}

// Whether the index holds every token chunk of ids for the served model on ep, as a scheduled prompt leaves them
func (h *harness) indexedTokens(ep datalayer.EndpointID, ids []uint32) bool {
	hashes := h.g.index.Hash("served", "", "", ints(ids))
	for _, m := range h.g.index.Match(hashes, []datalayer.EndpointID{ep}) {
		if m.Endpoint == ep && m.Blocks == len(hashes) {
			return true
		}
	}
	return false
}

// The ids a reply lists under name
func (r reply) ids(t *testing.T, name string) []uint32 {
	t.Helper()
	raw, ok := r.json(t)[name].([]any)
	if !ok {
		t.Fatalf("reply lacks %s: %s", name, r.body)
	}
	out := make([]uint32, len(raw))
	for i, v := range raw {
		out[i] = uint32(v.(float64))
	}
	return out
}

// The content every streamed chunk of an OpenAI chat answer carries, joined
func streamText(t *testing.T, body []byte) string {
	t.Helper()
	var b strings.Builder
	for _, p := range ssePayloads(body) {
		if p == "[DONE]" {
			continue
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(p), &chunk); err != nil {
			t.Fatalf("chunk %q: %v", p, err)
		}
		for _, c := range chunk.Choices {
			b.WriteString(c.Delta.Content)
		}
	}
	return b.String()
}

func withTokenIDs(body string) string {
	return strings.Replace(body, `"max_tokens":8`, `"max_tokens":8,"return_token_ids":true`, 1)
}

// A vLLM gRPC pool in tokens mode sends the rendered prompt as ids, asks for ids alone, the gateway decoding the answer
func TestTokensInOutGRPC(t *testing.T) {
	h := newHarness(t)
	f, addr := startVLLM(t)
	pool := h.tokensPool("chat", "grpc", "vllm", datalayer.AppProtocolGRPC, addr, true)
	tok := loadFixture(t)
	answer := encode(t, tok, "hi there", false)
	prompt := encode(t, tok, renderedChat, false)
	f.answer(answer)
	ep := h.store.Endpoints(pool, nil)[0].ID()
	cases := []struct {
		name, body string
		check      func(t *testing.T, r reply)
	}{
		{"whole", withTokenIDs(chatBody), func(t *testing.T, r reply) {
			if !strings.Contains(string(r.body), `"content":"hi there"`) {
				t.Fatalf("answer %s", r.body)
			}
			if got := r.ids(t, "prompt_token_ids"); !slices.Equal(got, prompt) {
				t.Fatalf("prompt ids %v want %v", got, prompt)
			}
			choice := r.json(t)["choices"].([]any)[0].(map[string]any)
			got := (reply{body: mustJSON(t, choice)}).ids(t, "token_ids")
			if !slices.Equal(got, answer) {
				t.Fatalf("answer ids %v want %v", got, answer)
			}
		}},
		{"stream", strings.Replace(chatBody, `"max_tokens":8`, `"max_tokens":8,"stream":true`, 1), func(t *testing.T, r reply) {
			if text := streamText(t, r.body); text != "hi there" {
				t.Fatalf("streamed %q", text)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := h.do(http.MethodPost, "/v1/chat/completions", tc.body)
			if r.status != http.StatusOK {
				t.Fatalf("status %d: %s", r.status, r.body)
			}
			tc.check(t, r)
			got := f.last()
			if !slices.Equal(got.GetTokenIds().GetIds(), prompt) || got.GetText() != "" {
				t.Fatalf("engine got ids %v text %q, want ids %v", got.GetTokenIds().GetIds(), got.GetText(), prompt)
			}
			if text := got.GetResponse().OutputText; text == nil || *text {
				t.Fatal("the engine was asked for text, tokens out mode decodes at the gateway")
			}
			if !got.GetResponse().GetOutputTokenIds() {
				t.Fatal("the engine was not asked for the answer ids")
			}
			if !h.indexedTokens(ep, prompt) {
				t.Fatal("the scheduler did not record the prompt as token chunks on the serving endpoint")
			}
		})
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// A started stub speaking as the named engine with the fixture tokenizer, closed with the test
func (h *harness) tokensStub(names stub.Names, tok *tokenizer.Tokenizer) (*stub.Stub, *httptest.Server) {
	h.t.Helper()
	st, err := stub.New(stub.Config{ID: string(names), Model: "served", Names: names, Tokenizer: stubTokens{tok}})
	if err != nil {
		h.t.Fatal(err)
	}
	srv := httptest.NewServer(st)
	h.t.Cleanup(func() {
		srv.Close()
		st.Close()
	})
	return st, srv
}

// Tokens mode turns vLLM chats into completions of prompt ids, gives SGLang chats input_ids, and folds tools the endpoint cannot read
func TestTokensInOutHTTP(t *testing.T) {
	h := newHarness(t)
	tok := loadFixture(t)
	st, srv := h.tokensStub(stub.NamesVLLM, tok)
	sg, sgsrv := h.tokensStub(stub.NamesSGLang, tok)
	pool := h.tokensPool("chat", "http", "vllm", datalayer.AppProtocolHTTP, srv.URL, true)
	h.tokensPool("sg", "sgm", "sglang", datalayer.AppProtocolHTTP, sgsrv.URL, true)
	ep := h.store.Endpoints(pool, nil)[0].ID()
	chatPrompt := encode(t, tok, renderedChat, false)
	textPrompt := encode(t, tok, "hello there", true)
	userOnly := encode(t, tok, "<user>hello there<assistant>", false)
	const toolsBody = `{"model":"chat","messages":[{"role":"user","content":"hello there"}],"max_tokens":8,"return_token_ids":true,"tools":[{"type":"function","function":{"name":"lookup_weather","description":"finds the weather","parameters":{"type":"object","properties":{}}}}]}`
	cases := []struct {
		name, path, body string
		header           []string
		status           int
		check            func(t *testing.T, r reply)
	}{
		{"sglang chat", "/v1/chat/completions", withTokenIDs(modelBody("sg")), nil, http.StatusOK, func(t *testing.T, r reply) {
			if got := r.ids(t, "prompt_token_ids"); !slices.Equal(got, chatPrompt) {
				t.Fatalf("prompt ids %v want %v", got, chatPrompt)
			}
		}},
		{"sglang tools stay with the engine", "/v1/chat/completions", strings.Replace(toolsBody, `"model":"chat"`, `"model":"sg"`, 1), nil, http.StatusOK, func(t *testing.T, r reply) {
			if got := r.ids(t, "prompt_token_ids"); !slices.Equal(got, userOnly) {
				t.Fatalf("prompt ids %v want the bare chat %v", got, userOnly)
			}
		}},
		{"sglang chat logprobs reach the engine", "/v1/chat/completions", strings.Replace(modelBody("sg"), `"max_tokens":8`, `"max_tokens":8,"logprobs":true`, 1), nil, http.StatusBadRequest, func(t *testing.T, r reply) {
			if !strings.Contains(string(r.body), "not supported by this server") {
				t.Fatalf("the engine did not see the logprobs request: %s", r.body)
			}
		}},
		{"chat", "/v1/chat/completions", withTokenIDs(chatBody), nil, http.StatusOK, func(t *testing.T, r reply) {
			if got := r.ids(t, "prompt_token_ids"); !slices.Equal(got, chatPrompt) {
				t.Fatalf("prompt ids %v want %v", got, chatPrompt)
			}
			if !strings.Contains(string(r.body), `"role":"assistant"`) || r.json(t)["object"] != "chat.completion" {
				t.Fatalf("answer %s", r.body)
			}
			if !h.indexedTokens(ep, chatPrompt) {
				t.Fatal("the scheduler did not record the chat as token chunks")
			}
		}},
		{"completions", "/v1/completions", `{"model":"chat","prompt":"hello there","max_tokens":4,"return_token_ids":true}`, nil, http.StatusOK, func(t *testing.T, r reply) {
			choice := r.json(t)["choices"].([]any)[0].(map[string]any)
			if got := (reply{body: mustJSON(t, choice)}).ids(t, "prompt_token_ids"); !slices.Equal(got, textPrompt) {
				t.Fatalf("prompt ids %v want %v", got, textPrompt)
			}
			if !h.indexedTokens(ep, textPrompt) {
				t.Fatal("the scheduler did not record the prompt as token chunks")
			}
		}},
		{"anthropic", "/v1/messages", `{"model":"chat","max_tokens":8,"system":"be brief","messages":[{"role":"user","content":"hello there"}]}`, []string{"anthropic-version", "2023-06-01"}, http.StatusOK, func(t *testing.T, r reply) {
			if r.json(t)["type"] != "message" || !strings.Contains(string(r.body), `"type":"text"`) {
				t.Fatalf("answer %s", r.body)
			}
		}},
		{"tools", "/v1/chat/completions", toolsBody, nil, http.StatusOK, func(t *testing.T, r reply) {
			text, err := tok.Decode(r.ids(t, "prompt_token_ids"), false)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(text, "lookup_weather") || !strings.HasPrefix(text, "<system>") || !strings.HasSuffix(text, "<assistant>") {
				t.Fatalf("the prompt sent as ids reads %q, want the tool folded into a system turn", text)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := h.do(http.MethodPost, tc.path, tc.body, tc.header...)
			if r.status != tc.status {
				t.Fatalf("status %d: %s", r.status, r.body)
			}
			tc.check(t, r)
		})
	}
	if got, sgot := st.Stats().Served, sg.Stats().Served; got != 4 || sgot != 2 {
		t.Fatalf("vllm stub served %d want 4, sglang stub served %d want 2", got, sgot)
	}
}

// A tokens mode pool refuses before dispatch when its instance holds no tokenizer, and chat logprobs over vLLM JSON
func TestTokensInOutRefusals(t *testing.T) {
	h := newHarness(t)
	st, srv := h.stub("served")
	f, addr := startVLLM(t)
	h.tokensPool("http", "http", "vllm", datalayer.AppProtocolHTTP, srv.URL, false)
	h.tokensPool("grpc", "grpc", "vllm", datalayer.AppProtocolGRPC, addr, false)
	h.tokensPool("ready", "ready", "vllm", datalayer.AppProtocolHTTP, srv.URL, true)
	cases := []struct {
		name, body string
		status     int
		message    string
	}{
		{"http without tokenizer", modelBody("http"), http.StatusInternalServerError, "holds no tokenizer.json"},
		{"grpc without tokenizer", modelBody("grpc"), http.StatusInternalServerError, "holds no tokenizer.json"},
		{"chat logprobs over json", strings.Replace(modelBody("ready"), `"max_tokens":8`, `"max_tokens":8,"logprobs":true`, 1), http.StatusBadRequest, "logprobs on a chat are refused"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := h.do(http.MethodPost, "/v1/chat/completions", tc.body)
			if r.status != tc.status || !strings.Contains(string(r.body), tc.message) {
				t.Fatalf("status %d: %s", r.status, r.body)
			}
		})
	}
	if st.Stats().Served != 0 || f.last() != nil {
		t.Fatalf("a refused request reached a backend, stub served %d, engine got %v", st.Stats().Served, f.last())
	}
	if r := h.do(http.MethodPost, "/v1/chat/completions", strings.Replace(modelBody("ready"), `"max_tokens":8`, `"max_tokens":8,"logprobs":false`, 1)); r.status != http.StatusOK {
		t.Fatalf("logprobs false refused: %d %s", r.status, r.body)
	}
}

// Instances of one model file share one loaded tokenizer, failed lookups stay uncached, keep unloads files no live instance uses
func TestTokenizersShareFiles(t *testing.T) {
	var mu sync.Mutex
	calls := map[string]int{}
	locate := func(id string) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		calls[id]++
		switch id {
		case "a", "b":
			return tokenizerFixture, nil
		case "c":
			return wordLevelFixture, nil
		}
		return "", errors.New("instance " + id + " holds no tokenizer.json")
	}
	c := newTokenizers(locate)
	a, err := c.Get("a")
	if err != nil {
		t.Fatal(err)
	}
	b, err := c.Get("b")
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatal("two instances of one file loaded it twice")
	}
	if w, err := c.Get("c"); err != nil || w == a {
		t.Fatalf("another file %v %v", w == a, err)
	}
	if _, err := c.Get("missing"); err == nil || !strings.Contains(err.Error(), "holds no tokenizer.json") {
		t.Fatalf("missing file: %v", err)
	}
	if _, err := c.Get("a"); err != nil || calls["a"] != 1 {
		t.Fatalf("the file of a was located %d times, %v", calls["a"], err)
	}
	if _, ok := c.files["missing"]; ok {
		t.Fatal("a failed lookup was cached")
	}
	c.keep(map[string]bool{"b": true})
	if _, ok := c.files["a"]; ok {
		t.Fatal("a dead instance kept its file")
	}
	if _, ok := c.loads[tokenizerFixture]; !ok {
		t.Fatal("the file a live instance uses was unloaded")
	}
	if _, ok := c.loads[wordLevelFixture]; ok {
		t.Fatal("a file no instance uses stayed loaded")
	}
	c.keep(nil)
	if len(c.loads) != 0 || len(c.files) != 0 {
		t.Fatalf("loads %d files %d after every instance left", len(c.loads), len(c.files))
	}
	shared := newTokenizers(locate)
	got := make([]*tokenizer.Tokenizer, 8)
	var wg sync.WaitGroup
	for i := range got {
		wg.Go(func() { got[i], _ = shared.Get("a") })
	}
	wg.Wait()
	for _, tok := range got {
		if tok == nil || tok != got[0] {
			t.Fatal("concurrent first loads did not share one tokenizer")
		}
	}
}

// The tokens runtime renders chats as completions of prompt ids, encodes text prompts and embedding inputs, keeps id prompts
func TestTokensRuntimeRenders(t *testing.T) {
	tok := loadFixture(t)
	tpl, err := jinja.Compile(&chattemplate.Template{Text: roleTemplate})
	if err != nil {
		t.Fatal(err)
	}
	client := chat.ClientOf(v1.ApiFlavor_API_FLAVOR_OPENAI)
	inner := chat.RuntimeOf(v1.ApiFlavor_API_FLAVOR_OPENAI)
	rt := tokensRuntime{Runtime: inner, enc: modelTokens{t: tok}, runtime: runtimeVLLM, template: func() (*jinja.Template, error) { return tpl, nil }}
	sg := tokensRuntime{Runtime: inner, enc: modelTokens{t: tok}, runtime: runtimeSGLang, template: func() (*jinja.Template, error) { return tpl, nil }}
	hello := ints(encode(t, tok, "hello there", true))
	hi := ints(encode(t, tok, "hi", true))
	chatIDs := ints(encode(t, tok, renderedChat, false))
	cases := []struct {
		name, path, body string
		sglang           bool
		wantPath         string
		want             map[string]any
		absent           []string
		refused          string
	}{
		{"chat", chat.OpenAIChat, `{"model":"m","messages":[{"role":"system","content":"be brief"},{"role":"user","content":"hello there"}],"max_tokens":8,"seed":7,"logprobs":false,"chat_template_kwargs":{"enable_thinking":false}}`, false,
			chat.OpenAICompletions, map[string]any{"prompt": chatIDs, "seed": 7, "max_tokens": 8}, []string{"messages", "logprobs", "chat_template_kwargs"}, ""},
		{"text prompt", chat.OpenAICompletions, `{"model":"m","prompt":"hello there","max_tokens":4}`, false, chat.OpenAICompletions, map[string]any{"prompt": hello}, nil, ""},
		{"id prompt", chat.OpenAICompletions, `{"model":"m","prompt":[5,6,7],"max_tokens":4}`, false, chat.OpenAICompletions, map[string]any{"prompt": []int{5, 6, 7}}, nil, ""},
		{"embeddings", chat.OpenAIEmbeddings, `{"model":"m","input":["hello there","hi"]}`, false, chat.OpenAIEmbeddings, map[string]any{"input": [][]int{hello, hi}}, nil, ""},
		{"chat logprobs", chat.OpenAIChat, `{"model":"m","messages":[{"role":"user","content":"hello there"}],"logprobs":true,"top_logprobs":2}`, false, "", nil, nil, "logprobs on a chat are refused"},
		{"sglang chat", chat.OpenAIChat, `{"model":"m","messages":[{"role":"system","content":"be brief"},{"role":"user","content":"hello there"}],"max_tokens":8,"logprobs":true,"top_logprobs":2}`, true,
			chat.OpenAIChat, map[string]any{"input_ids": chatIDs, "logprobs": true, "top_logprobs": 2, "messages": []map[string]any{{"role": "system", "content": "be brief"}, {"role": "user", "content": "hello there"}}}, []string{"prompt"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, err := client.Parse(tc.path, []byte(tc.body))
			if err != nil {
				t.Fatal(err)
			}
			render := rt
			if tc.sglang {
				render = sg
			}
			path, body, err := render.Render(req)
			if tc.refused != "" {
				if err == nil || !strings.Contains(err.Error(), tc.refused) {
					t.Fatalf("rendered %s %s, err %v", path, body, err)
				}
				return
			}
			if err != nil || path != tc.wantPath {
				t.Fatalf("path %s err %v", path, err)
			}
			var got map[string]any
			if err := json.Unmarshal(body, &got); err != nil {
				t.Fatal(err)
			}
			for k, want := range tc.want {
				if mustJSONString(t, got[k]) != mustJSONString(t, want) {
					t.Fatalf("%s: got %s want %s in %s", k, mustJSONString(t, got[k]), mustJSONString(t, want), body)
				}
			}
			for _, k := range tc.absent {
				if _, ok := got[k]; ok {
					t.Fatalf("%s travelled to the completions endpoint: %s", k, body)
				}
			}
		})
	}
	if _, _, err := rt.Native(&chat.Request{}); err == nil {
		t.Fatal("a native pass-through was allowed in tokens mode")
	}
}

func mustJSONString(t *testing.T, v any) string { return string(mustJSON(t, v)) }
