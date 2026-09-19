package services

import (
	"context"
	"errors"
	"fmt"

	"connectrpc.com/connect"
	"github.com/nickheyer/nebu/internal/gateway"
	"github.com/nickheyer/nebu/internal/instances"
	"github.com/nickheyer/nebu/internal/slots"
	"github.com/nickheyer/nebu/internal/tasks"
	"github.com/nickheyer/nebu/pkg/events"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/proto/nebu/v1/nebuv1connect"
	"github.com/nickheyer/nebu/pkg/runtimes"
)

var (
	_ nebuv1connect.InstanceServiceHandler = (*InstanceService)(nil)
	_ nebuv1connect.SlotServiceHandler     = (*SlotService)(nil)
	_ nebuv1connect.GatewayServiceHandler  = (*GatewayService)(nil)
	_ nebuv1connect.TaskServiceHandler     = (*TaskService)(nil)
	_ nebuv1connect.EventServiceHandler    = (*EventService)(nil)
)

// Serves running models
type InstanceService struct {
	instances *instances.Manager
}

func NewInstanceService(m *instances.Manager) *InstanceService {
	return &InstanceService{instances: m}
}

func (s *InstanceService) Run(ctx context.Context, req *connect.Request[v1.RunRequest]) (*connect.Response[v1.RunResponse], error) {
	in, task, err := s.instances.Run(ctx, req.Msg)
	return reply(&v1.RunResponse{Instance: in, Task: task}, err)
}

func (s *InstanceService) ListInstances(ctx context.Context, req *connect.Request[v1.ListInstancesRequest]) (*connect.Response[v1.ListInstancesResponse], error) {
	return reply(&v1.ListInstancesResponse{Instances: s.instances.List(req.Msg.GetRunningOnly())}, nil)
}

func (s *InstanceService) GetInstance(ctx context.Context, req *connect.Request[v1.GetInstanceRequest]) (*connect.Response[v1.GetInstanceResponse], error) {
	in, err := s.instances.Get(req.Msg.GetId())
	return reply(&v1.GetInstanceResponse{Instance: in}, err)
}

func (s *InstanceService) StopInstance(ctx context.Context, req *connect.Request[v1.StopInstanceRequest]) (*connect.Response[v1.StopInstanceResponse], error) {
	in, err := s.instances.Stop(ctx, req.Msg.GetId())
	return reply(&v1.StopInstanceResponse{Instance: in}, err)
}

func (s *InstanceService) Logs(ctx context.Context, req *connect.Request[v1.LogsRequest], stream *connect.ServerStream[v1.LogsResponse]) error {
	return wrap(s.instances.Logs(ctx, req.Msg.GetId(), req.Msg.GetFollow(), int(req.Msg.GetTail()), func(lines []string) error {
		return stream.Send(&v1.LogsResponse{Lines: lines})
	}))
}

// Serves slots and swaps
type SlotService struct {
	slots *slots.Manager
}

func NewSlotService(m *slots.Manager) *SlotService {
	return &SlotService{slots: m}
}

func (s *SlotService) ListSlots(ctx context.Context, req *connect.Request[v1.ListSlotsRequest]) (*connect.Response[v1.ListSlotsResponse], error) {
	return reply(&v1.ListSlotsResponse{Slots: s.slots.List()}, nil)
}

func (s *SlotService) GetSlot(ctx context.Context, req *connect.Request[v1.GetSlotRequest]) (*connect.Response[v1.GetSlotResponse], error) {
	slot, in, err := s.slots.Get(req.Msg.GetId())
	return reply(&v1.GetSlotResponse{Slot: slot, Instance: in}, err)
}

func (s *SlotService) CreateSlot(ctx context.Context, req *connect.Request[v1.CreateSlotRequest]) (*connect.Response[v1.CreateSlotResponse], error) {
	slot, err := s.slots.Create(ctx, req.Msg)
	return reply(&v1.CreateSlotResponse{Slot: slot}, err)
}

func (s *SlotService) UpdateSlot(ctx context.Context, req *connect.Request[v1.UpdateSlotRequest]) (*connect.Response[v1.UpdateSlotResponse], error) {
	slot, err := s.slots.Update(ctx, req.Msg)
	return reply(&v1.UpdateSlotResponse{Slot: slot}, err)
}

func (s *SlotService) DeleteSlot(ctx context.Context, req *connect.Request[v1.DeleteSlotRequest]) (*connect.Response[v1.DeleteSlotResponse], error) {
	slot, err := s.slots.Delete(ctx, req.Msg.GetId(), req.Msg.GetForce())
	return reply(&v1.DeleteSlotResponse{Slot: slot}, err)
}

func (s *SlotService) Swap(ctx context.Context, req *connect.Request[v1.SwapRequest]) (*connect.Response[v1.SwapResponse], error) {
	slot, in, task, err := s.slots.Swap(ctx, req.Msg)
	return reply(&v1.SwapResponse{Slot: slot, Instance: in, Task: task}, err)
}

func (s *SlotService) EvictSlot(ctx context.Context, req *connect.Request[v1.EvictSlotRequest]) (*connect.Response[v1.EvictSlotResponse], error) {
	slot, err := s.slots.Evict(ctx, req.Msg.GetId())
	return reply(&v1.EvictSlotResponse{Slot: slot}, err)
}

func (s *SlotService) RelaunchSlot(ctx context.Context, req *connect.Request[v1.RelaunchSlotRequest]) (*connect.Response[v1.RelaunchSlotResponse], error) {
	slot, in, task, err := s.slots.Relaunch(ctx, req.Msg.GetId())
	return reply(&v1.RelaunchSlotResponse{Slot: slot, Instance: in, Task: task}, err)
}

// Serves routes and gateway status
type GatewayService struct {
	gateway   *gateway.Gateway
	instances *instances.Manager
}

func NewGatewayService(g *gateway.Gateway, m *instances.Manager) *GatewayService {
	return &GatewayService{gateway: g, instances: m}
}

func (s *GatewayService) GetGatewayStatus(ctx context.Context, req *connect.Request[v1.GetGatewayStatusRequest]) (*connect.Response[v1.GetGatewayStatusResponse], error) {
	return reply(&v1.GetGatewayStatusResponse{Status: s.gateway.Status()}, nil)
}

func (s *GatewayService) ListRoutes(ctx context.Context, req *connect.Request[v1.ListRoutesRequest]) (*connect.Response[v1.ListRoutesResponse], error) {
	return reply(&v1.ListRoutesResponse{Routes: s.gateway.Table().List()}, nil)
}

// Adds an alias for a ready instance, reserving slot names for slots.
func (s *GatewayService) SetRoute(ctx context.Context, req *connect.Request[v1.SetRouteRequest]) (*connect.Response[v1.SetRouteResponse], error) {
	in, err := s.instances.Get(req.Msg.GetInstanceId())
	if err != nil {
		return nil, wrap(err)
	}
	if in.GetState() != v1.InstanceState_INSTANCE_STATE_READY {
		return nil, wrap(fmt.Errorf("%w: instance %s is not ready", runtimes.ErrParam, in.GetName()))
	}
	if err := s.slotless(req.Msg.GetName(), "belongs to a slot"); err != nil {
		return nil, err
	}
	route := s.gateway.Table().Serve(req.Msg.GetName(), in, s.instances.Runtimes.API(in.GetRuntimeId()), "", req.Msg.GetPolicy(), req.Msg.GetProfile())
	return reply(&v1.SetRouteResponse{Route: route}, nil)
}

func (s *GatewayService) ListTraces(ctx context.Context, req *connect.Request[v1.ListTracesRequest]) (*connect.Response[v1.ListTracesResponse], error) {
	return reply(&v1.ListTracesResponse{Traces: s.gateway.Traces().List(req.Msg.GetRoute(), int(req.Msg.GetLimit()))}, nil)
}

func (s *GatewayService) GetTrace(ctx context.Context, req *connect.Request[v1.GetTraceRequest]) (*connect.Response[v1.GetTraceResponse], error) {
	t, ok := s.gateway.Traces().Get(req.Msg.GetId())
	if !ok {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("no trace %q", req.Msg.GetId()))
	}
	return reply(&v1.GetTraceResponse{Trace: t}, nil)
}

func (s *GatewayService) DeleteRoute(ctx context.Context, req *connect.Request[v1.DeleteRouteRequest]) (*connect.Response[v1.DeleteRouteResponse], error) {
	if err := s.slotless(req.Msg.GetName(), "belongs to a slot, delete the slot instead"); err != nil {
		return nil, err
	}
	route, ok := s.gateway.Table().Delete(req.Msg.GetName())
	if !ok {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("no route %q", req.Msg.GetName()))
	}
	return reply(&v1.DeleteRouteResponse{Route: route}, nil)
}

// Rejects route names owned by slots.
func (s *GatewayService) slotless(name, why string) error {
	if r, ok := s.gateway.Table().Lookup(name); ok && r.GetSlotId() != "" {
		return wrap(fmt.Errorf("%w: %s %s", runtimes.ErrParam, name, why))
	}
	return nil
}

// Serves task listing and watching
type TaskService struct {
	tasks *tasks.Manager
}

func NewTaskService(m *tasks.Manager) *TaskService {
	return &TaskService{tasks: m}
}

func (s *TaskService) ListTasks(ctx context.Context, req *connect.Request[v1.ListTasksRequest]) (*connect.Response[v1.ListTasksResponse], error) {
	return reply(&v1.ListTasksResponse{Tasks: s.tasks.List(req.Msg.GetActiveOnly())}, nil)
}

func (s *TaskService) GetTask(ctx context.Context, req *connect.Request[v1.GetTaskRequest]) (*connect.Response[v1.GetTaskResponse], error) {
	task, logs, err := s.tasks.Get(req.Msg.GetId())
	return reply(&v1.GetTaskResponse{Task: task, Logs: logs}, err)
}

func (s *TaskService) WatchTask(ctx context.Context, req *connect.Request[v1.WatchTaskRequest], stream *connect.ServerStream[v1.WatchTaskResponse]) error {
	return wrap(s.tasks.Watch(ctx, req.Msg.GetId(), stream.Send))
}

func (s *TaskService) CancelTask(ctx context.Context, req *connect.Request[v1.CancelTaskRequest]) (*connect.Response[v1.CancelTaskResponse], error) {
	task, err := s.tasks.Cancel(req.Msg.GetId())
	return reply(&v1.CancelTaskResponse{Task: task}, err)
}

// Produces the current state as events for a fresh subscriber
type Snapshotter func(ctx context.Context, kinds []v1.EventKind) []*v1.Event

// Streams live updates
type EventService struct {
	bus      *events.Bus
	snapshot Snapshotter
}

func NewEventService(bus *events.Bus, snapshot Snapshotter) *EventService {
	return &EventService{bus: bus, snapshot: snapshot}
}

func (s *EventService) WatchEvents(ctx context.Context, req *connect.Request[v1.WatchEventsRequest], stream *connect.ServerStream[v1.WatchEventsResponse]) error {
	sub := s.bus.Subscribe(ctx, req.Msg.GetKinds())
	if req.Msg.GetSnapshot() && s.snapshot != nil {
		for _, ev := range s.snapshot(ctx, req.Msg.GetKinds()) {
			if err := stream.Send(&v1.WatchEventsResponse{Event: ev}); err != nil {
				return err
			}
		}
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case ev := <-sub.Events():
			if err := stream.Send(&v1.WatchEventsResponse{Event: ev}); err != nil {
				return err
			}
			// Dropped deletes require clients to reload a snapshot.
			if sub.Dropped() > 0 {
				return connect.NewError(connect.CodeResourceExhausted, errors.New("event stream fell behind, subscribe again with a snapshot"))
			}
		}
	}
}
