package slots

import (
	"context"
	"testing"

	"github.com/nickheyer/nebu/internal/instances"
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
func (s selfOnly) Node(string) (*v1.Node, error)                       { return nil, nil }
func (s selfOnly) Self() string                                        { return s.id }

// A pin written as this node's id over a device is the device itself; one on another node stays
// node qualified and makes the run a formation
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
	req := m.meshRequest(remote, &v1.RunRequest{Repo: "org/m", Params: map[string]string{"temp": "1"}})
	if req.GetSlotId() != "s" || req.GetName() != "big" || req.GetRuntimeId() != "llamacpp" || len(req.GetSpan()) != 2 || req.GetSpan()[0] != "self/gpu0" || req.GetSpan()[1] != "other/gpu0" {
		t.Fatalf("mesh request %v", req)
	}
	if req.GetParams()["n_ctx"] != "8192" || req.GetParams()["temp"] != "1" {
		t.Fatalf("params merged under the request's: %v", req.GetParams())
	}
	// Without a conductor nothing spans.
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

// A run aimed straight at the conductor takes the slot's settings, and a slot a formation serves
// refuses it unless a swap is under way
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
	// The solo path sees the formation through the reservation.
	res, err := m.Reservation(ctx, "s")
	if err != nil || res.FormationID != "f1" || res.FormationName != "big" {
		t.Fatalf("reservation %v %v", res, err)
	}
	if res, err := m.Reservation(ctx, "e"); err != nil || res.FormationID != "" {
		t.Fatalf("an empty slot reserves no formation: %v %v", res, err)
	}
}
