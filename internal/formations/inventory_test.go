package formations

import (
	"context"
	"testing"

	"github.com/nickheyer/nebu/internal/instances/fake"
	"github.com/nickheyer/nebu/internal/pools"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/requestcontrol"
	"github.com/nickheyer/nebu/pkg/runtimes"
	"google.golang.org/protobuf/proto"
)

// Replicas show one replica's inventory, every other shape the serving seat's with every seat's devices
func TestFormationInventoryByShape(t *testing.T) {
	m := &Manager{}
	head := &v1.Instance{Name: "f/head0", Inventory: &v1.RuntimeInventory{ContextLength: 32768, Quantization: "Q4_K_M", Accelerator: "1x NVIDIA H100 80GB HBM3", Modalities: []string{"text"}, ThroughputClass: runtimes.ThroughputDevice}}
	stage := &v1.Instance{Name: "f/stage1", Inventory: &v1.RuntimeInventory{ContextLength: 32768, Quantization: "Q4_K_M", Accelerator: "2x NVIDIA H100 80GB HBM3", Modalities: []string{"text"}, ThroughputClass: runtimes.ThroughputOffload}}
	seats := []*v1.Seat{{NodeId: "a", Role: "head", Rank: 0, InstanceId: "i0"}, {NodeId: "b", Role: "stage", Rank: 1, InstanceId: "i1"}}
	records := map[string]*v1.Instance{seatKey(seats[0]): head, seatKey(seats[1]): stage}
	cases := []struct {
		shape v1.Shape
		acc   string
		class string
	}{
		{v1.Shape_SHAPE_REPLICAS, "1x NVIDIA H100 80GB HBM3", runtimes.ThroughputDevice},
		{v1.Shape_SHAPE_CHAIN, "3x NVIDIA H100 80GB HBM3", runtimes.ThroughputOffload},
		{v1.Shape_SHAPE_LOCKSTEP, "3x NVIDIA H100 80GB HBM3", runtimes.ThroughputOffload},
		{v1.Shape_SHAPE_RELAY, "3x NVIDIA H100 80GB HBM3", runtimes.ThroughputOffload},
	}
	for _, c := range cases {
		t.Run(c.shape.String(), func(t *testing.T) {
			f := &v1.Formation{Name: "f", Shape: c.shape, Seats: seats}
			inv, err := m.formationInventory(context.Background(), f, head, records, nil)
			if err != nil {
				t.Fatal(err)
			}
			if inv.GetAccelerator() != c.acc || inv.GetThroughputClass() != c.class || inv.GetContextLength() != 32768 || inv.GetQuantization() != "Q4_K_M" {
				t.Fatalf("inventory %v, want %s %s", inv, c.acc, c.class)
			}
		})
	}
	chain := &v1.Formation{Name: "f", Shape: v1.Shape_SHAPE_CHAIN, Seats: seats}
	if inv, err := m.formationInventory(context.Background(), chain, head, records, map[string]bool{seatKey(seats[1]): true}); err != nil || inv.GetAccelerator() != "1x NVIDIA H100 80GB HBM3" {
		t.Fatalf("a skipped seat is left out, got %v %v", inv, err)
	}
	bare := map[string]*v1.Instance{seatKey(seats[0]): head, seatKey(seats[1]): {Name: "f/stage1"}}
	if _, err := m.formationInventory(context.Background(), chain, head, bare, nil); err == nil {
		t.Fatal("a seat reporting no inventory is refused")
	}
	unlaunched := &v1.Formation{Name: "f", Shape: v1.Shape_SHAPE_CHAIN, Seats: []*v1.Seat{seats[0], {NodeId: "b", Role: "stage", Rank: 1}}}
	if _, err := m.formationInventory(context.Background(), unlaunched, head, records, nil); err == nil {
		t.Fatal("a seat with no instance is refused")
	}
	if _, err := m.formationInventory(context.Background(), chain, &v1.Instance{Name: "f/head0"}, records, nil); err == nil {
		t.Fatal("a serving seat reporting no inventory is refused")
	}
}

// A replicas formation hands its pool the inventory its replica launches recorded
func TestFormationPublishesInventory(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n := newReplicasNode(t, ctx)
	f := n.run(t, ctx, "inv")
	member, ok := n.reg.Membership(pools.OwnerRef{Kind: pools.OwnerFormation, ID: f.GetId()})
	if !ok || member.Inventory == nil {
		t.Fatalf("membership %+v", member)
	}
	seat, err := n.m.Instances.Get(f.GetSeats()[0].GetInstanceId())
	if err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(member.Inventory, seat.GetInventory()) || member.Runtime != fake.RuntimeID {
		t.Fatalf("membership inventory %v, the replica recorded %v", member.Inventory, seat.GetInventory())
	}
}

// An SGLang relay prefill seat publishes its bootstrap port, no other seat or runtime does
func TestBootstrapPortLabel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := argsConductor(t, ctx)
	cases := []struct {
		name  string
		rt    runtimes.Runtime
		shape v1.Shape
		role  string
		want  string
	}{
		{"sglang relay prefill", runtimes.SGLang{}, v1.Shape_SHAPE_RELAY, runtimes.RolePrefill, "8998"},
		{"sglang relay decode", runtimes.SGLang{}, v1.Shape_SHAPE_RELAY, runtimes.RoleDecode, ""},
		{"vllm relay prefill", runtimes.VLLM{}, v1.Shape_SHAPE_RELAY, runtimes.RolePrefill, ""},
		{"sglang replica", runtimes.SGLang{}, v1.Shape_SHAPE_REPLICAS, runtimes.RoleReplica, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := &v1.Formation{Id: "f1", Name: "f", Shape: c.shape}
			seat := &v1.Seat{NodeId: m.self(), Role: c.role, InstanceId: "i1"}
			in := &v1.Instance{Id: "i1", Endpoint: "http://127.0.0.1:30000", Seat: &v1.SeatSpec{Shape: c.shape, Role: c.role, AuxPort: 8998}}
			eps, err := m.seatPublished(f, c.rt, seat, in)
			if err != nil || len(eps) != 1 {
				t.Fatalf("published %v %v", eps, err)
			}
			if got := eps[0].Labels[requestcontrol.LabelBootstrapPort]; got != c.want {
				t.Fatalf("bootstrap port label %q, want %q", got, c.want)
			}
		})
	}
}
