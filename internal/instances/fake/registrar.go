package fake

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sync"

	"github.com/nickheyer/nebu/internal/pools"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/proto"
)

// One route a fake registrar holds
type Route struct {
	Owner   pools.OwnerRef
	Spec    pools.RouteSpec
	Targets []pools.WeightedTarget
	Split   bool
}

// A registrar recording what owners publish and name, for lifecycle tests
type Registrar struct {
	mu        sync.Mutex
	published map[pools.OwnerRef][]pools.Published
	members   map[pools.OwnerRef]pools.Membership
	ready     map[string]bool
	ranks     map[rankKey]bool
	drained   map[pools.OwnerRef]int
	routes    map[string]*Route
	// Objectives each owner holds by name
	objectives map[pools.OwnerRef]map[string]*v1.Objective
	// Every call in order, by method name and owner id
	calls []string
}

// One rank of an instance
type rankKey struct {
	instance string
	rank     uint32
}

// An empty registrar
func NewRegistrar() *Registrar {
	return &Registrar{published: map[pools.OwnerRef][]pools.Published{}, members: map[pools.OwnerRef]pools.Membership{}, ready: map[string]bool{}, ranks: map[rankKey]bool{}, drained: map[pools.OwnerRef]int{}, routes: map[string]*Route{}, objectives: map[pools.OwnerRef]map[string]*v1.Objective{}}
}

func (r *Registrar) record(call string) { r.calls = append(r.calls, call) }

func (r *Registrar) Publish(_ context.Context, owner pools.OwnerRef, m pools.Membership, endpoints []pools.Published) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.record("publish " + owner.ID)
	r.published[owner] = slices.Clone(endpoints)
	r.members[owner] = m
	for _, ep := range endpoints {
		r.ready[ep.InstanceID] = ep.Ready
		r.ranks[rankKey{ep.InstanceID, ep.Rank}] = ep.Ready
	}
	return nil
}

func (r *Registrar) SetReady(_ context.Context, instanceID string, ready bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.record("ready " + instanceID)
	r.ready[instanceID] = ready
	for k := range r.ranks {
		if k.instance == instanceID {
			r.ranks[k] = ready
		}
	}
}

// Flips one rank and counts the instance ready while every rank it published is
func (r *Registrar) SetRankReady(_ context.Context, instanceID string, rank uint32, ready bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.record(fmt.Sprintf("rankready %s %d %v", instanceID, rank, ready))
	r.ranks[rankKey{instanceID, rank}] = ready
	all := true
	for k, v := range r.ranks {
		if k.instance == instanceID && !v {
			all = false
		}
	}
	r.ready[instanceID] = all
}

// Removes the owner's endpoints, keeping its pool while it names routes as the pool manager does
func (r *Registrar) Unpublish(_ context.Context, owner pools.OwnerRef) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.record("unpublish " + owner.ID)
	r.published[owner] = nil
	r.releaseLocked(owner)
}

// Forgets an owner holding neither endpoints nor routes
func (r *Registrar) releaseLocked(owner pools.OwnerRef) {
	if len(r.published[owner]) > 0 {
		return
	}
	for _, rt := range r.routes {
		if rt.Owner == owner {
			return
		}
	}
	delete(r.published, owner)
	delete(r.members, owner)
}

func (r *Registrar) Drain(_ context.Context, owner pools.OwnerRef) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.record("drain " + owner.ID)
	r.drained[owner]++
	for _, ep := range r.published[owner] {
		r.ready[ep.InstanceID] = false
		r.ranks[rankKey{ep.InstanceID, ep.Rank}] = false
	}
}

func (r *Registrar) NameRoute(_ context.Context, owner pools.OwnerRef, name string, spec pools.RouteSpec) (*v1.Route, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.record("route " + name)
	if _, ok := r.members[owner]; !ok {
		return nil, fmt.Errorf("%s %s has published no pool", owner.Kind, owner.ID)
	}
	rt, ok := r.routes[name]
	if ok && rt.Owner != owner {
		return nil, fmt.Errorf("route %s is held by %s %s", name, rt.Owner.Kind, rt.Owner.ID)
	}
	if !ok {
		rt = &Route{Owner: owner}
		r.routes[name] = rt
	}
	rt.Spec, rt.Targets, rt.Split = spec, nil, false
	return &v1.Route{Name: name, OwnerKind: owner.Kind, OwnerId: owner.ID}, nil
}

func (r *Registrar) SplitRoute(_ context.Context, owner pools.OwnerRef, name string, targets []pools.WeightedTarget) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.record("split " + name)
	rt, ok := r.routes[name]
	switch {
	case !ok:
		return fmt.Errorf("route %s is not named", name)
	case rt.Owner != owner:
		return fmt.Errorf("route %s is held by %s %s", name, rt.Owner.Kind, rt.Owner.ID)
	}
	rt.Targets, rt.Split = slices.Clone(targets), true
	return nil
}

func (r *Registrar) DropRoute(_ context.Context, owner pools.OwnerRef, name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.record("droproute " + name)
	if rt, ok := r.routes[name]; ok && rt.Owner == owner {
		delete(r.routes, name)
	}
	r.releaseLocked(owner)
}

func (r *Registrar) DropRoutes(_ context.Context, owner pools.OwnerRef) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.record("droproutes " + owner.ID)
	for name, rt := range r.routes {
		if rt.Owner == owner {
			delete(r.routes, name)
		}
	}
	r.releaseLocked(owner)
}

// Holds an objective for the owner and returns it
func (r *Registrar) PutObjectiveFor(_ context.Context, owner pools.OwnerRef, o *v1.Objective) (*v1.Objective, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.record("objective " + o.GetName())
	if r.objectives[owner] == nil {
		r.objectives[owner] = map[string]*v1.Objective{}
	}
	r.objectives[owner][o.GetName()] = proto.Clone(o).(*v1.Objective)
	return proto.Clone(o).(*v1.Objective), nil
}

func (r *Registrar) DropObjective(_ context.Context, owner pools.OwnerRef, name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.record("dropobjective " + name)
	delete(r.objectives[owner], name)
}

func (r *Registrar) DropObjectives(_ context.Context, owner pools.OwnerRef) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.record("dropobjectives " + owner.ID)
	delete(r.objectives, owner)
}

// The endpoints an owner publishes now
func (r *Registrar) Endpoints(owner pools.OwnerRef) []pools.Published {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.published[owner])
}

// The membership an owner holds a pool under, false once it holds none
func (r *Registrar) Membership(owner pools.OwnerRef) (pools.Membership, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	m, ok := r.members[owner]
	return m, ok
}

// Whether endpoints of an instance take traffic
func (r *Registrar) Ready(instanceID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ready[instanceID]
}

// Whether the endpoints one rank of an instance published take traffic
func (r *Registrar) RankReady(instanceID string, rank uint32) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ranks[rankKey{instanceID, rank}]
}

// Times an owner was drained
func (r *Registrar) Drains(owner pools.OwnerRef) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.drained[owner]
}

// The route a name resolves to as the pool view shows it, false when none is named
func (r *Registrar) Route(name string) (*v1.Route, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rt, ok := r.routes[name]
	if !ok {
		return nil, false
	}
	return &v1.Route{Name: name, OwnerKind: rt.Owner.Kind, OwnerId: rt.Owner.ID}, true
}

// A copy of a route by name, false when none is named
func (r *Registrar) Named(name string) (Route, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rt, ok := r.routes[name]
	if !ok {
		return Route{}, false
	}
	return Route{Owner: rt.Owner, Spec: rt.Spec, Targets: slices.Clone(rt.Targets), Split: rt.Split}, true
}

// Every route name held now
func (r *Registrar) RouteNames() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Sorted(maps.Keys(r.routes))
}

// A copy of every call so far
func (r *Registrar) History() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.calls)
}

// Requests in flight per instance, set by tests and read as a drain waits
type Traffic struct {
	mu         sync.Mutex
	inFlight   map[string]int
	dispatched map[string]uint64
	// Running and queued requests the scrape of each instance reports
	running, queued map[string]int
}

// Traffic with nothing in flight
func NewTraffic() *Traffic {
	return &Traffic{inFlight: map[string]int{}, dispatched: map[string]uint64{}, running: map[string]int{}, queued: map[string]int{}}
}

// Sets the requests in flight on an instance
func (t *Traffic) SetInFlight(id string, n int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.inFlight[id] = n
}

// Counts requests sent to an instance
func (t *Traffic) Dispatch(id string, n uint64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.dispatched[id] += n
}

// Sets the running and queued requests the scrape of an instance reports
func (t *Traffic) SetLoad(id string, running, queued int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.running[id], t.queued[id] = running, queued
}

func (t *Traffic) InFlight(id string) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.inFlight[id]
}

func (t *Traffic) Dispatched(id string) uint64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.dispatched[id]
}

// The scraped running and queued requests of an instance, false when it was never scraped
func (t *Traffic) Load(id string) (running, queued int, scraped bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	running, scraped = t.running[id]
	return running, t.queued[id], scraped
}
