package archive

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTar(t *testing.T, name string, gz bool, headers []*tar.Header, bodies map[string]string) string {
	t.Helper()
	var buf bytes.Buffer
	var w io.Writer = &buf
	var gzw *gzip.Writer
	if gz {
		gzw = gzip.NewWriter(&buf)
		w = gzw
	}
	tw := tar.NewWriter(w)
	for _, h := range headers {
		body := bodies[h.Name]
		h.Size = int64(len(body))
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if gzw != nil {
		gzw.Close()
	}
	path := filepath.Join(t.TempDir(), name)
	os.WriteFile(path, buf.Bytes(), 0o644)
	return path
}

// Tars regular files holding entries
func tarball(t *testing.T, name string, gz bool, entries map[string]string) string {
	t.Helper()
	var headers []*tar.Header
	for n := range entries {
		headers = append(headers, &tar.Header{Name: n, Mode: 0o755, Typeflag: tar.TypeReg})
	}
	return writeTar(t, name, gz, headers, entries)
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

func TestExtractRefusesWriteThroughSymlink(t *testing.T) {
	out := t.TempDir()
	victim := filepath.Join(filepath.Dir(out), "victim")
	os.Remove(victim)
	archive := writeTar(t, "a.tar", false, []*tar.Header{
		{Name: "lib", Typeflag: tar.TypeSymlink, Linkname: "..", Mode: 0o777},
		{Name: "lib/victim", Typeflag: tar.TypeReg, Mode: 0o644},
	}, map[string]string{"lib/victim": "owned"})
	if err := Extract(archive, out); err == nil {
		t.Fatal("expected an error for a file under an escaping symlink")
	}
	if _, err := os.Stat(victim); err == nil {
		t.Fatal("file was written outside the extraction directory")
	}
}

func TestExtractKeepsInternalSymlink(t *testing.T) {
	out := t.TempDir()
	archive := writeTar(t, "a.tar", false, []*tar.Header{
		{Name: "bin/real", Typeflag: tar.TypeReg, Mode: 0o755},
		{Name: "bin/alias", Typeflag: tar.TypeSymlink, Linkname: "real", Mode: 0o777},
	}, map[string]string{"bin/real": "#!/bin/sh\n"})
	if err := Extract(archive, out); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(out, "bin", "alias"))
	if err != nil || string(got) != "#!/bin/sh\n" {
		t.Fatalf("alias unreadable: %v %q", err, got)
	}
}

func TestExtractRefusesEscapingSymlink(t *testing.T) {
	for _, target := range []string{"/etc/passwd", "../../outside"} {
		out := t.TempDir()
		archive := writeTar(t, "a.tar", false, []*tar.Header{{Name: "link", Typeflag: tar.TypeSymlink, Linkname: target, Mode: 0o777}}, nil)
		if err := Extract(archive, out); err == nil {
			t.Fatalf("expected an error for symlink to %s", target)
		}
	}
}
