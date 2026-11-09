// Package assumed counts the requests and tokens this gateway has sent to each endpoint and not yet seen complete
package assumed

import (
	"context"
	"sync"

	"github.com/nickheyer/nebu/pkg/datalayer"
	"github.com/nickheyer/nebu/pkg/scheduling/framework"
	"github.com/nickheyer/nebu/pkg/scheduling/plugins"
	"github.com/nickheyer/nebu/pkg/scheduling/registry"
)

// Plugin type name
const AssumedLoadType = "assumed-load"

// Requests and their tokens counted on one endpoint
type counter struct {
	mu     sync.Mutex
	n      int
	tokens int
}

// One request's hold on one endpoint
type claim struct {
	id   datalayer.EndpointID
	cost int
}

// The counts of every endpoint of one store, shared by every plugin built over that store
type ledger struct {
	store datalayer.Store

	mu       sync.Mutex
	counters map[datalayer.EndpointID]*counter
	requests map[string][]claim
}

// Ledgers by store, so two schedulers over one store reserve against the same counts
var ledgers sync.Map

func ledgerFor(store datalayer.Store) *ledger {
	if v, ok := ledgers.Load(store); ok {
		return v.(*ledger)
	}
	v, _ := ledgers.LoadOrStore(store, &ledger{store: store, counters: map[datalayer.EndpointID]*counter{}, requests: map[string][]claim{}})
	return v.(*ledger)
}

// Reserves room on the picked endpoint inside the pick, counts every routed head, and releases on completion
type AssumedLoad struct {
	plugins.Named
	*ledger
}

func New(store datalayer.Store) *AssumedLoad {
	return &AssumedLoad{Named: plugins.NewNamed(AssumedLoadType), ledger: ledgerFor(store)}
}

// Counts the request on the endpoint when it is under its request capacity and its KV tokens, false when not
func (a *AssumedLoad) Reserve(_ context.Context, _ *framework.CycleState, req *framework.Request, ep datalayer.Endpoint) bool {
	cost := plugins.Cost(req)
	capacity, _ := datalayer.Attribute[int](ep, datalayer.CollectorCapacity)
	kv, hasKV := plugins.KVTokens(ep)
	c := a.counter(ep.ID())
	c.mu.Lock()
	if capacity > 0 && c.n >= capacity || hasKV && c.tokens > 0 && c.tokens+cost > kv {
		c.mu.Unlock()
		return false
	}
	c.n++
	c.tokens += cost
	a.write(ep.ID(), c)
	c.mu.Unlock()
	a.hold(req.ID, claim{ep.ID(), cost})
	return true
}

// Gives back one hold of the request on the endpoint
func (a *AssumedLoad) Release(_ context.Context, _ *framework.CycleState, req *framework.Request, ep datalayer.Endpoint) {
	a.mu.Lock()
	claims := a.requests[req.ID]
	var found *claim
	for i := range claims {
		if claims[i].id == ep.ID() {
			found = &claims[i]
			a.requests[req.ID] = append(claims[:i:i], claims[i+1:]...)
			break
		}
	}
	if len(a.requests[req.ID]) == 0 {
		delete(a.requests, req.ID)
	}
	a.mu.Unlock()
	if found != nil {
		a.add(found.id, -1, -found.cost)
	}
}

// Counts the request on the head of every routed profile the pick did not already reserve
func (a *AssumedLoad) PreRequest(_ context.Context, req *framework.Request, res *framework.Result) {
	heads := plugins.ResultHeads(res)
	if len(heads) == 0 {
		return
	}
	cost := plugins.Cost(req)
	a.mu.Lock()
	held := a.requests[req.ID]
	var fresh []claim
	for _, ep := range heads {
		if !holds(held, ep.ID()) {
			fresh = append(fresh, claim{ep.ID(), cost})
		}
	}
	a.requests[req.ID] = append(held, fresh...)
	a.mu.Unlock()
	for _, c := range fresh {
		a.add(c.id, 1, c.cost)
	}
}

// Moves the primary count to the endpoint that answered when the request fell back from the one picked
func (a *AssumedLoad) ResponseReceived(_ context.Context, req *framework.Request, resp *framework.Response) {
	if resp == nil || resp.Endpoint == nil {
		return
	}
	served := resp.Endpoint.ID()
	a.mu.Lock()
	claims := a.requests[req.ID]
	if len(claims) == 0 || claims[0].id == served || holds(claims, served) {
		a.mu.Unlock()
		return
	}
	from := claims[0]
	claims[0].id = served
	a.mu.Unlock()
	a.add(from.id, -1, -from.cost)
	a.add(served, 1, from.cost)
}

// Releases every count the request holds
func (a *AssumedLoad) ResponseComplete(_ context.Context, req *framework.Request, _ *framework.Response) {
	a.mu.Lock()
	claims, ok := a.requests[req.ID]
	delete(a.requests, req.ID)
	a.mu.Unlock()
	if !ok {
		return
	}
	for _, c := range claims {
		a.add(c.id, -1, -c.cost)
	}
}

// The current request count of an endpoint
func (a *AssumedLoad) Count(id datalayer.EndpointID) int {
	c := a.counter(id)
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}

// The current token count of an endpoint
func (a *AssumedLoad) Tokens(id datalayer.EndpointID) int {
	c := a.counter(id)
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.tokens
}

func holds(claims []claim, id datalayer.EndpointID) bool {
	for _, c := range claims {
		if c.id == id {
			return true
		}
	}
	return false
}

func (l *ledger) hold(req string, c claim) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.requests[req] = append(l.requests[req], c)
}

// Changes one endpoint's counts and writes them through under its lock so the store never ends on a stale value
func (l *ledger) add(id datalayer.EndpointID, requests, tokens int) {
	c := l.counter(id)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.n += requests
	c.tokens += tokens
	l.write(id, c)
}

// Writes the counter's values to the store, with the counter lock held
func (l *ledger) write(id datalayer.EndpointID, c *counter) {
	l.store.SetAttribute(id, datalayer.CollectorAssumed, c.n)
	l.store.SetAttribute(id, datalayer.CollectorAssumedTokens, c.tokens)
}

func (l *ledger) counter(id datalayer.EndpointID) *counter {
	l.mu.Lock()
	defer l.mu.Unlock()
	c, ok := l.counters[id]
	if !ok {
		c = &counter{}
		l.counters[id] = c
	}
	return c
}

// Registers the plugin, which takes no parameters and needs the store
func init() {
	registry.Register(AssumedLoadType, func(name string, params map[string]any, deps registry.Deps) (framework.Plugin, error) {
		if err := registry.Decode(params, &struct{}{}); err != nil {
			return nil, err
		}
		if deps.Store == nil {
			return nil, registry.Missing("Store")
		}
		a := New(deps.Store)
		a.SetName(name)
		return a, nil
	})
}
