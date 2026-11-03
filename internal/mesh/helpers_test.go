package mesh

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/nickheyer/nebu/internal/auth"
	"github.com/nickheyer/nebu/internal/db"
	"github.com/nickheyer/nebu/internal/installs"
	"github.com/nickheyer/nebu/internal/pools"
	"github.com/nickheyer/nebu/pkg/datalayer"
	"github.com/nickheyer/nebu/pkg/events"
	"github.com/nickheyer/nebu/pkg/host"
	"github.com/nickheyer/nebu/pkg/perf"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/runtimes"
	"github.com/nickheyer/nebu/pkg/store"
	"google.golang.org/protobuf/proto"
)

// One Publish a fake registrar took
type publishCall struct {
	model      pools.ModelKey
	runtime    string
	kvCapacity uint32
	endpoints  []pools.Published
}

// A pool view and registrar the tests drive, recording what imports publish and name
type fakePools struct {
	mu        sync.Mutex
	store     datalayer.Store
	exported  []*v1.Pool
	routes    []*v1.Route
	published map[pools.OwnerRef]publishCall
	named     map[pools.OwnerRef]map[string]pools.RouteSpec
	// Objectives this node defines, and those held for owners with the holders of each
	local   map[string]*v1.Objective
	held    map[string]*v1.Objective
	holders map[string]map[pools.OwnerRef]bool
	// Registrar calls in order, as "drain", "unpublish" or "droproutes" and the owner id, and requests in flight per instance
	calls    []string
	inflight map[string]int
}

func newFakePools() *fakePools {
	return &fakePools{
		store: datalayer.NewStore(), published: map[pools.OwnerRef]publishCall{}, named: map[pools.OwnerRef]map[string]pools.RouteSpec{},
		local: map[string]*v1.Objective{}, held: map[string]*v1.Objective{}, holders: map[string]map[pools.OwnerRef]bool{},
		inflight: map[string]int{},
	}
}

func (f *fakePools) RouteView(name string) (*v1.Route, bool) { return f.Route(name) }

// Requests in flight on an instance, as the tests set them
func (f *fakePools) InFlight(instance string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.inflight[instance]
}

// Sets the requests in flight on an instance
func (f *fakePools) setInFlight(instance string, n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inflight[instance] = n
}

// The registrar calls taken so far, in order
func (f *fakePools) log() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

func (f *fakePools) Store() datalayer.Store { return f.store }

func (f *fakePools) Pools() []*v1.Pool { return f.Exported() }

func (f *fakePools) Pool(id string) (*v1.Pool, bool) {
	for _, p := range f.Exported() {
		if p.GetId() == id {
			return p, true
		}
	}
	return nil, false
}

func (f *fakePools) Routes() []*v1.Route {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*v1.Route, len(f.routes))
	for i, r := range f.routes {
		out[i] = proto.Clone(r).(*v1.Route)
	}
	return out
}

func (f *fakePools) Route(name string) (*v1.Route, bool) {
	for _, r := range f.Routes() {
		if r.GetName() == name {
			return r, true
		}
	}
	return nil, false
}

func (f *fakePools) Exported() []*v1.Pool {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*v1.Pool, len(f.exported))
	for i, p := range f.exported {
		out[i] = proto.Clone(p).(*v1.Pool)
	}
	return out
}

func (f *fakePools) set(exported []*v1.Pool, routes []*v1.Route) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.exported, f.routes = exported, routes
}

func (f *fakePools) Publish(ctx context.Context, owner pools.OwnerRef, m pools.Membership, endpoints []pools.Published) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.published[owner] = publishCall{model: m.Model, runtime: m.Runtime, kvCapacity: m.KVCapacity, endpoints: endpoints}
	return nil
}

func (f *fakePools) SetReady(ctx context.Context, instanceID string, ready bool) {}

func (f *fakePools) SetRankReady(ctx context.Context, instanceID string, rank uint32, ready bool) {}

func (f *fakePools) Unpublish(ctx context.Context, owner pools.OwnerRef) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "unpublish "+owner.ID)
	delete(f.published, owner)
}

func (f *fakePools) Objective(name string) (*v1.Objective, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if o, ok := f.local[name]; ok {
		return proto.Clone(o).(*v1.Objective), true
	}
	o, ok := f.held[name]
	return o, ok
}

func (f *fakePools) PutObjectiveFor(ctx context.Context, owner pools.OwnerRef, o *v1.Objective) (*v1.Objective, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.held[o.GetName()] = proto.Clone(o).(*v1.Objective)
	if f.holders[o.GetName()] == nil {
		f.holders[o.GetName()] = map[pools.OwnerRef]bool{}
	}
	f.holders[o.GetName()][owner] = true
	return o, nil
}

func (f *fakePools) DropObjective(ctx context.Context, owner pools.OwnerRef, name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.holders[name], owner)
	if len(f.holders[name]) == 0 {
		delete(f.holders, name)
		delete(f.held, name)
	}
}

func (f *fakePools) DropObjectives(ctx context.Context, owner pools.OwnerRef) {
	f.mu.Lock()
	names := make([]string, 0, len(f.holders))
	for name := range f.holders {
		names = append(names, name)
	}
	f.mu.Unlock()
	for _, name := range names {
		f.DropObjective(ctx, owner, name)
	}
}

func (f *fakePools) NameRoute(ctx context.Context, owner pools.OwnerRef, name string, spec pools.RouteSpec) (*v1.Route, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if spec.Objective != "" && f.held[spec.Objective] == nil && f.local[spec.Objective] == nil {
		return nil, fmt.Errorf("objective %q does not exist", spec.Objective)
	}
	if f.named[owner] == nil {
		f.named[owner] = map[string]pools.RouteSpec{}
	}
	f.named[owner][name] = spec
	return &v1.Route{Name: name, OwnerKind: owner.Kind, OwnerId: owner.ID}, nil
}

func (f *fakePools) Drain(ctx context.Context, owner pools.OwnerRef) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "drain "+owner.ID)
}

func (f *fakePools) SplitRoute(ctx context.Context, owner pools.OwnerRef, name string, targets []pools.WeightedTarget) error {
	return nil
}

func (f *fakePools) DropRoute(ctx context.Context, owner pools.OwnerRef, name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.named[owner], name)
}

func (f *fakePools) DropRoutes(ctx context.Context, owner pools.OwnerRef) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "droproutes "+owner.ID)
	delete(f.named, owner)
}

// Defines an objective on this node
func (f *fakePools) define(o *v1.Objective) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.local[o.GetName()] = o
}

// The objectives held for an owner by name
func (f *fakePools) heldFor(owner pools.OwnerRef) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for name, holders := range f.holders {
		if holders[owner] {
			out = append(out, name)
		}
	}
	return out
}

// What the registrar holds now, published owners and the routes named under each
func (f *fakePools) snapshot() (map[pools.OwnerRef]publishCall, map[pools.OwnerRef][]string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	pub := map[pools.OwnerRef]publishCall{}
	for k, v := range f.published {
		pub[k] = v
	}
	named := map[pools.OwnerRef][]string{}
	for k, v := range f.named {
		for name := range v {
			named[k] = append(named[k], name)
		}
	}
	return pub, named
}

// A test node with its own store and fake pools, listening on loopback and advertising it
type testNode struct {
	*Manager
	pools *fakePools
}

// The logger tests run with, silent unless a test fails loudly on its own
func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// Starts a node whose gossip rounds last the interval given
func newNode(t *testing.T, ctx context.Context, interval time.Duration, opts ...func(*Manager)) *testNode {
	t.Helper()
	dir := t.TempDir()
	database, err := db.Open(filepath.Join(dir, "nebu.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	blobs, err := store.Open(filepath.Join(dir, "store"))
	if err != nil {
		t.Fatal(err)
	}
	rts, err := runtimes.New(runtimes.All())
	if err != nil {
		t.Fatal(err)
	}
	table, err := perf.Open(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	fake := newFakePools()
	guard := auth.NewGuard("", nil, nil)
	m := &Manager{
		DB:        database,
		Config:    &v1.MeshConfig{Listen: "127.0.0.1:0", Advertise: "127.0.0.1", Announce: proto.Bool(false)},
		Host:      host.New(nil, nil, time.Minute),
		Installs:  &installs.Manager{DB: database},
		Runtimes:  rts,
		Store:     blobs,
		Perf:      table,
		Pools:     fake,
		Registrar: fake,
		Events:    events.New(),
		Guard:     guard,
		Log:       quietLog(),
		Version:   "test",
		InFlight:  fake.InFlight,
		interval:  interval,
	}
	for _, o := range opts {
		o(m)
	}
	if err := m.Open(ctx); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	// Answers with the member the certificate proved and the member the bearer names
	mux.HandleFunc("/test/peer", func(w http.ResponseWriter, r *http.Request) {
		c, _ := callerOf(r.Context())
		bearer, _ := m.Peer(auth.Bearer(r.Header))
		io.WriteString(w, c.id+"|"+bearer)
	})
	m.Handler = mux
	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	return &testNode{Manager: m, pools: fake}
}

// A manager with its tables and stores made and no listener, for driving merges and beacons by hand
func bare(t *testing.T, id string) *Manager {
	t.Helper()
	dir := t.TempDir()
	database, err := db.Open(filepath.Join(dir, "nebu.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	blobs, err := store.Open(filepath.Join(dir, "store"))
	if err != nil {
		t.Fatal(err)
	}
	rts, err := runtimes.New(runtimes.All())
	if err != nil {
		t.Fatal(err)
	}
	fake := newFakePools()
	m := &Manager{
		DB:            database,
		Installs:      &installs.Manager{DB: database},
		Runtimes:      rts,
		Store:         blobs,
		Config:        &v1.MeshConfig{},
		Host:          host.New(nil, nil, time.Minute),
		Events:        events.New(),
		Log:           quietLog(),
		Pools:         fake,
		Registrar:     fake,
		InFlight:      fake.InFlight,
		identity:      &db.Identity{ID: id},
		interval:      SyncInterval,
		members:       map[string]*member{},
		nearby:        map[string]*heard{},
		admissions:    map[string]*v1.Admission{},
		grants:        map[string]string{},
		sessions:      map[string]*session{},
		accept:        map[string]string{},
		links:         map[string]*v1.Link{},
		revoked:       map[string]*v1.Revocation{},
		imports:       map[string]map[string]*imported{},
		objectives:    map[string]map[string][32]byte{},
		unpublished:   map[string]*v1.Condition{},
		relays:        map[string]*relay{},
		remoteAddrs:   map[string]remoteTarget{},
		exporters:     map[string]*federated{},
		importers:     map[string]*v1.Node{},
		parts:         map[v1.RecordPart]*partState{},
		h2:            map[string]http.RoundTripper{},
		wake:          make(chan struct{}, 1),
		defaultListen: DefaultListen,
	}
	m.reissueMember = m.reissueOne
	t.Cleanup(m.Close)
	return m
}

// Waits up to the deadline for a condition, failing the test when it never holds
func eventually(t *testing.T, what string, within time.Duration, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s did not happen within %s", what, within)
}

// Makes a mesh on the first node and joins every other through a token of its own
func formMesh(t *testing.T, ctx context.Context, nodes ...*testNode) {
	t.Helper()
	if _, _, err := nodes[0].Init(ctx, "test"); err != nil {
		t.Fatal(err)
	}
	for _, n := range nodes[1:] {
		token, _, err := nodes[0].Token(ctx, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := n.Join(ctx, token); err != nil {
			t.Fatal(err)
		}
	}
}

// Whether a node holds a ready record of another, filled by a sync
func met(m *Manager, id string) bool {
	rec, ok := m.Member(id)
	return ok && rec.GetState() == v1.NodeState_NODE_STATE_READY && rec.GetProfile() != nil
}

// Whether every node has met every other
func allMet(nodes ...*testNode) bool {
	for _, a := range nodes {
		for _, b := range nodes {
			if a != b && !met(a.Manager, b.Self()) {
				return false
			}
		}
	}
	return true
}

// Waits until no node's record sequence has moved for a second, so probes and pushes are done
func settle(t *testing.T, nodes ...*testNode) {
	t.Helper()
	seqs := func() []uint64 {
		var out []uint64
		for _, n := range nodes {
			out = append(out, n.Record().GetSequence())
		}
		return out
	}
	last, quiet := seqs(), time.Now()
	deadline := time.Now().Add(60 * time.Second)
	for time.Since(quiet) < time.Second {
		if time.Now().After(deadline) {
			t.Fatal("the records never settled")
		}
		time.Sleep(50 * time.Millisecond)
		if now := seqs(); !slices.Equal(now, last) {
			last, quiet = now, time.Now()
		}
	}
}
