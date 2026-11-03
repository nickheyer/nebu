package gateway

import (
	"context"

	"github.com/nickheyer/nebu/pkg/datalayer"
	"github.com/nickheyer/nebu/pkg/prefixcache"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/scheduling/plugins"
)

// Requests a media endpoint runs at once, one generation job each
const mediaCapacity = 1

// Sizes the prefix index from scraped KV blocks, holds media endpoints to one job, and forgets departed endpoints
func (g *Gateway) followStore(ctx context.Context) {
	changes := g.store.Watch(ctx)
	sized := map[datalayer.EndpointID]int{}
	for _, ep := range g.store.Endpoints("", nil) {
		g.fit(ep, sized)
	}
	for {
		select {
		case <-ctx.Done():
			return
		case ch, ok := <-changes:
			if !ok {
				return
			}
			switch ch.Kind {
			case datalayer.ChangeEndpointDeleted:
				delete(sized, ch.Endpoint)
				g.index.Forget(ch.Endpoint)
				g.predictor.Forget(ch.Endpoint)
				g.tokenizers.keep(g.liveInstances())
			case datalayer.ChangePoolPut:
				for _, ep := range g.store.Endpoints(ch.Pool, nil) {
					g.fit(ep, sized)
				}
			case datalayer.ChangeEndpointPut, datalayer.ChangeAttribute:
				if ep, ok := g.store.Endpoint(ch.Endpoint); ok {
					g.fit(ep, sized)
				}
			}
		}
	}
}

// Sets the index capacity from scraped KV blocks or the kv-capacity label when it changed and the capacity attribute of a media endpoint
func (g *Gateway) fit(ep datalayer.Endpoint, sized map[datalayer.EndpointID]int) {
	if tokens, ok := plugins.KVTokens(ep); ok {
		chunk := g.cfg.Prefix.ChunkTokens
		if chunk <= 0 {
			chunk = prefixcache.DefaultChunkTokens
		}
		if blocks := tokens / chunk; sized[ep.ID()] != blocks {
			sized[ep.ID()] = blocks
			g.index.SetCapacity(ep.ID(), blocks)
		}
	}
	pool, ok := g.store.Pool(ep.Pool())
	if !ok || g.runtimes.API(pool.Runtime) != v1.ApiFlavor_API_FLAVOR_SDCPP {
		return
	}
	if n, ok := datalayer.Attribute[int](ep, datalayer.CollectorCapacity); !ok || n != mediaCapacity {
		g.store.SetAttribute(ep.ID(), datalayer.CollectorCapacity, mediaCapacity)
	}
}
