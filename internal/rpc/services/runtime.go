package services

import (
	"context"

	"connectrpc.com/connect"
	"github.com/nickheyer/nebu/internal/installs"
	"github.com/nickheyer/nebu/pkg/host"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/proto/nebu/v1/nebuv1connect"
	"github.com/nickheyer/nebu/pkg/runtime"
)

var _ nebuv1connect.RuntimeServiceHandler = (*RuntimeService)(nil)

// Serves the runtime catalog and installs
type RuntimeService struct {
	runtimes *runtime.Registry
	prober   *host.Prober
	installs *installs.Manager
}

// Builds the runtime service
func NewRuntimeService(reg *runtime.Registry, prober *host.Prober, inst *installs.Manager) *RuntimeService {
	return &RuntimeService{runtimes: reg, prober: prober, installs: inst}
}

func (s *RuntimeService) ListRuntimes(ctx context.Context, req *connect.Request[v1.ListRuntimesRequest]) (*connect.Response[v1.ListRuntimesResponse], error) {
	profile, err := s.prober.Profile(ctx, false)
	if err != nil {
		return nil, wrap(err)
	}
	resp := &v1.ListRuntimesResponse{}
	for _, rt := range s.runtimes.List() {
		resp.Runtimes = append(resp.Runtimes, rt.Status(profile))
	}
	return connect.NewResponse(resp), nil
}

func (s *RuntimeService) GetRuntime(ctx context.Context, req *connect.Request[v1.GetRuntimeRequest]) (*connect.Response[v1.GetRuntimeResponse], error) {
	rt, err := s.runtimes.Get(req.Msg.GetId())
	if err != nil {
		return nil, wrap(err)
	}
	profile, err := s.prober.Profile(ctx, false)
	if err != nil {
		return nil, wrap(err)
	}
	return connect.NewResponse(&v1.GetRuntimeResponse{Runtime: rt.Status(profile)}), nil
}

func (s *RuntimeService) ListInstalls(ctx context.Context, req *connect.Request[v1.ListInstallsRequest]) (*connect.Response[v1.ListInstallsResponse], error) {
	list, err := s.installs.List(req.Msg.GetRuntimeId())
	if err != nil {
		return nil, wrap(err)
	}
	return connect.NewResponse(&v1.ListInstallsResponse{Installs: list}), nil
}

func (s *RuntimeService) AdoptInstall(ctx context.Context, req *connect.Request[v1.AdoptInstallRequest]) (*connect.Response[v1.AdoptInstallResponse], error) {
	in, err := s.installs.Adopt(ctx, req.Msg.GetRuntimeId(), req.Msg.GetPath())
	if err != nil {
		return nil, wrap(err)
	}
	return connect.NewResponse(&v1.AdoptInstallResponse{Install: in}), nil
}

func (s *RuntimeService) InstallPrebuilt(ctx context.Context, req *connect.Request[v1.InstallPrebuiltRequest]) (*connect.Response[v1.InstallPrebuiltResponse], error) {
	task, err := s.installs.InstallPrebuilt(ctx, req.Msg.GetRuntimeId())
	if err != nil {
		return nil, wrap(err)
	}
	return connect.NewResponse(&v1.InstallPrebuiltResponse{Task: task}), nil
}

func (s *RuntimeService) RemoveInstall(ctx context.Context, req *connect.Request[v1.RemoveInstallRequest]) (*connect.Response[v1.RemoveInstallResponse], error) {
	in, err := s.installs.Remove(req.Msg.GetId())
	if err != nil {
		return nil, wrap(err)
	}
	return connect.NewResponse(&v1.RemoveInstallResponse{Install: in}), nil
}
