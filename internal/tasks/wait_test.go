package tasks

import (
	"context"
	"errors"
	"testing"
	"time"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

func TestWait(t *testing.T) {
	m := manager(t)
	task := m.Start("k", "slow", nil, func(ctx context.Context, h *Handle) error {
		time.Sleep(30 * time.Millisecond)
		return errors.New("nope")
	})
	final, err := m.Wait(context.Background(), task.GetId())
	if err != nil {
		t.Fatal(err)
	}
	if final.GetState() != v1.TaskState_TASK_STATE_FAILED || final.GetError() != "nope" {
		t.Fatalf("final %v", final)
	}
	if _, err := m.Wait(context.Background(), "missing"); err == nil {
		t.Fatal("unknown task should error")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	stuck := m.Start("k", "stuck", nil, func(ctx context.Context, h *Handle) error {
		<-ctx.Done()
		return ctx.Err()
	})
	if _, err := m.Wait(ctx, stuck.GetId()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("wait should respect ctx, got %v", err)
	}
	m.Cancel(stuck.GetId())
	m.Wait(context.Background(), stuck.GetId())
}
