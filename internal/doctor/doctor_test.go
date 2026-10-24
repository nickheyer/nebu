package doctor

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nickheyer/nebu/internal/db"
	"github.com/nickheyer/nebu/internal/installs"
	"github.com/nickheyer/nebu/internal/tasks"
	"github.com/nickheyer/nebu/pkg/build"
	"github.com/nickheyer/nebu/pkg/events"
	"github.com/nickheyer/nebu/pkg/host"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/runtimes"
	"github.com/nickheyer/nebu/pkg/sources"
	"github.com/nickheyer/nebu/pkg/store"
)

// A host probe that reports each run and holds it until the test lets it finish
type gate struct{ started, release chan struct{} }

func (gate) ID() string               { return "gate" }
func (gate) Description() string      { return "a probe the test holds open" }
func (gate) Runs(string, string) bool { return true }
func (g gate) Run(ctx context.Context) host.Result {
	g.started <- struct{}{}
	select {
	case <-g.release:
	case <-ctx.Done():
	}
	return host.Result{Status: v1.ProbeStatus_PROBE_STATUS_OK}
}

func newDoctor(t *testing.T, ctx context.Context, probe host.Probe) *Doctor {
	t.Helper()
	dir := t.TempDir()
	database, err := db.Open(filepath.Join(dir, "nebu.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	blobs, err := store.Open(filepath.Join(dir, "store"))
	if err != nil {
		t.Fatal(err)
	}
	rts, err := runtimes.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	recipes, err := build.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	prober := host.New([]host.Probe{probe}, nil, time.Minute)
	return &Doctor{Host: prober, Runtimes: rts, Sources: sources.NewRegistry(), Store: blobs, Installs: &installs.Manager{DB: database, Host: prober, Recipes: recipes}, Tasks: tasks.New(ctx, log, database, events.New())}
}

// Probe tasks the doctor started, oldest first
func probeTasks(d *Doctor) []*v1.Task {
	var out []*v1.Task
	for _, t := range d.Tasks.List(false) {
		if t.GetKind() == Kind {
			out = append(out, t)
		}
	}
	return out
}

// Waits until every probe task has ended
func settled(t *testing.T, d *Doctor) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		ended := true
		for _, task := range probeTasks(d) {
			ended = ended && terminal(task.GetState())
		}
		if ended {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("probe tasks never ended")
}

func terminal(s v1.TaskState) bool {
	return s == v1.TaskState_TASK_STATE_SUCCEEDED || s == v1.TaskState_TASK_STATE_FAILED || s == v1.TaskState_TASK_STATE_CANCELED
}

// A forced probe starts at once when none runs, a start while one runs answers with it, and probes
// forced while one runs start exactly one more once it ends
func TestForceQueuesOneProbe(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	g := gate{started: make(chan struct{}, 4), release: make(chan struct{})}
	d := newDoctor(t, ctx, g)
	d.Force()
	<-g.started
	first := probeTasks(d)
	if len(first) != 1 {
		t.Fatalf("%d probe tasks after the first force", len(first))
	}
	if running, err := d.Start(ctx); err != nil || running.GetId() != first[0].GetId() {
		t.Fatalf("a start while a probe runs answers %v err %v, want %s", running.GetId(), err, first[0].GetId())
	}
	d.Force()
	d.Force()
	if n := len(probeTasks(d)); n != 1 {
		t.Fatalf("%d probe tasks while the first runs", n)
	}
	g.release <- struct{}{}
	<-g.started
	if n := len(probeTasks(d)); n != 2 {
		t.Fatalf("%d probe tasks once the first ended", n)
	}
	g.release <- struct{}{}
	settled(t, d)
	if n := len(probeTasks(d)); n != 2 || len(g.started) != 0 {
		t.Fatalf("%d probe tasks and %d probes waiting after the queued one ended", n, len(g.started))
	}
	d.Force()
	<-g.started
	g.release <- struct{}{}
	settled(t, d)
	if n := len(probeTasks(d)); n != 3 {
		t.Fatalf("%d probe tasks after a force with none running", n)
	}
}

// A probe forced while one runs still starts when that one is cancelled, and none starts once the
// daemon is shutting down
func TestForceOutlivesCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	g := gate{started: make(chan struct{}, 4), release: make(chan struct{})}
	d := newDoctor(t, ctx, g)
	d.Force()
	<-g.started
	d.Force()
	if _, err := d.Tasks.Cancel(probeTasks(d)[0].GetId()); err != nil {
		t.Fatal(err)
	}
	<-g.started
	if n := len(probeTasks(d)); n != 2 {
		t.Fatalf("%d probe tasks after cancelling the first", n)
	}
	d.Force()
	cancel()
	settled(t, d)
	if n := len(probeTasks(d)); n != 2 || len(g.started) != 0 {
		t.Fatalf("%d probe tasks and %d probes waiting after shutdown", n, len(g.started))
	}
}
