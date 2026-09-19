package gateway

import (
	"fmt"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"io"
	"net/http"
	"sync"
)

// In-process request address, recorded as the trace's remote address.
const localBase = "http://nebu.gateway"

// Returns an in-process gateway client with an origin label and configured key.
// Requests use normal gateway limits and tracing.
func (g *Gateway) Client(origin string) *http.Client {
	key := ""
	if len(g.keys) > 0 {
		key = g.keys[0]
	}
	return &http.Client{Transport: &localTransport{handler: g.Handler(), key: key, origin: origin}}
}

// Base URL for in-process client requests.
func (g *Gateway) LocalBase() string { return localBase }

// Returns a protocol adapter for callers that render and parse requests.
func FlavorFor(api v1.ApiFlavor) Flavor { return flavorOf(api) }

// Serves handler responses through a pipe for streaming.
type localTransport struct {
	handler http.Handler
	key     string
	origin  string
}

func (t *localTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.RemoteAddr = t.origin
	if t.key != "" && req.Header.Get("Authorization") == "" && req.Header.Get("X-Api-Key") == "" {
		req.Header.Set("Authorization", "Bearer "+t.key)
	}
	pr, pw := io.Pipe()
	w := &pipeWriter{header: http.Header{}, pw: pw, ready: make(chan struct{})}
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

// Streams handler output through a pipe, fixing headers on the first write.
type pipeWriter struct {
	header   http.Header
	snapshot http.Header
	pw       *io.PipeWriter
	status   int
	ready    chan struct{}
	once     sync.Once
}

func (w *pipeWriter) Header() http.Header { return w.header }

func (w *pipeWriter) WriteHeader(code int) {
	w.once.Do(func() {
		w.status = code
		w.snapshot = w.header.Clone()
		close(w.ready)
	})
}

func (w *pipeWriter) Write(p []byte) (int, error) {
	w.WriteHeader(http.StatusOK)
	return w.pw.Write(p)
}

func (w *pipeWriter) Flush() { w.WriteHeader(http.StatusOK) }
