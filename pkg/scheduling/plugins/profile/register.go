package profile

import (
	"slices"

	"github.com/nickheyer/nebu/pkg/requestcontrol"
	"github.com/nickheyer/nebu/pkg/scheduling/framework"
	"github.com/nickheyer/nebu/pkg/scheduling/registry"
)

// Decode selector names the pd handler accepts
const (
	SelectPromptTokens   = "prompt-tokens"
	SelectUncachedTokens = "uncached-tokens"
)

// Registers every profile handler with its parameter validation
func init() {
	registry.Register(SingleProfileHandlerType, func(name string, params map[string]any, _ registry.Deps) (framework.Plugin, error) {
		var p struct {
			Profile string `json:"profile"`
		}
		if err := registry.Decode(params, &p); err != nil {
			return nil, err
		}
		if p.Profile == "" {
			return nil, registry.Invalid("profile must name the profile to run")
		}
		h := NewSingleProfileHandler(p.Profile)
		h.SetName(name)
		return h, nil
	})
	registry.Register(ShadowProfileHandlerType, func(name string, params map[string]any, deps registry.Deps) (framework.Plugin, error) {
		var p struct {
			Primary string   `json:"primary"`
			Shadows []string `json:"shadows"`
		}
		if err := registry.Decode(params, &p); err != nil {
			return nil, err
		}
		if p.Primary == "" {
			return nil, registry.Invalid("primary must name the routing profile")
		}
		if len(p.Shadows) == 0 {
			return nil, registry.Invalid("shadows must name at least one profile")
		}
		for i, s := range p.Shadows {
			if s == "" || s == p.Primary || slices.Contains(p.Shadows[:i], s) {
				return nil, registry.Invalid("shadow %q must be a distinct profile other than the primary", s)
			}
		}
		h := NewShadowProfileHandler(LogShadow(deps.Logger), p.Primary, p.Shadows...)
		h.SetName(name)
		return h, nil
	})
	registry.Register(PDProfileHandlerType, func(name string, params map[string]any, deps registry.Deps) (framework.Plugin, error) {
		p := struct {
			PrefillProfile string `json:"prefillProfile"`
			DecodeProfile  string `json:"decodeProfile"`
			Threshold      *int   `json:"threshold"`
			DecodeSelector string `json:"decodeSelector"`
			Handoff        string `json:"handoff"`
		}{DecodeSelector: SelectPromptTokens}
		if err := registry.Decode(params, &p); err != nil {
			return nil, err
		}
		if p.PrefillProfile == "" || p.DecodeProfile == "" || p.PrefillProfile == p.DecodeProfile {
			return nil, registry.Invalid("prefillProfile and decodeProfile must name two distinct profiles")
		}
		if p.Threshold == nil || *p.Threshold < 0 {
			return nil, registry.Invalid("threshold must be a token count of zero or more")
		}
		var sel DecodeSelector
		switch p.DecodeSelector {
		case SelectPromptTokens:
			sel = PromptTokens
		case SelectUncachedTokens:
			if deps.Indexer == nil {
				return nil, registry.Missing("Indexer")
			}
			sel = UncachedTokens(deps.Indexer)
		default:
			return nil, registry.Invalid("decodeSelector must be %s or %s, got %q", SelectPromptTokens, SelectUncachedTokens, p.DecodeSelector)
		}
		if p.Handoff != "" && !slices.Contains(requestcontrol.HandoffTypes(), p.Handoff) {
			return nil, registry.Invalid("handoff must be one of %v, got %q", requestcontrol.HandoffTypes(), p.Handoff)
		}
		h := NewPDProfileHandler(p.PrefillProfile, p.DecodeProfile, *p.Threshold, sel)
		h.handoff = p.Handoff
		h.SetName(name)
		return h, nil
	})
}
