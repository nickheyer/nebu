package services

import (
	"context"

	"connectrpc.com/connect"
	"github.com/nickheyer/nebu/pkg/host"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/proto/nebu/v1/nebuv1connect"
	"github.com/nickheyer/nebu/pkg/runtime"
)

var _ nebuv1connect.RuntimeServiceHandler = (*RuntimeService)(nil)

// Serves the runtime catalog
type RuntimeService struct {
	runtimes *runtime.Registry
	prober   *host.Prober
}

// Builds the runtime service
func NewRuntimeService(reg *runtime.Registry, prober *host.Prober) *RuntimeService {
	return &RuntimeService{runtimes: reg, prober: prober}
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
