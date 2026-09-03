// Package calibrate learns estimator corrections from measured runs.
package calibrate

import (
	"context"
	"sync"

	"github.com/nickheyer/nebu/internal/db"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const smoothing = 0.3

// Corrections by runtime and architecture, written through to the store
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

// Returns the learned overhead delta in bytes, zero when unknown
func (t *Table) Delta(runtimeID, architecture string) float64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	if c, ok := t.entries[Key(runtimeID, architecture)]; ok {
		return c.GetOverheadDelta()
	}
	return 0
}

// Folds measured minus planned device bytes into the table
func (t *Table) Record(ctx context.Context, runtimeID, architecture string, measured, planned uint64) error {
	observed := float64(measured) - float64(planned)
	t.mu.Lock()
	defer t.mu.Unlock()
	key := Key(runtimeID, architecture)
	c, ok := t.entries[key]
	if !ok {
		c = &v1.Calibration{}
		t.entries[key] = c
	}
	if c.Samples == 0 {
		c.OverheadDelta = observed
	} else {
		c.OverheadDelta = (1-smoothing)*c.OverheadDelta + smoothing*observed
	}
	c.Samples++
	c.UpdatedAt = timestamppb.Now()
	return t.store.PutCalibration(ctx, runtimeID, architecture, c)
}

// Returns a copy of every correction
func (t *Table) Snapshot() *v1.CalibrationTable {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := &v1.CalibrationTable{Entries: map[string]*v1.Calibration{}}
	for k, c := range t.entries {
		out.Entries[k] = proto.Clone(c).(*v1.Calibration)
	}
	return out
}
