package kvevents

import (
	"context"
	"errors"
	"math/rand"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/nickheyer/nebu/pkg/datalayer"
	"github.com/nickheyer/nebu/pkg/prefixcache"
)

// Sixteen token ids starting at first
func tokens(first, n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = first + i
	}
	return out
}

// Batches of vLLM's shape, bytes hashes with a lora name, and SGLang's shape, integer hashes, decode to one Batch
func TestDecodeBatch(t *testing.T) {
	h1, h2 := []byte("0123456789abcdef0123456789abcdef"), []byte("fedcba9876543210fedcba9876543210")
	vllm := encodeBatch(
		event{kind: "BlockStored", hashes: []any{h1, h2}, parent: nil, tokens: tokens(1, 32), size: 16, lora: nil, loraName: "sql", extra: map[string]any{"group_idx": 0, "extra_keys": []any{nil, nil}}},
		event{kind: "BlockRemoved", hashes: []any{h1}},
		event{kind: "AllBlocksCleared"},
	)
	got, err := decodeBatch(7, vllm)
	if err != nil {
		t.Fatal(err)
	}
	want := &Batch{Seq: 7, Cleared: true,
		Stored:  []Stored{{Hashes: []string{string(h1), string(h2)}, TokenIDs: tokens(1, 32), BlockSize: 16, Adapter: "sql"}},
		Removed: []Removed{{Hashes: []string{string(h1)}}},
		Order:   []step{{stepStored, 0}, {stepRemoved, 0}, {stepCleared, 0}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("vllm batch\n got %+v\nwant %+v", got, want)
	}
	sglang := encodeBatch(event{kind: "BlockStored", hashes: []any{int64(-5), int64(1 << 40)}, parent: int64(-5), tokens: tokens(1, 2), size: 1, lora: nil, extra: map[string]any{"cache_salt": nil}})
	got, err = decodeBatch(0, sglang)
	if err != nil {
		t.Fatal(err)
	}
	neg := "\xff\xff\xff\xff\xff\xff\xff\xfb"
	big := "\x00\x00\x01\x00\x00\x00\x00\x00"
	if len(got.Stored) != 1 || !got.Stored[0].HasParent || got.Stored[0].Parent != neg || !slices.Equal(got.Stored[0].Hashes, []string{neg, big}) {
		t.Fatalf("sglang batch %+v", got.Stored)
	}
	bad := []struct {
		name string
		b    event
		err  error
	}{
		{"unknown type", event{kind: "BlockMoved"}, ErrEventType},
		{"tokens past the blocks", event{kind: "BlockStored", hashes: []any{h1}, tokens: tokens(1, 17), size: 16}, ErrEventTokens},
		{"tokens short of the blocks", event{kind: "BlockStored", hashes: []any{h1, h2}, tokens: tokens(1, 16), size: 16}, ErrEventTokens},
		{"adapter by numeric id alone", event{kind: "BlockStored", hashes: []any{h1}, tokens: tokens(1, 16), size: 16, lora: 3}, ErrEventLoRA},
	}
	for _, tc := range bad {
		if _, err := decodeBatch(0, encodeBatch(tc.b)); !errors.Is(err, tc.err) {
			t.Errorf("%s: err = %v, want %v", tc.name, err, tc.err)
		}
	}
	if _, err := decodeBatch(0, vllm[:len(vllm)/2]); !errors.Is(err, ErrTruncated) {
		t.Errorf("truncated err = %v", err)
	}
}

// Chained blocks of any size land on exactly the hashes the index computes for the prompt through each block
func TestChainMatchesIndex(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for _, tc := range []struct{ chunk, block, blocks, limit int }{{16, 16, 8, 256}, {16, 5, 20, 256}, {7, 16, 9, 256}, {16, 32, 5, 4}, {4, 3, 10, 256}} {
		idx := prefixcache.New(prefixcache.Config{ChunkTokens: tc.chunk, MaxChunks: tc.limit})
		all := make([]int, tc.block*tc.blocks)
		for i := range all {
			all[i] = rng.Intn(1 << 17)
		}
		c := newCache("m", tc.chunk, tc.limit)
		var parent string
		var hashes []string
		for i := 0; i < tc.blocks; i++ {
			h := string(rune('a'+i)) + "hash"
			ev := Stored{Hashes: []string{h}, Parent: parent, HasParent: i > 0, TokenIDs: all[i*tc.block : (i+1)*tc.block], BlockSize: tc.block, Adapter: "lora"}
			if !c.store(ev) {
				t.Fatalf("block %d orphaned", i)
			}
			parent = h
			hashes = append(hashes, h)
		}
		held := map[prefixcache.BlockHash]bool{}
		for _, h := range c.hashes() {
			held[h] = true
		}
		want := map[prefixcache.BlockHash]bool{}
		for i := 1; i <= tc.blocks; i++ {
			for _, h := range idx.Hash("m", "lora", "", all[:i*tc.block]) {
				want[h] = true
			}
		}
		if !reflect.DeepEqual(held, want) {
			t.Fatalf("chunk %d block %d: held %d hashes, index wants %d", tc.chunk, tc.block, len(held), len(want))
		}
		for _, h := range idx.Hash("other", "lora", "", all) {
			if held[h] {
				t.Fatal("another model's hash held")
			}
		}
		own := c.blocks[hashes[tc.blocks-1]].own
		c.apply(&Batch{Removed: []Removed{{Hashes: hashes[tc.blocks-1:]}}, Order: []step{{stepRemoved, 0}}})
		for _, h := range own {
			delete(want, h)
		}
		held = map[prefixcache.BlockHash]bool{}
		for _, h := range c.hashes() {
			held[h] = true
		}
		if !reflect.DeepEqual(held, want) || c.size() != tc.blocks-1 {
			t.Fatal("removing the last block kept its hashes")
		}
	}
	c := newCache("m", 16, 256)
	if c.store(Stored{Hashes: []string{"x"}, Parent: "never", HasParent: true, TokenIDs: tokens(0, 16), BlockSize: 16}) {
		t.Fatal("a block under an unknown parent stored")
	}
}

type recorder struct {
	mu     sync.Mutex
	errs   []error
	blocks []int
}

func (r *recorder) OnKVEvents(_ datalayer.EndpointID, blocks int, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.errs = append(r.errs, err)
	r.blocks = append(r.blocks, blocks)
}

func (r *recorder) has(target error) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, err := range r.errs {
		if errors.Is(err, target) {
			return true
		}
	}
	return false
}

// A source over one endpoint publishing at the fake, reporting into a fresh index
func startSource(t *testing.T, pub *fakePub, replay bool, rec *recorder) (*Source, *prefixcache.Index) {
	t.Helper()
	store := datalayer.NewStore()
	store.PutPool(datalayer.Pool{ID: "p", Model: "m"})
	labels := map[string]string{datalayer.LabelKVEvents: pub.events()}
	if replay {
		labels[datalayer.LabelKVReplay] = pub.replays()
	}
	store.UpsertEndpoint(datalayer.EndpointSpec{ID: "e", Pool: "p", Labels: labels, Ready: true})
	idx := prefixcache.New(prefixcache.Config{ChunkTokens: 16})
	src := New(store, StoreResolver(store), Options{Interval: 5 * time.Millisecond, Reconnect: 20 * time.Millisecond, Observer: rec})
	if err := src.Start(context.Background()); !errors.Is(err, ErrNoIndex) {
		t.Fatalf("start without an index err = %v", err)
	}
	src.SetIndexer(idx)
	if err := src.UpdateEndpoints([]datalayer.EndpointID{"e"}); err != nil {
		t.Fatal(err)
	}
	if err := src.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := src.Start(context.Background()); !errors.Is(err, ErrStarted) {
		t.Fatalf("second start err = %v", err)
	}
	t.Cleanup(func() { _ = src.Stop() })
	return src, idx
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// Blocks a prompt of n tokens matches on endpoint e
func matched(idx *prefixcache.Index, prompt []int) int {
	for _, m := range idx.Match(idx.Hash("m", "", "", prompt), []datalayer.EndpointID{"e"}) {
		if m.Endpoint == "e" {
			return m.Blocks
		}
	}
	return 0
}

// Stored blocks become index matches, removed and cleared blocks leave, under both ZMTP minor versions
func TestSourceReportsBlocks(t *testing.T) {
	for _, minor := range []byte{1, 0} {
		pub := newFakePub(t, minor)
		rec := &recorder{}
		_, idx := startSource(t, pub, true, rec)
		eventually(t, "subscriber", func() bool { return pub.subscribers() == 1 })
		prompt := tokens(100, 48)
		pub.publish(encodeBatch(event{kind: "BlockStored", hashes: []any{"a", "b", "c"}, parent: nil, tokens: prompt, size: 16, lora: nil}))
		eventually(t, "three matched blocks", func() bool { return matched(idx, prompt) == 3 })
		if matched(idx, tokens(100, 32)) != 2 || matched(idx, tokens(5, 48)) != 0 {
			t.Fatal("prefix matches disagree with the stored chain")
		}
		pub.publish(encodeBatch(event{kind: "BlockRemoved", hashes: []any{"c"}}))
		eventually(t, "two matched blocks", func() bool { return matched(idx, prompt) == 2 })
		pub.publish(encodeBatch(event{kind: "AllBlocksCleared"}))
		eventually(t, "no matched blocks", func() bool { return matched(idx, prompt) == 0 })
		if rec.has(ErrGap) || rec.has(ErrOrphans) {
			t.Fatalf("errors %v", rec.errs)
		}
	}
}

// Batches buffered before the subscription and those lost in flight are replayed, a gap past the buffer restarts the view
func TestReplay(t *testing.T) {
	pub := newFakePub(t, 1)
	early := tokens(0, 16)
	pub.publish(encodeBatch(event{kind: "BlockStored", hashes: []any{"a"}, parent: nil, tokens: early, size: 16, lora: nil}))
	rec := &recorder{}
	_, idx := startSource(t, pub, true, rec)
	eventually(t, "early block replayed", func() bool { return matched(idx, early) == 1 })
	eventually(t, "subscriber", func() bool { return pub.subscribers() == 1 })
	dropped := tokens(200, 16)
	pub.drop(encodeBatch(event{kind: "BlockStored", hashes: []any{"d"}, parent: nil, tokens: dropped, size: 16, lora: nil}))
	live := tokens(300, 16)
	pub.publish(encodeBatch(event{kind: "BlockStored", hashes: []any{"l"}, parent: "d", tokens: live, size: 16, lora: nil}))
	eventually(t, "dropped block replayed", func() bool { return matched(idx, dropped) == 1 && matched(idx, slices.Concat(dropped, live)) == 2 })
	if rec.has(ErrGap) || rec.has(ErrOrphans) {
		t.Fatalf("errors %v", rec.errs)
	}
	pub.drop(encodeBatch(event{kind: "BlockStored", hashes: []any{"lost"}, parent: nil, tokens: tokens(400, 16), size: 16, lora: nil}))
	pub.forget(4)
	after := tokens(500, 16)
	pub.publish(encodeBatch(event{kind: "BlockStored", hashes: []any{"after"}, parent: nil, tokens: after, size: 16, lora: nil}))
	eventually(t, "view restarted", func() bool { return rec.has(ErrGap) && matched(idx, after) == 1 && matched(idx, early) == 0 })
}

// A dropped connection is dialed again and the publisher's buffer refills the view
func TestReconnect(t *testing.T) {
	pub := newFakePub(t, 1)
	rec := &recorder{}
	_, idx := startSource(t, pub, true, rec)
	eventually(t, "subscriber", func() bool { return pub.subscribers() == 1 })
	pub.kick()
	eventually(t, "resubscribed", func() bool { return pub.subscribers() == 1 })
	prompt := tokens(0, 16)
	pub.publish(encodeBatch(event{kind: "BlockStored", hashes: []any{"a"}, parent: nil, tokens: prompt, size: 16, lora: nil}))
	eventually(t, "block after reconnect", func() bool { return matched(idx, prompt) == 1 })
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if !slices.ContainsFunc(rec.errs, func(err error) bool { return err != nil && !errors.Is(err, ErrGap) }) {
		t.Fatal("the dropped connection went unreported")
	}
}

// An endpoint without a replay address loses the view on a gap, and a target change restarts its subscription
func TestNoReplayAndRetarget(t *testing.T) {
	pub := newFakePub(t, 1)
	rec := &recorder{}
	src, idx := startSource(t, pub, false, rec)
	eventually(t, "subscriber", func() bool { return pub.subscribers() == 1 })
	first := tokens(0, 16)
	pub.publish(encodeBatch(event{kind: "BlockStored", hashes: []any{"a"}, parent: nil, tokens: first, size: 16, lora: nil}))
	eventually(t, "first block", func() bool { return matched(idx, first) == 1 })
	pub.drop(encodeBatch(event{kind: "AllBlocksCleared"}))
	second := tokens(50, 16)
	pub.publish(encodeBatch(event{kind: "BlockStored", hashes: []any{"b"}, parent: nil, tokens: second, size: 16, lora: nil}))
	eventually(t, "gap without replay", func() bool { return rec.has(ErrNoReplay) && matched(idx, second) == 1 && matched(idx, first) == 0 })
	other := newFakePub(t, 1)
	src.store.UpsertEndpoint(datalayer.EndpointSpec{ID: "e", Pool: "p", Labels: map[string]string{datalayer.LabelKVEvents: other.events()}, Ready: true})
	if err := src.UpdateEndpoints([]datalayer.EndpointID{"e"}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "moved subscription", func() bool { return other.subscribers() == 1 })
	if err := src.UpdateEndpoints(nil); err != nil {
		t.Fatal(err)
	}
	src.mu.Lock()
	n := len(src.workers)
	src.mu.Unlock()
	if n != 0 {
		t.Fatal("untracked endpoint kept its subscription")
	}
}

// The store resolver reads the publisher labels and the pool model, refusing endpoints without them
func TestStoreResolver(t *testing.T) {
	store := datalayer.NewStore()
	store.PutPool(datalayer.Pool{ID: "p", Model: "m"})
	store.UpsertEndpoint(datalayer.EndpointSpec{ID: "pub", Pool: "p", Labels: map[string]string{datalayer.LabelKVEvents: "10.0.0.1:9", datalayer.LabelKVReplay: "10.0.0.1:10"}})
	store.UpsertEndpoint(datalayer.EndpointSpec{ID: "quiet", Pool: "p"})
	store.UpsertEndpoint(datalayer.EndpointSpec{ID: "orphan", Pool: "missing", Labels: map[string]string{datalayer.LabelKVEvents: "10.0.0.1:9"}})
	resolve := StoreResolver(store)
	if got, ok := resolve("pub"); !ok || got != (Target{Events: "10.0.0.1:9", Replay: "10.0.0.1:10", Model: "m"}) {
		t.Fatalf("pub %+v %v", got, ok)
	}
	for _, id := range []datalayer.EndpointID{"quiet", "orphan", "absent"} {
		if _, ok := resolve(id); ok {
			t.Fatalf("%s resolved", id)
		}
	}
}
