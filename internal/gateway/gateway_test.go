package gateway

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nickheyer/nebu/internal/db"
	"github.com/nickheyer/nebu/internal/instances"
	"github.com/nickheyer/nebu/internal/mesh"
	"github.com/nickheyer/nebu/internal/pools"
	"github.com/nickheyer/nebu/pkg/datalayer"
	"github.com/nickheyer/nebu/pkg/flowcontrol"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/requestcontrol"
)

const chatBody = `{"model":"chat","messages":[{"role":"system","content":"be brief"},{"role":"user","content":"hello there"}],"max_tokens":8}`

// Every client protocol reaches an OpenAI runtime, passed through when it speaks OpenAI and translated otherwise
func TestProtocols(t *testing.T) {
	h := newHarness(t)
	_, srv := h.stub("stub-model")
	h.publish("chat", "stub", "vllm", datalayer.AppProtocolHTTP, true, pools.RouteSpec{ServedName: "stub-model"}, srv.URL)
	cases := []struct {
		name, path, body string
		header           []string
		check            func(t *testing.T, r reply)
		translated       bool
	}{
		{"openai chat", "/v1/chat/completions", chatBody, nil, func(t *testing.T, r reply) {
			if r.json(t)["object"] != "chat.completion" {
				t.Fatalf("not a chat completion: %s", r.body)
			}
		}, false},
		{"openai completions", "/v1/completions", `{"model":"chat","prompt":"once upon","max_tokens":4}`, nil, func(t *testing.T, r reply) {
			if r.json(t)["object"] != "text_completion" {
				t.Fatalf("not a text completion: %s", r.body)
			}
		}, false},
		{"anthropic messages", "/v1/messages", `{"model":"chat","max_tokens":8,"system":"be brief","messages":[{"role":"user","content":"hello there"}]}`, []string{"anthropic-version", "2023-06-01"}, func(t *testing.T, r reply) {
			if r.json(t)["type"] != "message" {
				t.Fatalf("not an anthropic message: %s", r.body)
			}
		}, true},
		{"ollama chat", "/api/chat", `{"model":"chat","stream":false,"messages":[{"role":"user","content":"hello there"}],"options":{"num_predict":8}}`, nil, func(t *testing.T, r reply) {
			m, _ := r.json(t)["message"].(map[string]any)
			if m["role"] != "assistant" || m["content"] == "" {
				t.Fatalf("not an ollama chat answer: %s", r.body)
			}
		}, true},
		{"ollama generate", "/api/generate", `{"model":"chat","stream":false,"prompt":"once upon","options":{"num_predict":4}}`, nil, func(t *testing.T, r reply) {
			if r.json(t)["done"] != true {
				t.Fatalf("not an ollama generate answer: %s", r.body)
			}
		}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := h.do(http.MethodPost, tc.path, tc.body, tc.header...)
			if r.status != http.StatusOK {
				t.Fatalf("status %d: %s", r.status, r.body)
			}
			tc.check(t, r)
			if r.header.Get(requestcontrol.HeaderServed) != hostOf(srv) {
				t.Fatalf("served header %q, want %q", r.header.Get(requestcontrol.HeaderServed), hostOf(srv))
			}
			id := r.header.Get(TraceHeader)
			waitFor(t, "trace "+id, func() bool {
				tr, ok, _ := h.g.Traces().Get(context.Background(), Viewer{All: true}, id)
				return ok && tr.GetFinishedAt() != nil
			})
			tr, _, _ := h.g.Traces().Get(context.Background(), Viewer{All: true}, id)
			if tr.GetTranslated() != tc.translated || tr.GetEndpoint() != hostOf(srv) || tr.GetPoolId() == "" || tr.GetRequestId() == "" {
				t.Fatalf("trace translated %v endpoint %q pool %q request %q", tr.GetTranslated(), tr.GetEndpoint(), tr.GetPoolId(), tr.GetRequestId())
			}
			if tr.GetCompletionTokens() == 0 {
				t.Fatalf("trace kept no usage: %+v", tr)
			}
		})
	}
}

// A streamed answer arrives event by event with the served endpoint header and the request id echoed
func TestStreamingServedEndpoint(t *testing.T) {
	h := newHarness(t)
	_, srv := h.stub("stub-model")
	h.publish("chat", "stub", "vllm", datalayer.AppProtocolHTTP, true, pools.RouteSpec{ServedName: "stub-model"}, srv.URL)
	body := strings.Replace(chatBody, `"max_tokens":8`, `"max_tokens":8,"stream":true`, 1)
	r := h.do(http.MethodPost, "/v1/chat/completions", body, requestcontrol.HeaderRequestID, "req-42")
	if r.status != http.StatusOK || !strings.HasPrefix(r.header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("status %d type %q: %s", r.status, r.header.Get("Content-Type"), r.body)
	}
	if got := r.header.Get(requestcontrol.HeaderServed); got != hostOf(srv) {
		t.Fatalf("served header %q", got)
	}
	if got := r.header.Get(requestcontrol.HeaderRequestID); got != "req-42" {
		t.Fatalf("request id %q", got)
	}
	payloads := ssePayloads(r.body)
	if len(payloads) < 3 || payloads[len(payloads)-1] != "[DONE]" {
		t.Fatalf("stream payloads %q", payloads)
	}
	id := r.header.Get(TraceHeader)
	waitFor(t, "streamed trace", func() bool {
		tr, ok, _ := h.g.Traces().Get(context.Background(), Viewer{All: true}, id)
		return ok && tr.GetFinishedAt() != nil && tr.GetFirstTokenAt() != nil && tr.GetResponse() != ""
	})
}

// A backend refusing with 429 passes the request to the next endpoint before any byte reaches the client, the retry marked on the trace
func TestFallbackBeforeBytes(t *testing.T) {
	for round := range 3 {
		t.Run(fmt.Sprint("round ", round), func(t *testing.T) {
			h := newHarness(t)
			var refused, afterBytes atomic.Int32
			var clientGotBytes atomic.Bool
			busy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if clientGotBytes.Load() {
					afterBytes.Add(1)
				}
				refused.Add(1)
				w.Header().Set("Retry-After", "3")
				w.WriteHeader(http.StatusTooManyRequests)
				io.WriteString(w, `{"error":{"message":"busy","type":"rate_limit_error"}}`)
			}))
			defer busy.Close()
			_, good := h.stub("stub-model")
			pool := h.publish("chat", "stub", "vllm", datalayer.AppProtocolHTTP, true, pools.RouteSpec{ServedName: "stub-model"}, busy.URL, good.URL)
			doc, err := parseDoc("/v1/chat/completions", []byte(chatBody))
			if err != nil {
				t.Fatal(err)
			}
			for _, ep := range h.store.Endpoints(pool, nil) {
				if ep.Address().String() == hostOf(busy) {
					h.g.index.Record(ep.ID(), h.g.index.Hash("stub-model", "", doc.PromptText(), nil))
				}
			}
			r := h.do(http.MethodPost, "/v1/chat/completions", chatBody)
			clientGotBytes.Store(true)
			if r.status != http.StatusOK || r.header.Get(requestcontrol.HeaderServed) != hostOf(good) {
				t.Fatalf("status %d served %q: %s", r.status, r.header.Get(requestcontrol.HeaderServed), r.body)
			}
			id := r.header.Get(TraceHeader)
			waitFor(t, "fallback trace", func() bool {
				tr, ok, _ := h.g.Traces().Get(context.Background(), Viewer{All: true}, id)
				return ok && tr.GetFinishedAt() != nil
			})
			tr, _, _ := h.g.Traces().Get(context.Background(), Viewer{All: true}, id)
			if fb := tr.GetFallbacks(); len(fb) != 1 || fb[0].GetAddress() != hostOf(busy) || !fb[0].GetRequestWritten() || fb[0].GetBytesToClient() != 0 || !strings.Contains(fb[0].GetReason(), "429") {
				t.Fatalf("fallbacks %v after the busy endpoint refused %d times", fb, refused.Load())
			}
			if refused.Load() != 1 || afterBytes.Load() != 0 {
				t.Fatalf("busy endpoint saw %d requests, %d after the client got bytes", refused.Load(), afterBytes.Load())
			}
		})
	}
}

// Every refusal answers its own status, with Retry-After where a wait helps
func TestStatusTable(t *testing.T) {
	flow := flowcontrol.Config{QueueCapacity: 64, QueueTimeout: 5 * time.Second, SheddableBelow: -1, Tick: 10 * time.Millisecond}
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(1500 * time.Millisecond):
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"x","object":"chat.completion","model":"slow","choices":[{"index":0,"message":{"role":"assistant","content":"late"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	defer slow.Close()
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		io.WriteString(w, `{"error":{"message":"engine died"}}`)
	}))
	defer failing.Close()
	closed := httptest.NewServer(http.NotFoundHandler())
	closedAddr := closed.URL
	closed.Close()

	h := newHarness(t, func(s *setup) { s.flow = &flow })
	_, good := h.stub("stub-model")
	ctx := context.Background()
	h.publish("chat", "stub", "vllm", datalayer.AppProtocolHTTP, true, pools.RouteSpec{ServedName: "stub-model"}, good.URL)
	h.publish("second", "other", "vllm", datalayer.AppProtocolHTTP, true, pools.RouteSpec{ServedName: "stub-model"}, good.URL)
	h.publish("starting", "starting", "vllm", datalayer.AppProtocolHTTP, false, pools.RouteSpec{}, "127.0.0.1:1")
	h.publish("slow", "slow", "vllm", datalayer.AppProtocolHTTP, true, pools.RouteSpec{ServedName: "slow", Policy: &v1.Policy{UpstreamTimeoutMs: 100}}, slow.URL)
	h.publish("limited", "limited", "vllm", datalayer.AppProtocolHTTP, true, pools.RouteSpec{ServedName: "slow", Policy: &v1.Policy{MaxInFlight: 1}}, slow.URL)
	h.publish("rated", "rated", "vllm", datalayer.AppProtocolHTTP, true, pools.RouteSpec{ServedName: "stub-model", Policy: &v1.Policy{RequestsPerSecond: 0.5, Burst: 1}}, good.URL)
	h.publish("broken", "broken", "vllm", datalayer.AppProtocolHTTP, true, pools.RouteSpec{ServedName: "x"}, failing.URL)
	h.publish("gone", "gone", "vllm", datalayer.AppProtocolHTTP, true, pools.RouteSpec{ServedName: "x"}, closedAddr)
	closePool := h.publish("closed", "fc", "vllm", datalayer.AppProtocolHTTP, true, pools.RouteSpec{ServedName: "stub-model"}, good.URL)
	openPool := h.publish("opened", "fo", "vllm", datalayer.AppProtocolHTTP, true, pools.RouteSpec{ServedName: "stub-model"}, good.URL)
	if _, err := h.mgr.PutSchedulerConfig(ctx, &v1.SchedulerConfig{Name: "unreachable", Spec: &v1.SchedulerConfigSpec{Yaml: mediaConfig + "remote:\n  url: " + closedAddr + "\n  timeout: 1s\n"}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "unreachable remote configuration hosted", func() bool { _, ok := h.g.hosting.get("unreachable"); return ok })
	unknownPool := h.publish("unknown", "fu", "vllm", datalayer.AppProtocolHTTP, true, pools.RouteSpec{ServedName: "stub-model"}, good.URL)
	goodAddr, _ := datalayer.ParseAddress(hostOf(good))
	for id, set := range map[datalayer.PoolID]struct {
		model, config string
		mode          v1.FailureMode
	}{
		closePool:   {"fc", "unreachable", v1.FailureMode_FAILURE_MODE_FAIL_CLOSE},
		openPool:    {"fo", "unreachable", v1.FailureMode_FAILURE_MODE_FAIL_OPEN},
		unknownPool: {"fu", "missing", v1.FailureMode_FAILURE_MODE_FAIL_OPEN},
	} {
		if _, err := h.mgr.PutPool(ctx, string(id), &v1.PoolSpec{Model: key(set.model).String(), RuntimeId: "vllm", TargetPorts: []uint32{uint32(goodAddr.Port)}, FailureMode: set.mode, SchedulerConfig: set.config}); err != nil {
			t.Fatal(err)
		}
	}
	saturated := h.publish("saturated", "sat", "vllm", datalayer.AppProtocolHTTP, true, pools.RouteSpec{ServedName: "stub-model"}, good.URL)
	for _, ep := range h.store.Endpoints(saturated, nil) {
		h.store.SetAttribute(ep.ID(), datalayer.CollectorCapacity, 0)
	}
	body := func(model string) string {
		return strings.Replace(chatBody, `"model":"chat"`, `"model":"`+model+`"`, 1)
	}

	var held sync.WaitGroup
	held.Go(func() { h.do(http.MethodPost, "/v1/chat/completions", body("limited")) })
	waitFor(t, "limited request in flight", func() bool { return h.g.routeState("limited").inflight.Load() == 1 })
	if r := h.do(http.MethodPost, "/v1/chat/completions", body("rated")); r.status != http.StatusOK {
		t.Fatalf("first rated request %d: %s", r.status, r.body)
	}

	cases := []struct {
		name   string
		body   string
		header []string
		status int
		retry  bool
	}{
		{"policy rate", body("rated"), nil, http.StatusTooManyRequests, true},
		{"policy in flight cap", body("limited"), nil, http.StatusOK, false},
		{"unknown model", body("nope"), nil, http.StatusNotFound, false},
		{"no model with two served", `{"messages":[{"role":"user","content":"hi"}]}`, nil, http.StatusBadRequest, false},
		{"pending route", body("starting"), nil, http.StatusServiceUnavailable, true},
		{"no ready endpoint", body(key("starting").String()), nil, http.StatusServiceUnavailable, true},
		{"shed by flow control", body("saturated"), []string{requestcontrol.HeaderObjective, "-1"}, http.StatusTooManyRequests, true},
		{"upstream failure", body("broken"), nil, http.StatusBadGateway, false},
		{"upstream unreachable", body("gone"), nil, http.StatusBadGateway, false},
		{"upstream timeout", body("slow"), nil, http.StatusGatewayTimeout, false},
		{"scheduler unavailable fail close", body("closed"), nil, http.StatusServiceUnavailable, true},
		{"scheduler unavailable fail open", body("opened"), nil, http.StatusOK, false},
		{"unknown scheduler config fails closed under fail open", body("unknown"), nil, http.StatusServiceUnavailable, true},
		{"bad mode header", body("chat"), []string{GhostHeader, "maybe"}, http.StatusBadRequest, false},
		{"bad objective", body("chat"), []string{requestcontrol.HeaderObjective, "gold"}, http.StatusBadRequest, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := h.do(http.MethodPost, "/v1/chat/completions", tc.body, tc.header...)
			if r.status != tc.status {
				t.Fatalf("status %d, want %d: %s", r.status, tc.status, r.body)
			}
			if tc.retry != (r.header.Get("Retry-After") != "") {
				t.Fatalf("retry-after %q", r.header.Get("Retry-After"))
			}
		})
	}
	held.Wait()
	r := h.do(http.MethodPost, "/v1/chat/completions", body("opened"))
	id := r.header.Get(TraceHeader)
	waitFor(t, "fail open trace", func() bool {
		tr, ok, _ := h.g.Traces().Get(ctx, Viewer{All: true}, id)
		return ok && tr.GetFinishedAt() != nil
	})
	if tr, _, _ := h.g.Traces().Get(ctx, Viewer{All: true}, id); !tr.GetFailedOpen() {
		t.Fatal("a request routed past a failed scheduler was not marked failed open")
	}
}

// Named objectives and fairness ids reach flow control and land on metrics and traces
func TestObjectiveAndFairness(t *testing.T) {
	h := newHarness(t, func(s *setup) { s.keys = []string{"alice-key"} })
	ctx := context.Background()
	if _, err := h.mgr.PutObjective(ctx, &v1.Objective{Name: "gold", Priority: 7, TtftMs: 5000}); err != nil {
		t.Fatal(err)
	}
	_, srv := h.stub("stub-model")
	pool := h.publish("chat", "stub", "vllm", datalayer.AppProtocolHTTP, true, pools.RouteSpec{ServedName: "stub-model"}, srv.URL)
	r := h.do(http.MethodPost, "/v1/chat/completions", chatBody, "Authorization", "Bearer alice-key", requestcontrol.HeaderObjective, "gold", requestcontrol.HeaderFairnessID, "tenant-a")
	if r.status != http.StatusOK {
		t.Fatalf("status %d: %s", r.status, r.body)
	}
	id := r.header.Get(TraceHeader)
	waitFor(t, "objective trace", func() bool {
		tr, ok, _ := h.g.Traces().Get(ctx, Viewer{All: true}, id)
		return ok && tr.GetFinishedAt() != nil
	})
	tr, _, _ := h.g.Traces().Get(ctx, Viewer{All: true}, id)
	if tr.GetPriority() != 7 || tr.GetObjective() != "gold" || tr.GetFairnessId() != "tenant-a" || !tr.GetObjectiveAttained() {
		t.Fatalf("trace priority %d objective %q fairness %q attained %v", tr.GetPriority(), tr.GetObjective(), tr.GetFairnessId(), tr.GetObjectiveAttained())
	}
	page := h.do(http.MethodGet, "/metrics", "", "Authorization", "Bearer alice-key")
	for _, want := range []string{
		fmt.Sprintf(`nebu_request_queue_wait_seconds_count{endpoint="%s",fairness_id="tenant-a",model="stub-model",pool="%s",priority="7"} 1`, h.endpointID(pool), pool),
		fmt.Sprintf(`nebu_objective_attained_total{attained="true",objective="ttft",pool="%s",priority="7"} 1`, pool),
	} {
		if !strings.Contains(string(page.body), want) {
			t.Fatalf("metrics lack %s", want)
		}
	}
	r = h.do(http.MethodPost, "/v1/chat/completions", chatBody, "Authorization", "Bearer alice-key")
	waitFor(t, "default fairness", func() bool {
		tr, ok, _ := h.g.Traces().Get(ctx, Viewer{All: true}, r.header.Get(TraceHeader))
		return ok && tr.GetFinishedAt() != nil
	})
	tr, _, _ = h.g.Traces().Get(ctx, Viewer{All: true}, r.header.Get(TraceHeader))
	if tr.GetFairnessId() != keyOwner("alice-key") {
		t.Fatalf("fairness %q, want the key's owner", tr.GetFairnessId())
	}
	waitFor(t, "both requests counted on their target", func() bool { return h.g.Outcome(pool, "stub-model").Requests == 2 })
	if o := h.g.Outcome(pool, "stub-model"); o.Judged != 1 || o.Met != 1 || o.Errors != 0 || o.TTFTs != 2 || o.TTFT <= 0 {
		t.Fatalf("outcome %+v", o)
	}
	if o := h.g.Outcome(pool, "other"); o != (pools.Outcome{}) {
		t.Fatalf("an unused target counted %+v", o)
	}
}

// A pool naming a configuration that appears later is scheduled once the manager publishes it, and swapped on change
func TestSchedulerHotSwap(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	_, srv := h.stub("stub-model")
	owner := pools.OwnerRef{Kind: pools.OwnerInstance, ID: "custom-1"}
	addr, _ := datalayer.ParseAddress(hostOf(srv))
	if err := h.mgr.Publish(ctx, owner, pools.Membership{Model: key("custom"), Runtime: "vllm", Protocol: datalayer.AppProtocolHTTP, Concurrency: 4, SchedulerConfig: "custom", Metrics: &v1.MetricNames{HealthPath: "/health"}}, []pools.Published{{InstanceID: owner.ID, NodeID: testNode, Address: addr, Ready: true}}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.mgr.NameRoute(ctx, owner, "chat", pools.RouteSpec{ServedName: "stub-model"}); err != nil {
		t.Fatal(err)
	}
	if r := h.do(http.MethodPost, "/v1/chat/completions", chatBody); r.status != http.StatusServiceUnavailable {
		t.Fatalf("status %d before the configuration exists: %s", r.status, r.body)
	}
	random := strings.Replace(mediaConfig, "max-score-picker", "random-picker", 1)
	if _, err := h.mgr.PutSchedulerConfig(ctx, &v1.SchedulerConfig{Name: "custom", Spec: &v1.SchedulerConfigSpec{Yaml: mediaConfig}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "custom configuration hosted", func() bool { _, ok := h.g.hosting.get("custom"); return ok })
	if r := h.do(http.MethodPost, "/v1/chat/completions", chatBody); r.status != http.StatusOK {
		t.Fatalf("status %d after the configuration exists: %s", r.status, r.body)
	}
	hs, _ := h.g.hosting.get("custom")
	before := hs.cur.Load()
	if _, err := h.mgr.PutSchedulerConfig(ctx, &v1.SchedulerConfig{Name: "custom", Spec: &v1.SchedulerConfigSpec{Yaml: random}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "custom configuration swapped", func() bool { return hs.cur.Load() != before && hs.cur.Load().yaml == random })
	if r := h.do(http.MethodPost, "/v1/chat/completions", chatBody); r.status != http.StatusOK {
		t.Fatalf("status %d after the swap: %s", r.status, r.body)
	}
}

// A Responses request reaches a runtime serving the Responses API as sent, the model renamed
func TestResponsesPassthrough(t *testing.T) {
	h := newHarness(t)
	var got struct {
		sync.Mutex
		path, model string
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		got.Lock()
		got.path, got.model = r.URL.Path, fmt.Sprint(body["model"])
		got.Unlock()
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"resp_1","object":"response","status":"completed","model":"stub-model","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hi"}]}],"usage":{"input_tokens":3,"output_tokens":1,"total_tokens":4}}`)
	}))
	defer srv.Close()
	h.publish("chat", "stub", "vllm", datalayer.AppProtocolHTTP, true, pools.RouteSpec{ServedName: "stub-model"}, srv.URL)
	r := h.do(http.MethodPost, "/v1/responses", `{"model":"chat","input":"hello","store":true,"previous_response_id":"resp_0"}`)
	if r.status != http.StatusOK || r.json(t)["object"] != "response" {
		t.Fatalf("status %d: %s", r.status, r.body)
	}
	got.Lock()
	defer got.Unlock()
	if got.path != "/v1/responses" || got.model != "stub-model" {
		t.Fatalf("backend saw %s with model %s", got.path, got.model)
	}
}

// Traces are kept under the caller and persisted, each caller listing their own and an admin every one
func TestTracesPersistedAndScoped(t *testing.T) {
	h := newHarness(t, func(s *setup) { s.keys = []string{"alice-key", "bob-key"} })
	ctx := context.Background()
	_, srv := h.stub("stub-model")
	h.publish("chat", "stub", "vllm", datalayer.AppProtocolHTTP, true, pools.RouteSpec{ServedName: "stub-model"}, srv.URL)
	ids := map[string]string{}
	for _, who := range []string{"alice", "bob"} {
		r := h.do(http.MethodPost, "/v1/chat/completions", chatBody, "Authorization", "Bearer "+who+"-key")
		if r.status != http.StatusOK {
			t.Fatalf("%s status %d: %s", who, r.status, r.body)
		}
		ids[who] = r.header.Get(TraceHeader)
	}
	ghost := h.do(http.MethodPost, "/v1/chat/completions", chatBody, "Authorization", "Bearer alice-key", GhostHeader, "1")
	if ghost.status != http.StatusOK || ghost.header.Get(TraceHeader) != "" {
		t.Fatalf("ghost request %d traced as %q", ghost.status, ghost.header.Get(TraceHeader))
	}
	alice := Viewer{Owner: keyOwner("alice-key")}
	if v, ok := h.g.ViewerFor(http.Header{"Authorization": {"Bearer alice-key"}}); !ok || v != alice {
		t.Fatalf("viewer %+v %v", v, ok)
	}
	if _, ok := h.g.ViewerFor(http.Header{}); ok {
		t.Fatal("a caller without credentials got a viewer")
	}
	waitFor(t, "persisted traces", func() bool {
		all, err := h.db.ListTraces(ctx, db.TraceQuery{AllOwners: true})
		return err == nil && len(all) == 2
	})
	list, err := h.g.Traces().List(ctx, alice, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].GetId() != ids["alice"] || list[0].GetRequest() != "" {
		t.Fatalf("alice lists %v", list)
	}
	if _, ok, _ := h.g.Traces().Get(ctx, alice, ids["bob"]); ok {
		t.Fatal("alice read bob's trace")
	}
	tr, ok, err := h.g.Traces().Get(ctx, alice, ids["alice"])
	if err != nil || !ok || tr.GetRequest() != chatBody {
		t.Fatalf("alice's trace %v %v body %q", ok, err, tr.GetRequest())
	}
	stored, err := h.db.GetTrace(ctx, keyOwner("bob-key"), false, ids["bob"])
	if err != nil || stored.GetStatus() != http.StatusOK || stored.GetEndpoint() != hostOf(srv) {
		t.Fatalf("stored trace %v %v", stored, err)
	}
	if every, _ := h.g.Traces().List(ctx, Viewer{All: true}, "chat", 10); len(every) != 2 {
		t.Fatalf("admin lists %d", len(every))
	}
}

// The metrics endpoint serves request families after traffic
func TestMetricsEndpoint(t *testing.T) {
	h := newHarness(t)
	_, srv := h.stub("stub-model")
	pool := h.publish("chat", "stub", "vllm", datalayer.AppProtocolHTTP, true, pools.RouteSpec{ServedName: "stub-model"}, srv.URL)
	h.do(http.MethodPost, "/v1/chat/completions", chatBody)
	page := h.do(http.MethodGet, "/metrics", "")
	if page.status != http.StatusOK {
		t.Fatalf("metrics status %d", page.status)
	}
	for _, want := range []string{"nebu_request_total", "nebu_request_ttft_seconds", "nebu_scheduler_pick_duration_seconds", fmt.Sprintf(`nebu_pool_ready_endpoints{pool="%s"} 1`, pool)} {
		waitFor(t, want, func() bool { return strings.Contains(string(h.do(http.MethodGet, "/metrics", "").body), want) })
	}
}

// Readiness fails until the endpoint set synced and passes once it has, liveness passing throughout
func TestReadiness(t *testing.T) {
	h := newHarness(t, func(s *setup) { s.unsynced = true })
	if r := h.do(http.MethodGet, "/livez", ""); r.status != http.StatusOK {
		t.Fatalf("livez %d", r.status)
	}
	if r := h.do(http.MethodGet, "/readyz", ""); r.status != http.StatusServiceUnavailable {
		t.Fatalf("readyz before sync %d", r.status)
	}
	h.store.MarkSynced()
	if r := h.do(http.MethodGet, "/readyz", ""); r.status != http.StatusOK {
		t.Fatalf("readyz after sync %d: %s", r.status, r.body)
	}
}

// A request that crossed the most gateways allowed is refused before routing
func TestHopLimit(t *testing.T) {
	h := newHarness(t)
	_, srv := h.stub("stub-model")
	h.publish("chat", "stub", "vllm", datalayer.AppProtocolHTTP, true, pools.RouteSpec{ServedName: "stub-model"}, srv.URL)
	if r := h.do(http.MethodPost, "/v1/chat/completions", chatBody, mesh.HopHeader, fmt.Sprint(mesh.MaxHops)); r.status != statusLoop {
		t.Fatalf("status %d at the hop limit: %s", r.status, r.body)
	}
	if r := h.do(http.MethodPost, "/v1/chat/completions", chatBody, mesh.HopHeader, fmt.Sprint(mesh.MaxHops-1)); r.status != http.StatusOK {
		t.Fatalf("status %d under the hop limit: %s", r.status, r.body)
	}
}

// A pool declaring h2c is reached over cleartext HTTP/2 and gateway headers never reach it
func TestH2CAndHeaders(t *testing.T) {
	h := newHarness(t)
	var proto atomic.Int32
	var leaked atomic.Value
	stubSrv, _ := h.stub("stub-model")
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proto.Store(int32(r.ProtoMajor))
		for k := range r.Header {
			if strings.HasPrefix(k, "X-Nebu-") || k == mesh.HopHeader || k == "Authorization" {
				leaked.Store(k)
			}
		}
		if r.Header.Get(requestcontrol.HeaderRequestID) == "" || r.Header.Get("Traceparent") == "" {
			leaked.Store("missing request id or trace context")
		}
		stubSrv.ServeHTTP(w, r)
	}))
	srv.Config.Protocols = new(http.Protocols)
	srv.Config.Protocols.SetUnencryptedHTTP2(true)
	srv.Start()
	defer srv.Close()
	h.publish("chat", "stub", "vllm", datalayer.AppProtocolH2C, true, pools.RouteSpec{ServedName: "stub-model"}, srv.URL)
	system := base64.StdEncoding.EncodeToString([]byte("answer in French"))
	r := h.do(http.MethodPost, "/v1/chat/completions", chatBody, SystemHeader, system, mesh.HopHeader, "1", ToolsHeader, "")
	if r.status != http.StatusOK {
		t.Fatalf("status %d: %s", r.status, r.body)
	}
	if proto.Load() != 2 {
		t.Fatalf("backend spoke HTTP/%d", proto.Load())
	}
	if v := leaked.Load(); v != nil {
		t.Fatalf("backend got %v", v)
	}
	id := r.header.Get(TraceHeader)
	waitFor(t, "system trace", func() bool {
		tr, ok, _ := h.g.Traces().Get(context.Background(), Viewer{All: true}, id)
		return ok && strings.Contains(tr.GetUpstreamRequest(), "answer in French") && !strings.Contains(tr.GetUpstreamRequest(), "be brief")
	})
}

// A vLLM pool scraped by the manager's sources runs the metrics aware default configuration
func TestScrapedPool(t *testing.T) {
	h := newHarness(t, func(s *setup) { s.scrape = true })
	ctx := context.Background()
	_, srv := h.stub("stub-model")
	owner := pools.OwnerRef{Kind: pools.OwnerInstance, ID: "v1"}
	addr, _ := datalayer.ParseAddress(hostOf(srv))
	if err := h.mgr.Publish(ctx, owner, pools.Membership{Model: key("scraped"), Runtime: "vllm", Protocol: datalayer.AppProtocolHTTP, Concurrency: 4}, []pools.Published{{InstanceID: owner.ID, NodeID: testNode, Address: addr, Ready: true}}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.mgr.NameRoute(ctx, owner, "chat", pools.RouteSpec{ServedName: "stub-model"}); err != nil {
		t.Fatal(err)
	}
	pool := pools.PoolIDOf(key("scraped"), "vllm", "")
	waitFor(t, "scraped metrics", func() bool {
		eps := h.store.Endpoints(pool, nil)
		if len(eps) != 1 {
			return false
		}
		m, ok := datalayer.Attribute[*datalayer.ServerMetrics](eps[0], datalayer.CollectorMetrics)
		return ok && m.NumGPUBlocks > 0
	})
	r := h.do(http.MethodPost, "/v1/chat/completions", chatBody)
	if r.status != http.StatusOK {
		t.Fatalf("status %d: %s", r.status, r.body)
	}
	id := r.header.Get(TraceHeader)
	waitFor(t, "scraped trace", func() bool {
		tr, ok, _ := h.g.Traces().Get(ctx, Viewer{All: true}, id)
		return ok && tr.GetFinishedAt() != nil && tr.GetProfile() == "default" && tr.GetScore() > 0
	})
}

// Callers need a gateway key or the inference scope, and browsers get CORS answers for listed origins
func TestAuthAndCORS(t *testing.T) {
	h := newHarness(t, func(s *setup) { s.keys = []string{"k1"} })
	_, srv := h.stub("stub-model")
	h.publish("chat", "stub", "openai", datalayer.AppProtocolHTTP, true, pools.RouteSpec{ServedName: "stub-model"}, srv.URL)
	if r := h.do(http.MethodPost, "/v1/chat/completions", chatBody); r.status != http.StatusUnauthorized || r.header.Get("WWW-Authenticate") == "" {
		t.Fatalf("keyless request %d", r.status)
	}
	if r := h.do(http.MethodGet, "/v1/models", "", "X-Api-Key", "k1"); r.status != http.StatusOK || !strings.Contains(string(r.body), `"id":"chat"`) {
		t.Fatalf("models %d: %s", r.status, r.body)
	}
	pre := h.do(http.MethodOptions, "/v1/chat/completions", "", "Origin", "http://app.example", "Access-Control-Request-Headers", "authorization")
	if pre.status != http.StatusNoContent || pre.header.Get("Access-Control-Allow-Origin") != "http://app.example" || pre.header.Get("Access-Control-Allow-Credentials") != "" {
		t.Fatalf("preflight %d %v", pre.status, pre.header)
	}
	if r := h.do(http.MethodGet, "/", ""); r.status != http.StatusOK || string(r.body) != heartbeat {
		t.Fatalf("heartbeat %d %q", r.status, r.body)
	}
	if r := h.do(http.MethodGet, "/api/tags", "", "Authorization", "Bearer k1"); r.status != http.StatusOK || !strings.Contains(string(r.body), `"models"`) {
		t.Fatalf("tags %d: %s", r.status, r.body)
	}
}

// A token count runs a one token generation on a runtime without a tokenizer endpoint
func TestCountTokens(t *testing.T) {
	h := newHarness(t)
	_, srv := h.stub("stub-model")
	h.publish("chat", "stub", "openai", datalayer.AppProtocolHTTP, true, pools.RouteSpec{ServedName: "stub-model"}, srv.URL)
	r := h.do(http.MethodPost, "/v1/messages/count_tokens", `{"model":"chat","messages":[{"role":"user","content":"hello there friend"}]}`, "anthropic-version", "2023-06-01")
	if r.status != http.StatusOK {
		t.Fatalf("status %d: %s", r.status, r.body)
	}
	if n, _ := r.json(t)["input_tokens"].(float64); n <= 0 {
		t.Fatalf("count %s", r.body)
	}
}

// A split route sends every request to the endpoints whose served label names its target, never to the other served name in the pool
func TestServedLabelSplit(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	_, oldSrv := h.stub("old-model")
	_, newSrv := h.stub("new-model")
	mem := pools.Membership{Model: key("canary"), Runtime: "openai", Protocol: datalayer.AppProtocolHTTP, Concurrency: 8, Metrics: &v1.MetricNames{HealthPath: "/health"}}
	for served, srv := range map[string]string{"old-model": oldSrv.URL, "new-model": newSrv.URL} {
		addr, _ := datalayer.ParseAddress(strings.TrimPrefix(srv, "http://"))
		owner := pools.OwnerRef{Kind: pools.OwnerInstance, ID: served}
		if err := h.mgr.Publish(ctx, owner, mem, []pools.Published{{InstanceID: served, NodeID: testNode, Address: addr, Ready: true, Labels: map[string]string{instances.LabelServed: served}}}); err != nil {
			t.Fatal(err)
		}
	}
	pool := string(pools.PoolIDOf(key("canary"), "openai", ""))
	for name, target := range map[string]string{"canary": "new-model", "stable": "old-model"} {
		if _, err := h.mgr.PutRoute(ctx, name, &v1.RouteSpec{Rules: []*v1.RewriteRule{{Match: name, Targets: []*v1.RewriteTarget{{PoolId: pool, Model: target, Weight: 100}}}}}); err != nil {
			t.Fatal(err)
		}
	}
	for route, want := range map[string]string{"canary": hostOf(newSrv), "stable": hostOf(oldSrv)} {
		for range 12 {
			r := h.do(http.MethodPost, "/v1/chat/completions", strings.Replace(chatBody, `"model":"chat"`, `"model":"`+route+`"`, 1))
			if r.status != http.StatusOK || r.header.Get(requestcontrol.HeaderServed) != want {
				t.Fatalf("%s answered %d from %q, want %s: %s", route, r.status, r.header.Get(requestcontrol.HeaderServed), want, r.body)
			}
		}
	}
	if r := h.do(http.MethodPost, "/v1/chat/completions", strings.Replace(chatBody, `"model":"chat"`, `"model":"`+key("canary").String()+`"`, 1)); r.status == http.StatusServiceUnavailable {
		t.Fatalf("the pool's base model found no endpoint: %s", r.body)
	}
}
