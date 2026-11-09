package slots

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nickheyer/nebu/internal/db"
	"github.com/nickheyer/nebu/internal/instances"
	"github.com/nickheyer/nebu/pkg/events"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// A conductor that knows this node's id and nothing else
type selfOnly struct{ id string }

func (s selfOnly) Run(context.Context, *v1.RunRequest) (*v1.Formation, *v1.Task, error) {
	return nil, nil, nil
}
func (s selfOnly) Plan(context.Context, *v1.RunRequest) (*v1.FormationPlan, *v1.Descriptor, error) {
	return nil, nil, nil
}
func (s selfOnly) Stop(context.Context, string) (*v1.Formation, error) { return nil, nil }
func (s selfOnly) Get(string) (*v1.Formation, error)                   { return nil, nil }
func (s selfOnly) List(bool) []*v1.Formation                           { return nil }
func (s selfOnly) Node(string) (*v1.Node, error)                       { return nil, nil }
func (s selfOnly) Self() string                                        { return s.id }

// A pin naming this node is the device itself and one on another node makes the run a formation
func TestLocalDevicesAndMeshRun(t *testing.T) {
	m := &Manager{Formations: selfOnly{id: "self"}}
	got := m.localDevices([]string{"self/gpu0", "gpu1", "other/gpu0"})
	if len(got) != 3 || got[0] != "gpu0" || got[1] != "gpu1" || got[2] != "other/gpu0" {
		t.Fatalf("local devices %v", got)
	}
	local := &v1.Slot{Id: "s", DeviceIds: []string{"self/gpu0", "gpu1"}}
	if m.meshRun(local, &v1.RunRequest{}) {
		t.Fatal("devices on this node alone make a solo run")
	}
	if !m.meshRun(local, &v1.RunRequest{Span: []string{"other"}}) || !m.meshRun(local, &v1.RunRequest{Shape: v1.Shape_SHAPE_CHAIN}) {
		t.Fatal("a span or a shape makes a formation")
	}
	remote := &v1.Slot{Id: "s", Name: "big", RuntimeId: "llamacpp", DeviceIds: []string{"gpu0", "other/gpu0"}, Params: map[string]string{"n_ctx": "8192"}}
	if !m.meshRun(remote, &v1.RunRequest{}) {
		t.Fatal("a device on another node makes a formation")
	}
	remote.Args = "--cache-reuse 256"
	req := m.meshRequest(remote, &v1.RunRequest{Repo: "org/m", Params: map[string]string{"temp": "1"}, Args: "--cache-reuse 512"})
	if req.GetArgs() != "--cache-reuse 512" {
		t.Fatalf("the planner joins the slot's words, the request keeps its own: %q", req.GetArgs())
	}
	if req.GetSlotId() != "s" || req.GetName() != "big" || req.GetRuntimeId() != "llamacpp" || len(req.GetSpan()) != 2 || req.GetSpan()[0] != "self/gpu0" || req.GetSpan()[1] != "other/gpu0" {
		t.Fatalf("mesh request %v", req)
	}
	if req.GetParams()["n_ctx"] != "8192" || req.GetParams()["temp"] != "1" {
		t.Fatalf("params merged under the request's: %v", req.GetParams())
	}
	// Without a conductor nothing spans
	alone := &Manager{}
	if alone.meshRun(remote, &v1.RunRequest{Span: []string{"other"}}) {
		t.Fatal("no conductor, no formation")
	}
}

// A conductor holding one formation
type withFormation struct {
	selfOnly
	f *v1.Formation
}

func (w withFormation) Get(id string) (*v1.Formation, error) {
	if w.f != nil && w.f.GetId() == id {
		return w.f, nil
	}
	return nil, ErrUnknownSlot
}

// A run aimed at the conductor takes the slot's settings and is refused beside a formation outside a swap
func TestClaimSlot(t *testing.T) {
	ctx := context.Background()
	live := &v1.Formation{Id: "f1", Name: "big", State: v1.FormationState_FORMATION_STATE_READY}
	m := &Manager{Instances: &instances.Manager{}, Formations: withFormation{selfOnly: selfOnly{id: "self"}, f: live}, slots: map[string]*v1.Slot{
		"s": {Id: "s", Name: "big", RuntimeId: "llamacpp", FormationId: "f1", DeviceIds: []string{"gpu0", "other/gpu1"}},
		"e": {Id: "e", Name: "empty", DeviceIds: []string{"gpu0"}},
	}}
	if _, err := m.Claim(ctx, "s", &v1.RunRequest{Repo: "org/m"}); err == nil {
		t.Fatal("a slot a formation serves refuses a run aimed at it")
	}
	req, err := m.Claim(instances.WithSwap(ctx), "s", &v1.RunRequest{Repo: "org/m"})
	if err != nil || req.GetName() != "big" || req.GetRuntimeId() != "llamacpp" || len(req.GetSpan()) != 2 {
		t.Fatalf("a swap claims the slot with its settings: %v %v", req, err)
	}
	req, err = m.Claim(ctx, "e", &v1.RunRequest{Repo: "org/m"})
	if err != nil || req.GetName() != "empty" || req.GetSlotId() != "e" {
		t.Fatalf("an empty slot is claimed: %v %v", req, err)
	}
	if _, err := m.Claim(ctx, "nope", &v1.RunRequest{}); err == nil {
		t.Fatal("an unknown slot is refused")
	}
	// The solo path sees the formation through the reservation
	res, err := m.Reservation(ctx, "s")
	if err != nil || res.FormationID != "f1" || res.FormationName != "big" {
		t.Fatalf("reservation %v %v", res, err)
	}
	if res, err := m.Reservation(ctx, "e"); err != nil || res.FormationID != "" {
		t.Fatalf("an empty slot reserves no formation: %v %v", res, err)
	}
}

// A conductor listing formations and recording the ones it is asked to stop
type stoppingFormations struct {
	selfOnly
	list    []*v1.Formation
	stopped []string
}

func (f *stoppingFormations) List(bool) []*v1.Formation { return f.list }

func (f *stoppingFormations) Stop(_ context.Context, id string) (*v1.Formation, error) {
	f.stopped = append(f.stopped, id)
	return nil, nil
}

// Recovery stops a canary formation beside the slot's formation and leaves the occupant and other slots alone
func TestAbandonStopsCanaryFormation(t *testing.T) {
	ctx := context.Background()
	d, err := db.Open(filepath.Join(t.TempDir(), "nebu.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	fs := &stoppingFormations{selfOnly: selfOnly{id: "self"}, list: []*v1.Formation{
		{Id: "f1", Name: "big", SlotId: "s"},
		{Id: "f2", Name: "big~3", SlotId: "s"},
		{Id: "f3", Name: "other", SlotId: "o"},
	}}
	m := &Manager{DB: d, Events: events.New(), Instances: &instances.Manager{}, Formations: fs, Log: slog.New(slog.DiscardHandler), slots: map[string]*v1.Slot{
		"s": {Id: "s", Name: "big", FormationId: "f1"},
	}}
	m.abandonCanary(ctx, m.mustFind("s"))
	if len(fs.stopped) != 1 || fs.stopped[0] != "f2" {
		t.Fatalf("stopped %v, want the canary formation alone", fs.stopped)
	}
	wantCondition(t, m.mustFind("s"), CondCanary, false, CanaryAbandoned, "stopped formation big~3")
}

// A conductor on a two node mesh, the other node holding one GPU with memory and one without
type twoNodes struct{ selfOnly }

func (twoNodes) Node(id string) (*v1.Node, error) {
	if id != "other" {
		return nil, fmt.Errorf("no node %s", id)
	}
	return &v1.Node{Id: "other", Name: "other", Profile: &v1.HostProfile{
		Devices: []*v1.Device{
			{Id: "gpu1", Kind: v1.DeviceKind_DEVICE_KIND_GPU, Vendor: "nvidia", Name: "far gpu"},
			{Id: "igpu1", Kind: v1.DeviceKind_DEVICE_KIND_GPU, Vendor: "intel", Name: "far igpu"},
		},
		Pools: []*v1.MemoryPool{{Id: "gpu1", Kind: v1.PoolKind_POOL_KIND_DEVICE, DeviceId: "gpu1", TotalBytes: 24 << 30, FreeBytes: 24 << 30}},
	}}, nil
}

// A slot pins devices the profiles list, here or on a member, never one without memory of its own
func TestSlotRefusesDevicesWithoutMemory(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n := newSlotNode(t, ctx)
	n.slots.Formations = twoNodes{selfOnly{id: "self"}}
	refused := `device "blind0" was not probed, see nebu host`
	if _, err := n.slots.Create(ctx, &v1.CreateSlotRequest{Name: "blind", DeviceIds: []string{"blind0"}}); err == nil || !strings.Contains(err.Error(), refused) {
		t.Fatalf("a slot on a gpu the probes found no memory for: %v", err)
	}
	if _, err := n.slots.Create(ctx, &v1.CreateSlotRequest{Name: "blind", DeviceIds: []string{"self/blind0"}}); err == nil || !strings.Contains(err.Error(), `device "self/blind0" was not probed`) {
		t.Fatalf("a slot on that gpu named through this node: %v", err)
	}
	if _, err := n.slots.Create(ctx, &v1.CreateSlotRequest{Name: "shared", DeviceIds: []string{"igpu0"}}); err == nil || !strings.Contains(err.Error(), `device "igpu0" was not probed`) {
		t.Fatalf("a slot on a gpu sharing system memory: %v", err)
	}
	if _, err := n.slots.Create(ctx, &v1.CreateSlotRequest{Name: "far", DeviceIds: []string{"other/igpu1"}}); err == nil || !strings.Contains(err.Error(), `device "other/igpu1" (far igpu) has no memory of its own, so a slot cannot pin it`) {
		t.Fatalf("a slot on a member's igpu: %v", err)
	}
	if _, err := n.slots.Create(ctx, &v1.CreateSlotRequest{Name: "far", DeviceIds: []string{"other/gpu9"}}); err == nil || !strings.Contains(err.Error(), `other has no device "gpu9"`) {
		t.Fatalf("a slot on a device a member lacks: %v", err)
	}
	s, err := n.slots.Create(ctx, &v1.CreateSlotRequest{Name: "near", DeviceIds: []string{"gpu0", "other/gpu1"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := n.slots.Update(ctx, &v1.UpdateSlotRequest{Id: s.GetId(), DeviceIds: []string{"gpu0", "blind0"}}); err == nil || !strings.Contains(err.Error(), refused) {
		t.Fatalf("an update pinning a gpu the probes found no memory for: %v", err)
	}
	if _, err := n.slots.Update(ctx, &v1.UpdateSlotRequest{Id: s.GetId(), DeviceIds: []string{"gpu0"}}); err != nil {
		t.Fatal(err)
	}
}
