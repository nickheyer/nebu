// Package plugins holds what the reference scheduling plugins share
package plugins

import (
	"maps"
	"slices"
	"strconv"
	"time"

	"github.com/nickheyer/nebu/pkg/datalayer"
	"github.com/nickheyer/nebu/pkg/latency"
	"github.com/nickheyer/nebu/pkg/prefixcache"
	"github.com/nickheyer/nebu/pkg/scheduling/framework"
	"github.com/nickheyer/nebu/pkg/scheduling/registry"
)

// The cycle state key under which the prompt's chained chunk hashes are kept as []prefixcache.BlockHash
const PrefixHashesKey = "prefix.hashes"

// The cycle state key the prefix cache scorer keeps each candidate's matched prompt fraction under
const PrefixFractionsKey = "prefix.fractions"

// A plugin's type and instance name, the name defaulting to the type
type Named struct {
	typed framework.TypedName
}

// A name whose type and instance name are both typ
func NewNamed(typ string) Named { return Named{typed: framework.TypedName{Type: typ, Name: typ}} }

func (n *Named) TypedName() framework.TypedName { return n.typed }

// Replaces the instance name, an empty name keeping the type as the name
func (n *Named) SetName(name string) {
	if name != "" {
		n.typed.Name = name
	}
}

// A factory for a plugin that takes no parameters and no dependencies, refusing any parameter
func Plain[T interface {
	framework.Plugin
	SetName(string)
}](build func() T) registry.Factory {
	return func(name string, params map[string]any, _ registry.Deps) (framework.Plugin, error) {
		if err := registry.Decode(params, &struct{}{}); err != nil {
			return nil, err
		}
		p := build()
		p.SetName(name)
		return p, nil
	}
}

// The scraped metrics of an endpoint, false when the metrics collector stored none
func Metrics(ep datalayer.Endpoint) (*datalayer.ServerMetrics, bool) {
	m, ok := datalayer.Attribute[*datalayer.ServerMetrics](ep, datalayer.CollectorMetrics)
	return m, ok && m != nil
}

// The prompt hashes kept in the cycle state, hashing and storing them on the first call of a cycle
func PromptHashes(state *framework.CycleState, idx prefixcache.Indexer, req *framework.Request) []prefixcache.BlockHash {
	if state != nil {
		if h, ok := framework.ReadState[[]prefixcache.BlockHash](state, PrefixHashesKey); ok {
			return h
		}
	}
	h := idx.Hash(req.TargetModel, req.Adapter, req.Prompt, req.PromptTokenIDs)
	if state != nil {
		state.Write(PrefixHashesKey, h)
	}
	return h
}

// Share of the prompt blocks each endpoint holds from the start of the prompt, zero for no match
func PrefixFractions(idx prefixcache.Indexer, hashes []prefixcache.BlockHash, eps []datalayer.Endpoint) map[datalayer.EndpointID]float64 {
	out := make(map[datalayer.EndpointID]float64, len(eps))
	ids := make([]datalayer.EndpointID, len(eps))
	for i, ep := range eps {
		ids[i] = ep.ID()
		out[ep.ID()] = 0
	}
	if len(hashes) == 0 {
		return out
	}
	total := float64(len(hashes))
	for _, m := range idx.Match(hashes, ids) {
		if _, ok := out[m.Endpoint]; ok {
			out[m.Endpoint] = min(float64(m.Blocks), total) / total
		}
	}
	return out
}

// Every endpoint the result routes to, each profile's endpoints in order and each endpoint once
func ResultEndpoints(res *framework.Result) []datalayer.Endpoint {
	if res == nil {
		return nil
	}
	seen := map[datalayer.EndpointID]bool{}
	var out []datalayer.Endpoint
	add := func(r *framework.ProfileRunResult) {
		if r == nil {
			return
		}
		for _, ep := range r.Endpoints {
			if !seen[ep.ID()] {
				seen[ep.ID()] = true
				out = append(out, ep)
			}
		}
	}
	add(res.ProfileResults[res.PrimaryProfile])
	for _, name := range sortedKeys(res.ProfileResults) {
		if name != res.PrimaryProfile {
			add(res.ProfileResults[name])
		}
	}
	return out
}

// The first endpoint of every profile in the result, the primary profile first
func ResultHeads(res *framework.Result) []datalayer.Endpoint {
	if res == nil {
		return nil
	}
	var out []datalayer.Endpoint
	seen := map[datalayer.EndpointID]bool{}
	add := func(r *framework.ProfileRunResult) {
		if r != nil && len(r.Endpoints) > 0 && !seen[r.Endpoints[0].ID()] {
			seen[r.Endpoints[0].ID()] = true
			out = append(out, r.Endpoints[0])
		}
	}
	add(res.ProfileResults[res.PrimaryProfile])
	for _, name := range sortedKeys(res.ProfileResults) {
		if name != res.PrimaryProfile {
			add(res.ProfileResults[name])
		}
	}
	return out
}

func sortedKeys(m map[string]*framework.ProfileRunResult) []string {
	return slices.Sorted(maps.Keys(m))
}

// Prompt and expected output tokens the request will hold on an endpoint
func Cost(req *framework.Request) int {
	return max(req.PromptTokens, 0) + max(req.ExpectedOutputTokens, 0)
}

// KV cache tokens an endpoint holds, from scraped blocks or its kv-capacity label, false when neither reports
func KVTokens(ep datalayer.Endpoint) (int, bool) {
	if m, ok := Metrics(ep); ok && m.NumGPUBlocks > 0 && m.BlockSize > 0 {
		return m.NumGPUBlocks * m.BlockSize, true
	}
	if n, err := strconv.Atoi(ep.Labels()[datalayer.LabelKVCapacity]); err == nil && n > 0 {
		return n, true
	}
	return 0, false
}

// The predictor inputs of the request on each endpoint, cached tokens read from the prefix index
func LatencyFeatures(idx prefixcache.Indexer, state *framework.CycleState, req *framework.Request, eps []datalayer.Endpoint) map[datalayer.EndpointID]latency.Features {
	frac := PrefixFractions(idx, PromptHashes(state, idx, req), eps)
	out := make(map[datalayer.EndpointID]latency.Features, len(eps))
	for _, ep := range eps {
		f := latency.Features{
			PromptTokens: req.PromptTokens,
			CachedTokens: int(frac[ep.ID()] * float64(req.PromptTokens)),
		}
		if m, ok := Metrics(ep); ok {
			f.QueuedRequests = m.QueuedRequests
			f.RunningRequests = m.RunningRequests
			f.KVCacheUtilization = m.KVCacheUtilization
		}
		f.AssumedRequests, _ = datalayer.Attribute[int](ep, datalayer.CollectorAssumed)
		out[ep.ID()] = f
	}
	return out
}

// Whether a prediction misses the objective, mean plus one deviation once confident and the prior's mean before
func Misses(p latency.Prediction, objective time.Duration) bool {
	bound := p.Mean
	if p.Confident {
		bound += p.StdDev
	}
	return bound > objective
}
