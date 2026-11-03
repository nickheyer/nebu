// Package server accepts vLLM Inference and SGLang pass-through gRPC calls and forwards each to the backend a router picks
package server

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"slices"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/nickheyer/nebu/pkg/transcode"
	runtimev1 "github.com/nickheyer/nebu/pkg/transcode/gen/sglang/runtime/v1"
	vllmpb "github.com/nickheyer/nebu/pkg/transcode/gen/vllm"
)

// Picks the backend connection for one request, release returns the slot once the call ends
type Router interface {
	// The backend of a vLLM Inference call routed by model and prompt, req rewritten in place for that backend
	Pick(ctx context.Context, req *vllmpb.GenerateRequest, model, prompt string, md metadata.MD) (target *grpc.ClientConn, release func(), err error)
	// The backend of a JSON pass-through call routed by its body under the path, req rewritten in place for it
	PickJSON(ctx context.Context, path string, req *runtimev1.OpenAIRequest, md metadata.MD) (target *grpc.ClientConn, release func(), err error)
	// Told the moment the picked backend answered its response headers, before any message reaches the client
	Headers(ctx context.Context, md metadata.MD)
}

// Serves vllm.Inference by forwarding each call to the backend the router picks
type Server struct {
	vllmpb.UnimplementedInferenceServer
	router Router
}

// Builds a server that asks router for the backend of every call
func New(router Router) *Server {
	return &Server{router: router}
}

// Server options whose keepalive policy admits the idle pings of clients built by transcode.Dial
func ServerOptions() []grpc.ServerOption {
	return []grpc.ServerOption{grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
		MinTime:             transcode.KeepaliveTime / 2,
		PermitWithoutStream: true,
	})}
}

// Mounts the Inference service on a gRPC server
func (s *Server) Register(g grpc.ServiceRegistrar) {
	vllmpb.RegisterInferenceServer(g, s)
}

// Forwards a unary call to the picked backend with client metadata and relays its headers and trailers
func (s *Server) Generate(ctx context.Context, req *vllmpb.GenerateRequest) (*vllmpb.GenerateResponse, error) {
	conn, release, out, err := s.pick(ctx, req)
	if err != nil {
		return nil, err
	}
	defer release()
	var header, trailer metadata.MD
	resp, err := vllmpb.NewInferenceClient(conn).Generate(out, req, grpc.Header(&header), grpc.Trailer(&trailer))
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

// Relays the picked backend stream message by message, cancelling it when the client goes away
func (s *Server) GenerateStream(req *vllmpb.GenerateRequest, stream vllmpb.Inference_GenerateStreamServer) error {
	conn, release, out, err := s.pick(stream.Context(), req)
	if err != nil {
		return err
	}
	defer release()
	out, cancel := context.WithCancel(out)
	defer cancel()
	cs, err := vllmpb.NewInferenceClient(conn).GenerateStream(out, req)
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

// Asks the router for a backend, which may rewrite req, and builds the outgoing context carrying the client metadata
func (s *Server) pick(ctx context.Context, req *vllmpb.GenerateRequest) (*grpc.ClientConn, func(), context.Context, error) {
	prompt, ok := promptKey(req)
	if !ok {
		return nil, nil, nil, status.Error(codes.InvalidArgument, "prompt is required")
	}
	model := req.GetModel()
	if req.GetLoraName() != "" {
		model = req.GetLoraName()
	}
	md, _ := metadata.FromIncomingContext(ctx)
	conn, release, err := s.router.Pick(ctx, req, model, prompt, md)
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

// Returns the prompt text, or token ids as little endian four byte runs so token prefixes stay string prefixes
func promptKey(req *vllmpb.GenerateRequest) (string, bool) {
	switch p := req.GetPrompt().(type) {
	case *vllmpb.GenerateRequest_Text:
		return p.Text, true
	case *vllmpb.GenerateRequest_TokenIds:
		ids := p.TokenIds.GetIds()
		b := make([]byte, 0, 4*len(ids))
		for _, id := range ids {
			b = binary.LittleEndian.AppendUint32(b, id)
		}
		return string(b), true
	}
	return "", false
}

// Copies metadata for the next hop, dropping pseudo headers and keys the gRPC transport owns
func forward(md metadata.MD) metadata.MD {
	out := make(metadata.MD, len(md))
	for k, v := range md {
		if strings.HasPrefix(k, ":") || strings.HasPrefix(k, "grpc-") || k == "content-type" || k == "user-agent" || k == "te" {
			continue
		}
		out[k] = slices.Clone(v)
	}
	return out
}
