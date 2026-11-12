package flowcontrol

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nickheyer/nebu/pkg/datalayer"
)

// Admits requests per pool by band priority and deficit round robin, dispatching each once its placement finds room
type FlowController struct {
	cfg      Config
	observer Observer
	meter    *meter
	limits   *limits
	stop     chan struct{}
	wg       sync.WaitGroup

	mu     sync.Mutex
	pools  map[datalayer.PoolID]*poolQueue
	closed bool
}

var _ Controller = (*FlowController)(nil)

// Builds a controller after checking the config, a nil observer meaning no admission reports
func NewController(cfg Config, observer Observer) (*FlowController, error) {
	switch {
	case cfg.QueueCapacity < 1:
		return nil, fmt.Errorf("flowcontrol: queue capacity must be at least 1, got %d", cfg.QueueCapacity)
	case cfg.QueueTimeout <= 0:
		return nil, fmt.Errorf("flowcontrol: queue timeout must be positive, got %s", cfg.QueueTimeout)
	case cfg.Tick <= 0:
		return nil, fmt.Errorf("flowcontrol: tick must be positive, got %s", cfg.Tick)
	}
	m, err := newMeter(cfg)
	if err != nil {
		return nil, err
	}
	return &FlowController{
		cfg:      cfg,
		observer: observer,
		meter:    m,
		limits:   &limits{inflight: map[string]int{}},
		stop:     make(chan struct{}),
		pools:    map[datalayer.PoolID]*poolQueue{},
	}, nil
}

// Dispatches once the item's placement finds room, otherwise sheds, refuses, or waits for room, timeout, ctx end or close
func (c *FlowController) Admit(ctx context.Context, item *Item) (Permit, error) {
	if item.Place == nil {
		return nil, errors.New("flowcontrol: the item carries no placement")
	}
	if err := ctx.Err(); err != nil {
		c.observe(item, 0, OutcomeCanceled)
		return nil, err
	}
	item.EnqueuedAt = time.Now()
	if err := c.meter.admit(item.Flow.FairnessID, item.EnqueuedAt); err != nil {
		c.observe(item, 0, OutcomeQuota)
		return nil, err
	}
	p := c.pool(item.Pool)
	if p == nil {
		c.observe(item, 0, OutcomeClosed)
		return nil, context.Canceled
	}
	w, pm, outcome, err := p.admit(item)
	switch {
	case err != nil:
		c.observe(item, 0, outcome)
		return nil, err
	case pm != nil:
		c.observe(item, pm.waited, outcome)
		return pm, nil
	}
	return c.wait(ctx, p, w)
}

// Blocks until the waiter resolves, abandoning it when ctx ends or the timeout passes unless its placement is under way
func (c *FlowController) wait(ctx context.Context, p *poolQueue, w *waiter) (Permit, error) {
	timer := time.NewTimer(c.cfg.QueueTimeout - time.Since(w.item.EnqueuedAt))
	defer timer.Stop()
	var cause error
	var outcome string
	select {
	case <-w.done:
		return c.settle(w, nil, "")
	case <-ctx.Done():
		cause, outcome = ctx.Err(), OutcomeCanceled
	case <-timer.C:
		cause, outcome = ErrQueueTimeout, OutcomeTimeout
	}
	if !p.abandon(w) {
		<-w.done
	}
	return c.settle(w, cause, outcome)
}

// Turns a resolved waiter into its permit or its error, an abandoned one answering the cause given
func (c *FlowController) settle(w *waiter, cause error, outcome string) (Permit, error) {
	waited := time.Since(w.item.EnqueuedAt)
	switch w.state {
	case stateDispatched:
		c.observe(w.item, w.permit.waited, OutcomeDispatched)
		return w.permit, nil
	case stateAbandoned:
		c.observe(w.item, waited, outcome)
		return nil, cause
	case stateShed:
		c.observe(w.item, waited, OutcomeShed)
		return nil, w.err
	case stateUnplaced:
		c.observe(w.item, waited, OutcomeUnplaced)
		return nil, w.err
	}
	c.observe(w.item, waited, OutcomeClosed)
	return nil, context.Canceled
}

func (c *FlowController) observe(item *Item, waited time.Duration, outcome string) {
	if c.observer != nil {
		c.observer.OnAdmit(item.Pool, item.Priority, waited, outcome)
	}
}

// The pool queue, created with its dispatcher on first use, nil once the controller closed
func (c *FlowController) pool(id datalayer.PoolID) *poolQueue {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	p := c.pools[id]
	if p == nil {
		p = &poolQueue{id: id, cfg: c.cfg, meter: c.meter, limits: c.limits, wakeAll: c.wakeAll, wake: make(chan struct{}, 1), round: 1, debts: map[FlowKey]int{}}
		c.pools[id] = p
		c.wg.Go(func() { p.run(c.stop) })
	}
	return p
}

func (c *FlowController) lookup(id datalayer.PoolID) *poolQueue {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.pools[id]
}

// Starts a new round on every pool, after a route slot freed that any pool's waiters may take
func (c *FlowController) wakeAll() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, p := range c.pools {
		p.advance()
	}
}

// Waiting items per band of the pool, bands without waiting items omitted
func (c *FlowController) Depth(pool datalayer.PoolID) map[int]int {
	out := map[int]int{}
	p := c.lookup(pool)
	if p == nil {
		return out
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, b := range p.bands {
		out[b.priority] = b.n
	}
	return out
}

// Dispatched items of the pool not yet released
func (c *FlowController) InFlight(pool datalayer.PoolID) int {
	p := c.lookup(pool)
	if p == nil {
		return 0
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.inflight
}

// Metered requests and tokens of the fairness id in its current window
func (c *FlowController) Usage(fairnessID string) Usage {
	return c.meter.usage(fairnessID, time.Now())
}

// Stops every dispatcher and fails every waiting item with context canceled
func (c *FlowController) Close() {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	pools := slices.Collect(maps.Values(c.pools))
	c.mu.Unlock()
	close(c.stop)
	c.wg.Wait()
	for _, p := range pools {
		p.shut()
	}
}

// Items in flight per route across every pool, taken before dispatch and given back on release
type limits struct {
	mu       sync.Mutex
	inflight map[string]int
}

// Whether the item's route has its limit in flight
func (l *limits) full(item *Item) bool {
	if item.RouteLimit <= 0 {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.inflight[item.Route] >= item.RouteLimit
}

// Takes a route slot for the item, false when its route is at its limit
func (l *limits) take(item *Item) bool {
	if item.RouteLimit <= 0 {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.inflight[item.Route] >= item.RouteLimit {
		return false
	}
	l.inflight[item.Route]++
	return true
}

// Gives the item's route slot back
func (l *limits) release(item *Item) {
	if item.RouteLimit <= 0 {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if n := l.inflight[item.Route] - 1; n > 0 {
		l.inflight[item.Route] = n
	} else {
		delete(l.inflight, item.Route)
	}
}

// The bands, counters and dispatcher of one pool
type poolQueue struct {
	id      datalayer.PoolID
	cfg     Config
	meter   *meter
	limits  *limits
	wakeAll func()
	wake    chan struct{}

	mu sync.Mutex
	// Bands holding waiting items, highest priority first
	bands    []*band
	waiting  int
	inflight int
	// Counts releases and ticks, the only events after which an item refused room is placed again
	round uint64
	// The round a placement found the pool full in, nothing placed for the rest of it
	full uint64
	pass uint64
	// Token overruns of flows that left their band, charged when the flow returns
	debts  map[FlowKey]int
	closed bool
}

// The route cap refusing an item, room like any other
var errRouteFull = fmt.Errorf("%w: its route is at the in flight limit", ErrNoRoom)

// Takes the item's route slot and runs its placement with the pool unlocked, answering the round it began in
func (p *poolQueue) placeLocked(item *Item) (uint64, error) {
	round := p.round
	if !p.limits.take(item) {
		return round, errRouteFull
	}
	p.mu.Unlock()
	err := item.Place()
	p.mu.Lock()
	if err != nil {
		p.limits.release(item)
	}
	return round, err
}

// Runs the placement and records a full pool, answering the round an item refused alone sits out
func (p *poolQueue) tryLocked(item *Item) (uint64, error) {
	began, err := p.placeLocked(item)
	if errors.Is(err, ErrPoolFull) {
		p.full = began
		return 0, err
	}
	return began, err
}

// Places an item nothing waits ahead of, grants it on room, and otherwise sheds, refuses or queues it
func (p *poolQueue) admit(item *Item) (*waiter, *permit, string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil, nil, OutcomeClosed, context.Canceled
	}
	sheddable := item.Priority <= p.cfg.SheddableBelow
	var held uint64
	switch {
	case p.full == p.round:
		if sheddable {
			return nil, nil, OutcomeShed, fmt.Errorf("%w: %w", ErrShed, ErrPoolFull)
		}
	case !p.waitingAtOrAbove(item.Priority):
		began, err := p.tryLocked(item)
		switch {
		case err == nil:
			return nil, p.grantLocked(item, 0, time.Now()), OutcomeDispatched, nil
		case !errors.Is(err, ErrNoRoom):
			return nil, nil, OutcomeUnplaced, err
		case sheddable:
			return nil, nil, OutcomeShed, fmt.Errorf("%w: %w", ErrShed, err)
		case p.closed:
			return nil, nil, OutcomeClosed, context.Canceled
		}
		held = began
	}
	if p.waiting >= p.cfg.QueueCapacity && !p.displaceLocked(item.Priority) {
		return nil, nil, OutcomeQueueFull, ErrQueueFull
	}
	w := &waiter{item: item, cost: chargeOf(item), held: held, done: make(chan struct{})}
	p.enqueueLocked(w)
	p.signal()
	return w, nil, "", nil
}

// Links the waiter into its band's flow, a returning flow starting with its debt capped at one quantum
func (p *poolQueue) enqueueLocked(w *waiter) {
	b := p.band(w.item.Priority, true)
	f, created := b.add(w, w.item.Flow.FairnessID)
	if created {
		if d := p.debts[w.item.Flow]; d > 0 {
			f.deficit = -min(d, b.quantum())
			delete(p.debts, w.item.Flow)
		}
	}
	p.waiting++
}

// Sheds the newest item of the lowest band not being placed when that band ranks below the arriving priority
func (p *poolQueue) displaceLocked(priority int) bool {
	if len(p.bands) == 0 {
		return false
	}
	low := p.bands[len(p.bands)-1]
	if low.priority >= priority {
		return false
	}
	var newest *waiter
	for _, f := range low.flows {
		w := f.tail
		for w != nil && w.state != stateWaiting {
			w = w.prev
		}
		if w != nil && (newest == nil || w.item.EnqueuedAt.After(newest.item.EnqueuedAt)) {
			newest = w
		}
	}
	if newest == nil {
		return false
	}
	p.unlinkLocked(newest)
	newest.state, newest.err = stateShed, fmt.Errorf("%w: displaced by a higher priority band", ErrShed)
	close(newest.done)
	return true
}

func (p *poolQueue) waitingAtOrAbove(priority int) bool {
	return len(p.bands) > 0 && p.bands[0].priority >= priority
}

// The band for a priority, inserted in descending order when missing
func (p *poolQueue) band(priority int, create bool) *band {
	i, found := slices.BinarySearchFunc(p.bands, priority, func(b *band, prio int) int { return cmp.Compare(prio, b.priority) })
	if found {
		return p.bands[i]
	}
	if !create {
		return nil
	}
	b := newBand(priority)
	p.bands = slices.Insert(p.bands, i, b)
	return b
}

// Removes a waiter from its band, keeping an emptied flow's debt and dropping the band once it empties
func (p *poolQueue) unlinkLocked(w *waiter) {
	b := w.flow.band
	if debt := b.remove(w); debt > 0 {
		p.debts[w.item.Flow] = min(debt, maxCost)
	}
	p.waiting--
	if b.n == 0 {
		p.bands = slices.DeleteFunc(p.bands, func(x *band) bool { return x == b })
	}
}

// Pulls a waiting item out of its queue, false when it resolved or its placement will resolve it
func (p *poolQueue) abandon(w *waiter) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch w.state {
	case stateWaiting:
		p.unlinkLocked(w)
		w.state = stateAbandoned
		return true
	case statePlacing:
		w.quit = true
	}
	return false
}

// Counts the item in flight and meters its request
func (p *poolQueue) grantLocked(item *Item, charged int, now time.Time) *permit {
	p.inflight++
	p.meter.count(item.Flow.FairnessID, now)
	return &permit{pool: p, item: item, charged: charged, waited: now.Sub(item.EnqueuedAt)}
}

// Gives back a dispatched item's room and starts a new round, on every pool when a route slot freed
func (p *poolQueue) release(item *Item) {
	p.mu.Lock()
	p.inflight--
	p.limits.release(item)
	p.mu.Unlock()
	if item.RouteLimit > 0 {
		p.wakeAll()
		return
	}
	p.advance()
}

// Starts a new round and wakes the dispatcher
func (p *poolQueue) advance() {
	p.mu.Lock()
	p.round++
	p.mu.Unlock()
	p.signal()
}

// Charges the difference between settled and estimated tokens to the live flow, or to its debt once it left
func (p *poolQueue) settle(key FlowKey, delta int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if b := p.band(key.Priority, false); b != nil {
		if f := b.flows[key.FairnessID]; f != nil {
			f.deficit = max(f.deficit-delta, -b.quantum())
			return
		}
	}
	if d := min(p.debts[key]+delta, maxCost); d > 0 {
		p.debts[key] = d
	} else {
		delete(p.debts, key)
	}
}

func (p *poolQueue) signal() {
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

// Runs a dispatch pass on every wake and a new round on each tick while items wait, until stop closes
func (p *poolQueue) run(stop <-chan struct{}) {
	ticker := time.NewTicker(p.cfg.Tick)
	defer ticker.Stop()
	var tick <-chan time.Time
	for {
		ticked := false
		select {
		case <-stop:
			return
		case <-p.wake:
		case <-tick:
			ticked = true
		}
		p.mu.Lock()
		if ticked {
			p.round++
		}
		p.dispatchLocked()
		tick = nil
		if p.waiting > 0 {
			tick = ticker.C
		}
		p.mu.Unlock()
	}
}

// Places waiting items highest band first until the pool is full, starting over from the top when a round begins
func (p *poolQueue) dispatchLocked() {
	p.pass++
	for round := p.round; p.waiting > 0 && p.full != p.round; {
		if round != p.round {
			round = p.round
			p.pass++
		}
		w := p.next()
		if w == nil {
			return
		}
		w.state = statePlacing
		began, err := p.tryLocked(w.item)
		if err != nil {
			w.flow.band.unpick()
		}
		switch {
		case err == nil:
			w.flow.deficit -= w.cost
			p.unlinkLocked(w)
			w.permit = p.grantLocked(w.item, w.cost, time.Now())
			w.state = stateDispatched
		case !errors.Is(err, ErrNoRoom):
			p.unlinkLocked(w)
			w.state, w.err = stateUnplaced, err
		case w.quit:
			p.unlinkLocked(w)
			w.state = stateAbandoned
		case w.item.Priority <= p.cfg.SheddableBelow:
			p.unlinkLocked(w)
			w.state, w.err = stateShed, fmt.Errorf("%w: %w", ErrShed, err)
		default:
			w.state, w.held = stateWaiting, began
			continue
		}
		close(w.done)
	}
}

// The next item by strict priority between bands, skipping bands already found fully held this pass
func (p *poolQueue) next() *waiter {
	for _, b := range p.bands {
		if b.heldPass == p.pass {
			continue
		}
		if w := b.pick(p.eligible); w != nil {
			return w
		}
		b.heldPass = p.pass
	}
	return nil
}

// Whether the item may be placed, one its own placement refused sitting out the round it was refused in
func (p *poolQueue) eligible(w *waiter) bool {
	return w.held < p.round
}

// Fails every waiting item with context canceled and refuses later admissions
func (p *poolQueue) shut() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	for _, b := range p.bands {
		for _, f := range b.flows {
			for w := f.head; w != nil; w = w.next {
				w.state = stateClosed
				close(w.done)
			}
		}
	}
	p.bands = nil
	p.waiting = 0
}

// Room a dispatched item holds until released
type permit struct {
	pool *poolQueue
	item *Item
	// Tokens the dispatch charged the flow, zero when granted without queueing
	charged int
	waited  time.Duration
	done    atomic.Bool
}

func (pm *permit) Release() {
	if pm.done.CompareAndSwap(false, true) {
		pm.pool.release(pm.item)
	}
}

// Meters the tokens used and charges a queued dispatch the difference against its estimate
func (pm *permit) Settle(promptTokens, outputTokens int) {
	pm.pool.meter.charge(pm.item.Flow.FairnessID, promptTokens, outputTokens, time.Now())
	if pm.charged > 0 {
		pm.pool.settle(pm.item.Flow, min(max(promptTokens+outputTokens, 1), maxCost)-pm.charged)
	}
}

func (pm *permit) Waited() time.Duration { return pm.waited }
