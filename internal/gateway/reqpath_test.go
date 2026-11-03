package gateway

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nickheyer/nebu/internal/chat"
	"github.com/nickheyer/nebu/internal/instances/fake"
	"github.com/nickheyer/nebu/internal/pools"
	"github.com/nickheyer/nebu/pkg/bodyparse"
	"github.com/nickheyer/nebu/pkg/datalayer"
	"github.com/nickheyer/nebu/pkg/launch"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/requestcontrol"
	"github.com/nickheyer/nebu/pkg/transcode"
	runtimev1 "github.com/nickheyer/nebu/pkg/transcode/gen/sglang/runtime/v1"
	vllmpb "github.com/nickheyer/nebu/pkg/transcode/gen/vllm"
	"github.com/nickheyer/nebu/pkg/transcode/server"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// A model named by header is decided before the body is read, which the stages then take on attachment
func TestHeaderModelRoutesBeforeBody(t *testing.T) {
	h := newHarness(t)
	seat := newSeat(t)
	h.publish("chat", "stub", "vllm", datalayer.AppProtocolHTTP, true, pools.RouteSpec{ServedName: "m"}, hostOf(seat.srv))
	t.Run("unknown model refused before any body byte", func(t *testing.T) {
		pr, pw := io.Pipe()
		defer pw.Close()
		req, _ := http.NewRequest(http.MethodPost, h.srv.URL+"/v1/chat/completions", pr)
		req.Header.Set(requestcontrol.HeaderModelName, "nope")
		req.Header.Set("Content-Type", "application/json")
		done := make(chan *http.Response, 1)
		go func() {
			resp, err := h.srv.Client().Do(req)
			if err != nil {
				t.Errorf("request: %v", err)
				done <- nil
				return
			}
			resp.Body.Close()
			done <- resp
		}()
		select {
		case resp := <-done:
			if resp == nil || resp.StatusCode != http.StatusNotFound {
				t.Fatalf("status %v, want 404 with the body unsent", resp)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the gateway waited for the body of a request it refuses on headers")
		}
	})
	t.Run("oversized body refused after the decision", func(t *testing.T) {
		big := `{"model":"x","pad":"` + strings.Repeat("a", maxBody+1024) + `"}`
		req, _ := http.NewRequest(http.MethodPost, h.srv.URL+"/v1/chat/completions", strings.NewReader(big))
		req.Header.Set(requestcontrol.HeaderModelName, "chat")
		req.Header.Set("Content-Type", "application/json")
		resp, err := h.srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusRequestEntityTooLarge {
			t.Fatalf("status %d", resp.StatusCode)
		}
	})
	t.Run("body stages run on the attached body", func(t *testing.T) {
		r := h.do(http.MethodPost, "/v1/chat/completions", chatBody, requestcontrol.HeaderModelName, "chat", SystemHeader, "b3ZlcnJpZGU=")
		if r.status != http.StatusOK {
			t.Fatalf("status %d: %s", r.status, r.body)
		}
		_, bodies := seat.got()
		last := bodies[len(bodies)-1]
		msgs, _ := last["messages"].([]any)
		first, _ := msgs[0].(map[string]any)
		if last["model"] != "m" || first["content"] != "override" {
			t.Fatalf("seat got %v", last)
		}
	})
}

// Bodies are read and sent in chunks of at most bodyparse.ChunkBytes and arrive whole
func TestBodyChunks(t *testing.T) {
	body := bytes.Repeat([]byte("x"), 3*bodyparse.ChunkBytes+17)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	got, err := readBody(rec, req)
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("readBody %d bytes %v", len(got), err)
	}
	req = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(bytes.Repeat([]byte("y"), maxBody+1)))
	if _, err := readBody(rec, req); !errors.Is(err, bodyparse.ErrTooLarge) {
		t.Fatalf("oversized body err %v", err)
	}
	if got, err := readBody(rec, httptest.NewRequest(http.MethodGet, "/v1/models", nil)); err != nil || got != nil {
		t.Fatalf("empty body %q %v", got, err)
	}
	rd := newChunkReader(body)
	buf := make([]byte, 1<<20)
	var reads []int
	for {
		n, err := rd.Read(buf)
		if n > 0 {
			reads = append(reads, n)
		}
		if err == io.EOF {
			break
		}
	}
	if len(reads) != 4 || reads[0] != bodyparse.ChunkBytes || reads[3] != 17 {
		t.Fatalf("chunk reads %v", reads)
	}
	for _, n := range reads {
		if n > bodyparse.ChunkBytes {
			t.Fatalf("a read of %d bytes passed the chunk size", n)
		}
	}
	h := newHarness(t)
	seat := newSeat(t)
	h.publish("chat", "stub", "vllm", datalayer.AppProtocolHTTP, true, pools.RouteSpec{ServedName: "m"}, hostOf(seat.srv))
	pad := strings.Repeat("p", 2*bodyparse.ChunkBytes+5)
	r := h.do(http.MethodPost, "/v1/chat/completions", strings.Replace(chatBody, `"max_tokens":8`, `"max_tokens":8,"pad":"`+pad+`"`, 1))
	if r.status != http.StatusOK {
		t.Fatalf("status %d: %s", r.status, r.body)
	}
	_, bodies := seat.got()
	if got, _ := bodies[0]["pad"].(string); got != pad {
		t.Fatalf("seat got a pad of %d bytes, want %d", len(got), len(pad))
	}
}

// An endpoint that read the whole request and died is never resent to, while one that refused the connection is
func TestRetryOnlyBeforeTheRequestWasWritten(t *testing.T) {
	t.Run("written then dropped", func(t *testing.T) {
		h := newHarness(t)
		var crashed, good atomic.Int32
		dying := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			io.ReadAll(r.Body)
			crashed.Add(1)
			time.Sleep(50 * time.Millisecond)
			conn, _, err := http.NewResponseController(w).Hijack()
			if err == nil {
				conn.(*net.TCPConn).SetLinger(0)
				conn.Close()
			}
		}))
		defer dying.Close()
		seat := newSeat(t)
		counting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			good.Add(1)
			seat.srv.Config.Handler.ServeHTTP(w, r)
		}))
		defer counting.Close()
		pool := h.publish("chat", "stub", "vllm", datalayer.AppProtocolHTTP, true, pools.RouteSpec{ServedName: "m"}, dying.URL, counting.URL)
		pin(t, h, pool, hostOf(dying))
		r := h.do(http.MethodPost, "/v1/chat/completions", chatBody)
		if r.status != http.StatusBadGateway || crashed.Load() != 1 || good.Load() != 0 {
			t.Fatalf("status %d, dying read %d, second endpoint got %d: %s", r.status, crashed.Load(), good.Load(), r.body)
		}
		tr := traceOf(t, h, r.header.Get(TraceHeader))
		if len(tr.GetFallbacks()) != 0 || tr.GetError() == "" {
			t.Fatalf("trace fallbacks %v error %q", tr.GetFallbacks(), tr.GetError())
		}
	})
	t.Run("refused before writing", func(t *testing.T) {
		h := newHarness(t)
		closed, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		dead := closed.Addr().String()
		closed.Close()
		seat := newSeat(t)
		pool := h.publish("chat", "stub", "vllm", datalayer.AppProtocolHTTP, true, pools.RouteSpec{ServedName: "m"}, dead, hostOf(seat.srv))
		pin(t, h, pool, dead)
		r := h.do(http.MethodPost, "/v1/chat/completions", chatBody)
		if r.status != http.StatusOK || r.header.Get(requestcontrol.HeaderServed) != hostOf(seat.srv) {
			t.Fatalf("status %d served %q: %s", r.status, r.header.Get(requestcontrol.HeaderServed), r.body)
		}
		tr := traceOf(t, h, r.header.Get(TraceHeader))
		fb := tr.GetFallbacks()
		if len(fb) != 1 || fb[0].GetAddress() != dead || fb[0].GetRequestWritten() || fb[0].GetBytesToClient() != 0 || fb[0].GetReason() == "" {
			t.Fatalf("fallbacks %v", fb)
		}
	})
	t.Run("retry rule", func(t *testing.T) {
		reset := &net.OpError{Op: "read", Err: errors.New("connection reset by peer")}
		for _, c := range []struct {
			name    string
			err     error
			method  string
			written bool
			want    bool
		}{
			{"post unwritten reset", reset, http.MethodPost, false, true},
			{"post written reset", reset, http.MethodPost, true, false},
			{"get written reset", reset, http.MethodGet, true, true},
			{"post written eof", io.ErrUnexpectedEOF, http.MethodPost, true, false},
			{"cancelled", context.Canceled, http.MethodGet, false, false},
			{"grpc unavailable unwritten", transcode.NewError(http.StatusServiceUnavailable, "down"), http.MethodPost, false, true},
			{"grpc unavailable written", transcode.NewError(http.StatusServiceUnavailable, "down"), http.MethodPost, true, false},
		} {
			if got := retryable(c.err, c.method, c.written); got != c.want {
				t.Fatalf("%s: retryable %v, want %v", c.name, got, c.want)
			}
		}
	})
}

// Pins a prompt's prefix to one endpoint so the scheduler tries it first
func pin(t *testing.T, h *harness, pool datalayer.PoolID, addr string) {
	t.Helper()
	doc, err := parseDoc("/v1/chat/completions", []byte(chatBody))
	if err != nil {
		t.Fatal(err)
	}
	for _, ep := range h.store.Endpoints(pool, nil) {
		if ep.Address().String() == addr {
			h.g.index.Record(ep.ID(), h.g.index.Hash("m", "", doc.PromptText(), nil))
		}
	}
}

// The finished trace of an answer
func traceOf(t *testing.T, h *harness, id string) *v1.Trace {
	t.Helper()
	var tr *v1.Trace
	waitFor(t, "trace "+id, func() bool {
		got, ok, _ := h.g.Traces().Get(context.Background(), Viewer{All: true}, id)
		tr = got
		return ok && got.GetFinishedAt() != nil
	})
	return tr
}

// An SGLang engine answering pass-through calls with scripted chunks and recording the last body
type fakeSGLang struct {
	runtimev1.UnimplementedSglangServiceServer
	last atomic.Pointer[runtimev1.OpenAIRequest]
	fail bool
}

const sglangAnswer = `{"id":"x","object":"chat.completion","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"hi there"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`

func (f *fakeSGLang) stream(req *runtimev1.OpenAIRequest, s grpc.ServerStreamingServer[runtimev1.OpenAIStreamChunk]) error {
	f.last.Store(req)
	if f.fail {
		if err := s.Send(&runtimev1.OpenAIStreamChunk{JsonChunk: []byte(`{"choices":[{"index":0,"delta":{"content":"hi"}}]}`)}); err != nil {
			return err
		}
		return status.Error(codes.Internal, "engine boom")
	}
	if !bytes.Contains(req.GetJsonBody(), []byte(`"stream":true`)) {
		return s.Send(&runtimev1.OpenAIStreamChunk{JsonChunk: []byte(sglangAnswer), Finished: true})
	}
	for _, text := range []string{"hi", " there"} {
		if err := s.Send(&runtimev1.OpenAIStreamChunk{JsonChunk: []byte(`{"id":"x","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"content":"` + text + `"}}]}`)}); err != nil {
			return err
		}
	}
	return s.Send(&runtimev1.OpenAIStreamChunk{JsonChunk: []byte(`{"id":"x","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`), Finished: true})
}

func (f *fakeSGLang) ChatComplete(req *runtimev1.OpenAIRequest, s grpc.ServerStreamingServer[runtimev1.OpenAIStreamChunk]) error {
	return f.stream(req, s)
}

func (f *fakeSGLang) Complete(req *runtimev1.OpenAIRequest, s grpc.ServerStreamingServer[runtimev1.OpenAIStreamChunk]) error {
	return f.stream(req, s)
}

func (f *fakeSGLang) OpenAIEmbed(_ context.Context, req *runtimev1.OpenAIRequest) (*runtimev1.OpenAIResponse, error) {
	f.last.Store(req)
	return &runtimev1.OpenAIResponse{JsonBody: []byte(`{"object":"list","model":"m","data":[{"object":"embedding","index":0,"embedding":[0.5,0.25]}],"usage":{"prompt_tokens":1,"total_tokens":1}}`), StatusCode: 200}, nil
}

func startSGLang(t *testing.T, f *fakeSGLang) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer(server.ServerOptions()...)
	runtimev1.RegisterSglangServiceServer(srv, f)
	go srv.Serve(lis)
	t.Cleanup(srv.Stop)
	return lis.Addr().String()
}

// HTTP chat, streams and embeddings reach an SGLang pool over its gRPC service with the target model written in
func TestGRPCPoolSGLang(t *testing.T) {
	h := newHarness(t)
	f := &fakeSGLang{}
	addr := startSGLang(t, f)
	h.publish("chat", "sg", "sglang", datalayer.AppProtocolGRPC, true, pools.RouteSpec{ServedName: "served"}, addr)
	for _, tc := range []struct {
		name, path, body, want string
	}{
		{"chat whole", "/v1/chat/completions", chatBody, `"content":"hi there"`},
		{"chat stream", "/v1/chat/completions", strings.Replace(chatBody, `"max_tokens":8`, `"max_tokens":8,"stream":true`, 1), "[DONE]"},
		{"embeddings", "/v1/embeddings", `{"model":"chat","input":"hello"}`, `"embedding":[0.5,0.25]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := h.do(http.MethodPost, tc.path, tc.body)
			if r.status != http.StatusOK || !strings.Contains(string(r.body), tc.want) || r.header.Get(requestcontrol.HeaderServed) != addr {
				t.Fatalf("status %d served %q: %s", r.status, r.header.Get(requestcontrol.HeaderServed), r.body)
			}
			got := f.last.Load()
			if !bytes.Contains(got.GetJsonBody(), []byte(`"model":"served"`)) {
				t.Fatalf("engine got %s", got.GetJsonBody())
			}
		})
	}
}

// SGLang pass-through calls arriving over gRPC are routed by the director, traced, and refused for pools of another runtime
func TestGRPCInSGLang(t *testing.T) {
	h := newHarness(t)
	f := &fakeSGLang{}
	addr := startSGLang(t, f)
	h.publish("chat", "sg", "sglang", datalayer.AppProtocolGRPC, true, pools.RouteSpec{ServedName: "served"}, addr)
	vf, vaddr := startVLLM(t)
	h.publish("vchat", "grpc", "vllm", datalayer.AppProtocolGRPC, true, pools.RouteSpec{ServedName: "vserved"}, vaddr)
	conn, err := transcode.Dial(hostOf(h.srv), transcode.GRPC)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ctx := metadata.AppendToOutgoingContext(context.Background(), "x-request-id", "sg1")
	client := runtimev1.NewSglangServiceClient(conn)
	cs, err := client.ChatComplete(ctx, &runtimev1.OpenAIRequest{JsonBody: []byte(chatBody)})
	if err != nil {
		t.Fatal(err)
	}
	var chunks []string
	for {
		msg, err := cs.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		chunks = append(chunks, string(msg.GetJsonChunk()))
	}
	if len(chunks) != 1 || chunks[0] != sglangAnswer || !bytes.Contains(f.last.Load().GetJsonBody(), []byte(`"model":"served"`)) {
		t.Fatalf("chunks %v, engine got %s", chunks, f.last.Load().GetJsonBody())
	}
	waitFor(t, "SGLang trace", func() bool {
		list, _ := h.g.Traces().List(ctx, Viewer{All: true}, "chat", 1)
		return len(list) == 1 && list[0].GetFinishedAt() != nil && list[0].GetStatus() == http.StatusOK && list[0].GetEndpoint() == addr && list[0].GetRequestId() == "sg1"
	})
	emb, err := client.OpenAIEmbed(ctx, &runtimev1.OpenAIRequest{JsonBody: []byte(`{"model":"chat","input":"hello"}`)})
	if err != nil || !bytes.Contains(emb.GetJsonBody(), []byte(`"embedding"`)) {
		t.Fatalf("embed %v %v", emb, err)
	}
	if _, err := client.OpenAIEmbed(ctx, &runtimev1.OpenAIRequest{JsonBody: []byte(`{"model":"nope","input":"hello"}`)}); status.Code(err) != codes.NotFound {
		t.Fatalf("unknown model %v", err)
	}
	if _, err := client.OpenAIEmbed(ctx, &runtimev1.OpenAIRequest{JsonBody: []byte(`{"model":"vchat","input":"hello"}`)}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("SGLang call to a vLLM pool %v", err)
	}
	if _, err := vllmpb.NewInferenceClient(conn).Generate(ctx, &vllmpb.GenerateRequest{Model: "chat", Prompt: &vllmpb.GenerateRequest_Text{Text: "hello"}}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("vLLM call to an SGLang pool %v", err)
	}
	if _, err := vllmpb.NewInferenceClient(conn).Generate(ctx, &vllmpb.GenerateRequest{Model: "vchat", Prompt: &vllmpb.GenerateRequest_Text{Text: "hello"}}); err != nil || vf.last().GetModel() != "vserved" {
		t.Fatalf("vLLM call to the vLLM pool %v, engine saw %v", err, vf.last())
	}
}

// A gRPC backend failing mid stream ends the client's stream with the error recorded on the trace and the director's response
func TestGRPCStreamFailureRecorded(t *testing.T) {
	h := newHarness(t)
	f := &fakeSGLang{fail: true}
	addr := startSGLang(t, f)
	h.publish("chat", "sg", "sglang", datalayer.AppProtocolGRPC, true, pools.RouteSpec{ServedName: "served"}, addr)
	r := h.do(http.MethodPost, "/v1/chat/completions", strings.Replace(chatBody, `"max_tokens":8`, `"max_tokens":8,"stream":true`, 1))
	if r.status != http.StatusOK {
		t.Fatalf("status %d: %s", r.status, r.body)
	}
	payloads := ssePayloads(r.body)
	if len(payloads) < 2 || !strings.Contains(payloads[len(payloads)-1], "engine boom") || strings.Contains(payloads[len(payloads)-2], "engine boom") || payloads[len(payloads)-1] == "[DONE]" {
		t.Fatalf("stream %q, want the one error event last without [DONE]", payloads)
	}
	tr := traceOf(t, h, r.header.Get(TraceHeader))
	if !strings.Contains(tr.GetError(), "engine boom") {
		t.Fatalf("trace error %q", tr.GetError())
	}
}

// The chain document selects and orders the stages, a chain without the tools planner leaving the system prompt alone
func TestChainFromConfig(t *testing.T) {
	bare := "plugins:\n- type: mesh-entry\n- type: objective-resolver\n- type: route-policy\n  after: [mesh-entry, objective-resolver]\n- type: served-only\n- type: served-endpoint\n- type: trace-id\n- type: retry-after\n"
	for _, tc := range []struct {
		name  string
		chain string
		want  string
	}{
		{"built in", "", "override"},
		{"without tools planner", bare, "be brief"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, func(s *setup) { s.chain = []byte(tc.chain) })
			seat := newSeat(t)
			h.publish("chat", "stub", "vllm", datalayer.AppProtocolHTTP, true, pools.RouteSpec{ServedName: "m"}, hostOf(seat.srv))
			r := h.do(http.MethodPost, "/v1/chat/completions", chatBody, SystemHeader, "b3ZlcnJpZGU=")
			if r.status != http.StatusOK {
				t.Fatalf("status %d: %s", r.status, r.body)
			}
			_, bodies := seat.got()
			msgs, _ := bodies[0]["messages"].([]any)
			first, _ := msgs[0].(map[string]any)
			if first["content"] != tc.want {
				t.Fatalf("first turn %v, want %q", first, tc.want)
			}
		})
	}
	h := newHarness(t)
	for name, doc := range map[string]string{
		"unknown type": "plugins:\n- type: nope\n",
		"renamed":      "plugins:\n- type: trace-id\n  name: other\n",
		"parameters":   "plugins:\n- type: trace-id\n  parameters: {x: 1}\n",
		"cycle":        "plugins:\n- type: trace-id\n  after: [retry-after]\n- type: retry-after\n  after: [trace-id]\n",
	} {
		if _, err := h.g.chain([]byte(doc)); !errors.Is(err, requestcontrol.ErrInvalidChain) {
			t.Fatalf("%s: err %v", name, err)
		}
	}
	if got := strings.Join(h.g.registry().Types(), ","); got != "guardrail,mesh-entry,objective-resolver,retry-after,route-policy,served-endpoint,served-only,tokenize,tools-emulation,tools-planner,trace-id" {
		t.Fatalf("types %s", got)
	}
}

// A guardrail in the chain refuses denied prompts with 403 and redacts personal data in the request and the answer
func TestGuardrailInChain(t *testing.T) {
	chain := DefaultChain + "- type: guardrail\n  name: rail\n  after: [tools-planner]\n  parameters:\n    deny: ['(?i)forbidden']\n    pii: true\n"
	h := newHarness(t, func(s *setup) { s.chain = []byte(chain) })
	var seen atomic.Pointer[map[string]any]
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		seen.Store(&body)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"x","object":"chat.completion","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"write to bob@example.com or 555-123-4567"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}`)
	}))
	defer upstream.Close()
	h.publish("chat", "stub", "vllm", datalayer.AppProtocolHTTP, true, pools.RouteSpec{ServedName: "m"}, hostOf(upstream))
	denied := h.do(http.MethodPost, "/v1/chat/completions", `{"model":"chat","messages":[{"role":"user","content":"tell me something FORBIDDEN"}]}`)
	if denied.status != http.StatusForbidden || !strings.Contains(string(denied.body), "guardrail") {
		t.Fatalf("denied prompt answered %d: %s", denied.status, denied.body)
	}
	r := h.do(http.MethodPost, "/v1/chat/completions", `{"model":"chat","messages":[{"role":"user","content":"my mail is alice@example.org"}]}`)
	if r.status != http.StatusOK {
		t.Fatalf("status %d: %s", r.status, r.body)
	}
	msgs, _ := (*seen.Load())["messages"].([]any)
	first, _ := msgs[0].(map[string]any)
	if first["content"] != "my mail is [REDACTED]" {
		t.Fatalf("upstream saw %v", first)
	}
	if body := string(r.body); !strings.Contains(body, "write to [REDACTED] or [REDACTED]") || strings.Contains(body, "bob@") {
		t.Fatalf("answer %s", body)
	}
}

// The deny only guardrail chain the answer side tests run
const denyChain = DefaultChain + "- type: guardrail\n  name: rail\n  after: [tools-planner]\n  parameters:\n    deny: ['(?i)forbidden']\n"

// A guardrail blocking an unstreamed answer answers its 403 and error body, no upstream status or header reaching the client
func TestGuardrailBlocksUnstreamedAnswer(t *testing.T) {
	h := newHarness(t, func(s *setup) { s.chain = []byte(denyChain) })
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Upstream", "1")
		io.WriteString(w, `{"id":"x","object":"chat.completion","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"that is forbidden"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":3,"total_tokens":6}}`)
	}))
	defer upstream.Close()
	h.publish("chat", "stub", "vllm", datalayer.AppProtocolHTTP, true, pools.RouteSpec{ServedName: "m"}, hostOf(upstream))
	r := h.do(http.MethodPost, "/v1/chat/completions", `{"model":"chat","messages":[{"role":"user","content":"say it"}]}`)
	if r.status != http.StatusForbidden || r.header.Get("X-Upstream") != "" || strings.Contains(string(r.body), "that is forbidden") {
		t.Fatalf("status %d headers %v: %s", r.status, r.header, r.body)
	}
	e, _ := r.json(t)["error"].(map[string]any)
	if msg, _ := e["message"].(string); !strings.Contains(msg, "the answer matched a deny pattern") || e["code"] != chat.CodeInvalidRequest {
		t.Fatalf("error body %s", r.body)
	}
	if tr := traceOf(t, h, r.header.Get(TraceHeader)); tr.GetStatus() != http.StatusForbidden || !strings.Contains(tr.GetError(), "deny pattern") {
		t.Fatalf("trace status %d error %q", tr.GetStatus(), tr.GetError())
	}
}

// A streamed answer starts with the upstream 200 and a guardrail blocking it mid stream ends it with the client protocol's error
func TestGuardrailEndsStreamWithProtocolError(t *testing.T) {
	sse := func(events ...string) string { return strings.Join(events, "\n\n") + "\n\n" }
	for _, tc := range []struct {
		name, path, runtime, body, contentType string
		// What the runtime streams before the test lets it go on, and the denied rest
		first, denied string
		// The event name the stream must end with, and a check of its data
		event string
		last  func(map[string]any) bool
	}{
		{
			name: "openai chat", path: chat.OpenAIChat, runtime: "vllm", contentType: "text/event-stream",
			body:   `{"model":"chat","stream":true,"messages":[{"role":"user","content":"say it"}]}`,
			first:  sse(`data: {"id":"c1","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"hello"}}]}`),
			denied: sse(`data: {"id":"c1","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"content":"forbidden"}}]}`, "data: [DONE]"),
			last: func(m map[string]any) bool {
				e, _ := m["error"].(map[string]any)
				msg, _ := e["message"].(string)
				return e["code"] == chat.CodeInvalidRequest && e["type"] == chat.CodeInvalidRequest && strings.Contains(msg, "deny pattern")
			},
		},
		{
			name: "openai responses", path: chat.OpenAIResponses, runtime: "vllm", contentType: "text/event-stream", event: "error",
			body:   `{"model":"chat","stream":true,"input":"say it"}`,
			first:  sse("event: response.created\n"+`data: {"type":"response.created","sequence_number":0,"response":{"id":"resp_1","status":"in_progress"}}`, "event: response.output_text.delta\n"+`data: {"type":"response.output_text.delta","sequence_number":1,"delta":"hello"}`),
			denied: sse("event: response.output_text.delta\n"+`data: {"type":"response.output_text.delta","sequence_number":2,"delta":"forbidden"}`, "event: response.completed\n"+`data: {"type":"response.completed","sequence_number":3}`),
			last: func(m map[string]any) bool {
				msg, _ := m["message"].(string)
				return m["type"] == "error" && m["code"] == chat.CodeInvalidRequest && m["sequence_number"] == float64(2) && strings.Contains(msg, "deny pattern")
			},
		},
		{
			name: "anthropic", path: chat.AnthropicMessages, runtime: "claude", contentType: "text/event-stream", event: "error",
			body:   `{"model":"chat","stream":true,"max_tokens":8,"messages":[{"role":"user","content":"say it"}]}`,
			first:  sse("event: message_start\n"+`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"m","content":[],"usage":{"input_tokens":3,"output_tokens":0}}}`, "event: content_block_start\n"+`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`, "event: content_block_delta\n"+`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}`),
			denied: sse("event: content_block_delta\n"+`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"forbidden"}}`, "event: message_stop\n"+`data: {"type":"message_stop"}`),
			last: func(m map[string]any) bool {
				e, _ := m["error"].(map[string]any)
				msg, _ := e["message"].(string)
				return m["type"] == "error" && e["type"] == chat.CodeInvalidRequest && strings.Contains(msg, "deny pattern")
			},
		},
		{
			name: "ollama", path: chat.OllamaChat, runtime: "ollama", contentType: "application/x-ndjson",
			body:   `{"model":"chat","stream":true,"messages":[{"role":"user","content":"say it"}]}`,
			first:  `{"model":"m","created_at":"2026-11-03T00:00:00Z","message":{"role":"assistant","content":"hello"},"done":false}` + "\n",
			denied: `{"model":"m","created_at":"2026-11-03T00:00:01Z","message":{"role":"assistant","content":"forbidden"},"done":false}` + "\n" + `{"model":"m","created_at":"2026-11-03T00:00:02Z","message":{"role":"assistant","content":""},"done":true}` + "\n",
			last: func(m map[string]any) bool {
				msg, _ := m["error"].(string)
				return m["done"] == true && strings.Contains(msg, "deny pattern")
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, func(s *setup) {
				s.chain = []byte(denyChain)
				s.runtimes.api["claude"] = v1.ApiFlavor_API_FLAVOR_ANTHROPIC
			})
			release := make(chan struct{})
			unblock := sync.OnceFunc(func() { close(release) })
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", tc.contentType)
				io.WriteString(w, tc.first)
				w.(http.Flusher).Flush()
				<-release
				io.WriteString(w, tc.denied)
			}))
			defer upstream.Close()
			defer unblock()
			h.publish("chat", "stub", tc.runtime, datalayer.AppProtocolHTTP, true, pools.RouteSpec{ServedName: "m"}, hostOf(upstream))
			req, err := http.NewRequest(http.MethodPost, h.srv.URL+tc.path, strings.NewReader(tc.body))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Content-Type", "application/json")
			resp, err := h.srv.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != tc.contentType {
				t.Fatalf("status %d content type %q", resp.StatusCode, resp.Header.Get("Content-Type"))
			}
			rd := bufio.NewReader(resp.Body)
			var got strings.Builder
			for !strings.Contains(got.String(), "hello") {
				line, err := rd.ReadString('\n')
				got.WriteString(line)
				if err != nil {
					t.Fatalf("stream before the denied part %q: %v", got.String(), err)
				}
			}
			unblock()
			rest, err := io.ReadAll(rd)
			if err != nil {
				t.Fatal(err)
			}
			got.Write(rest)
			all := got.String()
			if strings.Contains(all, "forbidden") || strings.Contains(all, "[DONE]") || strings.Contains(all, "message_stop") || strings.Contains(all, "response.completed") || strings.Contains(all, "00:00:02Z") {
				t.Fatalf("the denied part reached the client %q", all)
			}
			var event string
			var last map[string]any
			if tc.contentType == "application/x-ndjson" {
				lines := strings.Split(strings.TrimSuffix(all, "\n"), "\n")
				err = json.Unmarshal([]byte(lines[len(lines)-1]), &last)
			} else {
				err = chat.ReadSSE(strings.NewReader(all), func(name string, data []byte) error {
					event, last = name, nil
					return json.Unmarshal(data, &last)
				})
			}
			if err != nil || event != tc.event || !tc.last(last) {
				t.Fatalf("stream ended with event %q %v (%v) in %q", event, last, err, all)
			}
			if tr := traceOf(t, h, resp.Header.Get(TraceHeader)); tr.GetStatus() != http.StatusOK || !strings.Contains(tr.GetError(), "deny pattern") {
				t.Fatalf("trace status %d error %q", tr.GetStatus(), tr.GetError())
			}
		})
	}
}

// A runtime dropping its connection mid stream leaves the 200 and ends the stream with the protocol's upstream error
func TestUpstreamCutEndsStreamWithProtocolError(t *testing.T) {
	for _, tc := range []struct {
		name, path, runtime, body, first string
		// The event name the stream must end with, and the error object of its data
		event string
		err   func(map[string]any) map[string]any
	}{
		{
			name: "openai chat", path: chat.OpenAIChat, runtime: "vllm",
			body:  `{"model":"chat","stream":true,"messages":[{"role":"user","content":"say it"}]}`,
			first: `data: {"id":"c1","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"hello"}}]}` + "\n\n",
			err:   func(m map[string]any) map[string]any { e, _ := m["error"].(map[string]any); return e },
		},
		{
			name: "anthropic", path: chat.AnthropicMessages, runtime: "claude", event: "error",
			body:  `{"model":"chat","stream":true,"max_tokens":8,"messages":[{"role":"user","content":"say it"}]}`,
			first: "event: message_start\n" + `data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"m","content":[],"usage":{"input_tokens":3,"output_tokens":0}}}` + "\n\nevent: content_block_delta\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}` + "\n\n",
			err: func(m map[string]any) map[string]any {
				if m["type"] != "error" {
					return nil
				}
				e, _ := m["error"].(map[string]any)
				return map[string]any{"code": e["type"], "message": e["message"]}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, func(s *setup) { s.runtimes.api["claude"] = v1.ApiFlavor_API_FLAVOR_ANTHROPIC })
			cutting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.ReadAll(r.Body)
				conn, buf, err := http.NewResponseController(w).Hijack()
				if err != nil {
					return
				}
				defer conn.Close()
				fmt.Fprintf(buf, "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nTransfer-Encoding: chunked\r\n\r\n%x\r\n%s\r\n", len(tc.first), tc.first)
				buf.Flush()
			}))
			defer cutting.Close()
			h.publish("chat", "stub", tc.runtime, datalayer.AppProtocolHTTP, true, pools.RouteSpec{ServedName: "m"}, hostOf(cutting))
			r := h.do(http.MethodPost, tc.path, tc.body)
			if r.status != http.StatusOK || !strings.Contains(string(r.body), "hello") {
				t.Fatalf("status %d: %s", r.status, r.body)
			}
			var event string
			var last map[string]any
			if err := chat.ReadSSE(bytes.NewReader(r.body), func(name string, data []byte) error {
				event, last = name, nil
				return json.Unmarshal(data, &last)
			}); err != nil {
				t.Fatalf("stream %q: %v", r.body, err)
			}
			e := tc.err(last)
			msg, _ := e["message"].(string)
			if event != tc.event || e["code"] != chat.CodeServer || !strings.Contains(msg, "unexpected EOF") {
				t.Fatalf("stream ended with event %q %v in %q", event, last, r.body)
			}
			if tr := traceOf(t, h, r.header.Get(TraceHeader)); tr.GetStatus() != http.StatusOK || !strings.Contains(tr.GetError(), "upstream: unexpected EOF") {
				t.Fatalf("trace status %d error %q", tr.GetStatus(), tr.GetError())
			}
		})
	}
}

// A system override or tool attachment on a path the gateway proxies unparsed is refused instead of ignored
func TestUnparsedPathRefusesOverrides(t *testing.T) {
	h := newHarness(t)
	_, srv := h.stub("stub-model")
	h.publish("chat", "stub", "vllm", datalayer.AppProtocolHTTP, true, pools.RouteSpec{ServedName: "stub-model"}, srv.URL)
	r := h.do(http.MethodPost, tokenizePath, `{"model":"chat","prompt":"hi"}`, SystemHeader, "b3ZlcnJpZGU=")
	if r.status != http.StatusBadRequest || !strings.Contains(string(r.body), SystemHeader) {
		t.Fatalf("status %d: %s", r.status, r.body)
	}
	r = h.do(http.MethodPost, tokenizePath, `{"model":"chat","prompt":"hi"}`, ToolsHeader, "x")
	if r.status != http.StatusBadRequest {
		t.Fatalf("status %d: %s", r.status, r.body)
	}
}

// A ghost request builds no trace body while a recorded one keeps request, upstream request and answer
func TestGhostBuildsNoTraceBody(t *testing.T) {
	h := newHarness(t)
	seat := newSeat(t)
	h.publish("chat", "stub", "vllm", datalayer.AppProtocolHTTP, true, pools.RouteSpec{ServedName: "m"}, hostOf(seat.srv))
	run := func(ghost bool) *exchange {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(chatBody))
		req.Header.Set("Content-Type", "application/json")
		if ghost {
			req.Header.Set(GhostHeader, "1")
		}
		rec := httptest.NewRecorder()
		x := h.g.newExchange(rec, req)
		x.run(req.Context())
		x.finish()
		if rec.Code != http.StatusOK {
			t.Fatalf("ghost %v status %d: %s", ghost, rec.Code, rec.Body)
		}
		return x
	}
	ghost := run(true)
	if ghost.traced || ghost.t.GetRequest() != "" || ghost.t.GetUpstreamRequest() != "" || ghost.t.GetResponse() != "" || ghost.sink.text.Len() != 0 || ghost.sink.keep {
		t.Fatalf("ghost trace %v text %d", ghost.t, ghost.sink.text.Len())
	}
	if ghost.resp.FirstTokenAt.IsZero() {
		t.Fatal("a ghost request lost its first token time for the director")
	}
	kept := run(false)
	if !kept.traced || kept.t.GetRequest() != chatBody || !strings.Contains(kept.t.GetUpstreamRequest(), `"model":"m"`) || kept.t.GetResponse() != "seated" {
		t.Fatalf("recorded trace %v", kept.t)
	}
}

// A multipart image edit is read from the form parsed on arrival, never from the bytes again
func TestMultipartParsedOnce(t *testing.T) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.WriteField("model", "sd")
	mw.WriteField("prompt", "a cat")
	mw.WriteField("n", "2")
	fw, _ := mw.CreateFormFile("image", "a.png")
	fw.Write([]byte("png-bytes"))
	mw.Close()
	req := httptest.NewRequest(http.MethodPost, chat.OpenAIEdits, &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	body, _ := io.ReadAll(req.Body)
	f, err := parseForm(req, body)
	if err != nil {
		t.Fatal(err)
	}
	media, err := parseMedia(f, nil)
	if err != nil || media.Model != "sd" || media.Prompt != "a cat" || media.N != 2 || media.InitImage == "" {
		t.Fatalf("media %+v %v", media, err)
	}
	if _, err := parseMedia(nil, body); err == nil {
		t.Fatal("multipart bytes parsed as JSON without the form")
	}
	jsonMedia, err := parseMedia(nil, []byte(`{"model":"sd","prompt":"a dog"}`))
	if err != nil || jsonMedia.Prompt != "a dog" {
		t.Fatalf("json media %+v %v", jsonMedia, err)
	}
}

// The multipart parser never panics and reads the model as mime/multipart does
func FuzzParseForm(f *testing.F) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.WriteField("model", "sd")
	fw, _ := mw.CreateFormFile("image", "a.png")
	fw.Write([]byte("img"))
	mw.Close()
	f.Add(mw.Boundary(), buf.Bytes())
	f.Add("b", []byte("--b\r\nContent-Disposition: form-data; name=\"model\"\r\n\r\nx\r\n--b--\r\n"))
	f.Add("b", []byte("--b\r\nContent-Disposition: form-data; name=\"model[]\"\r\n\r\nx\r\n--b--\r\n"))
	f.Add("", []byte("nothing"))
	f.Add("b", []byte("--b\r\nbroken"))
	f.Fuzz(func(t *testing.T, boundary string, body []byte) {
		req := httptest.NewRequest(http.MethodPost, chat.OpenAIEdits, bytes.NewReader(body))
		req.Header.Set("Content-Type", "multipart/form-data; boundary="+boundary)
		got, err := parseForm(req, body)
		ref := httptest.NewRequest(http.MethodPost, chat.OpenAIEdits, bytes.NewReader(body))
		ref.Header.Set("Content-Type", "multipart/form-data; boundary="+boundary)
		refErr := ref.ParseMultipartForm(int64(maxBody))
		if err != nil {
			if refErr == nil && len(ref.MultipartForm.Value) > 0 {
				t.Fatalf("parseForm refused %q with %v, mime/multipart read %v", body, err, ref.MultipartForm.Value)
			}
			return
		}
		if refErr != nil {
			return
		}
		if _, bracketed := got.fields["model"]; bracketed && ref.FormValue("model") == "" && len(ref.MultipartForm.Value["model[]"]) > 0 {
			return
		}
		if got.value("model") != ref.FormValue("model") {
			t.Fatalf("model %q, mime/multipart read %q", got.value("model"), ref.FormValue("model"))
		}
	})
}

// Members whose tool servers a request names are dialed at once, not one after another
type slowPeers struct {
	delay time.Duration
	dials atomic.Int32
}

func (p *slowPeers) Self() string { return testNode }

func (p *slowPeers) Dial(ctx context.Context, node string) (string, *http.Client, error) {
	p.dials.Add(1)
	select {
	case <-time.After(p.delay):
	case <-ctx.Done():
	}
	return "", nil, fmt.Errorf("node %s is unreachable", node)
}

func TestToolServersResolvedConcurrently(t *testing.T) {
	h := newHarness(t)
	peers := &slowPeers{delay: 400 * time.Millisecond}
	h.g.tools.SetPeers(peers)
	_, srv := h.stub("stub-model")
	h.publish("chat", "stub", "vllm", datalayer.AppProtocolHTTP, true, pools.RouteSpec{ServedName: "stub-model"}, srv.URL)
	start := time.Now()
	r := h.do(http.MethodPost, "/v1/chat/completions", chatBody, ToolsHeader, "n2/a,n3/b,n4/c,n5/d")
	took := time.Since(start)
	if r.status != http.StatusBadRequest || !strings.Contains(string(r.body), "unreachable") {
		t.Fatalf("status %d: %s", r.status, r.body)
	}
	if peers.dials.Load() != 4 || took > 3*peers.delay {
		t.Fatalf("%d dials took %v, want four at once within %v", peers.dials.Load(), took, 3*peers.delay)
	}
}

// A request naming a stored adapter no endpoint serves loads it into the base model's pool through the fake runtime and is served
func TestAdapterLoadedOnDemand(t *testing.T) {
	launcher := fake.New()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := lis.Addr().(*net.TCPAddr).Port
	lis.Close()
	handle, err := launcher.Launch(context.Background(), launch.Spec{Command: "fake", LogPath: filepath.Join(t.TempDir(), "fake.log"), Args: []string{"--kind", "http", "--host", "127.0.0.1", "--port", fmt.Sprint(port), "--name", "base-1", "--served", "base"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { handle.Stop(context.Background(), time.Second) })
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	waitFor(t, "the fake runtime", func() bool {
		resp, err := http.Get("http://" + addr + "/health")
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	})
	stored := []StoredAdapter{{Name: "org/my-lora", Base: "stub", Path: filepath.Join(t.TempDir(), "my-lora")}}
	h := newHarness(t, func(s *setup) { s.adapters = catalog(stored) })
	pool := h.publish("", "stub", "vllm", datalayer.AppProtocolHTTP, true, pools.RouteSpec{}, addr)
	var loads atomic.Int32
	h.inst.mu.Lock()
	h.inst.load = func(ctx context.Context, instanceID, name, path string) error {
		loads.Add(1)
		if err := (fake.Runtime{}).Adapters("http://"+addr, map[string]string{fake.ParamLoRA: "on"}).Load(ctx, name, path); err != nil {
			return err
		}
		owner := pools.OwnerRef{Kind: pools.OwnerInstance, ID: instanceID}
		epAddr, _ := datalayer.ParseAddress(addr)
		return h.mgr.Publish(ctx, owner, pools.Membership{Model: key("stub"), Runtime: "vllm", Protocol: datalayer.AppProtocolHTTP, Concurrency: 8, Metrics: &v1.MetricNames{HealthPath: "/health"}}, []pools.Published{
			{InstanceID: instanceID, NodeID: testNode, Address: epAddr, Ready: true},
			{InstanceID: instanceID, NodeID: testNode, Address: epAddr, Ready: true, Adapter: name},
		})
	}
	h.inst.mu.Unlock()
	r := h.do(http.MethodPost, "/v1/chat/completions", strings.Replace(chatBody, `"model":"chat"`, `"model":"org/my-lora"`, 1))
	if r.status != http.StatusOK || r.header.Get(requestcontrol.HeaderServed) != addr {
		t.Fatalf("status %d served %q: %s", r.status, r.header.Get(requestcontrol.HeaderServed), r.body)
	}
	listed, err := (fake.Runtime{}).Adapters("http://"+addr, map[string]string{fake.ParamLoRA: "on"}).List(context.Background())
	if err != nil || len(listed) != 1 || listed[0].Name != "org/my-lora" || listed[0].Path != stored[0].Path {
		t.Fatalf("runtime adapters %v %v", listed, err)
	}
	if loads.Load() != 1 || len(h.store.Endpoints(pool, datalayer.HasAdapter("org/my-lora"))) != 1 {
		t.Fatalf("%d loads, adapter endpoints %d", loads.Load(), len(h.store.Endpoints(pool, datalayer.HasAdapter("org/my-lora"))))
	}
	again := h.do(http.MethodPost, "/v1/chat/completions", strings.Replace(chatBody, `"model":"chat"`, `"model":"org/my-lora"`, 1))
	if again.status != http.StatusOK || loads.Load() != 1 {
		t.Fatalf("second request %d after %d loads", again.status, loads.Load())
	}
	unknown := h.do(http.MethodPost, "/v1/chat/completions", strings.Replace(chatBody, `"model":"chat"`, `"model":"org/other"`, 1))
	if unknown.status != http.StatusNotFound || loads.Load() != 1 {
		t.Fatalf("unstored adapter %d after %d loads", unknown.status, loads.Load())
	}
}

// A fixed list of stored adapters
type catalog []StoredAdapter

func (c catalog) Adapters() ([]StoredAdapter, error) { return c, nil }

// The models listing and show carry the launch inventory, an embedding only launch listing embedding alone
func TestModelsListingInventory(t *testing.T) {
	h := newHarness(t)
	inv := &v1.RuntimeInventory{ContextLength: 8192, Quantization: "Q4_K_M", Accelerator: "2x NVIDIA H100 80GB HBM3", Modalities: []string{"text", "image"}, ThroughputClass: "device"}
	rt := &v1.Route{Name: "big", Status: &v1.RouteStatus{State: v1.RouteState_ROUTE_STATE_READY, Inventory: inv}}
	for _, api := range []v1.ApiFlavor{v1.ApiFlavor_API_FLAVOR_OPENAI, v1.ApiFlavor_API_FLAVOR_OLLAMA, v1.ApiFlavor_API_FLAVOR_ANTHROPIC} {
		entry := h.g.modelEntry(api, rt)
		got, _ := entry["inventory"].(map[string]any)
		if got["context_length"] != uint32(8192) || got["quantization"] != "Q4_K_M" || got["accelerator"] != inv.Accelerator || got["throughput_class"] != "device" {
			t.Fatalf("%s inventory %v", api, got)
		}
		if mods, _ := got["modalities"].([]string); strings.Join(mods, ",") != "text,image" {
			t.Fatalf("%s modalities %v", api, got["modalities"])
		}
	}
	details, _ := h.g.modelEntry(v1.ApiFlavor_API_FLAVOR_OLLAMA, rt)["details"].(map[string]any)
	if details["quantization_level"] != "Q4_K_M" {
		t.Fatalf("ollama details %v", details)
	}
	embed := &v1.Route{Name: "embed", Status: &v1.RouteStatus{State: v1.RouteState_ROUTE_STATE_READY, Inventory: &v1.RuntimeInventory{Modalities: []string{"embedding"}}}}
	if caps := h.g.capabilities(embed); strings.Join(caps, ",") != "embedding" {
		t.Fatalf("embedding capabilities %v", caps)
	}
	if caps := h.g.capabilities(rt); strings.Join(caps, ",") != "completion,tools" {
		t.Fatalf("chat capabilities %v", caps)
	}
	if caps := h.g.capabilities(&v1.Route{Name: "bare"}); strings.Join(caps, ",") != "completion,tools" {
		t.Fatalf("no inventory capabilities %v", caps)
	}
}

// A prefill seat that does not hand off fails the request instead of decoding without disaggregation
func TestRelayPrefillFailureRefuses(t *testing.T) {
	h := newHarness(t, func(s *setup) {
		s.runtimes.api["nixl"] = v1.ApiFlavor_API_FLAVOR_OPENAI
		s.runtimes.handoff["nixl"] = "nixl"
	})
	prefill := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "no kv", http.StatusInternalServerError) }))
	defer prefill.Close()
	decode := newSeat(t)
	h.seats("nixl", hostOf(prefill), hostOf(decode.srv), nil)
	r := h.do(http.MethodPost, "/v1/chat/completions", chatBody)
	if r.status != http.StatusBadGateway || !strings.Contains(string(r.body), "prefill") {
		t.Fatalf("status %d: %s", r.status, r.body)
	}
	if paths, _ := decode.got(); len(paths) != 0 {
		t.Fatalf("decode seat got %v without a handoff", paths)
	}
	tr := traceOf(t, h, r.header.Get(TraceHeader))
	if tr.GetRelay() != relayNoPrefill {
		t.Fatalf("relay %q", tr.GetRelay())
	}
}
