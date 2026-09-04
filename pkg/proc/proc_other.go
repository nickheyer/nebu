//go:build !unix && !windows

package proc

import (
	"errors"
	"os"
	"syscall"
)

func attr() *syscall.SysProcAttr { return nil }

func adopt(pid int) *Tree { return &Tree{pid: pid} }

func interruptTree(t *Tree) { Interrupt(t.pid) }

func killTree(t *Tree) { Kill(t.pid) }

func closeTree(t *Tree) {}

// Asks a process to stop
func Interrupt(pid int) {
	if p, err := os.FindProcess(pid); err == nil {
		p.Signal(os.Interrupt)
	}
}

// Ends a process
func Kill(pid int) {
	if p, err := os.FindProcess(pid); err == nil {
		p.Kill()
	}
}

// Reports whether a process with pid exists
func Exists(pid int) bool {
	p, err := os.FindProcess(pid)
	return err == nil && p.Signal(syscall.Signal(0)) == nil
}

// This platform keeps no readable command line
func Cmdline(pid int) (string, error) { return "", errors.ErrUnsupported }
