package cli

import (
	"bytes"
	"cmp"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"net"
	"net/http"
	"net/url"
	"os"
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
	if addr := e.resolveAddr(); addr != "" {
		base = e.base(addr)
		httpClient.Transport = e.transport(base)
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

// The daemon address, configured or found listening, empty when this process would be the daemon
func (e *env) resolveAddr() string {
	if e.resolved {
		return e.addr
	}
	e.resolved = true
	addr := e.cfg.GetAddr()
	if listen := dialable(e.cfg.GetListen(), ""); addr == "" && reachable(listen) {
		addr = listen
		e.log.Debug("using running daemon", "addr", addr)
	}
	e.addr, e.remote = addr, addr != ""
	return addr
}

// Prefixes a bare address with the scheme the daemon serves
func (e *env) base(addr string) string {
	if strings.Contains(addr, "://") {
		return addr
	}
	if e.cfg.GetTls().GetCertFile() != "" {
		return "https://" + addr
	}
	return "http://" + addr
}

// Swaps an unspecified host for one this machine can dial
func dialable(addr, host string) string {
	h, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	if ip := net.ParseIP(h); h != "" && (ip == nil || !ip.IsUnspecified()) {
		return addr
	}
	if host == "" {
		host = "localhost"
	}
	return net.JoinHostPort(host, port)
}

// The transport a daemon at base is dialed through, trusting its own certificate as is
func (e *env) transport(base string) http.RoundTripper {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.ForceAttemptHTTP2 = true
	own := e.ownCert()
	// An address dialed by IP sends no server name, so the name checked is the one dialed
	dialed := ""
	if u, err := url.Parse(base); err == nil {
		dialed = u.Hostname()
	}
	t.TLSClientConfig = &tls.Config{
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: true,
		// The daemon's configured certificate needs no authority or name, anything else does
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return errors.New("tls: the server presented no certificate")
			}
			leaf := cs.PeerCertificates[0]
			if own != nil && bytes.Equal(leaf.Raw, own.Raw) {
				return nil
			}
			opts := x509.VerifyOptions{DNSName: cmp.Or(cs.ServerName, dialed), Intermediates: x509.NewCertPool()}
			for _, c := range cs.PeerCertificates[1:] {
				opts.Intermediates.AddCert(c)
			}
			_, err := leaf.Verify(opts)
			return err
		},
	}
	return t
}

// The certificate tls.cert_file holds, nil when unset or unreadable
func (e *env) ownCert() *x509.Certificate {
	file := e.cfg.GetTls().GetCertFile()
	if file == "" {
		return nil
	}
	data, err := os.ReadFile(file)
	if err != nil {
		e.log.Debug("tls.cert_file unreadable, verifying the daemon against system roots", "file", file, "err", err)
		return nil
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil
	}
	return cert
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
