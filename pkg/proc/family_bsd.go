//go:build darwin || freebsd

package proc

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// The start time ps reports for a live pid in the C locale, false for a pid gone
func identity(pid int) (string, bool, error) {
	cmd := exec.Command("ps", "-o", "lstart=", "-p", strconv.Itoa(pid))
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	out, err := cmd.Output()
	line := strings.TrimSpace(string(out))
	var exit *exec.ExitError
	if errors.As(err, &exit) && line == "" {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if line == "" {
		return "", false, nil
	}
	return line, true, nil
}

// Sends SIGKILL once the pid shows the recorded start time, true when it was sent
func killSame(pid int, id string) (bool, error) {
	now, alive, err := identity(pid)
	if err != nil || !alive || now != id {
		return false, err
	}
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
