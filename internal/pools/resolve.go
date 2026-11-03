package pools

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"sort"

	"github.com/nickheyer/nebu/pkg/datalayer"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/requestcontrol"
)

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
	t := m.draw(r)
	pool, ok := m.store.Pool(t.pool)
	if !ok {
		return datalayer.Pool{}, "", fmt.Errorf("%w: route %s sends to pool %s which does not exist", requestcontrol.ErrNoReadyEndpoints, r.route, t.pool)
	}
	model := t.model
	if model == "" {
		model = pool.Model
	}
	if err := m.awaken(ctx, v, pool.ID); err != nil {
		return datalayer.Pool{}, "", err
	}
	return pool, model, nil
}

// Draws a target among those with a ready healthy endpoint, or among all when none has one, by the rule's balance
func (m *Manager) draw(r *rule) target {
	weights := make([]int, len(r.targets))
	anyReady := false
	for i, t := range r.targets {
		ready := len(m.store.Endpoints(t.pool, datalayer.ReadyAndHealthy))
		if ready == 0 {
			continue
		}
		anyReady = true
		weights[i] = t.weight * max(m.balanceFactor(r.balance, t.pool, ready), 1)
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
func (m *Manager) balanceFactor(balance v1.RouteBalance, pool datalayer.PoolID, ready int) int {
	switch balance {
	case v1.RouteBalance_ROUTE_BALANCE_READY_ENDPOINTS:
		return ready
	case v1.RouteBalance_ROUTE_BALANCE_FREE_CAPACITY:
		return m.freeCapacity(pool)
	}
	return 1
}

// Requests the pool's ready healthy endpoints can take beyond those queued, running and assumed, from the scraped metrics and capacities
func (m *Manager) freeCapacity(pool datalayer.PoolID) int {
	free := 0
	for _, ep := range m.store.Endpoints(pool, datalayer.ReadyAndHealthy) {
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

// Wakes the slot and instance owners of a pool with no endpoints so a request can wait, failing when none wakes
func (m *Manager) awaken(ctx context.Context, v *view, id datalayer.PoolID) error {
	if len(m.store.Endpoints(id, nil)) > 0 {
		return nil
	}
	pv := v.pools[id]
	if pv == nil || len(pv.wake) == 0 {
		return fmt.Errorf("%w: pool %s has no endpoints and no slot or instance to wake", requestcontrol.ErrNoReadyEndpoints, id)
	}
	box := m.waker.Load()
	if box == nil || box.w == nil {
		return fmt.Errorf("%w: pool %s has no endpoints and no waker is set to launch its owners", requestcontrol.ErrNoReadyEndpoints, id)
	}
	var errs []error
	for _, owner := range pv.wake {
		err := box.w.Wake(ctx, owner)
		if err == nil {
			m.markWaking(id)
			return nil
		}
		errs = append(errs, fmt.Errorf("wake %s %s: %w", owner.Kind, owner.ID, err))
	}
	return fmt.Errorf("%w: pool %s has no endpoints: %w", requestcontrol.ErrNoReadyEndpoints, id, errors.Join(errs...))
}

// Records that an owner of the pool was woken, cleared once an endpoint joins or the pool goes
func (m *Manager) markWaking(id datalayer.PoolID) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ps := m.st.pools[id]
	if ps == nil || ps.waking {
		return
	}
	ps.waking = true
	m.publishViewLocked()
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

// The pool serving a name and the adapter it selects there, several pools sharing it by ready endpoint count
func (m *Manager) lookup(ctx context.Context, v *view, model string) (datalayer.Pool, string, error) {
	var pools []datalayer.Pool
	var adapters []bool
	for _, b := range v.served[model] {
		if p, ok := m.store.Pool(b.pool); ok {
			pools = append(pools, p)
			adapters = append(adapters, b.adapter)
		}
	}
	if len(pools) == 0 {
		return datalayer.Pool{}, "", fmt.Errorf("%w: %q", requestcontrol.ErrUnknownModel, model)
	}
	i := m.byReadiness(pools)
	if err := m.awaken(ctx, v, pools[i].ID); err != nil {
		return datalayer.Pool{}, "", err
	}
	adapter := ""
	if adapters[i] {
		adapter = model
	}
	return pools[i], adapter, nil
}

// An index into pools drawn in proportion to ready endpoints, the first pool when none has one
func (m *Manager) byReadiness(pools []datalayer.Pool) int {
	if len(pools) == 1 {
		return 0
	}
	cum := make([]int, len(pools))
	total := 0
	for i, p := range pools {
		total += len(m.store.Endpoints(p.ID, datalayer.ReadyAndHealthy))
		cum[i] = total
	}
	if total == 0 {
		return 0
	}
	x := rand.IntN(total)
	return sort.Search(len(cum), func(i int) bool { return cum[i] > x })
}
