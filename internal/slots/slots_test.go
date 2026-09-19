package slots

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/nickheyer/nebu/internal/db"
	"github.com/nickheyer/nebu/internal/gateway"
	"github.com/nickheyer/nebu/internal/instances"
	"github.com/nickheyer/nebu/internal/tasks"
	"github.com/nickheyer/nebu/pkg/events"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/runtimes"
	"github.com/nickheyer/nebu/pkg/store"
)

func manager(t *testing.T) *Manager {
	t.Helper()
	dir := t.TempDir()
	store_, err := db.Open(dir + "/nebu.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store_.Close() })
	blobs, err := store.Open(dir + "/store")
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	bus := events.New()
	routes, err := gateway.OpenTable(context.Background(), store_, bus, log)
	if err != nil {
		t.Fatal(err)
	}
	runtimes, err := runtimes.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	inst := &instances.Manager{DB: store_, Dir: dir, Store: blobs, Runtimes: runtimes, Routes: routes, Events: bus, Log: log}
	m := &Manager{DB: store_, Instances: inst, Routes: routes, Tasks: tasks.New(context.Background(), log, store_, bus), Events: bus, DrainTimeout: time.Millisecond, Log: log}
	inst.Slots = m
	if err := m.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	return m
}

func names(list []*v1.Slot) string {
	var out []string
	for _, s := range list {
		out = append(out, s.GetName())
	}
	return strings.Join(out, ",")
}

func positions(list []*v1.Slot) []uint32 {
	var out []uint32
	for _, s := range list {
		out = append(out, s.GetPosition())
	}
	return out
}

func TestOrder(t *testing.T) {
	m := manager(t)
	ctx := context.Background()
	for _, name := range []string{"a", "b", "c"} {
		if _, err := m.Create(ctx, &v1.CreateSlotRequest{Name: name}); err != nil {
			t.Fatal(err)
		}
	}
	if got := names(m.List()); got != "a,b,c" {
		t.Fatalf("created order %s", got)
	}
	d, err := m.Create(ctx, &v1.CreateSlotRequest{Name: "d", Position: 2})
	if err != nil {
		t.Fatal(err)
	}
	if got := names(m.List()); got != "a,d,b,c" {
		t.Fatalf("placed order %s", got)
	}
	if p := positions(m.List()); p[0] != 1 || p[1] != 2 || p[2] != 3 || p[3] != 4 {
		t.Fatalf("positions %v", p)
	}
	b, _ := m.find("b")
	if _, err := m.Delete(ctx, b.GetId(), false); err != nil {
		t.Fatal(err)
	}
	if got, p := names(m.List()), positions(m.List()); got != "a,d,c" || p[2] != 3 {
		t.Fatalf("after delete %s %v", got, p)
	}
	if _, err := m.Update(ctx, &v1.UpdateSlotRequest{Id: d.GetId(), Position: 9}); err != nil {
		t.Fatal(err)
	}
	if got := names(m.List()); got != "a,c,d" {
		t.Fatalf("moved last %s", got)
	}
	// Positions survive a reload
	again := &Manager{DB: m.DB, Instances: m.Instances, Routes: m.Routes, Tasks: m.Tasks, Events: m.Events, Log: m.Log}
	if err := again.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if got := names(again.List()); got != "a,c,d" {
		t.Fatalf("reloaded %s", got)
	}
}

func TestNames(t *testing.T) {
	m := manager(t)
	ctx := context.Background()
	if _, err := m.Create(ctx, &v1.CreateSlotRequest{Name: ""}); err == nil {
		t.Fatal("empty name should be refused")
	}
	if _, err := m.Create(ctx, &v1.CreateSlotRequest{Name: "has space"}); err == nil {
		t.Fatal("a name with a space should be refused")
	}
	main, err := m.Create(ctx, &v1.CreateSlotRequest{Name: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if r, ok := m.Routes.Lookup("main"); !ok || r.GetState() != v1.RouteState_ROUTE_STATE_PENDING || r.GetSlotId() != main.GetId() {
		t.Fatalf("a new slot answers pending %v", r)
	}
	if _, err := m.Create(ctx, &v1.CreateSlotRequest{Name: "main"}); err == nil {
		t.Fatal("a taken name should be refused")
	}
	if _, err := m.Create(ctx, &v1.CreateSlotRequest{Name: "other"}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Update(ctx, &v1.UpdateSlotRequest{Id: main.GetId(), Name: "other"}); err == nil {
		t.Fatal("rename onto another slot should be refused")
	}
	renamed, err := m.Update(ctx, &v1.UpdateSlotRequest{Id: main.GetId(), Name: "primary", Placement: v1.Placement_PLACEMENT_DEVICE})
	if err != nil || renamed.GetName() != "primary" || renamed.GetPlacement() != v1.Placement_PLACEMENT_DEVICE {
		t.Fatalf("rename %v %v", renamed, err)
	}
	if _, ok := m.Routes.Lookup("main"); ok {
		t.Fatal("the old name should stop answering")
	}
	if r, ok := m.Routes.Lookup("primary"); !ok || r.GetSlotId() != main.GetId() {
		t.Fatalf("the new name should answer %v", r)
	}
	// An empty name keeps the current one
	kept, err := m.Update(ctx, &v1.UpdateSlotRequest{Id: main.GetId()})
	if err != nil || kept.GetName() != "primary" {
		t.Fatalf("keep name %v %v", kept, err)
	}
}

func request(slotID string) *v1.RunRequest {
	return &v1.RunRequest{SourceId: "hf", Repo: "org/model", Group: "q4", RuntimeId: "rt", Name: "main", SlotId: slotID}
}

func TestOccupantLifecycle(t *testing.T) {
	m := manager(t)
	ctx := context.Background()
	s, err := m.Create(ctx, &v1.CreateSlotRequest{Name: "main"})
	if err != nil {
		t.Fatal(err)
	}
	rec := &v1.Instance{Id: "i1", Name: "main", Repo: "org/model", Group: "q4", RuntimeId: "rt", SlotId: s.GetId(), Endpoint: "http://127.0.0.1:1", State: v1.InstanceState_INSTANCE_STATE_STARTING, Request: request(s.GetId()), DesiredRunning: true}
	m.OnInstance(rec)
	if got := m.mustFind(s.GetId()); got.GetState() != v1.SlotState_SLOT_STATE_STARTING || got.GetInstanceId() != "i1" || got.GetRequest().GetRepo() != "org/model" {
		t.Fatalf("starting %v", got)
	}
	rec.State = v1.InstanceState_INSTANCE_STATE_READY
	m.OnInstance(rec)
	if got := m.mustFind(s.GetId()); got.GetState() != v1.SlotState_SLOT_STATE_READY {
		t.Fatalf("ready %v", got)
	}
	if r, _ := m.Routes.Lookup("main"); r.GetState() != v1.RouteState_ROUTE_STATE_READY || r.GetInstanceId() != "i1" {
		t.Fatalf("route %v", r)
	}
	// A failure keeps the request so the slot can be relaunched
	rec.State, rec.Error, rec.DesiredRunning = v1.InstanceState_INSTANCE_STATE_FAILED, "exited: boom", true
	m.OnInstance(rec)
	if got := m.mustFind(s.GetId()); got.GetState() != v1.SlotState_SLOT_STATE_FAILED || got.GetError() != "exited: boom" || got.GetRequest() == nil || got.GetInstanceId() != "" {
		t.Fatalf("failed %v", got)
	}
	if r, _ := m.Routes.Lookup("main"); r.GetState() != v1.RouteState_ROUTE_STATE_PENDING || r.GetModel() != "org/model:q4" {
		t.Fatalf("failed route %v", r)
	}
	// Relaunch fails when the stored model is missing.
	if _, _, _, err := m.Relaunch(ctx, s.GetId()); err == nil {
		t.Fatal("relaunch without stored weights should fail")
	}
	if got := m.mustFind(s.GetId()); got.GetState() != v1.SlotState_SLOT_STATE_FAILED || got.GetError() == "" || got.GetRequest() == nil {
		t.Fatalf("relaunch failure %v", got)
	}
	// An evict forgets the request
	if _, err := m.Evict(ctx, s.GetId()); err != nil {
		t.Fatal(err)
	}
	if got := m.mustFind(s.GetId()); got.GetState() != v1.SlotState_SLOT_STATE_EMPTY || got.GetRequest() != nil || got.GetError() != "" {
		t.Fatalf("evicted %v", got)
	}
	if r, _ := m.Routes.Lookup("main"); r.GetState() != v1.RouteState_ROUTE_STATE_PENDING {
		t.Fatalf("evicted route %v", r)
	}
	if _, _, _, err := m.Relaunch(ctx, s.GetId()); err == nil || !strings.Contains(err.Error(), "nothing to relaunch") {
		t.Fatalf("empty relaunch %v", err)
	}
	// User stops empty the slot. Shutdown keeps it starting for recovery.
	rec.State, rec.Error, rec.DesiredRunning = v1.InstanceState_INSTANCE_STATE_STARTING, "", true
	m.OnInstance(rec)
	rec.State, rec.DesiredRunning = v1.InstanceState_INSTANCE_STATE_STOPPED, false
	m.OnInstance(rec)
	if got := m.mustFind(s.GetId()); got.GetState() != v1.SlotState_SLOT_STATE_EMPTY || got.GetRequest() != nil {
		t.Fatalf("stopped on request %v", got)
	}
	rec.State, rec.DesiredRunning = v1.InstanceState_INSTANCE_STATE_STARTING, true
	m.OnInstance(rec)
	rec.State = v1.InstanceState_INSTANCE_STATE_STOPPED
	m.OnInstance(rec)
	if got := m.mustFind(s.GetId()); got.GetState() != v1.SlotState_SLOT_STATE_STARTING || got.GetRequest() == nil {
		t.Fatalf("stopped by daemon %v", got)
	}
}

func TestRecover(t *testing.T) {
	m := manager(t)
	ctx := context.Background()
	wanted, _ := m.Create(ctx, &v1.CreateSlotRequest{Name: "wanted"})
	broken, _ := m.Create(ctx, &v1.CreateSlotRequest{Name: "broken"})
	idle, _ := m.Create(ctx, &v1.CreateSlotRequest{Name: "idle"})
	// Legacy empty slot with a stale model request.
	stale, _ := m.Create(ctx, &v1.CreateSlotRequest{Name: "stale"})
	m.update(stale.GetId(), func(s *v1.Slot) { s.Request = request(stale.GetId()) })
	m.update(wanted.GetId(), func(s *v1.Slot) {
		s.State, s.Request, s.InstanceId = v1.SlotState_SLOT_STATE_READY, request(wanted.GetId()), "gone"
	})
	m.update(broken.GetId(), func(s *v1.Slot) {
		s.State, s.Error, s.Request = v1.SlotState_SLOT_STATE_FAILED, "boom", request(broken.GetId())
	})
	if err := m.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		got := m.mustFind(wanted.GetId())
		if got.GetState() == v1.SlotState_SLOT_STATE_FAILED && got.GetError() != "" {
			if got.GetRequest() == nil || got.GetInstanceId() != "" {
				t.Fatalf("relaunched slot %v", got)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("wanted slot never relaunched: %v", got)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := m.mustFind(broken.GetId()); got.GetState() != v1.SlotState_SLOT_STATE_FAILED || got.GetError() != "boom" {
		t.Fatalf("a failed slot stays failed %v", got)
	}
	if got := m.mustFind(idle.GetId()); got.GetState() != v1.SlotState_SLOT_STATE_EMPTY {
		t.Fatalf("idle %v", got)
	}
	if got := m.mustFind(stale.GetId()); got.GetState() != v1.SlotState_SLOT_STATE_EMPTY || got.GetRequest() != nil {
		t.Fatalf("an empty slot forgets a stale request %v", got)
	}
	for _, name := range []string{"wanted", "broken", "idle", "stale"} {
		if r, ok := m.Routes.Lookup(name); !ok || r.GetState() != v1.RouteState_ROUTE_STATE_PENDING {
			t.Fatalf("route %s %v", name, r)
		}
	}
}
