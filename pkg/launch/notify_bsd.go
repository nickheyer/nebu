//go:build darwin || freebsd

package launch

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

// Ident of the user event that ends a watch
const stopIdent = 1

// A kqueue vnode watch on one file and a user event that ends it
type watcher struct {
	notes
	kq   int
	file int
	done chan struct{}
}

// Watches path for writes, extension, and attribute changes such as truncation
func watchFile(path string) (*watcher, error) {
	file, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	kq, err := unix.Kqueue()
	if err != nil {
		unix.Close(file)
		return nil, fmt.Errorf("kqueue: %w", err)
	}
	unix.CloseOnExec(kq)
	var vnode, user unix.Kevent_t
	unix.SetKevent(&vnode, file, unix.EVFILT_VNODE, unix.EV_ADD|unix.EV_CLEAR)
	vnode.Fflags = unix.NOTE_WRITE | unix.NOTE_EXTEND | unix.NOTE_ATTRIB
	unix.SetKevent(&user, stopIdent, unix.EVFILT_USER, unix.EV_ADD|unix.EV_CLEAR)
	if _, err := unix.Kevent(kq, []unix.Kevent_t{vnode, user}, nil, nil); err != nil {
		unix.Close(kq)
		unix.Close(file)
		return nil, fmt.Errorf("kqueue watch %s: %w", path, err)
	}
	w := &watcher{notes: newNotes(), kq: kq, file: file, done: make(chan struct{})}
	go w.run()
	return w, nil
}

// Wakes once per batch of vnode events until stopped
func (w *watcher) run() {
	defer close(w.done)
	events := make([]unix.Kevent_t, 8)
	for {
		n, err := unix.Kevent(w.kq, nil, events, nil)
		if err != nil {
			if errors.Is(err, unix.EINTR) {
				continue
			}
			w.fail(fmt.Errorf("kqueue wait: %w", err))
			return
		}
		changed := false
		for _, ev := range events[:n] {
			switch ev.Filter {
			case unix.EVFILT_USER:
				return
			case unix.EVFILT_VNODE:
				changed = true
			}
		}
		if changed {
			w.wake()
		}
	}
}

// Stops delivery and releases the descriptors
func (w *watcher) close() {
	var trigger unix.Kevent_t
	unix.SetKevent(&trigger, stopIdent, unix.EVFILT_USER, 0)
	trigger.Fflags = unix.NOTE_TRIGGER
	unix.Kevent(w.kq, []unix.Kevent_t{trigger}, nil, nil)
	<-w.done
	unix.Close(w.kq)
	unix.Close(w.file)
}
