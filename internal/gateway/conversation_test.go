package gateway

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/nickheyer/nebu/internal/chat"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// A body the cap cut short closes into JSON holding everything whole before the cut and a cut string's text
func TestMendClosesCutBodies(t *testing.T) {
	for cut, want := range map[string]string{
		`{"model":"m","messages":[{"role":"user","content":"hello wor`: `{"model":"m","messages":[{"role":"user","content":"hello wor"}]}`,
		`{"a":1,"b`:              `{"a":1}`,
		`{"a":1,"b":`:            `{"a":1}`,
		`{"a":[1,2,{"c":tru`:     `{"a":[1,2,{}]}`,
		`{"a":12`:                `{}`,
		`{"a":"x\`:               `{"a":"x"}`,
		`{"a":"x\\`:              `{"a":"x\\"}`,
		`{"a":"x\u00`:            `{"a":"x"}`,
		`{"a":"xé`:               `{"a":"xé"}`,
		`{"a":{"b":[]},"c":"d"}`: `{"a":{"b":[]},"c":"d"}`,
		`[{"a":"b"},`:            `[{"a":"b"}]`,
	} {
		var got string
		mend([]byte(cut), func(body []byte) bool {
			got = string(body)
			return true
		})
		if got != want {
			t.Errorf("%s mended to %s, want %s", cut, got, want)
		}
		if !json.Valid([]byte(got)) {
			t.Errorf("%s mended to invalid JSON %s", cut, got)
		}
	}
	var offered []string
	mend([]byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hi"},{"type":"tool_res`), func(body []byte) bool {
		offered = append(offered, string(body))
		return false
	})
	want := []string{
		`{"messages":[{"role":"user","content":[{"type":"text","text":"hi"},{"type":"tool_res"}]}]}`,
		`{"messages":[{"role":"user","content":[{"type":"text","text":"hi"},{}]}]}`,
		`{"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`,
		`{"messages":[{"role":"user"}]}`,
		`{"messages":[]}`,
		`{}`,
	}
	if !slices.Equal(offered, want) {
		t.Errorf("offered %q", offered)
	}
}

// A trace's request reads as its turns, calls, results and tools, a capped one up to where it was cut
func TestConversationReadsTheRequest(t *testing.T) {
	body := `{"model":"m","max_tokens":8,"system":"be brief","messages":[
		{"role":"user","content":[{"type":"text","text":"read it"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAAA"}}]},
		{"role":"assistant","content":[{"type":"thinking","thinking":"hm","signature":"s"},{"type":"tool_use","id":"t1","name":"Read","input":{"path":"a.go"}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"package a"}]}],
		"tools":[{"name":"Read","description":"Reads a file","input_schema":{"type":"object"}}]}`
	tr := &v1.Trace{ClientApi: v1.ApiFlavor_API_FLAVOR_ANTHROPIC, Path: chat.AnthropicMessages, Request: body, RequestBytes: uint64(len(body))}
	c := Conversation(tr)
	if c == nil || len(c.GetTurns()) != 4 || len(c.GetTools()) != 1 {
		t.Fatalf("conversation %v", c)
	}
	turns := c.GetTurns()
	if turns[0].GetRole() != "system" || turns[1].GetParts()[1].GetType() != "image" || turns[1].GetParts()[1].GetSize() != 3 || turns[1].GetParts()[1].GetMediaType() != "image/png" {
		t.Errorf("first turns %v %v", turns[0], turns[1])
	}
	if call := turns[2].GetCalls()[0]; call.GetId() != "t1" || call.GetName() != "Read" || call.GetArguments() != `{"path":"a.go"}` || turns[2].GetParts()[0].GetType() != "thinking" {
		t.Errorf("assistant turn %v", turns[2])
	}
	if turns[3].GetRole() != "tool" || turns[3].GetToolCallId() != "t1" || turns[3].GetParts()[0].GetText() != "package a" {
		t.Errorf("tool turn %v", turns[3])
	}
	if tool := c.GetTools()[0]; tool.GetName() != "Read" || tool.GetSchema() != `{"type":"object"}` {
		t.Errorf("tool %v", tool)
	}
	cut := body[:len(body)-150]
	capped := Conversation(&v1.Trace{ClientApi: v1.ApiFlavor_API_FLAVOR_ANTHROPIC, Path: chat.AnthropicMessages, Request: cut, RequestBytes: uint64(len(body))})
	if capped == nil || len(capped.GetTurns()) < 3 {
		t.Fatalf("capped conversation %v", capped)
	}
	if c := Conversation(&v1.Trace{ClientApi: v1.ApiFlavor_API_FLAVOR_OPENAI, Path: "/v1/images/generations", Request: `{"prompt":"a cat"}`}); c != nil {
		t.Errorf("an image request read as turns %v", c)
	}
	if c := Conversation(&v1.Trace{ClientApi: v1.ApiFlavor_API_FLAVOR_OPENAI, Path: chat.OpenAIChat, Request: `not json`}); c != nil {
		t.Errorf("a refused body read as turns %v", c)
	}
}
