package pools

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nickheyer/nebu/internal/db"
	"github.com/nickheyer/nebu/pkg/datalayer"
	"github.com/nickheyer/nebu/pkg/events"
	"github.com/nickheyer/nebu/pkg/prefixcache"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/stub"
)

const node = "n1"

var llama = ModelKey{SourceID: "hf", Repo: "meta/llama", Group: "q4"}

// A manager over a fresh database in a temp dir, closed at cleanup
func newManager(t *testing.T) (*Manager, *db.DB, *events.Bus) {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "nebu.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	bus := events.New()
	return openManager(t, d, bus), d, bus
}

// A manager over an existing database, closed at cleanup
func openManager(t *testing.T, d *db.DB, bus *events.Bus) *Manager {
	t.Helper()
	m, err := New(context.Background(), Options{
		DB:              d,
		Events:          bus,
		NodeID:          node,
		ScrapeInterval:  20 * time.Millisecond,
		ProbeInterval:   20 * time.Millisecond,
		CatalogInterval: 20 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	m.SetIndexer(prefixcache.New(prefixcache.Config{}), prefixcache.Config{})
	t.Cleanup(func() {
		if err := m.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	return m
}

// The vLLM metric names
func vllmNames() *v1.MetricNames {
	return &v1.MetricNames{
		QueuedRequests:     "vllm:num_requests_waiting",
		RunningRequests:    "vllm:num_requests_running",
		KvCacheUtilization: "vllm:kv_cache_usage_perc",
		CacheConfigInfo:    "vllm:cache_config_info",
		BlockSizeLabel:     "block_size",
		NumBlocksLabel:     "num_gpu_blocks",
		MetricsPath:        "/metrics",
		HealthPath:         "/health",
	}
}

func membership(runtime string) Membership {
	return Membership{Model: llama, Runtime: runtime, Protocol: datalayer.AppProtocolHTTP, Concurrency: 4, Metrics: vllmNames()}
}

func published(instance string, port int, ready bool) Published {
	return Published{InstanceID: instance, NodeID: node, Address: datalayer.Address{Host: "127.0.0.1", Port: port}, Ready: ready}
}

// A metrics serving stub model server and its address
func stubServer(t *testing.T, model string) (*stub.Stub, datalayer.Address) {
	t.Helper()
	s, err := stub.New(stub.Config{Model: model})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s)
	t.Cleanup(func() {
		srv.Close()
		s.Close()
	})
	addr, err := datalayer.ParseAddress(strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	return s, addr
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Collects every event the bus delivers until the test ends
type recorder struct {
	mu     sync.Mutex
	events []*v1.Event
}

func record(t *testing.T, bus *events.Bus) *recorder {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	sub := bus.Subscribe(ctx, nil)
	r := &recorder{}
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case ev := <-sub.Events():
				r.mu.Lock()
				r.events = append(r.events, ev)
				r.mu.Unlock()
			}
		}
	}()
	return r
}

// Events of a kind, action and id seen so far
func (r *recorder) count(kind v1.EventKind, action v1.EventAction, id string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, ev := range r.events {
		if ev.GetKind() == kind && ev.GetAction() == action && ev.GetId() == id {
			n++
		}
	}
	return n
}

// The newest event of a kind and id
func (r *recorder) last(kind v1.EventKind, id string) *v1.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := len(r.events) - 1; i >= 0; i-- {
		if ev := r.events[i]; ev.GetKind() == kind && ev.GetId() == id {
			return ev
		}
	}
	return nil
}

// Records each slot woken
type wakeLog struct {
	mu    sync.Mutex
	woken []OwnerRef
	err   error
}

func (w *wakeLog) Wake(_ context.Context, owner OwnerRef) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.woken = append(w.woken, owner)
	return w.err
}

func (w *wakeLog) count() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.woken)
}

func condition(conds []*v1.Condition, typ string) *v1.Condition {
	for _, c := range conds {
		if c.GetType() == typ {
			return c
		}
	}
	return nil
}

func itoa(n int) string { return strconv.Itoa(n) }
