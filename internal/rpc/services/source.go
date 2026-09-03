package services

import (
	"context"

	"connectrpc.com/connect"
	"github.com/nickheyer/nebu/internal/inspect"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/proto/nebu/v1/nebuv1connect"
	"github.com/nickheyer/nebu/pkg/sources"
)

var _ nebuv1connect.SourceServiceHandler = (*SourceService)(nil)

// Serves catalog lookups
type SourceService struct {
	sources   *sources.Registry
	inspector *inspect.Inspector
}

// Builds the source service
func NewSourceService(reg *sources.Registry, insp *inspect.Inspector) *SourceService {
	return &SourceService{sources: reg, inspector: insp}
}

func (s *SourceService) ListSources(ctx context.Context, req *connect.Request[v1.ListSourcesRequest]) (*connect.Response[v1.ListSourcesResponse], error) {
	return connect.NewResponse(&v1.ListSourcesResponse{Sources: s.sources.List()}), nil
}

func (s *SourceService) Search(ctx context.Context, req *connect.Request[v1.SearchRequest]) (*connect.Response[v1.SearchResponse], error) {
	src, err := s.sources.Get(req.Msg.GetSourceId())
	if err != nil {
		return nil, wrap(err)
	}
	hits, err := src.Search(ctx, req.Msg.GetQuery(), req.Msg.GetTags(), int(req.Msg.GetLimit()))
	if err != nil {
		return nil, wrap(err)
	}
	return connect.NewResponse(&v1.SearchResponse{Hits: hits}), nil
}

func (s *SourceService) Resolve(ctx context.Context, req *connect.Request[v1.ResolveRequest]) (*connect.Response[v1.ResolveResponse], error) {
	_, model, err := s.inspector.Resolve(ctx, req.Msg.GetSourceId(), req.Msg.GetRepo(), req.Msg.GetRevision())
	if err != nil {
		return nil, wrap(err)
	}
	return connect.NewResponse(&v1.ResolveResponse{Model: model}), nil
}
