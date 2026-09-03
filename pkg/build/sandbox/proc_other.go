//go:build !unix

package sandbox

import "os/exec"

// Kills only the step itself where process groups are unavailable
func groupAttr(cmd *exec.Cmd) {
	cmd.WaitDelay = waitDelay
}
