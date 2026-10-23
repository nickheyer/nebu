package daemon

import (
	"errors"
	"strings"
	"testing"
)

func TestDataDirectoryLock(t *testing.T) {
	dir := t.TempDir()
	first, err := lockDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, err = lockDir(dir)
	if !errors.Is(err, ErrLocked) || !strings.Contains(err.Error(), "running in process") {
		t.Fatalf("second holder: %v", err)
	}
	if err := first.name("127.0.0.1:8484"); err != nil {
		t.Fatal(err)
	}
	_, err = lockDir(dir)
	if !errors.Is(err, ErrLocked) || !strings.Contains(err.Error(), "pass --addr 127.0.0.1:8484") {
		t.Fatalf("named holder: %v", err)
	}
	if err := first.release(); err != nil {
		t.Fatal(err)
	}
	again, err := lockDir(dir)
	if err != nil {
		t.Fatalf("after release: %v", err)
	}
	again.release()
	var none *dirLock
	if err := none.release(); err != nil {
		t.Fatal(err)
	}
}
