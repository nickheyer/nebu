// Package datalayer holds pools, their endpoints, and the attributes collectors attach to each endpoint
package datalayer

import (
	"context"
	"maps"
	"net"
	"strconv"
	"time"
)

// Identifies one endpoint, one process port, across the whole mesh
type EndpointID string

// Identifies a pool of endpoints sharing base model, runtime and accelerator class
type PoolID string

// How the gateway speaks to the endpoints of a pool
type AppProtocol string

const (
	AppProtocolHTTP AppProtocol = "http"
	AppProtocolH2C  AppProtocol = "h2c"
	AppProtocolGRPC AppProtocol = "grpc"
)

// What the gateway does when the scheduler cannot answer
type FailureMode string

const (
	FailClose FailureMode = "FailClose"
	FailOpen  FailureMode = "FailOpen"
)

// Label keys every pool bridge sets on its endpoints
const (
	LabelModel     = "model"
	LabelRuntime   = "runtime"
	LabelNode      = "node"
	LabelInstance  = "instance"
	LabelFormation = "formation"
	LabelRole      = "role"
	LabelRank      = "rank"
	LabelAdapter   = "adapter"
	// Ranks an expert parallel seat spreads its experts over, absent or one for a seat without expert parallelism
	LabelExpertParallel = "expert-parallel"
	// The accelerator class the endpoint runs on, as its pool spec names it
	LabelAccelerator = "accelerator"
)

// Labels carrying what the runtime tells the launch alone
const (
	// Cells of the endpoint's whole KV pool, unified or split into slots, or of the static caches requests hold
	LabelKVCapacity = "kv-capacity"
	// Host and port the runtime publishes its KV cache events on, absent when it publishes none
	LabelKVEvents = "kv-events"
	// Host and port the runtime replays missed KV cache event batches from
	LabelKVReplay = "kv-replay"
)

// Formats a runtime lists its loaded adapters in
const (
	// A llama.cpp lora-adapters array of path and scale, a positive scale applied
	AdaptersLlamaCpp = "llamacpp"
	// An OpenAI model listing whose entries with a parent are adapters of it
	AdaptersOpenAI = "openai"
)

// Roles an endpoint may take inside a disaggregated pool
const (
	RolePrefill = "prefill"
	RoleDecode  = "decode"
	RoleBoth    = "both"
)

// A host and port a request is sent to
type Address struct {
	Host string
	Port int
}

// The address as host colon port
func (a Address) String() string { return net.JoinHostPort(a.Host, strconv.Itoa(a.Port)) }

// Parses host colon port
func ParseAddress(s string) (Address, error) {
	h, p, err := net.SplitHostPort(s)
	if err != nil {
		return Address{}, err
	}
	port, err := strconv.Atoi(p)
	if err != nil {
		return Address{}, err
	}
	return Address{Host: h, Port: port}, nil
}

// A pool's desired shape
type Pool struct {
	ID      PoolID
	Model   string
	Runtime string
	// The accelerator class every endpoint runs on, empty for a pool that does not group by one
	Accelerator string
	Selector    map[string]string
	TargetPorts []int
	AppProtocol AppProtocol
	FailureMode FailureMode
	// The scheduler profile set this pool's requests run through
	SchedulerConfig string
	// Metric names and capabilities the pool's runtime declares, consumed by the scrape source
	Metrics MetricCapabilities
	// Whether the gateway encodes prompts itself and sends token ids, decoding answers from ids where the protocol returns them
	TokensInOut bool
}

// Which metrics a runtime emits and under which names
type MetricCapabilities struct {
	QueuedRequests     string
	RunningRequests    string
	KVCacheUtilization string
	// Label values a series must match to count toward the unsuffixed gauge field, an absent label matching empty
	QueuedRequestsMatch     map[string]string
	RunningRequestsMatch    map[string]string
	KVCacheUtilizationMatch map[string]string
	// Label naming the scheduler replica a series reports, series sharing its value counting once at their largest
	ReplicaLabel string
	// Labeled info gauge carrying block size and total blocks, with the label names
	CacheConfigInfo string
	BlockSizeLabel  string
	NumBlocksLabel  string
	// Gauges whose values are block size and total blocks, for runtimes without a cache config info gauge
	BlockSize      string
	BlockSizeMatch map[string]string
	NumBlocks      string
	NumBlocksMatch map[string]string
	// Labeled info gauge carrying LoRA state, exported only while the runtime serves adapters, with the label names
	LoRAInfo             string
	MaxLoRALabel         string
	RunningAdaptersLabel string
	WaitingAdaptersLabel string
	// JSON listing of the loaded adapters in the named format, for a runtime exporting no LoRA info gauge
	AdaptersPath   string
	AdaptersFormat string
	// JSON document and field holding how many adapters one batch runs, for a listing that states no cap
	MaxLoRAPath  string
	MaxLoRAField string
	// JSON slot listing KV utilization and cells are summed from when the runtime exports no KV gauge
	SlotsPath string
	// Whether every running request holds a full length cache, KV use then being running over capacity
	StaticCache bool
	// Path of the metrics endpoint
	Path string
	// Port serving the metrics path when the runtime serves it apart from the endpoint port
	MetricsPort int
	// Path of the health endpoint
	HealthPath string
}

// Whether the queue and running gauges and a KV source, a gauge, a slot listing or static caches, are declared
func (m MetricCapabilities) Complete() bool {
	return m.QueuedRequests != "" && m.RunningRequests != "" && (m.KVCacheUtilization != "" || m.SlotsPath != "" || m.StaticCache)
}

// A deep copy sharing no label matcher map with the original
func (m MetricCapabilities) Clone() MetricCapabilities {
	m.QueuedRequestsMatch = maps.Clone(m.QueuedRequestsMatch)
	m.RunningRequestsMatch = maps.Clone(m.RunningRequestsMatch)
	m.KVCacheUtilizationMatch = maps.Clone(m.KVCacheUtilizationMatch)
	m.BlockSizeMatch = maps.Clone(m.BlockSizeMatch)
	m.NumBlocksMatch = maps.Clone(m.NumBlocksMatch)
	return m
}

// The desired state of one endpoint as its bridge reports it
type EndpointSpec struct {
	ID      EndpointID
	Pool    PoolID
	Address Address
	Labels  map[string]string
	// Whether the bridge considers the process ready to take traffic
	Ready bool
}

// One endpoint and the attributes collected on it
type Endpoint interface {
	ID() EndpointID
	Pool() PoolID
	Address() Address
	Labels() map[string]string
	Ready() bool
	// The attribute a named collector stored, false when it never did
	Attribute(collector string) (any, bool)
}

// Reads a typed attribute from an endpoint, the zero value and false when absent or of another type
func Attribute[T any](ep Endpoint, collector string) (T, bool) {
	var zero T
	v, ok := ep.Attribute(collector)
	if !ok {
		return zero, false
	}
	t, ok := v.(T)
	if !ok {
		return zero, false
	}
	return t, true
}

// Collector names the core sources store under
const (
	CollectorMetrics = "metrics"
	CollectorHealth  = "health"
	CollectorCatalog = "catalog"
	CollectorAssumed = "scheduler.assumed"
	// Prompt and expected output tokens of the requests this gateway assumed on the endpoint
	CollectorAssumedTokens = "scheduler.assumed.tokens"
	CollectorLatency       = "scheduler.latency"
	// Requests one endpoint runs at once, set by the pool bridge from the runtime capacity
	CollectorCapacity = "capacity"
	// The metrics scrape an endpoint keeps failing, set by the scrape source once failures repeat
	CollectorScrapeFailure = "scrape.failure"
)

// The scraped state of a model server
type ServerMetrics struct {
	QueuedRequests     int
	RunningRequests    int
	KVCacheUtilization float64
	BlockSize          int
	NumGPUBlocks       int
	MaxLoRA            int
	RunningAdapters    map[string]struct{}
	WaitingAdapters    map[string]struct{}
	UpdatedAt          time.Time
}

// Whether the sample is older than the staleness threshold
func (m *ServerMetrics) Stale(now time.Time, threshold time.Duration) bool {
	return m == nil || now.Sub(m.UpdatedAt) > threshold
}

// Whether the adapter is loaded and serving
func (m *ServerMetrics) AdapterActive(name string) bool {
	if m == nil {
		return false
	}
	_, ok := m.RunningAdapters[name]
	return ok
}

// The error of the latest failed scrape of an endpoint whose scrapes keep failing, and when the failing began
type ScrapeFailure struct {
	Err   string
	Since time.Time
}

// The last health probe of an endpoint
type HealthState struct {
	Healthy   bool
	CheckedAt time.Time
	Failures  int
	Message   string
}

// What an endpoint's model catalog lists
type Catalog struct {
	Models    []string
	Adapters  []string
	UpdatedAt time.Time
}

// What changed in the store
type ChangeKind int

const (
	ChangePoolPut ChangeKind = iota
	ChangePoolDeleted
	ChangeEndpointPut
	ChangeEndpointDeleted
	ChangeAttribute
)

// One store change for watchers
type Change struct {
	Kind      ChangeKind
	Pool      PoolID
	Endpoint  EndpointID
	Collector string
}

// Holds pools, endpoints and attributes behind lock free reads
type Store interface {
	PutPool(p Pool)
	// Removes a pool and every endpoint in it
	DeletePool(id PoolID)
	Pool(id PoolID) (Pool, bool)
	Pools() []Pool
	// Adds or replaces an endpoint, keeping attributes already collected on it
	UpsertEndpoint(spec EndpointSpec)
	DeleteEndpoint(id EndpointID)
	Endpoint(id EndpointID) (Endpoint, bool)
	// Endpoints of a pool the predicate keeps, every endpoint when pool is empty
	Endpoints(pool PoolID, keep func(Endpoint) bool) []Endpoint
	// Stores a collector's attribute on an endpoint, a nil value removing it
	SetAttribute(id EndpointID, collector string, value any)
	// Delivers changes until ctx ends, slow watchers dropping nothing because the channel is unbounded
	Watch(ctx context.Context) <-chan Change
	// Whether every pool bridge has reported once since start
	Synced() bool
	MarkSynced()
}

// Keeps endpoints that are ready and whose health collector last found them healthy
func ReadyAndHealthy(ep Endpoint) bool {
	if !ep.Ready() {
		return false
	}
	h, ok := Attribute[*HealthState](ep, CollectorHealth)
	return !ok || h.Healthy
}

// Turns data from a source into an attribute on an endpoint
type Collector interface {
	Name() string
	// Extracts the attribute this collector owns from one source sample
	Extract(ep Endpoint, sample any) (any, error)
}

// Produces samples for the endpoints it tracks and hands them to collectors
type DataSource interface {
	Type() string
	Start(ctx context.Context) error
	Stop() error
	Subscribe(c Collector) error
	// Replaces the endpoints the source tracks
	UpdateEndpoints(ids []EndpointID) error
}

// A source that samples only the endpoints of pools it declares support for
type PoolScoped interface {
	Tracks(p Pool) bool
}

// Every registered source by type
type Sources map[string]DataSource

// Registers a source, refusing a second of the same type
func (s Sources) Register(src DataSource) error {
	if _, dup := s[src.Type()]; dup {
		return ErrDuplicateSource
	}
	s[src.Type()] = src
	return nil
}
