package chat

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestOpenAIUsageCarriesCachedTokens(t *testing.T) {
	req := &Request{Kind: Chat, Model: "m"}
	body := `{"id":"chatcmpl-1","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1200,"completion_tokens":5,"total_tokens":1205,"prompt_tokens_details":{"cached_tokens":1024}}}`
	r, err := openai{}.ParseResult(req, []byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if r.Usage.In != 1200 || r.Usage.Out != 5 || r.Usage.Cached != 1024 {
		t.Fatalf("usage %+v", r.Usage)
	}
	out, err := openai{}.renderResult(req, r)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Usage struct {
			Prompt  int `json:"prompt_tokens"`
			Details *struct {
				Cached int `json:"cached_tokens"`
			} `json:"prompt_tokens_details"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if got.Usage.Prompt != 1200 || got.Usage.Details == nil || got.Usage.Details.Cached != 1024 {
		t.Fatalf("rendered %s", out)
	}
	// Nothing cached leaves the details object out
	out, err = openai{}.renderResult(req, &Result{Model: "m", Usage: Usage{In: 3, Out: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "prompt_tokens_details") {
		t.Fatalf("rendered %s", out)
	}
}

func TestOpenAIStreamUsageCarriesCachedTokens(t *testing.T) {
	sse := "data: {\"id\":\"chatcmpl-1\",\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"ok\"},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"chatcmpl-1\",\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: {\"id\":\"chatcmpl-1\",\"model\":\"m\",\"choices\":[],\"usage\":{\"prompt_tokens\":1200,\"completion_tokens\":5,\"prompt_tokens_details\":{\"cached_tokens\":1024}}}\n\n" +
		"data: [DONE]\n\n"
	var stopped *Result
	err := openai{}.ParseStream(strings.NewReader(sse), func(e Event) error {
		if e.Kind == Stopped {
			stopped = e.Res
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if stopped == nil || stopped.Usage.In != 1200 || stopped.Usage.Out != 5 || stopped.Usage.Cached != 1024 {
		t.Fatalf("stopped %+v", stopped)
	}
}

func TestAnthropicUsageSplitsCachedTokens(t *testing.T) {
	// On the wire input excludes what the cache served
	u := antUsageOf(&Result{Usage: Usage{In: 1000, Out: 5, Cached: 900}})
	if u.InputTokens != 100 || u.CacheReadInputTokens != 900 || u.OutputTokens != 5 {
		t.Fatalf("usage %+v", u)
	}
	req := &Request{Kind: Chat, Model: "m"}
	body := `{"id":"msg_1","type":"message","role":"assistant","model":"m","content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn","usage":{"input_tokens":100,"cache_creation_input_tokens":50,"cache_read_input_tokens":850,"output_tokens":5}}`
	r, err := anthropic{}.ParseResult(req, []byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if r.Usage.In != 1000 || r.Usage.Cached != 850 || r.Usage.Out != 5 {
		t.Fatalf("usage %+v", r.Usage)
	}
	sse := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"m\",\"content\":[],\"usage\":{\"input_tokens\":100,\"cache_read_input_tokens\":850,\"output_tokens\":1}}}\n\n" +
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n\n" +
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":5}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	var started, stopped *Result
	err = anthropic{}.ParseStream(strings.NewReader(sse), func(e Event) error {
		switch e.Kind {
		case Started:
			started = e.Res
		case Stopped:
			stopped = e.Res
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if started == nil || started.Usage.In != 950 || started.Usage.Cached != 850 {
		t.Fatalf("started %+v", started)
	}
	if stopped == nil || stopped.Usage.In != 950 || stopped.Usage.Cached != 850 || stopped.Usage.Out != 5 {
		t.Fatalf("stopped %+v", stopped)
	}
}

func TestResponsesUsageCachedTokens(t *testing.T) {
	u := respUsage(Usage{In: 10, Out: 2, Cached: 4})
	details, _ := u["input_tokens_details"].(map[string]any)
	if details["cached_tokens"] != 4 || u["input_tokens"] != 10 {
		t.Fatalf("usage %v", u)
	}
}

// Ollama's cached count and llama.cpp's cache_n reach the usage, and Ollama clients get the cached count back
func TestCachedTokensFromEveryRuntime(t *testing.T) {
	req := &Request{Kind: Chat, Model: "m"}
	r, err := ollama{}.ParseResult(req, []byte(`{"model":"m","message":{"role":"assistant","content":"hi"},"done":true,"done_reason":"stop","prompt_eval_count":50,"prompt_eval_cached_count":40,"eval_count":2}`))
	if err != nil || r.Usage.In != 50 || r.Usage.Cached != 40 || r.Usage.Out != 2 {
		t.Fatalf("ollama result %+v %v", r, err)
	}
	var stopped *Result
	lines := "{\"model\":\"m\",\"message\":{\"role\":\"assistant\",\"content\":\"hi\"},\"done\":false}\n{\"model\":\"m\",\"message\":{\"role\":\"assistant\",\"content\":\"\"},\"done\":true,\"prompt_eval_count\":50,\"prompt_eval_cached_count\":40,\"eval_count\":2}\n"
	if err := (ollama{}).ParseStream(strings.NewReader(lines), func(e Event) error {
		if e.Kind == Stopped {
			stopped = e.Res
		}
		return nil
	}); err != nil || stopped.Usage.Cached != 40 || stopped.Usage.In != 50 {
		t.Fatalf("ollama stream %+v %v", stopped, err)
	}
	out, err := ollama{}.renderResult(req, &Result{Model: "m", Usage: Usage{In: 50, Out: 2, Cached: 40}})
	if err != nil || !strings.Contains(string(out), `"prompt_eval_cached_count":40`) {
		t.Fatalf("ollama render %s %v", out, err)
	}
	r, err = openai{}.ParseResult(req, []byte(`{"id":"x","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":100,"completion_tokens":1},"timings":{"cache_n":64,"prompt_n":36,"draft_n":5,"draft_n_accepted":3}}`))
	if err != nil || r.Usage.Cached != 64 || r.Usage.DraftOffered != 5 || r.Usage.DraftAccepted != 3 {
		t.Fatalf("llama.cpp timings %+v %v", r.Usage, err)
	}
	stopped = nil
	sse := "data: {\"id\":\"x\",\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: {\"id\":\"x\",\"model\":\"m\",\"choices\":[],\"usage\":{\"prompt_tokens\":100,\"completion_tokens\":1},\"timings\":{\"cache_n\":64}}\n\n"
	if err := (openai{}).ParseStream(strings.NewReader(sse), func(e Event) error {
		if e.Kind == Stopped {
			stopped = e.Res
		}
		return nil
	}); err != nil || stopped.Usage.Cached != 64 || stopped.Usage.In != 100 {
		t.Fatalf("llama.cpp stream %+v %v", stopped, err)
	}
	if _, body, _ := (openai{}).Render(&Request{Kind: Chat, Model: "m", Stream: true}); string(rawField(body, "stream_options")) != `{"include_usage":true}` {
		t.Fatalf("a rendered stream asks for usage: %s", body)
	}
}

// Estimates count four bytes a token per text part, four a turn, token ids as they are, images by area
func TestEstimateTokens(t *testing.T) {
	req := &Request{
		Messages: []Message{
			{Role: "system", Parts: []Part{{Type: "text", Text: "12345"}}},
			{Role: "user", Parts: []Part{{Type: "text", Text: "1234"}, {Type: "text", Text: "1"}, {Type: "image", URL: "https://e/x.png"}}},
			{Role: "assistant", Parts: []Part{{Type: "thinking", Text: "12345678"}}, ToolCalls: []ToolCall{{Name: "ab", Args: `{"a":1}`}}},
			{Role: "user", Parts: []Part{{Type: "tokens", Tokens: []int{1, 2, 3}}}},
		},
		Tools: []Tool{{Name: "abcd", Description: "12345", Schema: json.RawMessage(`{}`)}},
	}
	want := 4*turnTokens + 2 + (1 + 1) + imageTokensMax + 2 + (1 + 2) + 3 + (1 + 2 + 1)
	if n := EstimateTokens(req); n != want {
		t.Fatalf("estimate %d, want %d", n, want)
	}
	for _, tc := range []struct {
		rt   Runtime
		path string
	}{{openai{}, "/tokenize"}, {anthropic{}, AnthropicCount}, {ollama{}, ""}} {
		if got := tc.rt.Traits().TokenizePath; got != tc.path {
			t.Fatalf("%T tokenizes at %q", tc.rt, got)
		}
		path, _, err := tc.rt.Render(&Request{Kind: Count, Model: "m", Messages: []Message{{Role: "user", Parts: []Part{{Type: "text", Text: "x"}}}}})
		if tc.path == "" && err == nil || tc.path != "" && (err != nil || path != tc.path) {
			t.Fatalf("%T count renders to %q: %v", tc.rt, path, err)
		}
	}
	for body, want := range map[string]int{`{"count":7,"max_model_len":8192,"tokens":[1,2,3,4,5,6,7]}`: 7, `{"tokens":[1,2,3]}`: 3} {
		r, err := openai{}.ParseResult(&Request{Kind: Count}, []byte(body))
		if err != nil || r.Usage.In != want {
			t.Fatalf("%s counted %+v %v", body, r, err)
		}
	}
}

// A runtime's wait reaches the failure from retry-after-ms or Retry-After, and every protocol writes it back as seconds
func TestRetryAfter(t *testing.T) {
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		header http.Header
		want   time.Duration
	}{
		{http.Header{"Retry-After": {"7"}}, 7 * time.Second},
		{http.Header{"Retry-After": {"1.5"}}, 1500 * time.Millisecond},
		{http.Header{"Retry-After-Ms": {"250"}, "Retry-After": {"9"}}, 250 * time.Millisecond},
		{http.Header{"Retry-After": {now.Add(30 * time.Second).Format(http.TimeFormat)}}, 30 * time.Second},
		{http.Header{"Retry-After": {now.Add(-time.Minute).Format(http.TimeFormat)}}, 0},
		{http.Header{"Retry-After": {"soon"}}, 0},
		{http.Header{}, 0},
	} {
		if got := RetryAfterOf(tc.header, now); got != tc.want {
			t.Fatalf("%v: %v, want %v", tc.header, got, tc.want)
		}
	}
	f := Refusal(openai{}, http.StatusTooManyRequests, http.Header{"Retry-After": {"3"}}, []byte(`{"error":{"message":"slow down","type":"rate_limit_error"}}`))
	if f.RetryAfter != 3*time.Second || f.Message != "slow down" || f.Code != CodeRateLimit {
		t.Fatalf("refusal %+v", f)
	}
	f.RetryAfter = 2100 * time.Millisecond
	for _, c := range []Client{openai{}, anthropic{}, ollama{}, sdcpp{}} {
		w := httptest.NewRecorder()
		c.Error(w, *f)
		if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") != "3" || !strings.Contains(w.Body.String(), "slow down") {
			t.Fatalf("%T wrote %d %q %s", c, w.Code, w.Header().Get("Retry-After"), w.Body.String())
		}
	}
	w := httptest.NewRecorder()
	(openai{}).Error(w, Failure{Status: http.StatusBadRequest, Message: "no"})
	if w.Header().Get("Retry-After") != "" {
		t.Fatal("a failure with no wait set Retry-After")
	}
}
