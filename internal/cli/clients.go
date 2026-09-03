package cli

import (
	"net/http"
	"strings"

	"github.com/nickheyer/nebu/internal/daemon"
	"github.com/nickheyer/nebu/pkg/proto/nebu/v1/nebuv1connect"
)

const localBase = "http://nebu.local"

// Connect clients for every service
type clients struct {
	host     nebuv1connect.HostServiceClient
	sources  nebuv1connect.SourceServiceClient
	runtimes nebuv1connect.RuntimeServiceClient
	estimate nebuv1connect.EstimateServiceClient
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
		d, err := daemon.New(e.cfg, e.log)
		if err != nil {
			return nil, err
		}
		httpClient.Transport = handlerTransport{handler: d.Handler()}
	}
	e.cl = &clients{
		host:     nebuv1connect.NewHostServiceClient(httpClient, base),
		sources:  nebuv1connect.NewSourceServiceClient(httpClient, base),
		runtimes: nebuv1connect.NewRuntimeServiceClient(httpClient, base),
		estimate: nebuv1connect.NewEstimateServiceClient(httpClient, base),
	}
	return e.cl, nil
}
