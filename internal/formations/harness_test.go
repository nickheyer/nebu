package formations_test

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/nickheyer/nebu/internal/auth"
	"github.com/nickheyer/nebu/internal/db"
	"github.com/nickheyer/nebu/internal/formations"
	"github.com/nickheyer/nebu/internal/gateway"
	"github.com/nickheyer/nebu/internal/inspect"
	"github.com/nickheyer/nebu/internal/installs"
	"github.com/nickheyer/nebu/internal/instances"
	"github.com/nickheyer/nebu/internal/instances/fake"
	"github.com/nickheyer/nebu/internal/mesh"
	"github.com/nickheyer/nebu/internal/mesh/links"
	"github.com/nickheyer/nebu/internal/rpc/services"
	"github.com/nickheyer/nebu/internal/slots"
	"github.com/nickheyer/nebu/internal/tasks"
	"github.com/nickheyer/nebu/pkg/events"
	"github.com/nickheyer/nebu/pkg/host"
	"github.com/nickheyer/nebu/pkg/perf"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/proto/nebu/v1/nebuv1connect"
	"github.com/nickheyer/nebu/pkg/runtimes"
	"github.com/nickheyer/nebu/pkg/store"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	waitLong = 40 * time.Second
	modelKey = "org/model:q4"
)

// One daemon's worth of managers around a mesh node, with the fake runtime and launcher
type node struct {
	t          *testing.T
	ctx        context.Context
	name       string
	dir        string
	db         *db.DB
	store      *store.Store
	bus        *events.Bus
	mesh       *mesh.Manager
	tasks      *tasks.Manager
	routes     *gateway.Table
	gateway    *gateway.Gateway
	launcher   *fake.Launcher
	instances  *instances.Manager
	formations *formations.Manager
	rts        *runtimes.Registry
	install    *v1.Install
	svc        *services.MeshService
	conductor  *conductor
}

// The conductor of a node as the mesh, the instances, and the store see it, swapped when a test
// restarts the conductor, since those hold their conductor for the daemon's lifetime
type conductor struct {
	mu sync.Mutex
	m  *formations.Manager
}

func (c *conductor) set(m *formations.Manager) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m = m
}

func (c *conductor) current() *formations.Manager {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.m
}

func (c *conductor) Conducted() []*v1.Formation                { return c.current().Conducted() }
func (c *conductor) Seats() []*v1.SeatRef                      { return c.current().Seats() }
func (c *conductor) Slots() []*v1.SlotRef                      { return c.current().Slots() }
func (c *conductor) Serving(nodeID string) bool                { return c.current().Serving(nodeID) }
func (c *conductor) Merge(from string, list []*v1.Formation)   { c.current().Merge(from, list) }
func (c *conductor) MemberState(nodeID string, s v1.NodeState) { c.current().MemberState(nodeID, s) }
func (c *conductor) Live(name string) bool                     { return c.current().Live(name) }
func (c *conductor) HeadReady(ctx context.Context, formationID string, since time.Time) error {
	return c.current().HeadReady(ctx, formationID, since)
}

// Records what the conductor told the slots
type slotLog struct {
	ch chan *v1.Formation
}

func (s *slotLog) Refs() []*v1.SlotRef                   { return nil }
func (s *slotLog) Names(slotID string) []slots.RouteName { return nil }
func (s *slotLog) Claim(ctx context.Context, slotID string, run *v1.RunRequest) (*v1.RunRequest, error) {
	return run, nil
}
func (s *slotLog) OnFormation(f *v1.Formation) {
	s.ch <- proto.Clone(f).(*v1.Formation)
}
func (s *slotLog) Get(id string) (*v1.Slot, *v1.Instance, error) {
	return nil, nil, errors.New("no slots in this test")
}

// Stands up a node: a database, a store, the fake runtime installed, a mesh listener on loopback,
// the gateway, the instances manager over the fake launcher, and the conductor
func newNode(t *testing.T, ctx context.Context, name string) *node {
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
	rts, err := runtimes.New([]runtimes.Runtime{fake.Runtime{}})
	if err != nil {
		t.Fatal(err)
	}
	table, err := perf.Open(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	install := &v1.Install{Id: db.NewID(), RuntimeId: fake.RuntimeID, Kind: v1.InstallKind_INSTALL_KIND_ADOPTED, Path: filepath.Join(dir, "fake"), Dir: dir, Version: "1", CreatedAt: timestamppb.Now()}
	if err := database.PutInstall(ctx, install); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})).With("node", name)
	bus := events.New()
	guard := auth.NewGuard("", nil, nil)
	prober := host.New(nil, nil, time.Minute)
	inst := &installs.Manager{DB: database}
	n := &node{t: t, ctx: ctx, name: name, dir: dir, db: database, store: blobs, bus: bus, rts: rts, install: install, launcher: fake.New(), conductor: &conductor{}}
	n.mesh = &mesh.Manager{
		DB:        database,
		Config:    &v1.MeshConfig{Listen: "127.0.0.1:0", Advertise: "127.0.0.1", Announce: proto.Bool(false)},
		Host:      prober,
		Installs:  inst,
		Runtimes:  rts,
		Store:     blobs,
		Perf:      table,
		Events:    bus,
		Guard:     guard,
		Log:       log,
		Version:   "test",
		APIListen: "127.0.0.1:0",
		Settings:  nil,
	}
	if err := n.mesh.Open(ctx); err != nil {
		t.Fatal(err)
	}
	n.tasks = tasks.New(ctx, log, database, bus)
	n.routes, err = gateway.OpenTable(ctx, database, bus, log)
	if err != nil {
		t.Fatal(err)
	}
	n.routes.SetHandoffs(func(string) string { return fake.Runtime{}.Policy().Shapes.Handoff })
	n.gateway = gateway.New(n.routes, nil, nil, &v1.Policy{}, bus, log)
	n.instances = &instances.Manager{
		DB:        database,
		Dir:       filepath.Join(dir, "instances"),
		Store:     blobs,
		Runtimes:  rts,
		Installs:  inst,
		Inspector: &inspect.Inspector{Stored: blobs.ListManifests},
		Launcher:  n.launcher,
		Tasks:     n.tasks,
		Host:      prober,
		Routes:    n.routes,
		Events:    bus,
		Log:       log,
	}
	n.mesh.Formations = n.conductor
	n.instances.Formations = n.conductor
	blobs.CacheDir, blobs.KeepCache = filepath.Join(dir, "cache"), n.conductor.Live
	n.newFormations()
	n.svc = services.NewMeshService(n.mesh, n.formations, nil, n.tasks, blobs, table, n.instances, guard)
	mux := http.NewServeMux()
	mux.Handle(nebuv1connect.NewMeshServiceHandler(n.svc))
	mux.HandleFunc(links.SinkPath, links.Sink)
	n.mesh.Handler = h2c.NewHandler(mux, &http2.Server{})
	if err := n.mesh.Start(ctx); err != nil {
		t.Fatal(err)
	}
	n.formations.Start()
	t.Cleanup(func() {
		n.formations.Close()
		n.instances.Close()
		n.mesh.Close()
	})
	return n
}

// A conductor over this node's managers, the one the mesh and instances report to
func (n *node) newFormations() *formations.Manager {
	m := &formations.Manager{
		DB:           n.db,
		Mesh:         n.mesh,
		Instances:    n.instances,
		Routes:       n.routes,
		Tasks:        n.tasks,
		Inspector:    &inspect.Inspector{Stored: n.store.ListManifests},
		Runtimes:     n.rts,
		Installs:     &installs.Manager{DB: n.db},
		Store:        n.store,
		Events:       n.bus,
		Log:          slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})).With("node", n.name),
		CacheDir:     filepath.Join(n.dir, "cache"),
		Traces:       n.gateway.Traces(),
		Gateway:      n.gateway,
		DrainTimeout: 500 * time.Millisecond,
	}
	if err := m.Open(n.ctx); err != nil {
		n.t.Fatal(err)
	}
	n.formations = m
	n.conductor.set(m)
	return m
}

// Writes a stored model into the node's store, its weights a small file
func (n *node) storeModel(source, repo, group string) *v1.StoredModel {
	n.t.Helper()
	dir := filepath.Join(n.dir, "models", repo, group)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		n.t.Fatal(err)
	}
	file := filepath.Join(dir, "weights.fake")
	if err := os.WriteFile(file, []byte("weights of "+group), 0o644); err != nil {
		n.t.Fatal(err)
	}
	m := &v1.StoredModel{
		SourceId:    source,
		Repo:        repo,
		Group:       group,
		FormatId:    fake.Format,
		Bytes:       10,
		Path:        dir,
		PulledAt:    timestamppb.Now(),
		Descriptor_: &v1.Descriptor{Kind: v1.ModelKind_MODEL_KIND_LANGUAGE, Architecture: "fake", ParameterCount: 1000},
		Artifacts:   []*v1.StoredArtifact{{Digest: "sha256:" + repo + group, Path: file, Artifact: &v1.Artifact{Path: "weights.fake", Role: v1.ArtifactRole_ARTIFACT_ROLE_WEIGHTS}}},
	}
	if err := n.store.WriteManifest(m); err != nil {
		n.t.Fatal(err)
	}
	n.mesh.Bump()
	return m
}

// Two nodes in one mesh, each knowing the other's record and the link between them measured
func mesh2(t *testing.T, ctx context.Context) (*node, *node) {
	t.Helper()
	a, b := newNode(t, ctx, "alpha"), newNode(t, ctx, "beta")
	_, token, err := a.mesh.Init(ctx, "test", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := b.mesh.Join(ctx, token); err != nil {
		t.Fatal(err)
	}
	knows := func(n *node, other string) bool {
		rec, err := n.mesh.Node(other)
		return err == nil && rec.GetState() == v1.NodeState_NODE_STATE_READY && rec.GetProfile() != nil && rec.GetAddress() != ""
	}
	eventually(t, "the nodes know each other", func() bool { return knows(a, b.mesh.Self()) && knows(b, a.mesh.Self()) })
	if _, err := a.mesh.Probe(ctx, b.mesh.Self(), true); err != nil {
		t.Fatal(err)
	}
	if _, err := b.mesh.Probe(ctx, a.mesh.Self(), true); err != nil {
		t.Fatal(err)
	}
	return a, b
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(waitLong)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("%s did not happen within %s", what, waitLong)
}

// Waits for a formation to reach a state, failing when it ends in another
func waitFormation(t *testing.T, n *node, id string, want v1.FormationState) *v1.Formation {
	t.Helper()
	var last *v1.Formation
	deadline := time.Now().Add(waitLong)
	for time.Now().Before(deadline) {
		f, err := n.formations.Get(id)
		if err == nil {
			last = f
			if f.GetState() == want {
				return f
			}
			switch f.GetState() {
			case v1.FormationState_FORMATION_STATE_STOPPED, v1.FormationState_FORMATION_STATE_FAILED:
				t.Fatalf("formation %s ended %s wanting %s: %s", f.GetName(), f.GetState(), want, f.GetError())
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("formation %s did not reach %s within %s, last %v", id, want, waitLong, last)
	return nil
}

// A seat of a plan on a node
func seatOn(n *node, role string, rank, phase uint32) *v1.Seat {
	return &v1.Seat{NodeId: n.mesh.Self(), NodeName: n.mesh.Record().GetName(), Role: role, Rank: rank, Phase: phase, InstallId: n.install.GetId()}
}

// A plan of a shape over the seats given, fitting
func planOf(shape v1.Shape, seats ...*v1.Seat) *v1.FormationPlan {
	return &v1.FormationPlan{Shape: shape, Seats: seats, Verdict: v1.FitVerdict_FIT_VERDICT_FITS, Detail: "made by hand", RuntimeId: fake.RuntimeID}
}

// A run request of the model with parameters
func runOf(params map[string]string) *v1.RunRequest {
	return &v1.RunRequest{SourceId: "src", Repo: "org/model", Group: "q4", RuntimeId: fake.RuntimeID, Params: params}
}

// The seat of a formation by role
func seatByRole(f *v1.Formation, role string) *v1.Seat {
	for _, s := range f.GetSeats() {
		if s.GetRole() == role {
			return s
		}
	}
	return nil
}

// The plan every chain test launches: the stage on b, the head on a
func chainPlan(a, b *node) *v1.FormationPlan {
	return planOf(v1.Shape_SHAPE_CHAIN, seatOn(b, runtimes.RoleStage, 1, 1), seatOn(a, runtimes.RoleHead, 0, 2))
}

// A planner answering the same plan every time, for relaunches
func fixedPlanner(plan *v1.FormationPlan, stored *v1.StoredModel) func(context.Context, *v1.RunRequest) (formations.Planned, error) {
	return func(context.Context, *v1.RunRequest) (formations.Planned, error) {
		return formations.Planned{Plan: proto.Clone(plan).(*v1.FormationPlan), Stored: stored, Rt: fake.Runtime{}, Name: modelKey}, nil
	}
}
