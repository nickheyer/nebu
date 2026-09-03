package local

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

func TestLocal(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "org", "tiny", "sub"), 0o755)
	os.WriteFile(filepath.Join(root, "org", "tiny", "a.gguf"), []byte("hello"), 0o644)
	os.WriteFile(filepath.Join(root, "org", "tiny", "sub", "b.txt"), []byte("x"), 0o644)
	src, err := New(&v1.Source{Id: "local", Path: root})
	if err != nil {
		t.Fatal(err)
	}
	hits, err := src.Search(context.Background(), "tiny", nil, 10)
	if err != nil || len(hits) != 1 || hits[0].GetRepo() != "org/tiny" {
		t.Fatalf("hits %v err %v", hits, err)
	}
	model, err := src.Resolve(context.Background(), "org/tiny", "")
	if err != nil || len(model.GetArtifacts()) != 2 || model.GetArtifacts()[0].GetPath() != "a.gguf" || model.GetArtifacts()[0].GetSizeBytes() != 5 {
		t.Fatalf("model %+v err %v", model, err)
	}
	blob, err := src.Open(context.Background(), model, model.GetArtifacts()[0])
	if err != nil || blob.Size() != 5 {
		t.Fatalf("open %v", err)
	}
	buf := make([]byte, 2)
	if _, err := blob.ReadAt(buf, 3); err != nil || string(buf) != "lo" {
		t.Fatalf("read %q %v", buf, err)
	}
	blob.Close()
	if _, err := src.Resolve(context.Background(), "../etc", ""); err == nil {
		t.Fatal("traversal should fail")
	}
	if _, err := New(&v1.Source{Id: "x"}); err == nil {
		t.Fatal("path required")
	}
}
