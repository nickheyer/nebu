package server

import (
	"context"
	"errors"
	"io"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	runtimev1 "github.com/nickheyer/nebu/pkg/transcode/gen/sglang/runtime/v1"
)

// The OpenAI paths the SGLang pass-through calls stand for, handed to the router with the JSON body
const (
	PathChat        = "/v1/chat/completions"
	PathCompletions = "/v1/completions"
	PathEmbeddings  = "/v1/embeddings"
)

// Serves sglang.runtime.v1.SglangService OpenAI pass-through calls by forwarding each to the backend the router picks
type SGLang struct {
	runtimev1.UnimplementedSglangServiceServer
	router Router
}

// Builds an SGLang server that asks router for the backend of every pass-through call
func NewSGLang(router Router) *SGLang {
	return &SGLang{router: router}
}

// Mounts the SglangService on a gRPC server
func (s *SGLang) Register(g grpc.ServiceRegistrar) {
	runtimev1.RegisterSglangServiceServer(g, s)
}

// Opens one pass-through stream on a backend client
type openStream func(ctx context.Context, in *runtimev1.OpenAIRequest, opts ...grpc.CallOption) (grpc.ServerStreamingClient[runtimev1.OpenAIStreamChunk], error)

func (s *SGLang) ChatComplete(req *runtimev1.OpenAIRequest, stream grpc.ServerStreamingServer[runtimev1.OpenAIStreamChunk]) error {
	return s.relay(PathChat, req, stream, func(c runtimev1.SglangServiceClient) openStream { return c.ChatComplete })
}

func (s *SGLang) Complete(req *runtimev1.OpenAIRequest, stream grpc.ServerStreamingServer[runtimev1.OpenAIStreamChunk]) error {
	return s.relay(PathCompletions, req, stream, func(c runtimev1.SglangServiceClient) openStream { return c.Complete })
}

// Forwards a unary embedding call to the picked backend and relays its headers and trailers
func (s *SGLang) OpenAIEmbed(ctx context.Context, req *runtimev1.OpenAIRequest) (*runtimev1.OpenAIResponse, error) {
	conn, release, out, err := s.pickJSON(ctx, PathEmbeddings, req)
	if err != nil {
		return nil, err
	}
	defer release()
	var header, trailer metadata.MD
	resp, err := runtimev1.NewSglangServiceClient(conn).OpenAIEmbed(out, req, grpc.Header(&header), grpc.Trailer(&trailer))
	if err == nil {
		s.router.Headers(ctx, header)
	}
	if len(header) > 0 {
		if herr := grpc.SetHeader(ctx, forward(header)); herr != nil {
			return nil, herr
		}
	}
	if len(trailer) > 0 {
		if terr := grpc.SetTrailer(ctx, forward(trailer)); terr != nil {
			return nil, terr
		}
	}
	return resp, err
}

// Relays a picked backend's chunk stream message by message, cancelling it when the client goes away
func (s *SGLang) relay(path string, req *runtimev1.OpenAIRequest, stream grpc.ServerStreamingServer[runtimev1.OpenAIStreamChunk], open func(runtimev1.SglangServiceClient) openStream) error {
	conn, release, out, err := s.pickJSON(stream.Context(), path, req)
	if err != nil {
		return err
	}
	defer release()
	out, cancel := context.WithCancel(out)
	defer cancel()
	cs, err := open(runtimev1.NewSglangServiceClient(conn))(out, req)
	if err != nil {
		return err
	}
	header, herr := cs.Header()
	if herr == nil {
		s.router.Headers(stream.Context(), header)
	}
	if herr == nil && len(header) > 0 {
		if err := stream.SendHeader(forward(header)); err != nil {
			return err
		}
	}
	for {
		msg, err := cs.Recv()
		if err != nil {
			stream.SetTrailer(forward(cs.Trailer()))
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if err := stream.Send(msg); err != nil {
			return err
		}
	}
}

// Asks the router for a JSON body's backend, which may rewrite req, and builds the outgoing context with client metadata
func (s *SGLang) pickJSON(ctx context.Context, path string, req *runtimev1.OpenAIRequest) (*grpc.ClientConn, func(), context.Context, error) {
	if len(req.GetJsonBody()) == 0 {
		return nil, nil, nil, status.Error(codes.InvalidArgument, "json_body is required")
	}
	md, _ := metadata.FromIncomingContext(ctx)
	conn, release, err := s.router.PickJSON(ctx, path, req, md)
	if err != nil {
		if _, isStatus := status.FromError(err); isStatus {
			return nil, nil, nil, err
		}
		return nil, nil, nil, status.Error(codes.Unavailable, err.Error())
	}
	if conn == nil {
		return nil, nil, nil, status.Error(codes.Internal, "router picked no backend")
	}
	if release == nil {
		release = func() {}
	}
	return conn, release, metadata.NewOutgoingContext(ctx, forward(md)), nil
}
