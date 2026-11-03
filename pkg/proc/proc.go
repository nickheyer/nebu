// Package proc starts, finds, and stops process trees the same way on every OS
package proc

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	killWait   = 5 * time.Second
	settleWait = 500 * time.Millisecond
	settleStep = 20 * time.Millisecond
)

// How long a cancelled command may keep its output open before it is abandoned
const WaitDelay = 5 * time.Second

// Returns the attributes that put a child in its own group, tied to us where the OS can
func Attr() *syscall.SysProcAttr { return attr() }

// Takes charge of a started command's tree, call right after Start
func Adopt(cmd *exec.Cmd) *Tree { return adopt(cmd.Process.Pid) }

// Starts a context bound command as its own tree, closed once waited for
func start(cmd *exec.Cmd) (*Tree, error) {
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
	tree, err := start(cmd)
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

// Interrupts the tree and kills it once grace or ctx ends first, returning when done closes or ctx ends
func (t *Tree) Terminate(ctx context.Context, grace time.Duration, done <-chan struct{}) {
	t.Interrupt()
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case <-done:
		return
	case <-timer.C:
	case <-ctx.Done():
	}
	t.Kill()
	select {
	case <-done:
	case <-ctx.Done():
	}
}

// Whether pid runs the command line given, ignoring quotes the OS adds
func Running(pid int, command []string) bool {
	if len(command) == 0 {
		return false
	}
	want := strings.ReplaceAll(strings.Join(command, " "), `"`, "")
	return RunningMatch(pid, func(line string) bool { return strings.HasSuffix(line, want) })
}

// Whether pid is alive with a command line, quotes removed, that match accepts
func RunningMatch(pid int, match func(line string) bool) bool {
	if pid <= 0 || !Exists(pid) {
		return false
	}
	deadline := time.Now().Add(settleWait)
	for {
		line, err := cmdline(pid)
		if err != nil {
			return false
		}
		if line != "" {
			return match(strings.ReplaceAll(line, `"`, ""))
		}
		// An empty line comes right after exec or from a zombie, so look again unless it exits first
		left := time.Until(deadline)
		if left <= 0 {
			return false
		}
		ctx, cancel := context.WithTimeout(context.Background(), min(left, settleStep))
		err = WaitExit(ctx, pid)
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) {
			return false
		}
	}
}

// Stops a process this daemon did not start with its tree, killing it once grace or ctx ends
func Terminate(ctx context.Context, pid int, grace time.Duration) bool {
	if !Exists(pid) {
		return true
	}
	interrupt(pid)
	graceCtx, cancel := context.WithTimeout(ctx, grace)
	err := WaitExit(graceCtx, pid)
	cancel()
	if err == nil {
		return true
	}
	kill(pid)
	killCtx, cancel := context.WithTimeout(ctx, killWait)
	defer cancel()
	return WaitExit(killCtx, pid) == nil
}

// Blocks until pid is gone or ctx ends, nil once gone and the ctx error when ctx ends first
func WaitExit(ctx context.Context, pid int) error {
	exit, err := Watch(pid)
	if err != nil {
		return err
	}
	return exit.Wait(ctx)
}

// Runs wake once ctx ends and never after the returned release has returned
func onDone(ctx context.Context, wake func()) (release func()) {
	var mu sync.Mutex
	released := false
	stop := context.AfterFunc(ctx, func() {
		mu.Lock()
		defer mu.Unlock()
		if !released {
			wake()
		}
	})
	return func() {
		stop()
		mu.Lock()
		released = true
		mu.Unlock()
	}
}
