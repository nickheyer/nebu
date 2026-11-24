package gateway

import (
	"cmp"
	"fmt"
	"slices"

	"github.com/nickheyer/nebu/pkg/requestcontrol"
	"github.com/nickheyer/nebu/pkg/scheduling/framework"
)

// The built in request control chain, run when the configuration names no chain document
const DefaultChain = `plugins:
- type: mesh-entry
- type: objective-resolver
- type: route-policy
  after: [mesh-entry, objective-resolver]
- type: tools-planner
  after: [route-policy]
- type: tokenize
  after: [tools-planner]
- type: served-only
- type: tools-emulation
  after: [served-only]
- type: provider-credential
- type: served-endpoint
- type: trace-id
- type: retry-after
`

// The registry of every request control plugin the gateway offers a chain document, the reference guardrail included
func (g *Gateway) registry() *requestcontrol.Registry {
	reg := requestcontrol.NewRegistry()
	fixed := map[string]func() framework.Plugin{
		"mesh-entry":          func() framework.Plugin { return &meshEntry{} },
		"objective-resolver":  func() framework.Plugin { return &objectiveResolver{g: g} },
		"route-policy":        func() framework.Plugin { return &routePolicy{g: g} },
		"tools-planner":       func() framework.Plugin { return &toolsPlanner{g: g} },
		"tokenize":            func() framework.Plugin { return &tokenize{g: g} },
		"served-only":         func() framework.Plugin { return &servedOnly{g: g} },
		"provider-credential": func() framework.Plugin { return &providerCredential{g: g} },
		"tools-emulation":     func() framework.Plugin { return &toolsEmulation{g: g} },
		"served-endpoint":     func() framework.Plugin { return &servedEndpoint{} },
		"trace-id":            func() framework.Plugin { return &traceID{} },
		"retry-after":         func() framework.Plugin { return &retryAfter{clock: g.clock} },
	}
	for typ, build := range fixed {
		reg.Register(typ, func(name string, params map[string]any) (framework.Plugin, error) {
			p := build()
			// The step signing provider requests alone takes a name of its own, for a chain that gave its type's name to another plugin
			if signs, ok := p.(*providerCredential); ok {
				signs.name = name
			} else if name != typ {
				return nil, fmt.Errorf("plugin type %s is named by its type, not %q", typ, name)
			}
			if len(params) > 0 {
				return nil, fmt.Errorf("plugin type %s takes no parameters", typ)
			}
			return p, nil
		})
	}
	return reg
}

// Builds the processing chain from a YAML document, the built in one when the document is empty, a document without the
// provider credential step given it so provider models are signed
func (g *Gateway) chain(doc []byte) (requestcontrol.Topology, error) {
	if len(doc) == 0 {
		doc = []byte(DefaultChain)
	}
	c, err := requestcontrol.ParseChain(doc)
	if err != nil {
		return nil, fmt.Errorf("request chain: %w", err)
	}
	// Unordered as in the built in chain, so it runs among the post decision steps wherever the document places them
	signs := requestcontrol.ChainPlugin{Type: signingStep}
	added := !slices.ContainsFunc(c.Plugins, func(p requestcontrol.ChainPlugin) bool { return p.Type == signs.Type })
	if added {
		// A plugin of another type the document gave the step's name leaves it to take another
		taken := func(p requestcontrol.ChainPlugin) bool {
			return cmp.Or(p.Name, p.Type) == cmp.Or(signs.Name, signs.Type)
		}
		for slices.ContainsFunc(c.Plugins, taken) {
			signs.Name = cmp.Or(signs.Name, signs.Type) + "-signing"
		}
		c.Plugins = append(c.Plugins, signs)
	}
	topo, err := c.Build(g.registry())
	if err != nil {
		return nil, fmt.Errorf("request chain: %w", err)
	}
	if added {
		g.log.Warn("request chain names no provider-credential step, adding it so provider models are signed with the caller's credential")
	}
	return topo, nil
}
