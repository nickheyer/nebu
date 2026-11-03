package prefixcache

import (
	"encoding/binary"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cespare/xxhash/v2"
	"github.com/nickheyer/nebu/pkg/datalayer"
)

// Clock a test advances by hand
type fakeClock struct{ now atomic.Int64 }

func (c *fakeClock) read() int64             { return c.now.Load() }
func (c *fakeClock) set(d time.Duration)     { c.now.Store(int64(d)) }
func (c *fakeClock) advance(d time.Duration) { c.now.Add(int64(d)) }

// Distinct synthetic hashes named by a prefix and position
func chainOf(name string, n int) []BlockHash {
	out := make([]BlockHash, n)
	for i := range out {
		out[i] = BlockHash(xxhash.Sum64String(fmt.Sprintf("%s/%d", name, i)))
	}
	return out
}

func ids(names ...string) []datalayer.EndpointID {
	out := make([]datalayer.EndpointID, len(names))
	for i, n := range names {
		out[i] = datalayer.EndpointID(n)
	}
	return out
}

// Blocks per endpoint from a match result
func blocksOf(ms []Match) map[datalayer.EndpointID]int {
	out := make(map[datalayer.EndpointID]int, len(ms))
	for _, m := range ms {
		out[m.Endpoint] = m.Blocks
	}
	return out
}

// Fails the test when shard holders, endpoint lists, group order and counters disagree
func verify(t *testing.T, x *Index) {
	t.Helper()
	listed := map[*entry]bool{}
	var entries, linkedWanted int
	registry := 0
	x.eps.Range(func(k, v any) bool {
		registry++
		ep := v.(*endpoint)
		ep.mu.Lock()
		defer ep.mu.Unlock()
		if ep.gone {
			t.Errorf("registry holds forgotten endpoint %s", ep.id)
		}
		limit := ep.capacity
		if limit == 0 {
			limit = x.cfg.DefaultBlocks
		}
		if ep.approx.n > limit {
			t.Errorf("%s holds %d approximate entries over capacity %d", ep.id, ep.approx.n, limit)
		}
		for _, l := range []*entryList{&ep.approx, &ep.reported} {
			n := 0
			var prev *entry
			for e := l.head; e != nil; e = e.next {
				n++
				if e.prev != prev {
					t.Errorf("%s list back link broken", ep.id)
				}
				prev = e
				if !e.live || e.ep != ep {
					t.Errorf("%s lists a dead or foreign entry", ep.id)
				}
				if e.reported != (l == &ep.reported) {
					t.Errorf("%s entry sits in the wrong list", ep.id)
				}
				listed[e] = true
			}
			if l.tail != prev || n != l.n {
				t.Errorf("%s list count %d tail mismatch, want %d", ep.id, l.n, n)
			}
			entries += n
		}
		if ep.count() > 0 {
			linkedWanted++
		}
		ep.group.mu.Lock()
		if ep.linked != (ep.count() > 0) {
			t.Errorf("%s linked %v with %d entries", ep.id, ep.linked, ep.count())
		}
		ep.group.mu.Unlock()
		return true
	})
	held, keys := 0, 0
	for i := range x.shards {
		sh := &x.shards[i]
		sh.mu.RLock()
		for h, hs := range sh.m {
			keys++
			if hs.size() == 0 {
				t.Errorf("hash %x kept with no holders", h)
			}
			var each []*entry
			for _, s := range hs.list {
				if s.ep != s.e.ep {
					t.Errorf("hash %x slot names the wrong endpoint", h)
				}
				each = append(each, s.e)
			}
			for ep, e := range hs.big {
				if ep != e.ep {
					t.Errorf("hash %x map key names the wrong endpoint", h)
				}
				each = append(each, e)
			}
			for _, e := range each {
				held++
				if e.hash != h || !listed[e] {
					t.Errorf("hash %x holder %s missing from its endpoint lists", h, e.ep.id)
				}
			}
		}
		sh.mu.RUnlock()
	}
	linked := 0
	for i := range x.groups {
		g := &x.groups[i]
		g.mu.Lock()
		var prev *endpoint
		for ep := g.head; ep != nil; ep = ep.gnext {
			linked++
			if ep.gprev != prev {
				t.Errorf("group back link broken at %s", ep.id)
			}
			if prev != nil && prev.lastUsed < ep.lastUsed {
				t.Errorf("group order breaks at %s", ep.id)
			}
			prev = ep
		}
		if g.tail != prev {
			t.Errorf("group tail mismatch")
		}
		g.mu.Unlock()
	}
	if held != entries || held != len(listed) {
		t.Errorf("shards hold %d entries, lists hold %d", held, entries)
	}
	st := x.Stats()
	if st.Entries != entries || st.Hashes != keys || st.Endpoints != registry || x.Len() != keys {
		t.Errorf("stats %+v disagree with %d entries %d hashes %d endpoints", st, entries, keys, registry)
	}
	if linked != linkedWanted {
		t.Errorf("%d endpoints linked, want %d", linked, linkedWanted)
	}
	if st.Entries > x.cfg.MaxEntries {
		t.Errorf("%d entries over cap %d", st.Entries, x.cfg.MaxEntries)
	}
}

func TestHashChunks(t *testing.T) {
	cfg := Config{ChunkChars: 4, ChunkTokens: 3, MaxChunks: 5}
	cases := []struct {
		name   string
		prompt string
		tokens []int
		want   int
	}{
		{"empty", "", nil, 0},
		{"exact multiple", "abcdefgh", nil, 2},
		{"trailing partial", "abcdefghi", nil, 3},
		{"shorter than a chunk", "ab", nil, 1},
		{"multibyte counted as characters", "ééééé", nil, 2},
		{"capped by max chunks", strings.Repeat("x", 40), nil, 5},
		{"tokens", "ignored", []int{1, 2, 3, 4, 5, 6, 7}, 3},
		{"tokens capped", "", make([]int, 30), 5},
	}
	x := New(cfg)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := len(x.Hash("m", "", c.prompt, c.tokens)); got != c.want {
				t.Fatalf("%d hashes, want %d", got, c.want)
			}
		})
	}
}

func TestHashFormula(t *testing.T) {
	x := New(Config{ChunkChars: 3})
	got := x.Hash("llama", "sql", "abcdef", nil)
	frame := func(prev uint64, chunk string) uint64 {
		d := xxhash.New()
		_, _ = d.Write([]byte{domainText, 5})
		_, _ = d.WriteString("llama")
		_, _ = d.Write([]byte{3})
		_, _ = d.WriteString("sql")
		_, _ = d.WriteString(chunk)
		var p [8]byte
		binary.LittleEndian.PutUint64(p[:], prev)
		_, _ = d.Write(p[:])
		return d.Sum64()
	}
	h0 := frame(0, "abc")
	h1 := frame(h0, "def")
	if !slices.Equal(got, []BlockHash{BlockHash(h0), BlockHash(h1)}) {
		t.Fatalf("hashes %x, want %x %x", got, h0, h1)
	}
}

func TestHashChainsPrefixes(t *testing.T) {
	x := New(Config{ChunkChars: 4, ChunkTokens: 2})
	cases := []struct {
		name          string
		short, long   string
		tshort, tlong []int
	}{
		{name: "text", short: "abcdefgh", long: "abcdefghijklmnop"},
		{name: "multibyte", short: "日本語の文章", long: "日本語の文章です。さらに"},
		{name: "tokens", tshort: []int{1, 2, 3, 4}, tlong: []int{1, 2, 3, 4, 5, 6, 7}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := x.Hash("m", "a", c.short, c.tshort)
			l := x.Hash("m", "a", c.long, c.tlong)
			full := len(s)
			if len(c.tshort) == 0 && len([]rune(c.short))%4 != 0 {
				full--
			}
			if !slices.Equal(s[:full], l[:full]) {
				t.Fatalf("shared full chunks hash differently")
			}
		})
	}
	a := x.Hash("m", "", "XbcdEFGHijkl", nil)
	b := x.Hash("m", "", "abcdEFGHijkl", nil)
	for i := range a {
		if a[i] == b[i] {
			t.Fatalf("chunk %d hash ignores an earlier chunk", i)
		}
	}
}

func TestHashScopes(t *testing.T) {
	x := New(Config{ChunkChars: 8})
	type in struct {
		model, adapter, prompt string
		tokens                 []int
	}
	base := in{"llama", "", "same text", nil}
	cases := []struct {
		name  string
		other in
	}{
		{"model", in{"qwen", "", "same text", nil}},
		{"adapter", in{"llama", "sql", "same text", nil}},
		{"model and adapter framing", in{"llam", "a", "same text", nil}},
		{"tokens against text", in{"llama", "", "", []int{0x656d6173, 0x74786574}}},
	}
	h := x.Hash(base.model, base.adapter, base.prompt, base.tokens)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			o := x.Hash(c.other.model, c.other.adapter, c.other.prompt, c.other.tokens)
			if o[0] == h[0] {
				t.Fatalf("scopes share a first hash")
			}
		})
	}
}

func TestMatch(t *testing.T) {
	h := chainOf("p", 4)
	type rec struct {
		ep     string
		hashes []BlockHash
	}
	many := make([]string, 20)
	for i := range many {
		many[i] = fmt.Sprintf("e%02d", i)
	}
	var crowd []rec
	for _, e := range many {
		crowd = append(crowd, rec{e, h[:1]})
	}
	cases := []struct {
		name       string
		records    []rec
		candidates []datalayer.EndpointID
		want       []Match
	}{
		{
			name:       "longest prefix first",
			records:    []rec{{"a", h}, {"b", h[:2]}},
			candidates: ids("c", "b", "a"),
			want:       []Match{{"a", 4}, {"b", 2}, {"c", 0}},
		},
		{
			name:       "gap stops the count",
			records:    []rec{{"a", h[:1]}, {"a", h[2:3]}},
			candidates: ids("a"),
			want:       []Match{{"a", 1}},
		},
		{
			name:       "ties by id",
			records:    []rec{{"b", h[:2]}, {"a", h[:2]}},
			candidates: ids("b", "a"),
			want:       []Match{{"a", 2}, {"b", 2}},
		},
		{
			name:       "non candidates ignored",
			records:    []rec{{"z", h}, {"a", h[:1]}},
			candidates: ids("a"),
			want:       []Match{{"a", 1}},
		},
		{
			name:       "duplicate candidates collapse",
			records:    []rec{{"a", h[:3]}},
			candidates: ids("a", "a", "b", "a"),
			want:       []Match{{"a", 3}, {"b", 0}},
		},
		{
			name:       "candidates past the scan size walk holders",
			records:    []rec{{"e03", h}, {"e07", h[:2]}},
			candidates: ids(many...),
			want: func() []Match {
				out := []Match{{"e03", 4}, {"e07", 2}}
				for _, e := range many {
					if e != "e03" && e != "e07" {
						out = append(out, Match{datalayer.EndpointID(e), 0})
					}
				}
				return out
			}(),
		},
		{
			name:       "hash held by a map of holders",
			records:    append(slices.Clone(crowd), rec{"e05", h[:3]}),
			candidates: ids("e05", "e01"),
			want:       []Match{{"e05", 3}, {"e01", 1}},
		},
		{
			name:       "no candidates",
			records:    []rec{{"a", h}},
			candidates: nil,
			want:       nil,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			x := New(Config{})
			for _, r := range c.records {
				x.Record(datalayer.EndpointID(r.ep), r.hashes)
			}
			if got := x.Match(h, c.candidates); !slices.Equal(got, c.want) {
				t.Fatalf("match %v, want %v", got, c.want)
			}
			verify(t, x)
		})
	}
}

func TestCapacityEviction(t *testing.T) {
	a, b, c := chainOf("a", 2), chainOf("b", 2), chainOf("c", 2)
	long := chainOf("long", 6)
	cases := []struct {
		name  string
		steps func(x *Index)
		probe []BlockHash
		want  int
	}{
		{
			name: "record past capacity keeps the prefix",
			steps: func(x *Index) {
				x.SetCapacity("e", 3)
				x.Record("e", long)
			},
			probe: long,
			want:  3,
		},
		{
			name: "least recently recorded chain goes first",
			steps: func(x *Index) {
				x.SetCapacity("e", 4)
				x.Record("e", a)
				x.Record("e", b)
				x.Record("e", c)
			},
			probe: a,
			want:  0,
		},
		{
			name: "match keeps a chain recent",
			steps: func(x *Index) {
				x.SetCapacity("e", 4)
				x.Record("e", a)
				x.Record("e", b)
				x.Match(a, ids("e"))
				x.Record("e", c)
			},
			probe: a,
			want:  2,
		},
		{
			name: "shrinking capacity evicts immediately",
			steps: func(x *Index) {
				x.Record("e", long)
				x.SetCapacity("e", 2)
			},
			probe: long,
			want:  2,
		},
		{
			name: "non positive capacity restores the default",
			steps: func(x *Index) {
				x.SetCapacity("e", 1)
				x.SetCapacity("e", 0)
				x.Record("e", long)
			},
			probe: long,
			want:  5,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			x := New(Config{DefaultBlocks: 5})
			tc.steps(x)
			if got := blocksOf(x.Match(tc.probe, ids("e")))["e"]; got != tc.want {
				t.Fatalf("%d blocks, want %d", got, tc.want)
			}
			verify(t, x)
		})
	}
}

func TestReport(t *testing.T) {
	h := chainOf("r", 5)
	extra := chainOf("x", 4)
	x := New(Config{})
	x.Record("e", h)
	x.Report("e", h[:2])
	if got := blocksOf(x.Match(h, ids("e")))["e"]; got != 2 {
		t.Fatalf("report kept approximate entries, %d blocks", got)
	}
	x.Record("a", h[:2])
	if got := x.Match(h, ids("a", "e")); got[0].Endpoint != "e" || got[0].Blocks != 2 || got[1].Blocks != 2 {
		t.Fatalf("reported entries lost a tie, %v", got)
	}

	x.SetCapacity("e", 2)
	x.Report("e", h)
	x.Record("e", extra)
	if got := blocksOf(x.Match(h, ids("e")))["e"]; got != 5 {
		t.Fatalf("approximate LRU evicted reported entries, %d blocks", got)
	}
	if got := blocksOf(x.Match(extra, ids("e")))["e"]; got != 2 {
		t.Fatalf("approximate entries ignore capacity after a report, %d blocks", got)
	}
	x.Record("e", h[:1])
	verify(t, x)

	x.Report("e", extra[:1])
	got := blocksOf(x.Match(h, ids("e")))["e"]
	if got != 0 {
		t.Fatalf("next report kept old reported entries, %d blocks", got)
	}
	if got := blocksOf(x.Match(extra, ids("e")))["e"]; got != 1 {
		t.Fatalf("next report lost a carried over entry, %d blocks", got)
	}
	x.Report("e", nil)
	if st := x.Stats(); st.Entries != 2 {
		t.Fatalf("empty report left %d entries, want only the other endpoint's 2", st.Entries)
	}
	verify(t, x)
}

func TestUnrecord(t *testing.T) {
	h := chainOf("u", 4)
	other := chainOf("o", 2)
	x := New(Config{})
	x.Record("e", h)
	x.Record("e", other)
	x.Record("f", h)
	x.Unrecord("e", h)
	if got := blocksOf(x.Match(h, ids("e", "f"))); got["e"] != 0 || got["f"] != 4 {
		t.Fatalf("after unrecord e holds %d blocks and f %d", got["e"], got["f"])
	}
	if got := blocksOf(x.Match(other, ids("e")))["e"]; got != 2 {
		t.Fatalf("unrecord dropped another prompt, %d blocks", got)
	}
	x.Report("e", h[:2])
	x.Record("e", h)
	x.Unrecord("e", h)
	if got := blocksOf(x.Match(h, ids("e")))["e"]; got != 2 {
		t.Fatalf("unrecord touched reported entries, %d blocks", got)
	}
	x.Unrecord("ghost", h)
	x.Unrecord("e", nil)
	if st := x.Stats(); st.Entries != 6 {
		t.Fatalf("entries %d, want 2 reported on e and 4 on f", st.Entries)
	}
	verify(t, x)
}

func TestForget(t *testing.T) {
	h := chainOf("f", 3)
	x := New(Config{})
	x.Record("a", h)
	x.Record("b", h[:2])
	x.Report("a", h)
	x.Forget("a")
	x.Forget("missing")
	if got := blocksOf(x.Match(h, ids("a", "b"))); got["a"] != 0 || got["b"] != 2 {
		t.Fatalf("forget left %v", got)
	}
	st := x.Stats()
	if st.Entries != 2 || st.Hashes != 2 || st.Endpoints != 1 {
		t.Fatalf("stats after forget %+v", st)
	}
	verify(t, x)
	x.Record("a", h[:1])
	if got := blocksOf(x.Match(h, ids("a")))["a"]; got != 1 {
		t.Fatalf("record after forget gave %d blocks", got)
	}
	x.Forget("a")
	x.Forget("b")
	if st := x.Stats(); st != (Stats{}) {
		t.Fatalf("empty index reports %+v", st)
	}
	verify(t, x)
}

func TestTTL(t *testing.T) {
	h := chainOf("t", 3)
	cases := []struct {
		name  string
		steps func(x *Index, c *fakeClock)
		want  int
	}{
		{"fresh entries match", func(x *Index, c *fakeClock) { c.set(50 * time.Minute) }, 3},
		{"expired entries never match", func(x *Index, c *fakeClock) { c.set(61 * time.Minute) }, 0},
		{"a match refreshes", func(x *Index, c *fakeClock) {
			c.set(50 * time.Minute)
			x.Match(h[:2], ids("e"))
			c.set(100 * time.Minute)
		}, 2},
		{"a record refreshes an expired entry", func(x *Index, c *fakeClock) {
			c.set(70 * time.Minute)
			x.Record("e", h)
		}, 3},
		{"reported entries expire", func(x *Index, c *fakeClock) {
			x.Report("e", h[:2])
			c.set(61 * time.Minute)
		}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &fakeClock{}
			x := newIndex(Config{TTL: time.Hour}, c.read)
			defer x.Close()
			x.Record("e", h)
			tc.steps(x, c)
			if got := blocksOf(x.Match(h, ids("e")))["e"]; got != tc.want {
				t.Fatalf("%d blocks, want %d", got, tc.want)
			}
			x.sweep(c.read() - x.ttl)
			if got := x.Stats().Entries; (got > 0) != (tc.want > 0) {
				t.Fatalf("sweep left %d entries for %d matching blocks", got, tc.want)
			}
			verify(t, x)
		})
	}
}

func TestTTLSweepBatches(t *testing.T) {
	c := &fakeClock{}
	x := newIndex(Config{TTL: time.Hour, DefaultBlocks: 10 * sweepBatch}, c.read)
	defer x.Close()
	x.Record("old", chainOf("old", 3*sweepBatch+7))
	x.Report("old", chainOf("pinned", sweepBatch+1))
	x.Record("old", chainOf("old", 3*sweepBatch+7))
	c.advance(30 * time.Minute)
	x.Record("new", chainOf("new", 10))
	c.advance(31 * time.Minute)
	x.sweep(c.read() - x.ttl)
	st := x.Stats()
	if st.Entries != 10 || st.Endpoints != 2 {
		t.Fatalf("sweep left %+v, want only the 10 fresh entries", st)
	}
	verify(t, x)
}

func TestTTLSweepGoroutine(t *testing.T) {
	x := New(Config{TTL: 20 * time.Millisecond})
	defer x.Close()
	x.Record("e", chainOf("g", 50))
	deadline := time.Now().Add(5 * time.Second)
	for x.Stats().Entries > 0 {
		if time.Now().After(deadline) {
			t.Fatalf("sweep left %d entries", x.Stats().Entries)
		}
		time.Sleep(5 * time.Millisecond)
	}
	verify(t, x)
}

func TestMaxEntries(t *testing.T) {
	a, b, c := chainOf("a", 6), chainOf("b", 6), chainOf("c", 3)
	clock := &fakeClock{}
	tick := func() int64 { return clock.now.Add(1) }
	x := newIndex(Config{MaxEntries: 10}, tick)
	x.Record("a", a)
	x.Record("b", b)
	got := blocksOf(x.Match(a, ids("a")))
	if got["a"] != 4 {
		t.Fatalf("least recent endpoint kept %d blocks, want 4", got["a"])
	}
	x.Record("c", c)
	got = blocksOf(x.Match(b, ids("b")))
	if got["b"] != 3 {
		t.Fatalf("least recent endpoint after a match kept %d blocks, want 3", got["b"])
	}
	if got := blocksOf(x.Match(a, ids("a")))["a"]; got != 4 {
		t.Fatalf("recently matched endpoint lost entries, %d blocks", got)
	}
	verify(t, x)

	x.Report("r", chainOf("r", 10))
	if st := x.Stats(); st.Entries != 10 {
		t.Fatalf("cap exceeded by a report, %d entries", st.Entries)
	}
	x.Record("s", chainOf("s", 4))
	if got := blocksOf(x.Match(chainOf("r", 10), ids("r")))["r"]; got != 6 {
		t.Fatalf("cap kept %d reported blocks, want 6", got)
	}
	verify(t, x)
}

func TestStatsBytes(t *testing.T) {
	x := New(Config{})
	if x.Stats().Bytes != 0 {
		t.Fatalf("empty index estimates %d bytes", x.Stats().Bytes)
	}
	x.Record("a", chainOf("a", 100))
	one := x.Stats().Bytes
	for i := range 20 {
		x.Record(datalayer.EndpointID(fmt.Sprintf("crowd%d", i)), chainOf("a", 100))
	}
	twentyOne := x.Stats().Bytes
	if one <= 0 || twentyOne <= one || twentyOne > 30*one {
		t.Fatalf("estimates %d bytes for one endpoint and %d for twenty one", one, twentyOne)
	}
	verify(t, x)
}

func TestConcurrent(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
	}{
		{"lru and cap", Config{DefaultBlocks: 40, MaxEntries: 400}},
		{"lru cap and sweep", Config{DefaultBlocks: 40, MaxEntries: 400, TTL: 2 * time.Millisecond}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			x := New(tc.cfg)
			defer x.Close()
			hammer(t, x)
			x.Close()
			if tc.cfg.TTL == 0 && x.Stats().Entries == 0 {
				t.Fatalf("index emptied without a TTL")
			}
			verify(t, x)
		})
	}
}

// Runs every operation from many goroutines over a small shared universe
func hammer(t *testing.T, x *Index) {
	chains := make([][]BlockHash, 12)
	for i := range chains {
		chains[i] = chainOf(fmt.Sprintf("c%d", i%6), 10+i*3)
	}
	eps := ids("a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k", "l")
	var wg sync.WaitGroup
	for w := range 16 {
		wg.Go(func() {
			r := rand.New(rand.NewPCG(uint64(w), 7))
			for range 3000 {
				ep := eps[r.IntN(len(eps))]
				ch := chains[r.IntN(len(chains))]
				switch op := r.IntN(100); {
				case op < 45:
					x.Record(ep, ch)
				case op < 85:
					ms := x.Match(ch, eps[:r.IntN(len(eps))+1])
					for i := 1; i < len(ms); i++ {
						if ms[i].Blocks > ms[i-1].Blocks {
							t.Errorf("match not sorted %v", ms)
						}
					}
				case op < 92:
					x.Report(ep, ch[:r.IntN(len(ch))])
				case op < 96:
					x.SetCapacity(ep, r.IntN(60))
				default:
					x.Forget(ep)
				}
				if r.IntN(500) == 0 {
					x.Stats()
				}
			}
		})
	}
	wg.Wait()
}
