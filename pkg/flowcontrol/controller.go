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

// Admits requests per pool under strict priority between bands and deficit round robin between flows
type FlowController struct {
	cfg      Config
	detector SaturationDetector
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
func NewController(cfg Config, detector SaturationDetector, observer Observer) (*FlowController, error) {
	switch {
	case cfg.QueueCapacity < 1:
		return nil, fmt.Errorf("flowcontrol: queue capacity must be at least 1, got %d", cfg.QueueCapacity)
	case cfg.QueueTimeout <= 0:
		return nil, fmt.Errorf("flowcontrol: queue timeout must be positive, got %s", cfg.QueueTimeout)
	case cfg.Tick <= 0:
		return nil, fmt.Errorf("flowcontrol: tick must be positive, got %s", cfg.Tick)
	case detector == nil:
		return nil, errors.New("flowcontrol: a saturation detector is required")
	}
	m, err := newMeter(cfg)
	if err != nil {
		return nil, err
	}
	return &FlowController{
		cfg:      cfg,
		detector: detector,
		observer: observer,
		meter:    m,
		limits:   &limits{inflight: map[string]int{}},
		stop:     make(chan struct{}),
		pools:    map[datalayer.PoolID]*poolQueue{},
	}, nil
}

// Dispatches at once when allowed, otherwise sheds, refuses, or waits for dispatch, timeout, ctx end or close
func (c *FlowController) Admit(ctx context.Context, item *Item) (Permit, error) {
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

// Returns the dispatched item to the head of its flow, held for the pass its release wakes, and waits again
func (c *FlowController) Requeue(ctx context.Context, pm Permit, item *Item) (Permit, error) {
	held, ok := pm.(*permit)
	if !ok || held.item != item {
		return nil, errors.New("flowcontrol: the permit was not issued for the item")
	}
	if !held.done.CompareAndSwap(false, true) {
		return nil, errors.New("flowcontrol: the permit was already released")
	}
	w, err := held.pool.requeue(item)
	if err != nil {
		c.observe(item, time.Since(item.EnqueuedAt), OutcomeClosed)
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		if held.pool.abandon(w) {
			c.observe(item, time.Since(item.EnqueuedAt), OutcomeCanceled)
			return nil, err
		}
	}
	return c.wait(ctx, held.pool, w)
}

// Blocks until the waiter resolves, pulling it from its queue when ctx ends or the timeout passes
func (c *FlowController) wait(ctx context.Context, p *poolQueue, w *waiter) (Permit, error) {
	timer := time.NewTimer(c.cfg.QueueTimeout - time.Since(w.item.EnqueuedAt))
	defer timer.Stop()
	var cause error
	var outcome string
	select {
	case <-w.done:
		return c.settle(w)
	case <-ctx.Done():
		cause, outcome = ctx.Err(), OutcomeCanceled
	case <-timer.C:
		cause, outcome = ErrQueueTimeout, OutcomeTimeout
	}
	if p.abandon(w) {
		c.observe(w.item, time.Since(w.item.EnqueuedAt), outcome)
		return nil, cause
	}
	if w.state == stateDispatched && outcome == OutcomeCanceled {
		w.permit.Release()
		c.observe(w.item, w.permit.waited, OutcomeCanceled)
		return nil, cause
	}
	return c.settle(w)
}

// Turns a resolved waiter into its permit, ErrShed when displaced, or context canceled when close failed it
func (c *FlowController) settle(w *waiter) (Permit, error) {
	waited := time.Since(w.item.EnqueuedAt)
	switch w.state {
	case stateDispatched:
		c.observe(w.item, w.permit.waited, OutcomeDispatched)
		return w.permit, nil
	case stateShed:
		c.observe(w.item, waited, OutcomeShed)
		return nil, fmt.Errorf("%w: displaced by a higher priority band", ErrShed)
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
		p = &poolQueue{id: id, cfg: c.cfg, detector: c.detector, meter: c.meter, limits: c.limits, wakeAll: c.wakeAll, wake: make(chan struct{}, 1), debts: map[FlowKey]int{}}
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

// Wakes every pool dispatcher, after a route slot freed that any pool's waiters may take
func (c *FlowController) wakeAll() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, p := range c.pools {
		p.signal()
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

func hasObjective(item *Item) bool { return item.TTFT > 0 || item.TPOT > 0 }

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
	id       datalayer.PoolID
	cfg      Config
	detector SaturationDetector
	meter    *meter
	limits   *limits
	wakeAll  func()
	wake     chan struct{}

	mu sync.Mutex
	// Bands holding waiting items, highest priority first
	bands    []*band
	waiting  int
	inflight int
	pass     uint64
	// Token overruns of flows that left their band, charged when the flow returns
	debts  map[FlowKey]int
	closed bool
}

// Grants, sheds, refuses or queues one item under the pool lock
func (p *poolQueue) admit(item *Item) (*waiter, *permit, string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil, nil, OutcomeClosed, context.Canceled
	}
	hard := p.detector.HardSaturated(p.id, item)
	capped := p.limits.full(item)
	if !hard && !capped && !p.waitingAtOrAbove(item.Priority) && !(hasObjective(item) && p.detector.SoftSaturated(p.id, item)) && p.limits.take(item) {
		return nil, p.grantLocked(item, 0, time.Now()), OutcomeDispatched, nil
	}
	if (hard || capped) && item.Priority <= p.cfg.SheddableBelow {
		return nil, nil, OutcomeShed, ErrShed
	}
	if p.waiting >= p.cfg.QueueCapacity && !p.displaceLocked(item.Priority) {
		return nil, nil, OutcomeQueueFull, ErrQueueFull
	}
	w := &waiter{item: item, cost: chargeOf(item), done: make(chan struct{})}
	p.enqueueLocked(w, false)
	p.signal()
	return w, nil, "", nil
}

// Gives the item's capacity back and queues it at the head of its flow, held through the pass the release wakes
func (p *poolQueue) requeue(item *Item) (*waiter, error) {
	p.mu.Lock()
	p.releaseLocked(item)
	if p.closed {
		p.mu.Unlock()
		p.wakeAfterRelease(item)
		return nil, context.Canceled
	}
	w := &waiter{item: item, cost: chargeOf(item), done: make(chan struct{}), heldPass: p.pass + 1}
	p.enqueueLocked(w, true)
	p.mu.Unlock()
	p.wakeAfterRelease(item)
	return w, nil
}

// Links the waiter into its band's flow, a returning flow starting with its debt capped at one quantum
func (p *poolQueue) enqueueLocked(w *waiter, front bool) {
	b := p.band(w.item.Priority, true)
	f, created := b.add(w, w.item.Flow.FairnessID, front)
	if created {
		if d := p.debts[w.item.Flow]; d > 0 {
			f.deficit = -min(d, b.quantum())
			delete(p.debts, w.item.Flow)
		}
	}
	p.waiting++
}

// Sheds the newest item of the lowest band when that band ranks below the arriving priority
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
		if f.tail != nil && (newest == nil || f.tail.item.EnqueuedAt.After(newest.item.EnqueuedAt)) {
			newest = f.tail
		}
	}
	p.unlinkLocked(newest)
	newest.state = stateShed
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

// Pulls a still waiting item out of its queue, false when it already resolved
func (p *poolQueue) abandon(w *waiter) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if w.state != stateWaiting {
		return false
	}
	p.unlinkLocked(w)
	w.state = stateAbandoned
	return true
}

// Counts the item in flight, meters its request and tells the detector it now holds capacity
func (p *poolQueue) grantLocked(item *Item, charged int, now time.Time) *permit {
	p.inflight++
	p.detector.Dispatched(p.id, item)
	p.meter.count(item.Flow.FairnessID, now)
	return &permit{pool: p, item: item, charged: charged, waited: now.Sub(item.EnqueuedAt)}
}

// Gives back the capacity of a dispatched item and wakes the dispatchers its route slot may unblock
func (p *poolQueue) release(item *Item) {
	p.mu.Lock()
	p.releaseLocked(item)
	p.mu.Unlock()
	p.wakeAfterRelease(item)
}

func (p *poolQueue) releaseLocked(item *Item) {
	p.inflight--
	p.detector.Released(p.id, item)
	p.limits.release(item)
}

// Wakes every pool after a routed item freed its slot, this pool alone otherwise
func (p *poolQueue) wakeAfterRelease(item *Item) {
	if item.RouteLimit > 0 {
		p.wakeAll()
		return
	}
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

// Runs a dispatch pass on every wake and on each tick while items wait, until stop closes
func (p *poolQueue) run(stop <-chan struct{}) {
	ticker := time.NewTicker(p.cfg.Tick)
	defer ticker.Stop()
	var tick <-chan time.Time
	for {
		select {
		case <-stop:
			return
		case <-p.wake:
		case <-tick:
		}
		p.mu.Lock()
		p.dispatchLocked()
		tick = nil
		if p.waiting > 0 {
			tick = ticker.C
		}
		p.mu.Unlock()
	}
}

// Dispatches highest band first until the pool hard saturates or every remaining item is held
func (p *poolQueue) dispatchLocked() {
	p.pass++
	for p.waiting > 0 {
		if p.detector.HardSaturated(p.id, p.front()) {
			return
		}
		w := p.next()
		if w == nil {
			return
		}
		if !p.limits.take(w.item) {
			w.heldPass = p.pass
			continue
		}
		w.flow.deficit -= w.cost
		p.unlinkLocked(w)
		w.permit = p.grantLocked(w.item, w.cost, time.Now())
		w.state = stateDispatched
		close(w.done)
	}
}

// The oldest item of the highest band's current flow, the pool's next candidate before fairness and holds apply
func (p *poolQueue) front() *Item {
	return p.bands[0].cur.head.item
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

// Whether the item can go now, a capped route or an objective no endpoint meets holding it back
func (p *poolQueue) eligible(w *waiter) bool {
	if w.heldPass >= p.pass {
		return false
	}
	if p.limits.full(w.item) {
		return false
	}
	if hasObjective(w.item) && p.detector.SoftSaturated(p.id, w.item) {
		w.heldPass = p.pass
		return false
	}
	return true
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

// Capacity a dispatched item holds until released or requeued once
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
