package gateway

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nickheyer/nebu/internal/mesh"
	"github.com/nickheyer/nebu/internal/pools"
	"github.com/nickheyer/nebu/pkg/datalayer"
	"github.com/nickheyer/nebu/pkg/scheduling/framework"
	"github.com/nickheyer/nebu/pkg/scheduling/remote"
)

// Picks the first candidate under one profile
type firstPicker struct{}

func (firstPicker) Schedule(_ context.Context, _ *framework.Request, candidates []datalayer.Endpoint) (*framework.Result, error) {
	if len(candidates) == 0 {
		return nil, framework.ErrNoEndpoints
	}
	return &framework.Result{PrimaryProfile: "main", ProfileResults: map[string]*framework.ProfileRunResult{"main": {Endpoints: candidates[:1]}}}, nil
}

func (firstPicker) Profiles() map[string]*framework.Profile { return map[string]*framework.Profile{} }

// The ids of endpoints in order
func endpointIDs(list []datalayer.Endpoint) []string {
	out := make([]string, len(list))
	for i, ep := range list {
		out[i] = string(ep.ID())
	}
	return out
}

// An importer with one local endpoint and one imported from n2 under the exported id, local candidate first
func importingHarness(t *testing.T, base string, client *http.Client, exportedID string) (*harness, []datalayer.Endpoint) {
	t.Helper()
	ctx := context.Background()
	h := newHarness(t, func(s *setup) { s.peers = &fakePeers{base: base, client: client} })
	mem := pools.Membership{Model: key("shared"), Runtime: "vllm", Protocol: datalayer.AppProtocolHTTP, Concurrency: 4}
	local := pools.OwnerRef{Kind: pools.OwnerInstance, ID: "loc"}
	if err := h.mgr.Publish(ctx, local, mem, []pools.Published{{InstanceID: "loc", NodeID: testNode, Address: datalayer.Address{Host: "127.0.0.1", Port: 7001}, Ready: true}}); err != nil {
		t.Fatal(err)
	}
	pool := pools.PoolIDOf(key("shared"), "vllm", "")
	imported := pools.OwnerRef{Kind: pools.OwnerNode, ID: "n2/" + string(pool)}
	labels := map[string]string{mesh.LabelRemoteScheduler: "n2", remote.LabelExportedID: exportedID}
	if err := h.mgr.Publish(ctx, imported, mem, []pools.Published{{InstanceID: "x", NodeID: "n2", Address: datalayer.Address{Host: "10.0.0.2", Port: 15001}, Ready: true, Labels: labels}}); err != nil {
		t.Fatal(err)
	}
	candidates := h.store.Endpoints(pool, nil)
	slices.SortFunc(candidates, func(a, b datalayer.Endpoint) int { return strings.Compare(string(a.ID()), string(b.ID())) })
	if got := endpointIDs(candidates); fmt.Sprint(got) != "[loc/7001 x/15001]" {
		t.Fatalf("candidates %v", got)
	}
	return h, candidates
}

// Sends an exporter's candidates to its scheduler by exported id over the mesh, schedules the rest here, merges in turn
func TestImportAwareAsksTheExporter(t *testing.T) {
	ctx := context.Background()
	exporter := newHarness(t)
	owner := pools.OwnerRef{Kind: pools.OwnerInstance, ID: "x"}
	mem := pools.Membership{Model: key("shared"), Runtime: "vllm", Protocol: datalayer.AppProtocolHTTP, Concurrency: 4}
	if err := exporter.mgr.Publish(ctx, owner, mem, []pools.Published{{InstanceID: "x", NodeID: "n2", Address: datalayer.Address{Host: "10.0.0.2", Port: 5001}, Ready: true}}); err != nil {
		t.Fatal(err)
	}
	sched := httptest.NewServer(exporter.g.ScheduleHandler())
	defer sched.Close()
	importer, candidates := importingHarness(t, sched.URL, sched.Client(), "x/5001")
	s := &importAware{g: importer.g, name: pools.SchedulerDefault, local: firstPicker{}}
	// The exporter's endpoint carries a fresh metrics sample, as its scraper keeps one, so its default configuration keeps it
	exporter.store.SetAttribute("x/5001", datalayer.CollectorMetrics, &datalayer.ServerMetrics{BlockSize: 16, NumGPUBlocks: 1024, UpdatedAt: time.Now()})
	res, err := s.Schedule(ctx, &framework.Request{ID: "r1"}, candidates)
	if err != nil {
		t.Fatal(err)
	}
	if got := endpointIDs(res.Endpoints()); res.PrimaryProfile != meshProfile || fmt.Sprint(got) != "[loc/7001 x/15001]" || res.FailedOpen {
		t.Fatalf("merged pick %v under %q failed open %v", got, res.PrimaryProfile, res.FailedOpen)
	}
	resp, err := http.Get(sched.URL + remote.MetricsPath)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), `nebu_scheduler_schedule_total{outcome="scheduled"} 1`) {
		t.Fatalf("the exporter answered no schedule call:\n%s", body)
	}
	// Candidates of no exporter never leave this node
	res, err = s.Schedule(ctx, &framework.Request{ID: "r2"}, candidates[:1])
	if err != nil || res.PrimaryProfile != "main" || fmt.Sprint(endpointIDs(res.Endpoints())) != "[loc/7001]" {
		t.Fatalf("local only pick %v %v", res, err)
	}
	if !strings.Contains(string(body), "nebu_scheduler_schedule_duration_seconds_count 1") {
		t.Fatalf("the exporter counted more than the one call:\n%s", body)
	}
}

// An unreachable exporter keeps its endpoints out of a fail close pick and hands them local when failing open
func TestImportAwareExporterUnreachable(t *testing.T) {
	ctx := context.Background()
	gone := httptest.NewServer(http.NotFoundHandler())
	gone.Close()
	importer, candidates := importingHarness(t, gone.URL, gone.Client(), "x/5001")
	s := &importAware{g: importer.g, name: pools.SchedulerDefault, local: firstPicker{}}
	res, err := s.Schedule(ctx, &framework.Request{ID: "closed"}, candidates)
	if err != nil || res.PrimaryProfile != "main" || fmt.Sprint(endpointIDs(res.Endpoints())) != "[loc/7001]" || res.FailedOpen {
		t.Fatalf("fail close pick %v %v", res, err)
	}
	res, err = s.Schedule(ctx, &framework.Request{ID: "open", FailOpen: true}, candidates)
	if err != nil || res.PrimaryProfile != meshProfile || fmt.Sprint(endpointIDs(res.Endpoints())) != "[loc/7001 x/15001]" || !res.FailedOpen {
		t.Fatalf("fail open pick %v %v", res, err)
	}
	// With no local candidate a fail close pick has nothing to answer
	if _, err := s.Schedule(ctx, &framework.Request{ID: "remote-only"}, candidates[1:]); !errors.Is(err, framework.ErrSchedulerUnavailable) {
		t.Fatalf("remote only fail close: %v", err)
	}
}

// A mesh host suffix URL goes through member sessions, any other through the pinned certificate transport
func TestRemoteHTTPPicksTheTransport(t *testing.T) {
	h := newHarness(t)
	hc, err := h.g.remoteHTTP(MeshSchedulerURL("n2"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := hc.Transport.(*meshRoundTripper); !ok {
		t.Fatalf("a member host dials over the mesh, got %T", hc.Transport)
	}
	hc, err = h.g.remoteHTTP("sched.internal")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := hc.Transport.(*http.Transport); !ok {
		t.Fatalf("a plain host dials over the pinned transport, got %T", hc.Transport)
	}
	if _, err := (&meshRoundTripper{g: h.g}).RoundTrip(httptest.NewRequest(http.MethodPost, MeshSchedulerURL("n2")+remote.SchedulePath, nil)); err == nil {
		t.Fatal("a node in no mesh reached a member's scheduler")
	}
}
