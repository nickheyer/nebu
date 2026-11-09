package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/proto/nebu/v1/nebuv1connect"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Answers gateway calls from canned state and records every write the CLI sends
type fakeGateway struct {
	nebuv1connect.UnimplementedGatewayServiceHandler
	mu        sync.Mutex
	pools     map[string]*v1.Pool
	routes    map[string]*v1.Route
	status    *v1.GatewayStatus
	trace     *v1.Trace
	accepted  *v1.Condition
	puts      []proto.Message
	listCalls []*v1.ListTracesRequest
}

func (f *fakeGateway) record(msg proto.Message) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.puts = append(f.puts, proto.Clone(msg))
}

// The writes recorded so far
func (f *fakeGateway) sent() []proto.Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]proto.Message(nil), f.puts...)
}

// Changes the canned state under the lock the handlers read it with
func (f *fakeGateway) set(change func(*fakeGateway)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	change(f)
}

func (f *fakeGateway) GetGatewayStatus(context.Context, *connect.Request[v1.GetGatewayStatusRequest]) (*connect.Response[v1.GetGatewayStatusResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return connect.NewResponse(&v1.GetGatewayStatusResponse{Status: f.status}), nil
}

func (f *fakeGateway) ListRoutes(context.Context, *connect.Request[v1.ListRoutesRequest]) (*connect.Response[v1.ListRoutesResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return connect.NewResponse(&v1.ListRoutesResponse{Routes: f.status.GetRoutes()}), nil
}

func (f *fakeGateway) GetPool(_ context.Context, req *connect.Request[v1.GetPoolRequest]) (*connect.Response[v1.GetPoolResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.pools[req.Msg.GetId()]
	if !ok {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("no such pool"))
	}
	return connect.NewResponse(&v1.GetPoolResponse{Pool: p}), nil
}

func (f *fakeGateway) PutPool(_ context.Context, req *connect.Request[v1.PutPoolRequest]) (*connect.Response[v1.PutPoolResponse], error) {
	f.record(req.Msg)
	pool := &v1.Pool{Id: req.Msg.GetId(), Spec: req.Msg.GetSpec(), Status: &v1.PoolStatus{
		ReadyEndpoints: 1, TotalEndpoints: 1,
		Conditions: []*v1.Condition{{Type: "Accepted", Status: v1.ConditionStatus_CONDITION_STATUS_TRUE, Reason: "Accepted", Message: "the pool spec is valid"}},
		Endpoints: []*v1.Endpoint{{
			Address: "10.0.0.1:9000", NodeId: "node-abcdefghijklmnop", InstanceId: "inst-1", Role: "prefill", Rank: 1, Ready: true, Healthy: true,
			Metrics: &v1.EndpointMetrics{QueuedRequests: 3, RunningRequests: 5, KvCacheUtilization: 0.42}, AssumedRequests: 2,
		}},
	}}
	return connect.NewResponse(&v1.PutPoolResponse{Pool: pool}), nil
}

func (f *fakeGateway) GetRoute(_ context.Context, req *connect.Request[v1.GetRouteRequest]) (*connect.Response[v1.GetRouteResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.routes[req.Msg.GetName()]
	if !ok {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("no such route"))
	}
	return connect.NewResponse(&v1.GetRouteResponse{Route: r}), nil
}

func (f *fakeGateway) PutRoute(_ context.Context, req *connect.Request[v1.PutRouteRequest]) (*connect.Response[v1.PutRouteResponse], error) {
	f.record(req.Msg)
	route := &v1.Route{Name: req.Msg.GetName(), Spec: req.Msg.GetSpec(), Status: &v1.RouteStatus{
		State: v1.RouteState_ROUTE_STATE_READY, PoolIds: []string{"poolA", "poolB"},
		Conditions: []*v1.Condition{{Type: "ResolvedRefs", Status: v1.ConditionStatus_CONDITION_STATUS_TRUE, Reason: "ResolvedRefs", Message: "every target pool exists"}},
	}}
	return connect.NewResponse(&v1.PutRouteResponse{Route: route}), nil
}

func (f *fakeGateway) PutObjective(_ context.Context, req *connect.Request[v1.PutObjectiveRequest]) (*connect.Response[v1.PutObjectiveResponse], error) {
	f.record(req.Msg)
	return connect.NewResponse(&v1.PutObjectiveResponse{Objective: req.Msg.GetObjective()}), nil
}

func (f *fakeGateway) PutSchedulerConfig(_ context.Context, req *connect.Request[v1.PutSchedulerConfigRequest]) (*connect.Response[v1.PutSchedulerConfigResponse], error) {
	f.record(req.Msg)
	f.mu.Lock()
	defer f.mu.Unlock()
	cfg := proto.Clone(req.Msg.GetConfig()).(*v1.SchedulerConfig)
	if f.accepted != nil {
		cfg.Status = &v1.SchedulerConfigStatus{Conditions: []*v1.Condition{f.accepted}}
	}
	return connect.NewResponse(&v1.PutSchedulerConfigResponse{Config: cfg}), nil
}

func (f *fakeGateway) ListTraces(_ context.Context, req *connect.Request[v1.ListTracesRequest]) (*connect.Response[v1.ListTracesResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listCalls = append(f.listCalls, proto.Clone(req.Msg).(*v1.ListTracesRequest))
	return connect.NewResponse(&v1.ListTracesResponse{Traces: []*v1.Trace{f.trace}}), nil
}

func (f *fakeGateway) GetTrace(_ context.Context, req *connect.Request[v1.GetTraceRequest]) (*connect.Response[v1.GetTraceResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if req.Msg.GetId() != f.trace.GetId() {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("no such trace"))
	}
	return connect.NewResponse(&v1.GetTraceResponse{Trace: f.trace}), nil
}

// Answers the mesh calls the CLI makes and records their requests
type fakeMesh struct {
	nebuv1connect.UnimplementedMeshServiceHandler
	mu   sync.Mutex
	sent []proto.Message
}

func (f *fakeMesh) record(msg proto.Message) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, proto.Clone(msg))
}

func (f *fakeMesh) Token(_ context.Context, req *connect.Request[v1.TokenRequest]) (*connect.Response[v1.TokenResponse], error) {
	f.record(req.Msg)
	expires := time.Date(2026, 11, 3, 14, 0, 0, 0, time.UTC)
	return connect.NewResponse(&v1.TokenResponse{Token: "nebu-join-xyz", ExpiresAt: timestamppb.New(expires)}), nil
}

func (f *fakeMesh) HandOffIssuer(_ context.Context, req *connect.Request[v1.HandOffIssuerRequest]) (*connect.Response[v1.HandOffIssuerResponse], error) {
	f.record(req.Msg)
	return connect.NewResponse(&v1.HandOffIssuerResponse{Mesh: &v1.Mesh{Name: "lab", Issuer: "node2-0123456789abcdef", IssuerEpoch: 4}}), nil
}

func (f *fakeMesh) Init(_ context.Context, req *connect.Request[v1.InitRequest]) (*connect.Response[v1.InitResponse], error) {
	f.record(req.Msg)
	return connect.NewResponse(&v1.InitResponse{Mesh: &v1.Mesh{Id: "m1", Name: req.Msg.GetName(), Address: "10.0.0.5:8485", CaFingerprint: "ab12"}, Token: "nebu-join-first"}), nil
}

func (f *fakeMesh) Join(_ context.Context, req *connect.Request[v1.JoinRequest]) (*connect.Response[v1.JoinResponse], error) {
	f.record(req.Msg)
	return connect.NewResponse(&v1.JoinResponse{Mesh: &v1.Mesh{Id: "m1", Name: "lab", Members: 3}, Bootstrap: &v1.Node{Id: "node1-0123456789abcdef", Name: "alpha", Address: "10.0.0.5:8485"}}), nil
}

func (f *fakeMesh) ForgetNode(_ context.Context, req *connect.Request[v1.ForgetNodeRequest]) (*connect.Response[v1.ForgetNodeResponse], error) {
	f.record(req.Msg)
	return connect.NewResponse(&v1.ForgetNodeResponse{Node: &v1.Node{Id: req.Msg.GetNodeId(), Name: "gamma"}}), nil
}

// Signs in one local account by cookie and makes tokens for the session that carries it
type fakeAuth struct {
	nebuv1connect.UnimplementedAuthServiceHandler
	mu      sync.Mutex
	logins  []map[string]string
	created *v1.CreateTokenRequest
	headers http.Header
}

func (f *fakeAuth) login(w http.ResponseWriter, r *http.Request) {
	var creds map[string]string
	if r.Header.Get("Content-Type") != "application/json" || json.NewDecoder(r.Body).Decode(&creds) != nil {
		http.Error(w, `{"error":"send application/json"}`, http.StatusUnsupportedMediaType)
		return
	}
	f.mu.Lock()
	f.logins = append(f.logins, creds)
	f.mu.Unlock()
	if creds["username"] != "alice" || creds["password"] != "s3cret" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":"wrong username or password"}`))
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "nebu_session", Value: "sealed-alice", Path: "/", HttpOnly: true})
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"user":{"name":"alice"}}`))
}

func (f *fakeAuth) CreateToken(_ context.Context, req *connect.Request[v1.CreateTokenRequest]) (*connect.Response[v1.CreateTokenResponse], error) {
	f.mu.Lock()
	f.created, f.headers = proto.Clone(req.Msg).(*v1.CreateTokenRequest), req.Header().Clone()
	f.mu.Unlock()
	if c, err := (&http.Request{Header: req.Header()}).Cookie("nebu_session"); err != nil || c.Value != "sealed-alice" {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("no account, sign in first"))
	}
	tok := &v1.ApiToken{Id: "tok1", Name: req.Msg.GetName(), Prefix: "abcdefghijkl", Secret: "nebu_abcdefghijklSECRET", Scopes: req.Msg.GetScopes()}
	return connect.NewResponse(&v1.CreateTokenResponse{Token: tok}), nil
}

func (f *fakeAuth) ListTokens(_ context.Context, req *connect.Request[v1.ListTokensRequest]) (*connect.Response[v1.ListTokensResponse], error) {
	return connect.NewResponse(&v1.ListTokensResponse{Tokens: []*v1.ApiToken{{Id: "tok1", Name: "ci", Prefix: "abcdefghijkl", Scopes: []v1.Scope{v1.Scope_SCOPE_INFERENCE, v1.Scope_SCOPE_OPERATE}}}}), nil
}

// Starts runs and swaps that finish at once and records what they were asked to run
type fakeRuns struct {
	nebuv1connect.UnimplementedInstanceServiceHandler
	nebuv1connect.UnimplementedSlotServiceHandler
	nebuv1connect.UnimplementedTaskServiceHandler
	mu    sync.Mutex
	runs  []*v1.RunRequest
	swaps []*v1.SwapRequest
	// Adapters by name loaded on the one instance, with every load and unload in order
	adapters map[string]string
	loads    []string
}

func (f *fakeRuns) LoadAdapter(_ context.Context, req *connect.Request[v1.LoadAdapterRequest]) (*connect.Response[v1.LoadAdapterResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.adapters == nil {
		f.adapters = map[string]string{}
	}
	f.adapters[req.Msg.GetName()] = req.Msg.GetPath()
	f.loads = append(f.loads, "load "+req.Msg.GetId()+" "+req.Msg.GetName()+" "+req.Msg.GetPath())
	return connect.NewResponse(&v1.LoadAdapterResponse{Instance: &v1.Instance{Id: "in1", Name: "llama"}}), nil
}

func (f *fakeRuns) UnloadAdapter(_ context.Context, req *connect.Request[v1.UnloadAdapterRequest]) (*connect.Response[v1.UnloadAdapterResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.adapters[req.Msg.GetName()]; !ok {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("no adapter "+req.Msg.GetName()))
	}
	delete(f.adapters, req.Msg.GetName())
	f.loads = append(f.loads, "unload "+req.Msg.GetId()+" "+req.Msg.GetName())
	return connect.NewResponse(&v1.UnloadAdapterResponse{Instance: &v1.Instance{Id: "in1", Name: "llama"}}), nil
}

func (f *fakeRuns) ListAdapters(context.Context, *connect.Request[v1.ListAdaptersRequest]) (*connect.Response[v1.ListAdaptersResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	resp := &v1.ListAdaptersResponse{}
	for name, path := range f.adapters {
		resp.Adapters = append(resp.Adapters, &v1.LoadedAdapter{Name: name, Path: path})
	}
	return connect.NewResponse(resp), nil
}

func (f *fakeRuns) Run(_ context.Context, req *connect.Request[v1.RunRequest]) (*connect.Response[v1.RunResponse], error) {
	f.mu.Lock()
	f.runs = append(f.runs, proto.Clone(req.Msg).(*v1.RunRequest))
	f.mu.Unlock()
	return connect.NewResponse(&v1.RunResponse{Instance: &v1.Instance{Id: "in1", Name: "llama", RuntimeId: "vllm"}, Task: &v1.Task{Id: "task1"}}), nil
}

func (f *fakeRuns) GetInstance(context.Context, *connect.Request[v1.GetInstanceRequest]) (*connect.Response[v1.GetInstanceResponse], error) {
	return connect.NewResponse(&v1.GetInstanceResponse{Instance: &v1.Instance{Id: "in1", Name: "llama", State: v1.InstanceState_INSTANCE_STATE_READY}}), nil
}

func (f *fakeRuns) Swap(_ context.Context, req *connect.Request[v1.SwapRequest]) (*connect.Response[v1.SwapResponse], error) {
	f.mu.Lock()
	f.swaps = append(f.swaps, proto.Clone(req.Msg).(*v1.SwapRequest))
	f.mu.Unlock()
	return connect.NewResponse(&v1.SwapResponse{Slot: &v1.Slot{Id: "s1", Name: "main", State: v1.SlotState_SLOT_STATE_SWAPPING}, Task: &v1.Task{Id: "task2"}}), nil
}

func (f *fakeRuns) GetSlot(context.Context, *connect.Request[v1.GetSlotRequest]) (*connect.Response[v1.GetSlotResponse], error) {
	return connect.NewResponse(&v1.GetSlotResponse{Slot: &v1.Slot{Id: "s1", Name: "main", State: v1.SlotState_SLOT_STATE_READY, Request: &v1.RunRequest{Repo: "org/qwen", Group: "q4"}}}), nil
}

func (f *fakeRuns) WatchTask(_ context.Context, req *connect.Request[v1.WatchTaskRequest], stream *connect.ServerStream[v1.WatchTaskResponse]) error {
	return stream.Send(&v1.WatchTaskResponse{Task: &v1.Task{Id: req.Msg.GetId(), Title: "start", State: v1.TaskState_TASK_STATE_SUCCEEDED}})
}

// The fakes behind one loopback daemon address
type fakeDaemon struct {
	url     string
	srv     *httptest.Server
	gateway *fakeGateway
	mesh    *fakeMesh
	auth    *fakeAuth
	runs    *fakeRuns
}

// Serves the fakes on loopback, a TLS listener when secure
func startDaemon(t *testing.T, secure bool) *fakeDaemon {
	t.Helper()
	d := &fakeDaemon{
		gateway: &fakeGateway{pools: map[string]*v1.Pool{}, routes: map[string]*v1.Route{}, status: &v1.GatewayStatus{}},
		mesh:    &fakeMesh{},
		auth:    &fakeAuth{},
		runs:    &fakeRuns{},
	}
	mux := http.NewServeMux()
	mux.Handle(nebuv1connect.NewGatewayServiceHandler(d.gateway))
	mux.Handle(nebuv1connect.NewMeshServiceHandler(d.mesh))
	mux.Handle(nebuv1connect.NewAuthServiceHandler(d.auth))
	mux.Handle(nebuv1connect.NewInstanceServiceHandler(d.runs))
	mux.Handle(nebuv1connect.NewSlotServiceHandler(d.runs))
	mux.Handle(nebuv1connect.NewTaskServiceHandler(d.runs))
	mux.HandleFunc("POST /auth/local/login", d.auth.login)
	srv := httptest.NewUnstartedServer(mux)
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	if secure {
		srv.StartTLS()
	} else {
		srv.Start()
	}
	t.Cleanup(srv.Close)
	d.url, d.srv = srv.URL, srv
	return d
}

// Runs the CLI with a config in a fresh data directory, returning its output and exit code
func runCLI(t *testing.T, dataDir, configText, addr string, args ...string) (string, string, int) {
	t.Helper()
	for _, kv := range os.Environ() {
		if name, _, _ := strings.Cut(kv, "="); strings.HasPrefix(name, "NEBU_") {
			t.Setenv(name, "")
		}
	}
	cfg := filepath.Join(dataDir, "config.yaml")
	text := "data_dir: " + dataDir + "\nauth:\n  disabled: true\nlogging:\n  level: error\n" + configText
	if err := os.WriteFile(cfg, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errw bytes.Buffer
	code := Main(append([]string{"--config", cfg, "--addr", addr}, args...), &out, &errw)
	return out.String(), errw.String(), code
}

// Runs the CLI over plaintext against the fake daemon and fails the test on a nonzero exit
func (d *fakeDaemon) run(t *testing.T, args ...string) string {
	t.Helper()
	out, errOut, code := runCLI(t, t.TempDir(), "", d.url, args...)
	if code != 0 {
		t.Fatalf("nebu %s exited %d: %s", strings.Join(args, " "), code, errOut)
	}
	return out
}

// Fails the test unless the output holds every piece
func contains(t *testing.T, out string, pieces ...string) {
	t.Helper()
	for _, p := range pieces {
		if !strings.Contains(out, p) {
			t.Fatalf("output lacks %q:\n%s", p, out)
		}
	}
}

func TestPoolsPutCreatesFromFlags(t *testing.T) {
	d := startDaemon(t, false)
	out := d.run(t, "pools", "put", "p1", "--model", "llama", "--runtime", "vllm", "--port", "8001", "--protocol", "h2c",
		"--failure-mode", "fail-open", "--scheduler-config", "default", "--static", "10.0.0.1:9000,role=prefill,rank=1",
		"--export", "--concurrency", "4", "--selector", "tier=gpu")
	sent := d.gateway.sent()
	if len(sent) != 1 {
		t.Fatalf("sent %v", sent)
	}
	sameMessage(t, "put pool", sent[0], &v1.PutPoolRequest{Id: "p1", Spec: &v1.PoolSpec{
		Model: "llama", RuntimeId: "vllm", TargetPorts: []uint32{8001}, AppProtocol: v1.AppProtocol_APP_PROTOCOL_H2C,
		FailureMode: v1.FailureMode_FAILURE_MODE_FAIL_OPEN, SchedulerConfig: "default",
		StaticEndpoints: []*v1.StaticEndpoint{{Address: "10.0.0.1:9000", Role: "prefill", Rank: 1}},
		Export:          proto.Bool(true), Concurrency: 4, Selector: map[string]string{"tier": "gpu"},
	}})
	contains(t, out, "p1 owned by user", "h2c", "fail-open", "tier=gpu", "Accepted", "the pool spec is valid",
		"ADDRESS", "KV", "ASSUMED", "10.0.0.1:9000", "node-abcdefg", "inst-1", "prefill", "42%")
}

func TestPoolsPutChangesOnlyTheFlagsGiven(t *testing.T) {
	d := startDaemon(t, false)
	current := &v1.PoolSpec{Model: "llama", RuntimeId: "vllm", TargetPorts: []uint32{8000}, FailureMode: v1.FailureMode_FAILURE_MODE_FAIL_CLOSE, Concurrency: 2}
	d.gateway.set(func(f *fakeGateway) {
		f.pools["p1"] = &v1.Pool{Id: "p1", OwnerKind: "slot", OwnerId: "s1", Spec: current}
	})
	d.run(t, "pools", "put", "p1", "--failure-mode", "fail-open")
	want := proto.Clone(current).(*v1.PoolSpec)
	want.FailureMode = v1.FailureMode_FAILURE_MODE_FAIL_OPEN
	sameMessage(t, "put pool", d.gateway.sent()[0], &v1.PutPoolRequest{Id: "p1", Spec: want})
}

func TestPoolsPutFromFile(t *testing.T) {
	d := startDaemon(t, false)
	d.gateway.set(func(f *fakeGateway) {
		f.pools["p1"] = &v1.Pool{Id: "p1", Spec: &v1.PoolSpec{Model: "ignored", TargetPorts: []uint32{1}}}
	})
	file := filepath.Join(t.TempDir(), "pool.yaml")
	if err := os.WriteFile(file, []byte("model: llama\ntarget_ports: [8001]\napp_protocol: grpc\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	d.run(t, "pools", "put", "p1", "--file", file, "--port", "9001")
	sameMessage(t, "put pool", d.gateway.sent()[0], &v1.PutPoolRequest{Id: "p1", Spec: &v1.PoolSpec{Model: "llama", TargetPorts: []uint32{9001}, AppProtocol: v1.AppProtocol_APP_PROTOCOL_GRPC}})
}

func TestRoutesPut(t *testing.T) {
	d := startDaemon(t, false)
	d.gateway.set(func(f *fakeGateway) { f.status.Policy = &v1.Policy{UpstreamTimeoutMs: 600000} })
	out := d.run(t, "routes", "put", "chat", "--target", "poolA=llama:3", "--target", "poolB:1", "--match", "chat",
		"--objective", "interactive", "--max-in-flight", "8", "--rps", "2.5", "--burst", "4", "--timeout", "30s",
		"--tool-server", "fs", "--mutating-tools")
	sameMessage(t, "put route", d.gateway.sent()[0], &v1.PutRouteRequest{Name: "chat", Spec: &v1.RouteSpec{
		Rules:     []*v1.RewriteRule{{Match: "chat", Targets: []*v1.RewriteTarget{{PoolId: "poolA", Model: "llama", Weight: 3}, {PoolId: "poolB", Weight: 1}}}},
		Policy:    &v1.Policy{MaxInFlight: 8, RequestsPerSecond: 2.5, Burst: 4, RequestTimeoutMs: 30000},
		Profile:   &v1.Profile{ToolServers: []string{"fs"}, MutatingTools: true},
		Objective: "interactive",
	}})
	contains(t, out, "chat READY", "poolA, poolB", "interactive", "tool-servers fs mutating-tools", "upstream 10m0s",
		"MATCH", "poolA", "llama", "75%", "25%", "ResolvedRefs", "every target pool exists")
}

func TestRoutesPutKeepsOtherRulesAndSettings(t *testing.T) {
	d := startDaemon(t, false)
	catchAll := &v1.RewriteRule{Targets: []*v1.RewriteTarget{{PoolId: "old", Weight: 1}}}
	code := &v1.RewriteRule{Match: "code", Targets: []*v1.RewriteTarget{{PoolId: "coder", Weight: 1}}}
	d.gateway.set(func(f *fakeGateway) {
		f.routes["chat"] = &v1.Route{Name: "chat", Spec: &v1.RouteSpec{Rules: []*v1.RewriteRule{catchAll, code}, Objective: "batch", Policy: &v1.Policy{MaxInFlight: 2}}}
	})
	d.run(t, "routes", "put", "chat", "--target", "new:1", "--rps", "5")
	sameMessage(t, "put route", d.gateway.sent()[0], &v1.PutRouteRequest{Name: "chat", Spec: &v1.RouteSpec{
		Rules:     []*v1.RewriteRule{{Targets: []*v1.RewriteTarget{{PoolId: "new", Weight: 1}}}, code},
		Policy:    &v1.Policy{MaxInFlight: 2, RequestsPerSecond: 5},
		Profile:   &v1.Profile{},
		Objective: "batch",
	}})
}

func TestRoutesPutRefusesBadFlags(t *testing.T) {
	d := startDaemon(t, false)
	for _, args := range [][]string{
		{"routes", "put", "chat", "--match", "chat"},
		{"routes", "put", "chat", "--target", "p:0"},
		{"routes", "put", "chat", "--target", "p", "--mutating-tools"},
		{"routes", "put", "chat", "--target", "p", "--timeout", "-1s"},
	} {
		if _, _, code := runCLI(t, t.TempDir(), "", d.url, args...); code == 0 {
			t.Fatalf("nebu %s succeeded", strings.Join(args, " "))
		}
	}
	if sent := d.gateway.sent(); len(sent) != 0 {
		t.Fatalf("refused flags still sent %v", sent)
	}
}

func TestObjectivesPut(t *testing.T) {
	d := startDaemon(t, false)
	out := d.run(t, "objectives", "put", "interactive", "--priority", "-5", "--ttft-ms", "200", "--tpot-ms", "40")
	sameMessage(t, "put objective", d.gateway.sent()[0], &v1.PutObjectiveRequest{Objective: &v1.Objective{Name: "interactive", Priority: -5, TtftMs: 200, TpotMs: 40}})
	contains(t, out, "interactive", "-5", "200ms", "40ms")
}

func TestSchedulersValidateAndPut(t *testing.T) {
	d := startDaemon(t, false)
	file := filepath.Join(t.TempDir(), "sched.yaml")
	yamlText := "plugins:\n  - type: queue-scorer\n"
	if err := os.WriteFile(file, []byte(yamlText), 0o600); err != nil {
		t.Fatal(err)
	}
	d.gateway.set(func(f *fakeGateway) {
		f.accepted = &v1.Condition{Type: "Accepted", Status: v1.ConditionStatus_CONDITION_STATUS_TRUE, Reason: "Accepted", Message: "the configuration loads"}
	})
	out := d.run(t, "schedulers", "validate", "fast", "--file", file)
	sameMessage(t, "validate", d.gateway.sent()[0], &v1.PutSchedulerConfigRequest{Config: &v1.SchedulerConfig{Name: "fast", Spec: &v1.SchedulerConfigSpec{Yaml: yamlText}}, ValidateOnly: true})
	contains(t, out, "fast Accepted TRUE Accepted: the configuration loads")

	out = d.run(t, "schedulers", "put", "fast", "--file", file)
	sameMessage(t, "put", d.gateway.sent()[1], &v1.PutSchedulerConfigRequest{Config: &v1.SchedulerConfig{Name: "fast", Spec: &v1.SchedulerConfigSpec{Yaml: yamlText}}})
	contains(t, out, "CONDITIONS", "YAML", "queue-scorer")

	d.gateway.set(func(f *fakeGateway) {
		f.accepted = &v1.Condition{Type: "Accepted", Status: v1.ConditionStatus_CONDITION_STATUS_FALSE, Reason: "InvalidSchedulerConfig", Message: "unknown plugin"}
	})
	_, errOut, code := runCLI(t, t.TempDir(), "", d.url, "schedulers", "validate", "fast", "--file", file)
	if code == 0 || !strings.Contains(errOut, "unknown plugin") {
		t.Fatalf("a refused configuration exited %d: %s", code, errOut)
	}
	d.gateway.set(func(f *fakeGateway) { f.accepted = nil })
	if _, _, code := runCLI(t, t.TempDir(), "", d.url, "schedulers", "validate", "fast", "--file", file); code == 0 {
		t.Fatal("an answer without an Accepted condition passed validation")
	}
	if _, _, code := runCLI(t, t.TempDir(), "", d.url, "schedulers", "validate", "fast"); code == 0 {
		t.Fatal("validate without --file succeeded")
	}
}

func TestGatewayTracesShowTheSchedulingFields(t *testing.T) {
	d := startDaemon(t, false)
	start := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	d.gateway.set(func(f *fakeGateway) {
		f.trace = &v1.Trace{
			Id: "tr1", Route: "chat", PoolId: "poolA", Endpoint: "10.0.0.1:9000", Profile: "default", Score: 0.75,
			QueueWaitMs: 12, Priority: 3, Objective: "interactive", ObjectiveAttained: true, FairnessId: "team-a", FailedOpen: true,
			Fallbacks: []*v1.TraceFallback{{Address: "10.0.0.2:9000", Reason: "connection refused"}, {Address: "10.0.0.3:9000", Reason: "503", RequestWritten: true, BytesToClient: 0}}, RequestId: "req-9", EntryNode: "edge-1", Status: 200,
			StartedAt: timestamppb.New(start), FinishedAt: timestamppb.New(start.Add(time.Second)),
		}
	})
	out := d.run(t, "gateway", "trace", "tr1")
	contains(t, out, "poolA", "10.0.0.1:9000", "default, score 0.75", "12ms", "interactive, attained yes", "team-a",
		"failed open", "10.0.0.2:9000 (connection refused, before the request was written, 0 bytes to client), 10.0.0.3:9000 (503, after the request was written, 0 bytes to client)", "req-9", "edge-1")
	out = d.run(t, "gateway", "traces", "--route", "chat", "--limit", "5")
	contains(t, out, "POOL", "ENDPOINT", "WAIT", "tr1", "poolA", "10.0.0.1:9000", "12ms")
	d.gateway.set(func(f *fakeGateway) {
		if len(f.listCalls) != 1 || !proto.Equal(f.listCalls[0], &v1.ListTracesRequest{Route: "chat", Limit: 5}) {
			t.Errorf("list calls %v", f.listCalls)
		}
	})
}

func TestGatewayStatusShowsPoolsRoutesAndMetrics(t *testing.T) {
	d := startDaemon(t, false)
	d.gateway.set(func(f *fakeGateway) {
		f.status = &v1.GatewayStatus{
			Listeners:   []*v1.Listener{{Addr: "127.0.0.1:8080"}},
			MetricsAddr: "127.0.0.1:9090",
			Policy:      &v1.Policy{MaxInFlight: 16},
			Pools:       []*v1.Pool{{Id: "vllm/hf:org/llama:q4", OwnerKind: "slot", OwnerId: "s1", Spec: &v1.PoolSpec{Model: "llama", RuntimeId: "vllm", TargetPorts: []uint32{8001}}, Status: &v1.PoolStatus{ReadyEndpoints: 2, TotalEndpoints: 3}}},
			Routes:      []*v1.Route{{Name: "chat", Spec: &v1.RouteSpec{Objective: "interactive"}, Status: &v1.RouteStatus{State: v1.RouteState_ROUTE_STATE_READY, PoolIds: []string{"vllm/hf:org/llama:q4"}, Requests: 7, InFlight: 1}}},
		}
	})
	out := d.run(t, "gateway", "status")
	contains(t, out, "metrics 127.0.0.1:9090", "POOLS", "vllm/hf:org/llama:q4", "slot/s1", "2/3", "ROUTES", "chat", "READY", "in-flight 16", "interactive")
	out = d.run(t, "routes", "list")
	contains(t, out, "NAME", "OWNER", "POOLS", "OBJECTIVE", "chat", "user", "in-flight 16")
}

func TestMeshCommands(t *testing.T) {
	d := startDaemon(t, false)
	out := d.run(t, "mesh", "token", "--ttl", "2h")
	contains(t, out, "nebu-join-xyz\n", "expires ")
	out = d.run(t, "mesh", "hand-off-issuer", "beta")
	contains(t, out, "beta node2-012345 now holds the issuer role of lab, epoch 4")
	out = d.run(t, "mesh", "init", "--name", "lab")
	contains(t, out, "mesh lab m1 at 10.0.0.5:8485, certificate authority sha256 ab12", "nebu-join-first")
	out = d.run(t, "mesh", "join", "nebu-join-first")
	contains(t, out, "joined lab m1 through alpha node1-012345 at 10.0.0.5:8485, 3 members")
	d.run(t, "mesh", "forget", "gamma")
	if _, _, code := runCLI(t, t.TempDir(), "", d.url, "mesh", "token", "--ttl", "90ms"); code == 0 {
		t.Fatal("a token lifetime under a second was taken")
	}
	d.mesh.mu.Lock()
	defer d.mesh.mu.Unlock()
	want := []proto.Message{&v1.TokenRequest{TtlSeconds: 7200}, &v1.HandOffIssuerRequest{NodeId: "beta"}, &v1.InitRequest{Name: "lab"}, &v1.JoinRequest{Token: "nebu-join-first"}, &v1.ForgetNodeRequest{NodeId: "gamma"}}
	if len(d.mesh.sent) != len(want) {
		t.Fatalf("sent %v", d.mesh.sent)
	}
	for i := range want {
		sameMessage(t, "mesh request", d.mesh.sent[i], want[i])
	}
}

func TestTokensCreateSignsInAsTheAccount(t *testing.T) {
	d := startDaemon(t, false)
	out := d.run(t, "tokens", "create", "ci", "--scopes", "inference,read", "--user", "alice", "--password", "s3cret")
	contains(t, out, "created ci tok1 holding inference,read", "nebu_abcdefghijklSECRET")
	d.auth.mu.Lock()
	if len(d.auth.logins) != 1 || d.auth.logins[0]["username"] != "alice" {
		t.Fatalf("logins %v", d.auth.logins)
	}
	sameMessage(t, "create token", d.auth.created, &v1.CreateTokenRequest{Name: "ci", Scopes: []v1.Scope{v1.Scope_SCOPE_INFERENCE, v1.Scope_SCOPE_READ}})
	if d.auth.headers.Get("Authorization") != "" {
		t.Fatalf("the session call also sent %q", d.auth.headers.Get("Authorization"))
	}
	d.auth.mu.Unlock()

	_, errOut, code := runCLI(t, t.TempDir(), "", d.url, "tokens", "create", "ci", "--scopes", "inference", "--user", "alice", "--password", "wrong")
	if code == 0 || !strings.Contains(errOut, "wrong username or password") {
		t.Fatalf("a wrong password exited %d: %s", code, errOut)
	}
	if _, _, code := runCLI(t, t.TempDir(), "", d.url, "tokens", "create", "ci", "--scopes", "write", "--user", "alice", "--password", "s3cret"); code == 0 {
		t.Fatal("an unknown scope was sent")
	}
	out = d.run(t, "tokens", "list")
	contains(t, out, "SCOPES", "inference,operate", "abcdefghijkl")
}

func TestTLSTrustsTheDaemonCertificate(t *testing.T) {
	d := startDaemon(t, true)
	u, err := url.Parse(d.url)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "tls.crt"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: d.srv.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	out, errOut, code := runCLI(t, dir, "tls:\n  enabled: true\n", u.Host, "objectives", "put", "fast", "--priority", "1")
	if code != 0 {
		t.Fatalf("the generated certificate was not trusted, exit %d: %s", code, errOut)
	}
	contains(t, out, "fast")
	if _, errOut, code := runCLI(t, t.TempDir(), "tls:\n  enabled: true\n", u.Host, "objectives", "put", "fast"); code == 0 || !strings.Contains(errOut, "certificate") {
		t.Fatalf("an unknown certificate was trusted, exit %d: %s", code, errOut)
	}
	if _, _, code := runCLI(t, t.TempDir(), "", u.Host, "objectives", "put", "fast"); code == 0 {
		t.Fatal("plaintext reached a TLS listener")
	}
}

func TestRunSendsTheRouteItNamesForItself(t *testing.T) {
	d := startDaemon(t, false)
	out := d.run(t, "run", "org/llama", "--source", "hf", "--group", "q4", "--name", "llama", "--max-in-flight", "4", "--timeout", "2m",
		"--tool-server", "fs", "--mutating-tools", "--idle-unload", "30m")
	contains(t, out, "instance in1 llama on vllm", "model llama")
	d.runs.mu.Lock()
	defer d.runs.mu.Unlock()
	if len(d.runs.runs) != 1 {
		t.Fatalf("runs %v", d.runs.runs)
	}
	sameMessage(t, "run", d.runs.runs[0], &v1.RunRequest{
		SourceId: "hf", Repo: "org/llama", Group: "q4", Name: "llama",
		Policy:            &v1.Policy{MaxInFlight: 4, RequestTimeoutMs: 120000},
		RouteProfile:      &v1.Profile{ToolServers: []string{"fs"}, MutatingTools: true},
		IdleUnloadSeconds: 1800,
	})
}

func TestSwapSendsTheCanary(t *testing.T) {
	d := startDaemon(t, false)
	out := d.run(t, "swap", "main", "org/qwen", "--source", "hf", "--group", "q4", "--canary-percent", "10", "--canary-seconds", "600", "--canary-step-percent", "30", "--canary-max-regression-percent", "0")
	contains(t, out, "slot main swapping", "model main serves org/qwen:q4")
	d.run(t, "swap", "main", "org/qwen", "--source", "hf", "--group", "q4", "--canary-percent", "5", "--canary-seconds", "60")
	for _, shape := range [][]string{{"--canary-seconds", "600"}, {"--canary-step-percent", "10"}, {"--canary-max-regression-percent", "5"}} {
		args := append([]string{"swap", "main", "org/qwen", "--source", "hf", "--group", "q4"}, shape...)
		if _, errOut, code := runCLI(t, t.TempDir(), "", d.url, args...); code == 0 || !strings.Contains(errOut, "--canary-percent") {
			t.Fatalf("canary %v without a share exited %d: %s", shape, code, errOut)
		}
	}
	d.runs.mu.Lock()
	defer d.runs.mu.Unlock()
	if len(d.runs.swaps) != 2 {
		t.Fatalf("swaps %v", d.runs.swaps)
	}
	run := &v1.RunRequest{SourceId: "hf", Repo: "org/qwen", Group: "q4"}
	sameMessage(t, "stepped swap", d.runs.swaps[0], &v1.SwapRequest{SlotId: "main", Run: run, CanaryPercent: 10, CanarySeconds: 600, CanaryStepPercent: 30, CanaryMaxRegressionPercent: proto.Uint32(0)})
	sameMessage(t, "one step swap", d.runs.swaps[1], &v1.SwapRequest{SlotId: "main", Run: run, CanaryPercent: 5, CanarySeconds: 60})
	if d.runs.swaps[1].CanaryMaxRegressionPercent != nil {
		t.Fatal("an unset regression bound reached the daemon as set")
	}
}

// The adapter commands load, list and unload through the instance service, an unknown adapter failing the unload
func TestAdapterCommands(t *testing.T) {
	d := startDaemon(t, false)
	contains(t, d.run(t, "adapters", "load", "llama", "sql", "--path", "/adapters/sql"), "sql loaded on llama")
	contains(t, d.run(t, "adapters", "list", "llama"), "NAME", "sql", "/adapters/sql")
	contains(t, d.run(t, "adapters", "unload", "llama", "sql"), "sql unloaded from llama")
	if _, _, code := runCLI(t, t.TempDir(), "", d.url, "adapters", "unload", "llama", "sql"); code == 0 {
		t.Fatal("unloading an adapter that is not loaded fails")
	}
	d.runs.mu.Lock()
	defer d.runs.mu.Unlock()
	if want := []string{"load llama sql /adapters/sql", "unload llama sql"}; strings.Join(d.runs.loads, ",") != strings.Join(want, ",") {
		t.Fatalf("calls %v", d.runs.loads)
	}
}

// A run with --snapshot asks the daemon to snapshot the process once ready
func TestRunSnapshotFlag(t *testing.T) {
	d := startDaemon(t, false)
	d.run(t, "run", "org/qwen", "--source", "hf", "--group", "q4", "--snapshot")
	d.runs.mu.Lock()
	defer d.runs.mu.Unlock()
	if len(d.runs.runs) != 1 || !d.runs.runs[0].GetSnapshot() {
		t.Fatalf("runs %v", d.runs.runs)
	}
}

var exportExpires = time.Date(2026, 11, 3, 14, 0, 0, 0, time.UTC)

func (f *fakeMesh) ExportToken(_ context.Context, req *connect.Request[v1.ExportTokenRequest]) (*connect.Response[v1.ExportTokenResponse], error) {
	f.record(req.Msg)
	return connect.NewResponse(&v1.ExportTokenResponse{Token: "nebu-export.abc.def", ExpiresAt: timestamppb.New(exportExpires)}), nil
}

func (f *fakeMesh) ImportMesh(_ context.Context, req *connect.Request[v1.ImportMeshRequest]) (*connect.Response[v1.ImportMeshResponse], error) {
	f.record(req.Msg)
	return connect.NewResponse(&v1.ImportMeshResponse{Node: &v1.Node{Id: "node9-0123456789abcdef", Name: "omega", Pools: []*v1.Pool{{Id: "vllm/hf:org/m:Q4"}}, Federation: &v1.Federation{MeshName: "far", Address: "10.0.9.9:8485", Role: v1.FederationRole_FEDERATION_ROLE_EXPORTER}}}), nil
}

func (f *fakeMesh) DropFederation(_ context.Context, req *connect.Request[v1.DropFederationRequest]) (*connect.Response[v1.DropFederationResponse], error) {
	f.record(req.Msg)
	return connect.NewResponse(&v1.DropFederationResponse{Node: &v1.Node{Id: "node9-0123456789abcdef", Name: "omega", Federation: &v1.Federation{MeshName: "far"}}}), nil
}

func (f *fakeMesh) Discover(_ context.Context, req *connect.Request[v1.DiscoverRequest]) (*connect.Response[v1.DiscoverResponse], error) {
	f.record(req.Msg)
	return connect.NewResponse(&v1.DiscoverResponse{Status: &v1.MeshStatus{Federations: []*v1.Node{
		{Id: "node9-0123456789abcdef", Name: "omega", State: v1.NodeState_NODE_STATE_READY, Pools: []*v1.Pool{{Id: "p"}}, Federation: &v1.Federation{MeshName: "far", Address: "10.0.9.9:8485", Role: v1.FederationRole_FEDERATION_ROLE_EXPORTER}},
		{Id: "node8-0123456789abcdef", Name: "theta", State: v1.NodeState_NODE_STATE_READY, Federation: &v1.Federation{MeshName: "near", Address: "10.0.8.8:8485", Role: v1.FederationRole_FEDERATION_ROLE_IMPORTER}},
	}}}), nil
}

// Export tokens, imports from other meshes and their removal go through the mesh service and print what happened
func TestMeshFederationCommands(t *testing.T) {
	d := startDaemon(t, false)
	out := d.run(t, "mesh", "export-token", "--ttl", "30m")
	contains(t, out, "nebu-export.abc.def\n", "expires "+exportExpires.Local().Format(time.RFC3339), "nebu mesh import")
	out = d.run(t, "mesh", "import", "nebu-export.abc.def")
	contains(t, out, "importing from omega node9-012345 of mesh far at 10.0.9.9:8485, 1 pools exported")
	out = d.run(t, "mesh", "unimport", "omega")
	contains(t, out, "dropped omega node9-012345 of mesh far, its imports draining")
	out = d.run(t, "mesh", "federations")
	contains(t, out, "imports from", "omega", "far", "10.0.9.9:8485", "exports to", "theta", "near")
	d.mesh.mu.Lock()
	defer d.mesh.mu.Unlock()
	want := []proto.Message{&v1.ExportTokenRequest{TtlSeconds: 1800}, &v1.ImportMeshRequest{Token: "nebu-export.abc.def"}, &v1.DropFederationRequest{NodeId: "omega"}, &v1.DiscoverRequest{}}
	if len(d.mesh.sent) != len(want) {
		t.Fatalf("sent %v", d.mesh.sent)
	}
	for i := range want {
		sameMessage(t, "mesh request", d.mesh.sent[i], want[i])
	}
}
