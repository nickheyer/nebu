package gateway

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/nickheyer/nebu/internal/chat"
	"github.com/nickheyer/nebu/pkg/events"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	// Maximum retained inference traces
	traceRing = 500
	// Maximum retained token count traces, stored separately
	countRing = 50
	// Bytes of request and response bodies held across every retained trace
	traceBudget = 256 << 20
	traceHeader = TraceHeader
)

// The header a gateway answer carries its trace id in
const TraceHeader = "X-Nebu-Trace"

// Keeps recent requests whole in two rings, the oldest inference traces leaving past the byte budget
type Recorder struct {
	events *events.Bus

	mu     sync.Mutex
	main   ring
	counts ring
	byID   map[string]*v1.Trace
	// Bytes of bodies held across both rings, and the most allowed
	held, budget int
}

// Traces in start order, evicting the oldest at capacity
type ring struct {
	size  int
	items []*v1.Trace
}

// Appends a trace and returns the traces evicted
func (r *ring) add(t *v1.Trace) []*v1.Trace {
	r.items = append(r.items, t)
	var dropped []*v1.Trace
	for len(r.items) > r.size {
		dropped = append(dropped, r.evict())
	}
	return dropped
}

// Removes the oldest trace
func (r *ring) evict() *v1.Trace {
	t := r.items[0]
	r.items[0] = nil
	r.items = r.items[1:]
	return t
}

// Replaces a trace by id
func (r *ring) replace(t *v1.Trace) {
	for i, old := range r.items {
		if old.GetId() == t.GetId() {
			r.items[i] = t
			return
		}
	}
}

// Creates a recorder with separate inference and token count capacities, zero using the defaults
func NewRecorder(bus *events.Bus, size, counts int) *Recorder {
	if size <= 0 {
		size = traceRing
	}
	if counts <= 0 {
		counts = countRing
	}
	return &Recorder{events: bus, main: ring{size: size}, counts: ring{size: counts}, byID: map[string]*v1.Trace{}, budget: traceBudget}
}

// Records a trace as it starts
func (r *Recorder) Start(t *v1.Trace) {
	r.put(t, v1.EventAction_EVENT_ACTION_CREATED)
}

// Records a trace as it ends
func (r *Recorder) Finish(t *v1.Trace) {
	if t.GetFinishedAt() == nil {
		t.FinishedAt = timestamppb.Now()
	}
	r.put(t, v1.EventAction_EVENT_ACTION_UPDATED)
}

// Selects the trace ring by request kind
func (r *Recorder) ringOf(t *v1.Trace) *ring {
	if t.GetKind() == v1.TraceKind_TRACE_KIND_COUNT {
		return &r.counts
	}
	return &r.main
}

// Keeps one copy of the trace with its bodies, the event carrying a copy without them
func (r *Recorder) put(t *v1.Trace, action v1.EventAction) {
	snapshot := proto.Clone(t).(*v1.Trace)
	brief := summary(snapshot)
	r.mu.Lock()
	rg := r.ringOf(snapshot)
	if old, known := r.byID[snapshot.GetId()]; known {
		r.held -= weight(old)
		rg.replace(snapshot)
	} else {
		for _, gone := range rg.add(snapshot) {
			r.forget(gone)
		}
	}
	r.byID[snapshot.GetId()] = snapshot
	r.held += weight(snapshot)
	// The newest trace stays whatever its size
	for r.held > r.budget && len(r.main.items) > 1 {
		r.forget(r.main.evict())
	}
	r.mu.Unlock()
	r.events.Publish(v1.EventKind_EVENT_KIND_TRACE, action, brief.GetId(), brief)
}

// Drops an evicted trace from the index and the bytes held
func (r *Recorder) forget(t *v1.Trace) {
	delete(r.byID, t.GetId())
	r.held -= weight(t)
}

// Bytes of bodies a trace holds
func weight(t *v1.Trace) int {
	n := len(t.GetRequest()) + len(t.GetUpstreamRequest()) + len(t.GetResponse())
	for _, c := range t.GetToolCalls() {
		n += len(c.GetArguments()) + len(c.GetResult())
	}
	return n
}

func startedBefore(a, b *v1.Trace) bool {
	x, y := a.GetStartedAt(), b.GetStartedAt()
	if x.GetSeconds() != y.GetSeconds() {
		return x.GetSeconds() < y.GetSeconds()
	}
	return x.GetNanos() < y.GetNanos()
}

// Lists traces newest first without bodies, a route and limit narrowing the results
func (r *Recorder) List(route string, limit int) []*v1.Trace {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*v1.Trace
	i, j := len(r.main.items)-1, len(r.counts.items)-1
	for i >= 0 || j >= 0 {
		var t *v1.Trace
		// Newest first, inference traces winning ties
		if j < 0 || (i >= 0 && !startedBefore(r.main.items[i], r.counts.items[j])) {
			t = r.main.items[i]
			i--
		} else {
			t = r.counts.items[j]
			j--
		}
		if route != "" && t.GetRoute() != route {
			continue
		}
		out = append(out, summary(t))
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

// Returns one trace with its bodies
func (r *Recorder) Get(id string) (*v1.Trace, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.byID[id]
	if !ok {
		return nil, false
	}
	return proto.Clone(t).(*v1.Trace), true
}

// Copies trace metadata without bodies for lists and events, the bodies set aside while it copies
func summary(t *v1.Trace) *v1.Trace {
	request, upstream, response := t.Request, t.UpstreamRequest, t.Response
	t.Request, t.UpstreamRequest, t.Response = "", "", ""
	out := proto.Clone(t).(*v1.Trace)
	t.Request, t.UpstreamRequest, t.Response = request, upstream, response
	return out
}

// Records first token timing, response text, tool calls, and usage
type traceSink struct {
	t    *v1.Trace
	text strings.Builder
}

func (s *traceSink) event(ev chat.Event) {
	switch ev.Kind {
	case chat.Said:
		s.first()
		s.text.WriteString(ev.Text)
	case chat.Thought, chat.Called:
		s.first()
	case chat.Stopped:
		if ev.Res != nil {
			s.result(ev.Res)
		}
	case chat.Failed:
		if s.t.Error == "" {
			s.t.Error = ev.Failure.Message
		}
	}
}

func (s *traceSink) first() {
	if s.t.FirstTokenAt == nil {
		s.t.FirstTokenAt = timestamppb.Now()
	}
}

// Records a finished answer
func (s *traceSink) result(res *chat.Result) {
	s.t.PromptTokens, s.t.CompletionTokens, s.t.Stop = uint32(res.Usage.In), uint32(res.Usage.Out), res.Stop
	s.t.CachedTokens = uint32(res.Usage.Cached)
	if res.Usage.DraftOffered > 0 {
		s.t.DraftOffered, s.t.DraftAccepted = uint32(res.Usage.DraftOffered), uint32(res.Usage.DraftAccepted)
	}
	if res.Text != "" && s.text.Len() == 0 {
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

// Keeps the answer's text on the trace
func (s *traceSink) close() {
	if s.text.Len() > 0 {
		s.t.Response = s.text.String()
	}
}

// Records response status, first byte timing, and size while copying the body
type traceWriter struct {
	http.ResponseWriter
	t      *v1.Trace
	status int
	bytes  uint64
	tee    io.Writer
	wrote  bool
	// Whether an asynchronous job closes the trace
	detached bool
}

func (w *traceWriter) WriteHeader(status int) {
	if !w.wrote {
		w.wrote = true
		w.status = status
		w.t.FirstByteAt = timestamppb.Now()
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *traceWriter) Write(b []byte) (int, error) {
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += uint64(n)
	if w.tee != nil && n > 0 {
		w.tee.Write(b[:n])
	}
	return n, err
}

func (w *traceWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Parses a proxied answer into the trace through a pipe, streams as events arrive and whole answers at the end
type passthroughReader struct {
	pw   *io.PipeWriter
	done chan struct{}
}

func readPassthrough(t *v1.Trace, runtime chat.Runtime, req *chat.Request) *passthroughReader {
	pr, pw := io.Pipe()
	p := &passthroughReader{pw: pw, done: make(chan struct{})}
	sink := &traceSink{t: t}
	go func() {
		defer close(p.done)
		defer io.Copy(io.Discard, pr)
		if req.Stream {
			if err := runtime.ParseStream(pr, func(ev chat.Event) error {
				sink.event(ev)
				return nil
			}); err != nil && t.Error == "" {
				t.Error = "response: " + err.Error()
			}
		} else {
			raw, err := io.ReadAll(io.LimitReader(pr, maxBody))
			if err != nil {
				if t.Error == "" {
					t.Error = "response: " + err.Error()
				}
				return
			}
			if res, err := runtime.ParseResult(req, raw); err == nil {
				sink.first()
				sink.result(res)
			} else if t.Error == "" {
				t.Error = "response: " + err.Error()
			}
		}
		sink.close()
	}()
	return p
}

// Closes the body copy and waits for parsing
func (p *passthroughReader) close() {
	p.pw.Close()
	<-p.done
}

// Keeps an unparsed answer whole
type rawCapture struct {
	buf bytes.Buffer
}

func (c *rawCapture) Write(b []byte) (int, error) {
	return c.buf.Write(b)
}
