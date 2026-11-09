package gateway

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/nickheyer/nebu/internal/chat"
	"github.com/nickheyer/nebu/pkg/bodyparse"
	"github.com/nickheyer/nebu/pkg/datalayer"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/requestcontrol"
	"github.com/nickheyer/nebu/pkg/scheduling/framework"
	"github.com/nickheyer/nebu/pkg/scheduling/plugins"
)

// Who sent a request, the subject its traces and jobs are kept under and the default fairness id
type identity struct {
	owner    string
	fairness string
	// Whether the caller sees every account's traces and jobs
	admin bool
	// Whether a mesh member forwarded the request under its session
	peer bool
}

// One request's way through the gateway, shared by the handler and the plugins it reaches through the context
type exchange struct {
	g     *Gateway
	w     *writer
	r     *http.Request
	id    string
	start time.Time
	// The client protocol, its codec and the kind of request the path carries
	api    v1.ApiFlavor
	client chat.Client
	kind   chat.Kind
	body   []byte
	// The body as parsed on arrival, nil for one that is not a JSON object or was not read yet
	doc bodyparse.Document
	// The multipart form of an image edit, parsed once on arrival
	form *form
	// What the director was handed, its body following every stage's mutation
	in *requestcontrol.Inbound
	// The client's model name and the route of that name, nil when no route has it
	model string
	route *v1.Route
	who   identity
	modes modes
	// The objective the request names by header, empty when it names a priority or none
	objective string
	// Whether the objective header named an integer priority, which keeps the route's objective out
	priorityByHeader bool
	// The route policy with the gateway defaults filled in
	policy *v1.Policy
	// The node the request entered the mesh at when another gateway forwarded it, and the hops to forward it with
	entry string
	hops  int

	// What the request stage set, the tools the gateway runs and whether the turns changed
	plan    *toolPlan
	changed bool
	// The route policy slot the request holds, released once
	hold *hold

	// The ring slot of a recorded trace and whether it was recorded
	slot   uint64
	traced bool
	// Whether a background job ends the request rather than its handler
	detached bool
	pool     datalayer.Pool
	// The prompt encoder of a tokens in tokens out request, the last encoded prompt's ids kept for the send
	prompts *promptMemo
	// The last body sent upstream, kept for the trace of a recorded request
	sent []byte
	dir  requestcontrol.Director
	dec  *requestcontrol.Decision
	t    *v1.Trace
	sink *traceSink
	// The upstream protocol and its codec, emulating tools when the template cannot
	upstream v1.ApiFlavor
	runtime  chat.Runtime
	emulate  bool
	// What the serving endpoint answered, fed to the director once the request ends
	resp   framework.Response
	served datalayer.Endpoint
	// The instance count the request is held under, moving with each attempt
	counted *atomic.Int64
}

type exchangeKey struct{}

func withExchange(ctx context.Context, x *exchange) context.Context {
	return context.WithValue(ctx, exchangeKey{}, x)
}

// The exchange a context carries, nil for calls made outside a gateway request
func exchangeOf(ctx context.Context) *exchange {
	x, _ := ctx.Value(exchangeKey{}).(*exchange)
	return x
}

// The view key the chat request of a body is shared under
const chatView bodyparse.ViewKey = "chat"

// The body every stage reads now, the decision's once made, the inbound's while deciding, the arrival parse before
func (x *exchange) document() bodyparse.Document {
	if x.dec != nil && x.dec.Body != nil {
		return x.dec.Body
	}
	if x.in != nil && x.in.Body != nil {
		return x.in.Body
	}
	return x.doc
}

// The chat request of the body the stages read now, parsed once per document and shared with every reader
func (x *exchange) chatRequest() (*chat.Request, error) {
	if x.kind == chat.None {
		return nil, bad("%s is not an endpoint the gateway translates", x.r.URL.Path)
	}
	doc := x.document()
	if doc == nil {
		return x.client.Parse(x.r.URL.Path, x.body)
	}
	return chatOf(doc, x.client, x.r.URL.Path)
}

// The chat request a document holds under the chat view, parsed from its bytes on the first read
func chatOf(doc bodyparse.Document, client chat.Client, path string) (*chat.Request, error) {
	v, err := doc.View(chatView, func(d bodyparse.Document) (any, error) { return client.Parse(path, d.Bytes()) })
	if err != nil {
		return nil, err
	}
	return v.(*chat.Request), nil
}

// Records the pool the director resolved for the request
func (x *exchange) resolved(p datalayer.Pool) {
	x.pool = p
	if x.t != nil {
		x.t.PoolId = string(p.ID)
	}
}

// Notes the pool the director resolves on the exchange behind the context, loading a stored adapter a request names before giving up on it
type resolver struct {
	next requestcontrol.Resolver
	g    *Gateway
}

// Records a resolved pool unless an earlier call of the same decision already did
func note(ctx context.Context, p datalayer.Pool) {
	if x := exchangeOf(ctx); x != nil && x.pool.ID == "" {
		x.resolved(p)
	}
}

func (r *resolver) Resolve(ctx context.Context, clientModel string) (datalayer.Pool, string, error) {
	p, m, err := r.next.Resolve(ctx, clientModel)
	if errors.Is(err, requestcontrol.ErrUnknownModel) && r.g.loadAdapter(ctx, clientModel) == nil {
		p, m, err = r.next.Resolve(ctx, clientModel)
	}
	if err == nil {
		note(ctx, p)
	}
	return p, m, err
}

func (r *resolver) Default(ctx context.Context) (datalayer.Pool, string, bool) {
	p, m, ok := r.next.Default(ctx)
	if ok {
		note(ctx, p)
	}
	return p, m, ok
}

func (r *resolver) Lookup(ctx context.Context, model string) (datalayer.Pool, string, error) {
	p, a, err := r.next.Lookup(ctx, model)
	if errors.Is(err, requestcontrol.ErrUnknownModel) && r.g.loadAdapter(ctx, model) == nil {
		p, a, err = r.next.Lookup(ctx, model)
	}
	if err == nil {
		note(ctx, p)
	}
	return p, a, err
}

// Feeds pick latency and fail open fallbacks into metrics and every outcome into its target's counters
type observer struct{ g *Gateway }

func (o observer) Decided(_ *requestcontrol.Inbound, dec *requestcontrol.Decision, took time.Duration) {
	if dec.Permit != nil {
		took -= dec.Permit.Waited()
	}
	o.g.metrics.ObservePick(dec.Pool.ID, dec.Result.PrimaryProfile, took)
	if fractions, ok := framework.ReadState[map[datalayer.EndpointID]float64](dec.Result.State, plugins.PrefixFractionsKey); ok && len(dec.Endpoints) > 0 {
		o.g.metrics.ObservePrefixHitRatio(dec.Pool.ID, fractions[dec.Endpoints[0].ID()])
	}
	if dec.FailedOpen {
		o.g.metrics.SchedulerFallback(dec.Pool.ID, requestcontrol.FailOpenProfile)
	}
}

func (o observer) Refused(in *requestcontrol.Inbound, err error, took time.Duration) {
	o.g.log.Debug("request refused before dispatch", "request", in.ID, "path", in.Path, "took", took, "err", err)
}

func (o observer) Completed(dec *requestcontrol.Decision, resp *framework.Response) {
	o.g.tally(dec, resp)
}

// Records the response status and size while passing flushes through
type writer struct {
	http.ResponseWriter
	status int
	bytes  uint64
	wrote  bool
	clock  Clock
}

func (w *writer) WriteHeader(status int) {
	if !w.wrote {
		w.wrote, w.status = true, status
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *writer) Write(b []byte) (int, error) {
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += uint64(n)
	return n, err
}

func (w *writer) Flush() {
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}
	_ = http.NewResponseController(w.ResponseWriter).Flush()
}

func (w *writer) Unwrap() http.ResponseWriter { return w.ResponseWriter }
