package store

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

func TestBlobsAndLinks(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	digest := Digest("ABCD")
	if digest != "sha256:abcd" || Hex(digest) != "abcd" || s.HasBlob(digest) {
		t.Fatal("digest helpers")
	}
	partial := s.PartialPath("sha256-abcd")
	os.WriteFile(partial, []byte("data"), 0o644)
	if err := s.Commit(partial, digest); err != nil || !s.HasBlob(digest) {
		t.Fatalf("commit %v", err)
	}
	link, err := s.Link("src", "org/repo", "Q4", "sub/model.gguf", digest)
	if err != nil {
		t.Fatal(err)
	}
	target, err := os.Readlink(link)
	if err != nil || filepath.IsAbs(target) {
		t.Fatalf("link should be relative: %q %v", target, err)
	}
	if data, err := os.ReadFile(link); err != nil || string(data) != "data" {
		t.Fatalf("link resolves: %q %v", data, err)
	}
	if again, err := s.Link("src", "org/repo", "Q4", "sub/model.gguf", digest); err != nil || again != link {
		t.Fatal("relink should be idempotent")
	}
	if _, err := s.Link("src", "org/repo", "Q4", "../escape", digest); err == nil {
		t.Fatal("traversal should fail")
	}
	src := filepath.Join(t.TempDir(), "local.bin")
	os.WriteFile(src, []byte("local"), 0o644)
	other := Digest("ffff")
	if err := s.Adopt(src, other); err != nil || !s.HasBlob(other) {
		t.Fatalf("adopt %v", err)
	}
}

func TestManifestsGcStatus(t *testing.T) {
	s, _ := Open(t.TempDir())
	blobA, blobB := Digest("aaaa"), Digest("bbbb")
	for _, d := range []string{blobA, blobB} {
		os.WriteFile(s.BlobPath(d), []byte("xx"), 0o644)
	}
	os.WriteFile(s.PartialPath("sha256-cccc"), []byte("partial"), 0o644)
	link, _ := s.Link("src", "org/repo", "Q4", "m.gguf", blobA)
	m := &v1.StoredModel{SourceId: "src", Repo: "org/repo", Group: "Q4", Bytes: 2, Artifacts: []*v1.StoredArtifact{{Digest: blobA, Path: link, Artifact: &v1.Artifact{Path: "m.gguf"}}}}
	if err := s.WriteManifest(m); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadManifest("src", "org/repo", "nope"); err == nil {
		t.Fatal("missing manifest should fail")
	}
	got, err := s.ReadManifest("src", "org/repo", "Q4")
	if err != nil || got.GetArtifacts()[0].GetDigest() != blobA {
		t.Fatalf("read %v %v", got, err)
	}
	list, _ := s.ListManifests()
	if len(list) != 1 {
		t.Fatalf("list %d", len(list))
	}
	st, _ := s.Status()
	if st.GetModels() != 1 || st.GetBlobs() != 2 || st.GetPartials() != 1 || st.GetBlobBytes() != 4 {
		t.Fatalf("status %+v", st)
	}
	gc, err := s.Gc(false)
	if err != nil || gc.GetRemoved() != 1 || gc.GetFreedBytes() != 2 || s.HasBlob(blobB) || !s.HasBlob(blobA) {
		t.Fatalf("gc %+v %v", gc, err)
	}
	if _, err := os.Stat(s.PartialPath("sha256-cccc")); err != nil {
		t.Fatal("partial should survive plain gc")
	}
	gc, _ = s.Gc(true)
	if gc.GetRemoved() != 1 {
		t.Fatalf("partial gc %+v", gc)
	}
	removed, err := s.RemoveManifest("src", "org/repo", "Q4")
	if err != nil || removed.GetGroup() != "Q4" {
		t.Fatalf("remove %v", err)
	}
	if _, err := os.Lstat(link); !os.IsNotExist(err) {
		t.Fatal("links should be removed with the manifest")
	}
	if entries, _ := os.ReadDir(filepath.Join(s.Root(), modelsDir)); len(entries) != 0 {
		t.Fatal("empty model dirs should be pruned")
	}
	gc, _ = s.Gc(false)
	if gc.GetRemoved() != 1 || s.HasBlob(blobA) {
		t.Fatal("orphaned blob should be collected")
	}
}

func TestLock(t *testing.T) {
	s, _ := Open(t.TempDir())
	unlock := s.Lock("k")
	done := make(chan struct{})
	go func() {
		u := s.Lock("k")
		u()
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("second lock should wait")
	default:
	}
	unlock()
	<-done
}

// A key's lock leaves the table once its last holder lets go, and waiters keep it there meanwhile
func TestLockForgotten(t *testing.T) {
	s, _ := Open(t.TempDir())
	unlock := s.Lock("k")
	waited := make(chan struct{})
	go func() {
		u := s.Lock("k")
		u()
		close(waited)
	}()
	for {
		s.mu.Lock()
		waiting := s.locks["k"] != nil && s.locks["k"].holders == 2
		s.mu.Unlock()
		if waiting {
			break
		}
		time.Sleep(time.Millisecond)
	}
	unlock()
	<-waited
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.locks) != 0 {
		t.Fatalf("%d locks kept after every holder let go", len(s.locks))
	}
}

// Every write and removal keeps the index current, a second store reading the same from disk
func TestManifestIndex(t *testing.T) {
	s, _ := Open(t.TempDir())
	first := &v1.StoredModel{SourceId: "hf", Repo: "org/a", Group: "q4", Bytes: 3, Artifacts: []*v1.StoredArtifact{{Digest: Digest("aa"), Path: "a.gguf"}}}
	if err := s.WriteManifest(first); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListManifests()
	if err != nil || len(list) != 1 || list[0].GetRepo() != "org/a" {
		t.Fatalf("list %v %v", list, err)
	}
	// A copy handed out does not reach the index
	list[0].Bytes = 99
	if got, _ := s.ReadManifest("hf", "org/a", "q4"); got.GetBytes() != 3 {
		t.Fatalf("the index was changed through a copy: %v", got)
	}
	second := &v1.StoredModel{SourceId: "hf", Repo: "org/b", Group: "q8", Bytes: 5}
	if err := s.WriteManifest(second); err != nil {
		t.Fatal(err)
	}
	second.Bytes = 7
	if got, _ := s.ReadManifest("hf", "org/b", "q8"); got.GetBytes() != 5 {
		t.Fatalf("the index holds what was written, not what the caller changed later: %v", got)
	}
	if list, _ = s.ListManifests(); len(list) != 2 || list[1].GetRepo() != "org/b" {
		t.Fatalf("list after a second write %v", list)
	}
	if st, _ := s.Status(); st.GetModels() != 2 {
		t.Fatalf("status counts %d models", st.GetModels())
	}
	if _, err := s.Touch("hf", "org/a", "q4"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.ReadManifest("hf", "org/a", "q4"); got.GetUsedAt() == nil {
		t.Fatal("a touch reaches the index")
	}
	if got, _ := s.RemoveManifest("hf", "org/a", "q4"); got.GetRepo() != "org/a" {
		t.Fatalf("removed %v", got)
	}
	if _, err := s.ReadManifest("hf", "org/a", "q4"); !errors.Is(err, ErrNotStored) {
		t.Fatalf("read after removal: %v", err)
	}
	if list, _ = s.ListManifests(); len(list) != 1 || list[0].GetRepo() != "org/b" {
		t.Fatalf("list after removal %v", list)
	}
	// A store opened over the same directory reads what the first wrote
	again, _ := Open(s.Root())
	if list, _ = again.ListManifests(); len(list) != 1 || list[0].GetRepo() != "org/b" || list[0].GetBytes() != 5 {
		t.Fatalf("list from disk %v", list)
	}
}
