package cli

import (
	"fmt"
	"io"
	"net/http"
	"sync"
)

// Serves requests through an in process handler
type handlerTransport struct {
	handler http.Handler
}

func (t handlerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	pr, pw := io.Pipe()
	w := &responseWriter{header: http.Header{}, pw: pw, ready: make(chan struct{})}
	go func() {
		defer pw.Close()
		defer w.WriteHeader(http.StatusOK)
		t.handler.ServeHTTP(w, req)
	}()
	select {
	case <-w.ready:
	case <-req.Context().Done():
		pr.Close()
		return nil, req.Context().Err()
	}
	return &http.Response{
		Status:     fmt.Sprintf("%d %s", w.status, http.StatusText(w.status)),
		StatusCode: w.status,
		Proto:      "HTTP/1.1",
		ProtoMajor: 1,
		ProtoMinor: 1,
		Header:     w.snapshot,
		Body:       pr,
		Request:    req,
	}, nil
}

// Streams handler output into a pipe
type responseWriter struct {
	header   http.Header
	snapshot http.Header
	pw       *io.PipeWriter
	status   int
	ready    chan struct{}
	once     sync.Once
}

func (w *responseWriter) Header() http.Header { return w.header }

func (w *responseWriter) WriteHeader(code int) {
	w.once.Do(func() {
		w.status = code
		w.snapshot = w.header.Clone()
		close(w.ready)
	})
}

func (w *responseWriter) Write(p []byte) (int, error) {
	w.WriteHeader(http.StatusOK)
	return w.pw.Write(p)
}

func (w *responseWriter) Flush() { w.WriteHeader(http.StatusOK) }
