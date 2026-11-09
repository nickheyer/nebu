//go:build !windows

package daemon

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// SIGHUP reloads the config file the way a change does
func TestConfigReloadOnSIGHUP(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("gateway:\n  api_keys: [first]\nlogging:\n  level: info\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	d, sink := reloadDaemon(t, path)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		d.watchConfig(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	// The first reload proves the signal handler is installed before the signal goes out
	d.reloads <- struct{}{}
	eventually(t, "the first reload", func() bool { return strings.Count(sink.String(), "config reloaded") >= 2 })
	time.Sleep(50 * time.Millisecond)
	t.Setenv("NEBU_LOGGING_LEVEL", "error")
	if err := syscall.Kill(os.Getpid(), syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the level SIGHUP reloads", func() bool { return d.LogLevel.Level() == slog.LevelError })
}
