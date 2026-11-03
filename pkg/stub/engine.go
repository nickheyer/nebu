package stub

import (
	"bytes"
	"slices"
	"sort"
	"sync"
	"time"
)

type phase int

const (
	phaseQueued phase = iota
	phasePrefill
	phaseDecode
	phaseFinished
)

// Output words, each a single token under Tokenize
var vocab = []string{
	"alpha", "bravo", "charlie", "delta", "echo", "foxtrot", "golf", "hotel", "india",
	"juliet", "kilo", "lima", "mike", "november", "oscar", "papa", "quebec", "romeo",
	"sierra", "tango", "uniform", "victor", "whiskey", "xray", "yankee", "zulu",
	"zero", "one", "two", "three", "four", "five", "six", "seven", "eight", "nine",
}

// One output choice moving between the queue and the running set
type seq struct {
	g         *group
	lead      bool
	order     uint64
	ids       []uint32
	hashes    []uint64
	target    int
	generated int
	text      []byte
	ends      []int
	blocks    []int
	cached    int
	admitted  bool
	phase     phase
	next      time.Time
	reason    string
	slot      int
}

// The sequences of one request and the channel its handler waits on
type group struct {
	id      string
	model   string
	adapter string
	chat    bool
	limit   string
	stops   []string
	maxStop int
	seqs    []*seq
	arrival time.Time
	first   time.Time
	notify  chan struct{}
	err     *apiError
	done    bool
}

// What a handler sees of its request since the offsets it already sent
type view struct {
	pieces   [][]string
	finished []bool
	reasons  []string
	usage    usage
	done     bool
	err      *apiError
}

// Scheduler state shared by the loop and the handlers under one mutex
type engine struct {
	cfg       Config
	mu        sync.Mutex
	kv        *kvCache
	queue     []*seq
	running   []*seq
	loras     *loraSet
	faults    faults
	paused    bool
	closed    bool
	ready     time.Time
	order     uint64
	changed   time.Time
	served    int
	preempted int
	hits      int
	misses    int
	records   []Record
	feeds     []*feed
	slots     []*seq
	freed     []uint64
	releases  uint64
	wake      chan struct{}
	quit      chan struct{}
	stopped   chan struct{}
}

// Scheduler state with startup adapters resident and the loading period counted from now
func newEngine(cfg Config, now time.Time) *engine {
	e := &engine{
		cfg:     cfg,
		kv:      newKVCache(cfg.NumGPUBlocks),
		loras:   newLoraSet(cfg.MaxLoras),
		faults:  faults{rejectAbove: -1, latency: 1},
		ready:   now.Add(cfg.LoadTime),
		changed: now,
		slots:   make([]*seq, cfg.MaxNumSeqs),
		freed:   make([]uint64, cfg.MaxNumSeqs),
		wake:    make(chan struct{}, 1),
		quit:    make(chan struct{}),
		stopped: make(chan struct{}),
	}
	for _, a := range cfg.Adapters {
		l := &lora{name: a.Name, path: a.Path, created: now.Unix(), loaded: make(chan struct{})}
		e.loras.byName[a.Name] = l
		e.residentLocked(l)
	}
	return e
}

// Advances due steps and adapter loads, admits queued work, then sleeps until the next event
func (e *engine) loop() {
	defer close(e.stopped)
	t := time.NewTimer(time.Hour)
	defer t.Stop()
	for {
		e.mu.Lock()
		next, ok := e.stepLocked(time.Now())
		e.mu.Unlock()
		if ok {
			t.Reset(time.Until(next))
		} else {
			t.Stop()
		}
		select {
		case <-e.wake:
		case <-t.C:
		case <-e.quit:
			return
		}
	}
}

// Runs every event due by now and reports the earliest pending event time
func (e *engine) stepLocked(now time.Time) (time.Time, bool) {
	e.finishLoadsLocked(now)
	for s := e.dueLocked(now); s != nil; s = e.dueLocked(now) {
		e.advanceLocked(s, now)
	}
	e.admitLocked(now)
	return e.nextEventLocked()
}

// Wakes the loop without blocking
func (e *engine) kick() {
	select {
	case e.wake <- struct{}{}:
	default:
	}
}

// Wakes a request handler without blocking
func (e *engine) signal(g *group) {
	select {
	case g.notify <- struct{}{}:
	default:
	}
}

// Converts an emulated duration to wall time through the latency multiplier and the ratio speedup
func (e *engine) wall(d time.Duration) time.Duration {
	f := e.faults.latency
	if e.cfg.Mode == ModeRatio {
		f /= e.cfg.Speedup
	}
	return time.Duration(float64(d) * f)
}

// Scales a wall duration back up by the ratio speedup
func (e *engine) emulated(d time.Duration) time.Duration {
	if e.cfg.Mode == ModeRatio {
		return time.Duration(float64(d) * e.cfg.Speedup)
	}
	return d
}

// Prefill cost of uncached tokens while other sequences run alongside
func (e *engine) ttft(tokens, others int) time.Duration {
	return e.cfg.BaseLatency + time.Duration(float64(e.cfg.PrefillPerToken)*float64(tokens)*(1+float64(others)*e.cfg.Contention))
}

// Cost of one decoded token while other sequences run alongside
func (e *engine) tpot(others int) time.Duration {
	return time.Duration(float64(e.cfg.DecodePerToken) * (1 + float64(others)*e.cfg.Contention))
}

// Queues every sequence of a request after resolving its model and applying the queue depth fault
func (e *engine) submit(g *group) *apiError {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return errShutdown()
	}
	if g.model != e.cfg.Model {
		if e.loras.byName[g.model] == nil {
			return errModelNotFound(g.model)
		}
		g.adapter = g.model
	}
	if e.faults.rejectAbove >= 0 && len(e.queue) > e.faults.rejectAbove {
		return errQueueFull(len(e.queue), e.faults.rejectAbove)
	}
	for _, s := range g.seqs {
		s.order = e.order
		e.order++
		e.queue = append(e.queue, s)
	}
	e.changed = g.arrival
	e.kick()
	return nil
}

// The running sequence with the earliest step due by now, oldest first on ties
func (e *engine) dueLocked(now time.Time) *seq {
	var best *seq
	for _, s := range e.running {
		if s.next.After(now) {
			continue
		}
		if best == nil || s.next.Before(best.next) || (s.next.Equal(best.next) && s.order < best.order) {
			best = s
		}
	}
	return best
}

// The earliest step or adapter load still pending
func (e *engine) nextEventLocked() (time.Time, bool) {
	var t time.Time
	ok := false
	for _, s := range e.running {
		if !ok || s.next.Before(t) {
			t, ok = s.next, true
		}
	}
	for _, l := range e.loras.loading {
		if !ok || l.ready.Before(t) {
			t, ok = l.ready, true
		}
	}
	return t, ok
}

// Completes a prefill or decode step by growing blocks, committing a token and scheduling the next step
func (e *engine) advanceLocked(s *seq, now time.Time) {
	if s.phase == phasePrefill {
		for i, h := range s.hashes {
			e.kv.register(s.blocks[i], h)
		}
		s.phase = phaseDecode
	}
	if !e.growLocked(s, len(s.ids)+s.generated+1) {
		return
	}
	stopped := s.commit()
	if s.g.first.IsZero() {
		s.g.first = now
	}
	e.signal(s.g)
	switch {
	case stopped:
		e.finishLocked(s, "stop", now)
	case s.generated >= s.target:
		e.finishLocked(s, s.g.limit, now)
	default:
		s.next = s.next.Add(e.wall(e.tpot(len(e.running) - 1)))
	}
}

// Gives a sequence blocks for tokens, preempting the newest running sequences, itself included, until they fit
func (e *engine) growLocked(s *seq, tokens int) bool {
	need := ceilDiv(tokens, e.cfg.BlockSize) - len(s.blocks)
	for need > e.kv.available() {
		v := e.newestLocked()
		e.preemptLocked(v)
		if v == s {
			return false
		}
	}
	if need > 0 {
		s.blocks = append(s.blocks, e.kv.allocate(need)...)
	}
	return true
}

// The most recently arrived running sequence
func (e *engine) newestLocked() *seq {
	var v *seq
	for _, s := range e.running {
		if v == nil || s.order > v.order {
			v = s
		}
	}
	return v
}

// Frees a running sequence's blocks and puts it back at the queue head to recompute later
func (e *engine) preemptLocked(s *seq) {
	e.unlinkLocked(s)
	s.phase, s.next = phaseQueued, time.Time{}
	e.queue = slices.Insert(e.queue, 0, s)
	e.preempted++
}

// Moves queued sequences into the running set in arrival order while slots and blocks allow
func (e *engine) admitLocked(now time.Time) {
	if e.paused {
		return
	}
	for i := 0; i < len(e.queue) && len(e.running) < e.cfg.MaxNumSeqs; {
		s := e.queue[i]
		if !s.admitted && s.g.adapter != "" && !e.loras.isResident(s.g.adapter) {
			i++
			continue
		}
		tokens := len(s.ids) + s.generated
		lookup := s.hashes[:min(len(s.hashes), (tokens-1)/e.cfg.BlockSize)]
		matched, idle := e.kv.match(lookup)
		need := ceilDiv(tokens, e.cfg.BlockSize) - len(matched)
		if e.kv.available()-idle < need {
			return
		}
		e.queue = slices.Delete(e.queue, i, i+1)
		e.kv.acquire(matched)
		s.blocks = append(matched, e.kv.allocate(need)...)
		cached := len(matched) * e.cfg.BlockSize
		if !s.admitted {
			s.cached, s.admitted = cached, true
		}
		e.hits += len(matched)
		e.misses += len(lookup) - len(matched)
		e.seatLocked(s)
		s.phase = phasePrefill
		s.next = now.Add(e.wall(e.ttft(tokens-cached, len(e.running))))
		e.running = append(e.running, s)
		e.changed = now
	}
}

// Seats a sequence in an idle server slot, a never used one first, else the one released longest ago
func (e *engine) seatLocked(s *seq) {
	best := -1
	for i, o := range e.slots {
		switch {
		case o.runsIn(i):
		case o == nil:
			e.slots[i], s.slot = s, i
			return
		case best < 0 || e.freed[i] < e.freed[best]:
			best = i
		}
	}
	e.slots[best], s.slot = s, best
}

// Whether the sequence is running in the server slot numbered i
func (s *seq) runsIn(i int) bool {
	return s != nil && s.slot == i && (s.phase == phasePrefill || s.phase == phaseDecode)
}

// Every server slot as llama.cpp lists it with task fields once it ran a sequence, and the idle slot count
func (e *engine) slotList() ([]map[string]any, int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	nctx := e.cfg.NumGPUBlocks * e.cfg.BlockSize
	out := make([]map[string]any, len(e.slots))
	idle := 0
	for i, s := range e.slots {
		busy := s.runsIn(i)
		if !busy {
			idle++
		}
		out[i] = map[string]any{"id": i, "n_ctx": nctx, "speculative": false, "is_processing": busy}
		if s == nil {
			continue
		}
		out[i]["id_task"] = s.order
		out[i]["n_prompt_tokens"] = len(s.ids) + s.generated
		out[i]["n_prompt_tokens_processed"] = len(s.ids) - s.cached
		out[i]["n_prompt_tokens_cache"] = s.cached
		out[i]["params"] = map[string]any{"max_tokens": s.target, "n_predict": s.target}
		out[i]["next_token"] = []map[string]any{{
			"has_next_token": busy && s.generated < s.target,
			"has_new_line":   bytes.Contains(s.text, []byte("\n")),
			"n_remain":       s.target - s.generated,
			"n_decoded":      s.generated,
		}}
	}
	return out, idle
}

// Takes a sequence out of the queue or running set and releases its blocks
func (e *engine) unlinkLocked(s *seq) {
	switch s.phase {
	case phaseQueued:
		e.queue = slices.DeleteFunc(e.queue, func(o *seq) bool { return o == s })
	case phasePrefill, phaseDecode:
		e.running = slices.DeleteFunc(e.running, func(o *seq) bool { return o == s })
		e.releases++
		e.freed[s.slot] = e.releases
	}
	e.kv.release(s.blocks)
	s.blocks = nil
}

// Ends a sequence and records its request once every sequence of it has ended
func (e *engine) finishLocked(s *seq, reason string, now time.Time) {
	e.unlinkLocked(s)
	s.phase, s.reason = phaseFinished, reason
	e.changed = now
	g := s.g
	e.signal(g)
	for _, o := range g.seqs {
		if o.phase != phaseFinished {
			return
		}
	}
	g.done = true
	e.served++
	u := g.usageLocked()
	r := Record{
		ID:               g.id,
		Model:            g.model,
		PromptTokens:     u.PromptTokens,
		CachedTokens:     u.PromptTokensDetails.CachedTokens,
		CompletionTokens: u.CompletionTokens,
		TTFT:             e.emulated(g.first.Sub(g.arrival)),
		Total:            e.emulated(now.Sub(g.arrival)),
	}
	e.records = append(e.records, r)
	if lim := e.cfg.Records; lim > 0 && len(e.records) >= 2*lim {
		e.records = slices.Clone(e.records[len(e.records)-lim:])
	}
	for _, f := range e.feeds {
		f.push(r)
	}
}

// Removes every unfinished sequence of a request without recording it
func (e *engine) abortLocked(g *group) {
	for _, s := range g.seqs {
		if s.phase != phaseFinished {
			e.unlinkLocked(s)
			s.phase = phaseFinished
		}
	}
	g.done = true
	e.kick()
}

// Ends a request with an error its handler reports
func (e *engine) failLocked(g *group, err *apiError) {
	e.abortLocked(g)
	g.err = err
	e.signal(g)
}

// Drops a request whose client went away, freeing its blocks
func (e *engine) cancel(g *group) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !g.done {
		e.abortLocked(g)
	}
}

// New text per sequence since the sent offsets, holding back bytes a stop string could still claim
func (e *engine) view(g *group, sent []int, hold int) view {
	e.mu.Lock()
	defer e.mu.Unlock()
	n := len(g.seqs)
	v := view{pieces: make([][]string, n), finished: make([]bool, n), reasons: make([]string, n), err: g.err}
	all := true
	for i, s := range g.seqs {
		fin := s.phase == phaseFinished
		end := len(s.text)
		if !fin {
			end = max(sent[i], end-hold)
		}
		v.pieces[i] = s.split(sent[i], end)
		sent[i] = end
		v.finished[i], v.reasons[i] = fin, s.reason
		all = all && fin
	}
	v.done = all && g.err == nil
	if v.done {
		v.usage = g.usageLocked()
	}
	return v
}

// Appends the next output word and cuts the text at the earliest stop string it now holds
func (s *seq) commit() bool {
	old := len(s.text)
	if s.generated > 0 || !s.g.chat {
		s.text = append(s.text, ' ')
	}
	s.text = append(s.text, vocab[s.generated%len(vocab)]...)
	s.generated++
	s.ends = append(s.ends, len(s.text))
	cut := -1
	from := max(0, old-s.g.maxStop+1)
	for _, st := range s.g.stops {
		if i := bytes.Index(s.text[from:], []byte(st)); i >= 0 && (cut < 0 || from+i < cut) {
			cut = from + i
		}
	}
	if cut < 0 {
		return false
	}
	s.text = s.text[:cut]
	return true
}

// Text between two offsets cut at token boundaries
func (s *seq) split(from, to int) []string {
	var out []string
	for i := sort.SearchInts(s.ends, from+1); i < len(s.ends) && s.ends[i] < to; i++ {
		out = append(out, string(s.text[from:s.ends[i]]))
		from = s.ends[i]
	}
	if from < to {
		out = append(out, string(s.text[from:to]))
	}
	return out
}

// Prompt tokens of each distinct prompt, their cached part, and every generated token
func (g *group) usageLocked() usage {
	var u usage
	for _, s := range g.seqs {
		u.CompletionTokens += s.generated
		if s.lead {
			u.PromptTokens += len(s.ids)
			u.PromptTokensDetails.CachedTokens += s.cached
		}
	}
	u.TotalTokens = u.PromptTokens + u.CompletionTokens
	return u
}

// Why health checks fail right now, empty while serving
func (e *engine) status(now time.Time) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	switch {
	case now.Before(e.ready):
		return "loading"
	case e.paused:
		return "paused"
	}
	return ""
}

// Whether health and model endpoints still answer 503 for startup
func (e *engine) loading(now time.Time) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return now.Before(e.ready)
}

// Stops or restarts admission to the running set
func (e *engine) setPaused(p bool) {
	e.mu.Lock()
	e.paused = p
	e.mu.Unlock()
	e.kick()
}

// Counters and kept records at this moment
func (e *engine) stats() Stats {
	e.mu.Lock()
	defer e.mu.Unlock()
	recs := e.records
	if lim := e.cfg.Records; lim > 0 && len(recs) > lim {
		recs = recs[len(recs)-lim:]
	}
	return Stats{
		Served:      e.served,
		InFlight:    len(e.queue) + len(e.running),
		Queued:      len(e.queue),
		Running:     len(e.running),
		Preempted:   e.preempted,
		CacheHits:   e.hits,
		CacheMisses: e.misses,
		Records:     append([]Record{}, recs...),
	}
}

// Fails every pending request and adapter load, stops the loop and closes subscriber channels
func (e *engine) close() {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return
	}
	e.closed = true
	seen := map[*group]bool{}
	for _, s := range slices.Concat(e.queue, e.running) {
		if !seen[s.g] {
			seen[s.g] = true
			e.failLocked(s.g, errShutdown())
		}
	}
	for _, l := range slices.Concat(e.loras.loading, e.loras.waiting) {
		e.dropLoadLocked(l, errShutdown())
	}
	e.loras.loading, e.loras.waiting = nil, nil
	feeds := e.feeds
	e.feeds = nil
	e.mu.Unlock()
	close(e.quit)
	<-e.stopped
	for _, f := range feeds {
		f.stop()
	}
}
