// Package rpc mounts Connect handlers on an HTTP handler.
package rpc

import (
	"context"
	"crypto/subtle"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"connectrpc.com/connect"
	"connectrpc.com/grpcreflect"
	"github.com/nickheyer/nebu/internal/doctor"
	"github.com/nickheyer/nebu/internal/gateway"
	"github.com/nickheyer/nebu/internal/inspect"
	"github.com/nickheyer/nebu/internal/installs"
	"github.com/nickheyer/nebu/internal/instances"
	"github.com/nickheyer/nebu/internal/monitor"
	"github.com/nickheyer/nebu/internal/pull"
	"github.com/nickheyer/nebu/internal/rpc/services"
	"github.com/nickheyer/nebu/internal/slots"
	"github.com/nickheyer/nebu/internal/tasks"
	"github.com/nickheyer/nebu/pkg/events"
	"github.com/nickheyer/nebu/pkg/host"
	"github.com/nickheyer/nebu/pkg/proto/nebu/v1/nebuv1connect"
	"github.com/nickheyer/nebu/pkg/runtime"
	"github.com/nickheyer/nebu/pkg/sources"
	"github.com/nickheyer/nebu/pkg/store"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

// Everything the handlers need
type Deps struct {
	Host    *host.Prober
	Doctor  *doctor.Doctor
	Sources *sources.Manager
	// Format ids hits are tagged with when a catalog names them
	Formats   []string
	Runtimes  *runtime.Registry
	Inspector *inspect.Inspector
	Store     *store.Store
	Puller    *pull.Puller
	Tasks     *tasks.Manager
	Installs  *installs.Manager
	Instances *instances.Manager
	Slots     *slots.Manager
	Monitor   *monitor.Manager
	Gateway   *gateway.Gateway
	// Mounts the gateway under /v1/ on this handler when it has no listener of its own
	GatewayShared bool
	Events        *events.Bus
	Snapshot      services.Snapshotter
	Web           http.Handler
	Token         string
	Log           *slog.Logger
}

// Builds the h2c handler serving every service
func NewHandler(d Deps) http.Handler {
	opts := connect.WithInterceptors(logging(d.Log), &auth{token: d.Token})
	mux := http.NewServeMux()
	mux.Handle(nebuv1connect.NewHostServiceHandler(services.NewHostService(d.Host, d.Doctor), opts))
	mux.Handle(nebuv1connect.NewSourceServiceHandler(services.NewSourceService(d.Sources, d.Inspector, d.Formats), opts))
	mux.Handle(nebuv1connect.NewRuntimeServiceHandler(services.NewRuntimeService(d.Runtimes, d.Host, d.Installs), opts))
	mux.Handle(nebuv1connect.NewInstanceServiceHandler(services.NewInstanceService(d.Instances), opts))
	mux.Handle(nebuv1connect.NewEstimateServiceHandler(services.NewEstimateService(d.Inspector), opts))
	mux.Handle(nebuv1connect.NewStoreServiceHandler(services.NewStoreService(d.Store, d.Puller, d.Events), opts))
	mux.Handle(nebuv1connect.NewTaskServiceHandler(services.NewTaskService(d.Tasks), opts))
	mux.Handle(nebuv1connect.NewBuildServiceHandler(services.NewBuildService(d.Installs), opts))
	mux.Handle(nebuv1connect.NewSlotServiceHandler(services.NewSlotService(d.Slots), opts))
	mux.Handle(nebuv1connect.NewGatewayServiceHandler(services.NewGatewayService(d.Gateway, d.Instances), opts))
	mux.Handle(nebuv1connect.NewMonitorServiceHandler(services.NewMonitorService(d.Monitor), opts))
	mux.Handle(nebuv1connect.NewEventServiceHandler(services.NewEventService(d.Events, d.Snapshot), opts))
	reflector := grpcreflect.NewStaticReflector(
		nebuv1connect.HostServiceName,
		nebuv1connect.SourceServiceName,
		nebuv1connect.RuntimeServiceName,
		nebuv1connect.EstimateServiceName,
		nebuv1connect.StoreServiceName,
		nebuv1connect.TaskServiceName,
		nebuv1connect.InstanceServiceName,
		nebuv1connect.BuildServiceName,
		nebuv1connect.SlotServiceName,
		nebuv1connect.GatewayServiceName,
		nebuv1connect.MonitorServiceName,
		nebuv1connect.EventServiceName,
	)
	mux.Handle(grpcreflect.NewHandlerV1(reflector))
	mux.Handle(grpcreflect.NewHandlerV1Alpha(reflector))
	if d.Gateway != nil && d.GatewayShared {
		d.Gateway.Mount(mux)
	} else {
		// Keeps OpenAI paths from falling through to the single page app
		mux.HandleFunc("/v1/", func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "the gateway listens on its own address, see nebu gateway", http.StatusNotFound)
		})
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

// Requires the configured bearer token on every call
type auth struct {
	token string
}

var errUnauthenticated = errors.New("missing or invalid token, set auth.token or NEBU_TOKEN")

func (a *auth) ok(header http.Header) bool {
	if a.token == "" {
		return true
	}
	const scheme = "bearer "
	h := header.Get("Authorization")
	if len(h) < len(scheme) || !strings.EqualFold(h[:len(scheme)], scheme) {
		return false
	}
	got := strings.TrimSpace(h[len(scheme):])
	return subtle.ConstantTimeCompare([]byte(got), []byte(a.token)) == 1
}

func (a *auth) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		if !a.ok(req.Header()) {
			return nil, connect.NewError(connect.CodeUnauthenticated, errUnauthenticated)
		}
		return next(ctx, req)
	}
}

func (a *auth) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (a *auth) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		if !a.ok(conn.RequestHeader()) {
			return connect.NewError(connect.CodeUnauthenticated, errUnauthenticated)
		}
		return next(ctx, conn)
	}
}
