package pools

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/nickheyer/nebu/pkg/datalayer"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/proto"
)

func TestPublishLifecycleAcrossTwoOwners(t *testing.T) {
	ctx := context.Background()
	m, _, _ := newManager(t)
	inst := OwnerRef{Kind: OwnerInstance, ID: "i1"}
	form := OwnerRef{Kind: OwnerFormation, ID: "f1"}
	memA := membership("vllm")
	memA.KVCapacity = 8192
	if err := m.Publish(ctx, inst, memA, []Published{published("i1", 8001, true)}); err != nil {
		t.Fatal(err)
	}
	seat := published("s1", 8002, false)
	seat.Role, seat.Rank, seat.Adapter = datalayer.RoleDecode, 2, "sql"
	if err := m.Publish(ctx, form, membership("vllm"), []Published{seat}); err != nil {
		t.Fatal(err)
	}
	id := PoolIDOf(llama, "vllm", "")
	p, ok := m.Pool(string(id))
	if !ok {
		t.Fatal("shared pool missing")
	}
	if got := p.GetSpec().GetTargetPorts(); !slices.Equal(got, []uint32{8001, 8002}) {
		t.Fatalf("target ports %v", got)
	}
	if p.GetSpec().GetModel() != llama.String() || p.GetSpec().GetFailureMode() != v1.FailureMode_FAILURE_MODE_FAIL_CLOSE || p.GetSpec().GetSchedulerConfig() != SchedulerDefault {
		t.Fatalf("spec %v", p.GetSpec())
	}
	if p.GetOwnerKind() != OwnerInstance || p.GetOwnerId() != "i1" || p.GetStatus().GetTotalEndpoints() != 2 || p.GetStatus().GetReadyEndpoints() != 1 {
		t.Fatalf("pool %v", p)
	}
	ep, ok := m.Store().Endpoint("s1/8002/sql")
	if !ok {
		t.Fatal("adapter scoped formation endpoint missing")
	}
	want := map[string]string{
		datalayer.LabelModel: llama.String(), datalayer.LabelRuntime: "vllm", datalayer.LabelNode: node, datalayer.LabelInstance: "s1",
		datalayer.LabelFormation: "f1", datalayer.LabelRole: datalayer.RoleDecode, datalayer.LabelRank: "2", datalayer.LabelAdapter: "sql",
	}
	for k, v := range want {
		if ep.Labels()[k] != v {
			t.Fatalf("label %s is %q, want %q in %v", k, ep.Labels()[k], v, ep.Labels())
		}
	}
	first, _ := m.Store().Endpoint("i1/8001")
	if first.Labels()[datalayer.LabelKVCapacity] != "8192" {
		t.Fatalf("kv capacity label %v", first.Labels())
	}
	if n, ok := datalayer.Attribute[int](first, datalayer.CollectorCapacity); !ok || n != 4 {
		t.Fatalf("capacity attribute %d %v", n, ok)
	}

	if err := m.Publish(ctx, inst, memA, []Published{published("i1", 8001, true), published("i1", 8003, true)}); err != nil {
		t.Fatal(err)
	}
	if p, _ := m.Pool(string(id)); p.GetStatus().GetTotalEndpoints() != 3 {
		t.Fatalf("republish total %d", p.GetStatus().GetTotalEndpoints())
	}
	if err := m.Publish(ctx, inst, memA, []Published{published("i1", 8003, true)}); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Store().Endpoint("i1/8001"); ok {
		t.Fatal("endpoint the owner stopped publishing kept")
	}
	if err := m.Publish(ctx, form, membership("vllm"), []Published{published("i1", 8003, true)}); !errors.Is(err, ErrConflict) {
		t.Fatalf("endpoint taken from another owner: %v", err)
	}

	m.Unpublish(ctx, inst)
	if p, ok := m.Pool(string(id)); !ok || p.GetStatus().GetTotalEndpoints() != 1 || p.GetOwnerKind() != OwnerFormation {
		t.Fatalf("pool after one owner left %v %v", p, ok)
	}
	m.Unpublish(ctx, form)
	if _, ok := m.Pool(string(id)); ok {
		t.Fatal("pool outlived its last owner")
	}
	if len(m.Store().Endpoints("", nil)) != 0 {
		t.Fatal("endpoints outlived their pool")
	}
}

func TestPublishRefusals(t *testing.T) {
	ctx := context.Background()
	m, _, _ := newManager(t)
	inst := OwnerRef{Kind: OwnerInstance, ID: "i1"}
	cases := []struct {
		name  string
		owner OwnerRef
		mem   Membership
		eps   []Published
	}{
		{"user owner", OwnerRef{Kind: OwnerUser, ID: "u"}, membership("vllm"), nil},
		{"unknown kind", OwnerRef{Kind: "robot", ID: "r"}, membership("vllm"), nil},
		{"no model", inst, Membership{Runtime: "vllm"}, nil},
		{"no runtime", inst, Membership{Model: llama}, nil},
		{"bad protocol", inst, Membership{Model: llama, Runtime: "vllm", Protocol: "smtp"}, nil},
		{"no instance", inst, membership("vllm"), []Published{{Address: datalayer.Address{Host: "h", Port: 1}}}},
		{"bad port", inst, membership("vllm"), []Published{published("i1", 0, true)}},
		{"twice", inst, membership("vllm"), []Published{published("i1", 9, true), published("i1", 9, true)}},
	}
	for _, tc := range cases {
		if err := m.Publish(ctx, tc.owner, tc.mem, tc.eps); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s: %v", tc.name, err)
		}
	}
	if err := m.Publish(ctx, inst, membership("vllm"), nil); err != nil {
		t.Fatal(err)
	}
	grpc := membership("vllm")
	grpc.Protocol = datalayer.AppProtocolGRPC
	if err := m.Publish(ctx, OwnerRef{Kind: OwnerSlot, ID: "s"}, grpc, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("protocol conflict: %v", err)
	}
}

func TestReadinessCountsAndConditions(t *testing.T) {
	ctx := context.Background()
	m, _, _ := newManager(t)
	inst := OwnerRef{Kind: OwnerInstance, ID: "i1"}
	if err := m.Publish(ctx, inst, membership("vllm"), []Published{published("i1", 8001, false), published("i2", 8002, false)}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.NameRoute(ctx, inst, "llama", RouteSpec{}); err != nil {
		t.Fatal(err)
	}
	id := string(PoolIDOf(llama, "vllm", ""))
	p, _ := m.Pool(id)
	ready := condition(p.GetStatus().GetConditions(), ConditionReady)
	if ready.GetStatus() != v1.ConditionStatus_CONDITION_STATUS_FALSE || ready.GetReason() != ReasonNoReadyEndpoints || p.GetStatus().GetReadyEndpoints() != 0 {
		t.Fatalf("unready pool %v", p.GetStatus())
	}
	if acc := condition(p.GetStatus().GetConditions(), ConditionAccepted); acc.GetStatus() != v1.ConditionStatus_CONDITION_STATUS_TRUE {
		t.Fatalf("accepted %v", acc)
	}
	parents := p.GetStatus().GetParents()
	if len(parents) != 1 || parents[0].GetControllerName() != GatewayController || parents[0].GetParentRef().GetKind() != "Gateway" || parents[0].GetParentRef().GetName() != node {
		t.Fatalf("parents %v", parents)
	}
	if r, _ := m.Route("llama"); r.GetStatus().GetState() != v1.RouteState_ROUTE_STATE_PENDING {
		t.Fatalf("route before ready %v", r.GetStatus())
	}
	m.SetReady(ctx, "i2", true)
	p, _ = m.Pool(id)
	if p.GetStatus().GetReadyEndpoints() != 1 || p.GetStatus().GetTotalEndpoints() != 2 {
		t.Fatalf("counts %d of %d", p.GetStatus().GetReadyEndpoints(), p.GetStatus().GetTotalEndpoints())
	}
	if c := condition(p.GetStatus().GetConditions(), ConditionReady); c.GetStatus() != v1.ConditionStatus_CONDITION_STATUS_TRUE || c.GetObservedGeneration() != p.GetMeta().GetGeneration() {
		t.Fatalf("ready condition %v", c)
	}
	var readyEP *v1.Endpoint
	for _, ep := range p.GetStatus().GetEndpoints() {
		if ep.GetInstanceId() == "i2" {
			readyEP = ep
		}
	}
	if readyEP == nil || !readyEP.GetReady() || !readyEP.GetHealthy() || readyEP.GetAddress() != "127.0.0.1:8002" || readyEP.GetNodeId() != node {
		t.Fatalf("rendered endpoint %v", readyEP)
	}
	r, _ := m.Route("llama")
	if r.GetStatus().GetState() != v1.RouteState_ROUTE_STATE_READY || !slices.Equal(r.GetStatus().GetPoolIds(), []string{id}) {
		t.Fatalf("route after ready %v", r.GetStatus())
	}
	for _, typ := range []string{ConditionAccepted, ConditionResolvedRefs, ConditionReady} {
		if c := condition(r.GetStatus().GetConditions(), typ); c.GetStatus() != v1.ConditionStatus_CONDITION_STATUS_TRUE {
			t.Fatalf("route condition %s %v", typ, c)
		}
	}
}

func TestSchedulerConfigFollowsMetrics(t *testing.T) {
	ctx := context.Background()
	m, _, _ := newManager(t)
	nemo := Membership{Model: llama, Runtime: "nemo", Metrics: &v1.MetricNames{QueuedRequests: "q", RunningRequests: "r", MetricsPath: "/metrics", HealthPath: "/v1/health"}}
	if err := m.Publish(ctx, OwnerRef{Kind: OwnerInstance, ID: "n"}, nemo, nil); err != nil {
		t.Fatal(err)
	}
	if p, _ := m.Pool(string(PoolIDOf(llama, "nemo", ""))); p.GetSpec().GetSchedulerConfig() != SchedulerNoMetrics {
		t.Fatalf("nemo pool without a KV source runs %q", p.GetSpec().GetSchedulerConfig())
	}
	nemo.Metrics.StaticCache = true
	nemo.Runtime = "nemo-static"
	if err := m.Publish(ctx, OwnerRef{Kind: OwnerInstance, ID: "n2"}, nemo, nil); err != nil {
		t.Fatal(err)
	}
	if p, _ := m.Pool(string(PoolIDOf(llama, "nemo-static", ""))); p.GetSpec().GetSchedulerConfig() != SchedulerDefault || !p.GetSpec().GetMetrics().GetStaticCache() {
		t.Fatalf("nemo pool with static caches runs %q", p.GetSpec().GetSchedulerConfig())
	}
	llamacpp := Membership{Model: llama, Runtime: "llamacpp", Metrics: &v1.MetricNames{QueuedRequests: "q", RunningRequests: "r", SlotsPath: "/slots", MetricsPath: "/metrics"}}
	if err := m.Publish(ctx, OwnerRef{Kind: OwnerInstance, ID: "l"}, llamacpp, nil); err != nil {
		t.Fatal(err)
	}
	if p, _ := m.Pool(string(PoolIDOf(llama, "llamacpp", ""))); p.GetSpec().GetSchedulerConfig() != SchedulerDefault || p.GetSpec().GetMetrics().GetSlotsPath() != "/slots" {
		t.Fatalf("llamacpp pool %v", p.GetSpec())
	}
	pd := membership("sglang")
	pd.SchedulerConfig = SchedulerPD
	if err := m.Publish(ctx, OwnerRef{Kind: OwnerFormation, ID: "f"}, pd, nil); err != nil {
		t.Fatal(err)
	}
	if p, _ := m.Pool(string(PoolIDOf(llama, "sglang", ""))); p.GetSpec().GetSchedulerConfig() != SchedulerPD {
		t.Fatalf("pd pool runs %q", p.GetSpec().GetSchedulerConfig())
	}
	missing := membership("trtllm")
	missing.SchedulerConfig = "tenant-a"
	if err := m.Publish(ctx, OwnerRef{Kind: OwnerFormation, ID: "g"}, missing, nil); err != nil {
		t.Fatal(err)
	}
	p, _ := m.Pool(string(PoolIDOf(llama, "trtllm", "")))
	if c := condition(p.GetStatus().GetConditions(), ConditionAccepted); c.GetStatus() != v1.ConditionStatus_CONDITION_STATUS_FALSE || c.GetReason() != ReasonUnknownSchedulerConfig {
		t.Fatalf("missing scheduler config accepted %v", c)
	}
	if c := condition(p.GetStatus().GetParents()[0].GetConditions(), ConditionResolvedRefs); c.GetStatus() != v1.ConditionStatus_CONDITION_STATUS_FALSE {
		t.Fatalf("parent resolved refs %v", c)
	}
}

func TestDrain(t *testing.T) {
	ctx := context.Background()
	m, _, _ := newManager(t)
	slot := OwnerRef{Kind: OwnerSlot, ID: "s1"}
	if err := m.Publish(ctx, slot, membership("vllm"), []Published{published("i1", 8001, true)}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.NameRoute(ctx, slot, "chat", RouteSpec{}); err != nil {
		t.Fatal(err)
	}
	m.Drain(ctx, slot)
	if r, _ := m.Route("chat"); r.GetStatus().GetState() != v1.RouteState_ROUTE_STATE_DRAINING {
		t.Fatalf("drained route %v", r.GetStatus())
	}
	if ep, _ := m.Store().Endpoint("i1/8001"); ep.Ready() {
		t.Fatal("drained endpoint still schedulable")
	}
	m.SetReady(ctx, "i1", true)
	if ep, _ := m.Store().Endpoint("i1/8001"); ep.Ready() {
		t.Fatal("readiness overrode the drain")
	}
	if _, _, err := m.Resolve(ctx, "chat"); err == nil {
		t.Fatal("draining route took a request")
	}
	if err := m.Publish(ctx, slot, membership("vllm"), []Published{published("i2", 8001, true)}); err != nil {
		t.Fatal(err)
	}
	if r, _ := m.Route("chat"); r.GetStatus().GetState() != v1.RouteState_ROUTE_STATE_READY {
		t.Fatalf("route after publish %v", r.GetStatus())
	}
	if _, _, err := m.Resolve(ctx, "chat"); err != nil {
		t.Fatal(err)
	}
}

func TestImportedOwnersAndExport(t *testing.T) {
	ctx := context.Background()
	m, _, _ := newManager(t)
	inst := OwnerRef{Kind: OwnerInstance, ID: "i1"}
	local := published("i1", 8001, true)
	local.MetricsPort, local.HealthPort = 8101, 8201
	if err := m.Publish(ctx, inst, membership("vllm"), []Published{local}); err != nil {
		t.Fatal(err)
	}
	id := string(PoolIDOf(llama, "vllm", ""))
	if _, err := m.PutPool(ctx, id, &v1.PoolSpec{Model: llama.String(), RuntimeId: "vllm", TargetPorts: []uint32{8001, 9000}, Export: proto.Bool(true)}); err != nil {
		t.Fatal(err)
	}
	remote := OwnerRef{Kind: OwnerNode, ID: "n2/" + id}
	imported := Published{InstanceID: "r1", NodeID: "n2", Address: datalayer.Address{Host: "10.0.0.2", Port: 9000}, Ready: true, MetricsPort: 9100}
	sched := membership("vllm")
	sched.SchedulerConfig = SchedulerPD
	if err := m.Publish(ctx, remote, sched, []Published{imported}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.NameRoute(ctx, remote, "remote-llama", RouteSpec{}); err != nil {
		t.Fatal(err)
	}
	p, _ := m.Pool(id)
	if p.GetStatus().GetTotalEndpoints() != 2 || p.GetOwnerKind() != OwnerInstance || p.GetSpec().GetSchedulerConfig() != SchedulerDefault {
		t.Fatalf("pool shared with an imported owner %v", p)
	}
	exported := m.Exported()
	if len(exported) != 1 || exported[0].GetId() != id {
		t.Fatalf("exported %v", exported)
	}
	eps := exported[0].GetStatus().GetEndpoints()
	if len(eps) != 1 || eps[0].GetId() != "i1/8001" || eps[0].GetMetricsPort() != 8101 || eps[0].GetHealthPort() != 8201 {
		t.Fatalf("exported endpoints %v", eps)
	}
	for _, ep := range p.GetStatus().GetEndpoints() {
		if ep.GetId() == "r1/9000" && (ep.GetMetricsPort() != 9100 || ep.GetHealthPort() != 0 || ep.GetNodeId() != "n2") {
			t.Fatalf("imported endpoint %v", ep)
		}
	}
	if r, _ := m.Route("remote-llama"); r.GetOwnerKind() != OwnerNode {
		t.Fatalf("imported route %v", r)
	}
	m.Unpublish(ctx, inst)
	if p, _ := m.Pool(id); p.GetOwnerKind() != OwnerNode || p.GetSpec().GetSchedulerConfig() != SchedulerPD {
		t.Fatalf("pool held by an imported owner alone %v", p)
	}
	m.DropRoutes(ctx, remote)
	m.Unpublish(ctx, remote)
	p, ok := m.Pool(id)
	if !ok || p.GetStatus().GetTotalEndpoints() != 0 || p.GetOwnerKind() != "" {
		t.Fatalf("user override after every owner left %v %v", p, ok)
	}
	if len(m.Exported()[0].GetStatus().GetEndpoints()) != 0 {
		t.Fatal("export kept endpoints of departed owners")
	}
}
