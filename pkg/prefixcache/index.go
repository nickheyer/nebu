package prefixcache

import (
	"math"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/cespare/xxhash/v2"
	"github.com/nickheyer/nebu/pkg/datalayer"
)

// Values a zero or negative config field takes
const (
	DefaultChunkChars    = 64
	DefaultChunkTokens   = 16
	DefaultBlockCapacity = 31250
	DefaultMaxChunks     = 256
	DefaultMaxEntries    = 1 << 22
)

const (
	shardCount = 256
	groupCount = 32
	// Holders past this count move from a scanned slice to a map
	bigHolders = 16
	// Entries a sweep removes per endpoint lock hold
	sweepBatch = 1024
	cacheLine  = 64
)

// Estimated heap bytes, map slots costing twice their size and holder slices a third over their length
const (
	entryBytes    = int64(unsafe.Sizeof(entry{}) + unsafe.Sizeof(holder{})*4/3)
	hashBytes     = int64(unsafe.Sizeof(BlockHash(0))+unsafe.Sizeof(holders{})+1) * 2
	bigSlotBytes  = int64(2*unsafe.Sizeof((*endpoint)(nil))+1)*2 - int64(unsafe.Sizeof(holder{})*4/3)
	endpointBytes = int64(unsafe.Sizeof(endpoint{})) + syncMapEntryBytes
)

// Heap one sync.Map entry holds with its share of trie nodes
const syncMapEntryBytes = 96

// One hash held by one endpoint, linked into that endpoint's approximate or reported list
type entry struct {
	hash BlockHash
	ep   *endpoint
	prev *entry
	next *entry
	// Clock reading of the last record, match or report, readable under the shard lock
	touched atomic.Int64
	// Report generation that pinned the entry
	gen uint32
	// Written under both the endpoint and shard locks
	reported bool
	live     bool
}

// Doubly linked entries, most recently touched at the head
type entryList struct {
	head *entry
	tail *entry
	n    int
}

// Links the entry as most recent
func (l *entryList) pushFront(e *entry) {
	e.prev, e.next = nil, l.head
	if l.head != nil {
		l.head.prev = e
	} else {
		l.tail = e
	}
	l.head = e
	l.n++
}

// Removes the entry from the list
func (l *entryList) unlink(e *entry) {
	if e.prev != nil {
		e.prev.next = e.next
	} else {
		l.head = e.next
	}
	if e.next != nil {
		e.next.prev = e.prev
	} else {
		l.tail = e.prev
	}
	e.prev, e.next = nil, nil
	l.n--
}

// Makes the entry the most recent
func (l *entryList) moveFront(e *entry) {
	if l.head == e {
		return
	}
	l.unlink(e)
	l.pushFront(e)
}

// Inserts an unlinked entry right after prev, or at the head when prev is nil
func (l *entryList) link(prev, e *entry) {
	if prev == nil {
		l.pushFront(e)
		return
	}
	e.prev, e.next = prev, prev.next
	if prev.next != nil {
		prev.next.prev = e
	} else {
		l.tail = e
	}
	prev.next = e
	l.n++
}

// Moves a linked entry right after prev, or to the head when prev is nil, unless already there
func (l *entryList) place(prev, e *entry) {
	if prev == nil {
		l.moveFront(e)
		return
	}
	if prev.next == e {
		return
	}
	l.unlink(e)
	l.link(prev, e)
}

// Last entry placed in each list while touching one request's entries in order
type placer struct {
	approx   *entry
	reported *entry
}

// Places the entry after the request's previous entry in its list, so the request ends at the head in order
func (p *placer) put(ep *endpoint, e *entry, fresh bool) {
	l, last := &ep.approx, &p.approx
	if e.reported {
		l, last = &ep.reported, &p.reported
	}
	switch {
	case fresh:
		l.link(*last, e)
	case *last != e:
		l.place(*last, e)
	}
	*last = e
}

// One slot naming an endpoint inline beside its entry so scans never dereference entries
type holder struct {
	ep *endpoint
	e  *entry
}

// Endpoints holding one hash, a scanned slice while few and a map once many
type holders struct {
	list []holder
	big  map[*endpoint]*entry
}

// Holders counted
func (h *holders) size() int {
	if h.big != nil {
		return len(h.big)
	}
	return len(h.list)
}

// The entry the endpoint holds, nil when it holds none
func (h *holders) get(ep *endpoint) *entry {
	if h.big != nil {
		return h.big[ep]
	}
	for _, s := range h.list {
		if s.ep == ep {
			return s.e
		}
	}
	return nil
}

// Adds a holder and returns the change in map held holders
func (h *holders) add(e *entry) int {
	if h.big != nil {
		h.big[e.ep] = e
		return 1
	}
	h.list = append(h.list, holder{e.ep, e})
	if len(h.list) <= bigHolders {
		return 0
	}
	h.big = make(map[*endpoint]*entry, len(h.list))
	for _, s := range h.list {
		h.big[s.ep] = s.e
	}
	h.list = nil
	return len(h.big)
}

// Removes a holder and returns the change in map held holders
func (h *holders) remove(e *entry) int {
	if h.big != nil {
		delete(h.big, e.ep)
		if len(h.big) > bigHolders/2 {
			return -1
		}
		n := len(h.big)
		h.list = make([]holder, 0, bigHolders)
		for ep, o := range h.big {
			h.list = append(h.list, holder{ep, o})
		}
		h.big = nil
		return -n - 1
	}
	for i, s := range h.list {
		if s.e == e {
			last := len(h.list) - 1
			h.list[i] = h.list[last]
			h.list[last] = holder{}
			h.list = h.list[:last]
			break
		}
	}
	return 0
}

// Hashes sharing low bits and the lock guarding their holders
type shard struct {
	mu sync.RWMutex
	m  map[BlockHash]holders
	_  [cacheLine - (unsafe.Sizeof(sync.RWMutex{})+unsafe.Sizeof(map[BlockHash]holders(nil)))%cacheLine]byte
}

// Endpoints with entries ordered by last use, most recent at the head
type group struct {
	mu   sync.Mutex
	head *endpoint
	tail *endpoint
	_    [cacheLine - (unsafe.Sizeof(sync.Mutex{})+2*unsafe.Sizeof((*endpoint)(nil)))%cacheLine]byte
}

// Stamps the endpoint with the clock under the group lock and moves it to the head
func (g *group) use(ep *endpoint, clock func() int64) {
	g.mu.Lock()
	ep.lastUsed = clock()
	if g.head != ep {
		if ep.linked {
			g.unlink(ep)
		}
		ep.gprev, ep.gnext = nil, g.head
		if g.head != nil {
			g.head.gprev = ep
		} else {
			g.tail = ep
		}
		g.head = ep
		ep.linked = true
	}
	g.mu.Unlock()
}

// Takes an endpoint without entries out of the order
func (g *group) remove(ep *endpoint) {
	g.mu.Lock()
	if ep.linked {
		g.unlink(ep)
	}
	g.mu.Unlock()
}

// Removes the endpoint from the order with the group lock held
func (g *group) unlink(ep *endpoint) {
	if ep.gprev != nil {
		ep.gprev.gnext = ep.gnext
	} else {
		g.head = ep.gnext
	}
	if ep.gnext != nil {
		ep.gnext.gprev = ep.gprev
	} else {
		g.tail = ep.gprev
	}
	ep.gprev, ep.gnext = nil, nil
	ep.linked = false
}

// Entries, capacity and global use order position of one model server port
type endpoint struct {
	id    datalayer.EndpointID
	group *group

	mu       sync.Mutex
	approx   entryList
	reported entryList
	// Approximate entries kept, zero taking the configured default
	capacity int
	gen      uint32
	gone     bool

	// Guarded by the group lock
	lastUsed int64
	gprev    *endpoint
	gnext    *endpoint
	linked   bool
}

// Approximate and reported entries counted
func (ep *endpoint) count() int { return ep.approx.n + ep.reported.n }

// Sharded hash to endpoint map with per endpoint LRU lists, implementing Indexer
type Index struct {
	cfg   Config
	ttl   int64
	clock func() int64

	shards [shardCount]shard
	groups [groupCount]group
	eps    sync.Map

	entries   atomic.Int64
	hashes    atomic.Int64
	endpoints atomic.Int64
	idBytes   atomic.Int64
	bigSlots  atomic.Int64

	evictMu sync.Mutex
	stop    chan struct{}
	done    chan struct{}
	once    sync.Once
}

var _ Indexer = (*Index)(nil)

// Builds an index and starts its TTL sweep when TTL is positive
func New(cfg Config) *Index {
	epoch := time.Now()
	return newIndex(cfg, func() int64 { return int64(time.Since(epoch)) })
}

// Builds an index reading time from clock in monotonic nanoseconds
func newIndex(cfg Config, clock func() int64) *Index {
	cfg = withDefaults(cfg)
	x := &Index{cfg: cfg, ttl: int64(cfg.TTL), clock: clock}
	for i := range x.shards {
		x.shards[i].m = make(map[BlockHash]holders)
	}
	if cfg.TTL > 0 {
		x.stop = make(chan struct{})
		x.done = make(chan struct{})
		go x.sweepLoop()
	}
	return x
}

// Replaces zero or negative fields with their defaults
func withDefaults(cfg Config) Config {
	if cfg.ChunkChars <= 0 {
		cfg.ChunkChars = DefaultChunkChars
	}
	if cfg.ChunkTokens <= 0 {
		cfg.ChunkTokens = DefaultChunkTokens
	}
	if cfg.DefaultBlocks <= 0 {
		cfg.DefaultBlocks = DefaultBlockCapacity
	}
	if cfg.MaxChunks <= 0 {
		cfg.MaxChunks = DefaultMaxChunks
	}
	if cfg.MaxEntries <= 0 {
		cfg.MaxEntries = DefaultMaxEntries
	}
	return cfg
}

// Stops the TTL sweep and waits for it to exit
func (x *Index) Close() {
	if x.stop == nil {
		return
	}
	x.once.Do(func() {
		close(x.stop)
		<-x.done
	})
}

// Lock and holders map owning a hash, picked by its low bits
func (x *Index) shard(h BlockHash) *shard { return &x.shards[uint64(h)&(shardCount-1)] }

// The endpoint for an id, created on first use
func (x *Index) endpointFor(id datalayer.EndpointID) *endpoint {
	if v, ok := x.eps.Load(id); ok {
		return v.(*endpoint)
	}
	fresh := &endpoint{id: id, group: &x.groups[xxhash.Sum64String(string(id))%groupCount]}
	v, loaded := x.eps.LoadOrStore(id, fresh)
	if !loaded {
		x.endpoints.Add(1)
		x.idBytes.Add(int64(len(id)))
	}
	return v.(*endpoint)
}

// The live endpoint for an id with its lock held, replacing one a concurrent forget retired
func (x *Index) locked(id datalayer.EndpointID) *endpoint {
	for {
		ep := x.endpointFor(id)
		ep.mu.Lock()
		if !ep.gone {
			return ep
		}
		ep.mu.Unlock()
		x.retire(ep)
	}
}

// Deletes a forgotten endpoint from the registry once
func (x *Index) retire(ep *endpoint) {
	if x.eps.CompareAndDelete(ep.id, ep) {
		x.endpoints.Add(-1)
		x.idBytes.Add(-int64(len(ep.id)))
	}
}

// Marks the endpoint used or takes it out of the use order once it holds nothing
func (x *Index) settle(ep *endpoint, used bool) {
	switch {
	case ep.count() == 0:
		ep.group.remove(ep)
	case used:
		ep.group.use(ep, x.clock)
	}
}

// Removes an entry from its list and its hash, with the endpoint lock held
func (x *Index) drop(l *entryList, e *entry) {
	l.unlink(e)
	sh := x.shard(e.hash)
	sh.mu.Lock()
	hs := sh.m[e.hash]
	x.bigSlots.Add(int64(hs.remove(e)))
	if hs.size() == 0 {
		delete(sh.m, e.hash)
		x.hashes.Add(-1)
	} else {
		sh.m[e.hash] = hs
	}
	e.live = false
	sh.mu.Unlock()
	x.entries.Add(-1)
}

// Adds the endpoint to a hash's holders, returning the new entry or the one already held
func (x *Index) hold(ep *endpoint, h BlockHash, now int64, reported bool) (*entry, bool) {
	sh := x.shard(h)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	hs := sh.m[h]
	if e := hs.get(ep); e != nil {
		return e, false
	}
	if hs.size() == 0 {
		x.hashes.Add(1)
	}
	e := &entry{hash: h, ep: ep, reported: reported, gen: ep.gen, live: true}
	e.touched.Store(now)
	x.bigSlots.Add(int64(hs.add(e)))
	sh.m[h] = hs
	x.entries.Add(1)
	return e, true
}

// Evicts the least recently used approximate entries past the endpoint's capacity
func (x *Index) trim(ep *endpoint) {
	limit := ep.capacity
	if limit == 0 {
		limit = x.cfg.DefaultBlocks
	}
	for ep.approx.n > limit {
		x.drop(&ep.approx, ep.approx.tail)
	}
}

// Marks every hash held by the endpoint, the first chunk most recent so prefixes outlive their tails
func (x *Index) Record(id datalayer.EndpointID, hashes []BlockHash) {
	if len(hashes) == 0 {
		return
	}
	ep := x.locked(id)
	now := x.clock()
	var p placer
	for _, h := range hashes {
		e, created := x.hold(ep, h, now, false)
		if !created {
			e.touched.Store(now)
		}
		p.put(ep, e, created)
	}
	x.trim(ep)
	x.settle(ep, true)
	ep.mu.Unlock()
	x.enforceCap()
}

// Drops the endpoint's approximate entry of every hash, leaving entries its own report pinned
func (x *Index) Unrecord(id datalayer.EndpointID, hashes []BlockHash) {
	if len(hashes) == 0 {
		return
	}
	v, ok := x.eps.Load(id)
	if !ok {
		return
	}
	ep := v.(*endpoint)
	ep.mu.Lock()
	if !ep.gone {
		for _, h := range hashes {
			if e := x.held(ep, h); e != nil && !e.reported {
				x.drop(&ep.approx, e)
			}
		}
		x.settle(ep, false)
	}
	ep.mu.Unlock()
}

// The entry the endpoint holds for a hash, nil when none, with the endpoint lock held
func (x *Index) held(ep *endpoint, h BlockHash) *entry {
	sh := x.shard(h)
	sh.mu.RLock()
	defer sh.mu.RUnlock()
	hs := sh.m[h]
	return hs.get(ep)
}

// Sets the approximate entries kept for the endpoint, zero or negative restoring the default
func (x *Index) SetCapacity(id datalayer.EndpointID, blocks int) {
	ep := x.locked(id)
	ep.capacity = max(blocks, 0)
	x.trim(ep)
	x.settle(ep, false)
	ep.mu.Unlock()
}

// Replaces every entry of the endpoint with the reported hashes, pinned until the next report
func (x *Index) Report(id datalayer.EndpointID, hashes []BlockHash) {
	ep := x.locked(id)
	old := ep.reported
	ep.reported = entryList{}
	ep.gen++
	now := x.clock()
	for i := len(hashes) - 1; i >= 0; i-- {
		x.pin(ep, &old, hashes[i], now)
	}
	for ep.approx.tail != nil {
		x.drop(&ep.approx, ep.approx.tail)
	}
	for old.tail != nil {
		x.drop(&old, old.tail)
	}
	x.settle(ep, true)
	ep.mu.Unlock()
	x.enforceCap()
}

// Moves or adds one reported hash into the current reported list
func (x *Index) pin(ep *endpoint, old *entryList, h BlockHash, now int64) {
	e, created := x.hold(ep, h, now, true)
	if created {
		ep.reported.pushFront(e)
		return
	}
	var from *entryList
	switch {
	case !e.reported:
		from = &ep.approx
		sh := x.shard(h)
		sh.mu.Lock()
		e.reported = true
		sh.mu.Unlock()
	case e.gen != ep.gen:
		from = old
	default:
		from = &ep.reported
	}
	e.gen = ep.gen
	e.touched.Store(now)
	from.unlink(e)
	ep.reported.pushFront(e)
}

// Removes the endpoint and every entry it holds by walking its own lists
func (x *Index) Forget(id datalayer.EndpointID) {
	v, ok := x.eps.Load(id)
	if !ok {
		return
	}
	ep := v.(*endpoint)
	ep.mu.Lock()
	if !ep.gone {
		ep.gone = true
		for ep.approx.tail != nil {
			x.drop(&ep.approx, ep.approx.tail)
		}
		for ep.reported.tail != nil {
			x.drop(&ep.reported, ep.reported.tail)
		}
		ep.group.remove(ep)
	}
	ep.mu.Unlock()
	x.retire(ep)
}

// Distinct hashes held by at least one endpoint
func (x *Index) Len() int { return int(x.hashes.Load()) }

// Counts and the estimated heap the index holds
func (x *Index) Stats() Stats {
	e, h, n := x.entries.Load(), x.hashes.Load(), x.endpoints.Load()
	return Stats{
		Hashes:    int(h),
		Entries:   int(e),
		Endpoints: int(n),
		Bytes:     e*entryBytes + h*hashBytes + n*endpointBytes + x.idBytes.Load() + x.bigSlots.Load()*bigSlotBytes,
	}
}

// Evicts the globally least recently used endpoints' oldest entries until the cap holds
func (x *Index) enforceCap() {
	limit := int64(x.cfg.MaxEntries)
	if x.entries.Load() <= limit {
		return
	}
	x.evictMu.Lock()
	defer x.evictMu.Unlock()
	for x.entries.Load() > limit {
		victim := x.leastRecent()
		if victim == nil {
			return
		}
		victim.mu.Lock()
		if !victim.gone {
			excess := x.entries.Load() - limit
			for ; excess > 0 && victim.approx.tail != nil; excess-- {
				x.drop(&victim.approx, victim.approx.tail)
			}
			for ; excess > 0 && victim.reported.tail != nil; excess-- {
				x.drop(&victim.reported, victim.reported.tail)
			}
			x.settle(victim, false)
		}
		victim.mu.Unlock()
	}
}

// The endpoint whose last use is oldest across every group tail
func (x *Index) leastRecent() *endpoint {
	var victim *endpoint
	var oldest int64
	for i := range x.groups {
		g := &x.groups[i]
		g.mu.Lock()
		if t := g.tail; t != nil && (victim == nil || t.lastUsed < oldest) {
			victim, oldest = t, t.lastUsed
		}
		g.mu.Unlock()
	}
	return victim
}

// Removes expired entries every half TTL until closed
func (x *Index) sweepLoop() {
	defer close(x.done)
	t := time.NewTicker(max(x.cfg.TTL/2, 1))
	defer t.Stop()
	for {
		select {
		case <-x.stop:
			return
		case <-t.C:
			x.sweep(x.clock() - x.ttl)
		}
	}
}

// Removes every entry last touched before cutoff, a batch per endpoint lock hold
func (x *Index) sweep(cutoff int64) {
	x.eps.Range(func(_, v any) bool {
		ep := v.(*endpoint)
		for x.expire(ep, cutoff) {
		}
		return true
	})
}

// Removes up to a batch of expired entries, true when expired entries remain
func (x *Index) expire(ep *endpoint, cutoff int64) bool {
	ep.mu.Lock()
	defer ep.mu.Unlock()
	if ep.gone {
		return false
	}
	budget := sweepBatch
	budget = x.expireList(&ep.approx, cutoff, budget)
	budget = x.expireList(&ep.reported, cutoff, budget)
	x.settle(ep, false)
	return budget == 0 && (expired(ep.approx.tail, cutoff) || expired(ep.reported.tail, cutoff))
}

// Drops expired entries from the tail within budget and returns the budget left
func (x *Index) expireList(l *entryList, cutoff int64, budget int) int {
	for budget > 0 && expired(l.tail, cutoff) {
		x.drop(l, l.tail)
		budget--
	}
	return budget
}

// Whether the entry exists and was last touched before cutoff
func expired(e *entry, cutoff int64) bool { return e != nil && e.touched.Load() < cutoff }

// One candidate's progress through a match
type tally struct {
	id datalayer.EndpointID
	// Endpoint the id resolved to, nil before resolving and absent when the index never saw it
	ep       *endpoint
	blocks   int32
	reported int32
}

// Resolution of a candidate the index holds nothing for
var absent = &endpoint{}

// A matched entry and the candidate it counted for
type hit struct {
	cand int32
	e    *entry
}

// Buffers one match reuses from a pool so lookups stay off the allocator
type scratch struct {
	t      []tally
	alive  []int32
	hits   []hit
	start  []int32
	byCand []*entry
	index  map[datalayer.EndpointID]int32
}

var scratchPool = sync.Pool{New: func() any {
	return &scratch{index: make(map[datalayer.EndpointID]int32)}
}}

// Clears every pointer the buffers hold and returns them to the pool
func (sc *scratch) release() {
	clear(sc.t)
	clear(sc.hits)
	clear(sc.byCand)
	clear(sc.index)
	sc.t, sc.alive, sc.hits, sc.start, sc.byCand = sc.t[:0], sc.alive[:0], sc.hits[:0], sc.start[:0], sc.byCand[:0]
	scratchPool.Put(sc)
}

// Fills deduplicated candidates, indexing them by id once there are more than a scan's worth
func (sc *scratch) tallies(candidates []datalayer.EndpointID) bool {
	if len(candidates) <= bigHolders {
	next:
		for _, id := range candidates {
			for _, have := range sc.t {
				if have.id == id {
					continue next
				}
			}
			sc.t = append(sc.t, tally{id: id})
		}
		return false
	}
	for _, id := range candidates {
		if _, dup := sc.index[id]; dup {
			continue
		}
		sc.index[id] = int32(len(sc.t))
		sc.t = append(sc.t, tally{id: id})
	}
	return true
}

// Counts each candidate's leading held hashes, touches the matched entries, best first
func (x *Index) Match(hashes []BlockHash, candidates []datalayer.EndpointID) []Match {
	if len(candidates) == 0 {
		return nil
	}
	sc := scratchPool.Get().(*scratch)
	defer sc.release()
	indexed := sc.tallies(candidates)
	t := sc.t
	cutoff := int64(math.MinInt64)
	if x.ttl > 0 {
		cutoff = x.clock() - x.ttl
	}
	for i := range t {
		sc.alive = append(sc.alive, int32(i))
	}
	alive, hits := sc.alive, sc.hits
	for depth, h := range hashes {
		sh := x.shard(h)
		sh.mu.RLock()
		hs, ok := sh.m[h]
		if !ok {
			sh.mu.RUnlock()
			break
		}
		next := alive[:0]
		if indexed && hs.size() < len(alive) {
			next, hits = scanHolders(&hs, t, sc.index, depth, cutoff, next, hits)
		} else {
			for _, ci := range alive {
				c := &t[ci]
				if c.ep == nil {
					c.ep = absent
					if v, ok := x.eps.Load(c.id); ok {
						c.ep = v.(*endpoint)
					}
				}
				if c.ep == absent {
					continue
				}
				e := hs.get(c.ep)
				if e == nil || e.touched.Load() < cutoff {
					continue
				}
				c.blocks++
				if e.reported {
					c.reported++
				}
				hits = append(hits, hit{ci, e})
				next = append(next, ci)
			}
		}
		sh.mu.RUnlock()
		alive = next
		if len(alive) == 0 {
			break
		}
	}
	sc.alive, sc.hits = alive, hits
	x.touchHits(sc)
	return ranked(t)
}

// Matched candidates by blocks then reported blocks then id, followed by the rest by id
func ranked(t []tally) []Match {
	held := 0
	for i := range t {
		if t[i].blocks > 0 {
			t[held], t[i] = t[i], t[held]
			held++
		}
	}
	slices.SortFunc(t[:held], func(a, b tally) int {
		if a.blocks != b.blocks {
			return int(b.blocks - a.blocks)
		}
		if a.reported != b.reported {
			return int(b.reported - a.reported)
		}
		return strings.Compare(string(a.id), string(b.id))
	})
	out := make([]Match, len(t))
	for i, c := range t {
		out[i] = Match{Endpoint: c.id, Blocks: int(c.blocks)}
	}
	slices.SortFunc(out[held:], func(a, b Match) int { return strings.Compare(string(a.Endpoint), string(b.Endpoint)) })
	return out
}

// Advances candidates holding a hash by walking the hash's endpoints, used when they are fewer than candidates
func scanHolders(hs *holders, t []tally, index map[datalayer.EndpointID]int32, depth int, cutoff int64, next []int32, hits []hit) ([]int32, []hit) {
	visit := func(e *entry) {
		ci, ok := index[e.ep.id]
		if !ok || int(t[ci].blocks) != depth || e.touched.Load() < cutoff {
			return
		}
		c := &t[ci]
		c.ep = e.ep
		c.blocks++
		if e.reported {
			c.reported++
		}
		hits = append(hits, hit{ci, e})
		next = append(next, ci)
	}
	if hs.big != nil {
		for _, e := range hs.big {
			visit(e)
		}
	} else {
		for _, s := range hs.list {
			visit(s.e)
		}
	}
	return next, hits
}

// Groups matched entries by candidate and moves each group to the head of its lists in request order
func (x *Index) touchHits(sc *scratch) {
	if len(sc.hits) == 0 {
		return
	}
	cands := len(sc.t)
	sc.start = slices.Grow(sc.start[:0], 2*cands+1)[:2*cands+1]
	clear(sc.start)
	start, fill := sc.start[:cands+1], sc.start[cands+1:]
	for _, h := range sc.hits {
		start[h.cand+1]++
	}
	for i := 1; i <= cands; i++ {
		start[i] += start[i-1]
	}
	copy(fill, start[:cands])
	sc.byCand = slices.Grow(sc.byCand[:0], len(sc.hits))[:len(sc.hits)]
	for _, h := range sc.hits {
		sc.byCand[fill[h.cand]] = h.e
		fill[h.cand]++
	}
	for c := range cands {
		if run := sc.byCand[start[c]:start[c+1]]; len(run) > 0 {
			x.touch(run)
		}
	}
}

// Brings the entries to the head of their lists in request order, locking each owning endpoint in turn
func (x *Index) touch(run []*entry) {
	var ep *endpoint
	var now int64
	var p placer
	for _, e := range run {
		if e.ep != ep {
			x.release(ep)
			ep, p = e.ep, placer{}
			ep.mu.Lock()
			now = x.clock()
		}
		if !e.live {
			continue
		}
		e.touched.Store(now)
		p.put(ep, e, false)
	}
	x.release(ep)
}

// Marks a touched endpoint used and unlocks it
func (x *Index) release(ep *endpoint) {
	if ep == nil {
		return
	}
	if !ep.gone {
		x.settle(ep, true)
	}
	ep.mu.Unlock()
}
