// Package remote runs scheduling in another process over JSON on HTTP, the in-process scheduler answering requests that fail open
package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/nickheyer/nebu/pkg/datalayer"
	"github.com/nickheyer/nebu/pkg/scheduling/framework"
	"github.com/nickheyer/nebu/pkg/telemetry"
)

// The path the server answers schedule calls on with POST
const SchedulePath = "/schedule"

// The largest request or response body either side reads
const maxBody = 64 << 20

// The request objective on the wire, durations in nanoseconds
type Objective struct {
	Priority  int   `json:"priority,omitempty"`
	TTFTNanos int64 `json:"ttftNanos,omitempty"`
	TPOTNanos int64 `json:"tpotNanos,omitempty"`
}

// The framework request on the wire, subset addresses as host colon port
type Request struct {
	ID                   string            `json:"id"`
	TargetModel          string            `json:"targetModel,omitempty"`
	Adapter              string            `json:"adapter,omitempty"`
	Prompt               string            `json:"prompt,omitempty"`
	PromptTokenIDs       []int             `json:"promptTokenIds,omitempty"`
	PromptTokens         int               `json:"promptTokens,omitempty"`
	ExpectedOutputTokens int               `json:"expectedOutputTokens,omitempty"`
	Headers              map[string]string `json:"headers,omitempty"`
	Objective            Objective         `json:"objective"`
	FairnessID           string            `json:"fairnessId,omitempty"`
	Subset               []string          `json:"subset,omitempty"`
	SubsetSet            bool              `json:"subsetSet,omitempty"`
	Streaming            bool              `json:"streaming,omitempty"`
}

// What the client posts, the candidates as endpoint ids
type ScheduleRequest struct {
	Request    Request  `json:"request"`
	Candidates []string `json:"candidates"`
}

// One profile's endpoint ids in order of preference and the score each got
type ProfileResult struct {
	Endpoints []string           `json:"endpoints"`
	Scores    map[string]float64 `json:"scores,omitempty"`
}

// What the server answers on success
type ScheduleResponse struct {
	PrimaryProfile string                   `json:"primaryProfile"`
	Profiles       map[string]ProfileResult `json:"profiles"`
}

// What the server answers on failure
type ErrorResponse struct {
	Error string `json:"error"`
}

// Converts a framework request to its wire form
func ToWire(r *framework.Request) Request {
	w := Request{
		ID:                   r.ID,
		TargetModel:          r.TargetModel,
		Adapter:              r.Adapter,
		Prompt:               r.Prompt,
		PromptTokenIDs:       r.PromptTokenIDs,
		PromptTokens:         r.PromptTokens,
		ExpectedOutputTokens: r.ExpectedOutputTokens,
		Headers:              r.Headers,
		Objective:            Objective{Priority: r.Objective.Priority, TTFTNanos: int64(r.Objective.TTFT), TPOTNanos: int64(r.Objective.TPOT)},
		FairnessID:           r.FairnessID,
		SubsetSet:            r.SubsetSet,
		Streaming:            r.Streaming,
	}
	for _, a := range r.Subset {
		w.Subset = append(w.Subset, a.String())
	}
	return w
}

// Converts a wire request back to the framework form, refusing a malformed subset address
func FromWire(w Request) (*framework.Request, error) {
	r := &framework.Request{
		ID:                   w.ID,
		TargetModel:          w.TargetModel,
		Adapter:              w.Adapter,
		Prompt:               w.Prompt,
		PromptTokenIDs:       w.PromptTokenIDs,
		PromptTokens:         w.PromptTokens,
		ExpectedOutputTokens: w.ExpectedOutputTokens,
		Headers:              w.Headers,
		Objective:            framework.Objective{Priority: w.Objective.Priority, TTFT: time.Duration(w.Objective.TTFTNanos), TPOT: time.Duration(w.Objective.TPOTNanos)},
		FairnessID:           w.FairnessID,
		SubsetSet:            w.SubsetSet,
		Streaming:            w.Streaming,
	}
	for _, s := range w.Subset {
		a, err := datalayer.ParseAddress(s)
		if err != nil {
			return nil, fmt.Errorf("subset address %q: %w", s, err)
		}
		r.Subset = append(r.Subset, a)
	}
	return r, nil
}

// A scheduler that asks a remote server, answering ErrSchedulerUnavailable on any failure
type Client struct {
	url     string
	http    *http.Client
	timeout time.Duration
}

// A client of the server at baseURL, each call bounded by timeout and by the caller's deadline
func NewClient(baseURL string, hc *http.Client, timeout time.Duration) (*Client, error) {
	u, err := url.Parse(baseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("remote scheduler url %q must be an absolute http or https url", baseURL)
	}
	if timeout <= 0 {
		return nil, fmt.Errorf("remote scheduler timeout must be positive, got %s", timeout)
	}
	if hc == nil {
		hc = http.DefaultClient
	}
	return &Client{url: strings.TrimRight(baseURL, "/") + SchedulePath, http: hc, timeout: timeout}, nil
}

func (c *Client) Schedule(ctx context.Context, req *framework.Request, candidates []datalayer.Endpoint) (*framework.Result, error) {
	res, err := c.schedule(ctx, req, candidates)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", framework.ErrSchedulerUnavailable, err)
	}
	return res, nil
}

// No profile runs in this process, the remote holds them all
func (c *Client) Profiles() map[string]*framework.Profile { return map[string]*framework.Profile{} }

func (c *Client) schedule(ctx context.Context, req *framework.Request, candidates []datalayer.Endpoint) (*framework.Result, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	byID := make(map[string]datalayer.Endpoint, len(candidates))
	ids := make([]string, len(candidates))
	for i, ep := range candidates {
		ids[i] = candidateID(ep)
		byID[ids[i]] = ep
	}
	body, err := json.Marshal(ScheduleRequest{Request: ToWire(req), Candidates: ids})
	if err != nil {
		return nil, err
	}
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	hreq.Header.Set("Content-Type", "application/json")
	telemetry.Inject(ctx, hreq.Header)
	resp, err := c.http.Do(hreq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	dec := json.NewDecoder(io.LimitReader(resp.Body, maxBody))
	if resp.StatusCode != http.StatusOK {
		var e ErrorResponse
		if dec.Decode(&e) != nil || e.Error == "" {
			e.Error = resp.Status
		}
		return nil, fmt.Errorf("remote answered %d: %s", resp.StatusCode, e.Error)
	}
	var sr ScheduleResponse
	if err := dec.Decode(&sr); err != nil {
		return nil, fmt.Errorf("decoding remote answer: %w", err)
	}
	return resolve(sr, byID)
}

// Maps the remote's endpoint ids back onto the candidates, refusing any id that was not a candidate
func resolve(sr ScheduleResponse, byID map[string]datalayer.Endpoint) (*framework.Result, error) {
	if _, ok := sr.Profiles[sr.PrimaryProfile]; !ok {
		return nil, fmt.Errorf("remote primary profile %q has no result", sr.PrimaryProfile)
	}
	out := &framework.Result{
		ProfileResults: make(map[string]*framework.ProfileRunResult, len(sr.Profiles)),
		PrimaryProfile: sr.PrimaryProfile,
		State:          framework.NewCycleState(),
	}
	for name, pr := range sr.Profiles {
		if len(pr.Endpoints) == 0 {
			return nil, fmt.Errorf("remote profile %q picked no endpoint", name)
		}
		run := &framework.ProfileRunResult{Scores: make(map[datalayer.EndpointID]float64, len(pr.Scores))}
		for _, id := range pr.Endpoints {
			ep, ok := byID[id]
			if !ok {
				return nil, fmt.Errorf("remote profile %q picked %q which is not a candidate", name, id)
			}
			run.Endpoints = append(run.Endpoints, ep)
		}
		for id, s := range pr.Scores {
			ep, ok := byID[id]
			if !ok {
				return nil, fmt.Errorf("remote profile %q scored %q which is not a candidate", name, id)
			}
			run.Scores[ep.ID()] = s
		}
		out.ProfileResults[name] = run
	}
	return out, nil
}

// Looks endpoints up by id, which datalayer.Store does
type EndpointSource interface {
	Endpoint(id datalayer.EndpointID) (datalayer.Endpoint, bool)
}

// Serves schedule calls with a local scheduler over endpoints the source resolves, liveness on the health path and families on the metrics path
func NewHandler(sched framework.Scheduler, src EndpointSource) http.Handler {
	mux := http.NewServeMux()
	fam := newFamilies()
	mux.HandleFunc("GET "+HealthPath, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		io.WriteString(w, "ok\n")
	})
	mux.Handle("GET "+MetricsPath, fam.handler())
	mux.HandleFunc("POST "+SchedulePath, func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		outcome := "scheduled"
		defer func() {
			fam.calls.WithLabelValues(outcome).Inc()
			fam.duration.Observe(time.Since(start).Seconds())
		}()
		var sr ScheduleRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody)).Decode(&sr); err != nil {
			outcome = "malformed"
			writeError(w, http.StatusBadRequest, fmt.Errorf("decoding request: %w", err))
			return
		}
		req, err := FromWire(sr.Request)
		if err != nil {
			outcome = "malformed"
			writeError(w, http.StatusBadRequest, err)
			return
		}
		cands := make([]datalayer.Endpoint, 0, len(sr.Candidates))
		var unknown []string
		for _, id := range sr.Candidates {
			ep, ok := src.Endpoint(datalayer.EndpointID(id))
			if !ok {
				unknown = append(unknown, id)
				continue
			}
			cands = append(cands, ep)
		}
		if len(unknown) > 0 {
			outcome = "unknown-endpoint"
			writeError(w, http.StatusUnprocessableEntity, fmt.Errorf("%w %v", datalayer.ErrUnknownEndpoint, unknown))
			return
		}
		res, err := sched.Schedule(r.Context(), req, cands)
		if err != nil {
			outcome = "refused"
			writeError(w, http.StatusUnprocessableEntity, err)
			return
		}
		writeJSON(w, http.StatusOK, toResponse(res))
	})
	return mux
}

func toResponse(res *framework.Result) ScheduleResponse {
	out := ScheduleResponse{PrimaryProfile: res.PrimaryProfile, Profiles: make(map[string]ProfileResult, len(res.ProfileResults))}
	for name, run := range res.ProfileResults {
		pr := ProfileResult{Endpoints: make([]string, len(run.Endpoints)), Scores: make(map[string]float64, len(run.Scores))}
		for i, ep := range run.Endpoints {
			pr.Endpoints[i] = string(ep.ID())
		}
		for id, s := range run.Scores {
			pr.Scores[string(id)] = s
		}
		out.Profiles[name] = pr
	}
	return out
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, ErrorResponse{Error: err.Error()})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// Asks the remote scheduler first and the local one when the remote is unavailable and the request may fail open
type Fallback struct {
	remote  framework.Scheduler
	local   framework.Scheduler
	observe func(err error)
}

// A fallback calling observe with the remote's error each time the local scheduler answers instead
func NewFallback(remote, local framework.Scheduler, observe func(err error)) (*Fallback, error) {
	if remote == nil || local == nil || observe == nil {
		return nil, errors.New("a fallback scheduler needs a remote, a local scheduler and an observer")
	}
	return &Fallback{remote: remote, local: local, observe: observe}, nil
}

func (f *Fallback) Schedule(ctx context.Context, req *framework.Request, candidates []datalayer.Endpoint) (*framework.Result, error) {
	res, err := f.remote.Schedule(ctx, req, candidates)
	if !errors.Is(err, framework.ErrSchedulerUnavailable) || !req.FailOpen {
		return res, err
	}
	f.observe(err)
	if res, err = f.local.Schedule(ctx, req, candidates); err != nil {
		return nil, err
	}
	res.FailedOpen = true
	return res, nil
}

// The local scheduler's profiles, the only ones running in this process
func (f *Fallback) Profiles() map[string]*framework.Profile { return f.local.Profiles() }
