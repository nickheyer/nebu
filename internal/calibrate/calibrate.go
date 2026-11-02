// Package calibrate learns estimator corrections from measured runs.
package calibrate

import (
	"context"
	"fmt"
	"sync"

	"github.com/nickheyer/nebu/internal/db"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const smoothing = 0.3

// Stored estimator corrections by runtime and architecture.
type Table struct {
	store   *db.DB
	mu      sync.Mutex
	entries map[string]*v1.Calibration
}

// Loads every correction from the store
func Open(ctx context.Context, store *db.DB) (*Table, error) {
	rows, err := store.ListCalibrations(ctx)
	if err != nil {
		return nil, err
	}
	t := &Table{store: store, entries: map[string]*v1.Calibration{}}
	for _, r := range rows {
		t.entries[Key(r.RuntimeID, r.Architecture)] = r.Calibration
	}
	return t, nil
}

// Builds the lookup key
func Key(runtimeID, architecture string) string {
	return runtimeID + "|" + architecture
}

// Returns the learned overhead delta in bytes, zero when unknown or without a table
func (t *Table) Delta(runtimeID, architecture string) float64 {
	if t == nil {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if c, ok := t.entries[Key(runtimeID, architecture)]; ok {
		return c.GetOverheadDelta()
	}
	return 0
}

// Updates corrections from measured versus planned device memory.
func (t *Table) Record(ctx context.Context, runtimeID, architecture string, measured, planned uint64) error {
	observed := float64(measured) - float64(planned)
	t.mu.Lock()
	defer t.mu.Unlock()
	c := t.entry(runtimeID, architecture)
	if c.Samples == 0 {
		c.OverheadDelta = observed
	} else {
		c.OverheadDelta = (1-smoothing)*c.OverheadDelta + smoothing*observed
	}
	c.Samples++
	c.UpdatedAt = timestamppb.Now()
	return t.store.PutCalibration(ctx, runtimeID, architecture, c)
}

// Learned factor on the planned cache, one until a run of the runtime and architecture reports its cache
func (t *Table) Ratio(runtimeID, architecture string) float64 {
	if t == nil {
		return 1
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if c, ok := t.entries[Key(runtimeID, architecture)]; ok && c.GetCacheRatio() > 0 {
		return c.GetCacheRatio()
	}
	return 1
}

// Updates the cache factor from a run's allocated cache against its plan's unfactored cache
func (t *Table) RecordCache(ctx context.Context, runtimeID, architecture string, measured, planned uint64) error {
	if measured == 0 || planned == 0 {
		return fmt.Errorf("cache ratio needs a measured and a planned cache, got %d and %d", measured, planned)
	}
	observed := float64(measured) / float64(planned)
	t.mu.Lock()
	defer t.mu.Unlock()
	c := t.entry(runtimeID, architecture)
	if c.CacheSamples == 0 {
		c.CacheRatio = observed
	} else {
		c.CacheRatio = (1-smoothing)*c.CacheRatio + smoothing*observed
	}
	c.CacheSamples++
	c.UpdatedAt = timestamppb.Now()
	return t.store.PutCalibration(ctx, runtimeID, architecture, c)
}

// The entry for a runtime and architecture, made on first use, the lock held by the caller
func (t *Table) entry(runtimeID, architecture string) *v1.Calibration {
	key := Key(runtimeID, architecture)
	c, ok := t.entries[key]
	if !ok {
		c = &v1.Calibration{}
		t.entries[key] = c
	}
	return c
}
