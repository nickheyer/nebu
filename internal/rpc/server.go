// Package rpc mounts Connect handlers on an HTTP handler
package rpc

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	"connectrpc.com/connect"
	"connectrpc.com/grpcreflect"
	"github.com/nickheyer/nebu/internal/auth"
	"github.com/nickheyer/nebu/internal/bots"
	"github.com/nickheyer/nebu/internal/chats"
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
	"github.com/nickheyer/nebu/internal/settings"
	"github.com/nickheyer/nebu/internal/slots"
	"github.com/nickheyer/nebu/internal/sso"
	"github.com/nickheyer/nebu/internal/tasks"
	"github.com/nickheyer/nebu/internal/tools"
	"github.com/nickheyer/nebu/pkg/events"
	"github.com/nickheyer/nebu/pkg/formats"
	"github.com/nickheyer/nebu/pkg/host"
	"github.com/nickheyer/nebu/pkg/launch"
	"github.com/nickheyer/nebu/pkg/perf"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/proto/nebu/v1/nebuv1connect"
	"github.com/nickheyer/nebu/pkg/runtimes"
	"github.com/nickheyer/nebu/pkg/scheduling/remote"
	"github.com/nickheyer/nebu/pkg/sources"
	"github.com/nickheyer/nebu/pkg/store"
)

// Everything the handlers need
type Deps struct {
	Host     *host.Prober
	Doctor   *doctor.Doctor
	Settings *settings.Manager
	Sources  *sources.Manager
	// Formats in priority order for search labels and UI display
	Formats   *formats.Registry
	Runtimes  *runtimes.Registry
	Inspector *inspect.Inspector
	Store     *store.Store
	Puller    *pull.Puller
	Tasks     *tasks.Manager
	Installs  *installs.Manager
	Instances *instances.Manager
	Slots     *slots.Manager
	// Pools, routes, objectives and scheduler configurations the gateway routes over
	Pools   *pools.Manager
	Gateway *gateway.Gateway
	Bots    *bots.Manager
	// Saved web chat conversations, one store per account
	Chats *chats.Manager
	// Mounts /v1/ when the gateway shares the API listener
	GatewayShared bool
	// Builds the mesh listener's handler, holding the services and paths members call each other through and nothing else
	MeshSurface bool
	Events      *events.Bus
	Snapshot    services.Snapshotter
	Web         http.Handler
	// Checks every call's credential, the daemon token, a user's API token, or a browser session
	Guard *auth.Guard
	// Browser sessions, nil when auth.disabled is set
	Sessions *auth.Sessions
	// Local accounts, nil when single sign-on replaces them or auth is off
	Users *auth.Users
	// API tokens users make, nil when auth is off
	Tokens *auth.Tokens
	// Single sign-on, nil when auth.oidc is unset
	SSO *sso.Service
	// Recent daemon logs streamed to the Host page
	Recent *launch.Log
	Log    *slog.Logger
	// The mesh, the conductor, and the learned tables
	Mesh       *mesh.Manager
	Formations *formations.Manager
	Perf       *perf.Table
	// Where formation stages and relay seats keep their files
	CacheDir string
	// The tool servers Settings hold
	Tools *tools.Registry
	// Nebu's own MCP server, mounted for MCP clients
	MCP http.Handler
}

// Builds the h2c handler serving every service, or on the mesh surface only what members call each other through
func NewHandler(d Deps) (http.Handler, error) {
	valid, err := newValidating()
	if err != nil {
		return nil, err
	}
	opts := connect.WithInterceptors(logging{log: d.Log}, d.Guard.Interceptor(), valid)
	mux := http.NewServeMux()
	mux.Handle(nebuv1connect.NewMeshServiceHandler(services.NewMeshService(d.Mesh, d.Formations, d.Puller, d.Tasks, d.Store, d.Perf, d.Instances, d.Guard), opts))
	mux.Handle(nebuv1connect.NewToolServiceHandler(services.NewToolService(d.Tools, d.Mesh), opts))
	mountMesh(mux, d.Guard, d.Store, d.CacheDir)
	if d.MeshSurface {
		if d.Gateway != nil {
			d.Gateway.Mount(mux)
			mux.Handle(remote.SchedulePath, d.Gateway.ScheduleHandler())
		}
		return mux, nil
	}
	mux.Handle(nebuv1connect.NewHostServiceHandler(services.NewHostService(d.Host, d.Doctor, d.Recent), opts))
	mux.Handle(nebuv1connect.NewSettingsServiceHandler(services.NewSettingsService(d.Settings), opts))
	mux.Handle(nebuv1connect.NewSourceServiceHandler(services.NewSourceService(d.Sources, d.Inspector, d.Formats, d.Runtimes), opts))
	mux.Handle(nebuv1connect.NewRuntimeServiceHandler(services.NewRuntimeService(d.Runtimes, d.Host, d.Installs, d.Formats), opts))
	mux.Handle(nebuv1connect.NewInstanceServiceHandler(services.NewInstanceService(d.Instances), opts))
	mux.Handle(nebuv1connect.NewEstimateServiceHandler(services.NewEstimateService(d.Inspector), opts))
	mux.Handle(nebuv1connect.NewStoreServiceHandler(services.NewStoreService(d.Store, d.Puller, d.Events, d.Formations.PullTo, d.Runtimes), opts))
	mux.Handle(nebuv1connect.NewTaskServiceHandler(services.NewTaskService(d.Tasks), opts))
	mux.Handle(nebuv1connect.NewBuildServiceHandler(services.NewBuildService(d.Installs), opts))
	mux.Handle(nebuv1connect.NewSlotServiceHandler(services.NewSlotService(d.Slots), opts))
	mux.Handle(nebuv1connect.NewGatewayServiceHandler(services.NewGatewayService(d.Gateway, d.Pools, d.Guard), opts))
	mux.Handle(nebuv1connect.NewPoolImportServiceHandler(services.NewPoolImportService(d.Mesh, d.Events), opts))
	mux.Handle(nebuv1connect.NewEventServiceHandler(services.NewEventService(d.Events, d.Snapshot), opts))
	mux.Handle(nebuv1connect.NewBotServiceHandler(services.NewBotService(d.Bots), opts))
	mux.Handle(nebuv1connect.NewAuthServiceHandler(services.NewAuthService(d.Users, d.Tokens, d.Guard), opts))
	mux.Handle(nebuv1connect.NewChatServiceHandler(services.NewChatService(d.Chats, d.Guard), opts))
	reflector := grpcreflect.NewStaticReflector(
		nebuv1connect.HostServiceName,
		nebuv1connect.SettingsServiceName,
		nebuv1connect.SourceServiceName,
		nebuv1connect.RuntimeServiceName,
		nebuv1connect.EstimateServiceName,
		nebuv1connect.StoreServiceName,
		nebuv1connect.TaskServiceName,
		nebuv1connect.InstanceServiceName,
		nebuv1connect.BuildServiceName,
		nebuv1connect.SlotServiceName,
		nebuv1connect.GatewayServiceName,
		nebuv1connect.EventServiceName,
		nebuv1connect.BotServiceName,
		nebuv1connect.AuthServiceName,
		nebuv1connect.ChatServiceName,
		nebuv1connect.MeshServiceName,
		nebuv1connect.ToolServiceName,
		nebuv1connect.PoolImportServiceName,
	)
	reflectV1, reflectV1Handler := grpcreflect.NewHandlerV1(reflector)
	mux.Handle(reflectV1, &credentialed{guard: d.Guard, scope: v1.Scope_SCOPE_READ, next: reflectV1Handler})
	reflectAlpha, reflectAlphaHandler := grpcreflect.NewHandlerV1Alpha(reflector)
	mux.Handle(reflectAlpha, &credentialed{guard: d.Guard, scope: v1.Scope_SCOPE_READ, next: reflectAlphaHandler})
	mux.Handle(filesPath, &files{inspector: d.Inspector, auth: d.Guard, log: d.Log})
	entry := auth.Options{Sessions: d.Sessions, Users: d.Users, Log: d.Log}
	if d.SSO != nil {
		entry.SSO, entry.SSOHandler = d.SSO.Name(), sso.Handler(d.SSO)
	}
	mux.Handle(auth.BasePath, auth.Handler(entry))
	if d.MCP != nil {
		mux.Handle(mcpPath, &credentialed{guard: d.Guard, scope: v1.Scope_SCOPE_OPERATE, next: d.MCP})
	}
	switch {
	case d.Gateway != nil && d.GatewayShared:
		d.Gateway.Mount(mux)
	case d.Gateway != nil:
		d.Gateway.MountHealth(mux)
		fallthrough
	default:
		// Prevent gateway paths from reaching the SPA fallback
		for _, path := range []string{"/v1/", "/api/", "/health"} {
			mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, "the gateway listens on its own address, see nebu gateway", http.StatusNotFound)
			})
		}
	}
	if d.Web != nil {
		mux.Handle("/", d.Web)
	}
	return mux, nil
}

// Where MCP clients reach nebu's own tool server
const mcpPath = "/mcp"

// Serves requests carrying a credential that opens the scope and refuses the rest in JSON
type credentialed struct {
	guard *auth.Guard
	scope v1.Scope
	next  http.Handler
}

func (c *credentialed) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !c.guard.Scoped(r.Header, c.scope) {
		code, err := http.StatusUnauthorized, c.guard.Err()
		if _, ok := c.guard.Identify(r.Header); ok {
			code, err = http.StatusForbidden, fmt.Errorf("%s needs the %s scope, which this credential does not hold", r.URL.Path, auth.ScopeName(c.scope))
		}
		w.Header().Set("WWW-Authenticate", `Bearer realm="nebu"`)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(map[string]any{"error": err.Error()})
		return
	}
	c.next.ServeHTTP(w, r)
}
