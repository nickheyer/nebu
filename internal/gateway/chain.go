package gateway

import (
	"fmt"

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
- type: served-endpoint
- type: trace-id
- type: retry-after
`

// The registry of every request control plugin the gateway offers a chain document, the reference guardrail included
func (g *Gateway) registry() *requestcontrol.Registry {
	reg := requestcontrol.NewRegistry()
	fixed := map[string]func() framework.Plugin{
		"mesh-entry":         func() framework.Plugin { return &meshEntry{} },
		"objective-resolver": func() framework.Plugin { return &objectiveResolver{g: g} },
		"route-policy":       func() framework.Plugin { return &routePolicy{g: g} },
		"tools-planner":      func() framework.Plugin { return &toolsPlanner{g: g} },
		"tokenize":           func() framework.Plugin { return &tokenize{g: g} },
		"served-only":        func() framework.Plugin { return &servedOnly{g: g} },
		"tools-emulation":    func() framework.Plugin { return &toolsEmulation{g: g} },
		"served-endpoint":    func() framework.Plugin { return &servedEndpoint{} },
		"trace-id":           func() framework.Plugin { return &traceID{} },
		"retry-after":        func() framework.Plugin { return &retryAfter{clock: g.clock} },
	}
	for typ, build := range fixed {
		reg.Register(typ, func(name string, params map[string]any) (framework.Plugin, error) {
			if name != typ {
				return nil, fmt.Errorf("plugin type %s is named by its type, not %q", typ, name)
			}
			if len(params) > 0 {
				return nil, fmt.Errorf("plugin type %s takes no parameters", typ)
			}
			return build(), nil
		})
	}
	return reg
}

// Builds the processing chain from a YAML document, the built in one when the document is empty
func (g *Gateway) chain(doc []byte) (requestcontrol.Topology, error) {
	if len(doc) == 0 {
		doc = []byte(DefaultChain)
	}
	topo, err := requestcontrol.LoadChain(doc, g.registry())
	if err != nil {
		return nil, fmt.Errorf("request chain: %w", err)
	}
	return topo, nil
}
