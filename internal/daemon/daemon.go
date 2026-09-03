// Package daemon wires every manager and serves the API.
package daemon

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/nickheyer/nebu/internal/calibrate"
	"github.com/nickheyer/nebu/internal/doctor"
	"github.com/nickheyer/nebu/internal/gateway"
	"github.com/nickheyer/nebu/internal/inspect"
	"github.com/nickheyer/nebu/internal/installs"
	"github.com/nickheyer/nebu/internal/instances"
	"github.com/nickheyer/nebu/internal/pull"
	"github.com/nickheyer/nebu/internal/rpc"
	"github.com/nickheyer/nebu/internal/tasks"
	"github.com/nickheyer/nebu/pkg/cache"
	"github.com/nickheyer/nebu/pkg/descriptor"
	"github.com/nickheyer/nebu/pkg/formats"
	formatsall "github.com/nickheyer/nebu/pkg/formats/all"
	"github.com/nickheyer/nebu/pkg/host"
	"github.com/nickheyer/nebu/pkg/launch"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/runtime"
	"github.com/nickheyer/nebu/pkg/sources"
	sourcesall "github.com/nickheyer/nebu/pkg/sources/all"
	"github.com/nickheyer/nebu/pkg/spec"
	"github.com/nickheyer/nebu/pkg/store"
	"github.com/nickheyer/nebu/pkg/transfer"
	"github.com/nickheyer/nebu/pkg/triage"
	specfs "github.com/nickheyer/nebu/spec"
)

const (
	profileTTL        = 30 * time.Second
	shutdownTimeout   = 10 * time.Second
	readHeaderTimeout = 10 * time.Second
)

// Running set of managers behind one handler
type Daemon struct {
	Config    *v1.Config
	Catalog   *spec.Catalog
	Host      *host.Prober
	Sources   *sources.Registry
	Runtimes  *runtime.Registry
	Inspector *inspect.Inspector
	Doctor    *doctor.Doctor
	Store     *store.Store
	Tasks     *tasks.Manager
	Puller    *pull.Puller
	Installs  *installs.Manager
	Instances *instances.Manager
	Gateway   *gateway.Gateway
	Log       *slog.Logger
	handler   http.Handler
	cancel    context.CancelFunc
	addr      string
}

// Builds every manager from config
func New(cfg *v1.Config, log *slog.Logger) (*Daemon, error) {
	for _, dir := range []string{cfg.GetDataDir(), cfg.GetCacheDir(), cfg.GetStoreDir()} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	layers := append([]fs.FS{specfs.FS()}, spec.Dirs(cfg.GetSpecDirs())...)
	catalog, err := spec.Load(layers...)
	if err != nil {
		return nil, err
	}
	prober, err := host.New(catalog.Probes, []string{cfg.GetStoreDir(), cfg.GetDataDir(), cfg.GetCacheDir()}, profileTTL)
	if err != nil {
		return nil, err
	}
	srcs, err := sources.Build(cfg.GetSources(), sourcesall.Constructors())
	if err != nil {
		return nil, err
	}
	classifier, err := formats.NewClassifier(catalog.Formats)
	if err != nil {
		return nil, err
	}
	readers, err := formats.BuildReaders(catalog.Formats, formatsall.Constructors())
	if err != nil {
		return nil, err
	}
	builder, err := descriptor.New(catalog.Formats, catalog.Archs)
	if err != nil {
		return nil, err
	}
	runtimes, err := runtime.New(catalog.Runtimes)
	if err != nil {
		return nil, err
	}
	cacheStore, err := cache.Open(cfg.GetCacheDir())
	if err != nil {
		return nil, err
	}
	blobStore, err := store.Open(cfg.GetStoreDir())
	if err != nil {
		return nil, err
	}
	base, cancel := context.WithCancel(context.Background())
	d := &Daemon{
		Config:   cfg,
		Catalog:  catalog,
		Host:     prober,
		Sources:  srcs,
		Runtimes: runtimes,
		Store:    blobStore,
		Tasks:    tasks.New(base, log),
		Log:      log,
		cancel:   cancel,
	}
	d.Inspector = &inspect.Inspector{
		Sources:    srcs,
		Classifier: classifier,
		Readers:    readers,
		Builder:    builder,
		Runtimes:   runtimes,
		Host:       prober,
		Cache:      cacheStore,
		Contexts:   cfg.GetContexts(),
		Log:        log,
	}
	tr := cfg.GetTransfer()
	fetcher := transfer.New(int(tr.GetWorkers()), int64(tr.GetChunkBytes()), int(tr.GetRetries()), tr.GetMaxBytesPerSecond(), log)
	d.Puller = &pull.Puller{
		Inspector: d.Inspector,
		Store:     blobStore,
		Fetcher:   fetcher,
		Tasks:     d.Tasks,
		Log:       log,
	}
	d.Installs = &installs.Manager{
		Dir:         filepath.Join(cfg.GetDataDir(), "installs"),
		RuntimesDir: filepath.Join(cfg.GetDataDir(), "runtimes"),
		Runtimes:    runtimes,
		Host:        prober,
		Tasks:       d.Tasks,
		Fetcher:     fetcher,
		Log:         log,
	}
	matcher, err := triage.New(catalog.Triage)
	if err != nil {
		return nil, err
	}
	calibration, err := calibrate.Open(filepath.Join(cfg.GetDataDir(), "calibration.json"))
	if err != nil {
		return nil, err
	}
	d.Instances = &instances.Manager{
		Store:       blobStore,
		Runtimes:    runtimes,
		Installs:    d.Installs,
		Inspector:   d.Inspector,
		Launcher:    &launch.ProcessLauncher{Log: log},
		Tasks:       d.Tasks,
		Host:        prober,
		Triage:      matcher,
		Calibration: calibration,
		Log:         log,
	}
	d.Gateway = gateway.New(d.Instances, log)
	d.Doctor = &doctor.Doctor{Host: prober, Runtimes: runtimes, Sources: srcs, Store: blobStore, Installs: d.Installs, MinFree: cfg.GetMinFreeBytes()}
	d.handler = rpc.NewHandler(rpc.Deps{
		Host:      prober,
		Doctor:    d.Doctor,
		Sources:   srcs,
		Runtimes:  runtimes,
		Inspector: d.Inspector,
		Store:     blobStore,
		Puller:    d.Puller,
		Tasks:     d.Tasks,
		Installs:  d.Installs,
		Instances: d.Instances,
		Gateway:   d.Gateway,
		Log:       log,
	})
	return d, nil
}

// Stops instances and background tasks
func (d *Daemon) Close() {
	d.Instances.Close()
	d.cancel()
}

// Returns the bound API address once serving
func (d *Daemon) Addr() string { return d.addr }

// Returns the API handler for in process or network use
func (d *Daemon) Handler() http.Handler { return d.handler }

// Listens on the configured addresses until ctx ends
func (d *Daemon) ListenAndServe(ctx context.Context) error {
	ln, err := net.Listen("tcp", d.Config.GetListen())
	if err != nil {
		return err
	}
	var gatewayLn net.Listener
	if addr := d.Config.GetGateway().GetListen(); addr != "" {
		if gatewayLn, err = net.Listen("tcp", addr); err != nil {
			ln.Close()
			return err
		}
	}
	return d.Serve(ctx, ln, gatewayLn)
}

// Serves the API on ln and optionally the gateway alone on gatewayLn
func (d *Daemon) Serve(ctx context.Context, ln, gatewayLn net.Listener) error {
	d.addr = ln.Addr().String()
	servers := []*http.Server{{Handler: d.handler, ReadHeaderTimeout: readHeaderTimeout}}
	listeners := []net.Listener{ln}
	if gatewayLn != nil {
		servers = append(servers, &http.Server{Handler: d.Gateway.Handler(), ReadHeaderTimeout: readHeaderTimeout})
		listeners = append(listeners, gatewayLn)
		d.Log.Info("gateway listening", "addr", gatewayLn.Addr().String())
	}
	errCh := make(chan error, len(servers))
	for i, srv := range servers {
		go func(srv *http.Server, ln net.Listener) { errCh <- srv.Serve(ln) }(srv, listeners[i])
	}
	d.Log.Info("listening", "addr", d.addr)
	var result error
	select {
	case <-ctx.Done():
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			result = err
		}
	}
	d.Close()
	stop, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	for _, srv := range servers {
		if err := srv.Shutdown(stop); err != nil && result == nil {
			result = err
		}
	}
	return result
}
