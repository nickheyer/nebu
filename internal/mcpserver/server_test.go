package mcpserver

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// A loopback request naming another host is refused, as a DNS rebinding page would send it
func TestLocalhostProtection(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "nebu", Version: "test"}, nil)
	srv := httptest.NewServer(streamable(server, slog.New(slog.NewTextHandler(io.Discard, nil))))
	t.Cleanup(srv.Close)
	initialize := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`
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
	if code := send("evil.example"); code != http.StatusForbidden {
		t.Fatalf("rebound host answered %d", code)
	}
	if code := send(strings.TrimPrefix(srv.URL, "http://")); code != http.StatusOK {
		t.Fatalf("loopback host answered %d", code)
	}
}
