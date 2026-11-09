//go:build linux

package proc

import (
	"context"
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

// One watch on a process exit through a pidfd, with an eventfd that ends the wait early
type Exit struct {
	pidfd int
	wake  int
}

// Opens a watch on the exit of pid, a pid already gone giving a watch that is already over
func Watch(pid int) (*Exit, error) {
	wake, err := unix.Eventfd(0, unix.EFD_CLOEXEC|unix.EFD_NONBLOCK)
	if err != nil {
		return nil, fmt.Errorf("watch pid %d: eventfd: %w", pid, err)
	}
	pidfd, err := unix.PidfdOpen(pid, 0)
	if errors.Is(err, unix.ESRCH) {
		return &Exit{pidfd: -1, wake: wake}, nil
	}
	if err != nil {
		unix.Close(wake)
		return nil, fmt.Errorf("watch pid %d: pidfd: %w", pid, err)
	}
	return &Exit{pidfd: pidfd, wake: wake}, nil
}

// Blocks until the process exits or ctx ends, then releases the watch, which serves one wait
func (e *Exit) Wait(ctx context.Context) error {
	defer e.close()
	if e.pidfd < 0 {
		return nil
	}
	release := onDone(ctx, func() { unix.Write(e.wake, []byte{1, 0, 0, 0, 0, 0, 0, 1}) })
	defer release()
	fds := []unix.PollFd{{Fd: int32(e.pidfd), Events: unix.POLLIN}, {Fd: int32(e.wake), Events: unix.POLLIN}}
	for {
		if _, err := unix.Poll(fds, -1); err != nil {
			if errors.Is(err, unix.EINTR) {
				continue
			}
			return fmt.Errorf("wait for exit: %w", err)
		}
		if fds[0].Revents != 0 {
			return nil
		}
		if fds[1].Revents != 0 {
			return ctx.Err()
		}
	}
}

func (e *Exit) close() {
	if e.pidfd >= 0 {
		unix.Close(e.pidfd)
	}
	unix.Close(e.wake)
}
