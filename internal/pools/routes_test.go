package pools

import (
	"context"
	"errors"
	"math"
	"path/filepath"
	"testing"
	"time"

	"github.com/nickheyer/nebu/internal/db"
	"github.com/nickheyer/nebu/pkg/datalayer"
	"github.com/nickheyer/nebu/pkg/events"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/requestcontrol"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func rewrite(match string, targets ...*v1.RewriteTarget) *v1.RewriteRule {
	return &v1.RewriteRule{Match: match, Targets: targets}
}

func to(pool datalayer.PoolID, model string, weight uint32) *v1.RewriteTarget {
	return &v1.RewriteTarget{PoolId: string(pool), Model: model, Weight: weight}
}

func TestResolvePrecedenceAndLookup(t *testing.T) {
	ctx := context.Background()
	m, _, _ := newManager(t)
	vllm, sglang := PoolIDOf(llama, "vllm", ""), PoolIDOf(llama, "sglang", "")
	inst := OwnerRef{Kind: OwnerInstance, ID: "i1"}
	adapterEP := published("i1", 8001, true)
	adapterEP.Adapter = "sql-lora"
	if err := m.Publish(ctx, inst, membership("vllm"), []Published{adapterEP}); err != nil {
		t.Fatal(err)
	}
	if err := m.Publish(ctx, OwnerRef{Kind: OwnerInstance, ID: "i2"}, membership("sglang"), []Published{published("i2", 8002, true)}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.NameRoute(ctx, inst, "llama", RouteSpec{ServedName: "served-llama"}); err != nil {
		t.Fatal(err)
	}
	pool, model, err := m.Resolve(ctx, "llama")
	if err != nil || pool.ID != vllm || model != "served-llama" {
		t.Fatalf("owner route %v %q %v", pool.ID, model, err)
	}
	if pool, adapter, err := m.Lookup(ctx, "served-llama"); err != nil || pool.ID != vllm || adapter != "" {
		t.Fatalf("lookup served name %v %q %v", pool.ID, adapter, err)
	}
	if pool, adapter, err := m.Lookup(ctx, "sql-lora"); err != nil || pool.ID != vllm || adapter != "sql-lora" {
		t.Fatalf("lookup adapter %v %q %v", pool.ID, adapter, err)
	}
	if _, _, err := m.Resolve(ctx, ""); !errors.Is(err, requestcontrol.ErrNoModel) {
		t.Fatalf("unnamed without catch all: %v", err)
	}
	if _, _, ok := m.Default(ctx); ok {
		t.Fatal("default with two ready pools")
	}
	if _, err := m.PutRoute(ctx, "fallback", &v1.RouteSpec{Rules: []*v1.RewriteRule{rewrite("", to(sglang, "", 1))}}); err != nil {
		t.Fatal(err)
	}
	if pool, _, err := m.Resolve(ctx, "llama"); err != nil || pool.ID != vllm {
		t.Fatalf("catch all beat an owner route %v %v", pool.ID, err)
	}
	if pool, model, err := m.Resolve(ctx, "anything"); err != nil || pool.ID != sglang || model != llama.String() {
		t.Fatalf("catch all %v %q %v", pool.ID, model, err)
	}
	if _, err := m.PutRoute(ctx, "override", &v1.RouteSpec{Rules: []*v1.RewriteRule{rewrite("llama", to(sglang, "big", 1))}}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.PutRoute(ctx, "later", &v1.RouteSpec{Rules: []*v1.RewriteRule{rewrite("llama", to(vllm, "late", 1))}}); err != nil {
		t.Fatal(err)
	}
	if pool, model, err := m.Resolve(ctx, "llama"); err != nil || pool.ID != sglang || model != "big" {
		t.Fatalf("oldest user exact rule %v %q %v", pool.ID, model, err)
	}
	if pool, _, err := m.Lookup(ctx, "big"); err != nil || pool.ID != sglang {
		t.Fatalf("lookup of a user target model %v %v", pool.ID, err)
	}
	if _, err := m.DeleteRoute(ctx, "fallback"); err != nil {
		t.Fatal(err)
	}
	if pool, model, err := m.Resolve(ctx, llama.String()); err != nil || model != llama.String() || (pool.ID != vllm && pool.ID != sglang) {
		t.Fatalf("pool model without a route %v %q %v", pool.ID, model, err)
	}
	if _, _, err := m.Resolve(ctx, "nobody"); !errors.Is(err, requestcontrol.ErrUnknownModel) {
		t.Fatalf("unknown model: %v", err)
	}
	if _, err := m.DeleteRoute(ctx, "llama"); !errors.Is(err, ErrManaged) {
		t.Fatalf("owner route deleted: %v", err)
	}
	if _, err := m.PutRoute(ctx, "llama", &v1.RouteSpec{Rules: []*v1.RewriteRule{rewrite("x", to(vllm, "", 1))}}); !errors.Is(err, ErrManaged) {
		t.Fatalf("owner route replaced: %v", err)
	}
	m.SetReady(ctx, "i2", false)
	if pool, model, ok := m.Default(ctx); !ok || pool.ID != vllm || model != llama.String() {
		t.Fatalf("default with one ready pool %v %q %v", pool.ID, model, ok)
	}
}

// User rewrite rules resolve exact over catch all, then the oldest route, then the order routes list in
func TestRewritePrecedence(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	later := t0.Add(time.Hour)
	type stored struct {
		name    string
		created time.Time
		match   string
		model   string
	}
	cases := []struct {
		name   string
		routes []stored
		client string
		want   string
	}{
		{"exact over an older catch all", []stored{{"all", t0, "", "caught"}, {"pub", later, "public", "exact"}}, "public", "exact"},
		{"catch all for other names", []stored{{"all", t0, "", "caught"}, {"pub", later, "public", "exact"}}, "other", "caught"},
		{"oldest exact rule", []stored{{"young", later, "public", "young"}, {"old", t0, "public", "old"}}, "public", "old"},
		{"listed order at equal age", []stored{{"b", t0, "public", "second"}, {"a", t0, "public", "first"}}, "public", "first"},
		{"oldest catch all", []stored{{"x", later, "", "young"}, {"y", t0, "", "old"}}, "other", "old"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx := context.Background()
			d, err := db.Open(filepath.Join(t.TempDir(), "nebu.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { d.Close() })
			pool := PoolIDOf(llama, "vllm", "")
			for _, r := range c.routes {
				spec := &v1.RouteSpec{Rules: []*v1.RewriteRule{rewrite(r.match, to(pool, r.model, 1))}}
				if err := d.PutRoute(ctx, &v1.Route{Name: r.name, Meta: &v1.ObjectMeta{CreatedAt: timestamppb.New(r.created), Generation: 1}, Spec: spec}); err != nil {
					t.Fatal(err)
				}
			}
			m := openManager(t, d, events.New())
			if err := m.Publish(ctx, OwnerRef{Kind: OwnerInstance, ID: "i1"}, membership("vllm"), []Published{published("i1", 1, true)}); err != nil {
				t.Fatal(err)
			}
			for range 50 {
				if got, model, err := m.Resolve(ctx, c.client); err != nil || got.ID != pool || model != c.want {
					t.Fatalf("Resolve(%q) = %s %q %v, want %q", c.client, got.ID, model, err, c.want)
				}
			}
		})
	}
}

func TestWeightedChoice(t *testing.T) {
	ctx := context.Background()
	m, _, _ := newManager(t)
	a, b := PoolIDOf(llama, "vllm", ""), PoolIDOf(llama, "sglang", "")
	if err := m.Publish(ctx, OwnerRef{Kind: OwnerInstance, ID: "a"}, membership("vllm"), []Published{published("a", 1, true)}); err != nil {
		t.Fatal(err)
	}
	if err := m.Publish(ctx, OwnerRef{Kind: OwnerInstance, ID: "b"}, membership("sglang"), []Published{published("b", 2, true)}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.PutRoute(ctx, "split", &v1.RouteSpec{Rules: []*v1.RewriteRule{rewrite("chat", to(a, "a-model", 1), to(b, "b-model", 3))}}); err != nil {
		t.Fatal(err)
	}
	const n = 8000
	counts := map[datalayer.PoolID]int{}
	for range n {
		pool, model, err := m.Resolve(ctx, "chat")
		if err != nil {
			t.Fatal(err)
		}
		if (pool.ID == a) != (model == "a-model") {
			t.Fatalf("pool %s with model %s", pool.ID, model)
		}
		counts[pool.ID]++
	}
	if share := float64(counts[b]) / n; math.Abs(share-0.75) > 0.03 {
		t.Fatalf("weight three of four got share %.3f", share)
	}
}

func TestSplitRoute(t *testing.T) {
	ctx := context.Background()
	m, _, _ := newManager(t)
	slot := OwnerRef{Kind: OwnerSlot, ID: "s1"}
	if err := m.Publish(ctx, slot, membership("vllm"), []Published{published("i1", 1, true)}); err != nil {
		t.Fatal(err)
	}
	if err := m.Publish(ctx, OwnerRef{Kind: OwnerInstance, ID: "next"}, membership("sglang"), []Published{published("next", 2, true)}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.NameRoute(ctx, slot, "chat", RouteSpec{}); err != nil {
		t.Fatal(err)
	}
	if err := m.SplitRoute(ctx, slot, "chat", []WeightedTarget{{Pool: PoolIDOf(llama, "sglang", ""), Model: "next", Weight: 1}}); err != nil {
		t.Fatal(err)
	}
	if pool, model, err := m.Resolve(ctx, "chat"); err != nil || pool.ID != PoolIDOf(llama, "sglang", "") || model != "next" {
		t.Fatalf("split route %v %q %v", pool.ID, model, err)
	}
	r, _ := m.Route("chat")
	if r.GetMeta().GetGeneration() != 2 || r.GetStatus().GetObservedGeneration() != 2 {
		t.Fatalf("split generation %v", r.GetMeta())
	}
	if err := m.SplitRoute(ctx, slot, "chat", nil); err != nil {
		t.Fatal(err)
	}
	if r, _ := m.Route("chat"); r.GetStatus().GetState() != v1.RouteState_ROUTE_STATE_PENDING {
		t.Fatalf("route split to nothing %v", r.GetStatus())
	}
	if _, _, err := m.Resolve(ctx, "chat"); !errors.Is(err, requestcontrol.ErrNoReadyEndpoints) {
		t.Fatalf("route without targets: %v", err)
	}
	if err := m.SplitRoute(ctx, OwnerRef{Kind: OwnerSlot, ID: "other"}, "chat", nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("split by another owner: %v", err)
	}
	for _, bad := range [][]WeightedTarget{{{Pool: "", Weight: 1}}, {{Pool: "p", Weight: maxWeight + 1}}} {
		if err := m.SplitRoute(ctx, slot, "chat", bad); !errors.Is(err, ErrInvalid) {
			t.Fatalf("split %v: %v", bad, err)
		}
	}
	vllm, sglang := PoolIDOf(llama, "vllm", ""), PoolIDOf(llama, "sglang", "")
	for _, c := range []struct {
		weights [2]uint32
		want    datalayer.PoolID
		targets int
	}{{[2]uint32{0, 100}, sglang, 1}, {[2]uint32{100, 0}, vllm, 1}, {[2]uint32{0, 0}, "", 0}} {
		if err := m.SplitRoute(ctx, slot, "chat", []WeightedTarget{{Pool: vllm, Model: "old", Weight: c.weights[0]}, {Pool: sglang, Model: "next", Weight: c.weights[1]}}); err != nil {
			t.Fatal(err)
		}
		r, _ := m.Route("chat")
		if got := len(r.GetSpec().GetRules()[0].GetTargets()); got != c.targets {
			t.Fatalf("weights %v kept %d targets", c.weights, got)
		}
		for range 200 {
			pool, _, err := m.Resolve(ctx, "chat")
			if c.want == "" {
				if !errors.Is(err, requestcontrol.ErrNoReadyEndpoints) {
					t.Fatalf("weights %v resolved %v %v", c.weights, pool.ID, err)
				}
				continue
			}
			if err != nil || pool.ID != c.want {
				t.Fatalf("weights %v resolved %v %v, want %s", c.weights, pool.ID, err, c.want)
			}
		}
	}
	if _, err := m.NameRoute(ctx, slot, "chat", RouteSpec{}); err != nil {
		t.Fatal(err)
	}
	if pool, _, err := m.Resolve(ctx, "chat"); err != nil || pool.ID != PoolIDOf(llama, "vllm", "") {
		t.Fatalf("named again %v %v", pool.ID, err)
	}
}

func TestResolveWakesSlotAcrossRestart(t *testing.T) {
	ctx := context.Background()
	m, d, bus := newManager(t)
	slot := OwnerRef{Kind: OwnerSlot, ID: "s1"}
	inst := OwnerRef{Kind: OwnerInstance, ID: "i9"}
	if err := m.Publish(ctx, slot, membership("vllm"), []Published{published("i1", 8001, true)}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.NameRoute(ctx, slot, "chat", RouteSpec{}); err != nil {
		t.Fatal(err)
	}
	if err := m.Publish(ctx, inst, membership("sglang"), []Published{published("i9", 8009, true)}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.NameRoute(ctx, inst, "solo", RouteSpec{}); err != nil {
		t.Fatal(err)
	}
	m.Unpublish(ctx, slot)
	m.Unpublish(ctx, inst)
	if _, ok := m.Pool(string(PoolIDOf(llama, "vllm", ""))); !ok {
		t.Fatal("pool of an owner still naming routes dropped")
	}
	if _, _, err := m.Resolve(ctx, "chat"); !errors.Is(err, requestcontrol.ErrNoReadyEndpoints) {
		t.Fatalf("empty slot pool without a waker: %v", err)
	}
	w := &wakeLog{}
	m.SetWaker(w)
	pool, _, err := m.Resolve(ctx, "chat")
	if err != nil || pool.ID != PoolIDOf(llama, "vllm", "") || w.count() != 1 || w.woken[0] != slot {
		t.Fatalf("wake %v %v %v", pool.ID, err, w.woken)
	}
	if pool, _, err := m.Resolve(ctx, "solo"); err != nil || pool.ID != PoolIDOf(llama, "sglang", "") || w.count() != 2 || w.woken[1] != inst {
		t.Fatalf("instance pool not woken %v %v %v", pool.ID, err, w.woken)
	}
	if !m.Waking(PoolIDOf(llama, "sglang", "")) || !m.Waking(PoolIDOf(llama, "vllm", "")) {
		t.Fatal("woken pools not marked waking")
	}
	if pool, adapter, err := m.Lookup(ctx, "chat"); err != nil || pool.ID != PoolIDOf(llama, "vllm", "") || adapter != "" || w.count() != 3 {
		t.Fatalf("lookup of an empty slot pool %v %q %v %d", pool.ID, adapter, err, w.count())
	}
	w.err = errors.New("slot disabled")
	if _, _, err := m.Resolve(ctx, "chat"); !errors.Is(err, requestcontrol.ErrNoReadyEndpoints) {
		t.Fatalf("failed wake: %v", err)
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}

	again := openManager(t, d, bus)
	r, ok := again.Route("chat")
	if !ok || r.GetStatus().GetState() != v1.RouteState_ROUTE_STATE_PENDING || r.GetOwnerKind() != OwnerSlot {
		t.Fatalf("restored slot route %v", r)
	}
	if _, ok := again.Route("solo"); !ok {
		t.Fatal("instance route not restored")
	}
	w2 := &wakeLog{}
	again.SetWaker(w2)
	if pool, model, err := again.Resolve(ctx, "chat"); err != nil || pool.ID != PoolIDOf(llama, "vllm", "") || model != "chat" || w2.count() != 1 {
		t.Fatalf("restored wake %v %q %v %d", pool.ID, model, err, w2.count())
	}
	if err := again.Publish(ctx, slot, membership("vllm"), []Published{published("i2", 8001, true)}); err != nil {
		t.Fatal(err)
	}
	if r, _ := again.Route("chat"); r.GetStatus().GetState() != v1.RouteState_ROUTE_STATE_READY {
		t.Fatalf("route after the slot came back %v", r.GetStatus())
	}
	again.DropRoutes(ctx, inst)
	if _, ok := again.Pool(string(PoolIDOf(llama, "sglang", ""))); ok {
		t.Fatal("pool of a pruned instance kept")
	}
	routes, err := d.ListRoutes(ctx)
	if err != nil || len(routes) != 1 || routes[0].GetName() != "chat" {
		t.Fatalf("stored routes %v %v", routes, err)
	}
}

func TestUserRouteValidation(t *testing.T) {
	ctx := context.Background()
	m, _, _ := newManager(t)
	pool := PoolIDOf(llama, "vllm", "")
	if err := m.Publish(ctx, OwnerRef{Kind: OwnerInstance, ID: "i"}, membership("vllm"), nil); err != nil {
		t.Fatal(err)
	}
	cases := map[string]*v1.RouteSpec{
		"no rules":           {},
		"missing pool":       {Rules: []*v1.RewriteRule{rewrite("a", to("nope", "", 1))}},
		"zero weight":        {Rules: []*v1.RewriteRule{rewrite("a", to(pool, "", 0))}},
		"heavy weight":       {Rules: []*v1.RewriteRule{rewrite("a", to(pool, "", 1000001))}},
		"no targets":         {Rules: []*v1.RewriteRule{rewrite("a")}},
		"catch all not last": {Rules: []*v1.RewriteRule{rewrite("", to(pool, "", 1)), rewrite("a", to(pool, "", 1))}},
		"two catch alls":     {Rules: []*v1.RewriteRule{rewrite("", to(pool, "", 1)), rewrite("", to(pool, "", 1))}},
		"repeated match":     {Rules: []*v1.RewriteRule{rewrite("a", to(pool, "", 1)), rewrite("a", to(pool, "", 1))}},
		"unknown objective":  {Rules: []*v1.RewriteRule{rewrite("a", to(pool, "", 1))}, Objective: "fast"},
		"negative rate":      {Rules: []*v1.RewriteRule{rewrite("a", to(pool, "", 1))}, Policy: &v1.Policy{RequestsPerSecond: -1}},
	}
	for name, spec := range cases {
		if _, err := m.PutRoute(ctx, "r", spec); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if _, err := m.PutRoute(ctx, "", &v1.RouteSpec{Rules: []*v1.RewriteRule{rewrite("a", to(pool, "", 1))}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unnamed route: %v", err)
	}
	ok := &v1.RouteSpec{Rules: []*v1.RewriteRule{rewrite("a", to(pool, "", 1000000)), rewrite("", to(pool, "", 1))}}
	r, err := m.PutRoute(ctx, "r", ok)
	if err != nil || r.GetMeta().GetGeneration() != 1 {
		t.Fatalf("valid route %v %v", r, err)
	}
	if r, _ := m.PutRoute(ctx, "r", ok); r.GetMeta().GetGeneration() != 1 {
		t.Fatalf("unchanged spec bumped generation %v", r.GetMeta())
	}
	m.Unpublish(ctx, OwnerRef{Kind: OwnerInstance, ID: "i"})
	r, _ = m.Route("r")
	if c := condition(r.GetStatus().GetConditions(), ConditionResolvedRefs); c.GetStatus() != v1.ConditionStatus_CONDITION_STATUS_FALSE || c.GetReason() != ReasonInvalidPoolRef {
		t.Fatalf("dangling target %v", c)
	}
	if len(r.GetStatus().GetPoolIds()) != 0 {
		t.Fatalf("pool ids of a dangling route %v", r.GetStatus().GetPoolIds())
	}
	if _, _, err := m.Resolve(ctx, "a"); !errors.Is(err, requestcontrol.ErrNoReadyEndpoints) {
		t.Fatalf("resolving a dangling target: %v", err)
	}
}

func TestOwnerRouteOverride(t *testing.T) {
	ctx := context.Background()
	m, d, bus := newManager(t)
	slot := OwnerRef{Kind: OwnerSlot, ID: "s1"}
	if _, err := m.PutObjective(ctx, &v1.Objective{Name: "fast", Priority: 5}); err != nil {
		t.Fatal(err)
	}
	if err := m.Publish(ctx, slot, membership("vllm"), []Published{published("i1", 8001, true)}); err != nil {
		t.Fatal(err)
	}
	owned := RouteSpec{Policy: &v1.Policy{MaxInFlight: 2}}
	r, err := m.NameRoute(ctx, slot, "chat", owned)
	if err != nil {
		t.Fatal(err)
	}
	rules := r.GetSpec().GetRules()
	if _, err := m.PutRoute(ctx, "chat", &v1.RouteSpec{Rules: []*v1.RewriteRule{rewrite("chat", to(PoolIDOf(llama, "vllm", ""), "other", 1))}}); !errors.Is(err, ErrManaged) {
		t.Fatalf("rule change on an owner route: %v", err)
	}
	if _, err := m.PutRoute(ctx, "chat", &v1.RouteSpec{Rules: rules, Objective: "missing"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("override naming a missing objective: %v", err)
	}
	user := &v1.RouteSpec{Rules: rules, Policy: &v1.Policy{MaxInFlight: 9, RequestsPerSecond: 3}, Profile: &v1.Profile{ToolServers: []string{"web"}}, Objective: "fast"}
	r, err = m.PutRoute(ctx, "chat", user)
	if err != nil {
		t.Fatal(err)
	}
	if r.GetSpec().GetPolicy().GetMaxInFlight() != 9 || r.GetSpec().GetObjective() != "fast" || r.GetMeta().GetGeneration() != 2 || r.GetOwnerKind() != OwnerSlot {
		t.Fatalf("overridden route %v", r)
	}
	if _, err := m.DeleteObjective(ctx, "fast"); !errors.Is(err, ErrInUse) {
		t.Fatalf("delete of an objective an override names: %v", err)
	}
	if r, _ := m.NameRoute(ctx, slot, "chat", owned); r.GetSpec().GetPolicy().GetMaxInFlight() != 9 || r.GetMeta().GetGeneration() != 2 {
		t.Fatalf("naming again dropped the override %v", r)
	}
	if err := m.Publish(ctx, slot, membership("vllm"), []Published{published("i2", 8001, true)}); err != nil {
		t.Fatal(err)
	}
	if r, _ := m.Route("chat"); r.GetSpec().GetProfile().GetToolServers()[0] != "web" {
		t.Fatalf("publishing again dropped the override %v", r.GetSpec())
	}
	if _, err := m.DeleteRoute(ctx, "chat"); !errors.Is(err, ErrManaged) {
		t.Fatalf("delete of an owner route: %v", err)
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}

	again := openManager(t, d, bus)
	if err := again.Publish(ctx, slot, membership("vllm"), []Published{published("i2", 8001, true)}); err != nil {
		t.Fatal(err)
	}
	r, err = again.NameRoute(ctx, slot, "chat", owned)
	if err != nil || r.GetSpec().GetPolicy().GetMaxInFlight() != 9 || r.GetSpec().GetObjective() != "fast" {
		t.Fatalf("override across a restart %v %v", r, err)
	}
	r, err = again.PutRoute(ctx, "chat", &v1.RouteSpec{Rules: rules, Policy: &v1.Policy{MaxInFlight: 2}, Profile: &v1.Profile{}})
	if err != nil || r.GetSpec().GetPolicy().GetMaxInFlight() != 2 || r.GetSpec().GetObjective() != "" {
		t.Fatalf("override matching the owner's fields %v %v", r, err)
	}
	if rows, err := d.ListRouteOverrides(ctx); err != nil || len(rows) != 0 {
		t.Fatalf("override kept once it matched the owner %v %v", rows, err)
	}
	if r, _ := again.NameRoute(ctx, slot, "chat", RouteSpec{Policy: &v1.Policy{MaxInFlight: 4}}); r.GetSpec().GetPolicy().GetMaxInFlight() != 4 {
		t.Fatalf("owner fields after the override cleared %v", r.GetSpec())
	}
	if _, err := again.PutRoute(ctx, "chat", &v1.RouteSpec{Rules: rules, Objective: "fast"}); err != nil {
		t.Fatal(err)
	}
	again.DropRoute(ctx, slot, "chat")
	if rows, err := d.ListRouteOverrides(ctx); err != nil || len(rows) != 0 {
		t.Fatalf("override outlived its route %v %v", rows, err)
	}
	remote := OwnerRef{Kind: OwnerNode, ID: "n2/" + string(PoolIDOf(llama, "vllm", ""))}
	if err := again.Publish(ctx, remote, membership("vllm"), nil); err != nil {
		t.Fatal(err)
	}
	r, err = again.NameRoute(ctx, remote, "imported", RouteSpec{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := again.PutRoute(ctx, "imported", &v1.RouteSpec{Rules: r.GetSpec().GetRules(), Objective: "fast"}); !errors.Is(err, ErrManaged) {
		t.Fatalf("override of an imported route: %v", err)
	}
}
