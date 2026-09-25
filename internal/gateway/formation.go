package gateway

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/proto"
)

// The counter a route's requests in flight are kept under: its instance, or the route itself
// when its requests are counted on the seats that serve them, or forwarded to another node
func counterKey(r *v1.Route) string {
	if r.GetInstanceId() != "" && !servedBySeats(r) {
		return r.GetInstanceId()
	}
	return "route:" + r.GetName()
}

// Installs the lookup of a runtime's relay handoff, the one its shape facts declare
func (t *Table) SetHandoffs(fn func(runtimeID string) string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.handoffs = fn
}

func (t *Table) handoffLocked(runtimeID string) string {
	if t.handoffs == nil {
		return ""
	}
	return t.handoffs(runtimeID)
}

// Sequence slots a llama.cpp relay seat serves at once, from the formation's request
func relaySlots(f *v1.Formation) uint32 {
	n, err := strconv.ParseUint(f.GetRequest().GetParams()["n_parallel"], 10, 32)
	if err != nil || n == 0 {
		return 1
	}
	return uint32(n)
}

// Maps a formation's route to its head on this node, with the seats the gateway sends to
func (t *Table) ServeFormation(f *v1.Formation, head *v1.Instance, api v1.ApiFlavor, seats []*v1.RouteSeat, policy *v1.Policy, profile *v1.Profile) *v1.Route {
	return t.ServeFormationAs(f.GetName(), f, head, api, seats, policy, profile)
}

// Maps a name to a formation's head on this node, a slot's alias among them
func (t *Table) ServeFormationAs(name string, f *v1.Formation, head *v1.Instance, api v1.ApiFlavor, seats []*v1.RouteSeat, policy *v1.Policy, profile *v1.Profile) *v1.Route {
	t.mu.Lock()
	defer t.mu.Unlock()
	if probe := head.GetTemplate(); probe != nil {
		t.templates[head.GetId()] = proto.Clone(probe).(*v1.TemplateProbe)
	} else {
		delete(t.templates, head.GetId())
	}
	r, ok := t.routes[name]
	action := v1.EventAction_EVENT_ACTION_UPDATED
	if !ok {
		r = &v1.Route{Name: name}
		t.routes[name] = r
		action = v1.EventAction_EVENT_ACTION_CREATED
	}
	r.InstanceId, r.Endpoint, r.Model, r.Served, r.Api = head.GetId(), head.GetEndpoint(), f.GetRepo()+":"+f.GetGroup(), head.GetName(), api
	r.State = v1.RouteState_ROUTE_STATE_READY
	r.SlotId = f.GetSlotId()
	r.NodeId, r.Forwarded = "", false
	r.Shape, r.FormationId, r.RuntimeId = f.GetShape(), f.GetId(), f.GetRuntimeId()
	r.RelayBreakEvenPrompt = f.GetPlan().GetRelayBreakEvenPrompt()
	r.Affinity = proto.Clone(f.GetPlan().GetAffinity()).(*v1.AffinityPolicy)
	r.RelayHandoff = ""
	if f.GetShape() == v1.Shape_SHAPE_RELAY {
		r.RelayHandoff = t.handoffLocked(f.GetRuntimeId())
	}
	slots := relaySlots(f)
	r.Seats = nil
	for _, s := range seats {
		seat := proto.Clone(s).(*v1.RouteSeat)
		if seat.GetSlots() == 0 {
			seat.Slots = slots
		}
		r.Seats = append(r.Seats, seat)
	}
	if policy != nil {
		r.Policy = proto.Clone(policy).(*v1.Policy)
	}
	if profile != nil {
		r.Profile = proto.Clone(profile).(*v1.Profile)
	}
	r.Modes = append([]string(nil), t.modes[head.GetId()]...)
	t.save(r, action)
	return t.snapshotLocked(r)
}

// Adds a route for a formation another member conducts, its requests going to that member's
// gateway. A ready formation's route is ready, a degraded one's draining, a starting one's
// pending. A name a route here already holds for something else is a collision the caller
// records on the formation, and a formation without an endpoint cannot be forwarded
func (t *Table) Forward(f *v1.Formation, api v1.ApiFlavor) (*v1.Route, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	name := f.GetName()
	if f.GetEndpoint() == "" {
		return nil, fmt.Errorf("formation %s on %s has no gateway endpoint to forward to", name, f.GetConductorName())
	}
	r, ok := t.routes[name]
	action := v1.EventAction_EVENT_ACTION_UPDATED
	switch {
	case !ok:
		r = &v1.Route{Name: name}
		t.routes[name] = r
		action = v1.EventAction_EVENT_ACTION_CREATED
	case !r.GetForwarded():
		return nil, fmt.Errorf("route %s is served on this node, so formation %s on %s is not forwarded under that name", name, f.GetId(), f.GetConductorName())
	case r.GetFormationId() != "" && r.GetFormationId() != f.GetId():
		return nil, fmt.Errorf("route %s is forwarded to formation %s on %s, so formation %s on %s is not forwarded under that name", name, r.GetFormationId(), r.GetNodeId(), f.GetId(), f.GetConductorName())
	case r.GetFormationId() == "" && r.GetNodeId() != f.GetConductor():
		return nil, fmt.Errorf("route %s is forwarded to %s, so formation %s on %s is not forwarded under that name", name, r.GetNodeId(), f.GetId(), f.GetConductorName())
	}
	r.InstanceId, r.SlotId = "", ""
	r.Endpoint, r.NodeId, r.Forwarded = f.GetEndpoint(), f.GetConductor(), true
	r.Model, r.Api = f.GetRepo()+":"+f.GetGroup(), api
	r.Shape, r.FormationId, r.RuntimeId = f.GetShape(), f.GetId(), f.GetRuntimeId()
	r.RelayBreakEvenPrompt = f.GetPlan().GetRelayBreakEvenPrompt()
	r.Affinity = proto.Clone(f.GetPlan().GetAffinity()).(*v1.AffinityPolicy)
	r.Seats = nil
	for _, s := range f.GetSeats() {
		r.Seats = append(r.Seats, &v1.RouteSeat{NodeId: s.GetNodeId(), InstanceId: s.GetInstanceId(), Endpoint: s.GetEndpoint(), Role: s.GetRole(), Rank: s.GetRank(), State: s.GetState(), Address: s.GetAddress(), AuxPort: s.GetAuxPort()})
	}
	switch f.GetState() {
	case v1.FormationState_FORMATION_STATE_READY:
		r.State = v1.RouteState_ROUTE_STATE_READY
	case v1.FormationState_FORMATION_STATE_DEGRADED:
		r.State = v1.RouteState_ROUTE_STATE_DRAINING
	default:
		r.State = v1.RouteState_ROUTE_STATE_PENDING
	}
	t.save(r, action)
	return t.snapshotLocked(r), nil
}

// The gateway base a member's record reaches it at
func gatewayBase(node *v1.Node) string {
	if node.GetAddress() == "" {
		return ""
	}
	if node.GetTls() {
		return "https://" + node.GetAddress()
	}
	return "http://" + node.GetAddress()
}

// Keeps the routes of a member's instances on this gateway, from its record: every ready route
// it serves is forwarded to its gateway, a draining one drains here, and one it no longer
// serves leaves. Formation routes are the conductor's record's, kept by Forward. A name a route
// here holds for something else is a collision, returned with the rest
func (t *Table) ForwardNode(node *v1.Node) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	base := gatewayBase(node)
	if base == "" {
		return fmt.Errorf("%s has no mesh address, so its routes are not forwarded", nodeLabel(node))
	}
	keep := map[string]bool{}
	var errs []error
	for _, src := range node.GetRoutes() {
		if src.GetForwarded() || src.GetFormationId() != "" {
			continue
		}
		state := v1.RouteState_ROUTE_STATE_UNSPECIFIED
		switch src.GetState() {
		case v1.RouteState_ROUTE_STATE_READY:
			state = v1.RouteState_ROUTE_STATE_READY
		case v1.RouteState_ROUTE_STATE_DRAINING:
			state = v1.RouteState_ROUTE_STATE_DRAINING
		default:
			continue
		}
		name := src.GetName()
		r, ok := t.routes[name]
		action := v1.EventAction_EVENT_ACTION_UPDATED
		switch {
		case !ok:
			r = &v1.Route{Name: name}
			t.routes[name] = r
			action = v1.EventAction_EVENT_ACTION_CREATED
		case !r.GetForwarded():
			errs = append(errs, fmt.Errorf("route %s is served on this node, so %s's route of that name is not forwarded", name, nodeLabel(node)))
			continue
		case r.GetFormationId() != "" || r.GetNodeId() != node.GetId():
			errs = append(errs, fmt.Errorf("route %s is forwarded to %s, so %s's route of that name is not forwarded", name, r.GetNodeId(), nodeLabel(node)))
			continue
		}
		keep[name] = true
		r.InstanceId, r.SlotId = "", ""
		r.Endpoint, r.NodeId, r.Forwarded = base, node.GetId(), true
		r.Model, r.Served, r.Api, r.State = src.GetModel(), src.GetServed(), src.GetApi(), state
		r.Shape, r.FormationId, r.RuntimeId = src.GetShape(), "", src.GetRuntimeId()
		r.RelayBreakEvenPrompt, r.RelayHandoff = 0, ""
		r.Affinity = nil
		r.Seats = nil
		r.Modes = append([]string(nil), src.GetModes()...)
		r.Policy = proto.Clone(src.GetPolicy()).(*v1.Policy)
		r.Profile = proto.Clone(src.GetProfile()).(*v1.Profile)
		t.save(r, action)
	}
	for name, r := range t.routes {
		if r.GetForwarded() && r.GetFormationId() == "" && r.GetNodeId() == node.GetId() && !keep[name] {
			t.deleteLocked(name)
		}
	}
	return errors.Join(errs...)
}

// Removes every route forwarded to a member
func (t *Table) RemoveNode(nodeID string) []*v1.Route {
	t.mu.Lock()
	defer t.mu.Unlock()
	var gone []*v1.Route
	for name, r := range t.routes {
		if !r.GetForwarded() || r.GetNodeId() != nodeID {
			continue
		}
		if removed, ok := t.deleteLocked(name); ok {
			gone = append(gone, removed)
		}
	}
	sort.Slice(gone, func(i, j int) bool { return gone[i].GetName() < gone[j].GetName() })
	return gone
}

// Follows the members' records on the event bus, forwarding the routes of every member here
// and dropping a member's routes when it leaves or goes quiet
func (t *Table) Follow(ctx context.Context) {
	sub := t.events.Subscribe(ctx, []v1.EventKind{v1.EventKind_EVENT_KIND_NODE})
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case ev := <-sub.Events():
				t.follow(ev)
			}
		}
	}()
}

// Takes one node event into the routes
func (t *Table) follow(ev *v1.Event) {
	node := ev.GetNode()
	if node == nil || node.GetSelf() || node.GetId() == "" {
		return
	}
	if ev.GetAction() == v1.EventAction_EVENT_ACTION_DELETED || node.GetState() == v1.NodeState_NODE_STATE_UNREACHABLE || node.GetState() == v1.NodeState_NODE_STATE_GONE {
		if gone := t.RemoveNode(node.GetId()); len(gone) > 0 {
			t.log.Info("routes of a member dropped", "node", nodeLabel(node), "routes", len(gone))
		}
		return
	}
	if err := t.ForwardNode(node); err != nil {
		t.log.Warn("routes of a member not forwarded", "node", nodeLabel(node), "err", err)
	}
}

func nodeLabel(node *v1.Node) string {
	if node.GetName() != "" {
		return node.GetName()
	}
	return node.GetId()
}

// Removes every route of a formation
func (t *Table) RemoveFormation(id string) []*v1.Route {
	t.mu.Lock()
	defer t.mu.Unlock()
	var gone []*v1.Route
	for name, r := range t.routes {
		if r.GetFormationId() != id {
			continue
		}
		if removed, ok := t.deleteLocked(name); ok {
			gone = append(gone, removed)
		}
	}
	sort.Slice(gone, func(i, j int) bool { return gone[i].GetName() < gone[j].GetName() })
	return gone
}

// Marks a formation's routes draining, so new requests are refused while its seats stop
func (t *Table) DrainFormation(id string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, r := range t.routes {
		if r.GetFormationId() == id && r.GetState() == v1.RouteState_ROUTE_STATE_READY {
			r.State = v1.RouteState_ROUTE_STATE_DRAINING
			t.save(r, v1.EventAction_EVENT_ACTION_UPDATED)
		}
	}
}

// Updates one seat's state on the routes of its formation
func (t *Table) SetSeatState(formationID, instanceID string, state v1.InstanceState) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, r := range t.routes {
		if r.GetFormationId() != formationID {
			continue
		}
		changed := false
		for _, s := range r.GetSeats() {
			if s.GetInstanceId() == instanceID && s.GetState() != state {
				s.State = state
				changed = true
			}
		}
		if changed {
			t.save(r, v1.EventAction_EVENT_ACTION_UPDATED)
		}
	}
}

// Requests in flight on one seat
func (t *Table) SeatInFlight(instanceID string) int {
	return t.InFlight(instanceID)
}

// Counts a request in flight on a seat, returning the release
func (t *Table) HoldSeat(instanceID string) func() {
	t.mu.Lock()
	c := t.counterLocked(instanceID)
	t.mu.Unlock()
	c.Add(1)
	return func() { c.Add(-1) }
}
