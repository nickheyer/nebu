package gateway

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nickheyer/nebu/internal/instances"
	"github.com/nickheyer/nebu/internal/pools"
	"github.com/nickheyer/nebu/pkg/datalayer"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/scheduling/config"
	"github.com/nickheyer/nebu/pkg/scheduling/framework"
	"github.com/nickheyer/nebu/pkg/scheduling/plugins/profile"
	"github.com/nickheyer/nebu/pkg/scheduling/plugins/scorer"
	"github.com/nickheyer/nebu/pkg/scheduling/registry"
	"github.com/nickheyer/nebu/pkg/scheduling/remote"
)

// The scheduler configuration media pools run, hosted beside the pools manager's built in ones
const ConfigMedia = "media"

// One job at a time per media endpoint reserved inside the pick, the least loaded endpoint first and the next as fallback
const mediaConfig = `
plugins:
- type: ready-filter
  name: ready
- type: subset-filter
  name: subset
- type: capacity-filter
  name: capacity
- type: assumed-load-scorer
  name: assumed
- type: max-score-picker
  name: picker
  parameters:
    count: 2
- type: assumed-load
  name: assumed-load
- type: single-profile-handler
  name: handler
  parameters:
    profile: media
profiles:
- name: media
  plugins:
  - pluginRef: ready
  - pluginRef: subset
  - pluginRef: capacity
  - pluginRef: assumed
  - pluginRef: picker
profileHandler:
  pluginRef: handler
`

// Configurations always hosted by name, a stored configuration of the same name replacing the built in one
var builtins = map[string]string{
	pools.SchedulerDefault:   config.DefaultConfig,
	pools.SchedulerNoMetrics: config.DefaultNoMetricsConfig,
	pools.SchedulerPD:        config.DefaultPDConfig,
	pools.SchedulerReplicas:  config.DefaultReplicasConfig,
	ConfigMedia:              mediaConfig,
}

// The cycle state key a hosted scheduler stamps its build under, for lifecycle routing
const builtKey = "nebu.gateway.scheduler"

// Lifecycle plugin types that pin a prompt to an endpoint, skipped for ghost requests
var pinning = map[string]bool{scorer.PrefixCacheScorerType: true, scorer.SessionAffinityScorerType: true}

// One build of a configuration, its scheduler and the lifecycle plugins only its requests run
type built struct {
	name  string
	yaml  string
	sched framework.Scheduler
	life  []framework.Plugin
	// The handoff plugin type its pd profile handler names, empty when it names none
	handoff string
}

// A named scheduler whose build is swapped atomically when its configuration changes
type hosted struct {
	g    *Gateway
	name string
	cur  atomic.Pointer[built]
}

// Schedules the endpoints serving the request's target with the current build and stamps it on the cycle state
func (h *hosted) Schedule(ctx context.Context, req *framework.Request, candidates []datalayer.Endpoint) (*framework.Result, error) {
	candidates = h.g.serving(req, candidates)
	if len(candidates) == 0 {
		return nil, fmt.Errorf("%w: no endpoint serves %s", framework.ErrNoEndpoints, req.TargetModel)
	}
	b := h.cur.Load()
	res, err := b.sched.Schedule(ctx, req, candidates)
	if err != nil {
		return nil, err
	}
	if res.State == nil {
		res.State = framework.NewCycleState()
	}
	res.State.Write(builtKey, b)
	return res, nil
}

func (h *hosted) Profiles() map[string]*framework.Profile { return h.cur.Load().sched.Profiles() }

// Every hosted scheduler, the default selector, and the router feeding each build's lifecycle plugins
type hosting struct {
	g *Gateway
	// Serializes configuration loads, never taken on the request path
	mu       sync.Mutex
	set      atomic.Pointer[map[string]*hosted]
	selector *selector
	router   *lifecycleRouter
}

func newHosting(g *Gateway) *hosting {
	h := &hosting{g: g, router: &lifecycleRouter{}}
	h.selector = &selector{h: h}
	empty := map[string]*hosted{}
	h.set.Store(&empty)
	return h
}

// The hosted schedulers by name for the director, the generic defaults choosing media and disaggregated builds when called for
func (h *hosting) byName() map[string]framework.Scheduler {
	out := map[string]framework.Scheduler{}
	for name, hs := range *h.set.Load() {
		out[name] = hs
		if name == pools.SchedulerDefault || name == pools.SchedulerNoMetrics {
			out[name] = &selector{h: h, name: name}
		}
	}
	return out
}

func (h *hosting) get(name string) (*hosted, bool) {
	hs, ok := (*h.set.Load())[name]
	return hs, ok
}

// Builds every built in and stored configuration, failing when a built in one does not build
func (h *hosting) load(stored []*v1.SchedulerConfig) error {
	_, err := h.reload(stored)
	return err
}

// Rebuilds changed configurations in place, keeps a working build when its update fails, and reports a changed name set
func (h *hosting) reload(stored []*v1.SchedulerConfig) (bool, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	want := maps.Clone(builtins)
	for _, c := range stored {
		want[c.GetName()] = c.GetSpec().GetYaml()
	}
	cur := *h.set.Load()
	next := make(map[string]*hosted, len(want))
	var errs []error
	for name, yaml := range want {
		hs, had := cur[name]
		if had && hs.cur.Load().yaml == yaml {
			next[name] = hs
			continue
		}
		b, err := h.build(name, yaml)
		switch {
		case err != nil && builtins[name] == yaml:
			return false, fmt.Errorf("built in scheduler configuration %s: %w", name, err)
		case err != nil && had:
			h.g.log.Error("scheduler configuration not swapped, the previous build keeps serving", "config", name, "err", err)
			next[name] = hs
			errs = append(errs, err)
			continue
		case err != nil:
			h.g.log.Error("scheduler configuration not hosted, pools naming it are unavailable", "config", name, "err", err)
			errs = append(errs, err)
			continue
		}
		if !had {
			hs = &hosted{g: h.g, name: name}
		}
		hs.cur.Store(b)
		next[name] = hs
	}
	changed := len(next) != len(cur)
	for name := range next {
		if _, ok := cur[name]; !ok {
			changed = true
		}
	}
	h.set.Store(&next)
	return changed, errors.Join(errs...)
}

// Loads a configuration against the gateway's store, index and predictor, a remote one wrapped in a fallback
func (h *hosting) build(name, yaml string) (*built, error) {
	c, err := config.Parse([]byte(yaml))
	if err != nil {
		return nil, err
	}
	g := h.g
	deps := g.SchedulerDeps()
	deps.Logger = g.log.With("scheduler", name)
	sched, life, err := c.Build(deps)
	if err != nil {
		return nil, err
	}
	for _, p := range sched.Profiles() {
		p.Observer = g.metrics
	}
	handoff := ""
	if h, ok := framework.HandlerOf(sched); ok {
		if pd, ok := h.(*profile.PDProfileHandler); ok {
			handoff = pd.Handoff()
		}
	}
	if c.Remote != nil {
		hc, err := g.remoteHTTP(c.Remote.URL)
		if err != nil {
			return nil, err
		}
		client, err := remote.NewClient(remote.ResolveURL(c.Remote.URL), hc, time.Duration(c.Remote.Timeout))
		if err != nil {
			return nil, err
		}
		fallback, err := remote.NewFallback(counted{next: client, g: g}, sched, func(err error) {
			g.log.Warn("remote scheduler unavailable, scheduled in process", "config", name, "err", err)
		})
		if err != nil {
			return nil, err
		}
		sched = fallback
	}
	sched = &importAware{g: g, name: name, local: sched}
	return &built{name: name, yaml: yaml, sched: sched, life: life, handoff: handoff}, nil
}

// Fails readiness until at least one scheduler is hosted
func (h *hosting) ready(context.Context) error {
	if len(*h.set.Load()) == 0 {
		return errors.New("no scheduler is built")
	}
	return nil
}

// The configuration a pool runs, media or disaggregated when called for, else the named or metric fitting default
func (h *hosting) defaultFor(candidates []datalayer.Endpoint, named string) string {
	if len(candidates) == 0 {
		return firstOf(named, pools.SchedulerDefault)
	}
	pool, ok := h.g.store.Pool(candidates[0].Pool())
	if ok && h.g.runtimes.API(pool.Runtime) == v1.ApiFlavor_API_FLAVOR_SDCPP {
		return ConfigMedia
	}
	if slices.ContainsFunc(candidates, func(ep datalayer.Endpoint) bool {
		r := ep.Labels()[datalayer.LabelRole]
		return r == datalayer.RolePrefill || r == datalayer.RoleDecode
	}) {
		return pools.SchedulerPD
	}
	switch {
	case named != "":
		return named
	case ok && !pool.Metrics.Complete():
		return pools.SchedulerNoMetrics
	}
	return pools.SchedulerDefault
}

// Counts every remote scheduler failure under the pool before the fallback schedules in process
type counted struct {
	next framework.Scheduler
	g    *Gateway
}

func (c counted) Schedule(ctx context.Context, req *framework.Request, candidates []datalayer.Endpoint) (*framework.Result, error) {
	res, err := c.next.Schedule(ctx, req, candidates)
	if err != nil && len(candidates) > 0 {
		c.g.metrics.SchedulerFallback(candidates[0].Pool(), "remote")
	}
	return res, err
}

func (c counted) Profiles() map[string]*framework.Profile { return c.next.Profiles() }

// Schedules with the configuration a pool's runtime and endpoints call for, the generic default it names otherwise
type selector struct {
	h *hosting
	// The generic default the pool names, empty for a pool naming none
	name string
}

func (s *selector) Schedule(ctx context.Context, req *framework.Request, candidates []datalayer.Endpoint) (*framework.Result, error) {
	name := s.h.defaultFor(candidates, s.name)
	hs, ok := s.h.get(name)
	if !ok {
		return nil, fmt.Errorf("%w: built in configuration %s is not hosted", framework.ErrSchedulerUnavailable, name)
	}
	return hs.Schedule(ctx, req, candidates)
}

// The profiles of the configuration the pool names, the default's for a pool naming none
func (s *selector) Profiles() map[string]*framework.Profile {
	if hs, ok := s.h.get(firstOf(s.name, pools.SchedulerDefault)); ok {
		return hs.Profiles()
	}
	return map[string]*framework.Profile{}
}

// Hands each request's lifecycle hooks to the plugins of the build that scheduled it
type lifecycleRouter struct {
	active sync.Map
}

func (r *lifecycleRouter) TypedName() framework.TypedName {
	return framework.TypedName{Type: "scheduler-lifecycle", Name: "gateway"}
}

// Runs one hook of an inner plugin, a panic logged under its name rather than ending the others
func (r *lifecycleRouter) call(ctx context.Context, p framework.Plugin, fn func()) {
	defer func() {
		if v := recover(); v != nil {
			if x := exchangeOf(ctx); x != nil {
				x.g.log.Error("scheduler lifecycle hook panicked", "plugin", p.TypedName().String(), "panic", v)
			}
		}
	}()
	fn()
}

// Whether the request asked to leave no pin of its prompt behind
func ghostly(ctx context.Context) bool {
	x := exchangeOf(ctx)
	return x != nil && x.modes.ghost
}

func (r *lifecycleRouter) PreRequest(ctx context.Context, req *framework.Request, res *framework.Result) {
	if res == nil || res.State == nil {
		return
	}
	b, ok := framework.ReadState[*built](res.State, builtKey)
	if !ok {
		return
	}
	r.active.Store(req.ID, b)
	ghost := ghostly(ctx)
	for _, p := range b.life {
		if h, ok := p.(framework.PreRequestPlugin); ok && !(ghost && pinning[p.TypedName().Type]) {
			r.call(ctx, p, func() { h.PreRequest(ctx, req, res) })
		}
	}
}

func (r *lifecycleRouter) ResponseReceived(ctx context.Context, req *framework.Request, resp *framework.Response) {
	v, ok := r.active.Load(req.ID)
	if !ok {
		return
	}
	for _, p := range v.(*built).life {
		if h, ok := p.(framework.ResponseReceivedPlugin); ok {
			r.call(ctx, p, func() { h.ResponseReceived(ctx, req, resp) })
		}
	}
}

func (r *lifecycleRouter) ResponseStreaming(ctx context.Context, req *framework.Request, resp *framework.Response, chunk []byte) {
	v, ok := r.active.Load(req.ID)
	if !ok {
		return
	}
	for _, p := range v.(*built).life {
		if h, ok := p.(framework.ResponseStreamingPlugin); ok {
			r.call(ctx, p, func() { h.ResponseStreaming(ctx, req, resp, chunk) })
		}
	}
}

func (r *lifecycleRouter) ResponseComplete(ctx context.Context, req *framework.Request, resp *framework.Response) {
	v, ok := r.active.LoadAndDelete(req.ID)
	if !ok {
		return
	}
	ghost := ghostly(ctx)
	for _, p := range v.(*built).life {
		if h, ok := p.(framework.ResponseCompletePlugin); ok && !(ghost && pinning[p.TypedName().Type]) {
			r.call(ctx, p, func() { h.ResponseComplete(ctx, req, resp) })
		}
	}
}

// Rebuilds scheduler configurations whenever the pools manager publishes one, rebuilding the director when names change
func (g *Gateway) followConfigs(ctx context.Context) {
	sub := g.events.Subscribe(ctx, []v1.EventKind{v1.EventKind_EVENT_KIND_SCHEDULER_CONFIG})
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-sub.Events():
			g.reloadSchedulers(ev)
		}
	}
}

// Reads every configuration the manager holds with the event's own record over it and swaps in what changed
func (g *Gateway) reloadSchedulers(ev *v1.Event) {
	configs := g.manager.SchedulerConfigs()
	if c := ev.GetSchedulerConfig(); c.GetName() != "" && (c.GetSpec().GetYaml() != "" || ev.GetAction() == v1.EventAction_EVENT_ACTION_DELETED) {
		configs = slices.DeleteFunc(configs, func(x *v1.SchedulerConfig) bool { return x.GetName() == c.GetName() })
		if ev.GetAction() != v1.EventAction_EVENT_ACTION_DELETED {
			configs = append(configs, c)
		}
	}
	changed, err := g.hosting.reload(configs)
	if err != nil {
		g.log.Warn("scheduler configurations reloaded with failures", "err", err)
	}
	if !changed {
		return
	}
	if err := g.rebuildDirector(); err != nil {
		g.log.Error("director not rebuilt after scheduler configurations changed", "err", err)
	}
}

// The store, shared prefix index, latency predictor, logger, clock and metrics observer every scheduler configuration loads against
func (g *Gateway) SchedulerDeps() registry.Deps {
	return registry.Deps{Store: g.store, Indexer: g.index, Predictor: g.predictor, Logger: g.log, Clock: g.clock, Observer: g.metrics}
}

// Endpoints labelled with the target name or adapter, every endpoint when none is labelled or the pool's model names none
func (g *Gateway) serving(req *framework.Request, candidates []datalayer.Endpoint) []datalayer.Endpoint {
	if len(candidates) == 0 {
		return candidates
	}
	labeled := false
	out := make([]datalayer.Endpoint, 0, len(candidates))
	for _, ep := range candidates {
		l := ep.Labels()
		served, has := l[instances.LabelServed]
		labeled = labeled || has
		if has && served == req.TargetModel || req.Adapter != "" && l[datalayer.LabelAdapter] == req.Adapter {
			out = append(out, ep)
		}
	}
	if !labeled {
		return candidates
	}
	if len(out) == 0 {
		if pool, ok := g.store.Pool(candidates[0].Pool()); ok && req.TargetModel == pool.Model {
			return candidates
		}
	}
	return out
}
