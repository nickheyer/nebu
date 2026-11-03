package gateway

import (
	"context"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/nickheyer/nebu/internal/pools"
	"github.com/nickheyer/nebu/pkg/chattemplate"
	"github.com/nickheyer/nebu/pkg/datalayer"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/requestcontrol"
	"github.com/nickheyer/nebu/pkg/transcode"
	vllmpb "github.com/nickheyer/nebu/pkg/transcode/gen/vllm"
	"github.com/nickheyer/nebu/pkg/transcode/server"
	"google.golang.org/grpc"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

// A vLLM engine answering every call with a fixed completion and recording the last request
type fakeVLLM struct {
	vllmpb.UnimplementedInferenceServer
	mu  sync.Mutex
	got *vllmpb.GenerateRequest
	// The ids answered in place of text when set, as an engine asked for ids and no text answers
	tokens []uint32
}

func (f *fakeVLLM) record(req *vllmpb.GenerateRequest) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.got = req
}

func (f *fakeVLLM) last() *vllmpb.GenerateRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.got
}

// Makes every later answer the ids, text left empty
func (f *fakeVLLM) answer(ids []uint32) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tokens = ids
}

func (f *fakeVLLM) ids() []uint32 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tokens
}

// The prompt info of a request, token prompts counted by their ids and echoed when the request asks for them
func promptInfo(req *vllmpb.GenerateRequest) *vllmpb.PromptInfo {
	info := &vllmpb.PromptInfo{NumPromptTokens: 5}
	if ids := req.GetTokenIds().GetIds(); len(ids) > 0 {
		info.NumPromptTokens = uint32(len(ids))
		if req.GetResponse().GetPromptTokenIds() {
			info.TokenIds = ids
		}
	}
	return info
}

func (f *fakeVLLM) Generate(_ context.Context, req *vllmpb.GenerateRequest) (*vllmpb.GenerateResponse, error) {
	f.record(req)
	out := &vllmpb.SequenceOutput{Text: "hi there", NumTokens: 2, FinishInfo: &vllmpb.FinishInfo{NumOutputTokens: 2, FinishReason: vllmpb.FinishInfo_STOP}}
	if ids := f.ids(); ids != nil {
		out = &vllmpb.SequenceOutput{TokenIds: ids, NumTokens: uint32(len(ids)), FinishInfo: &vllmpb.FinishInfo{NumOutputTokens: uint32(len(ids)), FinishReason: vllmpb.FinishInfo_STOP}}
	}
	return &vllmpb.GenerateResponse{PromptInfo: promptInfo(req), Outputs: out}, nil
}

func (f *fakeVLLM) GenerateStream(req *vllmpb.GenerateRequest, s vllmpb.Inference_GenerateStreamServer) error {
	f.record(req)
	msgs := []*vllmpb.GenerateResponse{
		{PromptInfo: promptInfo(req)},
		{Outputs: &vllmpb.SequenceOutput{Text: "hi", NumTokens: 1}},
		{Outputs: &vllmpb.SequenceOutput{Text: " there", NumTokens: 1, FinishInfo: &vllmpb.FinishInfo{NumOutputTokens: 2, FinishReason: vllmpb.FinishInfo_STOP}}},
	}
	if ids := f.ids(); ids != nil {
		msgs = msgs[:1]
		for i, id := range ids {
			out := &vllmpb.SequenceOutput{TokenIds: []uint32{id}, NumTokens: 1}
			if i == len(ids)-1 {
				out.FinishInfo = &vllmpb.FinishInfo{NumOutputTokens: uint32(len(ids)), FinishReason: vllmpb.FinishInfo_STOP}
			}
			msgs = append(msgs, &vllmpb.GenerateResponse{Outputs: out})
		}
	}
	for _, m := range msgs {
		if err := s.Send(m); err != nil {
			return err
		}
	}
	return nil
}

// Serves a fake vLLM engine on a loopback port
func startVLLM(t *testing.T) (*fakeVLLM, string) {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeVLLM{}
	srv := grpc.NewServer(server.ServerOptions()...)
	vllmpb.RegisterInferenceServer(srv, f)
	go srv.Serve(lis)
	t.Cleanup(srv.Stop)
	return f, lis.Addr().String()
}

const roleTemplate = `{% for m in messages %}<{{ m.role }}>{{ m.content }}{% endfor %}{% if add_generation_prompt %}<assistant>{% endif %}`

// A gRPC pool takes HTTP chat in every client protocol, the prompt rendered from the instance's chat template
func TestGRPCPool(t *testing.T) {
	h := newHarness(t)
	f, addr := startVLLM(t)
	h.publish("chat", "grpc", "vllm", datalayer.AppProtocolGRPC, true, pools.RouteSpec{ServedName: "served"}, addr)
	h.inst.mu.Lock()
	h.inst.templates["chat-"+addr] = &chattemplate.Template{Text: roleTemplate}
	h.inst.mu.Unlock()
	cases := []struct {
		name, path, body string
		header           []string
		check            func(t *testing.T, r reply)
	}{
		{"openai whole", "/v1/chat/completions", chatBody, nil, func(t *testing.T, r reply) {
			if !strings.Contains(string(r.body), `"content":"hi there"`) {
				t.Fatalf("answer %s", r.body)
			}
		}},
		{"openai stream", "/v1/chat/completions", strings.Replace(chatBody, `"max_tokens":8`, `"max_tokens":8,"stream":true`, 1), nil, func(t *testing.T, r reply) {
			if p := ssePayloads(r.body); len(p) < 3 || p[len(p)-1] != "[DONE]" {
				t.Fatalf("stream %q", p)
			}
		}},
		{"anthropic", "/v1/messages", `{"model":"chat","max_tokens":8,"system":"be brief","messages":[{"role":"user","content":"hello there"}]}`, []string{"anthropic-version", "2023-06-01"}, func(t *testing.T, r reply) {
			if r.json(t)["type"] != "message" || !strings.Contains(string(r.body), "hi there") {
				t.Fatalf("answer %s", r.body)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := h.do(http.MethodPost, tc.path, tc.body, tc.header...)
			if r.status != http.StatusOK {
				t.Fatalf("status %d: %s", r.status, r.body)
			}
			tc.check(t, r)
			if r.header.Get(requestcontrol.HeaderServed) != addr {
				t.Fatalf("served %q", r.header.Get(requestcontrol.HeaderServed))
			}
			got := f.last()
			if got.GetModel() != "served" || got.GetText() != "<system>be brief<user>hello there<assistant>" {
				t.Fatalf("engine got model %q prompt %q", got.GetModel(), got.GetText())
			}
		})
	}
	r := h.do(http.MethodPost, "/v1/embeddings", `{"model":"chat","input":"hello"}`)
	if r.status != http.StatusNotFound {
		t.Fatalf("embeddings over vLLM gRPC answered %d: %s", r.status, r.body)
	}
}

// Inference calls arriving over gRPC are routed by the director, traced, and answered by the picked engine
func TestGRPCIn(t *testing.T) {
	h := newHarness(t)
	f, addr := startVLLM(t)
	h.publish("chat", "grpc", "vllm", datalayer.AppProtocolGRPC, true, pools.RouteSpec{ServedName: "served"}, addr)
	conn, err := transcode.Dial(hostOf(h.srv), transcode.GRPC)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ctx := context.Background()
	resp, err := vllmpb.NewInferenceClient(conn).Generate(ctx, &vllmpb.GenerateRequest{RequestId: "g1", Model: "chat", Prompt: &vllmpb.GenerateRequest_Text{Text: "hello"}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.GetOutputs().GetText() != "hi there" || f.last().GetRequestId() != "g1" {
		t.Fatalf("answer %v, engine saw %v", resp, f.last())
	}
	waitFor(t, "gRPC trace", func() bool {
		list, _ := h.g.Traces().List(ctx, Viewer{All: true}, "chat", 1)
		return len(list) == 1 && list[0].GetFinishedAt() != nil && list[0].GetStatus() == http.StatusOK && list[0].GetEndpoint() == addr && list[0].GetKind() == v1.TraceKind_TRACE_KIND_GENERATE
	})
	if _, err := vllmpb.NewInferenceClient(conn).Generate(ctx, &vllmpb.GenerateRequest{Model: "nope", Prompt: &vllmpb.GenerateRequest_Text{Text: "hello"}}); err == nil || !strings.Contains(err.Error(), "NotFound") {
		t.Fatalf("unknown model answered %v", err)
	}
	health, err := healthpb.NewHealthClient(conn).Check(ctx, &healthpb.HealthCheckRequest{Service: "readiness"})
	if err != nil || health.GetStatus() != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("readiness over gRPC %v %v", health, err)
	}
}
