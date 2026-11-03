package prefixcache

import (
	"encoding/binary"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cespare/xxhash/v2"
	"github.com/nickheyer/nebu/pkg/datalayer"
)

// Virtual nodes each endpoint places on the ring when none are configured
const DefaultReplicas = 128

const (
	// Virtual nodes kept built across every cached candidate set
	ringCachePoints = 1 << 20
	// Least time between two last use stamps of one ring, keeping hot rings off a shared write
	ringStampEvery = int64(time.Second / 16)
)

// Consistent hashing with bounded loads over virtual node rings cached per candidate set, implementing Ring
type HashRing struct {
	replicas int
	epoch    time.Time
	mu       sync.Mutex
	rings    atomic.Pointer[map[uint64]*vring]
}

var _ Ring = (*HashRing)(nil)

// One candidate set's sorted virtual nodes
type vring struct {
	ids    []datalayer.EndpointID
	index  map[datalayer.EndpointID]int32
	points []uint64
	owners []int32
	used   atomic.Int64
}

// Builds a ring placing replicas virtual nodes per endpoint, zero or negative taking the default
func NewHashRing(replicas int) *HashRing {
	if replicas <= 0 {
		replicas = DefaultReplicas
	}
	r := &HashRing{replicas: replicas, epoch: time.Now()}
	empty := map[uint64]*vring{}
	r.rings.Store(&empty)
	return r
}

// Walks clockwise from the hash to the first candidate whose load is under maxLoad, false when none is
func (r *HashRing) Pick(first BlockHash, candidates []datalayer.EndpointID, load func(datalayer.EndpointID) int, maxLoad int) (datalayer.EndpointID, bool) {
	if len(candidates) == 0 {
		return "", false
	}
	vr := r.ring(candidates)
	seen := newBitset(len(vr.ids))
	start, _ := slices.BinarySearch(vr.points, uint64(first))
	checked := 0
	for step := 0; step < len(vr.points) && checked < len(vr.ids); step++ {
		o := vr.owners[(start+step)%len(vr.points)]
		if seen.has(o) {
			continue
		}
		seen.set(o)
		checked++
		if load(vr.ids[o]) < maxLoad {
			return vr.ids[o], true
		}
	}
	return "", false
}

// Virtual nodes for exactly this candidate set, built and cached on first sight
func (r *HashRing) ring(candidates []datalayer.EndpointID) *vring {
	key := setKey(candidates)
	now := int64(time.Since(r.epoch))
	if vr := (*r.rings.Load())[key]; vr != nil && vr.holdsExactly(candidates) {
		vr.stamp(now)
		return vr
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	cur := *r.rings.Load()
	if vr := cur[key]; vr != nil && vr.holdsExactly(candidates) {
		vr.stamp(now)
		return vr
	}
	vr := buildRing(candidates, r.replicas)
	vr.used.Store(now)
	next := make(map[uint64]*vring, len(cur)+1)
	points := len(vr.points)
	for k, v := range cur {
		if k != key {
			next[k] = v
			points += len(v.points)
		}
	}
	for points > ringCachePoints && len(next) > 0 {
		k := leastUsedRing(next)
		points -= len(next[k].points)
		delete(next, k)
	}
	next[key] = vr
	r.rings.Store(&next)
	return vr
}

// Cache key whose virtual nodes went unused longest
func leastUsedRing(rings map[uint64]*vring) uint64 {
	var key uint64
	oldest := int64(-1)
	for k, v := range rings {
		if u := v.used.Load(); oldest < 0 || u < oldest {
			key, oldest = k, u
		}
	}
	return key
}

// Order independent hash of the candidate ids, equal for every permutation of one set
func setKey(candidates []datalayer.EndpointID) uint64 {
	var sum uint64
	for _, id := range candidates {
		sum += mix(xxhash.Sum64String(string(id)))
	}
	return sum ^ mix(uint64(len(candidates)))
}

// Finalizer spreading every input bit across the output so sums of hashes stay uniform
func mix(h uint64) uint64 {
	h ^= h >> 33
	h *= 0xff51afd7ed558ccd
	h ^= h >> 33
	h *= 0xc4ceb9fe1a85ec53
	h ^= h >> 33
	return h
}

// Records a use once enough time passed since the last stamp
func (vr *vring) stamp(now int64) {
	if now-vr.used.Load() >= ringStampEvery {
		vr.used.Store(now)
	}
}

// Whether the candidates name every member and nothing else
func (vr *vring) holdsExactly(candidates []datalayer.EndpointID) bool {
	seen := newBitset(len(vr.ids))
	distinct := 0
	for _, id := range candidates {
		o, ok := vr.index[id]
		if !ok {
			return false
		}
		if !seen.has(o) {
			seen.set(o)
			distinct++
		}
	}
	return distinct == len(vr.ids)
}

// Places replicas points per distinct candidate, sorted by point then id so every gateway agrees
func buildRing(candidates []datalayer.EndpointID, replicas int) *vring {
	vr := &vring{index: make(map[datalayer.EndpointID]int32, len(candidates))}
	for _, id := range candidates {
		if _, dup := vr.index[id]; dup {
			continue
		}
		vr.index[id] = int32(len(vr.ids))
		vr.ids = append(vr.ids, id)
	}
	type node struct {
		point uint64
		owner int32
	}
	nodes := make([]node, 0, len(vr.ids)*replicas)
	var rep [4]byte
	for o, id := range vr.ids {
		base := xxhash.New()
		_, _ = base.WriteString(string(id))
		for i := range replicas {
			d := *base
			binary.LittleEndian.PutUint32(rep[:], uint32(i))
			_, _ = d.Write(rep[:])
			nodes = append(nodes, node{d.Sum64(), int32(o)})
		}
	}
	slices.SortFunc(nodes, func(a, b node) int {
		if a.point != b.point {
			if a.point < b.point {
				return -1
			}
			return 1
		}
		return strings.Compare(string(vr.ids[a.owner]), string(vr.ids[b.owner]))
	})
	vr.points = make([]uint64, len(nodes))
	vr.owners = make([]int32, len(nodes))
	for i, n := range nodes {
		vr.points[i], vr.owners[i] = n.point, n.owner
	}
	return vr
}

// Membership flags for ring owners, held inline up to 64 members
type bitset struct {
	small uint64
	words []uint64
}

// Flags able to hold n members
func newBitset(n int) bitset {
	if n <= 64 {
		return bitset{}
	}
	return bitset{words: make([]uint64, (n+63)/64)}
}

// Whether member i is set
func (b *bitset) has(i int32) bool {
	if b.words == nil {
		return b.small&(1<<uint(i)) != 0
	}
	return b.words[i/64]&(1<<uint(i%64)) != 0
}

// Marks member i present
func (b *bitset) set(i int32) {
	if b.words == nil {
		b.small |= 1 << uint(i)
		return
	}
	b.words[i/64] |= 1 << uint(i%64)
}
