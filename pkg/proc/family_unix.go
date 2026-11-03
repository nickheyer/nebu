//go:build linux || darwin || freebsd

package proc

import (
	"errors"
	"fmt"
	"syscall"
)

// Kills every recorded descendant still alive and the root's process group, returning the descendants killed
func (f *Family) KillSurvivors() ([]int, error) {
	killed, err := f.killMembers()
	id, alive, idErr := identity(f.root)
	if idErr != nil {
		return killed, errors.Join(err, fmt.Errorf("root pid %d: %w", f.root, idErr))
	}
	// A root pid now held by another process means the group of that id is no longer ours
	if alive && id != f.rootID {
		return killed, err
	}
	if kerr := syscall.Kill(-f.root, syscall.SIGKILL); kerr != nil && !errors.Is(kerr, syscall.ESRCH) {
		err = errors.Join(err, fmt.Errorf("kill group %d: %w", f.root, kerr))
	}
	return killed, err
}

// Children always start after their parents on unix, where an orphan is reparented instead of kept
func descends(string, string) bool { return true }
