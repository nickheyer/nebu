package launch

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func launcher() *ProcessLauncher {
	return &ProcessLauncher{Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

func TestLaunchLogsAndStop(t *testing.T) {
	p, err := launcher().Launch(context.Background(), Spec{Command: "sh", Args: []string{"-c", "echo out; echo err 1>&2; echo $NEBU_TEST; sleep 30"}, Env: map[string]string{"NEBU_TEST": "envval"}})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && len(p.Log().Tail(0)) < 3 {
		time.Sleep(20 * time.Millisecond)
	}
	joined := strings.Join(p.Log().Tail(0), "\n")
	for _, want := range []string{"out", "err", "envval"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("log missing %q: %q", want, joined)
		}
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
	p, _ := launcher().Launch(context.Background(), Spec{Command: "sleep", Args: []string{"30"}})
	defer p.Stop(time.Second)
	if err := WaitHealthy(context.Background(), p, srv.URL, 10*time.Millisecond, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	dead, _ := launcher().Launch(context.Background(), Spec{Command: "sh", Args: []string{"-c", "exit 3"}})
	err := WaitHealthy(context.Background(), dead, "http://127.0.0.1:1/health", 10*time.Millisecond, 5*time.Second)
	if err == nil || !strings.Contains(err.Error(), "exited") {
		t.Fatalf("expected exit error, got %v", err)
	}
	alive, _ := launcher().Launch(context.Background(), Spec{Command: "sleep", Args: []string{"30"}})
	defer alive.Stop(time.Second)
	err = WaitHealthy(context.Background(), alive, "http://127.0.0.1:1/health", 10*time.Millisecond, 50*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "not healthy") {
		t.Fatalf("expected timeout, got %v", err)
	}
}
