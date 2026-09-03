package store

import (
	"os"
	"path/filepath"
	"testing"

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
