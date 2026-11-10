package scrape

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nickheyer/nebu/pkg/datalayer"
	stubserver "github.com/nickheyer/nebu/pkg/stub"
)

// The real scrape reads SGLang and llama.cpp stubs into full server metrics, refusing a llama.cpp pool of unknown size
func TestStubScrape(t *testing.T) {
	pool := map[string]string{datalayer.LabelKVCapacity: "40"}
	tests := []struct {
		name    string
		names   stubserver.Names
		load    int
		labels  map[string]string
		want    datalayer.ServerMetrics
		wantErr error
	}{
		{"sglang idle", stubserver.NamesSGLang, 0, nil, datalayer.ServerMetrics{BlockSize: 4, NumGPUBlocks: 10, MaxLoRA: 3, RunningAdapters: set("sql", "chat"), WaitingAdapters: set()}, nil},
		{"sglang one running two queued", stubserver.NamesSGLang, 3, nil, datalayer.ServerMetrics{QueuedRequests: 2, RunningRequests: 1, KVCacheUtilization: 0.2, BlockSize: 4, NumGPUBlocks: 10, MaxLoRA: 3, RunningAdapters: set("sql", "chat"), WaitingAdapters: set()}, nil},
		{"llamacpp idle", stubserver.NamesLlamaCpp, 0, pool, datalayer.ServerMetrics{BlockSize: 1, NumGPUBlocks: 40, MaxLoRA: 2, RunningAdapters: set("sql", "chat"), WaitingAdapters: set()}, nil},
		{"llamacpp one running two queued", stubserver.NamesLlamaCpp, 3, pool, datalayer.ServerMetrics{QueuedRequests: 2, RunningRequests: 1, KVCacheUtilization: 0.125, BlockSize: 1, NumGPUBlocks: 40, MaxLoRA: 2, RunningAdapters: set("sql", "chat"), WaitingAdapters: set()}, nil},
		{"llamacpp of unknown pool size", stubserver.NamesLlamaCpp, 0, nil, datalayer.ServerMetrics{}, ErrNoKVCapacity},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			st, err := stubserver.New(stubserver.Config{Names: tc.names, Model: "m", MaxNumSeqs: 1, BlockSize: 4, NumGPUBlocks: 10, MaxLoras: 3,
				BaseLatency: 10 * time.Second, Adapters: []stubserver.Adapter{{Name: "sql", Path: "/adapters/sql.gguf"}, {Name: "chat", Path: "/adapters/chat.gguf"}}})
			if err != nil {
				t.Fatal(err)
			}
			srv := httptest.NewServer(st)
			ctx, cancel := context.WithCancel(context.Background())
			var wg sync.WaitGroup
			t.Cleanup(func() {
				cancel()
				wg.Wait()
				st.Close()
				srv.Close()
			})
			for range tc.load {
				wg.Go(func() {
					req, _ := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+"/v1/completions", bytes.NewBufferString(`{"model":"m","prompt":"a b c d e","max_tokens":3}`))
					req.Header.Set("Content-Type", "application/json")
					if resp, err := http.DefaultClient.Do(req); err == nil {
						resp.Body.Close()
					}
				})
			}
			eventually(t, "the stub to hold the load", func() bool { s := st.Stats(); return s.Running+s.Queued == tc.load })
			addr, err := datalayer.ParseAddress(strings.TrimPrefix(srv.URL, "http://"))
			if err != nil {
				t.Fatal(err)
			}
			runtime := string(tc.names)
			store := datalayer.NewStore()
			store.PutPool(datalayer.Pool{ID: "p", Runtime: runtime, Metrics: caps(t, runtime)})
			store.UpsertEndpoint(datalayer.EndpointSpec{ID: "e", Pool: "p", Address: addr, Labels: tc.labels, Ready: true})
			rec := &recorder{}
			src := New(store, StoreResolver(store), Options{Pace: every(250 * time.Millisecond), Observer: rec})
			if err := src.Subscribe(Collector{}); err != nil {
				t.Fatal(err)
			}
			if err := src.UpdateEndpoints([]datalayer.EndpointID{"e"}); err != nil {
				t.Fatal(err)
			}
			if err := src.Start(context.Background()); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = src.Stop() })
			eventually(t, "a scrape outcome", func() bool { _, n := rec.failuresAfter(0); return n > 0 })
			rec.mu.Lock()
			first := rec.errs[0]
			rec.mu.Unlock()
			if !errors.Is(first, tc.wantErr) {
				t.Fatalf("scrape err = %v, want %v", first, tc.wantErr)
			}
			if tc.wantErr != nil {
				if metricsOf(store) != nil {
					t.Fatal("a refused scrape stored metrics")
				}
				return
			}
			got := *metricsOf(store)
			if got.UpdatedAt.IsZero() {
				t.Fatal("sample carries no scrape time")
			}
			if working := tc.load > 0; got.ActiveAt.IsZero() == working {
				t.Fatalf("last work at %v with %d requests held", got.ActiveAt, tc.load)
			}
			got.UpdatedAt, got.ActiveAt = time.Time{}, time.Time{}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("scraped %+v\nwant    %+v", got, tc.want)
			}
		})
	}
}
