package scrape

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nickheyer/nebu/pkg/datalayer"
)

// Records every scrape outcome
type recorder struct {
	mu    sync.Mutex
	pools []datalayer.PoolID
	errs  []error
}

func (r *recorder) OnScrape(pool datalayer.PoolID, _ datalayer.EndpointID, _ time.Duration, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pools = append(r.pools, pool)
	r.errs = append(r.errs, err)
}

// Failures recorded since the given count of scrapes
func (r *recorder) failuresAfter(n int) (int, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	fails := 0
	for _, err := range r.errs[min(n, len(r.errs)):] {
		if err != nil {
			fails++
		}
	}
	return fails, len(r.errs)
}

// Fake runtime whose metrics body and status can be swapped while it serves
type stub struct {
	srv    *httptest.Server
	body   atomic.Value
	status atomic.Int32
	hits   atomic.Int64
	slots  []Slot
}

func newStub(t *testing.T, body string, slots []Slot) *stub {
	s := &stub{slots: slots}
	s.body.Store(body)
	s.status.Store(http.StatusOK)
	mux := http.NewServeMux()
	handle := func(w http.ResponseWriter, _ *http.Request) {
		s.hits.Add(1)
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		w.WriteHeader(int(s.status.Load()))
		_, _ = io.WriteString(w, s.body.Load().(string))
	}
	mux.HandleFunc("/metrics", handle)
	mux.HandleFunc("/prometheus/metrics", handle)
	mux.HandleFunc("/slots", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(s.slots)
	})
	mux.HandleFunc("/lora-adapters", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `[{"id":0,"path":"/m/sql.gguf","scale":1.0,"task_name":"","prompt_prefix":""},{"id":1,"path":"/m/chat.gguf","scale":0.0,"task_name":"","prompt_prefix":""}]`)
	})
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, sglangModels("sql"))
	})
	mux.HandleFunc("/server_info", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, sglangServerInfo(`"max_loras_per_batch":8,`))
	})
	s.srv = httptest.NewServer(mux)
	t.Cleanup(s.srv.Close)
	return s
}

func (s *stub) addr(t *testing.T) datalayer.Address {
	a, err := datalayer.ParseAddress(strings.TrimPrefix(s.srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// Builds a store holding one pool of the runtime and one endpoint on the stub
func setup(t *testing.T, runtime string, st *stub, labels map[string]string) datalayer.Store {
	store := datalayer.NewStore()
	store.PutPool(datalayer.Pool{ID: "p", Runtime: runtime, Metrics: caps(t, runtime)})
	store.UpsertEndpoint(datalayer.EndpointSpec{ID: "e", Pool: "p", Address: st.addr(t), Labels: labels, Ready: true})
	return store
}

func start(t *testing.T, store datalayer.Store, obs Observer) *Source {
	src := New(store, StoreResolver(store), Options{Interval: 10 * time.Millisecond, Observer: obs})
	if err := src.Subscribe(Collector{}); err != nil {
		t.Fatal(err)
	}
	if err := src.Subscribe(Collector{}); !errors.Is(err, datalayer.ErrDuplicateCollector) {
		t.Fatalf("duplicate subscribe err = %v", err)
	}
	if err := src.UpdateEndpoints([]datalayer.EndpointID{"e"}); err != nil {
		t.Fatal(err)
	}
	if err := src.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = src.Stop() })
	return src
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func metricsOf(store datalayer.Store) *datalayer.ServerMetrics {
	ep, ok := store.Endpoint("e")
	if !ok {
		return nil
	}
	m, _ := datalayer.Attribute[*datalayer.ServerMetrics](ep, datalayer.CollectorMetrics)
	return m
}

func TestSourceScrapesEachRuntime(t *testing.T) {
	tests := []struct {
		runtime  string
		text     string
		slots    []Slot
		labels   map[string]string
		capacity int
		queued   int
		maxLoRA  int
		running  map[string]struct{}
		blocks   int
	}{
		{RuntimeVLLM, vllmText, nil, nil, 0, 5, 4, set("sql-lora", "chat-lora"), 27000},
		{RuntimeSGLang, sglangText, nil, nil, 0, 7, 8, set("sql"), 500000},
		{RuntimeTRTLLMServe, trtllmServeText, nil, nil, 0, 4, 0, nil, 2000},
		{RuntimeLlamaCpp, llamaText, llamaSlots, map[string]string{datalayer.LabelKVCapacity: "16384"}, 0, 1, 2, set("sql"), 16384},
		{RuntimeNeMo, nemoText, nil, map[string]string{datalayer.LabelKVCapacity: "65536"}, 8, 3, 0, nil, 8},
	}
	for _, tc := range tests {
		t.Run(tc.runtime, func(t *testing.T) {
			st := newStub(t, tc.text, tc.slots)
			store := setup(t, tc.runtime, st, tc.labels)
			if tc.capacity > 0 {
				store.SetAttribute("e", datalayer.CollectorCapacity, tc.capacity)
			}
			rec := &recorder{}
			start(t, store, rec)
			eventually(t, "metrics attribute", func() bool { return metricsOf(store) != nil })
			m := metricsOf(store)
			if m.QueuedRequests != tc.queued || m.MaxLoRA != tc.maxLoRA || m.NumGPUBlocks != tc.blocks || !reflect.DeepEqual(m.RunningAdapters, tc.running) {
				t.Fatalf("scraped %+v, want queued %d max lora %d blocks %d running %v", *m, tc.queued, tc.maxLoRA, tc.blocks, tc.running)
			}
			if fails, _ := rec.failuresAfter(0); fails != 0 {
				t.Fatalf("%d scrapes failed: %v", fails, rec.errs)
			}
			rec.mu.Lock()
			pool := rec.pools[0]
			rec.mu.Unlock()
			if pool != "p" {
				t.Fatalf("scrape reported under pool %q", pool)
			}
		})
	}
}

func TestFailedScrapeKeepsPreviousSample(t *testing.T) {
	tests := []struct {
		name    string
		breakIt func(*stub)
	}{
		{"server error", func(s *stub) { s.status.Store(http.StatusInternalServerError) }},
		{"mandatory gauge missing", func(s *stub) { s.body.Store("# TYPE vllm:num_requests_running gauge\nvllm:num_requests_running 1\n") }},
		{"malformed exposition", func(s *stub) { s.body.Store("vllm:num_requests_running{oops 1\n") }},
		{"server gone", func(s *stub) { s.srv.Close() }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			st := newStub(t, vllmText, nil)
			store := setup(t, RuntimeVLLM, st, nil)
			rec := &recorder{}
			start(t, store, rec)
			eventually(t, "first sample", func() bool { return metricsOf(store) != nil })
			first := metricsOf(store)
			_, seen := rec.failuresAfter(0)
			tc.breakIt(st)
			eventually(t, "two failed scrapes", func() bool { f, _ := rec.failuresAfter(seen + 1); return f >= 2 })
			after := metricsOf(store)
			if after != first {
				t.Fatal("a failed scrape replaced the previous sample")
			}
			now := time.Now()
			if !after.Stale(now, now.Sub(first.UpdatedAt)/2) {
				t.Fatal("previous sample not stale past its age")
			}
			ep, _ := store.Endpoint("e")
			if datalayer.FreshMetrics(now, now.Sub(first.UpdatedAt)/2)(ep) {
				t.Fatal("fresh filter kept a stale endpoint")
			}
		})
	}
}

func TestUntrackClearsAndStops(t *testing.T) {
	st := newStub(t, vllmText, nil)
	store := setup(t, RuntimeVLLM, st, nil)
	src := start(t, store, nil)
	eventually(t, "first sample", func() bool { return metricsOf(store) != nil })
	if err := src.UpdateEndpoints(nil); err != nil {
		t.Fatal(err)
	}
	if metricsOf(store) != nil {
		t.Fatal("attribute survived untracking")
	}
	hits := st.hits.Load()
	time.Sleep(50 * time.Millisecond)
	if st.hits.Load() != hits {
		t.Fatal("untracked endpoint still scraped")
	}
	if err := src.UpdateEndpoints([]datalayer.EndpointID{"e"}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "sample after retracking", func() bool { return metricsOf(store) != nil })
}

func TestUnresolvableEndpointReportsFailure(t *testing.T) {
	store := datalayer.NewStore()
	store.PutPool(datalayer.Pool{ID: "p"})
	store.UpsertEndpoint(datalayer.EndpointSpec{ID: "e", Pool: "p"})
	rec := &recorder{}
	start(t, store, rec)
	eventually(t, "failure report", func() bool { f, _ := rec.failuresAfter(0); return f > 0 })
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if !errors.Is(rec.errs[0], ErrUnresolved) {
		t.Fatalf("err = %v", rec.errs[0])
	}
}

func TestStoreResolver(t *testing.T) {
	store := datalayer.NewStore()
	store.PutPool(datalayer.Pool{ID: "triton", Metrics: caps(t, RuntimeTritonTRTLLM)})
	store.PutPool(datalayer.Pool{ID: "vllm", Metrics: caps(t, RuntimeVLLM)})
	store.PutPool(datalayer.Pool{ID: "bare"})
	addr := datalayer.Address{Host: "10.0.0.2", Port: 8000}
	for _, id := range []string{"triton", "vllm", "bare"} {
		store.UpsertEndpoint(datalayer.EndpointSpec{ID: datalayer.EndpointID(id), Pool: datalayer.PoolID(id), Address: addr})
	}
	store.UpsertEndpoint(datalayer.EndpointSpec{ID: "orphan", Pool: "missing", Address: addr})
	tests := []struct {
		id       datalayer.EndpointID
		ok       bool
		wantPort int
	}{
		{"triton", true, 8002},
		{"vllm", true, 8000},
		{"bare", false, 0},
		{"orphan", false, 0},
		{"absent", false, 0},
	}
	resolve := StoreResolver(store)
	for _, tc := range tests {
		t.Run(string(tc.id), func(t *testing.T) {
			_, a, ok := resolve(tc.id)
			if ok != tc.ok || (ok && a.Port != tc.wantPort) {
				t.Fatalf("resolve = %v %v", a, ok)
			}
		})
	}
}

func TestCappedReader(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		left    int64
		wantErr error
	}{
		{"under cap", "abc", 4, nil},
		{"at cap", "abcd", 4, nil},
		{"over cap", "abcde", 4, ErrTooLarge},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := io.ReadAll(&capped{r: strings.NewReader(tc.body), left: tc.left})
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestStartTwice(t *testing.T) {
	store := datalayer.NewStore()
	src := New(store, StoreResolver(store), Options{})
	if src.interval != DefaultInterval {
		t.Fatalf("default interval = %v", src.interval)
	}
	if err := src.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer src.Stop()
	if err := src.Start(context.Background()); err == nil {
		t.Fatal("second start accepted")
	}
}

// Fake SGLang HTTP server answering metrics, the model listing and server info as upstream shapes them, refusing every other path
type sglangServer struct {
	srv   *httptest.Server
	mu    sync.Mutex
	paths map[string]int
}

func newSGLangServer(t *testing.T, models, serverInfo string) *sglangServer {
	s := &sglangServer{paths: map[string]int{}}
	reply := func(contentType, body string) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", contentType)
			_, _ = io.WriteString(w, body)
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /metrics", reply("text/plain; version=0.0.4; charset=utf-8", sglangText))
	mux.HandleFunc("GET /v1/models", reply("application/json", models))
	mux.HandleFunc("GET /server_info", reply("application/json", serverInfo))
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.paths[r.URL.Path]++
		s.mu.Unlock()
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(s.srv.Close)
	return s
}

// Paths the server answered, sorted
func (s *sglangServer) requested() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.paths))
	for p := range s.paths {
		out = append(out, p)
	}
	slices.Sort(out)
	return out
}

// Server info as resolved_dict, launch command, scheduler info, kv events, startup time, internal states, version and frontend shape it
func sglangServerInfo(maxLoRAField string) string {
	return `{"model_path":"/models/m","served_model_name":"m","tp_size":1,"dp_size":1,"page_size":1,"enable_lora":true,` + maxLoRAField +
		`"max_loaded_loras":null,"lora_paths":null,"lora_backend":"csgmv","enable_dp_attention":false,"launch_command":"python -m sglang.launch_server --model-path /models/m",` +
		`"status":"ready","max_total_num_tokens":500000,"max_req_input_len":32762,"kv_events":null,"startup_time":1793700000.5,` +
		`"internal_states":[{"world_size":1,"startup_time":1793700000.5,"effective_max_running_requests_per_dp":2048,"last_gen_throughput":0.0,` +
		`"memory_usage":{"weight":15.2,"kvcache":30.1,"startup_available":79.1,"token_capacity":500000,"token_capacity_swa":null,"graph":{}}}],"version":"0.0.0.dev0","frontend":"python"}`
}

// Model listing of the base model and each registered adapter, adapters naming the base model as parent
func sglangModels(adapters ...string) string {
	cards := []string{`{"id":"m","object":"model","created":1793700000,"owned_by":"sglang","root":"m","parent":null,"max_model_len":32768}`}
	for _, a := range adapters {
		cards = append(cards, `{"id":"`+a+`","object":"model","created":1793700001,"owned_by":"sglang","root":"/adapters/`+a+`","parent":"m","max_model_len":null}`)
	}
	return `{"object":"list","data":[` + strings.Join(cards, ",") + `]}`
}

// SGLang scrape reads queue and pages from metrics, adapters from the model listing, the batch cap from server info
func TestSGLangServerScrape(t *testing.T) {
	tests := []struct {
		name     string
		models   string
		info     string
		maxLoRA  int
		running  map[string]struct{}
		wantErr  error
		parseErr bool
	}{
		{"adapters registered", sglangModels("sql", "chat"), sglangServerInfo(`"max_loras_per_batch":4,`), 4, set("sql", "chat"), nil, false},
		{"no adapters at the default cap", sglangModels(), sglangServerInfo(`"max_loras_per_batch":8,`), 8, set(), nil, false},
		{"server info without the cap", sglangModels("sql"), sglangServerInfo(""), 0, nil, ErrNoMaxLoRA, false},
		{"cap that is not an integer", sglangModels("sql"), sglangServerInfo(`"max_loras_per_batch":"eight",`), 0, nil, nil, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fake := newSGLangServer(t, tc.models, tc.info)
			addr, err := datalayer.ParseAddress(strings.TrimPrefix(fake.srv.URL, "http://"))
			if err != nil {
				t.Fatal(err)
			}
			store := datalayer.NewStore()
			store.PutPool(datalayer.Pool{ID: "p", Runtime: RuntimeSGLang, Metrics: caps(t, RuntimeSGLang)})
			store.UpsertEndpoint(datalayer.EndpointSpec{ID: "e", Pool: "p", Address: addr, Ready: true})
			rec := &recorder{}
			src := New(store, StoreResolver(store), Options{Interval: 250 * time.Millisecond, Observer: rec})
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
			if want := []string{"/metrics", "/server_info", "/v1/models"}; !slices.Equal(fake.requested(), want) {
				t.Fatalf("requested %v, want %v", fake.requested(), want)
			}
			switch {
			case tc.wantErr != nil || tc.parseErr:
				if first == nil || (tc.wantErr != nil && !errors.Is(first, tc.wantErr)) || (tc.parseErr && !strings.Contains(first.Error(), "max_loras_per_batch")) {
					t.Fatalf("scrape err = %v, want %v", first, tc.wantErr)
				}
				if metricsOf(store) != nil {
					t.Fatal("a failed scrape stored metrics")
				}
			default:
				if first != nil {
					t.Fatal(first)
				}
				m := metricsOf(store)
				if m.MaxLoRA != tc.maxLoRA || !reflect.DeepEqual(m.RunningAdapters, tc.running) || len(m.WaitingAdapters) != 0 || m.QueuedRequests != 7 || m.RunningRequests != 2 || m.BlockSize != 1 || m.NumGPUBlocks != 500000 {
					t.Fatalf("scraped %+v, want max lora %d running %v", *m, tc.maxLoRA, tc.running)
				}
			}
		})
	}
}

// The failure of an endpoint set once scrapes fail repeatedly
func scrapeFailureOf(store datalayer.Store) *datalayer.ScrapeFailure {
	ep, ok := store.Endpoint("e")
	if !ok {
		return nil
	}
	f, _ := datalayer.Attribute[*datalayer.ScrapeFailure](ep, datalayer.CollectorScrapeFailure)
	return f
}

// Repeated scrape failures set the failure on the endpoint, a success and untracking clear it
func TestScrapeFailureRecorded(t *testing.T) {
	st := newStub(t, vllmText, nil)
	store := setup(t, RuntimeVLLM, st, nil)
	rec := &recorder{}
	src := start(t, store, rec)
	eventually(t, "a sample", func() bool { return metricsOf(store) != nil })
	if scrapeFailureOf(store) != nil {
		t.Fatal("a healthy endpoint carries a scrape failure")
	}
	st.status.Store(http.StatusInternalServerError)
	eventually(t, "the failure set after repeated failures", func() bool { return scrapeFailureOf(store) != nil })
	f := scrapeFailureOf(store)
	if !strings.Contains(f.Err, "500") || f.Since.IsZero() {
		t.Fatalf("failure %+v", f)
	}
	if fails, _ := rec.failuresAfter(0); fails < FailureThreshold {
		t.Fatalf("failure set after %d failed scrapes, threshold %d", fails, FailureThreshold)
	}
	st.status.Store(http.StatusOK)
	eventually(t, "the failure cleared by a success", func() bool { return scrapeFailureOf(store) == nil })
	st.status.Store(http.StatusInternalServerError)
	eventually(t, "the failure set again", func() bool { return scrapeFailureOf(store) != nil })
	if err := src.UpdateEndpoints(nil); err != nil {
		t.Fatal(err)
	}
	if scrapeFailureOf(store) != nil {
		t.Fatal("untracking left the failure")
	}
}
