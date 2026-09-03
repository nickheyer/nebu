package services

import (
	"context"

	"connectrpc.com/connect"
	"github.com/nickheyer/nebu/internal/tasks"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/proto/nebu/v1/nebuv1connect"
)

var _ nebuv1connect.TaskServiceHandler = (*TaskService)(nil)

// Serves task listing and watching
type TaskService struct {
	tasks *tasks.Manager
}

// Builds the task service
func NewTaskService(m *tasks.Manager) *TaskService {
	return &TaskService{tasks: m}
}

func (s *TaskService) ListTasks(ctx context.Context, req *connect.Request[v1.ListTasksRequest]) (*connect.Response[v1.ListTasksResponse], error) {
	return connect.NewResponse(&v1.ListTasksResponse{Tasks: s.tasks.List(req.Msg.GetActiveOnly())}), nil
}

func (s *TaskService) GetTask(ctx context.Context, req *connect.Request[v1.GetTaskRequest]) (*connect.Response[v1.GetTaskResponse], error) {
	task, logs, err := s.tasks.Get(req.Msg.GetId())
	if err != nil {
		return nil, wrap(err)
	}
	return connect.NewResponse(&v1.GetTaskResponse{Task: task, Logs: logs}), nil
}

func (s *TaskService) WatchTask(ctx context.Context, req *connect.Request[v1.WatchTaskRequest], stream *connect.ServerStream[v1.WatchTaskResponse]) error {
	return wrap(s.tasks.Watch(ctx, req.Msg.GetId(), stream.Send))
}

func (s *TaskService) CancelTask(ctx context.Context, req *connect.Request[v1.CancelTaskRequest]) (*connect.Response[v1.CancelTaskResponse], error) {
	task, err := s.tasks.Cancel(req.Msg.GetId())
	if err != nil {
		return nil, wrap(err)
	}
	return connect.NewResponse(&v1.CancelTaskResponse{Task: task}), nil
}
