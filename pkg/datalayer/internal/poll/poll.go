// Package poll runs one sampling goroutine per tracked endpoint and fans samples out to collectors
package poll

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nickheyer/nebu/pkg/datalayer"
)

var ErrStarted = errors.New("source already started")

// Calls tick for every tracked endpoint immediately and then every interval, one goroutine each
type Group struct {
	interval time.Duration
	tick     func(ctx context.Context, id datalayer.EndpointID)
	exit     func(id datalayer.EndpointID)

	mu      sync.Mutex
	ctx     context.Context
	cancel  context.CancelFunc
	ids     map[datalayer.EndpointID]struct{}
	workers map[datalayer.EndpointID]*worker
}

type worker struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// Builds a group whose exit hook runs after an endpoint's goroutine has stopped for good
func NewGroup(interval time.Duration, tick func(ctx context.Context, id datalayer.EndpointID), exit func(id datalayer.EndpointID)) *Group {
	return &Group{
		interval: interval,
		tick:     tick,
		exit:     exit,
		ids:      map[datalayer.EndpointID]struct{}{},
		workers:  map[datalayer.EndpointID]*worker{},
	}
}

// Launches a goroutine for every tracked endpoint under ctx
func (g *Group) Start(ctx context.Context) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.ctx != nil {
		return ErrStarted
	}
	g.ctx, g.cancel = context.WithCancel(ctx)
	for id := range g.ids {
		g.launch(id)
	}
	return nil
}

// Stops every goroutine and waits for each to finish its exit hook
func (g *Group) Stop() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.ctx == nil {
		return nil
	}
	g.cancel()
	for id, w := range g.workers {
		<-w.done
		delete(g.workers, id)
	}
	g.ctx, g.cancel = nil, nil
	return nil
}

// Replaces the tracked set, stopping goroutines of dropped endpoints before starting new ones
func (g *Group) Update(ids []datalayer.EndpointID) error {
	next := make(map[datalayer.EndpointID]struct{}, len(ids))
	for _, id := range ids {
		next[id] = struct{}{}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.ids = next
	if g.ctx == nil {
		return nil
	}
	for id, w := range g.workers {
		if _, keep := next[id]; !keep {
			w.cancel()
			<-w.done
			delete(g.workers, id)
		}
	}
	for id := range next {
		if _, running := g.workers[id]; !running {
			g.launch(id)
		}
	}
	return nil
}

func (g *Group) launch(id datalayer.EndpointID) {
	ctx, cancel := context.WithCancel(g.ctx)
	w := &worker{cancel: cancel, done: make(chan struct{})}
	g.workers[id] = w
	go func() {
		defer close(w.done)
		defer g.exit(id)
		defer cancel()
		t := time.NewTicker(g.interval)
		defer t.Stop()
		for {
			g.tick(ctx, id)
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
}

// The subscribed set, read without a lock by sampling goroutines
type Collectors struct {
	mu   sync.Mutex
	list atomic.Pointer[[]datalayer.Collector]
}

// Adds a collector, refusing a second of the same name
func (c *Collectors) Subscribe(col datalayer.Collector) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	cur := c.load()
	for _, have := range cur {
		if have.Name() == col.Name() {
			return fmt.Errorf("%w: %s", datalayer.ErrDuplicateCollector, col.Name())
		}
	}
	next := append(append(make([]datalayer.Collector, 0, len(cur)+1), cur...), col)
	c.list.Store(&next)
	return nil
}

// Runs every collector on the sample and stores each result, joining the errors of collectors that failed
func (c *Collectors) Deliver(store datalayer.Store, id datalayer.EndpointID, sample any) error {
	ep, ok := store.Endpoint(id)
	if !ok {
		return fmt.Errorf("%w: %s", datalayer.ErrUnknownEndpoint, id)
	}
	var errs []error
	for _, col := range c.load() {
		v, err := col.Extract(ep, sample)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", col.Name(), err))
			continue
		}
		store.SetAttribute(id, col.Name(), v)
	}
	return errors.Join(errs...)
}

// Removes every subscribed collector's attribute from the endpoint
func (c *Collectors) Clear(store datalayer.Store, id datalayer.EndpointID) {
	for _, col := range c.load() {
		store.SetAttribute(id, col.Name(), nil)
	}
}

func (c *Collectors) load() []datalayer.Collector {
	if p := c.list.Load(); p != nil {
		return *p
	}
	return nil
}
