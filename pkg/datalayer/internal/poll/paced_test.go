package poll

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nickheyer/nebu/pkg/datalayer"
)

// Counts samples per endpoint, reports work for the endpoints marked working and holds samples while gated
type sampler struct {
	mu      sync.Mutex
	ticks   map[datalayer.EndpointID]int
	working map[datalayer.EndpointID]bool
	exits   map[datalayer.EndpointID]int
	gate    atomic.Pointer[chan struct{}]
}

func newSampler() *sampler {
	return &sampler{ticks: map[datalayer.EndpointID]int{}, working: map[datalayer.EndpointID]bool{}, exits: map[datalayer.EndpointID]int{}}
}

func (s *sampler) tick(ctx context.Context, id datalayer.EndpointID) bool {
	s.mu.Lock()
	s.ticks[id]++
	working := s.working[id]
	s.mu.Unlock()
	if gate := s.gate.Load(); gate != nil {
		select {
		case <-*gate:
		case <-ctx.Done():
		}
	}
	return working
}

func (s *sampler) exit(id datalayer.EndpointID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.exits[id]++
}

func (s *sampler) count(id datalayer.EndpointID) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ticks[id]
}

func (s *sampler) setWorking(id datalayer.EndpointID, working bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.working[id] = working
}

// A pace whose idle samples never come within a test and whose working samples are told apart from deciding ones
var testPace = Pace{Deciding: 5 * time.Millisecond, Working: 60 * time.Millisecond, Idle: time.Hour}

// A started group over one sampler tracking the ids, stopped with the test
func startPaced(t *testing.T, ids ...datalayer.EndpointID) (*Paced, *sampler) {
	t.Helper()
	s := newSampler()
	g := NewPaced(testPace, s.tick, s.exit)
	if err := g.Update(ids); err != nil {
		t.Fatal(err)
	}
	if err := g.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = g.Stop() })
	for _, id := range ids {
		waitFor(t, func() bool { return s.count(id) == 1 })
	}
	return g, s
}

// Fails when the endpoint is sampled during the window
func quiet(t *testing.T, s *sampler, id datalayer.EndpointID, window time.Duration, what string) {
	t.Helper()
	before := s.count(id)
	time.Sleep(window)
	if got := s.count(id); got != before {
		t.Fatalf("%s: %d samples in %s", what, got-before, window)
	}
}

func TestPacedLeavesAnIdleEndpointAlone(t *testing.T) {
	g, s := startPaced(t, "a")
	if err := g.Start(context.Background()); !errors.Is(err, ErrStarted) {
		t.Fatalf("second start err = %v", err)
	}
	quiet(t, s, "a", 3*testPace.Working, "an endpoint nothing asks for")
}

func TestPacedSampleWaitsForOneFreshSample(t *testing.T) {
	g, s := startPaced(t, "a", "b")
	time.Sleep(2 * testPace.Deciding)
	g.Sample(context.Background(), []datalayer.EndpointID{"a", "missing"})
	if s.count("a") != 2 || s.count("b") != 1 {
		t.Fatalf("a sampled %d times and b %d after one sample of a", s.count("a"), s.count("b"))
	}
	g.Sample(context.Background(), []datalayer.EndpointID{"a"})
	if s.count("a") != 2 {
		t.Fatalf("a sample one tick old was taken again, %d samples", s.count("a"))
	}
	quiet(t, s, "a", 3*testPace.Working, "an endpoint after its asked sample")
}

func TestPacedDecidingSamplesUntilReleasedThenOnceMore(t *testing.T) {
	g, s := startPaced(t, "a", "b")
	time.Sleep(testPace.Working + 2*testPace.Deciding)
	release := g.Deciding(context.Background(), []datalayer.EndpointID{"a"})
	if s.count("a") != 2 {
		t.Fatalf("the lease returned with %d samples of a, want the fresh second one", s.count("a"))
	}
	waitFor(t, func() bool { return s.count("a") >= 6 })
	released := time.Now()
	release()
	release()
	held := s.count("a")
	waitFor(t, func() bool { return s.count("a") > held && time.Since(released) >= testPace.Working })
	if got := s.count("a") - held; got > 2 {
		t.Fatalf("%d samples after the release, want the one under way at most and the settle sample", got)
	}
	quiet(t, s, "a", 3*testPace.Working, "an endpoint after its settle sample")
	if s.count("b") != 1 {
		t.Fatalf("an endpoint outside the lease was sampled %d times", s.count("b"))
	}
}

// A decision on an endpoint sampled within the working pace starts at once and still has it sampled at the deciding pace
func TestPacedDecidingDoesNotWaitOnARecentSample(t *testing.T) {
	g, s := startPaced(t, "a")
	time.Sleep(2 * testPace.Deciding)
	gate := make(chan struct{})
	s.gate.Store(&gate)
	start := time.Now()
	release := g.Deciding(context.Background(), []datalayer.EndpointID{"a"})
	if took := time.Since(start); took >= testPace.Deciding {
		t.Fatalf("the lease waited %s with a sample younger than the working pace", took)
	}
	waitFor(t, func() bool { return s.count("a") == 2 })
	close(gate)
	s.gate.Store(nil)
	waitFor(t, func() bool { return s.count("a") >= 5 })
	release()
}

func TestPacedDecidingWaitsOneTickAtMost(t *testing.T) {
	g, s := startPaced(t, "a")
	gate := make(chan struct{})
	s.gate.Store(&gate)
	time.Sleep(testPace.Working + 2*testPace.Deciding)
	start := time.Now()
	release := g.Deciding(context.Background(), []datalayer.EndpointID{"a"})
	if took := time.Since(start); took < testPace.Deciding || took > 10*testPace.Deciding {
		t.Fatalf("the lease waited %s on a sample that never finished, want one tick of %s", took, testPace.Deciding)
	}
	close(gate)
	release()
}

func TestPacedServingHoldsTheWorkingPace(t *testing.T) {
	g, s := startPaced(t, "a")
	time.Sleep(testPace.Working)
	start := time.Now()
	release := g.Serving("a")
	waitFor(t, func() bool { return s.count("a") >= 4 })
	if took := time.Since(start); took < 2*testPace.Working {
		t.Fatalf("three samples in %s, faster than the working pace of %s", took, testPace.Working)
	}
	release()
	release()
	held := s.count("a")
	time.Sleep(3 * testPace.Working)
	if got := s.count("a") - held; got > 1 {
		t.Fatalf("%d samples after the release, want the one already due at most", got)
	}
	quiet(t, s, "a", 3*testPace.Working, "an endpoint after its last request")
}

func TestPacedFollowsWorkASampleShows(t *testing.T) {
	g, s := startPaced(t, "a")
	s.setWorking("a", true)
	time.Sleep(2 * testPace.Deciding)
	g.Sample(context.Background(), []datalayer.EndpointID{"a"})
	waitFor(t, func() bool { return s.count("a") >= 4 })
	s.setWorking("a", false)
	held := s.count("a")
	time.Sleep(3 * testPace.Working)
	if got := s.count("a") - held; got > 1 {
		t.Fatalf("%d samples after the work ended, want the one that saw it end", got)
	}
	quiet(t, s, "a", 3*testPace.Working, "an endpoint whose work ended")
}

func TestPacedUpdateLeavesKeptEndpointsAlone(t *testing.T) {
	g, s := startPaced(t, "a", "b")
	time.Sleep(2 * testPace.Deciding)
	if err := g.Update([]datalayer.EndpointID{"a", "c"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return s.count("c") == 1 })
	s.mu.Lock()
	exits := s.exits["b"]
	s.mu.Unlock()
	if exits != 1 || s.count("b") != 1 {
		t.Fatalf("the dropped endpoint exited %d times after %d samples", exits, s.count("b"))
	}
	quiet(t, s, "a", 3*testPace.Working, "an endpoint kept through an update")
	if s.count("a") != 1 {
		t.Fatalf("a kept endpoint was sampled %d times", s.count("a"))
	}
}

func TestPacedAskSamplesOnceWithoutWaiting(t *testing.T) {
	g, s := startPaced(t, "a", "b")
	time.Sleep(2 * testPace.Deciding)
	gate := make(chan struct{})
	s.gate.Store(&gate)
	g.Ask([]datalayer.EndpointID{"a", "missing"})
	waitFor(t, func() bool { return s.count("a") == 2 })
	close(gate)
	s.gate.Store(nil)
	quiet(t, s, "a", 3*testPace.Working, "an endpoint after its asked sample")
	if s.count("b") != 1 {
		t.Fatalf("an endpoint nobody asked about was sampled %d times", s.count("b"))
	}
}

func TestPacedLeasesOutliveARestartAndAnUntrackedEndpoint(t *testing.T) {
	g, s := startPaced(t, "a")
	done := g.Deciding(context.Background(), []datalayer.EndpointID{"later"})
	done()
	release := g.Serving("a")
	if err := g.Stop(); err != nil {
		t.Fatal(err)
	}
	quiet(t, s, "a", 3*testPace.Working, "a stopped group")
	if err := g.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	held := s.count("a")
	waitFor(t, func() bool { return s.count("a") >= held+3 })
	release()
	g.dmu.Lock()
	left := len(g.demand)
	g.dmu.Unlock()
	if left != 0 {
		t.Fatalf("%d demand entries left with every lease released", left)
	}
}
