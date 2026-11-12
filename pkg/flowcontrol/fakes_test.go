package flowcontrol

import (
	"context"
	"sync"
	"time"

	"github.com/nickheyer/nebu/pkg/datalayer"
)

// Room for a fixed count of placed items in each pool
type seats struct {
	limit int

	mu   sync.Mutex
	used map[datalayer.PoolID]int
}

// Takes a seat of the pool, ErrPoolFull when every one is taken
func (s *seats) take(pool datalayer.PoolID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.used[pool] >= s.limit {
		return ErrPoolFull
	}
	s.used[pool]++
	return nil
}

func (s *seats) free(pool datalayer.PoolID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.used[pool]--
}

// The controller under test, items without a placement taking a seat and every permit giving its seat back
type seated struct {
	*FlowController
	seats *seats
}

func (s *seated) Admit(ctx context.Context, item *Item) (Permit, error) {
	if item.Place == nil {
		item.Place = func() error { return s.seats.take(item.Pool) }
	}
	pm, err := s.FlowController.Admit(ctx, item)
	if err != nil {
		return nil, err
	}
	return &seatPermit{Permit: pm, seats: s.seats, pool: item.Pool}, nil
}

// Frees its seat before the release that starts the next round
type seatPermit struct {
	Permit
	seats *seats
	pool  datalayer.PoolID
	once  sync.Once
}

func (p *seatPermit) Release() {
	p.once.Do(func() {
		p.seats.free(p.pool)
		p.Permit.Release()
	})
}

type admission struct {
	pool    datalayer.PoolID
	band    int
	waited  time.Duration
	outcome string
}

type recorder struct {
	mu     sync.Mutex
	events []admission
}

func (r *recorder) OnAdmit(pool datalayer.PoolID, band int, waited time.Duration, outcome string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, admission{pool, band, waited, outcome})
}

func (r *recorder) count(outcome string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, e := range r.events {
		if e.outcome == outcome {
			n++
		}
	}
	return n
}

func (r *recorder) last(outcome string) (admission, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := len(r.events) - 1; i >= 0; i-- {
		if r.events[i].outcome == outcome {
			return r.events[i], true
		}
	}
	return admission{}, false
}
