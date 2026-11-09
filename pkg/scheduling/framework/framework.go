// Package framework defines the pluggable scheduling cycle of filters, scorers, pickers and profiles
package framework

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/nickheyer/nebu/pkg/datalayer"
)

var (
	// Returned when filters leave no endpoint
	ErrNoEndpoints = errors.New("no endpoint is eligible")
	// Returned when a profile has no picker
	ErrNoPicker = errors.New("a profile needs exactly one picker")
	// Returned when the profile handler selects no profile on the first pass
	ErrNoProfile = errors.New("the profile handler picked no profile")
	// Returned when an out of process scheduler cannot answer
	ErrSchedulerUnavailable = errors.New("the scheduler is unavailable")
	// Returned when every endpoint could serve the request but none has room for it right now
	ErrSaturated = errors.New("every endpoint is saturated for the request")
)

// The cycle state key a filter writes the reason it kept nothing under
const refusalKey = "framework.refusal"

// Records why a filter keeps no endpoint, RunProfile answering that reason in place of ErrNoEndpoints
func Refuse(state *CycleState, err error) {
	if state != nil && err != nil {
		state.Write(refusalKey, err)
	}
}

// Reads and clears the refusal a filter recorded, nil when none
func takeRefusal(state *CycleState) error {
	if state == nil {
		return nil
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	err, _ := state.data[refusalKey].(error)
	delete(state.data, refusalKey)
	return err
}

// Identifies a plugin by its type and its configured instance name
type TypedName struct {
	Type string
	Name string
}

func (t TypedName) String() string {
	if t.Name == "" || t.Name == t.Type {
		return t.Type
	}
	return t.Type + "/" + t.Name
}

// The parent of every plugin
type Plugin interface {
	TypedName() TypedName
}

// A latency target the request carries
type Objective struct {
	// Priority band, higher sheds later, zero is the default band
	Priority int
	TTFT     time.Duration
	TPOT     time.Duration
}

// What the scheduler knows about one request
type Request struct {
	ID string
	// The model after rewrite and split, the name the endpoint serves
	TargetModel string
	// The adapter the request names, empty for the base model
	Adapter string
	// The prompt text every message concatenated in order, for prefix hashing
	Prompt string
	// Token ids when the client sent tokens
	PromptTokenIDs []int
	PromptTokens   int
	// The output length the request bounds or the predictor expects
	ExpectedOutputTokens int
	Headers              map[string]string
	Objective            Objective
	FairnessID           string
	// Addresses the caller restricts the pick to, every endpoint when empty
	Subset []datalayer.Address
	// Whether the caller set a subset at all, an empty set then meaning no endpoint
	SubsetSet bool
	Streaming bool
	// Whether the pool lets the request through when its scheduler fails, set by the director
	FailOpen bool
}

// State shared by the plugins of one scheduling cycle
type CycleState struct {
	mu   sync.RWMutex
	data map[string]any
}

func NewCycleState() *CycleState { return &CycleState{data: map[string]any{}} }

func (c *CycleState) Write(key string, v any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.data[key] = v
}

func (c *CycleState) Read(key string) (any, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	v, ok := c.data[key]
	return v, ok
}

// Reads a typed cycle value, the zero value and false when absent or of another type
func ReadState[T any](c *CycleState, key string) (T, bool) {
	var zero T
	v, ok := c.Read(key)
	if !ok {
		return zero, false
	}
	t, ok := v.(T)
	if !ok {
		return zero, false
	}
	return t, true
}

// An endpoint with the score a profile gave it
type ScoredEndpoint struct {
	Endpoint datalayer.Endpoint
	Score    float64
}

// Removes endpoints unfit for the request
type Filter interface {
	Plugin
	Filter(ctx context.Context, state *CycleState, req *Request, endpoints []datalayer.Endpoint) []datalayer.Endpoint
}

// Scores endpoints in the range zero to one
type Scorer interface {
	Plugin
	Score(ctx context.Context, state *CycleState, req *Request, endpoints []datalayer.Endpoint) map[datalayer.EndpointID]float64
}

// A scorer and the weight its profile gives it
type WeightedScorer struct {
	Scorer
	Weight float64
}

// Chooses one or more endpoints from the scored set, the first the primary and the rest fallbacks
type Picker interface {
	Plugin
	Pick(ctx context.Context, state *CycleState, scored []ScoredEndpoint) *ProfileRunResult
}

// Claims room on an endpoint inside the pick, so the pick and the count are one step
type Reserver interface {
	Plugin
	// Whether the endpoint had room for the request, counting the request on it when it did
	Reserve(ctx context.Context, state *CycleState, req *Request, ep datalayer.Endpoint) bool
	// Gives back a reservation of a profile the handler did not route to
	Release(ctx context.Context, state *CycleState, req *Request, ep datalayer.Endpoint)
}

// Receives what every filter dropped and every scorer answered in a profile run
type Observer interface {
	FilterDropped(pool datalayer.PoolID, profile, filter string, dropped int)
	ObserveScore(pool datalayer.PoolID, profile, scorer string, score float64)
}

// The endpoints one filter kept
type FilterOutcome struct {
	Filter string
	Kept   []datalayer.EndpointID
}

// The endpoints one profile run chose, in order of preference
type ProfileRunResult struct {
	Endpoints []datalayer.Endpoint
	Scores    map[datalayer.EndpointID]float64
	// What each filter kept, in filter order
	Filtered []FilterOutcome
	// The raw score every scorer gave each endpoint, by scorer name
	Scored map[string]map[datalayer.EndpointID]float64
}

// A named set of filters, weighted scorers and one picker, reservers claiming the picked head
type Profile struct {
	Name      string
	Filters   []Filter
	Scorers   []WeightedScorer
	Picker    Picker
	Reservers []Reserver
	Observer  Observer
}

// Chooses which profiles run and folds their results into one answer
type ProfileHandler interface {
	Plugin
	// The profiles to run next, none when the cycle is complete
	Pick(ctx context.Context, state *CycleState, req *Request, profiles map[string]*Profile, results map[string]*ProfileRunResult) map[string]*Profile
	// The scheduling result built from every profile run
	ProcessResults(ctx context.Context, state *CycleState, req *Request, results map[string]*ProfileRunResult) (*Result, error)
}

// A profile handler that lets a named profile fail without failing the whole cycle
type ProfileFailureHandler interface {
	ProfileHandler
	// Whether the cycle goes on after the named profile failed, the failed profile then absent from results
	ProfileFailed(ctx context.Context, state *CycleState, req *Request, profile string, err error) bool
}

// What the scheduler answers for one request
type Result struct {
	ProfileResults map[string]*ProfileRunResult
	// The profile whose endpoints the request is sent to
	PrimaryProfile string
	// The cycle state the profiles ran with, handed to lifecycle plugins
	State *CycleState
	// Whether a fallback answered because the scheduler failed and the request may fail open
	FailedOpen bool
}

// The primary profile's endpoints, nil when none
func (r *Result) Endpoints() []datalayer.Endpoint {
	if r == nil {
		return nil
	}
	if p, ok := r.ProfileResults[r.PrimaryProfile]; ok {
		return p.Endpoints
	}
	return nil
}

// The endpoints of a named profile, nil when it did not run
func (r *Result) Profile(name string) []datalayer.Endpoint {
	if r == nil {
		return nil
	}
	if p, ok := r.ProfileResults[name]; ok {
		return p.Endpoints
	}
	return nil
}

// Runs profiles through their filters, scorers and picker for one request
type Scheduler interface {
	Schedule(ctx context.Context, req *Request, candidates []datalayer.Endpoint) (*Result, error)
	Profiles() map[string]*Profile
}

// Runs after a pick with the result, to count assumed load or index the prompt
type PreRequestPlugin interface {
	Plugin
	PreRequest(ctx context.Context, req *Request, result *Result)
}

// Runs when the endpoint answers headers
type ResponseReceivedPlugin interface {
	Plugin
	ResponseReceived(ctx context.Context, req *Request, resp *Response)
}

// Runs on every streamed chunk
type ResponseStreamingPlugin interface {
	Plugin
	ResponseStreaming(ctx context.Context, req *Request, resp *Response, chunk []byte)
}

// Runs once the endpoint finishes answering, with the measured latencies
type ResponseCompletePlugin interface {
	Plugin
	ResponseComplete(ctx context.Context, req *Request, resp *Response)
}

// What an endpoint answered
type Response struct {
	Endpoint     datalayer.Endpoint
	Status       int
	Headers      map[string]string
	StartedAt    time.Time
	FirstByteAt  time.Time
	FirstTokenAt time.Time
	FinishedAt   time.Time
	PromptTokens int
	OutputTokens int
	CachedTokens int
	Err          error
}

// Time to first token, zero when none arrived
func (r *Response) TTFT() time.Duration {
	if r.FirstTokenAt.IsZero() {
		return 0
	}
	return r.FirstTokenAt.Sub(r.StartedAt)
}

// Time per output token after the first, zero when fewer than two
func (r *Response) TPOT() time.Duration {
	if r.OutputTokens < 2 || r.FirstTokenAt.IsZero() || r.FinishedAt.IsZero() {
		return 0
	}
	return r.FinishedAt.Sub(r.FirstTokenAt) / time.Duration(r.OutputTokens-1)
}
