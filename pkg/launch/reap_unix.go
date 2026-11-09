//go:build unix

package launch

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/nickheyer/nebu/pkg/proc"
)

// A process restored as a child of this daemon without an exec here, waited on by pid
type Restored struct {
	pid      int
	log      *Log
	tail     *tailer
	exited   chan struct{}
	done     chan struct{}
	once     sync.Once
	stopping atomic.Bool
	err      error
}

// Follows a child this daemon never started, reaping it with its exit status and killing its recorded workers once it is gone
func Reap(pid int, logPath string) (Handle, error) {
	log := NewLog(logCapacity)
	tail, err := newTailer(logPath, log)
	if err != nil {
		return nil, err
	}
	family, err := proc.Snapshot(pid)
	if err != nil {
		tail.close()
		return nil, err
	}
	r := &Restored{pid: pid, log: log, tail: tail, exited: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(r.exited)
		var status syscall.WaitStatus
		for {
			_, err := syscall.Wait4(pid, &status, 0, nil)
			if errors.Is(err, syscall.EINTR) {
				continue
			}
			if err != nil {
				r.err = fmt.Errorf("waiting for restored pid %d: %w", pid, err)
				break
			}
			r.err = exitError(status, r.stopping.Load())
			break
		}
		killed, err := family.KillSurvivors()
		if len(killed) > 0 {
			r.log.Write(fmt.Sprintf("killed workers %v left behind by pid %d", killed, pid))
		}
		if err != nil {
			r.log.Write(fmt.Sprintf("workers left behind by pid %d: %v", pid, err))
			r.err = errors.Join(r.err, err)
		}
	}()
	go r.tail.follow(r.exited, r.done)
	return r, nil
}

// The exit of a waited child as an error, nil for status zero or a kill this daemon asked for
func exitError(status syscall.WaitStatus, stopped bool) error {
	switch {
	case status.Exited() && status.ExitStatus() == 0:
		return nil
	case status.Exited():
		return fmt.Errorf("exit status %d", status.ExitStatus())
	case status.Signaled() && stopped:
		return nil
	case status.Signaled():
		return fmt.Errorf("signal: %s", status.Signal())
	}
	return fmt.Errorf("wait status %d", status)
}

// Returns the process id
func (r *Restored) Pid() int { return r.pid }

// Closes once the process is reaped and its output is read
func (r *Restored) Done() <-chan struct{} { return r.done }

// The exit error once Done is closed
func (r *Restored) Err() error {
	select {
	case <-r.done:
		return r.err
	default:
		return nil
	}
}

// Returns captured output
func (r *Restored) Log() *Log { return r.log }

// Reads everything written so far into the log
func (r *Restored) Sync() { r.tail.drain() }

// Signals the process and its group, kills it once grace or ctx ends, and waits for output until ctx ends
func (r *Restored) Stop(ctx context.Context, grace time.Duration) error {
	select {
	case <-r.done:
		return nil
	default:
	}
	r.stopping.Store(true)
	r.once.Do(func() { go proc.Terminate(ctx, r.pid, grace) })
	select {
	case <-r.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
