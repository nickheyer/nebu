package mesh

import (
	"context"
	"testing"
	"time"

	"github.com/nickheyer/nebu/internal/db"
	"github.com/nickheyer/nebu/internal/pools"
	"github.com/nickheyer/nebu/pkg/datalayer"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/scheduling/remote"
)

// Exported pools, routes and objectives are imported under the exporter and released on withdrawal or departure
func TestExportedPoolsPublishAndUnpublish(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := newNode(t, ctx, 200*time.Millisecond)
	b := newNode(t, ctx, 200*time.Millisecond)
	formMesh(t, ctx, a, b)
	eventually(t, "a and b meet", 10*time.Second, func() bool { return allMet(a, b) })
	pool := testPool(b.Self(), "127.0.0.1:9400", true)
	model := pools.ModelKey{SourceID: "hf", Repo: "org/model", Group: "Q4"}
	if string(pools.PoolIDOf(model, "vllm", "")) != pool.GetId() {
		t.Fatalf("the test pool's id is the one its model and runtime name: %s", pools.PoolIDOf(model, "vllm", ""))
	}
	// An endpoint another member serves in the same pool is never re-exported
	pool.Status.Endpoints = append(pool.Status.Endpoints, &v1.Endpoint{Id: "far", Address: "10.9.9.9:1", NodeId: "elsewhere", Ready: true})
	b.pools.define(&v1.Objective{Name: "fast", Priority: 5, TtftMs: 200, Meta: &v1.ObjectMeta{Generation: 3}})
	chat := &v1.RewriteRule{Match: "chat", Targets: []*v1.RewriteTarget{{PoolId: pool.GetId(), Model: "llama-q4", Weight: 1}}}
	routes := []*v1.Route{
		{Name: "chat", Spec: &v1.RouteSpec{Objective: "fast", Policy: &v1.Policy{MaxInFlight: 8}, Rules: []*v1.RewriteRule{chat}}, Status: &v1.RouteStatus{PoolIds: []string{pool.GetId()}, Requests: 99}},
		{Name: "plain", Spec: &v1.RouteSpec{}, Status: &v1.RouteStatus{PoolIds: []string{pool.GetId()}}},
		{Name: "split", Spec: &v1.RouteSpec{}, Status: &v1.RouteStatus{PoolIds: []string{pool.GetId(), "other/pool"}}},
	}
	b.pools.set([]*v1.Pool{pool}, routes)
	b.Bump()
	owner := pools.OwnerRef{Kind: "node", ID: b.Self() + "/" + pool.GetId()}
	objectives := pools.OwnerRef{Kind: "node", ID: b.Self()}
	eventually(t, "a publishes b's pool and routes", 10*time.Second, func() bool {
		pub, named := a.pools.snapshot()
		call, ok := pub[owner]
		return ok && len(call.endpoints) == 1 && len(named[owner]) == 2
	})
	if held := a.pools.heldFor(objectives); len(held) != 1 || held[0] != "fast" {
		t.Fatalf("the objective b's route names is held for b: %v", held)
	}
	if o, ok := a.pools.Objective("fast"); !ok || o.GetPriority() != 5 || o.GetTtftMs() != 200 || o.GetMeta() != nil {
		t.Fatalf("the objective carries its targets alone: %v", o)
	}
	pub, named := a.pools.snapshot()
	call := pub[owner]
	ep := call.endpoints[0]
	// The endpoint is reached at b's mesh host on the relay serving its loopback address, never at the loopback address itself
	relay := b.relayPort("127.0.0.1:9400")
	if relay == 0 || relay == 9400 {
		t.Fatalf("b serves no relay for its endpoint: %d", relay)
	}
	if call.model != model || call.runtime != "vllm" || call.kvCapacity != 8192 || ep.NodeID != b.Self() || ep.Address != (datalayer.Address{Host: "127.0.0.1", Port: relay}) || !ep.Ready || ep.Adapter != "sql" {
		t.Fatalf("published %+v", call)
	}
	if ep.Labels[remote.LabelExportedID] != "ep1" || ep.Labels[LabelRemoteScheduler] != b.Self() {
		t.Fatalf("an endpoint mode import names its exported id and the exporter's scheduler: %v", ep.Labels)
	}
	a.pools.mu.Lock()
	spec, plain := a.pools.named[owner]["chat"], a.pools.named[owner]["plain"]
	_, split := a.pools.named[owner]["split"]
	a.pools.mu.Unlock()
	if split {
		t.Fatalf("a route over several pools is not named under one: %v", named)
	}
	if spec.Objective != "fast" || spec.Policy.GetMaxInFlight() != 8 || spec.ServedName != "llama-q4" {
		t.Fatalf("the route keeps its policy, objective and the model its rule targets: %+v", spec)
	}
	if plain.ServedName != "hf:org/model:Q4" {
		t.Fatalf("a route whose rule names no model serves the pool's model: %+v", plain)
	}
	// The import lists b as its exporter in endpoint mode with the endpoints b serves alone
	list := a.Imports()
	if len(list) != 1 || list[0].GetId() != pool.GetId() || list[0].GetStatus().GetRoutingMode() != RoutingEndpoint {
		t.Fatalf("imports %v", list)
	}
	ctl := list[0].GetStatus().GetControllers()
	if conds := ctl[0].GetConditions(); len(conds) != 1 || conds[0].GetReason() != ReasonPoolNotPublished || conds[0].GetStatus() != v1.ConditionStatus_CONDITION_STATUS_FALSE {
		t.Fatalf("an import whose pool the view lacks says so: %v", conds)
	}
	if len(ctl) != 1 || ctl[0].GetName() != ImportController || len(ctl[0].GetExportingSources()) != 1 || ctl[0].GetExportingSources()[0].GetNodeId() != b.Self() || ctl[0].GetExportingSources()[0].GetMeshHash() == "" {
		t.Fatalf("import controller %v", ctl)
	}
	seen := testPool(b.Self(), "127.0.0.1:9400", true)
	seen.Status.Endpoints = append(seen.Status.Endpoints, &v1.Endpoint{Id: "far", Address: "10.9.9.9:1", NodeId: "elsewhere", Ready: true})
	seen.Status.Conditions = []*v1.Condition{{Type: "Ready", Status: v1.ConditionStatus_CONDITION_STATUS_TRUE}}
	a.pools.set([]*v1.Pool{seen}, nil)
	imp, ok := a.Import(pool.GetId())
	a.pools.set(nil, nil)
	if !ok || len(imp.GetStatus().GetEndpoints()) != 1 || imp.GetStatus().GetEndpoints()[0].GetNodeId() != b.Self() || len(imp.GetStatus().GetControllers()[0].GetConditions()) != 1 {
		t.Fatalf("the import carries the exporter's endpoints and the pool's conditions: %v", imp)
	}
	if _, ok := a.Import("no/such:pool"); ok {
		t.Fatal("a pool nobody exports is no import")
	}
	// A readiness change on the exporter reaches the import
	b.pools.set([]*v1.Pool{testPool(b.Self(), "127.0.0.1:9400", false)}, routes)
	b.Bump()
	eventually(t, "a takes the endpoint's readiness", 10*time.Second, func() bool {
		pub, _ := a.pools.snapshot()
		call, ok := pub[owner]
		return ok && len(call.endpoints) == 1 && !call.endpoints[0].Ready
	})
	// Withdrawing the pool unpublishes it and its routes
	b.pools.set(nil, nil)
	b.Bump()
	eventually(t, "a unpublishes the withdrawn pool and lets go of its objective", 10*time.Second, func() bool {
		pub, named := a.pools.snapshot()
		return len(pub) == 0 && len(named) == 0 && len(a.pools.heldFor(objectives)) == 0
	})
	if list := a.Imports(); len(list) != 0 {
		t.Fatalf("a withdrawn pool is no longer imported: %v", list)
	}
	// A member that leaves takes everything it exported with it
	b.pools.set([]*v1.Pool{testPool(b.Self(), "127.0.0.1:9400", true)}, routes)
	b.Bump()
	eventually(t, "a publishes the pool again", 10*time.Second, func() bool {
		pub, _ := a.pools.snapshot()
		return len(pub) == 1 && len(a.pools.heldFor(objectives)) == 1
	})
	if _, err := b.Leave(ctx); err != nil {
		t.Fatal(err)
	}
	eventually(t, "a unpublishes what b owned and lets go of its objectives", 10*time.Second, func() bool {
		pub, named := a.pools.snapshot()
		return len(pub) == 0 && len(named) == 0 && len(a.pools.heldFor(objectives)) == 0
	})
}

// A pool whose id its model and runtime do not derive is not imported
func TestImportRefusesUnderivablePools(t *testing.T) {
	if k, ok := modelKeyOf("hf:org/model:Q4"); !ok || k != (pools.ModelKey{SourceID: "hf", Repo: "org/model", Group: "Q4"}) {
		t.Fatalf("model key %v", k)
	}
	if _, ok := modelKeyOf("org/model"); ok {
		t.Fatal("a model without source and group names no key")
	}
	m := bare(t, "self")
	fake := m.Registrar.(*fakePools)
	odd := testPool("x", "127.0.0.1:1", true)
	odd.Id = "my-pool"
	m.members["x"] = &member{rec: &v1.Node{Id: "x", State: v1.NodeState_NODE_STATE_READY, Pools: []*v1.Pool{odd}}}
	m.importFrom(context.Background(), "x")
	if pub, _ := fake.snapshot(); len(pub) != 0 {
		t.Fatalf("an underivable pool is not imported: %v", pub)
	}
	if protocolOf(v1.AppProtocol_APP_PROTOCOL_H2C) != datalayer.AppProtocolH2C || protocolOf(v1.AppProtocol_APP_PROTOCOL_GRPC) != datalayer.AppProtocolGRPC {
		t.Fatal("protocols map to the data layer's names")
	}
}

// A member record exporting one pool and one route, for driving imports by hand
func exportingMember(id, address, endpoint string) *v1.Node {
	pool := testPool(id, endpoint, true)
	pool.Status.Parents = []*v1.ParentStatus{{ParentRef: &v1.ParentReference{Kind: "Gateway", Name: id}, ControllerName: "nebu.dev/gateway", Conditions: []*v1.Condition{{Type: "Accepted", Status: v1.ConditionStatus_CONDITION_STATUS_TRUE, Reason: "Accepted"}}}}
	return &v1.Node{Id: id, Name: "node-" + id, Address: address, State: v1.NodeState_NODE_STATE_READY, Pools: []*v1.Pool{pool},
		Routes: []*v1.Route{{Name: "chat", Spec: &v1.RouteSpec{}, Status: &v1.RouteStatus{PoolIds: []string{pool.GetId()}}}}}
}

// Parent mode names the route through the exporter's gateway, endpoint mode labels endpoints for the exporter's scheduler, each per pool or mesh wide
func TestImportRoutingModes(t *testing.T) {
	tests := []struct {
		name      string
		mesh      string
		perPool   string
		wantMode  string
		wantNode  bool
		wantLabel bool
	}{
		{"endpoint by default", "", "", RoutingEndpoint, false, true},
		{"parent mesh wide", "parent", "", RoutingParent, true, false},
		{"endpoint for the pool over parent mesh wide", "parent", "endpoint", RoutingEndpoint, false, true},
		{"parent for the pool over endpoint mesh wide", "", "parent", RoutingParent, true, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := bare(t, "self")
			m.mesh = &db.MeshRow{ID: "mesh-1"}
			fake := m.Registrar.(*fakePools)
			rec := exportingMember("x", "10.0.0.1:8485", "10.0.0.1:5000")
			poolID := rec.GetPools()[0].GetId()
			m.Config.RoutingMode = tc.mesh
			if tc.perPool != "" {
				m.Config.ImportRouting = map[string]string{poolID: tc.perPool}
			}
			m.members["x"] = &member{rec: rec}
			m.importFrom(context.Background(), "x")
			owner := pools.OwnerRef{Kind: "node", ID: "x/" + poolID}
			pub, _ := fake.snapshot()
			call, ok := pub[owner]
			if !ok || len(call.endpoints) != 1 {
				t.Fatalf("published %v", pub)
			}
			if _, labelled := call.endpoints[0].Labels[LabelRemoteScheduler]; labelled != tc.wantLabel || call.endpoints[0].Labels[remote.LabelExportedID] != "ep1" {
				t.Fatalf("endpoint labels %v", call.endpoints[0].Labels)
			}
			fake.mu.Lock()
			spec := fake.named[owner]["chat"]
			fake.mu.Unlock()
			if (spec.NodeID == "x") != tc.wantNode || (spec.NodeID != "" && spec.NodeID != "x") {
				t.Fatalf("route node %q", spec.NodeID)
			}
			imp, ok := m.Import(poolID)
			if !ok || imp.GetSpec().GetRoutingMode() != tc.wantMode || imp.GetStatus().GetRoutingMode() != tc.wantMode {
				t.Fatalf("import %v", imp)
			}
			eps := imp.GetStatus().GetEndpoints()
			if tc.wantMode == RoutingParent && (len(eps) != 1 || eps[0].GetAddress() != "10.0.0.1:8485" || eps[0].GetNodeId() != "x" || !eps[0].GetReady()) {
				t.Fatalf("parent mode routes to the exporter's gateway: %v", eps)
			}
		})
	}
}

// A withdrawn import drains first and is unpublished once its requests in flight are done, at the drain timeout otherwise, a pool exported again meanwhile staying published
func TestWithdrawnImportDrainsBeforeUnpublish(t *testing.T) {
	ctx := context.Background()
	m := bare(t, "self")
	m.mesh = &db.MeshRow{ID: "mesh-1"}
	m.DrainTimeout = 400 * time.Millisecond
	fake := m.Registrar.(*fakePools)
	rec := exportingMember("x", "10.0.0.1:8485", "10.0.0.1:5000")
	poolID := rec.GetPools()[0].GetId()
	owner := pools.OwnerRef{Kind: "node", ID: "x/" + poolID}
	m.members["x"] = &member{rec: rec}
	m.importFrom(ctx, "x")
	fake.setInFlight("in-x", 2)
	m.members["x"].rec.Pools = nil
	m.importFrom(ctx, "x")
	time.Sleep(150 * time.Millisecond)
	if calls := fake.log(); len(calls) != 1 || calls[0] != "drain "+owner.ID {
		t.Fatalf("a draining import is not unpublished while requests are in flight: %v", calls)
	}
	if imp, ok := m.Import(poolID); !ok || len(imp.GetStatus().GetControllers()[0].GetParents()) != 0 {
		t.Fatalf("a draining import stays listed without the exporter's entries: %v %v", imp, ok)
	}
	// The pool exported again cancels the removal
	m.members["x"].rec.Pools = exportingMember("x", "10.0.0.1:8485", "10.0.0.1:5000").GetPools()
	m.importFrom(ctx, "x")
	fake.setInFlight("in-x", 0)
	time.Sleep(200 * time.Millisecond)
	pub, _ := fake.snapshot()
	if _, ok := pub[owner]; !ok || len(fake.log()) != 1 {
		t.Fatalf("a pool exported again while draining stays published: %v %v", pub, fake.log())
	}
	// Withdrawn with no request in flight, it goes once the in flight count is read as zero
	m.members["x"].rec.Pools = nil
	m.importFrom(ctx, "x")
	eventually(t, "the import is unpublished", 5*time.Second, func() bool {
		pub, _ := fake.snapshot()
		_, ok := pub[owner]
		return !ok
	})
	calls := fake.log()
	if len(calls) != 4 || calls[1] != "drain "+owner.ID || calls[2] != "droproutes "+owner.ID || calls[3] != "unpublish "+owner.ID {
		t.Fatalf("drain, then routes dropped, then unpublished: %v", calls)
	}
	if _, ok := m.Import(poolID); ok {
		t.Fatal("a removed import is listed")
	}
	// Requests stuck in flight hold the removal until the drain timeout alone
	m.members["x"].rec.Pools = exportingMember("x", "10.0.0.1:8485", "10.0.0.1:5000").GetPools()
	m.importFrom(ctx, "x")
	fake.setInFlight("in-x", 1)
	start := time.Now()
	m.members["x"].rec.Pools = nil
	m.importFrom(ctx, "x")
	eventually(t, "the import is unpublished at the drain timeout", 5*time.Second, func() bool {
		pub, _ := fake.snapshot()
		_, ok := pub[owner]
		return !ok
	})
	if took := time.Since(start); took < m.DrainTimeout {
		t.Fatalf("unpublished after %s, before the drain timeout", took)
	}
}

// Dropping a member drains its imports, drops its objectives and removes its controllers' entries from the import status
func TestDropImportsDrainsAndRemovesControllers(t *testing.T) {
	ctx := context.Background()
	m := bare(t, "self")
	m.mesh = &db.MeshRow{ID: "mesh-1"}
	fake := m.Registrar.(*fakePools)
	rec := exportingMember("x", "10.0.0.1:8485", "10.0.0.1:5000")
	rec.Routes[0].Spec.Objective = "fast"
	rec.Objectives = []*v1.Objective{{Name: "fast", Priority: 2}}
	poolID := rec.GetPools()[0].GetId()
	owner := pools.OwnerRef{Kind: "node", ID: "x/" + poolID}
	m.members["x"] = &member{rec: rec}
	m.importFrom(ctx, "x")
	imp, ok := m.Import(poolID)
	if !ok {
		t.Fatal("not imported")
	}
	parents := imp.GetStatus().GetControllers()[0].GetParents()
	if len(parents) != 1 || parents[0].GetControllerName() != "nebu.dev/gateway" || parents[0].GetParentRef().GetName() != "x" {
		t.Fatalf("the exporter's entry is carried under its controller and gateway: %v", parents)
	}
	if held := fake.heldFor(pools.OwnerRef{Kind: "node", ID: "x"}); len(held) != 1 {
		t.Fatalf("objective held %v", held)
	}
	fake.setInFlight("in-x", 1)
	m.dropImports(ctx, "x")
	imp, ok = m.Import(poolID)
	if !ok || len(imp.GetStatus().GetControllers()[0].GetParents()) != 0 {
		t.Fatalf("the dropped exporter's entries are removed while its import drains: %v %v", imp, ok)
	}
	if held := fake.heldFor(pools.OwnerRef{Kind: "node", ID: "x"}); len(held) != 0 {
		t.Fatalf("objectives are let go at once: %v", held)
	}
	fake.setInFlight("in-x", 0)
	eventually(t, "the import goes after the drain", 5*time.Second, func() bool {
		pub, _ := fake.snapshot()
		_, ok := pub[owner]
		return !ok
	})
}

// The not published condition of an import keeps its transition time across reads until the pool appears
func TestUnpublishedConditionKeepsItsTransitionTime(t *testing.T) {
	m := bare(t, "self")
	m.mesh = &db.MeshRow{ID: "mesh-1"}
	rec := exportingMember("x", "10.0.0.1:8485", "10.0.0.1:5000")
	poolID := rec.GetPools()[0].GetId()
	m.members["x"] = &member{rec: rec}
	m.importFrom(context.Background(), "x")
	first, _ := m.Import(poolID)
	time.Sleep(20 * time.Millisecond)
	second, _ := m.Import(poolID)
	c1, c2 := first.GetStatus().GetControllers()[0].GetConditions()[0], second.GetStatus().GetControllers()[0].GetConditions()[0]
	if c1.GetReason() != ReasonPoolNotPublished || !c1.GetLastTransitionTime().AsTime().Equal(c2.GetLastTransitionTime().AsTime()) {
		t.Fatalf("the transition time moved with no transition: %v %v", c1, c2)
	}
	fake := m.Pools.(*fakePools)
	fake.set([]*v1.Pool{testPool("x", "10.0.0.1:5000", true)}, nil)
	seen, _ := m.Import(poolID)
	if conds := seen.GetStatus().GetControllers()[0].GetConditions(); len(conds) != 0 {
		t.Fatalf("a published pool carries the pool's own conditions: %v", conds)
	}
	fake.set(nil, nil)
	third, _ := m.Import(poolID)
	if third.GetStatus().GetControllers()[0].GetConditions()[0].GetLastTransitionTime().AsTime().Before(c1.GetLastTransitionTime().AsTime().Add(20 * time.Millisecond)) {
		t.Fatal("a pool missing again transitions anew")
	}
}
