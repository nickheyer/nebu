package daemon

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/nickheyer/nebu/internal/auth"
	"github.com/nickheyer/nebu/internal/db"
	"github.com/nickheyer/nebu/internal/gateway"
	"github.com/nickheyer/nebu/internal/pools"
	"github.com/nickheyer/nebu/internal/tools"
	"github.com/nickheyer/nebu/pkg/chattemplate"
	"github.com/nickheyer/nebu/pkg/config"
	"github.com/nickheyer/nebu/pkg/events"
	"github.com/nickheyer/nebu/pkg/flowcontrol"
	"github.com/nickheyer/nebu/pkg/health"
	"github.com/nickheyer/nebu/pkg/metrics"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/telemetry"
	"github.com/prometheus/client_golang/prometheus"
)

type openAIRuntimes struct{}

func (openAIRuntimes) API(string) v1.ApiFlavor { return v1.ApiFlavor_API_FLAVOR_OPENAI }
func (openAIRuntimes) Handoff(string) string   { return "" }

type plainInstances struct{}

func (plainInstances) ToolSupport(string) v1.ToolSupport {
	return v1.ToolSupport_TOOL_SUPPORT_UNSPECIFIED
}
func (plainInstances) Modes(string) []string { return nil }
func (plainInstances) LoadAdapter(context.Context, string, string, string) error {
	return errors.New("no adapter loads in this test")
}
func (plainInstances) TokenizerFile(string) (string, error) {
	return "", errors.New("no tokenizer files in this test")
}
func (plainInstances) Template(string) (*chattemplate.Template, error) {
	return nil, errors.New("no templates in this test")
}

// A log sink the test reads back
type logSink struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *logSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *logSink) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// A daemon holding a real gateway built from the config file at path, its reload loop polling fast
func reloadDaemon(t *testing.T, path string) (*Daemon, *logSink) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	sink := &logSink{}
	level := new(slog.LevelVar)
	log := slog.New(slog.NewTextHandler(sink, &slog.HandlerOptions{Level: level}))
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	store, err := db.Open(filepath.Join(t.TempDir(), "nebu.db"))
	if err != nil {
		t.Fatal(err)
	}
	bus := events.New()
	mgr, err := pools.New(ctx, pools.Options{DB: store, Events: bus, NodeID: "n1", Log: log})
	if err != nil {
		t.Fatal(err)
	}
	mgr.Store().MarkSynced()
	tp, shutdown, err := telemetry.Setup(ctx, telemetry.Config{ServiceName: "daemon-test", SampleRatio: 1})
	if err != nil {
		t.Fatal(err)
	}
	mreg, err := metrics.New(prometheus.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}
	gw := cfg.GetGateway()
	g, err := gateway.New(gateway.Deps{
		Pools: mgr, Resolver: mgr, Manager: mgr, Store: mgr.Store(), DB: store, Events: bus, Guard: auth.NewGuard("daemon-token", nil, nil),
		Tools: tools.New(log, bus), Runtimes: openAIRuntimes{}, Instances: plainInstances{},
		Metrics: mreg, Telemetry: tp, Health: health.NewRegistry(), Logger: log,
		Config: gateway.Config{
			Keys: gw.GetApiKeys(), Policy: gw.GetPolicy(), OutputTokens: 64,
			Flow:       flowcontrol.Config{QueueCapacity: 16, QueueTimeout: time.Second, SheddableBelow: -1, Tick: 10 * time.Millisecond},
			Saturation: flowcontrol.Thresholds{QueueDepth: 8, KVUtilization: 0.9}, Staleness: 2 * time.Second,
			Retry: gateway.Retry{Shed: time.Second, Unavailable: time.Second}, TraceRetention: time.Hour, TraceRing: 32,
			Version: "test", Interval: 50 * time.Millisecond,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		mgr.Close()
		shutdown(context.Background())
		store.Close()
	})
	d := &Daemon{Config: cfg, Gateway: g, Log: log, LogLevel: level, ConfigPath: path, reloads: make(chan struct{}, 1), reloadEvery: 20 * time.Millisecond}
	d.Reload(cfg)
	return d, sink
}

// The status a request with the bearer gets from the gateway listing
func bearerStatus(t *testing.T, d *Daemon, key string) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+key)
	rec := httptest.NewRecorder()
	d.Gateway.Handler().ServeHTTP(rec, req)
	return rec.Code
}

// Polls until cond holds
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("%s never happened", what)
}

// A rewritten config file, a kick as SIGHUP gives, and a broken rewrite each reach the gateway keys, the default policy and the log level the right way
func TestConfigReloadSwapsKeysPolicyAndLevel(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	path := filepath.Join(t.TempDir(), "config.yaml")
	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("gateway:\n  api_keys: [first]\n  policy:\n    max_in_flight: 3\nlogging:\n  level: info\n")
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
	if bearerStatus(t, d, "first") == http.StatusUnauthorized || bearerStatus(t, d, "second") != http.StatusUnauthorized {
		t.Fatal("the configured key must open the gateway before any reload and an unknown one must not")
	}
	if d.Gateway.Status().GetPolicy().GetMaxInFlight() != 3 || d.LogLevel.Level() != slog.LevelInfo {
		t.Fatalf("policy %v level %v before any reload", d.Gateway.Status().GetPolicy(), d.LogLevel.Level())
	}
	// Requests keep flowing while the file is rewritten under them
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					bearerStatus(t, d, "first")
				}
			}
		}()
	}
	time.Sleep(30 * time.Millisecond)
	write("gateway:\n  api_keys: [second]\n  policy:\n    max_in_flight: 7\nlogging:\n  level: debug\n")
	eventually(t, "the rewritten policy", func() bool { return d.Gateway.Status().GetPolicy().GetMaxInFlight() == 7 })
	close(stop)
	wg.Wait()
	if bearerStatus(t, d, "second") == http.StatusUnauthorized || bearerStatus(t, d, "first") != http.StatusUnauthorized {
		t.Fatal("the rewritten key must open the gateway and the old one must not")
	}
	if d.LogLevel.Level() != slog.LevelDebug {
		t.Fatalf("level after the rewrite %v", d.LogLevel.Level())
	}
	// A kick reloads the same file with the environment over it, as SIGHUP does
	t.Setenv("NEBU_LOGGING_LEVEL", "warn")
	d.reloads <- struct{}{}
	eventually(t, "the kicked level", func() bool { return d.LogLevel.Level() == slog.LevelWarn })
	// A file that no longer loads leaves the running settings as they are and says so
	time.Sleep(30 * time.Millisecond)
	write("gateway:\n  nonsense: 1\n")
	eventually(t, "the refusal", func() bool { return bytes.Contains([]byte(sink.String()), []byte("config reload refused")) })
	if bearerStatus(t, d, "second") == http.StatusUnauthorized || d.Gateway.Status().GetPolicy().GetMaxInFlight() != 7 || d.LogLevel.Level() != slog.LevelWarn {
		t.Fatal("a refused reload must leave the running settings alone")
	}
}
