package stub

import (
	"slices"
	"strconv"
	"testing"
	"time"
)

func TestTokenize(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"hello world", []string{"hello", "world"}},
		{"Hello, world!", []string{"Hello", ",", "world", "!"}},
		{"  a\tb\nc  ", []string{"a", "b", "c"}},
		{"x1y2 3.5", []string{"x1y2", "3", ".", "5"}},
		{"héllo wörld", []string{"héllo", "wörld"}},
		{"<|user|>", []string{"<", "|", "user", "|", ">"}},
		{"", nil},
	}
	for _, c := range cases {
		if got := Tokenize(c.in); !slices.Equal(got, c.want) {
			t.Errorf("Tokenize(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	a, b := tokenIDs(Tokenize("same text, same cost")), tokenIDs(Tokenize("same   text ,same cost"))
	if !slices.Equal(a, b) {
		t.Fatalf("whitespace changed token ids %v %v", a, b)
	}
}

func TestBlockHashesChain(t *testing.T) {
	ids := tokenIDs(Tokenize(promptOf("t", 40)))
	full := blockHashes("m", ids, 16)
	if len(full) != 2 {
		t.Fatalf("got %d hashes for 40 tokens in blocks of 16, want 2", len(full))
	}
	if !slices.Equal(blockHashes("m", ids[:32], 16), full) {
		t.Fatal("a shared prefix produced different hashes")
	}
	if blockHashes("other", ids, 16)[0] == full[0] {
		t.Fatal("scope did not change the hash")
	}
	changed := slices.Clone(ids)
	changed[3]++
	h := blockHashes("m", changed, 16)
	if h[0] == full[0] || h[1] == full[1] {
		t.Fatal("a change in the first block did not change every later hash")
	}
}

func TestKVCacheEvictsLeastRecentlyUsed(t *testing.T) {
	c := newKVCache(4)
	a := c.allocate(2)
	c.register(a[0], 1)
	c.register(a[1], 2)
	c.release(a)
	if c.available() != 4 || c.used() != 0 {
		t.Fatalf("available %d used %d after release", c.available(), c.used())
	}
	ids, idle := c.match([]uint64{1, 2, 3})
	if len(ids) != 2 || idle != 2 {
		t.Fatalf("match found %v idle %d", ids, idle)
	}
	b := c.allocate(3)
	if len(b) != 3 || c.used() != 3 {
		t.Fatalf("allocated %v used %d", b, c.used())
	}
	if ids, _ := c.match([]uint64{1, 2}); len(ids) != 1 {
		t.Fatalf("eviction took the head block, matched %v", ids)
	}
	c.acquire([]int{a[0]})
	if c.available() != 0 {
		t.Fatalf("acquired cached block still counted available")
	}
	c.release([]int{a[0]})
	c.release(b)
	if c.available() != 4 {
		t.Fatalf("available %d after releasing everything", c.available())
	}
}

// An engine without a loop for stepping by hand
func newTestEngine(t *testing.T, cfg Config) *engine {
	t.Helper()
	cfg, err := cfg.withDefaults()
	if err != nil {
		t.Fatal(err)
	}
	return newEngine(cfg, time.Unix(1000, 0))
}

// Queues a single sequence request of distinct tokens
func queueSeq(t *testing.T, e *engine, id string, tokens, target int, at time.Time) *seq {
	t.Helper()
	toks := make([]string, tokens)
	for i := range toks {
		toks[i] = id + strconv.Itoa(i)
	}
	ids := tokenIDs(toks)
	g := &group{id: id, model: e.cfg.Model, limit: "length", notify: make(chan struct{}, 1), arrival: at}
	s := &seq{g: g, lead: true, ids: ids, hashes: blockHashes(g.model, ids, e.cfg.BlockSize), target: target}
	g.seqs = []*seq{s}
	if err := e.submit(g); err != nil {
		t.Fatal(err.message)
	}
	return s
}

// Runs one scheduler step at a given time
func stepAt(e *engine, now time.Time) (time.Time, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.stepLocked(now)
}

func TestPreemptionReturnsNewestToQueueHead(t *testing.T) {
	e := newTestEngine(t, Config{BlockSize: 4, NumGPUBlocks: 8, MaxNumSeqs: 2, DecodePerToken: time.Second})
	t0 := time.Unix(1000, 0)
	a := queueSeq(t, e, "a", 8, 12, t0)
	b := queueSeq(t, e, "b", 8, 12, t0)
	c := queueSeq(t, e, "c", 4, 1, t0)
	sawPreempt, sawReadmit := false, false
	now := t0
	for range 500 {
		next, ok := stepAt(e, now)
		if e.preempted == 1 && !sawPreempt {
			sawPreempt = true
			if len(e.queue) != 2 || e.queue[0] != b || e.queue[1] != c {
				t.Fatalf("queue after preemption is not [b c]")
			}
			if b.blocks != nil || b.phase != phaseQueued || b.generated == 0 {
				t.Fatalf("preempted b kept blocks %v phase %d generated %d", b.blocks, b.phase, b.generated)
			}
		}
		if a.phase == phaseFinished && !sawReadmit {
			sawReadmit = true
			if len(e.running) != 2 || e.running[0] != b || e.running[1] != c {
				t.Fatal("b did not re-enter the running set ahead of c")
			}
		}
		if !ok {
			break
		}
		now = next
	}
	if !sawPreempt || !sawReadmit {
		t.Fatalf("preempted %v readmitted %v", sawPreempt, sawReadmit)
	}
	for _, s := range []*seq{a, b, c} {
		if s.phase != phaseFinished || s.generated != s.target || string(s.text) != words(s.target, false) {
			t.Fatalf("%s ended phase %d with %d of %d tokens", s.g.id, s.phase, s.generated, s.target)
		}
	}
	if e.served != 3 || e.preempted != 1 || e.hits != 2 || e.misses != 2 {
		t.Fatalf("served %d preempted %d hits %d misses %d", e.served, e.preempted, e.hits, e.misses)
	}
	if e.kv.used() != 0 {
		t.Fatalf("%d blocks still referenced", e.kv.used())
	}
}

func TestContentionModel(t *testing.T) {
	cfg := Config{BlockSize: 4, BaseLatency: 10 * time.Millisecond, PrefillPerToken: time.Millisecond, DecodePerToken: 4 * time.Millisecond, Contention: 0.5}
	e := newTestEngine(t, cfg)
	if got := e.ttft(100, 2); got != 210*time.Millisecond {
		t.Fatalf("ttft %v, want 210ms", got)
	}
	if got := e.tpot(2); got != 8*time.Millisecond {
		t.Fatalf("tpot %v, want 8ms", got)
	}
	t0 := time.Unix(1000, 0)
	a := queueSeq(t, e, "a", 8, 3, t0)
	b := queueSeq(t, e, "b", 8, 3, t0)
	stepAt(e, t0)
	if a.next != t0.Add(18*time.Millisecond) || b.next != t0.Add(22*time.Millisecond) {
		t.Fatalf("prefill ends %v %v", a.next.Sub(t0), b.next.Sub(t0))
	}
	stepAt(e, a.next)
	if a.generated != 1 || a.next != t0.Add(24*time.Millisecond) {
		t.Fatalf("a generated %d next %v", a.generated, a.next.Sub(t0))
	}
	cfg.Mode, cfg.Speedup = ModeRatio, 10
	r := newTestEngine(t, cfg)
	r.faults.latency = 2
	if got := r.wall(210 * time.Millisecond); got != 42*time.Millisecond {
		t.Fatalf("ratio wall time %v, want 42ms", got)
	}
	if got := r.emulated(21 * time.Millisecond); got != 210*time.Millisecond {
		t.Fatalf("emulated %v, want 210ms", got)
	}
}

func TestConfigValidation(t *testing.T) {
	bad := []Config{
		{Names: "tgi"},
		{Mode: ModeRatio},
		{Mode: "fast"},
		{MaxNumSeqs: -1},
		{DecodePerToken: -time.Second},
		{Contention: -0.1},
		{MaxLoras: 1, Adapters: []Adapter{{"a", "/a"}, {"b", "/b"}}},
		{Adapters: []Adapter{{"a", "/a"}, {"a", "/b"}}},
		{Model: "m", Adapters: []Adapter{{"m", "/m"}}},
		{Adapters: []Adapter{{"a", ""}}},
	}
	for i, c := range bad {
		if s, err := New(c); err == nil {
			s.Close()
			t.Errorf("config %d accepted", i)
		}
	}
	s, err := New(Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	want := Config{ID: "stub", Model: "stub-model", Names: NamesVLLM, MaxNumSeqs: 256, NumGPUBlocks: 4096, BlockSize: 16, MaxLoras: 4, Mode: ModeTime, DefaultMaxTokens: 16}
	if got := s.cfg; got.ID != want.ID || got.Model != want.Model || got.Names != want.Names || got.MaxNumSeqs != want.MaxNumSeqs ||
		got.NumGPUBlocks != want.NumGPUBlocks || got.BlockSize != want.BlockSize || got.MaxLoras != want.MaxLoras || got.Mode != want.Mode || got.DefaultMaxTokens != want.DefaultMaxTokens {
		t.Fatalf("defaults %+v", got)
	}
}
