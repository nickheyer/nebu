package pools

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/nickheyer/nebu/pkg/conditions"
	"github.com/nickheyer/nebu/pkg/datalayer"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Bounds every rewrite target weight is held to
const (
	minWeight = 1
	maxWeight = 1000000
)

// One node owner sharing a route's name, its pool target and the fields it named the route with
type sharedTarget struct {
	target *v1.RewriteTarget
	base   *v1.RouteSpec
}

// Creates or updates the owner's route, a node owner joining a held name as another target and a local owner taking one a node holds
func (m *Manager) NameRoute(ctx context.Context, owner OwnerRef, name string, spec RouteSpec) (*v1.Route, error) {
	if err := validOwner(owner); err != nil {
		return nil, err
	}
	if name == "" {
		return nil, fmt.Errorf("%w: route without a name", ErrInvalid)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	os := m.st.owners[owner]
	if os == nil || os.pool == "" {
		return nil, fmt.Errorf("%w: %s %s has published no pool", ErrNotFound, owner.Kind, owner.ID)
	}
	if spec.Objective != "" && m.st.objectives[spec.Objective] == nil {
		return nil, fmt.Errorf("%w: objective %q does not exist", ErrInvalid, spec.Objective)
	}
	base := &v1.RouteSpec{Policy: clonePolicy(spec.Policy), Profile: cloneProfile(spec.Profile), Objective: spec.Objective}
	target := &v1.RewriteTarget{PoolId: string(os.pool), Model: cmp.Or(spec.ServedName, name), Weight: 1}
	rs := m.st.routes[name]
	var next *routeState
	switch {
	case rs == nil:
		next = &routeState{owner: owner, base: base, own: &v1.RewriteRule{Match: name, Targets: []*v1.RewriteTarget{target}}}
	case rs.dropped != nil:
		return nil, fmt.Errorf("%w: route %s is %s and waits for the database to let it go: %v", ErrConflict, name, routeHolder(rs), rs.dropped)
	case rs.owner == owner:
		next = rs.with(owner, base, &v1.RewriteRule{Match: name, Targets: []*v1.RewriteTarget{target}})
	case rs.owner == (OwnerRef{}):
		return nil, fmt.Errorf("%w: route %s is %s", ErrConflict, name, routeHolder(rs))
	case owner.Kind == OwnerNode:
		next = rs.with(rs.owner, rs.base, rs.own)
		next.shared[owner] = sharedTarget{target: target, base: base}
	case rs.owner.Kind == OwnerNode:
		next = rs.with(owner, base, &v1.RewriteRule{Match: name, Targets: []*v1.RewriteTarget{target}})
		next.shared[rs.owner] = sharedTarget{target: m.firstOwnTargetLocked(rs), base: rs.base}
	default:
		return nil, fmt.Errorf("%w: route %s is %s", ErrConflict, name, routeHolder(rs))
	}
	if next.owner == owner {
		next.shape, next.seats, next.nodeID = spec.Shape, slices.Clone(spec.Seats), spec.NodeID
	}
	if err := m.putRouteLocked(ctx, name, rs, next); err != nil {
		return nil, err
	}
	m.publishViewLocked()
	return proto.Clone(next.route).(*v1.Route), nil
}

// A copy of the route state under a primary owner, keeping its conditions, record, override and shared owners
func (rs *routeState) with(owner OwnerRef, base *v1.RouteSpec, own *v1.RewriteRule) *routeState {
	next := &routeState{owner: owner, conds: rs.conds, route: rs.route, override: rs.override, base: base, own: own, shared: maps.Clone(rs.shared), dirty: rs.dirty, shape: rs.shape, seats: rs.seats, nodeID: rs.nodeID, parents: rs.parents}
	if next.shared == nil {
		next.shared = map[OwnerRef]sharedTarget{}
	}
	delete(next.shared, owner)
	return next
}

// The first target of the primary owner's rule, the primary's pool when the rule is split to nothing
func (m *Manager) firstOwnTargetLocked(rs *routeState) *v1.RewriteTarget {
	if len(rs.own.GetTargets()) > 0 {
		return proto.Clone(rs.own.GetTargets()[0]).(*v1.RewriteTarget)
	}
	pool := ""
	if os := m.st.owners[rs.owner]; os != nil {
		pool = string(os.pool)
	}
	return &v1.RewriteTarget{PoolId: pool, Model: rs.route.GetName(), Weight: 1}
}

// Points the owner's route at weighted targets in place of its pool, zero weights taking nothing and none leaving it pending
func (m *Manager) SplitRoute(ctx context.Context, owner OwnerRef, name string, targets []WeightedTarget) error {
	if err := validOwner(owner); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	rs := m.st.routes[name]
	switch {
	case rs == nil:
		return fmt.Errorf("%w: route %s", ErrNotFound, name)
	case rs.owner != owner:
		return fmt.Errorf("%w: route %s is %s", ErrConflict, name, routeHolder(rs))
	case rs.dropped != nil:
		return fmt.Errorf("%w: route %s waits for the database to let it go: %v", ErrConflict, name, rs.dropped)
	}
	rule := &v1.RewriteRule{Match: name}
	for _, t := range targets {
		if t.Pool == "" || t.Weight > maxWeight {
			return fmt.Errorf("%w: split target pool %q weight %d", ErrInvalid, t.Pool, t.Weight)
		}
		if t.Weight == 0 {
			continue
		}
		rule.Targets = append(rule.Targets, &v1.RewriteTarget{PoolId: string(t.Pool), Model: t.Model, Weight: t.Weight})
	}
	next := rs.with(owner, rs.base, rule)
	if err := m.putRouteLocked(ctx, name, rs, next); err != nil {
		return err
	}
	m.publishViewLocked()
	return nil
}

// What the requests one split target took came to, counters that only grow
type Outcome struct {
	Requests uint64
	// Requests that failed or answered an error status
	Errors uint64
	// Requests carrying a TTFT or TPOT objective and those of them meeting it
	Judged uint64
	Met    uint64
	// Time to first token summed over TTFTs requests and time per output token summed over TPOTs requests
	TTFT  time.Duration
	TTFTs uint64
	TPOT  time.Duration
	TPOTs uint64
}

// The counts added between an earlier reading of the same counters and this one
func (o Outcome) Since(before Outcome) Outcome {
	return Outcome{
		Requests: o.Requests - before.Requests,
		Errors:   o.Errors - before.Errors,
		Judged:   o.Judged - before.Judged,
		Met:      o.Met - before.Met,
		TTFT:     o.TTFT - before.TTFT,
		TTFTs:    o.TTFTs - before.TTFTs,
		TPOT:     o.TPOT - before.TPOT,
		TPOTs:    o.TPOTs - before.TPOTs,
	}
}

// Removes the owner from a route it named or shares, the next node owner taking a primary's place, forgetting an owner holding nothing
func (m *Manager) DropRoute(ctx context.Context, owner OwnerRef, name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.dropRouteLocked(ctx, owner, name)
	m.afterDropLocked(ctx, owner)
	m.publishViewLocked()
}

func (m *Manager) DropRoutes(ctx context.Context, owner OwnerRef) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.dropRoutesLocked(ctx, owner)
	m.publishViewLocked()
}

func (m *Manager) dropRoutesLocked(ctx context.Context, owner OwnerRef) {
	for _, name := range slices.Sorted(maps.Keys(m.st.routes)) {
		m.dropRouteLocked(ctx, owner, name)
	}
	m.afterDropLocked(ctx, owner)
}

// Takes one owner out of a route, deleting it when nobody else holds the name
func (m *Manager) dropRouteLocked(ctx context.Context, owner OwnerRef, name string) {
	rs := m.st.routes[name]
	if rs == nil || rs.dropped != nil {
		return
	}
	if _, ok := rs.shared[owner]; ok {
		next := rs.with(rs.owner, rs.base, rs.own)
		delete(next.shared, owner)
		m.mustPutRouteLocked(ctx, name, rs, next)
		return
	}
	if rs.owner != owner {
		return
	}
	if len(rs.shared) == 0 {
		m.deleteRouteLocked(ctx, name)
		return
	}
	heir := slices.SortedFunc(maps.Keys(rs.shared), compareOwners)[0]
	st := rs.shared[heir]
	next := rs.with(heir, st.base, &v1.RewriteRule{Match: name, Targets: []*v1.RewriteTarget{st.target}})
	m.mustPutRouteLocked(ctx, name, rs, next)
}

// Writes a route state change an owner leaving causes, a refused write keeping the route under its old state until retried
func (m *Manager) mustPutRouteLocked(ctx context.Context, name string, rs, next *routeState) {
	if err := m.putRouteLocked(ctx, name, rs, next); err != nil {
		m.log.Error("route not rewritten after an owner left, retrying on the next change", "route", name, "err", err)
		rs.dirty = true
	}
}

// Releases an owner that lost its last route and applies the pool it may have left
func (m *Manager) afterDropLocked(ctx context.Context, owner OwnerRef) {
	os := m.st.owners[owner]
	if os == nil {
		return
	}
	m.releaseLocked(os)
	if m.st.owners[owner] == nil {
		if ps := m.st.pools[os.pool]; ps != nil {
			m.applyLocked(ps, false)
		}
	}
	m.refreshRoutesLocked(ctx)
}

// Creates or replaces a user route, or sets policy, profile, objective and balance over an owner route's unchanged rules
func (m *Manager) PutRoute(ctx context.Context, name string, spec *v1.RouteSpec) (*v1.Route, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if rs := m.st.routes[name]; rs != nil && rs.owner != (OwnerRef{}) {
		return m.overrideLocked(ctx, name, rs, spec)
	}
	if err := m.validRouteLocked(name, spec); err != nil {
		return nil, err
	}
	rs := m.st.routes[name]
	next := &routeState{user: proto.Clone(spec).(*v1.RouteSpec)}
	if rs != nil {
		next.conds, next.parents, next.route = rs.conds, rs.parents, rs.route
	}
	if err := m.putRouteLocked(ctx, name, rs, next); err != nil {
		return nil, err
	}
	m.publishViewLocked()
	return proto.Clone(next.route).(*v1.Route), nil
}

// Removes a user route, refusing a route an owner named
func (m *Manager) DeleteRoute(ctx context.Context, name string) (*v1.Route, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rs := m.st.routes[name]
	switch {
	case rs == nil:
		return nil, fmt.Errorf("%w: route %s", ErrNotFound, name)
	case rs.owner != OwnerRef{}:
		return nil, fmt.Errorf("%w: route %s is %s", ErrManaged, name, routeHolder(rs))
	}
	if err := m.db.DeleteRoute(ctx, name); err != nil {
		return nil, err
	}
	out := proto.Clone(rs.route).(*v1.Route)
	delete(m.st.routes, name)
	m.emitLocked(v1.EventKind_EVENT_KIND_ROUTE, v1.EventAction_EVENT_ACTION_DELETED, name, out)
	m.publishViewLocked()
	return out, nil
}

// Stores a user's policy, profile, objective and balance over an owner route, dropping the override once they match the owner's
func (m *Manager) overrideLocked(ctx context.Context, name string, rs *routeState, spec *v1.RouteSpec) (*v1.Route, error) {
	rules := rs.route.GetSpec().GetRules()
	switch {
	case rs.owner.Kind == OwnerNode:
		return nil, fmt.Errorf("%w: route %s is %s", ErrManaged, name, routeHolder(rs))
	case rs.dropped != nil:
		return nil, fmt.Errorf("%w: route %s waits for the database to let it go: %v", ErrManaged, name, rs.dropped)
	case !slices.EqualFunc(spec.GetRules(), rules, func(a, b *v1.RewriteRule) bool { return proto.Equal(a, b) }):
		return nil, fmt.Errorf("%w: the rules of route %s are %s", ErrManaged, name, routeHolder(rs))
	case spec.GetObjective() != "" && m.st.objectives[spec.GetObjective()] == nil:
		return nil, fmt.Errorf("%w: route %s names objective %q which does not exist", ErrInvalid, name, spec.GetObjective())
	case spec.GetPolicy().GetRequestsPerSecond() < 0:
		return nil, fmt.Errorf("%w: route %s requests per second %g", ErrInvalid, name, spec.GetPolicy().GetRequestsPerSecond())
	case !validBalance(spec.GetBalance()):
		return nil, fmt.Errorf("%w: route %s balance %d", ErrInvalid, name, spec.GetBalance())
	}
	fields := &v1.RouteSpec{Policy: clonePolicy(spec.GetPolicy()), Profile: cloneProfile(spec.GetProfile()), Objective: spec.GetObjective(), Balance: spec.GetBalance()}
	var override *v1.Route
	if rs.base == nil || !sameFields(fields, rs.base) {
		now := timestamppb.Now()
		override = &v1.Route{Name: name, Meta: &v1.ObjectMeta{CreatedAt: now, UpdatedAt: now}, Spec: fields}
		if rs.override != nil {
			override.Meta.CreatedAt = rs.override.GetMeta().GetCreatedAt()
		}
	}
	if err := m.storeOverride(ctx, name, override); err != nil {
		return nil, err
	}
	next := rs.with(rs.owner, rs.base, rs.own)
	next.override = override
	if err := m.putRouteLocked(ctx, name, rs, next); err != nil {
		if rerr := m.storeOverride(ctx, name, rs.override); rerr != nil {
			return nil, fmt.Errorf("%w, and the previous override was not restored: %w", err, rerr)
		}
		return nil, err
	}
	m.publishViewLocked()
	return proto.Clone(next.route).(*v1.Route), nil
}

// Writes or removes the stored override of a route
func (m *Manager) storeOverride(ctx context.Context, name string, override *v1.Route) error {
	if override == nil {
		_, err := m.db.DeleteRouteOverride(ctx, name)
		return err
	}
	return m.db.PutRouteOverride(ctx, override)
}

// Copies policy, profile, objective and balance from fields into a route spec
func setFields(spec, fields *v1.RouteSpec) {
	spec.Policy = clonePolicy(fields.GetPolicy())
	spec.Profile = cloneProfile(fields.GetProfile())
	spec.Objective = fields.GetObjective()
	spec.Balance = fields.GetBalance()
}

// Whether two specs carry the same policy, profile, objective and balance, an empty message matching an absent one
func sameFields(a, b *v1.RouteSpec) bool {
	return sameMessage(a.GetPolicy(), b.GetPolicy()) && sameMessage(a.GetProfile(), b.GetProfile()) && a.GetObjective() == b.GetObjective() && a.GetBalance() == b.GetBalance()
}

func sameMessage(a, b proto.Message) bool {
	if proto.Size(a) == 0 && proto.Size(b) == 0 {
		return true
	}
	return proto.Equal(a, b)
}

func validBalance(b v1.RouteBalance) bool {
	_, ok := v1.RouteBalance_name[int32(b)]
	return ok
}

// Refuses a user route spec with no rules, bad weights, unknown pools, objective or balance, or a misplaced catch all
func (m *Manager) validRouteLocked(name string, spec *v1.RouteSpec) error {
	if name == "" {
		return fmt.Errorf("%w: route without a name", ErrInvalid)
	}
	if len(spec.GetRules()) == 0 {
		return fmt.Errorf("%w: route %s has no rules", ErrInvalid, name)
	}
	if o := spec.GetObjective(); o != "" && m.st.objectives[o] == nil {
		return fmt.Errorf("%w: route %s names objective %q which does not exist", ErrInvalid, name, o)
	}
	if rps := spec.GetPolicy().GetRequestsPerSecond(); rps < 0 {
		return fmt.Errorf("%w: route %s requests per second %g", ErrInvalid, name, rps)
	}
	if !validBalance(spec.GetBalance()) {
		return fmt.Errorf("%w: route %s balance %d", ErrInvalid, name, spec.GetBalance())
	}
	matches := map[string]bool{}
	for i, r := range spec.GetRules() {
		if r.GetMatch() == "" && i != len(spec.GetRules())-1 {
			return fmt.Errorf("%w: route %s rule %d matches every request but is not the last rule", ErrInvalid, name, i)
		}
		if matches[r.GetMatch()] {
			return fmt.Errorf("%w: route %s matches %q twice", ErrInvalid, name, r.GetMatch())
		}
		matches[r.GetMatch()] = true
		if len(r.GetTargets()) == 0 {
			return fmt.Errorf("%w: route %s rule %d has no targets", ErrInvalid, name, i)
		}
		for _, t := range r.GetTargets() {
			if w := t.GetWeight(); w < minWeight || w > maxWeight {
				return fmt.Errorf("%w: route %s rule %d target %s weight %d is outside %d to %d", ErrInvalid, name, i, t.GetPoolId(), w, minWeight, maxWeight)
			}
			if m.st.pools[datalayer.PoolID(t.GetPoolId())] == nil {
				return fmt.Errorf("%w: route %s rule %d names pool %q which does not exist", ErrInvalid, name, i, t.GetPoolId())
			}
		}
	}
	return nil
}

// The spec a route state renders, a user's as written, an owner's rule with every shared target under the primary's fields
func (rs *routeState) spec() *v1.RouteSpec {
	if rs.owner == (OwnerRef{}) {
		return proto.Clone(rs.user).(*v1.RouteSpec)
	}
	rule := proto.Clone(rs.own).(*v1.RewriteRule)
	for _, o := range slices.SortedFunc(maps.Keys(rs.shared), compareOwners) {
		rule.Targets = append(rule.Targets, proto.Clone(rs.shared[o].target).(*v1.RewriteTarget))
	}
	spec := &v1.RouteSpec{Rules: []*v1.RewriteRule{rule}}
	fields := rs.base
	if rs.override != nil {
		fields = rs.override.GetSpec()
	}
	setFields(spec, fields)
	return spec
}

// The route as its row stores it, an owner route carrying its own rule alone since node owners name it again on import
func (rs *routeState) stored(r *v1.Route) *v1.Route {
	if rs.owner == (OwnerRef{}) || len(rs.shared) == 0 {
		return r
	}
	out := proto.Clone(r).(*v1.Route)
	out.Spec.Rules = []*v1.RewriteRule{proto.Clone(rs.own).(*v1.RewriteRule)}
	return out
}

// Writes a route state, bumping its generation on a spec change, persisting it and emitting its creation or update
func (m *Manager) putRouteLocked(ctx context.Context, name string, cur, rs *routeState) error {
	spec := rs.spec()
	if cur != nil && cur.owner == rs.owner && proto.Equal(cur.route.GetSpec(), spec) && !cur.dirty && cur.route.GetNodeId() == rs.nodeID && cur.shape == rs.shape && slices.Equal(cur.seats, rs.seats) {
		rs.route, rs.conds, rs.parents = cur.route, cur.conds, cur.parents
		m.st.routes[name] = rs
		return nil
	}
	now := timestamppb.Now()
	meta := &v1.ObjectMeta{CreatedAt: now}
	if cur != nil {
		meta = proto.Clone(cur.route.GetMeta()).(*v1.ObjectMeta)
	}
	if cur == nil || !proto.Equal(cur.route.GetSpec(), spec) {
		meta.Generation++
		meta.UpdatedAt = now
	}
	next := &v1.Route{Name: name, Meta: meta, Spec: spec, OwnerKind: rs.owner.Kind, OwnerId: rs.owner.ID, NodeId: rs.nodeID}
	conds := slices.Clone(rs.conds)
	next.Status = m.routeStatusLocked(m.servingLocked(), rs, next, &conds)
	switch {
	case persisted(rs.owner):
		if err := m.db.PutRoute(ctx, rs.stored(next)); err != nil {
			return err
		}
	case cur != nil && persisted(cur.owner):
		if err := m.db.DeleteRoute(ctx, name); err != nil {
			return err
		}
	}
	rs.route, rs.conds, rs.dirty = next, conds, false
	m.st.routes[name] = rs
	action := v1.EventAction_EVENT_ACTION_UPDATED
	if cur == nil {
		action = v1.EventAction_EVENT_ACTION_CREATED
	}
	m.emitLocked(v1.EventKind_EVENT_KIND_ROUTE, action, name, next)
	return nil
}

// Removes a route from the state and database and emits its deletion, a refused database delete keeping it out of resolution until retried
func (m *Manager) deleteRouteLocked(ctx context.Context, name string) {
	rs := m.st.routes[name]
	if err := m.unstoreRouteLocked(ctx, rs); err != nil {
		m.log.Error("route not deleted from the database, retrying on the next change", "route", name, "err", err)
		rs.dropped = err
		conds := slices.Clone(rs.conds)
		next := proto.Clone(rs.route).(*v1.Route)
		next.Status = m.routeStatusLocked(m.servingLocked(), rs, next, &conds)
		rs.route, rs.conds = next, conds
		m.emitLocked(v1.EventKind_EVENT_KIND_ROUTE, v1.EventAction_EVENT_ACTION_UPDATED, name, next)
		return
	}
	delete(m.st.routes, name)
	m.emitLocked(v1.EventKind_EVENT_KIND_ROUTE, v1.EventAction_EVENT_ACTION_DELETED, name, rs.route)
}

// Deletes a route's row and override row from the database
func (m *Manager) unstoreRouteLocked(ctx context.Context, rs *routeState) error {
	if persisted(rs.owner) {
		if err := m.db.DeleteRoute(ctx, rs.route.GetName()); err != nil {
			return err
		}
	}
	if rs.override != nil {
		if _, err := m.db.DeleteRouteOverride(ctx, rs.route.GetName()); err != nil {
			return err
		}
		rs.override = nil
	}
	return nil
}

// Retries every route delete the database refused, then recomputes every route's status, persisting and announcing those that changed
func (m *Manager) refreshRoutesLocked(ctx context.Context) {
	for _, name := range slices.Sorted(maps.Keys(m.st.routes)) {
		rs := m.st.routes[name]
		if rs.dropped == nil {
			continue
		}
		if err := m.unstoreRouteLocked(ctx, rs); err != nil {
			rs.dropped = err
			continue
		}
		delete(m.st.routes, name)
		m.emitLocked(v1.EventKind_EVENT_KIND_ROUTE, v1.EventAction_EVENT_ACTION_DELETED, name, rs.route)
	}
	v := m.servingLocked()
	for _, name := range slices.Sorted(maps.Keys(m.st.routes)) {
		rs := m.st.routes[name]
		conds := slices.Clone(rs.conds)
		status := m.routeStatusLocked(v, rs, rs.route, &conds)
		if proto.Equal(status, rs.route.GetStatus()) && !rs.dirty {
			continue
		}
		next := proto.Clone(rs.route).(*v1.Route)
		next.Status = status
		rs.dirty = false
		if persisted(rs.owner) {
			if err := m.db.PutRoute(ctx, rs.stored(next)); err != nil {
				m.log.Error("route status not stored, retrying on the next change", "route", name, "err", err)
				rs.dirty = true
			}
		}
		rs.route, rs.conds = next, conds
		m.coalesceLocked(eventKey{kind: v1.EventKind_EVENT_KIND_ROUTE, id: name})
	}
}

// Whether a ready healthy endpoint serves a target of the route, judged by the served names and holders in v
func (m *Manager) routeReadyLocked(v *view, rs *routeState, r *v1.Route) bool {
	for _, rr := range r.GetSpec().GetRules() {
		c := &rule{user: rs.owner == (OwnerRef{}), targets: m.ruleTargetsLocked(rs, rr, false)}
		for _, t := range c.targets {
			if m.st.pools[t.pool] != nil && len(m.readyIn(v, m.ruleScope(v, c, t.pool, m.targetModel(t)))) > 0 {
				return true
			}
		}
	}
	return false
}

// The served names and endpoint holders as the state holds them now, what route readiness is judged against
func (m *Manager) servingLocked() *view {
	v := &view{served: m.servedLocked(), holders: make(map[datalayer.EndpointID][]OwnerRef, len(m.st.endpoints))}
	for id, owner := range m.st.endpoints {
		v.holders[id] = m.holdersLocked(owner)
	}
	return v
}

// A route's status now, draining while every target's owner drains, ready while an endpoint serving a target is ready
func (m *Manager) routeStatusLocked(v *view, rs *routeState, r *v1.Route, conds *[]conditions.Condition) *v1.RouteStatus {
	gen := r.GetMeta().GetGeneration()
	status := &v1.RouteStatus{ObservedGeneration: gen, Shape: rs.shape, Seats: slices.Clone(rs.seats)}
	var missing []string
	ready := m.routeReadyLocked(v, rs, r)
	seen := map[string]bool{}
	for _, rule := range r.GetSpec().GetRules() {
		for _, t := range rule.GetTargets() {
			id := t.GetPoolId()
			ps := m.st.pools[datalayer.PoolID(id)]
			if ps == nil {
				missing = append(missing, id)
				continue
			}
			if !seen[id] {
				seen[id] = true
				status.PoolIds = append(status.PoolIds, id)
			}
			if status.Inventory == nil {
				status.Inventory = cloneInventory(ps.spec.GetInventory())
			}
		}
	}
	g := int64(gen)
	accepted := conditions.Condition{Type: ConditionAccepted, Status: conditions.True, Reason: ReasonAccepted, Message: "the route spec is valid", ObservedGeneration: g}
	if rs.dropped != nil {
		accepted.Status, accepted.Reason = conditions.False, ReasonStoreFailed
		accepted.Message = "the route was dropped by " + routeHolder(rs) + " but the database refused to delete it: " + rs.dropped.Error()
	}
	conditions.Set(conds, accepted)
	refs := conditions.Condition{Type: ConditionResolvedRefs, Status: conditions.True, Reason: ReasonResolvedRefs, Message: "every target pool exists", ObservedGeneration: g}
	if len(missing) > 0 {
		refs.Status, refs.Reason, refs.Message = conditions.False, ReasonInvalidPoolRef, "target pools "+strings.Join(missing, ", ")+" do not exist"
	}
	conditions.Set(conds, refs)
	readyCond := conditions.Condition{Type: ConditionReady, Status: conditions.True, Reason: ReasonReady, Message: "an endpoint serving a target is ready", ObservedGeneration: g}
	if !ready {
		readyCond.Status, readyCond.Reason, readyCond.Message = conditions.False, ReasonNoReadyEndpoints, "no endpoint serving a target is ready"
	}
	conditions.Set(conds, readyCond)
	status.Conditions = protoConditions(*conds)
	parents := cloneParents(rs.parents)
	gw := conditions.ParentRef{Kind: "Gateway", Name: m.nodeID}
	for _, c := range []conditions.Condition{accepted, refs, readyCond} {
		if _, err := conditions.SetParent(&parents, GatewayController, gw, c); err != nil {
			panic(fmt.Sprintf("route %s parent status under %s: %v", r.GetName(), m.nodeID, err))
		}
	}
	rs.parents = parents
	status.Parents = protoParents(parents)
	switch {
	case rs.owner != (OwnerRef{}) && m.allDrainingLocked(rs):
		status.State = v1.RouteState_ROUTE_STATE_DRAINING
	case ready:
		status.State = v1.RouteState_ROUTE_STATE_READY
	default:
		status.State = v1.RouteState_ROUTE_STATE_PENDING
	}
	return status
}

// Whether the owner drains
func (m *Manager) drainingLocked(owner OwnerRef) bool {
	os := m.st.owners[owner]
	return os != nil && os.draining
}

// Whether every owner of a route drains
func (m *Manager) allDrainingLocked(rs *routeState) bool {
	if !m.drainingLocked(rs.owner) {
		return false
	}
	for o := range rs.shared {
		if !m.drainingLocked(o) {
			return false
		}
	}
	return true
}

// Whether the route outlives a restart, every route but those imported from another member
func persisted(owner OwnerRef) bool { return owner.Kind != OwnerNode }

// Who holds a route, for refusals
func routeHolder(rs *routeState) string {
	if rs.owner == (OwnerRef{}) {
		return "user authored"
	}
	return "named by " + rs.owner.Kind + " " + rs.owner.ID
}

func clonePolicy(p *v1.Policy) *v1.Policy {
	if p == nil {
		return nil
	}
	return proto.Clone(p).(*v1.Policy)
}

func cloneProfile(p *v1.Profile) *v1.Profile {
	if p == nil {
		return nil
	}
	return proto.Clone(p).(*v1.Profile)
}
