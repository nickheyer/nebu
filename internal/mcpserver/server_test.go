package mcpserver

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/nickheyer/nebu/internal/auth"
)

// A loopback request naming another host is refused while auth is off, as a DNS rebinding page would send it, and served once auth is on, as a reverse proxy on the same machine sends every request
func TestLocalhostProtection(t *testing.T) {
	initialize := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`
	for _, c := range []struct {
		name    string
		guard   *auth.Guard
		foreign int
	}{
		{"auth off", auth.NewGuard("", nil, nil), http.StatusForbidden},
		{"auth on", auth.NewGuard("token", nil, nil), http.StatusOK},
	} {
		t.Run(c.name, func(t *testing.T) {
			server := mcp.NewServer(&mcp.Implementation{Name: "nebu", Version: "test"}, nil)
			srv := httptest.NewServer(streamable(server, c.guard.Enabled(), slog.New(slog.NewTextHandler(io.Discard, nil))))
			t.Cleanup(srv.Close)
			send := func(host string) int {
				req, _ := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(initialize))
				req.Host = host
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Accept", "application/json, text/event-stream")
				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				resp.Body.Close()
				return resp.StatusCode
			}
			if code := send("nebu.example"); code != c.foreign {
				t.Fatalf("another host answered %d, want %d", code, c.foreign)
			}
			if code := send(strings.TrimPrefix(srv.URL, "http://")); code != http.StatusOK {
				t.Fatalf("loopback host answered %d", code)
			}
		})
	}
}
