package remote_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nickheyer/nebu/pkg/datalayer"
	"github.com/nickheyer/nebu/pkg/scheduling/framework"
	"github.com/nickheyer/nebu/pkg/scheduling/internal/schedtest"
	"github.com/nickheyer/nebu/pkg/scheduling/plugins/filter"
	"github.com/nickheyer/nebu/pkg/scheduling/plugins/picker"
	"github.com/nickheyer/nebu/pkg/scheduling/plugins/profile"
	"github.com/nickheyer/nebu/pkg/scheduling/plugins/scorer"
	"github.com/nickheyer/nebu/pkg/scheduling/remote"
	"github.com/nickheyer/nebu/pkg/telemetry"
	"go.opentelemetry.io/otel/trace"
)

var ctx = context.Background()

// Endpoints the server side resolves by id
type source map[datalayer.EndpointID]datalayer.Endpoint

func (s source) Endpoint(id datalayer.EndpointID) (datalayer.Endpoint, bool) {
	ep, ok := s[id]
	return ep, ok
}

func endpoints() []datalayer.Endpoint {
	return schedtest.Eps(
		schedtest.EP("a").At("10.0.0.1", 8000).Metrics(datalayer.ServerMetrics{QueuedRequests: 9}),
		schedtest.EP("b").At("10.0.0.2", 8000).Metrics(datalayer.ServerMetrics{QueuedRequests: 0}),
		schedtest.EP("c").At("10.0.0.3", 8000).Metrics(datalayer.ServerMetrics{QueuedRequests: 3}),
	)
}

func local(t *testing.T) framework.Scheduler {
	t.Helper()
	s, err := framework.NewScheduler(profile.NewSingleProfileHandler("main"), map[string]*framework.Profile{
		"main": {
			Filters: []framework.Filter{filter.NewSubsetFilter()},
			Scorers: []framework.WeightedScorer{{Scorer: scorer.NewQueueScorer(), Weight: 1}},
			Picker:  picker.NewMaxScorePicker(2),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func server(t *testing.T, sched framework.Scheduler, eps []datalayer.Endpoint) *httptest.Server {
	t.Helper()
	src := source{}
	for _, ep := range eps {
		src[ep.ID()] = ep
	}
	srv := httptest.NewServer(remote.NewHandler(sched, src))
	t.Cleanup(srv.Close)
	return srv
}

func client(t *testing.T, url string, timeout time.Duration) *remote.Client {
	t.Helper()
	c, err := remote.NewClient(url, http.DefaultClient, timeout)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestWireRoundTrip(t *testing.T) {
	in := &framework.Request{
		ID: "r", TargetModel: "m", Adapter: "x", Prompt: "hello", PromptTokenIDs: []int{1, 2}, PromptTokens: 2,
		ExpectedOutputTokens: 7, Headers: map[string]string{"k": "v"},
		Objective:  framework.Objective{Priority: 2, TTFT: 150 * time.Millisecond, TPOT: 20 * time.Millisecond},
		FairnessID: "tenant", Subset: []datalayer.Address{{Host: "10.0.0.1", Port: 8000}, {Host: "::1", Port: 9}},
		SubsetSet: true, Streaming: true,
	}
	raw, err := json.Marshal(remote.ToWire(in))
	if err != nil {
		t.Fatal(err)
	}
	var w remote.Request
	if err := json.Unmarshal(raw, &w); err != nil {
		t.Fatal(err)
	}
	out, err := remote.FromWire(w)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(in, out) {
		t.Fatalf("round trip\n got %+v\nwant %+v", out, in)
	}
	if _, err := remote.FromWire(remote.Request{Subset: []string{"nonsense"}}); err == nil {
		t.Fatal("malformed subset accepted")
	}
}

func TestClientServerRoundTrip(t *testing.T) {
	eps := endpoints()
	srv := server(t, local(t), eps)
	c := client(t, srv.URL, time.Second)
	tests := []struct {
		name string
		req  framework.Request
		want []string
	}{
		{"scores by queue", framework.Request{ID: "r1"}, []string{"b", "c"}},
		{"subset crosses the wire", framework.Request{ID: "r2", SubsetSet: true, Subset: []datalayer.Address{{Host: "10.0.0.1", Port: 8000}}}, []string{"a"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res, err := c.Schedule(ctx, &tc.req, eps)
			if err != nil {
				t.Fatal(err)
			}
			got := res.Endpoints()
			if !slices.Equal(schedtest.IDs(got), tc.want) || res.PrimaryProfile != "main" || res.State == nil {
				t.Fatalf("result %v primary %q", schedtest.IDs(got), res.PrimaryProfile)
			}
			for _, ep := range got {
				if !slices.Contains(eps, ep) {
					t.Fatalf("endpoint %s is not the caller's candidate value", ep.ID())
				}
			}
			if s := res.ProfileResults["main"].Scores[got[0].ID()]; s != 1 {
				t.Fatalf("primary score %v, want 1", s)
			}
		})
	}
}

func TestClientFailuresAreUnavailable(t *testing.T) {
	eps := endpoints()
	answer := func(status int, body string) *httptest.Server {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
		}))
		t.Cleanup(srv.Close)
		return srv
	}
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	t.Cleanup(slow.Close)
	down := httptest.NewServer(http.NotFoundHandler())
	down.Close()

	tests := []struct {
		name    string
		url     string
		cands   []datalayer.Endpoint
		timeout time.Duration
		reason  string
	}{
		{"server down", down.URL, eps, time.Second, "connect"},
		{"deadline passes", slow.URL, eps, 50 * time.Millisecond, "deadline"},
		{"unknown candidate on the server", server(t, local(t), eps[:2]).URL, eps, time.Second, "unknown endpoint"},
		{"scheduling fails on the server", server(t, local(t), nil).URL, nil, time.Second, "no endpoint"},
		{"answer names a non candidate", answer(200, `{"primaryProfile":"main","profiles":{"main":{"endpoints":["zz"]}}}`).URL, eps, time.Second, "not a candidate"},
		{"answer lacks the primary", answer(200, `{"primaryProfile":"main","profiles":{}}`).URL, eps, time.Second, "no result"},
		{"answer picks nothing", answer(200, `{"primaryProfile":"main","profiles":{"main":{"endpoints":[]}}}`).URL, eps, time.Second, "picked no endpoint"},
		{"answer is not json", answer(200, `<html>`).URL, eps, time.Second, "decoding"},
		{"error status", answer(503, `{"error":"overloaded"}`).URL, eps, time.Second, "overloaded"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := client(t, tc.url, tc.timeout).Schedule(ctx, &framework.Request{ID: "r"}, tc.cands)
			if !errors.Is(err, framework.ErrSchedulerUnavailable) {
				t.Fatalf("err %v, want unavailable", err)
			}
			if !strings.Contains(err.Error(), tc.reason) {
				t.Fatalf("err %v lacks %q", err, tc.reason)
			}
		})
	}
}

func TestServerRejectsMalformedCalls(t *testing.T) {
	srv := server(t, local(t), endpoints())
	tests := []struct {
		name   string
		method string
		body   string
		status int
	}{
		{"wrong method", http.MethodGet, "", http.StatusMethodNotAllowed},
		{"not json", http.MethodPost, "{", http.StatusBadRequest},
		{"bad subset", http.MethodPost, `{"request":{"id":"r","subset":["nope"]},"candidates":["a"]}`, http.StatusBadRequest},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req, _ := http.NewRequest(tc.method, srv.URL+remote.SchedulePath, strings.NewReader(tc.body))
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != tc.status {
				t.Fatalf("status %d, want %d", resp.StatusCode, tc.status)
			}
		})
	}
}

func TestNewClientValidates(t *testing.T) {
	for _, tc := range []struct {
		url     string
		timeout time.Duration
	}{{"not a url", time.Second}, {"ftp://x", time.Second}, {"http://x", 0}} {
		if _, err := remote.NewClient(tc.url, nil, tc.timeout); err == nil {
			t.Fatalf("accepted %q with timeout %s", tc.url, tc.timeout)
		}
	}
}

// A scheduler answering a fixed error or a fixed result
type fixed struct {
	err   error
	res   *framework.Result
	calls atomic.Int32
}

func (f *fixed) Schedule(context.Context, *framework.Request, []datalayer.Endpoint) (*framework.Result, error) {
	f.calls.Add(1)
	return f.res, f.err
}
func (f *fixed) Profiles() map[string]*framework.Profile { return nil }

func TestFallback(t *testing.T) {
	eps := endpoints()
	down := httptest.NewServer(http.NotFoundHandler())
	down.Close()
	up := server(t, local(t), eps)
	other := errors.New("remote refused by policy")
	tests := []struct {
		name          string
		remote        framework.Scheduler
		failOpen      bool
		wantFallbacks int32
		wantLocal     int32
		wantErr       error
	}{
		{"remote down uses local", client(t, down.URL, time.Second), true, 1, 1, nil},
		{"remote down fails closed", client(t, down.URL, time.Second), false, 0, 0, framework.ErrSchedulerUnavailable},
		{"remote up skips local", client(t, up.URL, time.Second), true, 0, 0, nil},
		{"other remote errors pass through", &fixed{err: other}, true, 0, 0, other},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var fallbacks atomic.Int32
			loc := &fixed{res: schedtest.Result("local", eps[2])}
			f, err := remote.NewFallback(tc.remote, loc, func(err error) {
				if !errors.Is(err, framework.ErrSchedulerUnavailable) {
					t.Errorf("observer got %v", err)
				}
				fallbacks.Add(1)
			})
			if err != nil {
				t.Fatal(err)
			}
			res, err := f.Schedule(ctx, &framework.Request{ID: "r", FailOpen: tc.failOpen}, eps)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err %v, want %v", err, tc.wantErr)
			}
			if fallbacks.Load() != tc.wantFallbacks || loc.calls.Load() != tc.wantLocal {
				t.Fatalf("fallbacks %d local calls %d", fallbacks.Load(), loc.calls.Load())
			}
			if tc.wantErr == nil && res.FailedOpen != (tc.wantLocal > 0) {
				t.Fatalf("failed open %v with %d local calls", res.FailedOpen, tc.wantLocal)
			}
			if tc.wantLocal > 0 && res.PrimaryProfile != "local" {
				t.Fatalf("result not from local: %+v", res)
			}
		})
	}
	if _, err := remote.NewFallback(nil, &fixed{}, func(error) {}); err == nil {
		t.Fatal("fallback without a remote accepted")
	}
	if _, err := remote.NewFallback(&fixed{}, &fixed{}, nil); err == nil {
		t.Fatal("fallback without an observer accepted")
	}
}

// The schedule call carries the caller's span in traceparent, so the remote scheduler joins the request trace
func TestScheduleCallCarriesTraceContext(t *testing.T) {
	_, shutdown, err := telemetry.Setup(ctx, telemetry.Config{ServiceName: "remote-test", SampleRatio: 1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = shutdown(ctx) })
	seen := make(chan string, 1)
	inner := remote.NewHandler(local(t), func() source {
		src := source{}
		for _, ep := range endpoints() {
			src[ep.ID()] = ep
		}
		return src
	}())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Header.Get("traceparent")
		inner.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	c := client(t, srv.URL, time.Second)
	spanCtx, span := telemetry.StartRequestSpan(ctx, "schedule")
	defer span.End()
	if _, err := c.Schedule(spanCtx, &framework.Request{ID: "traced"}, endpoints()); err != nil {
		t.Fatal(err)
	}
	want := span.SpanContext()
	got := trace.SpanContextFromContext(telemetry.Extract(ctx, http.Header{"Traceparent": {<-seen}}))
	if !got.IsValid() || got.TraceID() != want.TraceID() || got.SpanID() != want.SpanID() {
		t.Fatalf("remote saw %+v, want trace %s span %s", got, want.TraceID(), want.SpanID())
	}
}
