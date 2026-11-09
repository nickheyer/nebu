package proc

import (
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// A free port that nothing else holds, bound and closed by the test
func freeTestPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func TestReserveConfirmRelease(t *testing.T) {
	r := NewReservations("127.0.0.1")
	p := freeTestPort(t)
	one := Range{p, p}
	got, err := r.Reserve(one, 1)
	if err != nil || got != p {
		t.Fatalf("reserve %d %v", got, err)
	}
	if _, err := r.Reserve(one, 1); err == nil || !strings.Contains(err.Error(), strconv.Itoa(p)) {
		t.Fatalf("a pending port is handed out once: %v", err)
	}
	if err := r.Confirm(p + 1); err == nil {
		t.Fatal("confirming a port nobody reserved")
	}
	r.Release(p)
	if got, err := r.Reserve(one, 1); err != nil || got != p {
		t.Fatalf("a released port returns %d %v", got, err)
	}
	if err := r.Confirm(p); err != nil {
		t.Fatal(err)
	}
	if err := r.Confirm(p); err == nil {
		t.Fatal("a confirmed span is no longer pending")
	}
	if got, err := r.Reserve(one, 1); err != nil || got != p {
		t.Fatalf("an unbound confirmed port binds again %d %v", got, err)
	}
}

// A port an outside listener holds is skipped, and freed once the listener closes
func TestReserveSkipsBoundPorts(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := ln.Addr().(*net.TCPAddr).Port
	r := NewReservations("127.0.0.1")
	if _, err := r.Reserve(Range{p, p}, 1); err == nil {
		t.Fatal("a bound port was handed out")
	}
	ln.Close()
	if got, err := r.Reserve(Range{p, p}, 1); err != nil || got != p {
		t.Fatalf("closed listener %d %v", got, err)
	}
}

func TestReserveValidates(t *testing.T) {
	r := NewReservations("127.0.0.1")
	for _, c := range []struct {
		rng  Range
		span int
	}{
		{Range{0, 10}, 1},
		{Range{10, 70000}, 1},
		{Range{20, 10}, 1},
		{Range{40000, 40001}, 3},
		{Range{40000, 40001}, 0},
	} {
		if _, err := r.Reserve(c.rng, c.span); err == nil {
			t.Errorf("range %v span %d accepted", c.rng, c.span)
		}
	}
}

// Concurrent reservations never overlap and successive ones move through the range
func TestReserveConcurrentSpans(t *testing.T) {
	r := NewReservations("127.0.0.1")
	rng := Range{41000, 41999}
	var mu sync.Mutex
	taken := map[int]bool{}
	var wg sync.WaitGroup
	for range 64 {
		wg.Go(func() {
			p, err := r.Reserve(rng, 2)
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			for q := p; q < p+2; q++ {
				if taken[q] {
					t.Errorf("port %d handed out twice", q)
				}
				taken[q] = true
			}
		})
	}
	wg.Wait()
	if len(taken) != 128 {
		t.Fatalf("%d ports taken, want 128", len(taken))
	}
	a, err := r.Reserve(rng, 1)
	if err != nil {
		t.Fatal(err)
	}
	r.Release(a)
	b, err := r.Reserve(rng, 1)
	if err != nil || b == a {
		t.Fatalf("the cursor moves on after a reservation %d then %d %v", a, b, err)
	}
}

// A reserved span stays bound against other binders until it is yielded, and pending until confirmed
func TestReserveHoldsPortsUntilYield(t *testing.T) {
	r := NewReservations("127.0.0.1")
	p := freeTestPort(t)
	got, err := r.Reserve(Range{p, p + 1}, 2)
	if err != nil {
		t.Fatal(err)
	}
	for q := got; q < got+2; q++ {
		if ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(q)); err == nil {
			ln.Close()
			t.Fatalf("port %d binds elsewhere while reserved", q)
		}
	}
	if err := r.Yield(got + 1); err == nil {
		t.Fatal("yielding a port that starts no span")
	}
	if err := r.Yield(got); err != nil {
		t.Fatal(err)
	}
	for q := got; q < got+2; q++ {
		ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(q))
		if err != nil {
			t.Fatalf("port %d binds after the yield: %v", q, err)
		}
		ln.Close()
	}
	if _, err := r.Reserve(Range{got, got}, 1); err == nil {
		t.Fatal("a yielded span is still pending")
	}
	if err := r.Confirm(got); err != nil {
		t.Fatal(err)
	}
}
