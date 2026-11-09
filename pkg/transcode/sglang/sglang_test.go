package sglang

import (
	"context"
	"errors"
	"io"
	"maps"
	"net"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"

	"github.com/nickheyer/nebu/pkg/transcode"
	runtimev1 "github.com/nickheyer/nebu/pkg/transcode/gen/sglang/runtime/v1"
)

// fakeSGLang records pass-through requests and answers with scripted chunks, responses and info
type fakeSGLang struct {
	runtimev1.UnimplementedSglangServiceServer
	mu      sync.Mutex
	method  string
	got     *runtimev1.OpenAIRequest
	chunks  []*runtimev1.OpenAIStreamChunk
	err     error
	embed   *runtimev1.OpenAIResponse
	healthy bool
	load    string
	rank    *int32
	info    string
	models  []*runtimev1.ModelCard
}

func (f *fakeSGLang) relay(method string, req *runtimev1.OpenAIRequest, s grpc.ServerStreamingServer[runtimev1.OpenAIStreamChunk]) error {
	f.mu.Lock()
	f.method, f.got = method, req
	chunks, err := f.chunks, f.err
	f.mu.Unlock()
	for _, c := range chunks {
		if err := s.Send(c); err != nil {
			return err
		}
	}
	return err
}

func (f *fakeSGLang) ChatComplete(req *runtimev1.OpenAIRequest, s grpc.ServerStreamingServer[runtimev1.OpenAIStreamChunk]) error {
	return f.relay("ChatComplete", req, s)
}

func (f *fakeSGLang) Complete(req *runtimev1.OpenAIRequest, s grpc.ServerStreamingServer[runtimev1.OpenAIStreamChunk]) error {
	return f.relay("Complete", req, s)
}

func (f *fakeSGLang) OpenAIEmbed(_ context.Context, req *runtimev1.OpenAIRequest) (*runtimev1.OpenAIResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.method, f.got = "OpenAIEmbed", req
	return f.embed, f.err
}

func (f *fakeSGLang) HealthCheck(context.Context, *runtimev1.HealthCheckRequest) (*runtimev1.HealthCheckResponse, error) {
	return &runtimev1.HealthCheckResponse{Healthy: f.healthy}, f.err
}

func (f *fakeSGLang) GetLoad(_ context.Context, req *runtimev1.GetLoadRequest) (*runtimev1.GetLoadResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rank = req.DpRank
	return &runtimev1.GetLoadResponse{JsonInfo: f.load}, nil
}

func (f *fakeSGLang) GetServerInfo(context.Context, *runtimev1.GetServerInfoRequest) (*runtimev1.GetServerInfoResponse, error) {
	return &runtimev1.GetServerInfoResponse{JsonInfo: f.info}, nil
}

func (f *fakeSGLang) ListModels(context.Context, *runtimev1.ListModelsRequest) (*runtimev1.ListModelsResponse, error) {
	return &runtimev1.ListModelsResponse{Models: f.models}, nil
}

func (f *fakeSGLang) seen() (string, *runtimev1.OpenAIRequest) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.method, f.got
}

// Starts the fake on an in-memory listener and returns a Client for it
func newClient(t *testing.T, f *fakeSGLang) *Client {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	runtimev1.RegisterSglangServiceServer(srv, f)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return New(conn)
}

// Writes a streamed reply through a recorder and returns its data payloads and the write result
func sse(t *testing.T, reply *transcode.Reply) ([]string, error) {
	t.Helper()
	rec := httptest.NewRecorder()
	werr := reply.Write(rec)
	src := transcode.NewEventSource(rec.Body)
	var out []string
	for {
		ev, err := src.Next()
		if errors.Is(err, io.EOF) {
			return out, werr
		}
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, ev.Data)
	}
}

func chunk(json string, finished bool, code ...int32) *runtimev1.OpenAIStreamChunk {
	c := &runtimev1.OpenAIStreamChunk{JsonChunk: []byte(json), Finished: finished}
	if len(code) > 0 {
		c.StatusCode = proto.Int32(code[0])
	}
	return c
}

var trace = map[string]string{"traceparent": "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"}

func TestChatUnary(t *testing.T) {
	f := &fakeSGLang{chunks: []*runtimev1.OpenAIStreamChunk{chunk(`{"id":"x","object":"chat.completion","choices":[]}`, true, 200)}}
	c := newClient(t, f)
	body := `{"model":"m","messages":[{"role":"user","content":"hi"}]}`
	reply, err := c.ChatCompletions(context.Background(), []byte(body), false, trace)
	if err != nil {
		t.Fatal(err)
	}
	if reply.Events != nil || string(reply.Body) != `{"id":"x","object":"chat.completion","choices":[]}` {
		t.Fatalf("reply %+v", reply)
	}
	method, got := f.seen()
	if method != "ChatComplete" || string(got.GetJsonBody()) != body || !maps.Equal(got.GetTraceHeaders(), trace) {
		t.Fatalf("backend saw %s %s %v", method, got.GetJsonBody(), got.GetTraceHeaders())
	}
}

func TestCompletionsStream(t *testing.T) {
	f := &fakeSGLang{chunks: []*runtimev1.OpenAIStreamChunk{
		chunk(`{"choices":[{"text":"a"}]}`, false),
		chunk(`{"choices":[{"text":"b"}]}`, false),
		chunk(``, true),
	}}
	c := newClient(t, f)
	reply, err := c.Completions(context.Background(), []byte(`{"prompt":"p","stream":true}`), true, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := sse(t, reply)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{`{"choices":[{"text":"a"}]}`, `{"choices":[{"text":"b"}]}`, "[DONE]"}; !slices.Equal(got, want) {
		t.Fatalf("events %q", got)
	}
	if method, _ := f.seen(); method != "Complete" {
		t.Fatalf("method %s", method)
	}
}

func TestStreamErrors(t *testing.T) {
	cases := []struct {
		name   string
		chunks []*runtimev1.OpenAIStreamChunk
		err    error
		events []string
		status int
	}{
		{
			name:   "finished with error status",
			chunks: []*runtimev1.OpenAIStreamChunk{chunk(`{"a":1}`, false), chunk(`{"error":{"message":"bad"}}`, true, 400)},
			events: []string{`{"a":1}`, `{"error":{"message":"bad"}}`, "[DONE]"},
			status: 400,
		},
		{
			name:   "grpc failure",
			chunks: []*runtimev1.OpenAIStreamChunk{chunk(`{"a":1}`, false)},
			err:    status.Error(codes.Unavailable, "engine gone"),
			events: []string{`{"a":1}`, `{"error":{"message":"engine gone","type":"ServiceUnavailableError","param":null,"code":503}}`, "[DONE]"},
			status: 503,
		},
		{
			name:   "in stream error event",
			chunks: []*runtimev1.OpenAIStreamChunk{chunk(` { "error":{"message":"too long","code":422}}`, false), chunk(``, true)},
			events: []string{` { "error":{"message":"too long","code":422}}`, "[DONE]"},
			status: 422,
		},
		{
			name:   "ends without finish",
			chunks: []*runtimev1.OpenAIStreamChunk{chunk(`{"a":1}`, false)},
			events: []string{`{"a":1}`, `{"error":{"message":"backend stream ended without a finished chunk","type":"BadGatewayError","param":null,"code":502}}`, "[DONE]"},
			status: 502,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeSGLang{chunks: tc.chunks, err: tc.err}
			c := newClient(t, f)
			reply, err := c.ChatCompletions(context.Background(), []byte(`{}`), true, nil)
			if err != nil {
				t.Fatal(err)
			}
			got, werr := sse(t, reply)
			var te *transcode.Error
			if !errors.As(werr, &te) || te.Status != tc.status {
				t.Fatalf("Write returned %v, want status %d", werr, tc.status)
			}
			if !slices.Equal(got, tc.events) {
				t.Fatalf("events %q", got)
			}
		})
	}
}

func TestEarlyErrors(t *testing.T) {
	cases := []struct {
		name   string
		stream bool
		chunks []*runtimev1.OpenAIStreamChunk
		err    error
		status int
		body   string
	}{
		{"stream rejected", true, []*runtimev1.OpenAIStreamChunk{chunk(`{"error":{"message":"bad model"}}`, true, 404)}, nil, 404, `{"error":{"message":"bad model"}}`},
		{"unary rejected", false, []*runtimev1.OpenAIStreamChunk{chunk(`{"error":{"message":"invalid"}}`, true, 400)}, nil, 400, `{"error":{"message":"invalid"}}`},
		{"unary non json", false, []*runtimev1.OpenAIStreamChunk{chunk(`busy`, true, 503)}, nil, 503, `{"error":{"message":"busy","type":"ServiceUnavailableError","param":null,"code":503}}`},
		{"unary odd status", false, []*runtimev1.OpenAIStreamChunk{chunk(`{}`, true, 302)}, nil, 502, `{"error":{"message":"backend answered with status 302","type":"BadGatewayError","param":null,"code":502}}`},
		{"unary partial", false, []*runtimev1.OpenAIStreamChunk{chunk(`{"a":1}`, false)}, nil, 502, `{"error":{"message":"backend streamed chunks for a non-streaming request","type":"BadGatewayError","param":null,"code":502}}`},
		{"grpc", true, nil, status.Error(codes.InvalidArgument, "bad json"), 400, `{"error":{"message":"bad json","type":"BadRequestError","param":null,"code":400}}`},
		{"empty", false, nil, nil, 502, `{"error":{"message":"backend stream ended without a finished chunk","type":"BadGatewayError","param":null,"code":502}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeSGLang{chunks: tc.chunks, err: tc.err}
			c := newClient(t, f)
			_, err := c.ChatCompletions(context.Background(), []byte(`{}`), tc.stream, nil)
			var te *transcode.Error
			if !errors.As(err, &te) || te.Status != tc.status || string(te.Body) != tc.body {
				t.Fatalf("got %v, want %d %s", err, tc.status, tc.body)
			}
		})
	}
}

func TestEmbeddings(t *testing.T) {
	f := &fakeSGLang{embed: &runtimev1.OpenAIResponse{JsonBody: []byte(`{"data":[{"embedding":[0.1]}]}`), StatusCode: 200}}
	c := newClient(t, f)
	reply, err := c.Embeddings(context.Background(), []byte(`{"input":"x"}`), trace)
	if err != nil {
		t.Fatal(err)
	}
	if string(reply.Body) != `{"data":[{"embedding":[0.1]}]}` {
		t.Fatalf("body %s", reply.Body)
	}
	if method, got := f.seen(); method != "OpenAIEmbed" || string(got.GetJsonBody()) != `{"input":"x"}` || !maps.Equal(got.GetTraceHeaders(), trace) {
		t.Fatalf("backend saw %s %v", method, got)
	}
	f.embed = &runtimev1.OpenAIResponse{JsonBody: []byte(`{"error":{"message":"no"}}`), StatusCode: 400}
	_, err = c.Embeddings(context.Background(), []byte(`{}`), nil)
	var te *transcode.Error
	if !errors.As(err, &te) || te.Status != 400 || string(te.Body) != `{"error":{"message":"no"}}` {
		t.Fatalf("error %v", err)
	}
}

func TestInfo(t *testing.T) {
	f := &fakeSGLang{
		healthy: true,
		load:    `[{"dp_rank":1,"num_reqs":3}]`,
		info:    `{"max_total_num_tokens":4096}`,
		models: []*runtimev1.ModelCard{
			{Id: "base", Root: "/models/base", MaxModelLen: proto.Int32(8192)},
			{Id: "lora-a", Root: "/adapters/a", Parent: proto.String("base")},
		},
	}
	c := newClient(t, f)
	ctx := context.Background()
	if err := c.HealthCheck(ctx); err != nil {
		t.Fatal(err)
	}
	load, err := c.GetLoad(ctx, proto.Int32(1))
	if err != nil || string(load) != f.load || f.rank == nil || *f.rank != 1 {
		t.Fatalf("load %s %v rank %v", load, err, f.rank)
	}
	if _, err := c.GetLoad(ctx, nil); err != nil || f.rank != nil {
		t.Fatalf("all ranks load %v rank %v", err, f.rank)
	}
	info, err := c.GetServerInfo(ctx)
	if err != nil || string(info) != f.info {
		t.Fatalf("info %s %v", info, err)
	}
	models, err := c.ListModels(ctx)
	want := []Model{{ID: "base", Root: "/models/base", MaxModelLen: 8192}, {ID: "lora-a", Root: "/adapters/a", Parent: "base"}}
	if err != nil || !slices.Equal(models, want) {
		t.Fatalf("models %+v %v", models, err)
	}
	f.healthy = false
	if err := c.HealthCheck(ctx); !errors.Is(err, ErrUnhealthy) {
		t.Fatalf("unhealthy check returned %v", err)
	}
	f.info = `{not json`
	var te *transcode.Error
	if _, err := c.GetServerInfo(ctx); !errors.As(err, &te) || te.Status != 502 || !strings.Contains(string(te.Body), "server info is not valid JSON") {
		t.Fatalf("invalid info returned %v", err)
	}
	f.err = status.Error(codes.Unavailable, "down")
	if err := c.HealthCheck(ctx); !errors.As(err, &te) || te.Status != 503 {
		t.Fatalf("failed check returned %v", err)
	}
}
