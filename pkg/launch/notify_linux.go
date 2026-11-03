//go:build linux

package launch

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

// An inotify watch on one file and an eventfd that ends it
type watcher struct {
	notes
	fd   int
	stop int
	done chan struct{}
}

// Watches path for writes, closes after writing, and truncation
func watchFile(path string) (*watcher, error) {
	fd, err := unix.InotifyInit1(unix.IN_CLOEXEC | unix.IN_NONBLOCK)
	if err != nil {
		return nil, fmt.Errorf("inotify: %w", err)
	}
	if _, err := unix.InotifyAddWatch(fd, path, unix.IN_MODIFY|unix.IN_CLOSE_WRITE|unix.IN_ATTRIB); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("inotify watch %s: %w", path, err)
	}
	stop, err := unix.Eventfd(0, unix.EFD_CLOEXEC|unix.EFD_NONBLOCK)
	if err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("eventfd: %w", err)
	}
	w := &watcher{notes: newNotes(), fd: fd, stop: stop, done: make(chan struct{})}
	go w.run()
	return w, nil
}

// Wakes once per batch of events until stopped
func (w *watcher) run() {
	defer close(w.done)
	fds := []unix.PollFd{{Fd: int32(w.fd), Events: unix.POLLIN}, {Fd: int32(w.stop), Events: unix.POLLIN}}
	buf := make([]byte, 4096)
	for {
		if _, err := unix.Poll(fds, -1); err != nil {
			if errors.Is(err, unix.EINTR) {
				continue
			}
			w.fail(fmt.Errorf("inotify poll: %w", err))
			return
		}
		if fds[1].Revents != 0 {
			return
		}
		if fds[0].Revents == 0 {
			continue
		}
		for {
			n, err := unix.Read(w.fd, buf)
			if errors.Is(err, unix.EINTR) {
				continue
			}
			if errors.Is(err, unix.EAGAIN) || err == nil && n <= 0 {
				break
			}
			if err != nil {
				w.fail(fmt.Errorf("inotify read: %w", err))
				return
			}
		}
		w.wake()
	}
}

// Stops delivery and releases the descriptors
func (w *watcher) close() {
	unix.Write(w.stop, []byte{1, 0, 0, 0, 0, 0, 0, 1})
	<-w.done
	unix.Close(w.fd)
	unix.Close(w.stop)
}
