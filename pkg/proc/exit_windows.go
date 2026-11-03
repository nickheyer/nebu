//go:build windows

package proc

import (
	"context"
	"errors"
	"fmt"

	"golang.org/x/sys/windows"
)

// One watch on a process exit through its handle, with an event that ends the wait early
type Exit struct {
	process windows.Handle
	wake    windows.Handle
}

// Opens a watch on the exit of pid, a pid already gone giving a watch that is already over
func Watch(pid int) (*Exit, error) {
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return &Exit{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("watch pid %d: %w", pid, err)
	}
	wake, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		windows.CloseHandle(h)
		return nil, fmt.Errorf("watch pid %d: event: %w", pid, err)
	}
	return &Exit{process: h, wake: wake}, nil
}

// Blocks until the process exits or ctx ends, then releases the watch, which serves one wait
func (e *Exit) Wait(ctx context.Context) error {
	if e.process == 0 {
		return nil
	}
	defer windows.CloseHandle(e.process)
	defer windows.CloseHandle(e.wake)
	release := onDone(ctx, func() { windows.SetEvent(e.wake) })
	defer release()
	r, err := windows.WaitForMultipleObjects([]windows.Handle{e.process, e.wake}, false, windows.INFINITE)
	if err != nil {
		return fmt.Errorf("wait for exit: %w", err)
	}
	switch r {
	case windows.WAIT_OBJECT_0:
		return nil
	case windows.WAIT_OBJECT_0 + 1:
		return ctx.Err()
	}
	return fmt.Errorf("wait for exit: wait answered %d", r)
}
