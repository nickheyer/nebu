package pools

import (
	"context"
	"errors"
	"testing"

	"github.com/nickheyer/nebu/pkg/datalayer"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/scheduling/config"
	"github.com/nickheyer/nebu/pkg/scheduling/registry"
	"google.golang.org/protobuf/proto"
)

func TestUserPoolsPersist(t *testing.T) {
	ctx := context.Background()
	m, d, bus := newManager(t)
	spec := &v1.PoolSpec{
		Model:           "external-llama",
		RuntimeId:       "vllm",
		TargetPorts:     []uint32{9000},
		FailureMode:     v1.FailureMode_FAILURE_MODE_FAIL_OPEN,
		Selector:        map[string]string{"zone": "a"},
		Concurrency:     8,
		StaticEndpoints: []*v1.StaticEndpoint{{Address: "10.1.0.1:9000", Role: datalayer.RolePrefill, Rank: 1}, {Address: "10.1.0.2:9000"}},
	}
	p, err := m.PutPool(ctx, "ext", spec)
	if err != nil {
		t.Fatal(err)
	}
	if p.GetOwnerKind() != "" || p.GetStatus().GetReadyEndpoints() != 2 || p.GetSpec().GetFailureMode() != v1.FailureMode_FAILURE_MODE_FAIL_OPEN {
		t.Fatalf("user pool %v", p)
	}
	if p.GetSpec().GetMetrics().GetQueuedRequests() != "vllm:num_requests_waiting" || p.GetSpec().GetSchedulerConfig() != SchedulerDefault {
		t.Fatalf("user pool metrics from its runtime %v", p.GetSpec())
	}
	ep, ok := m.Store().Endpoint("ext/10.1.0.1:9000")
	if !ok || !ep.Ready() || ep.Labels()["zone"] != "a" || ep.Labels()[datalayer.LabelRole] != datalayer.RolePrefill || ep.Labels()[datalayer.LabelRank] != "1" {
		t.Fatalf("static endpoint %v", ep)
	}
	if n, _ := datalayer.Attribute[int](ep, datalayer.CollectorCapacity); n != 8 {
		t.Fatalf("static capacity %d", n)
	}
	bad := map[string]*v1.PoolSpec{
		"no model":       {TargetPorts: []uint32{1}},
		"no ports":       {Model: "m"},
		"nine ports":     {Model: "m", TargetPorts: []uint32{1, 2, 3, 4, 5, 6, 7, 8, 9}},
		"port zero":      {Model: "m", TargetPorts: []uint32{0}},
		"bad address":    {Model: "m", TargetPorts: []uint32{1}, StaticEndpoints: []*v1.StaticEndpoint{{Address: "nope"}}},
		"untargeted":     {Model: "m", TargetPorts: []uint32{1}, StaticEndpoints: []*v1.StaticEndpoint{{Address: "h:2"}}},
		"listed twice":   {Model: "m", TargetPorts: []uint32{1}, StaticEndpoints: []*v1.StaticEndpoint{{Address: "h:1"}, {Address: "h:1"}}},
		"bad enum":       {Model: "m", TargetPorts: []uint32{1}, AppProtocol: 42},
		"export unnamed": {Model: "m", TargetPorts: []uint32{1}, Export: proto.Bool(true)},
	}
	for name, s := range bad {
		if _, err := m.PutPool(ctx, "bad", s); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if _, err := m.PutRoute(ctx, "ext-route", &v1.RouteSpec{Rules: []*v1.RewriteRule{rewrite("ext", to("ext", "", 1))}}); err != nil {
		t.Fatal(err)
	}
	inst := OwnerRef{Kind: OwnerInstance, ID: "i1"}
	if err := m.Publish(ctx, inst, membership("vllm"), []Published{published("i1", 8001, true)}); err != nil {
		t.Fatal(err)
	}
	derived := string(PoolIDOf(llama, "vllm", ""))
	if _, err := m.DeletePool(ctx, derived); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete of a derived pool without a user spec: %v", err)
	}
	if _, err := m.PutPool(ctx, derived, &v1.PoolSpec{Model: "other", TargetPorts: []uint32{1}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("override naming another model: %v", err)
	}
	override, err := m.PutPool(ctx, derived, &v1.PoolSpec{Model: llama.String(), TargetPorts: []uint32{8001}, FailureMode: v1.FailureMode_FAILURE_MODE_FAIL_OPEN, SchedulerConfig: SchedulerNoMetrics})
	if err != nil {
		t.Fatal(err)
	}
	if override.GetSpec().GetFailureMode() != v1.FailureMode_FAILURE_MODE_FAIL_OPEN || override.GetSpec().GetSchedulerConfig() != SchedulerNoMetrics || override.GetOwnerKind() != OwnerInstance {
		t.Fatalf("override %v", override)
	}
	if dl, _ := m.Store().Pool(PoolIDOf(llama, "vllm", "")); dl.FailureMode != datalayer.FailOpen {
		t.Fatalf("store pool failure mode %v", dl.FailureMode)
	}
	if _, err := m.DeletePool(ctx, derived); !errors.Is(err, ErrManaged) {
		t.Fatalf("delete of a derived pool: %v", err)
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}

	again := openManager(t, d, bus)
	p, ok = again.Pool("ext")
	if !ok || p.GetStatus().GetTotalEndpoints() != 2 || !proto.Equal(p.GetSpec().GetStaticEndpoints()[0], spec.GetStaticEndpoints()[0]) || p.GetMeta().GetGeneration() != 1 {
		t.Fatalf("restored user pool %v", p)
	}
	if r, ok := again.Route("ext-route"); !ok || r.GetStatus().GetState() != v1.RouteState_ROUTE_STATE_READY {
		t.Fatalf("restored user route %v", r)
	}
	if pool, _, err := again.Resolve(ctx, "ext"); err != nil || pool.ID != "ext" || pool.Model != "external-llama" {
		t.Fatalf("resolve to a user pool %v %v", pool, err)
	}
	if err := again.Publish(ctx, inst, membership("vllm"), []Published{published("i1", 8001, true)}); err != nil {
		t.Fatal(err)
	}
	if p, _ := again.Pool(derived); p.GetSpec().GetFailureMode() != v1.FailureMode_FAILURE_MODE_FAIL_OPEN {
		t.Fatalf("override lost across restart %v", p.GetSpec())
	}
	removed, err := again.DeletePool(ctx, "ext")
	if err != nil || removed.GetId() != "ext" {
		t.Fatalf("delete user pool %v %v", removed, err)
	}
	if _, ok := again.Pool("ext"); ok || len(again.Store().Endpoints("ext", nil)) != 0 {
		t.Fatal("deleted user pool kept")
	}
	if r, _ := again.Route("ext-route"); condition(r.GetStatus().GetConditions(), ConditionResolvedRefs).GetReason() != ReasonInvalidPoolRef {
		t.Fatalf("route to a deleted pool %v", r.GetStatus())
	}
	rows, err := d.ListPools(ctx)
	if err != nil || len(rows) != 1 || rows[0].GetId() != derived {
		t.Fatalf("stored pools %v %v", rows, err)
	}
}

func TestObjectives(t *testing.T) {
	ctx := context.Background()
	m, d, bus := newManager(t)
	if _, err := m.PutObjective(ctx, &v1.Objective{Name: "fast", Priority: 1001}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("priority out of range: %v", err)
	}
	if _, err := m.PutObjective(ctx, &v1.Objective{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unnamed objective: %v", err)
	}
	o, err := m.PutObjective(ctx, &v1.Objective{Name: "fast", Priority: 10, TtftMs: 200, TpotMs: 30})
	if err != nil || o.GetMeta().GetGeneration() != 1 {
		t.Fatalf("objective %v %v", o, err)
	}
	if o, _ := m.PutObjective(ctx, &v1.Objective{Name: "fast", Priority: 20, TtftMs: 200, TpotMs: 30}); o.GetMeta().GetGeneration() != 2 {
		t.Fatalf("changed objective generation %v", o.GetMeta())
	}
	if got, ok := m.Objective("fast"); !ok || got.GetPriority() != 20 {
		t.Fatalf("lookup %v %v", got, ok)
	}
	inst := OwnerRef{Kind: OwnerInstance, ID: "i"}
	if err := m.Publish(ctx, inst, membership("vllm"), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := m.NameRoute(ctx, inst, "chat", RouteSpec{Objective: "slow"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("route naming a missing objective: %v", err)
	}
	if _, err := m.NameRoute(ctx, inst, "chat", RouteSpec{Objective: "fast"}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.DeleteObjective(ctx, "fast"); !errors.Is(err, ErrInUse) {
		t.Fatalf("delete of a named objective: %v", err)
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	again := openManager(t, d, bus)
	if list := again.Objectives(); len(list) != 1 || list[0].GetPriority() != 20 || list[0].GetMeta().GetGeneration() != 2 {
		t.Fatalf("restored objectives %v", list)
	}
	again.DropRoutes(ctx, inst)
	if _, err := again.DeleteObjective(ctx, "fast"); err != nil {
		t.Fatal(err)
	}
	if _, ok := again.Objective("fast"); ok {
		t.Fatal("deleted objective kept")
	}
	if _, err := again.DeleteObjective(ctx, "fast"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
}

// Supplies a registry with no prefix index or latency predictor, as a gateway running without them would
type bareDeps struct{ store datalayer.Store }

func (b bareDeps) SchedulerDeps() registry.Deps { return registry.Deps{Store: b.store} }

func TestSchedulerConfigs(t *testing.T) {
	ctx := context.Background()
	m, d, bus := newManager(t)
	names := map[string]bool{}
	for _, c := range m.SchedulerConfigs() {
		names[c.GetName()] = true
		if condition(c.GetStatus().GetConditions(), ConditionAccepted).GetStatus() != v1.ConditionStatus_CONDITION_STATUS_TRUE {
			t.Fatalf("built in config %s not accepted", c.GetName())
		}
	}
	if !names[SchedulerDefault] || !names[SchedulerNoMetrics] || !names[SchedulerPD] {
		t.Fatalf("built in configs %v", names)
	}
	if c, ok := m.SchedulerConfig(SchedulerDefault); !ok || c.GetSpec().GetYaml() != config.DefaultConfig {
		t.Fatalf("default yaml %v", c)
	}
	if _, err := m.DeleteSchedulerConfig(ctx, SchedulerPD); !errors.Is(err, ErrManaged) {
		t.Fatalf("delete of a built in: %v", err)
	}
	for _, yaml := range []string{"", "plugins: [}", "profileHandler:\n  pluginRef: nobody\n"} {
		if err := m.ValidateSchedulerConfig(yaml); !errors.Is(err, ErrInvalid) {
			t.Fatalf("validate only %q: %v", yaml, err)
		}
	}
	if err := m.ValidateSchedulerConfig(config.DefaultConfig); err != nil {
		t.Fatalf("validate only default: %v", err)
	}
	if rows, err := d.ListSchedulerConfigs(ctx); err != nil || len(rows) != 0 {
		t.Fatalf("validate only stored %v %v", rows, err)
	}
	if _, err := m.PutSchedulerConfig(ctx, &v1.SchedulerConfig{Name: "broken", Spec: &v1.SchedulerConfigSpec{Yaml: "plugins: [}"}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("broken yaml: %v", err)
	}
	if _, err := m.PutSchedulerConfig(ctx, &v1.SchedulerConfig{Name: "dangling", Spec: &v1.SchedulerConfigSpec{Yaml: "profileHandler:\n  pluginRef: nobody\n"}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown handler: %v", err)
	}
	c, err := m.PutSchedulerConfig(ctx, &v1.SchedulerConfig{Name: "tenant-a", Spec: &v1.SchedulerConfigSpec{Yaml: config.DefaultNoMetricsConfig}})
	if err != nil || c.GetMeta().GetGeneration() != 1 {
		t.Fatalf("valid config %v %v", c, err)
	}
	mem := membership("vllm")
	mem.SchedulerConfig = "tenant-a"
	if err := m.Publish(ctx, OwnerRef{Kind: OwnerFormation, ID: "f"}, mem, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := m.DeleteSchedulerConfig(ctx, "tenant-a"); !errors.Is(err, ErrInUse) {
		t.Fatalf("delete of a config a pool runs: %v", err)
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}

	again := openManager(t, d, bus)
	if c, ok := again.SchedulerConfig("tenant-a"); !ok || c.GetSpec().GetYaml() != config.DefaultNoMetricsConfig {
		t.Fatalf("restored config %v", c)
	}
	if err := again.Publish(ctx, OwnerRef{Kind: OwnerInstance, ID: "i"}, membership("sglang"), nil); err != nil {
		t.Fatal(err)
	}
	again.SetSchedulerDeps(ctx, bareDeps{store: again.Store()})
	if err := again.Validate(config.DefaultConfig); err == nil {
		t.Fatal("default config loaded without a prefix index")
	}
	if err := again.Validate(config.DefaultNoMetricsConfig); err == nil {
		t.Fatal("no metrics config loaded without a prefix index")
	}
	def, _ := again.SchedulerConfig(SchedulerDefault)
	if c := condition(def.GetStatus().GetConditions(), ConditionAccepted); c.GetStatus() != v1.ConditionStatus_CONDITION_STATUS_FALSE || c.GetReason() != ReasonInvalid {
		t.Fatalf("default under bare deps %v", c)
	}
	p, _ := again.Pool(string(PoolIDOf(llama, "sglang", "")))
	if c := condition(p.GetStatus().GetConditions(), ConditionAccepted); c.GetReason() != ReasonInvalidSchedulerConfig {
		t.Fatalf("pool on an invalid config %v", c)
	}
	if _, err := again.PutSchedulerConfig(ctx, &v1.SchedulerConfig{Name: "other", Spec: &v1.SchedulerConfigSpec{Yaml: config.DefaultConfig}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("put refused by the gateway's deps: %v", err)
	}
}
