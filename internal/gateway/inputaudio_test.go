package gateway

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/nickheyer/nebu/internal/chat"
	"github.com/nickheyer/nebu/internal/pools"
	"github.com/nickheyer/nebu/pkg/datalayer"
	"github.com/nickheyer/nebu/pkg/estimate"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/stub"
)

const (
	voicePart = `{"type":"input_audio","input_audio":{"data":"UklGRiQAAABXQVZFZm10IBAAAAABAAEAQB8AAIA+AAACABAAZGF0YQAAAAA=","format":"wav"}}`
	mp3Voice  = `{"type":"input_audio","input_audio":{"data":"SUQzBAAAAAAAI1RTU0UAAAAPAAADTGF2ZjU4Ljc2LjEwMAAAAAAA","format":"mp3"}}`
	pixelPart = `{"type":"image_url","image_url":{"url":"data:image/png;base64,iVBORw0KGgoAAAANSUhEUg=="}}`
)

func voiceChat(model, extra string) string {
	return `{"model":"` + model + `","messages":[{"role":"user","content":[{"type":"text","text":"what did I say"},` + voicePart + `]}]` + extra + `}`
}

type roundsSeen struct {
	mu     sync.Mutex
	bodies [][]byte
}

func (s *roundsSeen) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	s.bodies = append(s.bodies, body)
	n := len(s.bodies)
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if n == 1 && bytes.Contains(body, []byte(`"fake__echo"`)) {
		io.WriteString(w, `{"id":"r1","object":"chat.completion","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"fake__echo","arguments":"{\"text\":\"hi\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`)
		return
	}
	io.WriteString(w, `{"id":"r2","object":"chat.completion","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"you said hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":30,"completion_tokens":3,"total_tokens":33}}`)
}

func (s *roundsSeen) seen() [][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([][]byte(nil), s.bodies...)
}

func userContent(t *testing.T, body []byte) json.RawMessage {
	t.Helper()
	var doc struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("body %s: %v", body, err)
	}
	for _, m := range doc.Messages {
		if m.Role == "user" {
			return m.Content
		}
	}
	t.Fatalf("no user turn in %s", body)
	return nil
}

func TestInputAudioWithGatewayToolsReachesRuntime(t *testing.T) {
	h := newHarness(t)
	mcpSrv := fakeMCP(t)
	h.g.tools.Apply(context.Background(), []*v1.ToolServer{{Name: "fake", Enabled: true, Kind: v1.ToolServerKind_TOOL_SERVER_KIND_HTTP, Url: mcpSrv.URL}})
	t.Cleanup(h.g.tools.Close)
	var native, emulated roundsSeen
	nativeSrv, emulatedSrv := httptest.NewServer(&native), httptest.NewServer(&emulated)
	t.Cleanup(nativeSrv.Close)
	t.Cleanup(emulatedSrv.Close)
	h.publish("voice", "voice", "vllm", datalayer.AppProtocolHTTP, true, pools.RouteSpec{ServedName: "voice-served"}, nativeSrv.URL)
	h.publish("folds", "folds", "vllm", datalayer.AppProtocolHTTP, true, pools.RouteSpec{ServedName: "folds"}, emulatedSrv.URL)
	h.inst.mu.Lock()
	h.inst.support["folds-"+hostOf(emulatedSrv)] = v1.ToolSupport_TOOL_SUPPORT_EMULATED
	h.inst.mu.Unlock()

	r := h.do(http.MethodPost, chat.OpenAIChat, voiceChat("voice", ""), ToolsHeader, "fake", SystemHeader, base64.StdEncoding.EncodeToString([]byte("answer briefly")))
	if r.status != http.StatusOK || !strings.Contains(string(r.body), "you said hi") {
		t.Fatalf("voice with tools %d %s", r.status, r.body)
	}
	rounds := native.seen()
	if len(rounds) != 2 {
		t.Fatalf("runtime saw %d rounds", len(rounds))
	}
	for i, body := range rounds {
		if !bytes.Contains(body, []byte(voicePart)) || !bytes.Contains(body, []byte(`"text":"what did I say"`)) || !bytes.Contains(body, []byte(`"fake__echo"`)) || !bytes.Contains(body, []byte(`"model":"voice-served"`)) || !bytes.Contains(body, []byte("answer briefly")) {
			t.Fatalf("round %d sent %s", i+1, body)
		}
	}

	r = h.do(http.MethodPost, chat.OpenAIChat, voiceChat("folds", `,"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}]`))
	sent := emulated.seen()
	if r.status != http.StatusOK || len(sent) != 1 || !bytes.Contains(sent[0], []byte(voicePart)) || bytes.Contains(sent[0], []byte(`"tools":`)) || !bytes.Contains(sent[0], []byte("lookup")) {
		t.Fatalf("emulated tools %d %s, runtime saw %q", r.status, r.body, sent)
	}
}

func TestInputAudioMixedPartsSurviveRerender(t *testing.T) {
	h := newHarness(t)
	var up roundsSeen
	srv := httptest.NewServer(&up)
	t.Cleanup(srv.Close)
	h.publish("mixed", "mixed", "vllm", datalayer.AppProtocolHTTP, true, pools.RouteSpec{ServedName: "mixed-served"}, srv.URL)
	content := `[{"type":"text","text":"compare these"},` + pixelPart + `,` + voicePart + `,{"type":"text","text":"and this"},` + mp3Voice + `]`
	body := `{"model":"mixed","messages":[{"role":"user","content":` + content + `}]}`
	r := h.do(http.MethodPost, chat.OpenAIChat, body, SystemHeader, base64.StdEncoding.EncodeToString([]byte("be brief")))
	sent := up.seen()
	if r.status != http.StatusOK || len(sent) != 1 {
		t.Fatalf("mixed %d %s", r.status, r.body)
	}
	if got := userContent(t, sent[0]); !bytes.Contains(sent[0], []byte(content)) {
		t.Fatalf("user content went upstream as %s", got)
	}
	if !bytes.Contains(sent[0], []byte(`{"role":"system","content":"be brief"}`)) {
		t.Fatalf("system prompt missing from %s", sent[0])
	}
	tr := finishedTrace(t, h, r.header.Get(TraceHeader))
	conv := Conversation(tr)
	if conv == nil || len(conv.GetTurns()) == 0 {
		t.Fatalf("conversation %v", conv)
	}
	parts := conv.GetTurns()[len(conv.GetTurns())-1].GetParts()
	if len(parts) != 5 || parts[2].GetType() != "audio" || parts[2].GetMediaType() != "wav" || parts[2].GetSize() == 0 || parts[2].GetText() != "" {
		t.Fatalf("traced parts %v", parts)
	}
}

func TestInputAudioRefusedWhereNotCarried(t *testing.T) {
	h := newHarness(t, func(s *setup) { s.runtimes.api["relay-anthropic"] = v1.ApiFlavor_API_FLAVOR_ANTHROPIC })
	var anthropic, ollama roundsSeen
	antSrv, olSrv := httptest.NewServer(&anthropic), httptest.NewServer(&ollama)
	t.Cleanup(antSrv.Close)
	t.Cleanup(olSrv.Close)
	h.publish("claude", "claude", "relay-anthropic", datalayer.AppProtocolHTTP, true, pools.RouteSpec{ServedName: "claude"}, antSrv.URL)
	h.publish("olla", "olla", "ollama", datalayer.AppProtocolHTTP, true, pools.RouteSpec{ServedName: "olla"}, olSrv.URL)
	tok := loadFixture(t)
	st, stubSrv := h.tokensStub(stub.NamesVLLM, tok)
	h.tokensPool("ids", "ids", "vllm", datalayer.AppProtocolHTTP, stubSrv.URL, true)
	cases := []struct{ model, message string }{
		{"claude", "the anthropic protocol takes no audio"},
		{"olla", "the ollama protocol takes no audio"},
		{"ids", "cannot travel in a text prompt"},
	}
	for _, c := range cases {
		r := h.do(http.MethodPost, chat.OpenAIChat, voiceChat(c.model, ""))
		if r.status != http.StatusBadRequest || !strings.Contains(string(r.body), c.message) {
			t.Fatalf("%s answered %d %s", c.model, r.status, r.body)
		}
	}
	if len(anthropic.seen()) != 0 || len(ollama.seen()) != 0 || st.Stats().Served != 0 {
		t.Fatal("a runtime that cannot take audio was sent it")
	}
}

func TestInputAudioTraceStaysCapped(t *testing.T) {
	h := newHarness(t)
	var up roundsSeen
	srv := httptest.NewServer(&up)
	t.Cleanup(srv.Close)
	h.publish("long", "long", "vllm", datalayer.AppProtocolHTTP, true, pools.RouteSpec{ServedName: "long-served"}, srv.URL)
	data := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0xff, 0xfb, 0x90, 0x44}, 1<<20))
	body := `{"model":"long","messages":[{"role":"user","content":[{"type":"input_audio","input_audio":{"data":"` + data + `","format":"mp3"}}]}]}`
	r := h.do(http.MethodPost, chat.OpenAIChat, body, SystemHeader, base64.StdEncoding.EncodeToString([]byte("transcribe")))
	sent := up.seen()
	if r.status != http.StatusOK || len(sent) != 1 || !bytes.Contains(sent[0], []byte(data)) {
		t.Fatalf("long audio %d %s", r.status, r.body)
	}
	tr := finishedTrace(t, h, r.header.Get(TraceHeader))
	if tr.GetRequestBytes() != uint64(len(body)) || len(tr.GetRequest()) > traceBodyCap || len(tr.GetUpstreamRequest()) > traceBodyCap || tr.GetUpstreamRequest() == "" {
		t.Fatalf("trace kept %d request and %d upstream bytes of %d", len(tr.GetRequest()), len(tr.GetUpstreamRequest()), len(body))
	}
}

func TestInputAudioCrossesPrefillDecodeRelay(t *testing.T) {
	h := newHarness(t, func(s *setup) {
		s.runtimes.api["nixl"] = v1.ApiFlavor_API_FLAVOR_OPENAI
		s.runtimes.handoff["nixl"] = estimate.HandoffNIXL
	})
	answer := `{"id":"x","object":"chat.completion","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"relayed"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}`
	var mu sync.Mutex
	var prefilled, decoded []byte
	seat := func(into *[]byte, reply string) *httptest.Server {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			mu.Lock()
			*into = body
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, reply)
		}))
		t.Cleanup(srv.Close)
		return srv
	}
	prefill := seat(&prefilled, strings.Replace(answer, `"usage"`, `"kv_transfer_params":{"remote_engine_id":"e1","remote_block_ids":[1,2]},"usage"`, 1))
	decode := seat(&decoded, answer)
	h.seats("nixl", hostOf(prefill), hostOf(decode), nil)
	r := h.do(http.MethodPost, chat.OpenAIChat, voiceChat("chat", ""))
	if r.status != http.StatusOK || !strings.Contains(string(r.body), "relayed") {
		t.Fatalf("relay %d %s", r.status, r.body)
	}
	mu.Lock()
	defer mu.Unlock()
	if !bytes.Contains(prefilled, []byte(voicePart)) || !bytes.Contains(decoded, []byte(voicePart)) || !bytes.Contains(decoded, []byte(`"remote_engine_id":"e1"`)) {
		t.Fatalf("prefill got %s\ndecode got %s", prefilled, decoded)
	}
}

func TestInputAudioSurvivesEffortFitting(t *testing.T) {
	h := newHarness(t)
	var up roundsSeen
	text := xhighTemplateText(t)
	front := newLlamaFront(t, text, &up)
	h.publishTemplate("freddie", text, front)
	r := h.do(http.MethodPost, chat.OpenAIChat, voiceChat("freddie", `,"reasoning_effort":"none"`))
	sent := up.seen()
	if r.status != http.StatusOK || len(sent) != 1 || !bytes.Contains(sent[0], []byte(voicePart)) || bytes.Contains(sent[0], []byte(`"reasoning_effort"`)) {
		t.Fatalf("effort none %d %s, runtime saw %q", r.status, r.body, sent)
	}
}
