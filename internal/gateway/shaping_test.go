package gateway

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/proto"
)

// An Anthropic request the way a coding agent sends one, with a system prompt and unmapped fields
const agentRequest = `{"model":"m","max_tokens":50,"temperature":1,"metadata":{"user_id":"u"},"thinking":{"type":"adaptive"},"system":[{"type":"text","text":"x-anthropic-billing-header"},{"type":"text","text":"You are a cautious agent.","cache_control":{"type":"ephemeral"}}],"messages":[{"role":"user","content":[{"type":"text","text":"hi","cache_control":{"type":"ephemeral"}}]}],"tools":[{"name":"Read","description":"Reads","input_schema":{"type":"object"},"input_examples":[{"path":"a"}]}]}`

// The system text the runtime got, empty when it got no system turn
func systemSent(t *testing.T, body map[string]any) string {
	t.Helper()
	msgs, _ := body["messages"].([]any)
	if len(msgs) == 0 {
		return ""
	}
	first := msgs[0].(map[string]any)
	if first["role"] != "system" {
		return ""
	}
	switch c := first["content"].(type) {
	case string:
		return c
	case []any:
		var out []string
		for _, p := range c {
			out = append(out, p.(map[string]any)["text"].(string))
		}
		return strings.Join(out, "|")
	}
	return ""
}

// Whether the attribution block, unmapped fields, and identity headers all reached the runtime
func extrasReached(t *testing.T, call seatCall) bool {
	t.Helper()
	raw, _ := json.Marshal(call.body)
	_, metadata := call.body["metadata"]
	attribution := strings.Contains(systemSent(t, call.body), attributionMarker)
	header := call.header.Get("X-Claude-Code-Session-Id") != ""
	fields := strings.Contains(string(raw), "cache_control") || strings.Contains(string(raw), "input_examples")
	if metadata != attribution || attribution != header || header != fields {
		t.Fatalf("extras reached the runtime in part: metadata %v attribution %v header %v fields %v", metadata, attribution, header, fields)
	}
	return metadata
}

// A coding agent's extras and system prompt stay out by default, and a profile passes, keeps,
// drops, replaces, or overrides
func TestProfileShaping(t *testing.T) {
	model := newSeat(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) { chatAnswer(w, "ok") })
	headers := map[string]string{"X-Claude-Code-Session-Id": "s1", "anthropic-beta": "x"}
	for name, tc := range map[string]struct {
		profile *v1.Profile
		check   func(t *testing.T, call seatCall)
	}{
		"default": {nil, func(t *testing.T, call seatCall) {
			if extrasReached(t, call) {
				t.Fatal("the client's extras reached the runtime by default")
			}
			if got := systemSent(t, call.body); got != factsOpening {
				t.Fatalf("system %q", got)
			}
		}},
		"pass": {&v1.Profile{StripExtras: proto.Bool(false), SystemPrompt: v1.SystemPrompt_SYSTEM_PROMPT_KEEP}, func(t *testing.T, call seatCall) {
			if !extrasReached(t, call) {
				t.Fatal("a profile that passes extras lost them")
			}
			if got := systemSent(t, call.body); got != attributionMarker+"|You are a cautious agent." {
				t.Fatalf("system %q", got)
			}
		}},
		"strip": {&v1.Profile{StripExtras: proto.Bool(true), SystemPrompt: v1.SystemPrompt_SYSTEM_PROMPT_KEEP}, func(t *testing.T, call seatCall) {
			if extrasReached(t, call) {
				t.Fatal("a profile that strips extras passed them")
			}
			if got := systemSent(t, call.body); got != "You are a cautious agent." {
				t.Fatalf("system %q", got)
			}
		}},
		"drop": {&v1.Profile{SystemPrompt: v1.SystemPrompt_SYSTEM_PROMPT_DROP}, func(t *testing.T, call seatCall) {
			if got := systemSent(t, call.body); got != "" {
				t.Fatalf("system %q reached the runtime", got)
			}
		}},
		"replace": {&v1.Profile{SystemPrompt: v1.SystemPrompt_SYSTEM_PROMPT_REPLACE, SystemText: "Be terse."}, func(t *testing.T, call seatCall) {
			if got := systemSent(t, call.body); got != "Be terse." {
				t.Fatalf("system %q", got)
			}
		}},
		"overrides": {&v1.Profile{Overrides: `{"temperature":0.6,"chat_template_kwargs":{"enable_thinking":false}}`}, func(t *testing.T, call seatCall) {
			sent := call.body
			if sent["temperature"] != 0.6 || sent["chat_template_kwargs"].(map[string]any)["enable_thinking"] != false {
				t.Fatalf("overrides did not win: %v %v", sent["temperature"], sent["chat_template_kwargs"])
			}
			if sent["model"] != "m" || len(sent["messages"].([]any)) != 2 {
				t.Fatalf("overrides changed more than their fields: %v", sent)
			}
		}},
	} {
		t.Run(name, func(t *testing.T) {
			g, base := seatGateway(t, model, tc.profile)
			before := model.count()
			r, trace := ask(t, g, base, http.MethodPost, anthropicMessages, agentRequest, headers)
			if r.status != http.StatusOK {
				t.Fatalf("status %d: %s", r.status, r.body)
			}
			tc.check(t, model.call(before))
			if !trace.GetTranslated() {
				t.Fatal("a shaped request is recorded as translated")
			}
		})
	}
}

// An OpenAI client's extras are the runtime's own and pass by default, a profile stripping them
func TestOpenAIExtrasPassByDefault(t *testing.T) {
	model := newSeat(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) { chatAnswer(w, "ok") })
	body := `{"model":"m","min_p":0.05,"messages":[{"role":"user","content":"hi"}]}`
	headers := map[string]string{"anthropic-beta": "x"}
	g, base := seatGateway(t, model, nil)
	if r, trace := ask(t, g, base, http.MethodPost, openaiChat, body, headers); r.status != http.StatusOK || trace.GetTranslated() {
		t.Fatalf("status %d translated %v: %s", r.status, trace.GetTranslated(), r.body)
	}
	if call := model.call(0); call.body["min_p"] != 0.05 || call.header.Get("Anthropic-Beta") != "x" {
		t.Fatalf("an OpenAI client's extras were stripped by default: %v %v", call.body, call.header)
	}
	g, base = seatGateway(t, model, &v1.Profile{StripExtras: proto.Bool(true)})
	if r, _ := ask(t, g, base, http.MethodPost, openaiChat, body, headers); r.status != http.StatusOK {
		t.Fatalf("status %d: %s", r.status, r.body)
	}
	if call := model.call(1); call.body["min_p"] != nil || call.header.Get("Anthropic-Beta") != "" {
		t.Fatalf("a profile that strips extras passed them: %v %v", call.body, call.header)
	}
}

// A same format request on a shaped route is rendered rather than passed through
func TestProfileShapesPassthrough(t *testing.T) {
	model := newSeat(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) { chatAnswer(w, "ok") })
	g, base := seatGateway(t, model, &v1.Profile{SystemPrompt: v1.SystemPrompt_SYSTEM_PROMPT_DROP, Overrides: `{"top_p":0.9}`})
	r, trace := ask(t, g, base, http.MethodPost, openaiChat, chatBody, nil)
	if r.status != http.StatusOK {
		t.Fatalf("status %d: %s", r.status, r.body)
	}
	sent := model.call(0).body
	if got := systemSent(t, sent); got != "" || sent["top_p"] != 0.9 {
		t.Fatalf("system %q top_p %v", got, sent["top_p"])
	}
	if !trace.GetTranslated() || !json.Valid([]byte(trace.GetUpstreamRequest())) {
		t.Fatalf("trace translated %v upstream %q", trace.GetTranslated(), trace.GetUpstreamRequest())
	}
	msg, _ := oaiAnswer(t, r.body)
	if msg["content"] != "ok" {
		t.Fatalf("answer %v", msg)
	}
}

// Overrides reach the runtime on every round of a tool loop
func TestOverridesInToolLoop(t *testing.T) {
	searx := searxngWith(t, kittenPages[0])
	model := newSeat(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		if body["min_p"] != 0.05 {
			http.Error(w, "min_p missing", http.StatusBadRequest)
			return
		}
		if hasToolTurn(body) {
			chatAnswer(w, "Found them.")
			return
		}
		toolAnswer(w, "Looking.", searchCall("call_1", "kittens"))
	})
	g, table, base := toolGateway(t, model, searxngServer(searx))
	table.Set("m", "i1", "", model.URL, "r:g", "m", v1.ApiFlavor_API_FLAVOR_OPENAI, nil, &v1.Profile{Overrides: `{"min_p":0.05}`})
	r, _ := ask(t, g, base, http.MethodPost, anthropicMessages, searchBody(false, kittenTool), nil)
	if r.status != http.StatusOK || model.count() != 2 {
		t.Fatalf("status %d after %d calls: %s", r.status, model.count(), r.body)
	}
}

// An alias profile wins over its slot's field by field, and unusable shaping is refused
func TestProfileMergeAndValidation(t *testing.T) {
	slot := &v1.Profile{SystemPrompt: v1.SystemPrompt_SYSTEM_PROMPT_REPLACE, SystemText: "slot", StripExtras: proto.Bool(true), AllowAll: proto.Bool(true), Overrides: `{"a":1}`, ToolServers: []string{"web"}}
	if got := EffectiveProfile(nil, slot); got != slot {
		t.Fatal("an empty alias profile inherits the slot's whole")
	}
	got := EffectiveProfile(&v1.Profile{SystemPrompt: v1.SystemPrompt_SYSTEM_PROMPT_DROP, Overrides: `{"b":2}`, AllowAll: proto.Bool(false)}, slot)
	want := &v1.Profile{SystemPrompt: v1.SystemPrompt_SYSTEM_PROMPT_DROP, StripExtras: proto.Bool(true), AllowAll: proto.Bool(false), Overrides: `{"b":2}`, ToolServers: []string{"web"}}
	if !proto.Equal(got, want) {
		t.Fatalf("merged %v\nwant %v", got, want)
	}
	if !AllowsAll(nil) || AllowsAll(got) || !StripsExtras(nil, v1.ApiFlavor_API_FLAVOR_ANTHROPIC) || StripsExtras(nil, v1.ApiFlavor_API_FLAVOR_OPENAI) || !StripsExtras(got, v1.ApiFlavor_API_FLAVOR_OPENAI) {
		t.Fatal("the defaults allow checks and strip an Anthropic client's extras, a set field winning")
	}
	for _, tc := range []struct {
		p  *v1.Profile
		ok bool
	}{
		{nil, true},
		{&v1.Profile{SystemPrompt: v1.SystemPrompt_SYSTEM_PROMPT_REPLACE, SystemText: "x"}, true},
		{&v1.Profile{SystemPrompt: v1.SystemPrompt_SYSTEM_PROMPT_REPLACE}, false},
		{&v1.Profile{SystemPrompt: v1.SystemPrompt_SYSTEM_PROMPT_DROP, SystemText: "x"}, false},
		{&v1.Profile{Overrides: `{"temperature":0.5}`}, true},
		{&v1.Profile{Overrides: `[1]`}, false},
		{&v1.Profile{Overrides: `nope`}, false},
	} {
		if err := ValidateProfile(tc.p); (err == nil) != tc.ok {
			t.Fatalf("%v: ok %v, err %v", tc.p, tc.ok, err)
		}
	}
}

// The request Claude Code sends under server side review, with the check it asks for
func reviewBody(stream bool) string {
	return fmt.Sprintf(`{"model":"m","max_tokens":50,"stream":%v,"messages":[{"role":"user","content":"list the dir"}],"tools":[{"name":"Bash","description":"Runs","input_schema":{"type":"object"}}],"safeguards":[{"type":"dangerous_tool_use","classifier_context":{"v":1,"permission_mode":"auto","live_cwd":"/w","trusted_directories":["/w"]}}]}`, stream)
}

// A route answers server side review as not flagged by default, streamed or whole, without the runtime
func TestServerSideReview(t *testing.T) {
	model := newSeat(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		if _, has := body["safeguards"]; has {
			http.Error(w, "safeguards reached the runtime", http.StatusBadRequest)
			return
		}
		if body["stream"] == true {
			searchStream(w, "ls")
			return
		}
		toolAnswer(w, "Running.", toolCall("call_9", "Bash", `{"command":"ls"}`))
	})
	g, base := seatGateway(t, model, nil)
	headers := map[string]string{"anthropic-beta": "dangerous-tool-use-2026-09-03"}
	r, _ := ask(t, g, base, http.MethodPost, anthropicMessages, reviewBody(false), headers)
	if r.status != http.StatusOK {
		t.Fatalf("status %d: %s", r.status, r.body)
	}
	var reply struct {
		SafeguardResults []antSafeguardResult `json:"safeguard_results"`
	}
	json.Unmarshal(r.body, &reply)
	if len(reply.SafeguardResults) != 1 || reply.SafeguardResults[0].Type != dangerousToolUse || reply.SafeguardResults[0].Status.Type != "available" {
		t.Fatalf("results %s", r.body)
	}
	if v := reply.SafeguardResults[0].Status.ToolUses["call_9"]; v.Type != "evaluated" || v.Outcome != "not_flagged" || len(reply.SafeguardResults[0].Status.ToolUses) != 1 {
		t.Fatalf("verdicts %v", reply.SafeguardResults[0].Status.ToolUses)
	}
	r, _ = ask(t, g, base, http.MethodPost, anthropicMessages, reviewBody(true), headers)
	if r.status != http.StatusOK {
		t.Fatalf("status %d: %s", r.status, r.body)
	}
	events := eventsOf(t, r.body)
	var toolID string
	var results []antSafeguardResult
	for _, ev := range events {
		switch ev["type"] {
		case "content_block_start":
			if block := ev["content_block"].(map[string]any); block["type"] == "tool_use" {
				toolID, _ = block["id"].(string)
			}
		case "message_delta":
			raw, _ := json.Marshal(ev["delta"].(map[string]any)["safeguard_results"])
			json.Unmarshal(raw, &results)
		}
	}
	if toolID == "" || len(results) != 1 || results[0].Status.ToolUses[toolID].Outcome != "not_flagged" {
		t.Fatalf("stream verdicts %v for tool %q", results, toolID)
	}
	// An answer without tool calls still carries the check as answered, so the session stays reviewed
	plain := newSeat(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) { chatAnswer(w, "done") })
	g, base = seatGateway(t, plain, nil)
	r, _ = ask(t, g, base, http.MethodPost, anthropicMessages, reviewBody(false), headers)
	var empty struct {
		SafeguardResults []antSafeguardResult `json:"safeguard_results"`
	}
	json.Unmarshal(r.body, &empty)
	if len(empty.SafeguardResults) != 1 || len(empty.SafeguardResults[0].Status.ToolUses) != 0 {
		t.Fatalf("results without tool calls %s", r.body)
	}
}

// A route whose checks the model answers claims no review, and the check still never reaches an OpenAI runtime
func TestServerSideReviewNotClaimed(t *testing.T) {
	model := newSeat(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		if _, has := body["safeguards"]; has {
			http.Error(w, "safeguards reached the runtime", http.StatusBadRequest)
			return
		}
		toolAnswer(w, "Running.", toolCall("call_9", "Bash", `{"command":"ls"}`))
	})
	g, base := seatGateway(t, model, &v1.Profile{AllowAll: proto.Bool(false)})
	r, _ := ask(t, g, base, http.MethodPost, anthropicMessages, reviewBody(false), map[string]string{"anthropic-beta": "dangerous-tool-use-2026-09-03"})
	if r.status != http.StatusOK {
		t.Fatalf("status %d: %s", r.status, r.body)
	}
	var reply map[string]any
	json.Unmarshal(r.body, &reply)
	if _, has := reply["safeguard_results"]; has {
		t.Fatalf("a route whose checks the model answers claimed review: %s", r.body)
	}
}

// The check goes on to an Anthropic runtime only when the gateway does not answer it
func TestSafeguardsForwardedToAnthropicRuntime(t *testing.T) {
	chat, err := (anthropic{}).ParseRequest(anthropicMessages, []byte(reviewBody(false)))
	if err != nil {
		t.Fatal(err)
	}
	if len(chat.Safeguards) != 1 || chat.Safeguards[0].Type != dangerousToolUse {
		t.Fatalf("parsed %v", chat.Safeguards)
	}
	_, body, err := (anthropic{}).RenderRequest(chat)
	if err != nil || !strings.Contains(string(body), `"safeguards":[{"type":"dangerous_tool_use"`) {
		t.Fatalf("forwarded %s: %v", body, err)
	}
	chat.Allowed = true
	_, body, _ = (anthropic{}).RenderRequest(chat)
	if strings.Contains(string(body), "safeguards") {
		t.Fatalf("an answered check was forwarded: %s", body)
	}
	_, body, _ = (openai{}).RenderRequest(chat)
	if strings.Contains(string(body), "safeguards") {
		t.Fatalf("the check reached an OpenAI body: %s", body)
	}
}

// The identity line and environment part a coding agent harness sends, with a mid conversation system turn
const harnessSystem = `[{"type":"text","text":"You are Claude Code, Anthropic's official CLI for Claude."},{"type":"text","text":"\nYou are an agent working with the user.\n\nIMPORTANT: Refuse requests for destructive techniques.\n\n# Harness\n - Reference code as file_path:line_number."}]`

const harnessEnvironment = "\n\n# Environment\nYou have been invoked in the following environment: \n - Primary working directory: /home/nick/code/x\n - Is a git repository: false\n - Platform: linux\n - Shell: zsh\n - OS Version: Linux 7.2.4-arch1-2\n\nYou are powered by the model freddie.\n\nAvailable agent types for the Agent tool:\n- Explore: Read-only search agent.\n\nThe following skills are available:\n- code-review: Review the diff."

func harnessBody(identity bool) string {
	system := harnessSystem
	if !identity {
		system = `[{"type":"text","text":"You are an agent working with the user."}]`
	}
	return fmt.Sprintf(`{"model":"m","max_tokens":50,"system":%s,"messages":[
		{"role":"user","content":[{"type":"text","text":"<system-reminder>\nContents of CLAUDE.md:\n\nNever commit.\n</system-reminder>"},{"type":"text","text":"fix the bug"}]},
		{"role":"system","content":%q},
		{"role":"assistant","content":"Looking."},
		{"role":"user","content":"go on"}],
		"tools":[{"name":"Bash","description":"Runs","input_schema":{"type":"object"}},{"name":"Artifact","description":"Publishes","input_schema":{"type":"object"}},{"name":"CronCreate","description":"Schedules","input_schema":{"type":"object"}},{"name":"mcp__plugin_x__y","description":"Plugin","input_schema":{"type":"object"}}]}`, system, harnessEnvironment)
}

// A coding agent harness, known by its session header or its identity line, gets the gateway's
// facts prompt by default, nothing else of its system text, and a profile keeps or replaces it
func TestHarnessPromptByDefault(t *testing.T) {
	model := newSeat(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) { chatAnswer(w, "ok") })
	wantFacts := factsOpening + "\nWorking directory: /home/nick/code/x\nGit repository: false\nPlatform: linux\nShell: zsh\nOS version: Linux 7.2.4-arch1-2"
	for name, tc := range map[string]struct {
		body    string
		headers map[string]string
		profile *v1.Profile
		want    string
		// Whether the harness's later system turn still reaches the runtime
		later bool
	}{
		"identity line":   {harnessBody(true), nil, nil, wantFacts, false},
		"session header":  {harnessBody(false), map[string]string{"X-Claude-Code-Session-Id": "s1"}, nil, wantFacts, false},
		"facts by choice": {harnessBody(false), nil, &v1.Profile{SystemPrompt: v1.SystemPrompt_SYSTEM_PROMPT_FACTS}, wantFacts, false},
		"kept by choice":  {harnessBody(true), nil, &v1.Profile{SystemPrompt: v1.SystemPrompt_SYSTEM_PROMPT_KEEP}, "You are Claude Code, Anthropic's official CLI for Claude.|\nYou are an agent working with the user.\n\nIMPORTANT: Refuse requests for destructive techniques.\n\n# Harness\n - Reference code as file_path:line_number.", true},
		"not a harness":   {harnessBody(false), nil, nil, "You are an agent working with the user.", true},
	} {
		t.Run(name, func(t *testing.T) {
			g, base := seatGateway(t, model, tc.profile)
			before := model.count()
			r, _ := ask(t, g, base, http.MethodPost, anthropicMessages, tc.body, tc.headers)
			if r.status != http.StatusOK {
				t.Fatalf("status %d: %s", r.status, r.body)
			}
			sent := model.call(before).body
			if got := systemSent(t, sent); got != tc.want {
				t.Fatalf("system sent:\n%q\nwant:\n%q", got, tc.want)
			}
			msgs := sent["messages"].([]any)
			later := false
			for _, m := range msgs[1:] {
				later = later || m.(map[string]any)["role"] == "system"
			}
			if later != tc.later {
				t.Fatalf("a later system turn reaching the runtime: %v, want %v", later, tc.later)
			}
			if raw, _ := json.Marshal(msgs); !strings.Contains(string(raw), "Never commit.") || !strings.Contains(string(raw), "fix the bug") {
				t.Fatalf("the user's own turns were changed: %s", raw)
			}
			if tc.profile == nil && tc.headers == nil && name != "not a harness" {
				if raw, _ := json.Marshal(sent); strings.Contains(string(raw), "Claude Code") || strings.Contains(string(raw), "freddie") || strings.Contains(string(raw), "Explore") {
					t.Fatalf("harness text reached the runtime: %s", raw)
				}
			}
		})
	}
}

// Hidden tools never reach the runtime, by name or pattern, and the rest are offered as before
func TestHiddenTools(t *testing.T) {
	model := newSeat(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) { chatAnswer(w, "ok") })
	g, base := seatGateway(t, model, &v1.Profile{HiddenTools: []string{"Artifact", "Cron*", "mcp__plugin_*"}})
	r, trace := ask(t, g, base, http.MethodPost, anthropicMessages, harnessBody(true), nil)
	if r.status != http.StatusOK || !trace.GetTranslated() {
		t.Fatalf("status %d translated %v: %s", r.status, trace.GetTranslated(), r.body)
	}
	if names := toolNames(model.call(0).body); strings.Join(names, ",") != "Bash" {
		t.Fatalf("tools offered %v", names)
	}
	if err := ValidateProfile(&v1.Profile{HiddenTools: []string{" "}}); err == nil {
		t.Fatal("an empty pattern was accepted")
	}
	got := EffectiveProfile(&v1.Profile{HiddenTools: []string{"X"}}, &v1.Profile{HiddenTools: []string{"Y"}})
	if strings.Join(got.GetHiddenTools(), ",") != "X" {
		t.Fatalf("alias hidden tools %v", got.GetHiddenTools())
	}
	if !Shaped(&v1.Profile{HiddenTools: []string{"X"}, StripExtras: proto.Bool(false)}, v1.ApiFlavor_API_FLAVOR_OPENAI, false) {
		t.Fatal("hidden tools do not shape")
	}
}
