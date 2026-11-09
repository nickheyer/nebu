package datalayer

import (
	"cmp"
	"context"
	"maps"
	"slices"
	"sync"
	"sync/atomic"
)

// Builds an empty store whose reads never take a lock
func NewStore() Store {
	s := &memStore{}
	s.snap.Store(&snapshot{
		pools:     map[PoolID]Pool{},
		endpoints: map[EndpointID]*cell{},
		byPool:    map[PoolID][]*cell{},
	})
	s.log.tail = &logNode{ready: make(chan struct{})}
	return s
}

type memStore struct {
	// Serializes writers of pools and endpoint membership
	mu     sync.Mutex
	snap   atomic.Pointer[snapshot]
	log    changeLog
	synced atomic.Bool
}

// An immutable view of pools and endpoint membership, replaced whole by writers
type snapshot struct {
	pools     map[PoolID]Pool
	poolList  []Pool
	endpoints map[EndpointID]*cell
	all       []*cell
	byPool    map[PoolID][]*cell
}

// Holds the current version of one endpoint, swapped whole on every spec or attribute write
type cell struct {
	mu  sync.Mutex
	cur atomic.Pointer[version]
	// Set under mu once a newer snapshot no longer holds this cell
	retired bool
}

// An immutable endpoint spec and attribute set served to readers as the Endpoint
type version struct {
	spec  EndpointSpec
	attrs map[string]any
}

func (v *version) ID() EndpointID            { return v.spec.ID }
func (v *version) Pool() PoolID              { return v.spec.Pool }
func (v *version) Address() Address          { return v.spec.Address }
func (v *version) Labels() map[string]string { return v.spec.Labels }
func (v *version) Ready() bool               { return v.spec.Ready }

func (v *version) Attribute(collector string) (any, bool) {
	a, ok := v.attrs[collector]
	return a, ok
}

func (s *memStore) PutPool(p Pool) {
	p = clonePool(p)
	s.mu.Lock()
	defer s.mu.Unlock()
	cur := s.snap.Load()
	next := *cur
	next.pools = maps.Clone(cur.pools)
	next.pools[p.ID] = p
	next.poolList = sortedPools(next.pools)
	s.snap.Store(&next)
	s.log.append(Change{Kind: ChangePoolPut, Pool: p.ID})
}

func (s *memStore) DeletePool(id PoolID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur := s.snap.Load()
	if _, ok := cur.pools[id]; !ok {
		return
	}
	members := cur.byPool[id]
	for _, c := range members {
		c.mu.Lock()
	}
	next := *cur
	next.pools = maps.Clone(cur.pools)
	delete(next.pools, id)
	next.poolList = sortedPools(next.pools)
	next.endpoints = maps.Clone(cur.endpoints)
	next.byPool = maps.Clone(cur.byPool)
	delete(next.byPool, id)
	next.all = slices.DeleteFunc(slices.Clone(cur.all), func(c *cell) bool { return c.cur.Load().spec.Pool == id })
	for _, c := range members {
		delete(next.endpoints, c.cur.Load().spec.ID)
	}
	s.snap.Store(&next)
	for _, c := range members {
		s.log.append(Change{Kind: ChangeEndpointDeleted, Pool: id, Endpoint: c.cur.Load().spec.ID})
		c.retired = true
		c.mu.Unlock()
	}
	s.log.append(Change{Kind: ChangePoolDeleted, Pool: id})
}

func (s *memStore) Pool(id PoolID) (Pool, bool) {
	p, ok := s.snap.Load().pools[id]
	return p, ok
}

func (s *memStore) Pools() []Pool { return slices.Clone(s.snap.Load().poolList) }

func (s *memStore) UpsertEndpoint(spec EndpointSpec) {
	spec.Labels = maps.Clone(spec.Labels)
	s.mu.Lock()
	defer s.mu.Unlock()
	cur := s.snap.Load()
	old := cur.endpoints[spec.ID]
	var attrs map[string]any
	if old != nil {
		old.mu.Lock()
		prev := old.cur.Load()
		attrs = prev.attrs
		// Same pool keeps membership, so the new version replaces the old in place
		if prev.spec.Pool == spec.Pool {
			old.cur.Store(&version{spec: spec, attrs: attrs})
			s.log.append(Change{Kind: ChangeEndpointPut, Pool: spec.Pool, Endpoint: spec.ID})
			old.mu.Unlock()
			return
		}
	}
	nc := &cell{}
	nc.mu.Lock()
	nc.cur.Store(&version{spec: spec, attrs: attrs})
	next := *cur
	next.endpoints = maps.Clone(cur.endpoints)
	next.endpoints[spec.ID] = nc
	next.all = withCell(withoutCell(cur.all, spec.ID), nc)
	next.byPool = maps.Clone(cur.byPool)
	if old != nil {
		from := old.cur.Load().spec.Pool
		if rest := withoutCell(cur.byPool[from], spec.ID); len(rest) > 0 {
			next.byPool[from] = rest
		} else {
			delete(next.byPool, from)
		}
	}
	next.byPool[spec.Pool] = withCell(cur.byPool[spec.Pool], nc)
	s.snap.Store(&next)
	s.log.append(Change{Kind: ChangeEndpointPut, Pool: spec.Pool, Endpoint: spec.ID})
	if old != nil {
		old.retired = true
		old.mu.Unlock()
	}
	nc.mu.Unlock()
}

func (s *memStore) DeleteEndpoint(id EndpointID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur := s.snap.Load()
	c := cur.endpoints[id]
	if c == nil {
		return
	}
	c.mu.Lock()
	pool := c.cur.Load().spec.Pool
	next := *cur
	next.endpoints = maps.Clone(cur.endpoints)
	delete(next.endpoints, id)
	next.all = withoutCell(cur.all, id)
	next.byPool = maps.Clone(cur.byPool)
	if rest := withoutCell(cur.byPool[pool], id); len(rest) > 0 {
		next.byPool[pool] = rest
	} else {
		delete(next.byPool, pool)
	}
	s.snap.Store(&next)
	s.log.append(Change{Kind: ChangeEndpointDeleted, Pool: pool, Endpoint: id})
	c.retired = true
	c.mu.Unlock()
}

func (s *memStore) Endpoint(id EndpointID) (Endpoint, bool) {
	c := s.snap.Load().endpoints[id]
	if c == nil {
		return nil, false
	}
	return c.cur.Load(), true
}

func (s *memStore) Endpoints(pool PoolID, keep func(Endpoint) bool) []Endpoint {
	snap := s.snap.Load()
	cells := snap.all
	if pool != "" {
		cells = snap.byPool[pool]
	}
	out := make([]Endpoint, 0, len(cells))
	for _, c := range cells {
		v := c.cur.Load()
		if keep == nil || keep(v) {
			out = append(out, v)
		}
	}
	return out
}

func (s *memStore) SetAttribute(id EndpointID, collector string, value any) {
	for {
		c := s.snap.Load().endpoints[id]
		if c == nil {
			return
		}
		c.mu.Lock()
		// A retired cell was replaced by an already published snapshot, so the reload finds its successor
		if c.retired {
			c.mu.Unlock()
			continue
		}
		v := c.cur.Load()
		_, had := v.attrs[collector]
		if value == nil && !had {
			c.mu.Unlock()
			return
		}
		attrs := make(map[string]any, len(v.attrs)+1)
		for k, a := range v.attrs {
			attrs[k] = a
		}
		if value == nil {
			delete(attrs, collector)
		} else {
			attrs[collector] = value
		}
		c.cur.Store(&version{spec: v.spec, attrs: attrs})
		s.log.append(Change{Kind: ChangeAttribute, Pool: v.spec.Pool, Endpoint: id, Collector: collector})
		c.mu.Unlock()
		return
	}
}

func (s *memStore) Watch(ctx context.Context) <-chan Change {
	out := make(chan Change)
	n := s.log.end()
	go func() {
		defer close(out)
		for {
			select {
			case <-ctx.Done():
				return
			case <-n.ready:
			}
			n = n.next
			select {
			case out <- n.change:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out
}

func (s *memStore) Synced() bool { return s.synced.Load() }

func (s *memStore) MarkSynced() { s.synced.Store(true) }

// An unbounded singly linked change log every watcher walks at its own pace
type changeLog struct {
	mu   sync.Mutex
	tail *logNode
}

// One logged change, its successor readable once ready is closed
type logNode struct {
	change Change
	next   *logNode
	ready  chan struct{}
}

// Links a change after the tail and wakes every watcher waiting on the old tail
func (l *changeLog) append(c Change) {
	n := &logNode{change: c, ready: make(chan struct{})}
	l.mu.Lock()
	l.tail.next = n
	close(l.tail.ready)
	l.tail = n
	l.mu.Unlock()
}

// The newest node, after which a new watcher starts reading
func (l *changeLog) end() *logNode {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.tail
}

// A deep copy of a pool sharing no map or slice with the caller
func clonePool(p Pool) Pool {
	p.Selector = maps.Clone(p.Selector)
	p.TargetPorts = slices.Clone(p.TargetPorts)
	p.Metrics = p.Metrics.Clone()
	return p
}

// Pools ordered by id
func sortedPools(m map[PoolID]Pool) []Pool {
	out := slices.Collect(maps.Values(m))
	slices.SortFunc(out, func(a, b Pool) int { return cmp.Compare(a.ID, b.ID) })
	return out
}

// A copy of an id ordered cell list with the cell inserted at its place
func withCell(list []*cell, c *cell) []*cell {
	id := c.cur.Load().spec.ID
	i, _ := slices.BinarySearchFunc(list, id, cellOrder)
	return slices.Insert(slices.Clone(list), i, c)
}

// A copy of an id ordered cell list without the cell of that id
func withoutCell(list []*cell, id EndpointID) []*cell {
	i, found := slices.BinarySearchFunc(list, id, cellOrder)
	if !found {
		return list
	}
	return slices.Delete(slices.Clone(list), i, i+1)
}

func cellOrder(c *cell, id EndpointID) int { return cmp.Compare(c.cur.Load().spec.ID, id) }
