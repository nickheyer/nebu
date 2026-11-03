package metrics

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/nickheyer/nebu/pkg/datalayer"
	"github.com/nickheyer/nebu/pkg/flowcontrol"
)

// The band of requests naming no priority, whose depth series every pool carries from its first mirror
const defaultBand = 0

// The prefix cache index, read for its entry count
type Index interface {
	Len() int
}

// Mirrors store endpoint attributes, pool aggregates, flow control depth and the prefix index size into gauges
type Exporter struct {
	reg      *Registry
	store    datalayer.Store
	flow     flowcontrol.Controller
	index    Index
	interval time.Duration

	mu sync.Mutex
	// The pool each endpoint carrying series was last seen in
	endpoints map[datalayer.EndpointID]datalayer.PoolID
	// The bands carrying a depth series per pool
	bands map[datalayer.PoolID]map[int]struct{}
}

// Builds an exporter mirroring store, flow and index every interval
func NewExporter(reg *Registry, store datalayer.Store, flow flowcontrol.Controller, index Index, interval time.Duration) (*Exporter, error) {
	if interval <= 0 {
		return nil, fmt.Errorf("metrics: exporter interval must be positive, got %v", interval)
	}
	if index == nil {
		return nil, fmt.Errorf("metrics: exporter needs the prefix index")
	}
	return &Exporter{
		reg:       reg,
		store:     store,
		flow:      flow,
		index:     index,
		interval:  interval,
		endpoints: map[datalayer.EndpointID]datalayer.PoolID{},
		bands:     map[datalayer.PoolID]map[int]struct{}{},
	}, nil
}

// Mirrors once now and then every interval until ctx ends
func (e *Exporter) Run(ctx context.Context) {
	e.Mirror()
	t := time.NewTicker(e.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			e.Mirror()
		}
	}
}

func every(datalayer.Endpoint) bool { return true }

// The pool aggregates one mirror pass sums
type poolTotals struct {
	ready int
	// Endpoints reporting model server metrics, the KV mean's divisor
	reporting int
	kv        float64
	queued    int
}

// Sets every endpoint, pool and band gauge from the store, flow control and the index, deleting series of departed ones
func (e *Exporter) Mirror() {
	e.mu.Lock()
	defer e.mu.Unlock()
	r := e.reg

	totals := map[datalayer.PoolID]*poolTotals{}
	for _, p := range e.store.Pools() {
		totals[p.ID] = &poolTotals{}
	}
	live := map[datalayer.EndpointID]datalayer.PoolID{}
	for _, ep := range e.store.Endpoints("", every) {
		pool, id := string(ep.Pool()), string(ep.ID())
		live[ep.ID()] = ep.Pool()
		t, ok := totals[ep.Pool()]
		if !ok {
			t = &poolTotals{}
			totals[ep.Pool()] = t
		}
		if m, ok := datalayer.Attribute[*datalayer.ServerMetrics](ep, datalayer.CollectorMetrics); ok && m != nil {
			r.endpointQueued.WithLabelValues(pool, id).Set(float64(m.QueuedRequests))
			r.endpointRunning.WithLabelValues(pool, id).Set(float64(m.RunningRequests))
			r.endpointKV.WithLabelValues(pool, id).Set(m.KVCacheUtilization)
			t.reporting++
			t.kv += m.KVCacheUtilization
			t.queued += m.QueuedRequests
		} else {
			r.endpointQueued.DeleteLabelValues(pool, id)
			r.endpointRunning.DeleteLabelValues(pool, id)
			r.endpointKV.DeleteLabelValues(pool, id)
		}
		assumed, _ := datalayer.Attribute[int](ep, datalayer.CollectorAssumed)
		r.endpointAssumed.WithLabelValues(pool, id).Set(float64(assumed))
		ok = datalayer.ReadyAndHealthy(ep)
		r.endpointReady.WithLabelValues(pool, id).Set(boolGauge(ok))
		if ok {
			t.ready++
		}
	}

	for id, pool := range e.endpoints {
		if now, ok := live[id]; !ok || now != pool {
			e.deleteEndpoint(pool, id)
		}
	}
	e.endpoints = live

	for pool, t := range totals {
		name := string(pool)
		r.poolReady.WithLabelValues(name).Set(float64(t.ready))
		r.poolQueued.WithLabelValues(name).Set(float64(t.queued))
		if t.reporting > 0 {
			r.poolKV.WithLabelValues(name).Set(t.kv / float64(t.reporting))
		} else {
			r.poolKV.DeleteLabelValues(name)
		}
		e.mirrorDepth(pool)
	}
	for pool, bands := range e.bands {
		if _, ok := totals[pool]; ok {
			continue
		}
		r.poolReady.DeleteLabelValues(string(pool))
		r.poolQueued.DeleteLabelValues(string(pool))
		r.poolKV.DeleteLabelValues(string(pool))
		for band := range bands {
			r.queueDepth.DeleteLabelValues(string(pool), strconv.Itoa(band))
		}
		delete(e.bands, pool)
	}
	r.SetPrefixCacheEntries(e.index.Len())
}

// Sets the depth of every band flow control reports for a pool, zeroing the default band and bands that emptied
func (e *Exporter) mirrorDepth(pool datalayer.PoolID) {
	depth := e.flow.Depth(pool)
	seen := e.bands[pool]
	if seen == nil {
		seen = map[int]struct{}{defaultBand: {}}
		e.bands[pool] = seen
	}
	for band := range seen {
		if _, ok := depth[band]; !ok {
			e.reg.queueDepth.WithLabelValues(string(pool), strconv.Itoa(band)).Set(0)
		}
	}
	for band, n := range depth {
		e.reg.queueDepth.WithLabelValues(string(pool), strconv.Itoa(band)).Set(float64(n))
		seen[band] = struct{}{}
	}
}

// Deletes every series labeled with the endpoint under the pool it was recorded in
func (e *Exporter) deleteEndpoint(pool datalayer.PoolID, id datalayer.EndpointID) {
	p, ep := string(pool), string(id)
	for _, g := range []interface{ DeleteLabelValues(...string) bool }{
		e.reg.endpointQueued, e.reg.endpointRunning, e.reg.endpointKV, e.reg.endpointAssumed, e.reg.endpointReady, e.reg.scrapeErrors,
	} {
		g.DeleteLabelValues(p, ep)
	}
}

func boolGauge(b bool) float64 {
	if b {
		return 1
	}
	return 0
}
