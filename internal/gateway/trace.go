package gateway

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/nickheyer/nebu/pkg/events"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	// Maximum retained inference traces.
	traceRing = 500
	// Maximum retained token count traces, stored separately.
	countRing = 50
	// Maximum retained bytes per trace body.
	traceBodyCap = 64 << 10
	// Response header containing the trace ID.
	traceHeader = TraceHeader
)

// The header a gateway answer carries its trace id in
const TraceHeader = "X-Nebu-Trace"

// Records recent gateway requests and publishes trace events.
// The serving goroutine owns each trace and publishes copies at start and finish.
// Token counts use a separate ring so they cannot evict inference traces.
type Recorder struct {
	events *events.Bus

	mu     sync.Mutex
	main   ring
	counts ring
	byID   map[string]*v1.Trace
}

// Traces in start order, evicting the oldest at capacity.
type ring struct {
	size  int
	items []*v1.Trace
}

// Appends a trace and returns evicted IDs.
func (r *ring) add(t *v1.Trace) []string {
	r.items = append(r.items, t)
	var dropped []string
	for len(r.items) > r.size {
		dropped = append(dropped, r.items[0].GetId())
		r.items[0] = nil
		r.items = r.items[1:]
	}
	return dropped
}

// Replaces a trace by ID.
func (r *ring) replace(t *v1.Trace) {
	for i, old := range r.items {
		if old.GetId() == t.GetId() {
			r.items[i] = t
			return
		}
	}
}

// Creates a recorder with separate inference and token count capacities. Zero uses defaults.
func NewRecorder(bus *events.Bus, size, counts int) *Recorder {
	if size <= 0 {
		size = traceRing
	}
	if counts <= 0 {
		counts = countRing
	}
	return &Recorder{events: bus, main: ring{size: size}, counts: ring{size: counts}, byID: map[string]*v1.Trace{}}
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

// Selects the trace ring by request kind.
func (r *Recorder) ringOf(t *v1.Trace) *ring {
	if t.GetKind() == v1.TraceKind_TRACE_KIND_COUNT {
		return &r.counts
	}
	return &r.main
}

func (r *Recorder) put(t *v1.Trace, action v1.EventAction) {
	snapshot := proto.Clone(t).(*v1.Trace)
	r.mu.Lock()
	rg := r.ringOf(snapshot)
	if _, known := r.byID[snapshot.GetId()]; !known {
		for _, id := range rg.add(snapshot) {
			delete(r.byID, id)
		}
	} else {
		rg.replace(snapshot)
	}
	r.byID[snapshot.GetId()] = snapshot
	r.mu.Unlock()
	r.events.Publish(v1.EventKind_EVENT_KIND_TRACE, action, snapshot.GetId(), summary(snapshot))
}

func startedBefore(a, b *v1.Trace) bool {
	x, y := a.GetStartedAt(), b.GetStartedAt()
	if x.GetSeconds() != y.GetSeconds() {
		return x.GetSeconds() < y.GetSeconds()
	}
	return x.GetNanos() < y.GetNanos()
}

// Lists traces newest first without bodies. Optional route and limit narrow the results.
func (r *Recorder) List(route string, limit int) []*v1.Trace {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*v1.Trace
	i, j := len(r.main.items)-1, len(r.counts.items)-1
	for i >= 0 || j >= 0 {
		var t *v1.Trace
		// Merge newest first, preferring inference traces on ties.
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

// Copies trace metadata without bodies for lists and events.
func summary(t *v1.Trace) *v1.Trace {
	out := proto.Clone(t).(*v1.Trace)
	out.Request, out.UpstreamRequest, out.Response = "", "", ""
	return out
}

// Truncates a body for trace storage.
func capped(b []byte) string {
	if len(b) <= traceBodyCap {
		return string(b)
	}
	return string(b[:traceBodyCap])
}

// Classifies request paths, using other for unparsed proxy requests.
func kindOf(api v1.ApiFlavor, path string) v1.TraceKind {
	switch api {
	case v1.ApiFlavor_API_FLAVOR_ANTHROPIC:
		switch path {
		case anthropicMessages:
			return v1.TraceKind_TRACE_KIND_CHAT
		case anthropicCount:
			return v1.TraceKind_TRACE_KIND_COUNT
		}
	case v1.ApiFlavor_API_FLAVOR_OLLAMA:
		switch path {
		case ollamaChat:
			return v1.TraceKind_TRACE_KIND_CHAT
		case ollamaGenerate:
			return v1.TraceKind_TRACE_KIND_GENERATE
		case ollamaEmbed, ollamaEmbeddings:
			return v1.TraceKind_TRACE_KIND_EMBED
		}
	default:
		switch path {
		case openaiChat:
			return v1.TraceKind_TRACE_KIND_CHAT
		case openaiCompletions:
			return v1.TraceKind_TRACE_KIND_GENERATE
		case openaiEmbeddings:
			return v1.TraceKind_TRACE_KIND_EMBED
		case imagesPath, editsPath:
			return v1.TraceKind_TRACE_KIND_IMAGE
		case videosPath:
			return v1.TraceKind_TRACE_KIND_VIDEO
		}
	}
	return v1.TraceKind_TRACE_KIND_OTHER
}

// Records first token timing, response text, tool calls, and usage.
type traceSink struct {
	t    *v1.Trace
	text strings.Builder
}

func (s *traceSink) event(ev Event) {
	switch ev.Kind {
	case "text":
		s.first()
		if s.text.Len() < traceBodyCap {
			s.text.WriteString(ev.Text)
		}
	case "tool":
		s.first()
	case "stop":
		if ev.Res != nil {
			s.result(ev.Res)
		}
	case "error":
		if s.t.Error == "" {
			s.t.Error = ev.Text
		}
	}
}

func (s *traceSink) first() {
	if s.t.FirstTokenAt == nil {
		s.t.FirstTokenAt = timestamppb.Now()
	}
}

// Records a finished answer
func (s *traceSink) result(res *Result) {
	s.t.PromptTokens, s.t.CompletionTokens, s.t.Stop = uint32(res.In), uint32(res.Out), res.Stop
	if res.DraftOffered > 0 {
		s.t.DraftOffered, s.t.DraftAccepted = uint32(res.DraftOffered), uint32(res.DraftAccepted)
	}
	if res.Text != "" && s.text.Len() == 0 {
		s.text.WriteString(res.Text)
	}
	s.t.ToolCalls = nil
	for _, c := range res.ToolCalls {
		s.t.ToolCalls = append(s.t.ToolCalls, &v1.TraceToolCall{Id: c.ID, Name: c.Name, Arguments: c.Args})
	}
}

// Response representation stored in the trace.
func (s *traceSink) close() {
	if s.text.Len() > 0 {
		s.t.Response = capped([]byte(s.text.String()))
	}
}

// Stream writer that records events before forwarding them.
type tracedStream struct {
	StreamWriter
	sink *traceSink
}

func (s tracedStream) Write(ev Event) error {
	s.sink.event(ev)
	return s.StreamWriter.Write(ev)
}

// Records response status, first byte timing, and size while copying the body.
type traceWriter struct {
	http.ResponseWriter
	t      *v1.Trace
	status int
	bytes  uint64
	tee    io.Writer
	wrote  bool
	// Whether an asynchronous job closes the trace.
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
	flush(w.ResponseWriter)
}

// Parses proxied responses into traces through a pipe. Streams are parsed
// as events arrive. Complete responses are parsed after the body ends.
type passthroughReader struct {
	pw   *io.PipeWriter
	done chan struct{}
}

func readPassthrough(t *v1.Trace, upstream Flavor, c *Chat) *passthroughReader {
	pr, pw := io.Pipe()
	p := &passthroughReader{pw: pw, done: make(chan struct{})}
	sink := &traceSink{t: t}
	go func() {
		defer close(p.done)
		defer io.Copy(io.Discard, pr)
		if c.Stream {
			if err := upstream.ParseStream(pr, func(ev Event) error {
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
			if res, err := upstream.ParseResult(c, raw); err == nil {
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

// Closes the body copy and waits for parsing.
func (p *passthroughReader) close() {
	p.pw.Close()
	<-p.done
}

// Stores a prefix of unparsed responses.
type rawCapture struct {
	buf bytes.Buffer
}

func (c *rawCapture) Write(b []byte) (int, error) {
	if room := traceBodyCap - c.buf.Len(); room > 0 {
		if len(b) > room {
			b = b[:room]
		}
		c.buf.Write(b)
	}
	return len(b), nil
}
