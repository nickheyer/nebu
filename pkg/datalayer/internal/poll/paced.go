package poll

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nickheyer/nebu/pkg/datalayer"
)

// Time between two samples of one endpoint for each demand on it
type Pace struct {
	// While a routing decision waits on the endpoint, also the shortest gap between two of its samples
	Deciding time.Duration
	// While requests are in flight on it or its last sample showed work
	Working time.Duration
	// While nothing asks for it
	Idle time.Duration
}

// Calls tick for every tracked endpoint at the pace its demand sets, one goroutine each
type Paced struct {
	pace Pace
	tick func(ctx context.Context, id datalayer.EndpointID) bool
	exit func(id datalayer.EndpointID)

	mu      sync.Mutex
	ctx     context.Context
	cancel  context.CancelFunc
	ids     map[datalayer.EndpointID]struct{}
	workers map[datalayer.EndpointID]*pacer

	// Guards demand apart from the lifecycle lock, so a stopping worker never waits on it
	dmu    sync.Mutex
	demand map[datalayer.EndpointID]*demand
}

// Leases held on one endpoint
type demand struct {
	deciding int
	serving  int
	// When the sample that sees the last decision's request land is due, zero when none is owed
	settle time.Time
}

// One endpoint's sampling goroutine and the start of its latest finished sample
type pacer struct {
	cancel context.CancelFunc
	done   chan struct{}
	wake   chan struct{}
	asked  atomic.Bool

	mu       sync.Mutex
	began    time.Time
	finished chan struct{}
}

// Builds a group whose tick reports whether the endpoint showed work and whose exit hook runs after its goroutine stopped
func NewPaced(pace Pace, tick func(ctx context.Context, id datalayer.EndpointID) bool, exit func(id datalayer.EndpointID)) *Paced {
	return &Paced{
		pace:    pace,
		tick:    tick,
		exit:    exit,
		ids:     map[datalayer.EndpointID]struct{}{},
		workers: map[datalayer.EndpointID]*pacer{},
		demand:  map[datalayer.EndpointID]*demand{},
	}
}

// Launches a goroutine for every tracked endpoint under ctx
func (g *Paced) Start(ctx context.Context) error {
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

// Stops every goroutine and waits for each to finish its exit hook, leases staying held
func (g *Paced) Stop() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.ctx == nil {
		return nil
	}
	g.cancel()
	for id, w := range g.workers {
		<-w.done
		delete(g.workers, id)
		g.unsettle(id)
	}
	g.ctx, g.cancel = nil, nil
	return nil
}

// Replaces the tracked set, stopping goroutines of dropped endpoints before starting new ones
func (g *Paced) Update(ids []datalayer.EndpointID) error {
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
		if _, keep := next[id]; keep {
			continue
		}
		w.cancel()
		<-w.done
		delete(g.workers, id)
		g.unsettle(id)
	}
	for id := range next {
		if _, running := g.workers[id]; !running {
			g.launch(id)
		}
	}
	return nil
}

// Samples the endpoints at the deciding pace until released, first waiting a tick for any unsampled within the working pace
func (g *Paced) Deciding(ctx context.Context, ids []datalayer.EndpointID) (release func()) {
	now := time.Now()
	workers := g.pacers(ids)
	g.dmu.Lock()
	for _, id := range ids {
		g.need(id).deciding++
	}
	g.dmu.Unlock()
	for _, w := range workers {
		if w != nil {
			w.nudge()
		}
	}
	wait, cancel := context.WithTimeout(ctx, g.pace.Deciding)
	await(wait, workers, now.Add(-g.pace.Deciding), now.Add(-g.pace.Working))
	cancel()
	var once sync.Once
	return func() {
		once.Do(func() {
			workers := g.pacers(ids)
			settle := time.Now().Add(g.pace.Working)
			g.dmu.Lock()
			for i, id := range ids {
				d := g.demand[id]
				if d.deciding--; d.deciding == 0 && workers[i] != nil {
					d.settle = settle
				}
				g.forget(id, d)
			}
			g.dmu.Unlock()
			for _, w := range workers {
				if w != nil {
					w.nudge()
				}
			}
		})
	}
}

// Samples the endpoint at the working pace until the release
func (g *Paced) Serving(id datalayer.EndpointID) (release func()) {
	g.dmu.Lock()
	g.need(id).serving++
	g.dmu.Unlock()
	if w := g.pacers([]datalayer.EndpointID{id})[0]; w != nil {
		w.nudge()
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			g.dmu.Lock()
			d := g.demand[id]
			d.serving--
			g.forget(id, d)
			g.dmu.Unlock()
		})
	}
}

// Samples the endpoints again without waiting for the samples
func (g *Paced) Ask(ids []datalayer.EndpointID) {
	for _, w := range g.pacers(ids) {
		if w != nil {
			w.ask()
		}
	}
}

// Samples the endpoints now, returning once each has finished a sample or ctx ends
func (g *Paced) Sample(ctx context.Context, ids []datalayer.EndpointID) {
	fresh := time.Now().Add(-g.pace.Deciding)
	await(ctx, g.pacers(ids), fresh, fresh)
}

// The goroutine of each endpoint in order, nil for one that has none
func (g *Paced) pacers(ids []datalayer.EndpointID) []*pacer {
	out := make([]*pacer, len(ids))
	g.mu.Lock()
	defer g.mu.Unlock()
	for i, id := range ids {
		out[i] = g.workers[id]
	}
	return out
}

// The demand entry of an endpoint, made on first use, with the demand lock held
func (g *Paced) need(id datalayer.EndpointID) *demand {
	d := g.demand[id]
	if d == nil {
		d = &demand{}
		g.demand[id] = d
	}
	return d
}

// Drops a demand entry nothing holds any more, with the demand lock held
func (g *Paced) forget(id datalayer.EndpointID, d *demand) {
	if d.deciding == 0 && d.serving == 0 && d.settle.IsZero() {
		delete(g.demand, id)
	}
}

// Drops the settle sample owed to an endpoint whose goroutine is gone
func (g *Paced) unsettle(id datalayer.EndpointID) {
	g.dmu.Lock()
	defer g.dmu.Unlock()
	if d := g.demand[id]; d != nil {
		d.settle = time.Time{}
		g.forget(id, d)
	}
}

// Counts the sample starting now as the settle sample once that one is due
func (g *Paced) begin(id datalayer.EndpointID, now time.Time) {
	g.dmu.Lock()
	defer g.dmu.Unlock()
	if d := g.demand[id]; d != nil && !d.settle.IsZero() && !now.Before(d.settle) {
		d.settle = time.Time{}
		g.forget(id, d)
	}
}

// When the sample after the one begun at began is due under the demand held now
func (g *Paced) due(id datalayer.EndpointID, began time.Time, working, asked bool) time.Time {
	g.dmu.Lock()
	defer g.dmu.Unlock()
	d := g.demand[id]
	switch {
	case asked || d != nil && d.deciding > 0:
		return began.Add(g.pace.Deciding)
	case working || d != nil && d.serving > 0:
		return began.Add(g.pace.Working)
	case d != nil && !d.settle.IsZero():
		return d.settle
	}
	return began.Add(g.pace.Idle)
}

func (g *Paced) launch(id datalayer.EndpointID) {
	ctx, cancel := context.WithCancel(g.ctx)
	w := &pacer{cancel: cancel, done: make(chan struct{}), wake: make(chan struct{}, 1), finished: make(chan struct{})}
	g.workers[id] = w
	go func() {
		defer close(w.done)
		defer g.exit(id)
		defer cancel()
		for {
			w.asked.Store(false)
			began := time.Now()
			g.begin(id, began)
			working := g.tick(ctx, id)
			w.finish(began)
			if !g.rest(ctx, id, w, began, working) {
				return
			}
		}
	}()
}

// Waits until the endpoint's next sample is due, a wake reading its demand again, false once ctx ends
func (g *Paced) rest(ctx context.Context, id datalayer.EndpointID, w *pacer, began time.Time, working bool) bool {
	for {
		wait := time.Until(g.due(id, began, working, w.asked.Load()))
		if wait <= 0 {
			return ctx.Err() == nil
		}
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return false
		case <-w.wake:
			t.Stop()
		case <-t.C:
			return true
		}
	}
}

// Asks goroutines last sampled before fresh for another, then waits for those sampled before enough until ctx ends
func await(ctx context.Context, workers []*pacer, fresh, enough time.Time) {
	for _, w := range workers {
		if w != nil && !w.since(fresh) {
			w.ask()
		}
	}
	for _, w := range workers {
		if w != nil && !w.wait(ctx, enough) {
			return
		}
	}
}

// Records a finished sample and wakes everything waiting for one
func (w *pacer) finish(began time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.began = began
	close(w.finished)
	w.finished = make(chan struct{})
}

// Whether the latest finished sample began at or after t
func (w *pacer) since(t time.Time) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return !w.began.Before(t)
}

// Blocks until a sample begun at or after since has finished or the goroutine stopped, false when ctx ended first
func (w *pacer) wait(ctx context.Context, since time.Time) bool {
	for {
		w.mu.Lock()
		ok, next := !w.began.Before(since), w.finished
		w.mu.Unlock()
		if ok {
			return true
		}
		select {
		case <-next:
		case <-w.done:
			return true
		case <-ctx.Done():
			return false
		}
	}
}

// Has the goroutine sample again one deciding tick after its last sample at the latest
func (w *pacer) ask() {
	w.asked.Store(true)
	w.nudge()
}

// Has the goroutine read its demand again
func (w *pacer) nudge() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}
