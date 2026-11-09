package pools

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/nickheyer/nebu/pkg/datalayer"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/scheduling/config"
	"github.com/nickheyer/nebu/pkg/scheduling/framework"
	_ "github.com/nickheyer/nebu/pkg/scheduling/plugins/filter"
	_ "github.com/nickheyer/nebu/pkg/scheduling/plugins/picker"
	_ "github.com/nickheyer/nebu/pkg/scheduling/plugins/profile"
	_ "github.com/nickheyer/nebu/pkg/scheduling/plugins/scorer"
	"github.com/nickheyer/nebu/pkg/scheduling/registry"
)

// One endpoint per data parallel rank of an instance, each on its own port with metrics and health there
func rankEndpoints(instance string, ports ...int) []Published {
	out := make([]Published, len(ports))
	for i, port := range ports {
		out[i] = Published{InstanceID: instance, NodeID: node, Address: datalayer.Address{Host: "127.0.0.1", Port: port}, Rank: uint32(i), Ready: true, MetricsPort: uint32(port), HealthPort: uint32(port)}
	}
	return out
}

// The rendered endpoints of a pool by id
func rendered(t *testing.T, m *Manager, id datalayer.PoolID) map[string]*v1.Endpoint {
	t.Helper()
	p, ok := m.Pool(string(id))
	if !ok {
		t.Fatalf("pool %s missing", id)
	}
	out := map[string]*v1.Endpoint{}
	for _, ep := range p.GetStatus().GetEndpoints() {
		out[ep.GetId()] = ep
	}
	return out
}

// Each rank is an endpoint, one leaves rotation alone, and the set resyncs on port, selector and restart changes
func TestRankPortsAreEndpoints(t *testing.T) {
	ctx := context.Background()
	m, d, bus := newManager(t)
	inst := OwnerRef{Kind: OwnerInstance, ID: "i1"}
	id := PoolIDOf(llama, "vllm", "")
	steps := []struct {
		name  string
		apply func(t *testing.T)
		ports []uint32
		ready map[string]bool
	}{
		{name: "three ranks publish three endpoints", apply: func(t *testing.T) {
			if err := m.Publish(ctx, inst, membership("vllm"), rankEndpoints("i1", 8100, 8101, 8102)); err != nil {
				t.Fatal(err)
			}
		}, ports: []uint32{8100, 8101, 8102}, ready: map[string]bool{"i1/8100": true, "i1/8101": true, "i1/8102": true}},
		{name: "a rank out of rotation keeps its process's other ranks", apply: func(t *testing.T) { m.SetRankReady(ctx, "i1", 1, false) },
			ports: []uint32{8100, 8101, 8102}, ready: map[string]bool{"i1/8100": true, "i1/8101": false, "i1/8102": true}},
		{name: "the rank back in rotation", apply: func(t *testing.T) { m.SetRankReady(ctx, "i1", 1, true) },
			ports: []uint32{8100, 8101, 8102}, ready: map[string]bool{"i1/8100": true, "i1/8101": true, "i1/8102": true}},
		{name: "a shorter port list drops the rank it no longer names", apply: func(t *testing.T) {
			if err := m.Publish(ctx, inst, membership("vllm"), rankEndpoints("i1", 8100, 8101)); err != nil {
				t.Fatal(err)
			}
		}, ports: []uint32{8100, 8101}, ready: map[string]bool{"i1/8100": true, "i1/8101": true}},
		{name: "a selector keeps only the rank it names", apply: func(t *testing.T) {
			if _, err := m.PutPool(ctx, string(id), &v1.PoolSpec{Model: llama.String(), TargetPorts: []uint32{8101}, Selector: map[string]string{datalayer.LabelRank: "1"}}); err != nil {
				t.Fatal(err)
			}
		}, ports: []uint32{8101}, ready: map[string]bool{"i1/8101": true}},
		{name: "a restart starts from no endpoints and resyncs every rank under the stored selector", apply: func(t *testing.T) {
			if err := m.Close(); err != nil {
				t.Fatal(err)
			}
			m = openManager(t, d, bus)
			if eps := m.Store().Endpoints(id, nil); len(eps) != 0 {
				t.Fatalf("a restarted manager holds %d endpoints before any publish", len(eps))
			}
			if err := m.Publish(ctx, inst, membership("vllm"), rankEndpoints("i1", 8200, 8201, 8202)); err != nil {
				t.Fatal(err)
			}
		}, ports: []uint32{8101}, ready: map[string]bool{}},
		{name: "a target port list naming the new ports joins the selected rank", apply: func(t *testing.T) {
			if _, err := m.PutPool(ctx, string(id), &v1.PoolSpec{Model: llama.String(), TargetPorts: []uint32{8200, 8201, 8202}, Selector: map[string]string{datalayer.LabelRank: "1"}}); err != nil {
				t.Fatal(err)
			}
		}, ports: []uint32{8200, 8201, 8202}, ready: map[string]bool{"i1/8201": true}},
		{name: "dropping the selector brings every rank on a target port back", apply: func(t *testing.T) {
			if _, err := m.PutPool(ctx, string(id), &v1.PoolSpec{Model: llama.String(), TargetPorts: []uint32{8200, 8201, 8202}}); err != nil {
				t.Fatal(err)
			}
		}, ports: []uint32{8200, 8201, 8202}, ready: map[string]bool{"i1/8200": true, "i1/8201": true, "i1/8202": true}},
		{name: "a shorter target port list drops the ranks off it", apply: func(t *testing.T) {
			if _, err := m.PutPool(ctx, string(id), &v1.PoolSpec{Model: llama.String(), TargetPorts: []uint32{8200}}); err != nil {
				t.Fatal(err)
			}
		}, ports: []uint32{8200}, ready: map[string]bool{"i1/8200": true}},
	}
	for _, s := range steps {
		s.apply(t)
		p, _ := m.Pool(string(id))
		if got := p.GetSpec().GetTargetPorts(); !slices.Equal(got, s.ports) {
			t.Fatalf("%s: target ports %v, want %v", s.name, got, s.ports)
		}
		eps := rendered(t, m, id)
		if len(eps) != len(s.ready) {
			t.Fatalf("%s: endpoints %v", s.name, eps)
		}
		for eid, ready := range s.ready {
			ep, ok := eps[eid]
			if !ok || ep.GetReady() != ready || ep.GetInstanceId() != "i1" {
				t.Fatalf("%s: endpoint %s %v, want ready %v", s.name, eid, ep, ready)
			}
			stored, _ := m.Store().Endpoint(datalayer.EndpointID(eid))
			if ep.GetLabels()[datalayer.LabelRank] != stored.Labels()[datalayer.LabelRank] || itoa(int(ep.GetRank())) != stored.Labels()[datalayer.LabelRank] {
				t.Fatalf("%s: endpoint %s rank %d labels %v", s.name, eid, ep.GetRank(), stored.Labels())
			}
			_, addr, ok := m.metricsTarget(datalayer.EndpointID(eid))
			url, hok := m.healthURL(datalayer.EndpointID(eid))
			if !ok || !hok || addr.Port != stored.Address().Port || url != "http://"+stored.Address().String()+"/health" {
				t.Fatalf("%s: endpoint %s scrapes %v probes %s", s.name, eid, addr, url)
			}
		}
	}
}

// Scrape and probe reach each rank at its own port, a silent rank alone turning unhealthy
func TestRankPortsScrapedAndProbedApart(t *testing.T) {
	ctx := context.Background()
	m, _, _ := newManager(t)
	_, zero := stubServer(t, "rank-zero")
	one, oneAddr := stubServer(t, "rank-one")
	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	eps := []Published{
		{InstanceID: "i1", NodeID: node, Address: zero, Rank: 0, Ready: true, MetricsPort: uint32(zero.Port), HealthPort: uint32(zero.Port)},
		{InstanceID: "i1", NodeID: node, Address: oneAddr, Rank: 1, Ready: true, MetricsPort: uint32(oneAddr.Port), HealthPort: uint32(oneAddr.Port)},
	}
	if err := m.Publish(ctx, OwnerRef{Kind: OwnerInstance, ID: "i1"}, membership("vllm"), eps); err != nil {
		t.Fatal(err)
	}
	ids := []datalayer.EndpointID{datalayer.EndpointID("i1/" + itoa(zero.Port)), datalayer.EndpointID("i1/" + itoa(oneAddr.Port))}
	waitUntil(t, "metrics and health from both ranks", func() bool {
		for _, eid := range ids {
			e, ok := m.Store().Endpoint(eid)
			if !ok {
				return false
			}
			_, metrics := datalayer.Attribute[*datalayer.ServerMetrics](e, datalayer.CollectorMetrics)
			h, health := datalayer.Attribute[*datalayer.HealthState](e, datalayer.CollectorHealth)
			if !metrics || !health || !h.Healthy {
				return false
			}
		}
		return true
	})
	one.Pause()
	waitUntil(t, "rank 1 unhealthy while rank 0 serves", func() bool {
		eps := rendered(t, m, PoolIDOf(llama, "vllm", ""))
		return !eps[string(ids[1])].GetHealthy() && eps[string(ids[0])].GetHealthy()
	})
	one.Resume()
	waitUntil(t, "rank 1 healthy again", func() bool {
		return rendered(t, m, PoolIDOf(llama, "vllm", ""))[string(ids[1])].GetHealthy()
	})
}

// The scheduler sees each rank as a candidate, picks the less loaded one, and skips ranks out of rotation
func TestSchedulerBalancesAcrossRanks(t *testing.T) {
	ctx := context.Background()
	m, _, _ := newManager(t)
	if err := m.Publish(ctx, OwnerRef{Kind: OwnerInstance, ID: "i1"}, membership("vllm"), rankEndpoints("i1", 8300, 8301)); err != nil {
		t.Fatal(err)
	}
	sched, _, err := config.Load([]byte(rankScheduler), registry.Deps{Store: m.Store()})
	if err != nil {
		t.Fatal(err)
	}
	id := PoolIDOf(llama, "vllm", "")
	queue := func(port, n int) {
		m.Store().SetAttribute(datalayer.EndpointID("i1/"+itoa(port)), datalayer.CollectorMetrics, &datalayer.ServerMetrics{QueuedRequests: n, UpdatedAt: time.Now()})
	}
	cases := []struct {
		name   string
		setup  func()
		picked int
	}{
		{name: "rank 1 queued, rank 0 picked", setup: func() { queue(8300, 0); queue(8301, 9) }, picked: 8300},
		{name: "rank 0 queued, rank 1 picked", setup: func() { queue(8300, 9); queue(8301, 0) }, picked: 8301},
		{name: "the idle rank out of rotation, the queued rank picked", setup: func() { m.SetRankReady(ctx, "i1", 1, false) }, picked: 8300},
	}
	for _, c := range cases {
		c.setup()
		candidates := m.Store().Endpoints(id, nil)
		if len(candidates) != 2 {
			t.Fatalf("%s: candidates %v", c.name, candidates)
		}
		res, err := sched.Schedule(ctx, &framework.Request{ID: c.name, TargetModel: llama.String()}, candidates)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if eps := res.Endpoints(); len(eps) != 1 || eps[0].Address().Port != c.picked {
			t.Fatalf("%s: picked %v, want port %d", c.name, eps, c.picked)
		}
	}
}

// A profile that filters on readiness and fresh metrics and picks the shortest queue
const rankScheduler = `
plugins:
- type: ready-filter
  name: ready
- type: fresh-metrics-filter
  name: fresh
  parameters:
    threshold: 1m
- type: queue-scorer
  name: queue
- type: max-score-picker
  name: picker
  parameters:
    count: 1
- type: single-profile-handler
  name: handler
  parameters:
    profile: default
profiles:
- name: default
  plugins:
  - pluginRef: ready
  - pluginRef: fresh
  - pluginRef: queue
    weight: 1
  - pluginRef: picker
profileHandler:
  pluginRef: handler
`
