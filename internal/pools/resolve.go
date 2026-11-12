package pools

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"sort"

	"github.com/nickheyer/nebu/pkg/datalayer"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/requestcontrol"
)

// A pool serving a name, whether it is an adapter there, and the owners serving it
type scope struct {
	pool    datalayer.PoolID
	adapter bool
	any     bool
	owners  []OwnerRef
}

// Whether an endpoint serves for an owner of the scope
func (s scope) serves(v *view, ep datalayer.Endpoint) bool {
	if s.any {
		return true
	}
	for _, h := range v.holders[ep.ID()] {
		if slices.Contains(s.owners, h) {
			return true
		}
	}
	return false
}

// Whether a request woke an owner of the scope and no endpoint serving for it joined since
func (s scope) waking(v *view) bool {
	pv := v.pools[s.pool]
	if pv == nil {
		return false
	}
	if s.any {
		return len(pv.waking) > 0
	}
	for _, o := range s.owners {
		if pv.waking[o] {
			return true
		}
	}
	return false
}

// The slot and instance owners a wake may launch, all of the pool's when any owner serves the scope
func (s scope) wakeable(v *view) []OwnerRef {
	if s.any {
		if pv := v.pools[s.pool]; pv != nil {
			return pv.wake
		}
		return nil
	}
	var out []OwnerRef
	for _, o := range s.owners {
		if o.Kind == OwnerSlot || o.Kind == OwnerInstance {
			out = append(out, o)
		}
	}
	return out
}

// The ready healthy endpoints of the scope
func (m *Manager) readyIn(v *view, s scope) []datalayer.Endpoint {
	return m.store.Endpoints(s.pool, func(ep datalayer.Endpoint) bool { return datalayer.ReadyAndHealthy(ep) && s.serves(v, ep) })
}

// Resolves a client model through user exact rules, owner routes, the user catch all, then the names pools serve
func (m *Manager) Resolve(ctx context.Context, clientModel string) (datalayer.Pool, string, error) {
	v := m.view.Load()
	var r *rule
	if clientModel != "" {
		r = v.exact[clientModel]
	}
	if r == nil {
		r = v.catchAll
	}
	if r == nil {
		if clientModel == "" {
			return datalayer.Pool{}, "", fmt.Errorf("%w: no catch all route applies", requestcontrol.ErrNoModel)
		}
		pool, _, err := m.lookup(ctx, v, clientModel)
		return pool, clientModel, err
	}
	switch {
	case r.draining:
		return datalayer.Pool{}, "", fmt.Errorf("%w: route %s is draining", requestcontrol.ErrNoReadyEndpoints, r.route)
	case len(r.targets) == 0:
		return datalayer.Pool{}, "", fmt.Errorf("%w: route %s has no targets", requestcontrol.ErrNoReadyEndpoints, r.route)
	}
	t := m.draw(ctx, v, r)
	pool, ok := m.store.Pool(t.pool)
	if !ok {
		return datalayer.Pool{}, "", fmt.Errorf("%w: route %s sends to pool %s which does not exist", requestcontrol.ErrNoReadyEndpoints, r.route, t.pool)
	}
	model := m.targetModel(t)
	if err := m.awaken(ctx, v, m.ruleScope(v, r, pool.ID, model)); err != nil {
		return datalayer.Pool{}, "", err
	}
	return pool, model, nil
}

// The model name a target writes into requests, its pool's model when it names none
func (m *Manager) targetModel(t target) string {
	if t.model != "" {
		return t.model
	}
	pool, _ := m.store.Pool(t.pool)
	return pool.Model
}

// What serves a model resolved in a pool, the owners of the client model's owner route, else the name's owners
func (m *Manager) Scope(clientModel string, pool datalayer.PoolID, model string) requestcontrol.Scope {
	v := m.view.Load()
	var r *rule
	if clientModel != "" {
		r = v.exact[clientModel]
	}
	s := m.ruleScope(v, r, pool, model)
	out := requestcontrol.Scope{Waking: func() bool { return s.waking(m.view.Load()) }}
	if !s.any {
		out.Serves = func(ep datalayer.Endpoint) bool { return s.serves(m.view.Load(), ep) }
	}
	if s.adapter {
		out.Adapter = model
	}
	return out
}

// Owners serving a rule's model in a pool, its targets' owners there else all, by name for user rules
func (m *Manager) ruleScope(v *view, r *rule, pool datalayer.PoolID, model string) scope {
	s := nameScope(v, pool, model)
	if r == nil || r.user {
		return s
	}
	var there, every []OwnerRef
	for _, t := range r.targets {
		if !slices.Contains(every, t.owner) {
			every = append(every, t.owner)
		}
		if t.pool == pool && m.targetModel(t) == model && !slices.Contains(there, t.owner) {
			there = append(there, t.owner)
		}
	}
	if len(there) == 0 {
		there = every
	}
	return scope{pool: pool, adapter: s.adapter, owners: there}
}

// The owners serving a name in a pool, every owner when it is the pool's model or none is known
func nameScope(v *view, pool datalayer.PoolID, model string) scope {
	for _, s := range v.served[model] {
		if s.pool == pool {
			return s
		}
	}
	return scope{pool: pool, any: true}
}

// Draws a target by the rule's balance among those a ready endpoint serves, among all when none is served
func (m *Manager) draw(ctx context.Context, v *view, r *rule) target {
	scopes := make([]scope, len(r.targets))
	for i, t := range r.targets {
		scopes[i] = m.ruleScope(v, r, t.pool, m.targetModel(t))
	}
	if r.balance == v1.RouteBalance_ROUTE_BALANCE_FREE_CAPACITY {
		var ids []datalayer.EndpointID
		for _, s := range scopes {
			for _, ep := range m.readyIn(v, s) {
				ids = append(ids, ep.ID())
			}
		}
		defer m.metrics.Deciding(ctx, ids)()
	}
	weights := make([]int, len(r.targets))
	anyReady := false
	for i, t := range r.targets {
		ready := len(m.readyIn(v, scopes[i]))
		if ready == 0 {
			continue
		}
		anyReady = true
		weights[i] = t.weight * max(m.balanceFactor(v, r.balance, scopes[i], ready), 1)
	}
	if !anyReady {
		for i, t := range r.targets {
			weights[i] = t.weight
		}
	}
	cum := make([]int, len(weights))
	total := 0
	for i, w := range weights {
		total += w
		cum[i] = total
	}
	x := rand.IntN(total)
	return r.targets[sort.Search(len(cum), func(i int) bool { return cum[i] > x })]
}

// What a target's static weight is scaled by, one for static weights, the ready endpoint count, or the free request capacity
func (m *Manager) balanceFactor(v *view, balance v1.RouteBalance, s scope, ready int) int {
	switch balance {
	case v1.RouteBalance_ROUTE_BALANCE_READY_ENDPOINTS:
		return ready
	case v1.RouteBalance_ROUTE_BALANCE_FREE_CAPACITY:
		return m.freeCapacity(v, s)
	}
	return 1
}

// Requests the scope's ready healthy endpoints can take beyond those queued, running and assumed, from the scraped metrics and capacities
func (m *Manager) freeCapacity(v *view, s scope) int {
	free := 0
	for _, ep := range m.readyIn(v, s) {
		capacity, _ := datalayer.Attribute[int](ep, datalayer.CollectorCapacity)
		busy := 0
		if sm, ok := datalayer.Attribute[*datalayer.ServerMetrics](ep, datalayer.CollectorMetrics); ok {
			busy += sm.QueuedRequests + sm.RunningRequests
		}
		if n, ok := datalayer.Attribute[int](ep, datalayer.CollectorAssumed); ok {
			busy += n
		}
		if capacity <= 0 {
			capacity = busy + 1
		}
		free += max(capacity-busy, 0)
	}
	return free
}

// Wakes a slot or instance of the scope when no endpoint in its pool serves it, failing when none wakes
func (m *Manager) awaken(ctx context.Context, v *view, s scope) error {
	if len(m.store.Endpoints(s.pool, func(ep datalayer.Endpoint) bool { return s.serves(v, ep) })) > 0 {
		return nil
	}
	wake := s.wakeable(v)
	if len(wake) == 0 {
		return fmt.Errorf("%w: pool %s has no endpoints serving the request and no slot or instance to wake", requestcontrol.ErrNoReadyEndpoints, s.pool)
	}
	box := m.waker.Load()
	if box == nil || box.w == nil {
		return fmt.Errorf("%w: pool %s has no endpoints serving the request and no waker is set to launch their owners", requestcontrol.ErrNoReadyEndpoints, s.pool)
	}
	var errs []error
	for _, owner := range wake {
		err := box.w.Wake(ctx, owner)
		if err == nil {
			m.markWaking(s.pool, owner)
			return nil
		}
		errs = append(errs, fmt.Errorf("wake %s %s: %w", owner.Kind, owner.ID, err))
	}
	return fmt.Errorf("%w: pool %s has no endpoints serving the request: %w", requestcontrol.ErrNoReadyEndpoints, s.pool, errors.Join(errs...))
}

// Records that a request woke an owner of the pool, until an endpoint serving for it joins or leaves
func (m *Manager) markWaking(id datalayer.PoolID, owner OwnerRef) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ps := m.st.pools[id]
	if ps == nil || ps.waking[owner] || m.joinedForLocked(ps, owner) {
		return
	}
	if ps.waking == nil {
		ps.waking = map[OwnerRef]bool{}
	}
	ps.waking[owner] = true
	m.publishViewLocked()
}

// Whether the pool holds an endpoint serving for the owner
func (m *Manager) joinedForLocked(ps *poolState, owner OwnerRef) bool {
	for _, o := range ps.owners {
		if os := m.st.owners[o]; os != nil && len(os.joined) > 0 && slices.Contains(m.holdersLocked(o), owner) {
			return true
		}
	}
	return false
}

// Forgets the wakes of the owners a publisher's endpoints serve for
func (m *Manager) wokenLocked(ps *poolState, publisher OwnerRef) {
	for _, h := range m.holdersLocked(publisher) {
		delete(ps.waking, h)
	}
}

// The only pool with a ready endpoint and its model, false unless exactly one has one
func (m *Manager) Default(ctx context.Context) (datalayer.Pool, string, bool) {
	var found datalayer.Pool
	n := 0
	for _, p := range m.store.Pools() {
		if len(m.store.Endpoints(p.ID, datalayer.ReadyAndHealthy)) == 0 {
			continue
		}
		if n++; n > 1 {
			return datalayer.Pool{}, "", false
		}
		found = p
	}
	if n != 1 || found.Model == "" {
		return datalayer.Pool{}, "", false
	}
	return found, found.Model, true
}

func (m *Manager) Lookup(ctx context.Context, model string) (datalayer.Pool, string, error) {
	return m.lookup(ctx, m.view.Load(), model)
}

// The pool serving a name and the adapter it selects there, several pools sharing it by ready endpoints serving it
func (m *Manager) lookup(ctx context.Context, v *view, model string) (datalayer.Pool, string, error) {
	var pools []datalayer.Pool
	var scopes []scope
	for _, s := range v.served[model] {
		if p, ok := m.store.Pool(s.pool); ok {
			pools = append(pools, p)
			scopes = append(scopes, s)
		}
	}
	if len(pools) == 0 {
		return datalayer.Pool{}, "", fmt.Errorf("%w: %q", requestcontrol.ErrUnknownModel, model)
	}
	i := m.byReadiness(v, scopes)
	if err := m.awaken(ctx, v, scopes[i]); err != nil {
		return datalayer.Pool{}, "", err
	}
	adapter := ""
	if scopes[i].adapter {
		adapter = model
	}
	return pools[i], adapter, nil
}

// An index into scopes drawn in proportion to ready endpoints serving each, the first when none has one
func (m *Manager) byReadiness(v *view, scopes []scope) int {
	if len(scopes) == 1 {
		return 0
	}
	cum := make([]int, len(scopes))
	total := 0
	for i, s := range scopes {
		total += len(m.readyIn(v, s))
		cum[i] = total
	}
	if total == 0 {
		return 0
	}
	x := rand.IntN(total)
	return sort.Search(len(cum), func(i int) bool { return cum[i] > x })
}
