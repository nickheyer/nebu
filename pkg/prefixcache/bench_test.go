package prefixcache

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/nickheyer/nebu/pkg/datalayer"
)

const (
	benchEndpoints = 10000
	benchDocs      = 1000
	benchChunks    = 100
)

// An index of ten thousand endpoints each holding one of a thousand hundred chunk documents
type fixture struct {
	x    *Index
	docs [][]BlockHash
	eps  []datalayer.EndpointID
}

var (
	sharedOnce    sync.Once
	sharedFixture *fixture
)

// Builds a fixture with one million entries and the given per endpoint capacity
func buildFixture(b *testing.B, capacity int) *fixture {
	b.Helper()
	x := New(Config{DefaultBlocks: capacity, MaxEntries: 2 * benchEndpoints * benchChunks})
	f := &fixture{x: x, docs: make([][]BlockHash, benchDocs), eps: make([]datalayer.EndpointID, benchEndpoints)}
	for d := range f.docs {
		f.docs[d] = x.Hash("bench", "", docText(d), nil)
	}
	for e := range f.eps {
		f.eps[e] = datalayer.EndpointID(fmt.Sprintf("10.%d.%d.%d:8000", e>>16, (e>>8)&255, e&255))
		x.Record(f.eps[e], f.docs[e%benchDocs])
	}
	if st := x.Stats(); st.Entries != benchEndpoints*benchChunks {
		b.Fatalf("fixture holds %d entries", st.Entries)
	}
	return f
}

// The shared read mostly fixture
func shared(b *testing.B) *fixture {
	sharedOnce.Do(func() { sharedFixture = buildFixture(b, DefaultBlockCapacity) })
	return sharedFixture
}

// Text filling exactly the bench chunk count at the default chunk size
func docText(d int) string {
	unit := fmt.Sprintf("document %d says ", d)
	return strings.Repeat(unit, benchChunks*DefaultChunkChars/len(unit)+1)[:benchChunks*DefaultChunkChars]
}

// Candidates holding document d plus others that do not, n in all
func candidatesFor(f *fixture, d, n int) []datalayer.EndpointID {
	out := make([]datalayer.EndpointID, 0, n)
	for e := d; e < benchEndpoints && len(out) < n/2; e += benchDocs {
		out = append(out, f.eps[e])
	}
	for e := (d + 1) % benchEndpoints; len(out) < n; e = (e + 7) % benchEndpoints {
		out = append(out, f.eps[e])
	}
	return out
}

func BenchmarkMatch(b *testing.B) {
	f := shared(b)
	for _, n := range []int{16, 256} {
		sets := make([][]datalayer.EndpointID, benchDocs)
		for d := range sets {
			sets[d] = candidatesFor(f, d, n)
		}
		b.Run(fmt.Sprintf("candidates=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			i := 0
			for b.Loop() {
				d := i % benchDocs
				f.x.Match(f.docs[d], sets[d])
				i++
			}
		})
		b.Run(fmt.Sprintf("candidates=%d/parallel", n), func(b *testing.B) {
			b.ReportAllocs()
			var seed atomic.Uint64
			b.RunParallel(func(pb *testing.PB) {
				r := rand.New(rand.NewPCG(seed.Add(1), 1))
				for pb.Next() {
					d := r.IntN(benchDocs)
					f.x.Match(f.docs[d], sets[d])
				}
			})
		})
	}
}

func BenchmarkRecord(b *testing.B) {
	f := shared(b)
	b.Run("held", func(b *testing.B) {
		b.ReportAllocs()
		i := 0
		for b.Loop() {
			f.x.Record(f.eps[i%benchEndpoints], f.docs[i%benchDocs])
			i++
		}
		st := f.x.Stats()
		b.ReportMetric(float64(st.Bytes)/float64(st.Entries), "est-bytes/entry")
	})
	b.Run("held/parallel", func(b *testing.B) {
		b.ReportAllocs()
		var seed atomic.Uint64
		b.RunParallel(func(pb *testing.PB) {
			r := rand.New(rand.NewPCG(seed.Add(1), 2))
			for pb.Next() {
				e := r.IntN(benchEndpoints)
				f.x.Record(f.eps[e], f.docs[e%benchDocs])
			}
		})
	})
	evicting := buildFixture(b, benchChunks)
	fresh := make([][]BlockHash, 4096)
	for i := range fresh {
		fresh[i] = chainOf(fmt.Sprintf("fresh%d", i), benchChunks)
	}
	b.Run("insert and evict", func(b *testing.B) {
		b.ReportAllocs()
		i := 0
		for b.Loop() {
			evicting.x.Record(evicting.eps[i%benchEndpoints], fresh[i%len(fresh)])
			i++
		}
	})
	b.Run("insert and evict/parallel", func(b *testing.B) {
		b.ReportAllocs()
		var seed atomic.Uint64
		b.RunParallel(func(pb *testing.PB) {
			r := rand.New(rand.NewPCG(seed.Add(1), 3))
			for pb.Next() {
				evicting.x.Record(evicting.eps[r.IntN(benchEndpoints)], fresh[r.IntN(len(fresh))])
			}
		})
	})
	if st := evicting.x.Stats(); st.Entries != benchEndpoints*benchChunks {
		b.Fatalf("eviction drifted to %d entries", st.Entries)
	}
}

func BenchmarkHash(b *testing.B) {
	x := New(Config{})
	text := docText(1)
	tokens := make([]int, benchChunks*DefaultChunkTokens)
	for i := range tokens {
		tokens[i] = i * 31
	}
	b.Run("text", func(b *testing.B) {
		b.ReportAllocs()
		b.SetBytes(int64(len(text)))
		for b.Loop() {
			x.Hash("bench", "adapter", text, nil)
		}
	})
	b.Run("tokens", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			x.Hash("bench", "adapter", "", tokens)
		}
	})
}

func BenchmarkRingPick(b *testing.B) {
	for _, n := range []int{16, 512} {
		eps := endpointsNamed(n)
		ring := NewHashRing(0)
		b.Run(fmt.Sprintf("candidates=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			var h BlockHash
			for b.Loop() {
				h += 0x9e3779b97f4a7c15
				ring.Pick(h, eps, idle, 1)
			}
		})
	}
}
