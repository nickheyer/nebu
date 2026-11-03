package cli

import (
	"flag"
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/encoding/prototext"
	"google.golang.org/protobuf/proto"
)

// Fails the test when two messages differ, printing both
func sameMessage(t *testing.T, what string, got, want proto.Message) {
	t.Helper()
	if !proto.Equal(got, want) {
		t.Fatalf("%s\n got  %s\n want %s", what, prototext.Format(got), prototext.Format(want))
	}
}

func TestParseTarget(t *testing.T) {
	cases := []struct {
		in   string
		want *v1.RewriteTarget
	}{
		{"poolA", &v1.RewriteTarget{PoolId: "poolA", Weight: 1}},
		{"poolA:3", &v1.RewriteTarget{PoolId: "poolA", Weight: 3}},
		{"poolA=llama:3", &v1.RewriteTarget{PoolId: "poolA", Model: "llama", Weight: 3}},
		{" poolA=llama ", &v1.RewriteTarget{PoolId: "poolA", Model: "llama", Weight: 1}},
		{"vllm/hf:org/repo:Q4_K_M", &v1.RewriteTarget{PoolId: "vllm/hf:org/repo:Q4_K_M", Weight: 1}},
		{"vllm/hf:org/repo:Q4_K_M=served:7", &v1.RewriteTarget{PoolId: "vllm/hf:org/repo:Q4_K_M", Model: "served", Weight: 7}},
		{"p=qwen3:8b", &v1.RewriteTarget{PoolId: "p", Model: "qwen3:8b", Weight: 1}},
		{"p=qwen3:8b:2", &v1.RewriteTarget{PoolId: "p", Model: "qwen3:8b", Weight: 2}},
		{"p:1000000", &v1.RewriteTarget{PoolId: "p", Weight: 1000000}},
	}
	for _, c := range cases {
		got, err := parseTarget(c.in)
		if err != nil {
			t.Fatalf("%q: %v", c.in, err)
		}
		sameMessage(t, c.in, got, c.want)
	}
	for _, bad := range []string{"", ":3", "p:0", "p:1000001", "p:99999999999", "p=:2", "=m", "p="} {
		if got, err := parseTarget(bad); err == nil {
			t.Fatalf("%q parsed to %v, want an error", bad, got)
		}
	}
}

func TestParseScopes(t *testing.T) {
	cases := []struct {
		in   string
		want []v1.Scope
	}{
		{"inference", []v1.Scope{v1.Scope_SCOPE_INFERENCE}},
		{"inference,read", []v1.Scope{v1.Scope_SCOPE_INFERENCE, v1.Scope_SCOPE_READ}},
		{" operate , ADMIN ,", []v1.Scope{v1.Scope_SCOPE_OPERATE, v1.Scope_SCOPE_ADMIN}},
		{"SCOPE_READ", []v1.Scope{v1.Scope_SCOPE_READ}},
	}
	for _, c := range cases {
		got, err := parseScopes(c.in)
		if err != nil || !slices.Equal(got, c.want) {
			t.Fatalf("%q: %v %v, want %v", c.in, got, err, c.want)
		}
	}
	for _, bad := range []string{"", " , ", "write", "unspecified", "SCOPE_UNSPECIFIED", "inference,bogus"} {
		if got, err := parseScopes(bad); err == nil {
			t.Fatalf("%q parsed to %v, want an error", bad, got)
		}
	}
}

func TestParseRole(t *testing.T) {
	cases := map[string]v1.Role{
		"":          v1.Role_ROLE_UNSPECIFIED,
		"admin":     v1.Role_ROLE_ADMIN,
		"Operator":  v1.Role_ROLE_OPERATOR,
		"inference": v1.Role_ROLE_INFERENCE,
	}
	for in, want := range cases {
		if got, err := parseRole(in); err != nil || got != want {
			t.Fatalf("%q: %v %v, want %v", in, got, err, want)
		}
	}
	for _, bad := range []string{"root", "unspecified", "read"} {
		if got, err := parseRole(bad); err == nil {
			t.Fatalf("%q parsed to %v, want an error", bad, got)
		}
	}
}

func TestDurations(t *testing.T) {
	cases := []struct {
		parse func(string) (uint32, error)
		in    string
		want  uint32
		ok    bool
	}{
		{parseSeconds, "", 0, true},
		{parseSeconds, "0", 0, true},
		{parseSeconds, "30m", 1800, true},
		{parseSeconds, "2h", 7200, true},
		{parseSeconds, "200000h", 720000000, true},
		{parseSeconds, "2000000h", 0, false},
		{parseSeconds, "1500ms", 0, false},
		{parseSeconds, "-5s", 0, false},
		{parseSeconds, "soon", 0, false},
		{parseMillis, "1500ms", 1500, true},
		{parseMillis, "60s", 60000, true},
		{parseMillis, "1.5ms", 0, false},
		{parseMillis, "-1s", 0, false},
		{parseMillis, "1300h", 0, false},
	}
	for _, c := range cases {
		got, err := c.parse(c.in)
		if c.ok && (err != nil || got != c.want) {
			t.Fatalf("%q: %d %v, want %d", c.in, got, err, c.want)
		}
		if !c.ok && err == nil {
			t.Fatalf("%q parsed to %d, want an error", c.in, got)
		}
	}
}

func TestPoolFlags(t *testing.T) {
	base := func() *v1.PoolSpec {
		return &v1.PoolSpec{Model: "llama", RuntimeId: "vllm", TargetPorts: []uint32{8000}, FailureMode: v1.FailureMode_FAILURE_MODE_FAIL_CLOSE, Selector: map[string]string{"old": "x"}}
	}
	cases := []struct {
		name string
		args []string
		want *v1.PoolSpec
	}{
		{"nothing given keeps the base", nil, base()},
		{"every flag", []string{
			"--model", "qwen", "--runtime", "sglang", "--port", "8001", "--port", "8002", "--protocol", "h2c",
			"--failure-mode", "fail-open", "--scheduler-config", "default", "--static", "10.0.0.1:9000,role=prefill,rank=1,kv-capacity=65536",
			"--static", "[::1]:9001", "--export", "--concurrency", "4", "--selector", "tier=gpu", "--selector", "zone=a",
		}, &v1.PoolSpec{
			Model: "qwen", RuntimeId: "sglang", TargetPorts: []uint32{8001, 8002}, AppProtocol: v1.AppProtocol_APP_PROTOCOL_H2C,
			FailureMode: v1.FailureMode_FAILURE_MODE_FAIL_OPEN, SchedulerConfig: "default",
			StaticEndpoints: []*v1.StaticEndpoint{{Address: "10.0.0.1:9000", Role: "prefill", Rank: 1, KvCapacity: 65536}, {Address: "[::1]:9001"}},
			Export:          proto.Bool(true), Concurrency: 4, Selector: map[string]string{"tier": "gpu", "zone": "a"},
		}},
		{"one flag changes one field", []string{"--protocol", "GRPC"}, &v1.PoolSpec{Model: "llama", RuntimeId: "vllm", TargetPorts: []uint32{8000}, AppProtocol: v1.AppProtocol_APP_PROTOCOL_GRPC, FailureMode: v1.FailureMode_FAILURE_MODE_FAIL_CLOSE, Selector: map[string]string{"old": "x"}}},
		{"export can be turned off", []string{"--export=false"}, &v1.PoolSpec{Model: "llama", RuntimeId: "vllm", TargetPorts: []uint32{8000}, FailureMode: v1.FailureMode_FAILURE_MODE_FAIL_CLOSE, Selector: map[string]string{"old": "x"}, Export: proto.Bool(false)}},
	}
	for _, c := range cases {
		fs := flag.NewFlagSet("pools put", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		apply := poolFlags(fs)
		if err := fs.Parse(c.args); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		got := base()
		if err := apply(got); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		sameMessage(t, c.name, got, c.want)
	}
	for _, bad := range [][]string{
		{"--port", "0"}, {"--port", "70000"}, {"--port", "http"}, {"--protocol", "tcp"}, {"--protocol", "unspecified"},
		{"--failure-mode", "fail-soft"}, {"--static", "nohost"}, {"--static", "h:1,rank=x"}, {"--static", "h:1,weight=2"},
		{"--static", "h:1,kv-capacity=0"}, {"--static", "h:1,kv-capacity=lots"}, {"--selector", "tier"},
	} {
		fs := flag.NewFlagSet("pools put", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		apply := poolFlags(fs)
		if err := fs.Parse(bad); err != nil {
			t.Fatalf("%v: %v", bad, err)
		}
		if err := apply(base()); err == nil {
			t.Fatalf("%v applied, want an error", bad)
		}
	}
	fs := flag.NewFlagSet("pools put", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	poolFlags(fs)
	if err := fs.Parse([]string{"--concurrency", "-1"}); err == nil {
		t.Fatal("a negative concurrency parsed")
	}
}

func TestReadMessage(t *testing.T) {
	dir := t.TempDir()
	write := func(name, text string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	pool := write("pool.yaml", `
model: llama
runtime_id: vllm
targetPorts: [8001, 8002]
app_protocol: h2c
failure_mode: FAILURE_MODE_FAIL_OPEN
scheduler_config: default
selector:
  tier: gpu
metrics:
  queued_requests: vllm:num_requests_waiting
static_endpoints:
  - address: 10.0.0.1:9000
    role: decode
    rank: 2
export: true
concurrency: 8
`)
	got := &v1.PoolSpec{}
	if err := readMessage(pool, got); err != nil {
		t.Fatal(err)
	}
	sameMessage(t, "pool spec", got, &v1.PoolSpec{
		Model: "llama", RuntimeId: "vllm", TargetPorts: []uint32{8001, 8002}, AppProtocol: v1.AppProtocol_APP_PROTOCOL_H2C,
		FailureMode: v1.FailureMode_FAILURE_MODE_FAIL_OPEN, SchedulerConfig: "default", Selector: map[string]string{"tier": "gpu"},
		Metrics:         &v1.MetricNames{QueuedRequests: "vllm:num_requests_waiting"},
		StaticEndpoints: []*v1.StaticEndpoint{{Address: "10.0.0.1:9000", Role: "decode", Rank: 2}}, Export: proto.Bool(true), Concurrency: 8,
	})
	route := write("route.yaml", `
rules:
  - match: chat
    targets:
      - {pool_id: a, weight: 3}
      - {pool_id: b, model: small, weight: 1}
policy: {max_in_flight: 4, request_timeout_ms: 30000}
profile: {tool_servers: [fs], mutating_tools: true}
objective: interactive
`)
	gotRoute := &v1.RouteSpec{}
	if err := readMessage(route, gotRoute); err != nil {
		t.Fatal(err)
	}
	sameMessage(t, "route spec", gotRoute, &v1.RouteSpec{
		Rules:     []*v1.RewriteRule{{Match: "chat", Targets: []*v1.RewriteTarget{{PoolId: "a", Weight: 3}, {PoolId: "b", Model: "small", Weight: 1}}}},
		Policy:    &v1.Policy{MaxInFlight: 4, RequestTimeoutMs: 30000},
		Profile:   &v1.Profile{ToolServers: []string{"fs"}, MutatingTools: true},
		Objective: "interactive",
	})
	unspecified := &v1.PoolSpec{}
	if err := readMessage(write("zero.yaml", "model: m\napp_protocol: APP_PROTOCOL_UNSPECIFIED\n"), unspecified); err != nil || unspecified.GetAppProtocol() != v1.AppProtocol_APP_PROTOCOL_UNSPECIFIED {
		t.Fatalf("full unspecified name %v %v", unspecified, err)
	}
	for name, text := range map[string]string{
		"unknown-field.yaml": "model: m\nports: [1]\n",
		"unknown-enum.yaml":  "model: m\napp_protocol: tcp\n",
		"list.yaml":          "- model: m\n",
		"bad-yaml.yaml":      "model: [m\n",
	} {
		if err := readMessage(write(name, text), &v1.PoolSpec{}); err == nil {
			t.Fatalf("%s read, want an error", name)
		}
	}
	if err := readMessage(filepath.Join(dir, "missing.yaml"), &v1.PoolSpec{}); err == nil {
		t.Fatal("a missing file read")
	}
}

func TestUpsertRule(t *testing.T) {
	catchAll := &v1.RewriteRule{Targets: []*v1.RewriteTarget{{PoolId: "a", Weight: 1}}}
	chat := &v1.RewriteRule{Match: "chat", Targets: []*v1.RewriteTarget{{PoolId: "b", Weight: 1}}}
	rules := []*v1.RewriteRule{catchAll, chat}
	next := &v1.RewriteRule{Targets: []*v1.RewriteTarget{{PoolId: "c", Weight: 2}}}
	got := upsertRule(rules, next)
	if len(got) != 2 || got[0] != next || got[1] != chat || rules[0] != catchAll {
		t.Fatalf("replacing the catch-all gave %v, the input %v", got, rules)
	}
	extra := &v1.RewriteRule{Match: "code", Targets: []*v1.RewriteTarget{{PoolId: "d", Weight: 1}}}
	if got := upsertRule(rules, extra); len(got) != 3 || got[2] != extra {
		t.Fatalf("appending a new match gave %v", got)
	}
}

func TestBaseFollowsTLSDisabled(t *testing.T) {
	cases := []struct {
		tls  *v1.Tls
		addr string
		want string
	}{
		{&v1.Tls{CertFile: "/d/tls.crt", KeyFile: "/d/tls.key"}, "127.0.0.1:8484", "https://127.0.0.1:8484"},
		{&v1.Tls{}, "127.0.0.1:8484", "https://127.0.0.1:8484"},
		{&v1.Tls{Disabled: true}, "127.0.0.1:8484", "http://127.0.0.1:8484"},
		{&v1.Tls{Disabled: true, CertFile: "/d/tls.crt"}, "host:1", "http://host:1"},
		{&v1.Tls{Disabled: true}, "https://host:1", "https://host:1"},
	}
	for _, c := range cases {
		e := &env{cfg: &v1.Config{Tls: c.tls}}
		if got := e.base(c.addr); got != c.want {
			t.Fatalf("%v %s: %s, want %s", c.tls, c.addr, got, c.want)
		}
	}
}

func TestRunRouteFlags(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want *v1.RunRequest
	}{
		{"nothing given inherits everything", nil, &v1.RunRequest{}},
		{"every flag", []string{"--max-in-flight", "4", "--rps", "1.5", "--burst", "3", "--timeout", "90s", "--upstream-timeout", "10m",
			"--tool-server", "fs", "--tool-server", "peer/web", "--mutating-tools", "--idle-unload", "30m"},
			&v1.RunRequest{
				Policy:            &v1.Policy{MaxInFlight: 4, RequestsPerSecond: 1.5, Burst: 3, RequestTimeoutMs: 90000, UpstreamTimeoutMs: 600000},
				RouteProfile:      &v1.Profile{ToolServers: []string{"fs", "peer/web"}, MutatingTools: true},
				IdleUnloadSeconds: 1800,
			}},
		{"idle unload alone", []string{"--idle-unload", "2h"}, &v1.RunRequest{IdleUnloadSeconds: 7200}},
	}
	for _, c := range cases {
		fs := flag.NewFlagSet("run", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		read := runRouteFlags(fs)
		if err := fs.Parse(c.args); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		got := &v1.RunRequest{}
		if err := read(got); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		sameMessage(t, c.name, got, c.want)
	}
	for _, bad := range [][]string{{"--idle-unload", "90ms"}, {"--idle-unload", "-1m"}, {"--mutating-tools"}, {"--timeout", "soon"}, {"--max-in-flight", "-2"}} {
		fs := flag.NewFlagSet("run", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		read := runRouteFlags(fs)
		if err := fs.Parse(bad); err == nil {
			if err := read(&v1.RunRequest{}); err == nil {
				t.Fatalf("%v applied, want an error", bad)
			}
		}
	}
}
