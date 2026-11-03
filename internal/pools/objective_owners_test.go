package pools

import (
	"context"
	"testing"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// Owners hold objectives, a user's targets win, the last owner letting go deletes one no route names, and holders persist
func TestObjectiveOwners(t *testing.T) {
	ctx := context.Background()
	m, d, bus := newManager(t)
	a, b := OwnerRef{Kind: OwnerNode, ID: "a"}, OwnerRef{Kind: OwnerNode, ID: "b"}
	if _, err := m.PutObjectiveFor(ctx, OwnerRef{Kind: OwnerUser}, &v1.Objective{Name: "x"}); err == nil {
		t.Fatal("the user holds objectives through PutObjective")
	}
	if _, err := m.PutObjectiveFor(ctx, a, &v1.Objective{Name: "fast", Priority: 5, TtftMs: 100}); err != nil {
		t.Fatal(err)
	}
	if o, err := m.PutObjectiveFor(ctx, b, &v1.Objective{Name: "fast", Priority: 7, TtftMs: 90}); err != nil || o.GetPriority() != 7 {
		t.Fatalf("another owner's latest targets are taken: %v %v", o, err)
	}
	m.DropObjective(ctx, a, "fast")
	if _, ok := m.Objective("fast"); !ok {
		t.Fatal("an objective another owner holds stays")
	}
	m.DropObjectives(ctx, b)
	if _, ok := m.Objective("fast"); ok {
		t.Fatal("the last owner letting go deletes the objective")
	}
	// A user's objective keeps its targets and outlives every owner
	if _, err := m.PutObjective(ctx, &v1.Objective{Name: "mine", Priority: 1}); err != nil {
		t.Fatal(err)
	}
	if o, err := m.PutObjectiveFor(ctx, a, &v1.Objective{Name: "mine", Priority: 9}); err != nil || o.GetPriority() != 1 {
		t.Fatalf("a user's targets win: %v %v", o, err)
	}
	m.DropObjectives(ctx, a)
	if o, ok := m.Objective("mine"); !ok || o.GetPriority() != 1 {
		t.Fatal("a user's objective outlives its other owners")
	}
	// An owner's objective a route still names passes to the user when the owner lets go
	if _, err := m.PutObjectiveFor(ctx, a, &v1.Objective{Name: "named", Priority: 3}); err != nil {
		t.Fatal(err)
	}
	inst := OwnerRef{Kind: OwnerInstance, ID: "i"}
	if err := m.Publish(ctx, inst, membership("vllm"), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := m.NameRoute(ctx, inst, "chat", RouteSpec{Objective: "named"}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.PutObjectiveFor(ctx, b, &v1.Objective{Name: "held", Priority: 2}); err != nil {
		t.Fatal(err)
	}
	m.DropObjectives(ctx, a)
	if _, ok := m.Objective("named"); !ok {
		t.Fatal("an objective a route names stays")
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	again := openManager(t, d, bus)
	again.DropObjectives(ctx, b)
	if _, ok := again.Objective("held"); ok {
		t.Fatal("holders persist, so the restored owner letting go deletes the objective")
	}
	again.DropObjectives(ctx, a)
	if _, ok := again.Objective("named"); !ok {
		t.Fatal("the objective the route names passed to the user")
	}
	if o, ok := again.Objective("mine"); !ok || o.GetPriority() != 1 {
		t.Fatal("the user's objective persists")
	}
}
