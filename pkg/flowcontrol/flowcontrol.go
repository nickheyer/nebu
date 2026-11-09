// Package flowcontrol queues, prioritizes, shares, meters and sheds requests before they are scheduled
package flowcontrol

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/nickheyer/nebu/pkg/datalayer"
)

var (
	// Returned when the request is shed under saturation or displaced by a higher band
	ErrShed = errors.New("request shed under load")
	// Returned when the request waited past its queue timeout
	ErrQueueTimeout = errors.New("request timed out in queue")
	// Returned when the pool queue is full and no lower band holds an item to displace
	ErrQueueFull = errors.New("queue full")
	// Returned when the fairness id used up its quota for the window
	ErrQuotaExceeded = errors.New("fairness quota exceeded")
)

// Identifies the flow a request belongs to
type FlowKey struct {
	FairnessID string
	Priority   int
}

// What flow control needs of a request
type Item struct {
	ID       string
	Flow     FlowKey
	Pool     datalayer.PoolID
	Model    string
	Adapter  string
	Priority int
	// Estimated cost in tokens, prompt plus expected output
	Cost int
	// Prompt tokens, the predictor input for time to first token
	PromptTokens int
	// The latency objective, zero fields meaning none
	TTFT time.Duration
	TPOT time.Duration
	// When the request entered flow control
	EnqueuedAt time.Time
	// The route the item counts under across every pool and how many of its items may be in flight, zero uncapped
	Route      string
	RouteLimit int
}

// The permit a dispatched request holds until it is released
type Permit interface {
	// Releases the request's share of capacity
	Release()
	// Charges the tokens the request actually used to its fairness id, before Release
	Settle(promptTokens, outputTokens int)
	// How long the request waited in queue
	Waited() time.Duration
}

// Metered requests and tokens of one fairness id
type Usage struct {
	Requests     int64
	PromptTokens int64
	OutputTokens int64
}

// Tokens and requests one fairness id may use per window, zero limits unlimited
type Quota struct {
	Tokens   int64
	Requests int64
	Window   time.Duration
}

// Refuses an admission whose fairness id used up its quota, naming the wait until the window resets
type QuotaError struct {
	FairnessID string
	Retry      time.Duration
}

func (e *QuotaError) Error() string {
	return fmt.Sprintf("fairness id %s is over its quota, retry in %s", e.FairnessID, e.Retry.Round(time.Second))
}

func (e *QuotaError) Unwrap() error { return ErrQuotaExceeded }

// Judges whether a pool can take another request of a given cost
type SaturationDetector interface {
	// Whether the pool has no capacity for the item, at the hard limit of its endpoints
	HardSaturated(pool datalayer.PoolID, item *Item) bool
	// Whether every endpoint of the pool would miss the item's latency objective
	SoftSaturated(pool datalayer.PoolID, item *Item) bool
	// Records that the item left the queue and now holds capacity in the pool
	Dispatched(pool datalayer.PoolID, item *Item)
	// Records that the dispatched item gave its capacity back
	Released(pool datalayer.PoolID, item *Item)
}

// Admits requests into a pool under priority, fairness, quota and saturation
type Controller interface {
	// Blocks until the item may dispatch, or returns a shed, quota, timeout or ctx error
	Admit(ctx context.Context, item *Item) (Permit, error)
	// Gives a dispatched item's capacity back and blocks until it may dispatch again, timed from its first admission
	Requeue(ctx context.Context, pm Permit, item *Item) (Permit, error)
	// The number of items waiting per band for a pool
	Depth(pool datalayer.PoolID) map[int]int
	// The number of dispatched items not yet released for a pool
	InFlight(pool datalayer.PoolID) int
	// Metered requests and tokens of a fairness id in its current quota window
	Usage(fairnessID string) Usage
	Close()
}

// How an admission ended, as reported to an observer
const (
	OutcomeDispatched = "dispatched"
	OutcomeShed       = "shed"
	OutcomeQueueFull  = "queue_full"
	OutcomeQuota      = "quota"
	OutcomeTimeout    = "timeout"
	OutcomeCanceled   = "canceled"
	OutcomeClosed     = "closed"
)

// Receives one call per admission so a caller can export counters and histograms
type Observer interface {
	// Reports the pool, band, time spent queued and how the admission ended
	OnAdmit(pool datalayer.PoolID, band int, waited time.Duration, outcome string)
}

// How a controller queues, meters and sheds
type Config struct {
	// Items a pool holds queued across its bands before a lower band is displaced or the arrival refused
	QueueCapacity int
	// How long an item may wait before it times out
	QueueTimeout time.Duration
	// Bands at or below this priority are shed instead of queued under hard saturation
	SheddableBelow int
	// How often queued items are reconsidered when no release wakes them
	Tick time.Duration
	// The quota of every fairness id without one of its own
	Quota Quota
	// Quotas per fairness id, replacing the default
	Quotas map[string]Quota
}
