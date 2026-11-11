package host

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A use holds its own files, leaves a nested use to that use, and shares one measure with a use of the same directory
func TestStorageMeasuresEachUse(t *testing.T) {
	data := t.TempDir()
	store := filepath.Join(data, "store")
	if err := os.Mkdir(store, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(path string, n int) {
		t.Helper()
		if err := os.WriteFile(path, make([]byte, n), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(store, "blob"), 1<<20)
	write(filepath.Join(data, "db"), 1<<18)
	if err := os.Link(filepath.Join(store, "blob"), filepath.Join(store, "link")); err != nil {
		t.Skipf("hard links are not available here: %v", err)
	}
	p := New(nil, []Use{{Role: "store", Path: store}, {Role: "data", Path: data}, {Role: "cache", Path: data}}, time.Minute)
	profile, err := p.Profile(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if len(profile.GetStorage()) != 1 {
		t.Fatalf("mounts = %d, want 1", len(profile.GetStorage()))
	}
	held := profile.GetStorage()[0].GetUseBytes()
	if got := held["store"]; got < 1<<20 || got >= 2<<20 {
		t.Errorf("store holds %d, want the linked megabyte counted once", got)
	}
	if got := held["data, cache"]; got < 1<<18 || got >= 1<<20 {
		t.Errorf("data and cache hold %d, want the database without the store", got)
	}
	if len(held) != 2 {
		t.Errorf("uses = %v, want store and the shared data and cache", held)
	}
}
