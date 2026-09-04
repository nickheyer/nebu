package services

import (
	"context"
	"fmt"

	"connectrpc.com/connect"
	"github.com/nickheyer/nebu/internal/gateway"
	"github.com/nickheyer/nebu/internal/instances"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/proto/nebu/v1/nebuv1connect"
	"github.com/nickheyer/nebu/pkg/runtime"
)

var _ nebuv1connect.GatewayServiceHandler = (*GatewayService)(nil)

// Serves routes and gateway status
type GatewayService struct {
	gateway   *gateway.Gateway
	instances *instances.Manager
}

// Builds the gateway service
func NewGatewayService(g *gateway.Gateway, m *instances.Manager) *GatewayService {
	return &GatewayService{gateway: g, instances: m}
}

func (s *GatewayService) GetGatewayStatus(ctx context.Context, req *connect.Request[v1.GetGatewayStatusRequest]) (*connect.Response[v1.GetGatewayStatusResponse], error) {
	return connect.NewResponse(&v1.GetGatewayStatusResponse{Status: s.gateway.Status()}), nil
}

func (s *GatewayService) ListRoutes(ctx context.Context, req *connect.Request[v1.ListRoutesRequest]) (*connect.Response[v1.ListRoutesResponse], error) {
	return connect.NewResponse(&v1.ListRoutesResponse{Routes: s.gateway.Table().List()}), nil
}

func (s *GatewayService) SetRoute(ctx context.Context, req *connect.Request[v1.SetRouteRequest]) (*connect.Response[v1.SetRouteResponse], error) {
	in, err := s.instances.Get(req.Msg.GetInstanceId())
	if err != nil {
		return nil, wrap(err)
	}
	if in.GetState() != v1.InstanceState_INSTANCE_STATE_READY {
		return nil, wrap(fmt.Errorf("%w: instance %s is not ready", runtime.ErrParam, in.GetName()))
	}
	if r, ok := s.gateway.Table().Lookup(req.Msg.GetName()); ok && r.GetSlotId() != "" {
		return nil, wrap(fmt.Errorf("%w: %s belongs to a slot", runtime.ErrParam, req.Msg.GetName()))
	}
	route := s.gateway.Table().Set(req.Msg.GetName(), in.GetId(), "", in.GetEndpoint(), in.GetRepo()+":"+in.GetGroup(), s.instances.Runtimes.API(in.GetRuntimeId()), nil)
	return connect.NewResponse(&v1.SetRouteResponse{Route: route}), nil
}

func (s *GatewayService) DeleteRoute(ctx context.Context, req *connect.Request[v1.DeleteRouteRequest]) (*connect.Response[v1.DeleteRouteResponse], error) {
	if r, ok := s.gateway.Table().Lookup(req.Msg.GetName()); ok && r.GetSlotId() != "" {
		return nil, wrap(fmt.Errorf("%w: %s belongs to a slot, delete the slot instead", runtime.ErrParam, req.Msg.GetName()))
	}
	route, ok := s.gateway.Table().Delete(req.Msg.GetName())
	if !ok {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("no route %q", req.Msg.GetName()))
	}
	return connect.NewResponse(&v1.DeleteRouteResponse{Route: route}), nil
}
