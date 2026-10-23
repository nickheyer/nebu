package chat

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

func TestResponsesInputBecomesTurns(t *testing.T) {
	body := `{"model":"m","instructions":"be brief","max_output_tokens":9,"temperature":0.5,"stream":true,"store":false,"include":["reasoning.encrypted_content"],
		"reasoning":{"effort":"high"},"text":{"format":{"type":"json_schema","name":"answer","schema":{"type":"object"},"strict":true},"verbosity":"low"},"metadata":{"k":"v"},
		"input":[
		{"role":"developer","content":"rules"},
		{"type":"message","role":"user","content":[{"type":"input_text","text":"look"},{"type":"input_image","image_url":"data:image/png;base64,AAAA","detail":"auto"}]},
		{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"hm"}],"encrypted_content":"x"},
		{"type":"message","role":"assistant","id":"msg_1","status":"completed","content":[{"type":"output_text","text":"Reading.","annotations":[]}]},
		{"type":"function_call","id":"fc_1","call_id":"call_1","name":"read","arguments":"{\"path\":\"a\"}","status":"completed"},
		{"type":"function_call_output","call_id":"call_1","output":"buy milk"},
		{"role":"user","content":"thanks"}],
		"tools":[{"type":"function","name":"read","description":"Reads","parameters":{"type":"object"},"strict":true},
			{"type":"web_search","filters":{"allowed_domains":["example.com"]},"user_location":{"type":"approximate","country":"NZ"}},
			{"type":"mcp","server_label":"notes","server_url":"https://mcp.example.com","authorization":"tok","allowed_tools":["list"]}],
		"tool_choice":{"type":"function","name":"read"}}`
	req, err := (openai{}).Parse(OpenAIResponses, []byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if !req.Responses || req.Kind != Chat || req.Model != "m" || req.MaxTokens != 9 || *req.Temperature != 0.5 || !req.Stream || req.ToolChoice != "read" {
		t.Fatalf("request %+v", req)
	}
	roles := []string{}
	for _, m := range req.Messages {
		roles = append(roles, m.Role)
	}
	if strings.Join(roles, ",") != "system,system,user,assistant,tool,user" {
		t.Fatalf("roles %v", roles)
	}
	if req.Messages[0].Parts[0].Text != "be brief" || req.Messages[1].Parts[0].Text != "rules" {
		t.Fatalf("system turns %+v", req.Messages[:2])
	}
	if user := req.Messages[2]; len(user.Parts) != 2 || user.Parts[1].Type != "image" || user.Parts[1].MediaType != "image/png" || user.Parts[1].Data != "AAAA" {
		t.Fatalf("user parts %+v", user.Parts)
	}
	if a := req.Messages[3]; len(a.Parts) != 2 || a.Parts[0].Type != "thinking" || a.Parts[0].Text != "hm" || a.Parts[1].Text != "Reading." || len(a.ToolCalls) != 1 || a.ToolCalls[0].ID != "call_1" || a.ToolCalls[0].Args != `{"path":"a"}` {
		t.Fatalf("assistant turn %+v", a)
	}
	if tool := req.Messages[4]; tool.ToolID != "call_1" || TextOf(tool.Parts) != "buy milk" {
		t.Fatalf("tool turn %+v", tool)
	}
	if len(req.Tools) != 2 || req.Tools[0].Name != "read" || *req.Tools[0].Strict != true || string(req.Tools[0].Schema) != `{"type":"object"}` {
		t.Fatalf("tools %+v", req.Tools)
	}
	if search := req.Tools[1]; search.Provided == nil || search.Provided.Kind != v1.ToolServerKind_TOOL_SERVER_KIND_SEARCH || search.Provided.Allowed[0] != "example.com" || search.Provided.Country != "NZ" {
		t.Fatalf("search tool %+v", search)
	}
	if len(req.Sources) != 1 || req.Sources[0].Name != "notes" || req.Sources[0].Token != "tok" || req.Sources[0].Allowed[0] != "list" {
		t.Fatalf("sources %+v", req.Sources)
	}
	if string(req.Extra["reasoning_effort"]) != `"high"` || string(req.Extra["verbosity"]) != `"low"` || string(req.Extra["metadata"]) != `{"k":"v"}` {
		t.Fatalf("extra %v", req.Extra)
	}
	for _, gone := range []string{"include", "store", "text", "reasoning"} {
		if _, ok := req.Extra[gone]; ok {
			t.Fatalf("%s should not reach a runtime", gone)
		}
	}
	var format struct {
		Type   string `json:"type"`
		Schema struct {
			Name   string `json:"name"`
			Strict bool   `json:"strict"`
		} `json:"json_schema"`
	}
	if json.Unmarshal(req.Extra["response_format"], &format) != nil || format.Type != "json_schema" || format.Schema.Name != "answer" || !format.Schema.Strict {
		t.Fatalf("response_format %s", req.Extra["response_format"])
	}
	// What the runtime gets is a plain chat completion, the gateway having taken the provided tools for itself
	req.Tools = req.Tools[:1]
	path, out, err := (openai{}).Render(req)
	if err != nil || path != OpenAIChat || !strings.Contains(string(out), `"tool_call_id":"call_1"`) || !strings.Contains(string(out), `"reasoning_effort":"high"`) {
		t.Fatalf("render %s %s: %v", path, out, err)
	}
	if req, err := (openai{}).Parse(OpenAIResponses, []byte(`{"model":"m","input":"hi"}`)); err != nil || len(req.Messages) != 1 || req.Messages[0].Role != "user" || TextOf(req.Messages[0].Parts) != "hi" || req.Stream {
		t.Fatalf("string input %+v %v", req, err)
	}
}

func TestResponsesRefusals(t *testing.T) {
	cases := map[string]string{
		`{"model":"m","input":"hi","previous_response_id":"resp_1"}`: "previous_response_id",
		`{"model":"m","input":"hi","conversation":{"id":"conv_1"}}`:  "conversation",
		`{"model":"m","input":"hi","background":true}`:               "background",
		`{"model":"m"}`: "input is required",
		`{"model":"m","input":[{"type":"item_reference","id":"msg_1"}]}`:            "stored item",
		`{"model":"m","input":[{"role":"robot","content":"x"}]}`:                    "role",
		`{"model":"m","input":[{"role":"user","content":[{"type":"input_file"}]}]}`: "input_file parts",
		`{"model":"m","input":"hi","tools":[{"type":"file_search"}]}`:               "tool type",
		`{"model":"m","input":"hi","tool_choice":"maybe"}`:                          "tool_choice",
		`{"model":"m","input":"hi","text":{"format":{"type":"json_schema"}}}`:       "needs a schema",
	}
	for body, want := range cases {
		_, err := (openai{}).Parse(OpenAIResponses, []byte(body))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: want %q, got %v", body, want, err)
		}
	}
}

func TestResponsesWholeAnswer(t *testing.T) {
	req, _ := (openai{}).Parse(OpenAIResponses, []byte(`{"model":"m","input":"hi"}`))
	w := httptest.NewRecorder()
	out := (openai{}).Answer(w, req, "m")
	out.Write(Event{Kind: Thought, Text: "hm"})
	out.Write(Event{Kind: Said, Text: "Hel"})
	out.Write(Event{Kind: Said, Text: "lo"})
	out.Write(Event{Kind: Called, Index: 0, Tool: &ToolCall{ID: "call_9", Name: "read", Args: `{"path":"a"}`}})
	out.Write(Event{Kind: Stopped, Res: &Result{ID: "chatcmpl-77", Usage: Usage{In: 3, Out: 2}}})
	out.Close()
	var resp struct {
		ID     string `json:"id"`
		Object string `json:"object"`
		Status string `json:"status"`
		Model  string `json:"model"`
		Output []struct {
			Type      string `json:"type"`
			CallID    string `json:"call_id"`
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
			Status    string `json:"status"`
			Content   []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
			Summary []struct{ Text string } `json:"summary"`
		} `json:"output"`
		Usage struct {
			In    int `json:"input_tokens"`
			Out   int `json:"output_tokens"`
			Total int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("%v: %s", err, w.Body.String())
	}
	if resp.ID != "resp_77" || resp.Object != "response" || resp.Status != "completed" || resp.Model != "m" || resp.Usage.In != 3 || resp.Usage.Out != 2 || resp.Usage.Total != 5 {
		t.Fatalf("response %s", w.Body.String())
	}
	if len(resp.Output) != 3 || resp.Output[0].Type != "reasoning" || resp.Output[0].Summary[0].Text != "hm" || resp.Output[1].Type != "message" || resp.Output[1].Content[0].Type != "output_text" || resp.Output[1].Content[0].Text != "Hello" {
		t.Fatalf("output %s", w.Body.String())
	}
	if call := resp.Output[2]; call.Type != "function_call" || call.CallID != "call_9" || call.Name != "read" || call.Arguments != `{"path":"a"}` || call.Status != "completed" {
		t.Fatalf("call %+v", call)
	}
	w = httptest.NewRecorder()
	out = (openai{}).Answer(w, req, "m")
	out.Write(Event{Kind: Said, Text: "cut"})
	out.Write(Event{Kind: Stopped, Res: &Result{Stop: StopLength}})
	out.Close()
	if !strings.Contains(w.Body.String(), `"status":"incomplete"`) || !strings.Contains(w.Body.String(), `"reason":"max_output_tokens"`) {
		t.Fatalf("cut short %s", w.Body.String())
	}
}

// The events a stream wrote, by type, with their data
func responseEvents(t *testing.T, body string) ([]string, []map[string]any) {
	t.Helper()
	var kinds []string
	var data []map[string]any
	err := ReadSSE(strings.NewReader(body), func(event string, raw []byte) error {
		var fields map[string]any
		if err := json.Unmarshal(raw, &fields); err != nil {
			return err
		}
		if fields["type"] != event {
			t.Fatalf("event %s carries type %v", event, fields["type"])
		}
		kinds = append(kinds, event)
		data = append(data, fields)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return kinds, data
}

func TestResponsesStream(t *testing.T) {
	req, _ := (openai{}).Parse(OpenAIResponses, []byte(`{"model":"m","input":"hi","stream":true}`))
	w := httptest.NewRecorder()
	out := (openai{}).Answer(w, req, "m")
	out.Write(Event{Kind: Started, Res: &Result{ID: "x"}})
	out.Write(Event{Kind: Thought, Text: "hm"})
	out.Write(Event{Kind: Said, Text: "Hel"})
	out.Write(Event{Kind: Said, Text: "lo"})
	out.Write(Event{Kind: Called, Index: 0, Tool: &ToolCall{ID: "call_9", Name: "read", Args: `{"pa`}})
	out.Write(Event{Kind: Called, Index: 0, Tool: &ToolCall{Args: `th":"a"}`}})
	out.Write(Event{Kind: Stopped, Res: &Result{Usage: Usage{In: 3, Out: 2}}})
	out.Close()
	if ct := w.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type %q", ct)
	}
	kinds, data := responseEvents(t, w.Body.String())
	want := []string{
		"response.created", "response.in_progress",
		"response.output_item.added", "response.reasoning_summary_part.added", "response.reasoning_summary_text.delta", "response.reasoning_summary_text.done", "response.reasoning_summary_part.done", "response.output_item.done",
		"response.output_item.added", "response.content_part.added", "response.output_text.delta", "response.output_text.delta", "response.output_text.done", "response.content_part.done", "response.output_item.done",
		"response.output_item.added", "response.function_call_arguments.delta", "response.function_call_arguments.delta", "response.function_call_arguments.done", "response.output_item.done",
		"response.completed",
	}
	if strings.Join(kinds, "\n") != strings.Join(want, "\n") {
		t.Fatalf("events:\n%s", strings.Join(kinds, "\n"))
	}
	for i, d := range data {
		if d["sequence_number"] != float64(i) {
			t.Fatalf("event %d numbered %v", i, d["sequence_number"])
		}
	}
	if data[8]["output_index"] != 1.0 || data[15]["output_index"] != 2.0 || data[12]["text"] != "Hello" || data[18]["arguments"] != `{"path":"a"}` {
		t.Fatalf("indexes and gathered text: %v %v %v %v", data[8], data[15], data[12], data[18])
	}
	final := data[len(data)-1]["response"].(map[string]any)
	output := final["output"].([]any)
	if final["status"] != "completed" || len(output) != 3 || output[2].(map[string]any)["call_id"] != "call_9" || output[2].(map[string]any)["status"] != "completed" || final["usage"].(map[string]any)["total_tokens"] != 5.0 {
		t.Fatalf("final %v", final)
	}
	if !strings.HasPrefix(final["id"].(string), "resp_") || final["id"] != data[0]["response"].(map[string]any)["id"] {
		t.Fatalf("ids %v %v", final["id"], data[0]["response"])
	}
	// A failure ends the stream with the response failed and an error event
	w = httptest.NewRecorder()
	out = (openai{}).Answer(w, req, "m")
	out.Write(Event{Kind: Said, Text: "part"})
	out.Write(Event{Kind: Failed, Failure: &Failure{Code: CodeTimeout, Message: "too slow"}})
	out.Close()
	kinds, data = responseEvents(t, w.Body.String())
	if kinds[len(kinds)-2] != "response.failed" || kinds[len(kinds)-1] != "error" || data[len(data)-1]["message"] != "too slow" || data[len(data)-1]["code"] != CodeTimeout {
		t.Fatalf("failure events %v %v", kinds, data[len(data)-1])
	}
	// Nothing said still answers an empty message
	w = httptest.NewRecorder()
	out = (openai{}).Answer(w, req, "m")
	out.Write(Event{Kind: Stopped, Res: &Result{}})
	out.Close()
	kinds, data = responseEvents(t, w.Body.String())
	if kinds[len(kinds)-1] != "response.completed" || len(data[len(data)-1]["response"].(map[string]any)["output"].([]any)) != 1 {
		t.Fatalf("empty answer %v", kinds)
	}
}

func TestChatCompletionsUsageChunkTrailsWhenAsked(t *testing.T) {
	chunks := func(body string) []oaiResponse {
		req, err := (openai{}).Parse(OpenAIChat, []byte(body))
		if err != nil {
			t.Fatal(err)
		}
		w := httptest.NewRecorder()
		out := (openai{}).Answer(w, req, "m")
		out.Write(Event{Kind: Said, Text: "hi"})
		out.Write(Event{Kind: Stopped, Res: &Result{Usage: Usage{In: 3, Out: 1}}})
		out.Close()
		var got []oaiResponse
		var done bool
		ReadSSE(strings.NewReader(w.Body.String()), func(_ string, data []byte) error {
			if string(data) == "[DONE]" {
				done = true
				return nil
			}
			var c oaiResponse
			if err := json.Unmarshal(data, &c); err != nil {
				t.Fatalf("%v: %s", err, data)
			}
			got = append(got, c)
			return nil
		})
		if !done {
			t.Fatal("stream did not end with [DONE]")
		}
		return got
	}
	got := chunks(`{"model":"m","stream":true,"stream_options":{"include_usage":true},"messages":[{"role":"user","content":"x"}]}`)
	if len(got) != 3 || got[1].Usage != nil || *got[1].Choices[0].FinishReason != "stop" {
		t.Fatalf("finish chunk carries no usage: %+v", got)
	}
	if last := got[2]; last.Usage == nil || last.Usage.PromptTokens != 3 || last.Usage.TotalTokens != 4 || last.Choices == nil || len(last.Choices) != 0 {
		t.Fatalf("usage chunk %+v", last)
	}
	raw, _ := json.Marshal(got[2])
	if !strings.Contains(string(raw), `"choices":[]`) {
		t.Fatalf("choices must be an empty list: %s", raw)
	}
	got = chunks(`{"model":"m","stream":true,"messages":[{"role":"user","content":"x"}]}`)
	if len(got) != 2 || got[1].Usage != nil {
		t.Fatalf("no usage unless asked: %+v", got)
	}
}
