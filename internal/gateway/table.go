package gateway

import (
	"context"
	"errors"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nickheyer/nebu/internal/db"
	"github.com/nickheyer/nebu/pkg/events"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const drainPoll = 50 * time.Millisecond

var (
	// Returned when no route has the name
	ErrNoRoute = errors.New("no route")
	// Returned when the route exists but nothing serves it yet
	ErrPending = errors.New("route pending")
	// Returned when the route is draining
	ErrDraining = errors.New("route draining")
)

// Public names mapped to instances, persisted and counted
type Table struct {
	store    *db.DB
	events   *events.Bus
	mu       sync.Mutex
	routes   map[string]*v1.Route
	inflight map[string]*atomic.Int32
	total    atomic.Uint64
}

// Loads every route from the store
func OpenTable(ctx context.Context, store *db.DB, bus *events.Bus) (*Table, error) {
	t := &Table{store: store, events: bus, routes: map[string]*v1.Route{}, inflight: map[string]*atomic.Int32{}}
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
			t.events.Publish(v1.EventKind_EVENT_KIND_ROUTE, action, r.GetName(), &v1.Event_Route{Route: r})
			return
		}
	}
	t.events.Publish(v1.EventKind_EVENT_KIND_ROUTE, action, r.GetName(), &v1.Event_Route{Route: r})
}

// Points a name at a ready instance
func (t *Table) Set(name, instanceID, slotID, endpoint, model string, api v1.ApiFlavor) *v1.Route {
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
	if ok && r.GetInstanceId() == instanceID && r.GetEndpoint() == endpoint && r.GetModel() == model && r.GetApi() == api && r.GetSlotId() == slotID && r.GetState() == state {
		return t.snapshotLocked(r)
	}
	r.InstanceId, r.Endpoint, r.Model, r.Api, r.SlotId, r.State = instanceID, endpoint, model, api, slotID, state
	t.save(r, action)
	return t.snapshotLocked(r)
}

// Keeps a name alive with nothing behind it
func (t *Table) Pending(name, slotID, model string) *v1.Route {
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
	if model != "" {
		r.Model = model
	}
	t.save(r, action)
	return t.snapshotLocked(r)
}

// Removes a name entirely
func (t *Table) Delete(name string) (*v1.Route, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	r, ok := t.routes[name]
	if !ok {
		return nil, false
	}
	delete(t.routes, name)
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

// Claims a route for one request, returning endpoint and release
func (t *Table) Acquire(name string) (string, func(), error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	r, ok := t.routes[name]
	if !ok {
		return "", nil, ErrNoRoute
	}
	switch r.GetState() {
	case v1.RouteState_ROUTE_STATE_PENDING:
		return "", nil, ErrPending
	case v1.RouteState_ROUTE_STATE_DRAINING:
		return "", nil, ErrDraining
	}
	counter := t.counterLocked(r.GetInstanceId())
	counter.Add(1)
	r.Requests++
	t.total.Add(1)
	release := func() { counter.Add(-1) }
	return r.GetEndpoint(), release, nil
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
