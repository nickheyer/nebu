package gateway

import (
	"cmp"
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
	"github.com/nickheyer/nebu/internal/pools"
	"github.com/nickheyer/nebu/pkg/bodyparse"
	"github.com/nickheyer/nebu/pkg/chattemplate"
	"github.com/nickheyer/nebu/pkg/datalayer"
	"github.com/nickheyer/nebu/pkg/metrics"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/requestcontrol"
	"github.com/nickheyer/nebu/pkg/scheduling/framework"
	"github.com/nickheyer/nebu/pkg/sources"
	"google.golang.org/protobuf/proto"
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

// Decides on the headers alone when they name the model, reading the body after, and serves the request by kind,
// deciding again after a refusal another decision may meet while nothing reached the client
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
	x.left, x.cooled = map[datalayer.PoolID]bool{}, map[string]bool{}
	ctx = pools.WithLeft(ctx, func(pool datalayer.PoolID) bool { return x.left[pool] })
	before := proto.Clone(x.t).(*v1.Trace)
	read := !byHeader
	for {
		again := x.pass(ctx, &read)
		if k := x.w.take(); k != nil {
			if k.status == 0 || ctx.Err() != nil {
				x.give(k)
				return
			}
			k.trace, k.pool, k.sent, k.resp = proto.Clone(x.t).(*v1.Trace), x.pool, x.sent, x.resp
			x.passed(k.fallback)
			if k.stay && !x.cooled[x.credential.GetId()] {
				x.cooled[x.credential.GetId()] = true
			} else {
				x.left[x.pool.ID] = true
			}
			x.last = k
		} else if !again {
			return
		}
		x.reset(before)
	}
}

// Records on the trace a decision the request was decided again after
func (x *exchange) passed(f *v1.TraceFallback) {
	x.t.Fallbacks = append(x.t.Fallbacks, f)
	x.g.log.Warn("decision did not take the request, deciding again", "model", x.model, "pool", f.GetPoolId(), "credential", f.GetCredential(), "endpoint", f.GetAddress(), "why", f.GetReason())
}

// Writes the last refusal as the pass that met it would have, its trace, pool and answer back in place
func (x *exchange) give(k *kept) {
	if k.trace != nil {
		proto.Reset(x.t)
		proto.Merge(x.t, k.trace)
		x.pool, x.sent, x.resp = k.pool, k.sent, k.resp
	}
	if k.status != 0 {
		x.w.give(k)
	}
}

// Puts back everything a decision set: the trace as it was before the first with the passes left behind, the body as the client sent it,
// and the pool, credential, upstream, tool plan, answer and counts of the decision
func (x *exchange) reset(before *v1.Trace) {
	fallbacks := x.t.Fallbacks
	proto.Reset(x.t)
	proto.Merge(x.t, before)
	x.t.Fallbacks, x.t.RequestBytes = fallbacks, uint64(len(x.body))
	x.sink = &traceSink{t: x.t, clock: x.g.clock, keep: x.traced}
	if x.doc != nil {
		if doc, err := parseDoc(x.r.URL.Path, x.body); err == nil {
			x.doc = doc
		}
	}
	x.plan.close()
	x.uncount()
	x.in, x.dec, x.pool, x.credential, x.served, x.resp, x.sent = nil, nil, datalayer.Pool{}, nil, nil, framework.Response{}, nil
	x.plan, x.changed, x.emulate, x.prompts, x.runtime, x.upstream, x.rejected = nil, false, false, nil, nil, v1.ApiFlavor_API_FLAVOR_UNSPECIFIED, false
}

// Decides where the request goes and serves it, keeping back a refusal another decision may meet, and answers whether a decision
// that found no ready endpoint or every credential spent left its pool to decide again with the last refusal standing
func (x *exchange) pass(ctx context.Context, read *bool) bool {
	g := x.g
	x.in = x.inbound()
	x.dir = g.current()
	dec, err := x.dir.Decide(ctx, x.in)
	if err != nil {
		x.hold.release()
		if x.pool.ID != "" && !x.left[x.pool.ID] && ctx.Err() == nil && (errors.Is(err, requestcontrol.ErrNoReadyEndpoints) || errors.As(err, new(*requestcontrol.SpentError))) {
			f := &v1.TraceFallback{Reason: err.Error(), PoolId: string(x.pool.ID)}
			if x.last != nil {
				x.left[x.pool.ID] = true
				x.passed(f)
				return true
			}
			x.w.kept = &kept{header: http.Header{}, fallback: f}
		} else if x.last != nil {
			x.give(x.last)
			return false
		}
		x.fail(err)
		return false
	}
	x.dec = dec
	defer func() {
		if !x.detached {
			x.complete(ctx)
		}
	}()
	if !*read {
		*read = true
		if err := x.readRequest(); err != nil {
			x.resp.Err = err
			x.fail(err)
			return false
		}
		if x.doc != nil {
			if err := x.dir.AttachBody(ctx, dec, x.doc); err != nil {
				x.resp.Err = err
				x.fail(err)
				return false
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
	return false
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
	if x.doc == nil && (x.kind == chat.Image || x.kind == chat.Transcription) {
		if f, err := parseForm(x.r.Header.Get("Content-Type"), body); err == nil {
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
	return &requestcontrol.Inbound{ID: x.id, Method: x.r.Method, Path: x.r.URL.Path, Headers: h, Identity: x.who.fairness, Body: body, ReceivedAt: x.start, Route: x.routeName(), MaxInFlight: int(x.policy.GetMaxInFlight()), Metered: &x.metered}
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
	// A token count makes no generation under a credential, so none of it is recorded as the credential's usage
	if x.credential != nil && x.dec != nil && x.t.GetKind() != v1.TraceKind_TRACE_KIND_COUNT {
		_, model, _ := providerModel(x.dec.Pool.Model)
		in, out, cached := x.resp.PromptTokens, x.resp.OutputTokens, x.resp.CachedTokens
		if res := x.sink.res; res != nil {
			in, out, cached = res.Usage.In, res.Usage.Out, res.Usage.Cached
		}
		g.creds.Record(x.credential, model, in, out, cached)
		// A chat or a prompt streamed to its end over the API stream_options belongs to, with no usage named
		generated := x.t.GetKind() == v1.TraceKind_TRACE_KIND_CHAT || x.t.GetKind() == v1.TraceKind_TRACE_KIND_GENERATE
		if in == 0 && out == 0 && generated && x.t.GetStream() && x.t.GetError() == "" && x.t.GetStatus() == http.StatusOK && x.r.Context().Err() == nil &&
			x.upstream == v1.ApiFlavor_API_FLAVOR_OPENAI && x.r.URL.Path != chat.OpenAIResponses && x.responsesOnly() == nil {
			x.usageMissing()
		}
	}
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

// The upstream protocol's codec: the one for the Responses API alone when the provider serves the decision's credential
// nothing else, and one without native Responses or a tokenizer endpoint when the pool speaks gRPC
func (x *exchange) runtimeCodec() chat.Runtime {
	rt := chat.RuntimeOf(x.upstream)
	if rules := x.responsesOnly(); rules != nil {
		rt = chat.ResponsesOnly(chat.ResponsesRules{Namespace: rules.Namespace, Instructions: rules.Instructions})
	}
	if x.pool.AppProtocol == datalayer.AppProtocolGRPC {
		return grpcCodec{rt}
	}
	return rt
}

// What the decision's credential holds requests to when its provider serves it over the Responses API alone, nil for a
// pool no provider serves and a credential served every path
func (x *exchange) responsesOnly() *sources.Responses {
	if x.credential == nil || x.upstream != v1.ApiFlavor_API_FLAVOR_OPENAI {
		return nil
	}
	return x.g.creds.Responses(x.credential)
}

// Whether the runtime answers a request as a stream, as the client asked or because it answers no other way
func (x *exchange) streams(req *chat.Request) bool {
	return req.Stream || x.runtime.Traits().ResponsesOnly
}

// A runtime's refusal in its own words. A provider's rejection of the credential the request went under is said as that
// and as the caller's to mend, a 403 as a caller keeping no credential gets, rather than as a fault of the upstream that
// a client would try again.
func (x *exchange) refusalOf(rt chat.Runtime, resp *http.Response, raw []byte) *chat.Failure {
	if !x.rejected || x.credential == nil {
		return chat.Refusal(rt, resp.StatusCode, resp.Header, raw)
	}
	said := cmp.Or(rt.ErrorMessage(raw), http.StatusText(http.StatusUnauthorized))
	return &chat.Failure{Status: http.StatusForbidden, Code: chat.CodePermission, Message: fmt.Sprintf("%s rejected the credential %q this request went under (%s), check it or sign in again under Profile, Providers", x.g.creds.SourceName(x.credential), x.credential.GetName(), said)}
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
	// A provider serving the Responses API alone takes no other request as it is, and of those only one asking for the
	// stream it answers with, a whole answer being gathered from that stream
	if traits.ResponsesOnly && (x.r.URL.Path != chat.OpenAIResponses || err == nil && !creq.Stream) {
		native = false
	}
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
	ct := x.r.Header.Get("Content-Type")
	if doc == nil && x.form != nil && x.form.value("model") != x.dec.TargetModel {
		body, ct = x.form.with("model", x.dec.TargetModel)
	}
	x.asked = false
	if doc != nil && x.streamOptionsRefused() {
		// What the client set of an option the provider takes no body with is left out, the stream naming its usage as the provider does
		if _, set := doc.Field("stream_options"); set {
			if without, err := withoutField(doc, "stream_options"); err == nil {
				doc = without
			}
		}
	}
	plain := doc
	if doc != nil && x.usageUnasked(creq) {
		if with, err := withUsage(doc); err == nil {
			doc, x.asked = with, true
		}
	}
	if doc != nil {
		body = doc.Bytes()
	}
	x.sent = body
	stream := doc != nil && doc.Stream()
	x.t.Stream = stream
	out := &upstream{method: x.r.Method, path: x.r.URL.Path, query: x.r.URL.RawQuery, body: body, doc: doc, stream: stream, header: x.runtimeHeaders()}
	if ct != "" {
		out.header.Set("Content-Type", ct)
	}
	if len(body) == 0 && x.r.Method == http.MethodGet {
		out.body = nil
	}
	resp, ep, err := x.attempt(ctx, out, x.dec.Endpoints)
	if err == nil && x.asked && x.streamOptionsDoubted(resp.StatusCode) {
		// The request goes once more as the client wrote it
		io.Copy(io.Discard, io.LimitReader(resp.Body, maxBody))
		resp.Body.Close()
		x.fellBack(ep, resp.Status+" with stream_options added", true)
		x.asked, out.body, out.doc = false, plain.Bytes(), plain
		x.sent = out.body
		if resp, ep, err = x.attempt(ctx, out, x.dec.Endpoints); err == nil && resp.StatusCode < http.StatusBadRequest {
			x.streamOptionsRefusedBy()
		}
	}
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
		x.refuse(upstreamFailure(x.model, x.refusalOf(x.runtimeCodec(), resp, raw)))
		return
	}
	var rt chat.Runtime
	if creq != nil {
		rt = x.runtimeCodec()
	}
	x.relayAnswer(ctx, resp, rt, creq)
}

// What a provider source's answers taught of how it takes stream_options
type streamOptions uint8

const (
	// Nothing yet, what its catalog says standing
	optionsUnknown streamOptions = iota
	// It answered a body naming the option
	optionsTaken
	// It refused a body naming the option and answered the same one without it
	optionsRefused
	// Its catalog does not say it names a stream's usage only when asked, yet a stream of its named none, so it is asked
	optionsAsked
)

// What is kept across restarts of each
var optionsKept = map[streamOptions]string{optionsRefused: "refused", optionsAsked: "asked"}

// How long a refusal of stream_options learned from a source's answers stands across restarts before the source is asked
// once more, a server that came to take the option found out by then. That a source is asked stands for good, asking one
// that came to name usage unasked costing nothing.
const optionsKeep = 7 * 24 * time.Hour

// What is known of how the credential's source takes stream_options and the key it is kept by, with what its catalog
// says: whether a stream names its usage only when asked, and whether a body naming the option is refused
func (x *exchange) streamOptions() (state streamOptions, key string, asks, refuses bool) {
	asks, refuses, server := x.g.creds.StreamOptions(x.credential.GetSourceId())
	key = x.credential.GetSourceId() + "\x00" + server
	if known, ok := x.g.streamOpts.Load(key); ok {
		return known.(streamOptions), key, asks, refuses
	}
	// What was learned before a restart stands a while, sparing a strict server the request it would refuse again and a
	// provider naming usage only when asked the stream that would go unrecorded
	if data, kept := x.g.learned.Get(optionsCacheKey(key), 0); kept {
		switch string(data) {
		case optionsKept[optionsAsked]:
			state = optionsAsked
		case optionsKept[optionsRefused]:
			if _, fresh := x.g.learned.Get(optionsCacheKey(key), optionsKeep); fresh {
				state = optionsRefused
			}
		}
	}
	known, _ := x.g.streamOpts.LoadOrStore(key, state)
	return known.(streamOptions), key, asks, refuses
}

func optionsCacheKey(key string) string { return "gateway/stream-options/" + key }

// Sets what a source's answers taught of stream_options, kept across restarts where that spares the source a request
func (x *exchange) learn(key string, state streamOptions) {
	x.g.streamOpts.Store(key, state)
	if name, kept := optionsKept[state]; kept {
		if err := x.g.learned.Put(optionsCacheKey(key), []byte(name)); err != nil {
			x.g.log.Warn("what a provider's answers taught of stream_options not kept past a restart", "source", x.credential.GetSourceId(), "err", err)
		}
	}
}

// Whether a stream goes to a provider that names its usage only when asked while its client did not ask, which
// would leave nothing to record against the caller's credential
func (x *exchange) usageUnasked(creq *chat.Request) bool {
	if x.credential == nil || creq == nil || !creq.Stream || creq.IncludeUsage || x.upstream != v1.ApiFlavor_API_FLAVOR_OPENAI || x.r.URL.Path == chat.OpenAIResponses {
		return false
	}
	if x.streamOptionsRefused() {
		return false
	}
	// A provider not said to name usage only when asked is asked like the rest once a stream of its named none
	state, _, asks, _ := x.streamOptions()
	return asks || state == optionsAsked || state == optionsTaken
}

// Whether the decision's provider takes no body naming stream_options: one that refused it and then answered the same
// request without it, or one whose catalog says so and whose streams have named their usage unasked as it says
func (x *exchange) streamOptionsRefused() bool {
	if x.credential == nil {
		return false
	}
	state, _, _, refuses := x.streamOptions()
	switch state {
	case optionsRefused:
		return true
	case optionsTaken, optionsAsked:
		return false
	}
	return refuses
}

// Notes that a provider answered a body naming stream_options, and answers whether one it called malformed should go once
// more without the option: a server holding to a stricter schema than OpenAI's refuses it, which is found out once a source
func (x *exchange) streamOptionsDoubted(status int) bool {
	if x.credential == nil {
		return false
	}
	state, key, _, _ := x.streamOptions()
	open := state == optionsUnknown || state == optionsAsked
	if status < http.StatusBadRequest {
		// For the run alone: what is kept of a provider asked since a stream of its named no usage stays that it is asked
		if open {
			x.g.streamOpts.Store(key, optionsTaken)
		}
		return false
	}
	return open && (status == http.StatusBadRequest || status == http.StatusUnprocessableEntity)
}

// Remembers that the decision's provider answered once stream_options was left out, so no body for it names the option
// again, across restarts as well
func (x *exchange) streamOptionsRefusedBy() {
	if state, key, _, _ := x.streamOptions(); state != optionsRefused {
		x.learn(key, optionsRefused)
		x.g.log.Warn("provider refuses stream_options, left out of what it is sent from now on", "source", x.credential.GetSourceId())
	}
}

// Notes a stream that ended well under a credential and named no usage, which leaves nothing to count against the
// credential's budgets. A provider that was not asked for it, its catalog not saying it must be or saying it names usage
// unasked, is asked through stream_options from then on, and one that was asked or refuses to be is said once, nothing
// more being there to ask.
func (x *exchange) usageMissing() {
	state, key, asks, _ := x.streamOptions()
	if state == optionsUnknown && (!asks || x.streamOptionsRefused()) {
		x.learn(key, optionsAsked)
		x.g.log.Warn("provider named no usage for a stream it was not asked for it on, asked through stream_options from now on", "source", x.credential.GetSourceId())
		return
	}
	if _, said := x.g.usageWarned.LoadOrStore(key, true); !said {
		x.g.log.Warn("provider names no usage for its streams, which count toward no token budget of the credentials they go under", "source", x.credential.GetSourceId(), "stream_options_refused", state == optionsRefused)
	}
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
	// The usage the gateway asked for on the client's behalf is read here and goes no further
	var drop *usageDrop
	if sse, _ := streamed(resp.Header.Get("Content-Type")); sse && x.asked && resp.StatusCode < http.StatusMultipleChoices {
		drop = &usageDrop{}
	}
	send := func(b []byte) bool {
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
		return true
	}
	write := func(b []byte) bool {
		out := b
		if drop != nil {
			out = drop.pass(b)
		}
		if (drop == nil || len(out) > 0) && !send(out) {
			return false
		}
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
	if drop != nil && open && len(drop.held) > 0 {
		send(drop.held)
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

// Counts the prompt's tokens with the runtime's tokenizer, or with a one token generation when it has none, a provider's
// by estimate in its place
func (x *exchange) count(ctx context.Context, creq *chat.Request) {
	n, generated, err := x.countTokens(ctx, creq)
	if err != nil {
		x.resp.Err = err
		x.refuse(upstreamFailure(x.model, err))
		return
	}
	x.t.PromptTokens = uint32(n)
	// A tokenizer reads the prompt for nothing a quota or a meter counts, a generation has the model read it
	if generated {
		x.resp.PromptTokens = n
	}
	out := x.client.Answer(x.w, creq, x.model)
	defer out.Close()
	if err := out.Write(chat.Event{Kind: chat.Stopped, Res: &chat.Result{Model: x.model, Usage: chat.Usage{In: n}}}); err != nil {
		x.t.Error = err.Error()
	}
}

// The prompt token count and whether a generation made it: by the runtime's tokenizer when it counts everything sent and by
// a generation otherwise. A provider would bill that generation under the caller's credential, so a count its tokenizer
// does not answer is the gateway's own estimate there.
func (x *exchange) countTokens(ctx context.Context, creq *chat.Request) (int, bool, error) {
	traits := x.runtime.Traits()
	// A provider is reached through a relay forwarding its version path alone, so a tokenizer outside that path is not asked for
	there := x.credential == nil || strings.HasPrefix(traits.TokenizePath, "/v1/")
	if traits.TokenizePath != "" && there && (traits.CountsImages || !hasImages(creq)) {
		n, status, err := x.countWith(ctx, creq)
		if err == nil || status != http.StatusNotFound && status != http.StatusMethodNotAllowed && status != http.StatusNotImplemented {
			return n, false, err
		}
	}
	if x.credential != nil {
		// Estimated here and said so in the answer's headers, at no cost to the caller's credential
		x.w.Header().Set(CountHeader, "estimate")
		return chat.EstimateTokens(creq), false, nil
	}
	gen := *creq
	gen.Kind, gen.MaxTokens, gen.Stream, gen.N = chat.Chat, 1, false, 0
	n, err := x.countGenerated(ctx, &gen)
	return n, true, err
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
		return nil, resp.StatusCode, x.refusalOf(x.runtime, resp, raw)
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
