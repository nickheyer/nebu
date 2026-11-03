package chat

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// Each protocol ends a runtime's stream with the same error event its own stream writer ends with on a failure
func TestStreamFailure(t *testing.T) {
	f := Failure{Status: http.StatusForbidden, Code: CodeInvalidRequest, Message: "blocked by guardrail"}
	for _, tc := range []struct {
		name  string
		api   v1.ApiFlavor
		at    StreamAt
		event string
		want  map[string]any
	}{
		{"openai chat", v1.ApiFlavor_API_FLAVOR_OPENAI, StreamAt{Path: OpenAIChat}, "", map[string]any{"error": map[string]any{"message": f.Message, "type": f.Code, "code": f.Code}}},
		{"openai responses", v1.ApiFlavor_API_FLAVOR_OPENAI, StreamAt{Path: OpenAIResponses, Seq: 7}, "error", map[string]any{"type": "error", "code": f.Code, "message": f.Message, "param": nil, "sequence_number": float64(7)}},
		{"anthropic", v1.ApiFlavor_API_FLAVOR_ANTHROPIC, StreamAt{Path: AnthropicMessages}, "error", map[string]any{"type": "error", "error": map[string]any{"type": f.Code, "message": f.Message}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := ClientOf(tc.api).StreamFailure(tc.at, f)
			if err != nil {
				t.Fatal(err)
			}
			var events []string
			var got map[string]any
			if err := ReadSSE(bytes.NewReader(raw), func(event string, data []byte) error {
				events = append(events, event)
				return json.Unmarshal(data, &got)
			}); err != nil || len(events) != 1 || events[0] != tc.event || !bytes.HasSuffix(raw, []byte("\n\n")) || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("%q decoded %v %v %v", raw, events, got, err)
			}
			if tc.at.Path == OpenAIResponses {
				return
			}
			rec := httptest.NewRecorder()
			out := ClientOf(tc.api).Answer(rec, &Request{Kind: Chat, Stream: true}, "m")
			before := rec.Body.Len()
			if err := out.Write(Event{Kind: Failed, Failure: &f}); err != nil {
				t.Fatal(err)
			}
			out.Close()
			if written := rec.Body.Bytes()[before:]; !bytes.HasPrefix(written, raw) {
				t.Fatalf("the stream writer ended with %q, the runtime stream with %q", written, raw)
			}
		})
	}
	raw, err := ClientOf(v1.ApiFlavor_API_FLAVOR_OLLAMA).StreamFailure(StreamAt{Path: OllamaChat, Model: "m"}, f)
	var line map[string]any
	if err != nil || bytes.Count(raw, []byte("\n")) != 1 || !bytes.HasSuffix(raw, []byte("\n")) || json.Unmarshal(raw, &line) != nil || line["error"] != f.Message || line["model"] != "m" || line["done"] != true {
		t.Fatalf("ollama line %q %v", raw, err)
	}
}
