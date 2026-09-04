//go:build linux

package proc

import (
	"os"
	"strconv"
	"strings"
	"syscall"
)

// Puts the child in its own group and has the kernel end it when we die
func attr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGTERM}
}

// Returns the space joined command line of pid
func Cmdline(pid int) (string, error) {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
	if err != nil {
		return "", err
	}
	return strings.Join(strings.Split(strings.TrimRight(string(data), "\x00"), "\x00"), " "), nil
}
