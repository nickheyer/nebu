package daemon

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/nickheyer/nebu/internal/auth"
	"github.com/nickheyer/nebu/internal/mesh/links"
	"github.com/nickheyer/nebu/pkg/config"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

const (
	// Time between readiness checks while the gateway listener waits to bind
	readyPoll = 50 * time.Millisecond
	// Time between warnings naming the checks that keep the gateway listener unbound
	readyWarn = 10 * time.Second
)

// Returns the API handler for in process or network use
func (d *Daemon) Handler() http.Handler { return d.handler }

// Binds the API listener, then recovers and serves every listener until ctx ends, over TLS unless tls.disabled is set
func (d *Daemon) ListenAndServe(ctx context.Context) error {
	d.secure = d.tls != nil
	ln, err := d.listen(d.Config.GetListen())
	if err != nil {
		d.Close()
		return err
	}
	if err := d.lock.name(ln.Addr().String()); err != nil {
		ln.Close()
		d.Close()
		return err
	}
	d.warnExposure(d.secure)
	return d.Serve(ctx, ln)
}

// Binds an address under the listener certificate, plaintext when tls.disabled is set
func (d *Daemon) listen(addr string) (net.Listener, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil || d.tls == nil {
		return ln, err
	}
	return tls.NewListener(ln, d.tls), nil
}

// A server speaking HTTP/1.1, HTTP/2 over TLS and cleartext HTTP/2, its request contexts under requests
func (d *Daemon) server(h http.Handler, requests context.Context) *http.Server {
	return &http.Server{
		Handler:           h,
		ReadHeaderTimeout: readHeaderTimeout,
		IdleTimeout:       idleTimeout,
		BaseContext:       func(net.Listener) context.Context { return requests },
		Protocols:         links.Protocols(),
		ErrorLog:          slog.NewLogLogger(d.Log.Handler(), slog.LevelDebug),
	}
}

// Answers liveness while the daemon recovers and 503 on every other path, readiness included, until it opens
type gate struct {
	open   atomic.Bool
	health http.Handler
	next   http.Handler
}

func (g *gate) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if g.open.Load() {
		g.next.ServeHTTP(w, r)
		return
	}
	if r.URL.Path == "/livez" {
		g.health.ServeHTTP(w, r)
		return
	}
	w.Header().Set("Retry-After", "1")
	http.Error(w, "the daemon is recovering its state, retry shortly", http.StatusServiceUnavailable)
}

// Answers 503 with Retry-After on readiness and on gateway paths other than liveness until ready, passing every other path
type readyGate struct {
	ready *atomic.Bool
	// The gateway's paths alone, what a request is matched against
	gateway *http.ServeMux
	next    http.Handler
}

func (g *readyGate) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !g.ready.Load() && r.URL.Path != "/livez" {
		_, pattern := g.gateway.Handler(r)
		if r.URL.Path == "/readyz" || pattern != "" {
			w.Header().Set("Retry-After", "1")
			http.Error(w, "the gateway waits for every readiness check, retry shortly", http.StatusServiceUnavailable)
			return
		}
	}
	g.next.ServeHTTP(w, r)
}

// Serves health at once, recovers, opens the API, opens gateway paths and listeners when ready, and shuts down
func (d *Daemon) Serve(ctx context.Context, ln net.Listener) error {
	d.addr = ln.Addr().String()
	requests, endRequests := context.WithCancel(context.Background())
	defer endRequests()
	healthMux := http.NewServeMux()
	d.Health.Mount(healthMux)
	ready := new(atomic.Bool)
	gatewayPaths := http.NewServeMux()
	d.Gateway.Mount(gatewayPaths)
	next := d.handler
	if d.Config.GetGateway().GetListen() == "" {
		next = &readyGate{ready: ready, gateway: gatewayPaths, next: d.handler}
	}
	d.Mesh.Handler = &readyGate{ready: ready, gateway: gatewayPaths, next: d.Mesh.Handler}
	g := &gate{health: healthMux, next: next}
	errCh := make(chan error, 4)
	var servers []*http.Server
	serve := func(h http.Handler, l net.Listener) {
		srv := d.server(h, requests)
		servers = append(servers, srv)
		go func() { errCh <- srv.Serve(l) }()
	}
	serve(g, ln)
	d.Log.Info("listening", "addr", d.addr, "tls", d.secure)
	if err := d.start(ctx); err != nil {
		return errors.Join(err, d.shutdown(servers, endRequests))
	}
	g.open.Store(true)
	if err := d.awaitReady(ctx); err != nil {
		return d.shutdown(servers, endRequests)
	}
	ready.Store(true)
	listeners := []*v1.Listener{{Addr: d.addr, Shared: true}}
	if addr := d.Config.GetGateway().GetListen(); addr != "" {
		gln, err := d.listen(addr)
		if err != nil {
			return errors.Join(fmt.Errorf("gateway.listen %s: %w", addr, err), d.shutdown(servers, endRequests))
		}
		serve(d.Gateway.Handler(), gln)
		listeners = []*v1.Listener{{Addr: gln.Addr().String()}}
		d.Log.Info("gateway listening", "addr", gln.Addr().String(), "tls", d.secure)
	}
	d.Gateway.SetListeners(listeners, d.secure)
	if addr := d.Config.GetMetrics().GetListen(); addr != "" {
		mln, err := d.listen(addr)
		if err != nil {
			return errors.Join(fmt.Errorf("metrics.listen %s: %w", addr, err), d.shutdown(servers, endRequests))
		}
		mux := http.NewServeMux()
		d.Gateway.MountHealth(mux)
		serve(mux, mln)
		d.Log.Info("metrics listening", "addr", mln.Addr().String(), "tls", d.secure)
	}
	if addr := d.Config.GetScheduler().GetListen(); addr != "" {
		sln, err := d.listen(addr)
		if err != nil {
			return errors.Join(fmt.Errorf("scheduler.listen %s: %w", addr, err), d.shutdown(servers, endRequests))
		}
		serve(d.Gateway.ScheduleHandler(), sln)
		d.Log.Info("scheduler listening", "addr", sln.Addr().String(), "tls", d.secure)
	}
	var result error
	select {
	case <-ctx.Done():
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			result = err
		}
	}
	d.Log.Info("shutting down")
	return errors.Join(result, d.shutdown(servers, endRequests))
}

// Starts the pool sources, recovers every manager, marks the endpoint set synced, runs the gateway, then joins the mesh
func (d *Daemon) start(ctx context.Context) error {
	if err := d.Pools.Start(d.base); err != nil {
		return err
	}
	for _, recover := range []func(context.Context) error{d.Tasks.Recover, d.Installs.RecoverBuilds, d.Instances.Recover, d.Slots.Recover, d.Formations.Recover} {
		if err := recover(ctx); err != nil {
			return err
		}
	}
	d.Pools.Store().MarkSynced()
	for _, loop := range []func(context.Context){d.Gateway.Run, d.Notifier.Run, d.Instances.RefreshDescriptors, d.watchHost, d.Installs.Refresh, d.watchConfig} {
		d.background.Go(func() { loop(d.base) })
	}
	if _, err := d.Doctor.Start(ctx); err != nil {
		d.Log.Warn("host check failed to start", "err", err)
	}
	if err := d.Mesh.Start(d.base); err != nil {
		return err
	}
	d.Formations.Start()
	return d.Bots.Recover(ctx)
}

// Waits until every readiness check passes, naming the failing checks every few seconds
func (d *Daemon) awaitReady(ctx context.Context) error {
	tick := time.NewTicker(readyPoll)
	defer tick.Stop()
	warned := time.Now()
	for {
		rep := d.Health.Readiness(ctx)
		if rep.OK {
			return nil
		}
		if time.Since(warned) >= readyWarn {
			d.Log.Warn("the gateway listener waits for readiness", "checks", rep.Checks)
			warned = time.Now()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}

// Stops accepting, lets requests finish within the drain timeout while managers drain, ends the rest, then closes
func (d *Daemon) shutdown(servers []*http.Server, endRequests context.CancelFunc) error {
	drainFor := time.Duration(d.Config.GetGateway().GetDrainTimeoutMs()) * time.Millisecond
	stop, cancel := context.WithTimeout(context.Background(), drainFor+shutdownTimeout)
	defer cancel()
	done := make(chan error, len(servers))
	for _, srv := range servers {
		go func() { done <- srv.Shutdown(stop) }()
	}
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		d.drain()
	}()
	var result error
	finished := 0
	deadline := time.NewTimer(drainFor)
	defer deadline.Stop()
wait:
	for finished < len(servers) {
		select {
		case err := <-done:
			finished++
			result = errors.Join(result, err)
		case <-deadline.C:
			d.Log.Warn("requests still in flight at the drain timeout are ended", "drain_timeout", drainFor)
			break wait
		}
	}
	endRequests()
	for ; finished < len(servers); finished++ {
		result = errors.Join(result, <-done)
	}
	<-drained
	d.Close()
	return result
}

// Stops the bots, then drains formations and instances through their drain paths, once
func (d *Daemon) drain() {
	d.drainOnce.Do(func() {
		d.Bots.Close()
		d.Formations.Close()
		d.Instances.Close()
	})
}

// Drains, leaves the mesh listener, stops the gateway and pool sources, then closes the store and lets the data directory go
func (d *Daemon) Close() {
	d.closeOnce.Do(func() {
		d.drain()
		d.Mesh.Close()
		d.Tools.Close()
		d.cancel()
		d.background.Wait()
		if err := d.Pools.Close(); err != nil {
			d.Log.Warn("pool data sources stopped with an error", "err", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		d.Tasks.Drain(ctx)
		d.Perf.Flush()
		if err := d.stopTelemetry(ctx); err != nil {
			d.Log.Warn("spans not flushed", "err", err)
		}
		if err := d.DB.Close(); err != nil {
			d.Log.Warn("store close failed", "err", err)
		}
		if err := d.lock.release(); err != nil {
			d.Log.Warn("data directory lock not released", "err", err)
		}
		d.Log.Info("stopped")
	})
}

// Releases what a failed New built before returning its error
func (d *Daemon) abandon() {
	if d.Formations != nil {
		d.Formations.Close()
	}
	if d.Mesh != nil {
		d.Mesh.Close()
	}
	if d.Tools != nil {
		d.Tools.Close()
	}
	d.cancel()
	if d.Pools != nil {
		d.Pools.Close()
	}
	if d.stopTelemetry != nil {
		d.stopTelemetry(context.Background())
	}
	if d.DB != nil {
		d.DB.Close()
	}
	d.lock.release()
}

// The listener certificate, generated in data_dir when tls names none, nil when tls.disabled chooses plaintext
func (d *Daemon) tlsConfig() (*tls.Config, error) {
	config.DeriveTLS(d.Config)
	t := d.Config.GetTls()
	if t.GetDisabled() {
		if t.GetCertFile() != "" || t.GetKeyFile() != "" {
			return nil, errors.New("tls.disabled is set beside tls.cert_file or tls.key_file, keep one")
		}
		d.Log.Warn("tls.disabled is set, the API, gateway and metrics listeners serve plaintext")
		return nil, nil
	}
	generatedCert, generatedKey := filepath.Join(d.Config.GetDataDir(), config.CertFile), filepath.Join(d.Config.GetDataDir(), config.KeyFile)
	var cert tls.Certificate
	var err error
	created := false
	if t.GetCertFile() == generatedCert && t.GetKeyFile() == generatedKey {
		hosts := auth.CertHosts(d.Config.GetListen(), d.Config.GetGateway().GetListen(), d.Config.GetMetrics().GetListen(), d.Config.GetMesh().GetListen(), d.Config.GetMesh().GetAdvertise())
		cert, created, err = auth.LoadOrCreateCert(generatedCert, generatedKey, hosts, time.Now())
	} else {
		cert, err = tls.LoadX509KeyPair(t.GetCertFile(), t.GetKeyFile())
	}
	if err != nil {
		return nil, fmt.Errorf("tls: %w", err)
	}
	d.Log.Info("tls certificate", "file", t.GetCertFile(), "sha256", auth.Fingerprint(cert), "generated", created)
	return &tls.Config{Certificates: []tls.Certificate{cert}, NextProtos: []string{"h2", "http/1.1"}, MinVersion: tls.VersionTLS12}, nil
}

// Warns about listeners beyond loopback serving plaintext or without authentication, the metrics and scheduler listeners included, the mesh warning at its own bind
func (d *Daemon) warnExposure(secure bool) {
	api, gw, metricsAddr, schedAddr := d.Config.GetListen(), d.Config.GetGateway().GetListen(), d.Config.GetMetrics().GetListen(), d.Config.GetScheduler().GetListen()
	for _, addr := range []string{api, gw, metricsAddr, schedAddr} {
		host, _, err := net.SplitHostPort(addr)
		if addr == "" || err != nil {
			continue
		}
		ip := net.ParseIP(host)
		if host == "localhost" || ip != nil && ip.IsLoopback() {
			continue
		}
		if addr == schedAddr {
			d.Log.Warn("the scheduler listener answers schedule calls from any caller beyond loopback, keep it on a network only gateways reach", "addr", addr)
		}
		if !secure {
			d.Log.Warn("plaintext listener beyond loopback, tls.disabled is set, remove it or put a TLS reverse proxy in front", "addr", addr)
		}
		if addr == api && d.Sessions == nil {
			d.Log.Warn("public API listener with auth.disabled. Anyone with network access can control the daemon", "addr", addr)
		}
		if addr == api && d.Users != nil && d.Users.Count() == 0 {
			d.Log.Warn("no account exists yet, the first visitor to the web UI creates it", "addr", addr)
		}
		shared := gw == "" && addr == api
		if (addr == gw || shared || addr == metricsAddr) && len(d.Config.GetGateway().GetApiKeys()) == 0 && d.Sessions == nil {
			d.Log.Warn("a gateway or metrics listener is beyond loopback with auth.disabled and no gateway.api_keys. Anyone with network access reaches it", "addr", addr)
		}
	}
}
