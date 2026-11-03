package main

import (
	"bytes"
	"context"
	"encoding/pem"
	"errors"
	"flag"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/nickheyer/nebu/bench"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/proto/nebu/v1/nebuv1connect"
	"github.com/nickheyer/nebu/pkg/stub"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Runs the command line, returning stdout, stderr and the error
func runCmd(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	var out, errOut bytes.Buffer
	err := run(context.Background(), args, &out, &errOut)
	return out.String(), errOut.String(), err
}

func TestRunDispatch(t *testing.T) {
	cases := []struct {
		args []string
		want error
		out  string
	}{
		{nil, errUsage, ""},
		{[]string{"bogus"}, errUsage, ""},
		{[]string{"help"}, nil, "commands:"},
		{[]string{"synthesize", "-h"}, flag.ErrHelp, ""},
	}
	for _, tc := range cases {
		out, _, err := runCmd(t, tc.args...)
		if !errors.Is(err, tc.want) || !strings.Contains(out, tc.out) {
			t.Fatalf("%v gave %v and %q", tc.args, err, out)
		}
	}
}

func TestSynthesizeCommand(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a.jsonl"), filepath.Join(dir, "b.jsonl")
	args := []string{"synthesize", "--seed", "7", "--requests", "50", "--rate", "25", "--prompt-tokens", "uniform:10:20", "--output-tokens", "fixed:4",
		"--objectives", "0, 5", "--adapters", "lora-a", "--adapter-share", "0.5", "--max-prompt-tokens", "15", "--prefix-tokens", "6"}
	for _, out := range []string{a, b} {
		if _, _, err := runCmd(t, append(args, "--out", out)...); err != nil {
			t.Fatal(err)
		}
	}
	da, _ := os.ReadFile(a)
	db, _ := os.ReadFile(b)
	if !bytes.Equal(da, db) {
		t.Fatal("one seed wrote two traces")
	}
	reqs, err := bench.ReadFile(a)
	if err != nil {
		t.Fatal(err)
	}
	if len(reqs) != 50 {
		t.Fatalf("%d requests", len(reqs))
	}
	for i, r := range reqs {
		if r.MaxTokens != 4 || r.PromptTokens > 15 || (r.Objective != "0" && r.Objective != "5") || (r.Adapter != "" && r.Adapter != "lora-a") {
			t.Fatalf("request %d %+v", i, r)
		}
	}
	stdout, _, err := runCmd(t, "synthesize", "--requests", "3")
	if err != nil || strings.Count(stdout, "\n") != 3 {
		t.Fatalf("stdout %q, %v", stdout, err)
	}
	for _, bad := range [][]string{
		{"--prompt-tokens", "zipf:1"},
		{"--output-tokens", "fixed"},
		{"--max-output-tokens", "-1"},
		{"--rate", "0"},
		{"--max-prompt-tokens", "100"},
		{"extra"},
	} {
		if _, _, err := runCmd(t, append([]string{"synthesize"}, bad...)...); err == nil {
			t.Fatalf("%v accepted", bad)
		}
	}
}

// A trace file of n streamed requests a millisecond apart
func traceFile(t *testing.T, n int) string {
	t.Helper()
	reqs := make([]bench.Request, n)
	for i := range reqs {
		reqs[i] = bench.Request{OffsetMS: float64(i), PromptTokens: 8, MaxTokens: 3, Stream: true}
	}
	path := filepath.Join(t.TempDir(), "t.jsonl")
	if err := bench.WriteFile(path, reqs); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReplayCommand(t *testing.T) {
	st, err := stub.New(stub.Config{Mode: stub.ModeRatio, Speedup: 20, BaseLatency: 20 * time.Millisecond, DecodePerToken: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var mu sync.Mutex
	var auth []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		auth = append(auth, r.Header.Get("Authorization"))
		mu.Unlock()
		st.ServeHTTP(w, r)
	}))
	defer srv.Close()
	trace := traceFile(t, 5)
	tokenFile := filepath.Join(t.TempDir(), "api.token")
	os.WriteFile(tokenFile, []byte("tok\n"), 0o600)
	out := filepath.Join(t.TempDir(), "r.json")
	stdout, _, err := runCmd(t, "replay", "--gateway-url", srv.URL, "--token-file", tokenFile, "--trace", trace, "--name", "baseline", "--speed", "4", "--out", out)
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if len(auth) != 5 || slices.ContainsFunc(auth, func(a string) bool { return a != "Bearer tok" }) {
		t.Fatalf("authorizations %q", auth)
	}
	mu.Unlock()
	if !strings.Contains(stdout, "## baseline") || !strings.Contains(stdout, "## comparison") || !strings.Contains(stdout, "| baseline | 5 | 5 | 0 | 0 | 0 |") {
		t.Fatalf("stdout\n%s", stdout)
	}
	f, err := os.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	reps, err := bench.ReadReports(f)
	if err != nil || len(reps) != 1 || reps[0].Name != "baseline" || reps[0].Succeeded != 5 || reps[0].Trace != trace || reps[0].Speed != 4 {
		t.Fatalf("reports %+v, %v", reps, err)
	}
	cmpOut, _, err := runCmd(t, "compare", out, out)
	if err != nil || strings.Count(cmpOut, "| baseline |") != 2 {
		t.Fatalf("compare\n%s\n%v", cmpOut, err)
	}
	jsonOut, _, err := runCmd(t, "compare", "--json", out)
	if err != nil || !strings.Contains(jsonOut, `"runs"`) {
		t.Fatalf("compare json\n%s\n%v", jsonOut, err)
	}
}

func TestReplayCommandRefuses(t *testing.T) {
	trace := traceFile(t, 1)
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"--gateway-url", "http://h", "--trace", trace, "--scheduler-config", "default"}, "needs the pool"},
		{[]string{"--gateway-url", "http://h", "--trace", trace, "--scheduler-config", "default", "--pool", "p", "--name", "x"}, "named after it"},
		{[]string{"--gateway-url", "http://h", "--trace", trace, "--pool", "p"}, "only read with scheduler-config"},
		{[]string{"--gateway-url", "http://h"}, "needs a trace"},
		{[]string{"--trace", trace}, "needs the gateway URL"},
		{[]string{"--gateway-url", "h:1", "--trace", trace}, "not an http or https URL"},
		{[]string{"--gateway-url", "http://h", "--trace", trace, "--token", "a", "--token-file", "b"}, "not both"},
		{[]string{"--gateway-url", "http://h", "--trace", trace, "--tls-ca", trace}, "holds no PEM certificate"},
	}
	for _, tc := range cases {
		if _, _, err := runCmd(t, append([]string{"replay"}, tc.args...)...); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%v gave %v, want %q", tc.args, err, tc.want)
		}
	}
	for _, args := range [][]string{
		{"local", "--gateway-url", "http://h", "--trace", trace, "--stub-speedup", "3"},
		{"compare"},
		{"compare", trace},
		{"record"},
		{"record", "--api-url", "http://h", "--since", "yesterday"},
	} {
		if _, _, err := runCmd(t, args...); err == nil {
			t.Fatalf("%v accepted", args)
		}
	}
}

func TestSinceTime(t *testing.T) {
	now := time.Date(2026, 5, 6, 7, 8, 9, 0, time.UTC)
	cases := []struct {
		in   string
		want time.Time
		bad  bool
	}{
		{in: "2026-05-06T06:00:00Z", want: time.Date(2026, 5, 6, 6, 0, 0, 0, time.UTC)},
		{in: "90m", want: now.Add(-90 * time.Minute)},
		{in: "-1h", bad: true},
		{in: "soon", bad: true},
	}
	for _, tc := range cases {
		got, err := sinceTime(tc.in, now)
		if tc.bad != (err != nil) || !tc.bad && !got.Equal(tc.want) {
			t.Fatalf("%q gave %v, %v", tc.in, got, err)
		}
	}
}

func TestTrustingCAFile(t *testing.T) {
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	srv.StartTLS()
	defer srv.Close()
	ca := filepath.Join(t.TempDir(), "tls.crt")
	os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600)
	byName := strings.Replace(srv.URL, "127.0.0.1", "localhost", 1)
	cases := []struct {
		name, url, ca string
		ok            bool
	}{
		{"chained by ip", srv.URL, ca, true},
		{"leaf by a name it lacks", byName, ca, true},
		{"system roots", srv.URL, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := conn{tlsCA: tc.ca}
			hc, err := c.httpClient(tc.url, false, 4)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := hc.Get(tc.url)
			if err == nil {
				resp.Body.Close()
			}
			if (err == nil) != tc.ok {
				t.Fatalf("err %v", err)
			}
		})
	}
	c := conn{tlsCA: filepath.Join(t.TempDir(), "missing")}
	if _, err := c.httpClient(srv.URL, false, 0); err == nil {
		t.Fatal("a missing CA file loaded")
	}
}

// Serves pools, routes and traces for the record command, keeping the authorization each call carried
type recordAPI struct {
	nebuv1connect.UnimplementedGatewayServiceHandler
	mu   sync.Mutex
	auth []string
}

// Keeps the authorization a call carried
func (a *recordAPI) saw(h http.Header) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.auth = append(a.auth, h.Get("Authorization"))
}

func (a *recordAPI) ListPools(_ context.Context, req *connect.Request[v1.ListPoolsRequest]) (*connect.Response[v1.ListPoolsResponse], error) {
	a.saw(req.Header())
	return connect.NewResponse(&v1.ListPoolsResponse{Pools: []*v1.Pool{{Id: "p", Spec: &v1.PoolSpec{Model: "m"}}}}), nil
}

func (a *recordAPI) ListRoutes(_ context.Context, req *connect.Request[v1.ListRoutesRequest]) (*connect.Response[v1.ListRoutesResponse], error) {
	a.saw(req.Header())
	return connect.NewResponse(&v1.ListRoutesResponse{}), nil
}

func (a *recordAPI) ListTraces(_ context.Context, req *connect.Request[v1.ListTracesRequest]) (*connect.Response[v1.ListTracesResponse], error) {
	a.saw(req.Header())
	return connect.NewResponse(&v1.ListTracesResponse{Traces: []*v1.Trace{
		{Id: "b", Kind: v1.TraceKind_TRACE_KIND_COUNT, StartedAt: timestamppb.Now()},
		{Id: "a", Kind: v1.TraceKind_TRACE_KIND_CHAT, StartedAt: timestamppb.Now(), PoolId: "p"},
	}}), nil
}

func (a *recordAPI) GetTrace(_ context.Context, req *connect.Request[v1.GetTraceRequest]) (*connect.Response[v1.GetTraceResponse], error) {
	a.saw(req.Header())
	return connect.NewResponse(&v1.GetTraceResponse{Trace: &v1.Trace{Id: "a", Kind: v1.TraceKind_TRACE_KIND_CHAT, StartedAt: timestamppb.Now(), PoolId: "p", Route: "m",
		Path: "/v1/chat/completions", Request: `{"model":"m","messages":[{"role":"user","content":"hi"}],"max_tokens":5}`}}), nil
}

func TestRecordCommand(t *testing.T) {
	api := &recordAPI{}
	mux := http.NewServeMux()
	mux.Handle(nebuv1connect.NewGatewayServiceHandler(api))
	srv := httptest.NewUnstartedServer(mux)
	srv.EnableHTTP2 = true
	srv.StartTLS()
	defer srv.Close()
	ca := filepath.Join(t.TempDir(), "tls.crt")
	os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600)
	out := filepath.Join(t.TempDir(), "rec.jsonl")
	_, stderr, err := runCmd(t, "record", "--api-url", srv.URL, "--token", "tok", "--tls-ca", ca, "--since", "1h", "--out", out)
	if err != nil {
		t.Fatal(err)
	}
	if stderr != "recorded 1 of 2 listed traces, 1 count excluded\n" {
		t.Fatalf("stderr %q", stderr)
	}
	reqs, err := bench.ReadFile(out)
	if err != nil || len(reqs) != 1 || reqs[0].Model != "m" || reqs[0].MaxTokens != 5 || reqs[0].Messages[0].Content != "hi" {
		t.Fatalf("recorded %+v, %v", reqs, err)
	}
	api.mu.Lock()
	defer api.mu.Unlock()
	if len(api.auth) != 4 || slices.ContainsFunc(api.auth, func(a string) bool { return a != "Bearer tok" }) {
		t.Fatalf("authorizations %q", api.auth)
	}
}
