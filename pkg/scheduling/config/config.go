// Package config builds a scheduler and its lifecycle plugins from a YAML profile configuration
package config

import (
	"errors"
	"fmt"
	"net/url"
	"time"

	"sigs.k8s.io/yaml"

	"github.com/nickheyer/nebu/pkg/scheduling/framework"
	_ "github.com/nickheyer/nebu/pkg/scheduling/plugins/assumed"
	_ "github.com/nickheyer/nebu/pkg/scheduling/plugins/filter"
	_ "github.com/nickheyer/nebu/pkg/scheduling/plugins/picker"
	_ "github.com/nickheyer/nebu/pkg/scheduling/plugins/profile"
	_ "github.com/nickheyer/nebu/pkg/scheduling/plugins/scorer"
	_ "github.com/nickheyer/nebu/pkg/scheduling/plugins/throughput"
	"github.com/nickheyer/nebu/pkg/scheduling/registry"
)

// Bound on one remote schedule call when the configuration names none
const DefaultRemoteTimeout = 100 * time.Millisecond

var (
	// Returned when a profile or the handler names a plugin or profile the configuration does not declare
	ErrUnknownRef = errors.New("unknown reference")
	// Returned when the handler reference is not a profile handler
	ErrNotProfileHandler = errors.New("not a profile handler")
	// Returned for any other malformed configuration
	ErrInvalid = errors.New("invalid scheduler configuration")
)

// One plugin instance to build from the registry
type PluginSpec struct {
	Type       string         `json:"type"`
	Name       string         `json:"name,omitempty"`
	Parameters map[string]any `json:"parameters,omitempty"`
}

// A plugin a profile uses, the weight applying when it is a scorer and defaulting to one
type PluginRef struct {
	PluginRef string   `json:"pluginRef"`
	Weight    *float64 `json:"weight,omitempty"`
}

// A named profile and the plugins it runs in order
type ProfileSpec struct {
	Name    string      `json:"name"`
	Plugins []PluginRef `json:"plugins"`
}

// The plugin that chooses profiles
type HandlerRef struct {
	PluginRef string `json:"pluginRef"`
}

// An out of process scheduler asked first, the profiles of this configuration answering whenever it cannot
type RemoteSpec struct {
	// Base URL of the scheduler server, http or https
	URL string `json:"url"`
	// Bound on one schedule call, zero taking DefaultRemoteTimeout
	Timeout registry.Duration `json:"timeout,omitempty"`
}

// The whole scheduler configuration
type Config struct {
	Plugins        []PluginSpec  `json:"plugins"`
	Profiles       []ProfileSpec `json:"profiles"`
	ProfileHandler HandlerRef    `json:"profileHandler"`
	// The remote scheduler wrapped around the local profiles, nil when scheduling stays in process
	Remote *RemoteSpec `json:"remote,omitempty"`
}

// A profile handler naming the profiles it runs, which every handler a configuration references must be
type ProfileReferrer interface {
	framework.ProfileHandler
	ProfileRefs() []string
}

// Parses YAML, refusing unknown fields
func Parse(data []byte) (*Config, error) {
	var c Config
	if err := yaml.UnmarshalStrict(data, &c); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	return &c, nil
}

// Parses YAML and builds the scheduler and every plugin with a request lifecycle hook
func Load(data []byte, deps registry.Deps) (framework.Scheduler, []framework.Plugin, error) {
	c, err := Parse(data)
	if err != nil {
		return nil, nil, err
	}
	return c.Build(deps)
}

// Instantiates every plugin, assembles the profiles and handler, and collects the lifecycle plugins in declaration order
func (c *Config) Build(deps registry.Deps) (framework.Scheduler, []framework.Plugin, error) {
	if err := c.Remote.check(); err != nil {
		return nil, nil, err
	}
	instances := map[string]framework.Plugin{}
	order := make([]string, 0, len(c.Plugins))
	for _, spec := range c.Plugins {
		name := spec.Name
		if name == "" {
			name = spec.Type
		}
		if _, dup := instances[name]; dup {
			return nil, nil, fmt.Errorf("%w: plugin name %q declared twice", ErrInvalid, name)
		}
		p, err := registry.New(spec.Type, name, spec.Parameters, deps)
		if err != nil {
			return nil, nil, err
		}
		instances[name] = p
		order = append(order, name)
	}

	ref := c.ProfileHandler.PluginRef
	hp, ok := instances[ref]
	if !ok {
		return nil, nil, fmt.Errorf("%w: profile handler %q", ErrUnknownRef, ref)
	}
	if _, ok := hp.(framework.ProfileHandler); !ok {
		return nil, nil, fmt.Errorf("%w: %s", ErrNotProfileHandler, hp.TypedName())
	}
	handler, ok := hp.(ProfileReferrer)
	if !ok {
		return nil, nil, fmt.Errorf("%w: %s does not name the profiles it runs", ErrNotProfileHandler, hp.TypedName())
	}
	used := map[string]bool{ref: true}

	profiles := make(map[string]*framework.Profile, len(c.Profiles))
	for _, ps := range c.Profiles {
		if ps.Name == "" {
			return nil, nil, fmt.Errorf("%w: a profile has no name", ErrInvalid)
		}
		if _, dup := profiles[ps.Name]; dup {
			return nil, nil, fmt.Errorf("%w: profile %q declared twice", ErrInvalid, ps.Name)
		}
		prof, err := buildProfile(ps, instances, used)
		if err != nil {
			return nil, nil, err
		}
		profiles[ps.Name] = prof
	}
	run := map[string]bool{}
	for _, name := range handler.ProfileRefs() {
		if _, has := profiles[name]; !has {
			return nil, nil, fmt.Errorf("%w: %s runs profile %q", ErrUnknownRef, handler.TypedName(), name)
		}
		run[name] = true
	}
	for name := range profiles {
		if !run[name] {
			return nil, nil, fmt.Errorf("%w: profile %q is declared but %s never runs it", ErrInvalid, name, handler.TypedName())
		}
	}

	var lifecycle []framework.Plugin
	var reservers []framework.Reserver
	for _, name := range order {
		p := instances[name]
		if isLifecycle(p) {
			lifecycle = append(lifecycle, p)
			used[name] = true
		}
		if r, ok := p.(framework.Reserver); ok {
			reservers = append(reservers, r)
			used[name] = true
		}
		if !used[name] {
			return nil, nil, fmt.Errorf("%w: plugin %q is declared but no profile, handler or lifecycle hook uses it", ErrInvalid, name)
		}
	}
	for _, prof := range profiles {
		prof.Reservers = reservers
		prof.Observer = deps.Observer
	}

	s, err := framework.NewScheduler(handler, profiles)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	return s, lifecycle, nil
}

// Joins each referenced plugin to every profile extension point it implements
func buildProfile(ps ProfileSpec, instances map[string]framework.Plugin, used map[string]bool) (*framework.Profile, error) {
	prof := &framework.Profile{Name: ps.Name}
	pickers := 0
	seen := map[string]bool{}
	for _, r := range ps.Plugins {
		p, ok := instances[r.PluginRef]
		if !ok {
			return nil, fmt.Errorf("%w: profile %s uses plugin %q", ErrUnknownRef, ps.Name, r.PluginRef)
		}
		if seen[r.PluginRef] {
			return nil, fmt.Errorf("%w: profile %s uses plugin %q twice", ErrInvalid, ps.Name, r.PluginRef)
		}
		seen[r.PluginRef] = true
		used[r.PluginRef] = true
		joined := false
		if f, ok := p.(framework.Filter); ok {
			prof.Filters = append(prof.Filters, f)
			joined = true
		}
		if s, ok := p.(framework.Scorer); ok {
			w := 1.0
			if r.Weight != nil {
				w = *r.Weight
			}
			if w < 0 {
				return nil, fmt.Errorf("%w: profile %s gives %q a negative weight", ErrInvalid, ps.Name, r.PluginRef)
			}
			prof.Scorers = append(prof.Scorers, framework.WeightedScorer{Scorer: s, Weight: w})
			joined = true
		} else if r.Weight != nil {
			return nil, fmt.Errorf("%w: profile %s weighs %q which is not a scorer", ErrInvalid, ps.Name, r.PluginRef)
		}
		if pk, ok := p.(framework.Picker); ok {
			prof.Picker = pk
			pickers++
			joined = true
		}
		if !joined {
			return nil, fmt.Errorf("%w: profile %s uses %q which is no filter, scorer or picker", ErrInvalid, ps.Name, r.PluginRef)
		}
	}
	if pickers != 1 {
		return nil, fmt.Errorf("profile %s has %d pickers: %w", ps.Name, pickers, framework.ErrNoPicker)
	}
	return prof, nil
}

func isLifecycle(p framework.Plugin) bool {
	switch p.(type) {
	case framework.PreRequestPlugin, framework.ResponseReceivedPlugin, framework.ResponseStreamingPlugin, framework.ResponseCompletePlugin:
		return true
	}
	return false
}

// Refuses a remote that names no absolute http or https URL or a negative timeout, a zero timeout taking the default
func (r *RemoteSpec) check() error {
	if r == nil {
		return nil
	}
	u, err := url.Parse(r.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("%w: remote url %q is not an absolute http or https URL", ErrInvalid, r.URL)
	}
	if r.Timeout < 0 {
		return fmt.Errorf("%w: remote timeout must not be negative", ErrInvalid)
	}
	if r.Timeout == 0 {
		r.Timeout = registry.Duration(DefaultRemoteTimeout)
	}
	return nil
}
