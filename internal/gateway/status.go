package gateway

import (
	"errors"
	"sync/atomic"
	"time"

	"github.com/nickheyer/nebu/internal/pools"
	"github.com/nickheyer/nebu/pkg/datalayer"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/requestcontrol"
	"github.com/nickheyer/nebu/pkg/scheduling/framework"
	"google.golang.org/protobuf/proto"
)

// Counters of the requests one pool's target model took
type outcomes struct {
	requests, errors, judged, met, ttfts, tpots atomic.Uint64
	ttft, tpot                                  atomic.Int64
}

// The pool and target model a decision sent its request to
type targetKey struct {
	pool  datalayer.PoolID
	model string
}

// Counts a finished request against the pool and target model its decision named
func (g *Gateway) tally(dec *requestcontrol.Decision, resp *framework.Response) {
	k := targetKey{pool: dec.Pool.ID, model: dec.TargetModel}
	v, ok := g.outcomes.Load(k)
	if !ok {
		v, _ = g.outcomes.LoadOrStore(k, &outcomes{})
	}
	o := v.(*outcomes)
	o.requests.Add(1)
	if resp.Err != nil {
		o.errors.Add(1)
	}
	if obj := dec.Request.Objective; obj.TTFT > 0 || obj.TPOT > 0 {
		o.judged.Add(1)
		if attained(obj.TTFT, obj.TPOT, resp) {
			o.met.Add(1)
		}
	}
	if d := resp.TTFT(); d > 0 {
		o.ttft.Add(int64(d))
		o.ttfts.Add(1)
	}
	if d := resp.TPOT(); d > 0 {
		o.tpot.Add(int64(d))
		o.tpots.Add(1)
	}
}

// What the requests sent to a pool's target model came to since the gateway started, for canary swaps
func (g *Gateway) Outcome(pool datalayer.PoolID, model string) pools.Outcome {
	v, ok := g.outcomes.Load(targetKey{pool: pool, model: model})
	if !ok {
		return pools.Outcome{}
	}
	o := v.(*outcomes)
	return pools.Outcome{
		Requests: o.requests.Load(),
		Errors:   o.errors.Load(),
		Judged:   o.judged.Load(),
		Met:      o.met.Load(),
		TTFT:     time.Duration(o.ttft.Load()),
		TTFTs:    o.ttfts.Load(),
		TPOT:     time.Duration(o.tpot.Load()),
		TPOTs:    o.tpots.Load(),
	}
}

// The name this gateway reports itself under in its status
const Implementation = "nebu"

// The gateway's listeners, routes, pools, auth, totals, policy, and the implementation and version answering
func (g *Gateway) Status() *v1.GatewayStatus {
	l := g.listening()
	out := &v1.GatewayStatus{
		Listeners:      l.list,
		Auth:           g.required(),
		Requests:       g.total.Load(),
		Policy:         proto.Clone(effectivePolicy(nil, g.policy())).(*v1.Policy),
		Tls:            l.tls,
		Pools:          g.pools.Pools(),
		Routes:         g.Routes(),
		Implementation: Implementation,
		Version:        g.cfg.Version,
	}
	if len(l.list) > 0 {
		out.MetricsAddr = l.list[0].GetAddr() + "/metrics"
	}
	return out
}

// Every route with the requests this gateway sent it and holds in flight on it
func (g *Gateway) Routes() []*v1.Route {
	list := g.pools.Routes()
	for _, rt := range list {
		g.counted(rt)
	}
	return list
}

// One route with this gateway's request counts, false when no route has the name
func (g *Gateway) Route(name string) (*v1.Route, bool) {
	rt, ok := g.pools.Route(name)
	if !ok {
		return nil, false
	}
	g.counted(rt)
	return rt, true
}

// Writes this gateway's request and in flight counts into a route the view handed out
func (g *Gateway) counted(rt *v1.Route) {
	v, ok := g.routes.Load(rt.GetName())
	if !ok {
		return
	}
	st := v.(*routeState)
	if rt.Status == nil {
		rt.Status = &v1.RouteStatus{}
	}
	rt.Status.Requests, rt.Status.InFlight = st.requests.Load(), uint32(max(st.inflight.Load(), 0))
}

// Refuses a profile that offers mutating tools with no tool server to take them from
func CheckProfile(p *v1.Profile) error {
	if p.GetMutatingTools() && len(p.GetToolServers()) == 0 {
		return errors.New("mutating tools need at least one tool server")
	}
	return nil
}

// Whether a profile sets anything, an empty one inheriting everything
func ProfileSet(p *v1.Profile) bool {
	return len(p.GetToolServers()) > 0 || p.GetMutatingTools()
}

// Merges an alias profile over its slot's, every alias field that is set winning
func EffectiveProfile(alias, slot *v1.Profile) *v1.Profile {
	if !ProfileSet(alias) {
		return slot
	}
	out := &v1.Profile{ToolServers: append([]string(nil), slot.GetToolServers()...), MutatingTools: slot.GetMutatingTools()}
	if len(alias.GetToolServers()) > 0 {
		out.ToolServers, out.MutatingTools = append([]string(nil), alias.GetToolServers()...), alias.GetMutatingTools()
	}
	return out
}
