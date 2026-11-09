package jinja

import "strings"

// jinja2's loop object for one for loop
type LoopContext struct {
	items       []Value
	index0      int
	depth0      int
	recurse     func(Value) (Value, error)
	lastChanged *Tuple
	// Items returned by __next__, whether the iterator ended, the iterator pulled from and the key suffix of its steps
	consumed  int
	exhausted bool
	iter      *loopIter
	suffix    string
	r         *renderer
}

// The iterator a loop pulls from, sequence or generator items with pull frames, another loop, or a loop test
type loopIter struct {
	r      *renderer
	all    []Value
	frames func(k int) int
	pos    int
	done   bool
	sized  bool
	err    error
	of     *LoopContext
	test   *forStmt
	sc     *scope
	inner  *loopIter
	// Whether the loop filter generator has run its first resume
	started bool
}

// The iterator Python's iter() makes of a for loop's iterable, measuring LoopContext.__iter__ when it is a loop
func (r *renderer) loopIterOf(v Value) (*loopIter, error) {
	if l, ok := v.(*LoopContext); ok {
		key := "loop __iter__" + l.suffix
		if err := r.measured(key, r.opFrames(key, l, nil, nil)); err != nil {
			return nil, err
		}
		return &loopIter{r: r, of: l}, nil
	}
	it := &loopIter{r: r, frames: func(int) int { return 0 }}
	switch x := v.(type) {
	case *Iterator:
		if x.made != nil {
			start := x.taken
			it.frames = func(k int) int { return x.windowFrames(r, start+k, start+k+1) }
		}
	case *Undefined:
		it.frames = func(k int) int {
			if k == 0 {
				return 1
			}
			return 0
		}
	default:
		it.sized = true
	}
	if x, ok := v.(*Iterator); ok {
		for {
			item, more, err := x.pull()
			if err == nil && more {
				err = checkBytes((len(it.all) + 1) * slotBytes)
			}
			if err != nil || !more {
				it.err = err
				return it, nil
			}
			it.all = append(it.all, item)
		}
	}
	it.all, it.err = iterValues(v)
	return it, nil
}

// One next() on the iterator from a frame depth above the template frame, the item unless it ended
func (it *loopIter) next(depth int) (Value, bool, error) {
	r := it.r
	switch {
	case it.done:
		return nil, false, nil
	case it.err != nil && it.pos >= len(it.all):
		if err := r.transient(depth + it.frames(it.pos)); err != nil {
			return nil, false, err
		}
		return nil, false, it.err
	case it.test != nil:
		return it.nextFiltered(depth)
	case it.of != nil:
		v, ok, err := it.of.step(depth)
		if err != nil || !ok {
			it.done = err == nil
			return nil, false, err
		}
		return &Tuple{items: []Value{v, it.of}}, true, nil
	}
	if err := r.transient(depth + it.frames(it.pos)); err != nil {
		return nil, false, err
	}
	if it.pos >= len(it.all) {
		it.done = true
		return nil, false, nil
	}
	it.pos++
	return it.all[it.pos-1], true, nil
}

// One resume of jinja2's loop filter generator, resolving context names first, pulling until an item passes the test
func (it *loopIter) nextFiltered(depth int) (Value, bool, error) {
	r := it.r
	if err := r.enter(depth + 1); err != nil {
		return nil, false, err
	}
	defer r.leave(depth + 1)
	if !it.started && it.test.testResolves {
		if err := r.transient(1); err != nil {
			return nil, false, err
		}
	}
	it.started = true
	for {
		v, ok, err := it.inner.next(0)
		if err != nil {
			return nil, false, err
		}
		if !ok {
			it.done = true
			return nil, false, nil
		}
		keep, err := r.loopTest(it.test, it.sc, v)
		if err != nil {
			return nil, false, err
		}
		if keep {
			return v, true, nil
		}
	}
}

// Whether an item passes a loop's test, its target bound in a scope of its own
func (r *renderer) loopTest(n *forStmt, sc *scope, v Value) (bool, error) {
	tsc := newScope(sc)
	if err := r.assign(n.target, v, tsc); err != nil {
		return false, err
	}
	ok, err := r.eval(n.test, tsc)
	return err == nil && truthy(ok), err
}

// Python's truth of a template value, a LoopContext's through __len__ and its length property materializing its iterator
func (r *renderer) truth(v Value) (bool, error) {
	if l, ok := v.(*LoopContext); ok {
		err := l.lengthPulls(2)
		return l.length() > 0, err
	}
	return truthy(v), nil
}

// The depth of the length property list() or sorted() reaches through __len__ materializing a loop, else 0
func lengthHintDepth(name string, autoescape bool, kw *Dict, args []Value) int {
	switch name {
	case "list", "groupby":
		return 4
	case "sort", "reverse":
		return 3
	case "join":
		_, hasAttr := kw.lookup("attribute")
		d := firstArg(args)
		if kd, ok := kw.lookup("d"); ok {
			d = kd
		}
		if autoescape && !hasAttr && len(args) < 2 && !isMarkup(d) {
			return 4
		}
	}
	return 0
}

// Runs f measuring the deepest frame it reaches above the current one, with recursion checks, recording and charges suspended
func (r *renderer) probe(f func()) int {
	frames, peak, costs, allocated, probing := r.frames, r.peak, r.costs, r.allocated, r.probing
	r.costs, r.probing, r.peak = nil, true, r.frames
	defer func() { r.frames, r.peak, r.costs, r.allocated, r.probing = frames, peak, costs, allocated, probing }()
	f()
	return r.peak - frames
}

// The items the iterator yields from here and the frames each next() pushes, without pulling, the last the ending next()
func (it *loopIter) preview() ([]Value, []int, error) {
	r := it.r
	switch {
	case it.done:
		return nil, []int{0}, nil
	case it.test != nil:
		items, inner, err := it.inner.preview()
		var out []Value
		var frames []int
		cur := 1
		if !it.started && it.test.testResolves {
			cur = 2
		}
		for k, v := range items {
			cur = max(cur, 1+inner[k])
			var keep bool
			var terr error
			local := r.probe(func() { keep, terr = r.loopTest(it.test, it.sc, v) })
			cur = max(cur, 1+local)
			if terr != nil {
				return out, append(frames, cur), terr
			}
			if keep {
				out = append(out, v)
				frames = append(frames, cur)
				cur = 1
			}
		}
		return out, append(frames, max(cur, 1+inner[len(items)])), err
	case it.of != nil:
		items, frames, err := it.of.preview()
		return loopTuples(items, it.of), frames, err
	}
	n := len(it.all) - it.pos
	frames := make([]int, n+1)
	for k := range frames {
		frames[k] = it.frames(it.pos + k)
	}
	return append([]Value(nil), it.all[it.pos:]...), frames, it.err
}

// Items paired with their loop, as LoopContext.__next__ returns them
func loopTuples(items []Value, l *LoopContext) []Value {
	out := make([]Value, len(items))
	for i, v := range items {
		out[i] = &Tuple{items: []Value{v, l}}
	}
	return out
}

// Pulls one item from the loop's iterator from a frame depth above the template frame
func (l *LoopContext) pull(depth int) error {
	v, ok, err := l.iter.next(depth)
	if err != nil {
		return err
	}
	if !ok {
		l.exhausted = true
		return nil
	}
	l.items = append(l.items, v)
	return nil
}

// LoopContext.__next__ called from a frame depth above the template frame, its buffered or freshly pulled item
func (l *LoopContext) step(depth int) (Value, bool, error) {
	r := l.r
	if err := r.enter(depth); err != nil {
		return nil, false, err
	}
	defer r.leave(depth)
	local := 0
	var err error
	if l.consumed >= len(l.items) && !l.exhausted {
		base, outer := r.frames, r.peak
		r.peak = base
		err = l.pull(1)
		local = r.peak - base
		r.peak = max(outer, r.peak)
	}
	key := "loop __next__" + l.suffix
	if merr := r.measured(key, max(local, r.opFrames(key, l, nil, nil))); err == nil {
		err = merr
	}
	if err != nil || l.consumed >= len(l.items) {
		return nil, false, err
	}
	l.consumed++
	l.index0 = l.consumed - 1
	return l.items[l.index0], true, nil
}

// Pulls every remaining item from a frame depth, as LoopContext.length materializes the iterator
func (l *LoopContext) pullAll(depth int) error {
	for !l.exhausted {
		if err := l.pull(depth); err != nil {
			return err
		}
	}
	return nil
}

// The pulls the length property makes from a frame depth, none for a sized iterable, the iterated loop's own length
func (l *LoopContext) lengthPulls(depth int) error {
	switch {
	case l.iter.of != nil:
		return l.iter.of.lengthPulls(depth + 2)
	case l.iter.sized:
		return nil
	}
	return l.pullAll(depth)
}

// The length property, len of a sized iterable or of an iterated loop, else every item pulled
func (l *LoopContext) length() int {
	switch {
	case l.iter.of != nil:
		return l.iter.of.length()
	case l.iter.sized:
		return len(l.iter.all)
	}
	return len(l.items)
}

// The pulls Python's LoopContext makes reading an attribute through getattr, last and nextitem peeking one item and length counting all
func (l *LoopContext) peekFor(name string) error {
	switch name {
	case "last", "nextitem":
		if !l.exhausted && l.consumed >= len(l.items) {
			return l.pull(3)
		}
	case "length":
		return l.lengthPulls(2)
	case "revindex", "revindex0":
		return l.lengthPulls(3)
	}
	return nil
}

// The items iterating the loop yields from here and the frames each __next__ pushes, without pulling
func (l *LoopContext) preview() ([]Value, []int, error) {
	items := append([]Value(nil), l.items[l.consumed:]...)
	frames := make([]int, len(items), len(items)+1)
	for i := range frames {
		frames[i] = 1
	}
	if l.exhausted {
		return items, append(frames, 1), nil
	}
	rest, rf, err := l.iter.preview()
	for _, f := range rf {
		frames = append(frames, 1+f)
	}
	return append(items, rest...), frames, err
}

// Iterates the loop to its end as list() does, LoopContext.__iter__ then __next__ per item
func (l *LoopContext) drain() ([]Value, error) {
	next, err := l.puller()
	if err != nil {
		return nil, err
	}
	var out []Value
	for {
		v, ok, err := next()
		if err != nil || !ok {
			return out, err
		}
		out = append(out, v)
	}
}

// A lazy iteration of the loop, LoopContext.__iter__ then one __next__ per pull yielding the item paired with the loop
func (l *LoopContext) puller() (func() (Value, bool, error), error) {
	key := "loop __iter__" + l.suffix
	if err := l.r.measured(key, l.r.opFrames(key, l, nil, nil)); err != nil {
		return nil, err
	}
	return func() (Value, bool, error) {
		v, ok, err := l.step(0)
		if err != nil || !ok {
			return nil, false, err
		}
		return &Tuple{items: []Value{v, l}}, true, nil
	}, nil
}

// Runs a for loop at a recursion depth, pulling items as Python's iterator, loop filter and LoopContext do
func (r *renderer) runFor(n *forStmt, sc *scope, w *strings.Builder, iterVal Value, depth0 int) error {
	suffix := " " + keyType(iterVal) + producerSuffix(iterVal)
	if n.test != nil {
		suffix = " generator"
	}
	if n.extended {
		key := "loop __init__" + suffix
		if err := r.measured(key, r.opFrames(key, iterVal, nil, nil)); err != nil {
			return err
		}
	}
	src, err := r.loopIterOf(iterVal)
	if err != nil {
		return err
	}
	if n.test != nil {
		src = &loopIter{r: r, test: n, sc: sc, inner: src}
	} else if src.err != nil {
		return src.err
	}
	if !n.extended {
		count := 0
		for {
			v, ok, err := src.next(0)
			if err != nil || !ok {
				if err == nil && count == 0 {
					return r.exec(n.orelse, newScope(sc), w)
				}
				return err
			}
			count++
			if stop, err := r.loopBody(n, sc, w, v, nil); stop || err != nil {
				return err
			}
		}
	}
	key := "loop __iter__" + suffix
	if err := r.measured(key, r.opFrames(key, iterVal, nil, nil)); err != nil {
		return err
	}
	loop := &LoopContext{r: r, iter: src, suffix: suffix, depth0: depth0}
	r.bindRecurse(loop, n, sc, depth0)
	for {
		v, ok, err := loop.step(0)
		if err != nil || !ok {
			if err == nil && loop.consumed == 0 {
				return r.exec(n.orelse, newScope(sc), w)
			}
			return err
		}
		if stop, err := r.loopBody(n, sc, w, v, loop); stop || err != nil {
			return err
		}
	}
}

// Runs a loop body for one item, reporting whether the loop ends by break or error
func (r *renderer) loopBody(n *forStmt, sc *scope, w *strings.Builder, v Value, loop *LoopContext) (bool, error) {
	if err := r.charge(scopeBytes); err != nil {
		return true, err
	}
	isc := newScope(sc)
	if err := r.assign(n.target, v, isc); err != nil {
		return true, err
	}
	if loop != nil {
		isc.vars["loop"] = loop
	}
	switch err := r.exec(n.body, isc, w); {
	case err == errBreak:
		return true, nil
	case err == errContinue:
		return false, nil
	case err != nil:
		return true, err
	}
	return false, nil
}

// Makes an extended loop callable for recursion when the for statement is recursive
func (r *renderer) bindRecurse(loop *LoopContext, n *forStmt, sc *scope, depth0 int) {
	if !n.recursive {
		return
	}
	loop.recurse = func(v Value) (Value, error) {
		if err := r.enter(loopCallFrames); err != nil {
			return nil, err
		}
		defer r.leave(loopCallFrames)
		var buf strings.Builder
		if err := r.runFor(n, sc, &buf, v, depth0+1); err != nil {
			return nil, err
		}
		return r.wrapRendered(buf.String()), nil
	}
}
