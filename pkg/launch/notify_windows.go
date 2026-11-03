//go:build windows

package launch

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Directory changes that mark a file written or resized
const changeMask = windows.FILE_NOTIFY_CHANGE_SIZE | windows.FILE_NOTIFY_CHANGE_LAST_WRITE | windows.FILE_NOTIFY_CHANGE_FILE_NAME

// A ReadDirectoryChangesW watch on a file's directory, filtered to the file's name
type watcher struct {
	notes
	dir   windows.Handle
	event windows.Handle
	stop  windows.Handle
	name  string
	done  chan struct{}
}

// Watches the directory holding path for changes to the file's size and write time
func watchFile(path string) (*watcher, error) {
	dirPath, err := windows.UTF16PtrFromString(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	dir, err := windows.CreateFile(dirPath, windows.FILE_LIST_DIRECTORY, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OVERLAPPED, 0)
	if err != nil {
		return nil, fmt.Errorf("open directory of %s: %w", path, err)
	}
	event, err := windows.CreateEvent(nil, 0, 0, nil)
	if err != nil {
		windows.CloseHandle(dir)
		return nil, fmt.Errorf("event: %w", err)
	}
	stop, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		windows.CloseHandle(event)
		windows.CloseHandle(dir)
		return nil, fmt.Errorf("event: %w", err)
	}
	w := &watcher{notes: newNotes(), dir: dir, event: event, stop: stop, name: filepath.Base(path), done: make(chan struct{})}
	go w.run()
	return w, nil
}

// Issues one overlapped read at a time and wakes when a batch names the file or overflowed
func (w *watcher) run() {
	defer close(w.done)
	buf := make([]byte, 64<<10)
	for {
		ov := windows.Overlapped{HEvent: w.event}
		if err := windows.ReadDirectoryChanges(w.dir, &buf[0], uint32(len(buf)), false, changeMask, nil, &ov, 0); err != nil && !errors.Is(err, windows.ERROR_IO_PENDING) {
			w.fail(fmt.Errorf("read directory changes: %w", err))
			return
		}
		r, err := windows.WaitForMultipleObjects([]windows.Handle{w.event, w.stop}, false, windows.INFINITE)
		if err != nil {
			w.fail(fmt.Errorf("wait for directory changes: %w", err))
			return
		}
		var n uint32
		if r == windows.WAIT_OBJECT_0+1 {
			windows.CancelIoEx(w.dir, &ov)
			windows.GetOverlappedResult(w.dir, &ov, &n, true)
			return
		}
		if err := windows.GetOverlappedResult(w.dir, &ov, &n, false); err != nil {
			w.fail(fmt.Errorf("directory changes result: %w", err))
			return
		}
		// A zero count means the batch overflowed the buffer, so the file may have changed unseen
		if n == 0 || w.names(buf[:n]) {
			w.wake()
		}
	}
}

// Whether any record of a change batch names the watched file
func (w *watcher) names(batch []byte) bool {
	const header = int(unsafe.Offsetof(windows.FileNotifyInformation{}.FileName))
	for off := 0; off+header <= len(batch); {
		info := (*windows.FileNotifyInformation)(unsafe.Pointer(&batch[off]))
		chars := int(info.FileNameLength) / 2
		if off+header+chars*2 > len(batch) {
			return true
		}
		name := windows.UTF16ToString(unsafe.Slice(&info.FileName, chars))
		if strings.EqualFold(name, w.name) {
			return true
		}
		if info.NextEntryOffset == 0 {
			return false
		}
		off += int(info.NextEntryOffset)
	}
	return false
}

// Stops delivery and releases the handles
func (w *watcher) close() {
	windows.SetEvent(w.stop)
	<-w.done
	windows.CloseHandle(w.stop)
	windows.CloseHandle(w.event)
	windows.CloseHandle(w.dir)
}
