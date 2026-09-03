package cache

import (
	"testing"
	"time"
)

func TestStore(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Get("k", 0); ok {
		t.Fatal("empty get")
	}
	if err := s.Put("k", []byte("v")); err != nil {
		t.Fatal(err)
	}
	if v, ok := s.Get("k", time.Hour); !ok || string(v) != "v" {
		t.Fatalf("get %q %v", v, ok)
	}
	if _, ok := s.Get("k", time.Nanosecond); ok {
		t.Fatal("expired entry returned")
	}
	if err := s.Delete("k"); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Get("k", 0); ok {
		t.Fatal("deleted entry returned")
	}
	var nilStore *Store
	if _, ok := nilStore.Get("k", 0); ok || nilStore.Put("k", nil) != nil {
		t.Fatal("nil store should be inert")
	}
}
