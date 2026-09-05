package calibrate

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/nickheyer/nebu/internal/db"
)

func TestRecordAndReload(t *testing.T) {
	ctx := context.Background()
	store, err := db.Open(filepath.Join(t.TempDir(), "nebu.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	tbl, err := Open(ctx, store)
	if err != nil {
		t.Fatal(err)
	}
	if tbl.Delta("rt", "arch") != 0 {
		t.Fatal("unknown should be zero")
	}
	if err := tbl.Record(ctx, "rt", "arch", 1000, 800); err != nil {
		t.Fatal(err)
	}
	if d := tbl.Delta("rt", "arch"); d != 200 {
		t.Fatalf("first sample delta %v", d)
	}
	if err := tbl.Record(ctx, "rt", "arch", 1000, 1000); err != nil {
		t.Fatal(err)
	}
	if d := tbl.Delta("rt", "arch"); d != 140 {
		t.Fatalf("smoothed delta %v", d)
	}
	again, err := Open(ctx, store)
	rows, _ := store.ListCalibrations(ctx)
	if err != nil || again.Delta("rt", "arch") != 140 || len(rows) != 1 || rows[0].Calibration.GetSamples() != 2 {
		t.Fatalf("reload %v %v", rows, err)
	}
}
