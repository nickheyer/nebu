package pools

import (
	"context"
	"slices"
	"testing"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// The parent entry a controller wrote for a gateway on a route, nil when none
func parentEntry(r *v1.Route, controller, gateway string) *v1.ParentStatus {
	for _, p := range r.GetStatus().GetParents() {
		if p.GetControllerName() == controller && p.GetParentRef().GetKind() == "Gateway" && p.GetParentRef().GetName() == gateway {
			return p
		}
	}
	return nil
}

// A formation route keeps its shape, seats, node and this gateway's parent entry across a restart, seats changing without a new generation
func TestRouteRecordPersistsShapeSeatsNodeAndParents(t *testing.T) {
	ctx := context.Background()
	m, d, bus := newManager(t)
	owner := OwnerRef{Kind: OwnerFormation, ID: "f1"}
	if err := m.Publish(ctx, owner, membership("vllm"), []Published{published("i-a", 9401, true)}); err != nil {
		t.Fatal(err)
	}
	r, err := m.NameRoute(ctx, owner, "pair", RouteSpec{Policy: &v1.Policy{MaxInFlight: 2}, Shape: v1.Shape_SHAPE_RELAY, Seats: []string{"i-a", "i-b"}, NodeID: "n2"})
	if err != nil {
		t.Fatal(err)
	}
	if r.GetStatus().GetShape() != v1.Shape_SHAPE_RELAY || !slices.Equal(r.GetStatus().GetSeats(), []string{"i-a", "i-b"}) || r.GetNodeId() != "n2" || r.GetOwnerId() != "f1" || r.GetSpec().GetPolicy().GetMaxInFlight() != 2 {
		t.Fatalf("named route %v", r)
	}
	entry := parentEntry(r, GatewayController, node)
	if entry == nil || len(r.GetStatus().GetParents()) != 1 {
		t.Fatalf("parent entries %v", r.GetStatus().GetParents())
	}
	for _, typ := range []string{ConditionAccepted, ConditionResolvedRefs, ConditionReady} {
		if c := condition(entry.GetConditions(), typ); c.GetStatus() != v1.ConditionStatus_CONDITION_STATUS_TRUE {
			t.Fatalf("parent entry %s: %v", typ, c)
		}
	}
	readyAt := condition(entry.GetConditions(), ConditionReady).GetLastTransitionTime().AsTime()
	again, err := m.NameRoute(ctx, owner, "pair", RouteSpec{Policy: &v1.Policy{MaxInFlight: 2}, Shape: v1.Shape_SHAPE_RELAY, Seats: []string{"i-a", "i-c"}, NodeID: "n2"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(again.GetStatus().GetSeats(), []string{"i-a", "i-c"}) || again.GetMeta().GetGeneration() != 1 {
		t.Fatalf("replaced seat %v generation %d", again.GetStatus().GetSeats(), again.GetMeta().GetGeneration())
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	restored := openManager(t, d, bus)
	got, ok := restored.Route("pair")
	if !ok || got.GetStatus().GetShape() != v1.Shape_SHAPE_RELAY || !slices.Equal(got.GetStatus().GetSeats(), []string{"i-a", "i-c"}) || got.GetNodeId() != "n2" || got.GetOwnerKind() != OwnerFormation || got.GetOwnerId() != "f1" || got.GetSpec().GetPolicy().GetMaxInFlight() != 2 {
		t.Fatalf("restored route %v %v", got, ok)
	}
	kept := parentEntry(got, GatewayController, node)
	if kept == nil || len(got.GetStatus().GetParents()) != 1 {
		t.Fatalf("restored parent entries %v", got.GetStatus().GetParents())
	}
	if c := condition(kept.GetConditions(), ConditionReady); c.GetStatus() != v1.ConditionStatus_CONDITION_STATUS_FALSE || c.GetLastTransitionTime().AsTime().Equal(readyAt) {
		t.Fatalf("ready after the restart with no endpoint published: %v", c)
	}
	if c := condition(kept.GetConditions(), ConditionAccepted); c.GetStatus() != v1.ConditionStatus_CONDITION_STATUS_TRUE {
		t.Fatalf("accepted after the restart: %v", c)
	}
	if err := restored.Publish(ctx, owner, membership("vllm"), []Published{published("i-a", 9401, true)}); err != nil {
		t.Fatal(err)
	}
	if back, _ := restored.Route("pair"); !slices.Equal(back.GetStatus().GetSeats(), []string{"i-a", "i-c"}) || condition(parentEntry(back, GatewayController, node).GetConditions(), ConditionReady).GetStatus() != v1.ConditionStatus_CONDITION_STATUS_TRUE {
		t.Fatalf("route after the owner published again %v", back)
	}
}

// A gateway that no longer manages a restored route loses its parent entry while another controller's entry stays
func TestRestoredRouteDropsStaleGatewayEntry(t *testing.T) {
	ctx := context.Background()
	m, d, bus := newManager(t)
	owner := OwnerRef{Kind: OwnerFormation, ID: "f1"}
	if err := m.Publish(ctx, owner, membership("vllm"), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := m.NameRoute(ctx, owner, "pair", RouteSpec{Shape: v1.Shape_SHAPE_CHAIN}); err != nil {
		t.Fatal(err)
	}
	r, _ := m.Route("pair")
	r.Status.Parents = append(r.Status.Parents, &v1.ParentStatus{ParentRef: &v1.ParentReference{Kind: "Gateway", Name: "elsewhere"}, ControllerName: "other.example/importer", Conditions: []*v1.Condition{{Type: ConditionReady, Status: v1.ConditionStatus_CONDITION_STATUS_TRUE, Reason: ReasonReady}}})
	if err := d.PutRoute(ctx, r); err != nil {
		t.Fatal(err)
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	moved, err := New(ctx, Options{DB: d, Events: bus, NodeID: "n9"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { moved.Close() })
	got, _ := moved.Route("pair")
	if parentEntry(got, GatewayController, node) != nil {
		t.Fatalf("the old gateway's entry survived: %v", got.GetStatus().GetParents())
	}
	if parentEntry(got, GatewayController, "n9") == nil || parentEntry(got, "other.example/importer", "elsewhere") == nil || len(got.GetStatus().GetParents()) != 2 {
		t.Fatalf("parent entries after the move %v", got.GetStatus().GetParents())
	}
	if got.GetStatus().GetShape() != v1.Shape_SHAPE_CHAIN {
		t.Fatalf("shape after the move %v", got.GetStatus().GetShape())
	}
}

// Objectives and scheduler configurations carry Accepted and Ready conditions at their generation, kept across a restart
func TestObjectiveAndConfigStatus(t *testing.T) {
	ctx := context.Background()
	m, d, bus := newManager(t)
	o, err := m.PutObjective(ctx, &v1.Objective{Name: "fast", Priority: 1, TtftMs: 100})
	if err != nil {
		t.Fatal(err)
	}
	if o.GetStatus().GetObservedGeneration() != 1 || condition(o.GetStatus().GetConditions(), ConditionAccepted).GetStatus() != v1.ConditionStatus_CONDITION_STATUS_TRUE || condition(o.GetStatus().GetConditions(), ConditionReady).GetStatus() != v1.ConditionStatus_CONDITION_STATUS_TRUE {
		t.Fatalf("objective status %v", o.GetStatus())
	}
	acceptedAt := condition(o.GetStatus().GetConditions(), ConditionAccepted).GetLastTransitionTime().AsTime()
	o2, err := m.PutObjective(ctx, &v1.Objective{Name: "fast", Priority: 2, TtftMs: 100})
	if err != nil || o2.GetStatus().GetObservedGeneration() != 2 || !condition(o2.GetStatus().GetConditions(), ConditionAccepted).GetLastTransitionTime().AsTime().Equal(acceptedAt) {
		t.Fatalf("objective status after a change %v %v", o2.GetStatus(), err)
	}
	c, err := m.PutSchedulerConfig(ctx, &v1.SchedulerConfig{Name: "mine", Spec: &v1.SchedulerConfigSpec{Yaml: m.st.configs[SchedulerDefault].cfg.GetSpec().GetYaml()}})
	if err != nil || c.GetStatus().GetObservedGeneration() != 1 || condition(c.GetStatus().GetConditions(), ConditionAccepted).GetStatus() != v1.ConditionStatus_CONDITION_STATUS_TRUE {
		t.Fatalf("config status %v %v", c.GetStatus(), err)
	}
	configAt := condition(c.GetStatus().GetConditions(), ConditionAccepted).GetLastTransitionTime().AsTime()
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	again := openManager(t, d, bus)
	if got, _ := again.Objective("fast"); got.GetStatus().GetObservedGeneration() != 2 || !condition(got.GetStatus().GetConditions(), ConditionAccepted).GetLastTransitionTime().AsTime().Equal(acceptedAt) {
		t.Fatalf("restored objective status %v", got.GetStatus())
	}
	if got, _ := again.SchedulerConfig("mine"); got.GetStatus().GetObservedGeneration() != 1 || !condition(got.GetStatus().GetConditions(), ConditionAccepted).GetLastTransitionTime().AsTime().Equal(configAt) {
		t.Fatalf("restored config status %v", got.GetStatus())
	}
}
