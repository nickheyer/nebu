// Package mcpserver offers models, runtimes, builds, pools, routes, objectives, scheduler configs, traces, mesh and tools to MCP clients
package mcpserver

import (
	"bytes"
	"context"
	_ "embed"
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
	"github.com/nickheyer/nebu/internal/pools"
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
	Pools      *pools.Manager
	Gateway    *gateway.Gateway
	Mesh       *mesh.Manager
	Formations *formations.Manager
	Perf       *perf.Table
	Guard      *auth.Guard
	// The provider credentials callers keep, signing what a tool reaches of provider sources for whoever calls it
	Credentials *auth.Credentials
	Tools       *tools.Registry
	Events      *events.Bus
	Version     string
	Log         *slog.Logger
}

// The services the tools wrap, called in process
type nebu struct {
	d        Deps
	host     *services.HostService
	sources  *services.SourceService
	runtimes *services.RuntimeService
	builds   *services.BuildService
	store    *services.StoreService
	instance *services.InstanceService
	estimate *services.EstimateService
	slots    *services.SlotService
	gateway  *services.GatewayService
	imports  *services.PoolImportService
	tasks    *services.TaskService
	mesh     *services.MeshService
	tools    *services.ToolService
	client   *http.Client
}

// What every MCP client is told at initialize about using nebu's tools
//
//go:embed instructions.md
var instructions string

// The streamable HTTP handler of nebu's MCP server, one server for every session
func Handler(d Deps) http.Handler { return streamable(New(d), d.Guard.Enabled(), d.Log) }

// Serves one server over streamable HTTP, refusing loopback requests naming another host unless every request has already shown a credential
func streamable(server *mcp.Server, credentialed bool, log *slog.Logger) http.Handler {
	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Logger: log, DisableLocalhostProtection: credentialed})
}

// Builds the server with every tool and resource
func New(d Deps) *mcp.Server {
	n := &nebu{
		d:        d,
		host:     services.NewHostService(d.Host, d.Doctor, nil, d.Store),
		sources:  services.NewSourceService(d.Sources, d.Inspector, d.Formats, d.Runtimes),
		runtimes: services.NewRuntimeService(d.Runtimes, d.Host, d.Installs, d.Formats),
		builds:   services.NewBuildService(d.Installs),
		store:    services.NewStoreService(d.Store, d.Puller, d.Events, d.Formations.PullTo, d.Runtimes),
		instance: services.NewInstanceService(d.Instances),
		estimate: services.NewEstimateService(d.Inspector),
		slots:    services.NewSlotService(d.Slots),
		gateway:  services.NewGatewayService(d.Gateway, d.Pools, d.Guard),
		imports:  services.NewPoolImportService(d.Mesh, d.Events),
		tasks:    services.NewTaskService(d.Tasks),
		mesh:     services.NewMeshService(d.Mesh, d.Formations, d.Puller, d.Tasks, d.Store, d.Perf, d.Instances, d.Guard),
		tools:    services.NewToolService(d.Tools, d.Mesh),
		client:   d.Gateway.Client("mcp"),
	}
	s := mcp.NewServer(&mcp.Implementation{Name: "nebu", Title: "Nebu", Version: d.Version, Description: "Model weights, inference runtimes, routes, and the mesh they run on"}, &mcp.ServerOptions{
		Instructions: instructions,
		Logger:       d.Log,
	})
	// The services are called in process, past the interceptor that signs calls over the API, so each is signed here
	s.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if extra := req.GetExtra(); extra != nil {
				ctx = d.Credentials.SignCaller(ctx, d.Guard, extra.Header)
			}
			return next(ctx, method, req)
		}
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
	SourceID string `json:"source_id,omitempty" jsonschema:"source to search"`
	Limit    int    `json:"limit,omitempty" jsonschema:"max results (default 20)"`
}

type modelRef struct {
	SourceID string `json:"source_id" jsonschema:"model source, such as huggingface"`
	Repo     string `json:"repo" jsonschema:"repository, such as Qwen/Qwen3-8B"`
	Group    string `json:"group,omitempty" jsonschema:"weight group in the repo"`
}

type repoIn struct {
	SourceID string `json:"source_id" jsonschema:"model source, such as huggingface"`
	Repo     string `json:"repo" jsonschema:"repository, such as Qwen/Qwen3-8B"`
	Revision string `json:"revision,omitempty" jsonschema:"branch, tag, or commit"`
}

type inspectModelIn struct {
	repoIn
	Groups     []string          `json:"groups,omitempty" jsonschema:"weight groups to plan"`
	RuntimeIDs []string          `json:"runtime_ids,omitempty" jsonschema:"runtimes to plan on"`
	Contexts   []uint32          `json:"contexts,omitempty" jsonschema:"context lengths to plan at"`
	Params     map[string]string `json:"params,omitempty" jsonschema:"runtime parameters to plan with"`
	SlotID     string            `json:"slot_id,omitempty" jsonschema:"slot to plan for"`
}

type pullModelIn struct {
	modelRef
	Revision string   `json:"revision,omitempty"`
	NodeID   string   `json:"node_id,omitempty" jsonschema:"mesh member to pull onto"`
	Paths    []string `json:"paths,omitempty" jsonschema:"files of the repository to download on their own, such as a vision projector, each joining the group it belongs to"`
	Alone    bool     `json:"alone,omitempty" jsonschema:"download the group without the other parts its pipeline needs"`
	Slot     string   `json:"slot,omitempty" jsonschema:"the part of a pipeline the files fill, such as vae"`
}

type removeModelIn struct {
	modelRef
	Gc bool `json:"gc,omitempty" jsonschema:"also delete blobs nothing else uses"`
}

type runtimeIn struct {
	RuntimeID string `json:"runtime_id,omitempty"`
}

type installRuntimeIn struct {
	RuntimeID string            `json:"runtime_id" jsonschema:"runtime to install, such as llamacpp"`
	Method    string            `json:"method" jsonschema:"install method, such as adopt or release"`
	Settings  map[string]string `json:"settings,omitempty" jsonschema:"values for the method's fields, by field name"`
}

type listInstancesIn struct {
	RunningOnly bool `json:"running_only,omitempty"`
}

type runModelIn struct {
	modelRef
	RuntimeID         string            `json:"runtime_id,omitempty" jsonschema:"runtime to serve with"`
	InstallID         string            `json:"install_id,omitempty" jsonschema:"install of the runtime to launch, its newest when empty"`
	Name              string            `json:"name,omitempty" jsonschema:"model name clients send"`
	SlotID            string            `json:"slot_id,omitempty" jsonschema:"slot to run in"`
	Params            map[string]string `json:"params,omitempty" jsonschema:"runtime parameters by name, such as n_ctx, and environment variables of the server process as env.NAME"`
	Args              string            `json:"args,omitempty" jsonschema:"extra arguments for the runtime's server command, split like a shell, in place of the slot's"`
	Exact             bool              `json:"exact,omitempty" jsonschema:"launch with these settings alone and none of the slot's"`
	Span              []string          `json:"span,omitempty" jsonschema:"mesh nodes or node/device ids the run may use"`
	Shape             string            `json:"shape,omitempty" jsonschema:"solo, chain, lockstep, relay, replicas, draft, or stages"`
	IdleUnloadSeconds uint32            `json:"idle_unload_seconds,omitempty" jsonschema:"seconds without requests after which the model unloads, never when empty"`
	Snapshot          bool              `json:"snapshot,omitempty" jsonschema:"snapshot the process once ready, so later launches of the same run restore it instead of starting cold"`
}

// The run request a tool call describes
func (in runModelIn) request() (*v1.RunRequest, error) {
	shape, err := shapeOf(in.Shape)
	if err != nil {
		return nil, err
	}
	return &v1.RunRequest{SourceId: in.SourceID, Repo: in.Repo, Group: in.Group, RuntimeId: in.RuntimeID, InstallId: in.InstallID, Name: in.Name, SlotId: in.SlotID, Params: in.Params, Args: in.Args, Exact: in.Exact, Span: in.Span, Shape: shape, IdleUnloadSeconds: in.IdleUnloadSeconds, Snapshot: in.Snapshot}, nil
}

type idIn struct {
	ID string `json:"id"`
}

type instanceLogsIn struct {
	ID   string `json:"id" jsonschema:"instance id or name"`
	Tail uint32 `json:"tail,omitempty" jsonschema:"lines from the end of the log (default 200)"`
}

type swapSlotIn struct {
	SlotID string `json:"slot_id" jsonschema:"slot id or name"`
	modelRef
	RuntimeID  string            `json:"runtime_id,omitempty"`
	InstallID  string            `json:"install_id,omitempty" jsonschema:"install of the runtime to launch, the slot's when empty"`
	Params     map[string]string `json:"params,omitempty" jsonschema:"runtime parameters by name, and environment variables of the server process as env.NAME"`
	Args       string            `json:"args,omitempty" jsonschema:"extra arguments for the runtime's server command, in place of the slot's"`
	DrainFirst bool              `json:"drain_first,omitempty" jsonschema:"finish in-flight requests before stopping the current model"`
}

type nameIn struct {
	Name string `json:"name"`
}

type listTracesIn struct {
	Route string `json:"route,omitempty"`
	Limit int    `json:"limit,omitempty" jsonschema:"max traces (default 50)"`
}

type hostProfileIn struct {
	Refresh bool `json:"refresh,omitempty" jsonschema:"probe the host again instead of using the last probe"`
}

type emptyIn struct{}

type listTasksIn struct {
	ActiveOnly bool `json:"active_only,omitempty"`
}

type getTaskIn struct {
	ID   string `json:"id"`
	Tail uint32 `json:"tail,omitempty" jsonschema:"lines from the end of the log, the whole log when empty"`
}

type formationIn struct {
	modelRef
	RuntimeID string            `json:"runtime_id,omitempty"`
	Params    map[string]string `json:"params,omitempty"`
	Span      []string          `json:"span,omitempty" jsonschema:"mesh nodes or node/device ids the formation may use"`
	Shape     string            `json:"shape,omitempty" jsonschema:"chain, lockstep, relay, replicas, draft, or stages"`
}

type listFormationsIn struct {
	RunningOnly bool `json:"running_only,omitempty"`
}

type chatIn struct {
	Model     string `json:"model" jsonschema:"route name"`
	Prompt    string `json:"prompt"`
	System    string `json:"system,omitempty"`
	MaxTokens int    `json:"max_tokens,omitempty"`
}

type imageIn struct {
	Model  string `json:"model" jsonschema:"image route name"`
	Prompt string `json:"prompt"`
	Size   string `json:"size,omitempty" jsonschema:"WIDTHxHEIGHT"`
	N      int    `json:"n,omitempty" jsonschema:"number of images (default 1)"`
}

type listToolServersIn struct {
	Mesh bool `json:"mesh,omitempty" jsonschema:"include every mesh member's servers"`
}

type callToolIn struct {
	Name      string          `json:"name" jsonschema:"tool name, such as web_search"`
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
	mcp.AddTool(s, &mcp.Tool{Name: "get_model_card", Description: "Gets a repository's model card, tags, license, and README", Annotations: ro}, func(ctx context.Context, _ *mcp.CallToolRequest, in repoIn) (*mcp.CallToolResult, any, error) {
		r, err := n.sources.GetModelCard(ctx, connect.NewRequest(&v1.GetModelCardRequest{SourceId: in.SourceID, Repo: in.Repo, Revision: in.Revision}))
		return answer(r, err)
	})
	mcp.AddTool(s, &mcp.Tool{Name: "resolve_model", Description: "Lists a repository's weight groups with their formats and sizes", Annotations: ro}, func(ctx context.Context, _ *mcp.CallToolRequest, in repoIn) (*mcp.CallToolResult, any, error) {
		r, err := n.sources.Resolve(ctx, connect.NewRequest(&v1.ResolveRequest{SourceId: in.SourceID, Repo: in.Repo, Revision: in.Revision}))
		return answer(r, err)
	})
	mcp.AddTool(s, &mcp.Tool{Name: "inspect_model", Description: "Checks which of a repository's weight groups fit this host's memory, per runtime and context length", Annotations: ro}, func(ctx context.Context, _ *mcp.CallToolRequest, in inspectModelIn) (*mcp.CallToolResult, any, error) {
		r, err := n.estimate.Inspect(ctx, connect.NewRequest(&v1.InspectRequest{SourceId: in.SourceID, Repo: in.Repo, Revision: in.Revision, Groups: in.Groups, RuntimeIds: in.RuntimeIDs, Contexts: in.Contexts, Params: in.Params, SlotId: in.SlotID}))
		return answer(r, err)
	})
	mcp.AddTool(s, &mcp.Tool{Name: "pull_model", Description: "Downloads a model and returns its task"}, func(ctx context.Context, _ *mcp.CallToolRequest, in pullModelIn) (*mcp.CallToolResult, any, error) {
		r, err := n.store.Pull(ctx, connect.NewRequest(&v1.PullRequest{SourceId: in.SourceID, Repo: in.Repo, Group: in.Group, Revision: in.Revision, NodeId: in.NodeID, Paths: in.Paths, Alone: in.Alone, Slot: in.Slot}))
		return answer(r, err)
	})
	mcp.AddTool(s, &mcp.Tool{Name: "remove_model", Description: "Removes a stored model", Annotations: &mcp.ToolAnnotations{DestructiveHint: ptr(true)}}, func(ctx context.Context, _ *mcp.CallToolRequest, in removeModelIn) (*mcp.CallToolResult, any, error) {
		r, err := n.store.RemoveModel(ctx, connect.NewRequest(&v1.RemoveModelRequest{SourceId: in.SourceID, Repo: in.Repo, Group: in.Group, Gc: in.Gc}))
		return answer(r, err)
	})
	mcp.AddTool(s, &mcp.Tool{Name: "list_runtimes", Description: "Lists every runtime with its install methods and unmet requirements", Annotations: ro}, func(ctx context.Context, _ *mcp.CallToolRequest, _ emptyIn) (*mcp.CallToolResult, any, error) {
		r, err := n.runtimes.ListRuntimes(ctx, connect.NewRequest(&v1.ListRuntimesRequest{}))
		return answer(r, err)
	})
	mcp.AddTool(s, &mcp.Tool{Name: "get_runtime", Description: "Gets one runtime's parameters, unmet requirements, and install methods", Annotations: ro}, func(ctx context.Context, _ *mcp.CallToolRequest, in idIn) (*mcp.CallToolResult, any, error) {
		r, err := n.runtimes.GetRuntime(ctx, connect.NewRequest(&v1.GetRuntimeRequest{Id: in.ID}))
		return answer(r, err)
	})
	mcp.AddTool(s, &mcp.Tool{Name: "list_installs", Description: "Lists installed runtimes with their versions and facts", Annotations: ro}, func(ctx context.Context, _ *mcp.CallToolRequest, in runtimeIn) (*mcp.CallToolResult, any, error) {
		r, err := n.runtimes.ListInstalls(ctx, connect.NewRequest(&v1.ListInstallsRequest{RuntimeId: in.RuntimeID}))
		return answer(r, err)
	})
	mcp.AddTool(s, &mcp.Tool{Name: "install_runtime", Description: "Installs a runtime by one of its methods, a binary already on the host or a published build, and returns its task. To build one from source, use build_runtime"}, func(ctx context.Context, _ *mcp.CallToolRequest, in installRuntimeIn) (*mcp.CallToolResult, any, error) {
		r, err := n.runtimes.Install(ctx, connect.NewRequest(&v1.InstallRequest{RuntimeId: in.RuntimeID, Method: in.Method, Settings: in.Settings}))
		return answer(r, err)
	})
	n.addBuildTools(s, ro)
	mcp.AddTool(s, &mcp.Tool{Name: "list_instances", Description: "Lists running and past model instances with their states and endpoints", Annotations: ro}, func(ctx context.Context, _ *mcp.CallToolRequest, in listInstancesIn) (*mcp.CallToolResult, any, error) {
		r, err := n.instance.ListInstances(ctx, connect.NewRequest(&v1.ListInstancesRequest{RunningOnly: in.RunningOnly}))
		return answer(r, err)
	})
	mcp.AddTool(s, &mcp.Tool{Name: "run_model", Description: "Starts a stored model and returns the instance and its startup task"}, func(ctx context.Context, _ *mcp.CallToolRequest, in runModelIn) (*mcp.CallToolResult, any, error) {
		run, err := in.request()
		if err != nil {
			return nil, nil, err
		}
		r, err := n.instance.Run(ctx, connect.NewRequest(run))
		return answer(r, err)
	})
	mcp.AddTool(s, &mcp.Tool{Name: "preview_run", Description: "Renders the command a run would launch with and the parameters it resolves, starting nothing", Annotations: ro}, func(ctx context.Context, _ *mcp.CallToolRequest, in runModelIn) (*mcp.CallToolResult, any, error) {
		run, err := in.request()
		if err != nil {
			return nil, nil, err
		}
		r, err := n.instance.PreviewRun(ctx, connect.NewRequest(&v1.PreviewRunRequest{Run: run}))
		return answer(r, err)
	})
	mcp.AddTool(s, &mcp.Tool{Name: "stop_model", Description: "Stops a running instance by id or name"}, func(ctx context.Context, _ *mcp.CallToolRequest, in idIn) (*mcp.CallToolResult, any, error) {
		r, err := n.instance.StopInstance(ctx, connect.NewRequest(&v1.StopInstanceRequest{Id: in.ID}))
		return answer(r, err)
	})
	mcp.AddTool(s, &mcp.Tool{Name: "instance_logs", Description: "Gets the last lines of an instance's log", Annotations: ro}, func(ctx context.Context, _ *mcp.CallToolRequest, in instanceLogsIn) (*mcp.CallToolResult, any, error) {
		tail := in.Tail
		if tail == 0 {
			tail = 200
		}
		out := &v1.LogsResponse{}
		err := n.d.Instances.Logs(ctx, in.ID, false, int(tail), func(lines []string) error {
			out.Lines = lines
			return nil
		})
		if err != nil {
			return nil, nil, err
		}
		return text(out), nil, nil
	})
	mcp.AddTool(s, &mcp.Tool{Name: "list_slots", Description: "Lists slots, the device and memory reservations models run in", Annotations: ro}, func(ctx context.Context, _ *mcp.CallToolRequest, _ emptyIn) (*mcp.CallToolResult, any, error) {
		r, err := n.slots.ListSlots(ctx, connect.NewRequest(&v1.ListSlotsRequest{}))
		return answer(r, err)
	})
	mcp.AddTool(s, &mcp.Tool{Name: "swap_slot", Description: "Replaces the model in a slot and returns the slot, instance, and task"}, func(ctx context.Context, _ *mcp.CallToolRequest, in swapSlotIn) (*mcp.CallToolResult, any, error) {
		r, err := n.slots.Swap(ctx, connect.NewRequest(&v1.SwapRequest{SlotId: in.SlotID, DrainFirst: in.DrainFirst, Run: &v1.RunRequest{SourceId: in.SourceID, Repo: in.Repo, Group: in.Group, RuntimeId: in.RuntimeID, InstallId: in.InstallID, Params: in.Params, Args: in.Args}}))
		return answer(r, err)
	})
	mcp.AddTool(s, &mcp.Tool{Name: "evict_slot", Description: "Stops the model in a slot and keeps the slot"}, func(ctx context.Context, _ *mcp.CallToolRequest, in idIn) (*mcp.CallToolResult, any, error) {
		r, err := n.slots.EvictSlot(ctx, connect.NewRequest(&v1.EvictSlotRequest{Id: in.ID}))
		return answer(r, err)
	})
	n.addSlotTools(s)
	mcp.AddTool(s, &mcp.Tool{Name: "list_routes", Description: "Lists the model names the gateway serves, what each maps to, and how each takes tools", Annotations: ro}, func(ctx context.Context, _ *mcp.CallToolRequest, _ emptyIn) (*mcp.CallToolResult, any, error) {
		r, err := n.gateway.ListRoutes(ctx, connect.NewRequest(&v1.ListRoutesRequest{}))
		return answer(r, err)
	})
	mcp.AddTool(s, &mcp.Tool{Name: "get_route", Description: "Reads one route with its rules, weighted pool targets, policy, objective and conditions", Annotations: ro}, func(ctx context.Context, _ *mcp.CallToolRequest, in nameIn) (*mcp.CallToolResult, any, error) {
		r, err := n.gateway.GetRoute(ctx, connect.NewRequest(&v1.GetRouteRequest{Name: in.Name}))
		return answer(r, err)
	})
	mcp.AddTool(s, &mcp.Tool{Name: "put_route", Description: "Creates or replaces a route that splits a model name across weighted pools or tries them in order. On a route owned by a slot or instance, sets its policy, tools, objective, and balance"}, func(ctx context.Context, _ *mcp.CallToolRequest, in putRouteIn) (*mcp.CallToolResult, any, error) {
		spec, err := n.routeSpec(ctx, in)
		if err != nil {
			return nil, nil, err
		}
		req, err := checked(&v1.PutRouteRequest{Name: in.Name, Spec: spec})
		if err != nil {
			return nil, nil, err
		}
		r, err := n.gateway.PutRoute(ctx, req)
		return answer(r, err)
	})
	mcp.AddTool(s, &mcp.Tool{Name: "delete_route", Description: "Removes a model name the gateway serves", Annotations: &mcp.ToolAnnotations{DestructiveHint: ptr(true)}}, func(ctx context.Context, _ *mcp.CallToolRequest, in nameIn) (*mcp.CallToolResult, any, error) {
		r, err := n.gateway.DeleteRoute(ctx, connect.NewRequest(&v1.DeleteRouteRequest{Name: in.Name}))
		return answer(r, err)
	})
	n.addPoolTools(s, ro)
	mcp.AddTool(s, &mcp.Tool{Name: "list_traces", Description: "Lists recent gateway requests newest first, with timings, tokens, tool calls, the endpoint and pool that served each, the scheduler profile and score, queue wait, priority, objective outcome, fallbacks and request id", Annotations: ro}, func(ctx context.Context, req *mcp.CallToolRequest, in listTracesIn) (*mcp.CallToolResult, any, error) {
		limit := uint32(in.Limit)
		if limit == 0 {
			limit = 50
		}
		r, err := n.gateway.ListTraces(ctx, carry(req.Extra, &v1.ListTracesRequest{Route: in.Route, Limit: limit}))
		return answer(r, err)
	})
	mcp.AddTool(s, &mcp.Tool{Name: "get_trace", Description: "Reads one gateway request with its bodies, the tools the gateway ran, and the scheduler decision that placed it", Annotations: ro}, func(ctx context.Context, req *mcp.CallToolRequest, in idIn) (*mcp.CallToolResult, any, error) {
		r, err := n.gateway.GetTrace(ctx, carry(req.Extra, &v1.GetTraceRequest{Id: in.ID}))
		return answer(r, err)
	})
	mcp.AddTool(s, &mcp.Tool{Name: "host_profile", Description: "Reads the host's devices, memory pools, and storage", Annotations: ro}, func(ctx context.Context, _ *mcp.CallToolRequest, in hostProfileIn) (*mcp.CallToolResult, any, error) {
		r, err := n.host.GetProfile(ctx, connect.NewRequest(&v1.GetProfileRequest{Refresh: in.Refresh}))
		return answer(r, err)
	})
	mcp.AddTool(s, &mcp.Tool{Name: "doctor", Description: "Probes the host and checks its dependencies. Returns a task whose log lists each check"}, func(ctx context.Context, _ *mcp.CallToolRequest, _ emptyIn) (*mcp.CallToolResult, any, error) {
		r, err := n.host.Doctor(ctx, connect.NewRequest(&v1.DoctorRequest{}))
		return answer(r, err)
	})
	mcp.AddTool(s, &mcp.Tool{Name: "list_tasks", Description: "Lists long running tasks, such as pulls, installs, and launches", Annotations: ro}, func(ctx context.Context, _ *mcp.CallToolRequest, in listTasksIn) (*mcp.CallToolResult, any, error) {
		r, err := n.tasks.ListTasks(ctx, connect.NewRequest(&v1.ListTasksRequest{ActiveOnly: in.ActiveOnly}))
		return answer(r, err)
	})
	mcp.AddTool(s, &mcp.Tool{Name: "get_task", Description: "Reads one task with its progress and log, the end of the log alone when tail is set", Annotations: ro}, func(ctx context.Context, _ *mcp.CallToolRequest, in getTaskIn) (*mcp.CallToolResult, any, error) {
		r, err := n.tasks.GetTask(ctx, connect.NewRequest(&v1.GetTaskRequest{Id: in.ID}))
		if err == nil && in.Tail > 0 && len(r.Msg.GetLogs()) > int(in.Tail) {
			r.Msg.Logs = r.Msg.GetLogs()[len(r.Msg.GetLogs())-int(in.Tail):]
		}
		return answer(r, err)
	})
	mcp.AddTool(s, &mcp.Tool{Name: "cancel_task", Description: "Cancels a running task"}, func(ctx context.Context, _ *mcp.CallToolRequest, in idIn) (*mcp.CallToolResult, any, error) {
		r, err := n.tasks.CancelTask(ctx, connect.NewRequest(&v1.CancelTaskRequest{Id: in.ID}))
		return answer(r, err)
	})
	mcp.AddTool(s, &mcp.Tool{Name: "mesh_status", Description: "Reads this node's mesh, nearby nodes, and pending admissions", Annotations: ro}, func(ctx context.Context, _ *mcp.CallToolRequest, _ emptyIn) (*mcp.CallToolResult, any, error) {
		r, err := n.mesh.GetMesh(ctx, connect.NewRequest(&v1.GetMeshRequest{}))
		return answer(r, err)
	})
	mcp.AddTool(s, &mcp.Tool{Name: "list_nodes", Description: "Lists the mesh members with their devices, links, and stored models", Annotations: ro}, func(ctx context.Context, _ *mcp.CallToolRequest, _ emptyIn) (*mcp.CallToolResult, any, error) {
		r, err := n.mesh.ListNodes(ctx, connect.NewRequest(&v1.ListNodesRequest{}))
		return answer(r, err)
	})
	mcp.AddTool(s, &mcp.Tool{Name: "plan_formation", Description: "Plans how a model would run across mesh members and returns every candidate", Annotations: ro}, func(ctx context.Context, _ *mcp.CallToolRequest, in formationIn) (*mcp.CallToolResult, any, error) {
		run, err := formationRun(in)
		if err != nil {
			return nil, nil, err
		}
		r, err := n.mesh.PlanFormation(ctx, connect.NewRequest(&v1.PlanFormationRequest{Run: run}))
		return answer(r, err)
	})
	mcp.AddTool(s, &mcp.Tool{Name: "run_formation", Description: "Plans and launches a model across mesh members and returns the formation and its task"}, func(ctx context.Context, _ *mcp.CallToolRequest, in formationIn) (*mcp.CallToolResult, any, error) {
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
	mcp.AddTool(s, &mcp.Tool{Name: "chat", Description: "Sends a prompt to a model and returns its reply", Annotations: ro}, n.chat)
	mcp.AddTool(s, &mcp.Tool{Name: "generate_image", Description: "Makes images with a diffusion model the gateway serves"}, n.image)
	mcp.AddTool(s, &mcp.Tool{Name: "list_tool_servers", Description: "Lists the configured tool servers with their states and tools", Annotations: ro}, func(ctx context.Context, _ *mcp.CallToolRequest, in listToolServersIn) (*mcp.CallToolResult, any, error) {
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
		return msgOf(n.gateway.ListRoutes(ctx, connect.NewRequest(&v1.ListRoutesRequest{})))
	})
	read("nebu://models", "models", "The models stored on this node", func(ctx context.Context) (proto.Message, error) {
		return msgOf(n.store.ListModels(ctx, connect.NewRequest(&v1.ListModelsRequest{})))
	})
	read("nebu://instances", "instances", "Running and past model instances", func(ctx context.Context) (proto.Message, error) {
		return msgOf(n.instance.ListInstances(ctx, connect.NewRequest(&v1.ListInstancesRequest{})))
	})
	read("nebu://slots", "slots", "The slots models run in", func(ctx context.Context) (proto.Message, error) {
		return msgOf(n.slots.ListSlots(ctx, connect.NewRequest(&v1.ListSlotsRequest{})))
	})
	read("nebu://host", "host", "The host's devices, memory pools, and storage", func(ctx context.Context) (proto.Message, error) {
		return msgOf(n.host.GetProfile(ctx, connect.NewRequest(&v1.GetProfileRequest{})))
	})
	read("nebu://mesh", "mesh", "The mesh this node belongs to", func(ctx context.Context) (proto.Message, error) {
		return msgOf(n.mesh.GetMesh(ctx, connect.NewRequest(&v1.GetMeshRequest{})))
	})
	read("nebu://tools", "tool servers", "The tool servers Settings hold and the tools they offer", func(ctx context.Context) (proto.Message, error) {
		return msgOf(n.tools.ListToolServers(ctx, connect.NewRequest(&v1.ListToolServersRequest{})))
	})
	n.addPoolResources(s, read)
	n.template(s, "nebu://traces/{id}", "trace", "One gateway request with its bodies, tool calls and scheduler decision", func(ctx context.Context, req *mcp.ReadResourceRequest, id string) (proto.Message, error) {
		r, err := n.gateway.GetTrace(ctx, carry(req.Extra, &v1.GetTraceRequest{Id: id}))
		if err != nil {
			return nil, err
		}
		return r.Msg, nil
	})
}
