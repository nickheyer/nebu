package launch

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"time"
)

const (
	logCapacity = 5000
	maxLine     = 1 << 20
)

// What to start
type Spec struct {
	Command string
	Args    []string
	Env     map[string]string
	Dir     string
}

// A started runtime process
type Process struct {
	cmd  *exec.Cmd
	log  *Log
	done chan struct{}
	once sync.Once
	err  error
}

// Starts processes
type Launcher interface {
	Launch(ctx context.Context, spec Spec) (*Process, error)
}

// Launches bare processes in their own group
type ProcessLauncher struct {
	Log *slog.Logger
}

// Starts the process and begins capturing its output
func (l *ProcessLauncher) Launch(ctx context.Context, spec Spec) (*Process, error) {
	cmd := exec.Command(spec.Command, spec.Args...)
	cmd.Dir = spec.Dir
	cmd.Env = os.Environ()
	for k, v := range spec.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.SysProcAttr = procAttr()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	p := &Process{cmd: cmd, log: NewLog(logCapacity), done: make(chan struct{})}
	var readers sync.WaitGroup
	for _, r := range []io.Reader{stdout, stderr} {
		readers.Add(1)
		go func(r io.Reader) {
			defer readers.Done()
			sc := bufio.NewScanner(r)
			sc.Buffer(make([]byte, 64<<10), maxLine)
			for sc.Scan() {
				p.log.Write(sc.Text())
			}
		}(r)
	}
	go func() {
		readers.Wait()
		p.err = cmd.Wait()
		p.log.Close()
		close(p.done)
	}()
	l.Log.Info("launched", "pid", cmd.Process.Pid, "command", spec.Command)
	return p, nil
}

// Returns the process id
func (p *Process) Pid() int { return p.cmd.Process.Pid }

// Closes when the process has exited
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

// Polls a URL until it answers 200, the process exits, or the timeout passes
func WaitHealthy(ctx context.Context, p *Process, url string, interval, timeout time.Duration) error {
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
		resp, err := client.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-p.done:
			return fmt.Errorf("process exited before becoming healthy: %v", exitDetail(p.err))
		case <-deadline:
			return fmt.Errorf("not healthy after %s", timeout)
		case <-ticker.C:
		}
	}
}

func exitDetail(err error) string {
	if err == nil {
		return "exit status 0"
	}
	return err.Error()
}
