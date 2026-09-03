package tasks

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

func manager() *Manager {
	return New(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestLifecycleAndWatch(t *testing.T) {
	m := manager()
	release := make(chan struct{})
	task := m.Start("pull", "pull x", map[string]string{"repo": "x"}, func(ctx context.Context, h *Handle) error {
		h.Progress(0, 100, "starting")
		h.Logf("hello")
		<-release
		h.Add(60)
		h.Add(-10)
		h.Logf("done")
		return nil
	})
	if task.GetState() != v1.TaskState_TASK_STATE_PENDING || task.GetLabels()["repo"] != "x" {
		t.Fatalf("task %+v", task)
	}
	var events []*v1.WatchTaskResponse
	watched := make(chan error, 1)
	go func() {
		watched <- m.Watch(context.Background(), task.GetId(), func(ev *v1.WatchTaskResponse) error {
			events = append(events, ev)
			if len(events) == 2 {
				close(release)
			}
			return nil
		})
	}()
	if err := <-watched; err != nil {
		t.Fatal(err)
	}
	final := events[len(events)-1].GetTask()
	if final.GetState() != v1.TaskState_TASK_STATE_SUCCEEDED || final.GetProgress().GetDone() != 50 || final.GetFinishedAt() == nil {
		t.Fatalf("final %+v", final)
	}
	var logs []string
	for _, ev := range events {
		logs = append(logs, ev.GetLogs()...)
	}
	if len(logs) != 2 || logs[0] != "hello" || logs[1] != "done" {
		t.Fatalf("logs %v", logs)
	}
	got, tail, err := m.Get(task.GetId())
	if err != nil || got.GetState() != v1.TaskState_TASK_STATE_SUCCEEDED || len(tail) != 2 {
		t.Fatalf("get %v %v %v", got, tail, err)
	}
	if list := m.List(true); len(list) != 0 {
		t.Fatal("finished task should not be active")
	}
	if list := m.List(false); len(list) != 1 {
		t.Fatal("finished task should be listed")
	}
}

func TestFailureCancelAndPanic(t *testing.T) {
	m := manager()
	failed := m.Start("x", "fail", nil, func(ctx context.Context, h *Handle) error { return errors.New("boom") })
	waitState(t, m, failed.GetId(), v1.TaskState_TASK_STATE_FAILED)
	got, _, _ := m.Get(failed.GetId())
	if got.GetError() != "boom" {
		t.Fatalf("error %q", got.GetError())
	}
	blocked := m.Start("x", "cancel", nil, func(ctx context.Context, h *Handle) error {
		<-ctx.Done()
		return ctx.Err()
	})
	if _, err := m.Cancel(blocked.GetId()); err != nil {
		t.Fatal(err)
	}
	waitState(t, m, blocked.GetId(), v1.TaskState_TASK_STATE_CANCELED)
	panicked := m.Start("x", "panic", nil, func(ctx context.Context, h *Handle) error { panic("oops") })
	waitState(t, m, panicked.GetId(), v1.TaskState_TASK_STATE_FAILED)
	if _, _, err := m.Get("nope"); !errors.Is(err, ErrUnknownTask) {
		t.Fatal("unknown task")
	}
}

func waitState(t *testing.T, m *Manager, id string, want v1.TaskState) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		got, _, err := m.Get(id)
		if err != nil {
			t.Fatal(err)
		}
		if got.GetState() == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("task %s never reached %s", id, want)
}
