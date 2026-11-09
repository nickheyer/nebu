//go:build linux || darwin || freebsd

package proc

import (
	"context"
	"os/signal"
	"syscall"
	"testing"
	"time"
)

func init() {
	helpers["stubborn"] = func() {
		signal.Ignore(syscall.SIGTERM)
		time.Sleep(time.Minute)
	}
}

// A deadline shorter than the grace ends the grace, kills the tree, and returns without waiting it out
func TestTerminateHonorsTheDeadline(t *testing.T) {
	cmd := helper("stubborn")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go cmd.Wait()
	pid := cmd.Process.Pid
	time.Sleep(200 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	Terminate(ctx, pid, time.Minute)
	if waited := time.Since(start); waited > 2*time.Second {
		t.Fatalf("terminate waited %v past a 200ms deadline", waited)
	}
	gone, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	if err := WaitExit(gone, pid); err != nil {
		t.Fatalf("the stubborn process outlived the kill: %v", err)
	}
	if !Terminate(context.Background(), pid, time.Second) {
		t.Fatal("a pid gone terminates at once")
	}
}

// A process that leaves on the interrupt is gone within the grace
func TestTerminateInterrupts(t *testing.T) {
	cmd := helper("sleep")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go cmd.Wait()
	start := time.Now()
	if !Terminate(context.Background(), cmd.Process.Pid, time.Minute) {
		t.Fatal("an interrupted process is gone")
	}
	if waited := time.Since(start); waited > 5*time.Second {
		t.Fatalf("interrupt took %v", waited)
	}
}
