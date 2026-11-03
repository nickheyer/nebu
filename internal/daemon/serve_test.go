package daemon

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nickheyer/nebu/pkg/health"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// The API listener answers liveness and readiness while recovering and 503 elsewhere, then serves everything once open
func TestGateServesHealthUntilOpen(t *testing.T) {
	reg := health.NewRegistry()
	mux := http.NewServeMux()
	reg.Mount(mux)
	g := &gate{health: mux, next: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTeapot) })}
	srv := httptest.NewServer(g)
	t.Cleanup(srv.Close)
	get := func(path string) (int, string) {
		resp, err := srv.Client().Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		return resp.StatusCode, resp.Header.Get("Retry-After")
	}
	if code, _ := get("/livez"); code != http.StatusOK {
		t.Fatalf("livez while recovering %d", code)
	}
	if code, retry := get("/readyz"); code != http.StatusServiceUnavailable || retry == "" {
		t.Fatalf("readyz while recovering %d retry %q", code, retry)
	}
	if code, retry := get("/nebu.v1.HostService/GetProfile"); code != http.StatusServiceUnavailable || retry == "" {
		t.Fatalf("api while recovering %d retry %q", code, retry)
	}
	g.open.Store(true)
	if code, _ := get("/nebu.v1.HostService/GetProfile"); code != http.StatusTeapot {
		t.Fatalf("api once open %d", code)
	}
}

// The gateway listener waits for every readiness check and gives up when the daemon stops first
func TestAwaitReady(t *testing.T) {
	d := &Daemon{Health: health.NewRegistry(), Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Config: &v1.Config{}}
	var ready atomic.Bool
	d.Health.Register("synced", func(context.Context) error {
		if !ready.Load() {
			return errors.New("not synced")
		}
		return nil
	})
	go func() {
		time.Sleep(100 * time.Millisecond)
		ready.Store(true)
	}()
	start := time.Now()
	if err := d.awaitReady(context.Background()); err != nil || time.Since(start) < 100*time.Millisecond {
		t.Fatalf("waited %s %v", time.Since(start), err)
	}
	ready.Store(false)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := d.awaitReady(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a daemon stopping before ready %v", err)
	}
}
