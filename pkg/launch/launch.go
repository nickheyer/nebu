// Package launch starts runtime processes, follows their output, and finds them again.
package launch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

const (
	logCapacity  = 5000
	maxLine      = 1 << 20
	tailInterval = 100 * time.Millisecond
	tailChunk    = 64 << 10
)

// Bytes an output file may reach before it is truncated, kept small in tests
var LogFileMax int64 = 64 << 20

// Returned as the exit error of a process this daemon did not wait on
var ErrUnknownExit = errors.New("exit status unknown")

// What to start and where its output goes
type Spec struct {
	Command string
	Args    []string
	Env     map[string]string
	Dir     string
	LogPath string
}

// A runtime process, launched here or found alive after a restart
type Handle interface {
	Pid() int
	Done() <-chan struct{}
	Err() error
	Log() *Log
	Sync()
	Stop(grace time.Duration) error
}

// Starts processes
type Launcher interface {
	Launch(ctx context.Context, spec Spec) (Handle, error)
}

// Launches bare processes in their own group
type ProcessLauncher struct {
	Log *slog.Logger
}

// A process started by this daemon
type Process struct {
	cmd    *exec.Cmd
	log    *Log
	tail   *tailer
	exited chan struct{}
	done   chan struct{}
	once   sync.Once
	err    error
}

// Starts the process with its output appended to the log file, and follows that file
func (l *ProcessLauncher) Launch(ctx context.Context, spec Spec) (Handle, error) {
	if spec.LogPath == "" {
		return nil, errors.New("launch: log path required")
	}
	if err := os.MkdirAll(filepath.Dir(spec.LogPath), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(spec.LogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(spec.Command, spec.Args...)
	cmd.Dir = spec.Dir
	cmd.Env = os.Environ()
	for k, v := range spec.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.SysProcAttr = procAttr()
	cmd.Stdout, cmd.Stderr = f, f
	if err := cmd.Start(); err != nil {
		f.Close()
		return nil, err
	}
	f.Close()
	p := &Process{cmd: cmd, log: NewLog(logCapacity), exited: make(chan struct{}), done: make(chan struct{})}
	p.tail = newTailer(spec.LogPath, p.log)
	go func() {
		p.err = cmd.Wait()
		close(p.exited)
	}()
	go p.tail.follow(p.exited, p.done)
	l.Log.Info("launched", "pid", cmd.Process.Pid, "command", spec.Command)
	return p, nil
}

// Returns the process id
func (p *Process) Pid() int { return p.cmd.Process.Pid }

// Closes when the process has exited and its output is fully read
func (p *Process) Done() <-chan struct{} { return p.done }

// Returns the exit error once Done is closed
func (p *Process) Err() error {
	select {
	case <-p.done:
		return p.err
	default:
		return nil
	}
}

// Returns captured output
func (p *Process) Log() *Log { return p.log }

// Reads everything the process has written so far into the log
func (p *Process) Sync() { p.tail.drain() }

// Terminates gracefully, then forcefully after grace
func (p *Process) Stop(grace time.Duration) error {
	select {
	case <-p.done:
		return nil
	default:
	}
	p.once.Do(func() { terminate(p.cmd) })
	select {
	case <-p.done:
		return nil
	case <-time.After(grace):
	}
	kill(p.cmd)
	<-p.done
	return nil
}

// A process found alive after a daemon restart, supervised by pid
type Adopted struct {
	pid    int
	log    *Log
	tail   *tailer
	exited chan struct{}
	done   chan struct{}
	once   sync.Once
}

// Follows a live process this daemon did not start, reading the output file it still writes
func Adopt(pid int, logPath string) Handle {
	a := &Adopted{pid: pid, log: NewLog(logCapacity), exited: make(chan struct{}), done: make(chan struct{})}
	go func() {
		for exists(pid) {
			time.Sleep(pollInterval)
		}
		close(a.exited)
	}()
	a.tail = newTailer(logPath, a.log)
	a.tail.drain()
	go a.tail.follow(a.exited, a.done)
	return a
}

// Returns the process id
func (a *Adopted) Pid() int { return a.pid }

// Closes when the process is gone and its output is fully read
func (a *Adopted) Done() <-chan struct{} { return a.done }

// Reports an unknown exit once Done is closed, since a non child cannot be waited on
func (a *Adopted) Err() error {
	select {
	case <-a.done:
		return ErrUnknownExit
	default:
		return nil
	}
}

// Returns captured output
func (a *Adopted) Log() *Log { return a.log }

// Reads everything the process has written so far into the log
func (a *Adopted) Sync() { a.tail.drain() }

// Signals the group, escalating after grace, then waits for the output to drain
func (a *Adopted) Stop(grace time.Duration) error {
	select {
	case <-a.done:
		return nil
	default:
	}
	a.once.Do(func() { Terminate(a.pid, grace) })
	<-a.done
	return nil
}

// Follows one output file into a log ring
type tailer struct {
	mu      sync.Mutex
	path    string
	log     *Log
	f       *os.File
	offset  int64
	partial []byte
	buf     []byte
}

func newTailer(path string, log *Log) *tailer {
	t := &tailer{path: path, log: log, buf: make([]byte, tailChunk)}
	t.f, _ = os.Open(path)
	return t
}

// Reads everything appended since the last call into the log
func (t *tailer) drain() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.f == nil {
		return nil
	}
	for {
		n, err := t.f.Read(t.buf)
		if n > 0 {
			t.offset += int64(n)
			data := append(t.partial, t.buf[:n]...)
			for {
				i := bytes.IndexByte(data, '\n')
				if i < 0 {
					break
				}
				t.log.Write(string(bytes.TrimRight(data[:i], "\r")))
				data = data[i+1:]
			}
			if len(data) > maxLine {
				t.log.Write(string(data))
				data = nil
			}
			t.partial = append([]byte(nil), data...)
			continue
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		return nil
	}
}

// Keeps draining until the process is gone, truncating the file past the cap, then closes the log
func (t *tailer) follow(exited <-chan struct{}, done chan struct{}) {
	defer close(done)
	defer t.log.Close()
	if t.f == nil {
		<-exited
		return
	}
	defer t.f.Close()
	for {
		if err := t.drain(); err != nil {
			return
		}
		select {
		case <-exited:
			t.drain()
			t.mu.Lock()
			if len(t.partial) > 0 {
				t.log.Write(string(t.partial))
			}
			t.mu.Unlock()
			return
		default:
		}
		t.rotate()
		time.Sleep(tailInterval)
	}
}

// Truncates the file once it passes the cap, keeping the ring as the recent view
func (t *tailer) rotate() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.offset > LogFileMax {
		if err := os.Truncate(t.path, 0); err == nil {
			t.f.Seek(0, io.SeekStart)
			t.offset = 0
		}
	}
}

// Returns the last lines of an output file, all retained when n is zero
func ReadTail(path string, n int) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	const window = 4 << 20
	start := int64(0)
	if info.Size() > window {
		start = info.Size() - window
	}
	data, err := io.ReadAll(io.NewSectionReader(f, start, info.Size()-start))
	if err != nil {
		return nil, err
	}
	if start > 0 {
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			data = data[i+1:]
		}
	}
	data = bytes.TrimRight(data, "\n")
	if len(data) == 0 {
		return nil, nil
	}
	lines := bytes.Split(data, []byte("\n"))
	if n > 0 && len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = string(bytes.TrimRight(l, "\r"))
	}
	return out, nil
}

// Polls a URL until it answers 200, the process exits, or the timeout passes
func WaitHealthy(ctx context.Context, h Handle, url string, interval, timeout time.Duration) error {
	if interval <= 0 {
		interval = time.Second
	}
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	client := &http.Client{Timeout: interval * 2}
	deadline := time.After(timeout)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if Healthy(client, url) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-h.Done():
			return fmt.Errorf("process exited before becoming healthy: %v", exitDetail(h.Err()))
		case <-deadline:
			return fmt.Errorf("not healthy after %s", timeout)
		case <-ticker.C:
		}
	}
}

// Reports whether url answers 200 right now
func Healthy(client *http.Client, url string) bool {
	resp, err := client.Get(url)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func exitDetail(err error) string {
	if err == nil {
		return "exit status 0"
	}
	return err.Error()
}
