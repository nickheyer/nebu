//go:build linux

package proc

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// The start time in clock ticks from the stat file, false for a pid gone or a zombie
func identity(pid int) (string, bool, error) {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, unix.ESRCH) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	stat := string(data)
	end := strings.LastIndexByte(stat, ')')
	if end < 0 {
		return "", false, fmt.Errorf("stat of pid %d has no command field", pid)
	}
	fields := strings.Fields(stat[end+1:])
	// The state is field 3 and the start time field 22, counted from the pid as field 1
	if len(fields) < 20 {
		return "", false, fmt.Errorf("stat of pid %d holds %d fields past the command", pid, len(fields))
	}
	if fields[0] == "Z" || fields[0] == "X" {
		return "", false, nil
	}
	return fields[19], true, nil
}

// Sends SIGKILL through a pidfd once the pid shows the recorded start time, true when it was sent
func killSame(pid int, id string) (bool, error) {
	pidfd, err := unix.PidfdOpen(pid, 0)
	if errors.Is(err, unix.ESRCH) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer unix.Close(pidfd)
	// The open pidfd keeps the number from being reused, so the start time read now is the target's
	now, alive, err := identity(pid)
	if err != nil || !alive || now != id {
		return false, err
	}
	if err := unix.PidfdSendSignal(pidfd, unix.SIGKILL, nil, 0); err != nil {
		if errors.Is(err, unix.ESRCH) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
