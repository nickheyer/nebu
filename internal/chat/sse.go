package chat

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"time"
)

// Silence on a stream before a keepalive is written
const keepaliveEvery = 15 * time.Second

// Reads server sent events, calling fn with each event name and data
func ReadSSE(r io.Reader, fn func(event string, data []byte) error) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 16<<20)
	var event string
	var data bytes.Buffer
	flush := func() error {
		if data.Len() == 0 {
			event = ""
			return nil
		}
		err := fn(event, bytes.TrimSpace(data.Bytes()))
		event = ""
		data.Reset()
		return err
	}
	for sc.Scan() {
		line := sc.Bytes()
		switch {
		case len(line) == 0:
			if err := flush(); err != nil {
				return err
			}
		case bytes.HasPrefix(line, []byte("event:")):
			event = string(bytes.TrimSpace(line[len("event:"):]))
		case bytes.HasPrefix(line, []byte("data:")):
			line = line[len("data:"):]
			data.Write(bytes.TrimPrefix(line, []byte(" ")))
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return flush()
}

// Writes one server sent event and flushes it
func writeSSE(w http.ResponseWriter, event string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return writeSSEData(w, event, data)
}

// Writes one server sent event of data already in JSON and flushes it
func writeSSEData(w http.ResponseWriter, event string, data []byte) error {
	if _, err := w.Write(sseEvent(event, data)); err != nil {
		return err
	}
	flush(w)
	return nil
}

// One server sent event of data already in JSON, named when event is set
func sseEvent(event string, data []byte) []byte {
	buf := make([]byte, 0, len(data)+len(event)+16)
	if event != "" {
		buf = append(buf, "event: "...)
		buf = append(buf, event...)
		buf = append(buf, '\n')
	}
	buf = append(buf, "data: "...)
	buf = append(buf, data...)
	return append(buf, '\n', '\n')
}

// One server sent event of v encoded as JSON
func sseJSON(event string, v any) ([]byte, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return sseEvent(event, data), nil
}

func flush(w http.ResponseWriter) {
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

// Opens a streamed answer with the headers proxies and browsers need to pass it through live
func streamHeaders(w http.ResponseWriter, contentType string) {
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flush(w)
}

// Writes a JSON body with a status
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// Serializes stream writes and writes a keepalive once the stream has been silent for keepaliveEvery
type liveWriter struct {
	w    http.ResponseWriter
	ping func(http.ResponseWriter) error
	mu   sync.Mutex
	last time.Time
	err  error
	stop chan struct{}
	done chan struct{}
	once sync.Once
}

func newLiveWriter(w http.ResponseWriter, ping func(http.ResponseWriter) error) *liveWriter {
	l := &liveWriter{w: w, ping: ping, last: time.Now(), stop: make(chan struct{}), done: make(chan struct{})}
	go l.run()
	return l
}

// Wakes when the silence since the last write would reach keepaliveEvery, pinging only if it has
func (l *liveWriter) run() {
	defer close(l.done)
	timer := time.NewTimer(keepaliveEvery)
	defer timer.Stop()
	for {
		select {
		case <-l.stop:
			return
		case <-timer.C:
			l.mu.Lock()
			now := time.Now()
			wait := keepaliveEvery - now.Sub(l.last)
			if wait <= 0 && l.err == nil {
				l.err = l.ping(l.w)
				l.last, wait = now, keepaliveEvery
			}
			failed := l.err != nil
			l.mu.Unlock()
			if failed {
				return
			}
			timer.Reset(wait)
		}
	}
}

// Writes under the lock, every write after a failed one failing the same way
func (l *liveWriter) write(fn func(http.ResponseWriter) error) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.err != nil {
		return l.err
	}
	l.err = fn(l.w)
	l.last = time.Now()
	return l.err
}

func (l *liveWriter) sse(event string, v any) error {
	return l.write(func(w http.ResponseWriter) error { return writeSSE(w, event, v) })
}

// Writes one event whose data is already JSON
func (l *liveWriter) sseData(event string, data []byte) error {
	return l.write(func(w http.ResponseWriter) error { return writeSSEData(w, event, data) })
}

// Stops keepalives and waits for one in progress to finish
func (l *liveWriter) close() {
	l.once.Do(func() { close(l.stop) })
	<-l.done
}

// An SSE comment line, which every SSE client skips
func pingComment(w http.ResponseWriter) error {
	if _, err := io.WriteString(w, ": ping\n\n"); err != nil {
		return err
	}
	flush(w)
	return nil
}
