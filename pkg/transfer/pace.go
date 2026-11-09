package transfer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/nickheyer/nebu/pkg/estimate"
)

// How far below the pace of its fetch a range stream may fall before it is restarted
const paceSlack = 8

// How long a range stream's reads wait on the network between judgements when the fetcher names no window
const paceWindow = 5 * time.Second

// Ends a range stream whose reads waited a whole window without a byte
var errStalled = errors.New("stream stalled")

// Marks a chunk attempt that kept its bytes and is taken up again without counting as a failure
var errRestart = errors.New("stream restarted")

// Ends a range stream that fell far behind the pace of its fetch
type slowError struct{ rate, pace float64 }

func (e *slowError) Error() string {
	return fmt.Sprintf("stream fell to %s/s against %s/s", estimate.Human(uint64(e.rate)), estimate.Human(uint64(e.pace)))
}

// A blob serving a range as one stream that its context ends
type ranged interface {
	Range(ctx context.Context, off, length int64) (io.ReadCloser, error)
}

// The bytes per second the range streams of one fetch have lately held
type pace struct {
	// How long a stream's reads wait on the network between judgements
	window time.Duration
	mu     sync.Mutex
	best   float64
}

// Takes a stream's rate over one tick into the pace, the fastest lately seen
func (p *pace) raise(rate float64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.best = max(rate, p.best*0.9)
}

// Reports whether a stream's rate over a window lags the pace, with the pace it was held against
func (p *pace) lags(rate float64) (float64, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	best := p.best
	if rate >= best/paceSlack {
		return best, false
	}
	p.best = best / 2
	return best, true
}

// Counts what a range stream delivers and how long its reads wait on the network
type stream struct {
	ctx    context.Context
	r      io.Reader
	mu     sync.Mutex
	bytes  int64
	waited time.Duration
	began  time.Time
}

func (s *stream) Read(p []byte) (int, error) {
	if err := context.Cause(s.ctx); err != nil {
		return 0, err
	}
	s.mu.Lock()
	s.began = time.Now()
	s.mu.Unlock()
	n, err := s.r.Read(p)
	s.mu.Lock()
	s.waited += time.Since(s.began)
	s.began = time.Time{}
	s.bytes += int64(n)
	s.mu.Unlock()
	return n, err
}

// Bytes delivered and time waited on the network so far, a read in flight included
func (s *stream) sample(now time.Time) (int64, time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	waited := s.waited
	if !s.began.IsZero() {
		waited += now.Sub(s.began)
	}
	return s.bytes, waited
}

// Feeds a stream's rate into the pace each tick and cancels it once a window is empty or lags
func (p *pace) watch(ctx context.Context, s *stream, cancel context.CancelCauseFunc) {
	every := p.window / 5
	tick := time.NewTicker(every)
	defer tick.Stop()
	var tickBytes, seenBytes int64
	var tickWait, seenWait time.Duration
	// Takes what arrived since the last tick into the pace once it spans enough waiting to be a rate
	feed := func(bytes int64, waited time.Duration) {
		if waited-tickWait < every/2 {
			return
		}
		p.raise(float64(bytes-tickBytes) / (waited - tickWait).Seconds())
		tickBytes, tickWait = bytes, waited
	}
	for {
		select {
		case <-ctx.Done():
			feed(s.sample(time.Now()))
			return
		case now := <-tick.C:
			bytes, waited := s.sample(now)
			feed(bytes, waited)
			if waited-seenWait < p.window {
				continue
			}
			if bytes == seenBytes {
				cancel(fmt.Errorf("%w: nothing arrived for %s", errStalled, (waited - seenWait).Round(every)))
				return
			}
			rate := float64(bytes-seenBytes) / (waited - seenWait).Seconds()
			seenBytes, seenWait = bytes, waited
			if best, lagging := p.lags(rate); lagging {
				cancel(&slowError{rate: rate, pace: best})
				return
			}
		}
	}
}

type notesKey struct{}

// Sends what a transfer does to recover, a retry or a restarted stream, to the caller's log
func WithNotes(ctx context.Context, note func(line string)) context.Context {
	return context.WithValue(ctx, notesKey{}, note)
}

// Writes a recovery line to the fetcher's log and to the notes of ctx
func (f *Fetcher) note(ctx context.Context, format string, args ...any) {
	line := fmt.Sprintf(format, args...)
	f.Log.Debug(line)
	if note, ok := ctx.Value(notesKey{}).(func(string)); ok {
		note(line)
	}
}
