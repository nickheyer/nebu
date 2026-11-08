// Package daemon wires every manager and serves the API, gateway and metrics listeners
package daemon

import (
	"context"
	"crypto/tls"
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
	"github.com/nickheyer/nebu/internal/mcpserver"
	"github.com/nickheyer/nebu/internal/mesh"
	"github.com/nickheyer/nebu/internal/notify"
	"github.com/nickheyer/nebu/internal/pools"
	"github.com/nickheyer/nebu/internal/pull"
	"github.com/nickheyer/nebu/internal/rpc"
	"github.com/nickheyer/nebu/internal/settings"
	"github.com/nickheyer/nebu/internal/slots"
	"github.com/nickheyer/nebu/internal/sso"
	"github.com/nickheyer/nebu/internal/tasks"
	"github.com/nickheyer/nebu/internal/tools"
	"github.com/nickheyer/nebu/pkg/archs"
	"github.com/nickheyer/nebu/pkg/build"
	"github.com/nickheyer/nebu/pkg/cache"
	"github.com/nickheyer/nebu/pkg/descriptor"
	"github.com/nickheyer/nebu/pkg/events"
	"github.com/nickheyer/nebu/pkg/flowcontrol"
	"github.com/nickheyer/nebu/pkg/formats"
	formatsall "github.com/nickheyer/nebu/pkg/formats/all"
	"github.com/nickheyer/nebu/pkg/health"
	"github.com/nickheyer/nebu/pkg/host"
	"github.com/nickheyer/nebu/pkg/launch"
	"github.com/nickheyer/nebu/pkg/metrics"
	"github.com/nickheyer/nebu/pkg/perf"
	"github.com/nickheyer/nebu/pkg/precision"
	"github.com/nickheyer/nebu/pkg/probes"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/recipes"
	"github.com/nickheyer/nebu/pkg/runtimes"
	"github.com/nickheyer/nebu/pkg/sources"
	"github.com/nickheyer/nebu/pkg/store"
	"github.com/nickheyer/nebu/pkg/telemetry"
	"github.com/nickheyer/nebu/pkg/transfer"
	web "github.com/nickheyer/nebu/web/nebu"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
)

// Daemon version, supplied by CLI build info
var Version = "dev"

const (
	profileTTL        = 30 * time.Second
	liveProbeInterval = 5 * time.Second
	shutdownTimeout   = 10 * time.Second
	readHeaderTimeout = 10 * time.Second
	// How long a kept alive connection may sit between requests before it is closed
	idleTimeout = 2 * time.Minute
	// Seals single sign-on cookies, kept in the data directory
	sessionKeyFile = "session.key"
	// The service name spans are exported under
	serviceName = "nebu"
	// How often queued gateway requests are reconsidered when no release wakes them
	flowTick = 50 * time.Millisecond
	// Waits refused clients are told to take, shed by policy and with no ready endpoint
	retryShed        = time.Second
	retryUnavailable = 5 * time.Second
	// Time between gateway gauge mirrors and gRPC health probes
	gatewayInterval = 5 * time.Second
	// How often the config file is checked for a change
	reloadInterval = 2 * time.Second
)

// Daemon managers and API handler
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
	// Pools, routes, objectives and scheduler configurations every owner publishes into
	Pools   *pools.Manager
	Gateway *gateway.Gateway
	// Prometheus families, liveness and readiness checks, and the tracer provider
	Metrics   *metrics.Registry
	Health    *health.Registry
	Telemetry *telemetry.Provider
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
	// The tool servers Settings hold, run for models by the gateway
	Tools *tools.Registry
	// The mesh this node belongs to, the conductor of formations, and the numbers they learn
	Mesh       *mesh.Manager
	Formations *formations.Manager
	Perf       *perf.Table
	Log        *slog.Logger
	// The level every log line is filtered by, set again on each config reload
	LogLevel *slog.LevelVar
	// The config file a reload reads, the search path when empty
	ConfigPath string
	// Kicks a reload the way SIGHUP does, and how often the file is polled for a change
	reloads     chan struct{}
	reloadEvery time.Duration

	tls      *tls.Config
	handler  http.Handler
	registry *prometheus.Registry
	// Flushes and stops the span exporters
	stopTelemetry func(context.Context) error
	cancel        context.CancelFunc
	closeOnce     sync.Once
	drainOnce     sync.Once
	// The notifier, host watch, descriptor refresh and gateway loops, waited for on close
	background sync.WaitGroup
	base       context.Context
	addr       string
	secure     bool
}

// Builds every manager from config in dependency order, nothing started until Serve
func New(cfg *v1.Config, log *slog.Logger, level *slog.LevelVar, recent *launch.Log) (d *Daemon, err error) {
	for _, dir := range []string{cfg.GetDataDir(), cfg.GetCacheDir(), cfg.GetStoreDir()} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}

	base, cancel := context.WithCancel(context.Background())
	d = &Daemon{Config: cfg, Log: log, LogLevel: level, cancel: cancel, base: base, reloads: make(chan struct{}, 1), reloadEvery: reloadInterval}
	defer func() {
		if err != nil {
			d.abandon()
		}
	}()
	prober := host.New(probes.All(), []host.Use{{Role: "store", Path: cfg.GetStoreDir()}, {Role: "data", Path: cfg.GetDataDir()}, {Role: "cache", Path: cfg.GetCacheDir()}}, profileTTL)
	d.Host = prober
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
	d.Store = blobStore
	tr := cfg.GetTransfer()
	fetcher := transfer.New(int(tr.GetWorkers()), int64(tr.GetChunkBytes()), int(tr.GetRetries()), log)
	if fetcher.Schedule, err = transfer.NewSchedule(tr.GetMaxBytesPerSecond(), tr.GetWindows()); err != nil {
		return nil, fmt.Errorf("transfer windows: %w", err)
	}
	if d.DB, err = db.Open(filepath.Join(cfg.GetDataDir(), "nebu.db"), chats.Upgrade(blobStore)); err != nil {
		return nil, err
	}
	blobStore.Refs = func() ([]string, error) { return d.DB.ChatFileDigests(context.Background()) }
	if d.DB.Upgraded != "" {
		log.Warn("a database from before the atlas migrations was upgraded in place", "upgrade", d.DB.Upgraded)
	}
	if d.DB.Baselined != "" {
		log.Warn("database revisions no longer matched the migration directory", "baseline", d.DB.Baselined)
	}
	for _, line := range d.DB.Drift {
		log.Warn("schema drift, the database and schema.sql disagree, run make migrate-reset", "change", line)
	}
	bus := events.New()
	if err = d.openObservability(); err != nil {
		return nil, err
	}
	// Seed source rows from defaults and config, then load the registry from storage
	d.Sources = sources.NewManager(d.DB, bus, log, filepath.Join(cfg.GetCacheDir(), "sources"))
	if err = d.Sources.Load(context.Background(), cfg.GetSources()); err != nil {
		return nil, err
	}
	srcs := d.Sources.Registry
	// Publish probe results to update UI device meters
	prober.OnProbe = func(profile *v1.HostProfile) {
		bus.Publish(v1.EventKind_EVENT_KIND_HOST, v1.EventAction_EVENT_ACTION_UPDATED, profile.GetHostname(), profile)
	}
	d.Tasks = tasks.New(base, log, d.DB, bus)
	if d.tls, err = d.tlsConfig(); err != nil {
		return nil, err
	}
	d.Settings = &settings.Manager{DB: d.DB, Events: bus}
	if err = d.Settings.Load(context.Background()); err != nil {
		return nil, err
	}
	calibration, err := calibrate.Open(context.Background(), d.DB)
	if err != nil {
		return nil, err
	}
	// Use the same calibration for estimates and launches
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
		DB:          d.DB,
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
	if err = d.openAuth(d.DB); err != nil {
		return nil, err
	}
	// Tool servers follow Settings, and a server a route or persona attaches stays until they let go
	d.Tools = tools.New(log, bus)
	d.Tools.Apply(base, d.Settings.Full().GetToolServers())
	d.Settings.OnUpdate = func(ctx context.Context, s *v1.Settings) { d.Tools.Apply(base, s.GetToolServers()) }
	nodeID, err := mesh.NodeID(context.Background(), d.DB)
	if err != nil {
		return nil, err
	}
	log.Info("node identity", "node", nodeID)
	// The mesh opens after the pools, so its dialer is reached through the daemon at call time
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		return d.Mesh.DialEndpoint(ctx, network, addr)
	}
	if d.Pools, err = pools.New(context.Background(), pools.Options{DB: d.DB, Events: bus, NodeID: nodeID, Log: log, Scrapes: d.Metrics, Dial: dial}); err != nil {
		return nil, err
	}
	drain := time.Duration(cfg.GetGateway().GetDrainTimeoutMs()) * time.Millisecond
	d.Instances = &instances.Manager{
		DB:           d.DB,
		Dir:          filepath.Join(cfg.GetDataDir(), "instances"),
		Store:        blobStore,
		Runtimes:     rts,
		Installs:     d.Installs,
		Inspector:    d.Inspector,
		Launcher:     &launch.ProcessLauncher{Log: log},
		Tasks:        d.Tasks,
		Host:         prober,
		Calibration:  calibration,
		Registrar:    d.Pools,
		DrainTimeout: drain,
		Events:       bus,
		Log:          log,
	}
	d.Slots = &slots.Manager{DB: d.DB, Instances: d.Instances, Registrar: d.Pools, Routes: d.Pools, Tasks: d.Tasks, Host: prober, Events: bus, Log: log}
	if err = d.Slots.Load(context.Background()); err != nil {
		return nil, err
	}
	d.Instances.Slots = d.Slots
	d.Inspector.Constrain = d.Instances
	d.Pools.SetWaker(d.Slots)
	// Protect running models and slot relaunch targets from eviction
	d.Puller.Keep = d.keeps
	d.Notifier = &notify.Notifier{Webhooks: cfg.GetNotify().GetWebhooks(), Events: bus, Log: log}
	if err = d.openMesh(context.Background(), rts, calibration, bus); err != nil {
		return nil, err
	}
	if err = d.openGateway(rts, bus); err != nil {
		return nil, err
	}
	d.Pools.SetIndexer(d.Gateway.Index())
	// Load bots now and connect enabled bots when serving starts
	d.Bots = bots.New(base, d.DB, d.Gateway, d.Pools, bus, log, cfg.GetDiscord().GetFfmpeg())
	d.Bots.Tools = d.Tools
	if err = d.Bots.Load(context.Background()); err != nil {
		return nil, err
	}
	d.Settings.Referenced = d.toolServerUsers
	d.Chats = &chats.Manager{DB: d.DB, Blobs: blobStore, Log: log}
	d.Doctor = &doctor.Doctor{Host: prober, Runtimes: rts, Sources: srcs, Store: blobStore, Installs: d.Installs, Tasks: d.Tasks, MinFree: cfg.GetMinFreeBytes()}
	d.Instances.Probe = reprobe(prober, log)
	prober.Decorate = d.Instances.Attribute
	if err = d.openHandlers(fmts, rts, recent, bus); err != nil {
		return nil, err
	}
	return d, nil
}

// Probes the host again after a runtime exits, the doctor's searches left to its own runs
func reprobe(prober *host.Prober, log *slog.Logger) func() {
	return func() {
		if _, err := prober.Profile(context.Background(), true); err != nil {
			log.Warn("host not probed again after a runtime exit", "err", err)
		}
	}
}

// Builds the metrics registry with process collectors, the health registry and the tracer provider
func (d *Daemon) openObservability() error {
	d.registry = prometheus.NewRegistry()
	for _, c := range []prometheus.Collector{collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{})} {
		if err := d.registry.Register(c); err != nil {
			return err
		}
	}
	var err error
	if d.Metrics, err = metrics.New(d.registry); err != nil {
		return err
	}
	d.Health = health.NewRegistry()
	t := d.Config.GetTelemetry()
	d.Telemetry, d.stopTelemetry, err = telemetry.Setup(context.Background(), telemetry.Config{Endpoint: t.GetEndpoint(), ServiceName: serviceName, Insecure: t.GetInsecure(), SampleRatio: t.GetSampleRatio()})
	if err != nil {
		return fmt.Errorf("telemetry: %w", err)
	}
	return nil
}

// Opens the mesh identity, the conductor, and the learned tables, wiring them to the managers that reach other members
func (d *Daemon) openMesh(ctx context.Context, rts *runtimes.Registry, calibration *calibrate.Table, bus *events.Bus) error {
	table, err := perf.Open(ctx, d.DB)
	if err != nil {
		return err
	}
	table.Log = d.Log
	d.Perf = table
	d.Mesh = &mesh.Manager{
		DB:           d.DB,
		Config:       d.Config.GetMesh(),
		Host:         d.Host,
		Installs:     d.Installs,
		Runtimes:     rts,
		Store:        d.Store,
		Perf:         table,
		Pools:        d.Pools,
		Registrar:    d.Pools,
		Settings:     d.Settings,
		Events:       bus,
		Guard:        d.Guard,
		Log:          d.Log,
		Version:      Version,
		DrainTimeout: time.Duration(d.Config.GetGateway().GetDrainTimeoutMs()) * time.Millisecond,
	}
	if err := d.Mesh.Open(ctx); err != nil {
		return err
	}
	d.Formations = &formations.Manager{
		DB:           d.DB,
		Mesh:         d.Mesh,
		Instances:    d.Instances,
		Registrar:    d.Pools,
		Tasks:        d.Tasks,
		Inspector:    d.Inspector,
		Runtimes:     rts,
		Installs:     d.Installs,
		Store:        d.Store,
		Puller:       d.Puller,
		Perf:         table,
		Calibration:  calibration,
		Events:       bus,
		Log:          d.Log,
		CacheDir:     filepath.Join(d.Config.GetCacheDir(), "rpc"),
		SlotView:     d.Slots,
		DrainTimeout: time.Duration(d.Config.GetGateway().GetDrainTimeoutMs()) * time.Millisecond,
	}
	if err := d.Formations.Open(ctx); err != nil {
		return err
	}
	d.Mesh.Formations = d.Formations
	d.Slots.Formations = d.Formations
	d.Instances.Formations = d.Formations
	d.Instances.MeshPorts = d.Mesh.Ports
	d.Store.CacheDir, d.Store.KeepCache = d.Formations.CacheDir, d.Formations.Live
	d.Puller.Mesh = mesh.Source{Mesh: d.Mesh}
	d.Tools.SetPeers(d.Formations)
	return nil
}

// A flow control quota from its configured limits and window
func quotaOf(q *v1.Quota) flowcontrol.Quota {
	return flowcontrol.Quota{Tokens: int64(q.GetTokens()), Requests: int64(q.GetRequests()), Window: time.Duration(q.GetWindowMs()) * time.Millisecond}
}

// Per fairness id quotas from the configured map, nil when none is set
func quotasOf(qs map[string]*v1.Quota) map[string]flowcontrol.Quota {
	if len(qs) == 0 {
		return nil
	}
	out := make(map[string]flowcontrol.Quota, len(qs))
	for id, q := range qs {
		out[id] = quotaOf(q)
	}
	return out
}

// Builds the gateway over the pools manager and hands its counters to the managers that drain, idle and canary through it
func (d *Daemon) openGateway(rts *runtimes.Registry, bus *events.Bus) error {
	gw := d.Config.GetGateway()
	fc := gw.GetFlowControl()
	retention, err := time.ParseDuration(gw.GetTraceRetention())
	if err != nil || retention <= 0 {
		return fmt.Errorf("gateway.trace_retention %q must be a positive duration such as 168h", gw.GetTraceRetention())
	}
	var chain []byte
	if path := gw.GetRequestChain(); path != "" {
		if chain, err = os.ReadFile(path); err != nil {
			return fmt.Errorf("gateway.request_chain: %w", err)
		}
	}
	d.Gateway, err = gateway.New(gateway.Deps{
		Pools:     d.Pools,
		Resolver:  d.Pools,
		Manager:   d.Pools,
		Store:     d.Pools.Store(),
		DB:        d.DB,
		Events:    bus,
		Guard:     d.Guard,
		Tools:     d.Tools,
		Peers:     d.Formations,
		Dial:      d.Mesh.DialEndpoint,
		Runtimes:  runtimeReader{rts},
		Instances: d.Instances,
		Adapters:  storeAdapters{d.Store},
		Metrics:   d.Metrics,
		Telemetry: d.Telemetry,
		Health:    d.Health,
		Logger:    d.Log,
		Config: gateway.Config{
			Keys:         gw.GetApiKeys(),
			Origins:      gw.GetCorsOrigins(),
			Policy:       gw.GetPolicy(),
			OutputTokens: int(gw.GetDefaultOutputTokens()),
			Flow: flowcontrol.Config{
				QueueCapacity:  int(fc.GetQueueCapacity()),
				QueueTimeout:   time.Duration(fc.GetQueueTimeoutMs()) * time.Millisecond,
				SheddableBelow: int(fc.GetSheddableBelow()),
				Tick:           flowTick,
				Quota:          quotaOf(fc.GetQuota()),
				Quotas:         quotasOf(fc.GetQuotas()),
			},
			Saturation:     flowcontrol.Thresholds{QueueDepth: int(fc.GetSaturationQueueDepth()), KVUtilization: fc.GetSaturationKvUtilization()},
			Staleness:      time.Duration(fc.GetMetricsStalenessMs()) * time.Millisecond,
			Retry:          gateway.Retry{Shed: retryShed, Unavailable: retryUnavailable},
			TraceRetention: retention,
			TraceRing:      int(gw.GetTraceRing()),
			Version:        Version,
			Interval:       gatewayInterval,
			SchedulerCert:  d.Config.GetTls().GetCertFile(),
			Chain:          chain,
		},
	})
	if err != nil {
		return fmt.Errorf("gateway: %w", err)
	}
	d.Instances.InFlight, d.Instances.Dispatched = d.Gateway.InFlight, d.Gateway.Dispatched
	d.Instances.Load = instances.ScrapedLoad(d.Pools.Store(), time.Duration(fc.GetMetricsStalenessMs())*time.Millisecond)
	d.Slots.Outcomes = d.Gateway
	d.Formations.InFlight, d.Formations.Gateway = d.Gateway.InFlight, d.Gateway
	d.Mesh.InFlight = d.Gateway.InFlight
	return nil
}

// Builds the API handler, nebu's MCP server, and the handler members reach on the mesh listener
func (d *Daemon) openHandlers(fmts *formats.Registry, rts *runtimes.Registry, recent *launch.Log, bus *events.Bus) error {
	var ui http.Handler
	if !d.Config.GetWeb().GetDisabled() {
		ui = web.Handler()
	}
	deps := rpc.Deps{
		Host:      d.Host,
		Doctor:    d.Doctor,
		Settings:  d.Settings,
		Sources:   d.Sources,
		Formats:   fmts,
		Runtimes:  rts,
		Inspector: d.Inspector,
		Store:     d.Store,
		Puller:    d.Puller,
		Tasks:     d.Tasks,
		Installs:  d.Installs,
		Instances: d.Instances,
		Slots:     d.Slots,
		Pools:     d.Pools,
		Gateway:   d.Gateway,
		Bots:      d.Bots,
		Chats:     d.Chats,
		// The gateway shares the API listener unless config gives it one
		GatewayShared: d.Config.GetGateway().GetListen() == "",
		Events:        bus,
		Snapshot:      d.snapshot,
		Web:           ui,
		Guard:         d.Guard,
		Sessions:      d.Sessions,
		Users:         d.Users,
		Tokens:        d.Tokens,
		SSO:           d.SSO,
		Recent:        recent,
		Log:           d.Log,
		Mesh:          d.Mesh,
		Formations:    d.Formations,
		Perf:          d.Perf,
		CacheDir:      d.Formations.CacheDir,
		Tools:         d.Tools,
	}
	deps.MCP = mcpserver.Handler(mcpserver.Deps{Host: d.Host, Doctor: d.Doctor, Sources: d.Sources, Inspector: d.Inspector, Formats: fmts, Runtimes: rts, Store: d.Store, Puller: d.Puller, Tasks: d.Tasks, Installs: d.Installs, Instances: d.Instances, Slots: d.Slots, Pools: d.Pools, Gateway: d.Gateway, Mesh: d.Mesh, Formations: d.Formations, Perf: d.Perf, Guard: d.Guard, Tools: d.Tools, Events: bus, Version: Version, Log: d.Log})
	var err error
	if d.handler, err = rpc.NewHandler(deps); err != nil {
		return err
	}
	// Node traffic gets only what members call each other through, the gateway mounted whatever it listens on
	meshDeps := deps
	meshDeps.MeshSurface = true
	d.Mesh.Handler, err = rpc.NewHandler(meshDeps)
	return err
}
