package store

import (
	"os"
	"path/filepath"
	"testing"
)

func writeCache(t *testing.T, dir, formation, name string, n int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, formation), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, formation, name), make([]byte, n), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Tensor caches count toward the cap, the ones of formations that ended are freeable, and eviction
// collects them before any model
func TestTensorCachesCountAndCollect(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s.MaxBytes = 10
	s.CacheDir = t.TempDir()
	s.KeepCache = func(id string) bool { return id == "live" }
	writeCache(t, s.CacheDir, "live", "a", 5)
	writeCache(t, s.CacheDir, "ended", "b", 6)
	st, err := s.Status()
	if err != nil || st.GetCaches() != 2 || st.GetCacheBytes() != 11 {
		t.Fatalf("status %+v %v", st, err)
	}
	// 11 in caches plus 4 asked is 5 over the cap, and the ended cache's 6 bytes cover it
	free, err := s.Evictable(4, nil)
	if err != nil || free != 5 {
		t.Fatalf("evictable %d %v", free, err)
	}
	removed, release, err := s.Evict(4, nil)
	release()
	if err != nil || len(removed) != 0 {
		t.Fatalf("evict %v %v", removed, err)
	}
	if _, err := os.Stat(filepath.Join(s.CacheDir, "ended")); !os.IsNotExist(err) {
		t.Fatal("the ended formation's cache is collected")
	}
	if _, err := os.Stat(filepath.Join(s.CacheDir, "live", "a")); err != nil {
		t.Fatal("the live formation's cache stays")
	}
	st, _ = s.Status()
	if st.GetCaches() != 1 || st.GetCacheBytes() != 5 {
		t.Fatalf("status after collection %+v", st)
	}
	if free, err := s.Evictable(1, nil); err != nil || free != 0 {
		t.Fatalf("nothing more to free under the cap: %d %v", free, err)
	}
}

// A cache that cannot be read fails the accounting instead of counting as empty
func TestTensorCacheReadErrorsAreReported(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permissions do not bind root")
	}
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s.MaxBytes = 1
	s.CacheDir = t.TempDir()
	writeCache(t, s.CacheDir, "sealed", "a", 3)
	sealed := filepath.Join(s.CacheDir, "sealed")
	if err := os.Chmod(sealed, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(sealed, 0o755) })
	if _, err := s.Status(); err == nil {
		t.Fatal("status reports the unreadable cache")
	}
	if _, err := s.Evictable(1, nil); err == nil {
		t.Fatal("evictable reports the unreadable cache")
	}
	if _, err := s.Gc(false); err == nil {
		t.Fatal("gc reports the unreadable cache")
	}
}
