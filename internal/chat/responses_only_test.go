package chat

import (
	"encoding/json"
	"strings"
	"testing"
)

// A chat completions request as the web chat and most clients write one, with everything a plan sign-in's provider refuses
const planChat = `{"model":"gpt","stream":false,"max_tokens":64,"temperature":0.2,"top_p":0.9,"stop":["END"],"seed":7,"n":1,
	"stream_options":{"include_usage":true},"reasoning_effort":"high","parallel_tool_calls":false,"user":"u1","metadata":{"k":"v"},
	"response_format":{"type":"json_schema","json_schema":{"name":"answer","schema":{"type":"object"},"strict":true}},
	"messages":[
	{"role":"system","content":"be brief"},
	{"role":"developer","content":"and kind"},
	{"role":"user","content":[{"type":"text","text":"look"},{"type":"image_url","image_url":{"url":"https://example.com/a.png","detail":"low"}}]},
	{"role":"assistant","content":"Reading.","reasoning_content":"hm","tool_calls":[{"id":"call_1","type":"function","function":{"name":"read","arguments":"{\"path\":\"a\"}"}}]},
	{"role":"tool","tool_call_id":"call_1","content":"buy milk"},
	{"role":"system","content":"a later rule"},
	{"role":"user","content":"thanks"}],
	"tools":[{"type":"function","function":{"name":"read","description":"Reads","parameters":{"type":"object"},"strict":true}},
		{"type":"function","function":{"name":"now"}}],
	"tool_choice":{"type":"function","function":{"name":"read"}}}`

func renderPlan(t *testing.T, rules ResponsesRules, path, body string) map[string]any {
	t.Helper()
	req, err := (openai{}).Parse(path, []byte(body))
	if err != nil {
		t.Fatal(err)
	}
	to, out, err := ResponsesOnly(rules).Render(req)
	if err != nil {
		t.Fatal(err)
	}
	if to != OpenAIResponses {
		t.Fatalf("rendered for %s", to)
	}
	var doc map[string]any
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	return doc
}

// A chat rendered for a provider serving the Responses API alone is streamed and stored nowhere whatever the client asked,
// leads with its system turns as instructions, and carries none of the fields such a provider refuses
func TestResponsesOnlyRendersAChat(t *testing.T) {
	doc := renderPlan(t, ResponsesRules{Namespace: "functions"}, OpenAIChat, planChat)
	if doc["model"] != "gpt" || doc["store"] != false || doc["stream"] != true || doc["instructions"] != "be brief\n\nand kind" {
		t.Fatalf("head %v", doc)
	}
	for _, gone := range []string{"messages", "max_tokens", "max_output_tokens", "temperature", "top_p", "stop", "seed", "n", "stream_options", "user", "metadata", "reasoning_effort", "response_format", "previous_response_id"} {
		if _, ok := doc[gone]; ok {
			t.Fatalf("%s reached a provider that refuses it: %v", gone, doc[gone])
		}
	}
	want := []string{
		`{"content":[{"text":"look","type":"input_text"},{"detail":"low","image_url":"https://example.com/a.png","type":"input_image"}],"role":"user","type":"message"}`,
		`{"content":"Reading.","role":"assistant","type":"message"}`,
		`{"arguments":"{\"path\":\"a\"}","call_id":"call_1","name":"read","namespace":"functions","type":"function_call"}`,
		`{"call_id":"call_1","output":"buy milk","type":"function_call_output"}`,
		`{"content":"a later rule","role":"developer","type":"message"}`,
		`{"content":"thanks","role":"user","type":"message"}`,
	}
	input, _ := doc["input"].([]any)
	if len(input) != len(want) {
		t.Fatalf("input %v", input)
	}
	for i, item := range input {
		if got, _ := json.Marshal(item); string(got) != want[i] {
			t.Fatalf("input item %d is %s, want %s", i, got, want[i])
		}
	}
	tools, _ := json.Marshal(doc["tools"])
	if string(tools) != `[{"description":"Functions the client offers","name":"functions","tools":[`+
		`{"description":"Reads","name":"read","parameters":{"type":"object"},"strict":true,"type":"function"},`+
		`{"name":"now","parameters":{"properties":{},"type":"object"},"strict":false,"type":"function"}],"type":"namespace"}]` {
		t.Fatalf("tools %s", tools)
	}
	if choice, _ := json.Marshal(doc["tool_choice"]); string(choice) != `{"name":"read","type":"function"}` {
		t.Fatalf("tool_choice %s", choice)
	}
	if reasoning, _ := json.Marshal(doc["reasoning"]); string(reasoning) != `{"effort":"high","summary":"auto"}` {
		t.Fatalf("reasoning %s", reasoning)
	}
	if text, _ := json.Marshal(doc["text"]); string(text) != `{"format":{"name":"answer","schema":{"type":"object"},"strict":true,"type":"json_schema"}}` {
		t.Fatalf("text %s", text)
	}
	if doc["parallel_tool_calls"] != false {
		t.Fatalf("parallel_tool_calls %v", doc["parallel_tool_calls"])
	}
}

// A provider taking tools at the top level gets them flat, and one that refuses a request without instructions gets its
// own when the chat leads with no system turn, a chat leading with one keeping it
func TestResponsesOnlyFollowsItsProvidersRules(t *testing.T) {
	rules := ResponsesRules{Instructions: "You are a helpful assistant."}
	bare := `{"model":"gpt","messages":[{"role":"user","content":"sup bro"}],"stream":true,"stream_options":{"include_usage":true},"tools":[{"type":"function","function":{"name":"now"}}]}`
	doc := renderPlan(t, rules, OpenAIChat, bare)
	if doc["instructions"] != rules.Instructions || doc["store"] != false || doc["stream"] != true {
		t.Fatalf("head %v", doc)
	}
	if input, _ := json.Marshal(doc["input"]); string(input) != `[{"content":"sup bro","role":"user","type":"message"}]` {
		t.Fatalf("input %s", input)
	}
	if tools, _ := json.Marshal(doc["tools"]); string(tools) != `[{"name":"now","parameters":{"properties":{},"type":"object"},"strict":false,"type":"function"}]` {
		t.Fatalf("tools %s", tools)
	}
	if _, ok := doc["reasoning"]; ok {
		t.Fatalf("reasoning was asked for a client that named no effort: %v", doc["reasoning"])
	}
	led := renderPlan(t, rules, OpenAIChat, `{"model":"gpt","messages":[{"role":"system","content":"be brief"},{"role":"user","content":"hi"}]}`)
	if led["instructions"] != "be brief" {
		t.Fatalf("instructions %v", led["instructions"])
	}
	if none := renderPlan(t, ResponsesRules{}, OpenAIChat, `{"model":"gpt","messages":[{"role":"user","content":"hi"}]}`); none["instructions"] != nil {
		t.Fatalf("instructions %v sent to a provider asking for none", none["instructions"])
	}
}

// A Responses request of the client's own that is rendered again, as one asking for a whole answer is, keeps to the same rules
func TestResponsesOnlyRendersAResponsesRequest(t *testing.T) {
	doc := renderPlan(t, ResponsesRules{}, OpenAIResponses, `{"model":"gpt","instructions":"be brief","input":"hi","max_output_tokens":9,"temperature":0.5,"metadata":{"k":"v"},"text":{"verbosity":"low"}}`)
	if doc["instructions"] != "be brief" || doc["store"] != false || doc["stream"] != true {
		t.Fatalf("head %v", doc)
	}
	if text, _ := json.Marshal(doc["text"]); string(text) != `{"verbosity":"low"}` {
		t.Fatalf("text %s", text)
	}
	for _, gone := range []string{"max_output_tokens", "temperature", "metadata"} {
		if _, ok := doc[gone]; ok {
			t.Fatalf("%s reached a provider that refuses it", gone)
		}
	}
}

// What the Responses API has no form for is refused in words that say what would serve it
func TestResponsesOnlyRefusals(t *testing.T) {
	rt := ResponsesOnly(ResponsesRules{})
	for name, c := range map[string]struct{ path, body, want string }{
		"embeddings": {OpenAIEmbeddings, `{"model":"m","input":"a"}`, "embeddings take a key"},
		"raw prompt": {OpenAICompletions, `{"model":"m","prompt":"a"}`, "completions take a key"},
		"choices":    {OpenAIChat, `{"model":"m","n":3,"messages":[{"role":"user","content":"a"}]}`, "one choice a call"},
		"stored":     {OpenAIResponses, `{"model":"m","input":"a","previous_response_id":"resp_1"}`, "previous_response_id"},
	} {
		req, err := (openai{}).Parse(c.path, []byte(c.body))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if _, _, err := rt.Render(req); err == nil || !strings.Contains(err.Error(), c.want) || FailureOf(err).Status != 400 {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if tr := rt.Traits(); !tr.ResponsesOnly || !tr.Responses || tr.Choices || tr.TokenizePath != "" {
		t.Fatalf("traits %+v", tr)
	}
	chat, _ := (openai{}).Parse(OpenAIChat, []byte(`{"model":"m","messages":[{"role":"user","content":"a"}]}`))
	if _, _, err := rt.Native(chat); err == nil {
		t.Fatal("a chat completions body passed as it is to a provider serving the Responses API alone")
	}
	own, _ := (openai{}).Parse(OpenAIResponses, []byte(`{"model":"m","input":"a","stream":true,"store":false}`))
	if path, _, err := rt.Native(own); err != nil || path != OpenAIResponses {
		t.Fatalf("a Responses body of the client's own: %s %v", path, err)
	}
}

// The stream such a provider answers reads into the same events as any answer, a namespaced call under its bare name
func TestResponsesOnlyReadsItsStream(t *testing.T) {
	stream := strings.Join([]string{
		`event: response.created` + "\n" + `data: {"type":"response.created","sequence_number":0,"response":{"id":"resp_1","object":"response","model":"gpt-6.1-terra","status":"in_progress","output":[]}}`,
		`event: response.output_item.added` + "\n" + `data: {"type":"response.output_item.added","output_index":0,"item":{"id":"rs_1","type":"reasoning","summary":[]}}`,
		`event: response.reasoning_summary_text.delta` + "\n" + `data: {"type":"response.reasoning_summary_text.delta","item_id":"rs_1","output_index":0,"summary_index":0,"delta":"hm"}`,
		`event: response.output_item.added` + "\n" + `data: {"type":"response.output_item.added","output_index":1,"item":{"id":"msg_1","type":"message","role":"assistant","status":"in_progress","content":[]}}`,
		`event: response.output_text.delta` + "\n" + `data: {"type":"response.output_text.delta","item_id":"msg_1","output_index":1,"content_index":0,"delta":"sup"}`,
		`event: response.output_text.delta` + "\n" + `data: {"type":"response.output_text.delta","item_id":"msg_1","output_index":1,"content_index":0,"delta":" bro"}`,
		`event: response.output_item.added` + "\n" + `data: {"type":"response.output_item.added","output_index":2,"item":{"id":"fc_1","type":"function_call","status":"in_progress","call_id":"call_1","name":"read","namespace":"functions","arguments":""}}`,
		`event: response.function_call_arguments.delta` + "\n" + `data: {"type":"response.function_call_arguments.delta","item_id":"fc_1","output_index":2,"delta":"{\"path\":"}`,
		`event: response.function_call_arguments.delta` + "\n" + `data: {"type":"response.function_call_arguments.delta","item_id":"fc_1","output_index":2,"delta":"\"a\"}"}`,
		`event: response.function_call_arguments.done` + "\n" + `data: {"type":"response.function_call_arguments.done","item_id":"fc_1","output_index":2,"arguments":"{\"path\":\"a\"}"}`,
		`event: response.output_item.done` + "\n" + `data: {"type":"response.output_item.done","output_index":2,"item":{"id":"fc_1","type":"function_call","status":"completed","call_id":"call_1","name":"read","namespace":"functions","arguments":"{\"path\":\"a\"}"}}`,
		`event: response.completed` + "\n" + `data: {"type":"response.completed","response":{"id":"resp_1","object":"response","model":"gpt-6.1-terra","status":"completed","usage":{"input_tokens":11,"input_tokens_details":{"cached_tokens":4},"output_tokens":5,"total_tokens":16}}}`,
	}, "\n\n") + "\n\n"
	var said, thought strings.Builder
	var final *Result
	err := ResponsesOnly(ResponsesRules{Namespace: "functions"}).ParseStream(strings.NewReader(stream), func(ev Event) error {
		switch ev.Kind {
		case Said:
			said.WriteString(ev.Text)
		case Thought:
			thought.WriteString(ev.Text)
		case Stopped:
			final = ev.Res
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if said.String() != "sup bro" || thought.String() != "hm" || final == nil {
		t.Fatalf("said %q thought %q final %+v", said.String(), thought.String(), final)
	}
	if final.ID != "resp_1" || final.Model != "gpt-6.1-terra" || final.Stop != StopTool || final.Usage.In != 11 || final.Usage.Out != 5 || final.Usage.Cached != 4 {
		t.Fatalf("final %+v", final)
	}
	if len(final.ToolCalls) != 1 || final.ToolCalls[0] != (ToolCall{ID: "call_1", Name: "read", Args: `{"path":"a"}`}) {
		t.Fatalf("calls %+v", final.ToolCalls)
	}
	failed := `data: {"type":"response.failed","response":{"id":"resp_2","status":"failed","error":{"code":"subscription_sharing_usage_limit_exceeded","message":"the plan's usage limit was reached"}}}` + "\n\n"
	if err := ResponsesOnly(ResponsesRules{}).ParseStream(strings.NewReader(failed), func(Event) error { return nil }); err == nil || !strings.Contains(err.Error(), "usage limit") {
		t.Fatalf("a failed response read as %v", err)
	}
}

// A refusal's own words reach the client: the message with the parameter it blames, or the detail sent ahead of a stream
func TestResponsesOnlyErrorMessage(t *testing.T) {
	rt := ResponsesOnly(ResponsesRules{})
	for body, want := range map[string]string{
		`{"error":{"message":"Unsupported parameter","type":"invalid_request_error","code":"subscription_sharing_unsupported_capability","param":"temperature"}}`: "Unsupported parameter, param temperature",
		`{"error":{"message":"Unauthorized","type":"rejected_by_access_enforcement","code":"no_matching_rule","param":null},"status":401}`:                        "Unauthorized",
		`{"detail":"Instructions are required"}`: "Instructions are required",
		`<html>bad gateway</html>`:               "",
	} {
		if got := rt.ErrorMessage([]byte(body)); got != want {
			t.Fatalf("%s read as %q, want %q", body, got, want)
		}
	}
}
