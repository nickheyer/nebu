package services

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/nickheyer/nebu/internal/auth"
	"github.com/nickheyer/nebu/internal/db"
	"github.com/nickheyer/nebu/internal/gateway"
	"github.com/nickheyer/nebu/internal/pools"
	"github.com/nickheyer/nebu/internal/tools"
	"github.com/nickheyer/nebu/pkg/chattemplate"
	"github.com/nickheyer/nebu/pkg/datalayer"
	"github.com/nickheyer/nebu/pkg/events"
	"github.com/nickheyer/nebu/pkg/flowcontrol"
	"github.com/nickheyer/nebu/pkg/health"
	"github.com/nickheyer/nebu/pkg/metrics"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/scheduling/config"
	"github.com/nickheyer/nebu/pkg/stub"
	"github.com/nickheyer/nebu/pkg/telemetry"
	"github.com/prometheus/client_golang/prometheus"
)

type openAIRuntimes struct{}

func (openAIRuntimes) API(string) v1.ApiFlavor { return v1.ApiFlavor_API_FLAVOR_OPENAI }
func (openAIRuntimes) Handoff(string) string   { return "" }

type plainInstances struct{}

func (plainInstances) ToolSupport(string) v1.ToolSupport {
	return v1.ToolSupport_TOOL_SUPPORT_UNSPECIFIED
}
func (plainInstances) StripsBillingHeader(string) bool { return true }
func (plainInstances) Modes(string) []string           { return nil }
func (plainInstances) LoadAdapter(context.Context, string, string, string) error {
	return errors.New("no adapter loads in this test")
}
func (plainInstances) TokenizerFile(string) (string, error) {
	return "", errors.New("no tokenizer files in this test")
}
func (plainInstances) Template(string) (*chattemplate.Template, error) {
	return nil, errors.New("no templates in this test")
}

// A running gateway over a real pools manager whose guard takes the daemon token, with one stub behind route chat
func gatewayHarness(t *testing.T) (*GatewayService, *pools.Manager, *gateway.Gateway, *events.Bus) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	store, err := db.Open(filepath.Join(t.TempDir(), "nebu.db"))
	if err != nil {
		t.Fatal(err)
	}
	bus := events.New()
	log := slog.New(slog.DiscardHandler)
	mgr, err := pools.New(ctx, pools.Options{DB: store, Events: bus, NodeID: "n1", Log: log})
	if err != nil {
		t.Fatal(err)
	}
	mgr.Store().MarkSynced()
	tp, shutdown, err := telemetry.Setup(ctx, telemetry.Config{ServiceName: "services-test", SampleRatio: 1})
	if err != nil {
		t.Fatal(err)
	}
	mreg, err := metrics.New(prometheus.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}
	guard := auth.NewGuard("daemon-token", nil, nil)
	gw, err := gateway.New(gateway.Deps{
		Pools: mgr, Resolver: mgr, Manager: mgr, Store: mgr.Store(), Sampler: mgr.Sampler(), DB: store, Events: bus, Guard: guard,
		Tools: tools.New(log, bus), Runtimes: openAIRuntimes{}, Instances: plainInstances{},
		Metrics: mreg, Telemetry: tp, Health: health.NewRegistry(), Logger: log,
		Config: gateway.Config{
			Policy: &v1.Policy{}, OutputTokens: 64,
			Flow:  flowcontrol.Config{QueueCapacity: 16, QueueTimeout: 2 * time.Second, SheddableBelow: -1, Tick: 10 * time.Millisecond},
			Retry: gateway.Retry{Shed: time.Second, Unavailable: time.Second}, TraceRetention: time.Hour, TraceRing: 32,
			Version: "test", Interval: 50 * time.Millisecond,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		gw.Run(ctx)
	}()
	s, err := stub.New(stub.Config{ID: "s1", Model: "stub-model"})
	if err != nil {
		t.Fatal(err)
	}
	up := httptest.NewServer(s)
	t.Cleanup(func() {
		up.Close()
		s.Close()
		cancel()
		<-done
		mgr.Close()
		shutdown(context.Background())
		store.Close()
	})
	addr, err := datalayer.ParseAddress(strings.TrimPrefix(up.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	owner := pools.OwnerRef{Kind: pools.OwnerInstance, ID: "inst-1"}
	mem := pools.Membership{Model: pools.ModelKey{SourceID: "hf", Repo: "stub", Group: "q4"}, Runtime: "vllm", Protocol: datalayer.AppProtocolHTTP, Concurrency: 8, Metrics: &v1.MetricNames{HealthPath: "/health"}}
	if err := mgr.Publish(ctx, owner, mem, []pools.Published{{InstanceID: "inst-1", NodeID: "n1", Address: addr, Ready: true}}); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.NameRoute(ctx, owner, "chat", pools.RouteSpec{ServedName: "stub-model"}); err != nil {
		t.Fatal(err)
	}
	return NewGatewayService(gw, mgr, guard), mgr, gw, bus
}

func bearer[T any](msg *T) *connect.Request[T] {
	return withBearer(msg, "daemon-token")
}

// Pools, routes with weighted targets, objectives and scheduler configurations go through the pools manager with their refusals coded
func TestGatewayServiceResources(t *testing.T) {
	svc, _, _, _ := gatewayHarness(t)
	ctx := context.Background()
	pool, err := svc.PutPool(ctx, bearer(&v1.PutPoolRequest{Id: "static", Spec: &v1.PoolSpec{Model: "ext", TargetPorts: []uint32{8000}, StaticEndpoints: []*v1.StaticEndpoint{{Address: "127.0.0.1:8000"}}}}))
	if err != nil || pool.Msg.GetPool().GetId() != "static" {
		t.Fatalf("put pool %v %v", pool, err)
	}
	if got, err := svc.GetPool(ctx, bearer(&v1.GetPoolRequest{Id: "static"})); err != nil || got.Msg.GetPool().GetSpec().GetModel() != "ext" {
		t.Fatalf("get pool %v %v", got, err)
	}
	if _, err := svc.GetPool(ctx, bearer(&v1.GetPoolRequest{Id: "nope"})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("missing pool %v", err)
	}
	list, err := svc.ListPools(ctx, bearer(&v1.ListPoolsRequest{}))
	if err != nil || len(list.Msg.GetPools()) != 2 {
		t.Fatalf("pools %v %v", list, err)
	}
	if _, err := svc.PutObjective(ctx, bearer(&v1.PutObjectiveRequest{Objective: &v1.Objective{Name: "fast", Priority: 5, TtftMs: 200}})); err != nil {
		t.Fatal(err)
	}
	spec := &v1.RouteSpec{Objective: "fast", Rules: []*v1.RewriteRule{{Targets: []*v1.RewriteTarget{{PoolId: "static", Weight: 3}, {PoolId: "vllm/hf:stub:q4", Model: "stub-model", Weight: 1}}}}}
	rt, err := svc.PutRoute(ctx, bearer(&v1.PutRouteRequest{Name: "split", Spec: spec}))
	if err != nil || len(rt.Msg.GetRoute().GetSpec().GetRules()[0].GetTargets()) != 2 {
		t.Fatalf("put route %v %v", rt, err)
	}
	if got, err := svc.GetRoute(ctx, bearer(&v1.GetRouteRequest{Name: "split"})); err != nil || got.Msg.GetRoute().GetSpec().GetObjective() != "fast" {
		t.Fatalf("get route %v %v", got, err)
	}
	if _, err := svc.DeleteObjective(ctx, bearer(&v1.DeleteObjectiveRequest{Name: "fast"})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("an objective a route names is kept, got %v", err)
	}
	if _, err := svc.DeleteRoute(ctx, bearer(&v1.DeleteRouteRequest{Name: "chat"})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("an owner's route is refused, got %v", err)
	}
	routes, err := svc.ListRoutes(ctx, bearer(&v1.ListRoutesRequest{}))
	if err != nil || len(routes.Msg.GetRoutes()) != 2 {
		t.Fatalf("routes %v %v", routes, err)
	}
	if _, err := svc.DeleteRoute(ctx, bearer(&v1.DeleteRouteRequest{Name: "split"})); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.GetRoute(ctx, bearer(&v1.GetRouteRequest{Name: "split"})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("deleted route %v", err)
	}
	if _, err := svc.DeleteObjective(ctx, bearer(&v1.DeleteObjectiveRequest{Name: "fast"})); err != nil {
		t.Fatal(err)
	}
	objectives, err := svc.ListObjectives(ctx, bearer(&v1.ListObjectivesRequest{}))
	if err != nil || len(objectives.Msg.GetObjectives()) != 0 {
		t.Fatalf("objectives %v %v", objectives, err)
	}
	if _, err := svc.DeletePool(ctx, bearer(&v1.DeletePoolRequest{Id: "static"})); err != nil {
		t.Fatal(err)
	}
}

// A validate only put answers whether the YAML loads without storing it, a plain put stores it, and built in configs stay
func TestGatewayServiceSchedulerConfigs(t *testing.T) {
	svc, _, _, _ := gatewayHarness(t)
	ctx := context.Background()
	before, err := svc.ListSchedulerConfigs(ctx, bearer(&v1.ListSchedulerConfigsRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	checked, err := svc.PutSchedulerConfig(ctx, bearer(&v1.PutSchedulerConfigRequest{Config: &v1.SchedulerConfig{Name: "mine", Spec: &v1.SchedulerConfigSpec{Yaml: config.DefaultConfig}}, ValidateOnly: true}))
	if err != nil {
		t.Fatal(err)
	}
	conds := checked.Msg.GetConfig().GetStatus().GetConditions()
	if len(conds) != 1 || conds[0].GetType() != pools.ConditionAccepted || conds[0].GetStatus() != v1.ConditionStatus_CONDITION_STATUS_TRUE {
		t.Fatalf("validate only answers the accepted condition: %v", conds)
	}
	after, err := svc.ListSchedulerConfigs(ctx, bearer(&v1.ListSchedulerConfigsRequest{}))
	if err != nil || len(after.Msg.GetConfigs()) != len(before.Msg.GetConfigs()) {
		t.Fatalf("validate only stored nothing: %d then %d", len(before.Msg.GetConfigs()), len(after.Msg.GetConfigs()))
	}
	if _, err := svc.PutSchedulerConfig(ctx, bearer(&v1.PutSchedulerConfigRequest{Config: &v1.SchedulerConfig{Name: "mine", Spec: &v1.SchedulerConfigSpec{Yaml: "plugins: [{type: no-such-plugin}]"}}, ValidateOnly: true})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("broken YAML validated %v", err)
	}
	if _, err := svc.PutSchedulerConfig(ctx, bearer(&v1.PutSchedulerConfigRequest{Config: &v1.SchedulerConfig{Name: "mine", Spec: &v1.SchedulerConfigSpec{Yaml: config.DefaultConfig}}})); err != nil {
		t.Fatal(err)
	}
	stored, err := svc.ListSchedulerConfigs(ctx, bearer(&v1.ListSchedulerConfigsRequest{}))
	if err != nil || len(stored.Msg.GetConfigs()) != len(before.Msg.GetConfigs())+1 {
		t.Fatalf("put stored the config %v", err)
	}
	if _, err := svc.DeleteSchedulerConfig(ctx, bearer(&v1.DeleteSchedulerConfigRequest{Name: pools.SchedulerDefault})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("a built in config was deleted %v", err)
	}
	if _, err := svc.DeleteSchedulerConfig(ctx, bearer(&v1.DeleteSchedulerConfigRequest{Name: "mine"})); err != nil {
		t.Fatal(err)
	}
}

// Traces list and read for the credential that sent them, a caller without one is refused, and status counts the route
func TestGatewayServiceTraces(t *testing.T) {
	svc, _, gw, _ := gatewayHarness(t)
	ctx := context.Background()
	srv := httptest.NewServer(gw.Handler())
	t.Cleanup(srv.Close)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/chat/completions", strings.NewReader(`{"model":"chat","messages":[{"role":"user","content":"hi"}],"max_tokens":4}`))
	req.Header.Set("Authorization", "Bearer daemon-token")
	req.Header.Set("Content-Type", "application/json")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	id := resp.Header.Get(gateway.TraceHeader)
	if resp.StatusCode != http.StatusOK || id == "" {
		t.Fatalf("chat %d trace %q", resp.StatusCode, id)
	}
	if _, err := svc.ListTraces(ctx, connect.NewRequest(&v1.ListTracesRequest{})); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("traces without a credential %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		list, err := svc.ListTraces(ctx, bearer(&v1.ListTracesRequest{Route: "chat"}))
		if err != nil {
			t.Fatal(err)
		}
		if len(list.Msg.GetTraces()) == 1 && list.Msg.GetTraces()[0].GetFinishedAt() != nil {
			tr := list.Msg.GetTraces()[0]
			if tr.GetId() != id || tr.GetEndpoint() == "" || tr.GetPoolId() != "vllm/hf:stub:q4" || tr.GetRequest() != "" {
				t.Fatalf("listed trace %v", tr)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("trace never listed finished: %v", list.Msg.GetTraces())
		}
		time.Sleep(20 * time.Millisecond)
	}
	got, err := svc.GetTrace(ctx, bearer(&v1.GetTraceRequest{Id: id}))
	if err != nil || got.Msg.GetTrace().GetRequest() == "" {
		t.Fatalf("trace with bodies %v %v", got, err)
	}
	if _, err := svc.GetTrace(ctx, bearer(&v1.GetTraceRequest{Id: "nope"})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("missing trace %v", err)
	}
	status, err := svc.GetGatewayStatus(ctx, bearer(&v1.GetGatewayStatusRequest{}))
	if err != nil || status.Msg.GetStatus().GetRequests() != 1 || len(status.Msg.GetStatus().GetPools()) != 1 {
		t.Fatalf("status %v %v", status, err)
	}
	for _, r := range status.Msg.GetStatus().GetRoutes() {
		if r.GetName() == "chat" && r.GetStatus().GetRequests() != 1 {
			t.Fatalf("route counters %v", r.GetStatus())
		}
	}
	if gw.Dispatched("inst-1") != 1 || gw.InFlight("inst-1") != 0 {
		t.Fatalf("dispatched %d in flight %d", gw.Dispatched("inst-1"), gw.InFlight("inst-1"))
	}
}

// Imports the mesh tracks, changed in place by a test
type fakeImports struct {
	mu   sync.Mutex
	list []*v1.PoolImport
}

func (f *fakeImports) Imports() []*v1.PoolImport {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*v1.PoolImport(nil), f.list...)
}

func (f *fakeImports) Import(id string) (*v1.PoolImport, bool) {
	for _, imp := range f.Imports() {
		if imp.GetId() == id {
			return imp, true
		}
	}
	return nil, false
}

func (f *fakeImports) set(list ...*v1.PoolImport) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.list = list
}
