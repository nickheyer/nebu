package services

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/nickheyer/nebu/internal/bots"
	"github.com/nickheyer/nebu/internal/db"
	"github.com/nickheyer/nebu/internal/tasks"
	"github.com/nickheyer/nebu/pkg/events"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func openDB(t *testing.T) *db.DB {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "nebu.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	return database
}

// The kind and state filters each keep a task matching any value they list and narrow together
func TestListTasksFiltersTakeSeveralValues(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	m := tasks.New(ctx, log, openDB(t), events.New())
	hold := make(chan struct{})
	defer close(hold)
	m.Start("pull", "pull done", nil, func(context.Context, *tasks.Handle) error { return nil })
	m.Start("verify", "verify done", nil, func(context.Context, *tasks.Handle) error { return nil })
	m.Start("pull", "pull running", nil, func(ctx context.Context, _ *tasks.Handle) error {
		select {
		case <-hold:
		case <-ctx.Done():
		}
		return nil
	})
	svc := NewTaskService(m)
	titles := func(req *v1.ListTasksRequest) []string {
		t.Helper()
		resp, err := svc.ListTasks(ctx, connect.NewRequest(req))
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, task := range resp.Msg.GetTasks() {
			got = append(got, task.GetTitle())
		}
		slices.Sort(got)
		return got
	}
	deadline := time.Now().Add(10 * time.Second)
	done := []v1.TaskState{v1.TaskState_TASK_STATE_SUCCEEDED}
	for len(titles(&v1.ListTasksRequest{States: done})) < 2 {
		if time.Now().After(deadline) {
			t.Fatal("the two short tasks did not finish")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cases := []struct {
		name string
		req  *v1.ListTasksRequest
		want []string
	}{
		{"no filter", &v1.ListTasksRequest{}, []string{"pull done", "pull running", "verify done"}},
		{"one kind", &v1.ListTasksRequest{Kinds: []string{"verify"}}, []string{"verify done"}},
		{"two kinds", &v1.ListTasksRequest{Kinds: []string{"verify", "pull"}}, []string{"pull done", "pull running", "verify done"}},
		{"one state", &v1.ListTasksRequest{States: done}, []string{"pull done", "verify done"}},
		{"two states", &v1.ListTasksRequest{States: []v1.TaskState{v1.TaskState_TASK_STATE_SUCCEEDED, v1.TaskState_TASK_STATE_RUNNING}}, []string{"pull done", "pull running", "verify done"}},
		{"kind and state", &v1.ListTasksRequest{Kinds: []string{"pull"}, States: done}, []string{"pull done"}},
		{"a kind no task has", &v1.ListTasksRequest{Kinds: []string{"build"}}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := titles(c.req); !slices.Equal(got, c.want) {
				t.Fatalf("tasks %v, want %v", got, c.want)
			}
		})
	}
}

// The level and kind filters each keep an activity line matching any value they list and narrow together
func TestListBotActivityFiltersTakeSeveralValues(t *testing.T) {
	ctx := context.Background()
	database := openDB(t)
	bot := &v1.Bot{Id: "b1", Name: "helper", Spec: &v1.BotSpec{}, CreatedAt: timestamppb.Now(), UpdatedAt: timestamppb.Now()}
	if err := database.PutBot(ctx, bot, "token"); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	for i, a := range []*v1.BotActivity{
		{Level: "info", Kind: "reply", Message: "answered"},
		{Level: "warn", Kind: "image", Message: "slow image"},
		{Level: "error", Kind: "reply", Message: "reply failed"},
		{Level: "info", Kind: "shard", Message: "connected"},
	} {
		a.BotId, a.At = bot.GetId(), timestamppb.New(base.Add(time.Duration(i)*time.Second))
		if err := database.PutBotActivity(ctx, a, 100); err != nil {
			t.Fatal(err)
		}
	}
	m := bots.New(ctx, database, nil, nil, events.New(), nil, "")
	if err := m.Load(ctx); err != nil {
		t.Fatal(err)
	}
	svc := NewBotService(m)
	cases := []struct {
		name string
		req  *v1.ListBotActivityRequest
		want []string
	}{
		{"no filter", &v1.ListBotActivityRequest{}, []string{"connected", "reply failed", "slow image", "answered"}},
		{"one level", &v1.ListBotActivityRequest{Levels: []string{"info"}}, []string{"connected", "answered"}},
		{"two levels", &v1.ListBotActivityRequest{Levels: []string{"warn", "error"}}, []string{"reply failed", "slow image"}},
		{"two kinds", &v1.ListBotActivityRequest{Kinds: []string{"image", "shard"}}, []string{"connected", "slow image"}},
		{"level and kind", &v1.ListBotActivityRequest{Levels: []string{"info", "error"}, Kinds: []string{"reply"}}, []string{"reply failed", "answered"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.req.BotId = bot.GetId()
			resp, err := svc.ListBotActivity(ctx, connect.NewRequest(c.req))
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, a := range resp.Msg.GetActivity() {
				got = append(got, a.GetMessage())
			}
			if !slices.Equal(got, c.want) {
				t.Fatalf("activity %v, want %v", got, c.want)
			}
		})
	}
}
