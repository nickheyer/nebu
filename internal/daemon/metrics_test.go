package daemon

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// The status and body a GET with headers answers
func getWith(t *testing.T, url string, headers map[string]string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

// Makes the first local account and returns the session cookie the browser carries afterwards
func setupSession(t *testing.T, base string) string {
	t.Helper()
	resp, err := http.Post(base+"/auth/local/setup", "application/json", strings.NewReader(`{"username":"nick","password":"a long enough password"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("setup %d: %s", resp.StatusCode, body)
	}
	for _, c := range resp.Cookies() {
		if c.Value != "" {
			return c.Name + "=" + c.Value
		}
	}
	t.Fatalf("setup set no session cookie: %v", resp.Header)
	return ""
}

// The API listener serves /metrics to a browser session or a read scoped token under the API guard, with the gateway shared or on its own listener
func TestMetricsServedOnTheAPIListenerBehindTheGuard(t *testing.T) {
	for _, c := range []struct{ name, extra string }{
		{"shared gateway", ""},
		{"own gateway listener", "gateway:\n  listen: 127.0.0.1:0\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := serveConfigured(t, "auth:\n  token: test-daemon-token\n"+c.extra, nil)
			eventually(t, "the daemon ready", func() bool { code, _ := get(t, s.base+"/readyz"); return code == http.StatusOK })
			if code, _ := getWith(t, s.base+"/metrics", nil); code != http.StatusUnauthorized {
				t.Fatalf("metrics without a credential: %d", code)
			}
			if code, body := getWith(t, s.base+"/metrics", map[string]string{"Authorization": "Bearer test-daemon-token"}); code != http.StatusOK || !strings.Contains(body, "nebu_") {
				t.Fatalf("metrics with the daemon token: %d %.200s", code, body)
			}
			cookie := setupSession(t, s.base)
			if code, body := getWith(t, s.base+"/metrics", map[string]string{"Cookie": cookie}); code != http.StatusOK || !strings.Contains(body, "nebu_") {
				t.Fatalf("metrics with the browser session: %d %.200s", code, body)
			}
			if code, _ := getWith(t, s.base+"/metrics", map[string]string{"Cookie": "nebu_session=not-a-session"}); code != http.StatusUnauthorized {
				t.Fatalf("metrics with a forged session: %d", code)
			}
		})
	}
}
