package telemetry

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// Reads traceparent and tracestate out of incoming gRPC metadata
type metadataCarrier metadata.MD

func (c metadataCarrier) Get(key string) string {
	if v := metadata.MD(c).Get(key); len(v) > 0 {
		return v[0]
	}
	return ""
}

func (c metadataCarrier) Set(key, value string) { metadata.MD(c).Set(key, value) }

func (c metadataCarrier) Keys() []string {
	out := make([]string, 0, len(c))
	for k := range c {
		out = append(out, k)
	}
	return out
}

var _ propagation.TextMapCarrier = metadataCarrier{}

// Starts a server span for a gRPC call, continuing the trace the incoming metadata carries
func startCall(ctx context.Context, method string) (context.Context, trace.Span) {
	md, _ := metadata.FromIncomingContext(ctx)
	ctx = propagator.Extract(ctx, metadataCarrier(md))
	return otel.Tracer(scope).Start(ctx, method, trace.WithSpanKind(trace.SpanKindServer), trace.WithAttributes(
		attribute.String("rpc.system", "grpc"),
		attribute.String("rpc.method", method),
	))
}

// Ends a call span with the gRPC status code the call answered
func endCall(span trace.Span, err error) {
	code := status.Code(err)
	span.SetAttributes(attribute.Int("rpc.grpc.status_code", int(code)))
	if err != nil {
		span.SetStatus(codes.Error, status.Convert(err).Message())
	}
	span.End()
}

// Wraps every unary call in a server span whose context the handler receives
func UnaryServerInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		ctx, span := startCall(ctx, info.FullMethod)
		resp, err := handler(ctx, req)
		endCall(span, err)
		return resp, err
	}
}

// A server stream whose context carries the call span
type spanStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *spanStream) Context() context.Context { return s.ctx }

// Wraps every streaming call in a server span whose context the stream carries
func StreamServerInterceptor() grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		ctx, span := startCall(ss.Context(), info.FullMethod)
		err := handler(srv, &spanStream{ServerStream: ss, ctx: ctx})
		endCall(span, err)
		return err
	}
}
