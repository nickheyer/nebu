package gateway

import (
	"cmp"
	"context"
	"encoding/base64"
	"fmt"
	"math"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/nickheyer/nebu/internal/chat"
	"github.com/nickheyer/nebu/internal/mesh"
	"github.com/nickheyer/nebu/pkg/bodyparse"
	"github.com/nickheyer/nebu/pkg/datalayer"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/requestcontrol"
	"github.com/nickheyer/nebu/pkg/scheduling/framework"
)

// The headers that switch request modes, the system prompt one base64 encoded UTF-8
const (
	GhostHeader  = "X-Nebu-Ghost"
	BareHeader   = "X-Nebu-Bare"
	SystemHeader = "X-Nebu-System"
)

// What the mode headers ask of a request
type modes struct {
	// Keep no trace, event or prompt pin
	ghost bool
	// Attach no tool server of the route
	bare bool
	// The system prompt replacing the client's, empty when the client's stands
	system string
}

// Reads the mode headers, refusing values that are not 1, true, 0, false or valid base64 UTF-8
func parseModes(h http.Header) (modes, error) {
	var m modes
	var err error
	if m.ghost, err = switched(GhostHeader, h.Get(GhostHeader)); err != nil {
		return m, err
	}
	if m.bare, err = switched(BareHeader, h.Get(BareHeader)); err != nil {
		return m, err
	}
	if raw := h.Get(SystemHeader); raw != "" {
		text, err := base64.StdEncoding.DecodeString(raw)
		if err != nil {
			return m, bad("%s is not base64: %v", SystemHeader, err)
		}
		if !utf8.Valid(text) || len(text) == 0 {
			return m, bad("%s does not decode to UTF-8 text", SystemHeader)
		}
		m.system = string(text)
	}
	return m, nil
}

// Whether a mode header turns its mode on, 1 or true on, 0, false or unset off, anything else refused
func switched(name, value string) (bool, error) {
	switch {
	case value == "" || value == "0" || strings.EqualFold(value, "false"):
		return false, nil
	case value == "1" || strings.EqualFold(value, "true"):
		return true, nil
	}
	return false, bad("%s is 1, true, 0, false, or unset, not %q", name, value)
}

// Runs the pre request stage in topology order without a director, for requests forwarded to another node
func (g *Gateway) preRequest(ctx context.Context, in *requestcontrol.Inbound, req *framework.Request) error {
	for _, batch := range g.topology.Batches(requestcontrol.StagePreRequest) {
		for _, p := range batch {
			if pp, ok := p.(requestcontrol.PreRequestPlugin); ok {
				if err := pp.PreRequest(ctx, in, req); err != nil {
					return fmt.Errorf("pre request plugin %s: %w", p.TypedName(), err)
				}
			}
			m, ok := p.(requestcontrol.PreRequestMutator)
			if !ok {
				continue
			}
			doc, err := m.PreRequestBody(ctx, in, req)
			if err != nil {
				return fmt.Errorf("pre request plugin %s: %w", p.TypedName(), err)
			}
			if doc != nil {
				in.Body = doc
			}
		}
	}
	return nil
}

// Checks the hop count against the mesh limit and records the entry node of a request a member forwarded
type meshEntry struct{}

func (*meshEntry) TypedName() framework.TypedName {
	return framework.TypedName{Type: "mesh-entry", Name: "mesh-entry"}
}

func (*meshEntry) PreRequest(ctx context.Context, in *requestcontrol.Inbound, _ *framework.Request) error {
	x := exchangeOf(ctx)
	hops, err := mesh.NextHop(in.Headers)
	if err != nil {
		return err
	}
	x.hops = hops
	if x.who.peer {
		x.entry = in.Headers.Get(EntryHeader)
		x.t.EntryNode = x.entry
	}
	return nil
}

// Turns a named objective from the header or the route into a priority band and latency targets
type objectiveResolver struct{ g *Gateway }

func (*objectiveResolver) TypedName() framework.TypedName {
	return framework.TypedName{Type: "objective-resolver", Name: "objective-resolver"}
}

func (o *objectiveResolver) PreRequest(ctx context.Context, _ *requestcontrol.Inbound, req *framework.Request) error {
	x := exchangeOf(ctx)
	name, byHeader := x.objective, x.objective != ""
	if !byHeader && !x.priorityByHeader {
		name = x.route.GetSpec().GetObjective()
	}
	if name != "" {
		obj, ok := o.g.manager.Objective(name)
		switch {
		case !ok && byHeader:
			return fmt.Errorf("%w: %s names objective %q, which is not defined", requestcontrol.ErrInvalidHeader, requestcontrol.HeaderObjective, name)
		case !ok:
			return fmt.Errorf("route %s names objective %q, which is not defined", x.route.GetName(), name)
		}
		req.Objective.Priority = int(obj.GetPriority())
		if req.Objective.TTFT == 0 {
			req.Objective.TTFT = time.Duration(obj.GetTtftMs()) * time.Millisecond
		}
		if req.Objective.TPOT == 0 {
			req.Objective.TPOT = time.Duration(obj.GetTpotMs()) * time.Millisecond
		}
	}
	x.t.Objective, x.t.Priority, x.t.FairnessId = name, int32(req.Objective.Priority), req.FairnessID
	return nil
}

// A route's in flight count, request total and token bucket, read and changed without locks
type routeState struct {
	inflight atomic.Int64
	requests atomic.Uint64
	// Theoretical arrival time of the next request in unix nanoseconds, the GCRA bucket state
	tat atomic.Int64
}

// The state of a route, created on its first request
func (g *Gateway) routeState(name string) *routeState {
	if v, ok := g.routes.Load(name); ok {
		return v.(*routeState)
	}
	v, _ := g.routes.LoadOrStore(name, &routeState{})
	return v.(*routeState)
}

// Takes a token at rate per second with burst capacity, answering the wait until one frees when none is left
func (s *routeState) take(now time.Time, rate float64, burst int) (bool, time.Duration) {
	interval := int64(float64(time.Second) / rate)
	tolerance := interval * int64(burst-1)
	n := now.UnixNano()
	for {
		tat := s.tat.Load()
		t := max(tat, n)
		if t-tolerance > n {
			return false, time.Duration(t - tolerance - n)
		}
		if s.tat.CompareAndSwap(tat, t+interval) {
			return true, 0
		}
	}
}

// A route slot a request holds until it completes
type hold struct {
	st   *routeState
	once sync.Once
}

func (h *hold) release() {
	if h != nil {
		h.once.Do(func() { h.st.inflight.Add(-1) })
	}
}

// The route policy with every unset field taken from the gateway default, negative rates counting as unset
func effectivePolicy(route, def *v1.Policy) *v1.Policy {
	out := &v1.Policy{
		MaxInFlight:       def.GetMaxInFlight(),
		RequestsPerSecond: max(def.GetRequestsPerSecond(), 0),
		Burst:             def.GetBurst(),
		RequestTimeoutMs:  def.GetRequestTimeoutMs(),
		UpstreamTimeoutMs: def.GetUpstreamTimeoutMs(),
	}
	if v := route.GetMaxInFlight(); v > 0 {
		out.MaxInFlight = v
	}
	if v := route.GetRequestsPerSecond(); v > 0 {
		out.RequestsPerSecond = v
	}
	if v := route.GetBurst(); v > 0 {
		out.Burst = v
	}
	if v := route.GetRequestTimeoutMs(); v > 0 {
		out.RequestTimeoutMs = v
	}
	if v := route.GetUpstreamTimeoutMs(); v > 0 {
		out.UpstreamTimeoutMs = v
	}
	return out
}

// Counts a route's requests and rate limits them once per request across the mesh, at the node that dispatches
type routePolicy struct{ g *Gateway }

func (*routePolicy) TypedName() framework.TypedName {
	return framework.TypedName{Type: "route-policy", Name: "route-policy"}
}

func (p *routePolicy) PreRequest(ctx context.Context, _ *requestcontrol.Inbound, req *framework.Request) error {
	x := exchangeOf(ctx)
	st := p.g.routeState(x.routeName())
	st.requests.Add(1)
	if x.forwarding() {
		return nil
	}
	pol := x.policy
	st.inflight.Add(1)
	if rate := pol.GetRequestsPerSecond(); rate > 0 {
		burst := int(pol.GetBurst())
		if burst == 0 {
			burst = int(math.Ceil(rate))
		}
		if ok, wait := st.take(p.g.clock.Now(), rate, max(burst, 1)); !ok {
			st.inflight.Add(-1)
			p.g.metrics.Shed(x.pool.ID, req.Objective.Priority, "rate")
			return &policyError{wait: wait, msg: fmt.Sprintf("model %s is over its %g requests per second, retry shortly", x.model, rate)}
		}
	}
	x.hold = &hold{st: st}
	return nil
}

// Releases the route slot the request held
func (p *routePolicy) ResponseComplete(ctx context.Context, _ *requestcontrol.Decision, _ *framework.Response) {
	exchangeOf(ctx).hold.release()
}

// Applies the system prompt override and plans the tools the gateway runs, answering the body with its changed chat view
type toolsPlanner struct{ g *Gateway }

func (*toolsPlanner) TypedName() framework.TypedName {
	return framework.TypedName{Type: "tools-planner", Name: "tools-planner"}
}

func (t *toolsPlanner) PreRequestBody(ctx context.Context, in *requestcontrol.Inbound, _ *framework.Request) (bodyparse.Document, error) {
	x := exchangeOf(ctx)
	if x.forwarding() || x.kind == chat.Image || x.kind == chat.Video {
		return nil, nil
	}
	asked := x.modes.system != "" || in.Headers.Get(ToolsHeader) != ""
	if x.kind == chat.None {
		if asked {
			return nil, bad("%s is proxied unparsed, so %s and %s do not apply to it", x.r.URL.Path, SystemHeader, ToolsHeader)
		}
		return nil, nil
	}
	if in.Body == nil {
		return nil, nil
	}
	creq, err := chatOf(in.Body, x.client, x.r.URL.Path)
	if err != nil {
		if asked {
			return nil, err
		}
		return nil, nil
	}
	out := *creq
	changed := false
	if x.modes.system != "" && out.Kind != chat.Embed {
		turns := []chat.Message{{Role: "system", Parts: []chat.Part{{Type: "text", Text: x.modes.system}}}}
		for _, m := range out.Messages {
			if m.Role != "system" && m.Role != "developer" {
				turns = append(turns, m)
			}
		}
		out.Messages, changed = turns, true
	}
	if out.Kind == chat.Chat {
		plan, err := t.g.plan(ctx, &out, x.route, in.Headers, x.modes.bare)
		if err != nil {
			return nil, err
		}
		x.plan = plan
		if !x.modes.bare {
			if msgs, spliced := t.g.rounds.splice(x.routeName(), out.Messages); spliced {
				out.Messages, changed = msgs, true
			}
		}
	}
	x.changed = changed
	return in.Body.WithView(chatView, &out), nil
}

// Keeps a failed open decision to the endpoints serving its target, as the scheduler would have
type servedOnly struct{ g *Gateway }

func (*servedOnly) TypedName() framework.TypedName {
	return framework.TypedName{Type: "served-only", Name: "served-only"}
}

func (s *servedOnly) PostDecision(_ context.Context, _ *requestcontrol.Inbound, dec *requestcontrol.Decision) error {
	if !dec.FailedOpen {
		return nil
	}
	if dec.Endpoints = s.g.serving(dec.Request, dec.Endpoints); len(dec.Endpoints) == 0 {
		return fmt.Errorf("%w: no endpoint of pool %s serves %s", requestcontrol.ErrNoReadyEndpoints, dec.Pool.ID, dec.TargetModel)
	}
	addrs := make([]string, len(dec.Endpoints))
	for i, ep := range dec.Endpoints {
		addrs[i] = ep.Address().String()
	}
	dec.Headers[requestcontrol.HeaderDestination] = strings.Join(addrs, ",")
	return nil
}

// Folds tools into the prompt when the template cannot render them or token ids bypass the endpoint's parser
type toolsEmulation struct{ g *Gateway }

func (*toolsEmulation) TypedName() framework.TypedName {
	return framework.TypedName{Type: "tools-emulation", Name: "tools-emulation"}
}

func (e *toolsEmulation) PostDecision(ctx context.Context, _ *requestcontrol.Inbound, dec *requestcontrol.Decision) error {
	x := exchangeOf(ctx)
	if dec.Body == nil || x.kind != chat.Chat || len(dec.Endpoints) == 0 {
		return nil
	}
	creq, err := chatOf(dec.Body, x.client, x.r.URL.Path)
	if err != nil || len(creq.Tools) == 0 {
		return nil
	}
	support := e.g.instances.ToolSupport(dec.Endpoints[0].Labels()[datalayer.LabelInstance])
	if support == v1.ToolSupport_TOOL_SUPPORT_NONE {
		return bad("model %s cannot take tools", x.model)
	}
	if foldsTools(support, dec.Pool) {
		x.emulate = true
	}
	return nil
}

// Records the endpoint that served on the trace with the profile and score that picked it
type servedEndpoint struct{}

func (*servedEndpoint) TypedName() framework.TypedName {
	return framework.TypedName{Type: "served-endpoint", Name: "served-endpoint"}
}

func (*servedEndpoint) ResponseHeaders(ctx context.Context, dec *requestcontrol.Decision, resp *framework.Response, headers http.Header) error {
	x := exchangeOf(ctx)
	ep := resp.Endpoint
	headers.Set(requestcontrol.HeaderServed, ep.Address().String())
	x.t.Endpoint, x.t.InstanceId = ep.Address().String(), ep.Labels()[datalayer.LabelInstance]
	x.t.Profile = dec.Result.PrimaryProfile
	if r, ok := dec.Result.ProfileResults[dec.Result.PrimaryProfile]; ok {
		x.t.Score = r.Scores[ep.ID()]
		x.t.Filters, x.t.Candidates = decisionRecords(r, x.dec.Pool.ID, x.g.store)
	}
	return nil
}

// The filters a profile run applied with what each dropped, and every scored candidate highest first
func decisionRecords(r *framework.ProfileRunResult, pool datalayer.PoolID, store datalayer.Store) ([]*v1.TraceFilter, []*v1.TraceScore) {
	address := func(id datalayer.EndpointID) string {
		if ep, ok := store.Endpoint(id); ok {
			return ep.Address().String()
		}
		return string(id)
	}
	before := map[datalayer.EndpointID]bool{}
	for _, ep := range store.Endpoints(pool, func(datalayer.Endpoint) bool { return true }) {
		before[ep.ID()] = true
	}
	filters := make([]*v1.TraceFilter, 0, len(r.Filtered))
	for _, f := range r.Filtered {
		kept := make(map[datalayer.EndpointID]bool, len(f.Kept))
		for _, id := range f.Kept {
			kept[id] = true
		}
		tf := &v1.TraceFilter{Name: f.Filter}
		for id := range before {
			if !kept[id] {
				tf.Dropped = append(tf.Dropped, address(id))
			}
		}
		slices.Sort(tf.Dropped)
		filters = append(filters, tf)
		before = kept
	}
	candidates := make([]*v1.TraceScore, 0, len(r.Scores))
	for id, score := range r.Scores {
		ts := &v1.TraceScore{Endpoint: address(id), Score: score, ByScorer: map[string]float64{}}
		for scorer, scores := range r.Scored {
			ts.ByScorer[scorer] = scores[id]
		}
		candidates = append(candidates, ts)
	}
	slices.SortStableFunc(candidates, func(a, b *v1.TraceScore) int {
		if c := cmp.Compare(b.Score, a.Score); c != 0 {
			return c
		}
		return cmp.Compare(a.Endpoint, b.Endpoint)
	})
	return filters, candidates
}

// Names the request id and, for a traced request, its trace on the answer
type traceID struct{}

func (*traceID) TypedName() framework.TypedName {
	return framework.TypedName{Type: "trace-id", Name: "trace-id"}
}

func (*traceID) ResponseHeaders(ctx context.Context, dec *requestcontrol.Decision, _ *framework.Response, headers http.Header) error {
	x := exchangeOf(ctx)
	headers.Set(requestcontrol.HeaderRequestID, dec.Request.ID)
	if !x.modes.ghost {
		headers.Set(TraceHeader, x.t.GetId())
	}
	return nil
}

// Carries the wait an endpoint asked for to the client in whole seconds when the answer names none yet
type retryAfter struct{ clock Clock }

func (*retryAfter) TypedName() framework.TypedName {
	return framework.TypedName{Type: "retry-after", Name: "retry-after"}
}

func (r *retryAfter) ResponseHeaders(_ context.Context, _ *requestcontrol.Decision, resp *framework.Response, headers http.Header) error {
	if headers.Get("Retry-After") != "" {
		return nil
	}
	upstream := http.Header{}
	for k, v := range resp.Headers {
		upstream.Set(k, v)
	}
	if wait := chat.RetryAfterOf(upstream, r.clock.Now()); wait > 0 {
		headers.Set("Retry-After", fmt.Sprint(int64(seconds(wait)/time.Second)))
	}
	return nil
}

// The route name requests are counted and limited under, the client's model name when no route has it
func (x *exchange) routeName() string {
	if x.route != nil {
		return x.route.GetName()
	}
	return x.model
}

// Whether the request goes to another node's gateway
func (x *exchange) forwarding() bool {
	return x.route.GetNodeId() != "" && (x.g.peers == nil || x.route.GetNodeId() != x.g.peers.Self())
}
