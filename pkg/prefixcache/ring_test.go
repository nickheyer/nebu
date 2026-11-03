package prefixcache

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/nickheyer/nebu/pkg/datalayer"
)

func endpointsNamed(n int) []datalayer.EndpointID {
	out := make([]datalayer.EndpointID, n)
	for i := range out {
		out[i] = datalayer.EndpointID(fmt.Sprintf("10.0.0.%d:8000", i))
	}
	return out
}

func idle(datalayer.EndpointID) int { return 0 }

func TestRingAgreesAcrossGatewaysAndOrder(t *testing.T) {
	eps := endpointsNamed(12)
	shuffled := slices.Clone(eps)
	rand.New(rand.NewPCG(1, 2)).Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
	a, b := NewHashRing(0), NewHashRing(DefaultReplicas)
	r := rand.New(rand.NewPCG(3, 4))
	for range 2000 {
		h := BlockHash(r.Uint64())
		pa, _ := a.Pick(h, eps, idle, 1)
		pb, _ := b.Pick(h, shuffled, idle, 1)
		if pa != pb {
			t.Fatalf("gateways disagree on %x, %s and %s", h, pa, pb)
		}
	}
}

func TestRingSpread(t *testing.T) {
	eps := endpointsNamed(10)
	ring := NewHashRing(0)
	counts := map[datalayer.EndpointID]int{}
	r := rand.New(rand.NewPCG(5, 6))
	const draws = 200000
	for range draws {
		p, ok := ring.Pick(BlockHash(r.Uint64()), eps, idle, 1)
		if !ok {
			t.Fatalf("idle ring refused a pick")
		}
		counts[p]++
	}
	for _, e := range eps {
		share := float64(counts[e]) / draws
		if share < 0.07 || share > 0.13 {
			t.Fatalf("%s takes %.3f of keys, want near 0.1", e, share)
		}
	}
}

func TestRingBoundedLoad(t *testing.T) {
	eps := endpointsNamed(6)
	ring := NewHashRing(0)
	const h = BlockHash(0x9e3779b97f4a7c15)
	natural, _ := ring.Pick(h, eps, idle, 1)
	cases := []struct {
		name    string
		loads   map[datalayer.EndpointID]int
		maxLoad int
		avoid   []datalayer.EndpointID
		want    bool
	}{
		{"natural owner under the bound", nil, 3, nil, true},
		{"natural owner at the bound walks on", map[datalayer.EndpointID]int{natural: 3}, 3, []datalayer.EndpointID{natural}, true},
		{"only one under the bound", func() map[datalayer.EndpointID]int {
			m := map[datalayer.EndpointID]int{}
			for _, e := range eps[1:] {
				m[e] = 5
			}
			return m
		}(), 5, eps[1:], true},
		{"every candidate at the bound", func() map[datalayer.EndpointID]int {
			m := map[datalayer.EndpointID]int{}
			for _, e := range eps {
				m[e] = 2
			}
			return m
		}(), 2, nil, false},
		{"zero bound refuses all", nil, 0, nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			load := func(e datalayer.EndpointID) int { return c.loads[e] }
			got, ok := ring.Pick(h, eps, load, c.maxLoad)
			if ok != c.want {
				t.Fatalf("ok %v, want %v", ok, c.want)
			}
			if !ok {
				if got != "" {
					t.Fatalf("refused pick named %s", got)
				}
				return
			}
			if slices.Contains(c.avoid, got) || load(got) >= c.maxLoad {
				t.Fatalf("picked %s at load %d", got, load(got))
			}
			if c.loads == nil && got != natural {
				t.Fatalf("picked %s, natural owner %s", got, natural)
			}
		})
	}
}

func TestRingWalksClockwise(t *testing.T) {
	eps := endpointsNamed(5)
	ring := NewHashRing(0)
	vr := ring.ring(eps)
	r := rand.New(rand.NewPCG(7, 8))
	for range 200 {
		h := BlockHash(r.Uint64())
		start, _ := slices.BinarySearch(vr.points, uint64(h))
		var order []datalayer.EndpointID
		for step := range vr.points {
			id := vr.ids[vr.owners[(start+step)%len(vr.points)]]
			if !slices.Contains(order, id) {
				order = append(order, id)
			}
		}
		full := map[datalayer.EndpointID]bool{order[0]: true, order[1]: true}
		got, _ := ring.Pick(h, eps, func(e datalayer.EndpointID) int {
			if full[e] {
				return 1
			}
			return 0
		}, 1)
		if got != order[2] {
			t.Fatalf("picked %s, third clockwise owner %s", got, order[2])
		}
	}
}

func TestRingMinimalDisruption(t *testing.T) {
	before := endpointsNamed(10)
	after := endpointsNamed(11)
	ring := NewHashRing(0)
	r := rand.New(rand.NewPCG(9, 10))
	moved := 0
	const draws = 50000
	for range draws {
		h := BlockHash(r.Uint64())
		a, _ := ring.Pick(h, before, idle, 1)
		b, _ := ring.Pick(h, after, idle, 1)
		if a != b {
			moved++
			if b != after[10] {
				t.Fatalf("key moved between old endpoints %s and %s", a, b)
			}
		}
	}
	if share := float64(moved) / draws; share < 0.05 || share > 0.14 {
		t.Fatalf("%.3f of keys moved, want near 1/11", share)
	}
}

func TestRingCache(t *testing.T) {
	eps := endpointsNamed(8)
	ring := NewHashRing(0)
	first := ring.ring(eps)
	rev := slices.Clone(eps)
	slices.Reverse(rev)
	if ring.ring(rev) != first {
		t.Fatalf("a permutation rebuilt the ring")
	}
	dup := append(slices.Clone(eps), eps[0], eps[3])
	for h := range BlockHash(500) {
		a, _ := ring.Pick(h*0x9e3779b97f4a7c15, eps, idle, 1)
		b, _ := ring.Pick(h*0x9e3779b97f4a7c15, dup, idle, 1)
		if a != b {
			t.Fatalf("duplicate candidates changed the pick")
		}
	}
	if ring.ring(eps[:7]) == first {
		t.Fatalf("a changed candidate set reused the old ring")
	}
	big := NewHashRing(512)
	var last *vring
	for i := range 64 {
		last = big.ring(endpointsNamed(i + 1))
	}
	points := 0
	for _, v := range *big.rings.Load() {
		points += len(v.points)
	}
	if points > ringCachePoints {
		t.Fatalf("%d virtual nodes cached over the bound %d", points, ringCachePoints)
	}
	if big.ring(endpointsNamed(64)) != last {
		t.Fatalf("the most recent ring was evicted")
	}
	if len(*big.rings.Load()) == 64 {
		t.Fatalf("no ring was evicted past the bound")
	}
	if _, ok := ring.Pick(1, nil, idle, 1); ok {
		t.Fatalf("empty candidates produced a pick")
	}
}

func TestRingConcurrent(t *testing.T) {
	ring := NewHashRing(16)
	sets := make([][]datalayer.EndpointID, 100)
	for i := range sets {
		sets[i] = endpointsNamed(1 + i%90)
	}
	done := make(chan struct{})
	for w := range 8 {
		go func() {
			defer func() { done <- struct{}{} }()
			r := rand.New(rand.NewPCG(uint64(w), 11))
			for range 2000 {
				set := sets[r.IntN(len(sets))]
				if _, ok := ring.Pick(BlockHash(r.Uint64()), set, idle, 1); !ok {
					t.Errorf("idle ring refused a pick")
				}
			}
		}()
	}
	for range 8 {
		<-done
	}
}
