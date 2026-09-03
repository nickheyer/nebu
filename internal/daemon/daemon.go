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
	"time"

	"github.com/nickheyer/nebu/internal/doctor"
	"github.com/nickheyer/nebu/internal/inspect"
	"github.com/nickheyer/nebu/internal/pull"
	"github.com/nickheyer/nebu/internal/rpc"
	"github.com/nickheyer/nebu/internal/tasks"
	"github.com/nickheyer/nebu/pkg/cache"
	"github.com/nickheyer/nebu/pkg/descriptor"
	"github.com/nickheyer/nebu/pkg/formats"
	formatsall "github.com/nickheyer/nebu/pkg/formats/all"
	"github.com/nickheyer/nebu/pkg/host"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/runtime"
	"github.com/nickheyer/nebu/pkg/sources"
	sourcesall "github.com/nickheyer/nebu/pkg/sources/all"
	"github.com/nickheyer/nebu/pkg/spec"
	"github.com/nickheyer/nebu/pkg/store"
	"github.com/nickheyer/nebu/pkg/transfer"
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
	Log       *slog.Logger
	handler   http.Handler
	cancel    context.CancelFunc
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
	d.Puller = &pull.Puller{
		Inspector: d.Inspector,
		Store:     blobStore,
		Fetcher:   transfer.New(int(tr.GetWorkers()), int64(tr.GetChunkBytes()), int(tr.GetRetries()), tr.GetMaxBytesPerSecond(), log),
		Tasks:     d.Tasks,
		Log:       log,
	}
	d.Doctor = &doctor.Doctor{Host: prober, Runtimes: runtimes, Sources: srcs, Store: blobStore, MinFree: cfg.GetMinFreeBytes()}
	d.handler = rpc.NewHandler(rpc.Deps{
		Host:      prober,
		Doctor:    d.Doctor,
		Sources:   srcs,
		Runtimes:  runtimes,
		Inspector: d.Inspector,
		Store:     blobStore,
		Puller:    d.Puller,
		Tasks:     d.Tasks,
		Log:       log,
	})
	return d, nil
}

// Stops background tasks
func (d *Daemon) Close() { d.cancel() }

// Returns the API handler for in process or network use
func (d *Daemon) Handler() http.Handler { return d.handler }

// Listens until ctx ends, then drains
func (d *Daemon) Serve(ctx context.Context) error {
	ln, err := net.Listen("tcp", d.Config.GetListen())
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: d.handler, ReadHeaderTimeout: readHeaderTimeout}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()
	d.Log.Info("listening", "addr", ln.Addr().String())
	select {
	case <-ctx.Done():
		d.cancel()
		stop, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		return srv.Shutdown(stop)
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}
