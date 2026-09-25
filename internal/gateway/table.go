package gateway

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"slices"
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
	// Minimum interval between route counter updates.
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

// Persistent route aliases, counters, and limits.
type Table struct {
	store    *db.DB
	events   *events.Bus
	log      *slog.Logger
	mu       sync.Mutex
	routes   map[string]*v1.Route
	inflight map[string]*atomic.Int32
	limiters map[string]*rate.Limiter
	defaults *v1.Policy
	// Chat template probe results for automatic system message handling.
	templates map[string]*v1.TemplateProbe
	// Diffusion capabilities per instance: img_gen and vid_gen.
	modes map[string][]string
	total atomic.Uint64
	// Routes with pending counter updates, flushed by a shared timer.
	dirty map[string]bool
	flush *time.Timer
	// The relay handoff each runtime declares, for relay formation routes
	handoffs func(runtimeID string) string
}

// Loads every route from the store
func OpenTable(ctx context.Context, store *db.DB, bus *events.Bus, log *slog.Logger) (*Table, error) {
	if log == nil {
		log = slog.Default()
	}
	t := &Table{store: store, events: bus, log: log, routes: map[string]*v1.Route{}, inflight: map[string]*atomic.Int32{}, limiters: map[string]*rate.Limiter{}, templates: map[string]*v1.TemplateProbe{}, modes: map[string][]string{}, dirty: map[string]bool{}}
	if store == nil {
		return t, nil
	}
	rows, err := store.ListRoutes(ctx)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		if r.GetSlotId() != "" {
			r.InstanceId = ""
		}
		r.Endpoint, r.State, r.InFlight = "", v1.RouteState_ROUTE_STATE_PENDING, 0
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

// Queues a route counter update for the next flush.
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

// Sets the default route policy.
func (t *Table) SetDefaults(p *v1.Policy) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.defaults = p
}

// Returns the default route policy.
func (t *Table) Defaults() *v1.Policy {
	t.mu.Lock()
	defer t.mu.Unlock()
	return proto.Clone(t.defaults).(*v1.Policy)
}

// Merges route policy with defaults. Zero fields inherit.
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

// Maps a route to a ready instance and its served name, policy, and profile.
func (t *Table) Set(name, instanceID, slotID, endpoint, model, served string, api v1.ApiFlavor, policy *v1.Policy, profile *v1.Profile) *v1.Route {
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
	// Skip unchanged routes.
	if ok && r.GetInstanceId() == instanceID && r.GetEndpoint() == endpoint && r.GetModel() == model && r.GetServed() == served && r.GetApi() == api && r.GetSlotId() == slotID && r.GetState() == state && proto.Equal(r.GetPolicy(), policy) && proto.Equal(r.GetProfile(), profile) && slices.Equal(r.GetModes(), t.modes[instanceID]) {
		return t.snapshotLocked(r)
	}
	r.InstanceId, r.Endpoint, r.Model, r.Served, r.Api, r.SlotId, r.State = instanceID, endpoint, model, served, api, slotID, state
	r.Policy = proto.Clone(policy).(*v1.Policy)
	r.Profile = proto.Clone(profile).(*v1.Profile)
	r.Modes = append([]string(nil), t.modes[instanceID]...)
	t.save(r, action)
	if slotID == "" && state == v1.RouteState_ROUTE_STATE_READY {
		t.readoptLocked(instanceID, endpoint, model, served, api)
	}
	return t.snapshotLocked(r)
}

func (t *Table) readoptLocked(instanceID, endpoint, model, served string, api v1.ApiFlavor) {
	for _, r := range t.routes {
		if r.GetSlotId() != "" || r.GetInstanceId() != instanceID || r.GetState() != v1.RouteState_ROUTE_STATE_PENDING {
			continue
		}
		r.Endpoint, r.Model, r.Served, r.Api, r.State = endpoint, model, served, api, v1.RouteState_ROUTE_STATE_READY
		r.Modes = append([]string(nil), t.modes[instanceID]...)
		t.save(r, v1.EventAction_EVENT_ACTION_UPDATED)
	}
}

// Updates capabilities on all routes for an instance.
func (t *Table) SetModes(instanceID string, modes []string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.modes[instanceID] = append([]string(nil), modes...)
	for _, r := range t.routes {
		if r.GetInstanceId() == instanceID {
			r.Modes = append([]string(nil), modes...)
			t.save(r, v1.EventAction_EVENT_ACTION_UPDATED)
		}
	}
}

// Maps a name to a ready instance, copying slot policy, profile, and template probe.
func (t *Table) Serve(name string, in *v1.Instance, api v1.ApiFlavor, slotID string, policy *v1.Policy, profile *v1.Profile) *v1.Route {
	t.mu.Lock()
	if probe := in.GetTemplate(); probe != nil {
		t.templates[in.GetId()] = proto.Clone(probe).(*v1.TemplateProbe)
	} else {
		delete(t.templates, in.GetId())
	}
	t.mu.Unlock()
	return t.Set(name, in.GetId(), slotID, in.GetEndpoint(), in.GetRepo()+":"+in.GetGroup(), in.GetName(), api, policy, profile)
}

// Uses the route's system message mode. Auto merges later system messages if
// the template probe rejected them, otherwise keeps them.
func (t *Table) SystemMode(r *v1.Route) v1.SystemMessages {
	if mode := r.GetProfile().GetSystemMessages(); mode != v1.SystemMessages_SYSTEM_MESSAGES_UNSPECIFIED {
		return mode
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if probe, ok := t.templates[r.GetInstanceId()]; ok && probe.GetError() == "" && !probe.GetLateSystem() {
		return v1.SystemMessages_SYSTEM_MESSAGES_MERGE
	}
	return v1.SystemMessages_SYSTEM_MESSAGES_KEEP
}

// Keeps a route pending without an instance.
func (t *Table) Pending(name, slotID, model string, policy *v1.Policy, profile *v1.Profile) *v1.Route {
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
	r.Profile = proto.Clone(profile).(*v1.Profile)
	if model != "" {
		r.Model = model
	}
	t.save(r, action)
	return t.snapshotLocked(r)
}

// Moves a route to a new name, refusing one already taken
func (t *Table) Rename(from, to string) (*v1.Route, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if from == to {
		if r, ok := t.routes[from]; ok {
			return t.snapshotLocked(r), nil
		}
		return nil, ErrNoRoute
	}
	if _, taken := t.routes[to]; taken {
		return nil, fmt.Errorf("%q is already a route", to)
	}
	r, ok := t.routes[from]
	if !ok {
		return nil, ErrNoRoute
	}
	delete(t.routes, from)
	delete(t.limiters, from)
	delete(t.dirty, from)
	gone := proto.Clone(r).(*v1.Route)
	t.save(gone, v1.EventAction_EVENT_ACTION_DELETED)
	r.Name = to
	t.routes[to] = r
	t.save(r, v1.EventAction_EVENT_ACTION_CREATED)
	return t.snapshotLocked(r), nil
}

// Removes a route and its counter if no other route shares the instance.
func (t *Table) Delete(name string) (*v1.Route, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.deleteLocked(name)
}

func (t *Table) deleteLocked(name string) (*v1.Route, bool) {
	r, ok := t.routes[name]
	if !ok {
		return nil, false
	}
	delete(t.routes, name)
	delete(t.limiters, name)
	delete(t.dirty, name)
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

// Removes every route keep rejects and returns them.
func (t *Table) Prune(keep func(*v1.Route) bool) []*v1.Route {
	t.mu.Lock()
	defer t.mu.Unlock()
	var gone []*v1.Route
	for name, r := range t.routes {
		if keep(t.snapshotLocked(r)) {
			continue
		}
		if removed, ok := t.deleteLocked(name); ok {
			gone = append(gone, removed)
		}
	}
	sort.Slice(gone, func(i, j int) bool { return gone[i].GetName() < gone[j].GetName() })
	return gone
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

// Detaches an instance. Slot routes stay pending, other routes are removed.
func (t *Table) RemoveInstance(instanceID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for name, r := range t.routes {
		if r.GetInstanceId() != instanceID {
			continue
		}
		if r.GetSlotId() != "" {
			r.InstanceId, r.Endpoint, r.State, r.Modes = "", "", v1.RouteState_ROUTE_STATE_PENDING, nil
			t.save(r, v1.EventAction_EVENT_ACTION_UPDATED)
			continue
		}
		delete(t.routes, name)
		delete(t.limiters, name)
		t.save(r, v1.EventAction_EVENT_ACTION_DELETED)
	}
	delete(t.inflight, instanceID)
	delete(t.templates, instanceID)
	delete(t.modes, instanceID)
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

// Lists ready routes.
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

// Claims a route under its effective policy and returns a release function.
// Token counts are excluded from served request counters.
func (t *Table) Acquire(name string, served bool) (*v1.Route, *v1.Policy, func(), error) {
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
	counter := t.counterLocked(counterKey(r))
	if cap := routeCap(r, policy); cap > 0 && counter.Load() >= int32(cap) {
		return nil, nil, nil, ErrBusy
	}
	if policy.GetRequestsPerSecond() > 0 && !t.limiterLocked(name, policy).Allow() {
		return nil, nil, nil, ErrThrottled
	}
	counter.Add(1)
	if served {
		r.Requests++
		t.total.Add(1)
	}
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

// Returns the route's token bucket, updating it after policy changes.
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

// Requests a route may have in flight at once: the policy's cap, and for replicas the cap on
// every ready seat, since each seat takes the cap on its own
func routeCap(r *v1.Route, policy *v1.Policy) uint32 {
	cap := policy.GetMaxInFlight()
	if cap == 0 || r.GetShape() != v1.Shape_SHAPE_REPLICAS {
		return cap
	}
	ready := uint32(0)
	for _, s := range r.GetSeats() {
		if s.GetState() == v1.InstanceState_INSTANCE_STATE_READY && s.GetEndpoint() != "" {
			ready++
		}
	}
	return cap * max(ready, 1)
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
	if c, ok := t.inflight[counterKey(r)]; ok {
		out.InFlight = uint32(max(c.Load(), 0))
	}
	for _, s := range out.GetSeats() {
		if c, ok := t.inflight[s.GetInstanceId()]; ok && s.GetInstanceId() != "" {
			s.InFlight = uint32(max(c.Load(), 0))
		}
	}
	return out
}
