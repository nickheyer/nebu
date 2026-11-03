package bench

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net"
	"net/http"
	"slices"
	"strconv"
	"sync/atomic"
	"time"

	"connectrpc.com/connect"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/proto/nebu/v1/nebuv1connect"
	"github.com/nickheyer/nebu/pkg/stub"
)

// Target ports a pool takes at most, as proto/nebu/v1/pool.proto bounds them
const maxPoolPorts = 8

// Runtimes the stub emulates, each reporting resident adapters through the gateway scrape
var stubModes = []stub.Names{stub.NamesVLLM, stub.NamesSGLang, stub.NamesLlamaCpp}

// Defaults a local run starts from
const (
	defaultLocalPool    = "nebu-bench"
	defaultLocalHost    = "127.0.0.1"
	defaultLocalRuntime = "vllm"
)

// How StartLocal serves stubs and joins them to the gateway, zero values selecting the default named on each field
type LocalOptions struct {
	// Stub backends started, one to eight since a pool takes at most eight target ports
	Stubs int
	// Settings every stub starts from, its id numbered per stub with the trace's adapters made resident
	Stub stub.Config
	// Host the stubs listen on and the gateway dials, default 127.0.0.1
	Host string
	// Id of the user authored pool holding the stubs, default nebu-bench
	Pool string
	// Runtime the pool names and whose metric names the stubs serve, vllm, sglang or llamacpp, default vllm
	Runtime string
	// Longest wait for the pool to report every stub ready, default one minute
	ReadyTimeout time.Duration
}

// Fills zero fields and refuses settings StartLocal cannot serve
func (o LocalOptions) withDefaults() (LocalOptions, error) {
	if o.Host == "" {
		o.Host = defaultLocalHost
	}
	if o.Pool == "" {
		o.Pool = defaultLocalPool
	}
	if o.Runtime == "" {
		o.Runtime = defaultLocalRuntime
	}
	if o.ReadyTimeout == 0 {
		o.ReadyTimeout = time.Minute
	}
	if o.Stub.Model == "" {
		o.Stub.Model = "stub-model"
	}
	switch {
	case o.Stubs < 1 || o.Stubs > maxPoolPorts:
		return o, fmt.Errorf("%d stubs asked for, a pool takes one to %d", o.Stubs, maxPoolPorts)
	case o.ReadyTimeout < 0:
		return o, fmt.Errorf("ready timeout %v is negative", o.ReadyTimeout)
	case len(o.Stub.Adapters) > 0:
		return o, errors.New("stub adapters come from the trace, leave them unset")
	case !slices.Contains(stubModes, stub.Names(o.Runtime)):
		return o, fmt.Errorf("runtime %s is none the stub emulates, use vllm, sglang or llamacpp", o.Runtime)
	case o.Stub.Names != "" && o.Stub.Names != stub.Names(o.Runtime):
		return o, fmt.Errorf("stub metric names %s differ from runtime %s, leave them unset", o.Stub.Names, o.Runtime)
	}
	o.Stub.Names = stub.Names(o.Runtime)
	return o, nil
}

// One stub listener whose emulated server can be swapped for a fresh one
type localStub struct {
	ln  net.Listener
	srv *http.Server
	cur atomic.Pointer[stub.Stub]
}

func (s *localStub) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.cur.Load().ServeHTTP(w, r) }

// Stub backends served in process behind one user authored pool and a route per base model name a trace sends
type Local struct {
	api      nebuv1connect.GatewayServiceClient
	opts     LocalOptions
	cfg      stub.Config
	adapters []string
	routes   []string
	made     []string
	stubs    []*localStub
	pooled   bool
}

// Starts the stubs, creates the pool and routes the trace needs, and waits until the gateway sees every stub ready
func StartLocal(ctx context.Context, api nebuv1connect.GatewayServiceClient, reqs []Request, opts LocalOptions) (*Local, error) {
	opts, err := opts.withDefaults()
	if err != nil {
		return nil, err
	}
	l := &Local{api: api, opts: opts, cfg: opts.Stub}
	names := map[string]bool{}
	adapters := map[string]bool{}
	objectives := map[string]bool{}
	for i, r := range reqs {
		if slices.ContainsFunc(r.Messages, func(m Message) bool { return len(m.Images) > 0 }) {
			return nil, fmt.Errorf("request %d sends images and the stubs serve text alone, replay it against a pool that takes images", i)
		}
		switch {
		case r.Adapter != "":
			adapters[r.Adapter] = true
		case r.Model != "" && r.Model != opts.Stub.Model:
			names[r.Model] = true
		}
		if _, aerr := strconv.Atoi(r.Objective); r.Objective != "" && aerr != nil {
			objectives[r.Objective] = true
		}
	}
	l.adapters, l.routes = slices.Sorted(maps.Keys(adapters)), slices.Sorted(maps.Keys(names))
	for _, a := range l.adapters {
		l.cfg.Adapters = append(l.cfg.Adapters, stub.Adapter{Name: a, Path: "/nebu-bench/adapters/" + a})
	}
	l.cfg.MaxLoras = max(l.cfg.MaxLoras, len(l.adapters))
	if err := l.preflight(ctx, objectives); err != nil {
		return nil, err
	}
	if err := l.start(ctx); err != nil {
		return nil, errors.Join(err, l.Close(context.WithoutCancel(ctx)))
	}
	return l, nil
}

// Refuses a pool or route the gateway already holds and named objectives it does not define
func (l *Local) preflight(ctx context.Context, objectives map[string]bool) error {
	if _, err := l.api.GetPool(ctx, connect.NewRequest(&v1.GetPoolRequest{Id: l.opts.Pool})); err == nil {
		return fmt.Errorf("the gateway already holds pool %s, name another with the pool option", l.opts.Pool)
	} else if connect.CodeOf(err) != connect.CodeNotFound {
		return fmt.Errorf("reading pool %s: %w", l.opts.Pool, err)
	}
	routes, err := l.api.ListRoutes(ctx, connect.NewRequest(&v1.ListRoutesRequest{}))
	if err != nil {
		return fmt.Errorf("listing routes: %w", err)
	}
	for _, rt := range routes.Msg.GetRoutes() {
		if slices.Contains(l.routes, rt.GetName()) {
			return fmt.Errorf("the gateway already routes %s elsewhere, replay against it with the replay command", rt.GetName())
		}
	}
	defined, err := l.api.ListObjectives(ctx, connect.NewRequest(&v1.ListObjectivesRequest{}))
	if err != nil {
		return fmt.Errorf("listing objectives: %w", err)
	}
	for _, o := range defined.Msg.GetObjectives() {
		delete(objectives, o.GetName())
	}
	if len(objectives) > 0 {
		return fmt.Errorf("the trace names objectives the gateway does not define, %v", slices.Sorted(maps.Keys(objectives)))
	}
	return nil
}

// Listens and serves every stub, then creates the pool and routes and waits for every stub to be ready
func (l *Local) start(ctx context.Context) error {
	spec := &v1.PoolSpec{Model: l.cfg.Model, RuntimeId: l.opts.Runtime}
	for i := range l.opts.Stubs {
		st, err := l.fresh(i)
		if err != nil {
			return err
		}
		ln, err := net.Listen("tcp", net.JoinHostPort(l.opts.Host, "0"))
		if err != nil {
			st.Close()
			return err
		}
		ls := &localStub{ln: ln}
		ls.cur.Store(st)
		ls.srv = &http.Server{Handler: ls, ReadHeaderTimeout: 10 * time.Second}
		go ls.srv.Serve(ln)
		l.stubs = append(l.stubs, ls)
		port := ln.Addr().(*net.TCPAddr).Port
		spec.TargetPorts = append(spec.TargetPorts, uint32(port))
		spec.StaticEndpoints = append(spec.StaticEndpoints, &v1.StaticEndpoint{Address: net.JoinHostPort(l.opts.Host, strconv.Itoa(port)), KvCapacity: uint32(st.KVCapacity())})
	}
	if _, err := l.api.PutPool(ctx, connect.NewRequest(&v1.PutPoolRequest{Id: l.opts.Pool, Spec: spec})); err != nil {
		return fmt.Errorf("creating pool %s: %w", l.opts.Pool, err)
	}
	l.pooled = true
	for _, name := range l.routes {
		rule := &v1.RewriteRule{Targets: []*v1.RewriteTarget{{PoolId: l.opts.Pool, Model: l.cfg.Model, Weight: 1}}}
		if _, err := l.api.PutRoute(ctx, connect.NewRequest(&v1.PutRouteRequest{Name: name, Spec: &v1.RouteSpec{Rules: []*v1.RewriteRule{rule}}})); err != nil {
			return fmt.Errorf("creating route %s: %w", name, err)
		}
		l.made = append(l.made, name)
	}
	return l.await(ctx, nil)
}

// A new stub numbered i with the trace's adapters resident
func (l *Local) fresh(i int) (*stub.Stub, error) {
	cfg := l.cfg
	cfg.ID = "nebu-bench-" + strconv.Itoa(i)
	cfg.Adapters = slices.Clone(l.cfg.Adapters)
	return stub.New(cfg)
}

// Waits until every stub is ready, healthy, running each adapter and scraped after the times given
func (l *Local) await(ctx context.Context, after map[string]time.Time) error {
	return WaitPool(ctx, l.api, l.opts.Pool, l.opts.ReadyTimeout, func(p *v1.Pool) (bool, error) {
		if ok, err := allReady(p); !ok || err != nil {
			return false, err
		}
		eps := p.GetStatus().GetEndpoints()
		if len(eps) != len(l.stubs) {
			return false, nil
		}
		for _, ep := range eps {
			m := ep.GetMetrics()
			if m.GetUpdatedAt() == nil || !m.GetUpdatedAt().AsTime().After(after[ep.GetId()]) {
				return false, nil
			}
			for _, a := range l.adapters {
				if !slices.Contains(m.GetRunningAdapters(), a) {
					return false, nil
				}
			}
		}
		return true, nil
	})
}

// When the gateway last scraped each endpoint of the pool, by endpoint id
func (l *Local) scrapedAt(ctx context.Context) (map[string]time.Time, error) {
	got, err := l.api.GetPool(ctx, connect.NewRequest(&v1.GetPoolRequest{Id: l.opts.Pool}))
	if err != nil {
		return nil, fmt.Errorf("reading pool %s: %w", l.opts.Pool, err)
	}
	out := map[string]time.Time{}
	for _, ep := range got.Msg.GetPool().GetStatus().GetEndpoints() {
		out[ep.GetId()] = ep.GetMetrics().GetUpdatedAt().AsTime()
	}
	return out, nil
}

// Replaces every stub with a fresh one, then waits for two gateway scrapes so no metric predates the swap
func (l *Local) Reset(ctx context.Context) error {
	for i, ls := range l.stubs {
		st, err := l.fresh(i)
		if err != nil {
			return err
		}
		ls.cur.Swap(st).Close()
	}
	for range 2 {
		seen, err := l.scrapedAt(ctx)
		if err != nil {
			return err
		}
		if err := l.await(ctx, seen); err != nil {
			return err
		}
	}
	return nil
}

// Pool the stubs serve behind
func (l *Local) Pool() string { return l.opts.Pool }

// Routes created for the trace's base model names
func (l *Local) Routes() []string { return slices.Clone(l.routes) }

// Addresses the stubs listen on
func (l *Local) Addresses() []string {
	out := make([]string, len(l.stubs))
	for i, ls := range l.stubs {
		out[i] = ls.ln.Addr().String()
	}
	return out
}

// Deletes the routes and pool it created and stops every stub
func (l *Local) Close(ctx context.Context) error {
	var errs []error
	for _, name := range l.made {
		if _, err := l.api.DeleteRoute(ctx, connect.NewRequest(&v1.DeleteRouteRequest{Name: name})); err != nil {
			errs = append(errs, fmt.Errorf("deleting route %s: %w", name, err))
		}
	}
	l.made = nil
	if l.pooled {
		if _, err := l.api.DeletePool(ctx, connect.NewRequest(&v1.DeletePoolRequest{Id: l.opts.Pool})); err != nil {
			errs = append(errs, fmt.Errorf("deleting pool %s: %w", l.opts.Pool, err))
		}
		l.pooled = false
	}
	for _, ls := range l.stubs {
		if err := ls.srv.Shutdown(ctx); err != nil {
			errs = append(errs, err)
		}
		ls.cur.Load().Close()
	}
	l.stubs = nil
	return errors.Join(errs...)
}
