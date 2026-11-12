package requestcontrol

import (
	"cmp"
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"math/rand/v2"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nickheyer/nebu/pkg/bodyparse"
	"github.com/nickheyer/nebu/pkg/datalayer"
	"github.com/nickheyer/nebu/pkg/flowcontrol"
	"github.com/nickheyer/nebu/pkg/scheduling/framework"
	"github.com/nickheyer/nebu/pkg/telemetry"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
)

// Reads the current time
type Clock interface {
	Now() time.Time
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

// Receives the outcome of every request the director handles
type Observer interface {
	// A request was admitted and routed, took measured from the start of the decision
	Decided(in *Inbound, dec *Decision, took time.Duration)
	// A request was refused before dispatch
	Refused(in *Inbound, err error, took time.Duration)
	// A routed request finished, once per decision
	Completed(dec *Decision, resp *framework.Response)
}

type nopObserver struct{}

func (nopObserver) Decided(*Inbound, *Decision, time.Duration) {}
func (nopObserver) Refused(*Inbound, error, time.Duration)     {}
func (nopObserver) Completed(*Decision, *framework.Response)   {}

type noTopology struct{}

func (noTopology) Batches(Stage) [][]framework.Plugin { return nil }

// What a director runs on, Topology, Logger, Clock and Observer optional
type Deps struct {
	Store    datalayer.Store
	Resolver Resolver
	// Asked for endpoint samples while a decision is pending and while a request is in flight
	Sampler datalayer.Sampler
	// The scheduler for pools naming no scheduler config
	Scheduler framework.Scheduler
	// Schedulers by the config name a pool's SchedulerConfig selects
	Schedulers map[string]framework.Scheduler
	Flow       flowcontrol.Controller
	Topology   Topology
	// Scheduler hooks, each implementing at least one framework hook interface
	Lifecycle []framework.Plugin
	Logger    *slog.Logger
	Clock     Clock
	Observer  Observer
	// Output tokens expected of a request whose body sets no bound
	DefaultOutputTokens int
}

// The profile a fail open result names
const FailOpenProfile = "fail-open"

const (
	anonymous     = "anonymous"
	charsPerToken = 4
)

// Per decision state making completion run once and tracking the endpoint the request is counted on
type lifecycle struct {
	once    sync.Once
	mu      sync.Mutex
	counted datalayer.EndpointID
}

// Requests this director has in flight per endpoint, each endpoint sampled at the working pace while it has any
type inflight struct {
	mu      sync.Mutex
	n       map[datalayer.EndpointID]int
	sampler datalayer.Sampler
	serving map[datalayer.EndpointID]func()
}

func (f *inflight) add(id datalayer.EndpointID, delta int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if v := f.n[id] + delta; v > 0 {
		f.n[id] = v
		if f.serving[id] == nil {
			f.serving[id] = f.sampler.Serving(id)
		}
	} else {
		delete(f.n, id)
		if release := f.serving[id]; release != nil {
			delete(f.serving, id)
			release()
		}
	}
}

func (f *inflight) get(id datalayer.EndpointID) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.n[id]
}

type director struct {
	store          datalayer.Store
	resolver       Resolver
	scheduler      framework.Scheduler
	schedulers     map[string]framework.Scheduler
	flow           flowcontrol.Controller
	topology       Topology
	preHooks       []framework.PreRequestPlugin
	receivedHooks  []framework.ResponseReceivedPlugin
	streamingHooks []framework.ResponseStreamingPlugin
	completeHooks  []framework.ResponseCompletePlugin
	log            *slog.Logger
	clock          Clock
	observer       Observer
	defaultOutput  int
	sampler        datalayer.Sampler
	loads          inflight
}

// Builds a director, refusing missing dependencies and lifecycle plugins that implement no hook
func NewDirector(deps Deps) (Director, error) {
	switch {
	case deps.Store == nil:
		return nil, errors.New("a director needs a store")
	case deps.Resolver == nil:
		return nil, errors.New("a director needs a resolver")
	case deps.Flow == nil:
		return nil, errors.New("a director needs a flow controller")
	case deps.Sampler == nil:
		return nil, errors.New("a director needs a sampler")
	case deps.Scheduler == nil && len(deps.Schedulers) == 0:
		return nil, errors.New("a director needs a scheduler")
	case deps.DefaultOutputTokens <= 0:
		return nil, errors.New("a director needs a positive default output token count")
	}
	d := &director{
		store:         deps.Store,
		resolver:      deps.Resolver,
		scheduler:     deps.Scheduler,
		schedulers:    deps.Schedulers,
		flow:          deps.Flow,
		topology:      deps.Topology,
		log:           deps.Logger,
		clock:         deps.Clock,
		observer:      deps.Observer,
		defaultOutput: deps.DefaultOutputTokens,
		sampler:       deps.Sampler,
		loads:         inflight{n: map[datalayer.EndpointID]int{}, sampler: deps.Sampler, serving: map[datalayer.EndpointID]func(){}},
	}
	if d.topology == nil {
		d.topology = noTopology{}
	}
	if d.log == nil {
		d.log = slog.New(slog.DiscardHandler)
	}
	if d.clock == nil {
		d.clock = systemClock{}
	}
	if d.observer == nil {
		d.observer = nopObserver{}
	}
	for _, p := range deps.Lifecycle {
		used := false
		if h, ok := p.(framework.PreRequestPlugin); ok {
			d.preHooks, used = append(d.preHooks, h), true
		}
		if h, ok := p.(framework.ResponseReceivedPlugin); ok {
			d.receivedHooks, used = append(d.receivedHooks, h), true
		}
		if h, ok := p.(framework.ResponseStreamingPlugin); ok {
			d.streamingHooks, used = append(d.streamingHooks, h), true
		}
		if h, ok := p.(framework.ResponseCompletePlugin); ok {
			d.completeHooks, used = append(d.completeHooks, h), true
		}
		if !used {
			return nil, fmt.Errorf("lifecycle plugin %s implements no scheduler hook", p.TypedName())
		}
	}
	return d, nil
}

func (d *director) Decide(ctx context.Context, in *Inbound) (dec *Decision, err error) {
	start := d.clock.Now()
	defer func() {
		if took := d.clock.Now().Sub(start); err != nil {
			d.observer.Refused(in, err, took)
		} else {
			d.observer.Decided(in, dec, took)
		}
	}()
	req, clientModel, err := d.buildRequest(in)
	if err != nil {
		return nil, err
	}
	pool, scope, err := d.resolve(ctx, in, clientModel, req)
	if err != nil {
		return nil, err
	}
	if in.Body, err = mutate(in.Body, req.TargetModel); err != nil {
		return nil, err
	}
	if err := d.preRequest(ctx, in, req); err != nil {
		return nil, err
	}
	if in.Body, err = mutate(in.Body, req.TargetModel); err != nil {
		return nil, err
	}
	if err := d.awaitWake(ctx, pool.ID, scope); err != nil {
		return nil, err
	}
	if !d.anyReady(pool.ID, scope) {
		return nil, fmt.Errorf("%w: pool %s has no ready endpoint serving %s", ErrNoReadyEndpoints, pool.ID, req.TargetModel)
	}
	permit, result, failedOpen, err := d.dispatch(ctx, in, pool, scope, req)
	if err != nil {
		return nil, err
	}
	for _, h := range d.preHooks {
		d.hook(h, func() { h.PreRequest(ctx, req, result) })
	}
	dec = &Decision{
		Pool:        pool,
		Endpoints:   slices.Clone(result.Endpoints()),
		TargetModel: req.TargetModel,
		Adapter:     req.Adapter,
		Body:        in.Body,
		Permit:      permit,
		Request:     req,
		Result:      result,
		FailedOpen:  failedOpen,
	}
	addrs := make([]string, len(dec.Endpoints))
	for i, ep := range dec.Endpoints {
		addrs[i] = ep.Address().String()
	}
	dec.Headers = map[string]string{
		HeaderDestination: strings.Join(addrs, ","),
		HeaderModelName:   req.TargetModel,
		HeaderRequestID:   req.ID,
	}
	dec.life.counted = dec.Endpoints[0].ID()
	d.loads.add(dec.life.counted, 1)
	if err := d.postDecision(ctx, in, dec); err != nil {
		d.finish(ctx, dec, &framework.Response{Endpoint: dec.Endpoints[0], Err: err})
		return nil, err
	}
	return dec, nil
}

// Builds the scheduler's request from headers and body, returning the client model it names
func (d *director) buildRequest(in *Inbound) (*framework.Request, string, error) {
	h := in.Headers
	obj, err := parseObjective(h)
	if err != nil {
		return nil, "", err
	}
	subset, subsetSet, err := parseSubset(h)
	if err != nil {
		return nil, "", err
	}
	req := &framework.Request{
		ID:         requestID(in),
		Headers:    flatten(h),
		Objective:  obj,
		FairnessID: cmp.Or(strings.TrimSpace(h.Get(HeaderFairnessID)), in.Identity, anonymous),
		Subset:     subset,
		SubsetSet:  subsetSet,
	}
	model := strings.TrimSpace(h.Get(HeaderModelName))
	maxTokens := 0
	if in.Body != nil {
		if model == "" {
			m, err := in.Body.Model()
			if err != nil && !errors.Is(err, bodyparse.ErrNoModel) {
				return nil, "", err
			}
			model = m
		}
		req.Prompt = in.Body.PromptText()
		req.PromptTokenIDs = in.Body.PromptTokenIDs()
		req.Streaming = in.Body.Stream()
		maxTokens = in.Body.MaxTokens()
	}
	req.PromptTokens = len(req.PromptTokenIDs)
	if req.PromptTokens == 0 {
		req.PromptTokens = (len(req.Prompt) + charsPerToken - 1) / charsPerToken
	}
	req.ExpectedOutputTokens = cmp.Or(maxTokens, d.defaultOutput)
	return req, model, nil
}

// Sets the target model and adapter and returns the pool and its scope, the rewrite header naming the target
func (d *director) resolve(ctx context.Context, in *Inbound, clientModel string, req *framework.Request) (datalayer.Pool, Scope, error) {
	if target := strings.TrimSpace(in.Headers.Get(HeaderModelRewrite)); target != "" {
		pool, adapter, err := d.resolver.Lookup(ctx, target)
		if err != nil {
			return datalayer.Pool{}, Scope{}, err
		}
		req.TargetModel, req.Adapter = target, adapter
		return pool, d.resolver.Scope("", pool.ID, target), nil
	}
	pool, target, err := d.resolver.Resolve(ctx, clientModel)
	if clientModel == "" && errors.Is(err, ErrNoModel) {
		pool, model, ok := d.resolver.Default(ctx)
		if !ok {
			return datalayer.Pool{}, Scope{}, ErrNoModel
		}
		req.TargetModel = model
		return pool, d.resolver.Scope("", pool.ID, model), nil
	}
	if err != nil {
		return datalayer.Pool{}, Scope{}, err
	}
	scope := d.resolver.Scope(clientModel, pool.ID, target)
	req.TargetModel, req.Adapter = target, scope.Adapter
	return pool, scope, nil
}

// The endpoints of the pool the scope takes, those failing keep left out
func (d *director) scoped(pool datalayer.PoolID, scope Scope, keep func(datalayer.Endpoint) bool) []datalayer.Endpoint {
	return d.store.Endpoints(pool, func(ep datalayer.Endpoint) bool { return keep(ep) && scope.serves(ep) })
}

// Every endpoint, ready or not
func anyEndpoint(datalayer.Endpoint) bool { return true }

// Whether an endpoint of the scope is ready and healthy, a request without one refused before admission
func (d *director) anyReady(pool datalayer.PoolID, scope Scope) bool {
	return len(d.scoped(pool, scope, datalayer.ReadyAndHealthy)) > 0
}

// The ready and healthy endpoints of the scope, the ones a pending decision keeps sampled
func (d *director) readyIDs(pool datalayer.PoolID, scope Scope) []datalayer.EndpointID {
	eps := d.scoped(pool, scope, datalayer.ReadyAndHealthy)
	ids := make([]datalayer.EndpointID, len(eps))
	for i, ep := range eps {
		ids[i] = ep.ID()
	}
	return ids
}

// How often a held request looks at its pool between store changes
const wakePoll = 100 * time.Millisecond

// Holds a woken request until its scope gains a ready endpoint, the pool goes, the wake lapses, or ctx ends
func (d *director) awaitWake(ctx context.Context, pool datalayer.PoolID, scope Scope) error {
	if !scope.waking() || d.anyReady(pool, scope) {
		return nil
	}
	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	changes := d.store.Watch(wctx)
	tick := time.NewTicker(wakePoll)
	defer tick.Stop()
	for {
		switch {
		case d.anyReady(pool, scope):
			return nil
		case ctx.Err() != nil:
			return fmt.Errorf("%w: pool %s gained no ready endpoint before the request timeout: %w", ErrNoReadyEndpoints, pool, ctx.Err())
		}
		if _, ok := d.store.Pool(pool); !ok {
			return fmt.Errorf("%w: pool %s went away while its owner was waking", ErrNoReadyEndpoints, pool)
		}
		if !scope.waking() && len(d.scoped(pool, scope, anyEndpoint)) == 0 {
			return fmt.Errorf("%w: pool %s woke nothing", ErrNoReadyEndpoints, pool)
		}
		select {
		case <-ctx.Done():
		case <-changes:
		case <-tick.C:
		}
	}
}

// Admits the request with scheduling as its placement, the permit granted by the pick that reserved its endpoint
func (d *director) dispatch(ctx context.Context, in *Inbound, pool datalayer.Pool, scope Scope, req *framework.Request) (flowcontrol.Permit, *framework.Result, bool, error) {
	var (
		result     *framework.Result
		failedOpen bool
	)
	// A sample no older than the working pace is waited for once, a queued request then following the paced ones
	d.sampler.Deciding(ctx, d.readyIDs(pool.ID, scope))()
	item := d.item(in, pool, req)
	item.Place = func() error {
		res, open, err := d.schedule(ctx, pool, scope, req)
		switch {
		case err == nil:
			result, failedOpen = res, open
			return nil
		case !errors.Is(err, framework.ErrSaturated):
			return err
		case d.full(pool.ID, scope):
			return fmt.Errorf("%w: %w", flowcontrol.ErrPoolFull, err)
		}
		return fmt.Errorf("%w: %w", flowcontrol.ErrNoRoom, err)
	}
	permit, err := d.flow.Admit(ctx, item)
	if err != nil {
		return nil, nil, false, admitError(err)
	}
	return permit, result, failedOpen, nil
}

// Whether every ready endpoint of the scope holds as many requests as it runs at once
func (d *director) full(pool datalayer.PoolID, scope Scope) bool {
	eps := d.scoped(pool, scope, datalayer.ReadyAndHealthy)
	for _, ep := range eps {
		n, _ := datalayer.Attribute[int](ep, datalayer.CollectorAssumed)
		if capacity, _ := datalayer.Attribute[int](ep, datalayer.CollectorCapacity); capacity <= 0 || n < capacity {
			return false
		}
	}
	return len(eps) > 0
}

func (d *director) item(in *Inbound, pool datalayer.Pool, req *framework.Request) *flowcontrol.Item {
	return &flowcontrol.Item{
		ID:         req.ID,
		Flow:       flowcontrol.FlowKey{FairnessID: req.FairnessID, Priority: req.Objective.Priority},
		Pool:       pool.ID,
		Priority:   req.Objective.Priority,
		Cost:       req.PromptTokens + req.ExpectedOutputTokens,
		EnqueuedAt: d.clock.Now(),
		Route:      in.Route,
		RouteLimit: in.MaxInFlight,
	}
}

// Maps every policy refusal from flow control, shed, displaced, full, timed out or over quota, to ErrShed
func admitError(err error) error {
	if errors.Is(err, flowcontrol.ErrShed) || errors.Is(err, flowcontrol.ErrQueueFull) || errors.Is(err, flowcontrol.ErrQueueTimeout) || errors.Is(err, flowcontrol.ErrQuotaExceeded) {
		return fmt.Errorf("%w: %w", ErrShed, err)
	}
	return err
}

// Schedules over the scope's endpoints inside any requested subset, applying the failure mode when a scheduler errors
func (d *director) schedule(ctx context.Context, pool datalayer.Pool, scope Scope, req *framework.Request) (*framework.Result, bool, error) {
	candidates := d.scoped(pool.ID, scope, anyEndpoint)
	if len(candidates) == 0 {
		return nil, false, fmt.Errorf("%w: pool %s has no endpoints serving %s", ErrNoReadyEndpoints, pool.ID, req.TargetModel)
	}
	if req.SubsetSet {
		if candidates = inSubset(req, candidates); len(candidates) == 0 {
			return nil, false, fmt.Errorf("%w: no endpoint of pool %s is in the requested subset", ErrNoReadyEndpoints, pool.ID)
		}
	}
	req.FailOpen = pool.FailureMode == datalayer.FailOpen
	result, err := d.runScheduler(ctx, pool, req, candidates)
	if err == nil {
		if len(result.Endpoints()) == 0 {
			return nil, false, fmt.Errorf("%w: the scheduler picked nothing in pool %s", ErrNoReadyEndpoints, pool.ID)
		}
		return result, result.FailedOpen && req.FailOpen, nil
	}
	if ctx.Err() != nil {
		return nil, false, fmt.Errorf("scheduling: %w", ctx.Err())
	}
	switch {
	case errors.Is(err, ErrShed), errors.Is(err, ErrUnknownSchedulerConfig), errors.Is(err, framework.ErrSaturated):
		return nil, false, err
	case errors.Is(err, flowcontrol.ErrShed):
		return nil, false, fmt.Errorf("%w: %w", ErrShed, err)
	case errors.Is(err, ErrNoReadyEndpoints):
		return nil, false, err
	case errors.Is(err, framework.ErrNoEndpoints):
		return nil, false, fmt.Errorf("%w: %w", ErrNoReadyEndpoints, err)
	}
	if pool.FailureMode != datalayer.FailOpen {
		return nil, false, fmt.Errorf("%w: %w", ErrSchedulerUnavailable, err)
	}
	eps := d.plainPick(candidates)
	if len(eps) == 0 {
		return nil, false, fmt.Errorf("%w: pool %s failed open with no ready endpoint after %w", ErrNoReadyEndpoints, pool.ID, err)
	}
	d.log.Warn("scheduler failed, routing by failure mode", "pool", pool.ID, "request", req.ID, "endpoint", eps[0].ID(), "error", err)
	return &framework.Result{
		PrimaryProfile: FailOpenProfile,
		ProfileResults: map[string]*framework.ProfileRunResult{FailOpenProfile: {Endpoints: eps}},
	}, true, nil
}

// Runs the scheduler the pool's config selects, turning a panic into an error
func (d *director) runScheduler(ctx context.Context, pool datalayer.Pool, req *framework.Request, candidates []datalayer.Endpoint) (res *framework.Result, err error) {
	s := d.scheduler
	if pool.SchedulerConfig != "" {
		s = d.schedulers[pool.SchedulerConfig]
	}
	if s == nil {
		return nil, fmt.Errorf("%w: %w: pool %s names scheduler config %q which the director lacks", ErrSchedulerUnavailable, ErrUnknownSchedulerConfig, pool.ID, pool.SchedulerConfig)
	}
	ctx, span := telemetry.StartRequestSpan(ctx, "schedule",
		attribute.String("nebu.pool", string(pool.ID)),
		attribute.String("nebu.scheduler", pool.SchedulerConfig),
		attribute.Int("nebu.candidates", len(candidates)),
	)
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("scheduler panicked: %v", r)
		}
		if err != nil {
			span.SetStatus(codes.Error, err.Error())
		} else {
			span.SetAttributes(attribute.String("nebu.profile", res.PrimaryProfile))
		}
		span.End()
	}()
	return s.Schedule(ctx, req, candidates)
}

// The candidates whose address the request's subset names
func inSubset(req *framework.Request, candidates []datalayer.Endpoint) []datalayer.Endpoint {
	var eps []datalayer.Endpoint
	for _, ep := range candidates {
		if slices.Contains(req.Subset, ep.Address()) {
			eps = append(eps, ep)
		}
	}
	return eps
}

// Ready endpoints ordered by requests in flight, fewest first and ties shuffled
func (d *director) plainPick(candidates []datalayer.Endpoint) []datalayer.Endpoint {
	var eps []datalayer.Endpoint
	for _, ep := range candidates {
		if datalayer.ReadyAndHealthy(ep) {
			eps = append(eps, ep)
		}
	}
	rand.Shuffle(len(eps), func(i, j int) { eps[i], eps[j] = eps[j], eps[i] })
	load := make(map[datalayer.EndpointID]int, len(eps))
	for _, ep := range eps {
		load[ep.ID()] = d.loads.get(ep.ID())
	}
	slices.SortStableFunc(eps, func(a, b datalayer.Endpoint) int { return cmp.Compare(load[a.ID()], load[b.ID()]) })
	return eps
}

// The body with the target model written in, itself when it already names it, nil when none was read
func mutate(body bodyparse.Document, target string) (bodyparse.Document, error) {
	if body == nil {
		return nil, nil
	}
	if m, err := body.Model(); err == nil && m == target {
		return body, nil
	}
	return body.WithModel(target)
}

// Runs the body stages on a body read after the decision, the decision then carrying what they made of it
func (d *director) AttachBody(ctx context.Context, dec *Decision, body bodyparse.Document) error {
	if dec == nil || dec.Request == nil {
		return errors.New("a body attaches to a decision with its request")
	}
	in := &Inbound{ID: dec.Request.ID, Headers: http.Header{}, Body: body}
	for k, v := range dec.Request.Headers {
		in.Headers.Set(k, v)
	}
	var err error
	if in.Body, err = mutate(body, dec.TargetModel); err != nil {
		return err
	}
	for _, batch := range d.topology.Batches(StagePreRequest) {
		for _, p := range batch {
			if err := d.preMutate(ctx, p, in, dec.Request); err != nil {
				return err
			}
		}
	}
	dec.Body = in.Body
	return d.postDecision(ctx, in, dec)
}

// Runs one pre request mutator, the mutated body becoming the inbound body every later plugin reads
func (d *director) preMutate(ctx context.Context, p framework.Plugin, in *Inbound, req *framework.Request) error {
	m, ok := p.(PreRequestMutator)
	if !ok {
		return nil
	}
	var doc bodyparse.Document
	if err := guard(p, func() error {
		var err error
		doc, err = m.PreRequestBody(ctx, in, req)
		return err
	}); err != nil {
		return fmt.Errorf("pre request plugin %s: %w", p.TypedName(), err)
	}
	if doc != nil {
		in.Body = doc
	}
	return nil
}

// Runs pre request batches in order, each plugin on its own copy merged back in batch order, then the batch's body mutators one by one
func (d *director) preRequest(ctx context.Context, in *Inbound, req *framework.Request) error {
	for _, batch := range d.topology.Batches(StagePreRequest) {
		run := func(ctx context.Context, p framework.Plugin, r *framework.Request) error {
			pp, ok := p.(PreRequestPlugin)
			if !ok {
				return nil
			}
			if err := guard(p, func() error { return pp.PreRequest(ctx, in, r) }); err != nil {
				return fmt.Errorf("pre request plugin %s: %w", p.TypedName(), err)
			}
			return nil
		}
		if len(batch) == 1 {
			if err := run(ctx, batch[0], req); err != nil {
				return err
			}
		} else {
			base := isolate(req)
			copies := make([]*framework.Request, len(batch))
			for i := range copies {
				copies[i] = isolate(req)
			}
			if err := parallel(ctx, len(batch), func(ctx context.Context, i int) error { return run(ctx, batch[i], copies[i]) }); err != nil {
				return err
			}
			for _, c := range copies {
				merge(req, base, c)
			}
		}
		for _, p := range batch {
			if err := d.preMutate(ctx, p, in, req); err != nil {
				return err
			}
		}
	}
	return nil
}

// Runs post decision plugins one after another in topology order, each seeing the last one's changes and body
func (d *director) postDecision(ctx context.Context, in *Inbound, dec *Decision) error {
	for _, batch := range d.topology.Batches(StagePostDecision) {
		for _, p := range batch {
			if pp, ok := p.(PostDecisionPlugin); ok {
				if err := guard(p, func() error { return pp.PostDecision(ctx, in, dec) }); err != nil {
					return fmt.Errorf("post decision plugin %s: %w", p.TypedName(), err)
				}
			}
			m, ok := p.(PostDecisionMutator)
			if !ok {
				continue
			}
			var doc bodyparse.Document
			if err := guard(p, func() error {
				var err error
				doc, err = m.PostDecisionBody(ctx, in, dec)
				return err
			}); err != nil {
				return fmt.Errorf("post decision plugin %s: %w", p.TypedName(), err)
			}
			if doc != nil {
				dec.Body, in.Body = doc, doc
			}
		}
	}
	return nil
}

func (d *director) ResponseReceived(ctx context.Context, dec *Decision, resp *framework.Response, headers http.Header) error {
	if resp == nil || headers == nil {
		return errors.New("a received response needs its response and headers")
	}
	if resp.Endpoint == nil {
		resp.Endpoint = dec.Endpoints[0]
	}
	d.recount(dec, resp.Endpoint.ID())
	headers.Set(HeaderServed, resp.Endpoint.Address().String())
	if resp.Headers == nil {
		resp.Headers = flatten(headers)
	}
	for _, h := range d.receivedHooks {
		d.hook(h, func() { h.ResponseReceived(ctx, dec.Request, resp) })
	}
	for _, batch := range d.topology.Batches(StageResponseHeaders) {
		run := func(ctx context.Context, p framework.Plugin, hd http.Header) error {
			hp := p.(ResponseHeadersPlugin)
			if err := guard(p, func() error { return hp.ResponseHeaders(ctx, dec, resp, hd) }); err != nil {
				return fmt.Errorf("response headers plugin %s: %w", p.TypedName(), err)
			}
			return nil
		}
		if len(batch) == 1 {
			if err := run(ctx, batch[0], headers); err != nil {
				return err
			}
			continue
		}
		base := headers.Clone()
		copies := make([]http.Header, len(batch))
		for i := range copies {
			copies[i] = headers.Clone()
		}
		if err := parallel(ctx, len(batch), func(ctx context.Context, i int) error { return run(ctx, batch[i], copies[i]) }); err != nil {
			return err
		}
		for _, c := range copies {
			mergeHeader(headers, base, c)
		}
	}
	return nil
}

// Moves the in flight count to the endpoint that served, unless the decision already completed
func (d *director) recount(dec *Decision, served datalayer.EndpointID) {
	dec.life.mu.Lock()
	defer dec.life.mu.Unlock()
	if dec.life.counted == "" || dec.life.counted == served {
		return
	}
	d.loads.add(dec.life.counted, -1)
	d.loads.add(served, 1)
	dec.life.counted = served
}

func (d *director) ResponseChunk(ctx context.Context, dec *Decision, resp *framework.Response, chunk []byte, end bool) ([]byte, error) {
	for _, h := range d.streamingHooks {
		d.hook(h, func() { h.ResponseStreaming(ctx, dec.Request, resp, chunk) })
	}
	out := chunk
	for _, batch := range d.topology.Batches(StageResponseBody) {
		for _, p := range batch {
			bp := p.(ResponseBodyPlugin)
			err := guard(p, func() error {
				var err error
				out, err = bp.ResponseBody(ctx, dec, resp, out, end)
				return err
			})
			if err != nil {
				return nil, fmt.Errorf("response body plugin %s: %w", p.TypedName(), err)
			}
		}
	}
	return out, nil
}

func (d *director) ResponseComplete(ctx context.Context, dec *Decision, resp *framework.Response) {
	if resp == nil {
		resp = &framework.Response{}
	}
	if d.finish(ctx, dec, resp) {
		d.observer.Completed(dec, resp)
	}
}

// Runs every completion hook and releases the permit and in flight count, once per decision
func (d *director) finish(ctx context.Context, dec *Decision, resp *framework.Response) bool {
	ran := false
	dec.life.once.Do(func() {
		ran = true
		defer d.release(dec, resp)
		for _, h := range d.completeHooks {
			d.hook(h, func() { h.ResponseComplete(ctx, dec.Request, resp) })
		}
		for _, batch := range d.topology.Batches(StageResponseComplete) {
			together(len(batch), func(i int) {
				cp := batch[i].(ResponseCompletePlugin)
				d.hook(cp, func() { cp.ResponseComplete(ctx, dec, resp) })
			})
		}
	})
	return ran
}

// Settles the tokens the response used against the permit, then releases it and the in flight count
func (d *director) release(dec *Decision, resp *framework.Response) {
	if dec.Permit != nil {
		dec.Permit.Settle(resp.PromptTokens, resp.OutputTokens)
		dec.Permit.Release()
	}
	dec.life.mu.Lock()
	defer dec.life.mu.Unlock()
	if dec.life.counted != "" {
		d.loads.add(dec.life.counted, -1)
		dec.life.counted = ""
	}
}

// Runs a hook that returns nothing, logging a panic as an error naming the plugin
func (d *director) hook(p framework.Plugin, fn func()) {
	if err := guard(p, func() error { fn(); return nil }); err != nil {
		d.log.Error("lifecycle hook failed", "error", err)
	}
}

// Runs fn, turning a panic into an error naming the plugin
func guard(p framework.Plugin, fn func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("plugin %s panicked: %v", p.TypedName(), r)
		}
	}()
	return fn()
}

// Runs fn for every index at once, cancelling the others on the first error and returning it
func parallel(ctx context.Context, n int, fn func(context.Context, int) error) error {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	var (
		wg    sync.WaitGroup
		once  sync.Once
		first error
	)
	for i := range n {
		wg.Go(func() {
			if err := fn(ctx, i); err != nil {
				once.Do(func() {
					first = err
					cancel(err)
				})
			}
		})
	}
	wg.Wait()
	return first
}

// Runs fn for every index at once and waits for all, inline when there is one
func together(n int, fn func(int)) {
	if n == 1 {
		fn(0)
		return
	}
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() { fn(i) })
	}
	wg.Wait()
}

// The inbound id, then the request id header, then a fresh random id
func requestID(in *Inbound) string {
	if id := cmp.Or(in.ID, strings.TrimSpace(in.Headers.Get(HeaderRequestID))); id != "" {
		return id
	}
	var b [16]byte
	binary.LittleEndian.PutUint64(b[:8], rand.Uint64())
	binary.LittleEndian.PutUint64(b[8:], rand.Uint64())
	return hex.EncodeToString(b[:])
}

// Headers as one value per name, repeated values joined by commas
func flatten(h http.Header) map[string]string {
	out := make(map[string]string, len(h))
	for k, v := range h {
		out[k] = strings.Join(v, ", ")
	}
	return out
}

// Priority from the objective header and TTFT and TPOT targets from their millisecond headers
func parseObjective(h http.Header) (framework.Objective, error) {
	var o framework.Objective
	if v := strings.TrimSpace(h.Get(HeaderObjective)); v != "" {
		p, err := strconv.Atoi(v)
		if err != nil {
			return o, fmt.Errorf("%w: %s %q is not an integer priority", ErrInvalidHeader, HeaderObjective, v)
		}
		o.Priority = p
	}
	var err error
	if o.TTFT, err = millis(h, HeaderTTFTObjective); err != nil {
		return o, err
	}
	if o.TPOT, err = millis(h, HeaderTPOTObjective); err != nil {
		return o, err
	}
	return o, nil
}

func millis(h http.Header, name string) (time.Duration, error) {
	v := strings.TrimSpace(h.Get(name))
	if v == "" {
		return 0, nil
	}
	ms, err := strconv.ParseInt(v, 10, 64)
	if err != nil || ms < 0 || ms > math.MaxInt64/int64(time.Millisecond) {
		return 0, fmt.Errorf("%w: %s %q is not a millisecond count", ErrInvalidHeader, name, v)
	}
	return time.Duration(ms) * time.Millisecond, nil
}

// The comma separated host and port list, set whenever the header is present even when empty
func parseSubset(h http.Header) ([]datalayer.Address, bool, error) {
	vals, ok := h[http.CanonicalHeaderKey(HeaderSubset)]
	if !ok {
		return nil, false, nil
	}
	var out []datalayer.Address
	for _, v := range vals {
		for part := range strings.SplitSeq(v, ",") {
			if part = strings.TrimSpace(part); part == "" {
				continue
			}
			a, err := datalayer.ParseAddress(part)
			if err != nil {
				return nil, true, fmt.Errorf("%w: %s entry %q: %w", ErrInvalidHeader, HeaderSubset, part, err)
			}
			out = append(out, a)
		}
	}
	return out, true, nil
}
