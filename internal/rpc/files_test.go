package rpc

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/nickheyer/nebu/internal/inspect"
	"github.com/nickheyer/nebu/pkg/cache"
	"github.com/nickheyer/nebu/pkg/formats/all"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/sources"
)

func fileServer(t *testing.T, token string) *httptest.Server {
	t.Helper()
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "org", "model", "sub"), 0o755)
	os.WriteFile(filepath.Join(root, "org", "model", "sub", "notes.txt"), []byte("hello file"), 0o644)
	reg, err := sources.Build([]*v1.Source{{Id: "disk", Kind: v1.SourceKind_SOURCE_KIND_LOCAL, Config: map[string]string{"path": root}}})
	if err != nil {
		t.Fatal(err)
	}
	fmts, err := all.Registry()
	if err != nil {
		t.Fatal(err)
	}
	store, err := cache.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := &files{inspector: &inspect.Inspector{Sources: reg, Formats: fmts, Cache: store, Log: log}, auth: &auth{token: token}, log: log}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func TestFilesStreamsOneFileThroughTheDaemon(t *testing.T) {
	srv := fileServer(t, "")
	resp, err := http.Get(srv.URL + "/files?source=disk&repo=org/model&path=sub/notes.txt")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "hello file" || resp.Header.Get("Content-Disposition") != `attachment; filename=notes.txt` || resp.Header.Get("Content-Length") != "10" {
		t.Fatalf("%d %q %v", resp.StatusCode, body, resp.Header)
	}
	// Ranges resume a download from where it stopped
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/files?source=disk&repo=org/model&path=sub/notes.txt", nil)
	req.Header.Set("Range", "bytes=6-")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent || string(body) != "file" {
		t.Fatalf("range %d %q", resp.StatusCode, body)
	}
	for _, q := range []string{"source=disk&repo=org/model&path=missing.txt", "source=nope&repo=org/model&path=sub/notes.txt", "repo=org/model&path=sub/notes.txt"} {
		resp, err := http.Get(srv.URL + "/files?" + q)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			t.Fatalf("%s should not be served", q)
		}
	}
	resp, _ = http.Post(srv.URL+"/files?source=disk&repo=org/model&path=sub/notes.txt", "text/plain", nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("post %d", resp.StatusCode)
	}
}

// Authenticated downloads require the token.
func TestFilesRequireTheToken(t *testing.T) {
	srv := fileServer(t, "secret")
	get := func(url string, header string) int {
		req, _ := http.NewRequest(http.MethodGet, url, nil)
		if header != "" {
			req.Header.Set("Authorization", header)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	base := srv.URL + "/files?source=disk&repo=org/model&path=sub/notes.txt"
	if get(base, "") != http.StatusUnauthorized || get(base+"&token=wrong", "") != http.StatusUnauthorized {
		t.Fatal("a missing or wrong token is refused")
	}
	if get(base+"&token=secret", "") != http.StatusOK || get(base, "Bearer secret") != http.StatusOK {
		t.Fatal("the token in the link or the header is accepted")
	}
}
