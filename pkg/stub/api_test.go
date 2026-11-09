package stub

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestChatNonStreaming(t *testing.T) {
	_, srv := start(t, Config{ID: "stub-a", Model: "m", DefaultMaxTokens: 4})
	r := mustCall(t, "POST", srv.URL+"/v1/chat/completions", chatBody("m", "hello there", map[string]any{"max_tokens": 3}), "X-Request-Id", "req-1")
	if r.status != http.StatusOK {
		t.Fatalf("status %d body %s", r.status, r.body)
	}
	if got := r.header.Get("X-Stub-Id"); got != "stub-a" {
		t.Fatalf("X-Stub-Id %q", got)
	}
	if got := r.header.Get("X-Served-By"); got != srv.Listener.Addr().String() {
		t.Fatalf("X-Served-By %q, want %q", got, srv.Listener.Addr().String())
	}
	if got := r.header.Get("X-Request-Id"); got != "req-1" {
		t.Fatalf("X-Request-Id %q", got)
	}
	c := decodeBody[wireCompletion](t, r)
	if c.ID != "chatcmpl-req-1" || c.Object != "chat.completion" || c.Model != "m" || c.Created == 0 || len(c.Choices) != 1 {
		t.Fatalf("envelope %+v", c)
	}
	ch := c.Choices[0]
	if ch.Message.Role != "assistant" || ch.Message.Content != "alpha bravo charlie" || ch.FinishReason != "length" {
		t.Fatalf("choice %+v", ch)
	}
	if u := c.Usage; u.PromptTokens != 12 || u.CompletionTokens != 3 || u.TotalTokens != 15 || u.PromptTokensDetails.CachedTokens != 0 {
		t.Fatalf("usage %+v", u)
	}
	if !strings.Contains(string(r.body), `"logprobs":null`) {
		t.Fatalf("choice lacks a null logprobs %s", r.body)
	}
	r = mustCall(t, "POST", srv.URL+"/v1/chat/completions", chatBody("", "hello there", nil))
	c = decodeBody[wireCompletion](t, r)
	if c.Model != "m" || c.Choices[0].Message.Content != "alpha bravo charlie delta" || c.Choices[0].FinishReason != "stop" {
		t.Fatalf("default length reply %+v", c)
	}
	if !strings.HasPrefix(c.ID, "chatcmpl-stub-a-") || r.header.Get("X-Request-Id") != "" {
		t.Fatalf("generated id %q echoed %q", c.ID, r.header.Get("X-Request-Id"))
	}
	r = mustCall(t, "POST", srv.URL+"/v1/chat/completions", chatBody("m", "hello there", map[string]any{"n": 3, "max_tokens": 2}))
	c = decodeBody[wireCompletion](t, r)
	if len(c.Choices) != 3 || c.Usage.PromptTokens != 12 || c.Usage.CompletionTokens != 6 {
		t.Fatalf("n=3 reply %+v", c)
	}
	for i, ch := range c.Choices {
		if ch.Index != i || ch.Message.Content != "alpha bravo" {
			t.Fatalf("choice %d %+v", i, ch)
		}
	}
}

func TestChatStreaming(t *testing.T) {
	_, srv := start(t, Config{Model: "m"})
	r := mustCall(t, "POST", srv.URL+"/v1/chat/completions", chatBody("m", "hello there", map[string]any{
		"max_tokens": 3, "stream": true, "stream_options": map[string]any{"include_usage": true},
	}))
	if r.status != http.StatusOK || !strings.HasPrefix(r.header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("status %d content type %q", r.status, r.header.Get("Content-Type"))
	}
	data := sseData(t, r.body)
	if len(data) != 7 || data[6] != "[DONE]" {
		t.Fatalf("events %q", data)
	}
	var chunks []map[string]any
	for _, d := range data[:6] {
		var m map[string]any
		if err := json.Unmarshal([]byte(d), &m); err != nil {
			t.Fatalf("chunk %q: %v", d, err)
		}
		if m["object"] != "chat.completion.chunk" {
			t.Fatalf("object %v", m["object"])
		}
		chunks = append(chunks, m)
	}
	delta := func(i int) map[string]any {
		return chunks[i]["choices"].([]any)[0].(map[string]any)["delta"].(map[string]any)
	}
	if d := delta(0); d["role"] != "assistant" || d["content"] != "" {
		t.Fatalf("opening delta %v", d)
	}
	var text strings.Builder
	for i := 1; i <= 3; i++ {
		text.WriteString(delta(i)["content"].(string))
	}
	if text.String() != "alpha bravo charlie" {
		t.Fatalf("streamed %q", text.String())
	}
	fin := chunks[4]["choices"].([]any)[0].(map[string]any)
	if fin["finish_reason"] != "length" || len(fin["delta"].(map[string]any)) != 0 {
		t.Fatalf("finish chunk %v", fin)
	}
	for i := range 5 {
		if u, ok := chunks[i]["usage"]; !ok || u != nil {
			t.Fatalf("chunk %d usage %v present %v", i, u, ok)
		}
	}
	last := chunks[5]
	u := last["usage"].(map[string]any)
	if len(last["choices"].([]any)) != 0 || u["completion_tokens"] != 3.0 || u["prompt_tokens"] != 12.0 {
		t.Fatalf("usage chunk %v", last)
	}
	r = mustCall(t, "POST", srv.URL+"/v1/chat/completions", chatBody("m", "hello there", map[string]any{"max_tokens": 2, "stream": true}))
	data = sseData(t, r.body)
	if len(data) != 5 || strings.Contains(strings.Join(data, ""), "usage") {
		t.Fatalf("stream without usage %q", data)
	}
}

func TestCompletions(t *testing.T) {
	_, srv := start(t, Config{Model: "m"})
	url := srv.URL + "/v1/completions"
	r := mustCall(t, "POST", url, map[string]any{"model": "m", "prompt": "one two three", "max_tokens": 2}, "X-Request-Id", "c1")
	c := decodeBody[wireCompletion](t, r)
	if c.ID != "cmpl-c1" || c.Object != "text_completion" || c.Choices[0].Text != " alpha bravo" || c.Choices[0].FinishReason != "length" || c.Usage.PromptTokens != 3 {
		t.Fatalf("completion %+v", c)
	}
	r = mustCall(t, "POST", url, map[string]any{"prompt": "one two three", "max_tokens": 2, "echo": true})
	if c = decodeBody[wireCompletion](t, r); c.Choices[0].Text != "one two three alpha bravo" {
		t.Fatalf("echo %q", c.Choices[0].Text)
	}
	r = mustCall(t, "POST", url, map[string]any{"prompt": []string{"a b", "c d e"}, "n": 2, "max_tokens": 1})
	c = decodeBody[wireCompletion](t, r)
	if len(c.Choices) != 4 || c.Usage.PromptTokens != 5 || c.Usage.CompletionTokens != 4 {
		t.Fatalf("batch %+v", c)
	}
	for i, ch := range c.Choices {
		if ch.Index != i || ch.Text != " alpha" {
			t.Fatalf("batch choice %d %+v", i, ch)
		}
	}
	r = mustCall(t, "POST", url, map[string]any{"prompt": []int{5, 6, 7, 8}, "max_tokens": 1})
	if c = decodeBody[wireCompletion](t, r); c.Usage.PromptTokens != 4 {
		t.Fatalf("token id prompt usage %+v", c.Usage)
	}
	r = mustCall(t, "POST", url, map[string]any{"prompt": [][]int{{1, 2}, {3}}, "max_tokens": 1})
	if c = decodeBody[wireCompletion](t, r); c.Usage.PromptTokens != 3 || len(c.Choices) != 2 {
		t.Fatalf("token id batch %+v", c)
	}
	r = mustCall(t, "POST", url, map[string]any{"prompt": "one two three", "max_tokens": 2, "echo": true, "stream": true})
	data := sseData(t, r.body)
	want := []string{"one two three", " alpha", " bravo", ""}
	if len(data) != 5 || data[4] != "[DONE]" {
		t.Fatalf("events %q", data)
	}
	for i, w := range want {
		var ch struct {
			Object  string `json:"object"`
			Choices []struct {
				Text         string  `json:"text"`
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(data[i]), &ch); err != nil {
			t.Fatal(err)
		}
		if ch.Object != "text_completion" || ch.Choices[0].Text != w || (ch.Choices[0].FinishReason != nil) != (i == 3) {
			t.Fatalf("chunk %d %s", i, data[i])
		}
	}
}

func TestStopStrings(t *testing.T) {
	_, srv := start(t, Config{Model: "m"})
	url := srv.URL + "/v1/chat/completions"
	r := mustCall(t, "POST", url, chatBody("m", "hi", map[string]any{"max_tokens": 10, "stop": " charlie"}))
	c := decodeBody[wireCompletion](t, r)
	if c.Choices[0].Message.Content != "alpha bravo" || c.Choices[0].FinishReason != "stop" || c.Usage.CompletionTokens != 3 {
		t.Fatalf("stop reply %+v", c)
	}
	r = mustCall(t, "POST", url, chatBody("m", "hi", map[string]any{"max_tokens": 10, "stop": []string{"zzz", "lie de"}, "stream": true, "stream_options": map[string]any{"include_usage": true}}))
	data := sseData(t, r.body)
	var text strings.Builder
	var reason string
	var completion float64
	for _, d := range data[:len(data)-1] {
		var m struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
			Usage map[string]any `json:"usage"`
		}
		if err := json.Unmarshal([]byte(d), &m); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(d, "lie") {
			t.Fatalf("chunk leaked the stop string %s", d)
		}
		for _, ch := range m.Choices {
			text.WriteString(ch.Delta.Content)
			if ch.FinishReason != nil {
				reason = *ch.FinishReason
			}
		}
		if m.Usage != nil {
			completion = m.Usage["completion_tokens"].(float64)
		}
	}
	if text.String() != "alpha bravo char" || reason != "stop" || completion != 4 {
		t.Fatalf("streamed %q reason %q completion %v", text.String(), reason, completion)
	}
}

func TestBadRequests(t *testing.T) {
	_, srv := start(t, Config{Model: "m", BlockSize: 4, NumGPUBlocks: 4, DefaultMaxTokens: 4})
	chat, comp := srv.URL+"/v1/chat/completions", srv.URL+"/v1/completions"
	cases := []struct {
		name string
		url  string
		body any
	}{
		{"malformed json", chat, rawBody(`{`)},
		{"no messages", chat, map[string]any{"model": "m", "messages": []any{}}},
		{"no role", chat, map[string]any{"messages": []any{map[string]any{"content": "x"}}}},
		{"logprobs", chat, chatBody("m", "hi", map[string]any{"logprobs": true})},
		{"tool choice required", chat, chatBody("m", "hi", map[string]any{"tool_choice": "required"})},
		{"tool choice named", chat, chatBody("m", "hi", map[string]any{"tool_choice": map[string]any{"type": "function", "function": map[string]any{"name": "f"}}})},
		{"json response format", chat, chatBody("m", "hi", map[string]any{"response_format": map[string]any{"type": "json_object"}})},
		{"n zero", chat, chatBody("m", "hi", map[string]any{"n": 0})},
		{"max tokens zero", chat, chatBody("m", "hi", map[string]any{"max_tokens": 0})},
		{"empty stop", chat, chatBody("m", "hi", map[string]any{"stop": []string{""}})},
		{"stop wrong type", chat, chatBody("m", "hi", map[string]any{"stop": 3})},
		{"image part", chat, map[string]any{"messages": []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "image_url"}}}}}},
		{"stream options without stream", chat, chatBody("m", "hi", map[string]any{"stream_options": map[string]any{"include_usage": true}})},
		{"context too long", chat, chatBody("m", "hi", map[string]any{"max_tokens": 6})},
		{"no prompt", comp, map[string]any{"model": "m"}},
		{"empty prompt list", comp, map[string]any{"prompt": []string{}}},
		{"empty prompt", comp, map[string]any{"prompt": ""}},
		{"object prompt", comp, map[string]any{"prompt": map[string]any{"x": 1}}},
		{"suffix", comp, map[string]any{"prompt": "a", "suffix": "b"}},
		{"logprobs count", comp, map[string]any{"prompt": "a", "logprobs": 1}},
		{"echo token ids", comp, map[string]any{"prompt": []int{1, 2}, "echo": true}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := wantError(t, mustCall(t, "POST", c.url, c.body), http.StatusBadRequest, "")
			if e.Error.Type != "invalid_request_error" {
				t.Fatalf("type %q", e.Error.Type)
			}
		})
	}
	_, big := start(t, Config{Model: "m"})
	chat = big.URL + "/v1/chat/completions"
	plain := decodeBody[wireCompletion](t, mustCall(t, "POST", chat, chatBody("m", "hi", map[string]any{"max_tokens": 1})))
	tools := []any{map[string]any{"type": "function", "function": map[string]any{"name": "lookup"}}}
	r := mustCall(t, "POST", chat, chatBody("m", "hi", map[string]any{"max_tokens": 1, "tools": tools, "tool_choice": "auto"}))
	if r.status != http.StatusOK {
		t.Fatalf("tools with auto choice %d %s", r.status, r.body)
	}
	withTools := decodeBody[wireCompletion](t, r)
	if withTools.Usage.PromptTokens <= plain.Usage.PromptTokens {
		t.Fatalf("tools added no prompt tokens %d %d", withTools.Usage.PromptTokens, plain.Usage.PromptTokens)
	}
}

func TestModelsAndUnknownModel(t *testing.T) {
	_, srv := start(t, Config{Model: "base", BlockSize: 8, NumGPUBlocks: 10, Adapters: []Adapter{{"a", "/adapters/a"}}})
	r := mustCall(t, "GET", srv.URL+"/v1/models", nil)
	var list struct {
		Object string `json:"object"`
		Data   []struct {
			ID          string  `json:"id"`
			Object      string  `json:"object"`
			Root        string  `json:"root"`
			Parent      *string `json:"parent"`
			MaxModelLen int     `json:"max_model_len"`
		} `json:"data"`
	}
	if err := json.Unmarshal(r.body, &list); err != nil {
		t.Fatal(err)
	}
	if list.Object != "list" || len(list.Data) != 2 {
		t.Fatalf("models %s", r.body)
	}
	if b := list.Data[0]; b.ID != "base" || b.Parent != nil || b.MaxModelLen != 80 || b.Object != "model" {
		t.Fatalf("base card %+v", b)
	}
	if a := list.Data[1]; a.ID != "a" || a.Parent == nil || *a.Parent != "base" || a.Root != "/adapters/a" {
		t.Fatalf("adapter card %+v", a)
	}
	if !strings.Contains(string(r.body), `"parent":null`) {
		t.Fatalf("base parent is not null %s", r.body)
	}
	e := wantError(t, mustCall(t, "POST", srv.URL+"/v1/chat/completions", chatBody("nope", "hi", nil)), http.StatusNotFound, "model_not_found")
	if e.Error.Param == nil || *e.Error.Param != "model" {
		t.Fatalf("param %v", e.Error.Param)
	}
	wantError(t, mustCall(t, "POST", srv.URL+"/v1/completions", map[string]any{"model": "nope", "prompt": "x"}), http.StatusNotFound, "model_not_found")
	c := decodeBody[wireCompletion](t, mustCall(t, "POST", srv.URL+"/v1/chat/completions", chatBody("a", "hi", map[string]any{"max_tokens": 1})))
	if c.Model != "a" {
		t.Fatalf("adapter reply model %q", c.Model)
	}
	if r := mustCall(t, "GET", srv.URL+"/nowhere", nil); r.status != http.StatusNotFound || r.header.Get("X-Stub-Id") != "stub" {
		t.Fatalf("unknown path %d headers %v", r.status, r.header)
	}
}

func TestHealthLoadingAndPause(t *testing.T) {
	s, srv := start(t, Config{LoadTime: 150 * time.Millisecond})
	r := mustCall(t, "GET", srv.URL+"/health", nil)
	if r.status != http.StatusServiceUnavailable || string(r.body) != "loading" {
		t.Fatalf("health while loading %d %q", r.status, r.body)
	}
	wantError(t, mustCall(t, "POST", srv.URL+"/v1/chat/completions", chatBody("", "hi", nil)), http.StatusServiceUnavailable, "model_loading")
	wantError(t, mustCall(t, "GET", srv.URL+"/v1/models", nil), http.StatusServiceUnavailable, "model_loading")
	if r := mustCall(t, "GET", srv.URL+"/metrics", nil); r.status != http.StatusOK {
		t.Fatalf("metrics while loading %d", r.status)
	}
	eventually(t, 2*time.Second, "health after loading", func() bool {
		return mustCall(t, "GET", srv.URL+"/health", nil).status == http.StatusOK
	})
	s.Pause()
	if r := mustCall(t, "GET", srv.URL+"/health", nil); r.status != http.StatusServiceUnavailable || string(r.body) != "paused" {
		t.Fatalf("health while paused %d %q", r.status, r.body)
	}
	s.Resume()
	if r := mustCall(t, "GET", srv.URL+"/health", nil); r.status != http.StatusOK {
		t.Fatalf("health after resume %d", r.status)
	}
}
