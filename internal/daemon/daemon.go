// Package daemon wires every manager and serves the API.
package daemon

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/nickheyer/nebu/internal/calibrate"
	"github.com/nickheyer/nebu/internal/db"
	"github.com/nickheyer/nebu/internal/doctor"
	"github.com/nickheyer/nebu/internal/gateway"
	"github.com/nickheyer/nebu/internal/inspect"
	"github.com/nickheyer/nebu/internal/installs"
	"github.com/nickheyer/nebu/internal/instances"
	"github.com/nickheyer/nebu/internal/monitor"
	"github.com/nickheyer/nebu/internal/pull"
	"github.com/nickheyer/nebu/internal/rpc"
	"github.com/nickheyer/nebu/internal/slots"
	"github.com/nickheyer/nebu/internal/tasks"
	"github.com/nickheyer/nebu/pkg/build"
	"github.com/nickheyer/nebu/pkg/cache"
	"github.com/nickheyer/nebu/pkg/descriptor"
	"github.com/nickheyer/nebu/pkg/events"
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
	web "github.com/nickheyer/nebu/web/nebu"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	profileTTL        = 30 * time.Second
	shutdownTimeout   = 10 * time.Second
	readHeaderTimeout = 10 * time.Second
)

// Running set of managers behind one handler
type Daemon struct {
	Config    *v1.Config
	DB        *db.DB
	Catalog   *spec.Catalog
	Host      *host.Prober
	Sources   *sources.Registry
	Runtimes  *runtime.Registry
	Recipes   *build.Registry
	Events    *events.Bus
	Inspector *inspect.Inspector
	Doctor    *doctor.Doctor
	Store     *store.Store
	Tasks     *tasks.Manager
	Puller    *pull.Puller
	Installs  *installs.Manager
	Instances *instances.Manager
	Slots     *slots.Manager
	Monitor   *monitor.Manager
	Gateway   *gateway.Gateway
	Routes    *gateway.Table
	Log       *slog.Logger
	handler   http.Handler
	cancel    context.CancelFunc
	closeOnce sync.Once
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
	recipes, err := build.New(catalog.Recipes)
	if err != nil {
		return nil, err
	}
	for _, rt := range runtimes.List() {
		if id := rt.Manifest.GetAcquire().GetRecipeId(); id != "" {
			if _, err := recipes.Get(id); err != nil {
				return nil, fmt.Errorf("runtime %s: %w", rt.Manifest.GetId(), err)
			}
		}
	}
	cacheStore, err := cache.Open(cfg.GetCacheDir())
	if err != nil {
		return nil, err
	}
	blobStore, err := store.Open(cfg.GetStoreDir())
	if err != nil {
		return nil, err
	}
	store, err := db.Open(filepath.Join(cfg.GetDataDir(), "nebu.db"))
	if err != nil {
		return nil, err
	}
	if err := store.ImportLegacy(context.Background(), cfg.GetDataDir(), log); err != nil {
		store.Close()
		return nil, err
	}
	base, cancel := context.WithCancel(context.Background())
	bus := events.New()
	d := &Daemon{
		Config:   cfg,
		DB:       store,
		Catalog:  catalog,
		Host:     prober,
		Sources:  srcs,
		Runtimes: runtimes,
		Recipes:  recipes,
		Events:   bus,
		Store:    blobStore,
		Tasks:    tasks.New(base, log, store, bus),
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
		DB:          store,
		RuntimesDir: filepath.Join(cfg.GetDataDir(), "runtimes"),
		Runtimes:    runtimes,
		Recipes:     recipes,
		Engine:      &build.Engine{Root: cfg.GetBuilds().GetDir(), Patches: catalog.Patches, Jobs: int(cfg.GetBuilds().GetJobs()), Log: log},
		Defaults:    cfg.GetBuilds(),
		Host:        prober,
		Tasks:       d.Tasks,
		Fetcher:     fetcher,
		Events:      bus,
		Log:         log,
	}
	matcher, err := triage.New(catalog.Triage)
	if err != nil {
		store.Close()
		return nil, err
	}
	calibration, err := calibrate.Open(context.Background(), store)
	if err != nil {
		store.Close()
		return nil, err
	}
	routes, err := gateway.OpenTable(context.Background(), store, bus)
	if err != nil {
		store.Close()
		return nil, err
	}
	d.Routes = routes
	drain := time.Duration(cfg.GetGateway().GetDrainTimeoutMs()) * time.Millisecond
	d.Instances = &instances.Manager{
		DB:          store,
		Dir:         filepath.Join(cfg.GetDataDir(), "instances"),
		Store:       blobStore,
		Runtimes:    runtimes,
		Installs:    d.Installs,
		Inspector:   d.Inspector,
		Launcher:    &launch.ProcessLauncher{Log: log},
		Tasks:       d.Tasks,
		Host:        prober,
		Triage:      matcher,
		Calibration: calibration,
		Routes:      routes,
		Events:      bus,
		Log:         log,
	}
	d.Slots = &slots.Manager{DB: store, Instances: d.Instances, Routes: routes, Tasks: d.Tasks, Host: prober, Events: bus, DrainTimeout: drain, Log: log}
	if err := d.Slots.Load(context.Background()); err != nil {
		store.Close()
		return nil, err
	}
	d.Instances.Reserver = d.Slots
	d.Instances.OnChange = d.Slots.OnInstance
	d.Inspector.Constrain = func(ctx context.Context, slotID string, profile *v1.HostProfile) (*v1.HostProfile, error) {
		res, err := d.Slots.Reservation(ctx, slotID)
		if err != nil {
			return nil, err
		}
		return instances.Constrain(profile, res.DeviceIDs, res.MemoryBytes), nil
	}
	d.Monitor = &monitor.Manager{
		DB:        store,
		Inspector: d.Inspector,
		Puller:    d.Puller,
		Slots:     d.Slots,
		Tasks:     d.Tasks,
		Events:    bus,
		Interval:  time.Duration(cfg.GetMonitor().GetIntervalMs()) * time.Millisecond,
		Disabled:  cfg.GetMonitor().GetDisabled(),
		Log:       log,
	}
	if err := d.Monitor.Load(context.Background()); err != nil {
		store.Close()
		return nil, err
	}
	d.Gateway = gateway.New(routes, cfg.GetGateway().GetApiKeys(), log)
	// The gateway shares the API listener unless config says otherwise
	var shared *gateway.Gateway
	if cfg.GetGateway().GetListen() == "" {
		shared = d.Gateway
	}
	var ui http.Handler
	if !cfg.GetWeb().GetDisabled() {
		ui = web.Handler()
	}
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
		Slots:     d.Slots,
		Monitor:   d.Monitor,
		Gateway:   shared,
		Events:    bus,
		Snapshot:  d.snapshot,
		Web:       ui,
		Token:     cfg.GetAuth().GetToken(),
		Log:       log,
	})
	return d, nil
}

// Produces the current state of requested kinds as created events
func (d *Daemon) snapshot(ctx context.Context, kinds []v1.EventKind) []*v1.Event {
	want := map[v1.EventKind]bool{}
	for _, k := range kinds {
		want[k] = true
	}
	all := len(kinds) == 0
	var out []*v1.Event
	add := func(kind v1.EventKind, id string, payload any) {
		if !all && !want[kind] {
			return
		}
		ev := &v1.Event{Kind: kind, Action: v1.EventAction_EVENT_ACTION_CREATED, Id: id, At: timestamppb.Now()}
		switch p := payload.(type) {
		case *v1.HostProfile:
			ev.Payload = &v1.Event_Host{Host: p}
		case *v1.Task:
			ev.Payload = &v1.Event_Task{Task: p}
		case *v1.Instance:
			ev.Payload = &v1.Event_Instance{Instance: p}
		case *v1.Slot:
			ev.Payload = &v1.Event_Slot{Slot: p}
		case *v1.Route:
			ev.Payload = &v1.Event_Route{Route: p}
		case *v1.Install:
			ev.Payload = &v1.Event_Install{Install: p}
		case *v1.Build:
			ev.Payload = &v1.Event_Build{Build: p}
		case *v1.StoredModel:
			ev.Payload = &v1.Event_Model{Model: p}
		case *v1.Watch:
			ev.Payload = &v1.Event_Watch{Watch: p}
		case *v1.Finding:
			ev.Payload = &v1.Event_Finding{Finding: p}
		}
		out = append(out, ev)
	}
	if all || want[v1.EventKind_EVENT_KIND_HOST] {
		if profile, err := d.Host.Profile(ctx, false); err == nil {
			add(v1.EventKind_EVENT_KIND_HOST, profile.GetHostname(), profile)
		}
	}
	for _, t := range d.Tasks.List(false) {
		add(v1.EventKind_EVENT_KIND_TASK, t.GetId(), t)
	}
	for _, in := range d.Instances.List(false) {
		add(v1.EventKind_EVENT_KIND_INSTANCE, in.GetId(), in)
	}
	for _, s := range d.Slots.List() {
		add(v1.EventKind_EVENT_KIND_SLOT, s.GetId(), s)
	}
	for _, r := range d.Routes.List() {
		add(v1.EventKind_EVENT_KIND_ROUTE, r.GetName(), r)
	}
	if list, err := d.Installs.List(ctx, ""); err == nil {
		for _, in := range list {
			add(v1.EventKind_EVENT_KIND_INSTALL, in.GetId(), in)
		}
	}
	if list, err := d.Installs.ListBuilds(ctx, ""); err == nil {
		for _, b := range list {
			add(v1.EventKind_EVENT_KIND_BUILD, b.GetId(), b)
		}
	}
	if list, err := d.Store.ListManifests(); err == nil {
		for _, m := range list {
			add(v1.EventKind_EVENT_KIND_MODEL, store.Key(m.GetSourceId(), m.GetRepo(), m.GetGroup()), m)
		}
	}
	for _, w := range d.Monitor.List() {
		add(v1.EventKind_EVENT_KIND_WATCH, w.GetId(), w)
	}
	if list, err := d.Monitor.Findings(ctx, "", true); err == nil {
		for _, f := range list {
			add(v1.EventKind_EVENT_KIND_FINDING, f.GetId(), f)
		}
	}
	return out
}

// Stops instances and background tasks, then closes the store
func (d *Daemon) Close() {
	d.closeOnce.Do(func() {
		d.Instances.Close()
		d.cancel()
		drain, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		d.Tasks.Drain(drain)
		if err := d.DB.Close(); err != nil {
			d.Log.Warn("store close failed", "err", err)
		}
	})
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

// Recovers state, then serves the API and gateway listeners
func (d *Daemon) Serve(ctx context.Context, ln, gatewayLn net.Listener) error {
	d.addr = ln.Addr().String()
	if err := d.Tasks.Recover(ctx); err != nil {
		ln.Close()
		if gatewayLn != nil {
			gatewayLn.Close()
		}
		return err
	}
	if err := d.Installs.RecoverBuilds(ctx); err != nil {
		ln.Close()
		if gatewayLn != nil {
			gatewayLn.Close()
		}
		return err
	}
	if err := d.Instances.Recover(ctx); err != nil {
		ln.Close()
		if gatewayLn != nil {
			gatewayLn.Close()
		}
		return err
	}
	if err := d.Slots.Recover(ctx); err != nil {
		ln.Close()
		if gatewayLn != nil {
			gatewayLn.Close()
		}
		return err
	}
	go d.Monitor.Run(ctx)
	servers := []*http.Server{{Handler: d.handler, ReadHeaderTimeout: readHeaderTimeout}}
	listeners := []net.Listener{ln}
	d.Gateway.SetListeners([]*v1.Listener{{Addr: d.addr, Shared: true}})
	if gatewayLn != nil {
		servers = append(servers, &http.Server{Handler: d.Gateway.Handler(), ReadHeaderTimeout: readHeaderTimeout})
		listeners = append(listeners, gatewayLn)
		d.Gateway.SetListeners([]*v1.Listener{{Addr: gatewayLn.Addr().String()}})
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
