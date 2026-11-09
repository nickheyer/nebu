package gateway

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/nickheyer/nebu/internal/mesh"
	"github.com/nickheyer/nebu/pkg/bodyparse"
	"github.com/nickheyer/nebu/pkg/datalayer"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/requestcontrol"
	"github.com/nickheyer/nebu/pkg/telemetry"
	"github.com/nickheyer/nebu/pkg/transcode"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// Idle connections kept per upstream host for a route with no in flight cap
const idleUncapped = 256

// What a transport is shared by, its protocol, header timeout and idle connections per host
type transportKey struct {
	proto  datalayer.AppProtocol
	header time.Duration
	idle   int
}

// The pooled keep alive transport for a protocol and policy, HTTP/1.1 by default and HTTP/2 cleartext for h2c pools
func (g *Gateway) transport(proto datalayer.AppProtocol, header time.Duration, idle int) http.RoundTripper {
	key := transportKey{proto: proto, header: header, idle: idle}
	if v, ok := g.transports.Load(key); ok {
		return v.(http.RoundTripper)
	}
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = nil
	t.ResponseHeaderTimeout = header
	t.MaxIdleConns = 0
	t.MaxIdleConnsPerHost = idle
	if g.dial != nil {
		t.DialContext = g.dial
	}
	if proto == datalayer.AppProtocolH2C {
		p := new(http.Protocols)
		p.SetUnencryptedHTTP2(true)
		t.Protocols = p
	}
	v, _ := g.transports.LoadOrStore(key, t)
	return v.(http.RoundTripper)
}

// One request rendered for the runtime, sent unchanged to each endpoint an attempt tries
type upstream struct {
	method string
	path   string
	query  string
	body   []byte
	// The document the body came from, nil for a body rendered by the gateway
	doc    bodyparse.Document
	stream bool
	// The client's headers as a runtime gets them, the decision's headers added
	header http.Header
}

// Headers a runtime never gets, credentials, forwarding marks, the client's trace context, and what the connection itself sets
var clientOnlyHeaders = map[string]bool{
	"Authorization": true, "X-Api-Key": true, "Cookie": true, "Traceparent": true, "Tracestate": true,
	"Forwarded": true, "X-Forwarded-For": true, "X-Forwarded-Host": true, "X-Forwarded-Proto": true,
	"Content-Length": true, "Accept-Encoding": true, "Expect": true, "Connection": true, "Keep-Alive": true,
	"Proxy-Authenticate": true, "Proxy-Authorization": true, "Proxy-Connection": true, "Te": true, "Trailer": true, "Transfer-Encoding": true, "Upgrade": true,
}

// Whether a header belongs to the gateway and the mesh rather than to the runtime
func gatewayOnly(name string) bool {
	return strings.HasPrefix(name, "X-Nebu-") || strings.HasPrefix(name, "X-Gateway-") || name == mesh.HopHeader
}

// The client's headers as a runtime gets them with the decision's headers added
func (x *exchange) runtimeHeaders() http.Header {
	out := http.Header{}
	for name, v := range x.r.Header {
		if !clientOnlyHeaders[name] && !gatewayOnly(name) {
			out[name] = append([]string(nil), v...)
		}
	}
	if x.dec != nil {
		for k, v := range x.dec.Headers {
			out.Set(k, v)
		}
	}
	out.Set(requestcontrol.HeaderRequestID, x.requestID())
	return out
}

// The request id the runtime and the client see, the decision's once one was made
func (x *exchange) requestID() string {
	if x.dec != nil {
		return x.dec.Request.ID
	}
	return x.id
}

// An in flight count per instance, read by the lifecycle drain
func (g *Gateway) instanceCount(instance string) *atomic.Int64 {
	if v, ok := g.inflight.Load(instance); ok {
		return v.(*atomic.Int64)
	}
	v, _ := g.inflight.LoadOrStore(instance, new(atomic.Int64))
	return v.(*atomic.Int64)
}

// Requests in flight on an instance through this gateway
func (g *Gateway) InFlight(instanceID string) int {
	if v, ok := g.inflight.Load(instanceID); ok {
		return int(v.(*atomic.Int64).Load())
	}
	return 0
}

// The count of requests sent to an instance since start, read by idle unload
func (g *Gateway) dispatchCount(instance string) *atomic.Uint64 {
	if v, ok := g.dispatched.Load(instance); ok {
		return v.(*atomic.Uint64)
	}
	v, _ := g.dispatched.LoadOrStore(instance, new(atomic.Uint64))
	return v.(*atomic.Uint64)
}

// Requests this gateway sent an instance since the daemon started, retries to it included
func (g *Gateway) Dispatched(instanceID string) uint64 {
	if v, ok := g.dispatched.Load(instanceID); ok {
		return v.(*atomic.Uint64).Load()
	}
	return 0
}

// Moves the request's count to the instance behind an endpoint, answering the requests ahead of it there
func (x *exchange) countOn(ep datalayer.Endpoint) int64 {
	if x.counted != nil {
		x.counted.Add(-1)
	}
	instance := ep.Labels()[datalayer.LabelInstance]
	x.g.dispatchCount(instance).Add(1)
	x.counted = x.g.instanceCount(instance)
	return x.counted.Add(1) - 1
}

// Releases the request's instance count
func (x *exchange) uncount() {
	if x.counted != nil {
		x.counted.Add(-1)
		x.counted = nil
	}
}

// Sends to each endpoint in order, passing on refusals and failures of requests that never got written while no byte reached the client
func (x *exchange) attempt(ctx context.Context, out *upstream, eps []datalayer.Endpoint) (*http.Response, datalayer.Endpoint, error) {
	var last error
	for i, ep := range eps {
		final := i == len(eps)-1 || x.w.wrote
		resp, written, err := x.sendTo(ctx, ep, out)
		switch {
		case err != nil && !final && retryable(err, out.method, written):
			x.fellBack(ep, err.Error(), written)
			last = err
			continue
		case err != nil:
			return nil, ep, err
		case !final && refused(resp.StatusCode):
			io.Copy(io.Discard, io.LimitReader(resp.Body, maxBody))
			resp.Body.Close()
			x.fellBack(ep, resp.Status, written)
			continue
		}
		return resp, ep, nil
	}
	return nil, nil, last
}

// Records an endpoint the request left for the next one, with whether the request had been written and what the client had by then
func (x *exchange) fellBack(ep datalayer.Endpoint, why string, written bool) {
	x.t.Fallbacks = append(x.t.Fallbacks, &v1.TraceFallback{Address: ep.Address().String(), Reason: why, RequestWritten: written, BytesToClient: x.w.bytes})
	x.g.log.Warn("endpoint did not take the request, passing it to the next", "model", x.model, "endpoint", ep.Address().String(), "why", why, "request_written", written, "bytes_to_client", x.w.bytes)
}

// Sends one rendered request to one endpoint over its pool's protocol inside an upstream span, answering whether the request was fully written
func (x *exchange) sendTo(ctx context.Context, ep datalayer.Endpoint, out *upstream) (*http.Response, bool, error) {
	ahead := x.countOn(ep)
	header := time.Duration(x.policy.GetUpstreamTimeoutMs()) * time.Millisecond
	if n, ok := datalayer.Attribute[int](ep, datalayer.CollectorCapacity); ok && n > 0 && ahead >= int64(n) {
		x.t.QueuedBehind = uint32(ahead - int64(n) + 1)
		header = 0
	}
	ctx, span := x.g.tracer.Start(ctx, "upstream "+out.path, trace.WithSpanKind(trace.SpanKindClient), trace.WithAttributes(
		attribute.String("server.address", ep.Address().String()),
		attribute.String("nebu.pool", string(ep.Pool())),
	))
	defer span.End()
	first := x.served == nil
	if first {
		x.resp.StartedAt = x.g.clock.Now()
	}
	resp, written, err := x.send(ctx, ep, out, header)
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		return nil, written, err
	}
	span.SetAttributes(attribute.Int("http.response.status_code", resp.StatusCode))
	if first {
		x.resp.FirstByteAt = x.g.clock.Now()
	}
	return resp, true, nil
}

// Sends one rendered request to one endpoint over its pool's protocol, counting it nowhere, answering whether it was fully written
func (x *exchange) send(ctx context.Context, ep datalayer.Endpoint, out *upstream, header time.Duration) (*http.Response, bool, error) {
	if x.pool.AppProtocol == datalayer.AppProtocolGRPC {
		resp, err := x.sendGRPC(ctx, ep, out)
		return resp, err == nil || !dialFailed(err), err
	}
	return x.sendHTTP(ctx, ep, out, header)
}

// Whether a gRPC call failed before its connection carried the request
func dialFailed(err error) bool {
	if errors.Is(err, syscall.ECONNREFUSED) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "connection refused") || strings.Contains(msg, "Error while dialing") || strings.Contains(msg, "name resolver error")
}

// Streams a body to the transport in runs of at most bodyparse.ChunkBytes
type chunkReader struct {
	chunks [][]byte
}

func newChunkReader(body []byte) *chunkReader {
	return &chunkReader{chunks: bodyparse.SplitChunks(body, bodyparse.ChunkBytes)}
}

func (r *chunkReader) Read(p []byte) (int, error) {
	for len(r.chunks) > 0 && len(r.chunks[0]) == 0 {
		r.chunks = r.chunks[1:]
	}
	if len(r.chunks) == 0 {
		return 0, io.EOF
	}
	n := copy(p, r.chunks[0])
	r.chunks[0] = r.chunks[0][n:]
	return n, nil
}

// Sends over HTTP/1.1 or h2c in the caller's trace with the header timeout, following same host redirects, body chunked
func (x *exchange) sendHTTP(ctx context.Context, ep datalayer.Endpoint, out *upstream, header time.Duration) (*http.Response, bool, error) {
	u := "http://" + ep.Address().String() + out.path
	if out.query != "" {
		u += "?" + out.query
	}
	var written atomic.Bool
	ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{WroteRequest: func(info httptrace.WroteRequestInfo) {
		if info.Err == nil {
			written.Store(true)
		}
	}})
	var body io.Reader
	if out.body != nil {
		body = newChunkReader(out.body)
	}
	req, err := http.NewRequestWithContext(ctx, out.method, u, body)
	if err != nil {
		return nil, false, err
	}
	if out.body != nil {
		req.ContentLength = int64(len(out.body))
		req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(newChunkReader(out.body)), nil }
	}
	req.Header = out.header.Clone()
	telemetry.Inject(ctx, req.Header)
	if out.body != nil && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if out.stream {
		req.Header.Set("Accept", "text/event-stream, application/x-ndjson, application/json")
	}
	idle := int(x.policy.GetMaxInFlight())
	if idle == 0 {
		idle = idleUncapped
	}
	resp, err := (&sameHostRedirects{next: x.g.transport(x.pool.AppProtocol, header, idle), body: out.body}).RoundTrip(req)
	return resp, written.Load(), err
}

// Whether a method may be sent again after the upstream read it once
func idempotent(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodPut, http.MethodDelete:
		return true
	}
	return false
}

// Whether a failed exchange may go to another endpoint, nothing having reached the client and a non idempotent request never written
func retryable(err error, method string, written bool) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if written && !idempotent(method) {
		return false
	}
	if te, ok := errors.AsType[*transcode.Error](err); ok {
		return te.Status == http.StatusServiceUnavailable
	}
	if _, ok := errors.AsType[*net.OpError](err); ok {
		return true
	}
	return errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF)
}

// Whether a status refuses the request rather than answering it, over its limits or failing on its side
func refused(status int) bool {
	return status == http.StatusTooManyRequests || status >= http.StatusInternalServerError
}

// Follows same host redirects, trailing slash ones included, keeping backend URLs out of client answers
type sameHostRedirects struct {
	next http.RoundTripper
	body []byte
}

const maxRedirects = 3

func (t *sameHostRedirects) RoundTrip(req *http.Request) (*http.Response, error) {
	for hop := 0; ; hop++ {
		resp, err := t.next.RoundTrip(req)
		if err != nil {
			return nil, err
		}
		switch resp.StatusCode {
		case http.StatusTemporaryRedirect, http.StatusPermanentRedirect, http.StatusMovedPermanently, http.StatusFound:
		default:
			return resp, nil
		}
		location, err := resp.Location()
		if hop >= maxRedirects || err != nil || location.Host != req.URL.Host {
			return resp, nil
		}
		resp.Body.Close()
		next := req.Clone(req.Context())
		next.URL = location
		next.Body = io.NopCloser(newChunkReader(t.body))
		next.ContentLength = int64(len(t.body))
		req = next
	}
}

// Headers an upstream answer never passes to the client, those of the connection and the gateway's own
var answerOnly = map[string]bool{
	"Content-Length": true, "Connection": true, "Keep-Alive": true, "Transfer-Encoding": true, "Trailer": true, "Upgrade": true, "Proxy-Connection": true, "Te": true,
}

// Copies an upstream answer's headers to the client, the trace and request id headers left to the gateway
func copyAnswerHeaders(dst, src http.Header) {
	for name, v := range src {
		if answerOnly[name] || name == TraceHeader || name == requestcontrol.HeaderRequestID {
			continue
		}
		dst[name] = append([]string(nil), v...)
	}
}
