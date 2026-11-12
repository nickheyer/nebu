// Package requestcontrol directs each request through rewrite, flow control, scheduling and response hooks
package requestcontrol

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/nickheyer/nebu/pkg/bodyparse"
	"github.com/nickheyer/nebu/pkg/datalayer"
	"github.com/nickheyer/nebu/pkg/flowcontrol"
	"github.com/nickheyer/nebu/pkg/scheduling/framework"
)

var (
	// Answered 503, no endpoint of the pool is ready
	ErrNoReadyEndpoints = errors.New("no ready endpoint")
	// Answered 429, the request was shed by policy
	ErrShed = errors.New("request shed")
	// Answered 404, no pool serves the model
	ErrUnknownModel = errors.New("unknown model")
	// Answered 400, the body carries no model and no default applies
	ErrNoModel = errors.New("no model named")
	// Answered 503 under fail close, forwarded to a plain pick under fail open
	ErrSchedulerUnavailable = errors.New("scheduler unavailable")
	// Wrapped with ErrSchedulerUnavailable under every failure mode, the pool names a scheduler config nobody hosts
	ErrUnknownSchedulerConfig = errors.New("unknown scheduler config")
	// Answered 400, a routing header holds a value that does not parse
	ErrInvalidHeader = errors.New("invalid routing header")
)

// Headers the gateway reads and writes on the way through
const (
	HeaderObjective     = "X-Gateway-Inference-Objective"
	HeaderFairnessID    = "X-Gateway-Inference-Fairness-Id"
	HeaderModelRewrite  = "X-Gateway-Model-Name-Rewrite"
	HeaderSubset        = "X-Gateway-Destination-Endpoint-Subset"
	HeaderDestination   = "X-Gateway-Destination-Endpoint"
	HeaderServed        = "X-Gateway-Destination-Endpoint-Served"
	HeaderRequestID     = "X-Request-Id"
	HeaderModelName     = "X-Gateway-Model-Name"
	HeaderTTFTObjective = "X-Gateway-Objective-Ttft-Ms"
	HeaderTPOTObjective = "X-Gateway-Objective-Tpot-Ms"
)

// A request as it arrives, headers first and the body once read
type Inbound struct {
	ID      string
	Method  string
	Path    string
	Headers http.Header
	// The caller's authenticated identity, the default fairness id
	Identity string
	Body     bodyparse.Document
	// When the gateway received the headers
	ReceivedAt time.Time
	// The route the request counts under and its in flight cap, waited for in flow control, zero uncapped
	Route       string
	MaxInFlight int
}

// Where a request goes and with what body
type Decision struct {
	Pool datalayer.Pool
	// The endpoints to try in order, the first primary
	Endpoints []datalayer.Endpoint
	// The model name the endpoint serves, after rewrite and split
	TargetModel string
	Adapter     string
	// The body to forward, the target model written in and every mutator's change applied, nil when none was read
	Body bodyparse.Document
	// Headers set on the outbound request
	Headers map[string]string
	// The permit held until the response completes
	Permit flowcontrol.Permit
	// The scheduler's view of the request, handed to response hooks
	Request *framework.Request
	Result  *framework.Result
	// Whether the scheduler failed and the pool's failure mode let the request through
	FailedOpen bool

	life lifecycle
}

// Resolves client model names to pools and target models under the rewrite rules
type Resolver interface {
	// Pool and target model for a client name, ErrUnknownModel when unserved, ErrNoModel when unnamed and no rule applies
	Resolve(ctx context.Context, clientModel string) (datalayer.Pool, string, error)
	// The pool serving a request with no model, when exactly one pool is ready
	Default(ctx context.Context) (datalayer.Pool, string, bool)
	// The pool serving a model name without rules and the adapter it selects, ErrUnknownModel when none serves it
	Lookup(ctx context.Context, model string) (datalayer.Pool, string, error)
	// What serves a model resolved in a pool, through the client model's route or the model name alone
	Scope(clientModel string, pool datalayer.PoolID, model string) Scope
}

// The endpoints a resolved request may go to, the wake they await, and the adapter its model selects
type Scope struct {
	// Whether an endpoint of the pool serves the request, every endpoint when nil
	Serves func(datalayer.Endpoint) bool
	// Whether a request woke an owner serving the scope and none of its endpoints joined since, never when nil
	Waking func() bool
	// The adapter the model selects in the pool, empty for a base model
	Adapter string
}

// Whether the scope takes an endpoint
func (s Scope) serves(ep datalayer.Endpoint) bool { return s.Serves == nil || s.Serves(ep) }

// Whether an owner serving the scope is waking
func (s Scope) waking() bool { return s.Waking != nil && s.Waking() }

// Runs before scheduling, able to change the request the scheduler sees
type PreRequestPlugin interface {
	framework.Plugin
	PreRequest(ctx context.Context, in *Inbound, req *framework.Request) error
}

// Runs after the decision, able to change the forwarded body and headers
type PostDecisionPlugin interface {
	framework.Plugin
	PostDecision(ctx context.Context, in *Inbound, dec *Decision) error
}

// Runs before scheduling and answers a mutated copy of the body the later stages read, nil leaving it as it is
type PreRequestMutator interface {
	framework.Plugin
	PreRequestBody(ctx context.Context, in *Inbound, req *framework.Request) (bodyparse.Document, error)
}

// Runs after the decision and answers a mutated copy of the body to forward, nil leaving the decision's body as it is
type PostDecisionMutator interface {
	framework.Plugin
	PostDecisionBody(ctx context.Context, in *Inbound, dec *Decision) (bodyparse.Document, error)
}

// Runs on the way back, able to change response headers before the client sees them
type ResponseHeadersPlugin interface {
	framework.Plugin
	ResponseHeaders(ctx context.Context, dec *Decision, resp *framework.Response, headers http.Header) error
}

// Runs on every response chunk, able to replace the bytes the client sees
type ResponseBodyPlugin interface {
	framework.Plugin
	ResponseBody(ctx context.Context, dec *Decision, resp *framework.Response, chunk []byte, end bool) ([]byte, error)
}

// Runs once the response finished or failed, before the permit is released
type ResponseCompletePlugin interface {
	framework.Plugin
	ResponseComplete(ctx context.Context, dec *Decision, resp *framework.Response)
}

// Orders plugins by stage with declared dependencies so independent ones run together
type Topology interface {
	// Plugins of a stage in dependency order, grouped into batches that may run concurrently
	Batches(stage Stage) [][]framework.Plugin
}

// A pipeline stage
type Stage int

const (
	StagePreRequest Stage = iota
	StagePostDecision
	StageResponseHeaders
	StageResponseBody
	StageResponseComplete
)

// Directs a request from headers to a decision and feeds the response back to every hook
type Director interface {
	// Decides where the request goes, blocking in flow control until it may dispatch
	Decide(ctx context.Context, in *Inbound) (*Decision, error)
	// Attaches a body read after a decision made on headers alone, running the body stages on it
	AttachBody(ctx context.Context, dec *Decision, body bodyparse.Document) error
	// Called with the endpoint's response headers
	ResponseReceived(ctx context.Context, dec *Decision, resp *framework.Response, headers http.Header) error
	// Called with each chunk, the returned bytes forwarded
	ResponseChunk(ctx context.Context, dec *Decision, resp *framework.Response, chunk []byte, end bool) ([]byte, error)
	// Called once the response finished or failed, releasing the permit and assumed load
	ResponseComplete(ctx context.Context, dec *Decision, resp *framework.Response)
}
