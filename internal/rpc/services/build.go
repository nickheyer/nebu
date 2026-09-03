package services

import (
	"context"

	"connectrpc.com/connect"
	"github.com/nickheyer/nebu/internal/installs"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/proto/nebu/v1/nebuv1connect"
)

var _ nebuv1connect.BuildServiceHandler = (*BuildService)(nil)

// Serves recipes and builds
type BuildService struct {
	installs *installs.Manager
}

// Builds the build service
func NewBuildService(m *installs.Manager) *BuildService {
	return &BuildService{installs: m}
}

func (s *BuildService) ListRecipes(ctx context.Context, req *connect.Request[v1.ListRecipesRequest]) (*connect.Response[v1.ListRecipesResponse], error) {
	list, err := s.installs.ListRecipes(ctx, req.Msg.GetRuntimeId())
	if err != nil {
		return nil, wrap(err)
	}
	return connect.NewResponse(&v1.ListRecipesResponse{Recipes: list}), nil
}

func (s *BuildService) Build(ctx context.Context, req *connect.Request[v1.BuildRequest]) (*connect.Response[v1.BuildResponse], error) {
	b, task, err := s.installs.Build(ctx, req.Msg)
	if err != nil {
		return nil, wrap(err)
	}
	return connect.NewResponse(&v1.BuildResponse{Build: b, Task: task}), nil
}

func (s *BuildService) ListBuilds(ctx context.Context, req *connect.Request[v1.ListBuildsRequest]) (*connect.Response[v1.ListBuildsResponse], error) {
	list, err := s.installs.ListBuilds(ctx, req.Msg.GetRuntimeId())
	if err != nil {
		return nil, wrap(err)
	}
	return connect.NewResponse(&v1.ListBuildsResponse{Builds: list}), nil
}

func (s *BuildService) GetBuild(ctx context.Context, req *connect.Request[v1.GetBuildRequest]) (*connect.Response[v1.GetBuildResponse], error) {
	b, err := s.installs.GetBuild(ctx, req.Msg.GetId())
	if err != nil {
		return nil, wrap(err)
	}
	return connect.NewResponse(&v1.GetBuildResponse{Build: b}), nil
}

func (s *BuildService) RemoveBuild(ctx context.Context, req *connect.Request[v1.RemoveBuildRequest]) (*connect.Response[v1.RemoveBuildResponse], error) {
	b, err := s.installs.RemoveBuild(ctx, req.Msg.GetId())
	if err != nil {
		return nil, wrap(err)
	}
	return connect.NewResponse(&v1.RemoveBuildResponse{Build: b}), nil
}
