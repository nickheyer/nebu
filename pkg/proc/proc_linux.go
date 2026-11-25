//go:build linux

package proc

import (
	"os"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// The kcmp type that tells whether two processes run on the same memory
const kcmpVM = 1

// Puts the child in its own group and has the kernel end it when we die
func attr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGTERM}
}

// Returns the space joined command line of pid, empty while it has none of its own
func cmdline(pid int) (string, error) {
	// A child started here runs on our memory, showing our command line, until its exec replaces it
	if self := os.Getpid(); pid != self {
		if differs, _, errno := unix.Syscall6(unix.SYS_KCMP, uintptr(self), uintptr(pid), kcmpVM, 0, 0, 0); errno == 0 && differs == 0 {
			return "", nil
		}
	}
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
	if err != nil {
		return "", err
	}
	return strings.Join(strings.Split(strings.TrimRight(string(data), "\x00"), "\x00"), " "), nil
}
