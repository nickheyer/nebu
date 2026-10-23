package rpc

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nickheyer/nebu/internal/auth"
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
	h := &files{inspector: &inspect.Inspector{Sources: reg, Formats: fmts, Cache: store, Log: log}, auth: auth.NewGuard(token, nil, nil), log: log}
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
	// A link request without a JSON body is refused, and other methods are not served
	resp, _ = http.Post(srv.URL+"/files?source=disk&repo=org/model&path=sub/notes.txt", "text/plain", nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("post %d", resp.StatusCode)
	}
	req, _ = http.NewRequest(http.MethodPut, srv.URL+"/files", nil)
	resp, _ = http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("put %d", resp.StatusCode)
	}
}

// Authenticated downloads carry the token in a header, or a link the token asked for
func TestFilesRequireTheTokenOrALink(t *testing.T) {
	srv := fileServer(t, "secret")
	call := func(method, url, header, body string) (int, string) {
		req, _ := http.NewRequest(method, url, strings.NewReader(body))
		if header != "" {
			req.Header.Set("Authorization", header)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		out, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp.StatusCode, string(out)
	}
	get := func(url, header string) int {
		code, _ := call(http.MethodGet, url, header, "")
		return code
	}
	base := srv.URL + "/files?source=disk&repo=org/model&path=sub/notes.txt"
	if get(base, "") != http.StatusUnauthorized || get(base+"&token=secret", "") != http.StatusUnauthorized || get(base+"&grant=forged", "") != http.StatusUnauthorized {
		t.Fatal("a missing token, a token in the query, and a forged link are refused")
	}
	if get(base, "Bearer secret") != http.StatusOK {
		t.Fatal("the token in the header is accepted")
	}
	ask := `{"source":"disk","repo":"org/model","path":"sub/notes.txt"}`
	if code, _ := call(http.MethodPost, srv.URL+"/files", "", ask); code != http.StatusUnauthorized {
		t.Fatalf("a link without the token %d", code)
	}
	code, body := call(http.MethodPost, srv.URL+"/files", "Bearer secret", ask)
	var link struct {
		URL string `json:"url"`
	}
	if code != http.StatusOK || json.Unmarshal([]byte(body), &link) != nil || !strings.HasPrefix(link.URL, "/files?") {
		t.Fatalf("link %d %s", code, body)
	}
	if get(srv.URL+link.URL, "") != http.StatusOK {
		t.Fatal("the link downloads without a header")
	}
	if get(strings.Replace(srv.URL+link.URL, "notes.txt", "other.txt", 1), "") != http.StatusUnauthorized {
		t.Fatal("the link opens only the file it was made for")
	}
}
