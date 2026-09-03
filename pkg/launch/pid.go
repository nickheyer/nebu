package launch

import (
	"strings"
	"syscall"
	"time"
)

const (
	pollInterval = 100 * time.Millisecond
	killWait     = 5 * time.Second
	settleWait   = 500 * time.Millisecond
)

// Reports whether pid is alive and its command line ends with command
func Running(pid int, command []string) bool {
	if pid <= 0 || len(command) == 0 || !exists(pid) {
		return false
	}
	want := strings.Join(command, " ")
	deadline := time.Now().Add(settleWait)
	for {
		line, err := cmdline(pid)
		if err != nil {
			return false
		}
		if line != "" {
			return strings.HasSuffix(line, want)
		}
		// Empty right after exec while argv is still being set up, or a zombie
		if time.Now().After(deadline) || !exists(pid) {
			return false
		}
		time.Sleep(pollInterval / 5)
	}
}

// Stops a process group this daemon did not start, escalating after grace
func Terminate(pid int, grace time.Duration) bool {
	if !exists(pid) {
		return true
	}
	signalGroup(pid, syscall.SIGTERM)
	if waitGone(pid, grace) {
		return true
	}
	signalGroup(pid, syscall.SIGKILL)
	return waitGone(pid, killWait)
}

func waitGone(pid int, limit time.Duration) bool {
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if !exists(pid) {
			return true
		}
		time.Sleep(pollInterval)
	}
	return !exists(pid)
}
