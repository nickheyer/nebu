package chat

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/nickheyer/nebu/pkg/bodyparse"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// The previous two pass algorithm, a second unmarshal into a map with the mapped names deleted
func legacyExtra(raw []byte, known ...string) map[string]json.RawMessage {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return nil
	}
	for _, name := range known {
		delete(fields, name)
	}
	if len(fields) == 0 {
		return nil
	}
	return fields
}

func sameExtra(t *testing.T, where string, got, want map[string]json.RawMessage) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: extra keys %v, want %v", where, slices.Sorted(maps.Keys(got)), slices.Sorted(maps.Keys(want)))
	}
	for k, v := range want {
		if !bytes.Equal(got[k], v) {
			t.Fatalf("%s: extra %s is %s, want %s", where, k, got[k], v)
		}
	}
}

func rawField(raw json.RawMessage, key string) json.RawMessage {
	var fields map[string]json.RawMessage
	json.Unmarshal(raw, &fields)
	return fields[key]
}

func rawList(raw json.RawMessage) []json.RawMessage {
	var list []json.RawMessage
	json.Unmarshal(raw, &list)
	return list
}

// The names each wire reader maps, as the two pass algorithm deleted them
var (
	oaiRequestKnown  = []string{"model", "messages", "prompt", "input", "max_tokens", "max_completion_tokens", "temperature", "top_p", "top_k", "n", "stop", "stream", "stream_options", "tools", "tool_choice"}
	oaiMessageKnown  = []string{"role", "content", "tool_calls", "tool_call_id", "reasoning_content", "reasoning"}
	oaiPartKnown     = []string{"type", "text", "image_url"}
	oaiToolKnown     = []string{"type", "function"}
	oaiFunctionKnown = []string{"name", "description", "parameters", "strict"}
	antRequestKnown  = []string{"model", "system", "messages", "max_tokens", "temperature", "top_p", "top_k", "stop_sequences", "stream", "tools", "tool_choice", "mcp_servers"}
	antMessageKnown  = []string{"role", "content"}
	antBlockKnown    = []string{"type", "text", "id", "name", "input", "tool_use_id", "content", "source", "thinking", "signature", "server_name", "is_error", "title", "url", "encrypted_content", "page_age", "error_code", "retrieved_at"}
	antToolKnown     = []string{"type", "name", "description", "input_schema", "strict", "defer_loading"}
	olRequestKnown   = []string{"model", "messages", "prompt", "system", "images", "input", "stream", "raw", "options", "tools"}
	olMessageKnown   = []string{"role", "content", "thinking", "images", "tool_calls"}
	respRequestKnown = []string{"model", "input", "instructions", "max_output_tokens", "temperature", "top_p", "stream", "tools", "tool_choice", "previous_response_id", "conversation", "prompt", "background", "store", "reasoning", "text", "include", "truncation", "max_tool_calls"}
	respItemKnown    = []string{"type", "role", "content", "call_id", "name", "arguments", "output", "summary", "id", "status", "encrypted_content"}
	respPartKnown    = []string{"type", "text", "image_url", "file_id", "refusal", "detail", "annotations", "logprobs"}
	respToolKnown    = []string{"type", "name", "description", "parameters", "strict", "server_label", "server_url", "authorization", "headers", "allowed_tools", "filters", "user_location"}
)

// One pass keeps exactly the members the two pass algorithm kept, at the request, message, part, and tool levels
func TestSinglePassExtraMatchesTwoPass(t *testing.T) {
	openaiBodies := []string{
		`{"model":"m","seed":7,"user":"u","logit_bias":{"1":-100},"n":2,"top_k":5,"messages":[
			{"role":"system","content":"be é \"brief\"","name":"sys","x-trace":[1,2,{"a":null}]},
			{"role":"user","content":[{"type":"text","text":"look","cache_control":{"type":"ephemeral"}},{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA","detail":"high"},"vendor":true}]},
			{"role":"assistant","content":null,"reasoning_content":"hm","tool_calls":[{"id":"c1","type":"function","function":{"name":"f","arguments":"{\"a\":1}"}}],"refusal":null},
			{"role":"tool","tool_call_id":"c1","content":"ok","weird key \n":  1.5e3 }],
		 "tools":[{"type":"function","function":{"name":"f","parameters":{"type":"object"},"strict":true,"x-fn":"fn"},"x-tool":{"deep":[true]}}],
		 "tool_choice":{"type":"function","function":{"name":"f"}},"stream":true,"stream_options":{"include_usage":true},"seed":8}`,
		`{"model":"m","prompt":["a","b"],"suffix":"z","echo":false,"logprobs":2}`,
		`{"model":"m","input":[[1,2],[3]],"dimensions":256,"encoding_format":"float"}`,
	}
	for i, body := range openaiBodies {
		var r oaiRequest
		if err := decode([]byte(body), r.read); err != nil {
			t.Fatalf("openai %d: %v", i, err)
		}
		sameExtra(t, fmt.Sprintf("openai %d request", i), r.Extra, legacyExtra([]byte(body), oaiRequestKnown...))
		for j, m := range rawList(rawField([]byte(body), "messages")) {
			sameExtra(t, fmt.Sprintf("openai %d message %d", i, j), r.Messages[j].Extra, legacyExtra(m, oaiMessageKnown...))
			for k, p := range rawList(rawField(m, "content")) {
				sameExtra(t, fmt.Sprintf("openai %d message %d part %d", i, j, k), r.Messages[j].Content.Parts[k].Extra, legacyExtra(p, oaiPartKnown...))
			}
		}
		for j, tool := range rawList(rawField([]byte(body), "tools")) {
			sameExtra(t, fmt.Sprintf("openai %d tool %d", i, j), r.Tools[j].Extra, legacyExtra(tool, oaiToolKnown...))
			sameExtra(t, fmt.Sprintf("openai %d function %d", i, j), r.Tools[j].Function.Extra, legacyExtra(rawField(tool, "function"), oaiFunctionKnown...))
		}
	}

	antBodies := []string{string(agentBody(3)), `{"model":"m","max_tokens":5,"metadata":{"user_id":"u"},"thinking":{"type":"enabled","budget_tokens":1000},"system":[{"type":"text","text":"s","cache_control":{"type":"ephemeral"}}],
		"messages":[{"role":"user","content":[{"type":"document","title":"T","source":{"type":"text","media_type":"text/plain","data":"doc"},"citations":{"enabled":true}},{"type":"text","text":"hi","citations":[]}],"x-msg":1},
			{"role":"assistant","content":[{"type":"thinking","thinking":"t","signature":"sig"},{"type":"tool_use","id":"t1","name":"f","input":{"a":[1,2]},"caller":{"type":"direct"}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":[{"type":"text","text":"r","x-inner":"i"}],"is_error":false}]}],
		"tools":[{"name":"f","input_schema":{"type":"object"},"defer_loading":true,"cache_control":{"type":"ephemeral"},"input_examples":[{}]},{"type":"web_search_20250305","name":"web_search","max_uses":3,"allowed_domains":["a.example"],"user_location":{"type":"approximate","country":"US","city":"X"}}]}`}
	for i, body := range antBodies {
		var r antRequest
		if err := decode([]byte(body), r.read); err != nil {
			t.Fatalf("anthropic %d: %v", i, err)
		}
		sameExtra(t, fmt.Sprintf("anthropic %d request", i), r.Extra, legacyExtra([]byte(body), antRequestKnown...))
		for k, b := range rawList(rawField([]byte(body), "system")) {
			sameExtra(t, fmt.Sprintf("anthropic %d system %d", i, k), r.System.Blocks[k].Extra, legacyExtra(b, antBlockKnown...))
		}
		for j, m := range rawList(rawField([]byte(body), "messages")) {
			sameExtra(t, fmt.Sprintf("anthropic %d message %d", i, j), r.Messages[j].Extra, legacyExtra(m, antMessageKnown...))
			for k, b := range rawList(rawField(m, "content")) {
				block := r.Messages[j].Content.Blocks[k]
				sameExtra(t, fmt.Sprintf("anthropic %d message %d block %d", i, j, k), block.Extra, legacyExtra(b, antBlockKnown...))
				for n, inner := range rawList(rawField(b, "content")) {
					sameExtra(t, fmt.Sprintf("anthropic %d message %d block %d inner %d", i, j, k, n), block.Content.Blocks[n].Extra, legacyExtra(inner, antBlockKnown...))
				}
			}
		}
		for j, tool := range rawList(rawField([]byte(body), "tools")) {
			sameExtra(t, fmt.Sprintf("anthropic %d tool %d", i, j), r.Tools[j].Extra, legacyExtra(tool, antToolKnown...))
		}
	}

	olBody := `{"model":"m","keep_alive":"5m","format":"json","think":true,"options":{"temperature":0.1,"num_ctx":4096},"messages":[{"role":"user","content":"hi","images":["AAAA"],"tool_name":"x"},{"role":"assistant","content":"","tool_calls":[{"function":{"name":"f","arguments":{"a":1}}}]}],"tools":[{"type":"function","function":{"name":"f","x":1},"y":2}]}`
	var ol olRequest
	if err := decode([]byte(olBody), ol.read); err != nil {
		t.Fatal(err)
	}
	sameExtra(t, "ollama request", ol.Extra, legacyExtra([]byte(olBody), olRequestKnown...))
	for j, m := range rawList(rawField([]byte(olBody), "messages")) {
		sameExtra(t, fmt.Sprintf("ollama message %d", j), ol.Messages[j].Extra, legacyExtra(m, olMessageKnown...))
	}
	for j, tool := range rawList(rawField([]byte(olBody), "tools")) {
		sameExtra(t, fmt.Sprintf("ollama tool %d", j), ol.Tools[j].Extra, legacyExtra(tool, oaiToolKnown...))
		sameExtra(t, fmt.Sprintf("ollama function %d", j), ol.Tools[j].Function.Extra, legacyExtra(rawField(tool, "function"), oaiFunctionKnown...))
	}

	respBody := `{"model":"m","metadata":{"k":"v"},"parallel_tool_calls":false,"store":false,"include":["x"],"input":[
		{"type":"message","role":"user","content":[{"type":"input_text","text":"a","x-part":1},{"type":"input_image","image_url":"https://e/x.png","detail":"low","x-img":2}],"x-item":3},
		{"type":"function_call","id":"fc","call_id":"c","name":"f","arguments":"{}","status":"completed","x-call":4}],
		"tools":[{"type":"function","name":"f","parameters":{},"x-tool":5},{"type":"mcp","server_label":"l","server_url":"https://m","require_approval":"never"}]}`
	var rr respRequest
	if err := decode([]byte(respBody), rr.read); err != nil {
		t.Fatal(err)
	}
	sameExtra(t, "responses request", rr.Extra, legacyExtra([]byte(respBody), respRequestKnown...))
	for j, item := range rawList(rawField([]byte(respBody), "input")) {
		sameExtra(t, fmt.Sprintf("responses item %d", j), rr.Input[j].Extra, legacyExtra(item, respItemKnown...))
		for k, p := range rawList(rawField(item, "content")) {
			sameExtra(t, fmt.Sprintf("responses item %d part %d", j, k), rr.Input[j].Content.Parts[k].Extra, legacyExtra(p, respPartKnown...))
		}
	}
	for j, tool := range rawList(rawField([]byte(respBody), "tools")) {
		sameExtra(t, fmt.Sprintf("responses tool %d", j), rr.Tools[j].Extra, legacyExtra(tool, respToolKnown...))
	}
}

// Mapped fields read as encoding/json reads them, escapes, numbers, and nulls included
func TestSinglePassMappedFields(t *testing.T) {
	body := `{"model":"mé😀\"x","temperature":0.25,"top_p":null,"top_k":40,"max_tokens":1e2,"max_completion_tokens":50,"n":3,"stop":"END","stream":true,"stream_options":{"include_usage":true},
		"messages":[{"role":"user","content":"line\none\ttab \\ back \/ slash \u0001"},{"role":"assistant","content":[{"type":"text","text":"a"},{"type":"text","text":"b"}],"tool_calls":[{"index":0,"id":"c","type":"function","function":{"name":"f","arguments":{"x":1}}}]}],
		"tool_choice":"required"}`
	req, err := (openai{}).Parse(OpenAIChat, []byte(body))
	if err != nil {
		t.Fatal(err)
	}
	var model string
	json.Unmarshal(rawField([]byte(body), "model"), &model)
	if req.Model != model || *req.Temperature != 0.25 || req.TopP != nil || *req.TopK != 40 || req.MaxTokens != 100 || req.N != 3 || strings.Join(req.Stop, ",") != "END" || !req.Stream || !req.IncludeUsage || req.ToolChoice != "required" {
		t.Fatalf("request %+v", req)
	}
	var text string
	json.Unmarshal(rawField(rawList(rawField([]byte(body), "messages"))[0], "content"), &text)
	if TextOf(req.Messages[0].Parts) != text || TextOf(req.Messages[1].Parts) != "ab" || req.Messages[1].ToolCalls[0].Args != `{"x":1}` {
		t.Fatalf("messages %+v", req.Messages)
	}
	if req.Origin == nil || req.Origin.Path != OpenAIChat || string(req.Origin.Body) != body {
		t.Fatalf("origin %+v", req.Origin)
	}
	for bad, want := range map[string]string{
		`{"model":5}`:                                             "want a string",
		`{"model":"m","max_tokens":1.5}`:                          "whole number",
		`{"model":"m","messages":[{"role":}]}`:                    "want a string",
		`{"model":"m"} x`:                                         "unexpected data",
		`{"model":"m","messages":[{"role":"user","content":{}}]}`: "string or a list of parts",
		`{"model":"m","stream":"yes"}`:                            "true or false",
		`null`:                                                    "must be a JSON object",
		` [1]`:                                                    "must be a JSON object",
	} {
		if _, err := (openai{}).Parse(OpenAIChat, []byte(bad)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: want %q, got %v", bad, want, err)
		}
	}
}

// Keys match exactly as the protocols spell them, a differently cased key staying an unmapped member
func TestSinglePassKeysMatchExactly(t *testing.T) {
	req, err := (openai{}).Parse(OpenAIChat, []byte(`{"model":"m","Model":"other","messages":[]}`))
	if err != nil || req.Model != "m" || string(req.Extra["Model"]) != `"other"` {
		t.Fatalf("%+v %v", req, err)
	}
}

// The scanner accepts exactly what encoding/json accepts
func FuzzScanValid(f *testing.F) {
	for _, seed := range []string{`{}`, `[]`, `""`, `0`, `-0.5e+3`, `01`, `1.`, `.5`, `"\u00zz"`, `"\ud800"`, "\"\x01\"", `{"a":1,}`, `[1,]`, ` null `, `nul`, `{"a" 1}`, `"\/"`, `[[[[]]]]`, `{"a":{"b":[true,false,null]}}`, "\"\xff\""} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		err := decode(b, func(s *scan) error { return s.skip() })
		if (err == nil) != json.Valid(b) {
			t.Fatalf("%q: scanner error %v, json.Valid %v", b, err, json.Valid(b))
		}
	})
}

// Strings decode as encoding/json decodes them, bad UTF-8 and lone surrogates included
func FuzzScanString(f *testing.F) {
	for _, seed := range []string{`"plain"`, `"\n\t\\\"\/"`, `"é😀"`, `"\ud800x"`, `"\udc00\ud800"`, "\"\xff\xfe\"", `"é"`, `"\u0000"`} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		var want string
		if json.Unmarshal(b, &want) != nil {
			return
		}
		var got string
		if err := decode(b, func(s *scan) error {
			var err error
			got, err = s.str()
			return err
		}); err != nil || got != want {
			t.Fatalf("%q: got %q %v, want %q", b, got, err, want)
		}
	})
}

// Strings encode to JSON that reads back as encoding/json's own encoding reads back
func FuzzAppendStr(f *testing.F) {
	for _, seed := range []string{"plain", "quote\" back\\ nl\n tab\t ctl\x01", "  ", "\xff bad", "é😀", "<&>"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, s string) {
		var got, want string
		if err := json.Unmarshal(appendStr(nil, s), &got); err != nil {
			t.Fatalf("%q encodes to bad JSON: %v", s, err)
		}
		ref, _ := json.Marshal(s)
		json.Unmarshal(ref, &want)
		if got != want || (utf8.ValidString(s) && got != s) {
			t.Fatalf("%q reads back as %q, want %q", s, got, want)
		}
	})
}

// Rendered bodies parse back to the same request, nothing the client sent dropped on the way
func TestRenderRoundTrip(t *testing.T) {
	body := `{"model":"m","temperature":0.5,"top_k":3,"n":2,"stop":["a","b"],"seed":9,"messages":[{"role":"user","content":[{"type":"text","text":"x","cache_control":{"type":"ephemeral"}},{"type":"image_url","image_url":{"url":"https://e/i.png","detail":"low"}}],"name":"me"}],"tools":[{"type":"function","function":{"name":"f","description":"d","parameters":{"type":"object"},"strict":true}}],"tool_choice":{"type":"function","function":{"name":"f"}}}`
	req, err := (openai{}).Parse(OpenAIChat, []byte(body))
	if err != nil {
		t.Fatal(err)
	}
	_, out, err := (openai{}).Render(req)
	if err != nil {
		t.Fatal(err)
	}
	var sent, want any
	json.Unmarshal(out, &sent)
	json.Unmarshal([]byte(body), &want)
	got, _ := json.Marshal(sent)
	exp, _ := json.Marshal(want)
	if string(got) != string(exp) {
		t.Fatalf("rendered\n%s\nwant\n%s", got, exp)
	}
}

// Every client protocol parses any body without panicking, and names the model the body parser reads
func FuzzClientParse(f *testing.F) {
	for _, seed := range []string{
		`{"model":"m","messages":[{"role":"user","content":"hi"}]}`,
		`{"model":"m","messages":[{"role":"user","content":[{"type":"text","text":"a"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AA=="}}]}],"tools":[{"type":"function","function":{"name":"f","parameters":{}}}]}`,
		`{"model":"m","prompt":"hi","max_tokens":3,"stream":true}`,
		`{"model":"m","prompt":[1,2,3]}`,
		`{"model":"m","input":["a","b"]}`,
		`{"model":"m","input":"hi","instructions":"be brief"}`,
		`{"model":"m","max_tokens":5,"system":"s","messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`,
		`{"model":"m","messages":[{"role":"user","content":"hi","images":["AA=="]}],"options":{"num_predict":4}}`,
		`{"model":"m","prompt":"hi","raw":true}`,
		`{"model":"","messages":[]}`, `{"model":1}`, `{}`, `[]`, `{"messages":[{"role":"user","content":null}]}`, "\xff", `{"model":"m","messages":[{"role":"user","content":"\ud800"}]}`,
	} {
		for i := range 10 {
			f.Add(i, []byte(seed))
		}
	}
	paths := []struct {
		api  v1.ApiFlavor
		path string
		kind bodyparse.Kind
	}{
		{v1.ApiFlavor_API_FLAVOR_OPENAI, OpenAIChat, bodyparse.KindChatCompletions},
		{v1.ApiFlavor_API_FLAVOR_OPENAI, OpenAICompletions, bodyparse.KindCompletions},
		{v1.ApiFlavor_API_FLAVOR_OPENAI, OpenAIEmbeddings, bodyparse.KindEmbeddings},
		{v1.ApiFlavor_API_FLAVOR_OPENAI, OpenAIResponses, bodyparse.KindResponses},
		{v1.ApiFlavor_API_FLAVOR_ANTHROPIC, AnthropicMessages, bodyparse.KindAnthropicMessages},
		{v1.ApiFlavor_API_FLAVOR_ANTHROPIC, AnthropicCount, bodyparse.KindAnthropicMessages},
		{v1.ApiFlavor_API_FLAVOR_OLLAMA, OllamaChat, bodyparse.KindOllamaChat},
		{v1.ApiFlavor_API_FLAVOR_OLLAMA, OllamaGenerate, bodyparse.KindOllamaGenerate},
		{v1.ApiFlavor_API_FLAVOR_OLLAMA, OllamaEmbed, bodyparse.KindEmbeddings},
		{v1.ApiFlavor_API_FLAVOR_OLLAMA, OllamaEmbeddings, bodyparse.KindEmbeddings},
	}
	f.Fuzz(func(t *testing.T, which int, body []byte) {
		if which < 0 {
			which = -which
		}
		p := paths[which%len(paths)]
		req, err := ClientOf(p.api).Parse(p.path, body)
		if err != nil {
			return
		}
		if req == nil {
			t.Fatalf("%s parsed %q to a nil request without an error", p.path, body)
		}
		doc, derr := bodyparse.Parse(p.kind, body)
		if derr != nil {
			t.Fatalf("%s accepted %q which the body parser refuses: %v", p.path, body, derr)
		}
		want, merr := doc.Model()
		if merr != nil {
			want = ""
		}
		if req.Model != want {
			t.Fatalf("%s parsed model %q from %q, body parser read %q", p.path, req.Model, body, want)
		}
	})
}
