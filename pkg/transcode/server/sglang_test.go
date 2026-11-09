package server

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	runtimev1 "github.com/nickheyer/nebu/pkg/transcode/gen/sglang/runtime/v1"
)

// A fake SGLang backend recording the pass-through request and metadata it got and answering scripted chunks
type sglangBackend struct {
	runtimev1.UnimplementedSglangServiceServer
	mu     sync.Mutex
	method string
	got    *runtimev1.OpenAIRequest
	md     metadata.MD
	err    error
}

func (b *sglangBackend) record(ctx context.Context, method string, req *runtimev1.OpenAIRequest) {
	md, _ := metadata.FromIncomingContext(ctx)
	b.mu.Lock()
	defer b.mu.Unlock()
	b.method, b.got, b.md = method, req, md
}

func (b *sglangBackend) stream(method string, req *runtimev1.OpenAIRequest, s grpc.ServerStreamingServer[runtimev1.OpenAIStreamChunk]) error {
	b.record(s.Context(), method, req)
	if b.err != nil {
		return b.err
	}
	if err := s.SendHeader(metadata.Pairs("x-backend", "sg1")); err != nil {
		return err
	}
	s.SetTrailer(metadata.Pairs("x-usage", "2"))
	for _, c := range []*runtimev1.OpenAIStreamChunk{{JsonChunk: []byte(`{"a":1}`)}, {JsonChunk: []byte(`{"b":2}`), Finished: true}} {
		if err := s.Send(c); err != nil {
			return err
		}
	}
	return nil
}

func (b *sglangBackend) ChatComplete(req *runtimev1.OpenAIRequest, s grpc.ServerStreamingServer[runtimev1.OpenAIStreamChunk]) error {
	return b.stream("ChatComplete", req, s)
}

func (b *sglangBackend) Complete(req *runtimev1.OpenAIRequest, s grpc.ServerStreamingServer[runtimev1.OpenAIStreamChunk]) error {
	return b.stream("Complete", req, s)
}

func (b *sglangBackend) OpenAIEmbed(ctx context.Context, req *runtimev1.OpenAIRequest) (*runtimev1.OpenAIResponse, error) {
	b.record(ctx, "OpenAIEmbed", req)
	if b.err != nil {
		return nil, b.err
	}
	if err := grpc.SetHeader(ctx, metadata.Pairs("x-backend", "sg1")); err != nil {
		return nil, err
	}
	return &runtimev1.OpenAIResponse{JsonBody: []byte(`{"data":[]}`), StatusCode: 200}, nil
}

func (b *sglangBackend) seen() (string, *runtimev1.OpenAIRequest, metadata.MD) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.method, b.got, b.md
}

// Wires client to gateway to an SGLang backend
func setupSGLang(t *testing.T, b *sglangBackend) (runtimev1.SglangServiceClient, *router) {
	t.Helper()
	r := &router{conn: serve(t, func(s *grpc.Server) { runtimev1.RegisterSglangServiceServer(s, b) }), headers: make(chan metadata.MD, 8)}
	gw := serve(t, func(s *grpc.Server) { NewSGLang(r).Register(s) }, ServerOptions()...)
	return runtimev1.NewSglangServiceClient(gw), r
}

// Pass-through streams are routed by the body under their path and relayed chunk by chunk with metadata both ways
func TestSGLangStreamsForward(t *testing.T) {
	for _, tc := range []struct {
		name, path, method string
		open               func(runtimev1.SglangServiceClient, context.Context, *runtimev1.OpenAIRequest) (grpc.ServerStreamingClient[runtimev1.OpenAIStreamChunk], error)
	}{
		{"chat", PathChat, "ChatComplete", func(c runtimev1.SglangServiceClient, ctx context.Context, in *runtimev1.OpenAIRequest) (grpc.ServerStreamingClient[runtimev1.OpenAIStreamChunk], error) {
			return c.ChatComplete(ctx, in)
		}},
		{"completions", PathCompletions, "Complete", func(c runtimev1.SglangServiceClient, ctx context.Context, in *runtimev1.OpenAIRequest) (grpc.ServerStreamingClient[runtimev1.OpenAIStreamChunk], error) {
			return c.Complete(ctx, in)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := &sglangBackend{}
			client, r := setupSGLang(t, b)
			ctx := metadata.AppendToOutgoingContext(context.Background(), "x-request-id", "r1", "authorization", "Bearer k")
			body := []byte(`{"model":"m","stream":true}`)
			cs, err := tc.open(client, ctx, &runtimev1.OpenAIRequest{JsonBody: body})
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
			if strings.Join(chunks, "|") != `{"a":1}|{"b":2}` {
				t.Fatalf("chunks %v", chunks)
			}
			if header, _ := cs.Header(); header.Get("x-backend")[0] != "sg1" {
				t.Fatalf("header %v", header)
			}
			if cs.Trailer().Get("x-usage")[0] != "2" {
				t.Fatalf("trailer %v", cs.Trailer())
			}
			if r.model != tc.path || r.prompt != string(body) || r.md.Get("x-request-id")[0] != "r1" {
				t.Fatalf("router asked path %q body %q md %v", r.model, r.prompt, r.md)
			}
			method, got, md := b.seen()
			if method != tc.method || string(got.GetJsonBody()) != string(body) || md.Get("x-request-id")[0] != "r1" || md.Get("authorization")[0] != "Bearer k" {
				t.Fatalf("backend saw %s %s %v", method, got.GetJsonBody(), md)
			}
			if r.releases.Load() != 1 {
				t.Fatalf("released %d", r.releases.Load())
			}
		})
	}
}

// Embeddings forward as one unary call carrying the body the router wrote in, with the backend's headers relayed
func TestSGLangEmbedForwards(t *testing.T) {
	b := &sglangBackend{}
	client, r := setupSGLang(t, b)
	r.rewrite = `{"model":"served","input":"x"}`
	var header metadata.MD
	resp, err := client.OpenAIEmbed(context.Background(), &runtimev1.OpenAIRequest{JsonBody: []byte(`{"model":"m","input":"x"}`)}, grpc.Header(&header))
	if err != nil || string(resp.GetJsonBody()) != `{"data":[]}` || header.Get("x-backend")[0] != "sg1" {
		t.Fatalf("resp %v err %v header %v", resp, err, header)
	}
	if method, got, _ := b.seen(); method != "OpenAIEmbed" || string(got.GetJsonBody()) != r.rewrite || r.model != PathEmbeddings || r.releases.Load() != 1 {
		t.Fatalf("backend %s got %s router path %q releases %d", method, got.GetJsonBody(), r.model, r.releases.Load())
	}
}

// An empty body, a router refusal and a backend failure each reach the client as their own status
func TestSGLangErrors(t *testing.T) {
	b := &sglangBackend{}
	client, r := setupSGLang(t, b)
	if _, err := client.OpenAIEmbed(context.Background(), &runtimev1.OpenAIRequest{}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("empty body %v", err)
	}
	r.err = status.Error(codes.NotFound, "no such model")
	if _, err := client.OpenAIEmbed(context.Background(), &runtimev1.OpenAIRequest{JsonBody: []byte(`{}`)}); status.Code(err) != codes.NotFound {
		t.Fatalf("router refusal %v", err)
	}
	r.err = errors.New("plain")
	if _, err := client.OpenAIEmbed(context.Background(), &runtimev1.OpenAIRequest{JsonBody: []byte(`{}`)}); status.Code(err) != codes.Unavailable {
		t.Fatalf("plain router error %v", err)
	}
	r.err = nil
	b.err = status.Error(codes.ResourceExhausted, "full")
	cs, err := client.ChatComplete(context.Background(), &runtimev1.OpenAIRequest{JsonBody: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cs.Recv(); status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("backend failure %v", err)
	}
}
