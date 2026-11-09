package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/nickheyer/nebu/internal/auth"
	"github.com/nickheyer/nebu/internal/db"
	"github.com/nickheyer/nebu/internal/gateway"
	"github.com/nickheyer/nebu/internal/pools"
	"github.com/nickheyer/nebu/internal/tools"
	"github.com/nickheyer/nebu/pkg/chattemplate"
	"github.com/nickheyer/nebu/pkg/events"
	"github.com/nickheyer/nebu/pkg/flowcontrol"
	"github.com/nickheyer/nebu/pkg/health"
	"github.com/nickheyer/nebu/pkg/metrics"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/scheduling/config"
	"github.com/nickheyer/nebu/pkg/telemetry"
	"github.com/prometheus/client_golang/prometheus"
)

type openAI struct{}

func (openAI) API(string) v1.ApiFlavor { return v1.ApiFlavor_API_FLAVOR_OPENAI }
func (openAI) Handoff(string) string   { return "" }

type noInstances struct{}

func (noInstances) ToolSupport(string) v1.ToolSupport { return v1.ToolSupport_TOOL_SUPPORT_UNSPECIFIED }
func (noInstances) Modes(string) []string             { return nil }
func (noInstances) LoadAdapter(context.Context, string, string, string) error {
	return errors.New("no adapter loads in this test")
}
func (noInstances) TokenizerFile(string) (string, error) {
	return "", errors.New("no tokenizer files in this test")
}
func (noInstances) Template(string) (*chattemplate.Template, error) {
	return nil, errors.New("no templates in this test")
}

// An MCP client session on a server whose pool tools run over a real pools manager and gateway
func poolSession(t *testing.T) *mcp.ClientSession {
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
	tp, shutdown, err := telemetry.Setup(ctx, telemetry.Config{ServiceName: "mcp-test", SampleRatio: 1})
	if err != nil {
		t.Fatal(err)
	}
	mreg, err := metrics.New(prometheus.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}
	guard := auth.NewGuard("", nil, nil)
	gw, err := gateway.New(gateway.Deps{
		Pools: mgr, Resolver: mgr, Manager: mgr, Store: mgr.Store(), DB: store, Events: bus, Guard: guard,
		Tools: tools.New(log, bus), Runtimes: openAI{}, Instances: noInstances{},
		Metrics: mreg, Telemetry: tp, Health: health.NewRegistry(), Logger: log,
		Config: gateway.Config{
			Policy: &v1.Policy{}, OutputTokens: 64,
			Flow:       flowcontrol.Config{QueueCapacity: 16, QueueTimeout: time.Second, SheddableBelow: -1, Tick: 10 * time.Millisecond},
			Saturation: flowcontrol.Thresholds{QueueDepth: 8, KVUtilization: 0.9}, Staleness: time.Second,
			Retry: gateway.Retry{Shed: time.Second, Unavailable: time.Second}, TraceRetention: time.Hour, TraceRing: 16,
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
	server := New(Deps{Pools: mgr, Gateway: gw, Guard: guard, Events: bus, Version: "test", Log: log})
	st, ct := mcp.NewInMemoryTransports()
	ss, err := server.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "1"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cs.Close()
		ss.Close()
		cancel()
		<-done
		mgr.Close()
		shutdown(context.Background())
		store.Close()
	})
	return cs
}

// Calls a tool and decodes its JSON answer, failing on a tool error unless wantErr names part of it
func call(t *testing.T, cs *mcp.ClientSession, name string, args any, wantErr string) map[string]any {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	text := ""
	if len(res.Content) > 0 {
		text = res.Content[0].(*mcp.TextContent).Text
	}
	if wantErr != "" {
		if !res.IsError || !strings.Contains(text, wantErr) {
			t.Fatalf("%s answered %q, want an error naming %q", name, text, wantErr)
		}
		return nil
	}
	if res.IsError {
		t.Fatalf("%s failed: %s", name, text)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("%s answered %q: %v", name, text, err)
	}
	return out
}

// Pools, weighted routes, objectives and scheduler configurations are listed, put, read and deleted through the tools and resources
func TestPoolTools(t *testing.T) {
	cs := poolSession(t)
	pool := call(t, cs, "put_pool", map[string]any{"id": "static", "model": "ext", "target_ports": []int{8000}, "app_protocol": "h2c", "failure_mode": "fail-open", "static_endpoints": []map[string]any{{"address": "127.0.0.1:8000", "kv_capacity": 65536}}}, "")
	spec := pool["pool"].(map[string]any)["spec"].(map[string]any)
	if spec["app_protocol"] != "APP_PROTOCOL_H2C" || spec["failure_mode"] != "FAILURE_MODE_FAIL_OPEN" || spec["static_endpoints"].([]any)[0].(map[string]any)["kv_capacity"] != float64(65536) {
		t.Fatalf("pool spec %v", spec)
	}
	call(t, cs, "put_pool", map[string]any{"id": "bad", "model": "ext", "target_ports": []int{8000}, "app_protocol": "carrier-pigeon"}, "not http, h2c, or grpc")
	if got := call(t, cs, "list_pools", map[string]any{}, ""); len(got["pools"].([]any)) != 1 {
		t.Fatalf("pools %v", got)
	}
	call(t, cs, "put_objective", map[string]any{"name": "fast", "priority": 3, "ttft_ms": 250}, "")
	call(t, cs, "put_route", map[string]any{"name": "split", "objective": "fast", "rules": []map[string]any{{"targets": []map[string]any{{"pool_id": "static", "weight": 2}}}}}, "")
	// Without rules the route keeps its own, so policy changes alone
	call(t, cs, "put_route", map[string]any{"name": "split", "objective": "fast", "policy": map[string]any{"max_in_flight": 4}}, "")
	rt := call(t, cs, "get_route", map[string]any{"name": "split"}, "")["route"].(map[string]any)["spec"].(map[string]any)
	if rt["policy"].(map[string]any)["max_in_flight"] != float64(4) || len(rt["rules"].([]any)) != 1 || rt["objective"] != "fast" {
		t.Fatalf("route spec %v", rt)
	}
	call(t, cs, "put_route", map[string]any{"name": "fresh"}, "names no rules and has none to keep")
	call(t, cs, "put_route", map[string]any{"name": "neg", "rules": []map[string]any{{"targets": []map[string]any{{"pool_id": "static", "weight": 1}}}}, "policy": map[string]any{"requests_per_second": -2}}, "requests_per_second")
	call(t, cs, "put_pool", map[string]any{"id": "ports", "model": "ext", "target_ports": []int{70000}}, "target_ports")
	call(t, cs, "put_objective", map[string]any{"name": "loud", "priority": 5000}, "priority")
	call(t, cs, "delete_objective", map[string]any{"name": "fast"}, "names objective fast")
	checked := call(t, cs, "put_scheduler_config", map[string]any{"name": "mine", "yaml": config.DefaultConfig, "validate_only": true}, "")
	status, _ := checked["config"].(map[string]any)["status"].(map[string]any)
	if conds, _ := status["conditions"].([]any); len(conds) != 1 {
		t.Fatalf("validate only %v", checked)
	}
	before := len(call(t, cs, "list_scheduler_configs", map[string]any{}, "")["configs"].([]any))
	call(t, cs, "put_scheduler_config", map[string]any{"name": "mine", "yaml": config.DefaultConfig}, "")
	if after := len(call(t, cs, "list_scheduler_configs", map[string]any{}, "")["configs"].([]any)); after != before+1 {
		t.Fatalf("configs %d then %d", before, after)
	}
	call(t, cs, "delete_scheduler_config", map[string]any{"name": "mine"}, "")
	for _, uri := range []string{"nebu://pools", "nebu://objectives", "nebu://scheduler-configs", "nebu://pools/static", "nebu://routes/split"} {
		res, err := cs.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: uri})
		if err != nil || len(res.Contents) != 1 || !json.Valid([]byte(res.Contents[0].Text)) {
			t.Fatalf("%s: %v %v", uri, res, err)
		}
	}
	if _, err := cs.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: "nebu://pools/nope"}); err == nil {
		t.Fatal("a missing pool resource was read")
	}
	call(t, cs, "delete_route", map[string]any{"name": "split"}, "")
	call(t, cs, "delete_objective", map[string]any{"name": "fast"}, "")
	call(t, cs, "delete_pool", map[string]any{"id": "static"}, "")
	if got := call(t, cs, "list_traces", map[string]any{}, ""); len(got) != 0 {
		t.Fatalf("no request was sent, so no trace %v", got)
	}
}
