// Package tasks runs long operations and streams their progress.
package tasks

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	historyMax     = 200
	logMax         = 2000
	notifyInterval = 100 * time.Millisecond
)

// Returned when a task id is not known
var ErrUnknownTask = errors.New("unknown task")

// Runs tasks and fans progress out to watchers
type Manager struct {
	base context.Context
	log  *slog.Logger
	mu   sync.Mutex
	byID map[string]*entry
	list []*entry
}

type entry struct {
	mu       sync.Mutex
	task     *v1.Task
	logs     []string
	dropped  int
	cancel   context.CancelFunc
	done     chan struct{}
	watchers map[chan struct{}]struct{}
	last     time.Time
	timer    *time.Timer
}

// Lets a running task report progress and logs
type Handle struct {
	e    *entry
	done atomic.Uint64
}

// Builds a manager whose tasks outlive individual requests
func New(base context.Context, log *slog.Logger) *Manager {
	return &Manager{base: base, log: log, byID: map[string]*entry{}}
}

// Starts a task in the background and returns its snapshot
func (m *Manager) Start(kind, title string, labels map[string]string, run func(ctx context.Context, h *Handle) error) *v1.Task {
	ctx, cancel := context.WithCancel(m.base)
	e := &entry{
		task: &v1.Task{
			Id:        newID(),
			Kind:      kind,
			Title:     title,
			State:     v1.TaskState_TASK_STATE_PENDING,
			Progress:  &v1.TaskProgress{},
			CreatedAt: timestamppb.Now(),
			Labels:    labels,
		},
		cancel:   cancel,
		done:     make(chan struct{}),
		watchers: map[chan struct{}]struct{}{},
	}
	m.mu.Lock()
	m.byID[e.task.Id] = e
	m.list = append(m.list, e)
	m.prune()
	m.mu.Unlock()
	h := &Handle{e: e}
	go m.execute(ctx, e, h, run)
	return e.snapshot()
}

func (m *Manager) execute(ctx context.Context, e *entry, h *Handle, run func(context.Context, *Handle) error) {
	e.update(func(t *v1.Task) {
		t.State = v1.TaskState_TASK_STATE_RUNNING
		t.StartedAt = timestamppb.Now()
	}, true)
	err := runSafely(ctx, h, run)
	e.update(func(t *v1.Task) {
		t.FinishedAt = timestamppb.Now()
		t.Progress.Done = h.done.Load()
		switch {
		case err == nil:
			t.State = v1.TaskState_TASK_STATE_SUCCEEDED
		case errors.Is(err, context.Canceled) || ctx.Err() != nil:
			t.State = v1.TaskState_TASK_STATE_CANCELED
			t.Error = err.Error()
		default:
			t.State = v1.TaskState_TASK_STATE_FAILED
			t.Error = err.Error()
		}
	}, true)
	if err != nil {
		m.log.Warn("task ended", "id", e.task.Id, "kind", e.task.Kind, "err", err)
	} else {
		m.log.Info("task ended", "id", e.task.Id, "kind", e.task.Kind)
	}
	e.cancel()
	close(e.done)
}

func runSafely(ctx context.Context, h *Handle, run func(context.Context, *Handle) error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	return run(ctx, h)
}

// Returns a task snapshot and its log tail
func (m *Manager) Get(id string) (*v1.Task, []string, error) {
	e, err := m.entry(id)
	if err != nil {
		return nil, nil, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return proto.Clone(e.task).(*v1.Task), append([]string(nil), e.logs...), nil
}

// Lists tasks newest first
func (m *Manager) List(activeOnly bool) []*v1.Task {
	m.mu.Lock()
	entries := append([]*entry(nil), m.list...)
	m.mu.Unlock()
	var out []*v1.Task
	for i := len(entries) - 1; i >= 0; i-- {
		t := entries[i].snapshot()
		if activeOnly && terminal(t.GetState()) {
			continue
		}
		out = append(out, t)
	}
	return out
}

// Requests cancellation and returns the current snapshot
func (m *Manager) Cancel(id string) (*v1.Task, error) {
	e, err := m.entry(id)
	if err != nil {
		return nil, err
	}
	e.cancel()
	return e.snapshot(), nil
}

// Sends snapshots and new logs until the task ends or ctx is done
func (m *Manager) Watch(ctx context.Context, id string, send func(*v1.WatchTaskResponse) error) error {
	e, err := m.entry(id)
	if err != nil {
		return err
	}
	notify := make(chan struct{}, 1)
	e.mu.Lock()
	e.watchers[notify] = struct{}{}
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		delete(e.watchers, notify)
		e.mu.Unlock()
	}()
	cursor := 0
	for {
		e.mu.Lock()
		task := proto.Clone(e.task).(*v1.Task)
		start := max(cursor-e.dropped, 0)
		logs := append([]string(nil), e.logs[start:]...)
		cursor = e.dropped + len(e.logs)
		e.mu.Unlock()
		if err := send(&v1.WatchTaskResponse{Task: task, Logs: logs}); err != nil {
			return err
		}
		if terminal(task.GetState()) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-notify:
		case <-e.done:
		}
	}
}

func (m *Manager) entry(id string) (*entry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.byID[id]
	if !ok {
		return nil, fmt.Errorf("%w %q", ErrUnknownTask, id)
	}
	return e, nil
}

// Drops the oldest finished tasks beyond history
func (m *Manager) prune() {
	finished := 0
	for _, e := range m.list {
		if terminal(e.snapshot().GetState()) {
			finished++
		}
	}
	for i := 0; i < len(m.list) && finished > historyMax; i++ {
		e := m.list[i]
		if terminal(e.snapshot().GetState()) {
			delete(m.byID, e.task.Id)
			m.list = append(m.list[:i], m.list[i+1:]...)
			finished--
			i--
		}
	}
}

func (e *entry) snapshot() *v1.Task {
	e.mu.Lock()
	defer e.mu.Unlock()
	return proto.Clone(e.task).(*v1.Task)
}

// Applies a mutation and notifies watchers, throttled unless forced
func (e *entry) update(fn func(*v1.Task), force bool) {
	e.mu.Lock()
	fn(e.task)
	if force || time.Since(e.last) >= notifyInterval {
		e.broadcastLocked()
	} else if e.timer == nil {
		e.timer = time.AfterFunc(notifyInterval-time.Since(e.last), func() {
			e.mu.Lock()
			e.timer = nil
			e.broadcastLocked()
			e.mu.Unlock()
		})
	}
	e.mu.Unlock()
}

func (e *entry) broadcastLocked() {
	e.last = time.Now()
	for w := range e.watchers {
		select {
		case w <- struct{}{}:
		default:
		}
	}
}

// Sets done, total, and message at once
func (h *Handle) Progress(done, total uint64, message string) {
	h.done.Store(done)
	h.e.update(func(t *v1.Task) {
		t.Progress.Done, t.Progress.Total, t.Progress.Message = done, total, message
	}, false)
}

// Adds to done, tolerating negative deltas on retries
func (h *Handle) Add(delta int64) {
	var done uint64
	if delta < 0 {
		done = h.done.Add(^uint64(-delta - 1))
	} else {
		done = h.done.Add(uint64(delta))
	}
	h.e.update(func(t *v1.Task) { t.Progress.Done = done }, false)
}

// Sets the progress message
func (h *Handle) Message(message string) {
	h.e.update(func(t *v1.Task) { t.Progress.Message = message }, false)
}

// Appends a log line and notifies watchers right away
func (h *Handle) Logf(format string, args ...any) {
	line := fmt.Sprintf(format, args...)
	h.e.mu.Lock()
	h.e.logs = append(h.e.logs, line)
	if len(h.e.logs) > logMax {
		drop := len(h.e.logs) - logMax
		h.e.logs = h.e.logs[drop:]
		h.e.dropped += drop
	}
	h.e.broadcastLocked()
	h.e.mu.Unlock()
}

func terminal(s v1.TaskState) bool {
	switch s {
	case v1.TaskState_TASK_STATE_SUCCEEDED, v1.TaskState_TASK_STATE_FAILED, v1.TaskState_TASK_STATE_CANCELED:
		return true
	}
	return false
}

func newID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// Sorts tasks newest first by creation time
func SortNewest(tasks []*v1.Task) {
	sort.SliceStable(tasks, func(i, j int) bool {
		return tasks[i].GetCreatedAt().AsTime().After(tasks[j].GetCreatedAt().AsTime())
	})
}
