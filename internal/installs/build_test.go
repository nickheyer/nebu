package installs

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nickheyer/nebu/internal/db"
	"github.com/nickheyer/nebu/internal/tasks"
	"github.com/nickheyer/nebu/pkg/build"
	"github.com/nickheyer/nebu/pkg/events"
	"github.com/nickheyer/nebu/pkg/host"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/runtime"
	"github.com/nickheyer/nebu/pkg/spec"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func timestampNow() *timestamppb.Timestamp { return timestamppb.Now() }

const buildRecipe = `id: fake
runtime_id: fake
tools: [sh]
vars:
  greeting: hello
steps:
  - command: [sh, -c, 'printf "#!/bin/sh\necho fake version 4.5.6 {{.vars.greeting}}\n" > {{.out}}/fakebin && chmod +x {{.out}}/fakebin']
binary: fakebin
`

func buildManager(t *testing.T) *Manager {
	t.Helper()
	store, err := db.Open(filepath.Join(t.TempDir(), "nebu.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	manifest := &v1.RuntimeManifest{Id: "fake", Acquire: &v1.Acquire{Methods: []*v1.InstallMethod{{Id: "source", How: &v1.InstallMethod_Recipe{Recipe: &v1.FromRecipe{RecipeId: "fake"}}}}}, Probes: []*v1.CommandProbe{{Key: "version", Args: []string{"--version"}, Match: `fake version (?P<value>\S+)`}}}
	runtimes, err := runtime.New([]*v1.RuntimeManifest{manifest})
	if err != nil {
		t.Fatal(err)
	}
	rc := &v1.Recipe{}
	if err := spec.Decode([]byte(buildRecipe), rc); err != nil {
		t.Fatal(err)
	}
	recipes, err := build.New([]*v1.Recipe{rc})
	if err != nil {
		t.Fatal(err)
	}
	prober, err := host.New(nil, nil, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	root := t.TempDir()
	return &Manager{
		DB:       store,
		Runtimes: runtimes,
		Recipes:  recipes,
		Engine:   &build.Engine{Root: root, Log: log},
		Defaults: &v1.Builds{},
		Host:     prober,
		Tasks:    tasks.New(context.Background(), log, store, nil),
		Events:   events.New(),
		Log:      log,
	}
}

func TestBuildCacheAndRemove(t *testing.T) {
	m := buildManager(t)
	ctx := context.Background()
	recipes, err := m.ListRecipes(ctx, "")
	if err != nil || len(recipes) != 1 || recipes[0].GetVariant() != build.DefaultVariant || len(recipes[0].GetUnmet()) != 0 {
		t.Fatalf("recipes %v %v", recipes, err)
	}
	b, task, err := m.Build(ctx, &v1.BuildRequest{RuntimeId: "fake", Vars: map[string]string{"greeting": "world"}})
	if err != nil {
		t.Fatal(err)
	}
	final, err := m.Tasks.Wait(ctx, task.GetId())
	if err != nil || final.GetState() != v1.TaskState_TASK_STATE_SUCCEEDED {
		t.Fatalf("build task %v %v", final, err)
	}
	done, err := m.GetBuild(ctx, b.GetId())
	if err != nil {
		t.Fatal(err)
	}
	if done.GetState() != v1.BuildState_BUILD_STATE_SUCCEEDED || done.GetInstallId() == "" || done.GetVars()["greeting"] != "world" {
		t.Fatalf("finished build %v", done)
	}
	in, err := m.Get(ctx, done.GetInstallId())
	if err != nil {
		t.Fatal(err)
	}
	if in.GetKind() != v1.InstallKind_INSTALL_KIND_BUILT || in.GetBuildId() != b.GetId() || in.GetFacts()["version"] != "4.5.6" || in.GetFacts()["variant"] != build.DefaultVariant {
		t.Fatalf("install %v", in)
	}
	again, task2, err := m.Build(ctx, &v1.BuildRequest{RuntimeId: "fake", Vars: map[string]string{"greeting": "world"}})
	if err != nil {
		t.Fatal(err)
	}
	if again.GetId() != b.GetId() || task2.GetLabels()["cached"] != "true" {
		t.Fatalf("second build should hit the cache: %v %v", again, task2)
	}
	m.Tasks.Wait(ctx, task2.GetId())
	other, _, err := m.Build(ctx, &v1.BuildRequest{RecipeId: "fake", Vars: map[string]string{"greeting": "moon"}})
	if err != nil {
		t.Fatal(err)
	}
	if other.GetId() == b.GetId() {
		t.Fatal("different vars should be a different build")
	}
	m.Tasks.Wait(ctx, other.GetTaskId())
	forced, _, err := m.Build(ctx, &v1.BuildRequest{RuntimeId: "fake", Vars: map[string]string{"greeting": "world"}, Force: true})
	if err != nil {
		t.Fatal(err)
	}
	m.Tasks.Wait(ctx, forced.GetTaskId())
	if forced.GetId() != b.GetId() {
		t.Fatal("force keeps the id")
	}
	removed, err := m.RemoveBuild(ctx, b.GetId())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(removed.GetDir()); !os.IsNotExist(err) {
		t.Fatal("build dir should be gone")
	}
	if _, err := m.Get(ctx, removed.GetInstallId()); err == nil {
		t.Fatal("install should be gone with the build")
	}
	if _, err := m.GetBuild(ctx, b.GetId()); err == nil {
		t.Fatal("build row should be gone")
	}
	if _, _, err := m.Build(ctx, &v1.BuildRequest{RuntimeId: "nope"}); err == nil {
		t.Fatal("unknown runtime should fail")
	}
	if _, _, err := m.Build(ctx, &v1.BuildRequest{RecipeId: "fake", RuntimeId: "other"}); err == nil {
		t.Fatal("recipe runtime mismatch should fail")
	}
	if _, _, err := m.Build(ctx, &v1.BuildRequest{RuntimeId: "fake", Variant: "nope"}); err == nil {
		t.Fatal("unknown variant should fail")
	}
}

func TestInstallByMethod(t *testing.T) {
	m := buildManager(t)
	ctx := context.Background()
	rt, _ := m.Runtimes.Get("fake")
	profile, _ := m.Host.Profile(ctx, false)
	options, err := m.Options(ctx, rt, profile)
	if err != nil || len(options) != 1 || options[0].GetMethod().GetId() != "source" || options[0].GetRecipe().GetVariant() != build.DefaultVariant {
		t.Fatalf("options %v %v", options, err)
	}
	var names []string
	for _, f := range options[0].GetFields() {
		names = append(names, f.GetName())
	}
	if want := []string{"ref", "sandbox", "image", "force", "var.greeting"}; strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("fields %v", names)
	}
	if _, err := m.Install(ctx, "fake", "source", map[string]string{"nope": "1"}); err == nil {
		t.Fatal("an unknown setting should be refused")
	}
	if _, err := m.Install(ctx, "fake", "source", map[string]string{"sandbox": "cloud"}); err == nil {
		t.Fatal("an unknown sandbox should be refused")
	}
	if _, err := m.Install(ctx, "fake", "nope", nil); err == nil {
		t.Fatal("unknown method should fail")
	}
	task, err := m.Install(ctx, "fake", "source", map[string]string{"var.greeting": "moon"})
	if err != nil {
		t.Fatal(err)
	}
	if task.GetLabels()["method"] != "source" {
		t.Fatalf("labels %v", task.GetLabels())
	}
	again, err := m.Install(ctx, "fake", "source", nil)
	if err != nil || again.GetId() != task.GetId() {
		t.Fatalf("a second install should return the running task: %v %v", again, err)
	}
	final, err := m.Tasks.Wait(ctx, task.GetId())
	if err != nil || final.GetState() != v1.TaskState_TASK_STATE_SUCCEEDED {
		t.Fatalf("install task %v %v", final, err)
	}
	list, _ := m.List(ctx, "fake")
	if len(list) != 1 || list[0].GetKind() != v1.InstallKind_INSTALL_KIND_BUILT {
		t.Fatalf("installs %v", list)
	}
	builds, _ := m.ListBuilds(ctx, "fake")
	if len(builds) != 1 || builds[0].GetVars()["greeting"] != "moon" || builds[0].GetTaskId() != task.GetId() {
		t.Fatalf("the install's build should carry its var and task: %v", builds)
	}
}

func TestRemoveInstallDropsBuild(t *testing.T) {
	m := buildManager(t)
	ctx := context.Background()
	b, task, _ := m.Build(ctx, &v1.BuildRequest{RuntimeId: "fake"})
	m.Tasks.Wait(ctx, task.GetId())
	done, _ := m.GetBuild(ctx, b.GetId())
	if _, err := m.Remove(ctx, done.GetInstallId()); err != nil {
		t.Fatal(err)
	}
	if _, err := m.GetBuild(ctx, b.GetId()); err == nil {
		t.Fatal("removing the install should drop the build")
	}
	if _, err := os.Stat(done.GetDir()); !os.IsNotExist(err) {
		t.Fatal("build dir should be gone")
	}
}

func TestRecoverBuilds(t *testing.T) {
	m := buildManager(t)
	ctx := context.Background()
	m.DB.PutBuild(ctx, &v1.Build{Id: "stuck", RecipeId: "fake", RuntimeId: "fake", Variant: "default", State: v1.BuildState_BUILD_STATE_RUNNING, CreatedAt: timestampNow()})
	if err := m.RecoverBuilds(ctx); err != nil {
		t.Fatal(err)
	}
	b, _ := m.GetBuild(ctx, "stuck")
	if b.GetState() != v1.BuildState_BUILD_STATE_FAILED || b.GetError() != db.RestartNote {
		t.Fatalf("recovered %v", b)
	}
}
