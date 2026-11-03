package catalog

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nickheyer/nebu/pkg/datalayer"
)

func TestCollectorAdapters(t *testing.T) {
	tests := []struct {
		name         string
		sample       Sample
		wantModels   []string
		wantAdapters []string
	}{
		{
			name: "vllm parent field",
			sample: Sample{PoolModel: "llama", Models: []Model{
				{ID: "llama"}, {ID: "sql-lora", Parent: "llama"}, {ID: "chat-lora", Parent: "llama"},
			}},
			wantModels: []string{"llama"}, wantAdapters: []string{"chat-lora", "sql-lora"},
		},
		{
			name: "pool model with a parent stays a model",
			sample: Sample{PoolModel: "llama", Models: []Model{
				{ID: "llama", Parent: "base"},
			}},
			wantModels: []string{"llama"},
		},
		{
			name: "alias without parent is a model",
			sample: Sample{PoolModel: "llama", Models: []Model{
				{ID: "llama"}, {ID: "llama-alias"},
			}},
			wantModels: []string{"llama", "llama-alias"},
		},
		{
			name: "llama.cpp lora listing",
			sample: Sample{PoolModel: "qwen", Models: []Model{{ID: "qwen"}}, LoRA: []LoRA{
				{Path: "/models/loras/my_adapter_1.gguf"}, {Path: "my_adapter_2.gguf"}, {Path: ""},
			}},
			wantModels: []string{"qwen"}, wantAdapters: []string{"my_adapter_1", "my_adapter_2"},
		},
		{
			name: "entry named in the lora listing is an adapter",
			sample: Sample{PoolModel: "qwen", Models: []Model{{ID: "qwen"}, {ID: "tone"}}, LoRA: []LoRA{
				{Path: "tone.gguf"},
			}},
			wantModels: []string{"qwen"}, wantAdapters: []string{"tone"},
		},
		{
			name: "duplicates and blanks collapse",
			sample: Sample{PoolModel: "m", Models: []Model{
				{ID: "m"}, {ID: "m"}, {ID: ""}, {ID: "a", Parent: "m"}, {ID: "a", Parent: "m"},
			}},
			wantModels: []string{"m"}, wantAdapters: []string{"a"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tc.sample.FetchedAt = time.Unix(5, 0)
			v, err := Collector{}.Extract(nil, &tc.sample)
			if err != nil {
				t.Fatal(err)
			}
			c := v.(*datalayer.Catalog)
			if !reflect.DeepEqual(c.Models, tc.wantModels) || !reflect.DeepEqual(c.Adapters, tc.wantAdapters) {
				t.Fatalf("models %v adapters %v", c.Models, c.Adapters)
			}
			if !c.UpdatedAt.Equal(tc.sample.FetchedAt) {
				t.Fatalf("updated at %v", c.UpdatedAt)
			}
		})
	}
	if _, err := (Collector{}).Extract(nil, 3); !errors.Is(err, ErrSampleType) {
		t.Fatalf("wrong sample err = %v", err)
	}
}

// Records every fetch outcome
type recorder struct {
	mu   sync.Mutex
	errs []error
}

func (r *recorder) OnFetch(_ datalayer.EndpointID, _ time.Duration, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.errs = append(r.errs, err)
}

func (r *recorder) failures() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, err := range r.errs {
		if err != nil {
			n++
		}
	}
	return n
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func catalogOf(store datalayer.Store) *datalayer.Catalog {
	ep, ok := store.Endpoint("e")
	if !ok {
		return nil
	}
	c, _ := datalayer.Attribute[*datalayer.Catalog](ep, datalayer.CollectorCatalog)
	return c
}

func TestSourceFetches(t *testing.T) {
	const vllmModels = `{"object":"list","data":[{"id":"llama","object":"model","root":"/m/llama","parent":null},{"id":"sql-lora","object":"model","root":"/l/sql","parent":"llama"}]}`
	const llamaModels = `{"object":"list","data":[{"id":"qwen","object":"model","owned_by":"llamacpp","meta":{"n_ctx_train":32768}}]}`
	const llamaLoRA = `[{"id":0,"path":"loras/tone.gguf","scale":1.0},{"id":1,"path":"loras/terse.gguf","scale":0.0}]`
	tests := []struct {
		name         string
		model        string
		models       string
		lora         string
		loraStatus   int
		wantModels   []string
		wantAdapters []string
	}{
		{"vllm without lora route", "llama", vllmModels, "", http.StatusNotFound, []string{"llama"}, []string{"sql-lora"}},
		{"llama.cpp with lora route", "qwen", llamaModels, llamaLoRA, http.StatusOK, []string{"qwen"}, []string{"terse", "tone"}},
		{"lora route refusing the method", "llama", vllmModels, "", http.StatusMethodNotAllowed, []string{"llama"}, []string{"sql-lora"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var loraStatus atomic.Int32
			loraStatus.Store(int32(tc.loraStatus))
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/v1/models":
					_, _ = io.WriteString(w, tc.models)
				case "/lora-adapters":
					w.WriteHeader(int(loraStatus.Load()))
					_, _ = io.WriteString(w, tc.lora)
				default:
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()
			addr, err := datalayer.ParseAddress(strings.TrimPrefix(srv.URL, "http://"))
			if err != nil {
				t.Fatal(err)
			}
			store := datalayer.NewStore()
			store.PutPool(datalayer.Pool{ID: "p", Model: tc.model})
			store.UpsertEndpoint(datalayer.EndpointSpec{ID: "e", Pool: "p", Address: addr})
			rec := &recorder{}
			src := New(store, StoreResolver(store), Options{Interval: 5 * time.Millisecond, Observer: rec})
			if err := src.Subscribe(Collector{}); err != nil {
				t.Fatal(err)
			}
			if err := src.UpdateEndpoints([]datalayer.EndpointID{"e"}); err != nil {
				t.Fatal(err)
			}
			if err := src.Start(context.Background()); err != nil {
				t.Fatal(err)
			}
			defer src.Stop()
			eventually(t, "catalog", func() bool { return catalogOf(store) != nil })
			first := catalogOf(store)
			if !reflect.DeepEqual(first.Models, tc.wantModels) || !reflect.DeepEqual(first.Adapters, tc.wantAdapters) {
				t.Fatalf("models %v adapters %v", first.Models, first.Adapters)
			}
			if rec.failures() != 0 {
				t.Fatalf("fetch failures: %v", rec.errs)
			}

			loraStatus.Store(http.StatusInternalServerError)
			eventually(t, "failing fetches", func() bool { return rec.failures() >= 2 })
			held := catalogOf(store)
			seen := rec.failures()
			eventually(t, "more failing fetches", func() bool { return rec.failures() >= seen+2 })
			if catalogOf(store) != held {
				t.Fatal("a failed fetch replaced the catalog")
			}
		})
	}
}

func TestTracksAndResolver(t *testing.T) {
	store := datalayer.NewStore()
	store.PutPool(datalayer.Pool{ID: "http", Model: "m"})
	store.PutPool(datalayer.Pool{ID: "grpc", Model: "m", AppProtocol: datalayer.AppProtocolGRPC})
	addr := datalayer.Address{Host: "10.0.0.3", Port: 9000}
	store.UpsertEndpoint(datalayer.EndpointSpec{ID: "h", Pool: "http", Address: addr})
	store.UpsertEndpoint(datalayer.EndpointSpec{ID: "g", Pool: "grpc", Address: addr})
	src := New(store, StoreResolver(store), Options{})
	if src.client.Timeout != DefaultInterval {
		t.Fatalf("timeout %v", src.client.Timeout)
	}
	tests := []struct {
		id       datalayer.EndpointID
		pool     datalayer.PoolID
		wantOK   bool
		wantBase string
	}{
		{"h", "http", true, "http://10.0.0.3:9000"},
		{"g", "grpc", false, ""},
		{"absent", "", false, ""},
	}
	resolve := StoreResolver(store)
	for _, tc := range tests {
		t.Run(string(tc.id), func(t *testing.T) {
			base, model, ok := resolve(tc.id)
			if ok != tc.wantOK || base != tc.wantBase || (ok && model != "m") {
				t.Fatalf("resolve = %q %q %v", base, model, ok)
			}
			if p, found := store.Pool(tc.pool); found && src.Tracks(p) != tc.wantOK {
				t.Fatalf("Tracks(%s) disagrees with the resolver", tc.pool)
			}
		})
	}
}

func TestMissingModelListingFails(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
	}{
		{"model route absent", http.StatusNotFound, ""},
		{"model route refuses", http.StatusMethodNotAllowed, ""},
		{"model listing malformed", http.StatusOK, "{"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer srv.Close()
			addr, err := datalayer.ParseAddress(strings.TrimPrefix(srv.URL, "http://"))
			if err != nil {
				t.Fatal(err)
			}
			store := datalayer.NewStore()
			store.PutPool(datalayer.Pool{ID: "p", Model: "m"})
			store.UpsertEndpoint(datalayer.EndpointSpec{ID: "e", Pool: "p", Address: addr})
			rec := &recorder{}
			src := New(store, StoreResolver(store), Options{Interval: 5 * time.Millisecond, Observer: rec})
			if err := src.Subscribe(Collector{}); err != nil {
				t.Fatal(err)
			}
			if err := src.UpdateEndpoints([]datalayer.EndpointID{"e"}); err != nil {
				t.Fatal(err)
			}
			if err := src.Start(context.Background()); err != nil {
				t.Fatal(err)
			}
			defer src.Stop()
			eventually(t, "failed fetches", func() bool { return rec.failures() >= 2 })
			if catalogOf(store) != nil {
				t.Fatal("a failed listing produced a catalog")
			}
		})
	}
}
