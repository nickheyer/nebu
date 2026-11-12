package gateway

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nickheyer/nebu/internal/chat"
	"github.com/nickheyer/nebu/internal/db"
	"github.com/nickheyer/nebu/pkg/bodyparse"
	"github.com/nickheyer/nebu/pkg/chattemplate"
	"github.com/nickheyer/nebu/pkg/datalayer"
	"github.com/nickheyer/nebu/pkg/metrics"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/requestcontrol"
	"github.com/nickheyer/nebu/pkg/scheduling/framework"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Token count paths llama.cpp and vLLM serve, proxied to the runtime as they are
const (
	tokenizePath   = "/tokenize"
	detokenizePath = "/detokenize"
)

// A fresh random request id
func newRequestID() string {
	var b [16]byte
	rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// The trace kind of a request kind, count for token paths and other for paths proxied unparsed
func traceKind(kind chat.Kind, path string) v1.TraceKind {
	switch kind {
	case chat.Chat:
		return v1.TraceKind_TRACE_KIND_CHAT
	case chat.Generate:
		return v1.TraceKind_TRACE_KIND_GENERATE
	case chat.Embed:
		return v1.TraceKind_TRACE_KIND_EMBED
	case chat.Count:
		return v1.TraceKind_TRACE_KIND_COUNT
	case chat.Image:
		return v1.TraceKind_TRACE_KIND_IMAGE
	case chat.Video:
		return v1.TraceKind_TRACE_KIND_VIDEO
	}
	if path == tokenizePath || path == detokenizePath {
		return v1.TraceKind_TRACE_KIND_COUNT
	}
	return v1.TraceKind_TRACE_KIND_OTHER
}

// A fresh trace for an exchange, its kind read from the request kind and path
func newTrace(x *exchange) *v1.Trace {
	return &v1.Trace{Id: db.NewID(), ClientApi: x.api, Path: x.r.URL.Path, Kind: traceKind(x.kind, x.r.URL.Path), StartedAt: timestamppb.New(x.start), Remote: x.r.RemoteAddr, RequestId: x.id}
}

// Starts an exchange for a request with its trace, not yet recorded
func (g *Gateway) newExchange(w http.ResponseWriter, r *http.Request) *exchange {
	api := chat.Detect(r)
	client := chat.ClientOf(api)
	now := g.clock.Now()
	id := strings.TrimSpace(r.Header.Get(requestcontrol.HeaderRequestID))
	if id == "" {
		id = newRequestID()
	}
	x := &exchange{g: g, w: &writer{ResponseWriter: w, clock: g.clock}, r: r, id: id, start: now, api: api, client: client, kind: client.Endpoint(r.URL.Path), who: identityOf(r), policy: effectivePolicy(nil, g.policy())}
	x.t = newTrace(x)
	x.sink = &traceSink{t: x.t, clock: g.clock}
	return x
}

// Answers every inference, media and runtime path in the client's protocol, refusals on headers sent before the body arrives
func (g *Gateway) serve(w http.ResponseWriter, r *http.Request) {
	_ = http.NewResponseController(w).EnableFullDuplex()
	x := g.newExchange(w, r)
	defer func() {
		if !x.detached {
			x.finish()
		}
	}()
	x.run(r.Context())
}

// Decides on the headers alone when they name the model, reading the body after, and serves the request by kind
func (x *exchange) run(ctx context.Context) {
	g := x.g
	var err error
	x.modes, err = parseModes(x.r.Header)
	x.record()
	if err != nil {
		x.fail(err)
		return
	}
	x.model = strings.TrimSpace(x.r.Header.Get(requestcontrol.HeaderModelName))
	byHeader := x.model != ""
	if !byHeader {
		if err := x.readRequest(); err != nil {
			x.fail(err)
			return
		}
		x.model = x.bodyModel()
	}
	x.t.Route = x.model
	if x.model != "" {
		if rt, ok := g.pools.RouteView(x.model); ok {
			x.route = rt
		}
	}
	x.policy = effectivePolicy(x.route.GetSpec().GetPolicy(), g.policy())
	if d := x.policy.GetRequestTimeoutMs(); d > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(d)*time.Millisecond)
		defer cancel()
	}
	ctx = withExchange(ctx, x)
	if x.forwarding() {
		if byHeader {
			if err := x.readRequest(); err != nil {
				x.fail(err)
				return
			}
		}
		x.forward(ctx, x.inbound())
		return
	}
	x.in = x.inbound()
	x.dir = g.current()
	dec, err := x.dir.Decide(ctx, x.in)
	if err != nil {
		x.hold.release()
		x.fail(err)
		return
	}
	x.dec = dec
	defer func() {
		if !x.detached {
			x.complete(ctx)
		}
	}()
	if byHeader {
		if err := x.readRequest(); err != nil {
			x.resp.Err = err
			x.fail(err)
			return
		}
		if x.doc != nil {
			if err := x.dir.AttachBody(ctx, dec, x.doc); err != nil {
				x.resp.Err = err
				x.fail(err)
				return
			}
		}
	}
	x.upstream = g.runtimes.API(dec.Pool.Runtime)
	x.t.UpstreamApi, x.t.FailedOpen, x.t.Profile = x.upstream, dec.FailedOpen, dec.Result.PrimaryProfile
	if dec.Permit != nil {
		x.t.QueueWaitMs = uint32(dec.Permit.Waited().Milliseconds())
	}
	switch x.kind {
	case chat.Image, chat.Video:
		x.media(ctx)
	case chat.Chat, chat.Generate, chat.Embed, chat.Count:
		x.chat(ctx)
	default:
		x.passthrough(ctx, nil)
	}
}

// Reads the bounded body chunk by chunk and parses what routes on it once, the JSON object or the multipart form
func (x *exchange) readRequest() error {
	body, err := readBody(x.w, x.r)
	if err != nil {
		return err
	}
	x.body = body
	x.t.RequestBytes = uint64(len(body))
	if doc, err := parseDoc(x.r.URL.Path, body); err == nil {
		x.doc = doc
	}
	if x.doc == nil && x.kind == chat.Image {
		if f, err := parseForm(x.r, body); err == nil {
			x.form = f
		}
	}
	return nil
}

// The model the request names below its headers, by the JSON model field, the query, then the form
func (x *exchange) bodyModel() string {
	if x.doc != nil {
		if m, err := x.doc.Model(); err == nil {
			return m
		}
	}
	if m := x.r.URL.Query().Get("model"); m != "" {
		return m
	}
	if x.form != nil {
		return x.form.value("model")
	}
	return ""
}

// The request as the director reads it, credentials dropped and a named objective taken out for the resolver
func (x *exchange) inbound() *requestcontrol.Inbound {
	h := x.r.Header.Clone()
	for _, k := range []string{"Authorization", "X-Api-Key", "Cookie"} {
		h.Del(k)
	}
	if v := strings.TrimSpace(h.Get(requestcontrol.HeaderObjective)); v != "" {
		if _, err := strconv.Atoi(v); err != nil {
			x.objective = v
			h.Del(requestcontrol.HeaderObjective)
		} else {
			x.priorityByHeader = true
		}
	}
	if x.model != "" && h.Get(requestcontrol.HeaderModelName) == "" && (x.doc == nil || !x.docNames(x.model)) {
		h.Set(requestcontrol.HeaderModelName, x.model)
	}
	var body bodyparse.Document
	if x.doc != nil {
		body = x.doc
	}
	return &requestcontrol.Inbound{ID: x.id, Method: x.r.Method, Path: x.r.URL.Path, Headers: h, Identity: x.who.fairness, Body: body, ReceivedAt: x.start, Route: x.routeName(), MaxInFlight: int(x.policy.GetMaxInFlight())}
}

// Whether the parsed body names a model
func (x *exchange) docNames(model string) bool {
	m, err := x.doc.Model()
	return err == nil && m == model
}

// Records the trace's start unless the request asked to leave none, which then keeps no body either
func (x *exchange) record() {
	if !x.modes.ghost {
		x.slot = x.g.traces.start(x.t, x.who.owner)
		x.traced = true
	}
	x.sink.keep = x.traced
}

// Answers a refusal in the client's protocol and records it on the trace
func (x *exchange) fail(err error) {
	f := x.g.refusal(err, x.model, x.route, x.pool.ID)
	x.refuse(f)
}

// Writes a failure in the client's protocol unless the answer already started
func (x *exchange) refuse(f chat.Failure) {
	if x.t.Error == "" {
		x.t.Error = f.Message
	}
	if x.w.wrote {
		return
	}
	x.w.Header().Set(requestcontrol.HeaderRequestID, x.requestID())
	if x.traced {
		x.w.Header().Set(TraceHeader, x.t.GetId())
	}
	x.client.Error(x.w, f)
}

// Hands the director what the serving endpoint answered, releasing the decision's permit and load
func (x *exchange) complete(ctx context.Context) {
	if x.resp.FinishedAt.IsZero() {
		x.resp.FinishedAt = x.g.clock.Now()
	}
	if x.resp.Endpoint == nil {
		x.resp.Endpoint = x.served
	}
	if x.resp.Status == 0 {
		x.resp.Status = x.w.status
	}
	x.dir.ResponseComplete(context.WithoutCancel(ctx), x.dec, &x.resp)
}

// Ends the trace, counts the request and feeds its latencies to metrics
func (x *exchange) finish() {
	g := x.g
	x.uncount()
	x.plan.close()
	g.total.Add(1)
	if x.w.status == 0 && x.w.wrote {
		x.w.status = http.StatusOK
	}
	now := g.clock.Now()
	x.t.Status, x.t.ResponseBytes, x.t.FinishedAt = uint32(x.w.status), x.w.bytes, timestamppb.New(now)
	if !x.resp.FirstByteAt.IsZero() {
		x.t.FirstByteAt = timestamppb.New(x.resp.FirstByteAt)
	}
	q := metrics.Request{Model: x.model, Pool: x.pool.ID, Priority: int(x.t.GetPriority()), FairnessID: x.t.GetFairnessId(), Status: x.w.status, Duration: now.Sub(x.start), PromptTokens: int(x.t.GetPromptTokens()), OutputTokens: int(x.t.GetCompletionTokens())}
	if x.dec != nil {
		q.Model = x.dec.TargetModel
		if x.resp.Endpoint != nil {
			q.Endpoint = x.resp.Endpoint.ID()
		}
		q.TTFT, q.TPOT = x.resp.TTFT(), x.resp.TPOT()
		if x.dec.Permit != nil {
			q.Admitted, q.QueueWait = true, x.dec.Permit.Waited()
		}
		g.metrics.ObserveObjectives(x.pool.ID, x.dec.Request.Objective, &x.resp)
		obj := x.dec.Request.Objective
		if obj.TTFT > 0 || obj.TPOT > 0 {
			x.t.ObjectiveAttained = attained(obj.TTFT, obj.TPOT, &x.resp)
		}
	}
	g.metrics.ObserveRequest(x.r.Context(), q)
	if x.traced {
		x.t.Request = capped(x.body)
		if !sameBytes(x.sent, x.body) {
			x.t.UpstreamRequest = capped(x.sent)
		}
		g.traces.finish(x.t, x.who.owner, x.slot)
	}
}

// Whether two slices are one, the same length over the same first byte, or both empty
func sameBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	return len(a) == 0 || &a[0] == &b[0]
}

// Notes the endpoint that answered and runs the director's response header hooks onto the client's headers
func (x *exchange) received(ctx context.Context, resp *http.Response, ep datalayer.Endpoint) error {
	x.served = ep
	x.resp.Endpoint, x.resp.Status = ep, resp.StatusCode
	x.resp.Headers = make(map[string]string, len(resp.Header))
	for k, v := range resp.Header {
		x.resp.Headers[k] = strings.Join(v, ", ")
	}
	return x.dir.ResponseReceived(ctx, x.dec, &x.resp, x.w.Header())
}

// The upstream protocol's codec, without native Responses or a tokenizer endpoint when the pool speaks gRPC
func (x *exchange) runtimeCodec() chat.Runtime {
	rt := chat.RuntimeOf(x.upstream)
	if x.pool.AppProtocol == datalayer.AppProtocolGRPC {
		return grpcCodec{rt}
	}
	return rt
}

// An OpenAI codec for pools reached over gRPC, whose transcoders serve chat, completions and embeddings alone
type grpcCodec struct{ chat.Runtime }

func (c grpcCodec) Traits() chat.Traits {
	t := c.Runtime.Traits()
	t.Responses, t.TokenizePath = false, ""
	return t
}

// Serves a chat, prompt, embedding or count as sent when the runtime reads it, translated otherwise
func (x *exchange) chat(ctx context.Context) {
	rt := x.runtimeCodec()
	traits := rt.Traits()
	same := x.api == x.upstream
	relay := x.prefill() != nil && (x.kind == chat.Chat || x.kind == chat.Generate)
	tokens := x.tokensMode()
	creq, err := x.chatRequest()
	native := same && !relay && !tokens && (x.r.URL.Path != chat.OpenAIResponses || traits.Responses)
	if err != nil {
		if !native {
			x.fail(err)
			return
		}
		if err := x.fitUnparsed(err); err != nil {
			x.fail(err)
			return
		}
		x.passthrough(ctx, nil)
		return
	}
	x.t.Stream = creq.Stream
	x.runtime = rt
	if tokens {
		if x.runtime, err = x.tokensRuntime(rt); err != nil {
			x.fail(err)
			return
		}
	}
	tpl := x.templateReasoning()
	if !same {
		if x.api == v1.ApiFlavor_API_FLAVOR_ANTHROPIC && x.stripsBillingHeader() {
			chat.StripBillingHeader(creq)
		}
		if err := chat.Cross(creq, x.api, tpl); err != nil {
			x.fail(err)
			return
		}
	}
	fitted, err := chat.FitEffort(creq, tpl)
	if err != nil {
		x.fail(err)
		return
	}
	if native && !x.changed && !fitted && !x.plan.active() && !x.emulate && creq.Kind != chat.Count {
		x.passthrough(ctx, creq)
		return
	}
	x.t.Translated = !same
	if traits.Strict && !same {
		chat.DropExtras(creq)
	}
	if creq.Kind == chat.Count {
		x.count(ctx, creq)
		return
	}
	if traits.InlineImages {
		if err := x.inlineImages(ctx, creq); err != nil {
			x.refuse(chat.Failure{Status: http.StatusBadRequest, Code: chat.CodeInvalidRequest, Message: err.Error()})
			return
		}
	}
	if x.emulate {
		x.runtime = chat.Emulate(x.runtime)
	}
	x.rounds(ctx, creq)
}

// Fetches image URLs into bytes over the pooled outbound guard, for runtimes that take images inline
func (x *exchange) inlineImages(ctx context.Context, req *chat.Request) error {
	for mi := range req.Messages {
		for pi, p := range req.Messages[mi].Parts {
			if p.Type != "image" || p.Data != "" || p.URL == "" {
				continue
			}
			part := &req.Messages[mi].Parts[pi]
			if mt, raw, ok := chat.DataURL(p.URL); ok {
				part.MediaType, part.Data = mt, raw
				continue
			}
			fctx, cancel := context.WithTimeout(ctx, imageFetchTimeout(x.policy))
			raw, mt, err := x.g.fetchImage(fctx, p.URL)
			cancel()
			if err != nil {
				return fmt.Errorf("image %s: %w", p.URL, err)
			}
			part.Data, part.MediaType = base64.StdEncoding.EncodeToString(raw), chat.MediaTypeOf(raw, mt)
		}
	}
	return nil
}

// The bound on one image fetch, the policy's upstream timeout or a minute
func imageFetchTimeout(p *v1.Policy) time.Duration {
	if ms := p.GetUpstreamTimeoutMs(); ms > 0 {
		return time.Duration(ms) * time.Millisecond
	}
	return time.Minute
}

// Reads one image over the shared outbound client
func (g *Gateway) fetchImage(ctx context.Context, url string) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, "", err
	}
	resp, err := g.images.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= http.StatusMultipleChoices {
		return nil, "", fmt.Errorf("answered %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	return raw, resp.Header.Get("Content-Type"), err
}

// Proxies the body as the stages left it, the target model written in, parsing the answer for the trace
func (x *exchange) passthrough(ctx context.Context, creq *chat.Request) {
	body, doc := x.body, x.document()
	if doc != nil {
		body = doc.Bytes()
	}
	x.sent = body
	stream := doc != nil && doc.Stream()
	x.t.Stream = stream
	out := &upstream{method: x.r.Method, path: x.r.URL.Path, query: x.r.URL.RawQuery, body: body, doc: doc, stream: stream, header: x.runtimeHeaders()}
	if ct := x.r.Header.Get("Content-Type"); ct != "" {
		out.header.Set("Content-Type", ct)
	}
	if len(body) == 0 && x.r.Method == http.MethodGet {
		out.body = nil
	}
	resp, ep, err := x.attempt(ctx, out, x.dec.Endpoints)
	if err != nil {
		x.refuse(upstreamFailure(x.model, err))
		x.resp.Err = err
		return
	}
	if err := x.received(ctx, resp, ep); err != nil {
		resp.Body.Close()
		x.refuse(chat.Failure{Status: http.StatusInternalServerError, Code: chat.CodeServer, Message: err.Error()})
		return
	}
	if resp.StatusCode >= http.StatusInternalServerError {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxBody))
		resp.Body.Close()
		x.sink.refused(raw)
		x.resp.Err = fmt.Errorf("upstream answered %d", resp.StatusCode)
		x.refuse(upstreamFailure(x.model, chat.Refusal(x.runtimeCodec(), resp.StatusCode, resp.Header, raw)))
		return
	}
	var rt chat.Runtime
	if creq != nil {
		rt = x.runtimeCodec()
	}
	x.relayAnswer(ctx, resp, rt, creq)
}

// Copies an answer to the client chunk by chunk through the director, flushing streams with keepalives
func (x *exchange) relayAnswer(ctx context.Context, resp *http.Response, rt chat.Runtime, creq *chat.Request) {
	defer resp.Body.Close()
	var parse *tap
	switch {
	case resp.StatusCode < http.StatusMultipleChoices && creq != nil:
		parse = newTap(x.sink, rt, creq)
	case x.traced:
		parse = newTap(x.sink, nil, nil)
	}
	beat := x.pulse(resp.Header, creq)
	held := x.holdsStatus(resp.Header)
	commit := func() {
		copyAnswerHeaders(x.w.Header(), resp.Header)
		x.w.WriteHeader(resp.StatusCode)
		held = false
	}
	if !held {
		commit()
	}
	var failure, stop string
	// First hook or upstream failure, answered as the status when held or the stream's last event, refused when a hook's
	var cause, refused error
	seq := newSequence(x.r.URL.Path == chat.OpenAIResponses)
	write := func(b []byte) bool {
		if held && len(b) == 0 {
			return true
		}
		if held {
			commit()
		}
		if err := beat.write(b); err != nil {
			stop = "cancelled"
			if failure == "" {
				x.resp.Err, failure = err, "client went away"
			}
			return false
		}
		seq.see(b)
		parse.feed(b)
		return true
	}
	open := true
	buf := make([]byte, 32<<10)
	for open {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			chunk, err := x.chunk(ctx, cloned(buf[:n]), false)
			if err != nil {
				x.resp.Err, failure, cause, refused, open = err, err.Error(), err, err, false
				break
			}
			open = write(chunk)
		}
		if rerr != nil {
			if open && !errors.Is(rerr, io.EOF) {
				x.resp.Err, failure, cause = rerr, "upstream: "+rerr.Error(), rerr
				open = !held
			}
			break
		}
	}
	// The hooks see the end and the bytes they held back reach a client the relay still serves
	tail, err := x.chunk(ctx, nil, true)
	switch {
	case err != nil && failure == "":
		x.resp.Err, failure, cause, refused = err, err.Error(), err, err
	case err == nil && open && len(tail) > 0:
		write(tail)
	}
	switch {
	case held && cause != nil:
		x.fail(cause)
	case held:
		commit()
	case cause != nil && stop == "" && x.r.Context().Err() == nil:
		if _, relayed := errors.AsType[*relayedFailure](cause); relayed {
			break
		}
		f := upstreamFailure(x.model, cause)
		if refused != nil {
			f = x.g.refusal(refused, x.model, x.route, x.pool.ID)
		}
		event, err := x.client.StreamFailure(chat.StreamAt{Path: x.r.URL.Path, Model: x.model, Seq: seq.next}, f)
		switch {
		case err != nil:
			failure += ", the stream failure event not encoded: " + err.Error()
		case beat.end(event) != nil:
			stop = "cancelled"
		}
	}
	beat.stop()
	parse.close()
	if failure != "" {
		x.t.Error = failure
	}
	if stop != "" {
		x.t.Stop = stop
	}
	x.resp.FinishedAt = x.g.clock.Now()
	x.settle(resp.StatusCode)
}

// Whether a decided answer's client status waits for what the hooks pass, unless a requested stream came back
func (x *exchange) holdsStatus(h http.Header) bool {
	if x.dec == nil {
		return false
	}
	sse, ndjson := streamed(h.Get("Content-Type"))
	return x.dec.Request == nil || !x.dec.Request.Streaming || !sse && !ndjson
}

// Hands a chunk to the director's response hooks, a forwarded request having no decision to hand it to
func (x *exchange) chunk(ctx context.Context, b []byte, end bool) ([]byte, error) {
	if x.dec == nil {
		return b, nil
	}
	return x.dir.ResponseChunk(ctx, x.dec, &x.resp, b, end)
}

// A copy of a read buffer the caller reuses
func cloned(b []byte) []byte { return append([]byte(nil), b...) }

// Moves the parsed answer's first token time and token counts onto the director's response
func (x *exchange) settle(status int) {
	if !x.sink.first.IsZero() {
		x.resp.FirstTokenAt = x.sink.first
	}
	if res := x.sink.res; res != nil {
		x.resp.PromptTokens, x.resp.OutputTokens, x.resp.CachedTokens = res.Usage.In, res.Usage.Out, res.Usage.Cached
	}
	if status >= http.StatusBadRequest && x.resp.Err == nil {
		x.resp.Err = fmt.Errorf("upstream answered %d", status)
	}
}

// Whether a content type names a stream the gateway flushes event by event
func streamed(contentType string) (bool, bool) {
	mt, _, _ := mime.ParseMediaType(contentType)
	return mt == "text/event-stream", mt == "application/x-ndjson"
}

// Whether a request asked for images
func hasImages(req *chat.Request) bool {
	for _, m := range req.Messages {
		if slices.ContainsFunc(m.Parts, func(p chat.Part) bool { return p.Type == "image" }) {
			return true
		}
	}
	return false
}

// Counts the prompt's tokens with the runtime's tokenizer, or with a one token generation when it has none
func (x *exchange) count(ctx context.Context, creq *chat.Request) {
	n, err := x.countTokens(ctx, creq)
	if err != nil {
		x.resp.Err = err
		x.refuse(upstreamFailure(x.model, err))
		return
	}
	x.t.PromptTokens = uint32(n)
	x.resp.PromptTokens = n
	out := x.client.Answer(x.w, creq, x.model)
	defer out.Close()
	if err := out.Write(chat.Event{Kind: chat.Stopped, Res: &chat.Result{Model: x.model, Usage: chat.Usage{In: n}}}); err != nil {
		x.t.Error = err.Error()
	}
}

// The prompt token count the runtime reports, by tokenizer when it counts everything sent and by generation otherwise
func (x *exchange) countTokens(ctx context.Context, creq *chat.Request) (int, error) {
	traits := x.runtime.Traits()
	if traits.TokenizePath != "" && (traits.CountsImages || !hasImages(creq)) {
		n, status, err := x.countWith(ctx, creq)
		if err == nil || status != http.StatusNotFound && status != http.StatusMethodNotAllowed && status != http.StatusNotImplemented {
			return n, err
		}
	}
	gen := *creq
	gen.Kind, gen.MaxTokens, gen.Stream, gen.N = chat.Chat, 1, false, 0
	return x.countGenerated(ctx, &gen)
}

// Asks the runtime's tokenizer, answering the count or the status it refused with
func (x *exchange) countWith(ctx context.Context, creq *chat.Request) (int, int, error) {
	path, body, err := x.runtime.Render(creq)
	if err != nil {
		return 0, 0, err
	}
	x.sent = body
	res, status, err := x.call(ctx, creq, path, body)
	if err != nil {
		return 0, status, err
	}
	return res.Usage.In, status, nil
}

// Generates one token and answers the prompt tokens the runtime reports for it
func (x *exchange) countGenerated(ctx context.Context, gen *chat.Request) (int, error) {
	path, body, err := x.runtime.Render(gen)
	if err != nil {
		return 0, err
	}
	x.sent = body
	res, _, err := x.call(ctx, gen, path, body)
	if err != nil {
		return 0, err
	}
	return res.Usage.In, nil
}

// Sends one whole request through the decision's endpoints and parses the answer
func (x *exchange) call(ctx context.Context, req *chat.Request, path string, body []byte) (*chat.Result, int, error) {
	out := &upstream{method: http.MethodPost, path: path, body: body, header: x.runtimeHeaders()}
	resp, ep, err := x.attempt(ctx, out, x.endpoints())
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	if x.served == nil {
		if err := x.received(ctx, resp, ep); err != nil {
			return nil, resp.StatusCode, err
		}
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	if resp.StatusCode >= http.StatusMultipleChoices {
		return nil, resp.StatusCode, chat.Refusal(x.runtime, resp.StatusCode, resp.Header, raw)
	}
	res, err := x.runtime.ParseResult(req, raw)
	return res, resp.StatusCode, err
}

// The endpoints a send may try, the decision's until one served and that one alone after
func (x *exchange) endpoints() []datalayer.Endpoint {
	if x.served != nil {
		return []datalayer.Endpoint{x.served}
	}
	return x.dec.Endpoints
}

// Whether a decision's latencies met both objectives it carries, a failed request missing them
func attained(ttft, tpot time.Duration, resp *framework.Response) bool {
	ok := resp.Err == nil
	if ttft > 0 {
		ok = ok && resp.TTFT() > 0 && resp.TTFT() <= ttft
	}
	if tpot > 0 {
		ok = ok && resp.TPOT() > 0 && resp.TPOT() <= tpot
	}
	return ok
}

// How the template behind the picked endpoint takes reasoning settings, nil when the endpoint names no template
func (x *exchange) templateReasoning() *v1.ChatTemplate {
	if x.dec == nil || len(x.dec.Endpoints) == 0 {
		return nil
	}
	return chattemplate.FromLabels(x.dec.Endpoints[0].Labels())
}

// Checks the reasoning_effort of a body passed through unparsed, refusing one the template rejects or that needs rewriting
func (x *exchange) fitUnparsed(parseErr error) error {
	doc := x.document()
	if doc == nil {
		return nil
	}
	raw, ok := doc.Field("reasoning_effort")
	if !ok {
		return nil
	}
	fitted, err := chat.FitEffort(&chat.Request{Kind: x.kind, Model: x.model, Extra: map[string]json.RawMessage{"reasoning_effort": raw}}, x.templateReasoning())
	if err != nil {
		return err
	}
	if fitted {
		return bad("reasoning_effort none becomes the chat template's thinking switch, which needs a body the gateway parses, %v", parseErr)
	}
	return nil
}

// Whether the scheduled instance has Claude Code's billing header dropped, true before an endpoint is picked
func (x *exchange) stripsBillingHeader() bool {
	if x.dec == nil || len(x.dec.Endpoints) == 0 {
		return true
	}
	return x.g.instances.StripsBillingHeader(x.dec.Endpoints[0].Labels()[datalayer.LabelInstance])
}
