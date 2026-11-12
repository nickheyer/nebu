package formations

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nickheyer/nebu/internal/auth"
	"github.com/nickheyer/nebu/internal/db"
	"github.com/nickheyer/nebu/internal/inspect"
	"github.com/nickheyer/nebu/internal/installs"
	"github.com/nickheyer/nebu/internal/instances/fake"
	"github.com/nickheyer/nebu/internal/mesh"
	"github.com/nickheyer/nebu/internal/settings"
	"github.com/nickheyer/nebu/pkg/archs"
	"github.com/nickheyer/nebu/pkg/datalayer"
	"github.com/nickheyer/nebu/pkg/descriptor"
	"github.com/nickheyer/nebu/pkg/events"
	"github.com/nickheyer/nebu/pkg/host"
	"github.com/nickheyer/nebu/pkg/perf"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/runtimes"
	"github.com/nickheyer/nebu/pkg/store"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// A slot view holding one slot
type oneSlot struct{ slot *v1.Slot }

func (oneSlot) Refs() []*v1.SlotRef       { return nil }
func (s oneSlot) Owns(name string) bool   { return s.slot.GetName() == name }
func (oneSlot) OnFormation(*v1.Formation) {}
func (s oneSlot) Get(string) (*v1.Slot, *v1.Instance, error) {
	return s.slot, nil, nil
}
func (oneSlot) Claim(_ context.Context, _ string, run *v1.RunRequest) (*v1.RunRequest, error) {
	return run, nil
}

// A pool view holding nothing, what a mesh reads its record's pools and routes from
type emptyView struct{}

func (emptyView) Store() datalayer.Store                 { return datalayer.NewStore() }
func (emptyView) Pools() []*v1.Pool                      { return nil }
func (emptyView) Pool(string) (*v1.Pool, bool)           { return nil, false }
func (emptyView) Routes() []*v1.Route                    { return nil }
func (emptyView) Route(string) (*v1.Route, bool)         { return nil, false }
func (emptyView) RouteView(string) (*v1.Route, bool)     { return nil, false }
func (emptyView) Exported() []*v1.Pool                   { return nil }
func (emptyView) Objective(string) (*v1.Objective, bool) { return nil, false }

// A probe answering with a CPU and its host memory
type argsProbe struct{}

func (argsProbe) ID() string { return "memory" }
func (argsProbe) Description() string {
	return "a CPU and host memory beside two GPUs without memory of their own, which the profile drops"
}
func (argsProbe) Runs(string, string) bool { return true }
func (argsProbe) Run(context.Context) host.Result {
	return host.Result{
		Status:  v1.ProbeStatus_PROBE_STATUS_OK,
		Devices: []*v1.Device{{Id: "cpu0", Kind: v1.DeviceKind_DEVICE_KIND_CPU, Name: "cpu"}, {Id: "igpu0", Kind: v1.DeviceKind_DEVICE_KIND_GPU, Vendor: "intel", Name: "shared igpu"}, {Id: "blind0", Kind: v1.DeviceKind_DEVICE_KIND_GPU, Vendor: "other", Name: "unread gpu"}},
		Pools:   []*v1.MemoryPool{{Id: "host", Kind: v1.PoolKind_POOL_KIND_HOST, TotalBytes: 64 << 30, FreeBytes: 32 << 30}},
	}
}

// A conductor on a one-member mesh holding a stored model of the fake runtime
func argsConductor(t *testing.T, ctx context.Context) *Manager {
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
	if err := blobs.WriteManifest(&v1.StoredModel{SourceId: "src", Repo: "org/model", Group: "q4", FormatId: fake.Format, Path: dir, Descriptor_: &v1.Descriptor{Kind: v1.ModelKind_MODEL_KIND_LANGUAGE, Architecture: "fake", Metadata: map[string]string{descriptor.RevisionKey: descriptor.Revision}}}); err != nil {
		t.Fatal(err)
	}
	if err := database.PutInstall(ctx, &v1.Install{Id: db.NewID(), RuntimeId: fake.RuntimeID, Kind: v1.InstallKind_INSTALL_KIND_ADOPTED, Path: filepath.Join(dir, "fake"), Dir: dir, Version: "1", CreatedAt: timestamppb.Now()}); err != nil {
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
	families, err := archs.New(archs.All())
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	bus := events.New()
	set := &settings.Manager{DB: database, Events: bus}
	if err := set.Load(ctx); err != nil {
		t.Fatal(err)
	}
	node := &mesh.Manager{
		DB:        database,
		Config:    &v1.MeshConfig{Listen: "127.0.0.1:0", Advertise: "127.0.0.1", Announce: proto.Bool(false)},
		Host:      host.New([]host.Probe{argsProbe{}}, nil, time.Minute),
		Installs:  &installs.Manager{DB: database},
		Runtimes:  rts,
		Store:     blobs,
		Perf:      table,
		Pools:     emptyView{},
		Registrar: fake.NewRegistrar(),
		Settings:  set,
		Events:    bus,
		Guard:     auth.NewGuard("", nil, nil),
		Log:       log,
		Version:   "test",
	}
	if err := node.Open(ctx); err != nil {
		t.Fatal(err)
	}
	node.Handler = http.NewServeMux()
	node.InFlight = func(string) int { return 0 }
	if err := node.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(node.Close)
	if _, _, err := node.Init(ctx, "test"); err != nil {
		t.Fatal(err)
	}
	return &Manager{
		DB:        database,
		Mesh:      node,
		Store:     blobs,
		Runtimes:  rts,
		Perf:      table,
		Inspector: &inspect.Inspector{Builder: &descriptor.Builder{Archs: families}, Stored: blobs.ListManifests},
		SlotView:  oneSlot{&v1.Slot{Id: "s", Name: "main", Args: "--cache-reuse 256"}},
		Log:       log,
	}
}

// A formation's seats get the run's words in their block, the slot's when the run names none or all when it is exact, and bad words are refused
func TestFormationArgs(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := argsConductor(t, ctx)
	run := &v1.RunRequest{SourceId: "src", Repo: "org/model", Group: "q4", RuntimeId: fake.RuntimeID, Args: "--cache-reuse 512"}

	bare, err := m.plan(ctx, run)
	if err != nil {
		t.Fatal(err)
	}
	if bare.args != "--cache-reuse 512" {
		t.Fatalf("a run outside a slot: %q", bare.args)
	}

	inSlot := proto.Clone(run).(*v1.RunRequest)
	inSlot.SlotId = "s"
	p, err := m.plan(ctx, inSlot)
	if err != nil {
		t.Fatal(err)
	}
	if want := "--cache-reuse 512"; p.args != want {
		t.Fatalf("a run in a slot: %q, want %q", p.args, want)
	}
	wordless := proto.Clone(inSlot).(*v1.RunRequest)
	wordless.Args = ""
	if q, err := m.plan(ctx, wordless); err != nil || q.args != "--cache-reuse 256" {
		t.Fatalf("a run naming no words in a slot: %v %v", q, err)
	}
	wordless.Exact = true
	if q, err := m.plan(ctx, wordless); err != nil || q.args != "" {
		t.Fatalf("an exact run naming no words in a slot: %v %v", q, err)
	}

	seat := &v1.Seat{NodeId: m.self(), Role: runtimes.RoleHead}
	f := &v1.Formation{Name: "main", Request: inSlot, Seats: []*v1.Seat{seat}, Plan: &v1.FormationPlan{}}
	narrowed := m.seatRequest(f, p, seat, "", nil)
	if narrowed.GetArgs() != "" || narrowed.GetSeat().GetArgs() != p.args {
		t.Fatalf("seat request args %q, block %q", narrowed.GetArgs(), narrowed.GetSeat().GetArgs())
	}

	bad := proto.Clone(run).(*v1.RunRequest)
	bad.Args = `--alias "open`
	if _, err := m.plan(ctx, bad); !errors.Is(err, runtimes.ErrParam) {
		t.Fatalf("unclosed quote: %v", err)
	}
}

// A span pins devices a member's profile lists, the GPUs without memory of their own dropped from it
func TestPlanRefusesSpanDevicesWithoutMemory(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := argsConductor(t, ctx)
	run := &v1.RunRequest{SourceId: "src", Repo: "org/model", Group: "q4", RuntimeId: fake.RuntimeID, Name: "pinned", Span: []string{m.self() + "/blind0"}}
	if _, err := m.plan(ctx, run); err == nil || !strings.Contains(err.Error(), `has no device "blind0", see nebu mesh nodes`) {
		t.Fatalf("a span on a gpu the probes found no memory for: %v", err)
	}
	run.Span = []string{m.self() + "/igpu0"}
	if _, err := m.plan(ctx, run); err == nil || !strings.Contains(err.Error(), `has no device "igpu0", see nebu mesh nodes`) {
		t.Fatalf("a span on a gpu sharing system memory: %v", err)
	}
	run.Span = []string{m.self() + "/gpu9"}
	if _, err := m.plan(ctx, run); err == nil || !strings.Contains(err.Error(), `has no device "gpu9", see nebu mesh nodes`) {
		t.Fatalf("a span on a device the node lacks: %v", err)
	}
}
