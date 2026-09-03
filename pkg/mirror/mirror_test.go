package mirror

import (
	"path/filepath"
	"testing"
	"time"
)

func TestIndexRoundTrip(t *testing.T) {
	idx := &Index{Repo: "org/name", Commit: "abc", UpdatedAt: time.Unix(100, 0).UTC()}
	idx.Put(File{Path: "b.gguf", Size: 2, Sha256: "bb"})
	idx.Put(File{Path: "a.gguf", Size: 1, Sha256: "aa"})
	idx.Put(File{Path: "b.gguf", Size: 3, Sha256: "cc"})
	if len(idx.Files) != 2 || idx.Files[0].Path != "a.gguf" || idx.Files[1].Size != 3 {
		t.Fatalf("put should replace and sort: %+v", idx.Files)
	}
	path := filepath.Join(t.TempDir(), "sub", IndexFile)
	if err := WriteIndex(path, idx); err != nil {
		t.Fatal(err)
	}
	back, err := ReadIndex(path)
	if err != nil {
		t.Fatal(err)
	}
	if back.Repo != "org/name" || back.Commit != "abc" || len(back.Files) != 2 || back.Files[1].Sha256 != "cc" {
		t.Fatalf("read back %+v", back)
	}
}
