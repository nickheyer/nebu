package gateway

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// Cookies travel only to the daemon's own origins, other origins keep working with keys alone
func TestCorsCredentialsStayOnTheDaemonsOwnOrigins(t *testing.T) {
	g, _ := newGateway(t)
	g.SetCredentials(fakeCreds{key: "k"})
	g.SetListeners([]*v1.Listener{{Addr: "0.0.0.0:8485"}}, false)
	srv := httptest.NewServer(g.Handler())
	t.Cleanup(srv.Close)
	own, _ := url.Parse(srv.URL)
	preflight := func(origin string) http.Header {
		t.Helper()
		req, _ := http.NewRequest(http.MethodOptions, srv.URL+"/v1/models", nil)
		req.Header.Set("Origin", origin)
		req.Header.Set("Access-Control-Request-Method", "GET")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("preflight %d", resp.StatusCode)
		}
		return resp.Header
	}
	cases := []struct {
		origin           string
		allowed, cookies bool
	}{
		{srv.URL, true, true},
		{"http://" + own.Hostname() + ":8485", true, true},
		{"http://" + own.Hostname() + ":3000", true, false},
		{"http://" + own.Hostname(), true, false},
		{"https://app.example.com", true, false},
	}
	for _, c := range cases {
		h := preflight(c.origin)
		if got := h.Get("Access-Control-Allow-Origin") == c.origin; got != c.allowed {
			t.Errorf("%s allowed %v, want %v", c.origin, got, c.allowed)
		}
		if got := h.Get("Access-Control-Allow-Credentials") == "true"; got != c.cookies {
			t.Errorf("%s credentials %v, want %v", c.origin, got, c.cookies)
		}
	}
	g.origins = []string{"https://app.example.com"}
	if h := preflight("https://app.example.com"); h.Get("Access-Control-Allow-Credentials") != "true" {
		t.Error("a listed origin gets credentials")
	}
	if h := preflight("https://other.example.com"); h.Get("Access-Control-Allow-Origin") != "" {
		t.Error("an unlisted origin is refused once origins are listed")
	}
	if h := preflight("http://" + own.Hostname() + ":3000"); h.Get("Access-Control-Allow-Origin") != "" {
		t.Error("the daemon's own host on another port is refused once origins are listed")
	}
}
