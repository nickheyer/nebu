package gateway

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Fake OpenAI runtime streaming text, a tool call, and usage.
func openaiUpstream(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req oaiRequest
		if err := json.Unmarshal(body, &req); err != nil || r.URL.Path != openaiChat {
			t.Errorf("upstream got %s %s", r.URL.Path, body)
		}
		if !req.Stream {
			w.Header().Set("Content-Type", "application/json")
			last := strings.Trim(string(req.Messages[len(req.Messages)-1].Content), `"`)
			io.WriteString(w, `{"id":"c1","model":"m1","choices":[{"index":0,"message":{"role":"assistant","content":"hi `+last+`"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, line := range []string{
			`{"id":"c1","model":"m1","choices":[{"index":0,"delta":{"role":"assistant","content":"hel"}}]}`,
			`{"id":"c1","model":"m1","choices":[{"index":0,"delta":{"content":"lo"}}]}`,
			`{"id":"c1","model":"m1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"add","arguments":"{\"a\":"}}]}}]}`,
			`{"id":"c1","model":"m1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"1}"}}]},"finish_reason":"tool_calls"}]}`,
			`{"id":"c1","model":"m1","choices":[],"usage":{"prompt_tokens":5,"completion_tokens":4}}`,
			`[DONE]`,
		} {
			io.WriteString(w, "data: "+line+"\n\n")
		}
	}))
}

func TestTranslateFlavors(t *testing.T) {
	upstream := openaiUpstream(t)
	defer upstream.Close()
	g := New(tableOf(t, map[string]string{"m1": upstream.URL}), nil, nil, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	srv := httptest.NewServer(g.Handler())
	defer srv.Close()

	resp, err := http.Post(srv.URL+anthropicMessages, "application/json", strings.NewReader(`{"model":"m1","max_tokens":10,"stream":true,"system":"be brief","messages":[{"role":"user","content":[{"type":"text","text":"x"}]}],"tools":[{"name":"add","input_schema":{"type":"object"}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	got := string(body)
	for _, want := range []string{"event: message_start", `"text":"hel","type":"text_delta"`, `"type":"tool_use","id":"call_1","name":"add"`, `"partial_json":"{\"a\":"`, `"stop_reason":"tool_use"`, `"output_tokens":4`, "event: message_stop"} {
		if !strings.Contains(got, want) {
			t.Fatalf("anthropic stream lacks %q:\n%s", want, got)
		}
	}

	resp, err = http.Post(srv.URL+ollamaChat, "application/json", strings.NewReader(`{"model":"m1","stream":false,"messages":[{"role":"user","content":"there"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	var ol olResponse
	if err := json.Unmarshal(body, &ol); err != nil || !ol.Done || ol.Message == nil || ol.Message.Content != "hi there" || ol.EvalCount != 2 {
		t.Fatalf("ollama answer %s", body)
	}

	resp, err = http.Post(srv.URL+ollamaGenerate, "application/json", strings.NewReader(`{"model":"m1","prompt":"p"}`))
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	lines := strings.Split(strings.TrimSpace(string(body)), "\n")
	if len(lines) < 3 || !strings.Contains(lines[0], `"response":"hel"`) || !strings.Contains(lines[len(lines)-1], `"done":true`) {
		t.Fatalf("ollama generate stream %s", body)
	}

	req, _ := http.NewRequest(http.MethodOptions, srv.URL+openaiChat, nil)
	req.Header.Set("Origin", "http://app.example")
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != http.StatusNoContent || resp.Header.Get("Access-Control-Allow-Origin") != "http://app.example" {
		t.Fatalf("preflight %d %v", resp.StatusCode, resp.Header)
	}
	resp, _ = http.Get(srv.URL + ollamaTags)
	body, _ = io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"name":"m1"`) {
		t.Fatalf("tags %s", body)
	}
}

// A 2x2 PNG, estimated as one image token.
const tinyPNG = "iVBORw0KGgoAAAANSUhEUgAAAAIAAAACCAYAAABytg0kAAAAC0lEQVR4nGNgQAcAABIAAeRVjecAAAAASUVORK5CYII="

func TestImageTokensAddedToTokenizerCount(t *testing.T) {
	chat := &Chat{Messages: []Message{{Role: "user", Parts: []Part{{Type: "text", Text: "what is this"}, {Type: "image", MediaType: "image/png", Data: tinyPNG}, {Type: "image", MediaType: "image/png", Data: "not base64!"}}}}}
	if got := imageTokensOf(chat); got != 1+imageTokensMax {
		t.Fatalf("image tokens = %d, want %d", got, 1+imageTokensMax)
	}
	// Add image tokens only to text-only tokenizer counts.
	if got := withImageTokens(openai{}, chat, 10); got != 10+1+imageTokensMax {
		t.Errorf("openai count = %d, want %d", got, 10+1+imageTokensMax)
	}
	if got := withImageTokens(ollama{}, chat, 10); got != 10+1+imageTokensMax {
		t.Errorf("ollama count = %d, want %d", got, 10+1+imageTokensMax)
	}
	if got := withImageTokens(anthropic{}, chat, 10); got != 10 {
		t.Errorf("anthropic count = %d, want 10", got)
	}
	// Estimates include text and images.
	if got := estimateTokens(chat); got != (len(promptText(chat))+3)/4+3+1+imageTokensMax {
		t.Errorf("estimate = %d", got)
	}
}
