package gateway

import (
	"fmt"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"io"
	"net/http"
	"sync"
)

// Where in-process requests are addressed, a name the trace shows as the remote
const localBase = "http://nebu.gateway"

// Returns a client that reaches the gateway in process, its requests traced under origin and carrying a
// configured key when the gateway requires one, so a bot's generations are limited and recorded like any other
func (g *Gateway) Client(origin string) *http.Client {
	key := ""
	if len(g.keys) > 0 {
		key = g.keys[0]
	}
	return &http.Client{Transport: &localTransport{handler: g.Handler(), key: key, origin: origin}}
}

// The base URL requests through Client are written against
func (g *Gateway) LocalBase() string { return localBase }

// Returns the flavor of one wire format, for callers that render and read requests themselves
func FlavorFor(api v1.ApiFlavor) Flavor { return flavorOf(api) }

// Serves requests through the handler over a pipe, so streamed answers arrive as they are written
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

// Streams handler output into a pipe, the headers fixed at the first write
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
