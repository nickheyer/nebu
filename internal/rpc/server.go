// Package rpc mounts Connect handlers on an HTTP handler.
package rpc

import (
	"context"
	"log/slog"
	"net/http"
	"time"

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
	"github.com/nickheyer/nebu/internal/pull"
	"github.com/nickheyer/nebu/internal/rpc/services"
	"github.com/nickheyer/nebu/internal/settings"
	"github.com/nickheyer/nebu/internal/slots"
	"github.com/nickheyer/nebu/internal/sso"
	"github.com/nickheyer/nebu/internal/tasks"
	"github.com/nickheyer/nebu/pkg/events"
	"github.com/nickheyer/nebu/pkg/formats"
	"github.com/nickheyer/nebu/pkg/host"
	"github.com/nickheyer/nebu/pkg/launch"
	"github.com/nickheyer/nebu/pkg/perf"
	"github.com/nickheyer/nebu/pkg/proto/nebu/v1/nebuv1connect"
	"github.com/nickheyer/nebu/pkg/runtimes"
	"github.com/nickheyer/nebu/pkg/sources"
	"github.com/nickheyer/nebu/pkg/store"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

// Everything the handlers need
type Deps struct {
	Host     *host.Prober
	Doctor   *doctor.Doctor
	Settings *settings.Manager
	Sources  *sources.Manager
	// Formats in priority order for search labels and UI display.
	Formats   *formats.Registry
	Runtimes  *runtimes.Registry
	Inspector *inspect.Inspector
	Store     *store.Store
	Puller    *pull.Puller
	Tasks     *tasks.Manager
	Installs  *installs.Manager
	Instances *instances.Manager
	Slots     *slots.Manager
	Gateway   *gateway.Gateway
	Bots      *bots.Manager
	// Saved web chat conversations, one store per account
	Chats *chats.Manager
	// Mounts /v1/ when the gateway shares the API listener.
	GatewayShared bool
	Events        *events.Bus
	Snapshot      services.Snapshotter
	Web           http.Handler
	// Checks every call's credential: the daemon token, a user's API token, or a browser session
	Guard *auth.Guard
	// Browser sessions, nil when auth.disabled is set
	Sessions *auth.Sessions
	// Local accounts, nil when single sign-on replaces them or auth is off
	Users *auth.Users
	// API tokens users make, nil when auth is off
	Tokens *auth.Tokens
	// Single sign-on, nil when auth.oidc is unset
	SSO *sso.Service
	// Recent daemon logs streamed to the Host page.
	Recent *launch.Log
	Log    *slog.Logger
	// The mesh, the conductor, and the learned tables
	Mesh       *mesh.Manager
	Formations *formations.Manager
	Perf       *perf.Table
	// Where formation stages and relay seats keep their files
	CacheDir string
}

// Builds the h2c handler serving every service
func NewHandler(d Deps) http.Handler {
	opts := connect.WithInterceptors(logging(d.Log), &guard{d.Guard})
	mux := http.NewServeMux()
	mux.Handle(nebuv1connect.NewHostServiceHandler(services.NewHostService(d.Host, d.Doctor, d.Recent), opts))
	mux.Handle(nebuv1connect.NewSettingsServiceHandler(services.NewSettingsService(d.Settings), opts))
	mux.Handle(nebuv1connect.NewSourceServiceHandler(services.NewSourceService(d.Sources, d.Inspector, d.Formats, d.Runtimes), opts))
	mux.Handle(nebuv1connect.NewRuntimeServiceHandler(services.NewRuntimeService(d.Runtimes, d.Host, d.Installs, d.Formats), opts))
	mux.Handle(nebuv1connect.NewInstanceServiceHandler(services.NewInstanceService(d.Instances), opts))
	mux.Handle(nebuv1connect.NewEstimateServiceHandler(services.NewEstimateService(d.Inspector), opts))
	mux.Handle(nebuv1connect.NewStoreServiceHandler(services.NewStoreService(d.Store, d.Puller, d.Events, d.Formations.PullTo), opts))
	mux.Handle(nebuv1connect.NewTaskServiceHandler(services.NewTaskService(d.Tasks), opts))
	mux.Handle(nebuv1connect.NewBuildServiceHandler(services.NewBuildService(d.Installs), opts))
	mux.Handle(nebuv1connect.NewSlotServiceHandler(services.NewSlotService(d.Slots), opts))
	mux.Handle(nebuv1connect.NewGatewayServiceHandler(services.NewGatewayService(d.Gateway, d.Instances, d.Slots), opts))
	mux.Handle(nebuv1connect.NewEventServiceHandler(services.NewEventService(d.Events, d.Snapshot), opts))
	mux.Handle(nebuv1connect.NewBotServiceHandler(services.NewBotService(d.Bots), opts))
	mux.Handle(nebuv1connect.NewAuthServiceHandler(services.NewAuthService(d.Users, d.Tokens, d.Guard), opts))
	mux.Handle(nebuv1connect.NewChatServiceHandler(services.NewChatService(d.Chats, d.Guard), opts))
	mux.Handle(nebuv1connect.NewMeshServiceHandler(services.NewMeshService(d.Mesh, d.Formations, d.Puller, d.Tasks, d.Store, d.Perf, d.Instances, d.Guard), opts))
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
	)
	mux.Handle(grpcreflect.NewHandlerV1(reflector))
	mux.Handle(grpcreflect.NewHandlerV1Alpha(reflector))
	mux.Handle(filesPath, &files{inspector: d.Inspector, auth: d.Guard, log: d.Log})
	mountMesh(mux, d.Guard, d.Store, d.CacheDir)
	entry := auth.Options{Sessions: d.Sessions, Users: d.Users, Log: d.Log}
	if d.SSO != nil {
		entry.SSO, entry.SSOHandler = d.SSO.Name(), sso.Handler(d.SSO)
	}
	mux.Handle(auth.BasePath, auth.Handler(entry))
	if d.Gateway != nil && d.GatewayShared {
		d.Gateway.Mount(mux)
	} else {
		// Prevent gateway paths from reaching the SPA fallback.
		for _, path := range []string{"/v1/", "/api/", "/health"} {
			mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, "the gateway listens on its own address, see nebu gateway", http.StatusNotFound)
			})
		}
	}
	if d.Web != nil {
		mux.Handle("/", d.Web)
	}
	return h2c.NewHandler(mux, &http2.Server{})
}

// Logs every call with duration and outcome
func logging(log *slog.Logger) connect.UnaryInterceptorFunc {
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			start := time.Now()
			resp, err := next(ctx, req)
			attrs := []any{"procedure", req.Spec().Procedure, "ms", time.Since(start).Milliseconds()}
			if err != nil {
				log.Warn("rpc", append(attrs, "err", err)...)
			} else {
				log.Debug("rpc", attrs...)
			}
			return resp, err
		}
	}
}

// Refuses calls whose headers the guard does not accept. The handshake and the admission calls
// admit themselves, since a node outside holds no session; a member's session token opens the
// node to node procedures alone; every other procedure takes a daemon credential.
type guard struct {
	*auth.Guard
}

// Procedures a node outside any mesh may call without a credential: the handshake proves the
// secret, and admission is decided by the manager and the person at the page
var openProcedures = map[string]bool{
	nebuv1connect.MeshServiceHelloProcedure:   true,
	nebuv1connect.MeshServiceKnockProcedure:   true,
	nebuv1connect.MeshServiceOfferProcedure:   true,
	nebuv1connect.MeshServiceWelcomeProcedure: true,
}

// Procedures a member's session token opens: the calls members make to each other, and none of
// the ones that decide what this node does
var peerProcedures = map[string]bool{
	nebuv1connect.MeshServiceSyncProcedure:       true,
	nebuv1connect.MeshServiceStreamProcedure:     true,
	nebuv1connect.MeshServiceRunSeatProcedure:    true,
	nebuv1connect.MeshServiceStopSeatProcedure:   true,
	nebuv1connect.MeshServiceMoveSlotProcedure:   true,
	nebuv1connect.MeshServiceDropSlotProcedure:   true,
	nebuv1connect.MeshServiceGetSeatProcedure:    true,
	nebuv1connect.MeshServicePullSeatProcedure:   true,
	nebuv1connect.MeshServiceWatchPullProcedure:  true,
	nebuv1connect.MeshServiceSeatLogsProcedure:   true,
	nebuv1connect.MeshServiceGetStoredProcedure:  true,
	nebuv1connect.MeshServiceRekeyProcedure:      true,
	nebuv1connect.MeshServiceByeProcedure:        true,
	nebuv1connect.MeshServiceForgetNodeProcedure: true,
}

// Whether a call may proceed
func (a *guard) allows(procedure string, h http.Header) bool {
	if openProcedures[procedure] {
		return true
	}
	if a.Authenticated(h) {
		return true
	}
	if peerProcedures[procedure] {
		_, ok := a.PeerOf(h)
		return ok
	}
	return false
}

func (a *guard) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		if !a.allows(req.Spec().Procedure, req.Header()) {
			return nil, connect.NewError(connect.CodeUnauthenticated, a.Err())
		}
		return next(ctx, req)
	}
}

func (a *guard) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (a *guard) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		if !a.allows(conn.Spec().Procedure, conn.RequestHeader()) {
			return connect.NewError(connect.CodeUnauthenticated, a.Err())
		}
		return next(ctx, conn)
	}
}
