// Package gateway serves the OpenAI, Anthropic, Ollama and media APIs over pools through the request director
package gateway

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nickheyer/nebu/internal/auth"
	"github.com/nickheyer/nebu/internal/db"
	"github.com/nickheyer/nebu/internal/gateway/jinja"
	"github.com/nickheyer/nebu/internal/pools"
	"github.com/nickheyer/nebu/internal/tools"
	"github.com/nickheyer/nebu/pkg/chattemplate"
	"github.com/nickheyer/nebu/pkg/datalayer"
	"github.com/nickheyer/nebu/pkg/events"
	"github.com/nickheyer/nebu/pkg/flowcontrol"
	"github.com/nickheyer/nebu/pkg/health"
	"github.com/nickheyer/nebu/pkg/latency"
	"github.com/nickheyer/nebu/pkg/metrics"
	"github.com/nickheyer/nebu/pkg/outbound"
	"github.com/nickheyer/nebu/pkg/prefixcache"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/requestcontrol"
	"github.com/nickheyer/nebu/pkg/scheduling/framework"
	"github.com/nickheyer/nebu/pkg/telemetry"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

// Reads the current time
type Clock interface {
	Now() time.Time
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

// The wall clock
var SystemClock Clock = systemClock{}

// What the gateway reads of the pools manager beyond the view and resolver
type Manager interface {
	// A named latency objective and priority band, false when none has the name
	Objective(name string) (*v1.Objective, bool)
	// Every scheduler configuration the manager holds, built in ones included
	SchedulerConfigs() []*v1.SchedulerConfig
	// Sets the dependencies the manager validates scheduler configurations against
	SetSchedulerDeps(ctx context.Context, deps pools.SchedulerDeps)
}

// What the gateway reads of the runtimes behind pools
type Runtimes interface {
	// The wire protocol a runtime's endpoints speak
	API(runtimeID string) v1.ApiFlavor
	// The relay handoff a runtime declares between prefill and decode seats, empty for none
	Handoff(runtimeID string) string
}

// What the gateway reads of the instances serving endpoints
type Instances interface {
	// How the instance's chat template offers tools
	ToolSupport(instanceID string) v1.ToolSupport
	// The generation modes a media instance serves, img_gen and vid_gen
	Modes(instanceID string) []string
	// The chat template the instance's model carries, rendered for prompts sent over gRPC
	Template(instanceID string) (*chattemplate.Template, error)
	// Loads an adapter from its directory into a ready instance under the name requests use for it
	LoadAdapter(ctx context.Context, instanceID, name, path string) error
	// The tokenizer.json among the instance's model files, read by pools in tokens in tokens out mode
	TokenizerFile(instanceID string) (string, error)
}

// Connections to other members, for forwarded routes and the relay's slot moves
type Peers interface {
	// This node's id
	Self() string
	// A member's gateway base and a client carrying the session credential
	Dial(ctx context.Context, nodeID string) (base string, client *http.Client, err error)
	// Moves a relay's saved slot file to a node from another
	MoveSlotTo(ctx context.Context, formationID, toNode, fromNode, file string) error
	// Removes a relay's slot file from a node
	DropSlotOn(ctx context.Context, nodeID, formationID, file string) error
}

// Waits a refused client is told to take before trying again
type Retry struct {
	// Requests shed by flow control or a route policy
	Shed time.Duration
	// Requests to a pool with no ready endpoint or a route still starting
	Unavailable time.Duration
}

// What the gateway is configured with
type Config struct {
	// Bearer keys any of which opens inference
	Keys []string
	// Origins browsers may call from, every origin when empty
	Origins []string
	// The policy every unset route policy field inherits
	Policy *v1.Policy
	// Output tokens expected of a request whose body sets no bound
	OutputTokens int
	// Queueing, timeouts and shedding of flow control
	Flow flowcontrol.Config
	// Scraped levels at which an endpoint counts as saturated
	Saturation flowcontrol.Thresholds
	// Age past which scraped metrics are ignored by saturation and objectives
	Staleness time.Duration
	// Waits refused clients are told to take
	Retry Retry
	// How long persisted traces are kept before pruning
	TraceRetention time.Duration
	// Traces kept in memory for listing
	TraceRing int
	// The version the Ollama API reports
	Version string
	// The prefix cache index sizes
	Prefix prefixcache.Config
	// The latency predictor priors
	Latency latency.Config
	// Time between metric gauge mirrors and gRPC health probes
	Interval time.Duration
	// The request control chain as a YAML document, the built in chain when empty
	Chain []byte
	// The PEM certificate file out of process schedulers are trusted by, the system roots alone when empty
	SchedulerCert string
}

// What a gateway is built over
type Deps struct {
	Pools     pools.View
	Resolver  requestcontrol.Resolver
	Manager   Manager
	Store     datalayer.Store
	DB        *db.DB
	Events    *events.Bus
	Guard     *auth.Guard
	Tools     *tools.Registry
	Peers     Peers
	Runtimes  Runtimes
	Instances Instances
	// The model store's adapters, nil when no store backs the gateway
	Adapters  Adapters
	Metrics   *metrics.Registry
	Telemetry *telemetry.Provider
	Health    *health.Registry
	Logger    *slog.Logger
	Config    Config
	Clock     Clock
	// Opens upstream connections, imported endpoints reached through the mesh when set
	Dial Dialer
}

// Listener addresses and whether they speak TLS
type listening struct {
	list []*v1.Listener
	tls  bool
}

// Routes inference requests over pools through the director and answers in every client protocol
type Gateway struct {
	cfg       Config
	pools     pools.View
	resolver  *resolver
	manager   Manager
	store     datalayer.Store
	events    *events.Bus
	guard     *auth.Guard
	tools     *tools.Registry
	peers     Peers
	runtimes  Runtimes
	instances Instances
	adapters  Adapters
	metrics   *metrics.Registry
	tracer    trace.Tracer
	health    *health.Registry
	log       *slog.Logger
	clock     Clock
	dial      Dialer
	// Remote clients of members' schedulers by node id
	remotes sync.Map

	index     *prefixcache.Index
	predictor *latency.Bayes
	flow      *flowcontrol.FlowController
	hosting   *hosting
	topology  requestcontrol.Topology
	director  atomic.Pointer[requestcontrol.Director]
	// Serializes director rebuilds, never taken on the request path
	rebuild   sync.Mutex
	exporter  *metrics.Exporter
	traces    *Recorder
	templates *jinja.Cache
	// Tokenizers of the model files pools in tokens in tokens out mode encode with
	tokenizers *tokenizers
	conns      *conns
	// Fetches client image links through the outbound guard on one pooled transport
	images     *http.Client
	grpc       *grpc.Server
	grpcHealth *health.GRPC

	transports sync.Map
	routes     sync.Map
	// Adapter loads in flight by name, shared by every request naming one meanwhile
	loads      sync.Map
	inflight   sync.Map
	dispatched sync.Map
	outcomes   sync.Map
	videos     *videoStore
	jobs       *forwardedJobs
	// One plugin of each handoff type, the relay calling the one a scheduler profile or runtime names
	handoffs map[string]requestcontrol.Handoff
	rounds   *roundStore

	listeners atomic.Pointer[listening]
	// The keys and default policy a config reload swaps
	live  atomic.Pointer[live]
	total atomic.Uint64
	mux   http.Handler
}

// The prefix index and the configuration it chunks prompts by, for sources reporting into it
func (g *Gateway) Index() (prefixcache.Indexer, prefixcache.Config) { return g.index, g.cfg.Prefix }

// Builds the gateway, its schedulers, flow controller and director, refusing missing dependencies
func New(deps Deps) (*Gateway, error) {
	switch {
	case deps.Pools == nil, deps.Resolver == nil, deps.Manager == nil, deps.Store == nil:
		return nil, errors.New("gateway needs the pools view, resolver, manager and store")
	case deps.DB == nil, deps.Events == nil, deps.Guard == nil, deps.Tools == nil:
		return nil, errors.New("gateway needs the database, event bus, guard and tools registry")
	case deps.Runtimes == nil, deps.Instances == nil:
		return nil, errors.New("gateway needs the runtimes and instances readers")
	case deps.Metrics == nil, deps.Telemetry == nil, deps.Health == nil, deps.Logger == nil:
		return nil, errors.New("gateway needs the metrics registry, telemetry provider, health registry and logger")
	case deps.Config.OutputTokens <= 0:
		return nil, errors.New("gateway needs a positive default output token count")
	case deps.Config.Staleness <= 0, deps.Config.Interval <= 0:
		return nil, errors.New("gateway needs positive metric staleness and interval")
	case deps.Config.TraceRetention <= 0, deps.Config.TraceRing <= 0:
		return nil, errors.New("gateway needs a positive trace retention and ring size")
	case deps.Config.Retry.Shed <= 0, deps.Config.Retry.Unavailable <= 0:
		return nil, errors.New("gateway needs positive retry waits")
	}
	clock := deps.Clock
	if clock == nil {
		clock = SystemClock
	}
	g := &Gateway{
		cfg:        deps.Config,
		pools:      deps.Pools,
		manager:    deps.Manager,
		store:      deps.Store,
		events:     deps.Events,
		guard:      deps.Guard,
		tools:      deps.Tools,
		peers:      deps.Peers,
		runtimes:   deps.Runtimes,
		instances:  deps.Instances,
		adapters:   deps.Adapters,
		metrics:    deps.Metrics,
		tracer:     deps.Telemetry.Tracer("github.com/nickheyer/nebu/internal/gateway"),
		health:     deps.Health,
		log:        deps.Logger,
		clock:      clock,
		dial:       deps.Dial,
		index:      prefixcache.New(deps.Config.Prefix),
		predictor:  latency.New(deps.Config.Latency),
		templates:  jinja.NewCache(deps.Instances.Template),
		tokenizers: newTokenizers(deps.Instances.TokenizerFile),
		images:     &http.Client{Transport: outbound.Transport()},
		videos:     newVideoStore(),
		jobs:       newForwardedJobs(),
		handoffs:   map[string]requestcontrol.Handoff{},
		rounds:     newRoundStore(),
	}
	for _, typ := range requestcontrol.HandoffTypes() {
		h, err := requestcontrol.NewHandoff(typ)
		if err != nil {
			return nil, err
		}
		g.handoffs[typ] = h
	}
	g.resolver = &resolver{next: deps.Resolver, g: g}
	g.Reload(deps.Config.Keys, deps.Config.Policy)
	g.conns = newConns(deps.Dial)
	g.traces = newRecorder(deps.DB, deps.Events, deps.Logger, clock, deps.Config.TraceRing)
	detector, err := flowcontrol.NewMetricsDetector(deps.Store, g.predictor, deps.Config.Staleness, deps.Config.Saturation)
	if err != nil {
		return nil, err
	}
	if g.flow, err = flowcontrol.NewController(deps.Config.Flow, detector, deps.Metrics); err != nil {
		return nil, err
	}
	if g.exporter, err = metrics.NewExporter(deps.Metrics, deps.Store, g.flow, g.index, deps.Config.Interval); err != nil {
		return nil, err
	}
	g.hosting = newHosting(g)
	if err := g.hosting.load(deps.Manager.SchedulerConfigs()); err != nil {
		return nil, err
	}
	if g.topology, err = g.chain(deps.Config.Chain); err != nil {
		return nil, err
	}
	if err := g.rebuildDirector(); err != nil {
		return nil, err
	}
	deps.Health.Register(checkDatalayer, health.StoreSynced(deps.Store))
	deps.Health.Register(checkSchedulers, g.hosting.ready)
	g.grpc = grpc.NewServer(g.grpcOptions()...)
	g.registerTranscoder(g.grpc)
	if g.grpcHealth, err = health.NewGRPC(deps.Health, health.GRPCConfig{Subsystems: []string{checkDatalayer, checkSchedulers}, Interval: deps.Config.Interval}); err != nil {
		return nil, err
	}
	healthpb.RegisterHealthServer(g.grpc, g.grpcHealth.Server())
	// Readiness over gRPC answers from construction, not from the first refresh Run schedules
	probe, cancel := context.WithTimeout(context.Background(), deps.Config.Interval)
	g.grpcHealth.Refresh(probe)
	cancel()
	g.mux = g.routesMux()
	return g, nil
}

// Readiness check names, also the gRPC health subsystems
const (
	checkDatalayer  = "datalayer"
	checkSchedulers = "schedulers"
)

// Keeps schedulers, the prefix index, metrics, health and traces current until ctx ends, then stops flow control
func (g *Gateway) Run(ctx context.Context) {
	g.manager.SetSchedulerDeps(ctx, g)
	g.reloadSchedulers(nil)
	defer g.index.Close()
	defer g.flow.Close()
	defer g.conns.close()
	var wg sync.WaitGroup
	wg.Go(func() { g.followConfigs(ctx) })
	wg.Go(func() { g.followStore(ctx) })
	wg.Go(func() { g.exporter.Run(ctx) })
	wg.Go(func() {
		g.grpcHealth.Refresh(ctx)
		g.grpcHealth.Run(ctx)
	})
	wg.Go(func() { g.traces.run(ctx, g.cfg.TraceRetention) })
	wg.Wait()
}

// The director requests are decided by now
func (g *Gateway) current() requestcontrol.Director { return *g.director.Load() }

// Builds a director over the hosted schedulers and swaps it in, decisions in flight keeping the one that made them
func (g *Gateway) rebuildDirector() error {
	g.rebuild.Lock()
	defer g.rebuild.Unlock()
	d, err := requestcontrol.NewDirector(requestcontrol.Deps{
		Store:               g.store,
		Resolver:            g.resolver,
		Scheduler:           g.hosting.selector,
		Schedulers:          g.hosting.byName(),
		Flow:                g.flow,
		Topology:            g.topology,
		Lifecycle:           []framework.Plugin{g.hosting.router},
		Logger:              g.log,
		Clock:               g.clock,
		Observer:            observer{g: g},
		DefaultOutputTokens: g.cfg.OutputTokens,
	})
	if err != nil {
		return fmt.Errorf("director: %w", err)
	}
	g.director.Store(&d)
	return nil
}

// Records listener addresses and whether they speak TLS, for the status view and CORS
func (g *Gateway) SetListeners(list []*v1.Listener, tls bool) {
	g.listeners.Store(&listening{list: list, tls: tls})
}

func (g *Gateway) listening() *listening {
	if l := g.listeners.Load(); l != nil {
		return l
	}
	return &listening{}
}

// The request recorder, for listing and fetching traces
func (g *Gateway) Traces() *Recorder { return g.traces }

// The HTTP handler serving every gateway route, gRPC calls included
func (g *Gateway) Handler() http.Handler { return g.mux }
