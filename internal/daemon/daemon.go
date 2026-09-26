// Package daemon wires every manager and serves the API.
package daemon

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/nickheyer/nebu/internal/auth"
	"github.com/nickheyer/nebu/internal/bots"
	"github.com/nickheyer/nebu/internal/calibrate"
	"github.com/nickheyer/nebu/internal/chats"
	"github.com/nickheyer/nebu/internal/db"
	"github.com/nickheyer/nebu/internal/doctor"
	"github.com/nickheyer/nebu/internal/formations"
	"github.com/nickheyer/nebu/internal/gateway"
	"github.com/nickheyer/nebu/internal/inspect"
	"github.com/nickheyer/nebu/internal/installs"
	"github.com/nickheyer/nebu/internal/instances"
	"github.com/nickheyer/nebu/internal/mesh"
	"github.com/nickheyer/nebu/internal/notify"
	"github.com/nickheyer/nebu/internal/pull"
	"github.com/nickheyer/nebu/internal/rpc"
	"github.com/nickheyer/nebu/internal/settings"
	"github.com/nickheyer/nebu/internal/slots"
	"github.com/nickheyer/nebu/internal/sso"
	"github.com/nickheyer/nebu/internal/tasks"
	"github.com/nickheyer/nebu/pkg/archs"
	"github.com/nickheyer/nebu/pkg/build"
	"github.com/nickheyer/nebu/pkg/cache"
	"github.com/nickheyer/nebu/pkg/config"
	"github.com/nickheyer/nebu/pkg/descriptor"
	"github.com/nickheyer/nebu/pkg/events"
	formatsall "github.com/nickheyer/nebu/pkg/formats/all"
	"github.com/nickheyer/nebu/pkg/host"
	"github.com/nickheyer/nebu/pkg/launch"
	"github.com/nickheyer/nebu/pkg/perf"
	"github.com/nickheyer/nebu/pkg/precision"
	"github.com/nickheyer/nebu/pkg/probes"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/recipes"
	"github.com/nickheyer/nebu/pkg/runtimes"
	"github.com/nickheyer/nebu/pkg/sources"
	"github.com/nickheyer/nebu/pkg/store"
	"github.com/nickheyer/nebu/pkg/transfer"
	web "github.com/nickheyer/nebu/web/nebu"
	"google.golang.org/protobuf/proto"
)

// Daemon version, supplied by CLI build info.
var Version = "dev"

const (
	profileTTL        = 30 * time.Second
	shutdownTimeout   = 10 * time.Second
	readHeaderTimeout = 10 * time.Second
	// Seals single sign-on cookies, kept in the data directory
	sessionKeyFile = "session.key"
)

// Daemon managers and API handler.
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
	Bots      *bots.Manager
	// Saved web chat conversations
	Chats *chats.Manager
	// Browser sessions, nil when auth.disabled is set
	Sessions *auth.Sessions
	// Local accounts, nil when single sign-on replaces them or auth is off
	Users *auth.Users
	// API tokens users make, nil when auth is off
	Tokens *auth.Tokens
	// Checks every API and gateway request's credential
	Guard *auth.Guard
	// Single sign-on, nil when auth.oidc is unset
	SSO *sso.Service
	// The mesh this node belongs to, the conductor of formations, and the numbers they learn
	Mesh       *mesh.Manager
	Formations *formations.Manager
	Perf       *perf.Table
	Log        *slog.Logger
	tls        *tls.Config
	handler    http.Handler
	cancel     context.CancelFunc
	closeOnce  sync.Once
	// The notifier, waited for on close
	background sync.WaitGroup
	base       context.Context
	addr       string
	secure     bool
}

// Builds every manager from config
func New(cfg *v1.Config, log *slog.Logger, recent *launch.Log) (d *Daemon, err error) {
	for _, dir := range []string{cfg.GetDataDir(), cfg.GetCacheDir(), cfg.GetStoreDir()} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	prober := host.New(probes.All(), []string{cfg.GetStoreDir(), cfg.GetDataDir(), cfg.GetCacheDir()}, profileTTL)
	fmts, err := formatsall.Registry()
	if err != nil {
		return nil, err
	}
	families, err := archs.New(archs.All())
	if err != nil {
		return nil, err
	}
	builder := &descriptor.Builder{Formats: fmts, Archs: families, Scale: precision.Bits{}}
	rts, err := runtimes.New(runtimes.All())
	if err != nil {
		return nil, err
	}
	recipeBook, err := build.New(recipes.All())
	if err != nil {
		return nil, err
	}
	for _, rt := range rts.List() {
		for _, id := range runtimes.RecipeIDs(rt) {
			rc, err := recipeBook.Get(id)
			if err != nil {
				return nil, fmt.Errorf("runtime %s: %w", rt.ID(), err)
			}
			if rc.RuntimeID() != rt.ID() {
				return nil, fmt.Errorf("runtime %s: recipe %s builds %s", rt.ID(), id, rc.RuntimeID())
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
	// Close the store if initialization fails.
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
	// Seed source rows from defaults and config, then load the registry from storage.
	srcMgr := sources.NewManager(store, bus, log, filepath.Join(cfg.GetCacheDir(), "sources"))
	if err = srcMgr.Load(context.Background(), cfg.GetSources()); err != nil {
		return nil, err
	}
	srcs := srcMgr.Registry
	base, cancel := context.WithCancel(context.Background())
	// Publish probe results to update UI device meters.
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
	if d.tls, err = d.tlsConfig(); err != nil {
		return nil, err
	}
	// Load settings and subscribe to updates.
	d.Settings = &settings.Manager{DB: store, Events: bus}
	if err = d.Settings.Load(context.Background()); err != nil {
		return nil, err
	}
	calibration, err := calibrate.Open(context.Background(), store)
	if err != nil {
		return nil, err
	}
	// Use the same calibration for estimates and launches.
	d.Inspector = &inspect.Inspector{
		Sources:     srcs,
		Formats:     fmts,
		Builder:     builder,
		Runtimes:    rts,
		Host:        prober,
		Cache:       cacheStore,
		Calibration: calibration,
		Contexts:    cfg.GetContexts(),
		Log:         log,
		Stored:      blobStore.ListManifests,
	}
	d.Puller = &pull.Puller{Inspector: d.Inspector, Store: blobStore, Fetcher: fetcher, Tasks: d.Tasks, Events: bus, MinFree: cfg.GetMinFreeBytes()}
	d.Installs = &installs.Manager{
		DB:          store,
		RuntimesDir: filepath.Join(cfg.GetDataDir(), "runtimes"),
		Runtimes:    rts,
		Recipes:     recipeBook,
		Engine:      &build.Engine{Root: cfg.GetBuilds().GetDir(), Jobs: int(cfg.GetBuilds().GetJobs()), Sources: srcs, Fetcher: fetcher, Log: log},
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
	d.Routes.SetHandoffs(handoffOf(rts))
	d.Routes.Follow(base)
	d.Instances = &instances.Manager{
		DB:          store,
		Dir:         filepath.Join(cfg.GetDataDir(), "instances"),
		Store:       blobStore,
		Runtimes:    rts,
		Installs:    d.Installs,
		Inspector:   d.Inspector,
		Launcher:    &launch.ProcessLauncher{Log: log},
		Tasks:       d.Tasks,
		Host:        prober,
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
	// Protect running models and slot relaunch targets from eviction.
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
	if err = d.openAuth(store); err != nil {
		return nil, err
	}
	d.Gateway.SetCredentials(d.Guard)
	if err = d.openMesh(context.Background(), store, prober, rts, blobStore, calibration, bus, log); err != nil {
		return nil, err
	}
	// Load bots now and connect enabled bots when serving starts.
	d.Bots = bots.New(base, store, d.Gateway, bus, log, cfg.GetDiscord().GetFfmpeg())
	if err = d.Bots.Load(context.Background()); err != nil {
		return nil, err
	}
	d.Chats = &chats.Manager{DB: store, Log: log}
	var ui http.Handler
	if !cfg.GetWeb().GetDisabled() {
		ui = web.Handler()
	}
	d.Doctor = &doctor.Doctor{Host: prober, Runtimes: rts, Sources: srcs, Store: blobStore, Installs: d.Installs, Tasks: d.Tasks, MinFree: cfg.GetMinFreeBytes()}
	deps := rpc.Deps{
		Host:      prober,
		Doctor:    d.Doctor,
		Settings:  d.Settings,
		Sources:   srcMgr,
		Formats:   fmts,
		Runtimes:  rts,
		Inspector: d.Inspector,
		Store:     blobStore,
		Puller:    d.Puller,
		Tasks:     d.Tasks,
		Installs:  d.Installs,
		Instances: d.Instances,
		Slots:     d.Slots,
		Gateway:   d.Gateway,
		Bots:      d.Bots,
		Chats:     d.Chats,
		// The gateway shares the API listener unless config gives it one
		GatewayShared: cfg.GetGateway().GetListen() == "",
		Events:        bus,
		Snapshot:      d.snapshot,
		Web:           ui,
		Guard:         d.Guard,
		Sessions:      d.Sessions,
		Users:         d.Users,
		Tokens:        d.Tokens,
		SSO:           d.SSO,
		Recent:        recent,
		Log:           log,
		Mesh:          d.Mesh,
		Formations:    d.Formations,
		Perf:          d.Perf,
		CacheDir:      d.Formations.CacheDir,
	}
	d.handler = rpc.NewHandler(deps)
	// Node traffic gets the same services with the gateway mounted, whatever the gateway listens on.
	meshDeps := deps
	meshDeps.GatewayShared, meshDeps.Web = true, nil
	d.Mesh.Handler = rpc.NewHandler(meshDeps)
	return d, nil
}

// Opens the mesh identity, the conductor, and the learned tables, wiring them to the puller and gateway
func (d *Daemon) openMesh(ctx context.Context, store *db.DB, prober *host.Prober, rts *runtimes.Registry, blobStore *store.Store, calibration *calibrate.Table, bus *events.Bus, log *slog.Logger) error {
	table, err := perf.Open(ctx, store)
	if err != nil {
		return err
	}
	d.Perf = table
	d.Mesh = &mesh.Manager{
		DB:        store,
		Config:    d.Config.GetMesh(),
		Host:      prober,
		Installs:  d.Installs,
		Runtimes:  rts,
		Store:     blobStore,
		Perf:      table,
		Routes:    d.Routes,
		Settings:  d.Settings,
		Events:    bus,
		Guard:     d.Guard,
		Log:       log,
		Version:   Version,
		APIListen: d.Config.GetListen(),
		APITLS:    d.tls,
	}
	if err := d.Mesh.Open(ctx); err != nil {
		return err
	}
	d.Formations = &formations.Manager{
		DB:           store,
		Mesh:         d.Mesh,
		Instances:    d.Instances,
		Routes:       d.Routes,
		Tasks:        d.Tasks,
		Inspector:    d.Inspector,
		Runtimes:     rts,
		Installs:     d.Installs,
		Store:        blobStore,
		Puller:       d.Puller,
		Perf:         table,
		Calibration:  calibration,
		Events:       bus,
		Log:          log,
		CacheDir:     filepath.Join(d.Config.GetCacheDir(), "rpc"),
		SlotView:     d.Slots,
		Traces:       d.Gateway.Traces(),
		Gateway:      d.Gateway,
		DrainTimeout: time.Duration(d.Config.GetGateway().GetDrainTimeoutMs()) * time.Millisecond,
	}
	if err := d.Formations.Open(ctx); err != nil {
		return err
	}
	d.Mesh.Formations = d.Formations
	d.Slots.Formations = d.Formations
	d.Instances.Formations = d.Formations
	d.Instances.MeshPorts = d.Mesh.Ports
	blobStore.CacheDir, blobStore.KeepCache = d.Formations.CacheDir, d.Formations.Live
	d.Puller.Mesh = mesh.Source{Mesh: d.Mesh}
	d.Gateway.SetPeers(d.Formations)
	keep := d.Puller.Keep
	d.Puller.Keep = func(m *v1.StoredModel) bool {
		if keep(m) {
			return true
		}
		for _, f := range d.Formations.List(true) {
			if f.GetSourceId() == m.GetSourceId() && f.GetRepo() == m.GetRepo() && f.GetGroup() == m.GetGroup() {
				return true
			}
		}
		return false
	}
	return nil
}

// Sets up sessions, the daemon token, user API tokens, and accounts or single sign-on.
// With auth.disabled the guard passes everything.
func (d *Daemon) openAuth(store *db.DB) error {
	cfg, a := d.Config, d.Config.GetAuth()
	if a.GetDisabled() {
		if a.GetToken() != "" || a.GetOidc().GetIssuer() != "" {
			d.Log.Warn("auth.disabled is set, auth.token and auth.oidc are ignored")
		}
		d.Guard = auth.NewGuard("", nil, nil)
		return nil
	}
	ttl, err := time.ParseDuration(a.GetSessionTtl())
	if err != nil || ttl <= 0 {
		return fmt.Errorf("auth.session_ttl %q must be a positive duration such as 24h", a.GetSessionTtl())
	}
	if d.Sessions, err = auth.OpenSessions(filepath.Join(cfg.GetDataDir(), sessionKeyFile), ttl); err != nil {
		return err
	}
	if a.GetToken() == "" {
		path := filepath.Join(cfg.GetDataDir(), config.TokenFile)
		token, created, err := auth.LoadOrCreateToken(path)
		if err != nil {
			return err
		}
		cfg.Auth.Token = token
		if created {
			d.Log.Info("api token created for CLI and SDK clients", "file", path)
		}
	}
	if oidc := a.GetOidc(); oidc.GetIssuer() != "" {
		if d.SSO, err = sso.New(oidc, d.Sessions, d.Log); err != nil {
			return err
		}
		d.Log.Info("single sign-on enabled", "issuer", oidc.GetIssuer(), "name", oidc.GetName())
	} else {
		d.Users = auth.NewUsers(store, d.Log)
		if err := d.Users.Load(context.Background()); err != nil {
			return err
		}
		d.Sessions.Validate(d.Users.Valid)
	}
	d.Tokens = auth.NewTokens(store, d.Log)
	d.Guard = auth.NewGuard(a.GetToken(), d.Sessions, d.Tokens)
	return nil
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
	if d.Mesh.Joined() {
		for _, n := range d.Mesh.Nodes() {
			add(v1.EventKind_EVENT_KIND_NODE, n.GetId(), n)
		}
	}
	add(v1.EventKind_EVENT_KIND_MESH, "mesh", d.Mesh.Status())
	for _, f := range d.Formations.List(false) {
		add(v1.EventKind_EVENT_KIND_FORMATION, f.GetId(), f)
	}
	for _, b := range d.Bots.List() {
		add(v1.EventKind_EVENT_KIND_BOT, b.GetId(), b)
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
		d.Bots.Close()
		d.Formations.Close()
		d.Instances.Close()
		d.Mesh.Close()
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
	tlsConfig := d.tls
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

// Warns about public listeners without TLS or authentication.
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
			d.Log.Warn("public HTTP listener. Configure tls.cert_file and tls.key_file or a reverse proxy", "addr", addr)
		}
		if addr == d.Config.GetListen() && d.Sessions == nil {
			d.Log.Warn("public API listener with auth.disabled. Anyone with network access can control the daemon", "addr", addr)
		}
		if addr == d.Config.GetListen() && d.Users != nil && d.Users.Count() == 0 {
			d.Log.Warn("no account exists yet, the first visitor to the web UI creates it", "addr", addr)
		}
		shared := d.Config.GetGateway().GetListen() == "" && addr == d.Config.GetListen()
		if (addr == d.Config.GetGateway().GetListen() || shared) && len(d.Config.GetGateway().GetApiKeys()) == 0 && d.Sessions == nil {
			d.Log.Warn("the gateway listens beyond loopback with auth.disabled and no gateway.api_keys. Anyone with network access can run models", "addr", addr)
		}
	}
}

// Recovers state, then serves the API and gateway listeners
func (d *Daemon) Serve(ctx context.Context, ln, gatewayLn net.Listener) error {
	d.addr = ln.Addr().String()
	for _, recover := range []func(context.Context) error{d.Tasks.Recover, d.Installs.RecoverBuilds, d.Instances.Recover, d.Slots.Recover, d.Formations.Recover, d.Bots.Recover} {
		if err := recover(ctx); err != nil {
			ln.Close()
			if gatewayLn != nil {
				gatewayLn.Close()
			}
			return err
		}
	}
	// Aliases of instances outside slots outlive a restart, and formation routes follow their records
	for _, r := range d.Routes.Prune(func(r *v1.Route) bool {
		if r.GetSlotId() != "" || r.GetFormationId() != "" {
			return true
		}
		in, err := d.Instances.Get(r.GetInstanceId())
		return err == nil && !instances.Terminal(in.GetState())
	}) {
		d.Log.Info("dropped route of an instance that did not survive the restart", "route", r.GetName(), "instance", r.GetInstanceId())
	}
	// Keep the notifier active for the daemon's lifetime.
	d.background.Add(1)
	go func() {
		defer d.background.Done()
		d.Notifier.Run(d.base)
	}()
	// Backfill model kinds for older stored descriptors.
	d.background.Add(1)
	go func() {
		defer d.background.Done()
		d.Instances.RefreshDescriptors(d.base)
	}()
	// Run a host check at startup.
	if _, err := d.Doctor.Start(ctx); err != nil {
		d.Log.Warn("host check failed to start", "err", err)
	}
	// Join the mesh's loops and watch formations for the daemon's lifetime.
	if err := d.Mesh.Start(d.base); err != nil {
		ln.Close()
		if gatewayLn != nil {
			gatewayLn.Close()
		}
		return err
	}
	d.Formations.Start()
	go d.Installs.Refresh(d.base)
	// Cancel request contexts when serving stops.
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
	// Stop requests and streams before listeners and managers.
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

// Names each runtime's relay handoff from its shape facts, for the gateway's relay routes
func handoffOf(reg *runtimes.Registry) func(runtimeID string) string {
	return func(runtimeID string) string {
		rt, err := reg.Get(runtimeID)
		if err != nil {
			return ""
		}
		if p := rt.Policy(); p != nil && p.Shapes != nil {
			return p.Shapes.Handoff
		}
		return ""
	}
}
