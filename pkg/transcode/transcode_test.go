package transcode

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	vllmpb "github.com/nickheyer/nebu/pkg/transcode/gen/vllm"
)

func TestHTTPStatus(t *testing.T) {
	cases := map[codes.Code]int{
		codes.OK:                 200,
		codes.Canceled:           499,
		codes.Unknown:            500,
		codes.InvalidArgument:    400,
		codes.DeadlineExceeded:   504,
		codes.NotFound:           404,
		codes.AlreadyExists:      409,
		codes.PermissionDenied:   403,
		codes.ResourceExhausted:  429,
		codes.FailedPrecondition: 400,
		codes.Aborted:            409,
		codes.OutOfRange:         400,
		codes.Unimplemented:      501,
		codes.Internal:           500,
		codes.Unavailable:        503,
		codes.DataLoss:           500,
		codes.Unauthenticated:    401,
	}
	for code, want := range cases {
		if got := HTTPStatus(code); got != want {
			t.Errorf("HTTPStatus(%s) = %d, want %d", code, got, want)
		}
	}
}

func TestFromGRPC(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
		code int
	}{
		{"status", status.Error(codes.ResourceExhausted, "queue full"), `{"error":{"message":"queue full","type":"RateLimitError","param":null,"code":429}}`, 429},
		{"canceled", context.Canceled, `{"error":{"message":"context canceled","type":"ClientClosedRequestError","param":null,"code":499}}`, 499},
		{"deadline", context.DeadlineExceeded, `{"error":{"message":"context deadline exceeded","type":"GatewayTimeoutError","param":null,"code":504}}`, 504},
		{"passthrough", BadRequest("bad %s", "field"), `{"error":{"message":"bad field","type":"BadRequestError","param":null,"code":400}}`, 400},
		{"unmapped", NewError(418, "teapot"), `{"error":{"message":"teapot","type":"BadRequestError","param":null,"code":418}}`, 418},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := FromGRPC(c.err)
			if got.Status != c.code || string(got.Body) != c.want {
				t.Fatalf("got %d %s, want %d %s", got.Status, got.Body, c.code, c.want)
			}
		})
	}
}

func TestErrorWrite(t *testing.T) {
	rec := httptest.NewRecorder()
	if err := NewError(503, "down").Write(rec); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 503 || rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("status %d content type %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	if want := `{"error":{"message":"down","type":"ServiceUnavailableError","param":null,"code":503}}`; rec.Body.String() != want {
		t.Fatalf("body %s", rec.Body)
	}
}

// Reads every event and comment from an SSE body
func readAll(t *testing.T, r io.Reader) []Event {
	t.Helper()
	src := NewEventSource(r)
	var out []Event
	for {
		ev, err := src.Next()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, ev)
	}
}

func TestSSEWriterData(t *testing.T) {
	rec := httptest.NewRecorder()
	w, err := NewSSEWriter(rec)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Data([]byte(`{"a":1}`)); err != nil {
		t.Fatal(err)
	}
	if err := w.Data([]byte("line one\r\nline two\rline three")); err != nil {
		t.Fatal(err)
	}
	w.Close()
	if err := w.Data([]byte("late")); err == nil {
		t.Fatal("write after close succeeded")
	}
	if want := "data: {\"a\":1}\n\ndata: line one\ndata: line two\ndata: line three\n\n"; rec.Body.String() != want {
		t.Fatalf("body %q", rec.Body.String())
	}
	if !rec.Flushed {
		t.Fatal("events were not flushed")
	}
	h := rec.Header()
	if h.Get("Content-Type") != "text/event-stream" || h.Get("Cache-Control") != "no-cache" || h.Get("X-Accel-Buffering") != "no" {
		t.Fatalf("headers %v", h)
	}
}

func TestSSEWriterKeepalive(t *testing.T) {
	rec := httptest.NewRecorder()
	w, err := newSSEWriter(rec, 20*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(110 * time.Millisecond)
	if err := w.Data([]byte("x")); err != nil {
		t.Fatal(err)
	}
	w.Close()
	time.Sleep(50 * time.Millisecond)
	events := readAll(t, strings.NewReader(rec.Body.String()))
	if len(events) < 3 {
		t.Fatalf("want keepalives before data, got %+v", events)
	}
	last := events[len(events)-1]
	if last.Data != "x" {
		t.Fatalf("last event %+v", last)
	}
	for _, ev := range events[:len(events)-1] {
		if ev.Comment != "keepalive" {
			t.Fatalf("event %+v is not a keepalive", ev)
		}
	}
}

func TestSSEWriterKeepaliveWaitsForSilence(t *testing.T) {
	rec := httptest.NewRecorder()
	w, err := newSSEWriter(rec, 60*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	for range 6 {
		time.Sleep(20 * time.Millisecond)
		if err := w.Data([]byte("tick")); err != nil {
			t.Fatal(err)
		}
	}
	w.Close()
	for _, ev := range readAll(t, strings.NewReader(rec.Body.String())) {
		if ev.Comment != "" {
			t.Fatalf("keepalive sent while events flowed: %+v", ev)
		}
	}
}

func TestEventSource(t *testing.T) {
	in := ": hello\r\nevent: update\r\nid: 7\r\ndata: a\r\ndata: b\r\n\r\ndata:c\n\n\n\nretry: 10\nunknown: x\ndata: d\n\ndata: partial"
	got := readAll(t, strings.NewReader(in))
	want := []Event{
		{Comment: "hello"},
		{Type: "update", ID: "7", Data: "a\nb"},
		{ID: "7", Data: "c"},
		{ID: "7", Data: "d"},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
}

func TestReplyWriteUnary(t *testing.T) {
	rec := httptest.NewRecorder()
	if err := (&Reply{Body: []byte(`{"ok":true}`)}).Write(rec); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "application/json" || rec.Body.String() != `{"ok":true}` {
		t.Fatalf("got %d %q %s", rec.Code, rec.Header().Get("Content-Type"), rec.Body)
	}
}

func TestReplyWriteStreamFailure(t *testing.T) {
	failure := NewError(503, "gone")
	reply := &Reply{Events: func(yield func([]byte, error) bool) {
		_ = yield([]byte(`{"n":1}`), nil) && yield(failure.Body, failure) && yield(Done, nil)
	}}
	rec := httptest.NewRecorder()
	err := reply.Write(rec)
	var te *Error
	if !errors.As(err, &te) || te.Status != 503 {
		t.Fatalf("Write returned %v", err)
	}
	var data []string
	for _, ev := range readAll(t, rec.Body) {
		data = append(data, ev.Data)
	}
	want := []string{`{"n":1}`, string(failure.Body), "[DONE]"}
	if !slices.Equal(data, want) {
		t.Fatalf("got %q", data)
	}
}

func TestReplyWriteStopsOnClientFailure(t *testing.T) {
	served := 0
	reply := &Reply{Events: func(yield func([]byte, error) bool) {
		for {
			served++
			if !yield([]byte("x"), nil) {
				return
			}
		}
	}}
	if err := reply.Write(&brokenWriter{header: http.Header{}}); err == nil {
		t.Fatal("Write succeeded on a broken connection")
	}
	if served != 1 {
		t.Fatalf("producer ran %d times after the client failed", served)
	}
}

// brokenWriter accepts headers and fails every body write like a closed connection
type brokenWriter struct{ header http.Header }

func (b *brokenWriter) Header() http.Header         { return b.header }
func (b *brokenWriter) WriteHeader(int)             {}
func (b *brokenWriter) Write([]byte) (int, error)   { return 0, errors.New("broken pipe") }
func (b *brokenWriter) Flush()                      {}
func (b *brokenWriter) FlushError() error           { return nil }
func (b *brokenWriter) Unwrap() http.ResponseWriter { return nil }

func TestCallContext(t *testing.T) {
	ctx, cancel := CallContext(context.Background(), 0)
	defer cancel()
	if _, ok := ctx.Deadline(); ok {
		t.Fatal("zero timeout set a deadline")
	}
	ctx2, cancel2 := CallContext(context.Background(), time.Minute)
	defer cancel2()
	if d, ok := ctx2.Deadline(); !ok || time.Until(d) > time.Minute {
		t.Fatalf("deadline %v %v", d, ok)
	}
	parent, cancelParent := context.WithTimeout(context.Background(), time.Second)
	defer cancelParent()
	ctx3, cancel3 := CallContext(parent, time.Hour)
	defer cancel3()
	pd, _ := parent.Deadline()
	if d, _ := ctx3.Deadline(); !d.Equal(pd) {
		t.Fatalf("earlier parent deadline %v replaced by %v", pd, d)
	}
}

func TestDialRefusesNonGRPC(t *testing.T) {
	for _, p := range []AppProtocol{"http", "https", "kubernetes.io/ws", ""} {
		if _, err := Dial("127.0.0.1:1", p); err == nil {
			t.Errorf("Dial accepted %q", p)
		}
	}
}

// echoInference answers Generate with the request id it received
type echoInference struct {
	vllmpb.UnimplementedInferenceServer
}

func (echoInference) Generate(_ context.Context, req *vllmpb.GenerateRequest) (*vllmpb.GenerateResponse, error) {
	return &vllmpb.GenerateResponse{Outputs: &vllmpb.SequenceOutput{Text: req.GetRequestId()}}, nil
}

func TestDialH2C(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer()
	vllmpb.RegisterInferenceServer(srv, echoInference{})
	go func() { _ = srv.Serve(lis) }()
	defer srv.Stop()
	for _, p := range []AppProtocol{H2C, GRPC} {
		conn, err := Dial(lis.Addr().String(), p)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := CallContext(context.Background(), 5*time.Second)
		resp, err := vllmpb.NewInferenceClient(conn).Generate(ctx, &vllmpb.GenerateRequest{RequestId: string(p)})
		cancel()
		if err != nil || resp.GetOutputs().GetText() != string(p) {
			t.Fatalf("%s call got %v %v", p, resp, err)
		}
		_ = conn.Close()
	}
}
