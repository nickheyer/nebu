package gateway

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nickheyer/nebu/internal/chat"
	"github.com/nickheyer/nebu/internal/db"
	"github.com/nickheyer/nebu/pkg/events"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	// Bytes of each request, upstream request and response body a trace keeps
	traceBodyCap = 1 << 20
	// Finished traces waiting for the store before a finishing request waits for room
	persistQueue = 4096
	// Time between prunes of traces past their retention
	pruneEvery = time.Hour
	// First wait before a refused trace write is tried again, doubling up to persistRetryMax
	persistRetry    = 50 * time.Millisecond
	persistRetryMax = 5 * time.Second
	// How long the stopping writer keeps retrying refused writes before it gives up on one
	persistDrain = 10 * time.Second
)

// Where traces are kept, the database in the daemon
type traceStore interface {
	PutTrace(ctx context.Context, owner string, t *v1.Trace) error
	PruneTraces(ctx context.Context, before time.Time) (int64, error)
	ListTraces(ctx context.Context, q db.TraceQuery) ([]*v1.Trace, error)
	GetTrace(ctx context.Context, owner string, anyOwner bool, id string) (*v1.Trace, error)
}

// Who reads traces, an owner seeing their own and an admin or an open gateway seeing every account's
type Viewer struct {
	Owner string
	All   bool
}

// Whether the viewer may read a trace kept under owner
func (v Viewer) sees(owner string) bool { return v.All || v.Owner == owner }

// What a trace listing narrows by, every field off when zero
type TraceFilter struct {
	Route string
	// Kinds listed, every kind when empty
	Kinds []v1.TraceKind
	// First digits of the statuses listed, 2 for every 2xx answer, every status when empty
	StatusClasses []uint32
	// Nodes listed by the answering or the entry node, every node when empty
	NodeIDs []string
	// Objectives listed, every trace when empty
	Objectives []string
	FairnessID string
	// Traces started at or after From and before To
	From, To time.Time
	// Words the trace holds in its id, request id, route, pool, endpoint, node, objective, fairness id, or error
	Query string
}

// Stored traces per read while a filtered listing walks the store
const traceChunk = 500

// Whether the trace is one the filter lists
func (f TraceFilter) admits(t *v1.Trace) bool {
	if f.Route != "" && t.GetRoute() != f.Route {
		return false
	}
	if len(f.Kinds) > 0 && !slices.Contains(f.Kinds, t.GetKind()) {
		return false
	}
	if len(f.StatusClasses) > 0 && !slices.Contains(f.StatusClasses, t.GetStatus()/100) {
		return false
	}
	if len(f.NodeIDs) > 0 && !slices.Contains(f.NodeIDs, t.GetNodeId()) && !slices.Contains(f.NodeIDs, t.GetEntryNode()) {
		return false
	}
	if len(f.Objectives) > 0 && !slices.Contains(f.Objectives, t.GetObjective()) {
		return false
	}
	if f.FairnessID != "" && t.GetFairnessId() != f.FairnessID {
		return false
	}
	started := t.GetStartedAt().AsTime()
	if !f.From.IsZero() && started.Before(f.From) {
		return false
	}
	if !f.To.IsZero() && !started.Before(f.To) {
		return false
	}
	words := strings.Fields(strings.ToLower(f.Query))
	if len(words) == 0 {
		return true
	}
	hay := strings.ToLower(strings.Join([]string{t.GetId(), t.GetRequestId(), t.GetRoute(), t.GetPoolId(), t.GetEndpoint(), t.GetNodeId(), t.GetEntryNode(), t.GetObjective(), t.GetFairnessId(), t.GetError()}, "\n"))
	for _, w := range words {
		if !strings.Contains(hay, w) {
			return false
		}
	}
	return true
}

// One trace as the ring holds it, immutable once stored, with the subject it is kept under
type record struct {
	t     *v1.Trace
	owner string
}

// A finished trace on its way to the store
type persisted struct {
	t     *v1.Trace
	owner string
}

// Keeps recent traces in a lock free ring, persists finished ones and publishes start and finish events
type Recorder struct {
	db     traceStore
	events *events.Bus
	log    *slog.Logger
	clock  Clock
	ring   []atomic.Pointer[record]
	next   atomic.Uint64
	queue  chan persisted
	// Held shared by a finishing trace while it queues and whole by the writer marking itself stopped
	mu sync.RWMutex
	// Whether the writer stopped, after which finishing traces are written at once
	stopped bool
}

func newRecorder(store traceStore, bus *events.Bus, log *slog.Logger, clock Clock, size int) *Recorder {
	return &Recorder{db: store, events: bus, log: log, clock: clock, ring: make([]atomic.Pointer[record], size), queue: make(chan persisted, persistQueue)}
}

// Writes finished traces to the store and prunes the ones past retention until ctx ends, draining what is queued
func (r *Recorder) run(ctx context.Context, retention time.Duration) {
	prune := time.NewTicker(pruneEvery)
	defer prune.Stop()
	r.prune(retention)
	for {
		select {
		case <-ctx.Done():
			r.stop(ctx)
			return
		case p := <-r.queue:
			r.persist(ctx, p)
		case <-prune.C:
			r.prune(retention)
		}
	}
}

// Keeps writing queued traces until no finishing trace can queue another, then writes the rest of the queue
func (r *Recorder) stop(ctx context.Context) {
	marked := make(chan struct{})
	go func() {
		r.mu.Lock()
		r.stopped = true
		r.mu.Unlock()
		close(marked)
	}()
	for {
		select {
		case p := <-r.queue:
			r.persist(ctx, p)
		case <-marked:
			for {
				select {
				case p := <-r.queue:
					r.persist(ctx, p)
				default:
					return
				}
			}
		}
	}
}

// Writes one trace, retrying a refused write with growing waits for persistDrain, then giving it up so the queue never stalls requests
func (r *Recorder) persist(ctx context.Context, p persisted) {
	wait := persistRetry
	var deadline time.Time
	for attempt := 1; ; attempt++ {
		err := r.db.PutTrace(context.Background(), p.owner, p.t)
		if err == nil {
			return
		}
		if deadline.IsZero() {
			deadline = r.clock.Now().Add(persistDrain)
		}
		if !r.clock.Now().Before(deadline) {
			r.log.Error("trace not persisted, given up after retrying", "trace", p.t.GetId(), "attempts", attempt, "stopping", ctx.Err() != nil, "err", err)
			return
		}
		r.log.Warn("trace write refused, retrying", "trace", p.t.GetId(), "attempt", attempt, "wait", wait, "err", err)
		time.Sleep(wait)
		wait = min(wait*2, persistRetryMax)
	}
}

func (r *Recorder) prune(retention time.Duration) {
	n, err := r.db.PruneTraces(context.Background(), time.Now().Add(-retention))
	if err != nil {
		r.log.Error("traces past retention not pruned", "err", err)
		return
	}
	if n > 0 {
		r.log.Debug("traces past retention pruned", "count", n)
	}
}

// Records a trace as it starts under its owner, answering the ring slot it took
func (r *Recorder) start(t *v1.Trace, owner string) uint64 {
	t.Owner = owner
	snap := proto.Clone(t).(*v1.Trace)
	slot := r.next.Add(1) - 1
	r.ring[slot%uint64(len(r.ring))].Store(&record{t: snap, owner: owner})
	r.events.Publish(v1.EventKind_EVENT_KIND_TRACE, v1.EventAction_EVENT_ACTION_CREATED, snap.GetId(), summary(snap))
	return slot
}

// Stores a finished trace in its ring slot unless taken, then queues it or writes it after the writer stopped
func (r *Recorder) finish(t *v1.Trace, owner string, slot uint64) {
	if t.GetFinishedAt() == nil {
		t.FinishedAt = timestamppb.Now()
	}
	t.Owner = owner
	snap := proto.Clone(t).(*v1.Trace)
	cell := &r.ring[slot%uint64(len(r.ring))]
	if cur := cell.Load(); cur != nil && cur.t.GetId() == snap.GetId() {
		cell.CompareAndSwap(cur, &record{t: snap, owner: owner})
	}
	r.events.Publish(v1.EventKind_EVENT_KIND_TRACE, v1.EventAction_EVENT_ACTION_UPDATED, snap.GetId(), summary(snap))
	p := persisted{t: snap, owner: owner}
	r.mu.RLock()
	if !r.stopped {
		r.queue <- p
		r.mu.RUnlock()
		return
	}
	r.mu.RUnlock()
	stopped, cancel := context.WithCancel(context.Background())
	cancel()
	r.persist(stopped, p)
}

// The ring's traces the viewer may read and the filter admits, newest first
func (r *Recorder) recent(v Viewer, f TraceFilter) []*record {
	n := r.next.Load()
	size := uint64(len(r.ring))
	var out []*record
	for i := uint64(0); i < min(n, size); i++ {
		rec := r.ring[(n-1-i)%size].Load()
		if rec == nil || !v.sees(rec.owner) || !f.admits(rec.t) {
			continue
		}
		out = append(out, rec)
	}
	return out
}

// Traces newest first without bodies from the ring and the store, the page at offset of limit traces,
// every match when limit is zero, and whether more follow the page
func (r *Recorder) List(ctx context.Context, v Viewer, f TraceFilter, offset, limit int) ([]*v1.Trace, bool, error) {
	seen := map[string]bool{}
	var out []*v1.Trace
	for _, rec := range r.recent(v, f) {
		seen[rec.t.GetId()] = true
		out = append(out, summary(rec.t))
	}
	need := 0
	if limit > 0 {
		need = offset + limit + 1
	}
	q := db.TraceQuery{Owner: v.Owner, AllOwners: v.All, Route: f.Route, Kinds: f.Kinds, From: f.From, To: f.To, Limit: traceChunk}
	for need == 0 || len(out) < need {
		stored, err := r.db.ListTraces(ctx, q)
		if err != nil {
			return nil, false, err
		}
		for _, t := range stored {
			if !seen[t.GetId()] && f.admits(t) {
				seen[t.GetId()] = true
				out = append(out, t)
			}
		}
		if len(stored) < traceChunk {
			break
		}
		q.Offset += traceChunk
	}
	slices.SortStableFunc(out, func(a, b *v1.Trace) int { return b.GetStartedAt().AsTime().Compare(a.GetStartedAt().AsTime()) })
	offset = min(offset, len(out))
	end := len(out)
	if limit > 0 && offset+limit < end {
		end = offset + limit
	}
	return out[offset:end], end < len(out), nil
}

// One trace with bodies from the ring or the store, false when absent or hidden from the viewer
func (r *Recorder) Get(ctx context.Context, v Viewer, id string) (*v1.Trace, bool, error) {
	for _, rec := range r.recent(v, TraceFilter{}) {
		if rec.t.GetId() == id {
			return proto.Clone(rec.t).(*v1.Trace), true, nil
		}
	}
	t, err := r.db.GetTrace(ctx, v.Owner, v.All, id)
	if errors.Is(err, db.ErrNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return t, true, nil
}

// Trace metadata for lists and events, the request, response and tool call bodies left out
func summary(t *v1.Trace) *v1.Trace {
	out := proto.Clone(t).(*v1.Trace)
	out.Request, out.UpstreamRequest, out.Response = "", "", ""
	for _, c := range out.ToolCalls {
		c.Arguments, c.Result = "", ""
	}
	return out
}

// A body as a trace keeps it, cut to the trace cap at a character boundary
func capped(b []byte) string {
	if len(b) <= traceBodyCap {
		return string(b)
	}
	return strings.ToValidUTF8(string(b[:traceBodyCap]), "")
}

// Text as a trace keeps it, cut to the trace cap at a character boundary without a byte copy
func cappedString(s string) string {
	if len(s) <= traceBodyCap {
		return s
	}
	return strings.ToValidUTF8(s[:traceBodyCap], "")
}

// Records first token timing, response text, tool calls and usage of an answer as its events arrive
type traceSink struct {
	t     *v1.Trace
	clock Clock
	// Whether the answer's text is kept for the trace, off for a ghost request that leaves none
	keep bool
	text strings.Builder
	// When the first text, thinking or call arrived, zero until one did
	first time.Time
	res   *chat.Result
	// The first round's usage, set once it ended, keeping tool rounds out of the trace's prompt tokens
	prompt *chat.Usage
}

// Keeps the first round's prompt and cached tokens for the trace
func (s *traceSink) firstRound(u chat.Usage) {
	if s.prompt == nil {
		s.prompt = &u
	}
}

func (s *traceSink) event(ev chat.Event) {
	switch ev.Kind {
	case chat.Said:
		s.mark()
		if s.keep && s.text.Len() < traceBodyCap {
			s.text.WriteString(ev.Text)
		}
	case chat.Thought, chat.Called:
		s.mark()
	case chat.Stopped:
		if ev.Res != nil {
			s.result(ev.Res)
		}
	case chat.Failed:
		if s.t.Error == "" && ev.Failure != nil {
			s.t.Error = ev.Failure.Message
		}
	}
}

// Stamps the first token time once
func (s *traceSink) mark() {
	if s.first.IsZero() {
		s.first = s.clock.Now()
		s.t.FirstTokenAt = timestamppb.New(s.first)
	}
}

// Records a finished answer's usage, stop reason and calls
func (s *traceSink) result(res *chat.Result) {
	s.res = res
	s.t.PromptTokens, s.t.CompletionTokens, s.t.Stop = uint32(res.Usage.In), uint32(res.Usage.Out), res.Stop
	s.t.CachedTokens = uint32(res.Usage.Cached)
	if s.prompt != nil {
		s.t.PromptTokens, s.t.CachedTokens = uint32(s.prompt.In), uint32(s.prompt.Cached)
	}
	if res.Usage.DraftOffered > 0 {
		s.t.DraftOffered, s.t.DraftAccepted = uint32(res.Usage.DraftOffered), uint32(res.Usage.DraftAccepted)
	}
	if s.keep && res.Text != "" && s.text.Len() == 0 {
		s.text.WriteString(res.Text)
	}
	s.t.ToolCalls = nil
	for _, run := range res.Runs {
		s.t.ToolCalls = append(s.t.ToolCalls, traceRun(run))
	}
	for _, c := range res.ToolCalls {
		s.t.ToolCalls = append(s.t.ToolCalls, &v1.TraceToolCall{Id: c.ID, Name: c.Name, Arguments: c.Args})
	}
}

// Keeps the answer's text on the trace, capped
func (s *traceSink) close() {
	if s.text.Len() > 0 {
		s.t.Response = cappedString(s.text.String())
	}
}

// Feeds response chunks to a parser goroutine without ever blocking the writer on the parse
type tap struct {
	mu     sync.Mutex
	wake   *sync.Cond
	chunks [][]byte
	cur    []byte
	closed bool
	done   chan struct{}
}

// Starts parsing what the tap is fed, a stream event by event or a whole answer once it ends
func newTap(sink *traceSink, rt chat.Runtime, req *chat.Request) *tap {
	t := &tap{done: make(chan struct{})}
	t.wake = sync.NewCond(&t.mu)
	go func() {
		defer close(t.done)
		defer io.Copy(io.Discard, t)
		if req == nil {
			body, _ := io.ReadAll(io.LimitReader(t, traceBodyCap))
			if sink.keep {
				sink.t.Response = capped(body)
			}
			return
		}
		if req.Stream {
			if err := rt.ParseStream(t, func(ev chat.Event) error {
				sink.event(ev)
				return nil
			}); err != nil && sink.t.Error == "" {
				sink.t.Error = "response: " + err.Error()
			}
			sink.close()
			return
		}
		raw, err := io.ReadAll(io.LimitReader(t, maxBody))
		if err == nil {
			var res *chat.Result
			if res, err = rt.ParseResult(req, raw); err == nil {
				sink.mark()
				sink.result(res)
			}
		}
		if err != nil && sink.t.Error == "" {
			sink.t.Error = "response: " + err.Error()
		}
		sink.close()
	}()
	return t
}

// Queues a chunk the caller no longer writes to, nothing for a request that keeps no tap
func (t *tap) feed(p []byte) {
	if t == nil || len(p) == 0 {
		return
	}
	t.mu.Lock()
	t.chunks = append(t.chunks, p)
	t.mu.Unlock()
	t.wake.Signal()
}

func (t *tap) Read(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for len(t.cur) == 0 {
		if len(t.chunks) > 0 {
			t.cur, t.chunks = t.chunks[0], t.chunks[1:]
			continue
		}
		if t.closed {
			return 0, io.EOF
		}
		t.wake.Wait()
	}
	n := copy(p, t.cur)
	t.cur = t.cur[n:]
	return n, nil
}

// Ends the input and waits for the parse to finish, nothing for a request that keeps no tap
func (t *tap) close() {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.closed = true
	t.mu.Unlock()
	t.wake.Broadcast()
	<-t.done
}

// A run as a trace records it
func traceRun(run *chat.Run) *v1.TraceToolCall {
	return &v1.TraceToolCall{Id: run.Call.ID, Name: run.Call.Name, Arguments: run.Call.Args, Server: run.Server, Result: cappedString(run.Result.Text()), Failed: run.Result.Failed, TookMs: uint32(run.Took.Milliseconds())}
}

// The viewer a caller's credentials make, any daemon credential scoped to its account, nobody when none is valid
func (g *Gateway) ViewerFor(h http.Header) (Viewer, bool) {
	if id, ok := g.guard.Identify(h); ok {
		return Viewer{Owner: firstOf(id.Subject, id.TokenPrefix, id.Provider), All: id.Admin()}, true
	}
	id, ok := g.identify((&http.Request{Header: h}).WithContext(context.Background()))
	return viewerOf(id), ok
}
