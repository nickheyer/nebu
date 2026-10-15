package services

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"connectrpc.com/connect"
	"github.com/nickheyer/nebu/internal/mesh"
	"github.com/nickheyer/nebu/internal/tools"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/proto/nebu/v1/nebuv1connect"
)

var _ nebuv1connect.ToolServiceHandler = (*ToolService)(nil)

// Serves tool servers and runs their tools
type ToolService struct {
	tools *tools.Registry
	mesh  *mesh.Manager
}

func NewToolService(r *tools.Registry, m *mesh.Manager) *ToolService {
	return &ToolService{tools: r, mesh: m}
}

// Lists this node's tool servers and every reachable member's when asked, node qualified
func (s *ToolService) ListToolServers(ctx context.Context, req *connect.Request[v1.ListToolServersRequest]) (*connect.Response[v1.ListToolServersResponse], error) {
	out := s.tools.Status()
	if req.Msg.GetMesh() && s.mesh != nil && s.mesh.Joined() {
		self := s.mesh.Self()
		for _, n := range s.mesh.Nodes() {
			if n.GetId() == self || n.GetState() != v1.NodeState_NODE_STATE_READY {
				continue
			}
			cl, err := s.mesh.Client(ctx, n.GetId())
			if err != nil {
				continue
			}
			resp, err := nebuv1connect.NewToolServiceClient(cl.HTTP, cl.Base).ListToolServers(ctx, connect.NewRequest(&v1.ListToolServersRequest{}))
			if err != nil {
				continue
			}
			for _, st := range resp.Msg.GetServers() {
				st.NodeId, st.Ref = n.GetId(), n.GetId()+"/"+st.GetServer().GetName()
				out = append(out, st)
			}
		}
	}
	return reply(&v1.ListToolServersResponse{Servers: out}, nil)
}

// Runs one tool by the name the model calls it, here or on a member
func (s *ToolService) CallTool(ctx context.Context, req *connect.Request[v1.CallToolRequest]) (*connect.Response[v1.CallToolResponse], error) {
	args := json.RawMessage(req.Msg.GetArguments())
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}
	if !json.Valid(args) {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("the arguments are not JSON"))
	}
	start := nowMillis()
	res, err := s.tools.Call(ctx, req.Msg.GetNodeId(), req.Msg.GetName(), args)
	if res == nil {
		return nil, wrap(err)
	}
	return reply(&v1.CallToolResponse{Parts: tools.ToParts(res), Failed: res.Failed, TookMs: uint32(nowMillis() - start)}, nil)
}

func nowMillis() int64 { return time.Now().UnixMilli() }
