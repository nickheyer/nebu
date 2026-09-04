package db

import (
	"context"
	"testing"
	"time"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestMigrationsApplied(t *testing.T) {
	d, _ := open(t)
	versions, err := d.Migrations(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != 4 || versions[3] != 4 {
		t.Fatalf("versions %v", versions)
	}
}

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
	n, err := d.FailUnfinishedBuilds(ctx, "restart", time.Unix(20, 0))
	if err != nil || n != 2 {
		t.Fatalf("fail unfinished %d %v", n, err)
	}
	got, _ = d.GetBuild(ctx, "b1")
	if got.GetState() != v1.BuildState_BUILD_STATE_FAILED || got.GetError() != "restart" || got.GetFinishedAt() == nil {
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
	s := &v1.Slot{Id: "s1", Name: "main", Description: "d", DeviceIds: []string{"g1", "g0"}, MemoryBytes: 1 << 33, RuntimeId: "rt", Params: map[string]string{"n_ctx": "1"}, InstanceId: "i", State: v1.SlotState_SLOT_STATE_READY, Error: "", TaskId: "t", Request: &v1.RunRequest{SourceId: "src", Repo: "r", Group: "g", RuntimeId: "rt", Name: "main", SlotId: "s1", Params: map[string]string{"k": "v"}}, CreatedAt: timestamppb.New(time.Unix(1, 0)), UpdatedAt: timestamppb.New(time.Unix(2, 0))}
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
	r := &v1.Route{Name: "main", InstanceId: "i", SlotId: "s1", Endpoint: "http://x", Api: v1.ApiFlavor_API_FLAVOR_OPENAI, State: v1.RouteState_ROUTE_STATE_READY, Model: "r:g", Requests: 7, UpdatedAt: timestamppb.New(time.Unix(3, 0))}
	if err := d.PutRoute(ctx, r); err != nil {
		t.Fatal(err)
	}
	routes, _ := d.ListRoutes(ctx)
	if len(routes) != 1 || !proto.Equal(routes[0], r) {
		t.Fatalf("route round trip %v", routes)
	}
	d.DeleteRoute(ctx, "main")
	if routes, _ := d.ListRoutes(ctx); len(routes) != 0 {
		t.Fatal("route delete")
	}
}

func TestSourcesRoundTrip(t *testing.T) {
	d, _ := open(t)
	ctx := context.Background()
	s := &v1.Source{Id: "hf-mirror", Kind: v1.SourceKind_SOURCE_KIND_HUGGINGFACE, Endpoint: "https://hf-mirror.com", TokenEnv: "HF_TOKEN", Path: "", Options: map[string]string{"namespace": "ai"}, Seeded: false, CreatedAt: timestamppb.New(time.Unix(1, 0)), UpdatedAt: timestamppb.New(time.Unix(2, 0))}
	seed := &v1.Source{Id: "huggingface", Kind: v1.SourceKind_SOURCE_KIND_HUGGINGFACE, Seeded: true, CreatedAt: timestamppb.New(time.Unix(3, 0)), UpdatedAt: timestamppb.New(time.Unix(3, 0))}
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
	s.Options = nil
	s.TokenEnv = ""
	if err := d.PutSource(ctx, s); err != nil {
		t.Fatal(err)
	}
	list, _ = d.ListSources(ctx)
	if len(list[0].GetOptions()) != 0 || list[0].GetTokenEnv() != "" {
		t.Fatal("replace should drop the options")
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

func TestWatchesAndFindings(t *testing.T) {
	d, _ := open(t)
	ctx := context.Background()
	w := &v1.Watch{Id: "w1", SourceId: "hf", Repo: "org/name", Revision: "main", GroupMatch: "Q4", AutoPull: true, SlotId: "s1", RuntimeId: "rt", Params: map[string]string{"a": "b"}, LastCommit: "abc", KnownGroups: []string{"Q8", "Q4"}, CheckedAt: timestamppb.New(time.Unix(9, 0)), CreatedAt: timestamppb.New(time.Unix(8, 0)), Error: ""}
	if err := d.PutWatch(ctx, w); err != nil {
		t.Fatal(err)
	}
	list, _ := d.ListWatches(ctx)
	if len(list) != 1 || !proto.Equal(list[0], w) {
		t.Fatalf("watch round trip %v", list)
	}
	dup := proto.Clone(w).(*v1.Watch)
	dup.Id = "w2"
	if err := d.PutWatch(ctx, dup); err == nil {
		t.Fatal("same repo and revision should violate the unique index")
	}
	f := &v1.Finding{Id: "f1", WatchId: "w1", Kind: v1.FindingKind_FINDING_KIND_NEW_GROUP, Repo: "org/name", Commit: "abc", Group: "Q5", Detail: "d", TaskId: "t", FoundAt: timestamppb.New(time.Unix(11, 0))}
	if err := d.PutFinding(ctx, f); err != nil {
		t.Fatal(err)
	}
	later := proto.Clone(f).(*v1.Finding)
	later.Id, later.FoundAt, later.Acknowledged = "f2", timestamppb.New(time.Unix(12, 0)), true
	d.PutFinding(ctx, later)
	all, _ := d.ListFindings(ctx, "", false)
	if len(all) != 2 || all[0].GetId() != "f2" {
		t.Fatalf("findings newest first %v", all)
	}
	unacked, _ := d.ListFindings(ctx, "w1", true)
	if len(unacked) != 1 || !proto.Equal(unacked[0], f) {
		t.Fatalf("unacked %v", unacked)
	}
	if err := d.PruneFindings(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if got, _ := d.GetFinding(ctx, "f2"); got == nil {
		t.Fatal("newest acknowledged finding kept within keep")
	}
	if ok, _ := d.DeleteWatch(ctx, "w1"); !ok {
		t.Fatal("delete watch")
	}
	if rest, _ := d.ListFindings(ctx, "", false); len(rest) != 0 {
		t.Fatal("findings should cascade")
	}
	if _, err := d.GetFinding(ctx, "f1"); !IsNotFound(err) {
		t.Fatal("finding gone")
	}
}
