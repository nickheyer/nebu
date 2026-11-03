package db

import (
	"context"
	"testing"
	"time"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestPoolObjectiveSchedulerConfigRoundTrips(t *testing.T) {
	d, _ := open(t)
	ctx := context.Background()
	meta := &v1.ObjectMeta{Generation: 3, CreatedAt: timestamppb.New(time.Unix(10, 0)), UpdatedAt: timestamppb.New(time.Unix(20, 0))}

	pool := &v1.Pool{Id: "ext", Meta: meta, Spec: &v1.PoolSpec{
		Model: "m", RuntimeId: "vllm", TargetPorts: []uint32{9000}, FailureMode: v1.FailureMode_FAILURE_MODE_FAIL_OPEN,
		StaticEndpoints: []*v1.StaticEndpoint{{Address: "10.0.0.1:9000", Role: "prefill", Rank: 1}},
		Metrics:         &v1.MetricNames{QueuedRequests: "q", SlotsPath: "/slots"},
	}}
	if err := d.PutPool(ctx, pool); err != nil {
		t.Fatal(err)
	}
	pools, err := d.ListPools(ctx)
	if err != nil || len(pools) != 1 || !proto.Equal(pools[0], pool) {
		t.Fatalf("pool round trip\n got %v\nwant %v\nerr %v", pools, pool, err)
	}
	if ok, err := d.DeletePool(ctx, "ext"); !ok || err != nil {
		t.Fatalf("delete pool %v %v", ok, err)
	}
	if ok, _ := d.DeletePool(ctx, "ext"); ok {
		t.Fatal("second pool delete removed a row")
	}

	obj := &v1.Objective{Name: "fast", Priority: -5, TtftMs: 200, TpotMs: 30, Meta: meta}
	if err := d.PutObjective(ctx, obj); err != nil {
		t.Fatal(err)
	}
	objs, err := d.ListObjectives(ctx)
	if err != nil || len(objs) != 1 || !proto.Equal(objs[0], obj) {
		t.Fatalf("objective round trip\n got %v\nwant %v\nerr %v", objs, obj, err)
	}
	if ok, err := d.DeleteObjective(ctx, "fast"); !ok || err != nil {
		t.Fatalf("delete objective %v %v", ok, err)
	}

	cfg := &v1.SchedulerConfig{Name: "mine", Spec: &v1.SchedulerConfigSpec{Yaml: "plugins: []\n"}, Meta: meta}
	if err := d.PutSchedulerConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	cfgs, err := d.ListSchedulerConfigs(ctx)
	if err != nil || len(cfgs) != 1 || !proto.Equal(cfgs[0], cfg) {
		t.Fatalf("scheduler config round trip\n got %v\nwant %v\nerr %v", cfgs, cfg, err)
	}
	if ok, err := d.DeleteSchedulerConfig(ctx, "mine"); !ok || err != nil {
		t.Fatalf("delete scheduler config %v %v", ok, err)
	}
	if cfgs, _ := d.ListSchedulerConfigs(ctx); len(cfgs) != 0 {
		t.Fatalf("configs after delete %v", cfgs)
	}

	override := &v1.Route{Name: "chat", Meta: &v1.ObjectMeta{CreatedAt: meta.GetCreatedAt(), UpdatedAt: meta.GetUpdatedAt()}, Spec: &v1.RouteSpec{
		Policy: &v1.Policy{MaxInFlight: 4}, Profile: &v1.Profile{ToolServers: []string{"web"}}, Objective: "fast",
	}}
	if err := d.PutRouteOverride(ctx, override); err != nil {
		t.Fatal(err)
	}
	if err := d.PutRoute(ctx, &v1.Route{Name: "chat", OwnerKind: "slot", OwnerId: "s1", Spec: &v1.RouteSpec{}}); err != nil {
		t.Fatal(err)
	}
	overrides, err := d.ListRouteOverrides(ctx)
	if err != nil || len(overrides) != 1 || !proto.Equal(overrides[0], override) {
		t.Fatalf("override round trip across a route write\n got %v\nwant %v\nerr %v", overrides, override, err)
	}
	if ok, err := d.DeleteRouteOverride(ctx, "chat"); !ok || err != nil {
		t.Fatalf("delete override %v %v", ok, err)
	}
}
