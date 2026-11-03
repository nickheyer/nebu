package kvevents

import (
	"encoding/binary"

	"github.com/cespare/xxhash/v2"

	"github.com/nickheyer/nebu/pkg/prefixcache"
)

// Domain tag of token chunk hashes, as the prefix index spells it
const domainTokens byte = 'k'

// Chains token chunks exactly as the prefix index hashes a token prompt, so reported blocks land on lookup hashes
type chainer struct {
	base  xxhash.Digest
	chunk int
	limit int
}

// A chainer scoped to one model and adapter, hashing chunk tokens at a time up to limit chunks
func newChainer(model, adapter string, chunk, limit int) chainer {
	if chunk <= 0 {
		chunk = prefixcache.DefaultChunkTokens
	}
	if limit <= 0 {
		limit = prefixcache.DefaultMaxChunks
	}
	d := xxhash.New()
	var buf [1 + 2*binary.MaxVarintLen64]byte
	buf[0] = domainTokens
	n := 1 + binary.PutUvarint(buf[1:], uint64(len(model)))
	_, _ = d.Write(buf[:n])
	_, _ = d.WriteString(model)
	n = binary.PutUvarint(buf[:], uint64(len(adapter)))
	_, _ = d.Write(buf[:n])
	_, _ = d.WriteString(adapter)
	return chainer{base: *d, chunk: chunk, limit: limit}
}

// Where a chain of tokens ends, the hash of its last full chunk and the tokens past it
type chainState struct {
	prev    uint64
	chunks  int
	partial []int
}

// Extends a chain by tokens, returning where it ends and the hashes a prompt ending inside the extension gains
func (c chainer) extend(st chainState, tokens []int) (chainState, []prefixcache.BlockHash) {
	var own []prefixcache.BlockHash
	buf := make([]byte, 0, c.chunk*8)
	partial := append(make([]int, 0, c.chunk), st.partial...)
	for _, id := range tokens {
		if st.chunks >= c.limit {
			break
		}
		partial = append(partial, id)
		if len(partial) < c.chunk {
			continue
		}
		st.prev = c.sum(buf, partial, st.prev)
		st.chunks++
		own = append(own, prefixcache.BlockHash(st.prev))
		partial = partial[:0]
	}
	st.partial = partial
	if len(partial) > 0 && st.chunks < c.limit {
		own = append(own, prefixcache.BlockHash(c.sum(buf, partial, st.prev)))
	}
	return st, own
}

// Sums the scope, the chunk's ids as eight little endian bytes each, and the previous hash
func (c chainer) sum(buf []byte, ids []int, prev uint64) uint64 {
	buf = buf[:0]
	for _, id := range ids {
		buf = binary.LittleEndian.AppendUint64(buf, uint64(id))
	}
	d := c.base
	_, _ = d.Write(buf)
	var p [8]byte
	binary.LittleEndian.PutUint64(p[:], prev)
	_, _ = d.Write(p[:])
	return d.Sum64()
}

// One block a runtime holds, where its chain ends and the lookup hashes it alone contributes
type block struct {
	state chainState
	own   []prefixcache.BlockHash
}

// The blocks one endpoint holds, keyed by the runtime's own block hash, chained per adapter
type cache struct {
	model    string
	chunk    int
	limit    int
	chainers map[string]chainer
	blocks   map[string]*block
}

func newCache(model string, chunk, limit int) *cache {
	return &cache{model: model, chunk: chunk, limit: limit, chainers: map[string]chainer{}, blocks: map[string]*block{}}
}

// Applies a batch in publication order, returning how many stored events named a parent the cache never saw
func (c *cache) apply(b *Batch) (orphans int) {
	for _, s := range b.Order {
		switch s.kind {
		case stepCleared:
			c.blocks = map[string]*block{}
		case stepRemoved:
			for _, h := range b.Removed[s.index].Hashes {
				delete(c.blocks, h)
			}
		case stepStored:
			if !c.store(b.Stored[s.index]) {
				orphans++
			}
		}
	}
	return orphans
}

// Chains the stored blocks from their parent, false when the parent is unknown
func (c *cache) store(ev Stored) bool {
	var st chainState
	if ev.HasParent {
		parent, ok := c.blocks[ev.Parent]
		if !ok {
			return false
		}
		st = parent.state
	}
	ch, ok := c.chainers[ev.Adapter]
	if !ok {
		ch = newChainer(c.model, ev.Adapter, c.chunk, c.limit)
		c.chainers[ev.Adapter] = ch
	}
	for i, h := range ev.Hashes {
		end := min((i+1)*ev.BlockSize, len(ev.TokenIDs))
		var own []prefixcache.BlockHash
		st, own = ch.extend(st, ev.TokenIDs[i*ev.BlockSize:end])
		c.blocks[h] = &block{state: st, own: own}
	}
	return true
}

// Every lookup hash the endpoint holds
func (c *cache) hashes() []prefixcache.BlockHash {
	n := 0
	for _, b := range c.blocks {
		n += len(b.own)
	}
	out := make([]prefixcache.BlockHash, 0, n)
	for _, b := range c.blocks {
		out = append(out, b.own...)
	}
	return out
}

// Blocks held
func (c *cache) size() int { return len(c.blocks) }
