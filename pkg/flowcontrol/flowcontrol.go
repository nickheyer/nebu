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
	// Answered by an item's placement while no endpoint has room for it, the item then waiting in queue
	ErrNoRoom = errors.New("no endpoint has room for the request")
	// Answered by a placement that found every endpoint full, the whole pool then waiting for a release
	ErrPoolFull = fmt.Errorf("%w: every endpoint is full", ErrNoRoom)
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
	Priority int
	// Estimated cost in tokens, prompt plus expected output
	Cost int
	// When the request entered flow control
	EnqueuedAt time.Time
	// The route the item counts under across every pool and how many of its items may be in flight, zero uncapped
	Route      string
	RouteLimit int
	// Claims room on an endpoint, ErrNoRoom or ErrPoolFull keeping the item queued and any other error ending its admission
	Place func() error
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

// Admits requests into a pool under priority, fairness and quota, each dispatched once its placement finds room
type Controller interface {
	// Blocks until the item is placed, or returns a shed, quota, timeout, placement or ctx error
	Admit(ctx context.Context, item *Item) (Permit, error)
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
	// The placement failed for a reason other than room
	OutcomeUnplaced = "unplaced"
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
	// Bands at or below this priority are shed instead of queued when no endpoint has room
	SheddableBelow int
	// How often queued items are placed again when no release wakes them
	Tick time.Duration
	// The quota of every fairness id without one of its own
	Quota Quota
	// Quotas per fairness id, replacing the default
	Quotas map[string]Quota
}
