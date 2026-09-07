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
	// Traces the recorder keeps before the oldest is dropped
	traceRing = 500
	// Bytes of each body a trace keeps
	traceBodyCap = 64 << 10
	// The response header naming the trace of the request
	traceHeader = "X-Nebu-Trace"
)

// Keeps the newest requests through the gateway and tells the stream about each one
//
// A request's trace is written by the goroutine serving it and handed over
// as a copy when it starts and when it ends, so readers never race a writer.
type Recorder struct {
	events *events.Bus
	size   int

	mu   sync.Mutex
	ring []*v1.Trace
	byID map[string]*v1.Trace
}

// Builds a recorder that keeps size traces, the default when zero
func NewRecorder(bus *events.Bus, size int) *Recorder {
	if size <= 0 {
		size = traceRing
	}
	return &Recorder{events: bus, size: size, byID: map[string]*v1.Trace{}}
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

func (r *Recorder) put(t *v1.Trace, action v1.EventAction) {
	snapshot := proto.Clone(t).(*v1.Trace)
	r.mu.Lock()
	if _, known := r.byID[snapshot.GetId()]; !known {
		r.ring = append(r.ring, snapshot)
		for len(r.ring) > r.size {
			delete(r.byID, r.ring[0].GetId())
			r.ring[0] = nil
			r.ring = r.ring[1:]
		}
	} else {
		for i, old := range r.ring {
			if old.GetId() == snapshot.GetId() {
				r.ring[i] = snapshot
				break
			}
		}
	}
	r.byID[snapshot.GetId()] = snapshot
	r.mu.Unlock()
	r.events.Publish(v1.EventKind_EVENT_KIND_TRACE, action, snapshot.GetId(), summary(snapshot))
}

// Lists traces newest first, one route's when named, at most limit when positive, bodies left out
func (r *Recorder) List(route string, limit int) []*v1.Trace {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*v1.Trace
	for i := len(r.ring) - 1; i >= 0; i-- {
		t := r.ring[i]
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
