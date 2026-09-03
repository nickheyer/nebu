package archive

import (
	"archive/tar"
	"os"
	"path/filepath"
	"testing"
)

func writeTar(t *testing.T, entries []*tar.Header, bodies map[string]string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "a.tar")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	tw := tar.NewWriter(f)
	for _, h := range entries {
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
	f.Close()
	return path
}

// A symlink to a parent followed by a file under it must not write outside dir
func TestExtractRefusesWriteThroughSymlink(t *testing.T) {
	out := t.TempDir()
	victim := filepath.Join(filepath.Dir(out), "victim")
	os.Remove(victim)
	archive := writeTar(t, []*tar.Header{
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

// A symlink whose target stays inside dir is fine
func TestExtractKeepsInternalSymlink(t *testing.T) {
	out := t.TempDir()
	archive := writeTar(t, []*tar.Header{
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

// An absolute or escaping link target is refused outright
func TestExtractRefusesEscapingSymlink(t *testing.T) {
	for _, target := range []string{"/etc/passwd", "../../outside"} {
		out := t.TempDir()
		archive := writeTar(t, []*tar.Header{{Name: "link", Typeflag: tar.TypeSymlink, Linkname: target, Mode: 0o777}}, nil)
		if err := Extract(archive, out); err == nil {
			t.Fatalf("expected an error for symlink to %s", target)
		}
	}
}
