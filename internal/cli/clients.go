package cli

import (
	"net/http"
	"strings"

	"github.com/nickheyer/nebu/internal/daemon"
	"github.com/nickheyer/nebu/pkg/logger"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/proto/nebu/v1/nebuv1connect"
	"google.golang.org/protobuf/proto"
)

// Raises an in process daemon to warn level unless debugging
func quiet(cfg *v1.Logging) *v1.Logging {
	out := proto.Clone(cfg).(*v1.Logging)
	if !strings.EqualFold(out.GetLevel(), "debug") {
		out.Level = "warn"
	}
	return out
}

const localBase = "http://nebu.local"

// Connect clients for every service
type clients struct {
	host     nebuv1connect.HostServiceClient
	sources  nebuv1connect.SourceServiceClient
	runtimes nebuv1connect.RuntimeServiceClient
	estimate nebuv1connect.EstimateServiceClient
	store    nebuv1connect.StoreServiceClient
	tasks    nebuv1connect.TaskServiceClient
}

// Builds clients against the daemon or an in process handler
func (e *env) clients() (*clients, error) {
	if e.cl != nil {
		return e.cl, nil
	}
	base := localBase
	httpClient := &http.Client{}
	if addr := e.cfg.GetAddr(); addr != "" {
		base = addr
		if !strings.Contains(addr, "://") {
			base = "http://" + addr
		}
	} else {
		log, closer, err := logger.New(quiet(e.cfg.GetLogging()))
		if err != nil {
			return nil, err
		}
		e.closers = append(e.closers, closer)
		d, err := daemon.New(e.cfg, log)
		if err != nil {
			return nil, err
		}
		e.daemon = d
		httpClient.Transport = handlerTransport{handler: d.Handler()}
	}
	e.cl = &clients{
		host:     nebuv1connect.NewHostServiceClient(httpClient, base),
		sources:  nebuv1connect.NewSourceServiceClient(httpClient, base),
		runtimes: nebuv1connect.NewRuntimeServiceClient(httpClient, base),
		estimate: nebuv1connect.NewEstimateServiceClient(httpClient, base),
		store:    nebuv1connect.NewStoreServiceClient(httpClient, base),
		tasks:    nebuv1connect.NewTaskServiceClient(httpClient, base),
	}
	return e.cl, nil
}
