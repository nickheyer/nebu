// Package daemon wires every manager and serves the API.
package daemon

import (
	"context"
	"crypto/tls"
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
	"github.com/nickheyer/nebu/internal/notify"
	"github.com/nickheyer/nebu/internal/pull"
	"github.com/nickheyer/nebu/internal/rpc"
	"github.com/nickheyer/nebu/internal/settings"
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
	"github.com/nickheyer/nebu/pkg/spec"
	"github.com/nickheyer/nebu/pkg/store"
	"github.com/nickheyer/nebu/pkg/transfer"
	"github.com/nickheyer/nebu/pkg/triage"
	specfs "github.com/nickheyer/nebu/spec"
	web "github.com/nickheyer/nebu/web/nebu"
	"google.golang.org/protobuf/proto"
)

// What the daemon reports as its version, set by the CLI from build info
var Version = "dev"

const (
	profileTTL        = 30 * time.Second
	shutdownTimeout   = 10 * time.Second
	readHeaderTimeout = 10 * time.Second
)

// Running set of managers behind one handler
type Daemon struct {
	Config    *v1.Config
	DB        *db.DB
	Host      *host.Prober
	Settings  *settings.Manager
	Sources   *sources.Manager
	Inspector *inspect.Inspector
	Doctor    *doctor.Doctor
	Store     *store.Store
	Tasks     *tasks.Manager
	Puller    *pull.Puller
	Installs  *installs.Manager
	Instances *instances.Manager
	Slots     *slots.Manager
	Notifier  *notify.Notifier
	Gateway   *gateway.Gateway
	Routes    *gateway.Table
	Log       *slog.Logger
	handler   http.Handler
	cancel    context.CancelFunc
	closeOnce sync.Once
	// The notifier, waited for on close
	background sync.WaitGroup
	base       context.Context
	addr       string
	secure     bool
}

// Builds every manager from config
func New(cfg *v1.Config, log *slog.Logger) (d *Daemon, err error) {
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
	classifier, err := formats.NewClassifier(catalog.Formats)
	if err != nil {
		return nil, err
	}
	readers, err := formats.BuildReaders(catalog.Formats, formatsall.Constructors())
	if err != nil {
		return nil, err
	}
	builder, err := descriptor.New(catalog.Formats, catalog.Archs, catalog.Precisions)
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
		for _, id := range rt.RecipeIDs() {
			if _, err := recipes.Get(id); err != nil {
				return nil, fmt.Errorf("runtime %s: %w", rt.Manifest.GetId(), err)
			}
		}
	}
	matcher, err := triage.New(catalog.Triage)
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
	blobStore.MaxBytes = cfg.GetStore().GetMaxBytes()
	tr := cfg.GetTransfer()
	fetcher := transfer.New(int(tr.GetWorkers()), int64(tr.GetChunkBytes()), int(tr.GetRetries()), log)
	if fetcher.Schedule, err = transfer.NewSchedule(tr.GetMaxBytesPerSecond(), tr.GetWindows()); err != nil {
		return nil, fmt.Errorf("transfer windows: %w", err)
	}
	store, err := db.Open(filepath.Join(cfg.GetDataDir(), "nebu.db"))
	if err != nil {
		return nil, err
	}
	// Every failure past this point closes the store on the way out
	defer func() {
		if err != nil {
			store.Close()
		}
	}()
	if store.SetAside != "" {
		log.Warn("a database from before the atlas migrations was set aside and a fresh one started", "copy", store.SetAside)
	}
	if store.Baselined != "" {
		log.Warn("database revisions no longer matched the migration directory", "baseline", store.Baselined)
	}
	for _, line := range store.Drift {
		log.Warn("schema drift, the database and schema.sql disagree, run make migrate-reset", "change", line)
	}
	bus := events.New()
	// Seeded defaults and config entries become rows here, the registry follows the rows from then on
	srcMgr := sources.NewManager(store, bus, log, filepath.Join(cfg.GetCacheDir(), "sources"))
	if err = srcMgr.Load(context.Background(), cfg.GetSources()); err != nil {
		return nil, err
	}
	srcs := srcMgr.Registry
	base, cancel := context.WithCancel(context.Background())
	// Every re-probe reaches the UI, so device meters follow launches and stops
	prober.OnProbe = func(profile *v1.HostProfile) {
		bus.Publish(v1.EventKind_EVENT_KIND_HOST, v1.EventAction_EVENT_ACTION_UPDATED, profile.GetHostname(), profile)
	}
	d = &Daemon{
		Config:  cfg,
		DB:      store,
		Host:    prober,
		Sources: srcMgr,
		Store:   blobStore,
		Tasks:   tasks.New(base, log, store, bus),
		Log:     log,
		cancel:  cancel,
		base:    base,
	}
	// Settings are rows, loaded once and followed through events
	d.Settings = &settings.Manager{DB: store, Events: bus}
	if err = d.Settings.Load(context.Background()); err != nil {
		return nil, err
	}
	calibration, err := calibrate.Open(context.Background(), store)
	if err != nil {
		return nil, err
	}
	// Every plan, the fit table's and a run's, applies the same learned correction
	d.Inspector = &inspect.Inspector{
		Sources:     srcs,
		Classifier:  classifier,
		Readers:     readers,
		Builder:     builder,
		Runtimes:    runtimes,
		Host:        prober,
		Cache:       cacheStore,
		Calibration: calibration,
		Contexts:    cfg.GetContexts(),
		Log:         log,
	}
	d.Puller = &pull.Puller{Inspector: d.Inspector, Store: blobStore, Fetcher: fetcher, Tasks: d.Tasks, Events: bus, MinFree: cfg.GetMinFreeBytes()}
	d.Installs = &installs.Manager{
		DB:          store,
		RuntimesDir: filepath.Join(cfg.GetDataDir(), "runtimes"),
		Runtimes:    runtimes,
		Recipes:     recipes,
		Engine:      &build.Engine{Root: cfg.GetBuilds().GetDir(), Patches: catalog.Patches, Jobs: int(cfg.GetBuilds().GetJobs()), Sources: srcs, Fetcher: fetcher, Log: log},
		Defaults:    cfg.GetBuilds(),
		Host:        prober,
		Tasks:       d.Tasks,
		Fetcher:     fetcher,
		Sources:     srcs,
		Events:      bus,
		Log:         log,
	}
	d.Inspector.Installed = func(runtimeID string) bool {
		list, err := d.Installs.List(context.Background(), runtimeID)
		return err == nil && len(list) > 0
	}
	if d.Routes, err = gateway.OpenTable(context.Background(), store, bus, log); err != nil {
		return nil, err
	}
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
		Routes:      d.Routes,
		Events:      bus,
		Log:         log,
	}
	drain := time.Duration(cfg.GetGateway().GetDrainTimeoutMs()) * time.Millisecond
	d.Slots = &slots.Manager{DB: store, Instances: d.Instances, Routes: d.Routes, Tasks: d.Tasks, Host: prober, Events: bus, DrainTimeout: drain, Log: log}
	if err = d.Slots.Load(context.Background()); err != nil {
		return nil, err
	}
	d.Instances.Slots = d.Slots
	d.Inspector.Constrain = d.Instances
	// Eviction spares what is running and what a slot would relaunch
	d.Puller.Keep = func(m *v1.StoredModel) bool {
		same := func(source, repo, group string) bool {
			return source == m.GetSourceId() && repo == m.GetRepo() && group == m.GetGroup()
		}
		for _, in := range d.Instances.List(true) {
			if same(in.GetSourceId(), in.GetRepo(), in.GetGroup()) {
				return true
			}
		}
		for _, s := range d.Slots.List() {
			if r := s.GetRequest(); r != nil && same(r.GetSourceId(), r.GetRepo(), r.GetGroup()) {
				return true
			}
		}
		return false
	}
	d.Notifier = &notify.Notifier{Webhooks: cfg.GetNotify().GetWebhooks(), Events: bus, Log: log}
	d.Gateway = gateway.New(d.Routes, cfg.GetGateway().GetApiKeys(), cfg.GetGateway().GetCorsOrigins(), cfg.GetGateway().GetPolicy(), bus, log)
	d.Gateway.SetVersion(Version)
	var ui http.Handler
	if !cfg.GetWeb().GetDisabled() {
		ui = web.Handler()
	}
	d.Doctor = &doctor.Doctor{Host: prober, Runtimes: runtimes, Sources: srcs, Store: blobStore, Installs: d.Installs, Tasks: d.Tasks, MinFree: cfg.GetMinFreeBytes()}
	d.handler = rpc.NewHandler(rpc.Deps{
		Host:      prober,
		Doctor:    d.Doctor,
		Settings:  d.Settings,
		Sources:   srcMgr,
		Formats:   classifier.Specs(),
		Runtimes:  runtimes,
		Inspector: d.Inspector,
		Store:     blobStore,
		Puller:    d.Puller,
		Tasks:     d.Tasks,
		Installs:  d.Installs,
		Instances: d.Instances,
		Slots:     d.Slots,
		Gateway:   d.Gateway,
		// The gateway shares the API listener unless config gives it one
		GatewayShared: cfg.GetGateway().GetListen() == "",
		Events:        bus,
		Snapshot:      d.snapshot,
		Web:           ui,
		Token:         cfg.GetAuth().GetToken(),
		Log:           log,
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
	add := func(kind v1.EventKind, id string, record proto.Message) {
		if all || want[kind] {
			out = append(out, events.Event(kind, v1.EventAction_EVENT_ACTION_CREATED, id, record))
		}
	}
	if all || want[v1.EventKind_EVENT_KIND_HOST] {
		if profile, err := d.Host.Profile(ctx, false); err == nil {
			add(v1.EventKind_EVENT_KIND_HOST, profile.GetHostname(), profile)
		}
	}
	add(v1.EventKind_EVENT_KIND_SETTINGS, settings.ID, d.Settings.Get())
	for _, s := range d.Sources.List() {
		add(v1.EventKind_EVENT_KIND_SOURCE, s.GetId(), s)
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
	if st, err := d.Store.Status(); err == nil {
		add(v1.EventKind_EVENT_KIND_STORE, st.GetPath(), st)
	}
	return out
}

// Stops instances and background tasks, then closes the store
func (d *Daemon) Close() {
	d.closeOnce.Do(func() {
		d.Instances.Close()
		d.cancel()
		d.background.Wait()
		drain, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		d.Tasks.Drain(drain)
		if err := d.DB.Close(); err != nil {
			d.Log.Warn("store close failed", "err", err)
		}
	})
}

// Returns the API handler for in process or network use
func (d *Daemon) Handler() http.Handler { return d.handler }

// Listens on the configured addresses until ctx ends, over TLS when a certificate is configured
func (d *Daemon) ListenAndServe(ctx context.Context) error {
	tlsConfig, err := d.tlsConfig()
	if err != nil {
		return err
	}
	listen := func(addr string) (net.Listener, error) {
		ln, err := net.Listen("tcp", addr)
		if err != nil || tlsConfig == nil {
			return ln, err
		}
		return tls.NewListener(ln, tlsConfig), nil
	}
	ln, err := listen(d.Config.GetListen())
	if err != nil {
		return err
	}
	var gatewayLn net.Listener
	if addr := d.Config.GetGateway().GetListen(); addr != "" {
		if gatewayLn, err = listen(addr); err != nil {
			ln.Close()
			return err
		}
	}
	d.secure = tlsConfig != nil
	d.warnExposure(d.secure)
	return d.Serve(ctx, ln, gatewayLn)
}

// Loads the configured certificate, nil for plain HTTP
func (d *Daemon) tlsConfig() (*tls.Config, error) {
	t := d.Config.GetTls()
	if t.GetCertFile() == "" && t.GetKeyFile() == "" {
		return nil, nil
	}
	cert, err := tls.LoadX509KeyPair(t.GetCertFile(), t.GetKeyFile())
	if err != nil {
		return nil, fmt.Errorf("tls: %w", err)
	}
	return &tls.Config{Certificates: []tls.Certificate{cert}, NextProtos: []string{"h2", "http/1.1"}, MinVersion: tls.VersionTLS12}, nil
}

// Says so when a listener reaches beyond this machine without TLS or a token
func (d *Daemon) warnExposure(secure bool) {
	for _, addr := range []string{d.Config.GetListen(), d.Config.GetGateway().GetListen()} {
		host, _, err := net.SplitHostPort(addr)
		if addr == "" || err != nil {
			continue
		}
		ip := net.ParseIP(host)
		if host == "localhost" || ip != nil && ip.IsLoopback() {
			continue
		}
		if !secure {
			d.Log.Warn("listening beyond loopback over plain http, set tls.cert_file and tls.key_file or front it with a reverse proxy", "addr", addr)
		}
		if addr == d.Config.GetListen() && d.Config.GetAuth().GetToken() == "" {
			d.Log.Warn("the api listens beyond loopback with no auth.token, anyone who can reach it controls this daemon", "addr", addr)
		}
		shared := d.Config.GetGateway().GetListen() == "" && addr == d.Config.GetListen()
		if (addr == d.Config.GetGateway().GetListen() || shared) && len(d.Config.GetGateway().GetApiKeys()) == 0 {
			d.Log.Warn("the gateway listens beyond loopback with no gateway.api_keys", "addr", addr)
		}
	}
}

// Recovers state, then serves the API and gateway listeners
func (d *Daemon) Serve(ctx context.Context, ln, gatewayLn net.Listener) error {
	d.addr = ln.Addr().String()
	for _, recover := range []func(context.Context) error{d.Tasks.Recover, d.Installs.RecoverBuilds, d.Instances.Recover, d.Slots.Recover} {
		if err := recover(ctx); err != nil {
			ln.Close()
			if gatewayLn != nil {
				gatewayLn.Close()
			}
			return err
		}
	}
	// The notifier lives as long as the daemon, not as long as one Serve call
	d.background.Add(1)
	go func() {
		defer d.background.Done()
		d.Notifier.Run(d.base)
	}()
	// A fresh daemon checks the host once so the tasks list says what needs attention
	if _, err := d.Doctor.Start(ctx); err != nil {
		d.Log.Warn("host check failed to start", "err", err)
	}
	// Request contexts hang off this one so open streams end when serving stops
	requests, endRequests := context.WithCancel(context.Background())
	defer endRequests()
	base := func(net.Listener) context.Context { return requests }
	servers := []*http.Server{{Handler: d.handler, ReadHeaderTimeout: readHeaderTimeout, BaseContext: base}}
	listeners := []net.Listener{ln}
	d.Gateway.SetListeners([]*v1.Listener{{Addr: d.addr, Shared: true}}, d.secure)
	if gatewayLn != nil {
		servers = append(servers, &http.Server{Handler: d.Gateway.Handler(), ReadHeaderTimeout: readHeaderTimeout, BaseContext: base})
		listeners = append(listeners, gatewayLn)
		d.Gateway.SetListeners([]*v1.Listener{{Addr: gatewayLn.Addr().String()}}, d.secure)
		d.Log.Info("gateway listening", "addr", gatewayLn.Addr().String(), "tls", d.secure)
	}
	errCh := make(chan error, len(servers))
	for i, srv := range servers {
		go func(srv *http.Server, ln net.Listener) { errCh <- srv.Serve(ln) }(srv, listeners[i])
	}
	d.Log.Info("listening", "addr", d.addr, "tls", d.secure)
	var result error
	select {
	case <-ctx.Done():
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			result = err
		}
	}
	// Streams and in flight calls end first, then listeners close, then managers stop
	endRequests()
	stop, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	for _, srv := range servers {
		if err := srv.Shutdown(stop); err != nil && !errors.Is(err, context.DeadlineExceeded) && result == nil {
			result = err
		}
	}
	d.Close()
	return result
}
