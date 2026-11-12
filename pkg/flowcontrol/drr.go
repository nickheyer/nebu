package flowcontrol

import "math/bits"

// Largest token cost charged to one item so deficit counters stay far from overflow
const maxCost = 1 << 24

type waitState int

const (
	stateWaiting waitState = iota
	// Out of the pool lock while its placement runs, still linked in its flow
	statePlacing
	stateDispatched
	stateAbandoned
	stateShed
	stateUnplaced
	stateClosed
)

// One queued item, its links inside its flow, and how it resolved
type waiter struct {
	item *Item
	cost int
	flow *flow
	prev *waiter
	next *waiter
	// The round its placement alone found no room in, sat out until the round moves on
	held uint64
	// Whether its caller gave up while its placement ran
	quit  bool
	state waitState
	// Why it was shed or left unplaced
	err    error
	permit *permit
	done   chan struct{}
}

// Charges between one token and the ceiling so cheap items still take turns
func chargeOf(item *Item) int { return min(max(item.Cost, 1), maxCost) }

// The FIFO of one fairness id inside a band, linked into the band round robin ring
type flow struct {
	id      string
	band    *band
	head    *waiter
	tail    *waiter
	n       int
	deficit int
	// Whether the flow has received its quantum for the turn in progress
	granted bool
	prev    *flow
	next    *flow
}

// Appends the waiter at the tail
func (f *flow) push(w *waiter) {
	w.prev, w.next = f.tail, nil
	if f.tail == nil {
		f.head = w
	} else {
		f.tail.next = w
	}
	f.tail = w
	f.n++
}

// Removes the waiter from any position in constant time
func (f *flow) unlink(w *waiter) {
	if w.prev == nil {
		f.head = w.next
	} else {
		w.prev.next = w.next
	}
	if w.next == nil {
		f.tail = w.prev
	} else {
		w.next.prev = w.prev
	}
	w.prev, w.next = nil, nil
	f.n--
}

// The oldest waiter the check lets through, nil when every waiter is held back
func (f *flow) first(eligible func(*waiter) bool) *waiter {
	for w := f.head; w != nil; w = w.next {
		if eligible(w) {
			return w
		}
	}
	return nil
}

// One priority level whose flows share dispatch by deficit round robin on token cost
type band struct {
	priority int
	flows    map[string]*flow
	// The flow whose turn it is
	cur *flow
	n   int
	// The dispatch pass that found every item of the band held
	heldPass uint64
	// Queued items per bit length of their charged cost
	costs [26]int
	// The turn and the credit changes of the latest pick, taken back when its waiter is not dispatched
	turn    *flow
	changed []credit
}

// One change a pick made to a flow, the credit it added and whether the flow held its quantum before
type credit struct {
	flow    *flow
	added   int
	granted bool
}

func newBand(priority int) *band {
	return &band{priority: priority, flows: map[string]*flow{}}
}

// Smallest all ones value covering every queued cost so a fresh grant always fits the next item
func (b *band) quantum() int {
	for k := len(b.costs) - 1; k > 0; k-- {
		if b.costs[k] > 0 {
			return 1<<k - 1
		}
	}
	return 0
}

// Queues the waiter on its fairness id flow, opening a new flow behind the current turn
func (b *band) add(w *waiter, id string) (*flow, bool) {
	f, created := b.flows[id], false
	if f == nil {
		f = &flow{id: id, band: b}
		b.flows[id] = f
		b.link(f)
		created = true
	}
	w.flow = f
	f.push(w)
	b.n++
	b.costs[bits.Len(uint(w.cost))]++
	return f, created
}

// Takes the waiter out, dropping an emptied flow and answering the debt it still owed
func (b *band) remove(w *waiter) int {
	f := w.flow
	f.unlink(w)
	b.n--
	b.costs[bits.Len(uint(w.cost))]--
	if f.n > 0 {
		return 0
	}
	b.unlinkFlow(f)
	delete(b.flows, f.id)
	return -f.deficit
}

// Joins the ring just behind the current flow so a newcomer waits one full round
func (b *band) link(f *flow) {
	if b.cur == nil {
		f.prev, f.next = f, f
		b.cur = f
		return
	}
	tail := b.cur.prev
	f.prev, f.next = tail, b.cur
	tail.next = f
	b.cur.prev = f
}

// Leaves the ring, handing the turn to the next flow when it held it
func (b *band) unlinkFlow(f *flow) {
	if f.next == f {
		b.cur = nil
	} else {
		f.prev.next = f.next
		f.next.prev = f.prev
		if b.cur == f {
			b.cur = f.next
		}
	}
	f.prev, f.next = nil, nil
}

// Next waiter by deficit round robin with credit capped at two quanta, nil when every flow is held back
func (b *band) pick(eligible func(*waiter) bool) *waiter {
	q := b.quantum()
	b.turn, b.changed = b.cur, b.changed[:0]
	for idle := 0; idle < len(b.flows); {
		f := b.cur
		w := f.first(eligible)
		if w == nil {
			b.set(f, min(f.deficit, 0), false)
			b.cur = f.next
			idle++
			continue
		}
		if !f.granted {
			b.set(f, min(f.deficit, q)+q, true)
		}
		if w.cost <= f.deficit {
			return w
		}
		b.set(f, f.deficit, false)
		b.cur = f.next
		idle = 0
	}
	b.unpick()
	return nil
}

// Changes a flow's credit and quantum mark, remembering the change for unpick
func (b *band) set(f *flow, deficit int, granted bool) {
	b.changed = append(b.changed, credit{f, deficit - f.deficit, f.granted})
	f.deficit, f.granted = deficit, granted
}

// Takes back the turn and credit changes of the latest pick, for a pick whose waiter was not dispatched
func (b *band) unpick() {
	for i := len(b.changed) - 1; i >= 0; i-- {
		c := b.changed[i]
		c.flow.deficit -= c.added
		c.flow.granted = c.granted
	}
	if b.turn != nil && b.flows[b.turn.id] == b.turn {
		b.cur = b.turn
	}
	b.changed = b.changed[:0]
}
