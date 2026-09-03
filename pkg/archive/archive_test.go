package archive

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func tarball(t *testing.T, name string, gz bool, entries map[string]string) string {
	t.Helper()
	var buf bytes.Buffer
	var w interface {
		Write([]byte) (int, error)
	} = &buf
	var gzw *gzip.Writer
	if gz {
		gzw = gzip.NewWriter(&buf)
		w = gzw
	}
	tw := tar.NewWriter(w.(interface{ Write([]byte) (int, error) }))
	for name, content := range entries {
		tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(content)), Typeflag: tar.TypeReg})
		tw.Write([]byte(content))
	}
	tw.Close()
	if gzw != nil {
		gzw.Close()
	}
	path := filepath.Join(t.TempDir(), name)
	os.WriteFile(path, buf.Bytes(), 0o644)
	return path
}

func zipfile(t *testing.T, name string, entries map[string]string) string {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range entries {
		w, _ := zw.Create(name)
		w.Write([]byte(content))
	}
	zw.Close()
	path := filepath.Join(t.TempDir(), name)
	os.WriteFile(path, buf.Bytes(), 0o644)
	return path
}

func TestExtractKinds(t *testing.T) {
	entries := map[string]string{"pkg/bin/server": "#!/bin/sh\necho hi\n", "pkg/lib/x.so": "lib"}
	for _, path := range []string{tarball(t, "a.tar.gz", true, entries), tarball(t, "a.tgz", true, entries), tarball(t, "a.tar", false, entries), zipfile(t, "a.zip", entries), tarball(t, "noext", true, entries), zipfile(t, "noext2", entries)} {
		dir := t.TempDir()
		if err := Extract(path, dir); err != nil {
			t.Fatalf("%s: %v", filepath.Base(path), err)
		}
		if data, err := os.ReadFile(filepath.Join(dir, "pkg", "lib", "x.so")); err != nil || string(data) != "lib" {
			t.Fatalf("%s: content %q %v", filepath.Base(path), data, err)
		}
		if Root(dir) != filepath.Join(dir, "pkg") {
			t.Fatalf("%s: root %s", filepath.Base(path), Root(dir))
		}
	}
}

func TestExtractRejectsTraversal(t *testing.T) {
	dir := t.TempDir()
	if err := Extract(tarball(t, "bad.tar.gz", true, map[string]string{"../escape": "x"}), dir); err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("traversal should fail, got %v", err)
	}
	if err := Extract(zipfile(t, "bad.zip", map[string]string{"../escape": "x"}), dir); err == nil {
		t.Fatal("zip traversal should fail")
	}
}

func TestRootWithSeveralEntries(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a"), []byte("x"), 0o644)
	os.Mkdir(filepath.Join(dir, "b"), 0o755)
	if Root(dir) != dir {
		t.Fatal("root should be dir itself with several entries")
	}
}

func TestUnsupported(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.bin")
	os.WriteFile(path, []byte("not an archive at all"), 0o644)
	if err := Extract(path, t.TempDir()); err == nil {
		t.Fatal("garbage should fail")
	}
}
