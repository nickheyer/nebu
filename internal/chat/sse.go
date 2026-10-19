package chat

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
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
		line := sc.Text()
		switch {
		case line == "":
			if err := flush(); err != nil {
				return err
			}
		case strings.HasPrefix(line, "event:"):
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			data.WriteString(strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
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
	if event != "" {
		fmt.Fprintf(w, "event: %s\n", event)
	}
	if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
		return err
	}
	flush(w)
	return nil
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

// Serializes stream writes and pings when the stream has been silent, so idle watchdogs hold on
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

func (l *liveWriter) run() {
	defer close(l.done)
	ticker := time.NewTicker(keepaliveEvery / 3)
	defer ticker.Stop()
	for {
		select {
		case <-l.stop:
			return
		case now := <-ticker.C:
			l.mu.Lock()
			if l.err == nil && now.Sub(l.last) >= keepaliveEvery {
				l.err = l.ping(l.w)
				l.last = now
			}
			l.mu.Unlock()
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
