package config

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// Collects every reload the watcher hands out
type reloads struct {
	mu   sync.Mutex
	cfgs []*v1.Config
	errs []error
}

// How many reloads arrived so far
func (r *reloads) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.cfgs)
}

func (r *reloads) apply(cfg *v1.Config, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cfgs, r.errs = append(r.cfgs, cfg), append(r.errs, err)
}

// Waits until n reloads arrived and returns the last of them
func (r *reloads) wait(t *testing.T, n int) (*v1.Config, error) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		r.mu.Lock()
		if len(r.cfgs) >= n {
			cfg, err := r.cfgs[n-1], r.errs[n-1]
			r.mu.Unlock()
			return cfg, err
		}
		r.mu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("reload %d never arrived", n)
	return nil, nil
}

// A rewritten file, a kick with the environment changed, and a file turned invalid each hand the watcher's caller the outcome
func TestWatchReloadsOnChangeAndKick(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	file := filepath.Join(t.TempDir(), "config.yaml")
	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("gateway:\n  api_keys: [one]\n  policy:\n    max_in_flight: 3\nlogging:\n  level: info\n")
	if got, err := File(file); err != nil || got != file {
		t.Fatalf("File %q %v", got, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	kick := make(chan struct{}, 1)
	got := &reloads{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		Watch(ctx, file, 10*time.Millisecond, kick, got.apply)
	}()
	for _, tc := range []struct {
		name  string
		act   func()
		keys  []string
		cap   uint32
		level string
		bad   bool
	}{
		{name: "file rewritten", act: func() {
			time.Sleep(20 * time.Millisecond)
			write("gateway:\n  api_keys: [two, three]\n  policy:\n    max_in_flight: 7\nlogging:\n  level: debug\n")
		}, keys: []string{"two", "three"}, cap: 7, level: "debug"},
		{name: "kick with the environment changed", act: func() {
			t.Setenv("NEBU_LOGGING_LEVEL", "warn")
			kick <- struct{}{}
		}, keys: []string{"two", "three"}, cap: 7, level: "warn"},
		{name: "file made invalid", act: func() {
			time.Sleep(20 * time.Millisecond)
			write("gateway:\n  nonsense: 1\n")
		}, bad: true},
	} {
		n := got.count() + 1
		tc.act()
		cfg, err := got.wait(t, n)
		if tc.bad {
			if err == nil || cfg != nil {
				t.Fatalf("%s: %v %v", tc.name, cfg, err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if keys := cfg.GetGateway().GetApiKeys(); len(keys) != len(tc.keys) || keys[0] != tc.keys[0] || cfg.GetGateway().GetPolicy().GetMaxInFlight() != tc.cap || cfg.GetLogging().GetLevel() != tc.level {
			t.Fatalf("%s: keys %v cap %d level %s", tc.name, keys, cfg.GetGateway().GetPolicy().GetMaxInFlight(), cfg.GetLogging().GetLevel())
		}
	}
	// An unchanged file hands nothing out
	time.Sleep(50 * time.Millisecond)
	if n := got.count(); n != 3 {
		t.Fatalf("%d reloads for 3 changes", n)
	}
	cancel()
	<-done
}
