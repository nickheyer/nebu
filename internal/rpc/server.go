// Package rpc mounts Connect handlers on an HTTP handler.
package rpc

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"connectrpc.com/connect"
	"connectrpc.com/grpcreflect"
	"github.com/nickheyer/nebu/internal/doctor"
	"github.com/nickheyer/nebu/internal/gateway"
	"github.com/nickheyer/nebu/internal/inspect"
	"github.com/nickheyer/nebu/internal/installs"
	"github.com/nickheyer/nebu/internal/instances"
	"github.com/nickheyer/nebu/internal/pull"
	"github.com/nickheyer/nebu/internal/rpc/services"
	"github.com/nickheyer/nebu/internal/tasks"
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
	Host      *host.Prober
	Doctor    *doctor.Doctor
	Sources   *sources.Registry
	Runtimes  *runtime.Registry
	Inspector *inspect.Inspector
	Store     *store.Store
	Puller    *pull.Puller
	Tasks     *tasks.Manager
	Installs  *installs.Manager
	Instances *instances.Manager
	Gateway   *gateway.Gateway
	Log       *slog.Logger
}

// Builds the h2c handler serving every service
func NewHandler(d Deps) http.Handler {
	opts := connect.WithInterceptors(logging(d.Log))
	mux := http.NewServeMux()
	mux.Handle(nebuv1connect.NewHostServiceHandler(services.NewHostService(d.Host, d.Doctor), opts))
	mux.Handle(nebuv1connect.NewSourceServiceHandler(services.NewSourceService(d.Sources, d.Inspector), opts))
	mux.Handle(nebuv1connect.NewRuntimeServiceHandler(services.NewRuntimeService(d.Runtimes, d.Host, d.Installs), opts))
	mux.Handle(nebuv1connect.NewInstanceServiceHandler(services.NewInstanceService(d.Instances), opts))
	mux.Handle(nebuv1connect.NewEstimateServiceHandler(services.NewEstimateService(d.Inspector), opts))
	mux.Handle(nebuv1connect.NewStoreServiceHandler(services.NewStoreService(d.Store, d.Puller), opts))
	mux.Handle(nebuv1connect.NewTaskServiceHandler(services.NewTaskService(d.Tasks), opts))
	reflector := grpcreflect.NewStaticReflector(
		nebuv1connect.HostServiceName,
		nebuv1connect.SourceServiceName,
		nebuv1connect.RuntimeServiceName,
		nebuv1connect.EstimateServiceName,
		nebuv1connect.StoreServiceName,
		nebuv1connect.TaskServiceName,
		nebuv1connect.InstanceServiceName,
	)
	mux.Handle(grpcreflect.NewHandlerV1(reflector))
	mux.Handle(grpcreflect.NewHandlerV1Alpha(reflector))
	d.Gateway.Mount(mux)
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
