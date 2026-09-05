package services

import (
	"context"

	"connectrpc.com/connect"
	"github.com/nickheyer/nebu/internal/installs"
	"github.com/nickheyer/nebu/internal/profiles"
	"github.com/nickheyer/nebu/pkg/host"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/proto/nebu/v1/nebuv1connect"
	"github.com/nickheyer/nebu/pkg/runtime"
)

var (
	_ nebuv1connect.RuntimeServiceHandler = (*RuntimeService)(nil)
	_ nebuv1connect.BuildServiceHandler   = (*BuildService)(nil)
)

// Serves the runtime catalog, the formats it accepts, installs, and param profiles
type RuntimeService struct {
	runtimes *runtime.Registry
	prober   *host.Prober
	installs *installs.Manager
	profiles *profiles.Manager
	formats  []*v1.FormatSpec
}

// Builds the runtime service, formats in priority order
func NewRuntimeService(reg *runtime.Registry, prober *host.Prober, inst *installs.Manager, prof *profiles.Manager, formats []*v1.FormatSpec) *RuntimeService {
	return &RuntimeService{runtimes: reg, prober: prober, installs: inst, profiles: prof, formats: formats}
}

func (s *RuntimeService) ListFormats(ctx context.Context, req *connect.Request[v1.ListFormatsRequest]) (*connect.Response[v1.ListFormatsResponse], error) {
	return reply(&v1.ListFormatsResponse{Formats: s.formats}, nil)
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
	return reply(resp, nil)
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
	return reply(&v1.GetRuntimeResponse{Runtime: rt.Status(profile)}, nil)
}

func (s *RuntimeService) ListInstalls(ctx context.Context, req *connect.Request[v1.ListInstallsRequest]) (*connect.Response[v1.ListInstallsResponse], error) {
	list, err := s.installs.List(ctx, req.Msg.GetRuntimeId())
	return reply(&v1.ListInstallsResponse{Installs: list}, err)
}

func (s *RuntimeService) AdoptInstall(ctx context.Context, req *connect.Request[v1.AdoptInstallRequest]) (*connect.Response[v1.AdoptInstallResponse], error) {
	in, err := s.installs.Adopt(ctx, req.Msg.GetRuntimeId(), req.Msg.GetPath())
	return reply(&v1.AdoptInstallResponse{Install: in}, err)
}

func (s *RuntimeService) InstallPrebuilt(ctx context.Context, req *connect.Request[v1.InstallPrebuiltRequest]) (*connect.Response[v1.InstallPrebuiltResponse], error) {
	task, err := s.installs.InstallPrebuilt(ctx, req.Msg.GetRuntimeId())
	return reply(&v1.InstallPrebuiltResponse{Task: task}, err)
}

func (s *RuntimeService) RemoveInstall(ctx context.Context, req *connect.Request[v1.RemoveInstallRequest]) (*connect.Response[v1.RemoveInstallResponse], error) {
	in, err := s.installs.Remove(ctx, req.Msg.GetId())
	return reply(&v1.RemoveInstallResponse{Install: in}, err)
}

func (s *RuntimeService) ListProfiles(ctx context.Context, req *connect.Request[v1.ListProfilesRequest]) (*connect.Response[v1.ListProfilesResponse], error) {
	return reply(&v1.ListProfilesResponse{Profiles: s.profiles.List(req.Msg.GetRuntimeId())}, nil)
}

func (s *RuntimeService) CreateProfile(ctx context.Context, req *connect.Request[v1.CreateProfileRequest]) (*connect.Response[v1.CreateProfileResponse], error) {
	p, err := s.profiles.Create(ctx, req.Msg.GetProfile())
	return reply(&v1.CreateProfileResponse{Profile: p}, err)
}

func (s *RuntimeService) UpdateProfile(ctx context.Context, req *connect.Request[v1.UpdateProfileRequest]) (*connect.Response[v1.UpdateProfileResponse], error) {
	p, err := s.profiles.Update(ctx, req.Msg.GetProfile())
	return reply(&v1.UpdateProfileResponse{Profile: p}, err)
}

func (s *RuntimeService) DeleteProfile(ctx context.Context, req *connect.Request[v1.DeleteProfileRequest]) (*connect.Response[v1.DeleteProfileResponse], error) {
	p, err := s.profiles.Delete(ctx, req.Msg.GetId(), req.Msg.GetForce())
	return reply(&v1.DeleteProfileResponse{Profile: p}, err)
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
