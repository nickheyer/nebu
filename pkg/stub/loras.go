package stub

import (
	"slices"
	"strings"
	"time"
)

type loraState int

const (
	loraWaiting loraState = iota
	loraLoading
	loraResident
)

// An adapter the stub knows and where it stands
type lora struct {
	name    string
	path    string
	created int64
	state   loraState
	ready   time.Time
	loaded  chan struct{}
	fail    *apiError
}

// Adapters holding slots and the waiting list behind them
type loraSet struct {
	max      int
	resident []*lora
	loading  []*lora
	waiting  []*lora
	byName   map[string]*lora
}

// An empty set with max slots
func newLoraSet(max int) *loraSet { return &loraSet{max: max, byName: map[string]*lora{}} }

// Whether the named adapter is loaded and serving
func (ls *loraSet) isResident(name string) bool {
	l := ls.byName[name]
	return l != nil && l.state == loraResident
}

// Whether a slot is free for another load
func (ls *loraSet) open() bool { return len(ls.resident)+len(ls.loading) < ls.max }

// Adapters listed comma separated in order
func joinNames(ls []*lora) string {
	names := make([]string, len(ls))
	for i, l := range ls {
		names[i] = l.name
	}
	return strings.Join(names, ",")
}

// Starts loading an adapter into an open slot or appends it to the waiting list
func (e *engine) loadAdapter(name, path string, now time.Time) (*lora, bool, *apiError) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return nil, false, errShutdown()
	}
	if name == e.cfg.Model {
		return nil, false, badRequest("lora_name", "The lora adapter '%s' collides with the base model name.", name)
	}
	if e.loras.byName[name] != nil {
		return nil, false, badRequest("lora_name", "The lora adapter '%s' has already been loaded.", name)
	}
	l := &lora{name: name, path: path, created: now.Unix(), loaded: make(chan struct{})}
	e.loras.byName[name] = l
	e.changed = now
	if !e.loras.open() {
		e.loras.waiting = append(e.loras.waiting, l)
		return l, true, nil
	}
	e.startLoadLocked(l, now)
	return l, false, nil
}

// Puts an adapter in a slot, resident at once when its load takes no wall time
func (e *engine) startLoadLocked(l *lora, now time.Time) {
	d := e.wall(e.cfg.LoraLoadDelay)
	if d <= 0 {
		e.residentLocked(l)
		return
	}
	l.state, l.ready = loraLoading, now.Add(d)
	e.loras.loading = append(e.loras.loading, l)
	e.kick()
}

// Gives an adapter its slot as serving and releases requests and loaders waiting on it
func (e *engine) residentLocked(l *lora) {
	l.state = loraResident
	e.loras.resident = append(e.loras.resident, l)
	close(l.loaded)
	e.kick()
}

// Makes adapters whose load delay has passed resident
func (e *engine) finishLoadsLocked(now time.Time) {
	keep := e.loras.loading[:0]
	for _, l := range e.loras.loading {
		if l.ready.After(now) {
			keep = append(keep, l)
			continue
		}
		e.residentLocked(l)
		e.changed = now
	}
	clear(e.loras.loading[len(keep):])
	e.loras.loading = keep
}

// Ends a pending adapter with an error for whoever waits on it
func (e *engine) dropLoadLocked(l *lora, err *apiError) {
	l.fail = err
	close(l.loaded)
}

// Removes an adapter wherever it stands, failing queued requests for it that never started
func (e *engine) unloadAdapter(name string, now time.Time) *apiError {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return errShutdown()
	}
	l := e.loras.byName[name]
	if l == nil {
		return errAdapterNotFound(name)
	}
	delete(e.loras.byName, name)
	is := func(o *lora) bool { return o == l }
	switch l.state {
	case loraResident:
		e.loras.resident = slices.DeleteFunc(e.loras.resident, is)
	case loraLoading:
		e.loras.loading = slices.DeleteFunc(e.loras.loading, is)
		e.dropLoadLocked(l, errAdapterUnloaded(name))
	case loraWaiting:
		e.loras.waiting = slices.DeleteFunc(e.loras.waiting, is)
		e.dropLoadLocked(l, errAdapterUnloaded(name))
	}
	seen := map[*group]bool{}
	var failed []*group
	for _, s := range e.queue {
		if !s.admitted && s.g.adapter == name && !seen[s.g] {
			seen[s.g] = true
			failed = append(failed, s.g)
		}
	}
	for _, g := range failed {
		e.failLocked(g, errModelNotFound(name))
	}
	for e.loras.open() && len(e.loras.waiting) > 0 {
		next := e.loras.waiting[0]
		e.loras.waiting = slices.Delete(e.loras.waiting, 0, 1)
		e.startLoadLocked(next, now)
	}
	e.changed = now
	e.kick()
	return nil
}

// Serving adapters copied in load order
func (e *engine) residentAdapters() []lora {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]lora, len(e.loras.resident))
	for i, l := range e.loras.resident {
		out[i] = lora{name: l.name, path: l.path, created: l.created}
	}
	return out
}
