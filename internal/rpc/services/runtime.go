package services

import (
	"context"

	"connectrpc.com/connect"
	"github.com/nickheyer/nebu/internal/installs"
	"github.com/nickheyer/nebu/pkg/formats"
	"github.com/nickheyer/nebu/pkg/host"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/proto/nebu/v1/nebuv1connect"
	"github.com/nickheyer/nebu/pkg/runtimes"
)

var (
	_ nebuv1connect.RuntimeServiceHandler = (*RuntimeService)(nil)
	_ nebuv1connect.BuildServiceHandler   = (*BuildService)(nil)
)

// Serves the runtime catalog, the formats it accepts, and installs
type RuntimeService struct {
	runtimes *runtimes.Registry
	prober   *host.Prober
	installs *installs.Manager
	formats  *formats.Registry
}

// Builds the runtime service, formats in priority order
func NewRuntimeService(reg *runtimes.Registry, prober *host.Prober, inst *installs.Manager, fmts *formats.Registry) *RuntimeService {
	return &RuntimeService{runtimes: reg, prober: prober, installs: inst, formats: fmts}
}

func (s *RuntimeService) ListFormats(ctx context.Context, req *connect.Request[v1.ListFormatsRequest]) (*connect.Response[v1.ListFormatsResponse], error) {
	return reply(&v1.ListFormatsResponse{Formats: s.formats.Describe()}, nil)
}

func (s *RuntimeService) ListRuntimes(ctx context.Context, req *connect.Request[v1.ListRuntimesRequest]) (*connect.Response[v1.ListRuntimesResponse], error) {
	profile, err := s.prober.Profile(ctx, false)
	if err != nil {
		return nil, wrap(err)
	}
	resp := &v1.ListRuntimesResponse{}
	for _, rt := range s.runtimes.List() {
		st, err := s.status(ctx, rt, profile)
		if err != nil {
			return nil, wrap(err)
		}
		resp.Runtimes = append(resp.Runtimes, st)
	}
	return reply(resp, nil)
}

// The runtime's status with its install methods as this host sees them
func (s *RuntimeService) status(ctx context.Context, rt runtimes.Runtime, profile *v1.HostProfile) (*v1.RuntimeStatus, error) {
	st := s.runtimes.Status(rt, profile)
	options, err := s.installs.Options(ctx, rt, profile)
	if err != nil {
		return nil, err
	}
	st.Installs = options
	return st, nil
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
	st, err := s.status(ctx, rt, profile)
	return reply(&v1.GetRuntimeResponse{Runtime: st}, err)
}

func (s *RuntimeService) ListInstalls(ctx context.Context, req *connect.Request[v1.ListInstallsRequest]) (*connect.Response[v1.ListInstallsResponse], error) {
	list, err := s.installs.List(ctx, req.Msg.GetRuntimeId())
	return reply(&v1.ListInstallsResponse{Installs: list}, err)
}

func (s *RuntimeService) AdoptInstall(ctx context.Context, req *connect.Request[v1.AdoptInstallRequest]) (*connect.Response[v1.AdoptInstallResponse], error) {
	in, err := s.installs.Adopt(ctx, req.Msg.GetRuntimeId(), req.Msg.GetPath())
	return reply(&v1.AdoptInstallResponse{Install: in}, err)
}

func (s *RuntimeService) Install(ctx context.Context, req *connect.Request[v1.InstallRequest]) (*connect.Response[v1.InstallResponse], error) {
	task, err := s.installs.Install(ctx, req.Msg.GetRuntimeId(), req.Msg.GetMethod(), req.Msg.GetSettings())
	return reply(&v1.InstallResponse{Task: task}, err)
}

func (s *RuntimeService) RemoveInstall(ctx context.Context, req *connect.Request[v1.RemoveInstallRequest]) (*connect.Response[v1.RemoveInstallResponse], error) {
	in, err := s.installs.Remove(ctx, req.Msg.GetId())
	return reply(&v1.RemoveInstallResponse{Install: in}, err)
}

// Serves recipes and builds
type BuildService struct {
	installs *installs.Manager
}

func NewBuildService(m *installs.Manager) *BuildService {
	return &BuildService{installs: m}
}

func (s *BuildService) ListRecipes(ctx context.Context, req *connect.Request[v1.ListRecipesRequest]) (*connect.Response[v1.ListRecipesResponse], error) {
	list, err := s.installs.ListRecipes(ctx, req.Msg.GetRuntimeId())
	return reply(&v1.ListRecipesResponse{Recipes: list}, err)
}

func (s *BuildService) Build(ctx context.Context, req *connect.Request[v1.BuildRequest]) (*connect.Response[v1.BuildResponse], error) {
	b, task, err := s.installs.Build(ctx, req.Msg)
	return reply(&v1.BuildResponse{Build: b, Task: task}, err)
}

func (s *BuildService) ListBuilds(ctx context.Context, req *connect.Request[v1.ListBuildsRequest]) (*connect.Response[v1.ListBuildsResponse], error) {
	list, err := s.installs.ListBuilds(ctx, req.Msg.GetRuntimeId())
	return reply(&v1.ListBuildsResponse{Builds: list}, err)
}

func (s *BuildService) GetBuild(ctx context.Context, req *connect.Request[v1.GetBuildRequest]) (*connect.Response[v1.GetBuildResponse], error) {
	b, err := s.installs.GetBuild(ctx, req.Msg.GetId())
	return reply(&v1.GetBuildResponse{Build: b}, err)
}

func (s *BuildService) RemoveBuild(ctx context.Context, req *connect.Request[v1.RemoveBuildRequest]) (*connect.Response[v1.RemoveBuildResponse], error) {
	b, err := s.installs.RemoveBuild(ctx, req.Msg.GetId())
	return reply(&v1.RemoveBuildResponse{Build: b}, err)
}
