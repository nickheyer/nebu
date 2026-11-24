package build

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/transfer"
)

// A git repository whose default branch holds one commit and whose refs/pull/7/head holds a second, as a host keeps
// the head of a pull request, with the commit of each
func forkRepo(t *testing.T) (dir, main, pull string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir = t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git("init", "--quiet", "--initial-branch=main")
	write("tool.in", "#!/bin/sh\necho fork @GREETING@\n")
	write("notes.txt", "hello\nworld\n")
	git("add", ".")
	git("commit", "--quiet", "-m", "first")
	main = git("rev-parse", "HEAD")
	write("tool.in", "#!/bin/sh\necho pull @GREETING@\n")
	git("commit", "--quiet", "-am", "second")
	pull = git("rev-parse", "HEAD")
	git("update-ref", "refs/pull/7/head", pull)
	git("reset", "--quiet", "--hard", main)
	return dir, main, pull
}

// A request names its own repository, ref, steps, environment, patch, outputs, binary and time limit, and the build
// is made of those in place of everything the recipe fixes
func TestRequestReplacesWhatTheRecipeFixes(t *testing.T) {
	repo, main, pull := forkRepo(t)
	srv, downloads := server(t)
	rc := engineRecipe(srv.URL)
	e := &Engine{Root: t.TempDir(), Jobs: 3, Sources: githubRegistry(t, srv.URL), Fetcher: transfer.New(0, 0, 0, slog.Default())}
	host := &v1.HostProfile{Os: "linux", Arch: "amd64", Facts: map[string]string{}}
	opts := Options{
		Repo:        repo,
		Ref:         "refs/pull/7/head",
		Vars:        map[string]string{"greeting": "moon"},
		SkipPatches: []string{"inline", "fromurl", "optional"},
		Steps: []*v1.BuildStep{
			{Name: "render", Script: `sed "s/@GREETING@/{{.Vars.greeting}} $SUFFIX/" tool.in > built/tool && chmod +x built/tool`, Env: map[string]string{"SUFFIX": "{{.Variant}}"}},
			{Name: "words", Command: []string{"sh", "-c", `echo "$# words jobs=$1 flavor=$FLAVOR" > "$2/words.txt"`, "sh", "{{.Jobs}}", "{{.Out}}", "{{.Vars.unset}}"}},
			{Name: "where", Command: []string{"sh", "-c", "pwd > {{.Out}}/where.txt"}, Dir: "built"},
		},
		Env: map[string]string{"FLAVOR": "{{.Ref}}"},
		// In order: the second is made for notes the first has patched, and the one without a name makes the directory the steps write into
		Patches: []*v1.Patch{
			{Id: "first", Content: "--- a/notes.txt\n+++ b/notes.txt\n@@ -1,2 +1,3 @@\n hello\n+patched\n world\n"},
			{Id: "notes", Url: srv.URL + "/extra.patch", Strip: 1},
			{Content: "--- /dev/null\n+++ b/built/.keep\n@@ -0,0 +1 @@\n+kept\n"},
		},
		Outputs: []string{"built/tool"},
		Binary:  "tool",
		Timeout: time.Minute,
	}
	sel, err := Select(rc, host, opts)
	if err != nil {
		t.Fatal(err)
	}
	if sel.Ref != "refs/pull/7/head" || sel.Source.Repo != repo || sel.Source.Releases != "" || sel.Timeout() != time.Minute || sel.Binary() != "tool" || strings.Join(sel.Outputs(), ",") != "built/tool" {
		t.Fatalf("selection %+v", sel)
	}
	if err := sel.Err(); err != nil {
		t.Fatalf("a request's own steps need none of the recipe's tools: %v", err)
	}
	b := sel.Build()
	if err := e.Resolve(context.Background(), sel, b); err != nil {
		t.Fatal(err)
	}
	if b.GetRef() != "refs/pull/7/head" || b.GetCommit() != "" || strings.Join(b.GetPatches(), ",") != "first,notes,request-3" {
		t.Fatalf("resolved %v", b)
	}
	var out bytes.Buffer
	if err := e.Run(context.Background(), sel, b, &out, nil); err != nil {
		t.Fatalf("run: %v\n%s", err, out.String())
	}
	if b.GetCommit() != pull || b.GetCommit() == main || !strings.HasSuffix(b.GetBinary(), filepath.Join("out", "tool")) {
		t.Fatalf("build after run %v, the pull request's head is %s", b, pull)
	}
	if got, _ := exec.Command(b.GetBinary()).Output(); strings.TrimSpace(string(got)) != "pull moon cpu" {
		t.Fatalf("binary output %q\n%s", got, out.String())
	}
	read := func(name string) string {
		data, err := os.ReadFile(filepath.Join(b.GetDir(), name))
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(data))
	}
	// The word of the variable nothing set is left out, and the environment reaches every step
	if got := read("out/words.txt"); got != "2 words jobs=3 flavor=refs/pull/7/head" {
		t.Fatalf("words %q", got)
	}
	if got := read("out/where.txt"); got != filepath.Join(b.GetDir(), "src", "built") {
		t.Fatalf("step directory %q", got)
	}
	if got := read("src/notes.txt"); got != "hello\npatched\nfrom url\nworld" {
		t.Fatalf("the request's patches apply in order: %q", got)
	}
	for _, want := range []string{"cloning " + repo, "patch inline left out", "patch first applied", "patch notes applied", "env FLAVOR=refs/pull/7/head", "--- render: sh -c "} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("transcript missing %q:\n%s", want, out.String())
		}
	}
	if *downloads != 0 {
		t.Fatalf("a named repository is cloned and the recipe's archive left alone, downloads %d", *downloads)
	}
	// The default branch when no ref is named, a different build
	opts.Ref = ""
	plain, err := Select(rc, host, opts)
	if err != nil {
		t.Fatal(err)
	}
	pb := plain.Build()
	if err := e.Resolve(context.Background(), plain, pb); err != nil {
		t.Fatal(err)
	}
	if pb.GetRef() != "" || pb.GetId() == b.GetId() {
		t.Fatalf("default branch build %v beside %s", pb, b.GetId())
	}
	if err := e.Run(context.Background(), plain, pb, &out, nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := exec.Command(pb.GetBinary()).Output(); pb.GetCommit() != main || strings.TrimSpace(string(got)) != "fork moon cpu" {
		t.Fatalf("default branch build %v prints %q", pb, got)
	}
}

// The steps a preview answers are the recipe's in the form a request sends, so sending them back builds what the
// recipe builds
func TestPreviewedStepsBuildWhatTheRecipeBuilds(t *testing.T) {
	srv, _ := server(t)
	rc := engineRecipe(srv.URL)
	e := &Engine{Root: t.TempDir(), Jobs: 3, Sources: githubRegistry(t, srv.URL), Fetcher: transfer.New(0, 0, 0, slog.Default())}
	host := &v1.HostProfile{Os: "linux", Arch: "amd64", Facts: map[string]string{}}
	sel, err := Select(rc, host, Options{SkipPatches: []string{"optional"}})
	if err != nil {
		t.Fatal(err)
	}
	b := sel.Build()
	if err := e.Resolve(context.Background(), sel, b); err != nil {
		t.Fatal(err)
	}
	preview := e.Preview(sel, b)
	if preview.GetBinary() != "bin" || strings.Join(preview.GetOutputs(), ",") != "bin,nothing*.opt" || preview.GetSource() != "github.com/o/r" || preview.GetTimeoutMs() != uint32(DefaultTimeout/time.Millisecond) {
		t.Fatalf("preview %v", preview)
	}
	steps := preview.GetSteps()
	if len(steps) != 3 || steps[0].GetName() != "render" || steps[0].GetEnv()["STAMP"] != "v2.1-cpu" || len(steps[1].GetCommand()) != 3 {
		t.Fatalf("previewed steps %v", steps)
	}
	if last := strings.Join(steps[2].GetCommand(), " "); !strings.Contains(last, "{{.Out}}/copied.txt") || !strings.Contains(last, "jobs=3 root={{.Root}} dir={{.Src}}") || strings.Contains(last, e.Root) {
		t.Fatalf("the build's directories stay templates until a build fills them in: %s", last)
	}
	// One flag more on a step of the recipe, and the rest as it was
	steps[1].Command = append(steps[1].Command, "{{.Vars.unset}}")
	own, err := Select(rc, host, Options{SkipPatches: []string{"optional"}, Steps: steps})
	if err != nil {
		t.Fatal(err)
	}
	ob := own.Build()
	if err := e.Resolve(context.Background(), own, ob); err != nil {
		t.Fatal(err)
	}
	if again := e.Preview(own, ob).GetSteps(); len(again) != 3 || again[1] != steps[1] {
		t.Fatalf("a request's own steps preview as given: %v", again)
	}
	var out bytes.Buffer
	if err := e.Run(context.Background(), own, ob, &out, nil); err != nil {
		t.Fatalf("run: %v\n%s", err, out.String())
	}
	if got, _ := exec.Command(ob.GetBinary()).Output(); strings.TrimSpace(string(got)) != "v2.1-cpu" {
		t.Fatalf("binary output %q", got)
	}
	for _, want := range []string{"commit=c0ffee jobs=3", "root=" + ob.GetDir(), "dir=" + filepath.Join(ob.GetDir(), "src")} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("transcript missing %q:\n%s", want, out.String())
		}
	}
}

// A request's steps and patches are checked before any work starts
func TestRequestIsCheckedAtSelection(t *testing.T) {
	rc := selectRecipe()
	for name, opts := range map[string]Options{
		"a step with a command and a script": {Steps: []*v1.BuildStep{{Command: []string{"true"}, Script: "true"}}},
		"a step with neither":                {Steps: []*v1.BuildStep{{Name: "nothing"}}},
		"a template that does not parse":     {Steps: []*v1.BuildStep{{Script: "echo {{.Out"}}},
		"a template naming no field":         {Steps: []*v1.BuildStep{{Command: []string{"echo", "{{.Nowhere}}"}}}},
		"a step's environment":               {Steps: []*v1.BuildStep{{Script: "true", Env: map[string]string{"A": "{{.Nowhere}}"}}}},
		"the build's environment":            {Env: map[string]string{"A": "{{if}}"}},
		"a patch with a url and content":     {Patches: []*v1.Patch{{Url: "http://example/p.diff", Content: "--- a/x\n+++ b/x\n@@ -1 +1 @@\n-a\n+b\n"}}},
		"a patch with neither":               {Patches: []*v1.Patch{{Id: "empty"}}},
		"a patch that is no diff":            {Patches: []*v1.Patch{{Content: "not a diff"}}},
		"two patches of one name":            {Patches: []*v1.Patch{{Id: "p", Url: "http://example/a.diff"}, {Id: "p", Url: "http://example/b.diff"}}},
	} {
		if _, err := Select(rc, gpuProfile(), opts); !errors.Is(err, ErrSelection) {
			t.Errorf("%s should be refused: %v", name, err)
		}
	}
	// The gpu variant's tool is one the recipe's steps call, which a request's own steps need not
	sel, err := Select(rc, gpuProfile(), Options{Variant: "gpu"})
	if err != nil || sel.Err() == nil {
		t.Fatalf("the recipe's steps need the variant's tools: %v %v", sel, err)
	}
	sel, err = Select(rc, gpuProfile(), Options{Variant: "gpu", Steps: []*v1.BuildStep{{Script: "true"}}})
	if err != nil || sel.Err() != nil || len(sel.MissingTools) != 0 {
		t.Fatalf("a request's steps call what they like: %v %v", sel.Err(), err)
	}
	if got := sourceOf(rc, "fork/llama.cpp"); got.Repo != "https://github.com/fork/llama.cpp" || got.Releases != "" || got.Archive != nil {
		t.Fatalf("an owner and name is a GitHub repository: %+v", got)
	}
	for _, repo := range []string{"https://git.example/team/project.git", "git@git.example:team/project.git", "/srv/git/project", "./project/tree"} {
		if got := sourceOf(rc, repo); got.Repo != repo {
			t.Fatalf("%s is cloned as it is named: %+v", repo, got)
		}
	}
	if got := sourceOf(rc, ""); got.Releases != "o/r" {
		t.Fatalf("no repository named leaves the recipe's source: %+v", got)
	}
}

// A build that runs past the request's time limit is stopped
func TestRequestTimeLimitStopsTheBuild(t *testing.T) {
	rc := fakeRecipe{id: "slow", runtimeID: "fake", binary: "bin", timeout: time.Hour}
	e := &Engine{Root: t.TempDir(), Fetcher: transfer.New(0, 0, 0, slog.Default())}
	sel, err := Select(rc, &v1.HostProfile{Facts: map[string]string{}}, Options{Steps: []*v1.BuildStep{{Name: "wait", Script: "sleep 30"}}, Timeout: 200 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	b := sel.Build()
	if err := e.Resolve(context.Background(), sel, b); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	var out bytes.Buffer
	if err := e.Run(context.Background(), sel, b, &out, nil); !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > 10*time.Second {
		t.Fatalf("the limit should end the build: %v after %s", err, time.Since(started))
	}
}
