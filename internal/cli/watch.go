package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/nickheyer/nebu/pkg/estimate"
	"github.com/nickheyer/nebu/pkg/eval"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

const (
	plainInterval = 2 * time.Second
	lineWidth     = 100
)

// Follows a task to completion, rendering progress and logs
func (e *env) watch(ctx context.Context, cl *clients, id string) (*v1.Task, error) {
	stream, err := cl.tasks.WatchTask(ctx, connect.NewRequest(&v1.WatchTaskRequest{Id: id}))
	if err != nil {
		return nil, err
	}
	defer stream.Close()
	tty := isTerminal(e.out)
	r := &renderer{out: e.out, tty: tty}
	var last *v1.Task
	for stream.Receive() {
		msg := stream.Msg()
		for _, line := range msg.GetLogs() {
			r.log(line)
		}
		last = msg.GetTask()
		r.progress(last)
	}
	r.finish()
	if err := stream.Err(); err != nil {
		return last, err
	}
	if last == nil {
		return nil, fmt.Errorf("task %s ended without a snapshot", id)
	}
	switch last.GetState() {
	case v1.TaskState_TASK_STATE_FAILED:
		return last, fmt.Errorf("%s failed: %s", last.GetTitle(), last.GetError())
	case v1.TaskState_TASK_STATE_CANCELED:
		return last, fmt.Errorf("%s canceled", last.GetTitle())
	}
	return last, nil
}

// Draws a single updating status line on terminals
type renderer struct {
	out      io.Writer
	tty      bool
	lastDone uint64
	lastAt   time.Time
	rate     float64
	lastLine time.Time
	dirty    bool
}

func (r *renderer) log(line string) {
	r.clear()
	fmt.Fprintln(r.out, line)
}

func (r *renderer) progress(t *v1.Task) {
	now := time.Now()
	p := t.GetProgress()
	if !r.lastAt.IsZero() && p.GetDone() >= r.lastDone {
		elapsed := now.Sub(r.lastAt).Seconds()
		if elapsed > 0 {
			instant := float64(p.GetDone()-r.lastDone) / elapsed
			if r.rate == 0 {
				r.rate = instant
			} else {
				r.rate = 0.7*r.rate + 0.3*instant
			}
		}
	}
	r.lastDone, r.lastAt = p.GetDone(), now
	line := status(t, r.rate)
	if r.tty {
		fmt.Fprintf(r.out, "\r%-*s", lineWidth, truncate(line, lineWidth))
		r.dirty = true
		return
	}
	if terminalState(t.GetState()) || now.Sub(r.lastLine) >= plainInterval {
		fmt.Fprintln(r.out, line)
		r.lastLine = now
	}
}

func (r *renderer) clear() {
	if r.tty && r.dirty {
		fmt.Fprintf(r.out, "\r%-*s\r", lineWidth, "")
		r.dirty = false
	}
}

func (r *renderer) finish() {
	if r.tty && r.dirty {
		fmt.Fprintln(r.out)
		r.dirty = false
	}
}

func status(t *v1.Task, rate float64) string {
	p := t.GetProgress()
	parts := []string{t.GetTitle(), strings.ToUpper(eval.EnumShort(t.GetState()))}
	if p.GetTotal() > 0 {
		pct := float64(p.GetDone()) * 100 / float64(p.GetTotal())
		if p.GetTotal() < 1000 {
			parts = append(parts, fmt.Sprintf("%d/%d", p.GetDone(), p.GetTotal()))
		} else {
			parts = append(parts, fmt.Sprintf("%s/%s %.0f%%", estimate.Human(p.GetDone()), estimate.Human(p.GetTotal()), pct))
		}
	}
	if rate > 0 && !terminalState(t.GetState()) {
		parts = append(parts, estimate.Human(uint64(rate))+"/s")
	}
	if p.GetMessage() != "" {
		parts = append(parts, p.GetMessage())
	}
	return strings.Join(parts, "  ")
}

func terminalState(s v1.TaskState) bool {
	switch s {
	case v1.TaskState_TASK_STATE_SUCCEEDED, v1.TaskState_TASK_STATE_FAILED, v1.TaskState_TASK_STATE_CANCELED:
		return true
	}
	return false
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "..."
}

func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}
