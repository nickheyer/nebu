package chat

import (
	"net/http"
	"strings"
	"testing"
)

// A Responses request passes to a runtime serving the API with its stored state, and translates without it
func TestResponsesNativeVersusTranslated(t *testing.T) {
	body := `{"model":"alias","input":[{"type":"item_reference","id":"msg_1"},{"role":"user","content":"next"}],"previous_response_id":"resp_9","conversation":{"id":"conv_1"},"store":true,"background":true,"include":["reasoning.encrypted_content"]}`
	req, err := (openai{}).Parse(OpenAIResponses, []byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if !(openai{}).Traits().Responses || (anthropic{}).Traits().Responses || (ollama{}).Traits().Responses {
		t.Fatal("only the openai protocol serves the Responses API itself")
	}
	req.Model = "served"
	path, out, err := (openai{}).Native(req)
	if err != nil || path != OpenAIResponses {
		t.Fatalf("%s %v", path, err)
	}
	if want := strings.Replace(body, `"alias"`, `"served"`, 1); string(out) != want {
		t.Fatalf("passed\n%s\nwant\n%s", out, want)
	}
	for _, field := range []string{"previous_response_id", "conversation", "store", "background", "include", "input"} {
		if string(rawField(out, field)) != string(rawField([]byte(body), field)) {
			t.Fatalf("%s changed on the way: %s", field, rawField(out, field))
		}
	}
	_, _, err = (openai{}).Render(req)
	if f, ok := err.(*Failure); !ok || f.Status != http.StatusBadRequest || !strings.Contains(f.Message, "previous_response_id") {
		t.Fatalf("translation kept stored state: %v", err)
	}
	for _, rt := range []Runtime{anthropic{}, ollama{}, sdcpp{}, Emulate(openai{})} {
		if _, _, err := rt.Native(req); err == nil {
			t.Fatalf("%T took a Responses request natively", rt)
		}
	}
	plain, _ := (openai{}).Parse(OpenAIResponses, []byte(`{"model":"m","input":"hi"}`))
	if path, out, err := (openai{}).Render(plain); err != nil || path != OpenAIChat || rawField(out, "messages") == nil {
		t.Fatalf("translated %s %s %v", path, out, err)
	}
}

// Native bodies keep every byte but the model, which is inserted when the client named none
func TestNativeRenamesModelOnly(t *testing.T) {
	for _, tc := range []struct{ path, body, model, want string }{
		{OpenAIChat, `{ "messages" : [], "model" : "a" , "x":1}`, "b", `{ "messages" : [], "model" : "b" , "x":1}`},
		{OpenAIChat, `{"model":"a","x":"é"}`, "a", `{"model":"a","x":"é"}`},
		{AnthropicMessages, `{"messages":[]}`, "m\"q", `{"model":"m\"q","messages":[]}`},
		{OllamaChat, ` {}`, "m", ` {"model":"m"}`},
	} {
		var rt Runtime
		var cl Client
		switch tc.path {
		case AnthropicMessages:
			rt, cl = anthropic{}, anthropic{}
		case OllamaChat:
			rt, cl = ollama{}, ollama{}
		default:
			rt, cl = openai{}, openai{}
		}
		req, err := cl.Parse(tc.path, []byte(tc.body))
		if err != nil {
			t.Fatal(err)
		}
		req.Model = tc.model
		path, out, err := rt.Native(req)
		if err != nil || path != tc.path || string(out) != tc.want {
			t.Fatalf("%s: %s %s %v", tc.body, path, out, err)
		}
	}
	req, _ := (anthropic{}).Parse(AnthropicMessages, []byte(`{"model":"m","messages":[]}`))
	if _, _, err := (openai{}).Native(req); err == nil {
		t.Fatal("the openai protocol took an anthropic path natively")
	}
}

// A native Responses answer reads back whole and streamed, ids, text, reasoning, calls, usage, and stop kept
func TestResponsesNativeAnswers(t *testing.T) {
	req := &Request{Kind: Chat, Responses: true, Model: "m"}
	body := `{"id":"resp_1","object":"response","created_at":1,"model":"m","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},
		"output":[{"type":"reasoning","id":"rs","summary":[{"type":"summary_text","text":"think"}]},{"type":"message","id":"msg","role":"assistant","content":[{"type":"output_text","text":"hel","annotations":[]},{"type":"output_text","text":"lo"}]},{"type":"function_call","id":"fc","call_id":"c1","name":"f","arguments":"{\"a\":1}"}],
		"usage":{"input_tokens":10,"output_tokens":4,"input_tokens_details":{"cached_tokens":6},"output_tokens_details":{"reasoning_tokens":1}},"error":null,"store":true}`
	res, err := (openai{}).ParseResult(req, []byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if res.ID != "resp_1" || res.Text != "hello" || res.Thinking != "think" || len(res.ToolCalls) != 1 || res.ToolCalls[0].ID != "c1" || res.ToolCalls[0].Args != `{"a":1}` || res.Usage.In != 10 || res.Usage.Cached != 6 || res.Stop != StopLength {
		t.Fatalf("result %+v", res)
	}
	if respID(res) != "resp_1" {
		t.Fatalf("id %s", respID(res))
	}
	failed := `{"id":"resp_2","object":"response","status":"failed","output":[],"error":{"code":"server_error","message":"boom"}}`
	if _, err := (openai{}).ParseResult(req, []byte(failed)); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("failed response %v", err)
	}
	sse := "event: response.created\ndata: {\"type\":\"response.created\",\"sequence_number\":0,\"response\":{\"id\":\"resp_3\",\"model\":\"m\",\"status\":\"in_progress\",\"output\":[]}}\n\n" +
		"event: response.reasoning_summary_text.delta\ndata: {\"type\":\"response.reasoning_summary_text.delta\",\"delta\":\"hm\",\"item_id\":\"rs\",\"output_index\":0}\n\n" +
		"event: response.output_text.delta\ndata: {\"content_index\":0,\"delta\":\"Hi\",\"item_id\":\"msg\",\"logprobs\":[],\"output_index\":1,\"sequence_number\":2,\"type\":\"response.output_text.delta\"}\n\n" +
		"event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"output_index\":2,\"item\":{\"type\":\"function_call\",\"id\":\"fc\",\"call_id\":\"c1\",\"name\":\"f\",\"arguments\":\"\"}}\n\n" +
		"event: response.function_call_arguments.delta\ndata: {\"type\":\"response.function_call_arguments.delta\",\"item_id\":\"fc\",\"output_index\":2,\"delta\":\"{\\\"a\\\"\"}\n\n" +
		"event: response.function_call_arguments.delta\ndata: {\"type\":\"response.function_call_arguments.delta\",\"item_id\":\"fc\",\"output_index\":2,\"delta\":\":1}\"}\n\n" +
		"event: response.function_call_arguments.done\ndata: {\"type\":\"response.function_call_arguments.done\",\"item_id\":\"fc\",\"output_index\":2,\"arguments\":\"{\\\"a\\\":1}\"}\n\n" +
		"event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"output_index\":2,\"item\":{\"type\":\"function_call\",\"id\":\"fc\",\"call_id\":\"c1\",\"name\":\"f\",\"arguments\":\"{\\\"a\\\":1}\"}}\n\n" +
		"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_3\",\"model\":\"m\",\"status\":\"completed\",\"usage\":{\"input_tokens\":5,\"output_tokens\":2,\"input_tokens_details\":{\"cached_tokens\":3}}}}\n\n"
	var started, stopped *Result
	var said, thought string
	var calls toolGather
	err = (openai{}).ParseStream(strings.NewReader(sse), func(ev Event) error {
		switch ev.Kind {
		case Started:
			started = ev.Res
		case Said:
			said += ev.Text
		case Thought:
			thought += ev.Text
		case Called:
			calls.add(ev)
		case Stopped:
			stopped = ev.Res
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	got := calls.list()
	if started == nil || started.ID != "resp_3" || said != "Hi" || thought != "hm" || len(got) != 1 || got[0].ID != "c1" || got[0].Name != "f" || got[0].Args != `{"a":1}` {
		t.Fatalf("started %+v said %q thought %q calls %+v", started, said, thought, got)
	}
	if stopped == nil || stopped.ID != "resp_3" || stopped.Usage.In != 5 || stopped.Usage.Cached != 3 || stopped.Stop != StopTool || len(stopped.ToolCalls) != 1 || len(stopped.Extra) != 0 {
		t.Fatalf("stopped %+v", stopped)
	}
	failing := "event: response.failed\ndata: {\"type\":\"response.failed\",\"response\":{\"id\":\"r\",\"status\":\"failed\",\"error\":{\"code\":\"x\",\"message\":\"no\"}}}\n\n"
	if err := (openai{}).ParseStream(strings.NewReader(failing), func(Event) error { return nil }); err == nil || err.Error() != "no" {
		t.Fatalf("failed stream %v", err)
	}
}
