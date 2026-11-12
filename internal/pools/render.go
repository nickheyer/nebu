package pools

import (
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nickheyer/nebu/pkg/conditions"
	"github.com/nickheyer/nebu/pkg/datalayer"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Condition types and reasons pools, routes and scheduler configurations report, declared in pkg/conditions
const (
	ConditionAccepted     = conditions.Accepted
	ConditionReady        = conditions.Ready
	ConditionResolvedRefs = conditions.ResolvedRefs
	ConditionExported     = conditions.Exported
	ConditionScraped      = conditions.Scraped

	ReasonAccepted               = "Accepted"
	ReasonReady                  = "Ready"
	ReasonNoReadyEndpoints       = "NoReadyEndpoints"
	ReasonResolvedRefs           = "ResolvedRefs"
	ReasonInvalidPoolRef         = "InvalidPoolRef"
	ReasonUnknownSchedulerConfig = "UnknownSchedulerConfig"
	ReasonInvalidSchedulerConfig = "InvalidSchedulerConfig"
	ReasonInvalid                = "Invalid"
	ReasonExported               = "Exported"
	ReasonNotExported            = "NotExported"
	ReasonStoreFailed            = "StoreFailed"
	ReasonScraped                = "Scraped"
	ReasonNotScraped             = "NotScraped"
	ReasonScrapeFailing          = "ScrapeFailing"
)

// Everything readers see, swapped whole after each mutation so reads never take the mutation lock
type view struct {
	pools      map[datalayer.PoolID]*poolView
	poolOrder  []datalayer.PoolID
	routes     map[string]*v1.Route
	routeOrder []string
	exact      map[string]*rule
	catchAll   *rule
	served     map[string][]scope
	objectives map[string]*v1.Objective
	configs    map[string]*v1.SchedulerConfig
	configList []string
	imported   map[datalayer.EndpointID]bool
	// The owners each endpoint serves for, its publisher and the slot that publisher occupies
	holders map[datalayer.EndpointID][]OwnerRef
}

// A pool record, its slot and instance owners, and the owners woken with no endpoint serving for them joined since
type poolView struct {
	record *v1.Pool
	wake   []OwnerRef
	waking map[OwnerRef]bool
}

// Recounts the pool's endpoints and sets its conditions and parent entry, true when its status changed
func (m *Manager) refreshPoolLocked(ps *poolState) bool {
	var ready, total uint32
	for _, ep := range m.store.Endpoints(ps.id, nil) {
		total++
		if datalayer.ReadyAndHealthy(ep) {
			ready++
		}
	}
	gen := int64(ps.meta.GetGeneration())
	accepted := conditions.Condition{Type: ConditionAccepted, Status: conditions.True, Reason: ReasonAccepted, Message: "the pool spec is valid", ObservedGeneration: gen}
	refs := conditions.Condition{Type: ConditionResolvedRefs, Status: conditions.True, Reason: ReasonResolvedRefs, Message: "scheduler config " + ps.spec.GetSchedulerConfig() + " loads", ObservedGeneration: gen}
	if ps.spec.GetSchedulerConfig() == SchedulerNoMetrics && !ps.dl.Metrics.Complete() {
		refs.Message += ", chosen because the runtime declares no queue, running and KV gauges to schedule on"
	}
	switch cs := m.st.configs[ps.spec.GetSchedulerConfig()]; {
	case cs == nil:
		accepted.Status, accepted.Reason = conditions.False, ReasonUnknownSchedulerConfig
		accepted.Message = "scheduler config " + ps.spec.GetSchedulerConfig() + " does not exist"
	case cs.err != nil:
		accepted.Status, accepted.Reason = conditions.False, ReasonInvalidSchedulerConfig
		accepted.Message = "scheduler config " + ps.spec.GetSchedulerConfig() + " does not load: " + cs.err.Error()
	}
	if accepted.Status == conditions.False {
		refs.Status, refs.Reason, refs.Message = conditions.False, accepted.Reason, accepted.Message
	}
	readyCond := conditions.Condition{Type: ConditionReady, Status: conditions.True, Reason: ReasonReady, ObservedGeneration: gen,
		Message: fmt.Sprintf("%d of %d endpoints ready", ready, total)}
	if ready == 0 {
		readyCond.Status, readyCond.Reason = conditions.False, ReasonNoReadyEndpoints
	}
	exported := conditions.Condition{Type: ConditionExported, Status: conditions.True, Reason: ReasonExported, Message: "offered to every member of the mesh", ObservedGeneration: gen}
	if !ps.spec.GetExport() {
		exported.Status, exported.Reason, exported.Message = conditions.False, ReasonNotExported, "served by this node alone"
	}
	conds := slices.Clone(ps.conds)
	parents := cloneParents(ps.parents)
	conditions.Set(&conds, accepted)
	conditions.Set(&conds, readyCond)
	conditions.Set(&conds, exported)
	conditions.Set(&conds, m.scrapedLocked(ps, gen))
	gw := conditions.ParentRef{Kind: "Gateway", Name: m.nodeID}
	for _, c := range []conditions.Condition{accepted, refs} {
		if _, err := conditions.SetParent(&parents, GatewayController, gw, c); err != nil {
			panic(fmt.Sprintf("pools: pool %s holds %d parent entries under one controller and one gateway: %v", ps.id, len(parents), err))
		}
	}
	record := m.poolRecordLocked(ps, conds, parents, ready, total)
	if ps.record != nil && proto.Equal(ps.record, record) {
		return false
	}
	ps.conds, ps.parents, ps.ready = conds, parents, ready
	ps.record = record
	return true
}

// Whether the pool's endpoints answer their metrics scrapes, naming each endpoint whose scrapes keep failing and why
func (m *Manager) scrapedLocked(ps *poolState, gen int64) conditions.Condition {
	c := conditions.Condition{Type: ConditionScraped, Status: conditions.True, Reason: ReasonScraped, Message: "every endpoint answers its metrics scrape", ObservedGeneration: gen}
	if !ps.dl.Metrics.Complete() || ps.dl.Metrics.Path == "" {
		c.Reason, c.Message = ReasonNotScraped, "the runtime declares no queue, running and KV metrics to scrape"
		return c
	}
	var failing []string
	for _, ep := range m.store.Endpoints(ps.id, nil) {
		if f, ok := datalayer.Attribute[*datalayer.ScrapeFailure](ep, datalayer.CollectorScrapeFailure); ok {
			failing = append(failing, fmt.Sprintf("endpoint %s fails its metrics scrape since %s, %s", ep.ID(), f.Since.UTC().Format(time.RFC3339), f.Err))
		}
	}
	if len(failing) > 0 {
		slices.Sort(failing)
		c.Status, c.Reason, c.Message = conditions.False, ReasonScrapeFailing, strings.Join(failing, "; ")
	}
	return c
}

// The pool without endpoints as readers see it under the given conditions and parent entries
func (m *Manager) poolRecordLocked(ps *poolState, conds []conditions.Condition, parents []conditions.ParentStatus, ready, total uint32) *v1.Pool {
	record := &v1.Pool{
		Id:   string(ps.id),
		Meta: proto.Clone(ps.meta).(*v1.ObjectMeta),
		Spec: proto.Clone(ps.spec).(*v1.PoolSpec),
		Status: &v1.PoolStatus{
			Parents:            protoParents(parents),
			Conditions:         protoConditions(conds),
			ReadyEndpoints:     ready,
			TotalEndpoints:     total,
			ObservedGeneration: ps.meta.GetGeneration(),
		},
	}
	if owners := localFirst(ps.owners); len(owners) > 0 {
		record.OwnerKind, record.OwnerId = owners[0].Kind, owners[0].ID
	}
	return record
}

// The pool as an event carries it, with its endpoints read now, nil before its first commit
func (m *Manager) renderPoolLocked(ps *poolState) *v1.Pool {
	if ps.record == nil {
		return nil
	}
	return m.render(ps.record, nil)
}

// A copy of a pool record with the endpoints the filter keeps, every endpoint when it is nil
func (m *Manager) render(record *v1.Pool, keep func(datalayer.Endpoint) bool) *v1.Pool {
	out := proto.Clone(record).(*v1.Pool)
	ports := *m.ports.Load()
	for _, ep := range m.store.Endpoints(datalayer.PoolID(record.GetId()), keep) {
		out.Status.Endpoints = append(out.Status.Endpoints, endpointOf(ep, ports[ep.ID()]))
	}
	return out
}

// One store endpoint as the API shows it with the ports its metrics and health are sampled on
func endpointOf(ep datalayer.Endpoint, ports endpointPorts) *v1.Endpoint {
	labels := ep.Labels()
	rank, _ := strconv.ParseUint(labels[datalayer.LabelRank], 10, 32)
	out := &v1.Endpoint{
		Id:          string(ep.ID()),
		PoolId:      string(ep.Pool()),
		Address:     ep.Address().String(),
		NodeId:      labels[datalayer.LabelNode],
		InstanceId:  labels[datalayer.LabelInstance],
		Role:        labels[datalayer.LabelRole],
		Rank:        uint32(rank),
		Ready:       ep.Ready(),
		Healthy:     true,
		Labels:      maps.Clone(labels),
		MetricsPort: uint32(ports.metrics),
		HealthPort:  uint32(ports.health),
	}
	if h, ok := datalayer.Attribute[*datalayer.HealthState](ep, datalayer.CollectorHealth); ok {
		out.Healthy = h.Healthy
	}
	if sm, ok := datalayer.Attribute[*datalayer.ServerMetrics](ep, datalayer.CollectorMetrics); ok {
		out.Metrics = &v1.EndpointMetrics{
			QueuedRequests:     uint32(max(sm.QueuedRequests, 0)),
			RunningRequests:    uint32(max(sm.RunningRequests, 0)),
			KvCacheUtilization: sm.KVCacheUtilization,
			BlockSize:          uint32(max(sm.BlockSize, 0)),
			NumBlocks:          uint32(max(sm.NumGPUBlocks, 0)),
			MaxLora:            uint32(max(sm.MaxLoRA, 0)),
			RunningAdapters:    slices.Sorted(maps.Keys(sm.RunningAdapters)),
			WaitingAdapters:    slices.Sorted(maps.Keys(sm.WaitingAdapters)),
			UpdatedAt:          timestamppb.New(sm.UpdatedAt),
		}
	}
	if n, ok := datalayer.Attribute[int](ep, datalayer.CollectorAssumed); ok && n > 0 {
		out.AssumedRequests = uint32(n)
	}
	return out
}

func (m *Manager) Pools() []*v1.Pool {
	v := m.view.Load()
	out := make([]*v1.Pool, 0, len(v.poolOrder))
	for _, id := range v.poolOrder {
		out = append(out, m.render(v.pools[id].record, nil))
	}
	return out
}

func (m *Manager) Pool(id string) (*v1.Pool, bool) {
	pv, ok := m.view.Load().pools[datalayer.PoolID(id)]
	if !ok {
		return nil, false
	}
	return m.render(pv.record, nil), true
}

func (m *Manager) Routes() []*v1.Route {
	v := m.view.Load()
	out := make([]*v1.Route, 0, len(v.routeOrder))
	for _, name := range v.routeOrder {
		out = append(out, proto.Clone(v.routes[name]).(*v1.Route))
	}
	return out
}

func (m *Manager) Route(name string) (*v1.Route, bool) {
	r, ok := m.view.Load().routes[name]
	if !ok {
		return nil, false
	}
	return proto.Clone(r).(*v1.Route), true
}

// The route the view holds, shared with every reader and replaced whole on change, so callers only read it
func (m *Manager) RouteView(name string) (*v1.Route, bool) {
	r, ok := m.view.Load().routes[name]
	return r, ok
}

// Pools whose spec exports them, with the endpoints this node publishes and none imported from another member
func (m *Manager) Exported() []*v1.Pool {
	v := m.view.Load()
	var out []*v1.Pool
	for _, id := range v.poolOrder {
		record := v.pools[id].record
		if !record.GetSpec().GetExport() {
			continue
		}
		out = append(out, m.render(record, func(ep datalayer.Endpoint) bool { return !v.imported[ep.ID()] }))
	}
	return out
}

// Rebuilds the reader view from the current state
func (m *Manager) publishViewLocked() {
	v := &view{
		pools:      make(map[datalayer.PoolID]*poolView, len(m.st.pools)),
		routes:     make(map[string]*v1.Route, len(m.st.routes)),
		exact:      map[string]*rule{},
		objectives: maps.Clone(m.st.objectives),
		configs:    make(map[string]*v1.SchedulerConfig, len(m.st.configs)),
		imported:   map[datalayer.EndpointID]bool{},
		holders:    make(map[datalayer.EndpointID][]OwnerRef, len(m.st.endpoints)),
	}
	for id, ps := range m.st.pools {
		if ps.record == nil {
			continue
		}
		pv := &poolView{record: ps.record, waking: maps.Clone(ps.waking)}
		for _, o := range ps.owners {
			if o.Kind == OwnerSlot || o.Kind == OwnerInstance {
				pv.wake = append(pv.wake, o)
			}
		}
		v.pools[id] = pv
	}
	v.poolOrder = slices.Sorted(maps.Keys(v.pools))
	for name, rs := range m.st.routes {
		v.routes[name] = rs.route
		m.compileLocked(v, rs)
	}
	v.routeOrder = slices.Sorted(maps.Keys(v.routes))
	v.served = m.servedLocked()
	for name, cs := range m.st.configs {
		v.configs[name] = cs.record
	}
	v.configList = slices.Sorted(maps.Keys(v.configs))
	holders := map[OwnerRef][]OwnerRef{}
	for id, owner := range m.st.endpoints {
		if owner.Kind == OwnerNode {
			v.imported[id] = true
		}
		h, ok := holders[owner]
		if !ok {
			h = m.holdersLocked(owner)
			holders[owner] = h
		}
		v.holders[id] = h
	}
	m.view.Store(v)
}

// The owners a publisher's endpoints serve for, the publisher and the slot its membership occupies
func (m *Manager) holdersLocked(publisher OwnerRef) []OwnerRef {
	if os := m.st.owners[publisher]; os != nil && os.m != nil && os.m.Slot != "" {
		return []OwnerRef{publisher, {Kind: OwnerSlot, ID: os.m.Slot}}
	}
	return []OwnerRef{publisher}
}

// One rewrite rule ready for a weighted pick
type rule struct {
	route    string
	user     bool
	created  time.Time
	order    int
	draining bool
	balance  v1.RouteBalance
	targets  []target
}

// A rule target with its static weight and the owner whose endpoints serve it, zero for a user rule's
type target struct {
	pool   datalayer.PoolID
	model  string
	weight int
	owner  OwnerRef
}

// Whether this rule outranks another matching the same name, user over owner, then oldest route, name and rule order
func (r *rule) before(o *rule) bool {
	if r.user != o.user {
		return r.user
	}
	if !r.created.Equal(o.created) {
		return r.created.Before(o.created)
	}
	if r.route != o.route {
		return r.route < o.route
	}
	return r.order < o.order
}

// Adds a route's rules to the view, keeping the winning rule per exact match and the winning user catch all
func (m *Manager) compileLocked(v *view, rs *routeState) {
	if rs.dropped != nil {
		return
	}
	user := rs.owner == OwnerRef{}
	created := rs.route.GetMeta().GetCreatedAt().AsTime()
	for i, rr := range rs.route.GetSpec().GetRules() {
		c := &rule{route: rs.route.GetName(), user: user, created: created, order: i, balance: rs.route.GetSpec().GetBalance()}
		c.targets = m.ruleTargetsLocked(rs, rr, true)
		c.draining = !user && len(c.targets) == 0 && m.allDrainingLocked(rs) && (len(rs.own.GetTargets()) > 0 || len(rs.shared) > 0)
		if rr.GetMatch() == "" {
			if user && (v.catchAll == nil || c.before(v.catchAll)) {
				v.catchAll = c
			}
			continue
		}
		if cur, ok := v.exact[rr.GetMatch()]; !ok || c.before(cur) {
			v.exact[rr.GetMatch()] = c
		}
	}
}

// A rule's targets, a user's as written, an owner's own then each shared owner's, draining owners skipped when live
func (m *Manager) ruleTargetsLocked(rs *routeState, rr *v1.RewriteRule, live bool) []target {
	if rs.owner == (OwnerRef{}) {
		return targetsOf(rr.GetTargets(), OwnerRef{})
	}
	var out []target
	if !live || !m.drainingLocked(rs.owner) {
		out = append(out, targetsOf(rs.own.GetTargets(), rs.owner)...)
	}
	for _, o := range slices.SortedFunc(maps.Keys(rs.shared), compareOwners) {
		if !live || !m.drainingLocked(o) {
			out = append(out, targetsOf([]*v1.RewriteTarget{rs.shared[o].target}, o)...)
		}
	}
	return out
}

// Rewrite targets as rule targets the owner serves
func targetsOf(in []*v1.RewriteTarget, owner OwnerRef) []target {
	out := make([]target, len(in))
	for i, t := range in {
		out[i] = target{pool: datalayer.PoolID(t.GetPoolId()), model: t.GetModel(), weight: int(t.GetWeight()), owner: owner}
	}
	return out
}

// Every name a pool answers to by its model, endpoint catalogs, adapter labels and routes, with the owners serving it
func (m *Manager) servedLocked() map[string][]scope {
	names := map[string]map[datalayer.PoolID]*scope{}
	// Records a name in a pool as served by one owner, or by every owner when every is set
	add := func(name string, pool datalayer.PoolID, adapter, weak bool, owner OwnerRef, every bool) {
		if name == "" {
			return
		}
		byPool := names[name]
		if byPool == nil {
			byPool = map[datalayer.PoolID]*scope{}
			names[name] = byPool
		}
		s, had := byPool[pool]
		switch {
		case !had:
			s = &scope{pool: pool, adapter: adapter}
			byPool[pool] = s
		case !weak:
			s.adapter = s.adapter && adapter
		}
		switch {
		case every:
			s.any = true
		case owner != (OwnerRef{}) && !slices.Contains(s.owners, owner):
			s.owners = append(s.owners, owner)
		}
	}
	for id, ps := range m.st.pools {
		add(ps.dl.Model, id, false, false, OwnerRef{}, true)
		for _, ep := range m.store.Endpoints(id, nil) {
			publisher := m.st.endpoints[ep.ID()]
			add(ep.Labels()[datalayer.LabelAdapter], id, true, false, publisher, false)
			if c, ok := datalayer.Attribute[*datalayer.Catalog](ep, datalayer.CollectorCatalog); ok {
				for _, name := range c.Models {
					add(name, id, false, false, publisher, false)
				}
				for _, name := range c.Adapters {
					add(name, id, true, false, publisher, false)
				}
			}
		}
	}
	for _, rs := range m.st.routes {
		if rs.dropped != nil {
			continue
		}
		owned := rs.owner != OwnerRef{}
		for _, rr := range rs.route.GetSpec().GetRules() {
			for _, t := range m.ruleTargetsLocked(rs, rr, false) {
				if m.st.pools[t.pool] == nil {
					continue
				}
				add(t.model, t.pool, false, true, t.owner, false)
				if owned {
					add(rr.GetMatch(), t.pool, false, true, t.owner, false)
				}
			}
		}
	}
	out := make(map[string][]scope, len(names))
	for name, byPool := range names {
		list := make([]scope, 0, len(byPool))
		for _, pool := range slices.Sorted(maps.Keys(byPool)) {
			s := *byPool[pool]
			if s.any || len(s.owners) == 0 {
				s.any, s.owners = true, nil
			}
			slices.SortFunc(s.owners, compareOwners)
			list = append(list, s)
		}
		out[name] = list
	}
	return out
}

func protoConditions(in []conditions.Condition) []*v1.Condition {
	out := make([]*v1.Condition, len(in))
	for i, c := range in {
		out[i] = conditions.ToProto(c)
	}
	return out
}

// Conditions read back from a stored status, keeping their transition times
func conditionsOf(in []*v1.Condition) []conditions.Condition {
	out := make([]conditions.Condition, 0, len(in))
	for _, c := range in {
		out = append(out, conditions.FromProto(c))
	}
	return out
}

func protoParents(in []conditions.ParentStatus) []*v1.ParentStatus {
	out := make([]*v1.ParentStatus, len(in))
	for i, p := range in {
		out[i] = &v1.ParentStatus{
			ParentRef:      &v1.ParentReference{Group: p.ParentRef.Group, Kind: p.ParentRef.Kind, Name: p.ParentRef.Name, Namespace: p.ParentRef.Namespace},
			ControllerName: p.ControllerName,
			Conditions:     protoConditions(p.Conditions),
		}
	}
	return out
}

func cloneParents(in []conditions.ParentStatus) []conditions.ParentStatus {
	out := slices.Clone(in)
	for i := range out {
		out[i].Conditions = slices.Clone(out[i].Conditions)
	}
	return out
}
