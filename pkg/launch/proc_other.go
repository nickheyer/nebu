//go:build !unix

package launch

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

func procAttr() *syscall.SysProcAttr { return nil }

func terminate(cmd *exec.Cmd) { cmd.Process.Kill() }

func kill(cmd *exec.Cmd) { cmd.Process.Kill() }

func signalGroup(pid int, sig syscall.Signal) {
	if p, err := os.FindProcess(pid); err == nil {
		p.Kill()
	}
}

// Process identity cannot be checked here, so stragglers are never claimed
func exists(pid int) bool { return false }

func cmdline(pid int) (string, error) { return "", errors.New("command line lookup unsupported") }
