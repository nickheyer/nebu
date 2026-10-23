package gateway

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/nickheyer/nebu/internal/chat"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// A Responses API call reaches an OpenAI seat as a chat completion and comes back in the Responses shape
func TestResponsesTranslateToChatCompletions(t *testing.T) {
	seat := newSeat(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		if body["stream"] == true {
			chatStream(w, "Hello there")
			return
		}
		chatAnswer(w, "Hello there")
	})
	g, base := seatGateway(t, seat, nil)
	body := `{"model":"m","instructions":"be brief","input":[{"role":"user","content":"hi"}],"store":false}`
	resp, trace := ask(t, g, base, http.MethodPost, chat.OpenAIResponses, body, nil)
	if resp.status != http.StatusOK {
		t.Fatalf("status %d: %s", resp.status, resp.body)
	}
	var whole struct {
		Object string `json:"object"`
		Status string `json:"status"`
		Output []struct {
			Type    string                  `json:"type"`
			Content []struct{ Text string } `json:"content"`
		} `json:"output"`
	}
	if err := json.Unmarshal(resp.body, &whole); err != nil || whole.Object != "response" || whole.Status != "completed" || len(whole.Output) != 1 || whole.Output[0].Content[0].Text != "Hello there" {
		t.Fatalf("answer %s: %v", resp.body, err)
	}
	sent := seat.call(0)
	if sent.path != chat.OpenAIChat || sent.body["store"] != nil || rolesSent(sent.body)[0] != "system" || systemSent(t, sent.body) != "be brief" {
		t.Fatalf("upstream got %s %v", sent.path, sent.body)
	}
	if trace.GetKind() != v1.TraceKind_TRACE_KIND_CHAT || trace.GetClientApi() != v1.ApiFlavor_API_FLAVOR_OPENAI || trace.GetTranslated() {
		t.Fatalf("trace %v", trace)
	}
	resp, _ = ask(t, g, base, http.MethodPost, chat.OpenAIResponses, `{"model":"m","input":"hi","stream":true}`, nil)
	if resp.status != http.StatusOK || !strings.HasPrefix(resp.header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("stream %d %s", resp.status, resp.header.Get("Content-Type"))
	}
	text := string(resp.body)
	for _, want := range []string{"event: response.created", "event: response.output_text.delta", `"delta":"Hello there"`, "event: response.completed", `"total_tokens":4`} {
		if !strings.Contains(text, want) {
			t.Fatalf("stream lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "[DONE]") {
		t.Fatal("a responses stream ends without the chat completions sentinel")
	}
	resp, _ = ask(t, g, base, http.MethodPost, chat.OpenAIResponses, `{"model":"m","input":"hi","previous_response_id":"resp_1"}`, nil)
	if resp.status != http.StatusBadRequest || !strings.Contains(string(resp.body), "previous_response_id") {
		t.Fatalf("stored state refused with %d: %s", resp.status, resp.body)
	}
}
