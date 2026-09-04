package gateway

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nickheyer/nebu/internal/db"
	"github.com/nickheyer/nebu/pkg/events"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"golang.org/x/time/rate"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	drainPoll = 50 * time.Millisecond
	// How often at most a route's counters reach the stream while requests flow
	counterFlush = time.Second
)

var (
	// Returned when no route has the name
	ErrNoRoute = errors.New("no route")
	// Returned when the route exists but nothing serves it yet
	ErrPending = errors.New("route pending")
	// Returned when the route is draining
	ErrDraining = errors.New("route draining")
	// Returned when the route has as many requests in flight as its policy allows
	ErrBusy = errors.New("route busy")
	// Returned when the route is over its request rate
	ErrThrottled = errors.New("route throttled")
)

// Public names mapped to instances, persisted, counted, and limited
type Table struct {
	store    *db.DB
	events   *events.Bus
	log      *slog.Logger
	mu       sync.Mutex
	routes   map[string]*v1.Route
	inflight map[string]*atomic.Int32
	limiters map[string]*rate.Limiter
	defaults *v1.Policy
	total    atomic.Uint64
	// Routes whose counters moved since the stream last heard, flushed by one timer
	dirty map[string]bool
	flush *time.Timer
}

// Loads every route from the store
func OpenTable(ctx context.Context, store *db.DB, bus *events.Bus, log *slog.Logger) (*Table, error) {
	if log == nil {
		log = slog.Default()
	}
	t := &Table{store: store, events: bus, log: log, routes: map[string]*v1.Route{}, inflight: map[string]*atomic.Int32{}, limiters: map[string]*rate.Limiter{}, dirty: map[string]bool{}}
	if store == nil {
		return t, nil
	}
	rows, err := store.ListRoutes(ctx)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		// Nothing serves until an instance is adopted or relaunched
		r.InstanceId, r.Endpoint, r.State, r.InFlight = "", "", v1.RouteState_ROUTE_STATE_PENDING, 0
		t.routes[r.GetName()] = r
	}
	return t, nil
}

func (t *Table) save(r *v1.Route, action v1.EventAction) {
	r.UpdatedAt = timestamppb.Now()
	if t.store != nil {
		var err error
		if action == v1.EventAction_EVENT_ACTION_DELETED {
			err = t.store.DeleteRoute(context.Background(), r.GetName())
		} else {
			err = t.store.PutRoute(context.Background(), r)
		}
		if err != nil {
			t.log.Warn("route record write failed", "name", r.GetName(), "err", err)
		}
	}
	t.events.Publish(v1.EventKind_EVENT_KIND_ROUTE, action, r.GetName(), r)
}

// Notes that a route's counters moved, the stream hearing about it once per flush
func (t *Table) touchLocked(name string) {
	t.dirty[name] = true
	if t.flush == nil {
		t.flush = time.AfterFunc(counterFlush, t.publishCounters)
	}
}

func (t *Table) publishCounters() {
	t.mu.Lock()
	defer t.mu.Unlock()
	for name := range t.dirty {
		if r, ok := t.routes[name]; ok {
			t.events.Publish(v1.EventKind_EVENT_KIND_ROUTE, v1.EventAction_EVENT_ACTION_UPDATED, name, t.snapshotLocked(r))
		}
	}
	clear(t.dirty)
	t.flush = nil
}

// Sets the policy routes without one of their own follow
func (t *Table) SetDefaults(p *v1.Policy) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.defaults = p
}

// Returns the policy routes without one of their own follow
func (t *Table) Defaults() *v1.Policy {
	t.mu.Lock()
	defer t.mu.Unlock()
	return proto.Clone(t.defaults).(*v1.Policy)
}

// Resolves a route's policy over the defaults, each zero field inheriting
func Effective(route, defaults *v1.Policy) *v1.Policy {
	pick := func(a, b uint32) uint32 {
		if a != 0 {
			return a
		}
		return b
	}
	out := &v1.Policy{
		MaxInFlight:       pick(route.GetMaxInFlight(), defaults.GetMaxInFlight()),
		RequestsPerSecond: route.GetRequestsPerSecond(),
		Burst:             pick(route.GetBurst(), defaults.GetBurst()),
		RequestTimeoutMs:  pick(route.GetRequestTimeoutMs(), defaults.GetRequestTimeoutMs()),
		UpstreamTimeoutMs: pick(route.GetUpstreamTimeoutMs(), defaults.GetUpstreamTimeoutMs()),
	}
	if out.RequestsPerSecond == 0 {
		out.RequestsPerSecond = defaults.GetRequestsPerSecond()
	}
	return out
}

// Points a name at a ready instance answering to served, the policy is the slot's, nil for none
func (t *Table) Set(name, instanceID, slotID, endpoint, model, served string, api v1.ApiFlavor, policy *v1.Policy) *v1.Route {
	t.mu.Lock()
	defer t.mu.Unlock()
	r, ok := t.routes[name]
	action := v1.EventAction_EVENT_ACTION_UPDATED
	if !ok {
		r = &v1.Route{Name: name}
		t.routes[name] = r
		action = v1.EventAction_EVENT_ACTION_CREATED
	}
	state := v1.RouteState_ROUTE_STATE_READY
	if endpoint == "" {
		state = v1.RouteState_ROUTE_STATE_PENDING
	}
	if slotID == "" {
		slotID = r.GetSlotId()
	}
	// Nothing to write when the route already says exactly this
	if ok && r.GetInstanceId() == instanceID && r.GetEndpoint() == endpoint && r.GetModel() == model && r.GetServed() == served && r.GetApi() == api && r.GetSlotId() == slotID && r.GetState() == state && proto.Equal(r.GetPolicy(), policy) {
		return t.snapshotLocked(r)
	}
	r.InstanceId, r.Endpoint, r.Model, r.Served, r.Api, r.SlotId, r.State = instanceID, endpoint, model, served, api, slotID, state
	r.Policy = proto.Clone(policy).(*v1.Policy)
	t.save(r, action)
	return t.snapshotLocked(r)
}

// Keeps a name alive with nothing behind it
func (t *Table) Pending(name, slotID, model string, policy *v1.Policy) *v1.Route {
	t.mu.Lock()
	defer t.mu.Unlock()
	r, ok := t.routes[name]
	action := v1.EventAction_EVENT_ACTION_UPDATED
	if !ok {
		r = &v1.Route{Name: name}
		t.routes[name] = r
		action = v1.EventAction_EVENT_ACTION_CREATED
	}
	r.InstanceId, r.Endpoint, r.SlotId, r.State = "", "", slotID, v1.RouteState_ROUTE_STATE_PENDING
	r.Policy = proto.Clone(policy).(*v1.Policy)
	if model != "" {
		r.Model = model
	}
	t.save(r, action)
	return t.snapshotLocked(r)
}

// Removes a name entirely, and the instance's counter when no other name shares it
func (t *Table) Delete(name string) (*v1.Route, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	r, ok := t.routes[name]
	if !ok {
		return nil, false
	}
	delete(t.routes, name)
	delete(t.limiters, name)
	shared := false
	for _, other := range t.routes {
		shared = shared || other.GetInstanceId() == r.GetInstanceId()
	}
	if !shared {
		delete(t.inflight, r.GetInstanceId())
	}
	t.save(r, v1.EventAction_EVENT_ACTION_DELETED)
	return t.snapshotLocked(r), true
}

// Marks an instance's routes draining so new requests are refused
func (t *Table) Drain(instanceID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, r := range t.routes {
		if r.GetInstanceId() == instanceID && r.GetState() == v1.RouteState_ROUTE_STATE_READY {
			r.State = v1.RouteState_ROUTE_STATE_DRAINING
			t.save(r, v1.EventAction_EVENT_ACTION_UPDATED)
		}
	}
}

// Detaches an instance, slot routes stay pending and others disappear
func (t *Table) RemoveInstance(instanceID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for name, r := range t.routes {
		if r.GetInstanceId() != instanceID {
			continue
		}
		if r.GetSlotId() != "" {
			r.InstanceId, r.Endpoint, r.State = "", "", v1.RouteState_ROUTE_STATE_PENDING
			t.save(r, v1.EventAction_EVENT_ACTION_UPDATED)
			continue
		}
		delete(t.routes, name)
		delete(t.limiters, name)
		t.save(r, v1.EventAction_EVENT_ACTION_DELETED)
	}
	delete(t.inflight, instanceID)
}

// Returns one route
func (t *Table) Lookup(name string) (*v1.Route, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	r, ok := t.routes[name]
	if !ok {
		return nil, false
	}
	return t.snapshotLocked(r), true
}

// Lists routes by name
func (t *Table) List() []*v1.Route {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]*v1.Route, 0, len(t.routes))
	for _, r := range t.routes {
		out = append(out, t.snapshotLocked(r))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].GetName() < out[j].GetName() })
	return out
}

// Lists routes that answer requests right now
func (t *Table) Ready() []*v1.Route {
	var out []*v1.Route
	for _, r := range t.List() {
		if r.GetState() == v1.RouteState_ROUTE_STATE_READY {
			out = append(out, r)
		}
	}
	return out
}

// Counts requests served through every route
func (t *Table) Requests() uint64 { return t.total.Load() }

// Claims a route for one request under its policy, returning the route as it stands, the policy in force, and release
func (t *Table) Acquire(name string) (*v1.Route, *v1.Policy, func(), error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	r, ok := t.routes[name]
	if !ok {
		return nil, nil, nil, ErrNoRoute
	}
	switch r.GetState() {
	case v1.RouteState_ROUTE_STATE_PENDING:
		return nil, nil, nil, ErrPending
	case v1.RouteState_ROUTE_STATE_DRAINING:
		return nil, nil, nil, ErrDraining
	}
	policy := Effective(r.GetPolicy(), t.defaults)
	counter := t.counterLocked(r.GetInstanceId())
	if cap := policy.GetMaxInFlight(); cap > 0 && counter.Load() >= int32(cap) {
		return nil, nil, nil, ErrBusy
	}
	if policy.GetRequestsPerSecond() > 0 && !t.limiterLocked(name, policy).Allow() {
		return nil, nil, nil, ErrThrottled
	}
	counter.Add(1)
	r.Requests++
	t.total.Add(1)
	t.touchLocked(name)
	release := func() {
		counter.Add(-1)
		t.mu.Lock()
		t.touchLocked(name)
		t.mu.Unlock()
	}
	out := t.snapshotLocked(r)
	if out.GetApi() == v1.ApiFlavor_API_FLAVOR_UNSPECIFIED {
		out.Api = v1.ApiFlavor_API_FLAVOR_OPENAI
	}
	return out, policy, release, nil
}

// Returns the route's token bucket, retuned when its policy changed
func (t *Table) limiterLocked(name string, p *v1.Policy) *rate.Limiter {
	burst := int(p.GetBurst())
	if burst == 0 {
		burst = int(math.Ceil(p.GetRequestsPerSecond()))
	}
	burst = max(burst, 1)
	limit := rate.Limit(p.GetRequestsPerSecond())
	l, ok := t.limiters[name]
	if !ok {
		l = rate.NewLimiter(limit, burst)
		t.limiters[name] = l
		return l
	}
	if l.Limit() != limit {
		l.SetLimit(limit)
	}
	if l.Burst() != burst {
		l.SetBurst(burst)
	}
	return l
}

func (t *Table) counterLocked(instanceID string) *atomic.Int32 {
	c, ok := t.inflight[instanceID]
	if !ok {
		c = &atomic.Int32{}
		t.inflight[instanceID] = c
	}
	return c
}

// Reports requests in flight on an instance
func (t *Table) InFlight(instanceID string) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	if c, ok := t.inflight[instanceID]; ok {
		return int(c.Load())
	}
	return 0
}

// Waits until the instance has nothing in flight or timeout
func (t *Table) WaitDrained(ctx context.Context, instanceID string, limit time.Duration) bool {
	deadline := time.Now().Add(limit)
	for {
		if t.InFlight(instanceID) <= 0 {
			return true
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return false
		}
		time.Sleep(drainPoll)
	}
}

func (t *Table) snapshotLocked(r *v1.Route) *v1.Route {
	out := proto.Clone(r).(*v1.Route)
	if c, ok := t.inflight[r.GetInstanceId()]; ok && r.GetInstanceId() != "" {
		out.InFlight = uint32(max(c.Load(), 0))
	}
	return out
}
