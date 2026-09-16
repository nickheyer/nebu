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
	"github.com/nickheyer/nebu/pkg/estimate"
	"github.com/nickheyer/nebu/pkg/events"
	"github.com/nickheyer/nebu/pkg/host"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/recipes"
	"github.com/nickheyer/nebu/pkg/runtimes"
	"github.com/nickheyer/nebu/pkg/triage"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func timestampNow() *timestamppb.Timestamp { return timestamppb.Now() }

// A runtime built from one recipe, whose binary prints its version and the greeting it was built with
type fakeRuntime struct{}

func (fakeRuntime) ID() string                                         { return "fake" }
func (fakeRuntime) Name() string                                       { return "Fake" }
func (fakeRuntime) Description() string                                { return "A runtime for tests" }
func (fakeRuntime) Formats() []string                                  { return []string{"gguf"} }
func (fakeRuntime) API() v1.ApiFlavor                                  { return v1.ApiFlavor_API_FLAVOR_OPENAI }
func (fakeRuntime) Requirements() []string                             { return nil }
func (fakeRuntime) Unmet(*v1.HostProfile) []string                     { return nil }
func (fakeRuntime) Params() []*v1.Param                                { return nil }
func (fakeRuntime) Prepares(string) bool                               { return false }
func (fakeRuntime) Prepare(runtimes.Launch) (*runtimes.Command, error) { return nil, nil }
func (fakeRuntime) PrepareTimeout() time.Duration                      { return 0 }
func (fakeRuntime) Health() runtimes.Health                            { return runtimes.Health{Path: "/health"} }
func (fakeRuntime) StopGrace() time.Duration                           { return time.Second }
func (fakeRuntime) Policy() *estimate.Policy                           { return nil }
func (fakeRuntime) Measure([]string) []*v1.Measurement                 { return nil }
func (fakeRuntime) Triage() []triage.Set                               { return nil }

func (fakeRuntime) Methods() []runtimes.Method {
	return []runtimes.Method{{ID: "source", Description: "Build from source", Kind: v1.InstallKind_INSTALL_KIND_BUILT, RecipeID: "fake"}}
}

func (fakeRuntime) Launch(in runtimes.Launch) (*runtimes.Command, error) {
	return &runtimes.Command{Command: in.Install.Path}, nil
}

func (fakeRuntime) Probes() []runtimes.Probe {
	return []runtimes.Probe{{Key: "version", Args: []string{"--version"}, Parse: func(out string) (string, bool) {
		_, rest, ok := strings.Cut(out, "fake version ")
		if !ok {
			return "", false
		}
		if f := strings.Fields(rest); len(f) > 0 {
			return f[0], true
		}
		return "", false
	}}}
}

// A recipe with no source that writes a shell script as its binary
type fakeRecipe struct{}

func (fakeRecipe) ID() string             { return "fake" }
func (fakeRecipe) RuntimeID() string      { return "fake" }
func (fakeRecipe) Description() string    { return "Writes a script" }
func (fakeRecipe) Source() recipes.Source { return recipes.Source{} }
func (fakeRecipe) Tools() []string        { return []string{"sh"} }
func (fakeRecipe) Facts() []string        { return nil }
func (fakeRecipe) Vars() []recipes.Var {
	return []recipes.Var{{Name: "greeting", Label: "Greeting", Default: "hello", Description: "The word the script prints"}}
}
func (fakeRecipe) Variants() []recipes.Variant { return nil }
func (fakeRecipe) Sandbox() recipes.Sandbox {
	return recipes.Sandbox{Kind: v1.SandboxKind_SANDBOX_KIND_HOST}
}
func (fakeRecipe) Outputs() []string        { return nil }
func (fakeRecipe) Binary() string           { return "fakebin" }
func (fakeRecipe) Timeout() time.Duration   { return time.Minute }
func (fakeRecipe) Patches() []recipes.Patch { return nil }

func (fakeRecipe) Steps(b *recipes.Build) []recipes.Step {
	script := "#!/bin/sh\necho fake version 4.5.6 " + b.Vars["greeting"] + "\n"
	return []recipes.Step{{Command: []string{"sh", "-c", "printf '%s' \"$1\" > " + b.Out + "/fakebin && chmod +x " + b.Out + "/fakebin", "sh", script}}}
}

// The fake recipe with a container CLI no host has, so the container sandbox is offered but unmet
type cliLessRecipe struct{ fakeRecipe }

func (cliLessRecipe) ID() string { return "clifree" }
func (cliLessRecipe) Sandbox() recipes.Sandbox {
	return recipes.Sandbox{Kind: v1.SandboxKind_SANDBOX_KIND_HOST, CLIs: []string{"definitely-missing-cli-xyz"}}
}

// The fake recipe fetched from the releases of a repository, so a ref means something
type releasedRecipe struct{ fakeRecipe }

func (releasedRecipe) ID() string { return "released" }
func (releasedRecipe) Source() recipes.Source {
	return recipes.Source{Releases: "o/r", Archive: func(ref string) string { return "https://example.com/" + ref + ".tar.gz" }}
}

func fieldNamed(fields []*v1.ConfigField, name string) *v1.ConfigField {
	for _, f := range fields {
		if f.GetName() == name {
			return f
		}
	}
	return nil
}

func TestBuildFieldsDescribeSandboxAndRef(t *testing.T) {
	profile := &v1.HostProfile{Os: "linux", Arch: "amd64"}
	defaults := &v1.Builds{}
	sel, err := build.Select(cliLessRecipe{}, profile, build.Options{Defaults: defaults})
	if err != nil {
		t.Fatal(err)
	}
	fields := buildFields(cliLessRecipe{}, sel, profile, defaults)
	sandbox := fieldNamed(fields, "sandbox")
	if sandbox.GetChoiceLabels()["oci"] != "Container" || sandbox.GetChoiceLabels()["host"] != "Host toolchain" {
		t.Fatalf("sandbox choices need words: %v", sandbox.GetChoiceLabels())
	}
	if !strings.Contains(sandbox.GetChoiceUnmet()["oci"], "definitely-missing-cli-xyz") || sandbox.GetChoiceUnmet()["host"] != "" {
		t.Fatalf("a container needs a cli the host lacks: %v", sandbox.GetChoiceUnmet())
	}
	if fieldNamed(fields, "ref") != nil {
		t.Fatal("a recipe without a source takes no ref")
	}
	if image := fieldNamed(fields, "image"); image.GetPlaceholder() == "" || image.GetRequired() {
		t.Fatalf("the image shows its shape and is required only under a container: %v", image)
	}
	if greeting := fieldNamed(fields, "var.greeting"); greeting.GetDefault() != "hello" || greeting.GetDescription() == "" {
		t.Fatalf("recipe variables carry their default and description: %v", greeting)
	}

	sel, err = build.Select(releasedRecipe{}, profile, build.Options{Defaults: defaults})
	if err != nil {
		t.Fatal(err)
	}
	ref := fieldNamed(buildFields(releasedRecipe{}, sel, profile, defaults), "ref")
	if ref == nil || ref.GetDefault() != "" || ref.GetPlaceholder() != "Latest" || !strings.Contains(ref.GetDescription(), "github.com/o/r") {
		t.Fatalf("a released source takes a ref, the newest release when empty: %v", ref)
	}
}

func buildManager(t *testing.T) *Manager {
	t.Helper()
	store, err := db.Open(filepath.Join(t.TempDir(), "nebu.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	rts, err := runtimes.New([]runtimes.Runtime{fakeRuntime{}})
	if err != nil {
		t.Fatal(err)
	}
	book, err := build.New([]recipes.Recipe{fakeRecipe{}})
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	root := t.TempDir()
	return &Manager{
		DB:       store,
		Runtimes: rts,
		Recipes:  book,
		Engine:   &build.Engine{Root: root, Log: log},
		Defaults: &v1.Builds{},
		Host:     host.New(nil, nil, time.Minute),
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
	if want := []string{"sandbox", "image", "force", "var.greeting"}; strings.Join(names, ",") != strings.Join(want, ",") {
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
