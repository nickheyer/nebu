package requestcontrol

import (
	"context"
	"net/http"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nickheyer/nebu/pkg/bodyparse"
	"github.com/nickheyer/nebu/pkg/datalayer"
	"github.com/nickheyer/nebu/pkg/flowcontrol"
	"github.com/nickheyer/nebu/pkg/scheduling/framework"
)

type fakeEndpoint struct {
	spec  datalayer.EndpointSpec
	attrs map[string]any
}

func (e *fakeEndpoint) ID() datalayer.EndpointID       { return e.spec.ID }
func (e *fakeEndpoint) Pool() datalayer.PoolID         { return e.spec.Pool }
func (e *fakeEndpoint) Address() datalayer.Address     { return e.spec.Address }
func (e *fakeEndpoint) Labels() map[string]string      { return e.spec.Labels }
func (e *fakeEndpoint) Ready() bool                    { return e.spec.Ready }
func (e *fakeEndpoint) Attribute(c string) (any, bool) { v, ok := e.attrs[c]; return v, ok }

type fakeStore struct {
	mu    sync.RWMutex
	pools map[datalayer.PoolID]datalayer.Pool
	eps   []*fakeEndpoint
}

func newFakeStore() *fakeStore {
	return &fakeStore{pools: map[datalayer.PoolID]datalayer.Pool{}}
}

func (s *fakeStore) PutPool(p datalayer.Pool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pools[p.ID] = p
}

func (s *fakeStore) DeletePool(id datalayer.PoolID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.pools, id)
}

func (s *fakeStore) Pool(id datalayer.PoolID) (datalayer.Pool, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.pools[id]
	return p, ok
}

func (s *fakeStore) Pools() []datalayer.Pool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []datalayer.Pool
	for _, p := range s.pools {
		out = append(out, p)
	}
	return out
}

func (s *fakeStore) UpsertEndpoint(spec datalayer.EndpointSpec) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.eps {
		if e.spec.ID == spec.ID {
			e.spec = spec
			return
		}
	}
	s.eps = append(s.eps, &fakeEndpoint{spec: spec, attrs: map[string]any{}})
}

func (s *fakeStore) DeleteEndpoint(id datalayer.EndpointID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.eps = slices.DeleteFunc(s.eps, func(e *fakeEndpoint) bool { return e.spec.ID == id })
}

func (s *fakeStore) Endpoint(id datalayer.EndpointID) (datalayer.Endpoint, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, e := range s.eps {
		if e.spec.ID == id {
			return e, true
		}
	}
	return nil, false
}

func (s *fakeStore) Endpoints(pool datalayer.PoolID, keep func(datalayer.Endpoint) bool) []datalayer.Endpoint {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []datalayer.Endpoint
	for _, e := range s.eps {
		if (pool == "" || e.spec.Pool == pool) && keep(e) {
			out = append(out, e)
		}
	}
	return out
}

func (s *fakeStore) SetAttribute(id datalayer.EndpointID, collector string, value any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.eps {
		if e.spec.ID == id {
			e.attrs[collector] = value
		}
	}
}

func (s *fakeStore) Watch(ctx context.Context) <-chan datalayer.Change {
	ch := make(chan datalayer.Change)
	go func() {
		<-ctx.Done()
		close(ch)
	}()
	return ch
}

func (s *fakeStore) Synced() bool { return true }
func (s *fakeStore) MarkSynced()  {}

// Schedules by fn, the default keeping ready endpoints inside the subset in store order
type fakeScheduler struct {
	fn    func(ctx context.Context, req *framework.Request, candidates []datalayer.Endpoint) (*framework.Result, error)
	calls atomic.Int64
}

func pickAll(_ context.Context, req *framework.Request, candidates []datalayer.Endpoint) (*framework.Result, error) {
	var eps []datalayer.Endpoint
	for _, ep := range candidates {
		if datalayer.ReadyAndHealthy(ep) && (!req.SubsetSet || slices.Contains(req.Subset, ep.Address())) {
			eps = append(eps, ep)
		}
	}
	if len(eps) == 0 {
		return nil, framework.ErrNoEndpoints
	}
	return &framework.Result{PrimaryProfile: "default", ProfileResults: map[string]*framework.ProfileRunResult{"default": {Endpoints: eps}}}, nil
}

func (s *fakeScheduler) Schedule(ctx context.Context, req *framework.Request, candidates []datalayer.Endpoint) (*framework.Result, error) {
	s.calls.Add(1)
	if s.fn == nil {
		return pickAll(ctx, req, candidates)
	}
	return s.fn(ctx, req, candidates)
}

func (s *fakeScheduler) Profiles() map[string]*framework.Profile { return nil }

type fakeFlow struct {
	mu       sync.Mutex
	items    []flowcontrol.Item
	err      error
	admitted atomic.Int64
	released atomic.Int64
	doubled  atomic.Int64
	requeued atomic.Int64
	// Tokens the last settled permit reported
	prompt atomic.Int64
	output atomic.Int64
}

func (f *fakeFlow) Admit(ctx context.Context, item *flowcontrol.Item) (flowcontrol.Permit, error) {
	f.mu.Lock()
	f.items = append(f.items, *item)
	err := f.err
	f.mu.Unlock()
	if err != nil {
		return nil, err
	}
	f.admitted.Add(1)
	return &fakePermit{flow: f}, nil
}

// Releases the permit and hands out a fresh one, counting the round trip
func (f *fakeFlow) Requeue(ctx context.Context, pm flowcontrol.Permit, item *flowcontrol.Item) (flowcontrol.Permit, error) {
	pm.Release()
	f.requeued.Add(1)
	return f.Admit(ctx, item)
}

func (f *fakeFlow) Usage(string) flowcontrol.Usage { return flowcontrol.Usage{} }

func (f *fakeFlow) Depth(datalayer.PoolID) map[int]int { return nil }
func (f *fakeFlow) InFlight(datalayer.PoolID) int {
	return int(f.admitted.Load() - f.released.Load())
}
func (f *fakeFlow) Close() {}

func (f *fakeFlow) lastItem(t *testing.T) flowcontrol.Item {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.items) == 0 {
		t.Fatal("flow control saw no item")
	}
	return f.items[len(f.items)-1]
}

// Counts releases on its flow, flagging a second release of the same permit
type fakePermit struct {
	flow *fakeFlow
	n    atomic.Int32
}

func (p *fakePermit) Release() {
	if p.n.Add(1) == 1 {
		p.flow.released.Add(1)
	} else {
		p.flow.doubled.Add(1)
	}
}

func (p *fakePermit) Settle(prompt, output int) {
	p.flow.prompt.Store(int64(prompt))
	p.flow.output.Store(int64(output))
}

func (p *fakePermit) Waited() time.Duration { return 0 }

// Records named events in order across goroutines
type recorder struct {
	mu     sync.Mutex
	events []string
}

func (r *recorder) add(e string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
}

func (r *recorder) list() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.events)
}

func (r *recorder) count(e string) int {
	n := 0
	for _, x := range r.list() {
		if x == e {
			n++
		}
	}
	return n
}

// A scheduler lifecycle plugin recording every framework hook
type lifeHook struct {
	name string
	rec  *recorder
}

func (h *lifeHook) TypedName() framework.TypedName {
	return framework.TypedName{Type: "life", Name: h.name}
}
func (h *lifeHook) PreRequest(context.Context, *framework.Request, *framework.Result) {
	h.rec.add(h.name + ".pre")
}
func (h *lifeHook) ResponseReceived(context.Context, *framework.Request, *framework.Response) {
	h.rec.add(h.name + ".received")
}
func (h *lifeHook) ResponseStreaming(_ context.Context, _ *framework.Request, _ *framework.Response, chunk []byte) {
	h.rec.add(h.name + ".chunk:" + string(chunk))
}
func (h *lifeHook) ResponseComplete(context.Context, *framework.Request, *framework.Response) {
	h.rec.add(h.name + ".complete")
}

type prePlugin struct {
	name string
	fn   func(ctx context.Context, in *Inbound, req *framework.Request) error
}

func (p *prePlugin) TypedName() framework.TypedName {
	return framework.TypedName{Type: "pre", Name: p.name}
}
func (p *prePlugin) PreRequest(ctx context.Context, in *Inbound, req *framework.Request) error {
	return p.fn(ctx, in, req)
}

type postPlugin struct {
	name string
	fn   func(ctx context.Context, in *Inbound, dec *Decision) error
}

func (p *postPlugin) TypedName() framework.TypedName {
	return framework.TypedName{Type: "post", Name: p.name}
}
func (p *postPlugin) PostDecision(ctx context.Context, in *Inbound, dec *Decision) error {
	return p.fn(ctx, in, dec)
}

type headersPlugin struct {
	name string
	fn   func(ctx context.Context, dec *Decision, resp *framework.Response, h http.Header) error
}

func (p *headersPlugin) TypedName() framework.TypedName {
	return framework.TypedName{Type: "headers", Name: p.name}
}
func (p *headersPlugin) ResponseHeaders(ctx context.Context, dec *Decision, resp *framework.Response, h http.Header) error {
	return p.fn(ctx, dec, resp, h)
}

type bodyPlugin struct {
	name string
	fn   func(chunk []byte, end bool) ([]byte, error)
}

func (p *bodyPlugin) TypedName() framework.TypedName {
	return framework.TypedName{Type: "body", Name: p.name}
}
func (p *bodyPlugin) ResponseBody(_ context.Context, _ *Decision, _ *framework.Response, chunk []byte, end bool) ([]byte, error) {
	return p.fn(chunk, end)
}

type donePlugin struct {
	name string
	rec  *recorder
}

func (p *donePlugin) TypedName() framework.TypedName {
	return framework.TypedName{Type: "done", Name: p.name}
}
func (p *donePlugin) ResponseComplete(context.Context, *Decision, *framework.Response) {
	p.rec.add(p.name + ".done")
}

// A plugin taking part in the pre request and post decision stages
type bothPlugin struct {
	name string
}

func (p *bothPlugin) TypedName() framework.TypedName {
	return framework.TypedName{Type: "both", Name: p.name}
}
func (p *bothPlugin) PreRequest(context.Context, *Inbound, *framework.Request) error { return nil }
func (p *bothPlugin) PostDecision(context.Context, *Inbound, *Decision) error        { return nil }

// A sampler over a store nothing scrapes
type idleSampler struct{}

func (idleSampler) Deciding(context.Context, []datalayer.EndpointID) func() { return func() {} }
func (idleSampler) Serving(datalayer.EndpointID) func()                     { return func() {} }
func (idleSampler) Sample(context.Context, []datalayer.EndpointID)          {}

type countingObserver struct {
	decided, refused, completed atomic.Int64
}

func (o *countingObserver) Decided(*Inbound, *Decision, time.Duration) { o.decided.Add(1) }
func (o *countingObserver) Refused(*Inbound, error, time.Duration)     { o.refused.Add(1) }
func (o *countingObserver) Completed(*Decision, *framework.Response)   { o.completed.Add(1) }

// A store, rule table, flow and scheduler around one director
type env struct {
	store    *fakeStore
	table    *RuleTable
	flow     *fakeFlow
	sched    *fakeScheduler
	observer *countingObserver
	dir      *director
}

const defaultOutput = 128

func endpoint(id string, pool datalayer.PoolID, host string, ready bool) datalayer.EndpointSpec {
	return datalayer.EndpointSpec{
		ID:      datalayer.EndpointID(id),
		Pool:    pool,
		Address: datalayer.Address{Host: host, Port: 8000},
		Ready:   ready,
	}
}

// Pool p1 serving llama and adapter llama-lora on two ready endpoints, plus deps edits
func newEnv(t testing.TB, edit ...func(*Deps)) *env {
	t.Helper()
	e := &env{store: newFakeStore(), flow: &fakeFlow{}, sched: &fakeScheduler{}, observer: &countingObserver{}}
	e.store.PutPool(datalayer.Pool{ID: "p1", Model: "llama", FailureMode: datalayer.FailClose})
	e.store.UpsertEndpoint(endpoint("e1", "p1", "10.0.0.1", true))
	e.store.UpsertEndpoint(endpoint("e2", "p1", "10.0.0.2", true))
	e.table = NewRuleTable(e.store)
	e.table.Put("p1", []string{"llama"}, []string{"llama-lora"})
	deps := Deps{
		Store:               e.store,
		Resolver:            e.table,
		Sampler:             idleSampler{},
		Scheduler:           e.sched,
		Flow:                e.flow,
		Observer:            e.observer,
		DefaultOutputTokens: defaultOutput,
	}
	for _, f := range edit {
		f(&deps)
	}
	d, err := NewDirector(deps)
	if err != nil {
		t.Fatal(err)
	}
	e.dir = d.(*director)
	return e
}

func withTopology(t *testing.T, regs ...Registration) func(*Deps) {
	t.Helper()
	topo, err := NewTopology(regs...)
	if err != nil {
		t.Fatal(err)
	}
	return func(d *Deps) { d.Topology = topo }
}

// An inbound chat request with a body and header pairs
func inbound(t testing.TB, body string, headers ...string) *Inbound {
	t.Helper()
	in := &Inbound{Method: http.MethodPost, Path: "/v1/chat/completions", Headers: http.Header{}, ReceivedAt: time.Now()}
	for i := 0; i+1 < len(headers); i += 2 {
		in.Headers.Add(headers[i], headers[i+1])
	}
	if body != "" {
		doc, err := bodyparse.Parse(bodyparse.KindChatCompletions, []byte(body))
		if err != nil {
			t.Fatal(err)
		}
		in.Body = doc
	}
	return in
}

func chat(model string) string {
	return `{"model":"` + model + `","messages":[{"role":"user","content":"hello world"}]}`
}

func (e *env) settled(t *testing.T) {
	t.Helper()
	if a, r, d := e.flow.admitted.Load(), e.flow.released.Load(), e.flow.doubled.Load(); a != r || d != 0 {
		t.Fatalf("admitted %d released %d double released %d", a, r, d)
	}
	e.dir.loads.mu.Lock()
	defer e.dir.loads.mu.Unlock()
	if len(e.dir.loads.n) != 0 {
		t.Fatalf("in flight counts left %v", e.dir.loads.n)
	}
}
