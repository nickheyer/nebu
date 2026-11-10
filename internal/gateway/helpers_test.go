package gateway

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nickheyer/nebu/internal/auth"
	"github.com/nickheyer/nebu/internal/db"
	"github.com/nickheyer/nebu/internal/pools"
	"github.com/nickheyer/nebu/internal/tools"
	"github.com/nickheyer/nebu/pkg/chattemplate"
	"github.com/nickheyer/nebu/pkg/datalayer"
	"github.com/nickheyer/nebu/pkg/datalayer/scrape"
	"github.com/nickheyer/nebu/pkg/events"
	"github.com/nickheyer/nebu/pkg/flowcontrol"
	"github.com/nickheyer/nebu/pkg/health"
	"github.com/nickheyer/nebu/pkg/metrics"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/stub"
	"github.com/nickheyer/nebu/pkg/telemetry"
	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

const testNode = "n1"

// Wire protocols and relay handoffs of the runtimes the tests publish
type fakeRuntimes struct {
	api     map[string]v1.ApiFlavor
	handoff map[string]string
}

func (r fakeRuntimes) API(id string) v1.ApiFlavor {
	if a, ok := r.api[id]; ok {
		return a
	}
	return v1.ApiFlavor_API_FLAVOR_OPENAI
}

func (r fakeRuntimes) Handoff(id string) string { return r.handoff[id] }

// Tool support, media modes, chat templates and adapter loads per instance
type fakeInstances struct {
	mu        sync.Mutex
	support   map[string]v1.ToolSupport
	modes     map[string][]string
	templates map[string]*chattemplate.Template
	// The tokenizer.json of each instance, instances absent here having none
	files map[string]string
	// Runs an adapter load, refusing every load when unset
	load func(ctx context.Context, instanceID, name, path string) error
}

func (f *fakeInstances) TokenizerFile(id string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if p, ok := f.files[id]; ok {
		return p, nil
	}
	return "", errors.New("instance " + id + " holds no tokenizer.json")
}

func (f *fakeInstances) LoadAdapter(ctx context.Context, instanceID, name, path string) error {
	f.mu.Lock()
	load := f.load
	f.mu.Unlock()
	if load == nil {
		return errors.New("no adapter loads in this test")
	}
	return load(ctx, instanceID, name, path)
}

func (f *fakeInstances) ToolSupport(id string) v1.ToolSupport {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.support[id]
}

func (f *fakeInstances) Modes(id string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.modes[id]
}

func (f *fakeInstances) Template(id string) (*chattemplate.Template, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.templates[id]
	if !ok {
		return nil, io.EOF
	}
	return t, nil
}

// A gateway over the real pools manager and a fresh database, served over HTTP with cleartext HTTP/2
type harness struct {
	t     *testing.T
	g     *Gateway
	mgr   *pools.Manager
	db    *db.DB
	bus   *events.Bus
	store datalayer.Store
	inst  *fakeInstances
	srv   *httptest.Server
	reg   *prometheus.Registry
	hreg  *health.Registry
	// Every sampled span as it ends
	spans *tracetest.InMemoryExporter
}

// Options a test changes on the gateway it starts
type setup struct {
	keys     []string
	guard    *auth.Guard
	policy   *v1.Policy
	flow     *flowcontrol.Config
	peers    Peers
	runtimes fakeRuntimes
	unsynced bool
	// Runs the manager's scrape, probe and catalog sources
	scrape bool
	// The pace metrics are scraped at, every twenty milliseconds whatever the demand when zero
	pace scrape.Pace
	// The request chain document, the built in one when empty
	chain []byte
	// Stored adapters a request may name, none when nil
	adapters Adapters
}

func newHarness(t *testing.T, opts ...func(*setup)) *harness {
	t.Helper()
	s := setup{guard: auth.NewGuard("", nil, nil), policy: &v1.Policy{}, runtimes: fakeRuntimes{api: map[string]v1.ApiFlavor{
		"vllm":   v1.ApiFlavor_API_FLAVOR_OPENAI,
		"sglang": v1.ApiFlavor_API_FLAVOR_OPENAI,
		"ollama": v1.ApiFlavor_API_FLAVOR_OLLAMA,
		"sdcpp":  v1.ApiFlavor_API_FLAVOR_SDCPP,
	}, handoff: map[string]string{}}}
	for _, o := range opts {
		o(&s)
	}
	if s.pace == (scrape.Pace{}) {
		s.pace = scrape.Pace{Deciding: 20 * time.Millisecond, Working: 20 * time.Millisecond, Idle: 20 * time.Millisecond}
	}
	ctx, cancel := context.WithCancel(context.Background())
	d, err := db.Open(filepath.Join(t.TempDir(), "nebu.db"))
	if err != nil {
		t.Fatal(err)
	}
	bus := events.New()
	log := slog.New(slog.DiscardHandler)
	mgr, err := pools.New(ctx, pools.Options{DB: d, Events: bus, NodeID: testNode, Log: log, Scrape: s.pace, ProbeInterval: 20 * time.Millisecond, CatalogInterval: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if !s.unsynced {
		mgr.Store().MarkSynced()
	}
	spans := tracetest.NewInMemoryExporter()
	tp, shutdown, err := telemetry.Setup(ctx, telemetry.Config{ServiceName: "gateway-test", SampleRatio: 1, Exporter: spans})
	if err != nil {
		t.Fatal(err)
	}
	reg := prometheus.NewRegistry()
	mreg, err := metrics.New(reg)
	if err != nil {
		t.Fatal(err)
	}
	flow := flowcontrol.Config{QueueCapacity: 64, QueueTimeout: 2 * time.Second, SheddableBelow: -1, Tick: 10 * time.Millisecond}
	if s.flow != nil {
		flow = *s.flow
	}
	inst := &fakeInstances{support: map[string]v1.ToolSupport{}, modes: map[string][]string{}, templates: map[string]*chattemplate.Template{}, files: map[string]string{}}
	hreg := health.NewRegistry()
	g, err := New(Deps{
		Pools: mgr, Resolver: mgr, Manager: mgr, Store: mgr.Store(), Sampler: mgr.Sampler(), DB: d, Events: bus, Guard: s.guard,
		Tools: tools.New(log, bus), Peers: s.peers, Runtimes: s.runtimes, Instances: inst, Adapters: s.adapters,
		Metrics: mreg, Telemetry: tp, Health: hreg, Logger: log,
		Config: Config{
			Keys: s.keys, Policy: s.policy, OutputTokens: 64, Flow: flow,
			Saturation: flowcontrol.Thresholds{QueueDepth: 8, KVUtilization: 0.9}, Staleness: 2 * time.Second,
			Retry: Retry{Shed: time.Second, Unavailable: 2 * time.Second}, TraceRetention: 24 * time.Hour, TraceRing: 64,
			Version: "test", Interval: 50 * time.Millisecond, Chain: s.chain,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	mgr.SetIndexer(g.Index())
	if s.scrape {
		if err := mgr.Start(ctx); err != nil {
			t.Fatal(err)
		}
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		g.Run(ctx)
	}()
	srv := httptest.NewUnstartedServer(g.Handler())
	srv.Config.Protocols = new(http.Protocols)
	srv.Config.Protocols.SetHTTP1(true)
	srv.Config.Protocols.SetUnencryptedHTTP2(true)
	srv.Start()
	t.Cleanup(func() {
		srv.Close()
		cancel()
		<-done
		mgr.Close()
		shutdown(context.Background())
		d.Close()
	})
	return &harness{t: t, g: g, mgr: mgr, db: d, bus: bus, store: mgr.Store(), inst: inst, srv: srv, reg: reg, hreg: hreg, spans: spans}
}

// A model key per test model name
func key(model string) pools.ModelKey {
	return pools.ModelKey{SourceID: "hf", Repo: model, Group: "q4"}
}

// Publishes one metric less instance per address in a pool of the model and runtime, naming a route
func (h *harness) publish(route, model, runtime string, proto datalayer.AppProtocol, ready bool, spec pools.RouteSpec, addrs ...string) datalayer.PoolID {
	h.t.Helper()
	ctx := context.Background()
	var first pools.OwnerRef
	for i, a := range addrs {
		addr, err := datalayer.ParseAddress(strings.TrimPrefix(a, "http://"))
		if err != nil {
			h.t.Fatal(err)
		}
		owner := pools.OwnerRef{Kind: pools.OwnerInstance, ID: route + "-" + addr.String()}
		if i == 0 {
			first = owner
		}
		mem := pools.Membership{Model: key(model), Runtime: runtime, Protocol: proto, Concurrency: 8, Metrics: &v1.MetricNames{HealthPath: "/health"}}
		if err := h.mgr.Publish(ctx, owner, mem, []pools.Published{{InstanceID: owner.ID, NodeID: testNode, Address: addr, Ready: ready}}); err != nil {
			h.t.Fatal(err)
		}
	}
	if route != "" {
		if _, err := h.mgr.NameRoute(ctx, first, route, spec); err != nil {
			h.t.Fatal(err)
		}
	}
	return pools.PoolIDOf(key(model), runtime, "")
}

// The id of the one endpoint a pool holds, the endpoint label of its request metrics
func (h *harness) endpointID(pool datalayer.PoolID) datalayer.EndpointID {
	h.t.Helper()
	eps := h.store.Endpoints(pool, nil)
	if len(eps) != 1 {
		h.t.Fatalf("pool %s holds %d endpoints, want one", pool, len(eps))
	}
	return eps[0].ID()
}

// A started stub answering as model, closed with the test
func (h *harness) stub(model string) (*stub.Stub, *httptest.Server) {
	h.t.Helper()
	s, err := stub.New(stub.Config{ID: model, Model: model})
	if err != nil {
		h.t.Fatal(err)
	}
	srv := httptest.NewServer(s)
	h.t.Cleanup(func() {
		srv.Close()
		s.Close()
	})
	return s, srv
}

// A reply read whole
type reply struct {
	status int
	header http.Header
	body   []byte
}

// Sends a body to the gateway with header pairs and reads the whole reply
func (h *harness) do(method, path, body string, header ...string) reply {
	h.t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, h.srv.URL+path, rd)
	if err != nil {
		h.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	resp, err := h.srv.Client().Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		h.t.Fatal(err)
	}
	return reply{status: resp.StatusCode, header: resp.Header, body: raw}
}

// Decodes a JSON reply body
func (r reply) json(t *testing.T) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(r.body, &out); err != nil {
		t.Fatalf("reply %d is not JSON: %v\n%s", r.status, err, r.body)
	}
	return out
}

// The data payloads of a server sent event stream
func ssePayloads(body []byte) []string {
	var out []string
	sc := bufio.NewScanner(bytes.NewReader(body))
	for sc.Scan() {
		if d, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
			out = append(out, d)
		}
	}
	return out
}

// Waits for a condition, failing the test after five seconds
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// The address an httptest server listens on
func hostOf(srv *httptest.Server) string { return strings.TrimPrefix(srv.URL, "http://") }
