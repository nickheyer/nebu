//go:build conformance

package conformance

import (
	"context"
	"fmt"
	"maps"
	"math"
	"net/http"
	"slices"
	"strings"
	"testing"

	"connectrpc.com/connect"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// Requests sent per traffic check, and for the weighted split the total, its workers and tolerance
const (
	perCheck      = 20
	splitRequests = 2000
	splitWorkers  = 16
	splitShare    = 0.70
	splitSlack    = 0.05
)

// Sets up a ready pool over the backends and a ready route sending model to it
func (r *recorder) servedPool(ctx context.Context, id, model string, spec *v1.PoolSpec) {
	r.t.Helper()
	_, err := r.putPool(ctx, id, spec)
	r.must("create pool "+id, err)
	_, err = r.putRoute(ctx, model, routeTo(model, &v1.RewriteTarget{PoolId: id, Weight: 1}))
	r.must("create route "+model, err)
	_, err = r.h.poolReady(ctx, id, len(spec.GetStaticEndpoints()))
	r.must("pool "+id+" ready", err)
	_, err = r.h.routeReady(ctx, model)
	r.must("route "+model+" ready", err)
	_, _, err = r.h.until(ctx, model, nil, http.StatusOK)
	r.must("route "+model+" answers", err)
}

// Sends n requests one at a time and checks each was served by a backend in want with matching headers
func (r *recorder) confined(ctx context.Context, check, model string, header http.Header, n int, among []*backend, want ...*backend) {
	r.t.Helper()
	hits := map[string]int{}
	var bad []string
	for i := range n {
		a, b, err := r.h.attributed(ctx, model, header, i%2 == 1, among)
		switch {
		case err != nil:
			bad = append(bad, err.Error())
		case a.status != http.StatusOK:
			bad = append(bad, a.String())
		case b == nil:
			bad = append(bad, "no stub received the request, "+a.String())
		case !slices.Contains(want, b):
			bad = append(bad, fmt.Sprintf("stub %s outside the allowed set served, %s", b.id, a))
		case a.served != b.addr:
			bad = append(bad, fmt.Sprintf("stub %s at %s served but the served header names %q", b.id, b.addr, a.served))
		default:
			hits[b.addr]++
		}
	}
	r.check(check, len(bad) == 0, "%d of %d requests served inside %s with a matching served header, by address %v%s", n-len(bad), n, addrs(want), hits, first(bad))
}

// Sends n requests that must all answer status while no backend receives anything
func (r *recorder) refused(ctx context.Context, check, model string, header http.Header, status, n int, among []*backend) {
	r.t.Helper()
	before := counts(among)
	var bad []string
	for range n {
		a, err := r.h.chat(ctx, model, header, false)
		switch {
		case err != nil:
			bad = append(bad, err.Error())
		case a.status != status:
			bad = append(bad, a.String())
		}
	}
	after := counts(among)
	reached := !maps.Equal(before, after)
	r.check(check, len(bad) == 0 && !reached, "%d of %d answered %d, stub requests before %v after %v%s", n-len(bad), n, status, before, after, first(bad))
}

func counts(bs []*backend) map[string]int {
	out := make(map[string]int, len(bs))
	for _, b := range bs {
		out[b.id] = b.count()
	}
	return out
}

func addrs(bs []*backend) []string {
	out := make([]string, len(bs))
	for i, b := range bs {
		out[i] = b.addr
	}
	return out
}

// The first failure as a suffix, empty when there is none
func first(bad []string) string {
	if len(bad) == 0 {
		return ""
	}
	return fmt.Sprintf(", %d failures, first %s", len(bad), bad[0])
}

// Every plain and streamed response names the stub that served it and carries that stub's own id
func TestEndpointServed(t *testing.T) {
	h := suite
	r := h.feature(t, FeatureEndpointServed)
	ctx := t.Context()
	model := h.name("served")
	bs := []*backend{h.backend(t, h.name("served", "a"), model, false), h.backend(t, h.name("served", "b"), model, false), h.backend(t, h.name("served", "c"), model, false)}
	r.servedPool(ctx, model, model, staticPool(model, bs...))

	hits := map[string]int{}
	var unnamed, foreign []string
	streamed := 0
	n := 3 * perCheck
	for i := range n {
		stream := i%3 == 0
		a, b, err := h.attributed(ctx, model, nil, stream, bs)
		switch {
		case err != nil:
			unnamed = append(unnamed, err.Error())
			continue
		case a.status != http.StatusOK || b == nil:
			unnamed = append(unnamed, a.String())
			continue
		case a.served != b.addr:
			unnamed = append(unnamed, fmt.Sprintf("stub %s at %s served but the header names %q", b.id, b.addr, a.served))
		case a.stubID != b.id:
			foreign = append(foreign, fmt.Sprintf("stub %s served but the answer carries %s %q", b.id, headerStubID, a.stubID))
		}
		if stream {
			streamed++
		}
		hits[b.id]++
	}
	r.check("served header names the serving endpoint", len(unnamed) == 0, "%d of %d answers, %d of them streamed, named the stub that received the request, spread %v%s", n-len(unnamed), n, streamed, hits, first(unnamed))
	r.check("answer comes from the named endpoint", len(foreign) == 0, "every answer carried the %s of the stub that received it%s", headerStubID, first(foreign))
	r.check("every endpoint of the pool served", len(hits) == len(bs), "%d of %d stubs served at least once, %v", len(hits), len(bs), hits)
}

// A subset header confines picks to the named endpoints and a subset with no eligible endpoint answers 503
func TestEndpointConfinement(t *testing.T) {
	h := suite
	r := h.feature(t, FeatureEndpointConfinement)
	ctx := t.Context()
	model := h.name("subset")
	bs := []*backend{h.backend(t, h.name("subset", "a"), model, false), h.backend(t, h.name("subset", "b"), model, false), h.backend(t, h.name("subset", "c"), model, false)}
	outsider := h.backend(t, h.name("subset", "outside"), model, false)
	all := append(slices.Clone(bs), outsider)
	r.servedPool(ctx, model, model, staticPool(model, bs...))

	for _, b := range bs {
		r.confined(ctx, "subset of "+b.id+" alone", model, subset(b.addr), perCheck, all, b)
	}
	r.confined(ctx, "subset of two endpoints", model, subset(bs[0].addr, bs[1].addr), perCheck, all, bs[0], bs[1])
	r.refused(ctx, "subset naming only an endpoint outside the pool answers 503", model, subset(outsider.addr), http.StatusServiceUnavailable, perCheck/2, all)
	r.refused(ctx, "subset naming only a dead address answers 503", model, subset(h.deadAddr(t)), http.StatusServiceUnavailable, perCheck/2, all)
}

// Two ports of one host are two endpoints, each reachable alone, and a third port of the host never serves
func TestDataParallelPortConfinement(t *testing.T) {
	h := suite
	r := h.feature(t, FeatureDataParallelPortConfinement)
	ctx := t.Context()
	model := h.name("dp")
	rank0, rank1 := h.backend(t, h.name("dp", "rank0"), model, false), h.backend(t, h.name("dp", "rank1"), model, false)
	third := h.backend(t, h.name("dp", "unlisted"), model, false)
	all := []*backend{rank0, rank1, third}
	r.servedPool(ctx, model, model, staticPool(model, rank0, rank1))

	p, err := h.pool(ctx, model)
	r.must("read pool "+model, err)
	ports := slices.Sorted(slices.Values(p.GetSpec().GetTargetPorts()))
	want := slices.Sorted(slices.Values([]uint32{rank0.port, rank1.port}))
	r.check("target ports read back", slices.Equal(ports, want), "spec target_ports %v, created with %v", ports, want)
	var eps []string
	for _, ep := range p.GetStatus().GetEndpoints() {
		eps = append(eps, ep.GetAddress())
	}
	slices.Sort(eps)
	wantEps := slices.Sorted(slices.Values([]string{rank0.addr, rank1.addr}))
	r.check("one endpoint per target port", slices.Equal(eps, wantEps), "status endpoints %v, want %v", eps, wantEps)

	answers, err := h.burst(ctx, model, nil, 2*perCheck, 4)
	var bad []string
	if err != nil {
		bad = append(bad, err.Error())
	}
	for _, a := range answers {
		if a.status != http.StatusOK || (a.served != rank0.addr && a.served != rank1.addr) {
			bad = append(bad, a.String())
		}
	}
	r.check("concurrent traffic stays on listed ports", len(bad) == 0 && third.count() == 0, "%d of %d answers served from %s, unlisted port %d received %d%s", len(answers)-len(bad), len(answers), addrs(all[:2]), third.port, third.count(), first(bad))
	r.confined(ctx, "rank 0 port reachable alone", model, subset(rank0.addr), perCheck/2, all, rank0)
	r.confined(ctx, "rank 1 port reachable alone", model, subset(rank1.addr), perCheck/2, all, rank1)
	r.refused(ctx, "unlisted port of the same host never serves", model, subset(third.addr), http.StatusServiceUnavailable, perCheck/2, all)

	stray := staticPool(h.name("dp", "stray"), rank0, rank1)
	stray.StaticEndpoints = append(stray.StaticEndpoints, &v1.StaticEndpoint{Address: third.addr, Rank: 2})
	_, err = r.putPool(ctx, h.name("dp", "stray"), stray)
	r.check("endpoint on a port outside target_ports refused", connect.CodeOf(err) == connect.CodeInvalidArgument, "target_ports %v with an endpoint at %s answered %v", stray.GetTargetPorts(), third.addr, err)
}

// A route with two pool targets weighted 70 and 30 splits traffic within five points of those shares
func TestWeightedSplit(t *testing.T) {
	h := suite
	r := h.feature(t, FeatureWeightedSplit)
	ctx := t.Context()
	ph, pl := h.name("split", "heavy"), h.name("split", "light")
	heavy, light := h.backend(t, ph, ph, false), h.backend(t, pl, pl, false)
	model := h.name("split")
	_, err := r.putPool(ctx, ph, staticPool(ph, heavy))
	r.must("create pool "+ph, err)
	_, err = r.putPool(ctx, pl, staticPool(pl, light))
	r.must("create pool "+pl, err)
	wh, wl := uint32(math.Round(splitShare*100)), uint32(math.Round((1-splitShare)*100))
	_, err = r.putRoute(ctx, model, routeTo(model, &v1.RewriteTarget{PoolId: ph, Weight: wh}, &v1.RewriteTarget{PoolId: pl, Weight: wl}))
	r.must("create split route", err)
	for _, id := range []string{ph, pl} {
		_, err = h.poolReady(ctx, id, 1)
		r.must("pool "+id+" ready", err)
	}
	rt, err := h.routeReady(ctx, model)
	r.must("split route ready", err)
	r.check("route resolves both pools", slices.Equal(rt.GetStatus().GetPoolIds(), []string{ph, pl}), "status pool_ids %v", rt.GetStatus().GetPoolIds())
	_, _, err = h.until(ctx, model, nil, http.StatusOK)
	r.must("split route answers", err)

	before := counts([]*backend{heavy, light})
	answers, err := h.burst(ctx, model, nil, splitRequests, splitWorkers)
	ok := 0
	var bad []string
	if err != nil {
		bad = append(bad, err.Error())
	}
	for _, a := range answers {
		if a.status == http.StatusOK {
			ok++
		} else {
			bad = append(bad, a.String())
		}
	}
	nh, nl := heavy.count()-before[heavy.id], light.count()-before[light.id]
	r.check("every split request served", ok == splitRequests && nh+nl == splitRequests, "%d of %d answered 200, stubs received %d%s", ok, splitRequests, nh+nl, first(bad))
	share := float64(nh) / float64(max(nh+nl, 1))
	r.check("split follows the weights", math.Abs(share-splitShare) <= splitSlack, "weight %d pool received %d and weight %d pool received %d, share %.3f against %.2f within %.2f", wh, nh, wl, nl, share, splitShare, splitSlack)
}

// Scheduler YAML whose profiles pick among ready endpoints in the subset, asking remote first when set
func schedulerYAML(remote string) string {
	y := `plugins:
- type: ready-filter
  name: ready
- type: subset-filter
  name: subset
- type: max-score-picker
  name: picker
- type: single-profile-handler
  name: handler
  parameters:
    profile: default
profiles:
- name: default
  plugins:
  - pluginRef: ready
  - pluginRef: subset
  - pluginRef: picker
profileHandler:
  pluginRef: handler
`
	if remote != "" {
		y += "remote:\n  url: http://" + remote + "\n  timeout: 500ms\n"
	}
	return y
}

// With a remote scheduler that cannot be reached a FAIL_OPEN pool serves and a FAIL_CLOSE pool answers 503
func TestFailOpen(t *testing.T) {
	h := suite
	r := h.feature(t, FeatureFailOpen)
	ctx := t.Context()
	dead := h.deadAddr(t)
	cfg := h.name("failopen", "remote")
	c, err := r.putConfig(ctx, cfg, schedulerYAML(dead))
	r.must("create remote scheduler config", err)
	r.check("remote scheduler config accepted", isTrue(condition(c.GetStatus().GetConditions(), condAccepted)), "config %s at dead address %s, Accepted %s", cfg, dead, describe(condition(c.GetStatus().GetConditions(), condAccepted)))

	po, pc := h.name("failopen", "open"), h.name("failopen", "close")
	open, closed := h.backend(t, po, po, false), h.backend(t, pc, pc, false)
	so := staticPool(po, open)
	so.FailureMode, so.SchedulerConfig = v1.FailureMode_FAILURE_MODE_FAIL_OPEN, cfg
	sc := staticPool(pc, closed)
	sc.FailureMode, sc.SchedulerConfig = v1.FailureMode_FAILURE_MODE_FAIL_CLOSE, cfg
	for id, spec := range map[string]*v1.PoolSpec{po: so, pc: sc} {
		_, err := r.putPool(ctx, id, spec)
		r.must("create pool "+id, err)
		_, err = r.putRoute(ctx, id, routeTo(id, &v1.RewriteTarget{PoolId: id, Weight: 1}))
		r.must("create route "+id, err)
		p, err := h.poolReady(ctx, id, 1)
		r.must("pool "+id+" ready", err)
		acc := condition(p.GetStatus().GetConditions(), condAccepted)
		r.check("pool "+id+" accepted with the remote config", isTrue(acc), "failure mode %s, Accepted %s", p.GetSpec().GetFailureMode(), describe(acc))
		_, err = h.routeReady(ctx, id)
		r.must("route "+id+" ready", err)
	}

	_, tries, err := h.until(ctx, po, nil, http.StatusOK)
	r.check("FAIL_OPEN pool serves with the scheduler down", err == nil, "first 200 after %d tries %v", tries, err)
	r.confined(ctx, "FAIL_OPEN pool keeps serving", po, nil, perCheck, []*backend{open, closed}, open)
	r.refused(ctx, "FAIL_CLOSE pool answers 503 with the scheduler down", pc, nil, http.StatusServiceUnavailable, perCheck, []*backend{open, closed})
}

// A route whose target pool is removed reports ResolvedRefs False with InvalidPoolRef, answers 503, and recovers
func TestInvalidPoolRef(t *testing.T) {
	h := suite
	r := h.feature(t, FeatureInvalidPoolRef)
	ctx := t.Context()
	model := h.name("invalidref")
	b := h.backend(t, model, model, false)
	spec := staticPool(model, b)
	r.servedPool(ctx, model, model, spec)

	r.must("remove the target pool", h.deletePool(ctx, model))
	rt, err := eventually(ctx, func(ctx context.Context) (*v1.Route, error) { return h.route(ctx, model) }, func(rt *v1.Route) (bool, string) {
		c := condition(rt.GetStatus().GetConditions(), condResolvedRefs)
		return isFalse(c, reasonInvalidPoolRef), "ResolvedRefs " + describe(c)
	})
	conds := rt.GetStatus().GetConditions()
	r.check("ResolvedRefs False with InvalidPoolRef", err == nil, "ResolvedRefs %s%s", describe(condition(conds, condResolvedRefs)), errSuffix(err))
	acc, ready := condition(conds, condAccepted), condition(conds, condReady)
	r.check("route stays Accepted", isTrue(acc), "Accepted %s", describe(acc))
	r.check("route not Ready", ready != nil && ready.GetStatus() == v1.ConditionStatus_CONDITION_STATUS_FALSE, "Ready %s", describe(ready))
	_, _, err = h.until(ctx, model, nil, http.StatusServiceUnavailable)
	r.must("route answers 503", err)
	r.refused(ctx, "accepted route with no pool answers 503", model, nil, http.StatusServiceUnavailable, perCheck/2, []*backend{b})

	_, err = r.putPool(ctx, model, spec)
	r.must("restore the target pool", err)
	_, err = h.routeReady(ctx, model)
	r.check("ResolvedRefs True once the pool returns", err == nil, "route %s%s", model, errSuffix(err))
	_, _, err = h.until(ctx, model, nil, http.StatusOK)
	r.must("restored route answers", err)
	r.confined(ctx, "restored route serves its pool", model, nil, perCheck/2, []*backend{b}, b)
}

func errSuffix(err error) string {
	if err == nil {
		return ""
	}
	return ", " + err.Error()
}

// A pool naming an unknown scheduler config reports UnknownSchedulerConfig, answers 503 even when failing open, and recovers
func TestMissingSchedulerRef(t *testing.T) {
	h := suite
	r := h.feature(t, FeatureMissingSchedulerRef)
	ctx := t.Context()
	cfg := h.name("missingref", "config")
	modes := []v1.FailureMode{v1.FailureMode_FAILURE_MODE_FAIL_OPEN, v1.FailureMode_FAILURE_MODE_UNSPECIFIED}
	var bs []*backend
	var ids []string
	for _, mode := range modes {
		id := h.name("missingref", strings.ToLower(strings.TrimPrefix(mode.String(), "FAILURE_MODE_")))
		b := h.backend(t, id, id, false)
		spec := staticPool(id, b)
		spec.FailureMode, spec.SchedulerConfig = mode, cfg
		_, err := r.putPool(ctx, id, spec)
		r.must("create pool "+id, err)
		_, err = r.putRoute(ctx, id, routeTo(id, &v1.RewriteTarget{PoolId: id, Weight: 1}))
		r.must("create route "+id, err)
		bs, ids = append(bs, b), append(ids, id)
	}
	for i, id := range ids {
		p, err := eventually(ctx, func(ctx context.Context) (*v1.Pool, error) { return h.pool(ctx, id) }, func(p *v1.Pool) (bool, string) {
			acc, refs := condition(p.GetStatus().GetConditions(), condAccepted), parentRefs(p)
			return isFalse(acc, reasonUnknownSchedulerConfig) && refs.ok(reasonUnknownSchedulerConfig, false), "Accepted " + describe(acc) + " parents " + refs.String()
		})
		acc, refs := condition(p.GetStatus().GetConditions(), condAccepted), parentRefs(p)
		r.check("pool "+id+" Accepted False with UnknownSchedulerConfig", isFalse(acc, reasonUnknownSchedulerConfig), "failure mode %s, Accepted %s%s", modes[i], describe(acc), errSuffix(err))
		r.check("pool "+id+" ResolvedRefs False with UnknownSchedulerConfig", refs.ok(reasonUnknownSchedulerConfig, false), "parents %s", refs)
		_, _, err = h.until(ctx, id, nil, http.StatusServiceUnavailable)
		r.must("route "+id+" answers 503", err)
		r.refused(ctx, "pool "+id+" fails closed under "+modes[i].String(), id, nil, http.StatusServiceUnavailable, perCheck/2, bs)
	}

	_, err := r.putConfig(ctx, cfg, schedulerYAML(""))
	r.must("create the missing scheduler config", err)
	for i, id := range ids {
		p, err := eventually(ctx, func(ctx context.Context) (*v1.Pool, error) { return h.pool(ctx, id) }, func(p *v1.Pool) (bool, string) {
			acc, refs := condition(p.GetStatus().GetConditions(), condAccepted), parentRefs(p)
			return isTrue(acc) && refs.ok("", true), "Accepted " + describe(acc) + " parents " + refs.String()
		})
		r.check("pool "+id+" Accepted once the config exists", err == nil, "Accepted %s parents %s%s", describe(condition(p.GetStatus().GetConditions(), condAccepted)), parentRefs(p), errSuffix(err))
		_, _, err = h.until(ctx, id, nil, http.StatusOK)
		r.must("route "+id+" answers once the config exists", err)
		r.confined(ctx, "pool "+id+" serves once the config exists", id, nil, perCheck/2, bs, bs[i])
	}
}

// The ResolvedRefs condition of every parent a pool reports against
type parents []*v1.Condition

func parentRefs(p *v1.Pool) parents {
	var out parents
	for _, ps := range p.GetStatus().GetParents() {
		out = append(out, condition(ps.GetConditions(), condResolvedRefs))
	}
	return out
}

// Whether at least one parent reports and every parent reports ResolvedRefs True, or False with the reason
func (ps parents) ok(reason string, resolved bool) bool {
	if len(ps) == 0 {
		return false
	}
	for _, c := range ps {
		if resolved && !isTrue(c) || !resolved && !isFalse(c, reason) {
			return false
		}
	}
	return true
}

func (ps parents) String() string {
	if len(ps) == 0 {
		return "none"
	}
	out := make([]string, len(ps))
	for i, c := range ps {
		out[i] = "ResolvedRefs " + describe(c)
	}
	return strings.Join(out, ", ")
}

// A pool created without app_protocol reads back HTTP and reaches an HTTP/1.1 stub over HTTP/1.1 while H2C speaks HTTP/2
func TestAppProtocolDefaults(t *testing.T) {
	h := suite
	r := h.feature(t, FeatureAppProtocolDefaults)
	ctx := t.Context()
	pd, ph := h.name("proto", "default"), h.name("proto", "h2c")
	plain, h2c := h.backend(t, pd, pd, false), h.backend(t, ph, ph, true)

	created, err := r.putPool(ctx, pd, staticPool(pd, plain))
	r.must("create pool without app_protocol", err)
	r.check("created pool reports APP_PROTOCOL_HTTP", created.GetSpec().GetAppProtocol() == v1.AppProtocol_APP_PROTOCOL_HTTP, "PutPool answered app_protocol %s", created.GetSpec().GetAppProtocol())
	got, err := h.pool(ctx, pd)
	r.must("read pool "+pd, err)
	r.check("pool reads back APP_PROTOCOL_HTTP", got.GetSpec().GetAppProtocol() == v1.AppProtocol_APP_PROTOCOL_HTTP, "GetPool answered app_protocol %s", got.GetSpec().GetAppProtocol())
	_, err = r.putRoute(ctx, pd, routeTo(pd, &v1.RewriteTarget{PoolId: pd, Weight: 1}))
	r.must("create route "+pd, err)
	_, err = h.poolReady(ctx, pd, 1)
	r.must("pool "+pd+" ready", err)
	_, _, err = h.until(ctx, pd, nil, http.StatusOK)
	r.must("default pool answers", err)
	r.confined(ctx, "HTTP/1.1 only stub serves the default pool", pd, nil, perCheck/2, []*backend{plain, h2c}, plain)
	protos := plain.byProto()
	r.check("default pool speaks HTTP/1.1", len(protos) == 1 && protos["HTTP/1.1"] > 0, "stub received requests by protocol %v", protos)

	hs := staticPool(ph, h2c)
	hs.AppProtocol = v1.AppProtocol_APP_PROTOCOL_H2C
	r.servedPool(ctx, ph, ph, hs)
	r.confined(ctx, "h2c stub serves the H2C pool", ph, nil, perCheck/2, []*backend{plain, h2c}, h2c)
	protos = h2c.byProto()
	r.check("H2C pool speaks HTTP/2 so the default is a choice", len(protos) == 1 && protos["HTTP/2.0"] > 0, "stub received requests by protocol %v", protos)
}
