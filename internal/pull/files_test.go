package pull

import (
	"context"
	"strings"
	"testing"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/store"
)

// A lone file lands as a component, later files join it, and the full set makes a model
func TestFilePullsJoinTheGroup(t *testing.T) {
	ctx := context.Background()
	first, second := "tiny-Q4_K_M-00001-of-00002.gguf", "tiny-Q4_K_M-00002-of-00002.gguf"
	files := map[string][]byte{first: fixture(40), second: fixture(41), "README.md": []byte("# tiny")}
	site := newMirror(t, "org/model", files)
	p := testPuller(t, ctx, []*v1.Source{{Id: "mirror", Kind: v1.SourceKind_SOURCE_KIND_MIRROR, Config: map[string]string{"endpoint": site.srv.URL}}})
	task, err := p.Pull(ctx, &v1.PullRequest{SourceId: "mirror", Repo: "org/model", Paths: []string{second}})
	done, logs := finish(t, ctx, p, task, err)
	if done.GetState() != v1.TaskState_TASK_STATE_SUCCEEDED {
		t.Fatalf("%s\n%s", done.GetError(), strings.Join(logs, "\n"))
	}
	if done.GetTitle() != "pull org/model "+second || done.GetLabels()["path"] != second || done.GetLabels()["files"] != "1" || done.GetLabels()["group"] != "Q4_K_M" {
		t.Fatalf("task %s %v", done.GetTitle(), done.GetLabels())
	}
	if !hasLine(logs, "org/model Q4_K_M holds 1 of 2 files, kept as a component until the rest land") {
		t.Fatalf("the partial group is noted:\n%s", strings.Join(logs, "\n"))
	}
	m, err := p.Store.ReadManifest("mirror", "org/model", "Q4_K_M")
	if err != nil {
		t.Fatal(err)
	}
	if len(m.GetArtifacts()) != 1 || m.GetArtifacts()[0].GetArtifact().GetPath() != second || m.GetDescriptor_().GetKind() != v1.ModelKind_MODEL_KIND_COMPONENT || m.GetRuntimes() != 0 || !p.Store.HasBlob(digestOf(files[second])) {
		t.Fatalf("one file as a component: %v", m)
	}
	if p.Store.HasBlob(digestOf(files[first])) {
		t.Fatal("the file not asked for does not land")
	}
	task, err = p.Pull(ctx, &v1.PullRequest{SourceId: "mirror", Repo: "org/model", Paths: []string{first, first}})
	done, logs = finish(t, ctx, p, task, err)
	if done.GetState() != v1.TaskState_TASK_STATE_SUCCEEDED {
		t.Fatalf("%s\n%s", done.GetError(), strings.Join(logs, "\n"))
	}
	m, err = p.Store.ReadManifest("mirror", "org/model", "Q4_K_M")
	if err != nil {
		t.Fatal(err)
	}
	if len(m.GetArtifacts()) != 2 || m.GetBytes() != 81 || m.GetDescriptor_() != nil || m.GetRuntimes() != 0 {
		t.Fatalf("the second file completes the group, with no descriptor to read from the fixture: %v", m)
	}
	for _, sa := range m.GetArtifacts() {
		if !p.Store.HasBlob(sa.GetDigest()) || !strings.HasPrefix(sa.GetPath(), m.GetPath()) {
			t.Fatalf("%s stays linked: %v", sa.GetArtifact().GetPath(), sa)
		}
	}
	for path, want := range map[string]string{
		"README.md":   "README.md is not part of any weight group of org/model",
		"missing.bin": "org/model has no file missing.bin",
	} {
		if _, err := p.Pull(ctx, &v1.PullRequest{SourceId: "mirror", Repo: "org/model", Paths: []string{path}}); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%s: %v, want %s", path, err, want)
		}
	}
	if _, err := p.Pull(ctx, &v1.PullRequest{SourceId: "mirror", Repo: "org/model", Group: "Q8_0", Paths: []string{first}}); err == nil || !strings.Contains(err.Error(), "Q8_0") {
		t.Fatalf("a group the repo lacks is refused: %v", err)
	}
}

// A file pull takes the file from a member before the source and joins the files already stored
func TestFilePullsLandFromAMember(t *testing.T) {
	ctx := context.Background()
	first, second := "tiny-Q4_K_M-00001-of-00002.gguf", "tiny-Q4_K_M-00002-of-00002.gguf"
	files := map[string][]byte{first: fixture(40), second: fixture(41)}
	site := newMirror(t, "org/model", files)
	a := newMember(t, "a")
	a.hold(t, "mirror", "org/model", "Q4_K_M", files, llamaDescriptor("Q4_K_M"))
	p := testPuller(t, ctx, []*v1.Source{{Id: "mirror", Kind: v1.SourceKind_SOURCE_KIND_MIRROR, Config: map[string]string{"endpoint": site.srv.URL}}})
	p.Mesh = &fakeMesh{members: []*member{a}}
	task, err := p.Pull(ctx, &v1.PullRequest{SourceId: "mirror", Repo: "org/model", Paths: []string{first}})
	done, logs := finish(t, ctx, p, task, err)
	if done.GetState() != v1.TaskState_TASK_STATE_SUCCEEDED {
		t.Fatalf("%s\n%s", done.GetError(), strings.Join(logs, "\n"))
	}
	if !hasLine(logs, first+" verified "+digestOf(files[first])+" from a") {
		t.Fatalf("the member serves the file:\n%s", strings.Join(logs, "\n"))
	}
	m, err := p.Store.ReadManifest("mirror", "org/model", "Q4_K_M")
	if err != nil {
		t.Fatal(err)
	}
	if len(m.GetArtifacts()) != 1 || m.GetDescriptor_().GetKind() != v1.ModelKind_MODEL_KIND_COMPONENT || m.GetRuntimes() != 0 {
		t.Fatalf("one file as a component: %v", m)
	}
	task, err = p.Pull(ctx, &v1.PullRequest{SourceId: "mirror", Repo: "org/model", Paths: []string{second}})
	done, logs = finish(t, ctx, p, task, err)
	if done.GetState() != v1.TaskState_TASK_STATE_SUCCEEDED {
		t.Fatalf("%s\n%s", done.GetError(), strings.Join(logs, "\n"))
	}
	m, err = p.Store.ReadManifest("mirror", "org/model", "Q4_K_M")
	if err != nil {
		t.Fatal(err)
	}
	if len(m.GetArtifacts()) != 2 || m.GetDescriptor_().GetKind() == v1.ModelKind_MODEL_KIND_COMPONENT || m.GetRuntimes() == 0 || store.DescriptorDigest(m.GetDescriptor_()) == "" {
		t.Fatalf("the second file completes the group: %v", m)
	}
}
