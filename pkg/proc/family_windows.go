//go:build windows

package proc

import (
	"errors"
	"fmt"

	"golang.org/x/sys/windows"
)

// Kills every recorded descendant still alive under its recorded creation time, returning the pids killed
func (f *Family) KillSurvivors() ([]int, error) { return f.killMembers() }

// The creation time of a live pid as zero padded hundred nanosecond ticks, false for a pid gone
func identity(pid int) (string, bool, error) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	defer windows.CloseHandle(h)
	return created(h)
}

// The creation time behind an open process handle, false once the process has exited
func created(h windows.Handle) (string, bool, error) {
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return "", false, err
	}
	if code != stillActive {
		return "", false, nil
	}
	var creation, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(h, &creation, &exit, &kernel, &user); err != nil {
		return "", false, err
	}
	return fmt.Sprintf("%020d", uint64(creation.HighDateTime)<<32|uint64(creation.LowDateTime)), true, nil
}

// Terminates the pid once its handle shows the recorded creation time, true when it was terminated
func killSame(pid int, id string) (bool, error) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.PROCESS_TERMINATE, false, uint32(pid))
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer windows.CloseHandle(h)
	now, alive, err := created(h)
	if err != nil || !alive || now != id {
		return false, err
	}
	if err := windows.TerminateProcess(h, 1); err != nil {
		return false, err
	}
	return true, nil
}

// A child must be created after a parent of known creation time, since Windows keeps stale parent pids
func descends(child, parent string) bool { return parent != "" && child >= parent }
