package build

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/recipes"
	"github.com/nickheyer/nebu/pkg/sources"
	"github.com/nickheyer/nebu/pkg/transfer"
)

// A registry whose github source reads releases from a fake API
func githubRegistry(t *testing.T, base string) *sources.Registry {
	t.Helper()
	reg, err := sources.Build([]*v1.Source{{Id: "github", Kind: v1.SourceKind_SOURCE_KIND_GITHUB, Config: map[string]string{"endpoint": base}}})
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

// Serves the parts of the GitHub API a latest ref touches
func githubHandler(releases []map[string]any) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/o/r":
			w.Write([]byte(`{"default_branch":"main"}`))
		case r.URL.Path == "/repos/o/r/releases":
			json.NewEncoder(w).Encode(releases)
		case strings.HasPrefix(r.URL.Path, "/repos/o/r/commits/"):
			w.Write([]byte("c0ffee"))
		case r.URL.Path == "/repos/o/r/branches" || r.URL.Path == "/repos/o/r/tags":
			w.Write([]byte("[]"))
		default:
			http.NotFound(w, r)
		}
	}
}

func sourceTarball(t *testing.T, top string, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, content := range files {
		tw.WriteHeader(&tar.Header{Name: top + "/" + name, Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg})
		tw.Write([]byte(content))
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func server(t *testing.T) (*httptest.Server, *int) {
	t.Helper()
	downloads := 0
	tarball := sourceTarball(t, "fake-2.1", map[string]string{"bin.in": "#!/bin/sh\necho $STAMP\n", "notes.txt": "hello\nworld\n"})
	github := githubHandler([]map[string]any{
		{"tag_name": "3.0-rc1", "prerelease": true},
		{"tag_name": "2.1", "prerelease": false},
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/repos/"):
			github(w, r)
		case r.URL.Path == "/fake-2.1.tar.gz":
			if r.Method == http.MethodGet {
				downloads++
			}
			w.Write(tarball)
		case r.URL.Path == "/extra.patch":
			w.Write([]byte("--- a/notes.txt\n+++ b/notes.txt\n@@ -1,3 +1,4 @@\n hello\n patched\n+from url\n world\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &downloads
}

// Test recipe covering refs, variants, optional variables, and sandbox paths.
func engineRecipe(base string) fakeRecipe {
	return fakeRecipe{
		id: "fake", runtimeID: "fake",
		source: recipes.Source{Releases: "o/r", Archive: func(ref string) string { return base + "/fake-" + ref + ".tar.gz" }},
		tools:  []string{"sh"},
		vars:   []recipes.Var{{Name: "unset"}},
		variants: []recipes.Variant{
			{ID: "cpu", Applies: always},
		},
		patches: []recipes.Patch{
			{ID: "inline", Content: []byte("--- a/notes.txt\n+++ b/notes.txt\n@@ -1,2 +1,3 @@\n hello\n+patched\n world\n")},
			{ID: "fromurl", URL: base + "/extra.patch"},
			{ID: "skipped", Applies: func(b *recipes.Build) bool { return b.Variant == "gpu" }, Content: []byte("--- a/x\n+++ b/x\n@@ -0,0 +1 @@\n+never\n")},
		},
		steps: func(b *recipes.Build) []recipes.Step {
			steps := []recipes.Step{
				{Name: "render", Command: []string{"sh", "-c", `sed "s/\$STAMP/$STAMP/" bin.in > bin && chmod +x bin`}, Env: map[string]string{"STAMP": "v" + b.Ref + "-" + b.Variant}},
			}
			if b.Variant == "gpu" {
				steps = append(steps, recipes.Step{Name: "conditional", Command: []string{"false"}})
			}
			steps = append(steps,
				recipes.Step{Name: "dropempty", Command: args("sh", "-c", `test "$#" -eq 0`, b.Vars["unset"])},
				recipes.Step{Name: "outfile", Command: []string{"sh", "-c", "cp notes.txt " + b.Out + "/copied.txt && echo commit=" + b.Commit + " jobs=" + strconv.Itoa(b.Jobs) + " root=" + b.Root + " dir=" + b.Src}},
			)
			return steps
		},
		outputs: []string{"bin", "nothing*.opt"},
		binary:  "bin",
	}
}

func TestEngineBuildsFromArchiveWithPatches(t *testing.T) {
	srv, downloads := server(t)
	rc := engineRecipe(srv.URL)
	root := t.TempDir()
	e := &Engine{Root: root, Jobs: 3, Sources: githubRegistry(t, srv.URL), Fetcher: transfer.New(0, 0, 0, slog.Default())}
	sel, err := Select(rc, &v1.HostProfile{Os: "linux", Arch: "amd64", Facts: map[string]string{}}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if sel.Ref != Latest {
		t.Fatalf("ref before resolve %q", sel.Ref)
	}
	b := sel.Build()
	if err := e.Resolve(context.Background(), sel, b); err != nil {
		t.Fatal(err)
	}
	if b.GetRef() != "2.1" || b.GetVars()["unset"] != "" || strings.Join(b.GetPatches(), ",") != "inline,fromurl" || b.GetId() == "" || b.GetDir() != filepath.Join(root, "fake", b.GetId()) {
		t.Fatalf("resolved %v", b)
	}
	var out bytes.Buffer
	var steps []string
	err = e.Run(context.Background(), sel, b, &out, func(done, total int, name string) { steps = append(steps, name) })
	if err != nil {
		t.Fatalf("run: %v\n%s", err, out.String())
	}
	if b.GetCommit() != "2.1" || !strings.HasSuffix(b.GetBinary(), filepath.Join("out", "bin")) {
		t.Fatalf("build after run %v", b)
	}
	got, _ := exec.Command(b.GetBinary()).Output()
	if strings.TrimSpace(string(got)) != "v2.1-cpu" {
		t.Fatalf("binary output %q", got)
	}
	notes, _ := os.ReadFile(filepath.Join(b.GetDir(), "out", "copied.txt"))
	if string(notes) != "hello\npatched\nfrom url\nworld\n" {
		t.Fatalf("patches applied in order: %q", notes)
	}
	text := out.String()
	for _, want := range []string{"commit=2.1 jobs=3", "root=" + b.GetDir(), "dir=" + filepath.Join(b.GetDir(), "src"), "patch inline applied", "patch fromurl applied"} {
		if !strings.Contains(text, want) {
			t.Errorf("transcript missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "patch skipped") || strings.Contains(text, "conditional") {
		t.Error("the gpu only patch and step must not run on the cpu variant")
	}
	if steps[len(steps)-1] != "done" || len(steps) < 5 {
		t.Errorf("progress %v", steps)
	}
	if *downloads != 1 {
		t.Fatalf("downloads %d", *downloads)
	}
	if err := e.Run(context.Background(), sel, b, &out, nil); err != nil {
		t.Fatal(err)
	}
	if *downloads != 1 {
		t.Fatal("archive cache should be reused")
	}
}

func TestEngineFailsOnStepAndMissingOutput(t *testing.T) {
	srv, _ := server(t)
	rc := fakeRecipe{
		id: "bad", runtimeID: "fake",
		source: recipes.Source{Archive: func(ref string) string { return srv.URL + "/fake-" + ref + ".tar.gz" }},
		steps: func(*recipes.Build) []recipes.Step {
			return []recipes.Step{{Name: "boom", Command: []string{"sh", "-c", "echo failing >&2; exit 3"}}}
		},
		outputs: []string{"bin"},
		binary:  "bin",
	}
	e := &Engine{Root: t.TempDir(), Fetcher: transfer.New(0, 0, 0, slog.Default())}
	// An archive needs a ref, and takes the one given
	sel, _ := Select(rc, &v1.HostProfile{Facts: map[string]string{}}, Options{})
	if err := e.Resolve(context.Background(), sel, sel.Build()); err == nil {
		t.Fatal("an archive without a ref should fail to resolve")
	}
	sel, _ = Select(rc, &v1.HostProfile{Facts: map[string]string{}}, Options{Ref: "2.1"})
	b := sel.Build()
	if err := e.Resolve(context.Background(), sel, b); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err := e.Run(context.Background(), sel, b, &out, nil)
	if err == nil || !strings.Contains(err.Error(), "boom") || !strings.Contains(out.String(), "failing") {
		t.Fatalf("step failure %v %s", err, out.String())
	}
	rc.id = "noout"
	rc.steps = func(*recipes.Build) []recipes.Step { return []recipes.Step{{Name: "ok", Command: []string{"true"}}} }
	sel, _ = Select(rc, &v1.HostProfile{Facts: map[string]string{}}, Options{Ref: "2.1"})
	b = sel.Build()
	e.Resolve(context.Background(), sel, b)
	if err := e.Run(context.Background(), sel, b, &out, nil); err == nil || !strings.Contains(err.Error(), "not produced") {
		t.Fatalf("missing output should fail: %v", err)
	}
	rc.id = "empty"
	rc.steps = func(*recipes.Build) []recipes.Step { return []recipes.Step{{Name: "empty"}} }
	sel, _ = Select(rc, &v1.HostProfile{Facts: map[string]string{}}, Options{Ref: "2.1"})
	b = sel.Build()
	e.Resolve(context.Background(), sel, b)
	if err := e.Run(context.Background(), sel, b, &out, nil); err == nil || !strings.Contains(err.Error(), "empty command") {
		t.Fatalf("an empty command should fail: %v", err)
	}
}

func TestEngineNoSourceAndPatchURLMissing(t *testing.T) {
	rc := fakeRecipe{
		id: "nosrc", runtimeID: "fake",
		steps: func(b *recipes.Build) []recipes.Step {
			return []recipes.Step{{Command: []string{"sh", "-c", "mkdir -p " + b.Out + "/v && printf '#!/bin/sh\\necho ok\\n' > " + b.Out + "/v/tool && chmod +x " + b.Out + "/v/tool"}}}
		},
		binary: "v/tool",
	}
	e := &Engine{Root: t.TempDir(), Fetcher: transfer.New(0, 0, 0, slog.Default())}
	sel, _ := Select(rc, &v1.HostProfile{Facts: map[string]string{}}, Options{})
	b := sel.Build()
	if err := e.Resolve(context.Background(), sel, b); err != nil {
		t.Fatal(err)
	}
	if err := e.Run(context.Background(), sel, b, &bytes.Buffer{}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(b.GetBinary()); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	missing := fakeRecipe{
		id: "mp", runtimeID: "fake",
		patches: []recipes.Patch{{ID: "p", URL: srv.URL + "/nope.patch"}},
		steps:   func(*recipes.Build) []recipes.Step { return []recipes.Step{{Command: []string{"true"}}} },
		binary:  "b",
	}
	sel, _ = Select(missing, &v1.HostProfile{Facts: map[string]string{}}, Options{})
	b = sel.Build()
	if err := e.Resolve(context.Background(), sel, b); err != nil {
		t.Fatal(err)
	}
	if err := e.Run(context.Background(), sel, b, &bytes.Buffer{}, nil); err == nil || !strings.Contains(err.Error(), "patch p") {
		t.Fatalf("a patch that cannot be fetched should fail by name: %v", err)
	}
	// A build must be resolved before it runs
	if err := e.Run(context.Background(), sel, sel.Build(), &bytes.Buffer{}, nil); err == nil {
		t.Fatal("an unresolved build should fail")
	}
}

func TestEngineGitSource(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	repo := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(repo, "tool.sh"), []byte("#!/bin/sh\necho git\n"), 0o755)
	run("add", ".")
	run("commit", "-q", "-m", "one")
	run("tag", "v1")
	rc := fakeRecipe{
		id: "g", runtimeID: "fake",
		source: recipes.Source{Repo: repo},
		steps: func(*recipes.Build) []recipes.Step {
			return []recipes.Step{{Command: []string{"cp", "tool.sh", "tool"}}}
		},
		outputs: []string{"tool"},
		binary:  "tool",
	}
	e := &Engine{Root: t.TempDir()}
	sel, _ := Select(rc, &v1.HostProfile{Facts: map[string]string{}}, Options{Ref: "v1"})
	b := sel.Build()
	if err := e.Resolve(context.Background(), sel, b); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := e.Run(context.Background(), sel, b, &out, nil); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if len(b.GetCommit()) != 40 {
		t.Fatalf("commit %q", b.GetCommit())
	}
}

func TestCacheNameAndLatestTag(t *testing.T) {
	if !strings.HasSuffix(cacheName("https://x/y/z.tar.gz?x=1"), ".tar.gz") || !strings.HasSuffix(cacheName("https://x/p.patch"), ".patch") || strings.Contains(cacheName("https://x/plain"), ".") {
		t.Fatal("cache names")
	}
	srv := httptest.NewServer(githubHandler([]map[string]any{{"tag_name": "d", "draft": true}, {"tag_name": "p", "prerelease": true}}))
	defer srv.Close()
	reg := githubRegistry(t, srv.URL)
	if tag, err := latestTag(context.Background(), reg, "github", "o/r"); err != nil || tag != "d" {
		t.Fatalf("fallback %q %v", tag, err)
	}
	if _, err := latestTag(context.Background(), reg, "nope", "o/r"); err == nil {
		t.Fatal("unknown source should fail")
	}
	if _, err := latestTag(context.Background(), nil, "github", "o/r"); err == nil {
		t.Fatal("no registry should fail")
	}
}
