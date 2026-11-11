package mesh

import (
	"context"
	"net"
	"testing"
	"time"
)

// A measured member waits the probe interval and a failed one a round, doubling per failure up to that interval
func TestProbeWaitDoublesPerFailure(t *testing.T) {
	m := bare(t, "self")
	m.interval = 5 * time.Second
	for misses, want := range []time.Duration{ProbeInterval, 5 * time.Second, 10 * time.Second, 20 * time.Second, 40 * time.Second} {
		if got := m.probeWait(misses); got != want {
			t.Fatalf("after %d failures the wait is %s, want %s", misses, got, want)
		}
	}
	for _, misses := range []int{7, 8, 60, 1 << 20} {
		if got := m.probeWait(misses); got != ProbeInterval {
			t.Fatalf("after %d failures the wait is %s, want the probe interval", misses, got)
		}
	}
}

// A probe that fails is run again within rounds until it measures the link, which clears the failures counted
func TestFailedProbeRunsAgainWithinRounds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := newNode(t, ctx, 100*time.Millisecond)
	b := newNode(t, ctx, 100*time.Millisecond)
	formMesh(t, ctx, a, b)
	eventually(t, "a measures its link to b", 30*time.Second, func() bool { return a.Link(b.Self()) != nil })
	first := a.Link(b.Self()).GetMeasuredAt().AsTime()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	dead := ln.Addr().String()
	ln.Close()
	a.mu.Lock()
	mb := a.members[b.Self()]
	live := mb.rec.GetAddress()
	mb.rec.Address, mb.probedAt = dead, time.Time{}
	a.mu.Unlock()
	misses := func() int {
		a.mu.Lock()
		defer a.mu.Unlock()
		return a.members[b.Self()].probeMisses
	}
	eventually(t, "the probe of an address nothing answers on fails a second time", 10*time.Second, func() bool { return misses() >= 2 })
	if got := a.Link(b.Self()).GetMeasuredAt().AsTime(); !got.Equal(first) {
		t.Fatalf("a failed probe keeps the last measurement, yet it moved from %s to %s", first, got)
	}
	a.mu.Lock()
	a.members[b.Self()].rec.Address = live
	a.mu.Unlock()
	eventually(t, "the probe measures the link once the member answers", 30*time.Second, func() bool {
		return a.Link(b.Self()).GetMeasuredAt().AsTime().After(first) && misses() == 0
	})
}
