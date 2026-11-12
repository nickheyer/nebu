package formations

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strconv"

	"github.com/nickheyer/nebu/internal/instances"
	"github.com/nickheyer/nebu/internal/pools"
	"github.com/nickheyer/nebu/pkg/chattemplate"
	"github.com/nickheyer/nebu/pkg/datalayer"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/requestcontrol"
	"github.com/nickheyer/nebu/pkg/runtimes"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Condition types a formation reports
const (
	CondPlanned  = "Planned"
	CondLaunched = "Launched"
	CondReady    = "Ready"
	CondDegraded = "Degraded"
)

// Reasons the formation conditions carry
const (
	ReasonPlanned       = "Planned"
	ReasonLaunching     = "Launching"
	ReasonSeatsReady    = "SeatsReady"
	ReasonRouted        = "Routed"
	ReasonServingWhole  = "ServingWhole"
	ReasonLaunchFailed  = "LaunchFailed"
	ReasonStopping      = "Stopping"
	ReasonStopped       = "Stopped"
	ReasonSeatFailed    = "SeatFailed"
	ReasonFormationDown = "FormationDown"
)

// The owner a formation publishes its seats and names its routes under
func ownerOf(id string) pools.OwnerRef { return pools.OwnerRef{Kind: pools.OwnerFormation, ID: id} }

// The spec a formation names its route with, its request policy and profile, its shape and its seats
func routeSpec(f *v1.Formation) pools.RouteSpec {
	return pools.RouteSpec{Policy: f.GetRequest().GetPolicy(), Profile: f.GetRequest().GetRouteProfile(), Shape: f.GetShape(), Seats: pools.SeatIDs(f)}
}

// Whether a formation takes traffic, whole or with a replica out
func serving(s v1.FormationState) bool {
	return s == v1.FormationState_FORMATION_STATE_READY || s == v1.FormationState_FORMATION_STATE_DEGRADED
}

// Marks a formation ready at its generation with every seat serving
func markReady(r *v1.Formation) {
	gen := r.GetGeneration()
	r.State, r.ReadyAt, r.Error, r.ObservedGeneration = v1.FormationState_FORMATION_STATE_READY, timestamppb.Now(), "", gen
	r.Conditions = instances.SetCondition(r.Conditions, CondLaunched, true, ReasonSeatsReady, fmt.Sprintf("%d seats ready", len(r.GetSeats())), gen)
	r.Conditions = instances.SetCondition(r.Conditions, CondReady, true, ReasonRouted, "", gen)
	r.Conditions = instances.SetCondition(r.Conditions, CondDegraded, false, ReasonServingWhole, "", gen)
}

// Whether a seat of the role answers requests over HTTP, so the gateway sends to it
func seatServes(rt runtimes.Runtime, shape v1.Shape, role string) bool {
	r, err := runtimes.RoleOf(rt, &v1.SeatSpec{Shape: shape, Role: role})
	if err != nil || r.Helper {
		return false
	}
	return r.Health.Kind == runtimes.HealthHTTP || r.Health.Kind == runtimes.HealthCanary
}

// The data layer role a relay seat takes, empty for every other shape
func endpointRole(shape v1.Shape, role string) string {
	if shape != v1.Shape_SHAPE_RELAY {
		return ""
	}
	switch role {
	case runtimes.RolePrefill:
		return datalayer.RolePrefill
	case runtimes.RoleDecode:
		return datalayer.RoleDecode
	}
	return ""
}

// A seat's instance record from this node or its own
func (m *Manager) seatRecord(ctx context.Context, s *v1.Seat) (*v1.Instance, error) {
	if s.GetNodeId() == m.self() {
		return m.Instances.Get(s.GetInstanceId())
	}
	return m.remoteSeat(ctx, s.GetNodeId(), s.GetInstanceId())
}

// Where the conductor reaches each seat rank and whether it is healthy, the seat endpoint for one rank
func rankEndpoints(in *v1.Instance, remote bool) ([]string, []bool) {
	if len(in.GetRanks()) == 0 {
		return []string{seatEndpoint(in, remote)}, []bool{true}
	}
	var out []string
	var healthy []bool
	for _, r := range in.GetRanks() {
		endpoint := r.GetEndpoint()
		if remote {
			endpoint = "http://" + net.JoinHostPort(hostOf(in.GetSeat().GetAddress()), strconv.Itoa(int(r.GetPort())))
		}
		out = append(out, endpoint)
		healthy = append(healthy, r.GetHealthy())
	}
	return out, healthy
}

// Whether a seat is the prefill side of a relay whose runtime hands off over SGLang's bootstrap room
func bootstrapPrefill(rt runtimes.Runtime, shape v1.Shape, role string) bool {
	p := rt.Policy()
	return shape == v1.Shape_SHAPE_RELAY && role == runtimes.RolePrefill && p != nil && p.Shapes != nil && p.Shapes.Handoff == requestcontrol.HandoffSGLangBootstrap
}

// Endpoints the gateway reaches a ready seat at, one per rank, on the gRPC port when it serves gRPC
func (m *Manager) seatPublished(f *v1.Formation, rt runtimes.Runtime, s *v1.Seat, in *v1.Instance) ([]pools.Published, error) {
	remote := s.GetNodeId() != m.self()
	if remote && !in.GetSeat().GetExposed() && rt.AppProtocol(in.GetParams()) == v1.AppProtocol_APP_PROTOCOL_GRPC {
		return nil, fmt.Errorf("%s on %s speaks gRPC behind a guard listener that forwards its HTTP port alone, expose the mesh to serve it", s.GetRole(), s.GetNodeName())
	}
	endpoints, healthy := rankEndpoints(in, remote)
	spec := &v1.SeatSpec{Shape: f.GetShape(), Rank: s.GetRank()}
	out := make([]pools.Published, 0, len(endpoints))
	for rank, endpoint := range endpoints {
		u, err := url.Parse(endpoint)
		if err != nil {
			return nil, fmt.Errorf("%s on %s: %w", s.GetRole(), s.GetNodeName(), err)
		}
		port, err := strconv.Atoi(u.Port())
		if err != nil {
			return nil, fmt.Errorf("%s on %s: endpoint %s names no port", s.GetRole(), s.GetNodeName(), u.String())
		}
		ep, err := instances.ServingEndpoint(rt, in.GetParams(), in.GetId(), datalayer.Address{Host: u.Hostname(), Port: port})
		if err != nil {
			return nil, err
		}
		ep.NodeID = s.GetNodeId()
		ep.Role = endpointRole(f.GetShape(), s.GetRole())
		ep.Rank = instances.PublishedRank(spec, len(endpoints), rank)
		ep.Ready = healthy[rank]
		ep.Labels = map[string]string{instances.LabelServed: f.GetName(), datalayer.LabelFormation: f.GetId()}
		if width := rt.ExpertParallel(in.GetParams()); width > 1 {
			ep.Labels[datalayer.LabelExpertParallel] = strconv.Itoa(width)
		}
		if bootstrapPrefill(rt, f.GetShape(), s.GetRole()) {
			ep.Labels[requestcontrol.LabelBootstrapPort] = strconv.FormatUint(uint64(in.GetSeat().GetAuxPort()), 10)
		}
		out = append(out, ep)
	}
	return out, nil
}

// Labels every seat endpoint with the reasoning settings of the template a head seat recorded, the seats serving one model
func labelReasoning(eps []pools.Published, seats []*v1.Instance) {
	for _, in := range seats {
		if tp := in.GetTemplate(); tp != nil {
			for i := range eps {
				chattemplate.SetLabels(eps[i].Labels, tp)
			}
			return
		}
	}
}

// Publishes every ready serving seat under the formation's owner except those in skip, unpublishing when none serves
func (m *Manager) publishSeats(ctx context.Context, id string, skip map[string]bool) error {
	f, err := m.Get(id)
	if err != nil {
		return err
	}
	rt, err := m.Runtimes.Get(f.GetRuntimeId())
	if err != nil {
		return err
	}
	var eps []pools.Published
	var first *v1.Instance
	var served []*v1.Instance
	records := map[string]*v1.Instance{}
	for _, s := range f.GetSeats() {
		if skip[seatKey(s)] || s.GetInstanceId() == "" || s.GetState() != v1.InstanceState_INSTANCE_STATE_READY || !seatServes(rt, f.GetShape(), s.GetRole()) {
			continue
		}
		in, err := m.seatRecord(ctx, s)
		if err != nil {
			return fmt.Errorf("%s on %s: %w", s.GetRole(), s.GetNodeName(), err)
		}
		records[seatKey(s)] = in
		served = append(served, in)
		seatEps, err := m.seatPublished(f, rt, s, in)
		if err != nil {
			return err
		}
		if first == nil {
			first = in
		}
		eps = append(eps, seatEps...)
	}
	owner := ownerOf(id)
	if len(eps) == 0 {
		m.Registrar.Unpublish(ctx, owner)
		return nil
	}
	labelReasoning(eps, served)
	membership, err := instances.MembershipOf(rt, first, f.GetPlan().GetSchedulerConfig(), m.Instances.AcceleratorOf(ctx, first))
	if err != nil {
		return err
	}
	if membership.Inventory, err = m.formationInventory(ctx, f, first, records, skip); err != nil {
		return fmt.Errorf("inventory of %s: %w", f.GetName(), err)
	}
	membership.Slot = f.GetSlotId()
	if err := m.Registrar.Publish(ctx, owner, membership, eps); err != nil {
		return fmt.Errorf("publish %s: %w", f.GetName(), err)
	}
	return nil
}

// One replica's inventory for replicas, else the serving seat's with the devices and slowest class of every seat
func (m *Manager) formationInventory(ctx context.Context, f *v1.Formation, first *v1.Instance, records map[string]*v1.Instance, skip map[string]bool) (*v1.RuntimeInventory, error) {
	if first.GetInventory() == nil {
		return nil, fmt.Errorf("seat %s reports no inventory", first.GetName())
	}
	if f.GetShape() == v1.Shape_SHAPE_REPLICAS {
		return first.GetInventory(), nil
	}
	var launches []*v1.RuntimeInventory
	for _, s := range f.GetSeats() {
		if skip[seatKey(s)] {
			continue
		}
		if s.GetInstanceId() == "" {
			return nil, fmt.Errorf("%s on %s has no instance", s.GetRole(), s.GetNodeName())
		}
		in, ok := records[seatKey(s)]
		if !ok {
			var err error
			if in, err = m.seatRecord(ctx, s); err != nil {
				return nil, fmt.Errorf("%s on %s: %w", s.GetRole(), s.GetNodeName(), err)
			}
		}
		if in.GetInventory() == nil {
			return nil, fmt.Errorf("%s on %s reports no inventory", s.GetRole(), s.GetNodeName())
		}
		launches = append(launches, in.GetInventory())
	}
	return runtimes.SpanInventory(first.GetInventory(), launches)
}

// Removes a formation's endpoints and every route it named
func (m *Manager) withdraw(ctx context.Context, id string) {
	owner := ownerOf(id)
	m.Registrar.Unpublish(ctx, owner)
	m.Registrar.DropRoutes(ctx, owner)
}
