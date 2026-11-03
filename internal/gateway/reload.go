package gateway

import (
	"slices"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/proto"
)

// What a config reload swaps under requests, read whole once per request
type live struct {
	keys   []string
	policy *v1.Policy
}

// Swaps the gateway keys and the default policy every request from now on reads
func (g *Gateway) Reload(keys []string, policy *v1.Policy) {
	g.live.Store(&live{keys: slices.Clone(keys), policy: proto.Clone(policy).(*v1.Policy)})
}

// The bearer keys that open inference now
func (g *Gateway) keys() []string { return g.live.Load().keys }

// The policy every unset route policy field inherits now
func (g *Gateway) policy() *v1.Policy { return g.live.Load().policy }
