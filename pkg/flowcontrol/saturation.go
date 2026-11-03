package flowcontrol

import (
	"errors"
	"fmt"
	"hash/maphash"
	"math"
	"sync"
	"time"

	"github.com/nickheyer/nebu/pkg/datalayer"
	"github.com/nickheyer/nebu/pkg/latency"
)

// Collector name of the int attribute holding an endpoint concurrency limit
const CollectorCapacity = datalayer.CollectorCapacity

// Levels at which an endpoint with fresh metrics counts as saturated
type Thresholds struct {
	QueueDepth    int
	KVUtilization float64
}

// Judges latency objectives against predictions on every ready endpoint of a pool
type objectives struct {
	store     datalayer.Store
	predictor latency.Predictor
	staleness time.Duration
}

func newObjectives(store datalayer.Store, predictor latency.Predictor, staleness time.Duration) (objectives, error) {
	switch {
	case store == nil:
		return objectives{}, errors.New("flowcontrol: a datalayer store is required")
	case predictor == nil:
		return objectives{}, errors.New("flowcontrol: a latency predictor is required")
	case staleness <= 0:
		return objectives{}, fmt.Errorf("flowcontrol: staleness must be positive, got %s", staleness)
	}
	return objectives{store: store, predictor: predictor, staleness: staleness}, nil
}

// Whether the item carries an objective that the predictions miss on every ready endpoint
func (o objectives) SoftSaturated(pool datalayer.PoolID, item *Item) bool {
	if !hasObjective(item) {
		return false
	}
	now := time.Now()
	for _, ep := range o.store.Endpoints(pool, datalayer.ReadyAndHealthy) {
		if o.meets(ep, item, now) {
			return false
		}
	}
	return true
}

// Whether the predictions on one endpoint stay within every objective the item carries
func (o objectives) meets(ep datalayer.Endpoint, item *Item, now time.Time) bool {
	f := latency.Features{PromptTokens: item.PromptTokens}
	f.AssumedRequests, _ = datalayer.Attribute[int](ep, datalayer.CollectorAssumed)
	if m, ok := datalayer.Attribute[*datalayer.ServerMetrics](ep, datalayer.CollectorMetrics); ok && !m.Stale(now, o.staleness) {
		f.QueuedRequests, f.RunningRequests, f.KVCacheUtilization = m.QueuedRequests, m.RunningRequests, m.KVCacheUtilization
	}
	if item.TTFT > 0 && misses(o.predictor.PredictTTFT(ep.ID(), f), item.TTFT) {
		return false
	}
	return item.TPOT <= 0 || !misses(o.predictor.PredictTPOT(ep.ID(), f), item.TPOT)
}

// Whether the prediction exceeds the objective, by mean plus one deviation once learned and by the prior mean before
func misses(p latency.Prediction, objective time.Duration) bool {
	if !p.Confident {
		return p.Mean > objective
	}
	return p.Mean+p.StdDev > objective
}

// Saturates a pool from scraped queue and KV metrics, or from assumed load against capacity when none are fresh
type MetricsDetector struct {
	objectives
	thresholds Thresholds
	seed       maphash.Seed

	mu      sync.Mutex
	transit map[datalayer.PoolID]*transit
}

var _ SaturationDetector = (*MetricsDetector)(nil)

// Dispatches of one pool that its endpoint inputs have not yet had a chance to reflect
type transit struct {
	fingerprint uint64
	// Observations made so far, stamped on each dispatch to order it against input changes
	seq   uint64
	items map[*Item]uint64
}

// Builds a detector reading metrics fresher than staleness and judging objectives with the predictor
func NewMetricsDetector(store datalayer.Store, predictor latency.Predictor, staleness time.Duration, thresholds Thresholds) (*MetricsDetector, error) {
	o, err := newObjectives(store, predictor, staleness)
	if err != nil {
		return nil, err
	}
	switch {
	case thresholds.QueueDepth < 1:
		return nil, fmt.Errorf("flowcontrol: queue depth threshold must be at least 1, got %d", thresholds.QueueDepth)
	case thresholds.KVUtilization <= 0 || thresholds.KVUtilization > 1:
		return nil, fmt.Errorf("flowcontrol: KV utilization threshold must be in (0, 1], got %g", thresholds.KVUtilization)
	}
	return &MetricsDetector{
		objectives: o,
		thresholds: thresholds,
		seed:       maphash.MakeSeed(),
		transit:    map[datalayer.PoolID]*transit{},
	}, nil
}

// Whether the free slots of the pool are used up by dispatches its inputs do not yet show
func (d *MetricsDetector) HardSaturated(pool datalayer.PoolID, _ *Item) bool {
	room, open, fingerprint := d.headroom(pool, time.Now())
	d.mu.Lock()
	defer d.mu.Unlock()
	t := d.transitFor(pool)
	if fingerprint != t.fingerprint {
		for it, seq := range t.items {
			if seq < t.seq {
				delete(t.items, it)
			}
		}
		t.fingerprint = fingerprint
	}
	t.seq++
	return !open && room <= len(t.items)
}

// Free request slots across the pool, whether an endpoint has nothing to bound them, and a fingerprint of the inputs
func (d *MetricsDetector) headroom(pool datalayer.PoolID, now time.Time) (room int, open bool, fingerprint uint64) {
	var h maphash.Hash
	h.SetSeed(d.seed)
	for _, ep := range d.store.Endpoints(pool, datalayer.ReadyAndHealthy) {
		h.Reset()
		h.WriteString(string(ep.ID()))
		assumed, _ := datalayer.Attribute[int](ep, datalayer.CollectorAssumed)
		maphash.WriteComparable(&h, assumed)
		limit, capped := datalayer.Attribute[int](ep, CollectorCapacity)
		m, ok := datalayer.Attribute[*datalayer.ServerMetrics](ep, datalayer.CollectorMetrics)
		switch {
		case ok && !m.Stale(now, d.staleness):
			maphash.WriteComparable(&h, [4]uint64{1, uint64(m.UpdatedAt.UnixNano()), uint64(m.QueuedRequests), math.Float64bits(m.KVCacheUtilization)})
			if m.KVCacheUtilization < d.thresholds.KVUtilization {
				room += max(0, d.thresholds.QueueDepth-m.QueuedRequests)
				if capped {
					room += max(0, limit-m.RunningRequests)
				}
			}
		case capped:
			maphash.WriteComparable(&h, [2]uint64{2, uint64(limit)})
			room += max(0, limit-assumed)
		default:
			maphash.WriteComparable(&h, [1]uint64{3})
			open = true
		}
		fingerprint += h.Sum64()
	}
	return room, open, fingerprint
}

func (d *MetricsDetector) transitFor(pool datalayer.PoolID) *transit {
	t := d.transit[pool]
	if t == nil {
		t = &transit{items: map[*Item]uint64{}}
		d.transit[pool] = t
	}
	return t
}

// Counts the item against headroom until released or until an input change follows a later observation
func (d *MetricsDetector) Dispatched(pool datalayer.PoolID, item *Item) {
	d.mu.Lock()
	defer d.mu.Unlock()
	t := d.transitFor(pool)
	t.items[item] = t.seq
}

// Stops counting the item against the pool headroom
func (d *MetricsDetector) Released(pool datalayer.PoolID, item *Item) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.transitFor(pool).items, item)
}
