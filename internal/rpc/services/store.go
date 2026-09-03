package services

import (
	"context"

	"connectrpc.com/connect"
	"github.com/nickheyer/nebu/internal/pull"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/proto/nebu/v1/nebuv1connect"
	"github.com/nickheyer/nebu/pkg/store"
)

var _ nebuv1connect.StoreServiceHandler = (*StoreService)(nil)

// Serves the model store
type StoreService struct {
	store  *store.Store
	puller *pull.Puller
}

// Builds the store service
func NewStoreService(st *store.Store, p *pull.Puller) *StoreService {
	return &StoreService{store: st, puller: p}
}

func (s *StoreService) Pull(ctx context.Context, req *connect.Request[v1.PullRequest]) (*connect.Response[v1.PullResponse], error) {
	task, err := s.puller.Pull(ctx, req.Msg)
	if err != nil {
		return nil, wrap(err)
	}
	return connect.NewResponse(&v1.PullResponse{Task: task}), nil
}

func (s *StoreService) ListModels(ctx context.Context, req *connect.Request[v1.ListModelsRequest]) (*connect.Response[v1.ListModelsResponse], error) {
	models, err := s.store.ListManifests()
	if err != nil {
		return nil, wrap(err)
	}
	return connect.NewResponse(&v1.ListModelsResponse{Models: models}), nil
}

func (s *StoreService) GetModel(ctx context.Context, req *connect.Request[v1.GetModelRequest]) (*connect.Response[v1.GetModelResponse], error) {
	m, err := s.store.ReadManifest(req.Msg.GetSourceId(), req.Msg.GetRepo(), req.Msg.GetGroup())
	if err != nil {
		return nil, wrap(err)
	}
	return connect.NewResponse(&v1.GetModelResponse{Model: m}), nil
}

func (s *StoreService) RemoveModel(ctx context.Context, req *connect.Request[v1.RemoveModelRequest]) (*connect.Response[v1.RemoveModelResponse], error) {
	m, err := s.store.RemoveManifest(req.Msg.GetSourceId(), req.Msg.GetRepo(), req.Msg.GetGroup())
	if err != nil {
		return nil, wrap(err)
	}
	resp := &v1.RemoveModelResponse{Model: m}
	if req.Msg.GetGc() {
		if resp.Gc, err = s.store.Gc(false); err != nil {
			return nil, wrap(err)
		}
	}
	return connect.NewResponse(resp), nil
}

func (s *StoreService) Gc(ctx context.Context, req *connect.Request[v1.GcRequest]) (*connect.Response[v1.GcResponse], error) {
	resp, err := s.store.Gc(req.Msg.GetPartials())
	if err != nil {
		return nil, wrap(err)
	}
	return connect.NewResponse(resp), nil
}

func (s *StoreService) Verify(ctx context.Context, req *connect.Request[v1.VerifyRequest]) (*connect.Response[v1.VerifyResponse], error) {
	task, err := s.puller.Verify(ctx, req.Msg)
	if err != nil {
		return nil, wrap(err)
	}
	return connect.NewResponse(&v1.VerifyResponse{Task: task}), nil
}

func (s *StoreService) Export(ctx context.Context, req *connect.Request[v1.ExportRequest]) (*connect.Response[v1.ExportResponse], error) {
	task, err := s.puller.Export(ctx, req.Msg)
	if err != nil {
		return nil, wrap(err)
	}
	return connect.NewResponse(&v1.ExportResponse{Task: task}), nil
}

func (s *StoreService) GetStatus(ctx context.Context, req *connect.Request[v1.GetStatusRequest]) (*connect.Response[v1.GetStatusResponse], error) {
	st, err := s.store.Status()
	if err != nil {
		return nil, wrap(err)
	}
	return connect.NewResponse(&v1.GetStatusResponse{Status: st}), nil
}
