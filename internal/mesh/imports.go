package mesh

import (
	"cmp"
	"context"
	"crypto/sha256"
	"maps"
	"net"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nickheyer/nebu/internal/pools"
	"github.com/nickheyer/nebu/pkg/conditions"
	"github.com/nickheyer/nebu/pkg/datalayer"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/scheduling/remote"
	"google.golang.org/protobuf/proto"
)

const (
	// The owner kind imported pools and routes are published under
	importOwnerKind = "node"
	// The label an imported endpoint carries naming the exporter whose scheduler picks among its endpoints
	LabelRemoteScheduler = "remote-scheduler"
	// How long a withdrawn import waits for its requests in flight before it is unpublished, when the daemon sets none
	DefaultDrainTimeout = 30 * time.Second
	// How often a draining import checks its requests in flight
	drainPoll = 50 * time.Millisecond
)

// What one exporter's pool was last published as here, so an unchanged pool is not published again
type imported struct {
	exporter string
	pool     string
	owner    pools.OwnerRef
	digest   [32]byte
	routes   map[string][32]byte
	mode     string
	// The exporter's own parent entries on the pool, as its export carried them
	parents []conditions.ParentStatus
	// The instances behind the import and every address its endpoints are reached at
	instances []string
	addrs     []string
	// Set while the import drains before its removal, cleared when the pool is exported again
	retiring context.CancelFunc
}

// The owner an exporter's pool is published under, one per pool since publishing replaces what an owner holds
func importOwner(exporter, poolID string) pools.OwnerRef {
	return pools.OwnerRef{Kind: importOwnerKind, ID: exporter + "/" + poolID}
}

// Exported pools as this node's record carries them, its own endpoints behind relays on the mesh address, with its parent entries and conditions
func (m *Manager) exportedPools(list []*v1.Pool) []*v1.Pool {
	m.mu.Lock()
	host, _, _ := net.SplitHostPort(m.advertiseLocked())
	m.mu.Unlock()
	keep := map[string]bool{}
	var out []*v1.Pool
	for _, p := range list {
		shared := &v1.Pool{
			Id:        p.GetId(),
			Meta:      &v1.ObjectMeta{Generation: p.GetMeta().GetGeneration(), Labels: p.GetMeta().GetLabels(), Annotations: p.GetMeta().GetAnnotations()},
			Spec:      p.GetSpec(),
			OwnerKind: p.GetOwnerKind(),
			OwnerId:   p.GetOwnerId(),
			Status:    &v1.PoolStatus{Parents: p.GetStatus().GetParents(), Conditions: p.GetStatus().GetConditions(), ObservedGeneration: p.GetStatus().GetObservedGeneration()},
		}
		for _, ep := range p.GetStatus().GetEndpoints() {
			if ep.GetNodeId() != "" && ep.GetNodeId() != m.identity.ID {
				continue
			}
			if host == "" {
				m.Log.Warn("endpoint not exported, this node has no mesh address other nodes reach", "pool", p.GetId(), "endpoint", ep.GetId())
				continue
			}
			relayed, err := m.relayEndpoint(ep, host, keep)
			if err != nil {
				m.Log.Warn("endpoint not exported", "pool", p.GetId(), "endpoint", ep.GetId(), "err", err)
				continue
			}
			shared.Status.Endpoints = append(shared.Status.Endpoints, relayed)
			if ep.GetReady() {
				shared.Status.ReadyEndpoints++
			}
		}
		shared.Status.TotalEndpoints = uint32(len(shared.Status.Endpoints))
		if len(shared.Status.Endpoints) > 0 {
			out = append(out, shared)
		}
	}
	m.pruneRelays(keep)
	return out
}

// A local endpoint as other nodes reach it, its serving, metrics and health ports each behind a relay on the mesh host
func (m *Manager) relayEndpoint(ep *v1.Endpoint, host string, keep map[string]bool) (*v1.Endpoint, error) {
	addr, err := datalayer.ParseAddress(ep.GetAddress())
	if err != nil {
		return nil, err
	}
	port, err := m.relayFor(addr.String())
	if err != nil {
		return nil, err
	}
	keep[addr.String()] = true
	out := &v1.Endpoint{
		Id: ep.GetId(), PoolId: ep.GetPoolId(), Address: net.JoinHostPort(host, strconv.Itoa(port)), NodeId: m.identity.ID, InstanceId: ep.GetInstanceId(),
		Role: ep.GetRole(), Rank: ep.GetRank(), Ready: ep.GetReady(), Healthy: ep.GetHealthy(), Labels: ep.GetLabels(),
	}
	for _, aux := range []struct {
		port uint32
		set  func(uint32)
	}{
		{ep.GetMetricsPort(), func(p uint32) { out.MetricsPort = p }},
		{ep.GetHealthPort(), func(p uint32) { out.HealthPort = p }},
	} {
		if aux.port == 0 || int(aux.port) == addr.Port {
			continue
		}
		target := net.JoinHostPort(addr.Host, strconv.Itoa(int(aux.port)))
		relayed, err := m.relayFor(target)
		if err != nil {
			return nil, err
		}
		keep[target] = true
		aux.set(uint32(relayed))
	}
	return out, nil
}

// Routes this node serves itself as its record carries them, with their pools and without counters
func sharedRoutes(list []*v1.Route) []*v1.Route {
	var out []*v1.Route
	for _, r := range list {
		if r.GetOwnerKind() == importOwnerKind || r.GetNodeId() != "" {
			continue
		}
		out = append(out, &v1.Route{
			Name:      r.GetName(),
			Meta:      &v1.ObjectMeta{Generation: r.GetMeta().GetGeneration(), Labels: r.GetMeta().GetLabels(), Annotations: r.GetMeta().GetAnnotations()},
			Spec:      r.GetSpec(),
			OwnerKind: r.GetOwnerKind(),
			OwnerId:   r.GetOwnerId(),
			Status:    &v1.RouteStatus{PoolIds: r.GetStatus().GetPoolIds()},
		})
	}
	return out
}

// The objectives shared routes name, by name with their targets alone
func namedObjectives(view pools.View, routes []*v1.Route) []*v1.Objective {
	var names []string
	for _, r := range routes {
		if o := r.GetSpec().GetObjective(); o != "" && !slices.Contains(names, o) {
			names = append(names, o)
		}
	}
	slices.Sort(names)
	var out []*v1.Objective
	for _, name := range names {
		if o, ok := view.Objective(name); ok {
			out = append(out, &v1.Objective{Name: o.GetName(), Priority: o.GetPriority(), TtftMs: o.GetTtftMs(), TpotMs: o.GetTpotMs()})
		}
	}
	return out
}

// The model key a pool spec names as source colon repo colon group
func modelKeyOf(model string) (pools.ModelKey, bool) {
	parts := strings.SplitN(model, ":", 3)
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" {
		return pools.ModelKey{}, false
	}
	return pools.ModelKey{SourceID: parts[0], Repo: parts[1], Group: parts[2]}, true
}

// The protocol a pool spec names as the data layer spells it
func protocolOf(p v1.AppProtocol) datalayer.AppProtocol {
	switch p {
	case v1.AppProtocol_APP_PROTOCOL_HTTP:
		return datalayer.AppProtocolHTTP
	case v1.AppProtocol_APP_PROTOCOL_H2C:
		return datalayer.AppProtocolH2C
	case v1.AppProtocol_APP_PROTOCOL_GRPC:
		return datalayer.AppProtocolGRPC
	}
	return ""
}

// A copy of the metric names a pool spec declares, nil when it declares none
func metricNamesOf(spec *v1.PoolSpec) *v1.MetricNames {
	if spec.GetMetrics() == nil {
		return nil
	}
	return proto.Clone(spec.GetMetrics()).(*v1.MetricNames)
}

// The KV cache cells an endpoint of the pool holds, from the label the exporter's endpoints carry
func kvCapacityOf(p *v1.Pool) uint32 {
	for _, ep := range p.GetStatus().GetEndpoints() {
		if n, err := strconv.ParseUint(ep.GetLabels()[datalayer.LabelKVCapacity], 10, 32); err == nil {
			return uint32(n)
		}
	}
	return 0
}

// The membership an imported pool's endpoints join, read from the exporter's pool spec and endpoint labels
func membershipOf(p *v1.Pool, model pools.ModelKey) pools.Membership {
	spec := p.GetSpec()
	return pools.Membership{
		Model:           model,
		Runtime:         spec.GetRuntimeId(),
		Accelerator:     spec.GetAccelerator(),
		Protocol:        protocolOf(spec.GetAppProtocol()),
		Concurrency:     spec.GetConcurrency(),
		Metrics:         metricNamesOf(spec),
		KVCapacity:      kvCapacityOf(p),
		SchedulerConfig: spec.GetSchedulerConfig(),
	}
}

// An exporter's endpoint as it is published here, labelled with its exported id and the exporter's scheduler in endpoint mode
func importedEndpoint(ep *v1.Endpoint, exporter, mode string, live bool) (pools.Published, []string, error) {
	addr, err := datalayer.ParseAddress(ep.GetAddress())
	if err != nil {
		return pools.Published{}, nil, err
	}
	labels := maps.Clone(ep.GetLabels())
	if labels == nil {
		labels = map[string]string{}
	}
	labels[remote.LabelExportedID] = ep.GetId()
	if mode == RoutingEndpoint {
		labels[LabelRemoteScheduler] = exporter
	}
	addrs := []string{addr.String()}
	for _, port := range []uint32{ep.GetMetricsPort(), ep.GetHealthPort()} {
		if port != 0 && int(port) != addr.Port {
			addrs = append(addrs, net.JoinHostPort(addr.Host, strconv.Itoa(int(port))))
		}
	}
	pub := pools.Published{
		InstanceID: ep.GetInstanceId(), NodeID: exporter, Address: addr, Role: ep.GetRole(), Rank: ep.GetRank(), Ready: live && ep.GetReady(),
		Adapter: labels[datalayer.LabelAdapter], Labels: labels, MetricsPort: ep.GetMetricsPort(), HealthPort: ep.GetHealthPort(),
	}
	return pub, addrs, nil
}

// Holds an exporter's objectives and publishes its exported pools and routes in the configured routing mode, draining what it withdrew
func (m *Manager) importFrom(ctx context.Context, exporter string) {
	m.importing.Lock()
	defer m.importing.Unlock()
	rec, _, ok := m.exportSource(exporter)
	if !ok {
		return
	}
	have := m.imports[exporter]
	if have == nil {
		have = map[string]*imported{}
		m.imports[exporter] = have
	}
	carried := m.holdObjectives(ctx, exporter, rec.GetObjectives())
	live := rec.GetState() == v1.NodeState_NODE_STATE_READY
	wanted := map[string]bool{}
	for _, p := range rec.GetPools() {
		model, ok := modelKeyOf(p.GetSpec().GetModel())
		if !ok {
			m.Log.Warn("exported pool not imported, its model names no source, repo and group", "node", exporter, "pool", p.GetId(), "model", p.GetSpec().GetModel())
			continue
		}
		if id := pools.PoolIDOf(model, p.GetSpec().GetRuntimeId(), p.GetSpec().GetAccelerator()); string(id) != p.GetId() {
			m.Log.Warn("exported pool not imported, its id is not the one its model and runtime name across the mesh", "node", exporter, "pool", p.GetId(), "derived", id)
			continue
		}
		mode := m.routingMode(p.GetId())
		var endpoints []pools.Published
		var instances, addrs []string
		for _, ep := range p.GetStatus().GetEndpoints() {
			if ep.GetNodeId() != exporter {
				continue
			}
			pub, reached, err := importedEndpoint(ep, exporter, mode, live)
			if err != nil {
				m.Log.Warn("exported endpoint not imported, its address is not host and port", "node", exporter, "pool", p.GetId(), "address", ep.GetAddress())
				continue
			}
			endpoints = append(endpoints, pub)
			addrs = append(addrs, reached...)
			if !slices.Contains(instances, ep.GetInstanceId()) {
				instances = append(instances, ep.GetInstanceId())
			}
		}
		wanted[p.GetId()] = true
		raw, _ := proto.MarshalOptions{Deterministic: true}.Marshal(p)
		digest := sha256.Sum256(append(append(raw, boolByte(live)), mode...))
		imp := have[p.GetId()]
		if imp == nil {
			imp = &imported{exporter: exporter, pool: p.GetId(), owner: importOwner(exporter, p.GetId()), routes: map[string][32]byte{}}
		}
		if imp.retiring != nil {
			imp.retiring()
			imp.retiring = nil
			imp.digest = [32]byte{}
		}
		if imp.digest != digest {
			if err := m.Registrar.Publish(ctx, imp.owner, membershipOf(p, model), endpoints); err != nil {
				m.Log.Warn("exported pool not published", "node", exporter, "pool", p.GetId(), "err", err)
				continue
			}
			imp.digest, imp.mode = digest, mode
			imp.parents = parentsOf(p.GetStatus().GetParents())
			imp.instances, imp.addrs = instances, addrs
		}
		m.nameRoutes(ctx, imp, p, rec.GetRoutes())
		have[p.GetId()] = imp
	}
	for id, imp := range have {
		if !wanted[id] {
			m.retireLocked(ctx, imp)
		}
	}
	for name := range m.objectives[exporter] {
		if !carried[name] {
			m.Registrar.DropObjective(ctx, objectiveOwner(exporter), name)
			delete(m.objectives[exporter], name)
		}
	}
	m.refreshRemoteAddrsLocked()
}

// Drains a withdrawn import and unpublishes it once its requests in flight are done or the drain timeout passes
func (m *Manager) retireLocked(ctx context.Context, imp *imported) {
	if imp.retiring != nil {
		return
	}
	for _, name := range controllersOf(imp.parents) {
		conditions.RemoveController(&imp.parents, name)
	}
	m.Registrar.Drain(ctx, imp.owner)
	base := m.base
	if base == nil {
		base = context.Background()
	}
	wait, cancel := context.WithCancel(base)
	imp.retiring = cancel
	timeout := m.DrainTimeout
	if timeout <= 0 {
		timeout = DefaultDrainTimeout
	}
	m.Log.Info("imported pool withdrawn, draining before removal", "node", imp.exporter, "pool", imp.pool, "timeout", timeout)
	go func() {
		deadline := time.NewTimer(timeout)
		defer deadline.Stop()
		ticker := time.NewTicker(drainPoll)
		defer ticker.Stop()
		for m.inFlightOf(imp.instances) > 0 {
			select {
			case <-wait.Done():
				return
			case <-deadline.C:
				m.Log.Warn("imported pool removed with requests still in flight after the drain timeout", "node", imp.exporter, "pool", imp.pool)
				goto done
			case <-ticker.C:
			}
		}
	done:
		m.importing.Lock()
		defer m.importing.Unlock()
		if wait.Err() != nil {
			return
		}
		cancel()
		imp.retiring = nil
		m.Registrar.DropRoutes(base, imp.owner)
		m.Registrar.Unpublish(base, imp.owner)
		if have := m.imports[imp.exporter]; have != nil && have[imp.pool] == imp {
			delete(have, imp.pool)
			if len(have) == 0 {
				delete(m.imports, imp.exporter)
			}
		}
		m.refreshRemoteAddrsLocked()
		m.Log.Info("imported pool removed", "node", imp.exporter, "pool", imp.pool)
	}()
}

// Requests in flight on the instances behind an import through this node's gateway
func (m *Manager) inFlightOf(instances []string) int {
	total := 0
	for _, id := range instances {
		total += m.InFlight(id)
	}
	return total
}

// The distinct controller names a parent entry list carries
func controllersOf(list []conditions.ParentStatus) []string {
	var out []string
	for _, p := range list {
		if !slices.Contains(out, p.ControllerName) {
			out = append(out, p.ControllerName)
		}
	}
	return out
}

// The owner objectives a member's routes name are held under here
func objectiveOwner(exporter string) pools.OwnerRef {
	return pools.OwnerRef{Kind: importOwnerKind, ID: exporter}
}

// Holds the objectives a member carries under its owner before its routes are named, returning the names carried
func (m *Manager) holdObjectives(ctx context.Context, exporter string, list []*v1.Objective) map[string]bool {
	held := m.objectives[exporter]
	if held == nil {
		held = map[string][32]byte{}
		m.objectives[exporter] = held
	}
	carried := map[string]bool{}
	for _, o := range list {
		carried[o.GetName()] = true
		raw, _ := proto.MarshalOptions{Deterministic: true}.Marshal(o)
		digest := sha256.Sum256(raw)
		if have, ok := held[o.GetName()]; ok && have == digest {
			continue
		}
		if _, err := m.Registrar.PutObjectiveFor(ctx, objectiveOwner(exporter), o); err != nil {
			m.Log.Warn("exported objective not held", "node", exporter, "objective", o.GetName(), "err", err)
			continue
		}
		held[o.GetName()] = digest
	}
	return carried
}

// Names the exporter's routes resolving to one imported pool alone under the name it serves, through the exporter's gateway in parent mode
func (m *Manager) nameRoutes(ctx context.Context, imp *imported, pool *v1.Pool, routes []*v1.Route) {
	poolID := pool.GetId()
	wanted := map[string]bool{}
	for _, r := range routes {
		if ids := r.GetStatus().GetPoolIds(); len(ids) != 1 || ids[0] != poolID {
			continue
		}
		wanted[r.GetName()] = true
		served := servedName(r, pool)
		raw, _ := proto.MarshalOptions{Deterministic: true}.Marshal(r.GetSpec())
		digest := sha256.Sum256(append(append(raw, served...), imp.mode...))
		if imp.routes[r.GetName()] == digest {
			continue
		}
		spec := pools.RouteSpec{Policy: r.GetSpec().GetPolicy(), Profile: r.GetSpec().GetProfile(), Objective: r.GetSpec().GetObjective(), ServedName: served}
		if imp.mode == RoutingParent {
			spec.NodeID = imp.exporter
		}
		if _, err := m.Registrar.NameRoute(ctx, imp.owner, r.GetName(), spec); err != nil {
			m.Log.Warn("exported route not named", "route", r.GetName(), "pool", poolID, "err", err)
			continue
		}
		imp.routes[r.GetName()] = digest
	}
	for name := range imp.routes {
		if !wanted[name] {
			m.Registrar.DropRoute(ctx, imp.owner, name)
			delete(imp.routes, name)
		}
	}
}

// The model name a route's rule writes for the pool it targets, the pool's model when the rule names none
func servedName(r *v1.Route, pool *v1.Pool) string {
	for _, rule := range r.GetSpec().GetRules() {
		for _, t := range rule.GetTargets() {
			if t.GetPoolId() == pool.GetId() {
				return cmp.Or(t.GetModel(), pool.GetSpec().GetModel())
			}
		}
	}
	return pool.GetSpec().GetModel()
}

// Drains every pool imported from an exporter, removing its status entries, and lets go of its objectives
func (m *Manager) dropImports(ctx context.Context, exporter string) {
	m.importing.Lock()
	defer m.importing.Unlock()
	for _, imp := range m.imports[exporter] {
		m.retireLocked(ctx, imp)
	}
	delete(m.objectives, exporter)
	m.Registrar.DropObjectives(ctx, objectiveOwner(exporter))
	m.refreshRemoteAddrsLocked()
}

// One byte for a flag, folded into a digest
func boolByte(b bool) byte {
	if b {
		return 1
	}
	return 0
}
