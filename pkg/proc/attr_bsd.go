//go:build unix && !linux

package proc

import (
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// Puts the child in its own group
func attr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}

// Returns the space joined command line of pid through ps
func Cmdline(pid int) (string, error) {
	out, err := exec.Command("ps", "-o", "args=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
