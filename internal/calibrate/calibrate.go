// Package calibrate learns estimator corrections from measured runs.
package calibrate

import (
	"errors"
	"os"
	"path/filepath"
	"sync"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const smoothing = 0.3

// Corrections keyed by runtime and architecture, persisted as JSON
type Table struct {
	path  string
	mu    sync.Mutex
	table *v1.CalibrationTable
}

// Loads the table from path when it exists
func Open(path string) (*Table, error) {
	t := &Table{path: path, table: &v1.CalibrationTable{Entries: map[string]*v1.Calibration{}}}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return t, nil
	}
	if err != nil {
		return nil, err
	}
	if err := protojson.Unmarshal(data, t.table); err != nil {
		return nil, err
	}
	if t.table.Entries == nil {
		t.table.Entries = map[string]*v1.Calibration{}
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
	if c, ok := t.table.Entries[Key(runtimeID, architecture)]; ok {
		return c.GetOverheadDelta()
	}
	return 0
}

// Folds one measured versus planned device total into the table
func (t *Table) Record(runtimeID, architecture string, measured, planned uint64) error {
	observed := float64(measured) - float64(planned)
	t.mu.Lock()
	defer t.mu.Unlock()
	key := Key(runtimeID, architecture)
	c, ok := t.table.Entries[key]
	if !ok {
		c = &v1.Calibration{}
		t.table.Entries[key] = c
	}
	if c.Samples == 0 {
		c.OverheadDelta = observed
	} else {
		c.OverheadDelta = (1-smoothing)*c.OverheadDelta + smoothing*observed
	}
	c.Samples++
	c.UpdatedAt = timestamppb.Now()
	return t.saveLocked()
}

// Returns a copy of the table
func (t *Table) Snapshot() *v1.CalibrationTable {
	t.mu.Lock()
	defer t.mu.Unlock()
	return proto.Clone(t.table).(*v1.CalibrationTable)
}

func (t *Table) saveLocked() error {
	data, err := protojson.MarshalOptions{Multiline: true, Indent: "  "}.Marshal(t.table)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(t.path), 0o755); err != nil {
		return err
	}
	tmp := t.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, t.path)
}
