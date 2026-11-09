package flowcontrol

import "math/bits"

// Largest token cost charged to one item so deficit counters stay far from overflow
const maxCost = 1 << 24

type waitState int

const (
	stateWaiting waitState = iota
	stateDispatched
	stateAbandoned
	stateShed
	stateClosed
)

// One queued item, its links inside its flow, and how it resolved
type waiter struct {
	item *Item
	cost int
	flow *flow
	prev *waiter
	next *waiter
	// The latest dispatch pass the item sits out, after a soft saturation, a lost route slot or a requeue
	heldPass uint64
	state    waitState
	permit   *permit
	done     chan struct{}
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

// Inserts the waiter at the head, ahead of every item queued after its first admission
func (f *flow) pushFront(w *waiter) {
	w.prev, w.next = nil, f.head
	if f.head == nil {
		f.tail = w
	} else {
		f.head.prev = w
	}
	f.head = w
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

// Queues the waiter on its fairness id flow, at its head when front, opening a new flow behind the current turn
func (b *band) add(w *waiter, id string, front bool) (*flow, bool) {
	f, created := b.flows[id], false
	if f == nil {
		f = &flow{id: id, band: b}
		b.flows[id] = f
		b.link(f)
		created = true
	}
	w.flow = f
	if front {
		f.pushFront(w)
	} else {
		f.push(w)
	}
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
	for idle := 0; idle < len(b.flows); {
		f := b.cur
		w := f.first(eligible)
		if w == nil {
			f.deficit, f.granted = min(f.deficit, 0), false
			b.cur = f.next
			idle++
			continue
		}
		if !f.granted {
			f.deficit = min(f.deficit, q) + q
			f.granted = true
		}
		if w.cost <= f.deficit {
			return w
		}
		f.granted = false
		b.cur = f.next
		idle = 0
	}
	return nil
}
