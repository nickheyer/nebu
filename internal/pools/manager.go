package pools

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nickheyer/nebu/internal/db"
	"github.com/nickheyer/nebu/pkg/conditions"
	"github.com/nickheyer/nebu/pkg/datalayer"
	"github.com/nickheyer/nebu/pkg/datalayer/catalog"
	"github.com/nickheyer/nebu/pkg/datalayer/kvevents"
	"github.com/nickheyer/nebu/pkg/datalayer/probe"
	"github.com/nickheyer/nebu/pkg/datalayer/scrape"
	"github.com/nickheyer/nebu/pkg/events"
	"github.com/nickheyer/nebu/pkg/prefixcache"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/requestcontrol"
)

// Scheduler configurations every node holds from start
const (
	SchedulerDefault   = "default"
	SchedulerNoMetrics = "default-nometrics"
	SchedulerPD        = "pd"
	SchedulerReplicas  = "replicas"
)

// The controller name pool parent entries are written under
const GatewayController = "nebu.dev/gateway"

// Shortest gap between two readiness or health events of one pool or route
const coalesceWindow = 250 * time.Millisecond

var (
	ErrInvalid  = errors.New("invalid resource")
	ErrNotFound = errors.New("resource not found")
	ErrConflict = errors.New("resource held by another owner")
	ErrManaged  = errors.New("resource managed by an owner or built in")
	ErrInUse    = errors.New("resource still referenced")
)

var (
	_ Registrar               = (*Manager)(nil)
	_ View                    = (*Manager)(nil)
	_ requestcontrol.Resolver = (*Manager)(nil)
)

type Options struct {
	DB     *db.DB
	Events *events.Bus
	// This node's id, the gateway every pool status reports against
	NodeID string
	Log    *slog.Logger
	// Time between metric samples for each demand on an endpoint, a zero field taking the scrape default
	Scrape scrape.Pace
	// Time between samples of each source, the source default when zero
	ProbeInterval   time.Duration
	CatalogInterval time.Duration
	// Opens the sources' connections, imported endpoints reached through the mesh when set
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
	// Told the pool, duration and outcome of every metrics scrape
	Scrapes scrape.Observer
	// Told every KV cache event connection, batch and report
	KVEvents kvevents.Observer
}

// Bridges owners and user resources into the data layer store and resolves client model names to pools
type Manager struct {
	db     *db.DB
	bus    *events.Bus
	nodeID string
	log    *slog.Logger
	store  datalayer.Store
	runner *datalayer.Runner
	// Subscribes to each local endpoint's KV cache events for the index set before Start
	kv *kvevents.Source
	// Scrapes endpoint metrics while a decision, a request in flight or an idle check reads them
	metrics *scrape.Source

	// Serializes every mutation and guards everything below it
	mu      sync.Mutex
	st      state
	deps    SchedulerDeps
	pending map[eventKey]*pendingEvent
	started bool
	closed  bool
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	runErr  error

	view  atomic.Pointer[view]
	ports atomic.Pointer[map[datalayer.EndpointID]endpointPorts]
	waker atomic.Pointer[wakerBox]
}

// Everything the manager tracks, read and written under its mutex
type state struct {
	pools      map[datalayer.PoolID]*poolState
	owners     map[OwnerRef]*ownerState
	endpoints  map[datalayer.EndpointID]OwnerRef
	routes     map[string]*routeState
	objectives map[string]*v1.Objective
	// Who holds each objective, the user for one made through PutObjective
	objectiveOwners map[string]map[OwnerRef]bool
	configs         map[string]*configState
}

// One pool, the owners whose endpoints join it in join order, and the user spec that authors or overrides it
type poolState struct {
	id     datalayer.PoolID
	owners []OwnerRef
	// Whether an owner was woken for a request and no endpoint has joined since
	waking bool
	user   *v1.PoolSpec
	// The parsed addresses of the user spec's static endpoints in spec order
	userAddrs []datalayer.Address
	meta      *v1.ObjectMeta
	spec      *v1.PoolSpec
	dl        datalayer.Pool
	stored    bool
	conds     []conditions.Condition
	parents   []conditions.ParentStatus
	ready     uint32
	// The pool without endpoints as readers see it, replaced whole on every change
	record *v1.Pool
}

// One owner, the pool it joins, what it last published and what of that sits in the store
type ownerState struct {
	ref       OwnerRef
	pool      datalayer.PoolID
	m         *Membership
	published []Published
	joined    map[datalayer.EndpointID]joinedEndpoint
	draining  bool
}

// An endpoint as written to the store with the ports its sources sample and its request capacity
type joinedEndpoint struct {
	spec     datalayer.EndpointSpec
	ports    endpointPorts
	capacity int
}

// Ports serving the metrics and health paths, zero meaning the endpoint port
type endpointPorts struct {
	metrics int
	health  int
}

// One route, its primary owner or none for a user route, the node owners sharing its name, and the fields a user set over the owner's
type routeState struct {
	owner OwnerRef
	conds []conditions.Condition
	// The policy, profile, objective and balance the primary owner last named the route with, nil for a user route
	base *v1.RouteSpec
	// The primary owner's rule, its pool or the targets it split to, nil for a user route
	own *v1.RewriteRule
	// Node owners sharing the name, each adding its pool as one more target
	shared map[OwnerRef]sharedTarget
	// The spec a user wrote, nil for an owner route
	user *v1.RouteSpec
	// The policy, profile, objective and balance a user set over the owner's, nil when none
	override *v1.Route
	// The route as readers see it, replaced whole on every change
	route *v1.Route
	// The database refusal that keeps a dropped route in memory until the delete is retried, nil otherwise
	dropped error
	// Whether the stored row lags the state after a refused write, retried on the next change
	dirty bool
	// The shape and seat instance ids the owning formation serves the route with
	shape v1.Shape
	seats []string
	// The node whose gateway answers the route, empty for this node
	nodeID string
	// Each gateway controller's own entry, this node's written here, restored ones kept
	parents []conditions.ParentStatus
}

// One scheduler configuration, whether it loads, and whether it is built in
type configState struct {
	cfg     *v1.SchedulerConfig
	err     error
	builtin bool
	// Whether the configuration has a database row, so its status is written back when it changes
	stored bool
	conds  []conditions.Condition
	record *v1.SchedulerConfig
}

type wakerBox struct{ w Waker }

// Builds a manager over the database, restoring user resources and owner routes, without starting its sources
func New(ctx context.Context, opts Options) (*Manager, error) {
	switch {
	case opts.DB == nil:
		return nil, fmt.Errorf("%w: the pool manager needs a database", ErrInvalid)
	case opts.NodeID == "":
		return nil, fmt.Errorf("%w: the pool manager needs this node's id", ErrInvalid)
	}
	m := &Manager{
		db:      opts.DB,
		bus:     opts.Events,
		nodeID:  opts.NodeID,
		log:     opts.Log,
		store:   datalayer.NewStore(),
		pending: map[eventKey]*pendingEvent{},
		st: state{
			pools:           map[datalayer.PoolID]*poolState{},
			owners:          map[OwnerRef]*ownerState{},
			endpoints:       map[datalayer.EndpointID]OwnerRef{},
			routes:          map[string]*routeState{},
			objectives:      map[string]*v1.Objective{},
			objectiveOwners: map[string]map[OwnerRef]bool{},
			configs:         map[string]*configState{},
		},
	}
	if m.log == nil {
		m.log = slog.Default()
	}
	m.ports.Store(&map[datalayer.EndpointID]endpointPorts{})
	sources, err := m.sources(opts)
	if err != nil {
		return nil, err
	}
	m.runner = datalayer.NewRunner(m.store, sources)
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.loadLocked(ctx); err != nil {
		return nil, err
	}
	m.publishViewLocked()
	return m, nil
}

// The scrape, probe and catalog sources with their collectors, and the KV event source, resolving addresses through the manager
func (m *Manager) sources(opts Options) (datalayer.Sources, error) {
	srcs := datalayer.Sources{}
	m.kv = kvevents.New(m.store, m.kvEventsTarget, kvevents.Options{Observer: opts.KVEvents})
	if err := srcs.Register(m.kv); err != nil {
		return nil, err
	}
	m.metrics = scrape.New(m.store, m.metricsTarget, scrape.Options{Pace: opts.Scrape, Observer: opts.Scrapes, Dial: opts.Dial})
	for _, reg := range []struct {
		src datalayer.DataSource
		col datalayer.Collector
	}{
		{m.metrics, scrape.Collector{}},
		{probe.New(m.store, m.healthURL, probe.Options{Interval: opts.ProbeInterval, Dial: opts.Dial}), probe.NewCollector(probe.DefaultThreshold)},
		{catalog.New(m.store, catalog.StoreResolver(m.store), catalog.Options{Interval: opts.CatalogInterval, Dial: opts.Dial}), catalog.Collector{}},
	} {
		if err := reg.src.Subscribe(reg.col); err != nil {
			return nil, err
		}
		if err := srcs.Register(reg.src); err != nil {
			return nil, err
		}
	}
	return srcs, nil
}

// The metrics source, asked for samples by whatever is about to read them
func (m *Manager) Sampler() datalayer.Sampler { return m.metrics }

// Sets the prefix index KV cache events are reported to, sized by the index's chunking, before Start
func (m *Manager) SetIndexer(ix prefixcache.Indexer, cfg prefixcache.Config) {
	m.kv.SetIndexer(ix)
	m.kv.SetChunking(cfg.ChunkTokens, cfg.MaxChunks)
}

// Runs the data sources and the health and catalog watcher until Close or ctx ends, refusing to run without an index
func (m *Manager) Start(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	switch {
	case !m.kv.HasIndexer():
		return errors.New("pools: manager needs the prefix index before it starts")
	case m.closed:
		return errors.New("pools: manager is closed")
	case m.started:
		return errors.New("pools: manager already started")
	}
	m.started = true
	ctx, m.cancel = context.WithCancel(ctx)
	changes := m.store.Watch(ctx)
	m.wg.Add(2)
	go func() {
		defer m.wg.Done()
		err := m.runSources(ctx)
		m.mu.Lock()
		m.runErr = err
		m.mu.Unlock()
	}()
	go func() {
		defer m.wg.Done()
		m.watch(changes)
	}()
	return nil
}

// Shortest and longest pauses before the data sources are started again after they stop with an error
const (
	sourceRetryMin = 250 * time.Millisecond
	sourceRetryMax = 30 * time.Second
)

// Runs the data sources until ctx ends, starting them again with backoff whenever they stop with an error
func (m *Manager) runSources(ctx context.Context) error {
	wait := sourceRetryMin
	for {
		err := m.runner.Run(ctx)
		if ctx.Err() != nil {
			return err
		}
		if err == nil {
			err = errors.New("pools: data sources stopped before the manager closed")
		}
		m.log.Error("pool data sources stopped, starting them again", "err", err, "after", wait)
		select {
		case <-ctx.Done():
			return err
		case <-time.After(wait):
		}
		wait = min(wait*2, sourceRetryMax)
	}
}

// Stops the data sources, the watcher and pending events, returning the error the sources stopped with
func (m *Manager) Close() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	cancel := m.cancel
	for _, p := range m.pending {
		if p.timer != nil {
			p.timer.Stop()
		}
	}
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	m.wg.Wait()
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.runErr
}

func (m *Manager) Store() datalayer.Store { return m.store }

// Sets what launches a slot when a request arrives for its empty pool
func (m *Manager) SetWaker(w Waker) { m.waker.Store(&wakerBox{w: w}) }
