package chat

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
)

const (
	wavPart = `{"type":"input_audio","input_audio":{"data":"UklGRiQAAABXQVZFZm10IBAAAAABAAEAQB8AAIA+AAACABAAZGF0YQAAAAA=","format":"wav"}}`
	mp3Part = `{"type":"input_audio","input_audio":{"format":"mp3","url":"https://example.com/clip.mp3"}}`
	pngPart = `{"type":"image_url","image_url":{"url":"data:image/png;base64,iVBORw0KGgoAAAANSUhEUg=="}}`
)

func mixedChat() string {
	return `{"model":"m","messages":[{"role":"system","content":"be brief"},{"role":"user","content":[{"type":"text","text":"what is said"},` + pngPart + `,` + wavPart + `,` + mp3Part + `]}],"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}]}`
}

func contentOf(t *testing.T, body []byte, role string) json.RawMessage {
	t.Helper()
	var doc struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("rendered body %s: %v", body, err)
	}
	for _, m := range doc.Messages {
		if m.Role == role {
			return m.Content
		}
	}
	t.Fatalf("no %s turn in %s", role, body)
	return nil
}

func sameJSON(t *testing.T, a, b []byte) bool {
	t.Helper()
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	p, _ := json.Marshal(x)
	q, _ := json.Marshal(y)
	return string(p) == string(q)
}

func TestAudioPartsRoundTrip(t *testing.T) {
	sent := []byte(mixedChat())
	req, err := openai{}.Parse(OpenAIChat, sent)
	if err != nil {
		t.Fatal(err)
	}
	audio := req.Messages[1].Parts[2]
	if audio.Type != "audio" || audio.MediaType != "wav" || audio.Data == "" || req.Messages[1].Parts[3].URL != "https://example.com/clip.mp3" {
		t.Fatalf("parsed parts %+v", req.Messages[1].Parts)
	}
	if TextOf(req.Messages[1].Parts) != "what is said" || EstimateTokens(&Request{Messages: []Message{{Role: "user", Parts: []Part{audio}}}}) != turnTokens {
		t.Fatal("audio data read as text")
	}
	req.Model = "served"
	for name, rt := range map[string]Runtime{"openai": openai{}, "emulated tools": Emulate(openai{})} {
		_, out, err := rt.Render(req)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !strings.Contains(string(out), wavPart) || !strings.Contains(string(out), pngPart) {
			t.Fatalf("%s rendered %s", name, out)
		}
		want := contentOf(t, sent, "user")
		if got := contentOf(t, out, "user"); !sameJSON(t, got, want) {
			t.Fatalf("%s rendered user content %s, want %s", name, got, want)
		}
	}
}

func TestAudioInToolTurnsFollowsAsUserTurn(t *testing.T) {
	req := &Request{Kind: Chat, Model: "m", Messages: []Message{
		{Role: "user", Parts: []Part{{Type: "text", Text: "listen"}}},
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "c1", Name: "record", Args: "{}"}}},
		{Role: "tool", ToolID: "c1", Parts: []Part{{Type: "text", Text: "recorded"}, {Type: "audio", Data: "UklGRg==", MediaType: "wav"}}},
	}}
	_, out, err := openai{}.Render(req)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `{"role":"user","content":[{"type":"input_audio","input_audio":{"data":"UklGRg==","format":"wav"}}]}`) {
		t.Fatalf("rendered %s", out)
	}
}

func TestAudioRefusedWhereNotCarried(t *testing.T) {
	req, err := openai{}.Parse(OpenAIChat, []byte(mixedChat()))
	if err != nil {
		t.Fatal(err)
	}
	for name, rt := range map[string]Runtime{"anthropic": anthropic{}, "ollama": ollama{}, "responses": ResponsesOnly(ResponsesRules{})} {
		_, _, err := rt.Render(req)
		f, ok := errors.AsType[*Failure](err)
		if !ok || f.Status != http.StatusBadRequest || !strings.Contains(f.Message, "takes no audio") {
			t.Fatalf("%s rendered audio it cannot carry: %v", name, err)
		}
	}
}
