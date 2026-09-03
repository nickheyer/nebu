package cli

import (
	"net"
	"net/http"
	"strings"
	"time"

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

const (
	localBase   = "http://nebu.local"
	dialTimeout = 300 * time.Millisecond
)

// Connect clients for every service
type clients struct {
	host      nebuv1connect.HostServiceClient
	sources   nebuv1connect.SourceServiceClient
	runtimes  nebuv1connect.RuntimeServiceClient
	estimate  nebuv1connect.EstimateServiceClient
	store     nebuv1connect.StoreServiceClient
	tasks     nebuv1connect.TaskServiceClient
	instances nebuv1connect.InstanceServiceClient
	builds    nebuv1connect.BuildServiceClient
	slots     nebuv1connect.SlotServiceClient
	gateway   nebuv1connect.GatewayServiceClient
	monitor   nebuv1connect.MonitorServiceClient
	events    nebuv1connect.EventServiceClient
}

// Adds the bearer token to every request
type authTransport struct {
	base  http.RoundTripper
	token string
}

func (t authTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.token != "" && req.Header.Get("Authorization") == "" {
		req = req.Clone(req.Context())
		req.Header.Set("Authorization", "Bearer "+t.token)
	}
	return t.base.RoundTrip(req)
}

// Builds clients against the daemon or an in process handler
func (e *env) clients() (*clients, error) {
	if e.cl != nil {
		return e.cl, nil
	}
	base := localBase
	httpClient := &http.Client{}
	addr := e.cfg.GetAddr()
	if addr == "" && reachable(e.cfg.GetListen()) {
		addr = e.cfg.GetListen()
		e.log.Debug("using running daemon", "addr", addr)
	}
	if addr != "" {
		e.remote = true
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
	if httpClient.Transport == nil {
		httpClient.Transport = http.DefaultTransport
	}
	httpClient.Transport = authTransport{base: httpClient.Transport, token: e.cfg.GetAuth().GetToken()}
	e.cl = &clients{
		host:      nebuv1connect.NewHostServiceClient(httpClient, base),
		sources:   nebuv1connect.NewSourceServiceClient(httpClient, base),
		runtimes:  nebuv1connect.NewRuntimeServiceClient(httpClient, base),
		estimate:  nebuv1connect.NewEstimateServiceClient(httpClient, base),
		store:     nebuv1connect.NewStoreServiceClient(httpClient, base),
		tasks:     nebuv1connect.NewTaskServiceClient(httpClient, base),
		instances: nebuv1connect.NewInstanceServiceClient(httpClient, base),
		builds:    nebuv1connect.NewBuildServiceClient(httpClient, base),
		slots:     nebuv1connect.NewSlotServiceClient(httpClient, base),
		gateway:   nebuv1connect.NewGatewayServiceClient(httpClient, base),
		monitor:   nebuv1connect.NewMonitorServiceClient(httpClient, base),
		events:    nebuv1connect.NewEventServiceClient(httpClient, base),
	}
	return e.cl, nil
}

// Reports whether something accepts connections at addr
func reachable(addr string) bool {
	if addr == "" {
		return false
	}
	conn, err := net.DialTimeout("tcp", addr, dialTimeout)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}
