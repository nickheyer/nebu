package rpc

import (
	"io"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/nickheyer/nebu/internal/auth"
)

func TestAuthAcceptsTokenOrSession(t *testing.T) {
	sessions, err := auth.OpenSessions(filepath.Join(t.TempDir(), "session.key"), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	c, err := sessions.Cookie(auth.Session{Subject: "u1", Provider: auth.ProviderLocal, Issued: time.Now(), Expires: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	cookie := c.String()
	withCookie := http.Header{"Cookie": {cookie}}
	withToken := http.Header{"Authorization": {"Bearer tok"}}
	withWrong := http.Header{"Authorization": {"Bearer nope"}, "Cookie": {"nebu_session=garbage"}}

	both := auth.NewGuard("tok", sessions, nil)
	if !both.Authenticated(withCookie) || !both.Authenticated(withToken) || both.Authenticated(withWrong) || both.Authenticated(http.Header{}) {
		t.Fatal("token or session should pass, nothing else")
	}
	only := auth.NewGuard("", sessions, nil)
	if !only.Authenticated(withCookie) || only.Authenticated(withToken) || only.Authenticated(http.Header{}) {
		t.Fatal("with sessions alone only the cookie passes")
	}
	token := auth.NewGuard("tok", nil, nil)
	if !token.Authenticated(withToken) || token.Authenticated(withCookie) {
		t.Fatal("without sessions the cookie means nothing")
	}
	open := auth.NewGuard("", nil, nil)
	if !open.Authenticated(http.Header{}) {
		t.Fatal("auth.disabled should pass everything")
	}
	// The file download path honors the cookie too
	srv := fileServer(t, "tok")
	srv.Config.Handler.(*files).auth = both
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/files?source=disk&repo=org/model&path=sub/notes.txt", nil)
	req.Header.Set("Cookie", cookie)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "hello file" {
		t.Fatalf("files with cookie %d %s", resp.StatusCode, body)
	}
	resp, _ = http.Get(srv.URL + "/files?source=disk&repo=org/model&path=sub/notes.txt")
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized || string(body) != both.Err().Error()+"\n" {
		t.Fatalf("files without auth %d %q", resp.StatusCode, body)
	}
}
