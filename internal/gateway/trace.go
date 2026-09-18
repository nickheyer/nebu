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
	// Inference traces the recorder keeps before the oldest is dropped
	traceRing = 500
	// Token count traces the recorder keeps, in a ring of their own
	countRing = 50
	// Bytes of each body a trace keeps
	traceBodyCap = 64 << 10
	// The response header naming the trace of the request
	traceHeader = "X-Nebu-Trace"
)

// Keeps the newest requests through the gateway and tells the stream about each one
//
// A request's trace is written by the goroutine serving it and handed over
// as a copy when it starts and when it ends, so readers never race a writer.
// Token counts ring separately so they never evict answers.
type Recorder struct {
	events *events.Bus

	mu     sync.Mutex
	main   ring
	counts ring
	byID   map[string]*v1.Trace
}

// Traces in the order they started, the oldest dropped past a size
type ring struct {
	size  int
	items []*v1.Trace
}

// Appends a trace, returning the ids of those dropped to make room
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

// Swaps a newer copy in for the trace with its id
func (r *ring) replace(t *v1.Trace) {
	for i, old := range r.items {
		if old.GetId() == t.GetId() {
			r.items[i] = t
			return
		}
	}
}

// Builds a recorder that keeps size inference traces and counts token counts, the defaults when zero
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

// The ring a trace belongs in, by its kind
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

// Whether a started before b
func startedBefore(a, b *v1.Trace) bool {
	x, y := a.GetStartedAt(), b.GetStartedAt()
	if x.GetSeconds() != y.GetSeconds() {
		return x.GetSeconds() < y.GetSeconds()
	}
	return x.GetNanos() < y.GetNanos()
}

// Lists traces newest first across both rings, one route's when named, at most limit when positive, bodies left out
func (r *Recorder) List(route string, limit int) []*v1.Trace {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*v1.Trace
	i, j := len(r.main.items)-1, len(r.counts.items)-1
	for i >= 0 || j >= 0 {
		var t *v1.Trace
		// The newer head of the two rings goes next, the inference ring first on a tie
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

// A trace without its bodies, what lists and events carry
func summary(t *v1.Trace) *v1.Trace {
	out := proto.Clone(t).(*v1.Trace)
	out.Request, out.UpstreamRequest, out.Response = "", "", ""
	return out
}

// Keeps the head of a body for a trace
func capped(b []byte) string {
	if len(b) <= traceBodyCap {
		return string(b)
	}
	return string(b[:traceBodyCap])
}

// The kind of request a path names in a flavor, other for anything the gateway proxies unread
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

// Folds the answer's events into the trace: when the first token came, the text, the calls, and the usage
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
	if res.Text != "" && s.text.Len() == 0 {
		s.text.WriteString(res.Text)
	}
	s.t.ToolCalls = nil
	for _, c := range res.ToolCalls {
		s.t.ToolCalls = append(s.t.ToolCalls, &v1.TraceToolCall{Id: c.ID, Name: c.Name, Arguments: c.Args})
	}
}

// What the trace keeps as the response
func (s *traceSink) close() {
	if s.text.Len() > 0 {
		s.t.Response = capped([]byte(s.text.String()))
	}
}

// A stream writer that feeds every event to the trace on its way to the client
type tracedStream struct {
	StreamWriter
	sink *traceSink
}

func (s tracedStream) Write(ev Event) error {
	s.sink.event(ev)
	return s.StreamWriter.Write(ev)
}

// A response writer that records status, first byte, and size, and copies the body to a reader
type traceWriter struct {
	http.ResponseWriter
	t      *v1.Trace
	status int
	bytes  uint64
	tee    io.Writer
	wrote  bool
	// Whether a job that outlives the request finishes the trace itself
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

// Reads a passed through response the way the upstream flavor writes it, filling the trace
//
// The body is fed through a pipe as it is written to the client, so a
// streamed answer is parsed as it flows and a whole one once it ends.
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

// Ends the copy and waits for the parse
func (p *passthroughReader) close() {
	p.pw.Close()
	<-p.done
}

// Keeps the head of a response the gateway does not read
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
