package requestcontrol

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/nickheyer/nebu/pkg/datalayer"
	"github.com/nickheyer/nebu/pkg/scheduling/framework"
)

// Records every lease taken and released in order
type leaseSampler struct {
	mu     sync.Mutex
	events []string
}

func (s *leaseSampler) add(event string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, event)
}

func (s *leaseSampler) list() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.events)
}

func (s *leaseSampler) Deciding(_ context.Context, ids []datalayer.EndpointID) func() {
	names := make([]string, len(ids))
	for i, id := range ids {
		names[i] = string(id)
	}
	joined := strings.Join(names, ",")
	s.add("deciding " + joined)
	return func() { s.add("decided " + joined) }
}

func (s *leaseSampler) Serving(id datalayer.EndpointID) func() {
	s.add("serving " + string(id))
	return func() { s.add("served " + string(id)) }
}

func (s *leaseSampler) Sample(context.Context, []datalayer.EndpointID) {}

// An environment whose director leases samples from a recorder the scheduler also writes its runs to
func leasedEnv(t *testing.T) (*env, *leaseSampler) {
	t.Helper()
	s := &leaseSampler{}
	e := newEnv(t, func(d *Deps) { d.Sampler = s })
	e.store.UpsertEndpoint(endpoint("e3", "p1", "10.0.0.3", false))
	e.sched.fn = func(ctx context.Context, req *framework.Request, candidates []datalayer.Endpoint) (*framework.Result, error) {
		s.add("schedule")
		return pickAll(ctx, req, candidates)
	}
	return e, s
}

// The pool's ready endpoints are sampled while the request is admitted and scheduled, then its endpoint while it is in flight
func TestDecisionLeasesSamples(t *testing.T) {
	e, s := leasedEnv(t)
	dec, err := e.dir.Decide(context.Background(), inbound(t, chat("llama")))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"deciding e1,e2", "schedule", "decided e1,e2", "serving e1"}
	if got := s.list(); !slices.Equal(got, want) {
		t.Fatalf("leases %v, want %v", got, want)
	}
	second, err := e.dir.Decide(context.Background(), inbound(t, chat("llama")))
	if err != nil {
		t.Fatal(err)
	}
	e.dir.ResponseComplete(context.Background(), dec, nil)
	want = append(want, "deciding e1,e2", "schedule", "decided e1,e2")
	if got := s.list(); !slices.Equal(got, want) {
		t.Fatalf("leases with a second request in flight on the endpoint %v, want %v", got, want)
	}
	e.dir.ResponseComplete(context.Background(), second, nil)
	e.dir.ResponseComplete(context.Background(), second, nil)
	if got := s.list(); !slices.Equal(got, append(want, "served e1")) {
		t.Fatalf("leases after the last request ended %v", got)
	}
}

// The in flight lease follows the request to the endpoint that answered it
func TestServingLeaseFollowsTheServingEndpoint(t *testing.T) {
	e, s := leasedEnv(t)
	dec, err := e.dir.Decide(context.Background(), inbound(t, chat("llama")))
	if err != nil {
		t.Fatal(err)
	}
	if err := e.dir.ResponseReceived(context.Background(), dec, &framework.Response{Endpoint: dec.Endpoints[1]}, http.Header{}); err != nil {
		t.Fatal(err)
	}
	e.dir.ResponseComplete(context.Background(), dec, nil)
	want := []string{"deciding e1,e2", "schedule", "decided e1,e2", "serving e1", "served e1", "serving e2", "served e2"}
	if got := s.list(); !slices.Equal(got, want) {
		t.Fatalf("leases %v, want %v", got, want)
	}
}

// A request the scheduler refuses gives its deciding lease back and takes none for a request in flight
func TestRefusedDecisionReleasesItsLease(t *testing.T) {
	e, s := leasedEnv(t)
	boom := errors.New("boom")
	e.sched.fn = func(context.Context, *framework.Request, []datalayer.Endpoint) (*framework.Result, error) {
		s.add("schedule")
		return nil, boom
	}
	if _, err := e.dir.Decide(context.Background(), inbound(t, chat("llama"))); !errors.Is(err, boom) {
		t.Fatalf("err %v, want the scheduler's", err)
	}
	if got, want := s.list(), []string{"deciding e1,e2", "schedule", "decided e1,e2"}; !slices.Equal(got, want) {
		t.Fatalf("leases %v, want %v", got, want)
	}
}
