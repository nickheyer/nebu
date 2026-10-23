package daemon

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// The file one process holds while it owns the data directory, its address inside
const lockFile = "daemon.lock"

// Returned when another process owns the data directory
var ErrLocked = errors.New("data directory in use")

// Ownership of a data directory, held until released
type dirLock struct {
	f *os.File
}

// Takes the data directory for this process, unnamed until it listens, or answers who holds it
func lockDir(dir string) (*dirLock, error) {
	path := filepath.Join(dir, lockFile)
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, err
	}
	if err := tryLock(f); err != nil {
		held, _ := os.ReadFile(path)
		f.Close()
		return nil, fmt.Errorf("%w: %s", ErrLocked, holder(dir, strings.TrimSpace(string(held))))
	}
	l := &dirLock{f: f}
	if err := l.name(""); err != nil {
		l.release()
		return nil, err
	}
	return l, nil
}

// Writes the address the holder serves, empty for a command running in process
func (l *dirLock) name(addr string) error {
	if err := l.f.Truncate(0); err != nil {
		return err
	}
	_, err := l.f.WriteAt([]byte(addr+"\n"), 0)
	return err
}

// How to reach the process holding a data directory
func holder(dir, addr string) string {
	if addr == "" {
		return fmt.Sprintf("another nebu command is running in process on %s, wait for it to finish", dir)
	}
	return fmt.Sprintf("a daemon holds %s and listens at %s, pass --addr %s or set addr in the config", dir, addr, addr)
}

// Lets the directory go
func (l *dirLock) release() error {
	if l == nil {
		return nil
	}
	return errors.Join(unlock(l.f), l.f.Close())
}
