package launch

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nickheyer/nebu/pkg/proc"
)

func launcher() *ProcessLauncher {
	return &ProcessLauncher{Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

func logPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "out.log")
}

func waitLines(t *testing.T, l *Log, n int) []string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if lines := l.Tail(0); len(lines) >= n {
			return lines
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("wanted %d lines, have %v", n, l.Tail(0))
	return nil
}

func TestLaunchLogsAndStop(t *testing.T) {
	path := logPath(t)
	p, err := launcher().Launch(context.Background(), Spec{Command: "sh", Args: []string{"-c", "echo out; echo err 1>&2; echo $NEBU_TEST; sleep 30"}, Env: map[string]string{"NEBU_TEST": "envval"}, LogPath: path})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(waitLines(t, p.Log(), 3), "\n")
	for _, want := range []string{"out", "err", "envval"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("log missing %q: %q", want, joined)
		}
	}
	if data, _ := os.ReadFile(path); !strings.Contains(string(data), "envval") {
		t.Fatalf("output file should hold the output: %q", data)
	}
	if p.Err() != nil {
		t.Fatal("running process should have no exit error")
	}
	start := time.Now()
	if err := p.Stop(2 * time.Second); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("stop took too long")
	}
	select {
	case <-p.Done():
	default:
		t.Fatal("done should be closed after stop")
	}
	if p.Err() == nil {
		t.Fatal("terminated process should report an exit error")
	}
	if _, err := launcher().Launch(context.Background(), Spec{Command: "true"}); err == nil {
		t.Fatal("launch without a log path must fail")
	}
}

func TestFollow(t *testing.T) {
	l := NewLog(3)
	for _, s := range []string{"a", "b", "c", "d"} {
		l.Write(s)
	}
	if tail := l.Tail(2); len(tail) != 2 || tail[0] != "c" {
		t.Fatalf("tail %v", tail)
	}
	var got []string
	done := make(chan error, 1)
	go func() {
		done <- l.Follow(context.Background(), 0, func(lines []string) error {
			got = append(got, lines...)
			return nil
		})
	}()
	time.Sleep(20 * time.Millisecond)
	l.Write("e")
	l.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, "") != "bcde" {
		t.Fatalf("followed %v", got)
	}
}

func TestWaitHealthy(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	p, _ := launcher().Launch(context.Background(), Spec{Command: "sleep", Args: []string{"30"}, LogPath: logPath(t)})
	defer p.Stop(time.Second)
	if err := WaitHealthy(context.Background(), p, srv.URL, 10*time.Millisecond, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	dead, _ := launcher().Launch(context.Background(), Spec{Command: "sh", Args: []string{"-c", "exit 3"}, LogPath: logPath(t)})
	err := WaitHealthy(context.Background(), dead, "http://127.0.0.1:1/health", 10*time.Millisecond, 5*time.Second)
	if err == nil || !strings.Contains(err.Error(), "exited") {
		t.Fatalf("expected exit error, got %v", err)
	}
	alive, _ := launcher().Launch(context.Background(), Spec{Command: "sleep", Args: []string{"30"}, LogPath: logPath(t)})
	defer alive.Stop(time.Second)
	err = WaitHealthy(context.Background(), alive, "http://127.0.0.1:1/health", 10*time.Millisecond, 50*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "not healthy") {
		t.Fatalf("expected timeout, got %v", err)
	}
}

func TestRunningAndTerminate(t *testing.T) {
	spec := Spec{Command: "sleep", Args: []string{"30"}, LogPath: logPath(t)}
	p, err := launcher().Launch(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	pid := p.Pid()
	if !proc.Running(pid, append([]string{"sleep"}, spec.Args...)) {
		t.Fatal("live process with matching command should be running")
	}
	if proc.Running(pid, []string{"sleep", "31"}) {
		t.Fatal("different command must not match")
	}
	if proc.Running(pid, nil) {
		t.Fatal("empty command must not match")
	}
	if !proc.Terminate(pid, 2*time.Second) {
		t.Fatal("terminate should report the process gone")
	}
	<-p.Done()
	if proc.Running(pid, []string{"sleep", "30"}) {
		t.Fatal("terminated process must not be running")
	}
	if !proc.Terminate(pid, time.Second) {
		t.Fatal("terminate on a gone pid is true")
	}
}

func TestAdoptIgnoringTerm(t *testing.T) {
	path := logPath(t)
	p, err := launcher().Launch(context.Background(), Spec{Command: "sh", Args: []string{"-c", `trap "" TERM; i=0; while :; do i=$((i+1)); echo tick $i; sleep 0.05; done`}, LogPath: path})
	if err != nil {
		t.Fatal(err)
	}
	waitLines(t, p.Log(), 2)
	a := Adopt(p.Pid(), path)
	if a.Pid() != p.Pid() || a.Err() != nil {
		t.Fatal("adopted handle should mirror the pid with no exit yet")
	}
	before := len(waitLines(t, a.Log(), 3))
	waitLines(t, a.Log(), before+2)
	start := time.Now()
	if err := a.Stop(300 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 6*time.Second {
		t.Fatal("stop should escalate to kill within the grace plus kill wait")
	}
	select {
	case <-a.Done():
	default:
		t.Fatal("adopted done should be closed after stop")
	}
	if a.Err() != ErrUnknownExit {
		t.Fatalf("adopted exit error %v", a.Err())
	}
	<-p.Done()
	if proc.Running(p.Pid(), []string{"sh"}) {
		t.Fatal("process should be gone")
	}
	if a.Stop(time.Second) != nil {
		t.Fatal("stop after done is a no-op")
	}
}

func TestRotateAndReadTail(t *testing.T) {
	old := LogFileMax
	LogFileMax = 1500
	defer func() { LogFileMax = old }()
	path := logPath(t)
	p, err := launcher().Launch(context.Background(), Spec{Command: "sh", Args: []string{"-c", `for i in $(seq 1 300); do echo line$i; done; sleep 0.5; echo last`}, LogPath: path})
	if err != nil {
		t.Fatal(err)
	}
	<-p.Done()
	lines := p.Log().Tail(0)
	if len(lines) != 301 || lines[0] != "line1" || lines[300] != "last" {
		t.Fatalf("ring should hold every line: %d %v", len(lines), lines[:3])
	}
	info, _ := os.Stat(path)
	if info.Size() >= 2400 {
		t.Fatalf("file should have been truncated past the cap, size %d", info.Size())
	}
	tail, err := ReadTail(path, 2)
	if err != nil || len(tail) == 0 || len(tail) > 2 || tail[len(tail)-1] != "last" {
		t.Fatalf("tail %v %v", tail, err)
	}
	if all, _ := ReadTail(path, 0); len(all) == 0 || all[len(all)-1] != "last" {
		t.Fatalf("all %v", all)
	}
	if _, err := ReadTail(filepath.Join(t.TempDir(), "missing"), 5); err == nil {
		t.Fatal("missing file should error")
	}
}
