package pools

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nickheyer/nebu/internal/db"
	"github.com/nickheyer/nebu/pkg/conditions"
	"github.com/nickheyer/nebu/pkg/datalayer"
	"github.com/nickheyer/nebu/pkg/events"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/requestcontrol"
	"google.golang.org/protobuf/proto"
)

// The ports of a pool's endpoints in the store, sorted
func storePorts(m *Manager, id datalayer.PoolID) []int {
	var ports []int
	for _, ep := range m.Store().Endpoints(id, nil) {
		ports = append(ports, ep.Address().Port)
	}
	slices.Sort(ports)
	return ports
}

// A user target port list keeps only endpoints on it, across a port list change and across a restart
func TestTargetPortsFilterEndpoints(t *testing.T) {
	ctx := context.Background()
	m, d, bus := newManager(t)
	inst := OwnerRef{Kind: OwnerInstance, ID: "i1"}
	a, b := published("i1", 8001, true), published("i1b", 8002, true)
	if err := m.Publish(ctx, inst, membership("vllm"), []Published{a, b}); err != nil {
		t.Fatal(err)
	}
	id := PoolIDOf(llama, "vllm", "")
	steps := []struct {
		ports []uint32
		want  []int
	}{{[]uint32{8001}, []int{8001}}, {[]uint32{9999}, nil}, {[]uint32{8002, 8001}, []int{8001, 8002}}}
	for _, s := range steps {
		if _, err := m.PutPool(ctx, string(id), &v1.PoolSpec{Model: llama.String(), TargetPorts: s.ports}); err != nil {
			t.Fatal(err)
		}
		p, _ := m.Store().Pool(id)
		if got := storePorts(m, id); !slices.Equal(got, s.want) || !slices.Equal(p.TargetPorts, uints(s.ports)) {
			t.Fatalf("target ports %v: endpoints on %v, store pool ports %v", s.ports, got, p.TargetPorts)
		}
		if rec, _ := m.Pool(string(id)); !slices.Equal(rec.GetSpec().GetTargetPorts(), s.ports) {
			t.Fatalf("target ports %v: rendered spec %v", s.ports, rec.GetSpec().GetTargetPorts())
		}
	}
	if _, err := m.PutPool(ctx, string(id), &v1.PoolSpec{Model: llama.String(), TargetPorts: []uint32{8002}}); err != nil {
		t.Fatal(err)
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	again := openManager(t, d, bus)
	if err := again.Publish(ctx, inst, membership("vllm"), []Published{a, b}); err != nil {
		t.Fatal(err)
	}
	if got := storePorts(again, id); !slices.Equal(got, []int{8002}) {
		t.Fatalf("after restart endpoints on %v, want the stored target port alone", got)
	}
}

func uints(in []uint32) []int {
	out := make([]int, len(in))
	for i, p := range in {
		out[i] = int(p)
	}
	return out
}

// Pools group by accelerator class as part of their id, spec, data layer record and endpoint labels
func TestAcceleratorGroupsPools(t *testing.T) {
	cases := []struct{ accelerator, id, class string }{
		{"", "vllm/hf:meta/llama:q4", ""},
		{"cpu", "vllm/hf:meta/llama:q4@cpu", "cpu"},
		{"2x NVIDIA H100 80GB HBM3", "vllm/hf:meta/llama:q4@2x-nvidia-h100-80gb-hbm3", "2x-nvidia-h100-80gb-hbm3"},
		{"  AMD  Instinct MI300X + 1x cpu ", "vllm/hf:meta/llama:q4@amd-instinct-mi300x-1x-cpu", "amd-instinct-mi300x-1x-cpu"},
	}
	for _, c := range cases {
		if got := PoolIDOf(llama, "vllm", c.accelerator); string(got) != c.id {
			t.Fatalf("%q: id %s, want %s", c.accelerator, got, c.id)
		}
		runtime, model, class, ok := SplitPoolID(PoolIDOf(llama, "vllm", c.accelerator))
		if !ok || runtime != "vllm" || model != llama.String() || class != c.class {
			t.Fatalf("%q: split %q %q %q %v", c.accelerator, runtime, model, class, ok)
		}
	}
	if _, _, _, ok := SplitPoolID("noslash"); ok {
		t.Fatal("an id without a runtime split")
	}
	ctx := context.Background()
	m, _, _ := newManager(t)
	gpu := membership("vllm")
	gpu.Accelerator = "2x NVIDIA H100 80GB HBM3"
	cpu := membership("vllm")
	cpu.Accelerator = "cpu"
	if err := m.Publish(ctx, OwnerRef{Kind: OwnerInstance, ID: "g"}, gpu, []Published{published("g", 8001, true)}); err != nil {
		t.Fatal(err)
	}
	if err := m.Publish(ctx, OwnerRef{Kind: OwnerInstance, ID: "c"}, cpu, []Published{published("c", 8002, true)}); err != nil {
		t.Fatal(err)
	}
	if n := len(m.Pools()); n != 2 {
		t.Fatalf("%d pools for two accelerators", n)
	}
	gid := PoolIDOf(llama, "vllm", gpu.Accelerator)
	p, ok := m.Pool(string(gid))
	if !ok || p.GetSpec().GetAccelerator() != gpu.Accelerator || p.GetSpec().GetRuntimeId() != "vllm" || p.GetSpec().GetModel() != llama.String() {
		t.Fatalf("gpu pool %v %v", p, ok)
	}
	if dl, _ := m.Store().Pool(gid); dl.Accelerator != gpu.Accelerator {
		t.Fatalf("data layer pool accelerator %q", dl.Accelerator)
	}
	if ep, _ := m.Store().Endpoint("g/8001"); ep.Labels()[datalayer.LabelAccelerator] != gpu.Accelerator {
		t.Fatalf("endpoint labels %v", ep.Labels())
	}
	if _, _, err := m.Lookup(ctx, llama.String()); err != nil {
		t.Fatal(err)
	}
	other := membership("vllm")
	other.Accelerator = "1x NVIDIA H100 80GB HBM3"
	if _, err := m.PutPool(ctx, string(gid), &v1.PoolSpec{Model: llama.String(), RuntimeId: "vllm", Accelerator: gpu.Accelerator, TargetPorts: []uint32{8001}}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.PutPool(ctx, "exported", &v1.PoolSpec{Model: llama.String(), RuntimeId: "vllm", Accelerator: gpu.Accelerator, TargetPorts: []uint32{1}, Export: proto.Bool(true)}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("exported pool named off its accelerator class: %v", err)
	}
	if _, err := m.PutPool(ctx, string(gid), &v1.PoolSpec{Model: llama.String(), RuntimeId: "vllm", Accelerator: gpu.Accelerator, TargetPorts: []uint32{1}, Export: proto.Bool(true)}); err != nil {
		t.Fatal(err)
	}
	bad := membership("vllm")
	bad.Model = ModelKey{SourceID: "hf", Repo: "a@b", Group: "q4"}
	if err := m.Publish(ctx, OwnerRef{Kind: OwnerInstance, ID: "bad"}, bad, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("model carrying the accelerator separator: %v", err)
	}
	m.Unpublish(ctx, OwnerRef{Kind: OwnerInstance, ID: "g"})
	if err := m.Publish(ctx, OwnerRef{Kind: OwnerInstance, ID: "g2"}, other, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := m.PutPool(ctx, string(PoolIDOf(llama, "vllm", other.Accelerator)), &v1.PoolSpec{Model: llama.String(), RuntimeId: "vllm", Accelerator: gpu.Accelerator, TargetPorts: []uint32{1}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("user accelerator over a pool derived on another: %v", err)
	}
}

// Owner derived pools export unless a user sets export false, and the Exported condition says which
func TestOwnerPoolsExportByDefault(t *testing.T) {
	ctx := context.Background()
	m, _, _ := newManager(t)
	inst := OwnerRef{Kind: OwnerInstance, ID: "i1"}
	if err := m.Publish(ctx, inst, membership("vllm"), []Published{published("i1", 8001, true)}); err != nil {
		t.Fatal(err)
	}
	id := string(PoolIDOf(llama, "vllm", ""))
	p, _ := m.Pool(id)
	if !p.GetSpec().GetExport() || len(m.Exported()) != 1 || m.Exported()[0].GetId() != id {
		t.Fatalf("derived pool not exported by default %v %v", p.GetSpec(), m.Exported())
	}
	if c := condition(p.GetStatus().GetConditions(), ConditionExported); c.GetStatus() != v1.ConditionStatus_CONDITION_STATUS_TRUE || c.GetReason() != ReasonExported {
		t.Fatalf("exported condition %v", c)
	}
	if _, err := m.PutPool(ctx, id, &v1.PoolSpec{Model: llama.String(), TargetPorts: []uint32{8001}, Export: proto.Bool(false)}); err != nil {
		t.Fatal(err)
	}
	p, _ = m.Pool(id)
	if p.GetSpec().GetExport() || len(m.Exported()) != 0 {
		t.Fatalf("export false override ignored %v", p.GetSpec())
	}
	if c := condition(p.GetStatus().GetConditions(), ConditionExported); c.GetStatus() != v1.ConditionStatus_CONDITION_STATUS_FALSE || c.GetReason() != ReasonNotExported {
		t.Fatalf("not exported condition %v", c)
	}
	if _, err := m.PutPool(ctx, id, &v1.PoolSpec{Model: llama.String(), TargetPorts: []uint32{8001}}); err != nil {
		t.Fatal(err)
	}
	if p, _ := m.Pool(id); !p.GetSpec().GetExport() {
		t.Fatal("an override leaving export unset turned the default off")
	}
	if _, err := m.PutPool(ctx, "static", &v1.PoolSpec{Model: "m", TargetPorts: []uint32{1}, StaticEndpoints: []*v1.StaticEndpoint{{Address: "10.0.0.1:1"}}}); err != nil {
		t.Fatal(err)
	}
	if p, _ := m.Pool("static"); p.GetSpec().GetExport() {
		t.Fatal("a user pool exported without being asked")
	}
}

// Counts of pools resolved over many draws of one client name
func drawCounts(t *testing.T, m *Manager, name string, n int) map[datalayer.PoolID]int {
	t.Helper()
	counts := map[datalayer.PoolID]int{}
	for range n {
		pool, _, err := m.Resolve(context.Background(), name)
		if err != nil {
			t.Fatal(err)
		}
		counts[pool.ID]++
	}
	return counts
}

// Node owners naming a route an instance holds join it as targets, and the route follows the last owner standing
func TestSameNameAcrossNodesShares(t *testing.T) {
	ctx := context.Background()
	m, _, _ := newManager(t)
	inst := OwnerRef{Kind: OwnerInstance, ID: "i1"}
	if err := m.Publish(ctx, inst, membership("vllm"), []Published{published("i1", 8001, true)}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.NameRoute(ctx, inst, "llama", RouteSpec{}); err != nil {
		t.Fatal(err)
	}
	vllm, sglang := PoolIDOf(llama, "vllm", ""), PoolIDOf(llama, "sglang", "")
	n2 := OwnerRef{Kind: OwnerNode, ID: "n2/" + string(vllm)}
	if err := m.Publish(ctx, n2, membership("vllm"), []Published{{InstanceID: "r1", NodeID: "n2", Address: datalayer.Address{Host: "10.0.0.2", Port: 9000}, Ready: true}}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.NameRoute(ctx, n2, "llama", RouteSpec{}); err != nil {
		t.Fatalf("same name from another node refused: %v", err)
	}
	n3 := OwnerRef{Kind: OwnerNode, ID: "n3/" + string(sglang)}
	if err := m.Publish(ctx, n3, membership("sglang"), []Published{{InstanceID: "r2", NodeID: "n3", Address: datalayer.Address{Host: "10.0.0.3", Port: 9000}, Ready: true}}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.NameRoute(ctx, n3, "llama", RouteSpec{ServedName: "served-elsewhere"}); err != nil {
		t.Fatalf("same name from another runtime refused: %v", err)
	}
	r, _ := m.Route("llama")
	targets := r.GetSpec().GetRules()[0].GetTargets()
	if r.GetOwnerKind() != OwnerInstance || len(targets) != 3 || !slices.Equal(r.GetStatus().GetPoolIds(), []string{string(vllm), string(sglang)}) {
		t.Fatalf("shared route %v", r)
	}
	counts := drawCounts(t, m, "llama", 3000)
	if counts[sglang] < 700 || counts[sglang] > 1300 || counts[vllm]+counts[sglang] != 3000 {
		t.Fatalf("three targets of one weight drew %v", counts)
	}
	m.Drain(ctx, inst)
	if r, _ := m.Route("llama"); r.GetStatus().GetState() != v1.RouteState_ROUTE_STATE_READY {
		t.Fatalf("route with a draining primary and live shared targets %v", r.GetStatus())
	}
	if counts := drawCounts(t, m, "llama", 300); counts[vllm]+counts[sglang] != 300 {
		t.Fatalf("draws while the primary drains %v", counts)
	}
	m.DropRoute(ctx, n3, "llama")
	if r, _ := m.Route("llama"); len(r.GetSpec().GetRules()[0].GetTargets()) != 2 || slices.Contains(r.GetStatus().GetPoolIds(), string(sglang)) {
		t.Fatalf("route after a node left %v", r)
	}
	m.DropRoutes(ctx, inst)
	m.Unpublish(ctx, inst)
	r, ok := m.Route("llama")
	if !ok || r.GetOwnerKind() != OwnerNode || r.GetOwnerId() != n2.ID || len(r.GetSpec().GetRules()[0].GetTargets()) != 1 {
		t.Fatalf("route after the instance left %v %v", r, ok)
	}
	if pool, _, err := m.Resolve(ctx, "llama"); err != nil || pool.ID != vllm {
		t.Fatalf("resolve through the remaining node %v %v", pool.ID, err)
	}
	if _, err := m.PutRoute(ctx, "llama", &v1.RouteSpec{Rules: r.GetSpec().GetRules(), Policy: &v1.Policy{MaxInFlight: 1}}); !errors.Is(err, ErrManaged) {
		t.Fatalf("override of a node held route: %v", err)
	}
	if _, err := m.PutRoute(ctx, "user", &v1.RouteSpec{Rules: []*v1.RewriteRule{rewrite("mine", to(vllm, "", 1))}}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.NameRoute(ctx, n2, "user", RouteSpec{}); !errors.Is(err, ErrConflict) {
		t.Fatalf("node naming a user route: %v", err)
	}
	m.DropRoutes(ctx, n2)
	if _, ok := m.Route("llama"); ok {
		t.Fatal("route outlived its last owner")
	}
}

// A local owner takes a route a node holds, the node staying a target, and the node gets it back when the local owner drops it
func TestLocalAfterForwardedRouteOrder(t *testing.T) {
	ctx := context.Background()
	m, d, bus := newManager(t)
	vllm, sglang := PoolIDOf(llama, "vllm", ""), PoolIDOf(llama, "sglang", "")
	remote := OwnerRef{Kind: OwnerNode, ID: "n2/" + string(sglang)}
	if err := m.Publish(ctx, remote, membership("sglang"), []Published{{InstanceID: "r1", NodeID: "n2", Address: datalayer.Address{Host: "10.0.0.2", Port: 9000}, Ready: true}}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.NameRoute(ctx, remote, "llama", RouteSpec{Policy: &v1.Policy{MaxInFlight: 7}}); err != nil {
		t.Fatal(err)
	}
	inst := OwnerRef{Kind: OwnerInstance, ID: "i1"}
	if err := m.Publish(ctx, inst, membership("vllm"), []Published{published("i1", 8001, false)}); err != nil {
		t.Fatal(err)
	}
	r, err := m.NameRoute(ctx, inst, "llama", RouteSpec{Policy: &v1.Policy{MaxInFlight: 3}})
	if err != nil {
		t.Fatalf("local owner after a forwarded route: %v", err)
	}
	if r.GetOwnerKind() != OwnerInstance || r.GetSpec().GetPolicy().GetMaxInFlight() != 3 || !slices.Equal(r.GetStatus().GetPoolIds(), []string{string(vllm), string(sglang)}) {
		t.Fatalf("taken route %v", r)
	}
	if routes, err := d.ListRoutes(ctx); err != nil || len(routes) != 1 || routes[0].GetOwnerKind() != OwnerInstance {
		t.Fatalf("stored routes after the local owner took over %v %v", routes, err)
	}
	if counts := drawCounts(t, m, "llama", 200); counts[sglang] != 200 {
		t.Fatalf("draws while the local endpoint is not ready %v", counts)
	}
	m.SetReady(ctx, "i1", true)
	if counts := drawCounts(t, m, "llama", 2000); counts[vllm] < 800 || counts[sglang] < 800 {
		t.Fatalf("draws with both ready %v", counts)
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	m = openManager(t, d, bus)
	if r, ok := m.Route("llama"); !ok || r.GetOwnerKind() != OwnerInstance || len(r.GetSpec().GetRules()[0].GetTargets()) != 1 {
		t.Fatalf("restored route %v %v", r, ok)
	}
	if err := m.Publish(ctx, remote, membership("sglang"), []Published{{InstanceID: "r1", NodeID: "n2", Address: datalayer.Address{Host: "10.0.0.2", Port: 9000}, Ready: true}}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.NameRoute(ctx, remote, "llama", RouteSpec{Policy: &v1.Policy{MaxInFlight: 7}}); err != nil {
		t.Fatal(err)
	}
	if err := m.Publish(ctx, inst, membership("vllm"), []Published{published("i1", 8001, true)}); err != nil {
		t.Fatal(err)
	}
	m.DropRoutes(ctx, inst)
	m.Unpublish(ctx, inst)
	r, ok := m.Route("llama")
	if !ok || r.GetOwnerKind() != OwnerNode || r.GetOwnerId() != remote.ID || r.GetSpec().GetPolicy().GetMaxInFlight() != 7 || !slices.Equal(r.GetStatus().GetPoolIds(), []string{string(sglang)}) {
		t.Fatalf("route after the local owner dropped it %v %v", r, ok)
	}
	if routes, err := d.ListRoutes(ctx); err != nil || len(routes) != 0 {
		t.Fatalf("a node held route stayed stored %v %v", routes, err)
	}
	if pool, _, err := m.Resolve(ctx, "llama"); err != nil || pool.ID != sglang {
		t.Fatalf("resolve after restoration %v %v", pool.ID, err)
	}
}

// Weighted targets skip pools without a ready healthy endpoint, and balance options scale weights by readiness or free capacity
func TestWeightedTargetsFollowHealthAndBalance(t *testing.T) {
	ctx := context.Background()
	m, _, _ := newManager(t)
	a, b := PoolIDOf(llama, "vllm", ""), PoolIDOf(llama, "sglang", "")
	if err := m.Publish(ctx, OwnerRef{Kind: OwnerInstance, ID: "a"}, membership("vllm"), []Published{published("a", 1, false), published("a2", 3, false), published("a3", 4, false)}); err != nil {
		t.Fatal(err)
	}
	if err := m.Publish(ctx, OwnerRef{Kind: OwnerInstance, ID: "b"}, membership("sglang"), []Published{published("b", 2, true)}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.PutRoute(ctx, "split", &v1.RouteSpec{Rules: []*v1.RewriteRule{rewrite("chat", to(a, "a-model", 1), to(b, "b-model", 1))}}); err != nil {
		t.Fatal(err)
	}
	if counts := drawCounts(t, m, "chat", 2000); counts[a] != 0 || counts[b] != 2000 {
		t.Fatalf("unready target drew %v", counts)
	}
	m.SetReady(ctx, "b", false)
	if counts := drawCounts(t, m, "chat", 400); counts[a] == 0 || counts[b] == 0 {
		t.Fatalf("with nothing ready the static weights decide %v", counts)
	}
	m.SetReady(ctx, "a", true)
	m.SetReady(ctx, "a2", true)
	m.SetReady(ctx, "a3", true)
	m.SetReady(ctx, "b", true)
	m.Store().SetAttribute("b/2", datalayer.CollectorHealth, &datalayer.HealthState{Healthy: false})
	if counts := drawCounts(t, m, "chat", 400); counts[b] != 0 {
		t.Fatalf("unhealthy target drew %v", counts)
	}
	m.Store().SetAttribute("b/2", datalayer.CollectorHealth, &datalayer.HealthState{Healthy: true})
	if counts := drawCounts(t, m, "chat", 4000); counts[a] < 1700 || counts[a] > 2300 {
		t.Fatalf("static weights with both ready %v", counts)
	}
	if _, err := m.PutRoute(ctx, "split", &v1.RouteSpec{Rules: []*v1.RewriteRule{rewrite("chat", to(a, "a-model", 1), to(b, "b-model", 1))}, Balance: v1.RouteBalance(9)}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown balance accepted: %v", err)
	}
	if _, err := m.PutRoute(ctx, "split", &v1.RouteSpec{Rules: []*v1.RewriteRule{rewrite("chat", to(a, "a-model", 1), to(b, "b-model", 1))}, Balance: v1.RouteBalance_ROUTE_BALANCE_READY_ENDPOINTS}); err != nil {
		t.Fatal(err)
	}
	if counts := drawCounts(t, m, "chat", 4000); counts[a] < 2700 || counts[a] > 3300 {
		t.Fatalf("ready endpoint balance three to one drew %v", counts)
	}
	for _, id := range []datalayer.EndpointID{"a/1", "a2/3", "a3/4"} {
		m.Store().SetAttribute(id, datalayer.CollectorMetrics, &datalayer.ServerMetrics{RunningRequests: 3, QueuedRequests: 1})
	}
	if _, err := m.PutRoute(ctx, "split", &v1.RouteSpec{Rules: []*v1.RewriteRule{rewrite("chat", to(a, "a-model", 1), to(b, "b-model", 1))}, Balance: v1.RouteBalance_ROUTE_BALANCE_FREE_CAPACITY}); err != nil {
		t.Fatal(err)
	}
	if counts := drawCounts(t, m, "chat", 4000); counts[b] < 2700 {
		t.Fatalf("free capacity balance with a full and an idle pool drew %v", counts)
	}
	slot := OwnerRef{Kind: OwnerSlot, ID: "s"}
	if err := m.Publish(ctx, slot, membership("trtllm"), []Published{published("s", 5, true)}); err != nil {
		t.Fatal(err)
	}
	r, err := m.NameRoute(ctx, slot, "owned", RouteSpec{})
	if err != nil {
		t.Fatal(err)
	}
	r, err = m.PutRoute(ctx, "owned", &v1.RouteSpec{Rules: r.GetSpec().GetRules(), Balance: v1.RouteBalance_ROUTE_BALANCE_READY_ENDPOINTS})
	if err != nil || r.GetSpec().GetBalance() != v1.RouteBalance_ROUTE_BALANCE_READY_ENDPOINTS {
		t.Fatalf("balance override on an owner route %v %v", r.GetSpec(), err)
	}
}

// An override a user set on an owner route survives the owner naming it again after a restart without dropping it first
func TestOverrideSurvivesRenameAcrossRestart(t *testing.T) {
	ctx := context.Background()
	m, d, bus := newManager(t)
	slot := OwnerRef{Kind: OwnerSlot, ID: "s1"}
	if err := m.Publish(ctx, slot, membership("vllm"), []Published{published("i1", 8001, true)}); err != nil {
		t.Fatal(err)
	}
	owned := RouteSpec{Policy: &v1.Policy{MaxInFlight: 2}}
	r, err := m.NameRoute(ctx, slot, "chat", owned)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.PutRoute(ctx, "chat", &v1.RouteSpec{Rules: r.GetSpec().GetRules(), Policy: &v1.Policy{MaxInFlight: 9}}); err != nil {
		t.Fatal(err)
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	again := openManager(t, d, bus)
	if r, ok := again.Route("chat"); !ok || r.GetSpec().GetPolicy().GetMaxInFlight() != 9 {
		t.Fatalf("restored route %v %v", r, ok)
	}
	if err := again.Publish(ctx, slot, membership("vllm"), nil); err != nil {
		t.Fatal(err)
	}
	r, err = again.NameRoute(ctx, slot, "chat", owned)
	if err != nil || r.GetSpec().GetPolicy().GetMaxInFlight() != 9 {
		t.Fatalf("override after the owner named its route again %v %v", r.GetSpec(), err)
	}
	if rows, err := d.ListRouteOverrides(ctx); err != nil || len(rows) != 1 {
		t.Fatalf("override rows %v %v", rows, err)
	}
	again.DropRoutes(ctx, slot)
	if rows, err := d.ListRouteOverrides(ctx); err != nil || len(rows) != 0 {
		t.Fatalf("override outlived the dropped route %v %v", rows, err)
	}
}

// An instance publishes its base endpoint and an adapter scoped copy of the same port under their own ids
func TestAdapterScopedEndpointsPublish(t *testing.T) {
	ctx := context.Background()
	m, _, _ := newManager(t)
	inst := OwnerRef{Kind: OwnerInstance, ID: "i1"}
	base := published("i1", 8001, true)
	scoped := base
	scoped.Adapter = "sql"
	if err := m.Publish(ctx, inst, membership("vllm"), []Published{base, scoped}); err != nil {
		t.Fatalf("base plus adapter scoped endpoint: %v", err)
	}
	id := PoolIDOf(llama, "vllm", "")
	eps := rendered(t, m, id)
	if len(eps) != 2 || eps["i1/8001"] == nil || eps["i1/8001/sql"] == nil || eps["i1/8001/sql"].GetLabels()[datalayer.LabelAdapter] != "sql" || eps["i1/8001/sql"].GetAddress() != eps["i1/8001"].GetAddress() {
		t.Fatalf("endpoints %v", eps)
	}
	if pool, adapter, err := m.Lookup(ctx, "sql"); err != nil || pool.ID != id || adapter != "sql" {
		t.Fatalf("lookup of the adapter %v %q %v", pool.ID, adapter, err)
	}
	m.SetReady(ctx, "i1", false)
	for _, eid := range []datalayer.EndpointID{"i1/8001", "i1/8001/sql"} {
		if ep, _ := m.Store().Endpoint(eid); ep.Ready() {
			t.Fatalf("%s stayed ready", eid)
		}
	}
	if err := m.Publish(ctx, inst, membership("vllm"), []Published{base}); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Store().Endpoint("i1/8001/sql"); ok {
		t.Fatal("unloaded adapter endpoint kept")
	}
	if err := m.Publish(ctx, inst, membership("vllm"), []Published{scoped, scoped}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("the same adapter endpoint twice: %v", err)
	}
}

// Every condition pools, routes and scheduler configs report is declared with its reasons in pkg conditions
func TestConditionSetsDeclared(t *testing.T) {
	ctx := context.Background()
	m, _, _ := newManager(t)
	inst := OwnerRef{Kind: OwnerInstance, ID: "i1"}
	if err := m.Publish(ctx, inst, membership("vllm"), []Published{published("i1", 8001, true)}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.NameRoute(ctx, inst, "llama", RouteSpec{}); err != nil {
		t.Fatal(err)
	}
	p, _ := m.Pool(string(PoolIDOf(llama, "vllm", "")))
	types := func(conds []*v1.Condition) []string {
		var out []string
		for _, c := range conds {
			out = append(out, c.GetType())
		}
		slices.Sort(out)
		return out
	}
	if got := types(p.GetStatus().GetConditions()); !slices.Equal(got, []string{ConditionAccepted, ConditionExported, ConditionReady, ConditionScraped}) {
		t.Fatalf("pool conditions %v", got)
	}
	for _, c := range p.GetStatus().GetConditions() {
		if !slices.Contains(conditions.Reasons(conditions.ResourcePool, c.GetType()), c.GetReason()) {
			t.Fatalf("pool condition %s reason %s undeclared", c.GetType(), c.GetReason())
		}
	}
	for _, parent := range p.GetStatus().GetParents() {
		if got := types(parent.GetConditions()); !slices.Equal(got, []string{ConditionAccepted, ConditionResolvedRefs}) {
			t.Fatalf("pool parent conditions %v", got)
		}
	}
	r, _ := m.Route("llama")
	if got := types(r.GetStatus().GetConditions()); !slices.Equal(got, []string{ConditionAccepted, ConditionReady, ConditionResolvedRefs}) {
		t.Fatalf("route conditions %v", got)
	}
	for _, c := range r.GetStatus().GetConditions() {
		if !slices.Contains(conditions.Reasons(conditions.ResourceRoute, c.GetType()), c.GetReason()) {
			t.Fatalf("route condition %s reason %s undeclared", c.GetType(), c.GetReason())
		}
	}
	for _, reason := range []string{ReasonAccepted, ReasonUnknownSchedulerConfig, ReasonInvalidSchedulerConfig} {
		if !slices.Contains(conditions.Reasons(conditions.ResourcePool, ConditionAccepted), reason) {
			t.Fatalf("pool accepted reason %s undeclared", reason)
		}
	}
	for _, reason := range []string{ReasonResolvedRefs, ReasonInvalidPoolRef} {
		if !slices.Contains(conditions.Reasons(conditions.ResourceRoute, ConditionResolvedRefs), reason) {
			t.Fatalf("route resolved refs reason %s undeclared", reason)
		}
	}
	if !slices.Contains(conditions.Reasons(conditions.ResourceRoute, ConditionAccepted), ReasonStoreFailed) || !slices.Contains(conditions.Reasons(conditions.ResourceSchedulerConfig, ConditionAccepted), ReasonInvalid) {
		t.Fatal("store failed or invalid reasons undeclared")
	}
}

// A pool deleted by its last owner's unpublish leaves with the gateway's status entry removed
func TestDeletedPoolDropsGatewayEntry(t *testing.T) {
	ctx := context.Background()
	m, _, bus := newManager(t)
	rec := record(t, bus)
	inst := OwnerRef{Kind: OwnerInstance, ID: "i1"}
	if err := m.Publish(ctx, inst, membership("vllm"), []Published{published("i1", 8001, true)}); err != nil {
		t.Fatal(err)
	}
	id := string(PoolIDOf(llama, "vllm", ""))
	if p, _ := m.Pool(id); len(p.GetStatus().GetParents()) != 1 || p.GetStatus().GetParents()[0].GetControllerName() != GatewayController {
		t.Fatalf("live pool parents %v", p.GetStatus().GetParents())
	}
	m.Unpublish(ctx, inst)
	waitUntil(t, "the pool deletion event", func() bool {
		return rec.count(v1.EventKind_EVENT_KIND_POOL, v1.EventAction_EVENT_ACTION_DELETED, id) == 1
	})
	ev := rec.last(v1.EventKind_EVENT_KIND_POOL, id)
	if ev.GetAction() != v1.EventAction_EVENT_ACTION_DELETED || len(ev.GetPool().GetStatus().GetParents()) != 0 || len(ev.GetPool().GetStatus().GetConditions()) == 0 {
		t.Fatalf("deletion record %v", ev.GetPool())
	}
}

// The route view hands out the held record itself, a clone only coming from Route
func TestRouteViewSharesRecord(t *testing.T) {
	ctx := context.Background()
	m, _, _ := newManager(t)
	inst := OwnerRef{Kind: OwnerInstance, ID: "i1"}
	if err := m.Publish(ctx, inst, membership("vllm"), []Published{published("i1", 8001, true)}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.NameRoute(ctx, inst, "llama", RouteSpec{Policy: &v1.Policy{MaxInFlight: 2}}); err != nil {
		t.Fatal(err)
	}
	a, ok := m.RouteView("llama")
	b, _ := m.RouteView("llama")
	cloned, _ := m.Route("llama")
	if !ok || a != b || a == cloned || !proto.Equal(a, cloned) || a.GetSpec().GetPolicy().GetMaxInFlight() != 2 {
		t.Fatalf("route view %v %v", a, ok)
	}
	if _, ok := m.RouteView("nope"); ok {
		t.Fatal("unknown route found")
	}
	if _, err := m.NameRoute(ctx, inst, "llama", RouteSpec{Policy: &v1.Policy{MaxInFlight: 5}}); err != nil {
		t.Fatal(err)
	}
	c, _ := m.RouteView("llama")
	if c == a || a.GetSpec().GetPolicy().GetMaxInFlight() != 2 || c.GetSpec().GetPolicy().GetMaxInFlight() != 5 {
		t.Fatal("a change wrote into the record readers hold")
	}
}

// A route drop the database refuses keeps the route out of resolution with a StoreFailed condition and finishes once the database allows it
func TestRouteDropRetriesRefusedDelete(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "nebu.db")
	d, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	m := openManager(t, d, events.New())
	inst := OwnerRef{Kind: OwnerInstance, ID: "i1"}
	if err := m.Publish(ctx, inst, membership("vllm"), []Published{published("i1", 8001, true)}); err != nil {
		t.Fatal(err)
	}
	r, err := m.NameRoute(ctx, inst, "llama", RouteSpec{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.PutRoute(ctx, "llama", &v1.RouteSpec{Rules: r.GetSpec().GetRules(), Policy: &v1.Policy{MaxInFlight: 9}}); err != nil {
		t.Fatal(err)
	}
	lock, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(100)")
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	tx, err := lock.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, "UPDATE routes SET name = name WHERE 0"); err != nil {
		t.Fatal(err)
	}
	m.DropRoute(ctx, inst, "llama")
	r, ok := m.Route("llama")
	if !ok {
		t.Fatal("route dropped from memory while the database refused")
	}
	if c := condition(r.GetStatus().GetConditions(), ConditionAccepted); c.GetStatus() != v1.ConditionStatus_CONDITION_STATUS_FALSE || c.GetReason() != ReasonStoreFailed {
		t.Fatalf("refused drop condition %v", c)
	}
	if _, _, err := m.Resolve(ctx, "llama"); !errors.Is(err, requestcontrol.ErrUnknownModel) {
		t.Fatalf("a dropped route still resolved: %v", err)
	}
	if _, err := m.NameRoute(ctx, inst, "llama", RouteSpec{}); !errors.Is(err, ErrConflict) {
		t.Fatalf("naming a route the database holds onto: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	m.SetReady(ctx, "i1", false)
	if _, ok := m.Route("llama"); ok {
		t.Fatal("route kept after the database allowed the delete")
	}
	routes, err := d.ListRoutes(ctx)
	rows, rerr := d.ListRouteOverrides(ctx)
	if err != nil || rerr != nil || len(routes) != 0 || len(rows) != 0 {
		t.Fatalf("stored rows after the retried delete %v %v %v %v", routes, rows, err, rerr)
	}
}

// A data source that fails to start a given number of times, counting its starts
type flakySource struct {
	fails  int32
	starts atomic.Int32
}

func (s *flakySource) Type() string                                 { return "flaky" }
func (s *flakySource) Stop() error                                  { return nil }
func (s *flakySource) Subscribe(datalayer.Collector) error          { return nil }
func (s *flakySource) UpdateEndpoints([]datalayer.EndpointID) error { return nil }
func (s *flakySource) Start(context.Context) error {
	if n := s.starts.Add(1); int32(n) <= s.fails {
		return errors.New("flaky start")
	}
	return nil
}

// Data sources that stop with an error are started again until they run
func TestSourcesRestartAfterFailure(t *testing.T) {
	m, _, _ := newManager(t)
	src := &flakySource{fails: 2}
	sources := datalayer.Sources{}
	if err := sources.Register(src); err != nil {
		t.Fatal(err)
	}
	m.runner = datalayer.NewRunner(m.store, sources)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the sources to run after two failed starts", func() bool { return src.starts.Load() == 3 })
	time.Sleep(sourceRetryMin * 3)
	if src.starts.Load() != 3 {
		t.Fatalf("a running source was started again, %d starts", src.starts.Load())
	}
	cancel()
	if err := m.Close(); err != nil && !strings.Contains(err.Error(), "context canceled") {
		t.Fatalf("close: %v", err)
	}
}
