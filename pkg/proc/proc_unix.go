//go:build unix

package proc

import (
	"syscall"
)

// Nothing to claim, a signal reaches any process we may signal
func Init() {}

func adopt(pid int) *Tree { return &Tree{pid: pid} }

func interruptTree(t *Tree) { signalGroup(t.pid, syscall.SIGTERM) }

func killTree(t *Tree) { signalGroup(t.pid, syscall.SIGKILL) }

func closeTree(t *Tree) {}

// Asks a foreign process group to stop
func interrupt(pid int) { signalGroup(pid, syscall.SIGTERM) }

// Ends a foreign process group at once
func kill(pid int) { signalGroup(pid, syscall.SIGKILL) }

// Signals the process group, falling back to the process alone
func signalGroup(pid int, sig syscall.Signal) {
	if err := syscall.Kill(-pid, sig); err != nil {
		syscall.Kill(pid, sig)
	}
}

// Reports whether a process with pid exists
func Exists(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}
