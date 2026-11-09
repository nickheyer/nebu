package db

import (
	"context"
	"testing"
	"time"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestBuildsRoundTrip(t *testing.T) {
	d, _ := open(t)
	ctx := context.Background()
	b := &v1.Build{Id: "b1", RecipeId: "r", RuntimeId: "rt", Variant: "cpu", Ref: "v1", Commit: "c", Sandbox: v1.SandboxKind_SANDBOX_KIND_OCI, Image: "img", Dir: "/d", Binary: "/d/out/bin", InstallId: "i1", TaskId: "t1", State: v1.BuildState_BUILD_STATE_RUNNING, Vars: map[string]string{"z": "1", "a": "2"}, Facts: map[string]string{"f": "x"}, Patches: []string{"p2", "p1"}, CreatedAt: timestamppb.New(time.Unix(10, 0))}
	if err := d.PutBuild(ctx, b); err != nil {
		t.Fatal(err)
	}
	older := proto.Clone(b).(*v1.Build)
	older.Id, older.CreatedAt, older.RuntimeId = "b0", timestamppb.New(time.Unix(5, 0)), "other"
	d.PutBuild(ctx, older)
	got, err := d.GetBuild(ctx, "b1")
	if err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(got, b) {
		t.Fatalf("round trip\n got %v\nwant %v", got, b)
	}
	list, _ := d.ListBuilds(ctx, "")
	if len(list) != 2 || list[0].GetId() != "b1" {
		t.Fatalf("list %v", list)
	}
	if list, _ := d.ListBuilds(ctx, "rt"); len(list) != 1 {
		t.Fatal("filter by runtime")
	}
	n, err := d.FailUnfinishedBuilds(ctx)
	if err != nil || n != 2 {
		t.Fatalf("fail unfinished %d %v", n, err)
	}
	got, _ = d.GetBuild(ctx, "b1")
	if got.GetState() != v1.BuildState_BUILD_STATE_FAILED || got.GetError() != RestartNote || got.GetFinishedAt() == nil {
		t.Fatalf("after fail %v", got)
	}
	if ok, _ := d.DeleteBuild(ctx, "b1"); !ok {
		t.Fatal("delete")
	}
	if _, err := d.GetBuild(ctx, "b1"); !IsNotFound(err) {
		t.Fatalf("want not found, got %v", err)
	}
	in := &v1.Install{Id: "i1", RuntimeId: "rt", Kind: v1.InstallKind_INSTALL_KIND_BUILT, Path: "/p", Dir: "/d", BuildId: "b0", CreatedAt: timestamppb.Now()}
	if err := d.PutInstall(ctx, in); err != nil {
		t.Fatal(err)
	}
	if back, _ := d.GetInstall(ctx, "i1"); back.GetBuildId() != "b0" {
		t.Fatal("install build id")
	}
}

func TestSlotsRoutesRoundTrip(t *testing.T) {
	d, _ := open(t)
	ctx := context.Background()
	s := &v1.Slot{Id: "s1", Name: "main", Position: 2, Placement: v1.Placement_PLACEMENT_HOST, DeviceIds: []string{"g1", "g0"}, MemoryBytes: 1 << 33, RuntimeId: "rt", Params: map[string]string{"n_ctx": "1"}, Args: `--alias {{.Name}} "quoted words"`, InstanceId: "i", State: v1.SlotState_SLOT_STATE_READY, Error: "", TaskId: "t", Request: &v1.RunRequest{SourceId: "src", Repo: "r", Group: "g", RuntimeId: "rt", Name: "main", SlotId: "s1", Params: map[string]string{"k": "v"}, Args: "--cache-reuse 256", Snapshot: true}, CreatedAt: timestamppb.New(time.Unix(1, 0)), UpdatedAt: timestamppb.New(time.Unix(2, 0)), Profile: &v1.Profile{ToolServers: []string{"web"}}}
	if err := d.PutSlot(ctx, s); err != nil {
		t.Fatal(err)
	}
	list, err := d.ListSlots(ctx)
	if err != nil || len(list) != 1 {
		t.Fatal(err)
	}
	if !proto.Equal(list[0], s) {
		t.Fatalf("slot round trip\n got %v\nwant %v", list[0], s)
	}
	s.Request = nil
	s.State = v1.SlotState_SLOT_STATE_EMPTY
	d.PutSlot(ctx, s)
	list, _ = d.ListSlots(ctx)
	if list[0].GetRequest() != nil || list[0].GetState() != v1.SlotState_SLOT_STATE_EMPTY {
		t.Fatal("replace should drop the request")
	}
	if ok, _ := d.DeleteSlot(ctx, "s1"); !ok {
		t.Fatal("delete slot")
	}
	r := &v1.Route{Name: "main", OwnerKind: "slot", OwnerId: "s1", NodeId: "n1", Meta: &v1.ObjectMeta{Generation: 3, CreatedAt: timestamppb.New(time.Unix(3, 0))}, Spec: &v1.RouteSpec{Rules: []*v1.RewriteRule{{Match: "main", Targets: []*v1.RewriteTarget{{PoolId: "p1", Model: "m", Weight: 10}}}}, Profile: &v1.Profile{ToolServers: []string{"fs"}, MutatingTools: true}}, Status: &v1.RouteStatus{State: v1.RouteState_ROUTE_STATE_READY, Requests: 7, PoolIds: []string{"p1"}}}
	if err := d.PutRoute(ctx, r); err != nil {
		t.Fatal(err)
	}
	routes, _ := d.ListRoutes(ctx)
	if len(routes) != 1 || routes[0].GetOwnerId() != "s1" || routes[0].GetNodeId() != "n1" || routes[0].GetMeta().GetGeneration() != 3 || !routes[0].GetMeta().GetCreatedAt().AsTime().Equal(time.Unix(3, 0)) {
		t.Fatalf("route round trip: %v", routes)
	}
	if got := routes[0].GetSpec(); len(got.GetRules()) != 1 || got.GetRules()[0].GetTargets()[0].GetPoolId() != "p1" || got.GetRules()[0].GetTargets()[0].GetWeight() != 10 || !got.GetProfile().GetMutatingTools() {
		t.Fatalf("route spec round trip: %v", got)
	}
	if st := routes[0].GetStatus(); st.GetState() != v1.RouteState_ROUTE_STATE_READY || st.GetRequests() != 7 || len(st.GetPoolIds()) != 1 {
		t.Fatalf("route status round trip: %v", st)
	}
	d.DeleteRoute(ctx, "main")
	if routes, _ := d.ListRoutes(ctx); len(routes) != 0 {
		t.Fatal("route delete")
	}
}

func TestSourcesRoundTrip(t *testing.T) {
	d, _ := open(t)
	ctx := context.Background()
	s := &v1.Source{Id: "hf-mirror", Kind: v1.SourceKind_SOURCE_KIND_HUGGINGFACE, Name: "Mirror", Config: map[string]string{"endpoint": "https://hf-mirror.com", "token": "hf_stored"}, Seeded: false, CreatedAt: timestamppb.New(time.Unix(1, 0)), UpdatedAt: timestamppb.New(time.Unix(2, 0))}
	seed := &v1.Source{Id: "huggingface", Kind: v1.SourceKind_SOURCE_KIND_HUGGINGFACE, Name: "Hugging Face", Seeded: true, CreatedAt: timestamppb.New(time.Unix(3, 0)), UpdatedAt: timestamppb.New(time.Unix(3, 0))}
	for _, in := range []*v1.Source{s, seed} {
		if err := d.PutSource(ctx, in); err != nil {
			t.Fatal(err)
		}
	}
	list, err := d.ListSources(ctx)
	if err != nil || len(list) != 2 {
		t.Fatalf("list %v %v", list, err)
	}
	if !proto.Equal(list[0], s) || !proto.Equal(list[1], seed) {
		t.Fatalf("source round trip\n got %v %v\nwant %v %v", list[0], list[1], s, seed)
	}
	s.Config = nil
	if err := d.PutSource(ctx, s); err != nil {
		t.Fatal(err)
	}
	list, _ = d.ListSources(ctx)
	if len(list[0].GetConfig()) != 0 {
		t.Fatal("replace should drop the settings")
	}
	if ok, _ := d.DeleteSource(ctx, "hf-mirror"); !ok {
		t.Fatal("delete source")
	}
	if ok, _ := d.DeleteSource(ctx, "hf-mirror"); ok {
		t.Fatal("second delete should report absence")
	}
	if list, _ := d.ListSources(ctx); len(list) != 1 || list[0].GetId() != "huggingface" {
		t.Fatalf("after delete %v", list)
	}
}

func TestSettingsRoundTrip(t *testing.T) {
	d, _ := open(t)
	ctx := context.Background()
	empty, err := d.GetSettings(ctx)
	if err != nil || empty.GetHostLabel() != "" {
		t.Fatalf("fresh settings %v %v", empty, err)
	}
	s := &v1.Settings{HostLabel: "lab box"}
	if err := d.PutSettings(ctx, s); err != nil {
		t.Fatal(err)
	}
	got, err := d.GetSettings(ctx)
	if err != nil || !proto.Equal(got, s) {
		t.Fatalf("round trip %v %v", got, err)
	}
	if err := d.PutSettings(ctx, &v1.Settings{}); err != nil {
		t.Fatal(err)
	}
	if got, _ = d.GetSettings(ctx); got.GetHostLabel() != "" {
		t.Fatalf("replace should clear %v", got)
	}
}
