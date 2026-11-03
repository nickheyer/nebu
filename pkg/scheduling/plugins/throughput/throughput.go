// Package throughput measures output tokens per second per endpoint from completed requests
package throughput

import (
	"context"
	"sync"

	"github.com/nickheyer/nebu/pkg/datalayer"
	"github.com/nickheyer/nebu/pkg/scheduling/framework"
	"github.com/nickheyer/nebu/pkg/scheduling/plugins"
	"github.com/nickheyer/nebu/pkg/scheduling/plugins/scorer"
	"github.com/nickheyer/nebu/pkg/scheduling/registry"
)

// Plugin type name
const Type = "throughput"

// Weight of the newest measurement in the moving average when none is configured
const DefaultAlpha = 0.2

// Keeps an exponentially weighted decode rate per endpoint under scorer.CollectorThroughput
type Throughput struct {
	plugins.Named
	store datalayer.Store
	alpha float64

	mu    sync.Mutex
	rates map[datalayer.EndpointID]float64
}

// A collector weighting each new measurement by alpha, in (0, 1]
func New(store datalayer.Store, alpha float64) *Throughput {
	return &Throughput{Named: plugins.NewNamed(Type), store: store, alpha: alpha, rates: map[datalayer.EndpointID]float64{}}
}

// Folds the tokens after the first over the decode time of a successful response into the endpoint's rate
func (t *Throughput) ResponseComplete(_ context.Context, _ *framework.Request, resp *framework.Response) {
	if resp == nil || resp.Endpoint == nil || resp.Err != nil || resp.OutputTokens < 2 || resp.FirstTokenAt.IsZero() || resp.FinishedAt.IsZero() {
		return
	}
	decode := resp.FinishedAt.Sub(resp.FirstTokenAt).Seconds()
	if decode <= 0 {
		return
	}
	rate := float64(resp.OutputTokens-1) / decode
	id := resp.Endpoint.ID()
	t.mu.Lock()
	defer t.mu.Unlock()
	if prev, ok := t.rates[id]; ok {
		rate = (1-t.alpha)*prev + t.alpha*rate
	}
	t.rates[id] = rate
	t.store.SetAttribute(id, scorer.CollectorThroughput, rate)
}

// The current rate of an endpoint, zero when never measured
func (t *Throughput) Rate(id datalayer.EndpointID) float64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.rates[id]
}

// Registers the plugin with its alpha parameter, needing the store
func init() {
	registry.Register(Type, func(name string, params map[string]any, deps registry.Deps) (framework.Plugin, error) {
		p := struct {
			Alpha float64 `json:"alpha"`
		}{Alpha: DefaultAlpha}
		if err := registry.Decode(params, &p); err != nil {
			return nil, err
		}
		if p.Alpha <= 0 || p.Alpha > 1 {
			return nil, registry.Invalid("alpha must be in (0, 1]")
		}
		if deps.Store == nil {
			return nil, registry.Missing("Store")
		}
		t := New(deps.Store, p.Alpha)
		t.SetName(name)
		return t, nil
	})
}
