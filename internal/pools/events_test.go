package pools

import (
	"context"
	"testing"
	"time"

	"github.com/nickheyer/nebu/pkg/datalayer"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/scheduling/config"
)

func TestEventsEmitted(t *testing.T) {
	ctx := context.Background()
	m, _, bus := newManager(t)
	rec := record(t, bus)
	inst := OwnerRef{Kind: OwnerInstance, ID: "i1"}
	id := string(PoolIDOf(llama, "vllm", ""))
	if err := m.Publish(ctx, inst, membership("vllm"), []Published{published("i1", 8001, false)}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.NameRoute(ctx, inst, "chat", RouteSpec{}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.PutObjective(ctx, &v1.Objective{Name: "fast"}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.PutSchedulerConfig(ctx, &v1.SchedulerConfig{Name: "mine", Spec: &v1.SchedulerConfigSpec{Yaml: config.DefaultConfig}}); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "creation events", func() bool {
		return rec.count(v1.EventKind_EVENT_KIND_POOL, v1.EventAction_EVENT_ACTION_CREATED, id) == 1 &&
			rec.count(v1.EventKind_EVENT_KIND_ROUTE, v1.EventAction_EVENT_ACTION_CREATED, "chat") == 1 &&
			rec.count(v1.EventKind_EVENT_KIND_OBJECTIVE, v1.EventAction_EVENT_ACTION_CREATED, "fast") == 1 &&
			rec.count(v1.EventKind_EVENT_KIND_SCHEDULER_CONFIG, v1.EventAction_EVENT_ACTION_CREATED, "mine") == 1
	})
	if ev := rec.last(v1.EventKind_EVENT_KIND_POOL, id); ev.GetPool().GetId() != id || len(ev.GetPool().GetStatus().GetEndpoints()) != 1 {
		t.Fatalf("pool event record %v", ev)
	}

	time.Sleep(coalesceWindow)
	before := rec.count(v1.EventKind_EVENT_KIND_POOL, v1.EventAction_EVENT_ACTION_UPDATED, id)
	start := time.Now()
	for i := range 20 {
		m.SetReady(ctx, "i1", i%2 == 0)
	}
	flips := time.Since(start)
	waitUntil(t, "the trailing pool event", func() bool {
		ev := rec.last(v1.EventKind_EVENT_KIND_POOL, id)
		return ev.GetAction() == v1.EventAction_EVENT_ACTION_UPDATED && ev.GetPool().GetStatus().GetReadyEndpoints() == 0
	})
	time.Sleep(coalesceWindow)
	got := rec.count(v1.EventKind_EVENT_KIND_POOL, v1.EventAction_EVENT_ACTION_UPDATED, id) - before
	if limit := 2 + int(flips/coalesceWindow); got < 1 || got > limit {
		t.Fatalf("%d pool events for 20 readiness flips in %v, at most %d allowed", got, flips, limit)
	}
	if ev := rec.last(v1.EventKind_EVENT_KIND_ROUTE, "chat"); ev.GetRoute().GetStatus().GetState() != v1.RouteState_ROUTE_STATE_PENDING {
		t.Fatalf("route event carries %v", ev.GetRoute().GetStatus())
	}

	m.DropRoutes(ctx, inst)
	m.Unpublish(ctx, inst)
	if _, err := m.DeleteObjective(ctx, "fast"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.DeleteSchedulerConfig(ctx, "mine"); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "deletion events", func() bool {
		return rec.count(v1.EventKind_EVENT_KIND_POOL, v1.EventAction_EVENT_ACTION_DELETED, id) == 1 &&
			rec.count(v1.EventKind_EVENT_KIND_ROUTE, v1.EventAction_EVENT_ACTION_DELETED, "chat") == 1 &&
			rec.count(v1.EventKind_EVENT_KIND_OBJECTIVE, v1.EventAction_EVENT_ACTION_DELETED, "fast") == 1 &&
			rec.count(v1.EventKind_EVENT_KIND_SCHEDULER_CONFIG, v1.EventAction_EVENT_ACTION_DELETED, "mine") == 1
	})
	time.Sleep(2 * coalesceWindow)
	if ev := rec.last(v1.EventKind_EVENT_KIND_POOL, id); ev.GetAction() != v1.EventAction_EVENT_ACTION_DELETED {
		t.Fatalf("event after deletion %v", ev)
	}
}

func TestSourcesSampleThroughPorts(t *testing.T) {
	ctx := context.Background()
	m, _, bus := newManager(t)
	rec := record(t, bus)
	served, servedAddr := stubServer(t, "served-llama")
	_, sideAddr := stubServer(t, "side")
	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err := m.Start(ctx); err == nil {
		t.Fatal("second start accepted")
	}
	inst := OwnerRef{Kind: OwnerInstance, ID: "i1"}
	ep := Published{InstanceID: "i1", NodeID: node, Address: servedAddr, Ready: true, MetricsPort: uint32(sideAddr.Port)}
	if err := m.Publish(ctx, inst, membership("vllm"), []Published{ep}); err != nil {
		t.Fatal(err)
	}
	id := PoolIDOf(llama, "vllm", "")
	waitUntil(t, "metrics from the metrics port and a catalog", func() bool {
		e, ok := m.Store().Endpoint(datalayer.EndpointID("i1/" + itoa(servedAddr.Port)))
		if !ok {
			return false
		}
		_, metrics := datalayer.Attribute[*datalayer.ServerMetrics](e, datalayer.CollectorMetrics)
		c, catalog := datalayer.Attribute[*datalayer.Catalog](e, datalayer.CollectorCatalog)
		return metrics && catalog && len(c.Models) > 0
	})
	if pool, adapter, err := m.Lookup(ctx, "served-llama"); err != nil || pool.ID != id || adapter != "" {
		t.Fatalf("lookup of a catalog name %v %q %v", pool.ID, adapter, err)
	}
	p, _ := m.Pool(string(id))
	if got := p.GetStatus().GetEndpoints()[0]; got.GetMetrics() == nil || got.GetMetrics().GetNumBlocks() == 0 {
		t.Fatalf("rendered metrics %v", got.GetMetrics())
	}

	served.Pause()
	waitUntil(t, "the probe to mark the endpoint unhealthy", func() bool {
		p, _ := m.Pool(string(id))
		return p.GetStatus().GetReadyEndpoints() == 0 && !p.GetStatus().GetEndpoints()[0].GetHealthy()
	})
	waitUntil(t, "an unhealthy pool event", func() bool {
		ev := rec.last(v1.EventKind_EVENT_KIND_POOL, string(id))
		return ev.GetPool().GetStatus().GetReadyEndpoints() == 0
	})
	if r := condition(p.GetStatus().GetConditions(), ConditionReady); r.GetStatus() != v1.ConditionStatus_CONDITION_STATUS_TRUE {
		t.Fatalf("ready before the pause %v", r)
	}
	served.Resume()
	waitUntil(t, "the probe to mark the endpoint healthy again", func() bool {
		p, _ := m.Pool(string(id))
		return p.GetStatus().GetReadyEndpoints() == 1
	})

	healthOnly := Published{InstanceID: "i2", NodeID: node, Address: datalayer.Address{Host: "127.0.0.1", Port: 1}, Ready: true, HealthPort: uint32(sideAddr.Port), MetricsPort: uint32(sideAddr.Port)}
	if err := m.Publish(ctx, OwnerRef{Kind: OwnerInstance, ID: "i2"}, membership("vllm"), []Published{healthOnly}); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "health on the health port", func() bool {
		e, ok := m.Store().Endpoint("i2/1")
		if !ok {
			return false
		}
		h, ok := datalayer.Attribute[*datalayer.HealthState](e, datalayer.CollectorHealth)
		return ok && h.Healthy
	})
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
}
