package proc

import (
	"fmt"
	"math/rand/v2"
	"net"
	"strconv"
	"sync"
)

// Inclusive bounds of the ports a reservation draws from
type Range struct{ Low, High int }

func (r Range) validate() error {
	if r.Low < 1 || r.High > 65535 || r.Low > r.High {
		return fmt.Errorf("port range %d-%d must lie within 1-65535 with the low end first", r.Low, r.High)
	}
	return nil
}

// Ports handed to launches, each span bound in process from Reserve until Yield and pending until Confirm or Release
type Reservations struct {
	host    string
	mu      sync.Mutex
	pending map[int]*bound
	held    map[int]bool
	cursor  int
}

// A pending span and the listeners holding its ports until the runtime takes them
type bound struct {
	size      int
	listeners []net.Listener
}

// Hands out ports free to bind on host
func NewReservations(host string) *Reservations {
	return &Reservations{host: host, pending: map[int]*bound{}, held: map[int]bool{}}
}

// Returns the first of span consecutive ports, each bound here on the host and outside every pending span
func (r *Reservations) Reserve(rng Range, span int) (int, error) {
	if err := rng.validate(); err != nil {
		return 0, err
	}
	size := rng.High - rng.Low + 1
	if span < 1 || span > size {
		return 0, fmt.Errorf("a span of %d ports does not fit the range %d-%d", span, rng.Low, rng.High)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	starts := size - span + 1
	if r.cursor == 0 {
		r.cursor = rng.Low + rand.IntN(size)
	}
	first := 0
	if r.cursor >= rng.Low && r.cursor <= rng.High {
		first = (r.cursor - rng.Low) % starts
	}
	for i := range starts {
		port := rng.Low + (first+i)%starts
		listeners, ok := r.bind(port, span)
		if !ok {
			continue
		}
		r.pending[port] = &bound{size: span, listeners: listeners}
		for p := port; p < port+span; p++ {
			r.held[p] = true
		}
		r.cursor = port + span
		return port, nil
	}
	return 0, fmt.Errorf("no %d consecutive ports free on %s in %d-%d", span, r.host, rng.Low, rng.High)
}

// Binds every port of the span on the host, closing what it bound when one is held or taken
func (r *Reservations) bind(port, span int) ([]net.Listener, bool) {
	var listeners []net.Listener
	for p := port; p < port+span; p++ {
		if r.held[p] {
			closeAll(listeners)
			return nil, false
		}
		ln, err := net.Listen("tcp", net.JoinHostPort(r.host, strconv.Itoa(p)))
		if err != nil {
			closeAll(listeners)
			return nil, false
		}
		listeners = append(listeners, ln)
	}
	return listeners, true
}

// Unbinds the pending span starting at port so the runtime about to launch may bind it
func (r *Reservations) Yield(port int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.pending[port]
	if !ok {
		return fmt.Errorf("port %d starts no pending reservation", port)
	}
	closeAll(s.listeners)
	s.listeners = nil
	return nil
}

// Hands the span starting at port to the runtime that now holds it bound
func (r *Reservations) Confirm(port int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.pending[port]; !ok {
		return fmt.Errorf("port %d starts no pending reservation", port)
	}
	r.drop(port)
	return nil
}

// Gives up the pending span starting at port so a later Reserve may hand it out
func (r *Reservations) Release(port int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.drop(port)
}

func (r *Reservations) drop(port int) {
	s, ok := r.pending[port]
	if !ok {
		return
	}
	closeAll(s.listeners)
	delete(r.pending, port)
	for p := port; p < port+s.size; p++ {
		delete(r.held, p)
	}
}

func closeAll(listeners []net.Listener) {
	for _, ln := range listeners {
		ln.Close()
	}
}
