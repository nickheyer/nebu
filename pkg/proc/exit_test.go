package proc

import (
	"context"
	"errors"
	"testing"
	"time"
)

// A wait ends on the deadline while the process lives and at once on exit or a pid gone
func TestWaitExit(t *testing.T) {
	cmd := helper("sleep")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := WaitExit(ctx, pid); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a live process outlasts the deadline, got %v", err)
	}
	go func() {
		time.Sleep(50 * time.Millisecond)
		cmd.Process.Kill()
	}()
	start := time.Now()
	ctx, cancel = context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := WaitExit(ctx, pid); err != nil {
		t.Fatalf("exit not seen: %v", err)
	}
	if waited := time.Since(start); waited > 2*time.Second {
		t.Fatalf("exit seen after %v", waited)
	}
	cmd.Wait()
	if err := WaitExit(context.Background(), pid); err != nil {
		t.Fatalf("a pid gone is gone at once: %v", err)
	}
	cancelled, stop := context.WithCancel(context.Background())
	stop()
	if err := WaitExit(cancelled, pid); err != nil {
		t.Fatalf("a pid gone wins over a cancelled ctx: %v", err)
	}
}

// Release stops a pending wake, and a ctx ending before release wakes once
func TestOnDone(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	woke := make(chan struct{}, 2)
	release := onDone(ctx, func() { woke <- struct{}{} })
	cancel()
	select {
	case <-woke:
	case <-time.After(5 * time.Second):
		t.Fatal("no wake after cancel")
	}
	release()
	ctx, cancel = context.WithCancel(context.Background())
	release = onDone(ctx, func() { woke <- struct{}{} })
	release()
	cancel()
	select {
	case <-woke:
		t.Fatal("woke after release")
	case <-time.After(50 * time.Millisecond):
	}
}
