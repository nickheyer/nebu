package bench

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"slices"
	"sync"
	"testing"

	"connectrpc.com/connect"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/proto/nebu/v1/nebuv1connect"
	"github.com/nickheyer/nebu/pkg/requestcontrol"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func newTestRand(seed uint64) *rand.Rand { return rand.New(rand.NewPCG(seed, seed)) }

// In memory gateway API with a chat endpoint proxying to pool endpoints in turn, scraping them on reads
type fakeGateway struct {
	nebuv1connect.UnimplementedGatewayServiceHandler
	mu         sync.Mutex
	pools      map[string]*v1.Pool
	routes     map[string]*v1.Route
	configs    []string
	broken     map[string]bool
	objectives []string
	traces     []*v1.Trace
	// Scheduler configuration of the serving pool at each chat request, in arrival order
	served []string
	turn   int
	url    string
	api    nebuv1connect.GatewayServiceClient
}

// Starts a fake gateway holding the named scheduler configurations
func newFakeGateway(t *testing.T, configs ...string) *fakeGateway {
	t.Helper()
	g := &fakeGateway{pools: map[string]*v1.Pool{}, routes: map[string]*v1.Route{}, configs: configs, broken: map[string]bool{}}
	mux := http.NewServeMux()
	mux.Handle(nebuv1connect.NewGatewayServiceHandler(g))
	mux.HandleFunc("POST /v1/chat/completions", g.chat)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	g.url = srv.URL
	g.api = nebuv1connect.NewGatewayServiceClient(srv.Client(), srv.URL)
	return g
}

func notFound(kind, name string) error {
	return connect.NewError(connect.CodeNotFound, fmt.Errorf("%s %s not found", kind, name))
}

func (g *fakeGateway) ListSchedulerConfigs(context.Context, *connect.Request[v1.ListSchedulerConfigsRequest]) (*connect.Response[v1.ListSchedulerConfigsResponse], error) {
	out := &v1.ListSchedulerConfigsResponse{}
	for _, c := range g.configs {
		out.Configs = append(out.Configs, &v1.SchedulerConfig{Name: c, Spec: &v1.SchedulerConfigSpec{Yaml: "x"}})
	}
	return connect.NewResponse(out), nil
}

func (g *fakeGateway) ListObjectives(context.Context, *connect.Request[v1.ListObjectivesRequest]) (*connect.Response[v1.ListObjectivesResponse], error) {
	out := &v1.ListObjectivesResponse{}
	for _, o := range g.objectives {
		out.Objectives = append(out.Objectives, &v1.Objective{Name: o})
	}
	return connect.NewResponse(out), nil
}

func (g *fakeGateway) PutPool(ctx context.Context, req *connect.Request[v1.PutPoolRequest]) (*connect.Response[v1.PutPoolResponse], error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	spec := proto.Clone(req.Msg.GetSpec()).(*v1.PoolSpec)
	if spec.SchedulerConfig == "" {
		spec.SchedulerConfig = "default"
	}
	p := g.pools[req.Msg.GetId()]
	if p == nil {
		p = &v1.Pool{Id: req.Msg.GetId(), Meta: &v1.ObjectMeta{}}
		g.pools[p.Id] = p
	}
	p.Meta.Generation++
	p.Spec = spec
	return connect.NewResponse(&v1.PutPoolResponse{Pool: proto.Clone(p).(*v1.Pool)}), nil
}

func (g *fakeGateway) DeletePool(ctx context.Context, req *connect.Request[v1.DeletePoolRequest]) (*connect.Response[v1.DeletePoolResponse], error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	p, ok := g.pools[req.Msg.GetId()]
	if !ok {
		return nil, notFound("pool", req.Msg.GetId())
	}
	delete(g.pools, p.Id)
	return connect.NewResponse(&v1.DeletePoolResponse{Pool: p}), nil
}

// Scrapes every static endpoint of the pool and reports conditions at its generation
func (g *fakeGateway) GetPool(ctx context.Context, req *connect.Request[v1.GetPoolRequest]) (*connect.Response[v1.GetPoolResponse], error) {
	g.mu.Lock()
	p, ok := g.pools[req.Msg.GetId()]
	if ok {
		p = proto.Clone(p).(*v1.Pool)
	}
	broken := g.broken[p.GetSpec().GetSchedulerConfig()]
	g.mu.Unlock()
	if !ok {
		return nil, notFound("pool", req.Msg.GetId())
	}
	st := &v1.PoolStatus{}
	for _, se := range p.GetSpec().GetStaticEndpoints() {
		ep := &v1.Endpoint{Id: p.Id + "/" + se.GetAddress(), PoolId: p.Id, Address: se.GetAddress(), Metrics: &v1.EndpointMetrics{}}
		if adapters, err := scrape(ctx, se.GetAddress()); err == nil {
			ep.Ready, ep.Healthy = true, true
			ep.Metrics.RunningAdapters, ep.Metrics.UpdatedAt = adapters, timestamppb.Now()
			st.ReadyEndpoints++
		}
		st.TotalEndpoints++
		st.Endpoints = append(st.Endpoints, ep)
	}
	gen := p.GetMeta().GetGeneration()
	cond := func(typ string, ok bool, msg string) *v1.Condition {
		s := v1.ConditionStatus_CONDITION_STATUS_TRUE
		if !ok {
			s = v1.ConditionStatus_CONDITION_STATUS_FALSE
		}
		return &v1.Condition{Type: typ, Status: s, Message: msg, ObservedGeneration: gen}
	}
	st.Conditions = []*v1.Condition{cond(conditionAccepted, true, "valid"), cond(conditionReady, st.ReadyEndpoints > 0, "ready")}
	st.Parents = []*v1.ParentStatus{{Conditions: []*v1.Condition{cond(conditionResolvedRefs, !broken, "scheduler config "+p.GetSpec().GetSchedulerConfig()+" fails to load")}}}
	p.Status = st
	return connect.NewResponse(&v1.GetPoolResponse{Pool: p}), nil
}

// The adapters a stub reports resident, as a scrape of its model list
func scrape(ctx context.Context, addr string) ([]string, error) {
	get := func(path string) (*http.Response, error) {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+path, nil)
		resp, err := http.DefaultClient.Do(req)
		if err == nil && resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			err = fmt.Errorf("%s answered %d", path, resp.StatusCode)
		}
		return resp, err
	}
	health, err := get("/health")
	if err != nil {
		return nil, err
	}
	health.Body.Close()
	resp, err := get("/v1/models")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var list struct {
		Data []struct {
			ID     string  `json:"id"`
			Parent *string `json:"parent"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return nil, err
	}
	var out []string
	for _, m := range list.Data {
		if m.Parent != nil {
			out = append(out, m.ID)
		}
	}
	return out, nil
}

func (g *fakeGateway) ListPools(context.Context, *connect.Request[v1.ListPoolsRequest]) (*connect.Response[v1.ListPoolsResponse], error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := &v1.ListPoolsResponse{}
	for _, p := range g.pools {
		out.Pools = append(out.Pools, proto.Clone(p).(*v1.Pool))
	}
	return connect.NewResponse(out), nil
}

func (g *fakeGateway) PutRoute(ctx context.Context, req *connect.Request[v1.PutRouteRequest]) (*connect.Response[v1.PutRouteResponse], error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	rt := &v1.Route{Name: req.Msg.GetName(), Spec: req.Msg.GetSpec()}
	g.routes[rt.Name] = rt
	return connect.NewResponse(&v1.PutRouteResponse{Route: rt}), nil
}

func (g *fakeGateway) DeleteRoute(ctx context.Context, req *connect.Request[v1.DeleteRouteRequest]) (*connect.Response[v1.DeleteRouteResponse], error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	rt, ok := g.routes[req.Msg.GetName()]
	if !ok {
		return nil, notFound("route", req.Msg.GetName())
	}
	delete(g.routes, rt.Name)
	return connect.NewResponse(&v1.DeleteRouteResponse{Route: rt}), nil
}

func (g *fakeGateway) ListRoutes(context.Context, *connect.Request[v1.ListRoutesRequest]) (*connect.Response[v1.ListRoutesResponse], error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := &v1.ListRoutesResponse{}
	for _, rt := range g.routes {
		out.Routes = append(out.Routes, rt)
	}
	return connect.NewResponse(out), nil
}

// Lists traces newest first without bodies, the route's alone when one is named
func (g *fakeGateway) ListTraces(ctx context.Context, req *connect.Request[v1.ListTracesRequest]) (*connect.Response[v1.ListTracesResponse], error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := &v1.ListTracesResponse{}
	for _, t := range slices.Backward(g.traces) {
		if req.Msg.GetRoute() != "" && t.GetRoute() != req.Msg.GetRoute() {
			continue
		}
		if req.Msg.GetLimit() > 0 && len(out.Traces) == int(req.Msg.GetLimit()) {
			break
		}
		bare := proto.Clone(t).(*v1.Trace)
		bare.Request, bare.UpstreamRequest, bare.Response = "", "", ""
		out.Traces = append(out.Traces, bare)
	}
	return connect.NewResponse(out), nil
}

func (g *fakeGateway) GetTrace(ctx context.Context, req *connect.Request[v1.GetTraceRequest]) (*connect.Response[v1.GetTraceResponse], error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, t := range g.traces {
		if t.GetId() == req.Msg.GetId() {
			return connect.NewResponse(&v1.GetTraceResponse{Trace: proto.Clone(t).(*v1.Trace)}), nil
		}
	}
	return nil, notFound("trace", req.Msg.GetId())
}

// Resolves the model by route rewrite, base model or resident adapter and proxies to the pool's endpoints in turn
func (g *fakeGateway) chat(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var req struct {
		Model string `json:"model"`
	}
	json.Unmarshal(body, &req)
	g.mu.Lock()
	var pool *v1.Pool
	if rt, ok := g.routes[req.Model]; ok {
		target := rt.GetSpec().GetRules()[0].GetTargets()[0]
		pool = g.pools[target.GetPoolId()]
		var doc map[string]any
		json.Unmarshal(body, &doc)
		doc["model"] = target.GetModel()
		body, _ = json.Marshal(doc)
	}
	for _, p := range g.pools {
		if pool == nil && p.GetSpec().GetModel() == req.Model {
			pool = p
		}
	}
	if pool == nil {
		for _, p := range g.pools {
			if adapters, err := scrape(r.Context(), p.GetSpec().GetStaticEndpoints()[0].GetAddress()); err == nil && slices.Contains(adapters, req.Model) {
				pool = p
			}
		}
	}
	if pool == nil {
		g.mu.Unlock()
		http.Error(w, `{"error":{"message":"unknown model"}}`, http.StatusNotFound)
		return
	}
	eps := pool.GetSpec().GetStaticEndpoints()
	addr := eps[g.turn%len(eps)].GetAddress()
	g.turn++
	g.served = append(g.served, pool.GetSpec().GetSchedulerConfig())
	g.mu.Unlock()
	w.Header().Set(requestcontrol.HeaderServed, addr)
	r.Body, r.ContentLength = io.NopCloser(bytes.NewReader(body)), int64(len(body))
	proxy := httputil.NewSingleHostReverseProxy(&url.URL{Scheme: "http", Host: addr})
	proxy.FlushInterval = -1
	proxy.ServeHTTP(w, r)
}

// Runs fn holding the gateway's lock, for tests reading or changing its state
func (g *fakeGateway) locked(fn func()) {
	g.mu.Lock()
	defer g.mu.Unlock()
	fn()
}

// Scheduler configurations of the requests served so far
func (g *fakeGateway) servedConfigs() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return slices.Clone(g.served)
}
