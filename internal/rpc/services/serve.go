package services

import (
	"context"
	"errors"
	"slices"

	"connectrpc.com/connect"
	"github.com/nickheyer/nebu/internal/instances"
	"github.com/nickheyer/nebu/internal/slots"
	"github.com/nickheyer/nebu/internal/tasks"
	"github.com/nickheyer/nebu/pkg/events"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/proto/nebu/v1/nebuv1connect"
)

var (
	_ nebuv1connect.InstanceServiceHandler = (*InstanceService)(nil)
	_ nebuv1connect.SlotServiceHandler     = (*SlotService)(nil)
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

func (s *InstanceService) PreviewRun(ctx context.Context, req *connect.Request[v1.PreviewRunRequest]) (*connect.Response[v1.PreviewRunResponse], error) {
	command, params, err := s.instances.Preview(ctx, req.Msg.GetRun())
	return reply(&v1.PreviewRunResponse{Command: command, Params: params}, err)
}

func (s *InstanceService) ListInstances(ctx context.Context, req *connect.Request[v1.ListInstancesRequest]) (*connect.Response[v1.ListInstancesResponse], error) {
	list := s.instances.List(req.Msg.GetRunningOnly())
	if state := req.Msg.GetState(); state != v1.InstanceState_INSTANCE_STATE_UNSPECIFIED {
		list = keep(list, func(in *v1.Instance) bool { return in.GetState() == state })
	}
	list = match(list, req.Msg.GetQuery(), func(in *v1.Instance) []string {
		return []string{in.GetName(), in.GetRepo(), in.GetGroup(), in.GetRuntimeId(), in.GetError()}
	})
	list, next, err := page(list, req.Msg.GetCursor(), req.Msg.GetLimit())
	return reply(&v1.ListInstancesResponse{Instances: list, NextCursor: next}, err)
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

func (s *InstanceService) LoadAdapter(ctx context.Context, req *connect.Request[v1.LoadAdapterRequest]) (*connect.Response[v1.LoadAdapterResponse], error) {
	if err := s.instances.LoadAdapter(ctx, req.Msg.GetId(), req.Msg.GetName(), req.Msg.GetPath()); err != nil {
		return nil, wrap(err)
	}
	in, err := s.instances.Get(req.Msg.GetId())
	return reply(&v1.LoadAdapterResponse{Instance: in}, err)
}

func (s *InstanceService) UnloadAdapter(ctx context.Context, req *connect.Request[v1.UnloadAdapterRequest]) (*connect.Response[v1.UnloadAdapterResponse], error) {
	if err := s.instances.UnloadAdapter(ctx, req.Msg.GetId(), req.Msg.GetName()); err != nil {
		return nil, wrap(err)
	}
	in, err := s.instances.Get(req.Msg.GetId())
	return reply(&v1.UnloadAdapterResponse{Instance: in}, err)
}

func (s *InstanceService) ListAdapters(ctx context.Context, req *connect.Request[v1.ListAdaptersRequest]) (*connect.Response[v1.ListAdaptersResponse], error) {
	list, err := s.instances.ListAdapters(ctx, req.Msg.GetId())
	if err != nil {
		return nil, wrap(err)
	}
	resp := &v1.ListAdaptersResponse{}
	for _, a := range list {
		resp.Adapters = append(resp.Adapters, &v1.LoadedAdapter{Name: a.Name, Path: a.Path})
	}
	return reply(resp, nil)
}

// Serves slots and swaps
type SlotService struct {
	slots *slots.Manager
}

func NewSlotService(m *slots.Manager) *SlotService {
	return &SlotService{slots: m}
}

func (s *SlotService) ListSlots(ctx context.Context, req *connect.Request[v1.ListSlotsRequest]) (*connect.Response[v1.ListSlotsResponse], error) {
	list := s.slots.List()
	if state := req.Msg.GetState(); state != v1.SlotState_SLOT_STATE_UNSPECIFIED {
		list = keep(list, func(sl *v1.Slot) bool { return sl.GetState() == state })
	}
	list = match(list, req.Msg.GetQuery(), func(sl *v1.Slot) []string {
		return []string{sl.GetName(), sl.GetDescription(), sl.GetRuntimeId(), sl.GetError()}
	})
	list, next, err := page(list, req.Msg.GetCursor(), req.Msg.GetLimit())
	return reply(&v1.ListSlotsResponse{Slots: list, NextCursor: next}, err)
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

// Serves task listing and watching
type TaskService struct {
	tasks *tasks.Manager
}

func NewTaskService(m *tasks.Manager) *TaskService {
	return &TaskService{tasks: m}
}

func (s *TaskService) ListTasks(ctx context.Context, req *connect.Request[v1.ListTasksRequest]) (*connect.Response[v1.ListTasksResponse], error) {
	list := s.tasks.List(req.Msg.GetActiveOnly())
	if kinds := req.Msg.GetKinds(); len(kinds) > 0 {
		list = keep(list, func(t *v1.Task) bool { return slices.Contains(kinds, t.GetKind()) })
	}
	if states := req.Msg.GetStates(); len(states) > 0 {
		list = keep(list, func(t *v1.Task) bool { return slices.Contains(states, t.GetState()) })
	}
	list = match(list, req.Msg.GetQuery(), func(t *v1.Task) []string { return []string{t.GetKind(), t.GetTitle(), t.GetError()} })
	list, next, err := page(list, req.Msg.GetCursor(), req.Msg.GetLimit())
	return reply(&v1.ListTasksResponse{Tasks: list, NextCursor: next}, err)
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
type Snapshotter func(ctx context.Context, kinds []v1.EventKind) ([]*v1.Event, error)

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
		list, err := s.snapshot(ctx, req.Msg.GetKinds())
		if err != nil {
			return wrap(err)
		}
		for _, ev := range list {
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
