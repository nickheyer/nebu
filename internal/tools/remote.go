package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"connectrpc.com/connect"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/proto/nebu/v1/nebuv1connect"
)

// Connections to other members, for their tool servers
type Peers interface {
	// This node's id
	Self() string
	// A member's API base and a client carrying the session credential
	Dial(ctx context.Context, nodeID string) (base string, client *http.Client, err error)
}

// A member's tool server, its tools listed over the mesh and its calls sent there
type remoteServer struct {
	status *v1.ToolServerStatus
	client nebuv1connect.ToolServiceClient
	tools  []Tool
}

// Finds a server on a member and lists its tools
func (r *Registry) remote(ctx context.Context, peers Peers, node, name string) (Server, error) {
	base, hc, err := peers.Dial(ctx, node)
	if err != nil {
		return nil, fmt.Errorf("the node of tool server %s/%s cannot be reached: %w", node, name, err)
	}
	client := nebuv1connect.NewToolServiceClient(hc, base)
	resp, err := client.ListToolServers(ctx, connect.NewRequest(&v1.ListToolServersRequest{}))
	if err != nil {
		return nil, fmt.Errorf("tool servers of node %s could not be listed: %w", node, err)
	}
	for _, st := range resp.Msg.GetServers() {
		if st.GetServer().GetName() != name {
			continue
		}
		switch st.GetState() {
		case v1.ToolServerState_TOOL_SERVER_STATE_READY:
		case v1.ToolServerState_TOOL_SERVER_STATE_DISABLED:
			return nil, fmt.Errorf("tool server %s is turned off on node %s", name, node)
		default:
			return nil, fmt.Errorf("tool server %s on node %s is not ready: %s", name, node, st.GetError())
		}
		s := &remoteServer{status: st, client: client}
		for _, t := range st.GetTools() {
			s.tools = append(s.tools, Tool{Name: t.GetName(), Inner: t.GetName(), Description: t.GetDescription(), Schema: json.RawMessage(t.GetSchema()), ReadOnly: t.GetReadOnly()})
		}
		return s, nil
	}
	return nil, fmt.Errorf("%w %q on node %s", ErrUnknownServer, name, node)
}

func (s *remoteServer) Name() string            { return s.status.GetServer().GetName() }
func (s *remoteServer) Kind() v1.ToolServerKind { return s.status.GetServer().GetKind() }
func (s *remoteServer) Tools() []Tool           { return append([]Tool(nil), s.tools...) }

func (s *remoteServer) Call(ctx context.Context, c Call) (*Result, error) {
	return callWith(ctx, s.client, c.Name, c.Args)
}

// Runs a tool on a member
func callRemote(ctx context.Context, peers Peers, node, name string, args json.RawMessage) (*Result, error) {
	base, hc, err := peers.Dial(ctx, node)
	if err != nil {
		return nil, fmt.Errorf("node %s cannot be reached: %w", node, err)
	}
	return callWith(ctx, nebuv1connect.NewToolServiceClient(hc, base), name, args)
}

func callWith(ctx context.Context, client nebuv1connect.ToolServiceClient, name string, args json.RawMessage) (*Result, error) {
	resp, err := client.CallTool(ctx, connect.NewRequest(&v1.CallToolRequest{Name: name, Arguments: string(args)}))
	if err != nil {
		return textResult("The tool could not be called on its node: "+err.Error(), true), fmt.Errorf("%s over the mesh: %w", name, err)
	}
	out := FromParts(resp.Msg.GetParts(), resp.Msg.GetFailed())
	out.Asked = true
	return out, nil
}

// A result as the API carries it
func FromParts(parts []*v1.ToolPart, failed bool) *Result {
	out := &Result{Failed: failed}
	for _, p := range parts {
		out.Parts = append(out.Parts, Part{Type: p.GetType(), Text: p.GetText(), MediaType: p.GetMediaType(), Data: p.GetData()})
	}
	return out
}

// Parts as the API carries them
func ToParts(r *Result) []*v1.ToolPart {
	out := make([]*v1.ToolPart, 0, len(r.Parts))
	for _, p := range r.Parts {
		out = append(out, &v1.ToolPart{Type: p.Type, Text: p.Text, MediaType: p.MediaType, Data: p.Data})
	}
	return out
}
