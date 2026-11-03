package pools

import (
	"context"
	"testing"

	"github.com/nickheyer/nebu/pkg/datalayer"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/proto"
)

// The inventory an owner's launch reports reaches the derived pool spec and every route over the pool
func TestInventoryReachesPoolAndRoutes(t *testing.T) {
	ctx := context.Background()
	m, d, bus := newManager(t)
	owner := OwnerRef{Kind: OwnerInstance, ID: "i1"}
	mem := membership("vllm")
	if err := m.Publish(ctx, owner, mem, nil); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"chat", "assistant"} {
		if _, err := m.NameRoute(ctx, owner, name, RouteSpec{}); err != nil {
			t.Fatal(err)
		}
	}
	id := string(PoolIDOf(llama, "vllm", ""))
	if p, _ := m.Pool(id); p.GetSpec().GetInventory() != nil {
		t.Fatalf("a pool declared before launch carries inventory %v", p.GetSpec().GetInventory())
	}
	inv := &v1.RuntimeInventory{ContextLength: 16384, Quantization: "awq", Accelerator: "1x NVIDIA L40S", Modalities: []string{"text", "image"}, ThroughputClass: "device"}
	mem.Inventory = inv
	if err := m.Publish(ctx, owner, mem, []Published{published("i1", 8001, true)}); err != nil {
		t.Fatal(err)
	}
	inv.ContextLength = 1
	want := &v1.RuntimeInventory{ContextLength: 16384, Quantization: "awq", Accelerator: "1x NVIDIA L40S", Modalities: []string{"text", "image"}, ThroughputClass: "device"}
	p, _ := m.Pool(id)
	if !proto.Equal(p.GetSpec().GetInventory(), want) {
		t.Fatalf("pool inventory %v, want %v", p.GetSpec().GetInventory(), want)
	}
	for _, name := range []string{"chat", "assistant"} {
		rt, _ := m.Route(name)
		if !proto.Equal(rt.GetStatus().GetInventory(), want) {
			t.Fatalf("route %s inventory %v, want %v", name, rt.GetStatus().GetInventory(), want)
		}
	}
	relaunched := proto.Clone(want).(*v1.RuntimeInventory)
	relaunched.ContextLength = 32768
	mem.Inventory = relaunched
	if err := m.Publish(ctx, owner, mem, []Published{published("i1", 8001, true)}); err != nil {
		t.Fatal(err)
	}
	if rt, _ := m.Route("chat"); rt.GetStatus().GetInventory().GetContextLength() != 32768 {
		t.Fatalf("a relaunch with a longer context left the route at %v", rt.GetStatus().GetInventory())
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := openManager(t, d, bus)
	if err := reopened.Publish(ctx, owner, mem, []Published{published("i1", 8001, true)}); err != nil {
		t.Fatal(err)
	}
	if rt, ok := reopened.Route("chat"); !ok || rt.GetStatus().GetInventory().GetContextLength() != 32768 {
		t.Fatalf("the owner republished at recovery, the route shows %v", rt.GetStatus().GetInventory())
	}
}

// A split route shows the inventory of its first target in rule order whose pool reports one
func TestSplitRouteInventoryFollowsTargets(t *testing.T) {
	ctx := context.Background()
	m, _, _ := newManager(t)
	a, b := OwnerRef{Kind: OwnerInstance, ID: "a"}, OwnerRef{Kind: OwnerInstance, ID: "b"}
	other := ModelKey{SourceID: "hf", Repo: "meta/other", Group: "q4"}
	memA := membership("vllm")
	if err := m.Publish(ctx, a, memA, []Published{published("a", 8001, true)}); err != nil {
		t.Fatal(err)
	}
	memB := membership("vllm")
	memB.Model = other
	memB.Inventory = &v1.RuntimeInventory{ContextLength: 4096, Quantization: "Q8_0", Accelerator: "cpu", Modalities: []string{"embedding"}, ThroughputClass: "host"}
	if err := m.Publish(ctx, b, memB, []Published{published("b", 8002, true)}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.NameRoute(ctx, a, "split", RouteSpec{}); err != nil {
		t.Fatal(err)
	}
	poolA, poolB := PoolIDOf(llama, "vllm", ""), PoolIDOf(other, "vllm", "")
	split := func(first, second datalayer.PoolID) {
		t.Helper()
		if err := m.SplitRoute(ctx, a, "split", []WeightedTarget{{Pool: first, Model: "x", Weight: 1}, {Pool: second, Model: "y", Weight: 1}}); err != nil {
			t.Fatal(err)
		}
	}
	split(poolA, poolB)
	if rt, _ := m.Route("split"); rt.GetStatus().GetInventory().GetQuantization() != "Q8_0" {
		t.Fatalf("the first target reports none, the second's applies, got %v", rt.GetStatus().GetInventory())
	}
	memA.Inventory = &v1.RuntimeInventory{ContextLength: 8192, Quantization: "fp8", Accelerator: "1x NVIDIA H100 80GB HBM3", Modalities: []string{"text"}, ThroughputClass: "device"}
	if err := m.Publish(ctx, a, memA, []Published{published("a", 8001, true)}); err != nil {
		t.Fatal(err)
	}
	if rt, _ := m.Route("split"); rt.GetStatus().GetInventory().GetQuantization() != "fp8" {
		t.Fatalf("the first target's inventory applies once it reports one, got %v", rt.GetStatus().GetInventory())
	}
	split(poolB, poolA)
	if rt, _ := m.Route("split"); rt.GetStatus().GetInventory().GetQuantization() != "Q8_0" {
		t.Fatalf("reordered targets, got %v", rt.GetStatus().GetInventory())
	}
}
