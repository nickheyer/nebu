package services

import (
	"context"

	"connectrpc.com/connect"
	"github.com/nickheyer/nebu/internal/monitor"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/proto/nebu/v1/nebuv1connect"
)

var _ nebuv1connect.MonitorServiceHandler = (*MonitorService)(nil)

// Serves watches and findings
type MonitorService struct {
	monitor *monitor.Manager
}

// Builds the monitor service
func NewMonitorService(m *monitor.Manager) *MonitorService {
	return &MonitorService{monitor: m}
}

func (s *MonitorService) AddWatch(ctx context.Context, req *connect.Request[v1.AddWatchRequest]) (*connect.Response[v1.AddWatchResponse], error) {
	w, err := s.monitor.Add(ctx, req.Msg)
	if err != nil {
		return nil, wrap(err)
	}
	return connect.NewResponse(&v1.AddWatchResponse{Watch: w}), nil
}

func (s *MonitorService) ListWatches(ctx context.Context, req *connect.Request[v1.ListWatchesRequest]) (*connect.Response[v1.ListWatchesResponse], error) {
	return connect.NewResponse(&v1.ListWatchesResponse{Watches: s.monitor.List()}), nil
}

func (s *MonitorService) RemoveWatch(ctx context.Context, req *connect.Request[v1.RemoveWatchRequest]) (*connect.Response[v1.RemoveWatchResponse], error) {
	w, err := s.monitor.Remove(ctx, req.Msg.GetId())
	if err != nil {
		return nil, wrap(err)
	}
	return connect.NewResponse(&v1.RemoveWatchResponse{Watch: w}), nil
}

func (s *MonitorService) CheckWatches(ctx context.Context, req *connect.Request[v1.CheckWatchesRequest]) (*connect.Response[v1.CheckWatchesResponse], error) {
	task, err := s.monitor.Check(ctx, req.Msg.GetId())
	if err != nil {
		return nil, wrap(err)
	}
	return connect.NewResponse(&v1.CheckWatchesResponse{Task: task}), nil
}

func (s *MonitorService) ListFindings(ctx context.Context, req *connect.Request[v1.ListFindingsRequest]) (*connect.Response[v1.ListFindingsResponse], error) {
	list, err := s.monitor.Findings(ctx, req.Msg.GetWatchId(), req.Msg.GetUnacknowledgedOnly())
	if err != nil {
		return nil, wrap(err)
	}
	return connect.NewResponse(&v1.ListFindingsResponse{Findings: list}), nil
}

func (s *MonitorService) AckFinding(ctx context.Context, req *connect.Request[v1.AckFindingRequest]) (*connect.Response[v1.AckFindingResponse], error) {
	f, err := s.monitor.Ack(ctx, req.Msg.GetId())
	if err != nil {
		return nil, wrap(err)
	}
	return connect.NewResponse(&v1.AckFindingResponse{Finding: f}), nil
}
