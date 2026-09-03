package services

import (
	"context"

	"connectrpc.com/connect"
	"github.com/nickheyer/nebu/internal/inspect"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/proto/nebu/v1/nebuv1connect"
)

var _ nebuv1connect.EstimateServiceHandler = (*EstimateService)(nil)

// Serves inspection and planning
type EstimateService struct {
	inspector *inspect.Inspector
}

// Builds the estimate service
func NewEstimateService(insp *inspect.Inspector) *EstimateService {
	return &EstimateService{inspector: insp}
}

func (s *EstimateService) Inspect(ctx context.Context, req *connect.Request[v1.InspectRequest]) (*connect.Response[v1.InspectResponse], error) {
	resp, err := s.inspector.Inspect(ctx, req.Msg)
	if err != nil {
		return nil, wrap(err)
	}
	return connect.NewResponse(resp), nil
}

func (s *EstimateService) Estimate(ctx context.Context, req *connect.Request[v1.EstimateRequest]) (*connect.Response[v1.EstimateResponse], error) {
	resp, err := s.inspector.Estimate(ctx, req.Msg)
	if err != nil {
		return nil, wrap(err)
	}
	return connect.NewResponse(resp), nil
}
