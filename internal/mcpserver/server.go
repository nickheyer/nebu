// Package mcpserver offers nebu's models, runtimes, routes, traces, mesh, and tools to MCP clients.
package mcpserver

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"connectrpc.com/connect"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/nickheyer/nebu/internal/auth"
	"github.com/nickheyer/nebu/internal/chat"
	"github.com/nickheyer/nebu/internal/doctor"
	"github.com/nickheyer/nebu/internal/formations"
	"github.com/nickheyer/nebu/internal/gateway"
	"github.com/nickheyer/nebu/internal/inspect"
	"github.com/nickheyer/nebu/internal/installs"
	"github.com/nickheyer/nebu/internal/instances"
	"github.com/nickheyer/nebu/internal/mesh"
	"github.com/nickheyer/nebu/internal/pull"
	"github.com/nickheyer/nebu/internal/rpc/services"
	"github.com/nickheyer/nebu/internal/slots"
	"github.com/nickheyer/nebu/internal/tasks"
	"github.com/nickheyer/nebu/internal/tools"
	"github.com/nickheyer/nebu/pkg/events"
	"github.com/nickheyer/nebu/pkg/formats"
	"github.com/nickheyer/nebu/pkg/host"
	"github.com/nickheyer/nebu/pkg/perf"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/runtimes"
	"github.com/nickheyer/nebu/pkg/sources"
	"github.com/nickheyer/nebu/pkg/store"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// Everything the server answers with
type Deps struct {
	Host       *host.Prober
	Doctor     *doctor.Doctor
	Sources    *sources.Manager
	Inspector  *inspect.Inspector
	Formats    *formats.Registry
	Runtimes   *runtimes.Registry
	Store      *store.Store
	Puller     *pull.Puller
	Tasks      *tasks.Manager
	Installs   *installs.Manager
	Instances  *instances.Manager
	Slots      *slots.Manager
	Gateway    *gateway.Gateway
	Mesh       *mesh.Manager
	Formations *formations.Manager
	Perf       *perf.Table
	Guard      *auth.Guard
	Tools      *tools.Registry
	Events     *events.Bus
	Version    string
	Log        *slog.Logger
}

// The services the tools wrap, called in process
type nebu struct {
	d        Deps
	host     *services.HostService
	sources  *services.SourceService
	runtimes *services.RuntimeService
	store    *services.StoreService
	instance *services.InstanceService
	slots    *services.SlotService
	gateway  *services.GatewayService
	tasks    *services.TaskService
	mesh     *services.MeshService
	tools    *services.ToolService
	client   *http.Client
}

// The streamable HTTP handler of nebu's MCP server, one server for every session
func Handler(d Deps) http.Handler {
	server := New(d)
	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{DisableLocalhostProtection: true, Logger: d.Log})
}

// Builds the server with every tool and resource
func New(d Deps) *mcp.Server {
	n := &nebu{
		d:        d,
		host:     services.NewHostService(d.Host, d.Doctor, nil),
		sources:  services.NewSourceService(d.Sources, d.Inspector, d.Formats, d.Runtimes),
		runtimes: services.NewRuntimeService(d.Runtimes, d.Host, d.Installs, d.Formats),
		store:    services.NewStoreService(d.Store, d.Puller, d.Events, d.Formations.PullTo),
		instance: services.NewInstanceService(d.Instances),
		slots:    services.NewSlotService(d.Slots),
		gateway:  services.NewGatewayService(d.Gateway, d.Instances, d.Slots),
		tasks:    services.NewTaskService(d.Tasks),
		mesh:     services.NewMeshService(d.Mesh, d.Formations, d.Puller, d.Tasks, d.Store, d.Perf, d.Instances, d.Guard),
		tools:    services.NewToolService(d.Tools, d.Mesh),
		client:   d.Gateway.Client("mcp"),
	}
	s := mcp.NewServer(&mcp.Implementation{Name: "nebu", Title: "Nebu", Version: d.Version, Description: "Model weights, inference runtimes, routes, and the mesh they run on"}, &mcp.ServerOptions{
		Instructions: "Nebu manages model weights and inference runtimes. List what is stored and running before changing anything, pull and run models by source, repo, and group, and read traces to see what the gateway served.",
		Logger:       d.Log,
	})
	n.addTools(s)
	n.addResources(s)
	return s
}

// A tool answering with the JSON of a protobuf message
func text(msg proto.Message) *mcp.CallToolResult {
	data, _ := protojson.MarshalOptions{UseProtoNames: true}.Marshal(msg)
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(data)}}}
}

// The error of a Connect call as the tool reports it
func unwrap(err error) error {
	if ce, ok := err.(*connect.Error); ok {
		return fmt.Errorf("%s", ce.Message())
	}
	return err
}

type listModelsIn struct{}

type searchModelsIn struct {
	Query    string `json:"query" jsonschema:"what to search for"`
	SourceID string `json:"source_id,omitempty" jsonschema:"the source to search, every source when empty"`
	Limit    int    `json:"limit,omitempty" jsonschema:"results to return, 20 when unset"`
}

type modelRef struct {
	SourceID string `json:"source_id" jsonschema:"the source the repo lives on, such as huggingface"`
	Repo     string `json:"repo" jsonschema:"the repository, such as Qwen/Qwen3-8B"`
	Group    string `json:"group,omitempty" jsonschema:"the weight group within the repo, the default group when empty"`
}

type pullModelIn struct {
	modelRef
	Revision string `json:"revision,omitempty"`
	NodeID   string `json:"node_id,omitempty" jsonschema:"the mesh member to pull onto, this node when empty"`
}

type removeModelIn struct {
	modelRef
	Gc bool `json:"gc,omitempty" jsonschema:"whether to collect the blobs nothing else refers to"`
}

type runtimeIn struct {
	RuntimeID string `json:"runtime_id,omitempty"`
}

type installRuntimeIn struct {
	RuntimeID string `json:"runtime_id" jsonschema:"llamacpp, vllm, sglang, nemo, sdcpp, or diffusers"`
	Method    string `json:"method" jsonschema:"an install method the runtime lists, such as adopt, release, or source"`
}

type listInstancesIn struct {
	RunningOnly bool `json:"running_only,omitempty"`
}

type runModelIn struct {
	modelRef
	RuntimeID string            `json:"runtime_id,omitempty" jsonschema:"the runtime to serve with, the first installed one that takes the format when empty"`
	InstallID string            `json:"install_id,omitempty"`
	Name      string            `json:"name,omitempty" jsonschema:"the model name clients send, the repo name when empty"`
	SlotID    string            `json:"slot_id,omitempty" jsonschema:"the slot to run in, outside any slot when empty"`
	Params    map[string]string `json:"params,omitempty" jsonschema:"runtime parameters by name, such as n_ctx"`
	Span      []string          `json:"span,omitempty" jsonschema:"mesh nodes or node/device ids the run may use, this node alone when empty"`
	Shape     string            `json:"shape,omitempty" jsonschema:"solo, chain, lockstep, relay, replicas, draft, or stages, chosen by the planner when empty"`
}

type idIn struct {
	ID string `json:"id"`
}

type swapSlotIn struct {
	SlotID string `json:"slot_id" jsonschema:"the slot by id or name"`
	modelRef
	RuntimeID  string            `json:"runtime_id,omitempty"`
	Params     map[string]string `json:"params,omitempty"`
	DrainFirst bool              `json:"drain_first,omitempty" jsonschema:"whether to let the occupant finish its requests before it stops"`
}

type setRouteIn struct {
	Name          string   `json:"name" jsonschema:"the model name clients send"`
	SlotID        string   `json:"slot_id,omitempty" jsonschema:"the slot the name maps to"`
	InstanceID    string   `json:"instance_id,omitempty" jsonschema:"the ready instance the name maps to, when no slot"`
	ToolServers   []string `json:"tool_servers,omitempty" jsonschema:"tool servers every request on the route gets"`
	MutatingTools bool     `json:"mutating_tools,omitempty"`
}

type nameIn struct {
	Name string `json:"name"`
}

type listTracesIn struct {
	Route string `json:"route,omitempty"`
	Limit int    `json:"limit,omitempty" jsonschema:"traces to return, 50 when unset"`
}

type hostProfileIn struct {
	Refresh bool `json:"refresh,omitempty" jsonschema:"whether to probe the host again instead of answering from the last probe"`
}

type emptyIn struct{}

type listTasksIn struct {
	ActiveOnly bool `json:"active_only,omitempty"`
}

type formationIn struct {
	modelRef
	RuntimeID string            `json:"runtime_id,omitempty"`
	Params    map[string]string `json:"params,omitempty"`
	Span      []string          `json:"span,omitempty" jsonschema:"mesh nodes or node/device ids the formation may use, every member when empty"`
	Shape     string            `json:"shape,omitempty" jsonschema:"chain, lockstep, relay, replicas, draft, or stages, chosen by the planner when empty"`
}

type listFormationsIn struct {
	RunningOnly bool `json:"running_only,omitempty"`
}

type chatIn struct {
	Model     string `json:"model" jsonschema:"a route name the gateway serves"`
	Prompt    string `json:"prompt"`
	System    string `json:"system,omitempty"`
	MaxTokens int    `json:"max_tokens,omitempty"`
}

type imageIn struct {
	Model  string `json:"model" jsonschema:"an image route name the gateway serves"`
	Prompt string `json:"prompt"`
	Size   string `json:"size,omitempty" jsonschema:"WIDTHxHEIGHT, the model's default when empty"`
	N      int    `json:"n,omitempty" jsonschema:"images to make, 1 when unset"`
}

type listToolServersIn struct {
	Mesh bool `json:"mesh,omitempty" jsonschema:"whether to list every member's servers too"`
}

type callToolIn struct {
	Name      string          `json:"name" jsonschema:"the tool as a model calls it, such as web_search"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
	NodeID    string          `json:"node_id,omitempty"`
}

// Adds every tool, each one a call of the service behind the API
func (n *nebu) addTools(s *mcp.Server) {
	ro := &mcp.ToolAnnotations{ReadOnlyHint: true}
	mcp.AddTool(s, &mcp.Tool{Name: "list_models", Description: "Lists the models stored on this node with their formats, sizes, and runtimes", Annotations: ro}, func(ctx context.Context, _ *mcp.CallToolRequest, _ listModelsIn) (*mcp.CallToolResult, any, error) {
		r, err := n.store.ListModels(ctx, connect.NewRequest(&v1.ListModelsRequest{}))
		return answer(r, err)
	})
	mcp.AddTool(s, &mcp.Tool{Name: "search_models", Description: "Searches the model sources, such as Hugging Face, for repositories to pull", Annotations: ro}, func(ctx context.Context, _ *mcp.CallToolRequest, in searchModelsIn) (*mcp.CallToolResult, any, error) {
		limit := uint32(in.Limit)
		if limit == 0 {
			limit = 20
		}
		r, err := n.sources.Search(ctx, connect.NewRequest(&v1.SearchRequest{Query: in.Query, SourceId: in.SourceID, Limit: limit}))
		return answer(r, err)
	})
	mcp.AddTool(s, &mcp.Tool{Name: "pull_model", Description: "Downloads a model into the store and answers with the task to watch"}, func(ctx context.Context, _ *mcp.CallToolRequest, in pullModelIn) (*mcp.CallToolResult, any, error) {
		r, err := n.store.Pull(ctx, connect.NewRequest(&v1.PullRequest{SourceId: in.SourceID, Repo: in.Repo, Group: in.Group, Revision: in.Revision, NodeId: in.NodeID}))
		return answer(r, err)
	})
	mcp.AddTool(s, &mcp.Tool{Name: "remove_model", Description: "Removes a stored model", Annotations: &mcp.ToolAnnotations{DestructiveHint: ptr(true)}}, func(ctx context.Context, _ *mcp.CallToolRequest, in removeModelIn) (*mcp.CallToolResult, any, error) {
		r, err := n.store.RemoveModel(ctx, connect.NewRequest(&v1.RemoveModelRequest{SourceId: in.SourceID, Repo: in.Repo, Group: in.Group, Gc: in.Gc}))
		return answer(r, err)
	})
	mcp.AddTool(s, &mcp.Tool{Name: "list_runtimes", Description: "Lists the runtimes, their install methods, and what the host lacks for each", Annotations: ro}, func(ctx context.Context, _ *mcp.CallToolRequest, _ emptyIn) (*mcp.CallToolResult, any, error) {
		r, err := n.runtimes.ListRuntimes(ctx, connect.NewRequest(&v1.ListRuntimesRequest{}))
		return answer(r, err)
	})
	mcp.AddTool(s, &mcp.Tool{Name: "list_installs", Description: "Lists installed runtimes with their versions and facts", Annotations: ro}, func(ctx context.Context, _ *mcp.CallToolRequest, in runtimeIn) (*mcp.CallToolResult, any, error) {
		r, err := n.runtimes.ListInstalls(ctx, connect.NewRequest(&v1.ListInstallsRequest{RuntimeId: in.RuntimeID}))
		return answer(r, err)
	})
	mcp.AddTool(s, &mcp.Tool{Name: "install_runtime", Description: "Installs a runtime by one of its methods and answers with the task to watch"}, func(ctx context.Context, _ *mcp.CallToolRequest, in installRuntimeIn) (*mcp.CallToolResult, any, error) {
		r, err := n.runtimes.Install(ctx, connect.NewRequest(&v1.InstallRequest{RuntimeId: in.RuntimeID, Method: in.Method}))
		return answer(r, err)
	})
	mcp.AddTool(s, &mcp.Tool{Name: "list_instances", Description: "Lists running and past model instances with their states and endpoints", Annotations: ro}, func(ctx context.Context, _ *mcp.CallToolRequest, in listInstancesIn) (*mcp.CallToolResult, any, error) {
		r, err := n.instance.ListInstances(ctx, connect.NewRequest(&v1.ListInstancesRequest{RunningOnly: in.RunningOnly}))
		return answer(r, err)
	})
	mcp.AddTool(s, &mcp.Tool{Name: "run_model", Description: "Starts a stored model on a runtime and answers with the instance and its startup task"}, func(ctx context.Context, _ *mcp.CallToolRequest, in runModelIn) (*mcp.CallToolResult, any, error) {
		shape, err := shapeOf(in.Shape)
		if err != nil {
			return nil, nil, err
		}
		r, err := n.instance.Run(ctx, connect.NewRequest(&v1.RunRequest{SourceId: in.SourceID, Repo: in.Repo, Group: in.Group, RuntimeId: in.RuntimeID, InstallId: in.InstallID, Name: in.Name, SlotId: in.SlotID, Params: in.Params, Span: in.Span, Shape: shape}))
		return answer(r, err)
	})
	mcp.AddTool(s, &mcp.Tool{Name: "stop_model", Description: "Stops a running instance by id or name"}, func(ctx context.Context, _ *mcp.CallToolRequest, in idIn) (*mcp.CallToolResult, any, error) {
		r, err := n.instance.StopInstance(ctx, connect.NewRequest(&v1.StopInstanceRequest{Id: in.ID}))
		return answer(r, err)
	})
	mcp.AddTool(s, &mcp.Tool{Name: "list_slots", Description: "Lists slots, the device and memory reservations models run in", Annotations: ro}, func(ctx context.Context, _ *mcp.CallToolRequest, _ emptyIn) (*mcp.CallToolResult, any, error) {
		r, err := n.slots.ListSlots(ctx, connect.NewRequest(&v1.ListSlotsRequest{}))
		return answer(r, err)
	})
	mcp.AddTool(s, &mcp.Tool{Name: "swap_slot", Description: "Puts a model into a slot, replacing its occupant, and answers with the slot, the instance, and the task"}, func(ctx context.Context, _ *mcp.CallToolRequest, in swapSlotIn) (*mcp.CallToolResult, any, error) {
		r, err := n.slots.Swap(ctx, connect.NewRequest(&v1.SwapRequest{SlotId: in.SlotID, DrainFirst: in.DrainFirst, Run: &v1.RunRequest{SourceId: in.SourceID, Repo: in.Repo, Group: in.Group, RuntimeId: in.RuntimeID, Params: in.Params}}))
		return answer(r, err)
	})
	mcp.AddTool(s, &mcp.Tool{Name: "evict_slot", Description: "Stops the occupant of a slot, keeping the slot"}, func(ctx context.Context, _ *mcp.CallToolRequest, in idIn) (*mcp.CallToolResult, any, error) {
		r, err := n.slots.EvictSlot(ctx, connect.NewRequest(&v1.EvictSlotRequest{Id: in.ID}))
		return answer(r, err)
	})
	mcp.AddTool(s, &mcp.Tool{Name: "list_routes", Description: "Lists the model names the gateway serves, what each maps to, and how each takes tools", Annotations: ro}, func(ctx context.Context, _ *mcp.CallToolRequest, _ emptyIn) (*mcp.CallToolResult, any, error) {
		r, err := n.gateway.ListRoutes(ctx, connect.NewRequest(&v1.ListRoutesRequest{}))
		return answer(r, err)
	})
	mcp.AddTool(s, &mcp.Tool{Name: "set_route", Description: "Maps a model name onto a slot or a running instance, with the tool servers its requests get"}, func(ctx context.Context, _ *mcp.CallToolRequest, in setRouteIn) (*mcp.CallToolResult, any, error) {
		var profile *v1.Profile
		if len(in.ToolServers) > 0 {
			profile = &v1.Profile{ToolServers: in.ToolServers, MutatingTools: in.MutatingTools}
		}
		r, err := n.gateway.SetRoute(ctx, connect.NewRequest(&v1.SetRouteRequest{Name: in.Name, SlotId: in.SlotID, InstanceId: in.InstanceID, Profile: profile}))
		return answer(r, err)
	})
	mcp.AddTool(s, &mcp.Tool{Name: "delete_route", Description: "Removes a model name the gateway serves", Annotations: &mcp.ToolAnnotations{DestructiveHint: ptr(true)}}, func(ctx context.Context, _ *mcp.CallToolRequest, in nameIn) (*mcp.CallToolResult, any, error) {
		r, err := n.gateway.DeleteRoute(ctx, connect.NewRequest(&v1.DeleteRouteRequest{Name: in.Name}))
		return answer(r, err)
	})
	mcp.AddTool(s, &mcp.Tool{Name: "list_traces", Description: "Lists recent gateway requests newest first, with timings, tokens, and tool calls", Annotations: ro}, func(ctx context.Context, _ *mcp.CallToolRequest, in listTracesIn) (*mcp.CallToolResult, any, error) {
		limit := uint32(in.Limit)
		if limit == 0 {
			limit = 50
		}
		r, err := n.gateway.ListTraces(ctx, connect.NewRequest(&v1.ListTracesRequest{Route: in.Route, Limit: limit}))
		return answer(r, err)
	})
	mcp.AddTool(s, &mcp.Tool{Name: "get_trace", Description: "Reads one gateway request with its bodies and the tools the gateway ran", Annotations: ro}, func(ctx context.Context, _ *mcp.CallToolRequest, in idIn) (*mcp.CallToolResult, any, error) {
		r, err := n.gateway.GetTrace(ctx, connect.NewRequest(&v1.GetTraceRequest{Id: in.ID}))
		return answer(r, err)
	})
	mcp.AddTool(s, &mcp.Tool{Name: "host_profile", Description: "Reads the host's devices, memory pools, and storage", Annotations: ro}, func(ctx context.Context, _ *mcp.CallToolRequest, in hostProfileIn) (*mcp.CallToolResult, any, error) {
		r, err := n.host.GetProfile(ctx, connect.NewRequest(&v1.GetProfileRequest{Refresh: in.Refresh}))
		return answer(r, err)
	})
	mcp.AddTool(s, &mcp.Tool{Name: "doctor", Description: "Probes the host and checks its dependencies, answering with the task whose log lists each check"}, func(ctx context.Context, _ *mcp.CallToolRequest, _ emptyIn) (*mcp.CallToolResult, any, error) {
		r, err := n.host.Doctor(ctx, connect.NewRequest(&v1.DoctorRequest{}))
		return answer(r, err)
	})
	mcp.AddTool(s, &mcp.Tool{Name: "list_tasks", Description: "Lists long running tasks, such as pulls, installs, and launches", Annotations: ro}, func(ctx context.Context, _ *mcp.CallToolRequest, in listTasksIn) (*mcp.CallToolResult, any, error) {
		r, err := n.tasks.ListTasks(ctx, connect.NewRequest(&v1.ListTasksRequest{ActiveOnly: in.ActiveOnly}))
		return answer(r, err)
	})
	mcp.AddTool(s, &mcp.Tool{Name: "get_task", Description: "Reads one task with its progress and log", Annotations: ro}, func(ctx context.Context, _ *mcp.CallToolRequest, in idIn) (*mcp.CallToolResult, any, error) {
		r, err := n.tasks.GetTask(ctx, connect.NewRequest(&v1.GetTaskRequest{Id: in.ID}))
		return answer(r, err)
	})
	mcp.AddTool(s, &mcp.Tool{Name: "cancel_task", Description: "Cancels a running task"}, func(ctx context.Context, _ *mcp.CallToolRequest, in idIn) (*mcp.CallToolResult, any, error) {
		r, err := n.tasks.CancelTask(ctx, connect.NewRequest(&v1.CancelTaskRequest{Id: in.ID}))
		return answer(r, err)
	})
	mcp.AddTool(s, &mcp.Tool{Name: "mesh_status", Description: "Reads the mesh this node belongs to, what is nearby, and admissions under way", Annotations: ro}, func(ctx context.Context, _ *mcp.CallToolRequest, _ emptyIn) (*mcp.CallToolResult, any, error) {
		r, err := n.mesh.GetMesh(ctx, connect.NewRequest(&v1.GetMeshRequest{}))
		return answer(r, err)
	})
	mcp.AddTool(s, &mcp.Tool{Name: "list_nodes", Description: "Lists the mesh members with their devices, links, and stored models", Annotations: ro}, func(ctx context.Context, _ *mcp.CallToolRequest, _ emptyIn) (*mcp.CallToolResult, any, error) {
		r, err := n.mesh.ListNodes(ctx, connect.NewRequest(&v1.ListNodesRequest{}))
		return answer(r, err)
	})
	mcp.AddTool(s, &mcp.Tool{Name: "plan_formation", Description: "Plans how a model would run across mesh members, answering every candidate with its numbers", Annotations: ro}, func(ctx context.Context, _ *mcp.CallToolRequest, in formationIn) (*mcp.CallToolResult, any, error) {
		run, err := formationRun(in)
		if err != nil {
			return nil, nil, err
		}
		r, err := n.mesh.PlanFormation(ctx, connect.NewRequest(&v1.PlanFormationRequest{Run: run}))
		return answer(r, err)
	})
	mcp.AddTool(s, &mcp.Tool{Name: "run_formation", Description: "Plans and launches a model across mesh members, answering with the formation and its task"}, func(ctx context.Context, _ *mcp.CallToolRequest, in formationIn) (*mcp.CallToolResult, any, error) {
		run, err := formationRun(in)
		if err != nil {
			return nil, nil, err
		}
		r, err := n.mesh.RunFormation(ctx, connect.NewRequest(&v1.RunFormationRequest{Run: run}))
		return answer(r, err)
	})
	mcp.AddTool(s, &mcp.Tool{Name: "stop_formation", Description: "Stops a formation on every member"}, func(ctx context.Context, _ *mcp.CallToolRequest, in idIn) (*mcp.CallToolResult, any, error) {
		r, err := n.mesh.StopFormation(ctx, connect.NewRequest(&v1.StopFormationRequest{Id: in.ID}))
		return answer(r, err)
	})
	mcp.AddTool(s, &mcp.Tool{Name: "list_formations", Description: "Lists formations, models running across mesh members", Annotations: ro}, func(ctx context.Context, _ *mcp.CallToolRequest, in listFormationsIn) (*mcp.CallToolResult, any, error) {
		r, err := n.mesh.ListFormations(ctx, connect.NewRequest(&v1.ListFormationsRequest{RunningOnly: in.RunningOnly}))
		return answer(r, err)
	})
	mcp.AddTool(s, &mcp.Tool{Name: "chat", Description: "Sends a prompt to a model the gateway serves and answers with its reply", Annotations: ro}, n.chat)
	mcp.AddTool(s, &mcp.Tool{Name: "generate_image", Description: "Makes images with a diffusion model the gateway serves"}, n.image)
	mcp.AddTool(s, &mcp.Tool{Name: "list_tool_servers", Description: "Lists the tool servers Settings hold, their states, and the tools each offers", Annotations: ro}, func(ctx context.Context, _ *mcp.CallToolRequest, in listToolServersIn) (*mcp.CallToolResult, any, error) {
		r, err := n.tools.ListToolServers(ctx, connect.NewRequest(&v1.ListToolServersRequest{Mesh: in.Mesh}))
		return answer(r, err)
	})
	mcp.AddTool(s, &mcp.Tool{Name: "call_tool", Description: "Runs one tool of nebu's tool servers, such as web_search or web_fetch"}, func(ctx context.Context, _ *mcp.CallToolRequest, in callToolIn) (*mcp.CallToolResult, any, error) {
		r, err := n.tools.CallTool(ctx, connect.NewRequest(&v1.CallToolRequest{Name: in.Name, Arguments: string(in.Arguments), NodeId: in.NodeID}))
		if err != nil {
			return nil, nil, unwrap(err)
		}
		out := &mcp.CallToolResult{IsError: r.Msg.GetFailed()}
		for _, p := range r.Msg.GetParts() {
			if p.GetType() == "image" {
				data, _ := base64.StdEncoding.DecodeString(p.GetData())
				out.Content = append(out.Content, &mcp.ImageContent{MIMEType: p.GetMediaType(), Data: data})
				continue
			}
			out.Content = append(out.Content, &mcp.TextContent{Text: p.GetText()})
		}
		return out, nil, nil
	})
}

// A service's answer as a tool result, its error as the tool's
func answer[T any, PT interface {
	*T
	proto.Message
}](r *connect.Response[T], err error) (*mcp.CallToolResult, any, error) {
	if err != nil {
		return nil, nil, unwrap(err)
	}
	return text(PT(r.Msg)), nil, nil
}

func ptr[T any](v T) *T { return &v }

// The shape a name stands for, auto when empty
func shapeOf(name string) (v1.Shape, error) {
	if name == "" {
		return v1.Shape_SHAPE_UNSPECIFIED, nil
	}
	if shape, ok := v1.Shape_value["SHAPE_"+strings.ToUpper(name)]; ok {
		return v1.Shape(shape), nil
	}
	return 0, fmt.Errorf("shape %q is not solo, chain, lockstep, relay, replicas, draft, or stages", name)
}

// The run request a formation tool describes
func formationRun(in formationIn) (*v1.RunRequest, error) {
	shape, err := shapeOf(in.Shape)
	if err != nil {
		return nil, err
	}
	return &v1.RunRequest{SourceId: in.SourceID, Repo: in.Repo, Group: in.Group, RuntimeId: in.RuntimeID, Params: in.Params, Span: in.Span, Shape: shape}, nil
}

// Asks a served model through the gateway in process
func (n *nebu) chat(ctx context.Context, _ *mcp.CallToolRequest, in chatIn) (*mcp.CallToolResult, any, error) {
	req := &chat.Request{Kind: chat.Chat, Model: in.Model, MaxTokens: in.MaxTokens}
	if in.System != "" {
		req.Messages = append(req.Messages, chat.Message{Role: "system", Parts: []chat.Part{{Type: "text", Text: in.System}}})
	}
	req.Messages = append(req.Messages, chat.Message{Role: "user", Parts: []chat.Part{{Type: "text", Text: in.Prompt}}})
	flavor := chat.RuntimeOf(v1.ApiFlavor_API_FLAVOR_OPENAI)
	path, body, err := flavor.Render(req)
	if err != nil {
		return nil, nil, err
	}
	raw, err := n.post(ctx, path, body)
	if err != nil {
		return nil, nil, err
	}
	res, err := flavor.ParseResult(req, raw)
	if err != nil {
		return nil, nil, err
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: res.Text}}}, nil, nil
}

// Makes images through the gateway in process, answering each as image content
func (n *nebu) image(ctx context.Context, _ *mcp.CallToolRequest, in imageIn) (*mcp.CallToolResult, any, error) {
	req := map[string]any{"model": in.Model, "prompt": in.Prompt, "response_format": "b64_json"}
	if in.Size != "" {
		req["size"] = in.Size
	}
	if in.N > 0 {
		req["n"] = in.N
	}
	body, _ := json.Marshal(req)
	raw, err := n.post(ctx, "/v1/images/generations", body)
	if err != nil {
		return nil, nil, err
	}
	var resp struct {
		Data []struct {
			B64JSON string `json:"b64_json"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, nil, fmt.Errorf("the gateway answered in a shape the server could not read: %w", err)
	}
	out := &mcp.CallToolResult{}
	for _, img := range resp.Data {
		data, err := base64.StdEncoding.DecodeString(img.B64JSON)
		if err != nil {
			return nil, nil, fmt.Errorf("the gateway answered an image that is not base64: %w", err)
		}
		out.Content = append(out.Content, &mcp.ImageContent{MIMEType: http.DetectContentType(data), Data: data})
	}
	if len(out.Content) == 0 {
		return nil, nil, fmt.Errorf("the gateway answered no image")
	}
	return out, nil, nil
}

// Posts to the gateway in process and reads the whole answer, its error message on failure
func (n *nebu) post(ctx context.Context, path string, body []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.d.Gateway.LocalBase()+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := n.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= http.StatusMultipleChoices {
		message := chat.RuntimeOf(v1.ApiFlavor_API_FLAVOR_OPENAI).ErrorMessage(raw)
		if message == "" {
			message = strings.TrimSpace(string(raw))
		}
		return nil, fmt.Errorf("%s", message)
	}
	return raw, nil
}

// Adds the resources, the lists a client reads whole and traces by id
func (n *nebu) addResources(s *mcp.Server) {
	read := func(uri, name, description string, fetch func(context.Context) (proto.Message, error)) {
		s.AddResource(&mcp.Resource{URI: uri, Name: name, Description: description, MIMEType: "application/json"}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
			msg, err := fetch(ctx)
			if err != nil {
				return nil, unwrap(err)
			}
			data, _ := protojson.MarshalOptions{UseProtoNames: true}.Marshal(msg)
			return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: uri, MIMEType: "application/json", Text: string(data)}}}, nil
		})
	}
	read("nebu://routes", "routes", "The model names the gateway serves", func(ctx context.Context) (proto.Message, error) {
		r, err := n.gateway.ListRoutes(ctx, connect.NewRequest(&v1.ListRoutesRequest{}))
		return r.Msg, err
	})
	read("nebu://models", "models", "The models stored on this node", func(ctx context.Context) (proto.Message, error) {
		r, err := n.store.ListModels(ctx, connect.NewRequest(&v1.ListModelsRequest{}))
		return r.Msg, err
	})
	read("nebu://instances", "instances", "Running and past model instances", func(ctx context.Context) (proto.Message, error) {
		r, err := n.instance.ListInstances(ctx, connect.NewRequest(&v1.ListInstancesRequest{}))
		return r.Msg, err
	})
	read("nebu://slots", "slots", "The slots models run in", func(ctx context.Context) (proto.Message, error) {
		r, err := n.slots.ListSlots(ctx, connect.NewRequest(&v1.ListSlotsRequest{}))
		return r.Msg, err
	})
	read("nebu://host", "host", "The host's devices, memory pools, and storage", func(ctx context.Context) (proto.Message, error) {
		r, err := n.host.GetProfile(ctx, connect.NewRequest(&v1.GetProfileRequest{}))
		return r.Msg, err
	})
	read("nebu://mesh", "mesh", "The mesh this node belongs to", func(ctx context.Context) (proto.Message, error) {
		r, err := n.mesh.GetMesh(ctx, connect.NewRequest(&v1.GetMeshRequest{}))
		return r.Msg, err
	})
	read("nebu://tools", "tool servers", "The tool servers Settings hold and the tools they offer", func(ctx context.Context) (proto.Message, error) {
		r, err := n.tools.ListToolServers(ctx, connect.NewRequest(&v1.ListToolServersRequest{}))
		return r.Msg, err
	})
	s.AddResourceTemplate(&mcp.ResourceTemplate{URITemplate: "nebu://traces/{id}", Name: "trace", Description: "One gateway request with its bodies and tool calls", MIMEType: "application/json"}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		id := strings.TrimPrefix(req.Params.URI, "nebu://traces/")
		r, err := n.gateway.GetTrace(ctx, connect.NewRequest(&v1.GetTraceRequest{Id: id}))
		if err != nil {
			return nil, mcp.ResourceNotFoundError(req.Params.URI)
		}
		data, _ := protojson.MarshalOptions{UseProtoNames: true}.Marshal(r.Msg)
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: req.Params.URI, MIMEType: "application/json", Text: string(data)}}}, nil
	})
}
