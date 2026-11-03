package services

import (
	"context"
	"fmt"
	"net/http"

	"connectrpc.com/connect"
	"github.com/nickheyer/nebu/internal/auth"
	"github.com/nickheyer/nebu/internal/gateway"
	"github.com/nickheyer/nebu/internal/pools"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/proto/nebu/v1/nebuv1connect"
	"google.golang.org/protobuf/types/known/timestamppb"
)

var _ nebuv1connect.GatewayServiceHandler = (*GatewayService)(nil)

// Serves the gateway's status and traces and the pools, routes, objectives and scheduler configurations behind it
type GatewayService struct {
	gateway *gateway.Gateway
	pools   *pools.Manager
	guard   *auth.Guard
}

func NewGatewayService(g *gateway.Gateway, p *pools.Manager, guard *auth.Guard) *GatewayService {
	return &GatewayService{gateway: g, pools: p, guard: guard}
}

func (s *GatewayService) GetGatewayStatus(ctx context.Context, req *connect.Request[v1.GetGatewayStatusRequest]) (*connect.Response[v1.GetGatewayStatusResponse], error) {
	return reply(&v1.GetGatewayStatusResponse{Status: s.gateway.Status()}, nil)
}

func (s *GatewayService) ListRoutes(ctx context.Context, req *connect.Request[v1.ListRoutesRequest]) (*connect.Response[v1.ListRoutesResponse], error) {
	return reply(&v1.ListRoutesResponse{Routes: s.gateway.Routes()}, nil)
}

func (s *GatewayService) GetRoute(ctx context.Context, req *connect.Request[v1.GetRouteRequest]) (*connect.Response[v1.GetRouteResponse], error) {
	rt, ok := s.gateway.Route(req.Msg.GetName())
	if !ok {
		return nil, wrap(fmt.Errorf("%w: route %s", pools.ErrNotFound, req.Msg.GetName()))
	}
	return reply(&v1.GetRouteResponse{Route: rt}, nil)
}

func (s *GatewayService) PutRoute(ctx context.Context, req *connect.Request[v1.PutRouteRequest]) (*connect.Response[v1.PutRouteResponse], error) {
	rt, err := s.pools.PutRoute(ctx, req.Msg.GetName(), req.Msg.GetSpec())
	return reply(&v1.PutRouteResponse{Route: rt}, err)
}

func (s *GatewayService) DeleteRoute(ctx context.Context, req *connect.Request[v1.DeleteRouteRequest]) (*connect.Response[v1.DeleteRouteResponse], error) {
	rt, err := s.pools.DeleteRoute(ctx, req.Msg.GetName())
	return reply(&v1.DeleteRouteResponse{Route: rt}, err)
}

func (s *GatewayService) ListPools(ctx context.Context, req *connect.Request[v1.ListPoolsRequest]) (*connect.Response[v1.ListPoolsResponse], error) {
	return reply(&v1.ListPoolsResponse{Pools: s.pools.Pools()}, nil)
}

func (s *GatewayService) GetPool(ctx context.Context, req *connect.Request[v1.GetPoolRequest]) (*connect.Response[v1.GetPoolResponse], error) {
	p, ok := s.pools.Pool(req.Msg.GetId())
	if !ok {
		return nil, wrap(fmt.Errorf("%w: pool %s", pools.ErrNotFound, req.Msg.GetId()))
	}
	return reply(&v1.GetPoolResponse{Pool: p}, nil)
}

func (s *GatewayService) PutPool(ctx context.Context, req *connect.Request[v1.PutPoolRequest]) (*connect.Response[v1.PutPoolResponse], error) {
	p, err := s.pools.PutPool(ctx, req.Msg.GetId(), req.Msg.GetSpec())
	return reply(&v1.PutPoolResponse{Pool: p}, err)
}

func (s *GatewayService) DeletePool(ctx context.Context, req *connect.Request[v1.DeletePoolRequest]) (*connect.Response[v1.DeletePoolResponse], error) {
	p, err := s.pools.DeletePool(ctx, req.Msg.GetId())
	return reply(&v1.DeletePoolResponse{Pool: p}, err)
}

func (s *GatewayService) ListObjectives(ctx context.Context, req *connect.Request[v1.ListObjectivesRequest]) (*connect.Response[v1.ListObjectivesResponse], error) {
	return reply(&v1.ListObjectivesResponse{Objectives: s.pools.Objectives()}, nil)
}

func (s *GatewayService) PutObjective(ctx context.Context, req *connect.Request[v1.PutObjectiveRequest]) (*connect.Response[v1.PutObjectiveResponse], error) {
	o, err := s.pools.PutObjective(ctx, req.Msg.GetObjective())
	return reply(&v1.PutObjectiveResponse{Objective: o}, err)
}

func (s *GatewayService) DeleteObjective(ctx context.Context, req *connect.Request[v1.DeleteObjectiveRequest]) (*connect.Response[v1.DeleteObjectiveResponse], error) {
	o, err := s.pools.DeleteObjective(ctx, req.Msg.GetName())
	return reply(&v1.DeleteObjectiveResponse{Objective: o}, err)
}

func (s *GatewayService) ListSchedulerConfigs(ctx context.Context, req *connect.Request[v1.ListSchedulerConfigsRequest]) (*connect.Response[v1.ListSchedulerConfigsResponse], error) {
	return reply(&v1.ListSchedulerConfigsResponse{Configs: s.pools.SchedulerConfigs()}, nil)
}

// Stores a configuration, or with validate_only loads it against the gateway's plugins and answers it accepted without storing
func (s *GatewayService) PutSchedulerConfig(ctx context.Context, req *connect.Request[v1.PutSchedulerConfigRequest]) (*connect.Response[v1.PutSchedulerConfigResponse], error) {
	cfg := req.Msg.GetConfig()
	if !req.Msg.GetValidateOnly() {
		out, err := s.pools.PutSchedulerConfig(ctx, cfg)
		return reply(&v1.PutSchedulerConfigResponse{Config: out}, err)
	}
	if err := s.pools.ValidateSchedulerConfig(cfg.GetSpec().GetYaml()); err != nil {
		return nil, wrap(fmt.Errorf("scheduler config %s: %w", cfg.GetName(), err))
	}
	accepted := &v1.Condition{Type: pools.ConditionAccepted, Status: v1.ConditionStatus_CONDITION_STATUS_TRUE, Reason: pools.ReasonAccepted, Message: "the configuration loads", LastTransitionTime: timestamppb.Now()}
	return reply(&v1.PutSchedulerConfigResponse{Config: &v1.SchedulerConfig{Name: cfg.GetName(), Spec: &v1.SchedulerConfigSpec{Yaml: cfg.GetSpec().GetYaml()}, Status: &v1.SchedulerConfigStatus{Conditions: []*v1.Condition{accepted}}}}, nil)
}

func (s *GatewayService) DeleteSchedulerConfig(ctx context.Context, req *connect.Request[v1.DeleteSchedulerConfigRequest]) (*connect.Response[v1.DeleteSchedulerConfigResponse], error) {
	c, err := s.pools.DeleteSchedulerConfig(ctx, req.Msg.GetName())
	return reply(&v1.DeleteSchedulerConfigResponse{Config: c}, err)
}

// The traces a caller may read, every account's when the API runs without auth
func (s *GatewayService) viewer(h http.Header) (gateway.Viewer, error) {
	if !s.guard.Enabled() {
		return gateway.Viewer{All: true}, nil
	}
	v, ok := s.gateway.ViewerFor(h)
	if !ok {
		return gateway.Viewer{}, connect.NewError(connect.CodeUnauthenticated, s.guard.Err())
	}
	return v, nil
}

func (s *GatewayService) ListTraces(ctx context.Context, req *connect.Request[v1.ListTracesRequest]) (*connect.Response[v1.ListTracesResponse], error) {
	v, err := s.viewer(req.Header())
	if err != nil {
		return nil, err
	}
	list, err := s.gateway.Traces().List(ctx, v, req.Msg.GetRoute(), int(req.Msg.GetLimit()))
	return reply(&v1.ListTracesResponse{Traces: list}, err)
}

func (s *GatewayService) GetTrace(ctx context.Context, req *connect.Request[v1.GetTraceRequest]) (*connect.Response[v1.GetTraceResponse], error) {
	v, err := s.viewer(req.Header())
	if err != nil {
		return nil, err
	}
	t, ok, err := s.gateway.Traces().Get(ctx, v, req.Msg.GetId())
	if err == nil && !ok {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("no trace %q", req.Msg.GetId()))
	}
	return reply(&v1.GetTraceResponse{Trace: t}, err)
}
