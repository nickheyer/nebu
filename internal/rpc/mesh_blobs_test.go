package rpc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nickheyer/nebu/internal/auth"
	"github.com/nickheyer/nebu/internal/db"
	"github.com/nickheyer/nebu/pkg/store"
)

// The mesh session table as the guard sees it: one member's token
type onePeer struct{ token, node string }

func (p onePeer) Peer(token string) (string, bool) {
	if token == p.token {
		return p.node, true
	}
	return "", false
}

func blobNode(t *testing.T) (*httptest.Server, *auth.Guard, *auth.Sessions, *auth.Tokens, string, []byte) {
	t.Helper()
	dir := t.TempDir()
	database, err := db.Open(filepath.Join(dir, "nebu.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	st, err := store.Open(filepath.Join(dir, "store"))
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("the quick brown fox jumps over the lazy dog")
	sum := sha256.Sum256(data)
	digest := store.Digest(hex.EncodeToString(sum[:]))
	if err := os.WriteFile(st.BlobPath(digest), data, 0o644); err != nil {
		t.Fatal(err)
	}
	sessions, err := auth.OpenSessions(filepath.Join(dir, "session.key"), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	tokens := auth.NewTokens(database, slog.New(slog.NewTextHandler(io.Discard, nil)))
	guard := auth.NewGuard("daemon-token", sessions, tokens)
	guard.SetPeers(onePeer{token: "member-session", node: "peer"})
	mux := http.NewServeMux()
	mountMesh(mux, guard, st, filepath.Join(dir, "cache"))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, guard, sessions, tokens, digest, data
}

func get(t *testing.T, srv *httptest.Server, path string, set func(*http.Request)) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, srv.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	set(req)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, body
}

// Blob serving needs the member session: the web session, the daemon token, and a user's API
// token do not open it, and a member reads whole blobs and ranges of them
func TestBlobsServePeersAlone(t *testing.T) {
	srv, guard, sessions, tokens, digest, data := blobNode(t)
	cookie, err := sessions.Cookie(auth.Session{Subject: "u1", Name: "User", Provider: auth.ProviderLocal, Expires: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := guard.Identify(http.Header{"Cookie": {cookie.String()}}); !ok {
		t.Fatal("the web session is one the guard accepts for the API")
	}
	apiToken, err := tokens.Create(context.Background(), &auth.Session{Subject: "u1", Name: "User", Provider: auth.ProviderLocal}, "cli")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := guard.Identify(http.Header{"Authorization": {"Bearer " + apiToken.GetSecret()}}); !ok {
		t.Fatal("the API token is one the guard accepts for the API")
	}
	refused := map[string]func(*http.Request){
		"no credential": func(*http.Request) {},
		"web session":   func(r *http.Request) { r.AddCookie(cookie) },
		"daemon token":  func(r *http.Request) { r.Header.Set("Authorization", "Bearer daemon-token") },
		"api token":     func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+apiToken.GetSecret()) },
	}
	for name, set := range refused {
		resp, _ := get(t, srv, blobsPath+digest, set)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s opens blobs: %d", name, resp.StatusCode)
		}
	}
	member := func(r *http.Request) { r.Header.Set("Authorization", "Bearer member-session") }
	resp, body := get(t, srv, blobsPath+digest, member)
	if resp.StatusCode != http.StatusOK || string(body) != string(data) || resp.Header.Get("Accept-Ranges") != "bytes" || resp.Header.Get("Content-Length") != "43" {
		t.Fatalf("member whole read: %d %q %v", resp.StatusCode, body, resp.Header)
	}
	resp, body = get(t, srv, blobsPath+digest, func(r *http.Request) {
		member(r)
		r.Header.Set("Range", "bytes=4-8")
	})
	if resp.StatusCode != http.StatusPartialContent || string(body) != "quick" || resp.Header.Get("Content-Range") != "bytes 4-8/43" {
		t.Fatalf("member range read: %d %q %v", resp.StatusCode, body, resp.Header)
	}
	resp, body = get(t, srv, blobsPath+digest, func(r *http.Request) {
		member(r)
		r.Header.Set("Range", "bytes=40-")
	})
	if resp.StatusCode != http.StatusPartialContent || string(body) != "dog" {
		t.Fatalf("member open ended range: %d %q", resp.StatusCode, body)
	}
	head, err := http.NewRequest(http.MethodHead, srv.URL+blobsPath+digest, nil)
	if err != nil {
		t.Fatal(err)
	}
	member(head)
	hr, err := srv.Client().Do(head)
	if err != nil {
		t.Fatal(err)
	}
	hr.Body.Close()
	if hr.StatusCode != http.StatusOK || hr.Header.Get("Content-Length") != "43" {
		t.Fatalf("head: %d %v", hr.StatusCode, hr.Header)
	}
	for _, path := range []string{blobsPath + "sha256:" + hex.EncodeToString(make([]byte, 32)), blobsPath + "sha256:../../etc/passwd", blobsPath + "abcd"} {
		resp, _ := get(t, srv, path, member)
		if resp.StatusCode == http.StatusOK {
			t.Fatalf("%s is served", path)
		}
	}
}
