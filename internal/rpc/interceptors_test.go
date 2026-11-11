package rpc

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"buf.build/gen/go/bufbuild/protovalidate/protocolbuffers/go/buf/validate"
	"connectrpc.com/connect"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/proto/nebu/v1/nebuv1connect"
)

// A request breaking its schema rules is refused as InvalidArgument with the violations attached, before any handler runs
func TestRequestsBreakingTheirRulesAreRefused(t *testing.T) {
	deps, _ := surfaceDeps(t)
	srv := httptest.NewServer(handlerOf(t, deps))
	t.Cleanup(srv.Close)
	client := nebuv1connect.NewGatewayServiceClient(srv.Client(), srv.URL)
	put := func(req *v1.PutRouteRequest, bearer string) error {
		r := connect.NewRequest(req)
		r.Header().Set("Authorization", "Bearer "+bearer)
		_, err := client.PutRoute(context.Background(), r)
		return err
	}
	bad := &v1.PutRouteRequest{Name: "", Spec: &v1.RouteSpec{Rules: []*v1.RewriteRule{{Targets: []*v1.RewriteTarget{{PoolId: "p", Weight: 0}}}}, Policy: &v1.Policy{RequestsPerSecond: -1}}}
	err := put(bad, "daemon-token")
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("a violating request answered %v", err)
	}
	var ce *connect.Error
	if !errors.As(err, &ce) || len(ce.Details()) != 1 {
		t.Fatalf("the refusal carries the violations as one detail: %v", err)
	}
	detail, derr := ce.Details()[0].Value()
	violations, ok := detail.(*validate.Violations)
	if derr != nil || !ok {
		t.Fatalf("detail %T %v", detail, derr)
	}
	got := strings.Join(violated(violations), ",")
	for _, want := range []string{"name", "spec.rules.targets.weight", "spec.policy.requests_per_second"} {
		if !strings.Contains(got, want) {
			t.Errorf("violations %s do not name %s", got, want)
		}
	}
	// The guard answers before validation, so a caller without credentials learns nothing of the schema
	if err := put(bad, "wrong"); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("an unauthenticated violating request answered %v", err)
	}
	// Policy values past their bounds and an infinite rate are refused field by field
	unbounded := &v1.PutRouteRequest{Name: "chat", Spec: &v1.RouteSpec{Rules: []*v1.RewriteRule{{Targets: []*v1.RewriteTarget{{PoolId: "p", Weight: 1}}}}, Policy: &v1.Policy{RequestsPerSecond: math.Inf(1), MaxInFlight: 1000001, Burst: 1000001, RequestTimeoutMs: 86400001, UpstreamTimeoutMs: 86400001}}}
	err = put(unbounded, "daemon-token")
	if !errors.As(err, &ce) || len(ce.Details()) != 1 {
		t.Fatalf("an unbounded policy answered %v", err)
	}
	detail, _ = ce.Details()[0].Value()
	got = strings.Join(violated(detail.(*validate.Violations)), ",")
	for _, want := range []string{"spec.policy.requests_per_second", "spec.policy.max_in_flight", "spec.policy.burst", "spec.policy.request_timeout_ms", "spec.policy.upstream_timeout_ms"} {
		if !strings.Contains(got, want) {
			t.Errorf("violations %s do not name %s", got, want)
		}
	}
	ctx := context.Background()
	bearer := func(req connect.AnyRequest) { req.Header().Set("Authorization", "Bearer daemon-token") }
	auth := nebuv1connect.NewAuthServiceClient(srv.Client(), srv.URL)
	bots := nebuv1connect.NewBotServiceClient(srv.Client(), srv.URL)
	chat := nebuv1connect.NewChatServiceClient(srv.Client(), srv.URL)
	estimate := nebuv1connect.NewEstimateServiceClient(srv.Client(), srv.URL)
	eventsClient := nebuv1connect.NewEventServiceClient(srv.Client(), srv.URL)
	hostClient := nebuv1connect.NewHostServiceClient(srv.Client(), srv.URL)
	inst := nebuv1connect.NewInstanceServiceClient(srv.Client(), srv.URL)
	meshClient := nebuv1connect.NewMeshServiceClient(srv.Client(), srv.URL)
	builds := nebuv1connect.NewBuildServiceClient(srv.Client(), srv.URL)
	rt := nebuv1connect.NewRuntimeServiceClient(srv.Client(), srv.URL)
	settings := nebuv1connect.NewSettingsServiceClient(srv.Client(), srv.URL)
	slotsClient := nebuv1connect.NewSlotServiceClient(srv.Client(), srv.URL)
	sources := nebuv1connect.NewSourceServiceClient(srv.Client(), srv.URL)
	store := nebuv1connect.NewStoreServiceClient(srv.Client(), srv.URL)
	tasksClient := nebuv1connect.NewTaskServiceClient(srv.Client(), srv.URL)
	tools := nebuv1connect.NewToolServiceClient(srv.Client(), srv.URL)
	imports := nebuv1connect.NewPoolImportServiceClient(srv.Client(), srv.URL)
	// One violating request per proto file with a service, each refused before its handler runs
	perFile := []struct {
		file string
		send func() error
	}{
		{"auth.proto", func() error {
			r := connect.NewRequest(&v1.DeleteUserRequest{})
			bearer(r)
			_, err := auth.DeleteUser(ctx, r)
			return err
		}},
		{"bot.proto", func() error {
			r := connect.NewRequest(&v1.GetBotRequest{})
			bearer(r)
			_, err := bots.GetBot(ctx, r)
			return err
		}},
		{"chat.proto", func() error {
			r := connect.NewRequest(&v1.GetConversationRequest{})
			bearer(r)
			_, err := chat.GetConversation(ctx, r)
			return err
		}},
		{"estimate.proto", func() error {
			r := connect.NewRequest(&v1.PartsRequest{})
			bearer(r)
			_, err := estimate.Parts(ctx, r)
			return err
		}},
		{"event.proto", func() error {
			r := connect.NewRequest(&v1.WatchEventsRequest{Kinds: []v1.EventKind{v1.EventKind(999)}})
			bearer(r)
			stream, err := eventsClient.WatchEvents(ctx, r)
			if err != nil {
				return err
			}
			defer stream.Close()
			for stream.Receive() {
			}
			return stream.Err()
		}},
		{"gateway.proto", func() error {
			r := connect.NewRequest(&v1.GetTraceRequest{})
			bearer(r)
			_, err := client.GetTrace(ctx, r)
			return err
		}},
		{"host.proto", func() error {
			r := connect.NewRequest(&v1.ListDirectoryRequest{Path: strings.Repeat("a", 4097)})
			bearer(r)
			_, err := hostClient.ListDirectory(ctx, r)
			return err
		}},
		{"host.proto start place", func() error {
			r := connect.NewRequest(&v1.ListDirectoryRequest{Start: v1.DirPlaceKind_DIR_PLACE_KIND_VOLUME})
			bearer(r)
			_, err := hostClient.ListDirectory(ctx, r)
			return err
		}},
		{"instance.proto", func() error {
			r := connect.NewRequest(&v1.GetInstanceRequest{})
			bearer(r)
			_, err := inst.GetInstance(ctx, r)
			return err
		}},
		{"mesh.proto", func() error {
			r := connect.NewRequest(&v1.GetFormationRequest{})
			bearer(r)
			_, err := meshClient.GetFormation(ctx, r)
			return err
		}},
		{"recipe.proto", func() error {
			r := connect.NewRequest(&v1.GetBuildRequest{})
			bearer(r)
			_, err := builds.GetBuild(ctx, r)
			return err
		}},
		{"runtime.proto", func() error {
			r := connect.NewRequest(&v1.GetRuntimeRequest{})
			bearer(r)
			_, err := rt.GetRuntime(ctx, r)
			return err
		}},
		{"settings.proto", func() error {
			r := connect.NewRequest(&v1.UpdateSettingsRequest{})
			bearer(r)
			_, err := settings.UpdateSettings(ctx, r)
			return err
		}},
		{"slot.proto", func() error {
			r := connect.NewRequest(&v1.SwapRequest{SlotId: "s", Run: &v1.RunRequest{}, CanarySeconds: 86401})
			bearer(r)
			_, err := slotsClient.Swap(ctx, r)
			return err
		}},
		{"source.proto", func() error {
			r := connect.NewRequest(&v1.ResolveRequest{})
			bearer(r)
			_, err := sources.Resolve(ctx, r)
			return err
		}},
		{"store.proto", func() error {
			r := connect.NewRequest(&v1.GetModelRequest{})
			bearer(r)
			_, err := store.GetModel(ctx, r)
			return err
		}},
		{"task.proto", func() error {
			r := connect.NewRequest(&v1.GetTaskRequest{})
			bearer(r)
			_, err := tasksClient.GetTask(ctx, r)
			return err
		}},
		{"tools.proto", func() error {
			r := connect.NewRequest(&v1.CallToolRequest{})
			bearer(r)
			_, err := tools.CallTool(ctx, r)
			return err
		}},
		{"v1/poolimport.proto", func() error {
			r := connect.NewRequest(&v1.GetPoolImportRequest{})
			bearer(r)
			_, err := imports.GetPoolImport(ctx, r)
			return err
		}},
	}
	for _, tt := range perFile {
		t.Run(tt.file, func(t *testing.T) {
			err := tt.send()
			var ce *connect.Error
			if connect.CodeOf(err) != connect.CodeInvalidArgument || !errors.As(err, &ce) || len(ce.Details()) != 1 {
				t.Fatalf("a violating request answered %v", err)
			}
		})
	}
}

// The field paths a violations detail names, dotted
func violated(violations *validate.Violations) []string {
	var fields []string
	for _, v := range violations.GetViolations() {
		var path []string
		for _, e := range v.GetField().GetElements() {
			path = append(path, e.GetFieldName())
		}
		fields = append(fields, strings.Join(path, "."))
	}
	return fields
}

// A stream handler connection that hands out one message and records nothing else
type fakeConn struct {
	connect.StreamingHandlerConn
	procedure string
	fill      func(any)
}

func (c fakeConn) Spec() connect.Spec          { return connect.Spec{Procedure: c.procedure} }
func (c fakeConn) Peer() connect.Peer          { return connect.Peer{Addr: "192.0.2.1:9"} }
func (c fakeConn) Receive(msg any) error       { c.fill(msg); return nil }
func (c fakeConn) RequestHeader() http.Header  { return http.Header{} }
func (c fakeConn) ResponseHeader() http.Header { return http.Header{} }

// Every message a stream receives is checked against its rules before the handler reads it
func TestStreamMessagesAreValidated(t *testing.T) {
	v, err := newValidating()
	if err != nil {
		t.Fatal(err)
	}
	conn := fakeConn{procedure: "/test/Stream", fill: func(msg any) { msg.(*v1.PutRouteRequest).Name = "" }}
	handler := v.WrapStreamingHandler(func(ctx context.Context, c connect.StreamingHandlerConn) error {
		return c.Receive(&v1.PutRouteRequest{})
	})
	if err := handler(context.Background(), conn); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("a violating stream message answered %v", err)
	}
	conn.fill = func(msg any) { msg.(*v1.PutRouteRequest).Name = "chat" }
	if err := handler(context.Background(), conn); err != nil {
		t.Fatalf("a valid stream message was refused %v", err)
	}
}

// Unary calls and streams are logged with their duration and outcome, failures as warnings with their code
func TestLoggingCoversStreams(t *testing.T) {
	var buf bytes.Buffer
	l := logging{log: slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))}
	stream := l.WrapStreamingHandler(func(ctx context.Context, c connect.StreamingHandlerConn) error {
		return connect.NewError(connect.CodeResourceExhausted, errors.New("fell behind"))
	})
	if err := stream(context.Background(), fakeConn{procedure: "/nebu.v1.EventService/WatchEvents"}); err == nil {
		t.Fatal("the stream's error is passed on")
	}
	out := buf.String()
	for _, want := range []string{"rpc stream opened", "procedure=/nebu.v1.EventService/WatchEvents", "level=WARN msg=\"rpc stream closed\"", "code=resource_exhausted", "ms="} {
		if !strings.Contains(out, want) {
			t.Fatalf("stream log lacks %q:\n%s", want, out)
		}
	}
	buf.Reset()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	hangup := l.WrapStreamingHandler(func(ctx context.Context, c connect.StreamingHandlerConn) error { return ctx.Err() })
	if err := hangup(ctx, fakeConn{procedure: "/nebu.v1.TaskService/WatchTask"}); err != nil {
		t.Fatalf("a client hanging up ends the stream cleanly, got %v", err)
	}
	if out := buf.String(); !strings.Contains(out, "level=DEBUG msg=\"rpc stream closed\"") || !strings.Contains(out, "code=ok") {
		t.Fatalf("a hung up stream closes at debug:\n%s", out)
	}
}
