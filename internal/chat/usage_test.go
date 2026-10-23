package chat

import (
	"encoding/json"
	"strings"
	"testing"
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
