package installs

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

func tarball(t *testing.T, entries map[string]string) string {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, content := range entries {
		tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(content)), Typeflag: tar.TypeReg})
		tw.Write([]byte(content))
	}
	tw.Close()
	gz.Close()
	path := filepath.Join(t.TempDir(), "a.tar.gz")
	os.WriteFile(path, buf.Bytes(), 0o644)
	return path
}

func zipfile(t *testing.T, entries map[string]string) string {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range entries {
		w, _ := zw.Create(name)
		w.Write([]byte(content))
	}
	zw.Close()
	path := filepath.Join(t.TempDir(), "a.zip")
	os.WriteFile(path, buf.Bytes(), 0o644)
	return path
}

func TestExtractAndFind(t *testing.T) {
	dir := t.TempDir()
	if err := extract(tarball(t, map[string]string{"pkg/bin/server": "#!/bin/sh\necho hi\n", "pkg/lib/x.so": "lib"}), dir); err != nil {
		t.Fatal(err)
	}
	bin, err := findBinary(dir, "server")
	if err != nil || !strings.HasSuffix(bin, "pkg/bin/server") {
		t.Fatalf("find %q %v", bin, err)
	}
	if err := extract(zipfile(t, map[string]string{"z/server.exe": "x"}), dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "z", "server.exe")); err != nil {
		t.Fatal(err)
	}
	if err := extract(tarball(t, map[string]string{"../escape": "x"}), dir); err == nil {
		t.Fatal("traversal should fail")
	}
	if _, err := findBinary(dir, "missing"); err == nil {
		t.Fatal("missing binary should fail")
	}
}

func TestResolveAsset(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]map[string]any{
			{"tag_name": "b1", "assets": []map[string]any{{"name": "llama-b1-bin-win-x64.zip", "browser_download_url": "u1", "size": 10}}},
			{"tag_name": "b2", "assets": []map[string]any{{"name": "llama-b2-bin-ubuntu-x64.tar.gz", "browser_download_url": "u2", "size": 20}}},
		})
	}))
	defer srv.Close()
	m := &Manager{Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	a, err := m.resolveAsset(context.Background(), &v1.PrebuiltRule{Release: srv.URL, Asset: `^llama-b\d+-bin-ubuntu-x64\.tar\.gz$`})
	if err != nil || a.tag != "b2" || a.url != "u2" || a.size != 20 {
		t.Fatalf("asset %+v %v", a, err)
	}
	if _, err := m.resolveAsset(context.Background(), &v1.PrebuiltRule{Release: srv.URL, Asset: "nope"}); err == nil {
		t.Fatal("no match should fail")
	}
}
