package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/nickheyer/nebu/pkg/bodyparse"
	"github.com/nickheyer/nebu/pkg/datalayer"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/telemetry"
	"github.com/nickheyer/nebu/pkg/transcode"
	runtimev1 "github.com/nickheyer/nebu/pkg/transcode/gen/sglang/runtime/v1"
	vllmpb "github.com/nickheyer/nebu/pkg/transcode/gen/vllm"
	"github.com/nickheyer/nebu/pkg/transcode/server"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

// The method prefixes of the vLLM Inference and SGLang services the gateway accepts
const (
	inferencePrefix = "/vllm.Inference/"
	sglangPrefix    = "/sglang.runtime.v1.SglangService/"
)

// Whether a gRPC method is one the gateway routes to a backend
func inboundCall(method string) bool {
	return strings.HasPrefix(method, inferencePrefix) || strings.HasPrefix(method, sglangPrefix)
}

// Server options admitting transcoder client pings and tracing every Inference call as a gateway request
func (g *Gateway) grpcOptions() []grpc.ServerOption {
	return append(server.ServerOptions(), grpc.ChainUnaryInterceptor(telemetry.UnaryServerInterceptor(), g.unaryIn), grpc.ChainStreamInterceptor(telemetry.StreamServerInterceptor(), g.streamIn))
}

// Mounts the vLLM Inference and SGLang services that forward each call to the endpoint the director picks
func (g *Gateway) registerTranscoder(s *grpc.Server) {
	rt := &router{g: g}
	server.New(rt).Register(s)
	server.NewSGLang(rt).Register(s)
}

// A response writer for calls answered over gRPC, recording the status the call ended with
type grpcAnswer struct{ h http.Header }

func (a *grpcAnswer) Header() http.Header         { return a.h }
func (a *grpcAnswer) Write(b []byte) (int, error) { return len(b), nil }
func (a *grpcAnswer) WriteHeader(int)             {}

// Starts an exchange for an Inference call, refusing callers without the inference scope or a gateway key
func (g *Gateway) callIn(ctx context.Context, method string) (*exchange, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	h := http.Header{}
	for k, v := range md {
		if !strings.HasPrefix(k, ":") {
			h[http.CanonicalHeaderKey(k)] = v
		}
	}
	remote := ""
	if p, ok := peer.FromContext(ctx); ok {
		remote = p.Addr.String()
	}
	r := (&http.Request{Method: http.MethodPost, URL: &url.URL{Path: method}, Header: h, RemoteAddr: remote}).WithContext(ctx)
	who, ok := g.identify(r)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "missing or invalid api key, or a credential without the inference scope")
	}
	x := &exchange{g: g, w: &writer{ResponseWriter: &grpcAnswer{h: http.Header{}}, clock: g.clock}, r: r, start: g.clock.Now(), who: who, policy: effectivePolicy(nil, g.policy())}
	x.id = firstOf(strings.TrimSpace(h.Get("X-Request-Id")), newRequestID())
	x.t = newTrace(x)
	x.t.Kind = v1.TraceKind_TRACE_KIND_GENERATE
	x.sink = &traceSink{t: x.t, clock: g.clock}
	x.t.Stream = strings.HasSuffix(method, "Stream")
	m, err := parseModes(h)
	x.modes = m
	x.record()
	if err != nil {
		err = status.Error(codes.InvalidArgument, failureOf(err).Message)
		x.endCall(err)
		return nil, err
	}
	return x, nil
}

// Ends an Inference call's exchange with the status the call ended with
func (x *exchange) endCall(err error) {
	code := status.Code(err)
	x.w.wrote, x.w.status = true, transcode.HTTPStatus(code)
	if err != nil {
		x.t.Error, x.resp.Err = status.Convert(err).Message(), err
	}
	if x.dec != nil {
		x.resp.Status = x.w.status
		x.complete(context.Background())
	}
	x.finish()
}

func (g *Gateway) unaryIn(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	if !inboundCall(info.FullMethod) {
		return handler(ctx, req)
	}
	x, err := g.callIn(ctx, info.FullMethod)
	if err != nil {
		return nil, err
	}
	resp, err := handler(withExchange(ctx, x), req)
	x.endCall(err)
	return resp, err
}

// A server stream whose context carries the call's exchange
type exchangeStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *exchangeStream) Context() context.Context { return s.ctx }

func (g *Gateway) streamIn(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	if !inboundCall(info.FullMethod) {
		return handler(srv, ss)
	}
	x, err := g.callIn(ss.Context(), info.FullMethod)
	if err != nil {
		return err
	}
	err = handler(srv, &exchangeStream{ServerStream: ss, ctx: withExchange(ss.Context(), x)})
	x.endCall(err)
	return err
}

// Picks the backend of an Inference call through the director, the decision held until the call ends
type router struct{ g *Gateway }

// Stamps the first byte when the backend answers its headers and keeps them for the director
func (rt *router) Headers(ctx context.Context, md metadata.MD) {
	x := exchangeOf(ctx)
	x.resp.FirstByteAt = rt.g.clock.Now()
	for k, v := range md {
		x.resp.Headers[k] = strings.Join(v, ", ")
	}
}

// Picks the backend of a vLLM call under its request id, addressing the decision's target model or adapter
func (rt *router) Pick(ctx context.Context, req *vllmpb.GenerateRequest, model, prompt string, _ metadata.MD) (*grpc.ClientConn, func(), error) {
	x := exchangeOf(ctx)
	if id := req.GetRequestId(); id != "" {
		x.id, x.t.RequestId = id, id
	}
	body, err := json.Marshal(map[string]string{"model": model, "prompt": prompt})
	if err != nil {
		return nil, nil, status.Error(codes.InvalidArgument, err.Error())
	}
	doc, err := bodyparse.Parse(bodyparse.KindCompletions, body)
	if err != nil {
		return nil, nil, status.Error(codes.InvalidArgument, err.Error())
	}
	conn, release, err := rt.decide(ctx, model, doc, runtimeVLLM)
	if err != nil {
		return nil, nil, err
	}
	req.Model, req.LoraName = x.dec.TargetModel, ""
	if x.dec.Adapter != "" {
		req.Model, req.LoraName = "", x.dec.Adapter
	}
	return conn, release, nil
}

// Picks the backend of an SGLang pass-through call by the model its JSON body names, sending the body the stages left
func (rt *router) PickJSON(ctx context.Context, path string, req *runtimev1.OpenAIRequest, _ metadata.MD) (*grpc.ClientConn, func(), error) {
	doc, err := bodyparse.Parse(bodyparse.KindOf(path), req.GetJsonBody())
	if err != nil {
		return nil, nil, status.Error(codes.InvalidArgument, err.Error())
	}
	model, err := doc.Model()
	if err != nil {
		return nil, nil, status.Error(codes.InvalidArgument, err.Error())
	}
	conn, release, err := rt.decide(ctx, model, doc, runtimeSGLang)
	if err != nil {
		return nil, nil, err
	}
	req.JsonBody = exchangeOf(ctx).document().Bytes()
	return conn, release, nil
}

// Decides the backend of a gRPC call through the director, refusing a pool of another runtime or protocol
func (rt *router) decide(ctx context.Context, model string, doc bodyparse.Document, runtime string) (*grpc.ClientConn, func(), error) {
	g := rt.g
	x := exchangeOf(ctx)
	x.model = model
	x.t.Route = model
	if r, ok := g.pools.RouteView(model); ok {
		x.route = r
		x.policy = effectivePolicy(r.GetSpec().GetPolicy(), g.policy())
	}
	x.doc = doc
	x.dir = g.current()
	dec, err := x.dir.Decide(ctx, x.inbound())
	if err != nil {
		x.hold.release()
		f := g.refusal(err, model, x.route, x.pool.ID)
		return nil, nil, status.Error(grpcCode(f.Status), f.Message)
	}
	x.dec = dec
	x.upstream = g.runtimes.API(dec.Pool.Runtime)
	x.t.UpstreamApi, x.t.FailedOpen, x.t.Profile = x.upstream, dec.FailedOpen, dec.Result.PrimaryProfile
	if dec.Permit != nil {
		x.t.QueueWaitMs = uint32(dec.Permit.Waited().Milliseconds())
	}
	ep := dec.Endpoints[0]
	if dec.Pool.AppProtocol != datalayer.AppProtocolGRPC {
		return nil, nil, status.Errorf(codes.FailedPrecondition, "pool %s speaks %s, not gRPC", dec.Pool.ID, dec.Pool.AppProtocol)
	}
	if dec.Pool.Runtime != runtime {
		return nil, nil, status.Errorf(codes.FailedPrecondition, "pool %s runs %s, and this call speaks the %s service", dec.Pool.ID, dec.Pool.Runtime, runtime)
	}
	conn, err := g.conns.get(ep.Address().String())
	if err != nil {
		return nil, nil, status.Error(codes.Unavailable, err.Error())
	}
	x.countOn(ep)
	x.resp.StartedAt = g.clock.Now()
	if err := x.received(ctx, &http.Response{StatusCode: http.StatusOK, Header: http.Header{}}, ep); err != nil {
		return nil, nil, status.Error(codes.Internal, err.Error())
	}
	return conn, func() { x.resp.FinishedAt = g.clock.Now() }, nil
}

// The gRPC code an HTTP refusal status maps to
func grpcCode(httpStatus int) codes.Code {
	switch httpStatus {
	case http.StatusBadRequest, http.StatusRequestEntityTooLarge:
		return codes.InvalidArgument
	case http.StatusUnauthorized:
		return codes.Unauthenticated
	case http.StatusForbidden:
		return codes.PermissionDenied
	case http.StatusNotFound:
		return codes.NotFound
	case http.StatusTooManyRequests:
		return codes.ResourceExhausted
	case http.StatusServiceUnavailable:
		return codes.Unavailable
	case http.StatusGatewayTimeout:
		return codes.DeadlineExceeded
	}
	return codes.Internal
}
