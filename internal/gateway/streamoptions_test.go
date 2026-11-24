package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nickheyer/nebu/internal/auth"
	"github.com/nickheyer/nebu/internal/pools"
	"github.com/nickheyer/nebu/pkg/cache"
	"github.com/nickheyer/nebu/pkg/datalayer"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

const (
	streamBody = `{"model":"chat","messages":[{"role":"user","content":"hello there"}],"max_tokens":8,"stream":true}`
	textChunk  = "data: {\"id\":\"x\",\"object\":\"chat.completion.chunk\",\"model\":\"gpt\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"}}]}\n\n"
	stopChunk  = "data: {\"id\":\"x\",\"object\":\"chat.completion.chunk\",\"model\":\"gpt\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"
	usageChunk = "data: {\"id\":\"x\",\"object\":\"chat.completion.chunk\",\"model\":\"gpt\",\"choices\":[],\"usage\":{\"prompt_tokens\":11,\"completion_tokens\":4,\"total_tokens\":15}}\n\n"
	// The last chunk of a provider naming a stream's usage unasked, on the chunk that ends the answer
	stopWithUsage = "data: {\"id\":\"x\",\"object\":\"chat.completion.chunk\",\"model\":\"gpt\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":11,\"completion_tokens\":4,\"total_tokens\":15}}\n\n"
)

// A provider of streams that records each body it was sent and answers as the test has it
type streamer struct {
	mu     sync.Mutex
	bodies []map[string]json.RawMessage
	paths  []string
	header []http.Header
	// Answers one request, told whether its body named stream_options
	answer func(w http.ResponseWriter, asked bool)
	srv    *httptest.Server
}

func newStreamer(t *testing.T, answer func(w http.ResponseWriter, asked bool)) *streamer {
	s := &streamer{answer: answer}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]json.RawMessage
		json.NewDecoder(r.Body).Decode(&body)
		s.mu.Lock()
		s.bodies, s.paths, s.header = append(s.bodies, body), append(s.paths, r.URL.Path), append(s.header, r.Header.Clone())
		s.mu.Unlock()
		_, asked := body["stream_options"]
		s.answer(w, asked)
	}))
	t.Cleanup(s.srv.Close)
	return s
}

// Whether each body the provider was sent named stream_options, in order
func (s *streamer) asked() []bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]bool, len(s.bodies))
	for i, b := range s.bodies {
		_, out[i] = b["stream_options"]
	}
	return out
}

func stream(w http.ResponseWriter, chunks ...string) {
	w.Header().Set("Content-Type", "text/event-stream")
	for _, c := range chunks {
		io.WriteString(w, c)
	}
	io.WriteString(w, "data: [DONE]\n\n")
}

func sameBools(a []bool, b ...bool) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// The tokens recorded against the shared owner's credentials for a model
func recorded(t *testing.T, creds *auth.Credentials, model string) (requests, in, out uint64) {
	t.Helper()
	used, err := creds.Usage(context.Background(), auth.ProviderDaemon, "", time.Time{}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range used {
		if u.GetModel() == model {
			return u.GetRequests(), u.GetInputTokens(), u.GetOutputTokens()
		}
	}
	return 0, 0, 0
}

// A stream to a provider naming usage only when asked is asked on behalf of a client that did not, its usage recorded and
// the chunk of usage alone kept from that client, while a client that asked gets the chunk it asked for
func TestStreamUsageIsAskedForAndKeptFromAClientThatDidNot(t *testing.T) {
	creds := keptCreds(t, map[string][]string{"lab": {"a1"}})
	h := newHarness(t, func(s *setup) { s.creds = creds })
	up := newStreamer(t, func(w http.ResponseWriter, asked bool) {
		if asked {
			stream(w, textChunk, stopChunk, usageChunk)
			return
		}
		stream(w, textChunk, stopChunk)
	})
	p := h.provider("lab", "gpt", "vllm", up.srv.URL)
	h.ordered("chain", "chat", &v1.RewriteTarget{PoolId: string(p), Model: "gpt", Weight: 1})

	r := h.do(http.MethodPost, "/v1/chat/completions", streamBody)
	if r.status != http.StatusOK || !strings.Contains(string(r.body), `"content":"hi"`) || !strings.Contains(string(r.body), "[DONE]") {
		t.Fatalf("stream %d: %s", r.status, r.body)
	}
	if strings.Contains(string(r.body), `"usage"`) {
		t.Fatalf("a client that asked for no usage was sent the chunk the gateway asked for: %s", r.body)
	}
	var opts struct {
		IncludeUsage bool `json:"include_usage"`
	}
	if got := up.asked(); !sameBools(got, true) || json.Unmarshal(up.bodies[0]["stream_options"], &opts) != nil || !opts.IncludeUsage {
		t.Fatalf("the provider was asked %v with %s", got, up.bodies[0]["stream_options"])
	}
	h.settled("chat", p)
	if n, in, out := recorded(t, creds, "gpt"); n != 1 || in != 11 || out != 4 {
		t.Fatalf("recorded %d requests, %d in, %d out", n, in, out)
	}

	asking := strings.Replace(streamBody, `"stream":true`, `"stream":true,"stream_options":{"include_usage":true}`, 1)
	if r := h.do(http.MethodPost, "/v1/chat/completions", asking); r.status != http.StatusOK || !strings.Contains(string(r.body), `"prompt_tokens":11`) {
		t.Fatalf("a client that asked for usage: %d %s", r.status, r.body)
	}
}

// A server refusing stream_options is sent the request once more without it, found out once: not again for the next
// request, nor after a restart, and a client's own stream_options is left out of what it is sent
func TestStrictServerRefusingStreamOptionsIsFoundOutOnce(t *testing.T) {
	creds := keptCreds(t, map[string][]string{"lab": {"a1"}})
	h := newHarness(t, func(s *setup) { s.creds = creds })
	kept, err := cache.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h.g.learned = kept
	up := newStreamer(t, func(w http.ResponseWriter, asked bool) {
		if asked {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnprocessableEntity)
			io.WriteString(w, `{"detail":[{"type":"extra_forbidden","loc":["body","stream_options"],"msg":"Extra inputs are not permitted"}]}`)
			return
		}
		stream(w, textChunk, stopWithUsage)
	})
	p := h.provider("lab", "gpt", "vllm", up.srv.URL)
	h.ordered("chain", "chat", &v1.RewriteTarget{PoolId: string(p), Model: "gpt", Weight: 1})
	ask := func(body string) {
		t.Helper()
		if r := h.do(http.MethodPost, "/v1/chat/completions", body); r.status != http.StatusOK || !strings.Contains(string(r.body), `"content":"hi"`) {
			t.Fatalf("stream %d: %s", r.status, r.body)
		}
	}
	ask(streamBody)
	if got := up.asked(); !sameBools(got, true, false) {
		t.Fatalf("the first stream to a strict server went %v", got)
	}
	ask(streamBody)
	if got := up.asked(); !sameBools(got, true, false, false) {
		t.Fatalf("the second stream went %v", got)
	}
	// What the client set of the option the server refuses is left out
	ask(strings.Replace(streamBody, `"stream":true`, `"stream":true,"stream_options":{"include_usage":true}`, 1))
	if got := up.asked(); !sameBools(got, true, false, false, false) {
		t.Fatalf("a client's own stream_options went %v", got)
	}
	// A restart forgets what the run learned and reads the refusal kept
	h.g.streamOpts.Clear()
	ask(streamBody)
	if got := up.asked(); !sameBools(got, true, false, false, false, false) {
		t.Fatalf("the first stream after a restart went %v", got)
	}
	h.settled("chat", p)
	if n, in, out := recorded(t, creds, "gpt"); n != 4 || in != 44 || out != 16 {
		t.Fatalf("recorded %d requests, %d in, %d out", n, in, out)
	}
}

// A provider said to name a stream's usage unasked and to refuse stream_options is sent none, and its usage is recorded as
// it names it. Were that wrong of it, a stream naming no usage has it asked from then on, so its budgets fill all the same.
func TestProviderSaidToNameUsageUnaskedIsAskedOnceAStreamNamesNone(t *testing.T) {
	creds := keptCreds(t, map[string][]string{"mistral": {"m1"}})
	h := newHarness(t, func(s *setup) { s.creds = creds })
	asks, refuses, _ := creds.StreamOptions("mistral")
	if asks || !refuses {
		t.Fatalf("mistral is said to ask %v, refuse %v", asks, refuses)
	}
	// As its documents have it: usage on the last chunk, unasked
	documented := newStreamer(t, func(w http.ResponseWriter, asked bool) {
		if asked {
			w.WriteHeader(http.StatusUnprocessableEntity)
			return
		}
		stream(w, textChunk, stopWithUsage)
	})
	p := h.provider("mistral", "small", "vllm", documented.srv.URL)
	h.ordered("chain", "chat", &v1.RewriteTarget{PoolId: string(p), Model: "small", Weight: 1})
	for range 2 {
		if r := h.do(http.MethodPost, "/v1/chat/completions", streamBody); r.status != http.StatusOK {
			t.Fatalf("stream %d: %s", r.status, r.body)
		}
	}
	h.settled("chat", p)
	if got := documented.asked(); !sameBools(got, false, false) {
		t.Fatalf("a provider refusing stream_options was sent %v", got)
	}
	if n, in, out := recorded(t, creds, "small"); n != 2 || in != 22 || out != 8 {
		t.Fatalf("recorded %d requests, %d in, %d out", n, in, out)
	}

	// Were its documents wrong: usage only when asked
	h2 := newHarness(t, func(s *setup) { s.creds = creds })
	otherwise := newStreamer(t, func(w http.ResponseWriter, asked bool) {
		if asked {
			stream(w, textChunk, stopChunk, usageChunk)
			return
		}
		stream(w, textChunk, stopChunk)
	})
	p2 := h2.provider("mistral", "large", "vllm", otherwise.srv.URL)
	h2.ordered("chain", "chat", &v1.RewriteTarget{PoolId: string(p2), Model: "large", Weight: 1})
	for range 3 {
		r := h2.do(http.MethodPost, "/v1/chat/completions", streamBody)
		if r.status != http.StatusOK || strings.Contains(string(r.body), `"usage"`) {
			t.Fatalf("stream %d: %s", r.status, r.body)
		}
		h2.settled("chat", p2)
	}
	if got := otherwise.asked(); !sameBools(got, false, true, true) {
		t.Fatalf("a provider whose first stream named no usage was sent %v", got)
	}
	if n, in, out := recorded(t, creds, "large"); n != 3 || in != 22 || out != 8 {
		t.Fatalf("recorded %d requests, %d in, %d out", n, in, out)
	}
}

// A provider whose catalog says nothing of stream_options is asked for usage once a stream of its named none, and from its
// first stream after a restart, so no later stream goes unrecorded
func TestProviderNamingNoUsageIsAskedFromThenOnAcrossRestarts(t *testing.T) {
	creds := keptCreds(t, map[string][]string{"deepseek": {"d1"}})
	if asks, refuses, _ := creds.StreamOptions("deepseek"); asks || refuses {
		t.Fatalf("deepseek is said to ask %v, refuse %v", asks, refuses)
	}
	kept, err := cache.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	up := newStreamer(t, func(w http.ResponseWriter, asked bool) {
		if asked {
			stream(w, textChunk, stopChunk, usageChunk)
			return
		}
		stream(w, textChunk, stopChunk)
	})
	for run, want := range [][]bool{{false, true}, {false, true, true, true}} {
		// Each run a daemon started over what the last one kept
		h := newHarness(t, func(s *setup) { s.creds = creds })
		h.g.learned = kept
		p := h.provider("deepseek", "chat-v3", "vllm", up.srv.URL)
		h.ordered("chain", "chat", &v1.RewriteTarget{PoolId: string(p), Model: "chat-v3", Weight: 1})
		for range 2 {
			if r := h.do(http.MethodPost, "/v1/chat/completions", streamBody); r.status != http.StatusOK || strings.Contains(string(r.body), `"usage"`) {
				t.Fatalf("stream %d: %s", r.status, r.body)
			}
			h.settled("chat", p)
		}
		if got := up.asked(); !sameBools(got, want...) {
			t.Fatalf("run %d: the provider was sent %v, want %v", run, got, want)
		}
	}
	if n, in, out := recorded(t, creds, "chat-v3"); n != 4 || in != 33 || out != 12 {
		t.Fatalf("recorded %d requests, %d in, %d out", n, in, out)
	}
}

// A count through a provider with no tokenizer to ask is the gateway's estimate, said so, with nothing generated and
// nothing recorded against the caller's credential
func TestCountThroughAProviderIsEstimatedNotBilled(t *testing.T) {
	creds := keptCreds(t, map[string][]string{"lab": {"a1"}})
	h := newHarness(t, func(s *setup) { s.creds = creds })
	// A provider with no tokenizer where the gateway would ask for one
	up := newStreamer(t, func(w http.ResponseWriter, _ bool) { w.WriteHeader(http.StatusNotFound) })
	p := h.provider("lab", "gpt", "vllm", up.srv.URL)
	h.ordered("chain", "chat", &v1.RewriteTarget{PoolId: string(p), Model: "gpt", Weight: 1})
	r := h.do(http.MethodPost, "/v1/messages/count_tokens", `{"model":"chat","messages":[{"role":"user","content":"hello there friend, how many tokens is this"}]}`, "anthropic-version", "2023-06-01")
	var counted struct {
		InputTokens int `json:"input_tokens"`
	}
	if r.status != http.StatusOK || json.Unmarshal(r.body, &counted) != nil || counted.InputTokens < 5 || counted.InputTokens > 40 {
		t.Fatalf("count %d: %s", r.status, r.body)
	}
	if got := r.header.Get(CountHeader); got != "estimate" {
		t.Fatalf("an estimated count is marked %q", got)
	}
	h.settled("chat", p)
	up.mu.Lock()
	defer up.mu.Unlock()
	for _, path := range up.paths {
		if strings.Contains(path, "completions") || strings.Contains(path, "responses") {
			t.Fatalf("a count generated at the provider: %v", up.paths)
		}
	}
	if n, in, out := recorded(t, creds, "gpt"); n != 0 || in != 0 || out != 0 {
		t.Fatalf("a count was recorded against the credential: %d requests, %d in, %d out", n, in, out)
	}
}

// What only a browser sends of the page a request came from stays at the gateway, for a local runtime as for a provider,
// and every other header the client sent goes on
func TestBrowserHeadersStayAtTheGateway(t *testing.T) {
	h := newHarness(t)
	up := newStreamer(t, func(w http.ResponseWriter, _ bool) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, openaiAnswer)
	})
	h.publish("chat", "stub", "vllm", datalayer.AppProtocolHTTP, true, pools.RouteSpec{ServedName: "stub-model"}, up.srv.URL)
	r := h.do(http.MethodPost, "/v1/chat/completions", chatBody,
		"Origin", "https://nebu.example", "Referer", "https://nebu.example/chat", "Sec-Fetch-Mode", "cors", "Sec-Fetch-Site", "same-origin", "Sec-Ch-Ua", `"Chromium"`,
		"X-Request-Tag", "kept", "User-Agent", "client/1")
	if r.status != http.StatusOK {
		t.Fatalf("status %d: %s", r.status, r.body)
	}
	up.mu.Lock()
	defer up.mu.Unlock()
	got := up.header[len(up.header)-1]
	for name := range got {
		if name == "Origin" || name == "Referer" || strings.HasPrefix(name, "Sec-") {
			t.Fatalf("the runtime was sent %s: %v", name, got)
		}
	}
	if got.Get("X-Request-Tag") != "kept" || got.Get("User-Agent") != "client/1" {
		t.Fatalf("the client's other headers went on as %v", got)
	}
}

// A key its provider rejected is passed over for the caller's next. A caller's only key is rejected to the caller once, as
// theirs to mend, in the provider's words and naming the key, the request not sent again under it, and is tried again by
// the next request rather than waited out, so one rejection is not an outage
func TestRejectedKeyIsPassedOverAndAnOnlyKeyTriedAgain(t *testing.T) {
	creds := keptCreds(t, map[string][]string{"lab": {"a1", "a2"}, "lab2": {"b1"}})
	h := newHarness(t, func(s *setup) { s.creds = creds })
	a, b := newKeyed(t, openaiAnswer), newKeyed(t, openaiAnswer)
	// As a relay answers the gateway for a provider's 401
	rejection := http.Header{ProviderStatusHeader: {"401"}}
	a.refuse["a1"], a.header, a.body = http.StatusBadGateway, rejection, `{"error":{"message":"invalid api key"}}`
	b.refuse["b1"], b.header, b.body = http.StatusBadGateway, rejection, `{"error":{"message":"invalid api key"}}`
	pa, pb := h.provider("lab", "gpt", "vllm", a.srv.URL), h.provider("lab2", "gpt", "vllm", b.srv.URL)
	h.ordered("two", "two-keys", &v1.RewriteTarget{PoolId: string(pa), Model: "gpt", Weight: 1})
	h.ordered("one", "one-key", &v1.RewriteTarget{PoolId: string(pb), Model: "gpt", Weight: 1})
	ask := func(model string) reply {
		t.Helper()
		return h.do(http.MethodPost, "/v1/chat/completions", strings.Replace(chatBody, `"model":"chat"`, `"model":"`+model+`"`, 1))
	}
	if r := ask("two-keys"); r.status != http.StatusOK || !slices.Equal(a.sent(), []string{"a1", "a2"}) {
		t.Fatalf("a rejected key with another behind it: %d %s, sent %v", r.status, r.body, a.sent())
	}
	if r := ask("two-keys"); r.status != http.StatusOK || !slices.Equal(a.sent(), []string{"a1", "a2", "a2"}) {
		t.Fatalf("the rejected key is passed over at once: %d, sent %v", r.status, a.sent())
	}
	r := ask("one-key")
	if r.status != http.StatusForbidden || r.header.Get(ProviderStatusHeader) != "" || !strings.Contains(string(r.body), `OpenAI compatible rejected the credential \"b1\"`) || !strings.Contains(string(r.body), "invalid api key") || !strings.Contains(string(r.body), "permission_error") {
		t.Fatalf("an only key rejected: %d %v %s", r.status, r.header, r.body)
	}
	if got := b.sent(); !slices.Equal(got, []string{"b1"}) {
		t.Fatalf("a request refused under an only key was sent under it again: %v", got)
	}
	if tr := traceOf(t, h, r.header.Get(TraceHeader)); tr.GetStatus() != http.StatusForbidden || tr.GetCredential() != "b1" || !strings.Contains(tr.GetError(), "rejected the credential") {
		t.Fatalf("trace status %d credential %q error %q", tr.GetStatus(), tr.GetCredential(), tr.GetError())
	}
	// The provider takes the key again, as after a rejection that was its own fault
	b.mu.Lock()
	delete(b.refuse, "b1")
	b.mu.Unlock()
	if r := ask("one-key"); r.status != http.StatusOK {
		t.Fatalf("an only key rejected once was waited out rather than tried: %d %s", r.status, r.body)
	}
}
