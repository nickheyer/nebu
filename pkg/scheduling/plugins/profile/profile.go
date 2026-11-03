// Package profile holds the reference profile handlers choosing which profiles run and which result routes
package profile

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/nickheyer/nebu/pkg/datalayer"
	"github.com/nickheyer/nebu/pkg/prefixcache"
	"github.com/nickheyer/nebu/pkg/scheduling/framework"
	"github.com/nickheyer/nebu/pkg/scheduling/plugins"
)

// Plugin type names
const (
	SingleProfileHandlerType = "single-profile-handler"
	ShadowProfileHandlerType = "shadow-profile-handler"
	PDProfileHandlerType     = "pd-profile-handler"
)

// The cycle state key under which the decode run's *framework.ProfileRunResult is kept before prefill runs
const DecodeResultKey = "pd.decode.result"

const (
	shadowErrorKey = "shadow.error."
	prefillWanted  = "pd.prefill.wanted"
)

func missing(profile string) error {
	return fmt.Errorf("profile %s did not run: %w", profile, framework.ErrNoProfile)
}

// Runs one profile and routes by its result
type SingleProfileHandler struct {
	plugins.Named
	profile string
}

func NewSingleProfileHandler(profile string) *SingleProfileHandler {
	return &SingleProfileHandler{Named: plugins.NewNamed(SingleProfileHandlerType), profile: profile}
}

// The profile names the handler runs
func (h *SingleProfileHandler) ProfileRefs() []string { return []string{h.profile} }

func (h *SingleProfileHandler) Pick(_ context.Context, _ *framework.CycleState, _ *framework.Request, profiles map[string]*framework.Profile, results map[string]*framework.ProfileRunResult) map[string]*framework.Profile {
	if _, done := results[h.profile]; done {
		return nil
	}
	if p, ok := profiles[h.profile]; ok {
		return map[string]*framework.Profile{h.profile: p}
	}
	return nil
}

func (h *SingleProfileHandler) ProcessResults(_ context.Context, _ *framework.CycleState, _ *framework.Request, results map[string]*framework.ProfileRunResult) (*framework.Result, error) {
	r, ok := results[h.profile]
	if !ok {
		return nil, missing(h.profile)
	}
	return &framework.Result{ProfileResults: map[string]*framework.ProfileRunResult{h.profile: r}, PrimaryProfile: h.profile}, nil
}

// What one shadow profile chose next to the primary, or why it chose nothing
type ShadowOutcome struct {
	Profile string
	Primary *framework.ProfileRunResult
	Shadow  *framework.ProfileRunResult
	Err     error
}

// Receives every shadow outcome of a cycle
type ShadowLog func(ctx context.Context, req *framework.Request, out ShadowOutcome)

// Logs each shadow outcome with both picks and whether their primaries agree, failures at warn level
func LogShadow(logger *slog.Logger) ShadowLog {
	return func(ctx context.Context, req *framework.Request, out ShadowOutcome) {
		attrs := []any{"request", req.ID, "profile", out.Profile, "primary", ids(out.Primary), "shadow", ids(out.Shadow)}
		if out.Err != nil {
			logger.WarnContext(ctx, "shadow profile failed", append(attrs, "err", out.Err)...)
			return
		}
		agree := len(out.Primary.Endpoints) > 0 && len(out.Shadow.Endpoints) > 0 && out.Primary.Endpoints[0].ID() == out.Shadow.Endpoints[0].ID()
		logger.InfoContext(ctx, "shadow profile", append(attrs, "agree", agree)...)
	}
}

func ids(r *framework.ProfileRunResult) []datalayer.EndpointID {
	if r == nil {
		return nil
	}
	out := make([]datalayer.EndpointID, len(r.Endpoints))
	for i, ep := range r.Endpoints {
		out[i] = ep.ID()
	}
	return out
}

// Runs the primary and every shadow profile in one cycle, logs the shadows and routes by the primary alone
type ShadowProfileHandler struct {
	plugins.Named
	primary string
	shadows []string
	log     ShadowLog
}

func NewShadowProfileHandler(log ShadowLog, primary string, shadows ...string) *ShadowProfileHandler {
	return &ShadowProfileHandler{Named: plugins.NewNamed(ShadowProfileHandlerType), primary: primary, shadows: shadows, log: log}
}

// The profile names the handler runs, the primary first
func (h *ShadowProfileHandler) ProfileRefs() []string {
	return append([]string{h.primary}, h.shadows...)
}

func (h *ShadowProfileHandler) Pick(_ context.Context, _ *framework.CycleState, _ *framework.Request, profiles map[string]*framework.Profile, results map[string]*framework.ProfileRunResult) map[string]*framework.Profile {
	if _, done := results[h.primary]; done {
		return nil
	}
	p, ok := profiles[h.primary]
	if !ok {
		return nil
	}
	out := map[string]*framework.Profile{h.primary: p}
	for _, name := range h.shadows {
		if sp, ok := profiles[name]; ok {
			out[name] = sp
		}
	}
	return out
}

// Keeps the cycle going when a shadow fails, keeping the error for the shadow log
func (h *ShadowProfileHandler) ProfileFailed(_ context.Context, state *framework.CycleState, _ *framework.Request, profile string, err error) bool {
	for _, name := range h.shadows {
		if name == profile {
			state.Write(shadowErrorKey+profile, err)
			return true
		}
	}
	return false
}

func (h *ShadowProfileHandler) ProcessResults(ctx context.Context, state *framework.CycleState, req *framework.Request, results map[string]*framework.ProfileRunResult) (*framework.Result, error) {
	primary, ok := results[h.primary]
	if !ok {
		return nil, missing(h.primary)
	}
	for _, name := range h.shadows {
		out := ShadowOutcome{Profile: name, Primary: primary, Shadow: results[name]}
		if err, failed := framework.ReadState[error](state, shadowErrorKey+name); failed {
			out.Err = err
		} else if out.Shadow == nil {
			out.Err = missing(name)
		}
		h.log(ctx, req, out)
	}
	return &framework.Result{ProfileResults: map[string]*framework.ProfileRunResult{h.primary: primary}, PrimaryProfile: h.primary}, nil
}

// Counts the prompt tokens the chosen decode endpoint would prefill itself, prefill running at or above the threshold
type DecodeSelector func(ctx context.Context, state *framework.CycleState, req *framework.Request, decode *framework.ProfileRunResult) int

// Counts the whole prompt, so prefill runs whenever the prompt reaches the threshold
func PromptTokens(_ context.Context, _ *framework.CycleState, req *framework.Request, _ *framework.ProfileRunResult) int {
	return req.PromptTokens
}

// Counts the prompt tokens missing from the chosen decode endpoint's prefix cache
func UncachedTokens(idx prefixcache.Indexer) DecodeSelector {
	return func(_ context.Context, state *framework.CycleState, req *framework.Request, decode *framework.ProfileRunResult) int {
		if len(decode.Endpoints) == 0 {
			return req.PromptTokens
		}
		head := decode.Endpoints[0]
		frac := plugins.PrefixFractions(idx, plugins.PromptHashes(state, idx, req), []datalayer.Endpoint{head})[head.ID()]
		return req.PromptTokens - int(frac*float64(req.PromptTokens))
	}
}

// Runs decode, then prefill when the selector counts at least threshold tokens, routing by decode with prefill attached
type PDProfileHandler struct {
	plugins.Named
	prefill   string
	decode    string
	threshold int
	selector  DecodeSelector
	// The handoff plugin type the relay carries prefill to decode with, empty for the one the pool's runtime declares
	handoff string
}

func NewPDProfileHandler(prefill, decode string, threshold int, selector DecodeSelector) *PDProfileHandler {
	return &PDProfileHandler{Named: plugins.NewNamed(PDProfileHandlerType), prefill: prefill, decode: decode, threshold: threshold, selector: selector}
}

// The profile names the handler runs, decode first
func (h *PDProfileHandler) ProfileRefs() []string { return []string{h.decode, h.prefill} }

// The handoff plugin type the parameters name, empty when they name none
func (h *PDProfileHandler) Handoff() string { return h.handoff }

// Picks decode first, then prefill with the decode result written under DecodeResultKey
func (h *PDProfileHandler) Pick(ctx context.Context, state *framework.CycleState, req *framework.Request, profiles map[string]*framework.Profile, results map[string]*framework.ProfileRunResult) map[string]*framework.Profile {
	decoded, ok := results[h.decode]
	if !ok {
		if p, has := profiles[h.decode]; has {
			return map[string]*framework.Profile{h.decode: p}
		}
		return nil
	}
	if _, done := results[h.prefill]; done {
		return nil
	}
	state.Write(DecodeResultKey, decoded)
	if h.selector(ctx, state, req, decoded) < h.threshold {
		return nil
	}
	state.Write(prefillWanted, true)
	if p, has := profiles[h.prefill]; has {
		return map[string]*framework.Profile{h.prefill: p}
	}
	return nil
}

func (h *PDProfileHandler) ProcessResults(_ context.Context, state *framework.CycleState, _ *framework.Request, results map[string]*framework.ProfileRunResult) (*framework.Result, error) {
	decoded, ok := results[h.decode]
	if !ok {
		return nil, missing(h.decode)
	}
	out := map[string]*framework.ProfileRunResult{h.decode: decoded}
	if wanted, _ := framework.ReadState[bool](state, prefillWanted); wanted {
		pre, ok := results[h.prefill]
		if !ok {
			return nil, missing(h.prefill)
		}
		out[h.prefill] = pre
	}
	return &framework.Result{ProfileResults: out, PrimaryProfile: h.decode}, nil
}
