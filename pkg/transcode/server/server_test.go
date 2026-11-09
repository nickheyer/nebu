package server

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"

	runtimev1 "github.com/nickheyer/nebu/pkg/transcode/gen/sglang/runtime/v1"
	vllmpb "github.com/nickheyer/nebu/pkg/transcode/gen/vllm"
)

// backend is a fake vLLM engine that records metadata and answers with headers, trailers and scripted outputs
type backend struct {
	vllmpb.UnimplementedInferenceServer
	mu        sync.Mutex
	md        metadata.MD
	deadline  time.Time
	got       *vllmpb.GenerateRequest
	err       error
	hold      bool
	cancelled chan struct{}
}

func (b *backend) record(ctx context.Context, req *vllmpb.GenerateRequest) {
	md, _ := metadata.FromIncomingContext(ctx)
	deadline, _ := ctx.Deadline()
	b.mu.Lock()
	defer b.mu.Unlock()
	b.md, b.got, b.deadline = md, req, deadline
}

func (b *backend) Generate(ctx context.Context, req *vllmpb.GenerateRequest) (*vllmpb.GenerateResponse, error) {
	b.record(ctx, req)
	if b.err != nil {
		return nil, b.err
	}
	if err := grpc.SetHeader(ctx, metadata.Pairs("x-backend", "b1")); err != nil {
		return nil, err
	}
	if err := grpc.SetTrailer(ctx, metadata.Pairs("x-usage", "5")); err != nil {
		return nil, err
	}
	return &vllmpb.GenerateResponse{Outputs: &vllmpb.SequenceOutput{Text: "unary " + req.GetRequestId()}}, nil
}

func (b *backend) GenerateStream(req *vllmpb.GenerateRequest, s vllmpb.Inference_GenerateStreamServer) error {
	b.record(s.Context(), req)
	if b.err != nil {
		return b.err
	}
	if err := s.SendHeader(metadata.Pairs("x-backend", "b1")); err != nil {
		return err
	}
	s.SetTrailer(metadata.Pairs("x-usage", "3"))
	for _, text := range []string{"a", "b", "c"} {
		if err := s.Send(&vllmpb.GenerateResponse{Outputs: &vllmpb.SequenceOutput{Text: text}}); err != nil {
			return err
		}
	}
	if b.hold {
		<-s.Context().Done()
		close(b.cancelled)
		return s.Context().Err()
	}
	return nil
}

func (b *backend) seen() (metadata.MD, *vllmpb.GenerateRequest) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.md, b.got
}

// router hands out one backend connection and records what it was asked
type router struct {
	conn     *grpc.ClientConn
	err      error
	model    string
	prompt   string
	md       metadata.MD
	picks    atomic.Int32
	releases atomic.Int32
	// Response header metadata the backend answered, one per call that reached headers
	headers chan metadata.MD
	// Written into each picked request when set, as the model of a vLLM call or the body of a JSON one
	rewrite string
}

func (r *router) Headers(_ context.Context, md metadata.MD) { r.headers <- md }

func (r *router) PickJSON(_ context.Context, path string, req *runtimev1.OpenAIRequest, md metadata.MD) (*grpc.ClientConn, func(), error) {
	r.picks.Add(1)
	r.model, r.prompt, r.md = path, string(req.GetJsonBody()), md
	if r.err != nil {
		return nil, nil, r.err
	}
	if r.rewrite != "" {
		req.JsonBody = []byte(r.rewrite)
	}
	return r.conn, func() { r.releases.Add(1) }, nil
}

func (r *router) Pick(_ context.Context, req *vllmpb.GenerateRequest, model, prompt string, md metadata.MD) (*grpc.ClientConn, func(), error) {
	r.picks.Add(1)
	r.model, r.prompt, r.md = model, prompt, md
	if r.err != nil {
		return nil, nil, r.err
	}
	if r.rewrite != "" {
		req.Model = r.rewrite
	}
	return r.conn, func() { r.releases.Add(1) }, nil
}

// Serves register on an in-memory listener with the given options and returns a client connection to it
func serve(t *testing.T, register func(*grpc.Server), opts ...grpc.ServerOption) *grpc.ClientConn {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer(opts...)
	register(srv)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// Wires client to gateway to backend and returns the client, router and backend
func setup(t *testing.T, b *backend) (vllmpb.InferenceClient, *router) {
	t.Helper()
	r := &router{conn: serve(t, func(s *grpc.Server) { vllmpb.RegisterInferenceServer(s, b) }), headers: make(chan metadata.MD, 8)}
	gw := serve(t, func(s *grpc.Server) { New(r).Register(s) }, ServerOptions()...)
	return vllmpb.NewInferenceClient(gw), r
}

func TestGenerateForwards(t *testing.T) {
	b := &backend{}
	client, r := setup(t, b)
	ctx := metadata.AppendToOutgoingContext(context.Background(), "x-tenant", "acme", "x-session-id", "s1")
	var header, trailer metadata.MD
	req := &vllmpb.GenerateRequest{RequestId: "r1", Model: "m", Prompt: &vllmpb.GenerateRequest_Text{Text: "hello"}}
	resp, err := client.Generate(ctx, req, grpc.Header(&header), grpc.Trailer(&trailer))
	if err != nil {
		t.Fatal(err)
	}
	if resp.GetOutputs().GetText() != "unary r1" {
		t.Fatalf("response %v", resp)
	}
	md, got := b.seen()
	if !proto.Equal(got, req) {
		t.Fatalf("backend got %v", got)
	}
	if md.Get("x-tenant")[0] != "acme" || md.Get("x-session-id")[0] != "s1" {
		t.Fatalf("backend metadata %v", md)
	}
	if r.model != "m" || r.prompt != "hello" || r.md.Get("x-tenant")[0] != "acme" {
		t.Fatalf("router saw %q %q %v", r.model, r.prompt, r.md)
	}
	if v := header.Get("x-backend"); len(v) != 1 || v[0] != "b1" {
		t.Fatalf("header %v", header)
	}
	if v := trailer.Get("x-usage"); len(v) != 1 || v[0] != "5" {
		t.Fatalf("trailer %v", trailer)
	}
	if r.releases.Load() != 1 {
		t.Fatalf("releases %d", r.releases.Load())
	}
}

// A stream reaches the backend with the model the router wrote in and relays every message, header and trailer
func TestGenerateStreamForwards(t *testing.T) {
	b := &backend{}
	client, r := setup(t, b)
	r.rewrite = "served"
	ctx := metadata.AppendToOutgoingContext(context.Background(), "x-tenant", "acme")
	req := &vllmpb.GenerateRequest{RequestId: "r2", LoraName: "lora-a", Prompt: &vllmpb.GenerateRequest_TokenIds{TokenIds: &vllmpb.TokenIds{Ids: []uint32{1, 258}}}}
	st, err := client.GenerateStream(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	var texts string
	for {
		msg, err := st.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		texts += msg.GetOutputs().GetText()
	}
	if texts != "abc" {
		t.Fatalf("relayed %q", texts)
	}
	header, err := st.Header()
	if err != nil || header.Get("x-backend")[0] != "b1" || st.Trailer().Get("x-usage")[0] != "3" {
		t.Fatalf("header %v trailer %v err %v", header, st.Trailer(), err)
	}
	wantPrompt := string(binary.LittleEndian.AppendUint32(binary.LittleEndian.AppendUint32(nil, 1), 258))
	if r.model != "lora-a" || r.prompt != wantPrompt {
		t.Fatalf("router saw %q %x", r.model, r.prompt)
	}
	if md, got := b.seen(); md.Get("x-tenant")[0] != "acme" || got.GetModel() != "served" || got.GetRequestId() != "r2" {
		t.Fatalf("backend metadata %v request %v", md, got)
	}
	if r.releases.Load() != 1 {
		t.Fatalf("releases %d", r.releases.Load())
	}
}

func TestErrors(t *testing.T) {
	cases := []struct {
		name      string
		routerErr error
		backErr   error
		req       *vllmpb.GenerateRequest
		code      codes.Code
		msg       string
		picks     int32
	}{
		{"no prompt", nil, nil, &vllmpb.GenerateRequest{}, codes.InvalidArgument, "prompt is required", 0},
		{"router plain error", errors.New("no endpoint ready"), nil, nil, codes.Unavailable, "no endpoint ready", 1},
		{"router status", status.Error(codes.ResourceExhausted, "shed"), nil, nil, codes.ResourceExhausted, "shed", 1},
		{"backend status", nil, status.Error(codes.NotFound, "model `x` not found"), nil, codes.NotFound, "model `x` not found", 1},
	}
	for _, tc := range cases {
		for _, streaming := range []bool{false, true} {
			b := &backend{err: tc.backErr}
			client, r := setup(t, b)
			r.err = tc.routerErr
			req := tc.req
			if req == nil {
				req = &vllmpb.GenerateRequest{Model: "x", Prompt: &vllmpb.GenerateRequest_Text{Text: "p"}}
			}
			var err error
			if streaming {
				var st grpc.ServerStreamingClient[vllmpb.GenerateResponse]
				if st, err = client.GenerateStream(context.Background(), req); err == nil {
					_, err = st.Recv()
				}
			} else {
				_, err = client.Generate(context.Background(), req)
			}
			if s := status.Convert(err); s.Code() != tc.code || s.Message() != tc.msg {
				t.Errorf("%s stream=%v got %v", tc.name, streaming, err)
			}
			if r.picks.Load() != tc.picks || r.releases.Load() != r.picks.Load()-boolInt(tc.routerErr != nil) {
				t.Errorf("%s stream=%v picks %d releases %d", tc.name, streaming, r.picks.Load(), r.releases.Load())
			}
		}
	}
}

func boolInt(b bool) int32 {
	if b {
		return 1
	}
	return 0
}

func TestStreamCancellationReachesBackend(t *testing.T) {
	b := &backend{hold: true, cancelled: make(chan struct{})}
	client, r := setup(t, b)
	ctx, cancel := context.WithCancel(context.Background())
	st, err := client.GenerateStream(ctx, &vllmpb.GenerateRequest{Prompt: &vllmpb.GenerateRequest_Text{Text: "p"}})
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if _, err := st.Recv(); err != nil {
			t.Fatal(err)
		}
	}
	cancel()
	select {
	case <-b.cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("backend never saw the client cancel")
	}
	deadline := time.Now().Add(5 * time.Second)
	for r.releases.Load() != 1 {
		if time.Now().After(deadline) {
			t.Fatal("slot never released after cancel")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestDeadlinePropagates(t *testing.T) {
	b := &backend{}
	client, _ := setup(t, b)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if _, err := client.Generate(ctx, &vllmpb.GenerateRequest{Prompt: &vllmpb.GenerateRequest_Text{Text: "p"}}); err != nil {
		t.Fatal(err)
	}
	md, _ := b.seen()
	if len(md.Get("grpc-timeout")) != 0 {
		t.Fatalf("raw grpc-timeout forwarded as metadata %v", md)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if left := time.Until(b.deadline); b.deadline.IsZero() || left > time.Minute || left < 50*time.Second {
		t.Fatalf("backend deadline %v, want about a minute away", b.deadline)
	}
}

func TestForwardDropsTransportKeys(t *testing.T) {
	in := metadata.MD{
		":authority":   {"gw"},
		"content-type": {"application/grpc"},
		"user-agent":   {"grpc-go"},
		"te":           {"trailers"},
		"grpc-timeout": {"1S"},
		"x-keep":       {"a", "b"},
	}
	out := forward(in)
	if len(out) != 1 || len(out.Get("x-keep")) != 2 {
		t.Fatalf("forwarded %v", out)
	}
	out["x-keep"][0] = "changed"
	if in["x-keep"][0] != "a" {
		t.Fatal("forward shares value slices with its input")
	}
}

// The router hears the backend's response headers once per answered call, unary and stream alike, and never for a refused pick
func TestHeadersReachTheRouter(t *testing.T) {
	b := &backend{}
	client, r := setup(t, b)
	ctx := context.Background()
	if _, err := client.Generate(ctx, &vllmpb.GenerateRequest{RequestId: "h1", Model: "m", Prompt: &vllmpb.GenerateRequest_Text{Text: "hi"}}); err != nil {
		t.Fatal(err)
	}
	if md := <-r.headers; md.Get("x-backend")[0] != "b1" {
		t.Fatalf("unary headers %v", md)
	}
	st, err := client.GenerateStream(ctx, &vllmpb.GenerateRequest{RequestId: "h2", Model: "m", Prompt: &vllmpb.GenerateRequest_Text{Text: "hi"}})
	if err != nil {
		t.Fatal(err)
	}
	if md := <-r.headers; md.Get("x-backend")[0] != "b1" {
		t.Fatalf("stream headers %v", md)
	}
	for {
		if _, err := st.Recv(); err != nil {
			if !errors.Is(err, io.EOF) {
				t.Fatal(err)
			}
			break
		}
	}
	r.err = errors.New("no backend")
	if _, err := client.Generate(ctx, &vllmpb.GenerateRequest{RequestId: "h3", Model: "m", Prompt: &vllmpb.GenerateRequest_Text{Text: "hi"}}); err == nil {
		t.Fatal("refused pick answered")
	}
	if len(r.headers) != 0 {
		t.Fatalf("a refused pick reported headers %v", <-r.headers)
	}
}
