package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nickheyer/nebu/internal/db"
)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func openUsers(t *testing.T) (*Users, *Sessions) {
	t.Helper()
	dir := t.TempDir()
	store, err := db.Open(filepath.Join(dir, "nebu.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	users := NewUsers(store, quiet())
	if err := users.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	sessions, err := OpenSessions(filepath.Join(dir, "session.key"), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	sessions.Validate(users.Valid)
	return users, sessions
}

func TestSessionsSurviveRestartExpireAndBindPurpose(t *testing.T) {
	keyFile := filepath.Join(t.TempDir(), "session.key")
	first, err := OpenSessions(keyFile, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	sess, err := first.Issue(rec, req, Session{Subject: "u1", Name: "ann", Provider: ProviderLocal})
	if err != nil {
		t.Fatal(err)
	}
	if sess.Expires.Sub(sess.Issued) != time.Hour {
		t.Fatalf("lifetime %s", sess.Expires.Sub(sess.Issued))
	}
	set := rec.Header().Get("Set-Cookie")
	if !strings.Contains(set, "HttpOnly") || !strings.Contains(set, "Secure") || !strings.Contains(set, "SameSite=Lax") || !strings.Contains(set, "Path=/") {
		t.Fatalf("cookie attributes %s", set)
	}
	h := http.Header{"Cookie": {strings.Split(set, ";")[0]}}
	second, err := OpenSessions(keyFile, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := second.Session(h)
	if !ok || got.Name != "ann" || got.Provider != ProviderLocal {
		t.Fatalf("session after restart %v %v", got, ok)
	}
	second.now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	if second.Authenticated(h) {
		t.Fatal("expired session should not authenticate")
	}
	if second.Authenticated(http.Header{"Cookie": {"nebu_session=garbage"}}) {
		t.Fatal("garbage should not authenticate")
	}
	value := strings.TrimPrefix(strings.Split(set, ";")[0], sessionCookie+"=")
	var other Session
	if err := first.Open("other-purpose", value, &other); err == nil {
		t.Fatal("a value sealed for sessions opened for another purpose")
	}
	first.Validate(func(*Session) bool { return false })
	if first.Authenticated(h) {
		t.Fatal("a validator saying no should revoke the session")
	}
	if _, err := OpenSessions(filepath.Join(t.TempDir(), "missing", "k"), time.Hour); err == nil {
		t.Fatal("unwritable key path should fail")
	}
	if _, err := OpenSessions(keyFile, 0); err == nil {
		t.Fatal("zero lifetime should fail")
	}
}

func TestPasswords(t *testing.T) {
	hash, err := HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, "$argon2id$v=19$m=19456,t=2,p=1$") {
		t.Fatalf("hash form %s", hash)
	}
	if !VerifyPassword(hash, "correct horse battery") || VerifyPassword(hash, "correct horse batter") || VerifyPassword("garbage", "x") || VerifyPassword("$argon2id$v=19$m=1,t=1,p=1$a$b", "x") {
		t.Fatal("verify")
	}
	if _, err := HashPassword("short"); err == nil {
		t.Fatal("short passwords are refused")
	}
	if _, err := HashPassword(strings.Repeat("x", MaxPassword+1)); err == nil {
		t.Fatal("huge passwords are refused")
	}
	again, _ := HashPassword("correct horse battery")
	if again == hash {
		t.Fatal("salts should differ")
	}
}

func TestUsersLifecycleAndSessionRevocation(t *testing.T) {
	users, sessions := openUsers(t)
	ctx := context.Background()
	if users.Count() != 0 {
		t.Fatal("fresh store has no accounts")
	}
	ann, err := users.Create(ctx, "  Ann@Example.com ", "ann-password")
	if err != nil {
		t.Fatal(err)
	}
	if ann.GetUsername() != "ann@example.com" || ann.GetId() == "" || users.Count() != 1 {
		t.Fatalf("created %v", ann)
	}
	for _, bad := range []struct{ name, pw string }{{"ann@example.com", "another-pass"}, {"", "x-password"}, {"has space", "x-password"}, {strings.Repeat("a", 65), "x-password"}, {"bob", "short"}} {
		if _, err := users.Create(ctx, bad.name, bad.pw); err == nil {
			t.Fatalf("%q/%q should be refused", bad.name, bad.pw)
		}
	}
	if _, err := users.Verify(ctx, "ANN@example.com", "ann-password"); err != nil {
		t.Fatal("case-insensitive sign-in", err)
	}
	if _, err := users.Verify(ctx, "ann@example.com", "wrong-password"); err != ErrCredentials {
		t.Fatalf("wrong password: %v", err)
	}
	if _, err := users.Verify(ctx, "nobody", "ann-password"); err != ErrCredentials {
		t.Fatalf("unknown user: %v", err)
	}
	// A session issued before a password change is revoked, one issued after works
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	old, _ := sessions.Issue(rec, req, Session{Subject: ann.GetId(), Name: ann.GetUsername(), Provider: ProviderLocal})
	oldHeader := http.Header{"Cookie": {strings.Split(rec.Header().Get("Set-Cookie"), ";")[0]}}
	if !sessions.Authenticated(oldHeader) {
		t.Fatal("fresh session should authenticate")
	}
	users.now = func() time.Time { return old.Issued.Add(2 * time.Second) }
	if _, err := users.SetPassword(ctx, "ann@example.com", "new-password"); err != nil {
		t.Fatal(err)
	}
	if sessions.Authenticated(oldHeader) {
		t.Fatal("password change should revoke earlier sessions")
	}
	sessions.now = func() time.Time { return old.Issued.Add(3 * time.Second) }
	rec = httptest.NewRecorder()
	sessions.Issue(rec, req, Session{Subject: ann.GetId(), Name: ann.GetUsername(), Provider: ProviderLocal})
	newHeader := http.Header{"Cookie": {strings.Split(rec.Header().Get("Set-Cookie"), ";")[0]}}
	if !sessions.Authenticated(newHeader) {
		t.Fatal("session after the change should authenticate")
	}
	if _, err := users.Verify(ctx, "ann@example.com", "new-password"); err != nil {
		t.Fatal("new password", err)
	}
	// OIDC sessions are not subject to account checks
	rec = httptest.NewRecorder()
	sessions.Issue(rec, req, Session{Subject: "ext", Name: "someone", Provider: ProviderOIDC})
	if !sessions.Authenticated(http.Header{"Cookie": {strings.Split(rec.Header().Get("Set-Cookie"), ";")[0]}}) {
		t.Fatal("sso sessions are validated elsewhere")
	}
	if err := users.Delete(ctx, "ann@example.com"); err != ErrLastUser {
		t.Fatalf("last account: %v", err)
	}
	if _, err := users.Create(ctx, "bob", "bob-password"); err != nil {
		t.Fatal(err)
	}
	if err := users.Delete(ctx, "ann@example.com"); err != nil {
		t.Fatal(err)
	}
	if sessions.Authenticated(newHeader) {
		t.Fatal("deleting the account should revoke its sessions")
	}
	if err := users.Delete(ctx, "ann@example.com"); err == nil {
		t.Fatal("deleting twice should fail")
	}
	if _, err := users.SetPassword(ctx, "nobody", "whatever-pass"); err == nil {
		t.Fatal("password for unknown account")
	}
	list, _ := users.List(ctx)
	if len(list) != 1 || list[0].GetUsername() != "bob" {
		t.Fatalf("list %v", list)
	}
	// A fresh Users over the same database sees the accounts
	reloaded := NewUsers(users.db, quiet())
	if err := reloaded.Load(ctx); err != nil || reloaded.Count() != 1 {
		t.Fatalf("reload %v %d", err, reloaded.Count())
	}
}

func TestLoginThrottling(t *testing.T) {
	users, _ := openUsers(t)
	ctx := context.Background()
	if _, err := users.Create(ctx, "ann", "ann-password"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < userAttempts; i++ {
		if _, err := users.Login(ctx, "10.0.0.1", "ann", "wrong-password"); err != ErrCredentials {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	if _, err := users.Login(ctx, "10.0.0.1", "ann", "ann-password"); err != ErrThrottled {
		t.Fatalf("after %d failures the account is throttled, got %v", userAttempts, err)
	}
	if _, err := users.Login(ctx, "10.0.0.2", "ann", "ann-password"); err != ErrThrottled {
		t.Fatal("throttling follows the account, not only the client")
	}
	users.now = func() time.Time { return time.Now().Add(attemptWindow + time.Minute) }
	if _, err := users.Login(ctx, "10.0.0.1", "ann", "ann-password"); err != nil {
		t.Fatalf("window passed: %v", err)
	}
	if _, err := users.Login(ctx, "10.0.0.1", "ann", "wrong-password"); err != ErrCredentials {
		t.Fatal("success clears the count")
	}
	// Spraying many names from one client hits the client limit
	users.now = time.Now
	for i := 0; i < clientAttempts; i++ {
		users.Login(ctx, "10.0.0.9", fmt.Sprintf("ghost%d", i), "x-password")
	}
	if _, err := users.Login(ctx, "10.0.0.9", "ann", "ann-password"); err != ErrThrottled {
		t.Fatalf("client throttle: %v", err)
	}
}

type view struct {
	Enabled bool
	SSO     *struct{ Name string }
	Local   *struct{ Setup bool }
	User    *struct{ Subject, Name, Provider string }
}

func serve(t *testing.T, o Options) (*httptest.Server, *http.Client) {
	t.Helper()
	srv := httptest.NewServer(Handler(o))
	t.Cleanup(srv.Close)
	jar, _ := cookiejar.New(nil)
	return srv, &http.Client{Jar: jar}
}

func post(t *testing.T, client *http.Client, u, kind string, body any) (int, map[string]any) {
	t.Helper()
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, u, bytes.NewReader(raw))
	req.Header.Set("Content-Type", kind)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out := map[string]any{}
	json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func look(t *testing.T, client *http.Client, base string) view {
	t.Helper()
	resp, err := client.Get(base + "/auth/session")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var v view
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestLocalSetupLoginAndLogout(t *testing.T) {
	users, sessions := openUsers(t)
	srv, client := serve(t, Options{Sessions: sessions, Users: users, Log: quiet()})
	if v := look(t, client, srv.URL); !v.Enabled || v.SSO != nil || v.Local == nil || !v.Local.Setup || v.User != nil {
		t.Fatalf("fresh status %+v", v)
	}
	if code, _ := post(t, client, srv.URL+"/auth/local/login", "application/json", map[string]string{"username": "ann", "password": "ann-password"}); code != http.StatusUnauthorized {
		t.Fatalf("login before setup %d", code)
	}
	if code, out := post(t, client, srv.URL+"/auth/local/setup", "application/json", map[string]string{"username": "ann", "password": "short"}); code != http.StatusBadRequest || out["error"] == "" {
		t.Fatalf("weak setup %d %v", code, out)
	}
	if code, _ := post(t, client, srv.URL+"/auth/local/setup", "text/plain", map[string]string{"username": "ann", "password": "ann-password"}); code != http.StatusUnsupportedMediaType {
		t.Fatalf("form posts are refused, got %d", code)
	}
	code, out := post(t, client, srv.URL+"/auth/local/setup", "application/json", map[string]string{"username": "Ann", "password": "ann-password"})
	if code != http.StatusOK || out["user"].(map[string]any)["name"] != "ann" {
		t.Fatalf("setup %d %v", code, out)
	}
	if v := look(t, client, srv.URL); v.User == nil || v.User.Name != "ann" || v.User.Provider != ProviderLocal || v.Local.Setup {
		t.Fatalf("status after setup %+v", v)
	}
	if code, _ := post(t, client, srv.URL+"/auth/local/setup", "application/json", map[string]string{"username": "eve", "password": "eve-password"}); code != http.StatusConflict {
		t.Fatalf("second setup %d", code)
	}
	if code, _ := post(t, client, srv.URL+"/auth/logout", "", nil); code != http.StatusNoContent {
		t.Fatalf("logout %d", code)
	}
	if v := look(t, client, srv.URL); v.User != nil {
		t.Fatal("still signed in after logout")
	}
	if code, out := post(t, client, srv.URL+"/auth/local/login", "application/json", map[string]string{"username": "ann", "password": "nope-password"}); code != http.StatusUnauthorized || out["error"] != ErrCredentials.Error() {
		t.Fatalf("wrong password %d %v", code, out)
	}
	if code, _ := post(t, client, srv.URL+"/auth/local/login", "application/json", map[string]string{"username": "ann", "password": "ann-password"}); code != http.StatusOK {
		t.Fatalf("login %d", code)
	}
	if v := look(t, client, srv.URL); v.User == nil || v.User.Name != "ann" {
		t.Fatalf("status after login %+v", v)
	}
	for i := 0; i < userAttempts; i++ {
		post(t, client, srv.URL+"/auth/local/login", "application/json", map[string]string{"username": "ann", "password": "nope-password"})
	}
	if code, _ := post(t, client, srv.URL+"/auth/local/login", "application/json", map[string]string{"username": "ann", "password": "ann-password"}); code != http.StatusTooManyRequests {
		t.Fatalf("throttled login %d", code)
	}
	resp, _ := client.Get(srv.URL + "/auth/oidc/login")
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("sso off %d", resp.StatusCode)
	}
}

func TestHandlerWithAuthOffOrSSO(t *testing.T) {
	srv, client := serve(t, Options{Log: quiet()})
	if v := look(t, client, srv.URL); v.Enabled || v.SSO != nil || v.Local != nil || v.User != nil {
		t.Fatalf("disabled status %+v", v)
	}
	if code, _ := post(t, client, srv.URL+"/auth/local/login", "application/json", map[string]string{"username": "a", "password": "b"}); code != http.StatusNotFound {
		t.Fatalf("login with accounts off %d", code)
	}
	if code, _ := post(t, client, srv.URL+"/auth/logout", "", nil); code != http.StatusNoContent {
		t.Fatalf("logout with auth off %d", code)
	}
	sessions, _ := OpenSessions(filepath.Join(t.TempDir(), "k"), time.Hour)
	stub := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("sso " + r.URL.Path)) })
	srv2, client2 := serve(t, Options{Sessions: sessions, SSO: "Okta", SSOHandler: stub, Log: quiet()})
	if v := look(t, client2, srv2.URL); !v.Enabled || v.SSO == nil || v.SSO.Name != "Okta" || v.Local != nil {
		t.Fatalf("sso status %+v", v)
	}
	resp, _ := client2.Get(srv2.URL + "/auth/oidc/login")
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "sso /auth/oidc/login" {
		t.Fatalf("sso routes %s", body)
	}
	if code, _ := post(t, client2, srv2.URL+"/auth/local/setup", "application/json", map[string]string{"username": "a", "password": "b-password"}); code != http.StatusNotFound {
		t.Fatalf("setup with sso %d", code)
	}
}

func TestTokenFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "api.token")
	first, created, err := LoadOrCreateToken(path)
	if err != nil || !created || len(first) < 40 {
		t.Fatalf("create %q %v %v", first, created, err)
	}
	again, created, err := LoadOrCreateToken(path)
	if err != nil || created || again != first {
		t.Fatalf("reload %q %v %v", again, created, err)
	}
	if _, _, err := LoadOrCreateToken(filepath.Join(t.TempDir(), "nope", "api.token")); err == nil {
		t.Fatal("unwritable path should fail")
	}
}

func TestGuardAndUserTokens(t *testing.T) {
	users, sessions := openUsers(t)
	tokens := NewTokens(users.db, quiet())
	guard := NewGuard("daemon-token", sessions, tokens)
	ctx := context.Background()
	ann, err := users.Create(ctx, "ann", "ann-password")
	if err != nil {
		t.Fatal(err)
	}
	me := &Session{Subject: ann.GetId(), Name: "ann", Provider: ProviderLocal}
	if _, err := tokens.Create(ctx, me, ""); !errors.Is(err, ErrToken) {
		t.Fatalf("blank name %v", err)
	}
	if _, err := tokens.Create(ctx, me, strings.Repeat("n", maxTokenName+1)); !errors.Is(err, ErrToken) {
		t.Fatalf("long name %v", err)
	}
	tok, err := tokens.Create(ctx, me, " laptop ")
	if err != nil {
		t.Fatal(err)
	}
	if tok.GetName() != "laptop" || len(tok.GetSecret()) < 40 || tok.GetId() == "" {
		t.Fatalf("token %v", tok)
	}
	bearer := func(secret string) http.Header { return http.Header{"Authorization": {"Bearer " + secret}} }
	if !guard.Enabled() || guard.Authenticated(http.Header{}) || guard.Authenticated(bearer("wrong")) {
		t.Fatal("nothing or a wrong token should be refused")
	}
	if who, ok := guard.Identify(bearer("daemon-token")); !ok || who != nil {
		t.Fatalf("daemon token identifies nobody %v %v", who, ok)
	}
	if who, ok := guard.Identify(http.Header{"Authorization": {"BEARER " + tok.GetSecret()}}); !ok || who.Subject != ann.GetId() || who.Name != "ann" || who.Provider != ProviderLocal {
		t.Fatalf("user token %v %v", who, ok)
	}
	if !guard.Authenticated(http.Header{"X-Api-Key": {tok.GetSecret()}}) {
		t.Fatal("x-api-key carries a token too")
	}
	// Use is stamped once, then not again inside the gap
	listed, _ := tokens.List(ctx, me)
	if len(listed) != 1 || listed[0].GetLastUsedAt() == nil {
		t.Fatalf("after use %v", listed)
	}
	first := listed[0].GetLastUsedAt().AsTime()
	tokens.now = func() time.Time { return first.Add(touchGap / 2) }
	guard.Authenticated(bearer(tok.GetSecret()))
	listed, _ = tokens.List(ctx, me)
	if !listed[0].GetLastUsedAt().AsTime().Equal(first) {
		t.Fatal("use inside the gap should not be stamped")
	}
	tokens.now = func() time.Time { return first.Add(2 * touchGap) }
	guard.Authenticated(bearer(tok.GetSecret()))
	listed, _ = tokens.List(ctx, me)
	if !listed[0].GetLastUsedAt().AsTime().After(first) {
		t.Fatal("use after the gap should be stamped")
	}
	// A session cookie still identifies its owner alongside tokens
	rec := httptest.NewRecorder()
	sessions.Issue(rec, httptest.NewRequest(http.MethodGet, "/", nil), *me)
	if who, ok := guard.Identify(http.Header{"Cookie": {strings.Split(rec.Header().Get("Set-Cookie"), ";")[0]}}); !ok || who.Name != "ann" {
		t.Fatalf("cookie %v %v", who, ok)
	}
	// Another account cannot revoke it, its owner can, and then it is refused
	other := &Session{Subject: "someone-else", Name: "bob", Provider: ProviderLocal}
	if err := tokens.Delete(ctx, other, tok.GetId()); !errors.Is(err, ErrUnknownToken) {
		t.Fatalf("revoke by another %v", err)
	}
	if err := tokens.Delete(ctx, me, tok.GetId()); err != nil {
		t.Fatal(err)
	}
	if guard.Authenticated(bearer(tok.GetSecret())) {
		t.Fatal("revoked token still passes")
	}
	// Removing the account revokes the tokens it made
	again, _ := tokens.Create(ctx, me, "ci")
	users.Create(ctx, "bob", "bob-password")
	if err := users.Delete(ctx, "ann"); err != nil {
		t.Fatal(err)
	}
	if guard.Authenticated(bearer(again.GetSecret())) {
		t.Fatal("a removed account's token still passes")
	}
	// With auth off the guard passes everything and names nobody
	off := NewGuard("", nil, nil)
	if off.Enabled() || !off.Authenticated(http.Header{}) {
		t.Fatal("auth off")
	}
	if _, ok := off.Identify(bearer("anything")); ok {
		t.Fatal("auth off identifies nobody")
	}
}
