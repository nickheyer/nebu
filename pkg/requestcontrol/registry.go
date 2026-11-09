package requestcontrol

import (
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/nickheyer/nebu/pkg/scheduling/framework"
	"github.com/nickheyer/nebu/pkg/scheduling/registry"
	"sigs.k8s.io/yaml"
)

var (
	// Returned when no factory is registered under a plugin type
	ErrUnknownType = errors.New("unknown request control plugin type")
	// Returned for a chain document that does not parse or names what the registry lacks
	ErrInvalidChain = errors.New("invalid request control chain")
)

// Builds one request control plugin from its configured name and parameters
type Factory func(name string, params map[string]any) (framework.Plugin, error)

// Maps plugin type names to the factories that build them, one registry per host that offers plugins
type Registry struct {
	mu        sync.RWMutex
	factories map[string]Factory
}

// A registry holding the request control plugins this package ships
func NewRegistry() *Registry {
	r := &Registry{factories: map[string]Factory{}}
	r.Register(GuardrailType, NewGuardrailPlugin)
	return r
}

// Registers a factory under a plugin type, panicking on a second registration of the same type
func (r *Registry) Register(typ string, f Factory) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.factories[typ]; dup {
		panic(fmt.Sprintf("request control plugin type %q registered twice", typ))
	}
	r.factories[typ] = f
}

// Every registered plugin type in sorted order
func (r *Registry) Types() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.factories))
	for t := range r.factories {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// Builds a plugin of a registered type, naming it after its type when name is empty
func (r *Registry) New(typ, name string, params map[string]any) (framework.Plugin, error) {
	r.mu.RLock()
	f, ok := r.factories[typ]
	r.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w %q, known types are %v", ErrUnknownType, typ, r.Types())
	}
	if name == "" {
		name = typ
	}
	p, err := f(name, params)
	if err != nil {
		return nil, fmt.Errorf("plugin %s of type %s: %w", name, typ, err)
	}
	return p, nil
}

// One plugin of a chain document, ordered by the plugin names it runs before and after
type ChainPlugin struct {
	Type       string         `json:"type"`
	Name       string         `json:"name,omitempty"`
	Parameters map[string]any `json:"parameters,omitempty"`
	Before     []string       `json:"before,omitempty"`
	After      []string       `json:"after,omitempty"`
}

// The processing chain as a document, every plugin a registry type with its ordering
type Chain struct {
	Plugins []ChainPlugin `json:"plugins"`
}

// Parses a chain document from YAML, refusing unknown fields
func ParseChain(data []byte) (*Chain, error) {
	var c Chain
	if err := yaml.UnmarshalStrict(data, &c); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidChain, err)
	}
	if len(c.Plugins) == 0 {
		return nil, fmt.Errorf("%w: the chain names no plugins", ErrInvalidChain)
	}
	return &c, nil
}

// Builds every plugin from the registry and orders them into a topology by the names they run before and after
func (c *Chain) Build(r *Registry) (Topology, error) {
	regs := make([]Registration, 0, len(c.Plugins))
	names := make(map[string]framework.TypedName, len(c.Plugins))
	for i, spec := range c.Plugins {
		if spec.Type == "" {
			return nil, fmt.Errorf("%w: plugin %d names no type", ErrInvalidChain, i)
		}
		p, err := r.New(spec.Type, spec.Name, spec.Parameters)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrInvalidChain, err)
		}
		tn := p.TypedName()
		if _, dup := names[tn.Name]; dup {
			return nil, fmt.Errorf("%w: two plugins are named %q", ErrInvalidChain, tn.Name)
		}
		names[tn.Name] = tn
		regs = append(regs, Registration{Plugin: p})
	}
	for i, spec := range c.Plugins {
		for _, b := range spec.Before {
			tn, ok := names[b]
			if !ok {
				return nil, fmt.Errorf("%w: plugin %s runs before unknown plugin %q", ErrInvalidChain, regs[i].Plugin.TypedName(), b)
			}
			regs[i].Before = append(regs[i].Before, tn)
		}
		for _, a := range spec.After {
			tn, ok := names[a]
			if !ok {
				return nil, fmt.Errorf("%w: plugin %s runs after unknown plugin %q", ErrInvalidChain, regs[i].Plugin.TypedName(), a)
			}
			regs[i].After = append(regs[i].After, tn)
		}
	}
	t, err := NewTopology(regs...)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidChain, err)
	}
	return t, nil
}

// Parses a chain document and builds its topology from the registry
func LoadChain(data []byte, r *Registry) (Topology, error) {
	c, err := ParseChain(data)
	if err != nil {
		return nil, err
	}
	return c.Build(r)
}

// Decodes parameters into a struct by its JSON tags, refusing any name the struct does not declare
func DecodeParams(params map[string]any, into any) error {
	return registry.Decode(params, into)
}
