package services

import (
	"context"
	"strings"

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
	formats   []string
}

// Builds the source service, formats are the ids hits get tagged with
func NewSourceService(reg *sources.Registry, insp *inspect.Inspector, formats []string) *SourceService {
	return &SourceService{sources: reg, inspector: insp, formats: formats}
}

func (s *SourceService) ListSources(ctx context.Context, req *connect.Request[v1.ListSourcesRequest]) (*connect.Response[v1.ListSourcesResponse], error) {
	return connect.NewResponse(&v1.ListSourcesResponse{Sources: s.sources.Statuses(ctx)}), nil
}

func (s *SourceService) Search(ctx context.Context, req *connect.Request[v1.SearchRequest]) (*connect.Response[v1.SearchResponse], error) {
	src, err := s.sources.Get(req.Msg.GetSourceId())
	if err != nil {
		return nil, wrap(err)
	}
	resp, err := src.Search(ctx, req.Msg)
	if err != nil {
		return nil, wrap(err)
	}
	for _, h := range resp.GetHits() {
		if h.SourceId == "" {
			h.SourceId = src.Spec().GetId()
		}
		if len(h.Formats) == 0 {
			h.Formats = s.formatsOf(h)
		}
	}
	return connect.NewResponse(resp), nil
}

// Names the formats a hit advertises through its tags, using the loaded format specs
func (s *SourceService) formatsOf(h *v1.SearchHit) []string {
	var out []string
	for _, id := range s.formats {
		for _, t := range append([]string{h.GetLibrary()}, h.GetTags()...) {
			if strings.EqualFold(t, id) {
				out = append(out, id)
				break
			}
		}
	}
	return out
}

func (s *SourceService) Resolve(ctx context.Context, req *connect.Request[v1.ResolveRequest]) (*connect.Response[v1.ResolveResponse], error) {
	_, model, err := s.inspector.Resolve(ctx, req.Msg.GetSourceId(), req.Msg.GetRepo(), req.Msg.GetRevision())
	if err != nil {
		return nil, wrap(err)
	}
	return connect.NewResponse(&v1.ResolveResponse{Model: model}), nil
}

func (s *SourceService) ListRevisions(ctx context.Context, req *connect.Request[v1.ListRevisionsRequest]) (*connect.Response[v1.ListRevisionsResponse], error) {
	src, err := s.sources.Get(req.Msg.GetSourceId())
	if err != nil {
		return nil, wrap(err)
	}
	revisions, err := src.Revisions(ctx, req.Msg.GetRepo())
	if err != nil {
		return nil, wrap(err)
	}
	return connect.NewResponse(&v1.ListRevisionsResponse{Revisions: revisions}), nil
}

func (s *SourceService) GetModelCard(ctx context.Context, req *connect.Request[v1.GetModelCardRequest]) (*connect.Response[v1.GetModelCardResponse], error) {
	src, err := s.sources.Get(req.Msg.GetSourceId())
	if err != nil {
		return nil, wrap(err)
	}
	card, err := src.Card(ctx, req.Msg.GetRepo(), req.Msg.GetRevision())
	if err != nil {
		return nil, wrap(err)
	}
	return connect.NewResponse(&v1.GetModelCardResponse{Card: card}), nil
}
