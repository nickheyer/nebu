package transcode

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Silence after which the writer sends a keepalive comment
const KeepaliveInterval = 15 * time.Second

var errSSEClosed = errors.New("transcode: sse writer closed")

// Writes server sent events, flushing each one and sending a keepalive comment after idle periods
type SSEWriter struct {
	mu    sync.Mutex
	w     http.ResponseWriter
	rc    *http.ResponseController
	idle  time.Duration
	timer *time.Timer
	last  time.Time
	err   error
	done  bool
}

// Sends event stream headers and starts the keepalive timer
func NewSSEWriter(w http.ResponseWriter) (*SSEWriter, error) {
	return newSSEWriter(w, KeepaliveInterval)
}

// Sends event stream headers and arms a keepalive that fires after idle of silence
func newSSEWriter(w http.ResponseWriter, idle time.Duration) (*SSEWriter, error) {
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	s := &SSEWriter{w: w, rc: http.NewResponseController(w), idle: idle, last: time.Now()}
	if err := s.rc.Flush(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.timer = time.AfterFunc(idle, s.keepalive)
	s.mu.Unlock()
	return s, nil
}

// Writes one event with a data line per payload line, any line ending splitting lines, and flushes it
func (s *SSEWriter) Data(payload []byte) error {
	var buf bytes.Buffer
	payload = bytes.ReplaceAll(bytes.ReplaceAll(payload, []byte("\r\n"), []byte("\n")), []byte("\r"), []byte("\n"))
	for line := range bytes.SplitSeq(payload, []byte("\n")) {
		buf.WriteString("data: ")
		buf.Write(line)
		buf.WriteByte('\n')
	}
	buf.WriteByte('\n')
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done {
		return errSSEClosed
	}
	if s.err != nil {
		return s.err
	}
	s.err = s.write(buf.Bytes())
	return s.err
}

// Sends a keepalive comment when the stream has been silent for the idle interval and rearms the timer
func (s *SSEWriter) keepalive() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done || s.err != nil {
		return
	}
	if quiet := time.Since(s.last); quiet < s.idle {
		s.timer.Reset(s.idle - quiet)
		return
	}
	if s.err = s.write([]byte(": keepalive\n\n")); s.err == nil {
		s.timer.Reset(s.idle)
	}
}

// Writes and flushes bytes while holding the lock, recording the write time
func (s *SSEWriter) write(p []byte) error {
	if _, err := s.w.Write(p); err != nil {
		return err
	}
	s.last = time.Now()
	return s.rc.Flush()
}

// Stops the keepalive so nothing writes after the handler returns
func (s *SSEWriter) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.done = true
	s.timer.Stop()
}

// One parsed server sent event, or a comment line when the comment field is set
type Event struct {
	Type    string
	Data    string
	ID      string
	Comment string
}

// Reads server sent events from a stream following the WHATWG parsing rules
type EventSource struct {
	r    *bufio.Reader
	typ  string
	id   string
	data strings.Builder
	has  bool
}

// Reads events from r
func NewEventSource(r io.Reader) *EventSource {
	return &EventSource{r: bufio.NewReader(r)}
}

// Returns the following dispatched event or comment and io.EOF once the stream ends
func (e *EventSource) Next() (Event, error) {
	for {
		line, err := e.r.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				return Event{}, io.EOF
			}
			return Event{}, err
		}
		line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		if line == "" {
			if !e.has {
				e.typ = ""
				continue
			}
			ev := Event{Type: e.typ, Data: strings.TrimSuffix(e.data.String(), "\n"), ID: e.id}
			e.typ, e.has = "", false
			e.data.Reset()
			return ev, nil
		}
		if rest, ok := strings.CutPrefix(line, ":"); ok {
			return Event{Comment: strings.TrimPrefix(rest, " ")}, nil
		}
		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "data":
			e.data.WriteString(value)
			e.data.WriteByte('\n')
			e.has = true
		case "event":
			e.typ = value
		case "id":
			e.id = value
		}
	}
}
