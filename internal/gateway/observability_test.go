package gateway

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/nickheyer/nebu/internal/db"
	"github.com/nickheyer/nebu/internal/pools"
	"github.com/nickheyer/nebu/pkg/datalayer"
	"github.com/nickheyer/nebu/pkg/outbound"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/requestcontrol"
	"github.com/nickheyer/nebu/pkg/telemetry"
	"github.com/nickheyer/nebu/pkg/transcode"
	vllmpb "github.com/nickheyer/nebu/pkg/transcode/gen/vllm"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// The finished trace of a route, every account's, waited for
func (h *harness) finishedTrace(route string) *v1.Trace {
	h.t.Helper()
	var got *v1.Trace
	waitFor(h.t, "a finished trace of "+route, func() bool {
		list, _ := h.g.Traces().List(context.Background(), Viewer{All: true}, route, 1)
		if len(list) == 1 && list[0].GetFinishedAt() != nil {
			got = list[0]
			return true
		}
		return false
	})
	full, ok, err := h.g.Traces().Get(context.Background(), Viewer{All: true}, got.GetId())
	if err != nil || !ok {
		h.t.Fatalf("trace %s: %v %v", got.GetId(), ok, err)
	}
	return full
}

// The sample sum and count of one histogram series, by family name and a label pair
func (h *harness) histogram(name, label, value string) (sum float64, count uint64) {
	h.t.Helper()
	mfs, err := h.reg.Gather()
	if err != nil {
		h.t.Fatal(err)
	}
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
		for _, m := range mf.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == label && l.GetValue() == value {
					return m.GetHistogram().GetSampleSum(), m.GetHistogram().GetSampleCount()
				}
			}
		}
	}
	return 0, 0
}

// The spans of one trace by name, waited for until every wanted name is present
func (h *harness) spansOf(id trace.TraceID, want ...string) map[string]tracetest.SpanStub {
	h.t.Helper()
	out := map[string]tracetest.SpanStub{}
	waitFor(h.t, fmt.Sprintf("spans %v", want), func() bool {
		for _, s := range h.spans.GetSpans() {
			if s.SpanContext.TraceID() == id {
				out[s.Name] = s
			}
		}
		for _, w := range want {
			if _, ok := out[w]; !ok {
				return false
			}
		}
		return true
	})
	return out
}

// A client's trace continues through the server span, the schedule span and the upstream span to the runtime
func TestSpansShareOneTrace(t *testing.T) {
	h := newHarness(t)
	var upstreamParent atomic.Value
	_, srv := h.stub("stub-model")
	traced := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamParent.Store(r.Header.Get("traceparent"))
		req, _ := http.NewRequestWithContext(r.Context(), r.Method, srv.URL+r.URL.Path, r.Body)
		req.Header = r.Header.Clone()
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		copyAnswerHeaders(w.Header(), resp.Header)
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
	}))
	t.Cleanup(traced.Close)
	h.publish("chat", "stub", "vllm", datalayer.AppProtocolHTTP, true, pools.RouteSpec{ServedName: "stub-model"}, traced.URL)
	client := trace.SpanContext{}.WithTraceID(trace.TraceID{0xa, 1, 2, 3, 4, 5, 6, 7, 8, 9, 0xa, 0xb, 0xc, 0xd, 0xe, 0xf}).WithSpanID(trace.SpanID{1, 2, 3, 4, 5, 6, 7, 8}).WithTraceFlags(trace.FlagsSampled)
	r := h.do(http.MethodPost, "/v1/chat/completions", chatBody, "traceparent", fmt.Sprintf("00-%s-%s-01", client.TraceID(), client.SpanID()))
	if r.status != http.StatusOK {
		t.Fatalf("status %d: %s", r.status, r.body)
	}
	spans := h.spansOf(client.TraceID(), "POST /v1/chat/completions", "schedule", "upstream /v1/chat/completions")
	server := spans["POST /v1/chat/completions"]
	if server.Parent.SpanID() != client.SpanID() || server.SpanKind != trace.SpanKindServer {
		t.Fatalf("server span parent %s kind %v", server.Parent.SpanID(), server.SpanKind)
	}
	if spans["schedule"].Parent.SpanID() != server.SpanContext.SpanID() {
		t.Fatalf("schedule span parent %s, want the server span %s", spans["schedule"].Parent.SpanID(), server.SpanContext.SpanID())
	}
	if spans["upstream /v1/chat/completions"].Parent.SpanID() != server.SpanContext.SpanID() || spans["upstream /v1/chat/completions"].SpanKind != trace.SpanKindClient {
		t.Fatalf("upstream span %+v", spans["upstream /v1/chat/completions"])
	}
	runtimeSaw := trace.SpanContextFromContext(telemetry.Extract(context.Background(), http.Header{"Traceparent": {upstreamParent.Load().(string)}}))
	if runtimeSaw.TraceID() != client.TraceID() || runtimeSaw.SpanID() != spans["upstream /v1/chat/completions"].SpanContext.SpanID() {
		t.Fatalf("runtime saw %+v", runtimeSaw)
	}
}

// A fake OpenAI chat server answering a tool call first and text second, with per round usage
type toolRounds struct {
	calls atomic.Int32
}

func (s *toolRounds) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	n := s.calls.Add(1)
	w.Header().Set("Content-Type", "application/json")
	if n == 1 {
		io.WriteString(w, `{"id":"r1","object":"chat.completion","model":"tools","choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"fake__echo","arguments":"{\"text\":\"hi\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`)
		return
	}
	io.WriteString(w, `{"id":"r2","object":"chat.completion","model":"tools","choices":[{"index":0,"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}],"usage":{"prompt_tokens":30,"completion_tokens":3,"total_tokens":33}}`)
}

// An MCP server over HTTP with one echo tool
func fakeMCP(t *testing.T) *httptest.Server {
	t.Helper()
	s := mcp.NewServer(&mcp.Implementation{Name: "fake", Version: "1"}, nil)
	type echoIn struct {
		Text string `json:"text"`
	}
	mcp.AddTool(s, &mcp.Tool{Name: "echo", Description: "Says it back", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, func(ctx context.Context, req *mcp.CallToolRequest, in echoIn) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "echo: " + in.Text}}}, nil, nil
	})
	srv := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s }, &mcp.StreamableHTTPOptions{DisableLocalhostProtection: true}))
	t.Cleanup(srv.Close)
	return srv
}

// A tool round leaves the trace and the prompt token metric with the first round's prompt tokens, output tokens summed
func TestToolRoundsKeepFirstRoundPromptTokens(t *testing.T) {
	h := newHarness(t)
	upstream := httptest.NewServer(&toolRounds{})
	t.Cleanup(upstream.Close)
	mcpSrv := fakeMCP(t)
	h.g.tools.Apply(context.Background(), []*v1.ToolServer{{Name: "fake", Enabled: true, Kind: v1.ToolServerKind_TOOL_SERVER_KIND_HTTP, Url: mcpSrv.URL}})
	t.Cleanup(h.g.tools.Close)
	h.publish("tools", "tools", "vllm", datalayer.AppProtocolHTTP, true, pools.RouteSpec{ServedName: "tools"}, upstream.URL)
	r := h.do(http.MethodPost, "/v1/responses", `{"model":"tools","input":"say hi"}`, ToolsHeader, "fake")
	if r.status != http.StatusOK || !strings.Contains(string(r.body), "done") {
		t.Fatalf("status %d: %s", r.status, r.body)
	}
	tr := h.finishedTrace("tools")
	if tr.GetPromptTokens() != 10 || tr.GetCompletionTokens() != 8 {
		t.Fatalf("trace prompt %d completion %d, want 10 and 8", tr.GetPromptTokens(), tr.GetCompletionTokens())
	}
	if len(tr.GetToolCalls()) != 1 || tr.GetToolCalls()[0].GetServer() == "" || tr.GetToolCalls()[0].GetResult() != "echo: hi" {
		t.Fatalf("tool calls %v", tr.GetToolCalls())
	}
	if sum, count := h.histogram("nebu_request_prompt_tokens", "model", "tools"); sum != 10 || count != 1 {
		t.Fatalf("prompt token metric sum %v count %d", sum, count)
	}
	if sum, _ := h.histogram("nebu_request_output_tokens", "model", "tools"); sum != 8 {
		t.Fatalf("output token metric sum %v", sum)
	}
}

// An MCP server a request names on this host is refused by the outbound guard before any round runs
func TestRequestMCPServerOnThisHostRefused(t *testing.T) {
	h := newHarness(t)
	upstream := httptest.NewServer(&toolRounds{})
	t.Cleanup(upstream.Close)
	mcpSrv := fakeMCP(t)
	h.publish("tools", "tools", "vllm", datalayer.AppProtocolHTTP, true, pools.RouteSpec{ServedName: "tools"}, upstream.URL)
	body := fmt.Sprintf(`{"model":"tools","input":"say hi","tools":[{"type":"mcp","server_label":"fake","server_url":%q}]}`, mcpSrv.URL)
	r := h.do(http.MethodPost, "/v1/responses", body)
	if r.status != http.StatusBadRequest || !strings.Contains(string(r.body), outbound.ErrRefused.Error()) {
		t.Fatalf("status %d: %s", r.status, r.body)
	}
	if n := upstream.Config.Handler.(*toolRounds).calls.Load(); n != 0 {
		t.Fatalf("the runtime got %d rounds of a refused request", n)
	}
}

// A vLLM engine holding its response headers for a while before answering
type slowVLLM struct {
	vllmpb.UnimplementedInferenceServer
	hold time.Duration
}

func (f *slowVLLM) Generate(ctx context.Context, _ *vllmpb.GenerateRequest) (*vllmpb.GenerateResponse, error) {
	time.Sleep(f.hold)
	return &vllmpb.GenerateResponse{PromptInfo: &vllmpb.PromptInfo{NumPromptTokens: 5}, Outputs: &vllmpb.SequenceOutput{Text: "hi there", NumTokens: 2, FinishInfo: &vllmpb.FinishInfo{NumOutputTokens: 2, FinishReason: vllmpb.FinishInfo_STOP}}}, nil
}

func (f *slowVLLM) GenerateStream(_ *vllmpb.GenerateRequest, s vllmpb.Inference_GenerateStreamServer) error {
	time.Sleep(f.hold)
	if err := s.SendHeader(metadata.Pairs("x-engine", "slow")); err != nil {
		return err
	}
	time.Sleep(f.hold)
	return s.Send(&vllmpb.GenerateResponse{Outputs: &vllmpb.SequenceOutput{Text: "hi", NumTokens: 1, FinishInfo: &vllmpb.FinishInfo{NumOutputTokens: 1, FinishReason: vllmpb.FinishInfo_STOP}}})
}

// An Inference call's first byte is stamped when the engine answers headers, after the hold and before the body
func TestGRPCFirstByteAtBackendHeaders(t *testing.T) {
	h := newHarness(t)
	const hold = 150 * time.Millisecond
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	engine := grpc.NewServer()
	vllmpb.RegisterInferenceServer(engine, &slowVLLM{hold: hold})
	go engine.Serve(lis)
	t.Cleanup(engine.Stop)
	h.publish("slow", "grpc", "vllm", datalayer.AppProtocolGRPC, true, pools.RouteSpec{ServedName: "served"}, lis.Addr().String())
	conn, err := transcode.Dial(hostOf(h.srv), transcode.GRPC)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ctx := context.Background()
	if _, err := vllmpb.NewInferenceClient(conn).Generate(ctx, &vllmpb.GenerateRequest{RequestId: "u1", Model: "slow", Prompt: &vllmpb.GenerateRequest_Text{Text: "hello"}}); err != nil {
		t.Fatal(err)
	}
	unary := h.finishedTrace("slow")
	if unary.GetFirstByteAt() == nil || unary.GetFirstByteAt().AsTime().Sub(unary.GetStartedAt().AsTime()) < hold {
		t.Fatalf("unary first byte %v started %v", unary.GetFirstByteAt(), unary.GetStartedAt())
	}
	stream, err := vllmpb.NewInferenceClient(conn).GenerateStream(ctx, &vllmpb.GenerateRequest{RequestId: "s1", Model: "slow", Prompt: &vllmpb.GenerateRequest_Text{Text: "hello"}})
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := stream.Recv(); err != nil {
			break
		}
	}
	var streamed *v1.Trace
	waitFor(t, "the stream trace", func() bool {
		list, _ := h.g.Traces().List(ctx, Viewer{All: true}, "slow", 2)
		for _, tr := range list {
			if tr.GetRequestId() == "s1" && tr.GetFinishedAt() != nil {
				streamed = tr
				return true
			}
		}
		return false
	})
	first := streamed.GetFirstByteAt().AsTime().Sub(streamed.GetStartedAt().AsTime())
	finished := streamed.GetFinishedAt().AsTime().Sub(streamed.GetFirstByteAt().AsTime())
	if first < hold || first >= 2*hold || finished < hold {
		t.Fatalf("stream first byte after %v, finished %v after it, hold %v", first, finished, hold)
	}
}

// Every trace records the filters the picked profile ran with what they dropped, every candidate's score, and its owner
func TestTraceCarriesDecisionAndOwner(t *testing.T) {
	h := newHarness(t, func(s *setup) { s.keys = []string{"alice-key"} })
	_, srv := h.stub("stub-model")
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "down", http.StatusServiceUnavailable) }))
	t.Cleanup(down.Close)
	h.publish("chat", "stub", "vllm", datalayer.AppProtocolHTTP, true, pools.RouteSpec{ServedName: "stub-model"}, srv.URL)
	h.publish("", "stub", "vllm", datalayer.AppProtocolHTTP, false, pools.RouteSpec{}, down.URL)
	r := h.do(http.MethodPost, "/v1/chat/completions", chatBody, "Authorization", "Bearer alice-key")
	if r.status != http.StatusOK {
		t.Fatalf("status %d: %s", r.status, r.body)
	}
	tr := h.finishedTrace("chat")
	if tr.GetOwner() != keyOwner("alice-key") {
		t.Fatalf("owner %q", tr.GetOwner())
	}
	if len(tr.GetFilters()) == 0 {
		t.Fatalf("no filters on the trace %v", tr)
	}
	dropped := map[string][]string{}
	for _, f := range tr.GetFilters() {
		dropped[f.GetName()] = f.GetDropped()
	}
	if got := dropped["ready-filter/ready"]; len(got) != 1 || got[0] != hostOf(down) {
		t.Fatalf("ready filter dropped %v, filters %v", got, tr.GetFilters())
	}
	if len(tr.GetCandidates()) != 1 || tr.GetCandidates()[0].GetEndpoint() != hostOf(srv) || tr.GetCandidates()[0].GetScore() != tr.GetScore() || len(tr.GetCandidates()[0].GetByScorer()) == 0 {
		t.Fatalf("candidates %v, score %v", tr.GetCandidates(), tr.GetScore())
	}
	var stored *v1.Trace
	waitFor(t, "the stored trace", func() bool {
		got, err := h.db.GetTrace(context.Background(), keyOwner("alice-key"), false, tr.GetId())
		if err != nil && !errors.Is(err, db.ErrNotFound) {
			t.Fatalf("stored trace: %v", err)
		}
		stored = got
		return err == nil
	})
	if stored.GetOwner() != keyOwner("alice-key") || len(stored.GetFilters()) != len(tr.GetFilters()) {
		t.Fatalf("stored %v", stored)
	}
	list, _ := h.g.Traces().List(context.Background(), Viewer{All: true}, "chat", 1)
	if list[0].GetOwner() != keyOwner("alice-key") {
		t.Fatalf("listed owner %q", list[0].GetOwner())
	}
}

// The metrics page carries queue wait, token and latency series per model, endpoint, priority and fairness id, plus pool and scheduler families
func TestMetricsLabelsAndFamilies(t *testing.T) {
	h := newHarness(t)
	_, srv := h.stub("stub-model")
	pool := h.publish("chat", "stub", "vllm", datalayer.AppProtocolHTTP, true, pools.RouteSpec{ServedName: "stub-model"}, srv.URL)
	if r := h.do(http.MethodPost, "/v1/chat/completions", chatBody, requestcontrol.HeaderFairnessID, "tenant-a"); r.status != http.StatusOK {
		t.Fatalf("status %d: %s", r.status, r.body)
	}
	labels := fmt.Sprintf(`endpoint="%s",fairness_id="tenant-a",model="stub-model",pool="%s",priority="0"`, h.endpointID(pool), pool)
	for _, want := range []string{
		"nebu_request_queue_wait_seconds_count{" + labels + "} 1",
		"nebu_request_duration_seconds_count{" + labels + "} 1",
		"nebu_request_prompt_tokens_count{" + labels + "} 1",
		"nebu_request_output_tokens_count{" + labels + "} 1",
		"nebu_request_ttft_seconds_count{" + labels + "} 1",
		fmt.Sprintf(`nebu_pool_queued_requests{pool="%s"}`, pool),
		fmt.Sprintf(`nebu_scheduler_filter_dropped_total{filter="ready-filter/ready",pool="%s",profile=`, pool),
		fmt.Sprintf(`nebu_scheduler_score_count{pool="%s",profile=`, pool),
		"nebu_prefix_cache_entries ",
	} {
		waitFor(t, want, func() bool { return strings.Contains(string(h.do(http.MethodGet, "/metrics", "").body), want) })
	}
	page := string(h.do(http.MethodGet, "/metrics", "").body)
	if !strings.Contains(page, "nebu_pool_kv_cache_utilization") && strings.Contains(page, "nebu_endpoint_kv_cache_utilization{") {
		t.Fatal("pool utilization is missing while endpoint utilization is present")
	}
	families := map[string]bool{}
	mfs, err := h.reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range mfs {
		families[mf.GetName()] = true
	}
	for _, name := range []string{"nebu_pool_ready_endpoints", "nebu_pool_queued_requests", "nebu_flow_queue_depth", "nebu_scheduler_filter_dropped_total", "nebu_scheduler_score"} {
		if !families[name] {
			t.Fatalf("family %s missing", name)
		}
	}
}
