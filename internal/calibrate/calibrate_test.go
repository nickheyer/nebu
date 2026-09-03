package calibrate

import (
	"path/filepath"
	"testing"
)

func TestRecordAndReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cal.json")
	tbl, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if tbl.Delta("rt", "arch") != 0 {
		t.Fatal("unknown should be zero")
	}
	if err := tbl.Record("rt", "arch", 1000, 800); err != nil {
		t.Fatal(err)
	}
	if d := tbl.Delta("rt", "arch"); d != 200 {
		t.Fatalf("first sample delta %v", d)
	}
	if err := tbl.Record("rt", "arch", 1000, 1000); err != nil {
		t.Fatal(err)
	}
	if d := tbl.Delta("rt", "arch"); d != 140 {
		t.Fatalf("smoothed delta %v", d)
	}
	again, err := Open(path)
	if err != nil || again.Delta("rt", "arch") != 140 || again.Snapshot().GetEntries()[Key("rt", "arch")].GetSamples() != 2 {
		t.Fatalf("reload %v %v", again.Snapshot(), err)
	}
}
