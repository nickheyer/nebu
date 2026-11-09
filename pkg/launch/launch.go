// Package launch starts runtime processes and finds them again
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
	"sync/atomic"
	"time"

	"github.com/nickheyer/nebu/pkg/proc"
)

const (
	logCapacity = 5000
	tailChunk   = 64 << 10
)

// Output file size cap before truncation, small in tests
var LogFileMax int64 = 64 << 20

// Exit of an adopted process nobody here asked to stop, its status unreadable since it is no child
var ErrUnknownExit = errors.New("exit status unknown")

// What to start and where its output goes
type Spec struct {
	Command string
	Args    []string
	Env     map[string]string
	Dir     string
	LogPath string
}

// A runtime process, launched here or found after a restart
type Handle interface {
	Pid() int
	Done() <-chan struct{}
	Err() error
	Log() *Log
	Sync()
	Stop(ctx context.Context, grace time.Duration) error
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
	tree   *proc.Tree
	log    *Log
	tail   *tailer
	exited chan struct{}
	done   chan struct{}
	once   sync.Once
	err    error
}

// Starts the process writing the log file, then follows it through file notifications
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
	log := NewLog(logCapacity)
	tail, err := newTailer(spec.LogPath, log)
	if err != nil {
		f.Close()
		return nil, err
	}
	cmd := exec.Command(spec.Command, spec.Args...)
	cmd.Dir = spec.Dir
	cmd.Env = proc.Environ()
	for k, v := range spec.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.SysProcAttr = proc.Attr()
	cmd.Stdout, cmd.Stderr = f, f
	if err := cmd.Start(); err != nil {
		f.Close()
		tail.close()
		return nil, err
	}
	f.Close()
	p := &Process{cmd: cmd, tree: proc.Adopt(cmd), log: log, tail: tail, exited: make(chan struct{}), done: make(chan struct{})}
	go func() {
		p.err = cmd.Wait()
		// Kills what the root left running in its group
		p.tree.Kill()
		p.tree.Close()
		close(p.exited)
	}()
	go p.tail.follow(p.exited, p.done)
	l.Log.Info("launched", "pid", cmd.Process.Pid, "command", spec.Command)
	return p, nil
}

// Returns the process id
func (p *Process) Pid() int { return p.cmd.Process.Pid }

// Closes once the process exited and its output is read
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

// Reads everything written so far into the log
func (p *Process) Sync() { p.tail.drain() }

// Interrupts the tree, kills it once grace or ctx ends, and waits for output until ctx ends
func (p *Process) Stop(ctx context.Context, grace time.Duration) error {
	select {
	case <-p.done:
		return nil
	default:
	}
	p.once.Do(func() { go p.tree.Terminate(ctx, grace, p.done) })
	select {
	case <-p.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// A process found alive after a restart, supervised by pid
type Adopted struct {
	pid    int
	log    *Log
	tail   *tailer
	exited chan struct{}
	done   chan struct{}
	once   sync.Once
	// Whether this daemon asked the process to stop, its exit then expected
	stopping atomic.Bool
	err      error
}

// Follows a live process this daemon did not start, killing its recorded workers once it is gone
func Adopt(pid int, logPath string) (Handle, error) {
	log := NewLog(logCapacity)
	tail, err := newTailer(logPath, log)
	if err != nil {
		return nil, err
	}
	if err := tail.drain(); err != nil {
		tail.close()
		return nil, err
	}
	family, err := proc.Snapshot(pid)
	if err != nil {
		tail.close()
		return nil, err
	}
	exit, err := proc.Watch(pid)
	if err != nil {
		tail.close()
		return nil, err
	}
	a := &Adopted{pid: pid, log: log, tail: tail, exited: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(a.exited)
		if err := exit.Wait(context.Background()); err != nil {
			a.err = errors.Join(ErrUnknownExit, fmt.Errorf("watching pid %d: %w", pid, err))
			return
		}
		if !a.stopping.Load() {
			a.err = ErrUnknownExit
		}
		killed, err := family.KillSurvivors()
		if len(killed) > 0 {
			a.log.Write(fmt.Sprintf("killed workers %v left behind by pid %d", killed, pid))
		}
		if err != nil {
			a.log.Write(fmt.Sprintf("workers left behind by pid %d: %v", pid, err))
			a.err = errors.Join(a.err, err)
		}
	}()
	go a.tail.follow(a.exited, a.done)
	return a, nil
}

// Returns the process id
func (a *Adopted) Pid() int { return a.pid }

// Closes once the process is gone and output is read
func (a *Adopted) Done() <-chan struct{} { return a.done }

// Nil once done after a stop asked here, else an unknown exit since non children cannot be waited, joined with any cleanup failure
func (a *Adopted) Err() error {
	select {
	case <-a.done:
		return a.err
	default:
		return nil
	}
}

// Returns captured output
func (a *Adopted) Log() *Log { return a.log }

// Reads everything written so far into the log
func (a *Adopted) Sync() { a.tail.drain() }

// Signals the group, kills it once grace or ctx ends, and waits for output until ctx ends
func (a *Adopted) Stop(ctx context.Context, grace time.Duration) error {
	select {
	case <-a.done:
		return nil
	default:
	}
	a.stopping.Store(true)
	a.once.Do(func() { go proc.Terminate(ctx, a.pid, grace) })
	select {
	case <-a.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Follows one output file into a log ring, woken by notifications of the file changing
type tailer struct {
	mu       sync.Mutex
	path     string
	log      *Log
	f        *os.File
	offset   int64
	lines    *lineStream
	buf      []byte
	watch    *watcher
	rotation error
}

// Opens the file and its change watch before anything is written to it
func newTailer(path string, log *Log) (*tailer, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	w, err := watchFile(path)
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("watch %s: %w", path, err)
	}
	return &tailer{path: path, log: log, f: f, lines: newLineStream(), buf: make([]byte, tailChunk), watch: w}, nil
}

// Reads everything appended since the last call into the log
func (t *tailer) drain() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	for {
		n, err := t.f.Read(t.buf)
		if n > 0 {
			t.offset += int64(n)
			t.lines.feed(t.buf[:n], t.log.Write)
			continue
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		return nil
	}
}

// Drains on every notification until the process is gone, then drains once more and ends the log
func (t *tailer) follow(exited <-chan struct{}, done chan struct{}) {
	defer close(done)
	defer t.log.Close()
	defer t.close()
	changed, failed := t.watch.changed, t.watch.failed
	stop := func(err error) {
		t.log.Write("log follow stopped: " + err.Error())
		changed, failed = nil, nil
	}
	if err := t.step(); err != nil {
		stop(err)
	}
	for {
		select {
		case <-exited:
			if err := t.drain(); err != nil {
				t.log.Write("log read failed: " + err.Error())
			}
			t.mu.Lock()
			t.lines.flush(t.log.Write)
			t.mu.Unlock()
			return
		case <-changed:
			if err := t.step(); err != nil {
				stop(err)
			}
		case err := <-failed:
			stop(err)
		}
	}
}

// Reads what was appended, then truncates the file past the cap
func (t *tailer) step() error {
	if err := t.drain(); err != nil {
		return err
	}
	t.rotate()
	return nil
}

// Truncates the file past the cap once it holds nothing unread, the ring staying current
func (t *tailer) rotate() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.offset <= LogFileMax || t.rotation != nil {
		return
	}
	info, err := t.f.Stat()
	if err == nil && info.Size() != t.offset {
		return
	}
	if err == nil {
		err = os.Truncate(t.path, 0)
	}
	if err == nil {
		_, err = t.f.Seek(0, io.SeekStart)
	}
	if err != nil {
		t.rotation = err
		t.log.Write("log rotation stopped: " + err.Error())
		return
	}
	t.offset = 0
}

// Releases the file and its watch
func (t *tailer) close() {
	t.watch.close()
	t.f.Close()
}

// Returns the last lines of a file, all when zero
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
		if i := bytes.IndexAny(data, "\r\n"); i >= 0 {
			data = data[i+1:]
		}
	}
	var out []string
	lines := newLineStream()
	collect := func(line string) { out = append(out, Clean(line)) }
	lines.feed(data, collect)
	lines.flush(collect)
	if n > 0 && len(out) > n {
		out = out[len(out)-n:]
	}
	return out, nil
}

// Polls a URL until 200 or exit, reporting each slowAfter waited and never giving up on a live process
func WaitHealthy(ctx context.Context, h Handle, url string, interval, slowAfter time.Duration, slow func(waited time.Duration)) error {
	if interval <= 0 {
		interval = time.Second
	}
	if slowAfter <= 0 {
		slowAfter = 10 * time.Minute
	}
	client := &http.Client{Timeout: interval * 2}
	start := time.Now()
	warn := time.NewTicker(slowAfter)
	defer warn.Stop()
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
		case <-warn.C:
			slow(time.Since(start).Round(time.Second))
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
