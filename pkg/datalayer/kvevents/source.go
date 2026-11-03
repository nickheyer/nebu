// Package kvevents subscribes to each runtime's KV cache event stream and reports the blocks it holds to the prefix index
package kvevents

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nickheyer/nebu/pkg/datalayer"
	"github.com/nickheyer/nebu/pkg/prefixcache"
)

const (
	Type = "kvevents"
	// Shortest gap between two reports of one endpoint
	DefaultInterval = 100 * time.Millisecond
	// Wait before dialing a publisher again after a failure
	DefaultReconnect = time.Second
	// Longest wait for one replayed batch
	replayTimeout = 5 * time.Second
)

var (
	ErrNoIndex  = errors.New("kv events source has no prefix index to report to")
	ErrStarted  = errors.New("kv events source already started")
	ErrGap      = errors.New("batches missed beyond the publisher's replay buffer, view restarted")
	ErrOrphans  = errors.New("stored blocks chain from a parent the subscriber never saw")
	ErrFraming  = errors.New("publisher message is not topic, sequence and payload")
	ErrNoReplay = errors.New("publisher names no replay address")
)

// Where an endpoint publishes its events and replays missed batches, and the model its blocks are scoped by
type Target struct {
	Events string
	Replay string
	Model  string
}

// Resolves an endpoint to its publisher, false for one that publishes none
type Resolver func(datalayer.EndpointID) (Target, bool)

// Receives the outcome of every connection, batch and report of an endpoint, blocks being those held after it
type Observer interface {
	OnKVEvents(endpoint datalayer.EndpointID, blocks int, err error)
}

type Options struct {
	// Shortest gap between two reports of one endpoint, DefaultInterval when zero
	Interval time.Duration
	// Wait before dialing again after a failure, DefaultReconnect when zero
	Reconnect time.Duration
	// Tokens per chunk and chunks per prompt of the index reported to, its defaults when zero
	ChunkTokens int
	MaxChunks   int
	// Told about every connection, batch and report when set
	Observer Observer
}

// Subscribes to every tracked endpoint's publisher from its own goroutine and reports its blocks to the index
type Source struct {
	store    datalayer.Store
	resolve  Resolver
	opts     Options
	index    atomic.Pointer[indexBox]
	observer Observer

	mu      sync.Mutex
	ctx     context.Context
	cancel  context.CancelFunc
	targets map[datalayer.EndpointID]Target
	workers map[datalayer.EndpointID]*worker
}

type indexBox struct{ ix prefixcache.Indexer }

// One subscription
type worker struct {
	target Target
	cancel context.CancelFunc
	done   chan struct{}
}

// Builds a source whose index is set before Start
func New(store datalayer.Store, resolve Resolver, opts Options) *Source {
	if opts.Interval <= 0 {
		opts.Interval = DefaultInterval
	}
	if opts.Reconnect <= 0 {
		opts.Reconnect = DefaultReconnect
	}
	return &Source{
		store:    store,
		resolve:  resolve,
		opts:     opts,
		observer: opts.Observer,
		targets:  map[datalayer.EndpointID]Target{},
		workers:  map[datalayer.EndpointID]*worker{},
	}
}

// Sets the index reported to, replacing any earlier one for every report after this call
func (s *Source) SetIndexer(ix prefixcache.Indexer) { s.index.Store(&indexBox{ix: ix}) }

func (s *Source) Type() string { return Type }

// Launches a subscription for every tracked endpoint that publishes, refusing to start without an index
func (s *Source) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.index.Load() == nil {
		return ErrNoIndex
	}
	if s.ctx != nil {
		return ErrStarted
	}
	s.ctx, s.cancel = context.WithCancel(ctx)
	for id, t := range s.targets {
		s.launch(id, t)
	}
	return nil
}

// Stops every subscription and waits for each
func (s *Source) Stop() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ctx == nil {
		return nil
	}
	s.cancel()
	for id, w := range s.workers {
		<-w.done
		delete(s.workers, id)
	}
	s.ctx, s.cancel = nil, nil
	return nil
}

// Takes no collector, the index being where reports go
func (s *Source) Subscribe(datalayer.Collector) error { return datalayer.ErrDuplicateCollector }

// Resolves every id, dropping subscriptions of endpoints gone or republishing elsewhere and starting the rest
func (s *Source) UpdateEndpoints(ids []datalayer.EndpointID) error {
	next := make(map[datalayer.EndpointID]Target, len(ids))
	for _, id := range ids {
		if t, ok := s.resolve(id); ok && t.Events != "" {
			next[id] = t
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.targets = next
	if s.ctx == nil {
		return nil
	}
	for id, w := range s.workers {
		if t, keep := next[id]; !keep || t != w.target {
			w.cancel()
			<-w.done
			delete(s.workers, id)
		}
	}
	for id, t := range next {
		if _, running := s.workers[id]; !running {
			s.launch(id, t)
		}
	}
	return nil
}

func (s *Source) launch(id datalayer.EndpointID, t Target) {
	ctx, cancel := context.WithCancel(s.ctx)
	w := &worker{target: t, cancel: cancel, done: make(chan struct{})}
	s.workers[id] = w
	go func() {
		defer close(w.done)
		defer cancel()
		s.run(ctx, id, t)
	}()
}

// Reports an outcome to the observer
func (s *Source) observe(id datalayer.EndpointID, blocks int, err error) {
	if s.observer != nil {
		s.observer.OnKVEvents(id, blocks, err)
	}
}

// Keeps one endpoint subscribed until ctx ends, dialing again after every failure
func (s *Source) run(ctx context.Context, id datalayer.EndpointID, t Target) {
	for {
		err := s.subscribe(ctx, id, t)
		if ctx.Err() != nil {
			return
		}
		s.observe(id, 0, err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(s.opts.Reconnect):
		}
	}
}

// One subscription, replaying what the publisher buffered before the live stream and filling every gap in it
func (s *Source) subscribe(ctx context.Context, id datalayer.EndpointID, t Target) error {
	z, err := dialZMTP(ctx, t.Events, socketSub, kvEventsTopic)
	if err != nil {
		return err
	}
	defer z.Close()
	stop := context.AfterFunc(ctx, func() { z.Close() })
	defer stop()
	chunk, limit := s.chunking()
	c := newCache(t.Model, chunk, limit)
	rep := &reporter{src: s, id: id, c: c}
	defer rep.flush(true)
	go rep.tick(ctx)
	last, have, err := s.replay(ctx, id, t, c, rep, 0, 0, false)
	if err != nil {
		return err
	}
	for {
		frames, err := z.message()
		if err != nil {
			return err
		}
		if len(frames) != 3 || len(frames[1]) != 8 {
			return fmt.Errorf("%w: %d frames", ErrFraming, len(frames))
		}
		seq := binary.BigEndian.Uint64(frames[1])
		if have && seq <= last {
			continue
		}
		if have && seq > last+1 {
			if last, have, err = s.replay(ctx, id, t, c, rep, last+1, seq, true); err != nil {
				return err
			}
			if seq <= last {
				continue
			}
		}
		if err := s.apply(id, c, rep, seq, frames[2]); err != nil {
			return err
		}
		last, have = seq, true
	}
}

// Decodes and applies one batch, reporting orphans, then schedules a report
func (s *Source) apply(id datalayer.EndpointID, c *cache, rep *reporter, seq uint64, payload []byte) error {
	b, err := decodeBatch(seq, payload)
	if err != nil {
		return fmt.Errorf("batch %d: %w", seq, err)
	}
	if orphans := c.apply(b); orphans > 0 {
		s.observe(id, c.size(), fmt.Errorf("%w: %d in batch %d", ErrOrphans, orphans, seq))
	}
	rep.dirty()
	return nil
}

// Replays buffered batches from start up to before end, zero end taking everything, restarting the view when the gap outlives the buffer
func (s *Source) replay(ctx context.Context, id datalayer.EndpointID, t Target, c *cache, rep *reporter, start, end uint64, bounded bool) (last uint64, have bool, err error) {
	if t.Replay == "" {
		if bounded {
			s.observe(id, c.size(), fmt.Errorf("%w: %w", ErrGap, ErrNoReplay))
			c.apply(&Batch{Cleared: true, Order: []step{{stepCleared, 0}}})
			rep.dirty()
		}
		return 0, false, nil
	}
	z, err := dialZMTP(ctx, t.Replay, socketDealer, "")
	if err != nil {
		return 0, false, fmt.Errorf("replay: %w", err)
	}
	defer z.Close()
	stop := context.AfterFunc(ctx, func() { z.Close() })
	defer stop()
	var seq [8]byte
	binary.BigEndian.PutUint64(seq[:], start)
	if err := z.write([][]byte{{}, seq[:]}); err != nil {
		return 0, false, fmt.Errorf("replay: %w", err)
	}
	for {
		_ = z.c.SetReadDeadline(time.Now().Add(replayTimeout))
		frames, err := z.message()
		if err != nil {
			return 0, false, fmt.Errorf("replay: %w", err)
		}
		if len(frames) != 4 || len(frames[2]) != 8 {
			return 0, false, fmt.Errorf("replay: %w: %d frames", ErrFraming, len(frames))
		}
		if string(frames[2]) == "\xff\xff\xff\xff\xff\xff\xff\xff" {
			break
		}
		n := binary.BigEndian.Uint64(frames[2])
		if bounded && n >= end {
			continue
		}
		if !have && bounded && n > start {
			s.observe(id, c.size(), fmt.Errorf("%w: batches %d to %d", ErrGap, start, n-1))
			c.apply(&Batch{Cleared: true, Order: []step{{stepCleared, 0}}})
			rep.dirty()
		}
		if err := s.apply(id, c, rep, n, frames[3]); err != nil {
			return 0, false, err
		}
		last, have = n, true
	}
	if bounded && !have {
		s.observe(id, c.size(), fmt.Errorf("%w: batches %d to %d", ErrGap, start, end-1))
		c.apply(&Batch{Cleared: true, Order: []step{{stepCleared, 0}}})
		rep.dirty()
	}
	return last, have, nil
}

// Coalesces reports of one endpoint to at most one per interval
type reporter struct {
	src *Source
	id  datalayer.EndpointID
	c   *cache

	mu      sync.Mutex
	pending bool
	last    time.Time
}

// Marks the cache changed, reporting at once when the interval has passed
func (r *reporter) dirty() {
	r.mu.Lock()
	r.pending = true
	due := time.Since(r.last) >= r.src.opts.Interval
	r.mu.Unlock()
	if due {
		r.flush(false)
	}
}

// Reports when a change is pending, or unconditionally at the end of a subscription
func (r *reporter) flush(final bool) {
	r.mu.Lock()
	if !r.pending && !final {
		r.mu.Unlock()
		return
	}
	r.pending = false
	r.last = time.Now()
	hashes := r.c.hashes()
	blocks := r.c.size()
	r.mu.Unlock()
	if final && blocks == 0 && len(hashes) == 0 {
		return
	}
	r.src.index.Load().ix.Report(r.id, hashes)
	r.src.observe(r.id, blocks, nil)
}

// Flushes pending changes every interval until ctx ends
func (r *reporter) tick(ctx context.Context) {
	t := time.NewTicker(r.src.opts.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.flush(false)
		}
	}
}

// Topic the runtimes publish under, as the launch configures it
const kvEventsTopic = "kv-events"

// Resolves endpoints to the publisher their labels name and their pool's model
func StoreResolver(store datalayer.Store) Resolver {
	return func(id datalayer.EndpointID) (Target, bool) {
		ep, ok := store.Endpoint(id)
		if !ok {
			return Target{}, false
		}
		p, ok := store.Pool(ep.Pool())
		events := ep.Labels()[datalayer.LabelKVEvents]
		if !ok || events == "" {
			return Target{}, false
		}
		return Target{Events: events, Replay: ep.Labels()[datalayer.LabelKVReplay], Model: p.Model}, true
	}
}

// Whether an index has been set
func (s *Source) HasIndexer() bool { return s.index.Load() != nil }

// The chunk tokens and chunk cap subscriptions chain by
func (s *Source) chunking() (int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.opts.ChunkTokens, s.opts.MaxChunks
}

// Sets the chunk tokens and chunk cap new subscriptions chain by, the index's defaults when zero
func (s *Source) SetChunking(chunkTokens, maxChunks int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.opts.ChunkTokens, s.opts.MaxChunks = chunkTokens, maxChunks
}
