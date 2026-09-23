package gateway

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// Accepts one cookie value as a signed-in browser
type cookieSessions string

func (c cookieSessions) Enabled() bool { return true }

func (c cookieSessions) Authenticated(h http.Header) bool {
	r := http.Request{Header: h}
	cookie, err := r.Cookie("nebu_session")
	return err == nil && cookie.Value == string(c)
}

// Credentials with auth.disabled
type openCredentials struct{}

func (openCredentials) Enabled() bool                  { return false }
func (openCredentials) Authenticated(http.Header) bool { return true }

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestCredentialsAreRequiredWheneverAuthIsOn(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"ok":true}`)) }))
	defer upstream.Close()
	table, _ := OpenTable(context.Background(), nil, nil, nil)
	table.Set("ready", "i1", "", upstream.URL, "m", "", v1.ApiFlavor_API_FLAVOR_OPENAI, nil, nil)

	// No gateway keys, but the daemon authenticates: the gateway is closed to everyone else
	g := New(table, nil, nil, nil, nil, quietLog())
	g.SetCredentials(cookieSessions("good"))
	if !g.Status().GetAuth() {
		t.Fatal("status should report auth with daemon credentials on")
	}
	srv := httptest.NewServer(g.Handler())
	defer srv.Close()
	if resp := postTo(t, srv.URL, "", "", ""); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no credentials with auth on %d", resp.StatusCode)
	}
	if resp := postTo(t, srv.URL, "good", "", ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("session cookie %d", resp.StatusCode)
	}
	if resp := postTo(t, srv.URL, "bad", "", ""); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unknown cookie %d", resp.StatusCode)
	}
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/models", nil)
	resp, _ := http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("model list without credentials %d", resp.StatusCode)
	}
	// In-process callers such as bots pass without any credential
	local, err := g.Client("bot:test").Post(g.LocalBase()+"/v1/chat/completions", "application/json", strings.NewReader(`{"model":"ready"}`))
	if err != nil {
		t.Fatal(err)
	}
	local.Body.Close()
	if local.StatusCode != http.StatusOK {
		t.Fatalf("in-process call %d", local.StatusCode)
	}

	// Gateway keys and daemon credentials both open the door
	keyed := New(table, []string{"k1"}, nil, nil, nil, quietLog())
	keyed.SetCredentials(cookieSessions("good"))
	keyedSrv := httptest.NewServer(keyed.Handler())
	defer keyedSrv.Close()
	if resp := postTo(t, keyedSrv.URL, "bad", "k1", ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("key beside a stale cookie %d", resp.StatusCode)
	}
	if resp := postTo(t, keyedSrv.URL, "good", "", ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("cookie without key %d", resp.StatusCode)
	}
	if resp := postTo(t, keyedSrv.URL, "", "wrong", ""); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong key %d", resp.StatusCode)
	}

	// With auth.disabled and no keys the gateway is open, and a cookie means nothing
	open := New(table, nil, nil, nil, nil, quietLog())
	open.SetCredentials(openCredentials{})
	if open.Status().GetAuth() {
		t.Fatal("status should report no auth")
	}
	openSrv := httptest.NewServer(open.Handler())
	defer openSrv.Close()
	if resp := postTo(t, openSrv.URL, "", "", ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("open gateway %d", resp.StatusCode)
	}
	plain := New(table, []string{"k1"}, nil, nil, nil, quietLog())
	plainSrv := httptest.NewServer(plain.Handler())
	defer plainSrv.Close()
	if resp := postTo(t, plainSrv.URL, "good", "", ""); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("cookie without daemon credentials %d", resp.StatusCode)
	}
}

func TestCookiesRideAlongOnlyFromTrustedOrigins(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"ok":true}`)) }))
	defer upstream.Close()
	table, _ := OpenTable(context.Background(), nil, nil, nil)
	table.Set("ready", "i1", "", upstream.URL, "m", "", v1.ApiFlavor_API_FLAVOR_OPENAI, nil, nil)
	g := New(table, nil, nil, nil, nil, quietLog())
	g.SetCredentials(cookieSessions("good"))
	srv := httptest.NewServer(g.Handler())
	defer srv.Close()
	// Any origin is allowed, but cookies may ride along only from the daemon's own host on any port
	host := strings.TrimPrefix(srv.URL, "http://")
	hostname := host[:strings.LastIndex(host, ":")]
	for origin, want := range map[string]string{"http://" + hostname + ":9999": "true", "https://elsewhere.example": ""} {
		resp := postTo(t, srv.URL, "good", "", origin)
		if resp.Header.Get("Access-Control-Allow-Origin") != origin || resp.Header.Get("Access-Control-Allow-Credentials") != want {
			t.Fatalf("origin %s: allow %q credentials %q", origin, resp.Header.Get("Access-Control-Allow-Origin"), resp.Header.Get("Access-Control-Allow-Credentials"))
		}
	}
	// A listed origin gets cookies too, and the list still shuts out everyone else
	listed := New(table, nil, []string{"https://listed.example"}, nil, nil, quietLog())
	listed.SetCredentials(cookieSessions("good"))
	listedSrv := httptest.NewServer(listed.Handler())
	defer listedSrv.Close()
	if resp := postTo(t, listedSrv.URL, "good", "", "https://listed.example"); resp.Header.Get("Access-Control-Allow-Credentials") != "true" {
		t.Fatalf("listed origin credentials %q", resp.Header.Get("Access-Control-Allow-Credentials"))
	}
	if resp := postTo(t, listedSrv.URL, "good", "", "http://"+hostname+":9999"); resp.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("unlisted origin should get no CORS headers")
	}
}

func postTo(t *testing.T, base, cookie, key, origin string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, base+"/v1/chat/completions", strings.NewReader(`{"model":"ready"}`))
	req.Header.Set("Content-Type", "application/json")
	if cookie != "" {
		req.Header.Set("Cookie", "nebu_session="+cookie)
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp
}
