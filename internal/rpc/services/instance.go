package services

import (
	"context"

	"connectrpc.com/connect"
	"github.com/nickheyer/nebu/internal/instances"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/proto/nebu/v1/nebuv1connect"
)

var _ nebuv1connect.InstanceServiceHandler = (*InstanceService)(nil)

// Serves running models
type InstanceService struct {
	instances *instances.Manager
}

// Builds the instance service
func NewInstanceService(m *instances.Manager) *InstanceService {
	return &InstanceService{instances: m}
}

func (s *InstanceService) Run(ctx context.Context, req *connect.Request[v1.RunRequest]) (*connect.Response[v1.RunResponse], error) {
	in, task, err := s.instances.Run(ctx, req.Msg)
	if err != nil {
		return nil, wrap(err)
	}
	return connect.NewResponse(&v1.RunResponse{Instance: in, Task: task}), nil
}

func (s *InstanceService) ListInstances(ctx context.Context, req *connect.Request[v1.ListInstancesRequest]) (*connect.Response[v1.ListInstancesResponse], error) {
	return connect.NewResponse(&v1.ListInstancesResponse{Instances: s.instances.List(req.Msg.GetRunningOnly())}), nil
}

func (s *InstanceService) GetInstance(ctx context.Context, req *connect.Request[v1.GetInstanceRequest]) (*connect.Response[v1.GetInstanceResponse], error) {
	in, err := s.instances.Get(req.Msg.GetId())
	if err != nil {
		return nil, wrap(err)
	}
	return connect.NewResponse(&v1.GetInstanceResponse{Instance: in}), nil
}

func (s *InstanceService) StopInstance(ctx context.Context, req *connect.Request[v1.StopInstanceRequest]) (*connect.Response[v1.StopInstanceResponse], error) {
	in, err := s.instances.Stop(ctx, req.Msg.GetId())
	if err != nil {
		return nil, wrap(err)
	}
	return connect.NewResponse(&v1.StopInstanceResponse{Instance: in}), nil
}

func (s *InstanceService) Logs(ctx context.Context, req *connect.Request[v1.LogsRequest], stream *connect.ServerStream[v1.LogsResponse]) error {
	return wrap(s.instances.Logs(ctx, req.Msg.GetId(), req.Msg.GetFollow(), int(req.Msg.GetTail()), func(lines []string) error {
		return stream.Send(&v1.LogsResponse{Lines: lines})
	}))
}
