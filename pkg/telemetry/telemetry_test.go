package telemetry

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/grpc"
	grpccodes "google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"
)

func setup(t *testing.T, cfg Config) *tracetest.InMemoryExporter {
	t.Helper()
	exp := tracetest.NewInMemoryExporter()
	cfg.Exporter = exp
	_, shutdown, err := Setup(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = shutdown(context.Background()) })
	return exp
}

func stubAttrs(s tracetest.SpanStub) map[attribute.Key]attribute.Value {
	m := map[attribute.Key]attribute.Value{}
	for _, kv := range s.Attributes {
		m[kv.Key] = kv.Value
	}
	return m
}

func TestExporterReceivesRequestSpans(t *testing.T) {
	exp := setup(t, Config{ServiceName: "nebu-test", SampleRatio: 1})
	ctx, parent := StartRequestSpan(context.Background(), "gateway.request", attribute.String("nebu.pool", "p1"))
	_, child := StartRequestSpan(ctx, "scheduler.pick", attribute.Int("nebu.candidates", 3))
	child.End()
	parent.End()

	spans := exp.GetSpans()
	if len(spans) != 2 {
		t.Fatalf("exporter holds %d spans, want 2", len(spans))
	}
	if spans[0].Name != "scheduler.pick" || spans[1].Name != "gateway.request" {
		t.Fatalf("names %q %q", spans[0].Name, spans[1].Name)
	}
	if got := stubAttrs(spans[0])["nebu.candidates"].AsInt64(); got != 3 {
		t.Fatalf("child attribute %d", got)
	}
	if got := stubAttrs(spans[1])["nebu.pool"].AsString(); got != "p1" {
		t.Fatalf("parent attribute %q", got)
	}
	if spans[0].Parent.SpanID() != spans[1].SpanContext.SpanID() {
		t.Fatal("child span is not parented to the request span")
	}
	if spans[0].SpanKind != trace.SpanKindInternal {
		t.Fatalf("kind %v", spans[0].SpanKind)
	}
	svc, ok := spans[1].Resource.Set().Value("service.name")
	if !ok || svc.AsString() != "nebu-test" {
		t.Fatalf("service name %v %v", svc, ok)
	}
}

func TestSampleRatioZeroRecordsNothing(t *testing.T) {
	exp := setup(t, Config{ServiceName: "nebu-test", SampleRatio: 0})
	_, s := StartRequestSpan(context.Background(), "dropped")
	s.End()
	if n := len(exp.GetSpans()); n != 0 {
		t.Fatalf("exporter holds %d spans at ratio zero", n)
	}
}

var traceparentRE = regexp.MustCompile(`^00-[0-9a-f]{32}-[0-9a-f]{16}-01$`)

func TestPropagationRoundTrip(t *testing.T) {
	setup(t, Config{ServiceName: "nebu-test", SampleRatio: 1})
	ctx, span := StartRequestSpan(context.Background(), "outbound")
	defer span.End()

	h := http.Header{}
	Inject(ctx, h)
	if tp := h.Get("traceparent"); !traceparentRE.MatchString(tp) {
		t.Fatalf("traceparent %q", tp)
	}
	got := trace.SpanContextFromContext(Extract(context.Background(), h))
	want := span.SpanContext()
	if !got.IsValid() || !got.IsRemote() || got.TraceID() != want.TraceID() || got.SpanID() != want.SpanID() || !got.IsSampled() {
		t.Fatalf("extracted %+v, want trace %s span %s", got, want.TraceID(), want.SpanID())
	}
	if sc := trace.SpanContextFromContext(Extract(context.Background(), http.Header{})); sc.IsValid() {
		t.Fatal("extract from empty headers produced a span context")
	}
}

func TestMiddleware(t *testing.T) {
	exp := setup(t, Config{ServiceName: "nebu-test", SampleRatio: 1})
	const modelHeader = "X-Gateway-Model-Name"
	h := Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !trace.SpanContextFromContext(r.Context()).IsValid() {
			t.Error("handler context carries no span")
		}
		switch r.URL.Path {
		case "/created":
			w.WriteHeader(http.StatusCreated)
		case "/fail":
			http.Error(w, "boom", http.StatusBadGateway)
		case "/stream":
			_, _ = io.WriteString(w, "data: x\n\n")
			w.(http.Flusher).Flush()
		}
	}), modelHeader)

	upstreamCtx, upstream := StartRequestSpan(context.Background(), "client")
	upstream.End()

	tests := []struct {
		name       string
		path       string
		model      string
		propagate  bool
		wantStatus int64
		wantError  bool
	}{
		{"created with model and parent", "/created", "llama-3", true, 201, false},
		{"implicit ok without model", "/ok", "", false, 200, false},
		{"server error", "/fail", "m", false, 502, true},
		{"streaming flush", "/stream", "", false, 200, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, tc.path, nil)
			if tc.model != "" {
				req.Header.Set(modelHeader, tc.model)
			}
			if tc.propagate {
				Inject(upstreamCtx, req.Header)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if tc.path == "/stream" && !rec.Flushed {
				t.Fatal("flush did not reach the underlying writer")
			}

			spans := exp.GetSpans()
			s := spans[len(spans)-1]
			if s.SpanKind != trace.SpanKindServer || s.Name != "POST "+tc.path {
				t.Fatalf("span %q kind %v", s.Name, s.SpanKind)
			}
			a := stubAttrs(s)
			if a["http.request.method"].AsString() != "POST" || a["url.path"].AsString() != tc.path {
				t.Fatalf("attributes %v", a)
			}
			if got := a["http.response.status_code"].AsInt64(); got != tc.wantStatus {
				t.Fatalf("status %d, want %d", got, tc.wantStatus)
			}
			model, hasModel := a[AttrModel]
			if hasModel != (tc.model != "") || (hasModel && model.AsString() != tc.model) {
				t.Fatalf("model attribute %v present %v", model, hasModel)
			}
			if (s.Status.Code == codes.Error) != tc.wantError {
				t.Fatalf("span status %v", s.Status)
			}
			if tc.propagate && (s.Parent.TraceID() != upstream.SpanContext().TraceID() || s.Parent.SpanID() != upstream.SpanContext().SpanID()) {
				t.Fatal("server span did not continue the incoming trace")
			}
		})
	}
}

// A health server whose Check answers from the call context and whose Watch streams once then fails
type tracedHealth struct {
	healthpb.UnimplementedHealthServer
	seen chan trace.SpanContext
}

func (h *tracedHealth) Check(ctx context.Context, _ *healthpb.HealthCheckRequest) (*healthpb.HealthCheckResponse, error) {
	h.seen <- trace.SpanContextFromContext(ctx)
	return &healthpb.HealthCheckResponse{Status: healthpb.HealthCheckResponse_SERVING}, nil
}

func (h *tracedHealth) Watch(_ *healthpb.HealthCheckRequest, s healthpb.Health_WatchServer) error {
	h.seen <- trace.SpanContextFromContext(s.Context())
	if err := s.Send(&healthpb.HealthCheckResponse{Status: healthpb.HealthCheckResponse_SERVING}); err != nil {
		return err
	}
	return status.Error(grpccodes.Unavailable, "stream over")
}

// Unary and streaming gRPC calls get a server span continuing the caller's trace, ended with the call's status
func TestGRPCServerInterceptors(t *testing.T) {
	exp := setup(t, Config{ServiceName: "nebu-test", SampleRatio: 1})
	lis := bufconn.Listen(1 << 20)
	hs := &tracedHealth{seen: make(chan trace.SpanContext, 2)}
	srv := grpc.NewServer(grpc.ChainUnaryInterceptor(UnaryServerInterceptor()), grpc.ChainStreamInterceptor(StreamServerInterceptor()))
	healthpb.RegisterHealthServer(srv, hs)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	client := healthpb.NewHealthClient(conn)

	callerCtx, caller := StartRequestSpan(context.Background(), "caller")
	h := http.Header{}
	Inject(callerCtx, h)
	md := metadata.New(map[string]string{"traceparent": h.Get("traceparent")})
	caller.End()

	if _, err := client.Check(metadata.NewOutgoingContext(context.Background(), md), &healthpb.HealthCheckRequest{}); err != nil {
		t.Fatal(err)
	}
	unary := <-hs.seen
	if !unary.IsValid() || unary.TraceID() != caller.SpanContext().TraceID() {
		t.Fatalf("unary handler saw %+v, want trace %s", unary, caller.SpanContext().TraceID())
	}
	stream, err := client.Watch(metadata.NewOutgoingContext(context.Background(), md), &healthpb.HealthCheckRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Recv(); err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Recv(); status.Code(err) != grpccodes.Unavailable {
		t.Fatalf("stream ended with %v", err)
	}
	streamed := <-hs.seen
	if !streamed.IsValid() || streamed.TraceID() != caller.SpanContext().TraceID() {
		t.Fatalf("stream handler saw %+v", streamed)
	}
	var spans []tracetest.SpanStub
	deadline := time.Now().Add(5 * time.Second)
	for len(spans) < 3 {
		if time.Now().After(deadline) {
			t.Fatalf("spans %v", exp.GetSpans())
		}
		spans = exp.GetSpans()
		time.Sleep(5 * time.Millisecond)
	}
	byName := map[string]tracetest.SpanStub{}
	for _, s := range spans {
		byName[s.Name] = s
	}
	check, ok := byName["/grpc.health.v1.Health/Check"]
	if !ok || check.SpanKind != trace.SpanKindServer || check.Parent.SpanID() != caller.SpanContext().SpanID() || check.Status.Code == codes.Error {
		t.Fatalf("check span %+v", check)
	}
	if got := stubAttrs(check)["rpc.grpc.status_code"].AsInt64(); got != 0 {
		t.Fatalf("check status attribute %d", got)
	}
	watch, ok := byName["/grpc.health.v1.Health/Watch"]
	if !ok || watch.SpanKind != trace.SpanKindServer || watch.Parent.SpanID() != caller.SpanContext().SpanID() || watch.Status.Code != codes.Error {
		t.Fatalf("watch span %+v", watch)
	}
	if got := stubAttrs(watch)["rpc.grpc.status_code"].AsInt64(); got != int64(grpccodes.Unavailable) {
		t.Fatalf("watch status attribute %d", got)
	}
}

func TestOTLPExport(t *testing.T) {
	got := make(chan *coltracepb.ExportTraceServiceRequest, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/traces" {
			t.Errorf("path %s", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		var req coltracepb.ExportTraceServiceRequest
		if err := proto.Unmarshal(body, &req); err != nil {
			t.Errorf("decode: %v", err)
		}
		got <- &req
		w.Header().Set("Content-Type", "application/x-protobuf")
		_, _ = w.Write(nil)
	}))
	defer srv.Close()

	exp := tracetest.NewInMemoryExporter()
	_, shutdown, err := Setup(context.Background(), Config{
		Endpoint:    srv.Listener.Addr().String(),
		ServiceName: "nebu-test",
		Insecure:    true,
		SampleRatio: 1,
		Exporter:    exp,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, span := StartRequestSpan(context.Background(), "exported")
	span.End()
	if n := len(exp.GetSpans()); n != 1 {
		t.Fatalf("extra exporter holds %d spans alongside the collector", n)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case req := <-got:
		name := req.GetResourceSpans()[0].GetScopeSpans()[0].GetSpans()[0].GetName()
		if name != "exported" {
			t.Fatalf("collector got span %q", name)
		}
	case <-ctx.Done():
		t.Fatal("collector received nothing")
	}
}

func TestSetupRejectsBadConfig(t *testing.T) {
	for _, cfg := range []Config{
		{SampleRatio: 1},
		{ServiceName: "s", SampleRatio: 1.5},
		{ServiceName: "s", SampleRatio: -0.1},
		{ServiceName: "s", Endpoint: "http://collector:4318"},
	} {
		if _, _, err := Setup(context.Background(), cfg); err == nil {
			t.Fatalf("config %+v accepted", cfg)
		}
	}
}
