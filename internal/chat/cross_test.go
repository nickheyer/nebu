package chat

import (
	"encoding/json"
	"strings"
	"testing"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// The body an anthropic request reaches a chat completions runtime as
func crossed(t *testing.T, body string) (map[string]json.RawMessage, []string, []string) {
	t.Helper()
	req, err := (anthropic{}).Parse(AnthropicMessages, []byte(body))
	if err != nil {
		t.Fatal(err)
	}
	Cross(req, v1.ApiFlavor_API_FLAVOR_ANTHROPIC)
	_, out, err := (openai{}).Render(req)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "cache_control") {
		t.Fatalf("cache_control reached the runtime: %s", out)
	}
	var sent map[string]json.RawMessage
	if err := json.Unmarshal(out, &sent); err != nil {
		t.Fatal(err)
	}
	var messages []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(sent["messages"], &messages); err != nil {
		t.Fatal(err)
	}
	var roles, contents []string
	for _, m := range messages {
		roles = append(roles, m.Role)
		contents = append(contents, string(m.Content))
	}
	return sent, roles, contents
}

// A coding agent's reminder sent as a later system turn reaches the runtime as a user turn with its text unchanged
func TestCrossLateSystem(t *testing.T) {
	sent, roles, contents := crossed(t, `{"model":"m","max_tokens":64,"stream":true,
		"system":[{"type":"text","text":"You are an agent.","cache_control":{"type":"ephemeral"}}],
		"messages":[
			{"role":"user","content":[{"type":"text","text":"say hi","cache_control":{"type":"ephemeral"}}]},
			{"role":"system","content":[{"type":"text","text":"# Environment\nPlatform linux"}]},
			{"role":"assistant","content":"Hi."},
			{"role":"user","content":"say bye"}],
		"tools":[{"name":"Read","input_schema":{"type":"object"},"cache_control":{"type":"ephemeral"}}],
		"thinking":{"type":"adaptive","display":"updates"},"output_config":{"effort":"xhigh"},
		"context_management":{"edits":[]},"safeguards":{},"metadata":{"user_id":"u-1"},"min_p":0.05}`)
	if got := strings.Join(roles, ","); got != "system,user,user,assistant,user" {
		t.Fatalf("roles %s", got)
	}
	if contents[0] != `"You are an agent."` || contents[2] != `"# Environment\nPlatform linux"` {
		t.Fatalf("contents %q", contents)
	}
	for _, k := range antOnly {
		if _, ok := sent[k]; ok {
			t.Fatalf("%s reached the runtime", k)
		}
	}
	if string(sent["reasoning_effort"]) != `"xhigh"` || string(sent["user"]) != `"u-1"` || string(sent["min_p"]) != "0.05" {
		t.Fatalf("effort %s user %s min_p %s", sent["reasoning_effort"], sent["user"], sent["min_p"])
	}
}

// A later system turn waits behind the tool results it was sent ahead of, and a blank one is left out
func TestCrossSystemPlacement(t *testing.T) {
	_, roles, contents := crossed(t, `{"model":"m","max_tokens":64,"system":"base","messages":[
		{"role":"system","content":"permissions"},
		{"role":"user","content":"inspect"},
		{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Read","input":{}},{"type":"tool_use","id":"t2","name":"Read","input":{}}]},
		{"role":"system","content":"keep the policy"},
		{"role":"system","content":[{"type":"text","text":"  "}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"one"},{"type":"tool_result","tool_use_id":"t2","content":"two"}]},
		{"role":"system","content":"last"}]}`)
	if got := strings.Join(roles, ","); got != "system,user,user,assistant,tool,tool,user,user" {
		t.Fatalf("roles %s", got)
	}
	if contents[1] != `"permissions"` || contents[6] != `"keep the policy"` || contents[7] != `"last"` {
		t.Fatalf("contents %q", contents)
	}
	_, roles, _ = crossed(t, `{"model":"m","max_tokens":64,"messages":[{"role":"system","content":"first"},{"role":"system","content":"second"},{"role":"user","content":"hi"}]}`)
	if got := strings.Join(roles, ","); got != "system,user,user" {
		t.Fatalf("roles without a top level system %s", got)
	}
}

// Thinking and output settings become the effort and format a chat completions runtime reads
func TestCrossEffortAndFormat(t *testing.T) {
	for settings, want := range map[string]string{
		`"thinking":{"type":"disabled"},"output_config":{"effort":"high"}`: `"none"`,
		`"thinking":{"type":"adaptive"}`:                                   `"high"`,
		`"thinking":{"type":"adaptive"},"output_config":{"effort":"low"}`:  `"low"`,
		`"output_config":{"effort":"max"}`:                                 `"max"`,
		`"thinking":{"type":"enabled","budget_tokens":512}`:                `"minimal"`,
		`"thinking":{"type":"enabled","budget_tokens":1024}`:               `"low"`,
		`"thinking":{"type":"enabled","budget_tokens":2048}`:               `"medium"`,
		`"thinking":{"type":"enabled","budget_tokens":4096}`:               `"high"`,
		`"top_k":4`: ``,
	} {
		sent, _, _ := crossed(t, `{"model":"m","max_tokens":64,"messages":[{"role":"user","content":"hi"}],`+settings+`}`)
		if got := string(sent["reasoning_effort"]); got != want {
			t.Fatalf("%s gave effort %s, want %s", settings, got, want)
		}
	}
	const schema = `{"type":"object","properties":{"title":{"type":"string"}},"required":["title"]}`
	for settings, want := range map[string]string{
		`"output_config":{"effort":"high","format":{"type":"json_schema","schema":` + schema + `}}`:                                                   schema,
		`"output_format":{"type":"json_schema","schema":{"type":"string"}},"output_config":{"format":{"type":"json_schema","schema":` + schema + `}}`: `{"type":"string"}`,
	} {
		sent, _, _ := crossed(t, `{"model":"m","max_tokens":64,"messages":[{"role":"user","content":"hi"}],`+settings+`}`)
		if got := string(sent["response_format"]); got != `{"type":"json_schema","json_schema":{"name":"structured_output","schema":`+want+`}}` {
			t.Fatalf("%s gave format %s", settings, got)
		}
	}
	sent, _, _ := crossed(t, `{"model":"m","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`)
	if _, ok := sent["response_format"]; ok {
		t.Fatalf("a request without an output format gave %s", sent["response_format"])
	}
}

// A request already in the runtime's family keeps its turns and fields as the client wrote them
func TestCrossOtherProtocols(t *testing.T) {
	req, err := (openai{}).Parse(OpenAIChat, []byte(`{"model":"m","messages":[{"role":"user","content":"hi"},{"role":"system","content":"late"}],"metadata":{"user_id":"u"}}`))
	if err != nil {
		t.Fatal(err)
	}
	Cross(req, v1.ApiFlavor_API_FLAVOR_OPENAI)
	if req.Messages[1].Role != "system" || req.Extra["metadata"] == nil {
		t.Fatalf("messages %+v extra %v", req.Messages, req.Extra)
	}
}

// The billing header leaves a system turn whether it is a block of its own, a first line, or runs into the prompt
func TestStripBillingHeader(t *testing.T) {
	header := "x-anthropic-billing-header: cc_version=2.1.101.e51; cc_entrypoint=cli; cch=a5145;"
	for name, tc := range map[string]struct {
		parts []string
		want  []string
	}{
		"own block":  {parts: []string{header, "You are an agent."}, want: []string{"You are an agent."}},
		"first line": {parts: []string{header + "\nYou are an agent."}, want: []string{"You are an agent."}},
		"same line":  {parts: []string{header + "You are an agent.\nBe brief."}, want: []string{"You are an agent.\nBe brief."}},
		"no header":  {parts: []string{"You are an agent."}, want: []string{"You are an agent."}},
	} {
		req := &Request{Messages: []Message{{Role: "system"}, {Role: "user", Parts: []Part{{Type: "text", Text: header}}}}}
		for _, text := range tc.parts {
			req.Messages[0].Parts = append(req.Messages[0].Parts, Part{Type: "text", Text: text})
		}
		StripBillingHeader(req)
		var got []string
		for _, p := range req.Messages[0].Parts {
			got = append(got, p.Text)
		}
		if strings.Join(got, "|") != strings.Join(tc.want, "|") {
			t.Errorf("%s: system parts = %q, want %q", name, got, tc.want)
		}
		if last := req.Messages[len(req.Messages)-1]; last.Role != "user" || last.Parts[0].Text != header {
			t.Errorf("%s: a user turn quoting the header changed: %+v", name, last)
		}
	}
	only := &Request{Messages: []Message{{Role: "system", Parts: []Part{{Type: "text", Text: header}}}, {Role: "user"}}}
	StripBillingHeader(only)
	if len(only.Messages) != 1 || only.Messages[0].Role != "user" {
		t.Errorf("a system turn holding the header alone stayed: %+v", only.Messages)
	}
}
