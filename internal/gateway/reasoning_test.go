package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/nickheyer/nebu/internal/gateway/jinja"
	"github.com/nickheyer/nebu/internal/pools"
	"github.com/nickheyer/nebu/pkg/chattemplate"
	"github.com/nickheyer/nebu/pkg/datalayer"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/transcode/vllm"
)

// The production template, taking xhigh, medium and low with xhigh by default and raising on any other level
func xhighTemplateText(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("../../pkg/chattemplate/testdata/effort_xhigh_medium_low.jinja")
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// A template that always thinks, raises on levels other than low and high, and reads no thinking switch
const alwaysThinkingTemplate = `{%- set e = reasoning_effort | default('high') %}{%- if e not in ('low', 'high') %}{{ raise_exception('Unexpected reasoning effort ' ~ e) }}{%- endif %}{%- for message in messages %}{{ message.content }}{%- endfor %}{{ '<think>' + e }}`

// A llama-server front rendering each body's reasoning settings into the template, answering its 500 when the template raises
type llamaFront struct {
	srv *httptest.Server
	mu  sync.Mutex
	// The prompt the last request rendered to
	prompt string
}

func newLlamaFront(t *testing.T, text string, next http.Handler) *llamaFront {
	t.Helper()
	tpl, err := jinja.Compile(&chattemplate.Template{Text: text, Source: "tokenizer.chat_template"})
	if err != nil {
		t.Fatal(err)
	}
	f := &llamaFront{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body struct {
			Messages []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
			Effort *string        `json:"reasoning_effort"`
			Kwargs map[string]any `json:"chat_template_kwargs"`
		}
		json.Unmarshal(raw, &body)
		vars := map[string]any{"enable_thinking": true}
		for k, v := range body.Kwargs {
			vars[k] = v
		}
		switch {
		case body.Effort == nil, *body.Effort == "":
		case *body.Effort == "none":
			vars["enable_thinking"] = false
			delete(vars, "reasoning_effort")
		default:
			vars["reasoning_effort"], vars["reasoning_strength"] = *body.Effort, *body.Effort
		}
		var msgs []vllm.Message
		for _, m := range body.Messages {
			var text string
			if json.Unmarshal(m.Content, &text) != nil {
				var parts []vllm.ContentPart
				json.Unmarshal(m.Content, &parts)
				for _, p := range parts {
					text += p.Text
				}
			}
			msgs = append(msgs, vllm.Message{Role: m.Role, Content: []vllm.ContentPart{{Type: "text", Text: text}}})
		}
		prompt, err := tpl.With(vars).Render(msgs, nil, true)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			msg, _ := json.Marshal("\n------------\nWhile executing CallExpression\nError: Jinja Exception: " + err.Error())
			io.WriteString(w, `{"error":{"code":500,"message":`+string(msg)+`,"type":"server_error"}}`)
			return
		}
		f.mu.Lock()
		f.prompt = prompt
		f.mu.Unlock()
		r.Body = io.NopCloser(bytes.NewReader(raw))
		next.ServeHTTP(w, r)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// The prompt the last request rendered to
func (f *llamaFront) lastPrompt() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.prompt
}

// Publishes a llama.cpp instance at the front whose endpoint carries the template's reasoning labels, as an instance publishes it
func (h *harness) publishTemplate(route, text string, front *llamaFront) {
	h.t.Helper()
	tpl := &v1.ChatTemplate{}
	chattemplate.Probe(&chattemplate.Template{Text: text, Source: "tokenizer.chat_template"}, tpl)
	labels := map[string]string{}
	chattemplate.SetLabels(labels, tpl)
	addr, err := datalayer.ParseAddress(strings.TrimPrefix(front.srv.URL, "http://"))
	if err != nil {
		h.t.Fatal(err)
	}
	owner := pools.OwnerRef{Kind: pools.OwnerInstance, ID: route + "-" + addr.String()}
	mem := pools.Membership{Model: key("stub"), Runtime: "llamacpp", Protocol: datalayer.AppProtocolHTTP, Concurrency: 8, Metrics: &v1.MetricNames{HealthPath: "/health"}}
	eps := []pools.Published{{InstanceID: owner.ID, NodeID: testNode, Address: addr, Ready: true, Labels: labels}}
	if err := h.mgr.Publish(context.Background(), owner, mem, eps); err != nil {
		h.t.Fatal(err)
	}
	if _, err := h.mgr.NameRoute(context.Background(), owner, route, pools.RouteSpec{ServedName: "stub-model"}); err != nil {
		h.t.Fatal(err)
	}
}

// A Claude Code request reaches the template with an effort it takes, high becoming xhigh, and adaptive alone with none
func TestAnthropicEffortFitsTemplate(t *testing.T) {
	h := newHarness(t)
	_, stubSrv := h.stub("stub-model")
	text := xhighTemplateText(t)
	front := newLlamaFront(t, text, stubSrv.Config.Handler)
	h.publishTemplate("freddie", text, front)
	for settings, want := range map[string]string{
		`"thinking":{"type":"adaptive"},"output_config":{"effort":"high"}`:   `"reasoning_effort":"xhigh"`,
		`"thinking":{"type":"adaptive"},"output_config":{"effort":"medium"}`: `"reasoning_effort":"medium"`,
		`"thinking":{"type":"adaptive"}`:                                     ``,
	} {
		body := `{"model":"freddie","max_tokens":8,"stream":true,"system":[{"type":"text","text":"You are an agent."}],"messages":[{"role":"user","content":"hello there"}],` + settings + `}`
		r := h.do(http.MethodPost, "/v1/messages", body, "anthropic-version", "2023-06-01")
		if r.status != http.StatusOK {
			t.Fatalf("%s: status %d: %s", settings, r.status, r.body)
		}
		upstream := h.trace(r).GetUpstreamRequest()
		if want == "" && strings.Contains(upstream, "reasoning_effort") || want != "" && !strings.Contains(upstream, want) {
			t.Fatalf("%s: upstream request %s, want %q", settings, upstream, want)
		}
	}
	if !strings.Contains(front.lastPrompt(), "Reasoning effort: xhigh") {
		t.Fatalf("adaptive alone did not leave the template its default: %q", front.lastPrompt())
	}
}

// Disabled thinking reaches a template with no none level as its enable_thinking switch turned off
func TestAnthropicDisabledThinkingTurnsSwitchOff(t *testing.T) {
	h := newHarness(t)
	_, stubSrv := h.stub("stub-model")
	text := xhighTemplateText(t)
	front := newLlamaFront(t, text, stubSrv.Config.Handler)
	h.publishTemplate("freddie", text, front)
	body := `{"model":"freddie","max_tokens":8,"messages":[{"role":"user","content":"hello there"}],"thinking":{"type":"disabled"},"output_config":{"effort":"high"}}`
	r := h.do(http.MethodPost, "/v1/messages", body, "anthropic-version", "2023-06-01")
	if r.status != http.StatusOK {
		t.Fatalf("status %d: %s", r.status, r.body)
	}
	upstream := h.trace(r).GetUpstreamRequest()
	if !strings.Contains(upstream, `"chat_template_kwargs":{"enable_thinking":false}`) || strings.Contains(upstream, "reasoning_effort") {
		t.Fatalf("upstream request %s", upstream)
	}
	if !strings.HasSuffix(front.lastPrompt(), "<think>\n\n</think>\n\n") {
		t.Fatalf("the prompt opens thinking: %q", front.lastPrompt())
	}
}

// Disabled thinking to a template with no way to turn thinking off is refused saying so
func TestAnthropicDisabledThinkingRefused(t *testing.T) {
	h := newHarness(t)
	_, stubSrv := h.stub("stub-model")
	front := newLlamaFront(t, alwaysThinkingTemplate, stubSrv.Config.Handler)
	h.publishTemplate("thinker", alwaysThinkingTemplate, front)
	body := `{"model":"thinker","max_tokens":8,"messages":[{"role":"user","content":"hello there"}],"thinking":{"type":"disabled"}}`
	r := h.do(http.MethodPost, "/v1/messages", body, "anthropic-version", "2023-06-01")
	if r.status != http.StatusBadRequest || !strings.Contains(string(r.body), "no variable that turns thinking off") {
		t.Fatalf("status %d: %s", r.status, r.body)
	}
}

// A chat completions client's own effort the template rejects is refused naming the levels, not answered with the template's stack
func TestChatCompletionsRejectedEffort(t *testing.T) {
	h := newHarness(t)
	_, stubSrv := h.stub("stub-model")
	text := xhighTemplateText(t)
	front := newLlamaFront(t, text, stubSrv.Config.Handler)
	h.publishTemplate("freddie", text, front)
	r := h.do(http.MethodPost, "/v1/chat/completions", `{"model":"freddie","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"high"}`)
	if r.status != http.StatusBadRequest || !strings.Contains(string(r.body), "takes low, medium, xhigh") || strings.Contains(string(r.body), "Jinja") {
		t.Fatalf("status %d: %s", r.status, r.body)
	}
	r = h.do(http.MethodPost, "/v1/chat/completions", `{"model":"freddie","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"high","n":"two"}`)
	if r.status != http.StatusBadRequest || !strings.Contains(string(r.body), "takes low, medium, xhigh") {
		t.Fatalf("a body passed through unparsed: status %d: %s", r.status, r.body)
	}
	r = h.do(http.MethodPost, "/v1/chat/completions", `{"model":"freddie","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"medium"}`)
	if r.status != http.StatusOK || !strings.Contains(front.lastPrompt(), "Reasoning effort: medium") {
		t.Fatalf("a level it takes: status %d: %s", r.status, r.body)
	}
	r = h.do(http.MethodPost, "/v1/chat/completions", `{"model":"freddie","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"none"}`)
	if r.status != http.StatusOK || !strings.HasSuffix(front.lastPrompt(), "<think>\n\n</think>\n\n") {
		t.Fatalf("none: status %d: %s prompt %q", r.status, r.body, front.lastPrompt())
	}
}
