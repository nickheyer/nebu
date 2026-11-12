package requestcontrol

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"math/rand/v2"
	"slices"
	"sort"
	"sync"
	"sync/atomic"

	"github.com/nickheyer/nebu/pkg/datalayer"
	"time"
)

// A rule matching a client model name and splitting it over weighted targets
type RewriteRule struct {
	// Exact client model name, empty matching every request
	Match string
	// Weighted targets, one chosen per request by weight
	Targets []RewriteTarget
	// Oldest wins among equal matches across rule sets
	CreatedAt time.Time
	// Position inside its rule set, lower first among equal matches and ages
	Order int
}

// One destination of a rewrite rule
type RewriteTarget struct {
	Model  string
	Weight int
}

// Rewrite rules and the served model catalog, written by the owner and read lock free
type RuleTable struct {
	store datalayer.Store
	mu    sync.Mutex
	snap  atomic.Pointer[tableSnapshot]
}

// One immutable view of every rule set and the catalog
type tableSnapshot struct {
	sets     map[string][]RewriteRule
	exact    map[string]*compiledRule
	catchAll *compiledRule
	served   map[string][]servedBy
	pools    map[datalayer.PoolID][]string
}

// A rule with its cumulative weights for the weighted pick
type compiledRule struct {
	rule  RewriteRule
	set   string
	cum   []int
	total int
}

// A pool serving a name and whether the name is an adapter there
type servedBy struct {
	pool    datalayer.PoolID
	adapter bool
}

// An empty table reading pool specs and readiness from the store
func NewRuleTable(store datalayer.Store) *RuleTable {
	t := &RuleTable{store: store}
	t.snap.Store(&tableSnapshot{
		sets:   map[string][]RewriteRule{},
		exact:  map[string]*compiledRule{},
		served: map[string][]servedBy{},
		pools:  map[datalayer.PoolID][]string{},
	})
	return t
}

// Replaces a named rule set, all zero weights splitting evenly and negative weights or empty targets refused
func (t *RuleTable) SetRules(set string, rules []RewriteRule) error {
	for i, r := range rules {
		if len(r.Targets) == 0 {
			return fmt.Errorf("rule set %s rule %d has no targets", set, i)
		}
		for _, tg := range r.Targets {
			if tg.Model == "" || tg.Weight < 0 {
				return fmt.Errorf("rule set %s rule %d has target %q with weight %d", set, i, tg.Model, tg.Weight)
			}
		}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	next := t.copySnapshot()
	next.sets[set] = slices.Clone(rules)
	next.compile()
	t.snap.Store(next)
	return nil
}

// Removes a named rule set
func (t *RuleTable) DeleteRules(set string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	next := t.copySnapshot()
	delete(next.sets, set)
	next.compile()
	t.snap.Store(next)
}

// Records the models and adapters a pool serves, replacing what it served before
func (t *RuleTable) Put(pool datalayer.PoolID, models, adapters []string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	next := t.copySnapshot()
	next.unserve(pool)
	flags := map[string]bool{}
	for _, m := range models {
		if _, ok := flags[m]; !ok {
			flags[m] = false
		}
	}
	for _, a := range adapters {
		flags[a] = true
	}
	names := slices.Sorted(maps.Keys(flags))
	for _, name := range names {
		next.served[name] = append(next.served[name], servedBy{pool: pool, adapter: flags[name]})
		slices.SortFunc(next.served[name], func(a, b servedBy) int { return cmp.Compare(a.pool, b.pool) })
	}
	next.pools[pool] = names
	t.snap.Store(next)
}

// Forgets every name a pool serves
func (t *RuleTable) Delete(pool datalayer.PoolID) {
	t.mu.Lock()
	defer t.mu.Unlock()
	next := t.copySnapshot()
	next.unserve(pool)
	t.snap.Store(next)
}

// A copy of the current snapshot whose maps the writer may change
func (t *RuleTable) copySnapshot() *tableSnapshot {
	cur := t.snap.Load()
	next := &tableSnapshot{
		sets:     maps.Clone(cur.sets),
		exact:    cur.exact,
		catchAll: cur.catchAll,
		served:   make(map[string][]servedBy, len(cur.served)),
		pools:    maps.Clone(cur.pools),
	}
	for name, by := range cur.served {
		next.served[name] = slices.Clone(by)
	}
	return next
}

func (s *tableSnapshot) unserve(pool datalayer.PoolID) {
	for _, name := range s.pools[pool] {
		by := slices.DeleteFunc(s.served[name], func(b servedBy) bool { return b.pool == pool })
		if len(by) == 0 {
			delete(s.served, name)
		} else {
			s.served[name] = by
		}
	}
	delete(s.pools, pool)
}

// Keeps the winning rule per match, exact over catch all by key, then oldest, then lowest order, then set name
func (s *tableSnapshot) compile() {
	s.exact = map[string]*compiledRule{}
	s.catchAll = nil
	for set, rules := range s.sets {
		for _, r := range rules {
			c := newCompiled(set, r)
			if r.Match == "" {
				if s.catchAll == nil || c.before(s.catchAll) {
					s.catchAll = c
				}
				continue
			}
			if cur, ok := s.exact[r.Match]; !ok || c.before(cur) {
				s.exact[r.Match] = c
			}
		}
	}
}

func newCompiled(set string, r RewriteRule) *compiledRule {
	c := &compiledRule{rule: r, set: set, cum: make([]int, len(r.Targets))}
	even := true
	for _, tg := range r.Targets {
		even = even && tg.Weight == 0
	}
	for i, tg := range r.Targets {
		w := tg.Weight
		if even {
			w = 1
		}
		c.total += w
		c.cum[i] = c.total
	}
	return c
}

// Whether this rule outranks another matching the same requests
func (c *compiledRule) before(o *compiledRule) bool {
	if !c.rule.CreatedAt.Equal(o.rule.CreatedAt) {
		return c.rule.CreatedAt.Before(o.rule.CreatedAt)
	}
	if c.rule.Order != o.rule.Order {
		return c.rule.Order < o.rule.Order
	}
	return c.set < o.set
}

// One target chosen with probability proportional to its weight
func (c *compiledRule) pick() string {
	x := rand.IntN(c.total)
	return c.rule.Targets[sort.SearchInts(c.cum, x+1)].Model
}

func (t *RuleTable) Resolve(ctx context.Context, clientModel string) (datalayer.Pool, string, error) {
	s := t.snap.Load()
	target := clientModel
	if r, ok := s.exact[clientModel]; ok {
		target = r.pick()
	} else if s.catchAll != nil {
		target = s.catchAll.pick()
	}
	if target == "" {
		return datalayer.Pool{}, "", fmt.Errorf("%w: no catch all rule names one", ErrNoModel)
	}
	pool, _, err := t.lookup(s, target)
	return pool, target, err
}

func (t *RuleTable) Lookup(ctx context.Context, model string) (datalayer.Pool, string, error) {
	return t.lookup(t.snap.Load(), model)
}

// Every endpoint of the pool, with the adapter the model names there
func (t *RuleTable) Scope(_ string, pool datalayer.PoolID, model string) Scope {
	for _, b := range t.snap.Load().served[model] {
		if b.pool == pool && b.adapter {
			return Scope{Adapter: model}
		}
	}
	return Scope{}
}

// The pool serving a name, several pools sharing it by their ready endpoint counts
func (t *RuleTable) lookup(s *tableSnapshot, model string) (datalayer.Pool, string, error) {
	by := s.served[model]
	var pools []datalayer.Pool
	var adapters []bool
	for _, b := range by {
		if p, ok := t.store.Pool(b.pool); ok {
			pools = append(pools, p)
			adapters = append(adapters, b.adapter)
		}
	}
	if len(pools) == 0 {
		return datalayer.Pool{}, "", fmt.Errorf("%w: %q", ErrUnknownModel, model)
	}
	i := 0
	if len(pools) > 1 {
		i = t.byReadiness(pools)
	}
	adapter := ""
	if adapters[i] {
		adapter = model
	}
	return pools[i], adapter, nil
}

// An index into pools chosen with probability proportional to ready endpoints, the first pool when none is ready
func (t *RuleTable) byReadiness(pools []datalayer.Pool) int {
	cum := make([]int, len(pools))
	total := 0
	for i, p := range pools {
		total += len(t.store.Endpoints(p.ID, datalayer.ReadyAndHealthy))
		cum[i] = total
	}
	if total == 0 {
		return 0
	}
	return sort.SearchInts(cum, rand.IntN(total)+1)
}

func (t *RuleTable) Default(ctx context.Context) (datalayer.Pool, string, bool) {
	var ready []datalayer.Pool
	for _, p := range t.store.Pools() {
		if len(t.store.Endpoints(p.ID, datalayer.ReadyAndHealthy)) > 0 {
			if ready = append(ready, p); len(ready) > 1 {
				return datalayer.Pool{}, "", false
			}
		}
	}
	if len(ready) != 1 || ready[0].Model == "" {
		return datalayer.Pool{}, "", false
	}
	return ready[0], ready[0].Model, true
}
