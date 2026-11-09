//go:build darwin || freebsd

package proc

import (
	"context"
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

// Ident of the user event that ends a wait early
const wakeIdent = 1

// One watch on a process exit through a kqueue holding the exit filter and a user event
type Exit struct {
	kq   int
	gone bool
}

// Opens a watch on the exit of pid, a pid already gone giving a watch that is already over
func Watch(pid int) (*Exit, error) {
	kq, err := unix.Kqueue()
	if err != nil {
		return nil, fmt.Errorf("watch pid %d: kqueue: %w", pid, err)
	}
	unix.CloseOnExec(kq)
	var user unix.Kevent_t
	unix.SetKevent(&user, wakeIdent, unix.EVFILT_USER, unix.EV_ADD|unix.EV_CLEAR)
	if _, err := unix.Kevent(kq, []unix.Kevent_t{user}, nil, nil); err != nil {
		unix.Close(kq)
		return nil, fmt.Errorf("watch pid %d: user event: %w", pid, err)
	}
	var exit unix.Kevent_t
	unix.SetKevent(&exit, pid, unix.EVFILT_PROC, unix.EV_ADD|unix.EV_ONESHOT)
	exit.Fflags = unix.NOTE_EXIT
	if _, err := unix.Kevent(kq, []unix.Kevent_t{exit}, nil, nil); err != nil {
		if errors.Is(err, unix.ESRCH) {
			return &Exit{kq: kq, gone: true}, nil
		}
		unix.Close(kq)
		return nil, fmt.Errorf("watch pid %d: exit filter: %w", pid, err)
	}
	return &Exit{kq: kq}, nil
}

// Blocks until the process exits or ctx ends, then releases the watch, which serves one wait
func (e *Exit) Wait(ctx context.Context) error {
	defer unix.Close(e.kq)
	if e.gone {
		return nil
	}
	release := onDone(ctx, func() {
		var trigger unix.Kevent_t
		unix.SetKevent(&trigger, wakeIdent, unix.EVFILT_USER, 0)
		trigger.Fflags = unix.NOTE_TRIGGER
		unix.Kevent(e.kq, []unix.Kevent_t{trigger}, nil, nil)
	})
	defer release()
	events := make([]unix.Kevent_t, 2)
	for {
		n, err := unix.Kevent(e.kq, nil, events, nil)
		if err != nil {
			if errors.Is(err, unix.EINTR) {
				continue
			}
			return fmt.Errorf("wait for exit: %w", err)
		}
		woken := false
		for _, ev := range events[:n] {
			switch ev.Filter {
			case unix.EVFILT_PROC:
				return nil
			case unix.EVFILT_USER:
				woken = true
			}
		}
		if woken {
			return ctx.Err()
		}
	}
}
