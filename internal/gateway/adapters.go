package gateway

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/nickheyer/nebu/pkg/datalayer"
	"github.com/nickheyer/nebu/pkg/requestcontrol"
)

// Time a pool gets to serve an adapter after its endpoint loaded it
const adapterServeWait = 30 * time.Second

// One adapter the model store holds, named as requests name it
type StoredAdapter struct {
	Name string
	// The repo of the base model the adapter was trained on
	Base string
	// The directory holding its weights and adapter config
	Path string
}

// What the gateway reads of the model store, the adapters a request may name, an unreadable store refused as an error
type Adapters interface {
	Adapters() ([]StoredAdapter, error)
}

// One in flight load of an adapter, shared by every request naming it meanwhile
type adapterLoad struct {
	done chan struct{}
	err  error
}

// Loads the stored adapter a request names into a ready endpoint of a pool serving its base model, then waits for the pool to serve it
func (g *Gateway) loadAdapter(ctx context.Context, name string) error {
	if g.adapters == nil || name == "" {
		return fmt.Errorf("%w: no adapter store serves %q", requestcontrol.ErrUnknownModel, name)
	}
	load := &adapterLoad{done: make(chan struct{})}
	if v, loaded := g.loads.LoadOrStore(name, load); loaded {
		other := v.(*adapterLoad)
		select {
		case <-other.done:
			return other.err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	defer g.loads.Delete(name)
	defer close(load.done)
	load.err = g.loadAdapterOnce(ctx, name)
	return load.err
}

func (g *Gateway) loadAdapterOnce(ctx context.Context, name string) error {
	stored, err := g.adapters.Adapters()
	if err != nil {
		return fmt.Errorf("adapter store: %w", err)
	}
	var found *StoredAdapter
	for _, a := range stored {
		if a.Name == name {
			found = &a
			break
		}
	}
	if found == nil {
		return fmt.Errorf("%w: no stored adapter is named %q", requestcontrol.ErrUnknownModel, name)
	}
	ep, ok := g.adapterSeat(found.Base)
	if !ok {
		return fmt.Errorf("%w: adapter %s needs a ready endpoint of base model %s and none serves it", requestcontrol.ErrNoReadyEndpoints, name, found.Base)
	}
	instance := ep.Labels()[datalayer.LabelInstance]
	if err := g.instances.LoadAdapter(ctx, instance, name, found.Path); err != nil {
		return fmt.Errorf("adapter %s on %s: %w", name, instance, err)
	}
	g.log.Info("adapter loaded for a request naming it", "adapter", name, "instance", instance, "endpoint", ep.Address().String())
	wait, cancel := context.WithTimeout(ctx, adapterServeWait)
	defer cancel()
	for {
		if _, _, err := g.resolver.next.Lookup(wait, name); err == nil {
			return nil
		}
		select {
		case <-wait.Done():
			return fmt.Errorf("%w: adapter %s loaded on %s but the pool does not serve it yet", requestcontrol.ErrNoReadyEndpoints, name, instance)
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// The ready endpoint with the fewest requests in flight among pools serving the base model
func (g *Gateway) adapterSeat(base string) (datalayer.Endpoint, bool) {
	var best datalayer.Endpoint
	bestLoad := 0
	for _, p := range g.store.Pools() {
		if modelRepo(p.Model) != base && p.Model != base {
			continue
		}
		for _, ep := range g.store.Endpoints(p.ID, datalayer.ReadyAndHealthy) {
			if ep.Labels()[datalayer.LabelAdapter] != "" {
				continue
			}
			load := g.InFlight(ep.Labels()[datalayer.LabelInstance])
			if best == nil || load < bestLoad {
				best, bestLoad = ep, load
			}
		}
	}
	return best, best != nil
}

// The repo of a pool's model key, source colon repo colon group, the whole name when it is not a key
func modelRepo(model string) string {
	if parts := strings.Split(model, ":"); len(parts) == 3 {
		return parts[1]
	}
	return model
}
