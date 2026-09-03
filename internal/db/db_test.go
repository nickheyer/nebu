package db

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func open(t *testing.T) (*DB, string) {
	t.Helper()
	dir := t.TempDir()
	d, err := Open(filepath.Join(dir, "nebu.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d, dir
}

func TestMigrationsApplyOnce(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nebu.db")
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	versions, _ := d.Migrations(context.Background())
	if len(versions) != 1 || versions[0] != 1 {
		t.Fatalf("versions %v", versions)
	}
	d.Close()
	again, err := Open(path)
	if err != nil {
		t.Fatalf("reopen should not reapply: %v", err)
	}
	defer again.Close()
	if versions, _ = again.Migrations(context.Background()); len(versions) != 1 {
		t.Fatalf("versions after reopen %v", versions)
	}
}

func TestEnums(t *testing.T) {
	for _, s := range []v1.InstanceState{v1.InstanceState_INSTANCE_STATE_READY, v1.InstanceState_INSTANCE_STATE_FAILED} {
		if got := v1.InstanceState(enumVal(v1.InstanceState(0).Descriptor(), enumCol(s))); got != s {
			t.Fatalf("round trip %v gave %v", s, got)
		}
	}
	if got := enumVal(v1.TensorGroupKind(0).Descriptor(), enumCol(v1.TensorGroupKind_TENSOR_GROUP_KIND_LAYER)); v1.TensorGroupKind(got) != v1.TensorGroupKind_TENSOR_GROUP_KIND_LAYER {
		t.Fatalf("multi word enum %v", got)
	}
	if enumVal(v1.InstanceState(0).Descriptor(), "bogus") != 0 {
		t.Fatal("unknown should be zero")
	}
}

func TestInstalls(t *testing.T) {
	d, _ := open(t)
	ctx := context.Background()
	older := &v1.Install{Id: "rt-1", RuntimeId: "rt", Kind: v1.InstallKind_INSTALL_KIND_ADOPTED, Path: "/a", Dir: "/", Facts: map[string]string{"version": "1"}, CreatedAt: timestamppb.New(time.Now().Add(-time.Hour))}
	newer := &v1.Install{Id: "rt-2", RuntimeId: "rt", Kind: v1.InstallKind_INSTALL_KIND_PREBUILT, Path: "/b", Dir: "/x", Version: "b1", Origin: "u", CreatedAt: timestamppb.Now()}
	other := &v1.Install{Id: "o-1", RuntimeId: "other", Kind: v1.InstallKind_INSTALL_KIND_ADOPTED, Path: "/c", Dir: "/", CreatedAt: timestamppb.Now()}
	for _, in := range []*v1.Install{older, newer, other} {
		if err := d.PutInstall(ctx, in); err != nil {
			t.Fatal(err)
		}
	}
	list, err := d.ListInstalls(ctx, "rt")
	if err != nil || len(list) != 2 || list[0].GetId() != "rt-2" || list[1].GetFacts()["version"] != "1" {
		t.Fatalf("list %v %v", list, err)
	}
	if all, _ := d.ListInstalls(ctx, ""); len(all) != 3 {
		t.Fatalf("all %v", all)
	}
	got, err := d.GetInstall(ctx, "rt-2")
	if err != nil || !proto.Equal(got, newer) {
		t.Fatalf("get %v %v", got, err)
	}
	if _, err := d.GetInstall(ctx, "nope"); !IsNotFound(err) {
		t.Fatalf("missing should be not found, got %v", err)
	}
	if ok, err := d.DeleteInstall(ctx, "rt-1"); err != nil || !ok {
		t.Fatal(err)
	}
	if ok, _ := d.DeleteInstall(ctx, "rt-1"); ok {
		t.Fatal("second delete should report absent")
	}
	if facts, _ := d.stringMap(ctx, `SELECT key, value FROM install_facts WHERE install_id = ?`, "rt-1"); len(facts) != 0 {
		t.Fatal("facts should cascade")
	}
}

func TestCalibrations(t *testing.T) {
	d, _ := open(t)
	ctx := context.Background()
	c := &v1.Calibration{OverheadDelta: -12.5, Samples: 2, UpdatedAt: timestamppb.Now()}
	if err := d.PutCalibration(ctx, "rt", "arch", c); err != nil {
		t.Fatal(err)
	}
	c.Samples = 3
	if err := d.PutCalibration(ctx, "rt", "arch", c); err != nil {
		t.Fatal(err)
	}
	rows, err := d.ListCalibrations(ctx)
	if err != nil || len(rows) != 1 || rows[0].RuntimeID != "rt" || rows[0].Architecture != "arch" || rows[0].Calibration.GetSamples() != 3 || rows[0].Calibration.GetOverheadDelta() != -12.5 {
		t.Fatalf("rows %v %v", rows, err)
	}
}

func sampleInstance() *v1.Instance {
	return &v1.Instance{
		Id: "i1", Name: "qwen", SourceId: "hf", Repo: "org/repo", Group: "Q4", RuntimeId: "llamacpp", InstallId: "llamacpp-1",
		Params:   map[string]string{"n_ctx": "8192", "alias": "qwen"},
		Command:  []string{"/bin/server", "--port", "1"},
		Endpoint: "http://127.0.0.1:1", State: v1.InstanceState_INSTANCE_STATE_READY, Pid: 42, TaskId: "t1",
		CreatedAt: timestamppb.New(time.Unix(1000, 0)), ReadyAt: timestamppb.New(time.Unix(1001, 0)),
		Plan: &v1.MemoryPlan{
			Verdict:      v1.FitVerdict_FIT_VERDICT_PARTIAL,
			Pools:        []*v1.PoolUsage{{PoolId: "gpu0", Kind: v1.PoolKind_POOL_KIND_DEVICE, UsedBytes: 10, CapacityBytes: 20}, {PoolId: "ram", Kind: v1.PoolKind_POOL_KIND_HOST, UsedBytes: 1, CapacityBytes: 2}},
			Placements:   []*v1.Placement{{Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_LAYER, PoolId: "gpu0", Bytes: 9, Count: 28}},
			WeightsBytes: 9, CacheBytes: 1, OverheadBytes: 1, Params: map[string]string{"n_gpu_layers": "28"}, Detail: "ok",
		},
		Triage:         []*v1.TriageHit{{Id: "oom", Summary: "out of memory", Hint: "lower ctx", Line: "CUDA out of memory", Fix: map[string]string{"n_ctx": "1024"}}, {Id: "assert", Summary: "s", Hint: "h", Line: "l"}},
		Measurements:   []*v1.Measurement{{Key: "device.weights", Bytes: 9, Line: "x"}, {Key: "device.used", Bytes: 11}},
		Request:        &v1.RunRequest{SourceId: "hf", Repo: "org/repo", Group: "Q4", RuntimeId: "llamacpp", Name: "qwen", Params: map[string]string{"n_ctx": "8192"}},
		DesiredRunning: true,
	}
}

func TestInstances(t *testing.T) {
	d, _ := open(t)
	ctx := context.Background()
	in := sampleInstance()
	if err := d.PutInstance(ctx, in); err != nil {
		t.Fatal(err)
	}
	bare := &v1.Instance{Id: "i0", Name: "old", State: v1.InstanceState_INSTANCE_STATE_STOPPED, CreatedAt: timestamppb.New(time.Unix(500, 0)), StoppedAt: timestamppb.New(time.Unix(600, 0))}
	if err := d.PutInstance(ctx, bare); err != nil {
		t.Fatal(err)
	}
	list, err := d.ListInstances(ctx)
	if err != nil || len(list) != 2 {
		t.Fatalf("list %v %v", list, err)
	}
	if !proto.Equal(list[0], bare) {
		t.Fatalf("bare round trip\n got %v\nwant %v", list[0], bare)
	}
	if !proto.Equal(list[1], in) {
		t.Fatalf("full round trip\n got %v\nwant %v", list[1], in)
	}
	in.State = v1.InstanceState_INSTANCE_STATE_STOPPED
	in.DesiredRunning = false
	in.Triage = nil
	in.StoppedAt = timestamppb.New(time.Unix(2000, 0))
	if err := d.PutInstance(ctx, in); err != nil {
		t.Fatal(err)
	}
	list, _ = d.ListInstances(ctx)
	if !proto.Equal(list[1], in) {
		t.Fatalf("update should replace children\n got %v\nwant %v", list[1], in)
	}
	if err := d.DeleteInstance(ctx, "i1"); err != nil {
		t.Fatal(err)
	}
	if list, _ = d.ListInstances(ctx); len(list) != 1 {
		t.Fatalf("after delete %v", list)
	}
	if rows, _ := d.strings(ctx, `SELECT arg FROM instance_command WHERE instance_id = ?`, "i1"); len(rows) != 0 {
		t.Fatal("children should cascade")
	}
}

func TestTasks(t *testing.T) {
	d, _ := open(t)
	ctx := context.Background()
	running := &v1.Task{Id: "t1", Kind: "pull", Title: "pull x", State: v1.TaskState_TASK_STATE_RUNNING, Progress: &v1.TaskProgress{Done: 5, Total: 10, Message: "m"}, Labels: map[string]string{"repo": "x"}, CreatedAt: timestamppb.New(time.Unix(10, 0)), StartedAt: timestamppb.New(time.Unix(11, 0))}
	if err := d.PutTask(ctx, running); err != nil {
		t.Fatal(err)
	}
	for i, line := range []string{"a", "b"} {
		if err := d.AppendTaskLog(ctx, "t1", i, line); err != nil {
			t.Fatal(err)
		}
	}
	got, logs, err := d.GetTask(ctx, "t1")
	if err != nil || !proto.Equal(got, running) || len(logs) != 2 || logs[1] != "b" {
		t.Fatalf("get %v %v %v", got, logs, err)
	}
	running.State = v1.TaskState_TASK_STATE_SUCCEEDED
	running.FinishedAt = timestamppb.New(time.Unix(12, 0))
	if err := d.PutTask(ctx, running); err != nil {
		t.Fatal(err)
	}
	if _, logs, _ = d.GetTask(ctx, "t1"); len(logs) != 2 {
		t.Fatal("logs should survive a task update")
	}
	for i := 0; i < 5; i++ {
		if err := d.PutTask(ctx, &v1.Task{Id: "old" + string(rune('a'+i)), Kind: "k", Title: "t", State: v1.TaskState_TASK_STATE_FAILED, Progress: &v1.TaskProgress{}, CreatedAt: timestamppb.New(time.Unix(int64(i), 0))}); err != nil {
			t.Fatal(err)
		}
	}
	stuck := &v1.Task{Id: "stuck", Kind: "run", Title: "run y", State: v1.TaskState_TASK_STATE_RUNNING, Progress: &v1.TaskProgress{}, CreatedAt: timestamppb.New(time.Unix(20, 0))}
	if err := d.PutTask(ctx, stuck); err != nil {
		t.Fatal(err)
	}
	list, err := d.ListTasks(ctx, 100)
	if err != nil || len(list) != 7 || list[0].GetId() != "stuck" || list[1].GetId() != "t1" {
		t.Fatalf("list %v %v", list, err)
	}
	if err := d.PruneTasks(ctx, 2); err != nil {
		t.Fatal(err)
	}
	list, _ = d.ListTasks(ctx, 100)
	if len(list) != 3 || list[0].GetId() != "stuck" || list[1].GetId() != "t1" || list[2].GetId() != "olde" {
		t.Fatalf("prune keeps running plus newest finished: %v", list)
	}
	n, err := d.FailUnfinishedTasks(ctx, "daemon restarted", time.Unix(30, 0))
	if err != nil || n != 1 {
		t.Fatalf("fail unfinished %d %v", n, err)
	}
	got, _, _ = d.GetTask(ctx, "stuck")
	if got.GetState() != v1.TaskState_TASK_STATE_FAILED || got.GetError() != "daemon restarted" || got.GetFinishedAt() == nil {
		t.Fatalf("stuck %v", got)
	}
	if _, _, err := d.GetTask(ctx, "nope"); !IsNotFound(err) {
		t.Fatal("missing task should be not found")
	}
}

func TestImportLegacy(t *testing.T) {
	d, dir := open(t)
	ctx := context.Background()
	write := func(rel string, msg proto.Message) {
		data, _ := protojson.Marshal(msg)
		os.MkdirAll(filepath.Dir(filepath.Join(dir, rel)), 0o755)
		os.WriteFile(filepath.Join(dir, rel), data, 0o644)
	}
	write("installs/x.json", &v1.Install{Id: "x", RuntimeId: "rt", Kind: v1.InstallKind_INSTALL_KIND_PREBUILT, Path: "/p", Dir: "/d", CreatedAt: timestamppb.Now(), Facts: map[string]string{"a": "b"}})
	write("instances/i1.json", sampleInstance())
	write("calibration.json", &v1.CalibrationTable{Entries: map[string]*v1.Calibration{"rt|arch": {OverheadDelta: 1, Samples: 1, UpdatedAt: timestamppb.Now()}}})
	if err := d.ImportLegacy(ctx, dir, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatal(err)
	}
	if in, err := d.GetInstall(ctx, "x"); err != nil || in.GetFacts()["a"] != "b" {
		t.Fatalf("install import %v %v", in, err)
	}
	if list, _ := d.ListInstances(ctx); len(list) != 1 || list[0].GetName() != "qwen" {
		t.Fatalf("instance import %v", list)
	}
	if rows, _ := d.ListCalibrations(ctx); len(rows) != 1 || rows[0].Architecture != "arch" {
		t.Fatalf("calibration import %v", rows)
	}
	for _, rel := range []string{"installs/x.json", "instances/i1.json", "calibration.json"} {
		if _, err := os.Stat(filepath.Join(dir, rel)); err == nil {
			t.Fatalf("%s should be removed after import", rel)
		}
	}
	if err := d.ImportLegacy(ctx, dir, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatalf("second import with nothing to do: %v", err)
	}
}
