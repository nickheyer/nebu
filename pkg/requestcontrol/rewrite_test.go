package requestcontrol

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/nickheyer/nebu/pkg/datalayer"
)

// A table over pools p1 serving llama and llama-lora, p2 serving mistral and p3 serving qwen
func newTable(t *testing.T) (*RuleTable, *fakeStore) {
	t.Helper()
	store := newFakeStore()
	for _, p := range []datalayer.Pool{{ID: "p1", Model: "llama"}, {ID: "p2", Model: "mistral"}, {ID: "p3", Model: "qwen"}} {
		store.PutPool(p)
	}
	store.UpsertEndpoint(endpoint("e1", "p1", "10.0.0.1", true))
	store.UpsertEndpoint(endpoint("e2", "p2", "10.0.0.2", true))
	table := NewRuleTable(store)
	table.Put("p1", []string{"llama"}, []string{"llama-lora"})
	table.Put("p2", []string{"mistral"}, nil)
	table.Put("p3", []string{"qwen"}, nil)
	return table, store
}

func one(model string) []RewriteTarget { return []RewriteTarget{{Model: model, Weight: 1}} }

func TestRulePrecedence(t *testing.T) {
	old := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	young := old.Add(time.Hour)
	cases := []struct {
		name   string
		sets   map[string][]RewriteRule
		client string
		target string
		pool   datalayer.PoolID
		err    error
	}{
		{"no rule passes through", nil, "mistral", "mistral", "p2", nil},
		{"exact rule", map[string][]RewriteRule{"a": {{Match: "public", Targets: one("qwen")}}}, "public", "qwen", "p3", nil},
		{"exact over catch all", map[string][]RewriteRule{
			"a": {{Match: "", Targets: one("mistral"), CreatedAt: old}},
			"b": {{Match: "public", Targets: one("qwen"), CreatedAt: young}},
		}, "public", "qwen", "p3", nil},
		{"catch all for others", map[string][]RewriteRule{
			"a": {{Match: "", Targets: one("mistral")}},
			"b": {{Match: "public", Targets: one("qwen")}},
		}, "llama", "mistral", "p2", nil},
		{"oldest wins", map[string][]RewriteRule{
			"a": {{Match: "public", Targets: one("qwen"), CreatedAt: young}},
			"b": {{Match: "public", Targets: one("mistral"), CreatedAt: old}},
		}, "public", "mistral", "p2", nil},
		{"lower order wins at equal age", map[string][]RewriteRule{
			"a": {{Match: "public", Targets: one("qwen"), CreatedAt: old, Order: 2}, {Match: "public", Targets: one("mistral"), CreatedAt: old, Order: 1}},
		}, "public", "mistral", "p2", nil},
		{"set name breaks full ties", map[string][]RewriteRule{
			"b": {{Match: "public", Targets: one("qwen"), CreatedAt: old}},
			"a": {{Match: "public", Targets: one("mistral"), CreatedAt: old}},
		}, "public", "mistral", "p2", nil},
		{"oldest catch all", map[string][]RewriteRule{
			"a": {{Targets: one("qwen"), CreatedAt: young}},
			"b": {{Targets: one("mistral"), CreatedAt: old}},
		}, "", "mistral", "p2", nil},
		{"unknown model", nil, "gpt", "", "", ErrUnknownModel},
		{"no model and no catch all", nil, "", "", "", ErrNoModel},
		{"catch all to unserved model", map[string][]RewriteRule{"a": {{Targets: one("gone")}}}, "", "", "", ErrUnknownModel},
		{"rule to unserved model", map[string][]RewriteRule{"a": {{Match: "public", Targets: one("gone")}}}, "public", "", "", ErrUnknownModel},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			table, _ := newTable(t)
			for name, rules := range c.sets {
				if err := table.SetRules(name, rules); err != nil {
					t.Fatal(err)
				}
			}
			pool, target, err := table.Resolve(context.Background(), c.client)
			if c.err != nil {
				if !errors.Is(err, c.err) {
					t.Fatalf("err %v, want %v", err, c.err)
				}
				return
			}
			if err != nil || target != c.target || pool.ID != c.pool {
				t.Fatalf("Resolve = %s %q %v, want %s %q", pool.ID, target, err, c.pool, c.target)
			}
		})
	}
}

func TestRuleSetLifecycle(t *testing.T) {
	table, _ := newTable(t)
	if err := table.SetRules("a", []RewriteRule{{Match: "public", Targets: one("qwen")}}); err != nil {
		t.Fatal(err)
	}
	if err := table.SetRules("a", []RewriteRule{{Match: "public", Targets: one("mistral")}}); err != nil {
		t.Fatal(err)
	}
	if _, target, _ := table.Resolve(context.Background(), "public"); target != "mistral" {
		t.Fatalf("replaced set still routes to %q", target)
	}
	table.DeleteRules("a")
	if _, _, err := table.Resolve(context.Background(), "public"); !errors.Is(err, ErrUnknownModel) {
		t.Fatalf("deleted set still applies, err %v", err)
	}
	for _, bad := range [][]RewriteRule{
		{{Match: "x"}},
		{{Match: "x", Targets: []RewriteTarget{{Model: "", Weight: 1}}}},
		{{Match: "x", Targets: []RewriteTarget{{Model: "qwen", Weight: -1}}}},
	} {
		if err := table.SetRules("bad", bad); err == nil {
			t.Fatalf("rules %+v accepted", bad)
		}
	}
}

// Draws n picks and checks every target's share against its weight within five standard deviations
func checkShares(t *testing.T, table *RuleTable, weights map[string]float64, n int) {
	t.Helper()
	got := map[string]int{}
	for range n {
		_, target, err := table.Resolve(context.Background(), "public")
		if err != nil {
			t.Fatal(err)
		}
		got[target]++
	}
	total := 0.0
	for _, w := range weights {
		total += w
	}
	for model, w := range weights {
		p := w / total
		mean := p * float64(n)
		sd := math.Sqrt(float64(n) * p * (1 - p))
		if math.Abs(float64(got[model])-mean) > 5*sd+1e-9 {
			t.Fatalf("%s drawn %d times, want %.0f within %.0f", model, got[model], mean, 5*sd)
		}
	}
	for model := range got {
		if _, ok := weights[model]; !ok || weights[model] == 0 && got[model] > 0 {
			t.Fatalf("%s drawn %d times, want never", model, got[model])
		}
	}
}

func TestWeightedSplit(t *testing.T) {
	cases := []struct {
		name    string
		targets []RewriteTarget
		shares  map[string]float64
	}{
		{"one to three", []RewriteTarget{{Model: "mistral", Weight: 1}, {Model: "qwen", Weight: 3}}, map[string]float64{"mistral": 1, "qwen": 3}},
		{"canary", []RewriteTarget{{Model: "mistral", Weight: 95}, {Model: "qwen", Weight: 5}}, map[string]float64{"mistral": 95, "qwen": 5}},
		{"zero weight never chosen", []RewriteTarget{{Model: "mistral", Weight: 0}, {Model: "qwen", Weight: 2}, {Model: "llama", Weight: 2}}, map[string]float64{"mistral": 0, "qwen": 1, "llama": 1}},
		{"all zero splits evenly", []RewriteTarget{{Model: "mistral"}, {Model: "qwen"}, {Model: "llama"}}, map[string]float64{"mistral": 1, "qwen": 1, "llama": 1}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			table, _ := newTable(t)
			if err := table.SetRules("split", []RewriteRule{{Match: "public", Targets: c.targets}}); err != nil {
				t.Fatal(err)
			}
			checkShares(t, table, c.shares, 40000)
		})
	}
}

func TestLookup(t *testing.T) {
	cases := []struct {
		model   string
		pool    datalayer.PoolID
		adapter string
		err     bool
	}{
		{"llama", "p1", "", false},
		{"llama-lora", "p1", "llama-lora", false},
		{"mistral", "p2", "", false},
		{"public", "", "", true},
	}
	table, _ := newTable(t)
	if err := table.SetRules("a", []RewriteRule{{Match: "public", Targets: one("qwen")}}); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		pool, adapter, err := table.Lookup(context.Background(), c.model)
		if c.err {
			if !errors.Is(err, ErrUnknownModel) {
				t.Fatalf("Lookup(%s) err %v, want ErrUnknownModel skipping rules", c.model, err)
			}
			continue
		}
		if err != nil || pool.ID != c.pool || adapter != c.adapter {
			t.Fatalf("Lookup(%s) = %s %q %v", c.model, pool.ID, adapter, err)
		}
	}
}

func TestCatalogChanges(t *testing.T) {
	table, store := newTable(t)
	table.Put("p1", []string{"llama"}, nil)
	if _, adapter, err := table.Lookup(context.Background(), "llama-lora"); !errors.Is(err, ErrUnknownModel) {
		t.Fatalf("replaced catalog still serves the adapter %q, err %v", adapter, err)
	}
	table.Delete("p2")
	if _, _, err := table.Lookup(context.Background(), "mistral"); !errors.Is(err, ErrUnknownModel) {
		t.Fatalf("deleted pool still serves, err %v", err)
	}
	store.DeletePool("p3")
	if _, _, err := table.Lookup(context.Background(), "qwen"); !errors.Is(err, ErrUnknownModel) {
		t.Fatalf("pool gone from the store still serves, err %v", err)
	}
}

func TestSharedNameSplitsByReadiness(t *testing.T) {
	table, store := newTable(t)
	store.PutPool(datalayer.Pool{ID: "p4", Model: "mistral"})
	table.Put("p4", []string{"mistral"}, nil)
	for i, id := range []string{"f1", "f2", "f3"} {
		store.UpsertEndpoint(endpoint(id, "p4", "10.0.4."+string(rune('1'+i)), true))
	}
	counts := map[datalayer.PoolID]int{}
	const n = 20000
	for range n {
		pool, _, err := table.Lookup(context.Background(), "mistral")
		if err != nil {
			t.Fatal(err)
		}
		counts[pool.ID]++
	}
	p, mean := 0.25, 0.25*n
	if sd := math.Sqrt(n * p * (1 - p)); math.Abs(float64(counts["p2"])-mean) > 5*sd {
		t.Fatalf("p2 with one of four ready endpoints took %d of %d", counts["p2"], n)
	}
	store.UpsertEndpoint(endpoint("e2", "p2", "10.0.0.2", false))
	for range 1000 {
		if pool, _, _ := table.Lookup(context.Background(), "mistral"); pool.ID != "p4" {
			t.Fatal("a pool with no ready endpoint took traffic")
		}
	}
}

func TestDefault(t *testing.T) {
	cases := []struct {
		name  string
		ready []string
		pool  datalayer.PoolID
		ok    bool
	}{
		{"one ready pool", []string{"e1"}, "p1", true},
		{"two ready pools", []string{"e1", "e2"}, "", false},
		{"none ready", nil, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			table, store := newTable(t)
			for _, id := range []string{"e1", "e2"} {
				ep, _ := store.Endpoint(datalayer.EndpointID(id))
				spec := datalayer.EndpointSpec{ID: ep.ID(), Pool: ep.Pool(), Address: ep.Address()}
				for _, r := range c.ready {
					spec.Ready = spec.Ready || r == id
				}
				store.UpsertEndpoint(spec)
			}
			pool, model, ok := table.Default(context.Background())
			if ok != c.ok || pool.ID != c.pool {
				t.Fatalf("Default = %s %q %v, want %s %v", pool.ID, model, ok, c.pool, c.ok)
			}
			if ok && model != pool.Model {
				t.Fatalf("Default model %q, want the pool's base model %q", model, pool.Model)
			}
		})
	}
	table, store := newTable(t)
	store.SetAttribute("e2", datalayer.CollectorHealth, &datalayer.HealthState{Healthy: false})
	if pool, _, ok := table.Default(context.Background()); !ok || pool.ID != "p1" {
		t.Fatal("an unhealthy endpoint counted as ready")
	}
}
