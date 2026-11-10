package gateway

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/nickheyer/nebu/internal/mesh"
	"github.com/nickheyer/nebu/pkg/datalayer"
	"github.com/nickheyer/nebu/pkg/scheduling/framework"
	"github.com/nickheyer/nebu/pkg/scheduling/remote"
)

// Opens upstream connections, imported endpoints reached through the mesh when the daemon wires the mesh dialer
type Dialer = func(ctx context.Context, network, addr string) (net.Conn, error)

const (
	// The host suffix a scheduler URL carries to reach a member's scheduler over the mesh session
	meshHostSuffix = ".node.mesh"
	// A schedule call to an exporter's scheduler waits this long
	meshScheduleTimeout = 2 * time.Second
	// The profile a merged pick of local and exported endpoints is answered under
	meshProfile = "mesh"
)

// The URL a scheduler configuration names to reach a member's scheduler over the mesh session
func MeshSchedulerURL(nodeID string) string { return "https://" + nodeID + meshHostSuffix }

// The client a remote scheduler URL is reached with, the mesh session for a member host and the pinned certificate otherwise
func (g *Gateway) remoteHTTP(rawURL string) (*http.Client, error) {
	u, err := url.Parse(remote.ResolveURL(rawURL))
	if err == nil && strings.HasSuffix(u.Hostname(), meshHostSuffix) {
		return &http.Client{Transport: &meshRoundTripper{g: g}}, nil
	}
	t, err := remote.PinnedTransport(g.cfg.SchedulerCert)
	if err != nil {
		return nil, err
	}
	return &http.Client{Transport: t}, nil
}

// Sends a request addressed to a member host to the member's gateway base under the mesh session
type meshRoundTripper struct{ g *Gateway }

func (m *meshRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if m.g.peers == nil {
		return nil, errors.New("the scheduler is on another node and this node belongs to no mesh")
	}
	node := strings.TrimSuffix(req.URL.Hostname(), meshHostSuffix)
	base, hc, err := m.g.peers.Dial(req.Context(), node)
	if err != nil {
		return nil, err
	}
	target, err := url.Parse(base)
	if err != nil {
		return nil, err
	}
	target.Path, target.RawPath, target.RawQuery = req.URL.Path, req.URL.RawPath, req.URL.RawQuery
	out := req.Clone(req.Context())
	out.URL, out.Host = target, target.Host
	return hc.Transport.RoundTrip(out)
}

// The remote client of a member's scheduler, built once per node over the mesh session
func (g *Gateway) remoteFor(node string) (*remote.Client, error) {
	if v, ok := g.remotes.Load(node); ok {
		return v.(*remote.Client), nil
	}
	hc, err := g.remoteHTTP(MeshSchedulerURL(node))
	if err != nil {
		return nil, err
	}
	client, err := remote.NewClient(MeshSchedulerURL(node), hc, meshScheduleTimeout)
	if err != nil {
		return nil, err
	}
	v, _ := g.remotes.LoadOrStore(node, client)
	return v.(*remote.Client), nil
}

// Sends candidates an exporter's scheduler picks among to that exporter, the rest to the configuration's own scheduler, and merges the picks
type importAware struct {
	g     *Gateway
	name  string
	local framework.Scheduler
}

func (s *importAware) Profiles() map[string]*framework.Profile { return s.local.Profiles() }

func (s *importAware) Schedule(ctx context.Context, req *framework.Request, candidates []datalayer.Endpoint) (*framework.Result, error) {
	groups := map[string][]datalayer.Endpoint{}
	var locals []datalayer.Endpoint
	for _, ep := range candidates {
		if node := ep.Labels()[mesh.LabelRemoteScheduler]; node != "" {
			groups[node] = append(groups[node], ep)
			continue
		}
		locals = append(locals, ep)
	}
	if len(groups) == 0 {
		return s.local.Schedule(ctx, req, candidates)
	}
	var picks []*framework.Result
	var errs []error
	if len(locals) > 0 {
		res, err := s.local.Schedule(ctx, req, locals)
		if err != nil {
			errs = append(errs, err)
		} else {
			picks = append(picks, res)
		}
	}
	for _, node := range slices.Sorted(maps.Keys(groups)) {
		client, err := s.g.remoteFor(node)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		fallback, err := remote.NewFallback(counted{next: client, g: s.g}, s.local, func(err error) {
			s.g.log.Warn("exporter's scheduler unavailable, its endpoints scheduled in process", "config", s.name, "node", node, "err", err)
		})
		if err != nil {
			errs = append(errs, err)
			continue
		}
		res, err := fallback.Schedule(ctx, req, groups[node])
		if err != nil {
			errs = append(errs, fmt.Errorf("exporter %s: %w", node, err))
			continue
		}
		picks = append(picks, res)
	}
	if len(picks) == 0 {
		return nil, errors.Join(errs...)
	}
	return mergePicks(picks), nil
}

// One result over several, the primary endpoints taken in turn from each pick and every profile kept under its pick's state
func mergePicks(picks []*framework.Result) *framework.Result {
	if len(picks) == 1 {
		return picks[0]
	}
	out := &framework.Result{ProfileResults: map[string]*framework.ProfileRunResult{}, PrimaryProfile: meshProfile, State: picks[0].State}
	if out.State == nil {
		out.State = framework.NewCycleState()
	}
	merged := &framework.ProfileRunResult{Scores: map[datalayer.EndpointID]float64{}}
	lists := make([][]datalayer.Endpoint, len(picks))
	for i, p := range picks {
		lists[i] = p.Endpoints()
		out.FailedOpen = out.FailedOpen || p.FailedOpen
		for name, run := range p.ProfileResults {
			out.ProfileResults[name] = run
			maps.Copy(merged.Scores, run.Scores)
		}
	}
	for round := 0; ; round++ {
		took := false
		for _, list := range lists {
			if round < len(list) {
				merged.Endpoints = append(merged.Endpoints, list[round])
				took = true
			}
		}
		if !took {
			break
		}
	}
	out.ProfileResults[meshProfile] = merged
	return out
}

// Serves schedule calls for this node's schedulers, each answered by the configuration the candidates' pool runs
func (g *Gateway) ScheduleHandler() http.Handler {
	return remote.NewHandler(&poolScheduler{g: g}, g.store)
}

// Schedules freshly sampled candidates with the configuration their pool names, the generic default when it names none
type poolScheduler struct{ g *Gateway }

func (p *poolScheduler) Schedule(ctx context.Context, req *framework.Request, candidates []datalayer.Endpoint) (*framework.Result, error) {
	if len(candidates) == 0 {
		return nil, fmt.Errorf("%w: no candidate", framework.ErrNoEndpoints)
	}
	ids := make([]datalayer.EndpointID, len(candidates))
	for i, ep := range candidates {
		ids[i] = ep.ID()
	}
	defer p.g.sampler.Deciding(ctx, ids)()
	sampled := make([]datalayer.Endpoint, 0, len(candidates))
	for _, id := range ids {
		if ep, ok := p.g.store.Endpoint(id); ok {
			sampled = append(sampled, ep)
		}
	}
	if candidates = sampled; len(candidates) == 0 {
		return nil, fmt.Errorf("%w: every candidate left while it was sampled", framework.ErrNoEndpoints)
	}
	name := ""
	if pool, ok := p.g.store.Pool(candidates[0].Pool()); ok {
		name = pool.SchedulerConfig
	}
	return (&selector{h: p.g.hosting, name: name}).Schedule(ctx, req, candidates)
}

func (p *poolScheduler) Profiles() map[string]*framework.Profile {
	return map[string]*framework.Profile{}
}
