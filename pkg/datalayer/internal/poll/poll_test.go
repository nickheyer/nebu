package poll

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/nickheyer/nebu/pkg/datalayer"
)

// Counts ticks and exits per endpoint
type counter struct {
	mu    sync.Mutex
	ticks map[datalayer.EndpointID]int
	exits map[datalayer.EndpointID]int
}

func newCounter() *counter {
	return &counter{ticks: map[datalayer.EndpointID]int{}, exits: map[datalayer.EndpointID]int{}}
}

func (c *counter) tick(_ context.Context, id datalayer.EndpointID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ticks[id]++
}

func (c *counter) exit(id datalayer.EndpointID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.exits[id]++
}

func (c *counter) get(m map[datalayer.EndpointID]int, id datalayer.EndpointID) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return m[id]
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition never held")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestGroupLifecycle(t *testing.T) {
	c := newCounter()
	g := NewGroup(5*time.Millisecond, c.tick, c.exit)
	if err := g.Update([]datalayer.EndpointID{"a", "b"}); err != nil {
		t.Fatal(err)
	}
	if c.get(c.ticks, "a") != 0 {
		t.Fatal("ticked before start")
	}
	if err := g.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := g.Start(context.Background()); !errors.Is(err, ErrStarted) {
		t.Fatalf("second start err = %v", err)
	}
	waitFor(t, func() bool { return c.get(c.ticks, "a") >= 3 && c.get(c.ticks, "b") >= 3 })

	steps := []struct {
		name      string
		ids       []datalayer.EndpointID
		wantExits map[datalayer.EndpointID]int
		ticking   []datalayer.EndpointID
	}{
		{"drop b add c", []datalayer.EndpointID{"a", "c"}, map[datalayer.EndpointID]int{"a": 0, "b": 1, "c": 0}, []datalayer.EndpointID{"a", "c"}},
		{"same set changes nothing", []datalayer.EndpointID{"c", "a"}, map[datalayer.EndpointID]int{"a": 0, "b": 1, "c": 0}, []datalayer.EndpointID{"a", "c"}},
		{"re-add b", []datalayer.EndpointID{"a", "b", "c"}, map[datalayer.EndpointID]int{"a": 0, "b": 1, "c": 0}, []datalayer.EndpointID{"a", "b", "c"}},
	}
	for _, st := range steps {
		t.Run(st.name, func(t *testing.T) {
			if err := g.Update(st.ids); err != nil {
				t.Fatal(err)
			}
			for id, want := range st.wantExits {
				if got := c.get(c.exits, id); got != want {
					t.Fatalf("%s exits = %d, want %d", id, got, want)
				}
			}
			for _, id := range st.ticking {
				before := c.get(c.ticks, id)
				waitFor(t, func() bool { return c.get(c.ticks, id) > before })
			}
		})
	}
	if err := g.Stop(); err != nil {
		t.Fatal(err)
	}
	for _, id := range []datalayer.EndpointID{"a", "b", "c"} {
		before := c.get(c.ticks, id)
		time.Sleep(15 * time.Millisecond)
		if c.get(c.ticks, id) != before {
			t.Fatalf("%s ticked after stop", id)
		}
	}
	if c.get(c.exits, "b") != 2 || c.get(c.exits, "a") != 1 {
		t.Fatalf("exits after stop = %v", c.exits)
	}
	if err := g.Stop(); err != nil {
		t.Fatal(err)
	}
	if err := g.Start(context.Background()); err != nil {
		t.Fatalf("restart err = %v", err)
	}
	defer g.Stop()
	before := c.get(c.ticks, "a")
	waitFor(t, func() bool { return c.get(c.ticks, "a") > before })
}

// Returns one preset value or error from every extraction
type fixed struct {
	name string
	v    any
	err  error
}

func (f fixed) Name() string                                 { return f.name }
func (f fixed) Extract(datalayer.Endpoint, any) (any, error) { return f.v, f.err }

func TestCollectors(t *testing.T) {
	boom := errors.New("boom")
	tests := []struct {
		name    string
		cols    []fixed
		id      datalayer.EndpointID
		wantErr error
		want    map[string]any
	}{
		{"stores every value", []fixed{{"a", 1, nil}, {"b", 2, nil}}, "e", nil, map[string]any{"a": 1, "b": 2}},
		{"failing collector keeps the rest", []fixed{{"a", 1, boom}, {"b", 2, nil}}, "e", boom, map[string]any{"a": "old", "b": 2}},
		{"unknown endpoint", []fixed{{"a", 1, nil}}, "zz", datalayer.ErrUnknownEndpoint, map[string]any{"a": "old"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := datalayer.NewStore()
			store.UpsertEndpoint(datalayer.EndpointSpec{ID: "e", Pool: "p"})
			store.SetAttribute("e", "a", "old")
			var cs Collectors
			for _, c := range tc.cols {
				if err := cs.Subscribe(c); err != nil {
					t.Fatal(err)
				}
			}
			if err := cs.Subscribe(tc.cols[0]); !errors.Is(err, datalayer.ErrDuplicateCollector) {
				t.Fatalf("duplicate subscribe err = %v", err)
			}
			if err := cs.Deliver(store, tc.id, nil); !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			ep, _ := store.Endpoint("e")
			for k, want := range tc.want {
				if got, _ := ep.Attribute(k); got != want {
					t.Fatalf("%s = %v, want %v", k, got, want)
				}
			}
			cs.Clear(store, "e")
			ep, _ = store.Endpoint("e")
			for _, c := range tc.cols {
				if _, ok := ep.Attribute(c.name); ok {
					t.Fatalf("%s survived clear", c.name)
				}
			}
		})
	}
}
