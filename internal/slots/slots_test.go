package slots

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/nickheyer/nebu/internal/db"
	"github.com/nickheyer/nebu/internal/inspect"
	"github.com/nickheyer/nebu/internal/installs"
	"github.com/nickheyer/nebu/internal/instances"
	"github.com/nickheyer/nebu/internal/instances/fake"
	"github.com/nickheyer/nebu/internal/pools"
	"github.com/nickheyer/nebu/internal/tasks"
	"github.com/nickheyer/nebu/pkg/archs"
	"github.com/nickheyer/nebu/pkg/descriptor"
	"github.com/nickheyer/nebu/pkg/events"
	"github.com/nickheyer/nebu/pkg/host"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/runtimes"
	"github.com/nickheyer/nebu/pkg/store"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// One daemon's slots over instances of the fake runtime, with a recording registrar and fake traffic
type slotNode struct {
	dir       string
	db        *db.DB
	blobs     *store.Store
	launcher  *fake.Launcher
	registrar *fake.Registrar
	traffic   *fake.Traffic
	outcomes  *fakeOutcomes
	clock     *fakeClock
	instances *instances.Manager
	slots     *Manager
}

// A host with one GPU holding plenty of memory, so every plan fits beside another
type gpuProbe struct{}

func (gpuProbe) ID() string { return "gpu" }
func (gpuProbe) Description() string {
	return "one roomy GPU beside two GPUs without memory of their own, which the profile drops"
}
func (gpuProbe) Runs(string, string) bool { return true }
func (gpuProbe) Run(context.Context) host.Result {
	return host.Result{
		Status:  v1.ProbeStatus_PROBE_STATUS_OK,
		Devices: []*v1.Device{{Id: "gpu0", Kind: v1.DeviceKind_DEVICE_KIND_GPU, Vendor: "nvidia", Name: "fake gpu"}, {Id: "igpu0", Kind: v1.DeviceKind_DEVICE_KIND_GPU, Vendor: "intel", Name: "shared igpu"}, {Id: "blind0", Kind: v1.DeviceKind_DEVICE_KIND_GPU, Vendor: "other", Name: "unread gpu"}, {Id: "cpu0", Kind: v1.DeviceKind_DEVICE_KIND_CPU, Name: "cpu"}},
		Pools: []*v1.MemoryPool{
			{Id: "gpu0", Kind: v1.PoolKind_POOL_KIND_DEVICE, DeviceId: "gpu0", TotalBytes: 80 << 30, FreeBytes: 80 << 30},
			{Id: "host", Kind: v1.PoolKind_POOL_KIND_HOST, TotalBytes: 64 << 30, FreeBytes: 64 << 30},
		},
	}
}

func newSlotNode(t *testing.T, ctx context.Context) *slotNode {
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
	if err := database.PutInstall(ctx, &v1.Install{Id: db.NewID(), RuntimeId: fake.RuntimeID, Kind: v1.InstallKind_INSTALL_KIND_ADOPTED, Path: filepath.Join(dir, "fake"), Dir: dir, Version: "1", CreatedAt: timestamppb.Now()}); err != nil {
		t.Fatal(err)
	}
	modelDir := filepath.Join(dir, "model")
	if err := os.MkdirAll(modelDir, 0o755); err != nil {
		t.Fatal(err)
	}
	weights := filepath.Join(modelDir, "weights.fake")
	if err := os.WriteFile(weights, []byte("weights"), 0o644); err != nil {
		t.Fatal(err)
	}
	artifacts := []*v1.StoredArtifact{{Digest: "sha256:m", Path: weights, Artifact: &v1.Artifact{Path: "weights.fake", Role: v1.ArtifactRole_ARTIFACT_ROLE_WEIGHTS}}}
	for _, group := range []string{"q4", "q8"} {
		if err := blobs.WriteManifest(&v1.StoredModel{SourceId: "src", Repo: "org/model", Group: group, FormatId: fake.Format, Path: modelDir, Descriptor_: &v1.Descriptor{Kind: v1.ModelKind_MODEL_KIND_LANGUAGE, Architecture: "fake", Metadata: map[string]string{descriptor.RevisionKey: descriptor.Revision}}, Artifacts: artifacts}); err != nil {
			t.Fatal(err)
		}
	}
	n := &slotNode{dir: dir, db: database, blobs: blobs, launcher: fake.New(), registrar: fake.NewRegistrar(), traffic: fake.NewTraffic(), outcomes: &fakeOutcomes{}, clock: &fakeClock{}}
	n.start(t, ctx)
	return n
}

// Opens the instance and slot managers as one daemon would, over the node's database, store, launcher and registrar
func (n *slotNode) start(t *testing.T, ctx context.Context) {
	t.Helper()
	rts, err := runtimes.New([]runtimes.Runtime{fake.Runtime{}})
	if err != nil {
		t.Fatal(err)
	}
	families, err := archs.New(archs.All())
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	bus := events.New()
	database, blobs := n.db, n.blobs
	work := tasks.New(ctx, log, database, bus)
	prober := host.New([]host.Probe{gpuProbe{}}, nil, time.Minute)
	n.instances = &instances.Manager{
		DB:           database,
		Dir:          filepath.Join(n.dir, "instances"),
		Store:        blobs,
		Runtimes:     rts,
		Installs:     &installs.Manager{DB: database},
		Inspector:    &inspect.Inspector{Builder: &descriptor.Builder{Archs: families}, Stored: blobs.ListManifests},
		Launcher:     n.launcher,
		Tasks:        work,
		Host:         prober,
		Registrar:    n.registrar,
		InFlight:     n.traffic.InFlight,
		Dispatched:   n.traffic.Dispatched,
		Load:         n.traffic.Load,
		Listening:    n.launcher.Listening,
		DrainTimeout: 5 * time.Second,
		Events:       bus,
		Log:          log,
	}
	n.slots = &Manager{DB: database, Instances: n.instances, Registrar: n.registrar, Routes: n.registrar, Tasks: work, Host: prober, Events: bus, Outcomes: n.outcomes, Clock: n.clock, Log: log}
	n.instances.Slots = n.slots
	if err := n.slots.Load(ctx); err != nil {
		t.Fatal(err)
	}
}

// Swaps the fake registrar for a pool manager over the node's database, closed when the test ends
func (n *slotNode) realPools(t *testing.T, ctx context.Context) *pools.Manager {
	t.Helper()
	pm, err := pools.New(ctx, pools.Options{DB: n.db, Events: events.New(), NodeID: "n1"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pm.Close() })
	n.instances.Registrar, n.slots.Registrar, n.slots.Routes = pm, pm, pm
	return pm
}

// A run of the fake model's group in a slot
func slotRun(slotID, group string) *v1.RunRequest {
	return &v1.RunRequest{SourceId: "src", Repo: "org/model", Group: group, RuntimeId: fake.RuntimeID, SlotId: slotID}
}

// Waits until cond holds, failing with what after thirty seconds
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// The slot as the manager holds it now
func (n *slotNode) slot(t *testing.T, id string) *v1.Slot {
	t.Helper()
	s, _, err := n.slots.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// The slot's occupant record
func (n *slotNode) occupant(t *testing.T, id string) *v1.Instance {
	t.Helper()
	_, in, err := n.slots.Get(id)
	if err != nil || in == nil {
		t.Fatalf("slot %s has no occupant: %v", id, err)
	}
	return in
}

// Creates a slot named main and fills it with the q4 group, waiting for it to serve
func (n *slotNode) filled(t *testing.T, ctx context.Context) *v1.Slot {
	t.Helper()
	s, err := n.slots.Create(ctx, &v1.CreateSlotRequest{Name: "main", Description: "the main slot", Aliases: []*v1.SlotAlias{{Name: "chat"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := n.slots.Swap(ctx, &v1.SwapRequest{SlotId: s.GetId(), Run: slotRun(s.GetId(), "q4")}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the slot to serve", func() bool { return n.slot(t, s.GetId()).GetState() == v1.SlotState_SLOT_STATE_READY })
	return n.slot(t, s.GetId())
}

// A slot persists its description, wants its occupant running, reports its conditions, and names every route under its owner
func TestSlotServesAndPersists(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n := newSlotNode(t, ctx)
	s := n.filled(t, ctx)
	if !s.GetDesiredRunning() || s.GetObservedGeneration() != s.GetMeta().GetGeneration() {
		t.Fatalf("desired %v observed %d generation %d", s.GetDesiredRunning(), s.GetObservedGeneration(), s.GetMeta().GetGeneration())
	}
	for _, typ := range []string{CondAccepted, CondOccupied, CondReady} {
		if !instances.ConditionTrue(s.GetConditions(), typ) {
			t.Fatalf("condition %s is not true: %v", typ, s.GetConditions())
		}
	}
	if instances.ConditionTrue(s.GetConditions(), CondSwapping) {
		t.Fatal("a settled slot is not swapping")
	}
	saved, err := n.db.ListSlots(ctx)
	if err != nil || len(saved) != 1 || saved[0].GetDescription() != "the main slot" || !saved[0].GetDesiredRunning() {
		t.Fatalf("saved %v %v", saved, err)
	}
	owner := pools.OwnerRef{Kind: pools.OwnerSlot, ID: s.GetId()}
	served := instances.ServedName(n.occupant(t, s.GetId()))
	for _, name := range []string{"main", "chat"} {
		rt, ok := n.registrar.Named(name)
		if !ok || rt.Owner != owner || rt.Spec.ServedName != served {
			t.Fatalf("route %s %+v, want owner %v serving %s", name, rt, owner, served)
		}
	}
	if !n.slots.Owns("chat") || n.slots.Owns("other") {
		t.Fatal("a slot owns its name and aliases alone")
	}
	if _, err := n.slots.Evict(ctx, s.GetId()); err != nil {
		t.Fatal(err)
	}
	if names := n.registrar.RouteNames(); len(names) != 0 {
		t.Fatalf("an evicted slot keeps routes %v", names)
	}
	if got := n.slot(t, s.GetId()); got.GetDesiredRunning() || got.GetState() != v1.SlotState_SLOT_STATE_EMPTY {
		t.Fatalf("evicted desired %v state %s", got.GetDesiredRunning(), got.GetState())
	}
}

// A crashed occupant restarts in place and a desired slot whose occupant record is gone launches another
func TestSlotKeepsOccupantReconciled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	every := reconcileEvery
	reconcileEvery = 50 * time.Millisecond
	t.Cleanup(func() { reconcileEvery = every })
	n := newSlotNode(t, ctx)
	s := n.filled(t, ctx)
	if err := n.slots.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	first := n.occupant(t, s.GetId())
	n.launcher.Crash(first.GetName())
	waitFor(t, "the occupant restarted", func() bool {
		in := n.occupant(t, s.GetId())
		return in.GetId() == first.GetId() && in.GetRestarts() == 1 && in.GetState() == v1.InstanceState_INSTANCE_STATE_READY && n.slot(t, s.GetId()).GetState() == v1.SlotState_SLOT_STATE_READY
	})
	n.instances.Close()
	if err := n.db.DeleteInstance(ctx, first.GetId()); err != nil {
		t.Fatal(err)
	}
	n.start(t, ctx)
	if err := n.instances.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	if err := n.slots.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "a new occupant", func() bool {
		cur := n.slot(t, s.GetId())
		in, err := n.instances.Get(cur.GetInstanceId())
		return err == nil && cur.GetInstanceId() != first.GetId() && in.GetState() == v1.InstanceState_INSTANCE_STATE_READY && cur.GetState() == v1.SlotState_SLOT_STATE_READY
	})
	if rt, ok := n.registrar.Named("main"); !ok || rt.Spec.ServedName != "main" {
		t.Fatalf("the new occupant serves the slot's routes: %+v", rt)
	}
}

// A slot whose occupant unloaded wakes it through the slot owner
func TestSlotWakesIdleOccupant(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n := newSlotNode(t, ctx)
	s, err := n.slots.Create(ctx, &v1.CreateSlotRequest{Name: "main"})
	if err != nil {
		t.Fatal(err)
	}
	run := slotRun(s.GetId(), "q4")
	run.IdleUnloadSeconds = 1
	if _, _, _, err := n.slots.Swap(ctx, &v1.SwapRequest{SlotId: s.GetId(), Run: run}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the slot to idle", func() bool { return n.slot(t, s.GetId()).GetState() == v1.SlotState_SLOT_STATE_IDLE })
	if rt, ok := n.registrar.Named("main"); !ok || rt.Owner.Kind != pools.OwnerSlot {
		t.Fatal("an idle slot keeps its routes for the wake")
	}
	if err := n.slots.Wake(ctx, pools.OwnerRef{Kind: pools.OwnerSlot, ID: s.GetId()}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the slot to serve again", func() bool { return n.slot(t, s.GetId()).GetState() == v1.SlotState_SLOT_STATE_READY })
	if got := n.launcher.Launches(n.occupant(t, s.GetId()).GetName()); got != 2 {
		t.Fatalf("%d launches", got)
	}
}

// Fails unless the routes the slot holds are exactly the sorted names, each serving the occupant's model
func (n *slotNode) wantRoutes(t *testing.T, s *v1.Slot, served string, want ...string) {
	t.Helper()
	owner := pools.OwnerRef{Kind: pools.OwnerSlot, ID: s.GetId()}
	var got []string
	for _, name := range n.registrar.RouteNames() {
		rt, _ := n.registrar.Named(name)
		if rt.Owner != owner {
			continue
		}
		got = append(got, name)
		if rt.Spec.ServedName != served {
			t.Fatalf("route %s serves %q, want %q", name, rt.Spec.ServedName, served)
		}
	}
	if !slices.Equal(got, want) {
		t.Fatalf("slot routes %v, want %v", got, want)
	}
}

// A desired slot whose occupant record is gone relaunches its saved request and names every route at the new occupant
func TestSlotRelaunch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n := newSlotNode(t, ctx)
	s := n.filled(t, ctx)
	first := n.occupant(t, s.GetId())
	if _, _, _, err := n.slots.Relaunch(ctx, s.GetId()); !errors.Is(err, ErrSlot) {
		t.Fatalf("relaunching a slot that serves: %v", err)
	}
	n.instances.Close()
	if err := n.db.DeleteInstance(ctx, first.GetId()); err != nil {
		t.Fatal(err)
	}
	n.registrar = fake.NewRegistrar()
	n.start(t, ctx)
	if err := n.instances.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	n.wantRoutes(t, s, "")
	next, in, task, err := n.slots.Relaunch(ctx, s.GetId())
	if err != nil {
		t.Fatal(err)
	}
	if in.GetId() == first.GetId() || next.GetInstanceId() != in.GetId() || next.GetTaskId() != task.GetId() || !next.GetDesiredRunning() || next.GetRequest().GetGroup() != "q4" {
		t.Fatalf("relaunched slot %v occupant %s first %s", next, in.GetId(), first.GetId())
	}
	if err := n.instances.Tasks.WaitOK(ctx, task.GetId()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the relaunched slot to serve", func() bool { return n.slot(t, s.GetId()).GetState() == v1.SlotState_SLOT_STATE_READY })
	n.wantRoutes(t, s, instances.ServedName(n.occupant(t, s.GetId())), "chat", "main")
	if got := n.launcher.Launches(in.GetName()); got != 2 {
		t.Fatalf("%d launches of %s", got, in.GetName())
	}
	if _, err := n.slots.Evict(ctx, s.GetId()); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := n.slots.Relaunch(ctx, s.GetId()); !errors.Is(err, ErrSlot) {
		t.Fatalf("relaunching an evicted slot: %v", err)
	}
	n.wantRoutes(t, s, "")
}

// Adding, removing and renaming a slot's names moves its routes with them, and a name it cannot take changes nothing
func TestSlotNamesFollowRoutes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n := newSlotNode(t, ctx)
	s := n.filled(t, ctx)
	served := instances.ServedName(n.occupant(t, s.GetId()))
	n.wantRoutes(t, s, served, "chat", "main")
	other := pools.OwnerRef{Kind: pools.OwnerInstance, ID: "other"}
	if err := n.registrar.Publish(ctx, other, pools.Membership{}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := n.registrar.NameRoute(ctx, other, "taken", pools.RouteSpec{}); err != nil {
		t.Fatal(err)
	}
	next, route, err := n.slots.AddAlias(ctx, s.GetId(), &v1.SlotAlias{Name: " code ", Policy: &v1.Policy{MaxInFlight: 3}})
	if err != nil {
		t.Fatal(err)
	}
	if route.GetName() != "code" || route.GetOwnerKind() != pools.OwnerSlot || route.GetOwnerId() != s.GetId() || !slices.Equal(aliasNames(next.GetAliases()), []string{"chat", "code"}) {
		t.Fatalf("added alias route %v aliases %v", route, aliasNames(next.GetAliases()))
	}
	n.wantRoutes(t, s, served, "chat", "code", "main")
	if rt, _ := n.registrar.Named("code"); rt.Spec.Policy.GetMaxInFlight() != 3 {
		t.Fatalf("alias policy %v", rt.Spec.Policy)
	}
	next, _, err = n.slots.AddAlias(ctx, s.GetId(), &v1.SlotAlias{Name: "code", Policy: &v1.Policy{MaxInFlight: 5}})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(aliasNames(next.GetAliases()), []string{"chat", "code"}) {
		t.Fatalf("adding an alias again lists it twice %v", aliasNames(next.GetAliases()))
	}
	n.wantRoutes(t, s, served, "chat", "code", "main")
	if rt, _ := n.registrar.Named("code"); rt.Spec.Policy.GetMaxInFlight() != 5 {
		t.Fatalf("alias policy after adding it again %v", rt.Spec.Policy)
	}

	refusals := []struct {
		name string
		call func() error
	}{
		{"alias of its own name", func() error { _, _, err := n.slots.AddAlias(ctx, s.GetId(), &v1.SlotAlias{Name: "main"}); return err }},
		{"alias another owner routes", func() error { _, _, err := n.slots.AddAlias(ctx, s.GetId(), &v1.SlotAlias{Name: "taken"}); return err }},
		{"removing its own name", func() error { _, _, err := n.slots.RemoveAlias(ctx, s.GetId(), "main"); return err }},
		{"removing an alias it lacks", func() error { _, _, err := n.slots.RemoveAlias(ctx, s.GetId(), "nope"); return err }},
		{"renaming onto another owner's route", func() error {
			_, err := n.slots.Update(ctx, &v1.UpdateSlotRequest{Id: s.GetId(), Name: "taken", Aliases: next.GetAliases()})
			return err
		}},
	}
	for _, c := range refusals {
		if err := c.call(); !errors.Is(err, ErrSlot) {
			t.Fatalf("%s: %v", c.name, err)
		}
		n.wantRoutes(t, s, served, "chat", "code", "main")
	}

	next, route, err = n.slots.RemoveAlias(ctx, s.GetId(), "chat")
	if err != nil {
		t.Fatal(err)
	}
	if route.GetName() != "chat" || route.GetOwnerId() != s.GetId() || !slices.Equal(aliasNames(next.GetAliases()), []string{"code"}) || n.slots.Owns("chat") {
		t.Fatalf("removed alias route %v aliases %v", route, aliasNames(next.GetAliases()))
	}
	n.wantRoutes(t, s, served, "code", "main")

	renamed, err := n.slots.Update(ctx, &v1.UpdateSlotRequest{Id: s.GetId(), Name: "primary", Description: "renamed", Aliases: next.GetAliases()})
	if err != nil {
		t.Fatal(err)
	}
	if renamed.GetName() != "primary" || renamed.GetRequest().GetName() != "primary" || n.slots.Owns("main") || !n.slots.Owns("primary") {
		t.Fatalf("renamed slot %v", renamed)
	}
	n.wantRoutes(t, s, served, "code", "primary")
	if rt, _ := n.registrar.Named("code"); rt.Spec.Policy.GetMaxInFlight() != 5 {
		t.Fatalf("alias policy after the rename %v", rt.Spec.Policy)
	}
	renamed, err = n.slots.Update(ctx, &v1.UpdateSlotRequest{Id: s.GetId(), Description: "renamed", Aliases: []*v1.SlotAlias{{Name: "main"}}})
	if err != nil {
		t.Fatal(err)
	}
	if renamed.GetName() != "primary" || !slices.Equal(aliasNames(renamed.GetAliases()), []string{"main"}) {
		t.Fatalf("slot after taking its old name as an alias %v", renamed)
	}
	n.wantRoutes(t, s, served, "main", "primary")
	saved, err := n.db.ListSlots(ctx)
	if err != nil || len(saved) != 1 || saved[0].GetName() != "primary" || !slices.Equal(aliasNames(saved[0].GetAliases()), []string{"main"}) {
		t.Fatalf("saved %v %v", saved, err)
	}
}
