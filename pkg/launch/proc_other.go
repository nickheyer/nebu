//go:build !unix

package launch

import (
	"os/exec"
	"syscall"
)

func procAttr() *syscall.SysProcAttr { return nil }

func terminate(cmd *exec.Cmd) { cmd.Process.Kill() }

func kill(cmd *exec.Cmd) { cmd.Process.Kill() }
