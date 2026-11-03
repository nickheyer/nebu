package stub

import "container/list"

// One KV block, cached while it holds an indexed prompt hash
type block struct {
	ref    int
	hash   uint64
	cached bool
	elem   *list.Element
}

// Fixed pool of KV blocks with a prefix index and LRU eviction of unreferenced cached blocks
type kvCache struct {
	blocks []block
	free   []int
	lru    *list.List
	index  map[uint64]int
}

// A cache of n empty blocks
func newKVCache(n int) *kvCache {
	c := &kvCache{blocks: make([]block, n), free: make([]int, n), lru: list.New(), index: map[uint64]int{}}
	for i := range c.free {
		c.free[i] = n - 1 - i
	}
	return c
}

// Blocks no request references, cached ones included
func (c *kvCache) available() int { return len(c.free) + c.lru.Len() }

// Blocks some request references
func (c *kvCache) used() int { return len(c.blocks) - c.available() }

// Block ids of the leading hashes found in the index, with how many of them nothing references
func (c *kvCache) match(hashes []uint64) (ids []int, idle int) {
	for _, h := range hashes {
		id, ok := c.index[h]
		if !ok {
			break
		}
		ids = append(ids, id)
		if c.blocks[id].ref == 0 {
			idle++
		}
	}
	return ids, idle
}

// Takes a reference on each block, pulling unreferenced ones out of the eviction order
func (c *kvCache) acquire(ids []int) {
	for _, id := range ids {
		b := &c.blocks[id]
		if b.elem != nil {
			c.lru.Remove(b.elem)
			b.elem = nil
		}
		b.ref++
	}
}

// Hands out n referenced blocks from the free list, then by evicting the least recently used cached blocks
func (c *kvCache) allocate(n int) []int {
	ids := make([]int, 0, n)
	for len(ids) < n {
		var id int
		if k := len(c.free); k > 0 {
			id = c.free[k-1]
			c.free = c.free[:k-1]
		} else {
			id = c.lru.Remove(c.lru.Front()).(int)
			b := &c.blocks[id]
			delete(c.index, b.hash)
			b.cached, b.elem = false, nil
		}
		c.blocks[id].ref = 1
		ids = append(ids, id)
	}
	return ids
}

// Indexes a block under a prompt hash unless the block or the hash is indexed already
func (c *kvCache) register(id int, h uint64) {
	b := &c.blocks[id]
	if b.cached {
		return
	}
	if _, ok := c.index[h]; ok {
		return
	}
	b.hash, b.cached = h, true
	c.index[h] = id
}

// Drops a reference on each block from last to first so a prompt's tail blocks are evicted first
func (c *kvCache) release(ids []int) {
	for i := len(ids) - 1; i >= 0; i-- {
		b := &c.blocks[ids[i]]
		b.ref--
		if b.ref > 0 {
			continue
		}
		if b.cached {
			b.elem = c.lru.PushBack(ids[i])
		} else {
			c.free = append(c.free, ids[i])
		}
	}
}
