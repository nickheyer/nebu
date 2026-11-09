package stub

import (
	"sync"
	"time"
)

// One served request with durations in emulated time, wall time times the speedup in ratio mode
type Record struct {
	ID               string        `json:"id"`
	Model            string        `json:"model"`
	PromptTokens     int           `json:"prompt_tokens"`
	CachedTokens     int           `json:"cached_tokens"`
	CompletionTokens int           `json:"completion_tokens"`
	TTFT             time.Duration `json:"ttft_ns"`
	Total            time.Duration `json:"total_ns"`
}

// Counters and kept records at one moment
type Stats struct {
	// Requests answered in full
	Served int `json:"served"`
	// Sequences queued or running
	InFlight int `json:"in_flight"`
	// Sequences waiting for a slot, blocks or an adapter
	Queued int `json:"queued"`
	// Sequences holding KV blocks
	Running int `json:"running"`
	// Times a running sequence lost its blocks and went back to the queue head
	Preempted int `json:"preempted"`
	// Prompt blocks found in the prefix cache at admission
	CacheHits int `json:"cache_hits"`
	// Prompt blocks looked up at admission and not found
	CacheMisses int      `json:"cache_misses"`
	Records     []Record `json:"records"`
}

// Unbounded relay of records to one reader so the engine never waits on a slow reader
type feed struct {
	mu      sync.Mutex
	pending []Record
	signal  chan struct{}
	out     chan Record
	quit    chan struct{}
}

// A relay with nothing pending
func newFeed() *feed {
	return &feed{signal: make(chan struct{}, 1), out: make(chan Record), quit: make(chan struct{})}
}

// Queues a record for delivery without blocking
func (f *feed) push(r Record) {
	f.mu.Lock()
	f.pending = append(f.pending, r)
	f.mu.Unlock()
	select {
	case f.signal <- struct{}{}:
	default:
	}
}

// Delivers pending records in order until stopped, then closes the output channel
func (f *feed) run() {
	defer close(f.out)
	for {
		f.mu.Lock()
		batch := f.pending
		f.pending = nil
		f.mu.Unlock()
		for _, r := range batch {
			select {
			case f.out <- r:
			case <-f.quit:
				return
			}
		}
		select {
		case <-f.signal:
		case <-f.quit:
			return
		}
	}
}

// Ends delivery
func (f *feed) stop() { close(f.quit) }

// Registers a relay for records of requests served from now on
func (e *engine) subscribe() <-chan Record {
	f := newFeed()
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		close(f.out)
		return f.out
	}
	e.feeds = append(e.feeds, f)
	e.mu.Unlock()
	go f.run()
	return f.out
}
