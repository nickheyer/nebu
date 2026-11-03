package rpc

import (
	"io"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/nickheyer/nebu/internal/auth"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

func TestAuthAcceptsTokenOrSession(t *testing.T) {
	sessions, err := auth.OpenSessions(filepath.Join(t.TempDir(), "session.key"), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	c, err := sessions.Cookie(auth.Session{Subject: "u1", Provider: auth.ProviderOIDC, Role: v1.Role_ROLE_ADMIN, Issued: time.Now(), Expires: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	cookie := c.String()
	withCookie := http.Header{"Cookie": {cookie}}
	withToken := http.Header{"Authorization": {"Bearer tok"}}
	withWrong := http.Header{"Authorization": {"Bearer nope"}, "Cookie": {"nebu_session=garbage"}}

	reads := func(g *auth.Guard, h http.Header) bool { return g.Scoped(h, v1.Scope_SCOPE_READ) }
	both := auth.NewGuard("tok", sessions, nil)
	if !reads(both, withCookie) || !reads(both, withToken) || reads(both, withWrong) || reads(both, http.Header{}) {
		t.Fatal("token or session should pass, nothing else")
	}
	only := auth.NewGuard("", sessions, nil)
	if !reads(only, withCookie) || reads(only, withToken) || reads(only, http.Header{}) {
		t.Fatal("with sessions alone only the cookie passes")
	}
	token := auth.NewGuard("tok", nil, nil)
	if !reads(token, withToken) || reads(token, withCookie) {
		t.Fatal("without sessions the cookie means nothing")
	}
	open := auth.NewGuard("", nil, nil)
	if !reads(open, http.Header{}) {
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
