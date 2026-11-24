package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nickheyer/nebu/internal/auth"
	"github.com/nickheyer/nebu/internal/db"
	"github.com/nickheyer/nebu/internal/pools"
	"github.com/nickheyer/nebu/pkg/datalayer"
	"github.com/nickheyer/nebu/pkg/flowcontrol"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/sources"
)

// Keys by owner for the harness's source, every other source serving no models
type fakeCreds struct {
	keys map[string]string
	mu   sync.Mutex
	// Owners asked for, in order
	asked []string
	// How the provider is said to take stream_options
	asks, refuses bool
}

func (f *fakeCreds) Headers(_ context.Context, owner, sourceID, _ string, sent http.Header) (*v1.ProviderCredential, map[string]string, error) {
	if sourceID != "hf" {
		return nil, nil, nil
	}
	f.mu.Lock()
	f.asked = append(f.asked, owner)
	f.mu.Unlock()
	key, ok := f.keys[owner]
	if !ok {
		return nil, nil, fmt.Errorf("%w for Lab, add one under Profile, Providers", sources.ErrNoCredential)
	}
	out := map[string]string{"Authorization": "Bearer " + key}
	if sent.Get("X-Version") == "" {
		out["X-Version"] = "fixed"
	}
	return &v1.ProviderCredential{Id: owner, SourceId: sourceID, Name: owner}, out, nil
}

func (*fakeCreds) Answered(*v1.ProviderCredential, string, int, http.Header, time.Duration) {}

func (*fakeCreds) Record(*v1.ProviderCredential, string, int, int, int) {}

func (f *fakeCreds) StreamOptions(string) (bool, bool, string) { return f.asks, f.refuses, "" }

// The caller's key reaches the upstream in place of the client's own token, and a caller with none is refused before dispatch
func TestProviderCredentialSignsTheRequest(t *testing.T) {
	creds := &fakeCreds{keys: map[string]string{auth.ProviderDaemon: "sk-shared"}}
	h := newHarness(t, func(s *setup) { s.creds = creds })
	var seen sync.Map
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.Store("auth", r.Header.Get("Authorization"))
		seen.Store("version", r.Header.Get("X-Version"))
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"x","object":"chat.completion","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]}`)
	}))
	t.Cleanup(upstream.Close)
	h.publish("chat", "stub", "vllm", datalayer.AppProtocolHTTP, true, pools.RouteSpec{ServedName: "stub-model"}, upstream.URL)
	r := h.do(http.MethodPost, "/v1/chat/completions", chatBody, "Authorization", "Bearer client-token", "X-Version", "client")
	if r.status != http.StatusOK {
		t.Fatalf("status %d %s", r.status, r.body)
	}
	if got, _ := seen.Load("auth"); got != "Bearer sk-shared" {
		t.Fatalf("upstream saw %v, want the caller's stored key", got)
	}
	if got, _ := seen.Load("version"); got != "client" {
		t.Fatalf("a header the client sent stands over the provider's fixed one, got %v", got)
	}
	creds.keys = map[string]string{}
	r = h.do(http.MethodPost, "/v1/chat/completions", chatBody)
	if r.status != http.StatusForbidden || !strings.Contains(string(r.body), "no credential for Lab") || !strings.Contains(string(r.body), "Profile") {
		t.Fatalf("refusal %d %s", r.status, r.body)
	}
}

// A gateway key names no person, so its requests use the shared credentials
func TestProviderCredentialOwnerOfAGatewayKey(t *testing.T) {
	creds := &fakeCreds{keys: map[string]string{auth.ProviderDaemon: "sk-shared"}}
	h := newHarness(t, func(s *setup) { s.creds = creds; s.keys = []string{"gw-key"} })
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"x","object":"chat.completion","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]}`)
	}))
	t.Cleanup(upstream.Close)
	h.publish("chat", "stub", "vllm", datalayer.AppProtocolHTTP, true, pools.RouteSpec{ServedName: "stub-model"}, upstream.URL)
	if r := h.do(http.MethodPost, "/v1/chat/completions", chatBody, "Authorization", "Bearer gw-key"); r.status != http.StatusOK {
		t.Fatalf("status %d %s", r.status, r.body)
	}
	creds.mu.Lock()
	defer creds.mu.Unlock()
	if len(creds.asked) != 1 || creds.asked[0] != auth.ProviderDaemon {
		t.Fatalf("owners asked %v", creds.asked)
	}
}

// Credentials over their own database, sources lab and lab2 serving models, the shared owner keeping each source's keys in order
func keptCreds(t *testing.T, keys map[string][]string) *auth.Credentials {
	t.Helper()
	ctx := context.Background()
	store, err := db.Open(filepath.Join(t.TempDir(), "creds.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	log := slog.New(slog.DiscardHandler)
	rows := sources.Seeds()
	for _, id := range []string{"lab", "lab2"} {
		rows = append(rows, &v1.Source{Id: id, Kind: v1.SourceKind_SOURCE_KIND_OPENAI_COMPATIBLE, Config: map[string]string{"endpoint": "http://127.0.0.1:1/v1"}})
	}
	m := sources.NewManager(store, nil, log, t.TempDir())
	if err := m.Load(ctx, rows); err != nil {
		t.Fatal(err)
	}
	c := auth.NewCredentials(store, m.Registry, log, "urn:uuid:00000000-0000-4000-8000-000000000000")
	for _, source := range slices.Sorted(maps.Keys(keys)) {
		for _, k := range keys[source] {
			if _, err := c.Put(ctx, auth.ProviderDaemon, &v1.PutCredentialRequest{SourceId: source, Name: k, Secret: k}); err != nil {
				t.Fatal(err)
			}
		}
	}
	return c
}

// Publishes one ready endpoint of a provider source's model in the runtime's pool
func (h *harness) provider(source, model, runtime, addr string) datalayer.PoolID {
	h.t.Helper()
	mk := pools.ModelKey{SourceID: source, Repo: model, Group: "q4"}
	owner := pools.OwnerRef{Kind: pools.OwnerInstance, ID: source + "-" + model}
	a, err := datalayer.ParseAddress(strings.TrimPrefix(addr, "http://"))
	if err != nil {
		h.t.Fatal(err)
	}
	mem := pools.Membership{Model: mk, Runtime: runtime, Protocol: datalayer.AppProtocolHTTP, Concurrency: 8, Metrics: &v1.MetricNames{HealthPath: "/health"}}
	if err := h.mgr.Publish(context.Background(), owner, mem, []pools.Published{{InstanceID: owner.ID, NodeID: testNode, Address: a, Ready: true}}); err != nil {
		h.t.Fatal(err)
	}
	return pools.PoolIDOf(mk, runtime, "")
}

// Routes a client model name over targets in the order written
func (h *harness) ordered(name, match string, targets ...*v1.RewriteTarget) {
	h.t.Helper()
	if _, err := h.mgr.PutRoute(context.Background(), name, &v1.RouteSpec{Rules: []*v1.RewriteRule{{Match: match, Targets: targets}}, Balance: v1.RouteBalance_ROUTE_BALANCE_ORDERED}); err != nil {
		h.t.Fatal(err)
	}
}

// A provider answering by the key it was sent: refused keys with their status, headers and body, the rest with an answer
type keyed struct {
	mu     sync.Mutex
	keys   []string
	refuse map[string]int
	header http.Header
	body   string
	answer string
	bodies []map[string]any
	srv    *httptest.Server
}

func newKeyed(t *testing.T, answer string) *keyed {
	k := &keyed{refuse: map[string]int{}, header: http.Header{}, answer: answer}
	k.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		k.mu.Lock()
		k.keys, k.bodies = append(k.keys, key), append(k.bodies, body)
		status, refused := k.refuse[key]
		k.mu.Unlock()
		if refused {
			for name, v := range k.header {
				w.Header()[name] = v
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			io.WriteString(w, k.body)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, k.answer)
	}))
	t.Cleanup(k.srv.Close)
	return k
}

// The keys the provider was sent, in order
func (k *keyed) sent() []string {
	k.mu.Lock()
	defer k.mu.Unlock()
	return slices.Clone(k.keys)
}

const (
	openaiAnswer    = `{"id":"x","object":"chat.completion","model":"gpt","choices":[{"index":0,"message":{"role":"assistant","content":"from a"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`
	anthropicAnswer = `{"id":"msg_1","type":"message","role":"assistant","model":"claude","content":[{"type":"text","text":"from b"}],"stop_reason":"end_turn","usage":{"input_tokens":7,"output_tokens":2}}`
)

// Every permit, endpoint count and route slot the request held is back
func (h *harness) settled(route string, pools ...datalayer.PoolID) {
	h.t.Helper()
	waitFor(h.t, "every count back to zero", func() bool {
		for _, p := range pools {
			if h.g.flow.InFlight(p) != 0 {
				return false
			}
			for _, ep := range h.store.Endpoints(p, nil) {
				if h.g.InFlight(ep.Labels()[datalayer.LabelInstance]) != 0 {
					return false
				}
			}
		}
		return h.g.routeState(route).inflight.Load() == 0
	})
}

// A provider refusing the caller's first key moves the request to the second on the same target, in one trace
func TestDecideAgainOnTheNextCredential(t *testing.T) {
	creds := keptCreds(t, map[string][]string{"lab": {"a1", "a2"}, "lab2": {"b1"}})
	h := newHarness(t, func(s *setup) { s.creds = creds })
	a, b := newKeyed(t, openaiAnswer), newKeyed(t, openaiAnswer)
	a.refuse["a1"], a.header, a.body = http.StatusTooManyRequests, http.Header{"Retry-After": {"30"}}, `{"error":{"message":"a1 is over its limit","type":"rate_limit_error"}}`
	pa, pb := h.provider("lab", "gpt", "vllm", a.srv.URL), h.provider("lab2", "gpt", "vllm", b.srv.URL)
	h.ordered("chain", "chat", &v1.RewriteTarget{PoolId: string(pa), Model: "gpt", Weight: 1}, &v1.RewriteTarget{PoolId: string(pb), Model: "gpt", Weight: 1})
	r := h.do(http.MethodPost, "/v1/chat/completions", chatBody)
	if r.status != http.StatusOK || !strings.Contains(string(r.body), "from a") {
		t.Fatalf("status %d: %s", r.status, r.body)
	}
	if got := a.sent(); !slices.Equal(got, []string{"a1", "a2"}) || len(b.sent()) != 0 {
		t.Fatalf("the first target got %v, the second %v", got, b.sent())
	}
	tr := traceOf(t, h, r.header.Get(TraceHeader))
	fb := tr.GetFallbacks()
	if len(fb) != 1 || fb[0].GetPoolId() != string(pa) || fb[0].GetCredential() != "a1" || fb[0].GetAddress() != hostOf(a.srv) || !strings.Contains(fb[0].GetReason(), "429") || tr.GetCredential() != "a2" || tr.GetPoolId() != string(pa) {
		t.Fatalf("trace credential %q pool %s fallbacks %v", tr.GetCredential(), tr.GetPoolId(), fb)
	}
	if strings.Contains(tr.String(), "Bearer") {
		t.Fatal("a key reached the trace")
	}
	r = h.do(http.MethodPost, "/v1/chat/completions", chatBody)
	if tr := traceOf(t, h, r.header.Get(TraceHeader)); r.status != http.StatusOK || len(tr.GetFallbacks()) != 0 || tr.GetCredential() != "a2" {
		t.Fatalf("the cooling key is passed over at once, status %d trace %v", r.status, tr)
	}
	h.settled("chat", pa, pb)
	if n := h.g.routeState("chat").requests.Load(); n != 2 {
		t.Fatalf("route counted %d requests, want each once", n)
	}
}

// With every key of the first target refused the next target is drawn, across APIs, the body as the client sent it, in one trace
func TestDecideAgainOnTheNextTargetAcrossAPIs(t *testing.T) {
	creds := keptCreds(t, map[string][]string{"lab": {"a1", "a2"}, "lab2": {"b1"}})
	h := newHarness(t, func(s *setup) { s.creds = creds; s.runtimes.api["claude"] = v1.ApiFlavor_API_FLAVOR_ANTHROPIC })
	a, b := newKeyed(t, openaiAnswer), newKeyed(t, anthropicAnswer)
	a.refuse["a1"], a.refuse["a2"], a.body = http.StatusTooManyRequests, http.StatusTooManyRequests, `{"error":{"message":"over","type":"rate_limit_error"}}`
	pa, pb := h.provider("lab", "gpt", "vllm", a.srv.URL), h.provider("lab2", "claude", "claude", b.srv.URL)
	h.ordered("chain", "chat", &v1.RewriteTarget{PoolId: string(pa), Model: "gpt", Weight: 1}, &v1.RewriteTarget{PoolId: string(pb), Model: "claude", Weight: 1})
	r := h.do(http.MethodPost, "/v1/chat/completions", chatBody)
	if r.status != http.StatusOK || r.json(t)["object"] != "chat.completion" || !strings.Contains(string(r.body), "from b") {
		t.Fatalf("status %d: %s", r.status, r.body)
	}
	b.mu.Lock()
	got := b.bodies[0]
	b.mu.Unlock()
	if got["model"] != "claude" || got["system"] == nil || len(got["messages"].([]any)) != 1 {
		t.Fatalf("the second target got %v, not the client's body translated once", got)
	}
	tr := traceOf(t, h, r.header.Get(TraceHeader))
	fb := tr.GetFallbacks()
	if len(fb) != 3 || fb[0].GetCredential() != "a1" || fb[1].GetCredential() != "a2" || fb[2].GetPoolId() != string(pa) || !strings.Contains(fb[2].GetReason(), "spent") {
		t.Fatalf("fallbacks %v", fb)
	}
	if tr.GetCredential() != "b1" || tr.GetPoolId() != string(pb) || tr.GetUpstreamApi() != v1.ApiFlavor_API_FLAVOR_ANTHROPIC || !tr.GetTranslated() || tr.GetError() != "" || tr.GetPromptTokens() != 7 {
		t.Fatalf("trace %v", tr)
	}
	if list, _, _ := h.g.Traces().List(context.Background(), Viewer{All: true}, TraceFilter{}, 0, 0); len(list) != 1 {
		t.Fatalf("%d traces for one request", len(list))
	}
	h.settled("chat", pa, pb)
}

// A stream that began is not decided again, nor a refusal of the caller's own quota
func TestDecideAgainNeverAfterTheFirstByteOrForTheCallersQuota(t *testing.T) {
	t.Run("a stream that began", func(t *testing.T) {
		creds := keptCreds(t, map[string][]string{"lab": {"a1"}, "lab2": {"b1"}})
		h := newHarness(t, func(s *setup) { s.creds = creds })
		cut := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			io.Copy(io.Discard, r.Body)
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, "data: {\"id\":\"x\",\"object\":\"chat.completion.chunk\",\"model\":\"gpt\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"par\"}}]}\n\n")
			w.(http.Flusher).Flush()
			conn, _, err := http.NewResponseController(w).Hijack()
			if err == nil {
				conn.Close()
			}
		}))
		t.Cleanup(cut.Close)
		b := newKeyed(t, openaiAnswer)
		pa, pb := h.provider("lab", "gpt", "vllm", cut.URL), h.provider("lab2", "gpt", "vllm", b.srv.URL)
		h.ordered("chain", "chat", &v1.RewriteTarget{PoolId: string(pa), Model: "gpt", Weight: 1}, &v1.RewriteTarget{PoolId: string(pb), Model: "gpt", Weight: 1})
		r := h.do(http.MethodPost, "/v1/chat/completions", strings.Replace(chatBody, `"max_tokens":8`, `"max_tokens":8,"stream":true`, 1))
		if r.status != http.StatusOK || !strings.Contains(string(r.body), "par") || len(b.sent()) != 0 {
			t.Fatalf("status %d, second target got %v: %s", r.status, b.sent(), r.body)
		}
		if tr := traceOf(t, h, r.header.Get(TraceHeader)); len(tr.GetFallbacks()) != 0 || tr.GetError() == "" {
			t.Fatalf("trace %v", tr)
		}
		h.settled("chat", pa, pb)
	})
	t.Run("the caller's own quota", func(t *testing.T) {
		creds := keptCreds(t, map[string][]string{"lab": {"a1"}, "lab2": {"b1"}})
		flow := flowcontrol.Config{QueueCapacity: 64, QueueTimeout: 2 * time.Second, SheddableBelow: -1, Tick: 10 * time.Millisecond, Quota: flowcontrol.Quota{Requests: 1, Window: time.Hour}}
		h := newHarness(t, func(s *setup) { s.creds = creds; s.flow = &flow })
		a, b := newKeyed(t, openaiAnswer), newKeyed(t, openaiAnswer)
		pa, pb := h.provider("lab", "gpt", "vllm", a.srv.URL), h.provider("lab2", "gpt", "vllm", b.srv.URL)
		h.ordered("chain", "chat", &v1.RewriteTarget{PoolId: string(pa), Model: "gpt", Weight: 1}, &v1.RewriteTarget{PoolId: string(pb), Model: "gpt", Weight: 1})
		if r := h.do(http.MethodPost, "/v1/chat/completions", chatBody); r.status != http.StatusOK {
			t.Fatalf("under the quota %d: %s", r.status, r.body)
		}
		r := h.do(http.MethodPost, "/v1/chat/completions", chatBody)
		if r.status != http.StatusTooManyRequests || !strings.Contains(string(r.body), "quota") || len(a.sent()) != 1 || len(b.sent()) != 0 {
			t.Fatalf("status %d, targets got %v and %v: %s", r.status, a.sent(), b.sent(), r.body)
		}
		if tr := traceOf(t, h, r.header.Get(TraceHeader)); len(tr.GetFallbacks()) != 0 {
			t.Fatalf("fallbacks %v", tr.GetFallbacks())
		}
		h.settled("chat", pa, pb)
	})
}

// With nothing left the client gets the last refusal exactly as a route to that target alone answers it, nothing of the others reaching it
func TestDecideAgainLeavesTheLastRefusal(t *testing.T) {
	refusing := func(t *testing.T) (*harness, datalayer.PoolID, datalayer.PoolID) {
		creds := keptCreds(t, map[string][]string{"lab": {"a1"}, "lab2": {"b1"}})
		h := newHarness(t, func(s *setup) { s.creds = creds })
		a, b := newKeyed(t, openaiAnswer), newKeyed(t, openaiAnswer)
		a.refuse["a1"], a.header, a.body = http.StatusServiceUnavailable, http.Header{"X-Who": {"a"}}, `{"error":{"message":"a is down"}}`
		b.refuse["b1"], b.header, b.body = http.StatusTooManyRequests, http.Header{"Retry-After": {"7"}, "X-Who": {"b"}}, `{"error":{"message":"b is full","type":"rate_limit_error"}}`
		pa, pb := h.provider("lab", "gpt", "vllm", a.srv.URL), h.provider("lab2", "gpt", "vllm", b.srv.URL)
		h.ordered("chain", "chat", &v1.RewriteTarget{PoolId: string(pa), Model: "gpt", Weight: 1}, &v1.RewriteTarget{PoolId: string(pb), Model: "gpt", Weight: 1})
		h.ordered("alone", "alone", &v1.RewriteTarget{PoolId: string(pb), Model: "gpt", Weight: 1})
		return h, pa, pb
	}
	h, _, pb := refusing(t)
	alone := h.do(http.MethodPost, "/v1/chat/completions", strings.Replace(chatBody, `"model":"chat"`, `"model":"alone"`, 1))
	h.settled("alone", pb)
	h, pa, pb := refusing(t)
	r := h.do(http.MethodPost, "/v1/chat/completions", chatBody)
	if r.status != http.StatusTooManyRequests || r.status != alone.status || string(r.body) != string(alone.body) || r.header.Get("Retry-After") != "7" || r.header.Get("X-Who") != "b" {
		t.Fatalf("got %d %v %s, a route to the target alone answers %d %v %s", r.status, r.header, r.body, alone.status, alone.header, alone.body)
	}
	tr := traceOf(t, h, r.header.Get(TraceHeader))
	if fb := tr.GetFallbacks(); len(fb) != 1 || fb[0].GetPoolId() != string(pa) || tr.GetPoolId() != string(pb) || tr.GetStatus() != http.StatusTooManyRequests || tr.GetCredential() != "b1" {
		t.Fatalf("trace pool %s status %d fallbacks %v", tr.GetPoolId(), tr.GetStatus(), tr.GetFallbacks())
	}
	h.settled("chat", pa, pb)
}

// Usage is recorded against the credential that answered, tokens summed over every tool round and read from an answer passed through
func TestUsageRecordedOverRoundsAndPassthrough(t *testing.T) {
	creds := keptCreds(t, map[string][]string{"lab": {"a1"}, "lab2": {"b1"}})
	h := newHarness(t, func(s *setup) { s.creds = creds })
	upstream := httptest.NewServer(&toolRounds{})
	t.Cleanup(upstream.Close)
	mcpSrv := fakeMCP(t)
	h.g.tools.Apply(context.Background(), []*v1.ToolServer{{Name: "fake", Enabled: true, Kind: v1.ToolServerKind_TOOL_SERVER_KIND_HTTP, Url: mcpSrv.URL}})
	t.Cleanup(h.g.tools.Close)
	pt := h.provider("lab", "tools", "vllm", upstream.URL)
	h.ordered("tools", "tools", &v1.RewriteTarget{PoolId: string(pt), Model: "tools", Weight: 1})
	if r := h.do(http.MethodPost, "/v1/responses", `{"model":"tools","input":"say hi"}`, ToolsHeader, "fake"); r.status != http.StatusOK {
		t.Fatalf("status %d: %s", r.status, r.body)
	}
	stream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"id\":\"x\",\"object\":\"chat.completion.chunk\",\"model\":\"gpt\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"}}]}\n\n")
		io.WriteString(w, "data: {\"id\":\"x\",\"object\":\"chat.completion.chunk\",\"model\":\"gpt\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":11,\"completion_tokens\":4,\"total_tokens\":15,\"prompt_tokens_details\":{\"cached_tokens\":6}}}\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(stream.Close)
	ps := h.provider("lab2", "gpt", "vllm", stream.URL)
	h.ordered("chain", "chat", &v1.RewriteTarget{PoolId: string(ps), Model: "gpt", Weight: 1})
	if r := h.do(http.MethodPost, "/v1/chat/completions", strings.Replace(chatBody, `"max_tokens":8`, `"max_tokens":8,"stream":true`, 1)); r.status != http.StatusOK {
		t.Fatalf("status %d: %s", r.status, r.body)
	}
	h.settled("chat", pt, ps)
	used, err := creds.Usage(context.Background(), auth.ProviderDaemon, "", time.Time{}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	byModel := map[string]*v1.CredentialUsage{}
	for _, u := range used {
		byModel[u.GetModel()] = u
	}
	list, _ := creds.List(context.Background(), auth.ProviderDaemon)
	names := map[string]string{}
	for _, c := range list {
		names[c.GetId()] = c.GetName()
	}
	if u := byModel["tools"]; u == nil || names[u.GetCredentialId()] != "a1" || u.GetRequests() != 1 || u.GetInputTokens() != 40 || u.GetOutputTokens() != 8 {
		t.Fatalf("tool rounds usage %v", u)
	}
	if u := byModel["gpt"]; u == nil || names[u.GetCredentialId()] != "b1" || u.GetRequests() != 1 || u.GetInputTokens() != 11 || u.GetOutputTokens() != 4 || u.GetCachedTokens() != 6 {
		t.Fatalf("passed through usage %v", u)
	}
}

// A request naming no model, refused by the one pool serving, gets that refusal once the pool is left
func TestDecideAgainEndsWithNoModelNamed(t *testing.T) {
	h := newHarness(t)
	var calls atomic.Int32
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		calls.Add(1)
		http.Error(w, `{"error":{"message":"down"}}`, http.StatusServiceUnavailable)
	}))
	t.Cleanup(down.Close)
	pool := h.publish("", "stub", "vllm", datalayer.AppProtocolHTTP, true, pools.RouteSpec{}, down.URL)
	r := h.do(http.MethodPost, "/v1/chat/completions", `{"messages":[{"role":"user","content":"hi"}],"max_tokens":4}`)
	if r.status != http.StatusBadGateway || calls.Load() != 1 || !strings.Contains(string(r.body), "down") {
		t.Fatalf("status %d after %d calls: %s", r.status, calls.Load(), r.body)
	}
	if tr := traceOf(t, h, r.header.Get(TraceHeader)); len(tr.GetFallbacks()) != 0 || tr.GetPoolId() != string(pool) {
		t.Fatalf("trace pool %s fallbacks %v", tr.GetPoolId(), tr.GetFallbacks())
	}
}
