// Package proc starts, finds, and stops process trees the same way on every OS.
package proc

import (
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	killWait   = 5 * time.Second
	settleWait = 500 * time.Millisecond
)

// How often a watched process is checked for exit
const PollInterval = 100 * time.Millisecond

// How long a cancelled command may keep its output open before it is abandoned
const WaitDelay = 5 * time.Second

// A started process with everything it spawns, stoppable as one
type Tree struct {
	pid int
	job uintptr
}

// Returns the attributes that put a child in its own group, tied to us where the OS can
func Attr() *syscall.SysProcAttr { return attr() }

// Takes charge of a started command's tree, call right after Start
func Adopt(cmd *exec.Cmd) *Tree { return adopt(cmd.Process.Pid) }

// Starts a command built with a context as a tree of its own, a cancel ending all of it
//
// Close the tree once the command has been waited for.
func Start(cmd *exec.Cmd) (*Tree, error) {
	cmd.SysProcAttr = Attr()
	var mu sync.Mutex
	var tree *Tree
	// A cancel that lands before adoption waits for it
	cmd.Cancel = func() error {
		mu.Lock()
		defer mu.Unlock()
		tree.Kill()
		return nil
	}
	if cmd.WaitDelay == 0 {
		cmd.WaitDelay = WaitDelay
	}
	mu.Lock()
	defer mu.Unlock()
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	tree = Adopt(cmd)
	return tree, nil
}

// Runs a command built with a context to its end as a tree of its own
func Run(cmd *exec.Cmd) error {
	tree, err := Start(cmd)
	if err != nil {
		return err
	}
	defer tree.Close()
	return cmd.Wait()
}

// Pid of the tree's root
func (t *Tree) Pid() int { return t.pid }

// Asks the tree to stop
func (t *Tree) Interrupt() { interruptTree(t) }

// Ends the tree at once
func (t *Tree) Kill() { killTree(t) }

// Releases what the OS handed out, once the root has exited
func (t *Tree) Close() { closeTree(t) }

// Reports whether pid lives with a matching command line
//
// Quotes are dropped from both sides, so a path the OS quoted still matches
// the argument list it was launched with.
func Running(pid int, command []string) bool {
	if pid <= 0 || len(command) == 0 || !Exists(pid) {
		return false
	}
	want := strings.ReplaceAll(strings.Join(command, " "), `"`, "")
	deadline := time.Now().Add(settleWait)
	for {
		line, err := Cmdline(pid)
		if err != nil {
			return false
		}
		if line != "" {
			return strings.HasSuffix(strings.ReplaceAll(line, `"`, ""), want)
		}
		// Empty right after exec, or a zombie
		if time.Now().After(deadline) || !Exists(pid) {
			return false
		}
		time.Sleep(PollInterval / 5)
	}
}

// Stops a process this daemon did not start, with its tree, escalating after grace
func Terminate(pid int, grace time.Duration) bool {
	if !Exists(pid) {
		return true
	}
	Interrupt(pid)
	if waitGone(pid, grace) {
		return true
	}
	Kill(pid)
	return waitGone(pid, killWait)
}

func waitGone(pid int, limit time.Duration) bool {
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if !Exists(pid) {
			return true
		}
		time.Sleep(PollInterval)
	}
	return !Exists(pid)
}
