//go:build unix && !linux

package launch

import (
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// Puts the child in its own group
func procAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}

func terminate(cmd *exec.Cmd) { signalGroup(cmd.Process.Pid, syscall.SIGTERM) }

func kill(cmd *exec.Cmd) { signalGroup(cmd.Process.Pid, syscall.SIGKILL) }

// Signals the process group, falling back to the process alone
func signalGroup(pid int, sig syscall.Signal) {
	if err := syscall.Kill(-pid, sig); err != nil {
		syscall.Kill(pid, sig)
	}
}

// Reports whether a process with pid exists
func exists(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

// Returns the space joined command line of pid through ps
func cmdline(pid int) (string, error) {
	out, err := exec.Command("ps", "-o", "args=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
