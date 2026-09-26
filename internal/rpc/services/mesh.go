package services

import (
	"context"
	"errors"
	"net/http"

	"connectrpc.com/connect"
	"github.com/nickheyer/nebu/internal/auth"
	"github.com/nickheyer/nebu/internal/formations"
	"github.com/nickheyer/nebu/internal/instances"
	"github.com/nickheyer/nebu/internal/mesh"
	"github.com/nickheyer/nebu/internal/mesh/links"
	"github.com/nickheyer/nebu/internal/pull"
	"github.com/nickheyer/nebu/internal/tasks"
	"github.com/nickheyer/nebu/pkg/perf"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/proto/nebu/v1/nebuv1connect"
	"github.com/nickheyer/nebu/pkg/store"
)

var _ nebuv1connect.MeshServiceHandler = (*MeshService)(nil)

var errNotPeer = connect.NewError(connect.CodePermissionDenied, errors.New("this call is for mesh members and carries no member session"))

// Membership, links, planning, and formations across nodes
type MeshService struct {
	mesh       *mesh.Manager
	formations *formations.Manager
	puller     *pull.Puller
	tasks      *tasks.Manager
	store      *store.Store
	perf       *perf.Table
	instances  *instances.Manager
	guard      *auth.Guard
}

func NewMeshService(m *mesh.Manager, f *formations.Manager, p *pull.Puller, t *tasks.Manager, st *store.Store, pf *perf.Table, in *instances.Manager, g *auth.Guard) *MeshService {
	return &MeshService{mesh: m, formations: f, puller: p, tasks: t, store: st, perf: pf, instances: in, guard: g}
}

// The member behind a node to node call
func (s *MeshService) peer(h http.Header) (string, error) {
	id, ok := s.guard.PeerOf(h)
	if !ok {
		return "", errNotPeer
	}
	return id, nil
}

func (s *MeshService) Init(ctx context.Context, req *connect.Request[v1.InitRequest]) (*connect.Response[v1.InitResponse], error) {
	m, token, err := s.mesh.Init(ctx, req.Msg.GetName(), req.Msg.GetTls())
	return reply(&v1.InitResponse{Mesh: m, Token: token}, err)
}

func (s *MeshService) Join(ctx context.Context, req *connect.Request[v1.JoinRequest]) (*connect.Response[v1.JoinResponse], error) {
	m, bootstrap, warnings, err := s.mesh.Join(ctx, req.Msg.GetToken())
	return reply(&v1.JoinResponse{Mesh: m, Bootstrap: bootstrap, Warnings: warnings}, err)
}

func (s *MeshService) Leave(ctx context.Context, req *connect.Request[v1.LeaveRequest]) (*connect.Response[v1.LeaveResponse], error) {
	m, err := s.mesh.Leave(ctx)
	return reply(&v1.LeaveResponse{Mesh: m}, err)
}

func (s *MeshService) ForgetNode(ctx context.Context, req *connect.Request[v1.ForgetNodeRequest]) (*connect.Response[v1.ForgetNodeResponse], error) {
	n, err := s.mesh.Forget(ctx, req.Msg.GetNodeId(), req.Msg.GetTell())
	return reply(&v1.ForgetNodeResponse{Node: n}, err)
}

func (s *MeshService) ResetMesh(ctx context.Context, req *connect.Request[v1.ResetMeshRequest]) (*connect.Response[v1.ResetMeshResponse], error) {
	m, err := s.mesh.Reset(ctx)
	return reply(&v1.ResetMeshResponse{Mesh: m}, err)
}

func (s *MeshService) Rotate(ctx context.Context, req *connect.Request[v1.RotateRequest]) (*connect.Response[v1.RotateResponse], error) {
	m, rotated, missed, err := s.mesh.Rotate(ctx)
	return reply(&v1.RotateResponse{Mesh: m, Rotated: rotated, Missed: missed}, err)
}

func (s *MeshService) GetMesh(ctx context.Context, req *connect.Request[v1.GetMeshRequest]) (*connect.Response[v1.GetMeshResponse], error) {
	out := &v1.GetMeshResponse{Nodes: s.mesh.Nodes(), Formations: s.formations.List(false)}
	if m, err := s.mesh.Mesh(); err == nil {
		out.Mesh = m
	}
	if s.perf != nil {
		out.Ratios, out.Profiles = s.perf.Ratios(), s.perf.Profiles()
	}
	return reply(out, nil)
}

func (s *MeshService) ListNodes(ctx context.Context, req *connect.Request[v1.ListNodesRequest]) (*connect.Response[v1.ListNodesResponse], error) {
	return reply(&v1.ListNodesResponse{Nodes: s.mesh.Nodes()}, nil)
}

func (s *MeshService) Token(ctx context.Context, req *connect.Request[v1.TokenRequest]) (*connect.Response[v1.TokenResponse], error) {
	token, err := s.mesh.Token()
	return reply(&v1.TokenResponse{Token: token}, err)
}

func (s *MeshService) Probe(ctx context.Context, req *connect.Request[v1.ProbeRequest]) (*connect.Response[v1.ProbeResponse], error) {
	list, err := s.mesh.Probe(ctx, req.Msg.GetNodeId(), req.Msg.GetForce())
	return reply(&v1.ProbeResponse{Links: list}, err)
}

func (s *MeshService) Discover(ctx context.Context, req *connect.Request[v1.DiscoverRequest]) (*connect.Response[v1.DiscoverResponse], error) {
	return reply(&v1.DiscoverResponse{Status: s.mesh.Status()}, nil)
}

func (s *MeshService) Invite(ctx context.Context, req *connect.Request[v1.InviteRequest]) (*connect.Response[v1.InviteResponse], error) {
	a, err := s.mesh.Invite(ctx, req.Msg.GetNodeId(), req.Msg.GetAddress())
	return reply(&v1.InviteResponse{Admission: a}, err)
}

func (s *MeshService) Ask(ctx context.Context, req *connect.Request[v1.AskRequest]) (*connect.Response[v1.AskResponse], error) {
	a, err := s.mesh.Ask(ctx, req.Msg.GetMeshHash(), req.Msg.GetNodeId(), req.Msg.GetAddress())
	return reply(&v1.AskResponse{Admission: a}, err)
}

func (s *MeshService) Admit(ctx context.Context, req *connect.Request[v1.AdmitRequest]) (*connect.Response[v1.AdmitResponse], error) {
	a, err := s.mesh.Admit(ctx, req.Msg.GetAdmissionId())
	return reply(&v1.AdmitResponse{Admission: a}, err)
}

func (s *MeshService) Refuse(ctx context.Context, req *connect.Request[v1.RefuseRequest]) (*connect.Response[v1.RefuseResponse], error) {
	a, err := s.mesh.Refuse(ctx, req.Msg.GetAdmissionId())
	return reply(&v1.RefuseResponse{Admission: a}, err)
}

func (s *MeshService) Dismiss(ctx context.Context, req *connect.Request[v1.DismissRequest]) (*connect.Response[v1.DismissResponse], error) {
	return reply(&v1.DismissResponse{}, s.mesh.Dismiss(ctx, req.Msg.GetAdmissionId()))
}

func (s *MeshService) SetDeviceProfile(ctx context.Context, req *connect.Request[v1.SetDeviceProfileRequest]) (*connect.Response[v1.SetDeviceProfileResponse], error) {
	if err := s.perf.SetProfile(ctx, req.Msg.GetProfile()); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	return reply(&v1.SetDeviceProfileResponse{Profiles: s.perf.Profiles()}, nil)
}

func (s *MeshService) DeleteDeviceProfile(ctx context.Context, req *connect.Request[v1.DeleteDeviceProfileRequest]) (*connect.Response[v1.DeleteDeviceProfileResponse], error) {
	if err := s.perf.DeleteProfile(ctx, req.Msg.GetPattern()); err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}
	return reply(&v1.DeleteDeviceProfileResponse{Profiles: s.perf.Profiles()}, nil)
}

func (s *MeshService) PlanFormation(ctx context.Context, req *connect.Request[v1.PlanFormationRequest]) (*connect.Response[v1.PlanFormationResponse], error) {
	plan, d, err := s.formations.Plan(ctx, req.Msg.GetRun())
	return reply(&v1.PlanFormationResponse{Plan: plan, Descriptor_: d}, err)
}

func (s *MeshService) RunFormation(ctx context.Context, req *connect.Request[v1.RunFormationRequest]) (*connect.Response[v1.RunFormationResponse], error) {
	f, task, err := s.formations.Run(ctx, req.Msg.GetRun())
	return reply(&v1.RunFormationResponse{Formation: f, Task: task}, err)
}

func (s *MeshService) ListFormations(ctx context.Context, req *connect.Request[v1.ListFormationsRequest]) (*connect.Response[v1.ListFormationsResponse], error) {
	return reply(&v1.ListFormationsResponse{Formations: s.formations.List(req.Msg.GetRunningOnly())}, nil)
}

func (s *MeshService) GetFormation(ctx context.Context, req *connect.Request[v1.GetFormationRequest]) (*connect.Response[v1.GetFormationResponse], error) {
	f, err := s.formations.Get(req.Msg.GetId())
	return reply(&v1.GetFormationResponse{Formation: f}, err)
}

func (s *MeshService) StopFormation(ctx context.Context, req *connect.Request[v1.StopFormationRequest]) (*connect.Response[v1.StopFormationResponse], error) {
	f, err := s.formations.Stop(ctx, req.Msg.GetId())
	return reply(&v1.StopFormationResponse{Formation: f}, err)
}

func (s *MeshService) DeleteFormation(ctx context.Context, req *connect.Request[v1.DeleteFormationRequest]) (*connect.Response[v1.DeleteFormationResponse], error) {
	f, err := s.formations.Delete(ctx, req.Msg.GetId())
	return reply(&v1.DeleteFormationResponse{Formation: f}, err)
}

func (s *MeshService) FormationLogs(ctx context.Context, req *connect.Request[v1.FormationLogsRequest], stream *connect.ServerStream[v1.FormationLogsResponse]) error {
	return wrap(s.formations.Logs(ctx, req.Msg.GetId(), req.Msg.GetNodeId(), req.Msg.GetRole(), req.Msg.GetFollow(), int(req.Msg.GetTail()), func(node, role string, lines []string) error {
		return stream.Send(&v1.FormationLogsResponse{Lines: lines, NodeId: node, Role: role})
	}))
}

// A node outside holds no session, so the admission calls carry no credential: the manager
// decides what each may do
func (s *MeshService) Knock(ctx context.Context, req *connect.Request[v1.KnockRequest]) (*connect.Response[v1.KnockResponse], error) {
	resp, err := s.mesh.Knock(ctx, req.Msg)
	if err != nil {
		return nil, passConnect(err)
	}
	return connect.NewResponse(resp), nil
}

func (s *MeshService) Offer(ctx context.Context, req *connect.Request[v1.OfferRequest]) (*connect.Response[v1.OfferResponse], error) {
	resp, err := s.mesh.Offer(ctx, req.Msg)
	if err != nil {
		return nil, passConnect(err)
	}
	return connect.NewResponse(resp), nil
}

func (s *MeshService) Welcome(ctx context.Context, req *connect.Request[v1.WelcomeRequest]) (*connect.Response[v1.WelcomeResponse], error) {
	resp, err := s.mesh.Welcome(ctx, req.Msg)
	if err != nil {
		return nil, passConnect(err)
	}
	return connect.NewResponse(resp), nil
}

// A connect error as the manager coded it, any other wrapped by kind
func passConnect(err error) error {
	var ce *connect.Error
	if errors.As(err, &ce) {
		return err
	}
	return wrap(err)
}

// The handshake itself admits the caller, so it carries no credential
func (s *MeshService) Hello(ctx context.Context, req *connect.Request[v1.HelloRequest]) (*connect.Response[v1.HelloResponse], error) {
	resp, err := s.mesh.Hello(ctx, req.Msg)
	if err != nil {
		return nil, passConnect(err)
	}
	return connect.NewResponse(resp), nil
}

func (s *MeshService) Sync(ctx context.Context, req *connect.Request[v1.SyncRequest]) (*connect.Response[v1.SyncResponse], error) {
	peer, err := s.peer(req.Header())
	if err != nil {
		return nil, err
	}
	resp, err := s.mesh.Sync(ctx, peer, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (s *MeshService) Stream(ctx context.Context, req *connect.Request[v1.StreamRequest], stream *connect.ServerStream[v1.StreamResponse]) error {
	if _, err := s.peer(req.Header()); err != nil {
		return err
	}
	return links.Stream(ctx, req.Msg.GetBytes(), stream.Send)
}

func (s *MeshService) RunSeat(ctx context.Context, req *connect.Request[v1.RunSeatRequest]) (*connect.Response[v1.RunSeatResponse], error) {
	peer, err := s.peer(req.Header())
	if err != nil {
		return nil, err
	}
	in, task, err := s.formations.RunSeat(ctx, peer, req.Msg.GetRun())
	return reply(&v1.RunSeatResponse{Instance: in, Task: task}, err)
}

func (s *MeshService) StopSeat(ctx context.Context, req *connect.Request[v1.StopSeatRequest]) (*connect.Response[v1.StopSeatResponse], error) {
	if _, err := s.peer(req.Header()); err != nil {
		return nil, err
	}
	in, err := s.formations.StopSeat(ctx, req.Msg.GetInstanceId())
	return reply(&v1.StopSeatResponse{Instance: in}, err)
}

func (s *MeshService) MoveSlot(ctx context.Context, req *connect.Request[v1.MoveSlotRequest]) (*connect.Response[v1.MoveSlotResponse], error) {
	if _, err := s.peer(req.Header()); err != nil {
		return nil, err
	}
	path, bytes, err := s.formations.MoveSlot(ctx, req.Msg.GetFormationId(), req.Msg.GetFromNodeId(), req.Msg.GetFile())
	return reply(&v1.MoveSlotResponse{Path: path, Bytes: bytes}, err)
}

func (s *MeshService) DropSlot(ctx context.Context, req *connect.Request[v1.DropSlotRequest]) (*connect.Response[v1.DropSlotResponse], error) {
	if _, err := s.peer(req.Header()); err != nil {
		return nil, err
	}
	return reply(&v1.DropSlotResponse{}, s.formations.DropSlot(req.Msg.GetFormationId(), req.Msg.GetFile()))
}

func (s *MeshService) GetSeat(ctx context.Context, req *connect.Request[v1.GetSeatRequest]) (*connect.Response[v1.GetSeatResponse], error) {
	if _, err := s.peer(req.Header()); err != nil {
		return nil, err
	}
	in, err := s.formations.GetSeat(req.Msg.GetInstanceId())
	return reply(&v1.GetSeatResponse{Instance: in}, err)
}

func (s *MeshService) PullSeat(ctx context.Context, req *connect.Request[v1.PullSeatRequest]) (*connect.Response[v1.PullSeatResponse], error) {
	if _, err := s.peer(req.Header()); err != nil {
		return nil, err
	}
	task, err := s.puller.Pull(ctx, &v1.PullRequest{SourceId: req.Msg.GetSourceId(), Repo: req.Msg.GetRepo(), Revision: req.Msg.GetRevision(), Group: req.Msg.GetGroup(), Alone: req.Msg.GetAlone()})
	return reply(&v1.PullSeatResponse{Task: task}, err)
}

func (s *MeshService) WatchPull(ctx context.Context, req *connect.Request[v1.WatchPullRequest], stream *connect.ServerStream[v1.WatchPullResponse]) error {
	if _, err := s.peer(req.Header()); err != nil {
		return err
	}
	return wrap(s.tasks.Watch(ctx, req.Msg.GetTaskId(), func(resp *v1.WatchTaskResponse) error {
		return stream.Send(&v1.WatchPullResponse{Task: resp.GetTask(), Logs: resp.GetLogs()})
	}))
}

func (s *MeshService) SeatLogs(ctx context.Context, req *connect.Request[v1.SeatLogsRequest], stream *connect.ServerStream[v1.SeatLogsResponse]) error {
	if _, err := s.peer(req.Header()); err != nil {
		return err
	}
	return wrap(s.instances.Logs(ctx, req.Msg.GetInstanceId(), req.Msg.GetFollow(), int(req.Msg.GetTail()), func(lines []string) error {
		return stream.Send(&v1.SeatLogsResponse{Lines: lines})
	}))
}

func (s *MeshService) GetStored(ctx context.Context, req *connect.Request[v1.GetStoredRequest]) (*connect.Response[v1.GetStoredResponse], error) {
	if _, err := s.peer(req.Header()); err != nil {
		return nil, err
	}
	m, err := s.store.ReadManifest(req.Msg.GetSourceId(), req.Msg.GetRepo(), req.Msg.GetGroup())
	return reply(&v1.GetStoredResponse{Model: m}, err)
}

func (s *MeshService) Rekey(ctx context.Context, req *connect.Request[v1.RekeyRequest]) (*connect.Response[v1.RekeyResponse], error) {
	if _, err := s.peer(req.Header()); err != nil {
		return nil, err
	}
	if err := s.mesh.Rekey(req.Msg.GetSecret()); err != nil {
		return nil, err
	}
	return connect.NewResponse(&v1.RekeyResponse{NodeId: s.mesh.Self()}), nil
}

func (s *MeshService) Bye(ctx context.Context, req *connect.Request[v1.ByeRequest]) (*connect.Response[v1.ByeResponse], error) {
	peer, err := s.peer(req.Header())
	if err != nil {
		return nil, err
	}
	s.mesh.Bye(peer)
	return connect.NewResponse(&v1.ByeResponse{}), nil
}
