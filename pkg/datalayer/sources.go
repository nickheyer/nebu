package datalayer

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
)

// Keeps every registered source sampling exactly the endpoints of the pools it supports
type Runner struct {
	store   Store
	sources Sources
}

// Builds a runner over a store and the sources registered for it
func NewRunner(store Store, sources Sources) *Runner {
	return &Runner{store: store, sources: sources}
}

// Starts every source and retargets them on each pool or endpoint change until ctx ends, then stops them
func (r *Runner) Run(ctx context.Context) error {
	watchCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	changes := r.store.Watch(watchCtx)
	ordered := r.ordered()
	for i, src := range ordered {
		if err := src.Start(ctx); err != nil {
			return errors.Join(fmt.Errorf("start %s: %w", src.Type(), err), stopAll(ordered[:i]))
		}
	}
	if err := r.retarget(ordered); err != nil {
		return errors.Join(err, stopAll(ordered))
	}
	for {
		select {
		case <-ctx.Done():
			return stopAll(ordered)
		case ch, open := <-changes:
			if !open {
				return stopAll(ordered)
			}
			if ch.Kind == ChangeAttribute {
				continue
			}
			if err := r.retarget(ordered); err != nil {
				return errors.Join(err, stopAll(ordered))
			}
		}
	}
}

// Hands every source the ids of the endpoints whose pool it tracks, every endpoint for unscoped sources
func (r *Runner) retarget(sources []DataSource) error {
	pools := map[PoolID]Pool{}
	for _, p := range r.store.Pools() {
		pools[p.ID] = p
	}
	eps := r.store.Endpoints("", nil)
	var errs []error
	for _, src := range sources {
		scoped, isScoped := src.(PoolScoped)
		ids := make([]EndpointID, 0, len(eps))
		for _, ep := range eps {
			if isScoped {
				p, ok := pools[ep.Pool()]
				if !ok || !scoped.Tracks(p) {
					continue
				}
			}
			ids = append(ids, ep.ID())
		}
		if err := src.UpdateEndpoints(ids); err != nil {
			errs = append(errs, fmt.Errorf("update %s: %w", src.Type(), err))
		}
	}
	return errors.Join(errs...)
}

// Registered sources sorted by type so start, update and stop run in a stable sequence
func (r *Runner) ordered() []DataSource {
	out := make([]DataSource, 0, len(r.sources))
	for _, t := range slices.Sorted(maps.Keys(r.sources)) {
		out = append(out, r.sources[t])
	}
	return out
}

// Stops every source, joining the errors of those that failed to stop
func stopAll(sources []DataSource) error {
	var errs []error
	for _, src := range sources {
		if err := src.Stop(); err != nil {
			errs = append(errs, fmt.Errorf("stop %s: %w", src.Type(), err))
		}
	}
	return errors.Join(errs...)
}
