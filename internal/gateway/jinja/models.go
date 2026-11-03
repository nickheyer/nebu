package jinja

import (
	"regexp"
	"strings"
)

// Computes the frames a modeled operation pushes above its caller from its key, subject and arguments
type frameModel func(r *renderer, key string, v Value, args []Value, kw *Dict) int

// The port of each warmModeled operation's Python call path
var frameModels map[string]frameModel

func init() {
	frameModels = map[string]frameModel{
		"call format":               (*renderer).formatModel,
		"call generate_lorem_ipsum": (*renderer).lipsumModel,
		"call join":                 (*renderer).joinMethodModel,
		"escape":                    (*renderer).escapeModel,
		"filter attr":               (*renderer).attrModel,
		"filter dictsort":           (*renderer).dictsortModel,
		"filter e":                  (*renderer).escapeModel,
		"filter escape":             (*renderer).escapeModel,
		"filter groupby":            (*renderer).groupbyModel,
		"filter indent":             (*renderer).indentModel,
		"filter join":               (*renderer).joinModel,
		"filter last":               (*renderer).lastModel,
		"filter list":               (*renderer).listModel,
		"filter max":                (*renderer).maxModel,
		"filter min":                (*renderer).minModel,
		"filter replace":            (*renderer).replaceModel,
		"filter sort":               (*renderer).sortModel,
		"filter striptags":          (*renderer).striptagsModel,
		"filter sum":                (*renderer).sumModel,
		"filter urlencode":          (*renderer).urlencodeModel,
		"filter wordwrap":           (*renderer).wordwrapModel,
		"filter xmlattr":            (*renderer).xmlattrModel,
		"getattr LoopContext":       (*renderer).loopAttrModel,
		"filter length":             (*renderer).lenModel,
		"filter first":              (*renderer).firstModel,
		"filter count":              (*renderer).lenModel,
		"loop __next__":             (*renderer).loopNextModel,
		"test in":                   (*renderer).testInModel,
	}
}

// Frames sandbox call and Context.call push before the callee runs, the callee's own frame landing at the same depth
const contextCallFrames = 3

// Frames Markup.__getitem__ pushes, its own frame and Markup.__new__ wrapping the character
const markupItemFrames = 2

// Runs f with recursion checks, peak and cost recording and allocation charges suspended, for models reading computed values
func (r *renderer) quietly(f func()) {
	frames, peak, costs, allocated := r.frames, r.peak, r.costs, r.allocated
	r.frames, r.costs = -1<<30, nil
	defer func() { r.frames, r.peak, r.costs, r.allocated = frames, peak, costs, allocated }()
	f()
}

// Frames _fail_with_undefined_error pushes raising for an Undefined, its message property and object_type_repr for an object
func failFrames(v Value) int {
	if u, ok := v.(*Undefined); ok && u.hint == "" && u.hasObj {
		return 3
	}
	return 2
}

func isUndef(v Value) bool {
	_, ok := v.(*Undefined)
	return ok
}

// Frames a missed dunder attribute lookup pushes, Namespace.__getattribute__ reading its attrs through itself, or Undefined.__getattr__
func hasattrFrames(v Value) int {
	switch v.(type) {
	case *Namespace:
		return 2
	case *Undefined:
		return 1
	}
	return 0
}

// Frames isinstance pushes reading __class__ through Namespace.__getattribute__ after the type check fails
func isinstanceFrames(v Value) int {
	if _, ok := v.(*Namespace); ok {
		return 1
	}
	return 0
}

// Frames markupsafe.escape pushes counting its own frame, the __html__ hook, str() and Markup.__new__
func escapeFrames(v Value) int {
	switch v.(type) {
	case string:
		return 2
	case Markup:
		return 3
	}
	f, ok := strWalk(v)
	if !ok {
		return max(1+hasattrFrames(v), 1+f)
	}
	return max(2, 1+hasattrFrames(v), 1+f)
}

// Frames markupsafe.soft_str pushes counting its own frame
func softStrFrames(v Value) int {
	switch v.(type) {
	case string, Markup:
		return 1
	}
	f, _ := strWalk(v)
	return max(1+isinstanceFrames(v), 1+f)
}

// Frames jinja2's ignore_case pushes counting its own frame, typing.cast for strings and Markup.lower for Markup
func ignoreCaseFrames(v Value) int {
	switch v.(type) {
	case string:
		return 2
	case Markup:
		return 3
	}
	return 1 + isinstanceFrames(v)
}

// Frames a C rich comparison pushes below its caller and whether it raised, Undefined defining Python comparison methods
func compareFrames(op string, a, b Value) (int, bool) {
	ua, ub := isUndef(a), isUndef(b)
	if op == "==" {
		switch {
		case ua || ub:
			return 1, false
		}
		switch x := a.(type) {
		case *List:
			if y, ok := b.(*List); ok && len(x.items) == len(y.items) {
				return seqCompareFrames(op, x.items, y.items)
			}
		case *Tuple:
			if y, ok := b.(*Tuple); ok && len(x.items) == len(y.items) {
				return seqCompareFrames(op, x.items, y.items)
			}
		case *Dict:
			if y, ok := b.(*Dict); ok && len(x.keys) == len(y.keys) {
				peak := 0
				for i, k := range x.keys {
					w, found, err := y.get(k)
					if err != nil || !found {
						return peak, err != nil
					}
					f, raised := compareFrames("==", x.vals[i], w)
					peak = max(peak, f)
					if raised || !looseEqual(x.vals[i], w) {
						return peak, raised
					}
				}
				return peak, false
			}
		}
		return 0, false
	}
	if ua {
		return failFrames(a), true
	}
	if ub {
		return failFrames(b), true
	}
	switch x := a.(type) {
	case *List:
		if y, ok := b.(*List); ok {
			return seqCompareFrames(op, x.items, y.items)
		}
	case *Tuple:
		if y, ok := b.(*Tuple); ok {
			return seqCompareFrames(op, x.items, y.items)
		}
	}
	_, err := pyOrder(op, a, b)
	return 0, err != nil
}

// Python equality as a comparison's identity shortcut and __eq__ decide it, two Undefined values comparing equal
func looseEqual(a, b Value) bool {
	if ua, ub := isUndef(a), isUndef(b); ua || ub {
		return ua && ub
	}
	return pyEq(a, b)
}

// Frames comparing two sequences pushes, equality up to the first differing item then the operator on that pair
func seqCompareFrames(op string, x, y []Value) (int, bool) {
	peak := 0
	for i := 0; i < min(len(x), len(y)); i++ {
		if sameObject(x[i], y[i]) {
			continue
		}
		f, raised := compareFrames("==", x[i], y[i])
		peak = max(peak, f)
		if raised {
			return peak, true
		}
		if looseEqual(x[i], y[i]) {
			continue
		}
		if op == "==" {
			return peak, false
		}
		f, raised = compareFrames(op, x[i], y[i])
		return max(peak, f), raised
	}
	return peak, false
}

// A fresh iterator yielding a filter generator's items from position from, rebuilt from its producing call
func (r *renderer) replay(it *Iterator, from int) (Value, error) {
	if it.rest != nil {
		return &List{items: it.rest(from)}, nil
	}
	if it.made == nil {
		panic("jinja: a generator without the filter call that made it")
	}
	src := it.made.v
	switch inner := src.(type) {
	case *Iterator:
		fresh, err := r.replay(inner, it.made.skip)
		if err != nil {
			return nil, err
		}
		src = fresh
	case *LoopContext:
		items, _, err := r.loopSourceItems(inner, it.made.skip)
		if err != nil {
			return nil, err
		}
		src = &List{items: items}
	}
	var out Value
	var err error
	r.quietly(func() {
		if out, err = builtinFilters[it.made.name](r, src, it.made.args, it.made.kw); err != nil {
			return
		}
		g := out.(*Iterator)
		for i := 0; i < from && err == nil; i++ {
			_, _, err = g.pull()
		}
	})
	return out, err
}

// Items iterating a value yields until it raises, the most frames one pull pushes, and the error
func (r *renderer) pulls(v Value) ([]Value, int, error) {
	switch x := v.(type) {
	case *Undefined:
		return nil, 1, nil
	case *Iterator:
		return r.iteratorPulls(x, x.taken)
	case *LoopContext:
		items, frames, err := r.loopSourceItems(x, x.consumed)
		return items, max(1, maxInts(frames)), err
	}
	var items []Value
	var err error
	r.quietly(func() { items, err = iterValues(v) })
	return items, 0, err
}

// Items iterating a loop yields from its skip-th consumed item, and each __next__ step's frames, without pulling
func (r *renderer) loopSourceItems(l *LoopContext, skip int) ([]Value, []int, error) {
	done := loopTuples(l.items[min(skip, l.consumed):l.consumed], l)
	items, frames, err := l.preview()
	return append(done, loopTuples(items, l)...), append(make([]int, len(done)), frames...), err
}

func maxInts(xs []int) int {
	m := 0
	for _, x := range xs {
		m = max(m, x)
	}
	return m
}

// Pulls of an iterator from its from-th item on, a reversed iterator pushing nothing and a generator running its body
func (r *renderer) iteratorPulls(it *Iterator, from int) ([]Value, int, error) {
	if it.done {
		return nil, 0, nil
	}
	fresh, err := r.replay(it, from)
	if err != nil {
		return nil, 0, err
	}
	var items []Value
	r.quietly(func() {
		g, ok := fresh.(*Iterator)
		if !ok {
			items, err = iterValues(fresh)
			return
		}
		for {
			v, more, e := g.pull()
			if e != nil || !more {
				err = e
				return
			}
			items = append(items, v)
		}
	})
	if it.rest != nil {
		return items, 0, err
	}
	return items, r.generatorFrames(it, from, allPulls), err
}

// A window end past every pull a generator makes
const allPulls = int(^uint(0) >> 1)

// The deepest frame a filter generator's body reaches over pulls from to to, its own frame at depth 1
func (r *renderer) generatorFrames(it *Iterator, from, to int) int {
	if it.rest != nil {
		return 0
	}
	m := it.made
	src := m.v
	var srcItems []Value
	var srcErr error
	var loopFrames []int
	switch inner := src.(type) {
	case *Iterator:
		srcItems, _, srcErr = r.iteratorPulls(inner, m.skip)
	case *LoopContext:
		srcItems, loopFrames, srcErr = r.loopSourceItems(inner, m.skip)
	default:
		srcItems, _, srcErr = r.pulls(src)
	}
	// Frames pulling source items lo to hi push, hi past the end including the pull ending the source
	srcFrames := func(lo, hi int) int {
		switch x := src.(type) {
		case *Undefined:
			if lo == 0 {
				return 1
			}
		case *Iterator:
			return x.windowFrames(r, m.skip+lo, m.skip+hi)
		case *LoopContext:
			if m.name == "map" || namedSelect[m.name] {
				return 1
			}
			return maxInts(loopFrames[min(lo, len(loopFrames)):min(hi, len(loopFrames))])
		}
		return 0
	}
	switch m.name {
	case "slice":
		if from > 0 {
			return 1
		}
		return 1 + srcFrames(0, allPulls)
	case "batch":
		return 1 + batchSourceFrames(m, len(srcItems), from, to, srcFrames)
	case "items":
		if from > 0 || isUndef(src) {
			return 1
		}
		return 2 + isinstanceFrames(src)
	case "map":
		return r.mapFrames(m, src, srcItems, srcErr, from, to, srcFrames)
	case "select", "reject", "selectattr", "rejectattr":
		return r.selectFrames(m, src, srcItems, from, to, srcFrames)
	case "unique":
		return r.uniqueFrames(m, srcItems, from, to, srcFrames)
	}
	panic("jinja: no frame model for the generator of filter " + m.name)
}

// The frames pulls from to to of an iterator push below the pulling frame, nothing for reversed iterators
func (it *Iterator) windowFrames(r *renderer, from, to int) int {
	if it.rest != nil || it.made == nil {
		return 0
	}
	return r.generatorFrames(it, from, to)
}

// Source frames do_batch's pulls from to to consume, a batch yielding when the item after it arrives
func batchSourceFrames(m *filterCall, n, from, to int, srcFrames func(lo, hi int) int) int {
	lc := 1
	if len(m.args) > 0 {
		if v, err := argInt(m.args[0]); err == nil && v > 0 {
			lc = v
		}
	}
	lo := from * lc
	if from > 0 {
		lo++
	}
	hi := allPulls
	if to < allPulls && to*lc < n {
		hi = to*lc + 1
	}
	return srcFrames(lo, hi)
}

// Filters whose generators run select_or_reject
var namedSelect = map[string]bool{"select": true, "reject": true, "selectattr": true, "rejectattr": true}

// Frames the generator body's truth test of its source pushes, Undefined.__bool__ or LoopContext.__len__ materializing the loop
func (r *renderer) truthFrames(v Value) int {
	switch x := v.(type) {
	case *Undefined:
		return 2
	case *LoopContext:
		return 3 + r.lengthFrames(x)
	}
	return 1
}

// Frames the length property's pulls push below it, none when sized, through len() of an iterated loop
func (r *renderer) lengthFrames(l *LoopContext) int {
	switch {
	case l.iter.of != nil:
		return 2 + r.lengthFrames(l.iter.of)
	case l.iter.sized || l.exhausted:
		return 0
	}
	_, frames, _ := l.iter.preview()
	return maxInts(frames)
}

// The deepest frame sync_do_map reaches over pulls from to to, prepare_map then the getter or call_filter
func (r *renderer) mapFrames(m *filterCall, src Value, items []Value, srcErr error, from, to int, srcFrames func(lo, hi int) int) int {
	peak := 1
	if from == 0 {
		peak = r.truthFrames(src)
	}
	if !truthy(src) {
		return peak
	}
	peak = max(peak, 1+srcFrames(from, to))
	attr, isAttr := m.kw.lookup("attribute")
	isAttr = isAttr && len(m.args) == 0
	if from == 0 {
		switch {
		case isAttr && len(m.kw.keys) > 2, isAttr && len(m.kw.keys) == 2 && !hasKey(m.kw, "default"), !isAttr && len(m.args) == 0:
			return max(peak, 3)
		case isAttr:
			peak = max(peak, 4)
		default:
			peak = max(peak, 2)
		}
	}
	var parts []Value
	def := Value(none)
	if isAttr {
		var err error
		if parts, err = attributeParts(attr); err != nil {
			return peak
		}
		if d, ok := m.kw.lookup("default"); ok {
			def = d
		}
	}
	for i, it := range items {
		if i >= to {
			break
		}
		f, ok := r.mapItemFrames(m, it, isAttr, parts, def)
		if i >= from {
			peak = max(peak, f)
		}
		if !ok {
			return peak
		}
	}
	return peak
}

// Frames one sync_do_map item pushes below the generator frame, and whether its getter or filter returned
func (r *renderer) mapItemFrames(m *filterCall, it Value, isAttr bool, parts []Value, def Value) (int, bool) {
	if isAttr {
		f, _, ok := r.attrgetterFrames(it, parts, false, def)
		return 1 + f, ok
	}
	name, _ := asStr(m.args[0])
	rest := m.args[1:]
	key := builtinCallKey(name, true, it, rest, m.kw)
	f := max(5, 4+r.opFrames(key, it, rest, m.kw))
	var err error
	r.quietly(func() { _, err = r.callFilter(name, it, rest, m.kw) })
	return f, err == nil
}

// The deepest frame select_or_reject reaches over pulls from to to, its preparation then transfunc and the test
func (r *renderer) selectFrames(m *filterCall, src Value, items []Value, from, to int, srcFrames func(lo, hi int) int) int {
	peak := 1
	if from == 0 {
		peak = r.truthFrames(src)
	}
	if !truthy(src) {
		return peak
	}
	lookupAttr := m.name == "selectattr" || m.name == "rejectattr"
	off := 0
	var parts []Value
	if lookupAttr {
		if len(m.args) == 0 {
			return max(peak, 3)
		}
		var err error
		if parts, err = attributeParts(m.args[0]); err != nil {
			return max(peak, 4)
		}
		off = 1
	}
	if from == 0 {
		peak = max(peak, 2)
		if lookupAttr {
			peak = max(peak, 4)
		}
	}
	hasTest := len(m.args) > off
	yielded, lo := 0, -1
	for i, it := range items {
		if yielded >= to {
			return max(peak, 1+srcFrames(lo, i))
		}
		if yielded >= from {
			if lo < 0 {
				lo = i
			}
			peak = max(peak, r.selectItemFrames(m, it, lookupAttr, parts, off, hasTest))
		}
		keep, err := r.selectKeeps(m, it, lookupAttr, parts, off, hasTest)
		if err != nil {
			return peak
		}
		if keep {
			yielded++
		}
	}
	if lo < 0 {
		lo = len(items)
	}
	return max(peak, 1+srcFrames(lo, allPulls))
}

// Frames select_or_reject's lambda pushes for one item below the generator frame, transfunc then the test or bool
func (r *renderer) selectItemFrames(m *filterCall, it Value, lookupAttr bool, parts []Value, off int, hasTest bool) int {
	peak := 3
	x := it
	if lookupAttr {
		f, v, ok := r.attrgetterFrames(it, parts, false, none)
		peak = max(peak, 2+f)
		if !ok {
			return peak
		}
		x = v
	}
	if hasTest {
		name, _ := asStr(m.args[off])
		rest := m.args[off+1:]
		key := builtinCallKey(name, false, x, rest, m.kw)
		peak = max(peak, 6, 5+r.opFrames(key, x, rest, m.kw))
	}
	return peak
}

// Whether select_or_reject keeps an item, computed without recording frames
func (r *renderer) selectKeeps(m *filterCall, it Value, lookupAttr bool, parts []Value, off int, hasTest bool) (bool, error) {
	var keep bool
	var err error
	r.quietly(func() {
		x := it
		if lookupAttr {
			for _, p := range parts {
				if x, err = r.getitem(x, p); err != nil {
					return
				}
			}
		}
		if hasTest {
			name, _ := asStr(m.args[off])
			keep, err = r.callTest(name, x, m.args[off+1:], m.kw)
		} else {
			keep = truthy(x)
		}
	})
	if m.name == "reject" || m.name == "rejectattr" {
		keep = !keep
	}
	return keep, err
}

// The deepest frame sync_do_unique reaches over pulls from to to, make_attrgetter then the getter and set membership
func (r *renderer) uniqueFrames(m *filterCall, items []Value, from, to int, srcFrames func(lo, hi int) int) int {
	a, err := filterArgs("sync_do_unique", m.v, []param{req("value"), opt("case_sensitive", false), opt("attribute", none)}, m.args, m.kw)
	if err != nil {
		return 1
	}
	parts, err := attributeParts(a[2])
	if err != nil {
		return 3
	}
	post := !truthy(a[1])
	peak := 1
	if from == 0 {
		peak = 3
	}
	seen := map[string]bool{}
	yielded, lo := 0, -1
	for i, it := range items {
		if yielded >= to {
			return max(peak, 1+srcFrames(lo, i))
		}
		if yielded >= from && lo < 0 {
			lo = i
		}
		f, key, ok := r.attrgetterFrames(it, parts, post, none)
		if yielded >= from {
			peak = max(peak, 1+f)
		}
		if !ok {
			return peak
		}
		h, err := hashKey(key)
		if err != nil {
			return peak
		}
		if isUndef(key) && yielded >= from {
			peak = max(peak, 2)
		}
		if !seen[h] {
			seen[h] = true
			yielded++
		}
	}
	if lo < 0 {
		lo = len(items)
	}
	return max(peak, 1+srcFrames(lo, allPulls))
}

// Frames make_attrgetter's getter pushes counting its own frame, with the value it returns and whether it finished
func (r *renderer) attrgetterFrames(item Value, parts []Value, post bool, def Value) (int, Value, bool) {
	peak := 1
	for _, p := range parts {
		peak = max(peak, 1+r.getitemFrames(item, p))
		var err error
		r.quietly(func() { item, err = r.getitem(item, p) })
		if err != nil {
			return peak, nil, false
		}
		if _, isNone := def.(NoneType); !isNone && isUndef(item) {
			item = def
		}
	}
	if post {
		peak = max(peak, 1+ignoreCaseFrames(item))
		item = ignoreCase(item)
	}
	return peak, item, true
}

// Frames make_multi_attrgetter's getter pushes counting its own frame, with the list it returns and whether it finished
func (r *renderer) multiAttrgetterFrames(item Value, groups [][]Value, post bool) (int, Value, bool) {
	peak := 1
	out := make([]Value, len(groups))
	for i, parts := range groups {
		f, v, ok := r.attrgetterFrames(item, parts, post, none)
		peak = max(peak, f)
		if !ok {
			return peak, nil, false
		}
		out[i] = v
	}
	return peak, &List{items: out}, true
}

// The attribute paths make_multi_attrgetter splits a comma separated attribute into
func multiAttributeParts(attribute Value) ([][]Value, error) {
	groups := []Value{attribute}
	if s, ok := attribute.(string); ok {
		groups = nil
		for _, part := range strings.Split(s, ",") {
			groups = append(groups, part)
		}
	}
	out := make([][]Value, len(groups))
	for i, g := range groups {
		parts, err := attributeParts(g)
		if err != nil {
			return nil, err
		}
		out[i] = parts
	}
	return out, nil
}

// Frames the comparisons the engine's sort performs on keys push below the sorting frame
func sortCompareFrames(keys []Value, reverse bool) int {
	peak := 0
	order := append([]Value(nil), keys...)
	_ = pySortObserved(order, order, reverse, func(f int) { peak = max(peak, f) })
	return peak
}

func (r *renderer) escapeModel(key string, v Value, args []Value, kw *Dict) int {
	if len(args) > 0 || len(kw.keys) > 0 {
		return 0
	}
	return escapeFrames(v)
}

// Frames of the list filter, its async variant wrapper then list() pulling every item
func (r *renderer) listModel(key string, v Value, args []Value, kw *Dict) int {
	if len(args) > 0 || len(kw.keys) > 0 {
		return 3
	}
	_, pf, _ := r.pulls(v)
	return max(3, 2+pf)
}

// Frames LoopContext.__next__ pushes before pulling, its own frame, the renderer adding each pull as it happens
func (r *renderer) loopNextModel(key string, v Value, args []Value, kw *Dict) int {
	return 1
}

func (r *renderer) maxModel(key string, v Value, args []Value, kw *Dict) int {
	return r.minMaxModel("do_max", ">", v, args, kw)
}

func (r *renderer) minModel(key string, v Value, args []Value, kw *Dict) int {
	return r.minMaxModel("do_min", "<", v, args, kw)
}

// Frames of do_min or do_max through _min_or_max, the empty case making an Undefined and max() comparing keys
func (r *renderer) minMaxModel(fname, op string, v Value, args []Value, kw *Dict) int {
	a, err := filterArgs(fname, v, []param{req("value"), opt("case_sensitive", false), opt("attribute", none)}, args, kw)
	if err != nil {
		return 0
	}
	items, pf, iterErr := r.pulls(a[0])
	peak := max(2, 2+pf)
	if len(items) == 0 {
		if iterErr != nil {
			return peak
		}
		return max(peak, 2+warmBuiltinFrames("undefined", nil))
	}
	parts, err := attributeParts(a[2])
	if err != nil {
		return max(peak, 4)
	}
	peak = max(peak, 4)
	post := !truthy(a[1])
	var best Value
	for i, it := range items {
		f, k, ok := r.attrgetterFrames(it, parts, post, none)
		peak = max(peak, 2+f)
		if !ok {
			return peak
		}
		if i == 0 {
			best = k
			continue
		}
		cf, raised := compareFrames(op, k, best)
		peak = max(peak, 2+cf)
		if raised {
			return peak
		}
		if better, _ := pyOrder(op, k, best); better {
			best = k
		}
	}
	return peak
}

// Frames of do_sort, make_multi_attrgetter, then sorted() pulling items, computing keys and comparing them
func (r *renderer) sortModel(key string, v Value, args []Value, kw *Dict) int {
	a, err := filterArgs("do_sort", v, []param{req("value"), opt("reverse", false), opt("case_sensitive", false), opt("attribute", none)}, args, kw)
	if err != nil {
		return 0
	}
	groups, err := multiAttributeParts(a[3])
	if err != nil {
		return 3
	}
	peak := 3
	items, pf, iterErr := r.pulls(a[0])
	peak = max(peak, 1+pf)
	if iterErr != nil {
		return peak
	}
	post := !truthy(a[2])
	keys := make([]Value, 0, len(items))
	for _, it := range items {
		f, k, ok := r.multiAttrgetterFrames(it, groups, post)
		peak = max(peak, 1+f)
		if !ok {
			return peak
		}
		keys = append(keys, k)
	}
	return max(peak, 1+sortCompareFrames(keys, truthy(a[1])))
}

// Frames of dictsort, sorted() over the items keyed by sort_func through ignore_case
func (r *renderer) dictsortModel(key string, v Value, args []Value, kw *Dict) int {
	a, err := filterArgs("do_dictsort", v, []param{req("value"), opt("case_sensitive", false), opt("by", "key"), opt("reverse", false)}, args, kw)
	if err != nil {
		return 0
	}
	pos := 0
	switch {
	case looseEqual(a[2], "key"):
	case looseEqual(a[2], "value"):
		pos = 1
	default:
		return 2
	}
	d, ok := a[0].(*Dict)
	if !ok {
		if isUndef(a[0]) {
			return 2 + failFrames(a[0])
		}
		return 1 + hasattrFrames(a[0])
	}
	peak := 1
	keys := make([]Value, len(d.keys))
	for i, k := range d.keys {
		val := []Value{k, d.vals[i]}[pos]
		peak = max(peak, 2)
		if !truthy(a[1]) {
			peak = max(peak, 2+ignoreCaseFrames(val))
			val = ignoreCase(val)
		}
		keys[i] = val
	}
	return max(peak, 1+sortCompareFrames(keys, truthy(a[3])))
}

// Frames of the sum filter, its async variant wrapper, the attribute getter and the additions sum() performs
func (r *renderer) sumModel(key string, v Value, args []Value, kw *Dict) int {
	a, err := filterArgs("sync_do_sum", v, []param{req("iterable"), opt("attribute", none), opt("start", int64(0))}, args, kw)
	if err != nil {
		return 3
	}
	peak := 3
	var parts []Value
	if _, isNone := a[1].(NoneType); !isNone {
		if parts, err = attributeParts(a[1]); err != nil {
			return 4
		}
		peak = 4
	}
	if _, isStr := asStr(a[2]); isStr {
		return peak
	}
	items, pf, _ := r.pulls(a[0])
	peak = max(peak, 2+pf)
	acc := a[2]
	for _, it := range items {
		f, x, ok := r.attrgetterFrames(it, parts, false, none)
		if len(parts) > 0 {
			peak = max(peak, 2+f)
		}
		if !ok {
			return peak
		}
		af, raised := addFrames(acc, x)
		peak = max(peak, 2+af)
		if raised {
			return peak
		}
		var err error
		r.quietly(func() { acc, err = binaryOp("add", acc, x) })
		if err != nil {
			return peak
		}
	}
	return peak
}

// Frames a + b pushes below its C caller and whether it raised, through Undefined and Markup's Python methods
func addFrames(a, b Value) (int, bool) {
	_, am := a.(Markup)
	_, bm := b.(Markup)
	switch {
	case isUndef(a):
		return failFrames(a), true
	case isUndef(b):
		return failFrames(b), true
	case am:
		switch b.(type) {
		case string, Markup:
			return 2 + escapeFrames(b), false
		}
		return 1 + max(isinstanceFrames(b), hasattrFrames(b)), true
	case bm:
		if _, ok := a.(string); ok {
			return max(2+escapeFrames(a), 3+escapeFrames(b)), false
		}
		return 1 + max(isinstanceFrames(a), hasattrFrames(a)), true
	}
	_, err := binaryOp("add", a, b)
	return 0, err != nil
}

func hasKey(d *Dict, k string) bool {
	_, ok := d.lookup(k)
	return ok
}

// Frames of the groupby filter, sorting by the key getter, grouping by equal keys and building group tuples
func (r *renderer) groupbyModel(key string, v Value, args []Value, kw *Dict) int {
	a, err := filterArgs("sync_do_groupby", v, []param{req("value"), req("attribute"), opt("default", none), opt("case_sensitive", false)}, args, kw)
	if err != nil {
		return 3
	}
	parts, err := attributeParts(a[1])
	if err != nil {
		return 4
	}
	post := !truthy(a[3])
	peak := 4
	items, pf, iterErr := r.pulls(a[0])
	peak = max(peak, 2+pf)
	if iterErr != nil {
		return peak
	}
	keys := make([]Value, len(items))
	for i, it := range items {
		f, k, ok := r.attrgetterFrames(it, parts, post, a[2])
		peak = max(peak, 2+f)
		if !ok {
			return peak
		}
		keys[i] = k
	}
	order := append([]Value(nil), items...)
	sorted := append([]Value(nil), keys...)
	cf := 0
	if err := pySortObserved(order, sorted, false, func(f int) { cf = max(cf, f) }); err != nil {
		return max(peak, 2+cf)
	}
	peak = max(peak, 2+cf)
	for i := range sorted {
		if i > 0 {
			ef, _ := compareFrames("==", sorted[i], sorted[i-1])
			peak = max(peak, 2+ef)
		}
		if i == 0 || !looseEqual(sorted[i], sorted[i-1]) {
			peak = max(peak, 3)
			if post {
				f, _, _ := r.attrgetterFrames(order[i], parts, false, a[2])
				peak = max(peak, 2+f)
			}
		}
	}
	return peak
}

// Frames of the join filter, its async variant wrapper, the attribute getter, and str, escape or soft_str per item
func (r *renderer) joinModel(key string, v Value, args []Value, kw *Dict) int {
	a, err := filterArgs("sync_do_join", v, []param{req("value"), opt("d", ""), opt("attribute", none)}, args, kw)
	if err != nil {
		return 3
	}
	peak := 3
	var parts []Value
	if _, isNone := a[2].(NoneType); !isNone {
		if parts, err = attributeParts(a[2]); err != nil {
			return 4
		}
		peak = 4
	}
	d := a[1]
	dHTML := isMarkup(d)
	switch {
	case !r.autoescape:
		f, ok := strWalk(d)
		peak = max(peak, 2+f)
		if !ok {
			return peak
		}
	case dHTML:
		peak = max(peak, 2+softStrFrames(d))
	default:
		peak = max(peak, 2+hasattrFrames(d))
	}
	items, pf, _ := r.pulls(a[0])
	peak = max(peak, 2+pf)
	var vals []Value
	for _, it := range items {
		f, x, ok := r.attrgetterFrames(it, parts, false, none)
		if len(parts) > 0 {
			peak = max(peak, 2+f)
		}
		if !ok {
			return peak
		}
		switch {
		case !r.autoescape:
			sf, ok := strWalk(x)
			peak = max(peak, 2+sf)
			if !ok {
				return peak
			}
		case dHTML:
			peak = max(peak, 2+softStrFrames(x))
		default:
			vals = append(vals, x)
		}
	}
	if !r.autoescape || dHTML {
		return peak
	}
	doEscape := false
	for i, x := range vals {
		peak = max(peak, 2+hasattrFrames(x))
		if isMarkup(x) {
			doEscape = true
			continue
		}
		sf, ok := strWalk(x)
		peak = max(peak, 2+sf)
		if !ok {
			return peak
		}
		vals[i] = softStrValue(x)
	}
	if !doEscape {
		f, _ := strWalk(d)
		return max(peak, 2+f)
	}
	peak = max(peak, 2+escapeFrames(d), 4)
	for _, x := range vals {
		peak = max(peak, 4+escapeFrames(x))
	}
	return peak
}

// Frames of a str or Markup join method called through the sandbox, Markup.join escaping each item
func (r *renderer) joinMethodModel(key string, v Value, args []Value, kw *Dict) int {
	if len(args) != 1 || len(kw.keys) > 0 {
		return contextCallFrames
	}
	items, pf, _ := r.pulls(args[0])
	if _, ok := v.(Markup); !ok {
		return max(contextCallFrames, 2+pf)
	}
	peak := max(4, 3+pf)
	for _, it := range items {
		peak = max(peak, 4+escapeFrames(it))
	}
	return peak
}

// Frames of do_attr, inspect.getattr_static reaching _shadowed_dict, then the sandbox getattr for a found attribute
func (r *renderer) attrModel(key string, v Value, args []Value, kw *Dict) int {
	a, err := filterArgs("do_attr", v, []param{req("obj"), req("name")}, args, kw)
	if err != nil {
		return 0
	}
	name, ok := asStr(a[1])
	switch {
	case !ok:
		return 4
	case isUndef(a[0]) && !strings.HasPrefix(name, "__"):
		return max(4, 2+failFrames(a[0]))
	case isUndef(a[0]):
		return 4
	}
	var found bool
	r.quietly(func() { _, found, err = r.attribute(a[0], name) })
	if err != nil || !found {
		return 4
	}
	return max(4, 1+r.getattrFrames(a[0], name))
}

// Frames of do_indent, Markup escaping for Markup text and the generator expression joining continuation lines
func (r *renderer) indentModel(key string, v Value, args []Value, kw *Dict) int {
	a, err := filterArgs("do_indent", v, []param{req("s"), opt("width", int64(4)), opt("first", false), opt("blank", false)}, args, kw)
	if err != nil {
		return 1
	}
	switch a[1].(type) {
	case string, Markup, bool, int64:
	case *Undefined:
		return 1 + failFrames(a[1])
	default:
		return 1
	}
	switch s := a[0].(type) {
	case string:
		if !truthy(a[3]) && len(pySplitlines(s+"\n", false)) > 1 {
			return 2
		}
		return 1
	case Markup:
		if !truthy(a[3]) {
			for _, line := range pySplitlines(string(s)+"\n", false)[1:] {
				if line != "" {
					return 8
				}
			}
		}
		return 6
	case *Undefined:
		return 1 + failFrames(s)
	case *Namespace:
		return 1 + isinstanceFrames(s)
	}
	return 1
}

// Frames of do_last, an empty or undefined sequence making an Undefined
func (r *renderer) lastModel(key string, v Value, args []Value, kw *Dict) int {
	a, err := filterArgs("do_last", v, []param{req("seq")}, args, kw)
	if err != nil {
		return 0
	}
	switch x := a[0].(type) {
	case *Undefined:
		return 1 + warmBuiltinFrames("undefined", nil)
	case string, Markup, *List, *Tuple, *Dict, *Range, *DictView:
		var items []Value
		r.quietly(func() { items, err = iterValues(x) })
		switch {
		case err == nil && len(items) == 0:
			return 1 + warmBuiltinFrames("undefined", nil)
		case isMarkup(x):
			return 1 + markupItemFrames
		}
	}
	return 1
}

// Frames of do_replace, str of each argument without autoescape, escape or soft_str and Markup.replace with it
func (r *renderer) replaceModel(key string, v Value, args []Value, kw *Dict) int {
	a, err := filterArgs("do_replace", v, []param{req("s"), req("old"), req("new"), opt("count", none)}, args, kw)
	if err != nil {
		return 0
	}
	s, old, nw := a[0], a[1], a[2]
	peak := 1
	if !r.autoescape {
		for _, x := range []Value{s, old, nw} {
			f, ok := strWalk(x)
			peak = max(peak, 1+f)
			if !ok {
				return peak
			}
		}
		return peak
	}
	peak = max(peak, 1+hasattrFrames(old))
	escapeS := isMarkup(old)
	if !escapeS {
		peak = max(peak, 1+hasattrFrames(nw))
		if isMarkup(nw) {
			peak = max(peak, 1+hasattrFrames(s))
			escapeS = !isMarkup(s)
		}
	}
	if escapeS {
		peak = max(peak, 1+escapeFrames(s))
	} else {
		peak = max(peak, 1+softStrFrames(s))
	}
	peak = max(peak, 1+softStrFrames(old), 1+softStrFrames(nw))
	if escapeS || isMarkup(s) {
		peak = max(peak, 3, 3+escapeFrames(softStrValue(nw)))
	}
	return peak
}

// The value soft_str returns, a string for anything not already a string
func softStrValue(v Value) Value {
	switch v.(type) {
	case string, Markup:
		return v
	}
	s, _ := pyStr(v)
	return s
}

// html.unescape's character reference pattern, a match calling _replace_charref
var charrefRe = regexp.MustCompile(`&(#[0-9]+;?|#[xX][0-9a-fA-F]+;?|[^\t\n\f <&#;]{1,32};?)`)

// Frames of do_striptags, Markup.striptags importing html.unescape and replacing character references
func (r *renderer) striptagsModel(key string, v Value, args []Value, kw *Dict) int {
	a, err := filterArgs("do_striptags", v, []param{req("value")}, args, kw)
	if err != nil {
		return 0
	}
	x := a[0]
	peak := 1 + hasattrFrames(x)
	if isMarkup(x) {
		peak = 2
	}
	f, ok := strWalk(x)
	peak = max(peak, 1+f)
	if !ok {
		return peak
	}
	text, _ := pyStr(x)
	if charrefRe.MatchString(stripTagsText(text)) {
		return 5
	}
	return max(peak, 4)
}

// Frames of do_urlencode, the Iterable ABC check, url_quote per value and the generator expression over pairs
func (r *renderer) urlencodeModel(key string, v Value, args []Value, kw *Dict) int {
	a, err := filterArgs("do_urlencode", v, []param{req("value")}, args, kw)
	if err != nil {
		return 0
	}
	x := a[0]
	peak := 1 + isinstanceFrames(x)
	switch x.(type) {
	case string, Markup:
		return max(peak, 3)
	}
	peak = max(peak, 2)
	var items []Value
	var pf int
	switch d := x.(type) {
	case *Dict:
		for i, k := range d.keys {
			items = append(items, &Tuple{items: []Value{k, d.vals[i]}})
		}
	case *Undefined:
		return 3
	default:
		var iterErr error
		items, pf, iterErr = r.pulls(x)
		if iterErr != nil && len(items) == 0 {
			f, ok := strWalk(x)
			if !ok {
				return max(peak, 2+f)
			}
			return max(peak, 2+f, 3+isinstanceFrames(x), 3)
		}
	}
	peak = max(peak, 2+pf)
	for _, it := range items {
		if isUndef(it) {
			return max(peak, 3)
		}
		var pair []Value
		r.quietly(func() { pair, err = unpack(it, 2) })
		if err != nil {
			return peak
		}
		for _, p := range pair {
			peak = max(peak, 3+isinstanceFrames(p))
			if _, isStr := p.(string); !isStr && !isMarkup(p) {
				f, ok := strWalk(p)
				peak = max(peak, 3+f)
				if !ok {
					return peak
				}
			}
			peak = max(peak, 4)
		}
	}
	return peak
}

// Frames of do_wordwrap, textwrap.wrap per line reaching _munge_whitespace and the hyphen scan of _handle_long_word
func (r *renderer) wordwrapModel(key string, v Value, args []Value, kw *Dict) int {
	a, err := filterArgs("do_wordwrap", v, []param{req("s"), opt("width", int64(79)), opt("break_long_words", true), opt("wrapstring", none), opt("break_on_hyphens", true)}, args, kw)
	if err != nil {
		return 0
	}
	var text string
	peak := 1
	switch s := a[0].(type) {
	case string:
		text = s
	case Markup:
		text = string(s)
		peak = 2
	case *Undefined:
		return 2 + failFrames(s)
	case *Namespace:
		return 1 + hasattrFrames(s)
	default:
		return 1
	}
	lines := pySplitlines(text, false)
	if len(lines) == 0 {
		return peak
	}
	if isUndef(a[1]) {
		return 4 + failFrames(a[1])
	}
	width, err := argInt(a[1])
	if err != nil || width <= 0 {
		return 5
	}
	for _, line := range lines {
		if _, scanned := textWrap(line, width, truthy(a[2]), truthy(a[4])); scanned {
			return 6
		}
	}
	return 5
}

// Frames of do_xmlattr, escape of each kept key and value inside the attribute loop
func (r *renderer) xmlattrModel(key string, v Value, args []Value, kw *Dict) int {
	a, err := filterArgs("do_xmlattr", v, []param{req("d"), opt("autospace", true)}, args, kw)
	if err != nil {
		return 0
	}
	d, ok := a[0].(*Dict)
	if !ok {
		if isUndef(a[0]) {
			return 2 + failFrames(a[0])
		}
		return 1 + hasattrFrames(a[0])
	}
	peak := 1
	for i, k := range d.keys {
		val := d.vals[i]
		peak = max(peak, 1+isinstanceFrames(val))
		if _, isNone := val.(NoneType); isNone || isUndef(val) {
			continue
		}
		ks, ok := asStr(k)
		if !ok || attrKeyRe.MatchString(ks) {
			return peak
		}
		peak = max(peak, 1+escapeFrames(k), 1+escapeFrames(val))
	}
	if r.autoescape {
		peak = max(peak, 2)
	}
	return peak
}

// Frames of test_in, the membership check comparing or hashing the value against the container
func (r *renderer) testInModel(key string, v Value, args []Value, kw *Dict) int {
	if len(args) != 1 || len(kw.keys) > 0 {
		return 0
	}
	return 1 + r.containsFrames(v, args[0])
}

// Frames x in seq pushes below its caller, Undefined.__eq__ per compared item, Undefined.__hash__ or Undefined.__iter__
func (r *renderer) containsFrames(x, seq Value) int {
	switch s := seq.(type) {
	case *List, *Tuple, *Range:
		var items []Value
		r.quietly(func() { items, _ = iterValues(s) })
		peak := 0
		for _, it := range items {
			if sameObject(it, x) {
				return peak
			}
			f, _ := compareFrames("==", it, x)
			peak = max(peak, f)
			if looseEqual(it, x) {
				return peak
			}
		}
		return peak
	case *Dict:
		if isUndef(x) {
			return 1
		}
	case *DictView:
		if isUndef(x) && s.kind != "values" {
			return 1
		}
		if s.kind == "values" {
			return r.containsFrames(x, &List{items: s.dict.vals})
		}
	case *Undefined:
		return 1
	case *Iterator, *LoopContext:
		items, frames := r.stepFrames(s)
		peak := 0
		for k, it := range items {
			peak = max(peak, frames[k])
			f, _ := compareFrames("==", it, x)
			peak = max(peak, f)
			if sameObject(it, x) || looseEqual(it, x) {
				return peak
			}
		}
		return max(peak, frames[len(items)])
	}
	return 0
}

// Items an iterator or loop yields from here and each next() step's frames, the last ending it
func (r *renderer) stepFrames(v Value) ([]Value, []int) {
	switch x := v.(type) {
	case *LoopContext:
		items, frames, _ := r.loopSourceItems(x, x.consumed)
		frames[0] = max(frames[0], 1)
		return items, frames
	case *Iterator:
		items, _, _ := r.iteratorPulls(x, x.taken)
		frames := make([]int, len(items)+1)
		for k := range frames {
			frames[k] = x.windowFrames(r, x.taken+k, x.taken+k+1)
		}
		return items, frames
	}
	return nil, []int{0}
}

// Frames of the first filter, its async variant wrapper then next(iter(seq)) or an Undefined for an empty one
func (r *renderer) firstModel(key string, v Value, args []Value, kw *Dict) int {
	a, err := filterArgs("sync_do_first", v, []param{req("seq")}, args, kw)
	if err != nil {
		return 3
	}
	switch x := a[0].(type) {
	case *LoopContext, *Iterator:
		items, frames := r.stepFrames(x)
		if len(items) == 0 {
			return max(3, 2+frames[0], 2+warmBuiltinFrames("undefined", nil))
		}
		return max(3, 2+frames[0])
	}
	return 3
}

// Frames of generate_lorem_ipsum called through the sandbox, randrange and choice per word and escape under html
func (r *renderer) lipsumModel(key string, v Value, args []Value, kw *Dict) int {
	a, err := bindArgs("generate_lorem_ipsum", []param{opt("n", int64(5)), opt("html", true), opt("min", int64(20)), opt("max", int64(100))}, args, kw)
	if err != nil {
		return contextCallFrames
	}
	n, err := argInt(a[0])
	if err != nil {
		return contextCallFrames
	}
	html := truthy(a[1])
	if n <= 0 {
		if html {
			return 4
		}
		return 3
	}
	lo, err1 := argInt(a[2])
	hi, err2 := argInt(a[3])
	if err1 != nil || err2 != nil || hi <= lo {
		return 4
	}
	if html {
		return 6
	}
	return 5
}

// Frames of str.format or Markup.format through the sandbox's formatter, vformat and _vformat per nesting level
func (r *renderer) formatModel(key string, v Value, args []Value, kw *Dict) int {
	s, isStr := asStr(v)
	if !isStr {
		return contextCallFrames
	}
	peak := 5
	r.quietly(func() { _, _ = r.strFormatTracked(s, args, kw, isMarkup(v), func(f int) { peak = max(peak, f) }) })
	return peak
}

// Frames the sandbox getattr pushes on a LoopContext before pulls, is_internal_attribute, the private check or an Undefined
func (r *renderer) loopAttrModel(key string, v Value, args []Value, kw *Dict) int {
	switch key[strings.LastIndex(key, " ")+1:] {
	case "attr":
		return 4
	case "private":
		return 3
	}
	return 1 + warmBuiltinFrames("undefined", nil)
}

// Frames len() pushes, LoopContext.__len__ reading its length property before any pull, the measured C or Python __len__ otherwise
func (r *renderer) lenModel(key string, v Value, args []Value, kw *Dict) int {
	if _, ok := v.(*LoopContext); ok && len(args) == 0 && len(kw.keys) == 0 {
		return 2
	}
	return warmBuiltinFrames(key, v)
}
