package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/nickheyer/nebu/pkg/events"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// Records every post a webhook takes, answering the status queued for each
type hook struct {
	mu     sync.Mutex
	bodies []map[string]any
	status []int
}

func (h *hook) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	defer h.mu.Unlock()
	var body map[string]any
	data, _ := io.ReadAll(r.Body)
	json.Unmarshal(data, &body)
	if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" || r.Header.Get("User-Agent") != "nebu" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	h.bodies = append(h.bodies, body)
	code := http.StatusOK
	if len(h.status) > 0 {
		code, h.status = h.status[0], h.status[1:]
	}
	w.WriteHeader(code)
}

// The posts so far
func (h *hook) posts() []map[string]any {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]map[string]any(nil), h.bodies...)
}

// Polls until the hook holds n posts
func (h *hook) await(t *testing.T, n int, within time.Duration) []map[string]any {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if got := h.posts(); len(got) >= n {
			return got
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("%d posts never arrived, have %v", n, h.posts())
	return nil
}

// A running notifier over a bus and the hooks it posts to
func run(t *testing.T, bus *events.Bus, hooks ...*hook) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	var urls []string
	for _, h := range hooks {
		srv := httptest.NewServer(h)
		t.Cleanup(srv.Close)
		urls = append(urls, srv.URL)
	}
	n := &Notifier{Events: bus, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	n.Set(failureHooks(urls...))
	done := make(chan struct{})
	go func() {
		defer close(done)
		n.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	// The subscription is in place once an unrelated event flows through
	time.Sleep(20 * time.Millisecond)
}

// Webhooks told of failed models in the event document
func failureHooks(urls ...string) []*v1.Webhook {
	var out []*v1.Webhook
	for i, u := range urls {
		out = append(out, &v1.Webhook{Id: fmt.Sprint(i), Name: fmt.Sprint("hook ", i), Url: u, Enabled: true, Format: v1.WebhookFormat_WEBHOOK_FORMAT_EVENT, Events: []v1.NotifyEvent{v1.NotifyEvent_NOTIFY_EVENT_MODEL_FAILED}})
	}
	return out
}

func instance(id string, state v1.InstanceState) *v1.Instance {
	return &v1.Instance{Id: id, Name: "model-" + id, State: state}
}

// Only the move into failed posts, once per failure, every webhook taking the event as proto JSON
func TestRunPostsEachFailureOnce(t *testing.T) {
	bus := events.New()
	first, second := &hook{}, &hook{}
	run(t, bus, first, second)
	kind, updated := v1.EventKind_EVENT_KIND_INSTANCE, v1.EventAction_EVENT_ACTION_UPDATED
	bus.Publish(kind, updated, "i1", instance("i1", v1.InstanceState_INSTANCE_STATE_READY))
	bus.Publish(kind, updated, "i1", instance("i1", v1.InstanceState_INSTANCE_STATE_FAILED))
	bus.Publish(kind, updated, "i1", instance("i1", v1.InstanceState_INSTANCE_STATE_FAILED))
	bus.Publish(v1.EventKind_EVENT_KIND_TASK, updated, "t1", &v1.Task{Id: "t1"})
	bus.Publish(kind, updated, "i2", instance("i2", v1.InstanceState_INSTANCE_STATE_FAILED))
	for _, h := range []*hook{first, second} {
		got := h.await(t, 2, 3*time.Second)
		if len(got) != 2 || got[0]["id"] != "i1" || got[1]["id"] != "i2" {
			t.Fatalf("posts %v", got)
		}
		in, _ := got[0]["instance"].(map[string]any)
		if got[0]["kind"] != "EVENT_KIND_INSTANCE" || in["state"] != "INSTANCE_STATE_FAILED" || in["name"] != "model-i1" {
			t.Fatalf("first post %v", got[0])
		}
	}
	// A recovery then another failure posts again, a deletion forgets the instance
	bus.Publish(kind, updated, "i1", instance("i1", v1.InstanceState_INSTANCE_STATE_READY))
	bus.Publish(kind, updated, "i1", instance("i1", v1.InstanceState_INSTANCE_STATE_FAILED))
	bus.Publish(kind, v1.EventAction_EVENT_ACTION_DELETED, "i2", instance("i2", v1.InstanceState_INSTANCE_STATE_FAILED))
	bus.Publish(kind, updated, "i2", instance("i2", v1.InstanceState_INSTANCE_STATE_FAILED))
	if got := first.await(t, 4, 3*time.Second); len(got) != 4 || got[2]["id"] != "i1" || got[3]["id"] != "i2" {
		t.Fatalf("later posts %v", got)
	}
}

// A server error is retried with backoff until the webhook takes the post, a refusal is not retried
func TestPostRetriesServerErrors(t *testing.T) {
	bus := events.New()
	flaky := &hook{status: []int{http.StatusBadGateway, http.StatusOK}}
	refusing := &hook{status: []int{http.StatusForbidden}}
	run(t, bus, flaky, refusing)
	start := time.Now()
	bus.Publish(v1.EventKind_EVENT_KIND_INSTANCE, v1.EventAction_EVENT_ACTION_UPDATED, "i1", instance("i1", v1.InstanceState_INSTANCE_STATE_FAILED))
	if got := flaky.await(t, 2, 6*time.Second); len(got) != 2 || time.Since(start) < retryBackoff {
		t.Fatalf("flaky posts %v after %s", got, time.Since(start))
	}
	bus.Publish(v1.EventKind_EVENT_KIND_INSTANCE, v1.EventAction_EVENT_ACTION_UPDATED, "i2", instance("i2", v1.InstanceState_INSTANCE_STATE_FAILED))
	refusing.await(t, 2, 3*time.Second)
	time.Sleep(100 * time.Millisecond)
	if got := refusing.posts(); len(got) != 2 {
		t.Fatalf("a refused post was retried: %v", got)
	}
}

// Webhooks set while the notifier runs get the failures from then on, none set getting nothing
func TestWebhooksSetWhileRunning(t *testing.T) {
	bus := events.New()
	h := &hook{}
	srv := httptest.NewServer(h)
	defer srv.Close()
	n := &Notifier{Events: bus, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		n.Run(ctx)
	}()
	defer func() {
		cancel()
		<-done
	}()
	fail := func(id string) {
		bus.Publish(v1.EventKind_EVENT_KIND_INSTANCE, v1.EventAction_EVENT_ACTION_UPDATED, id, &v1.Instance{Id: id, State: v1.InstanceState_INSTANCE_STATE_FAILED})
	}
	for i := range 5 {
		fail(fmt.Sprintf("before-%d", i))
		time.Sleep(20 * time.Millisecond)
	}
	if got := h.posts(); len(got) > 0 {
		t.Fatalf("failures were posted with no webhook set: %v", got)
	}
	n.Set(failureHooks(srv.URL))
	deadline := time.Now().Add(5 * time.Second)
	for i := 0; len(h.posts()) == 0 && time.Now().Before(deadline); i++ {
		fail(fmt.Sprintf("after-%d", i))
		time.Sleep(20 * time.Millisecond)
	}
	if len(h.posts()) == 0 {
		t.Fatal("no failure reached the webhook set while running")
	}
}

// Each webhook gets only the events it names, in its own format, a disabled one nothing, a finished task once
func TestWebhooksTakeTheirEventsInTheirFormat(t *testing.T) {
	bus := events.New()
	discord, slack, tasks, off := &hook{}, &hook{}, &hook{}, &hook{}
	var hooks []*v1.Webhook
	for i, h := range []*hook{discord, slack, tasks, off} {
		srv := httptest.NewServer(h)
		t.Cleanup(srv.Close)
		hooks = append(hooks, &v1.Webhook{Id: fmt.Sprint(i), Name: fmt.Sprint("hook ", i), Url: srv.URL, Enabled: true})
	}
	hooks[0].Format, hooks[0].Events = v1.WebhookFormat_WEBHOOK_FORMAT_DISCORD, []v1.NotifyEvent{v1.NotifyEvent_NOTIFY_EVENT_MODEL_FAILED, v1.NotifyEvent_NOTIFY_EVENT_TASK_FAILED}
	hooks[1].Format, hooks[1].Events = v1.WebhookFormat_WEBHOOK_FORMAT_SLACK, []v1.NotifyEvent{v1.NotifyEvent_NOTIFY_EVENT_TASK_SUCCEEDED}
	hooks[2].Format, hooks[2].Events = v1.WebhookFormat_WEBHOOK_FORMAT_EVENT, []v1.NotifyEvent{v1.NotifyEvent_NOTIFY_EVENT_TASK_FAILED, v1.NotifyEvent_NOTIFY_EVENT_TASK_SUCCEEDED}
	hooks[3].Format, hooks[3].Events, hooks[3].Enabled = v1.WebhookFormat_WEBHOOK_FORMAT_EVENT, []v1.NotifyEvent{v1.NotifyEvent_NOTIFY_EVENT_MODEL_FAILED, v1.NotifyEvent_NOTIFY_EVENT_TASK_FAILED, v1.NotifyEvent_NOTIFY_EVENT_TASK_SUCCEEDED}, false
	n := &Notifier{Events: bus, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Host: func() string { return "disco" }}
	n.Set(hooks)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		n.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	time.Sleep(20 * time.Millisecond)
	updated := v1.EventAction_EVENT_ACTION_UPDATED
	task := func(id string, state v1.TaskState, err string) {
		bus.Publish(v1.EventKind_EVENT_KIND_TASK, updated, id, &v1.Task{Id: id, Kind: "pull", Title: "Pull " + id, State: state, Error: err})
	}
	task("t1", v1.TaskState_TASK_STATE_RUNNING, "")
	task("t1", v1.TaskState_TASK_STATE_FAILED, "no room")
	task("t1", v1.TaskState_TASK_STATE_FAILED, "no room")
	task("t2", v1.TaskState_TASK_STATE_SUCCEEDED, "")
	task("t3", v1.TaskState_TASK_STATE_CANCELED, "")
	failed := instance("i1", v1.InstanceState_INSTANCE_STATE_FAILED)
	failed.Error = "out of memory"
	bus.Publish(v1.EventKind_EVENT_KIND_INSTANCE, updated, "i1", failed)
	got := discord.await(t, 2, 3*time.Second)
	if got[0]["content"] != "disco: Pull t1 failed: no room" || got[1]["content"] != "disco: model-i1 failed: out of memory" {
		t.Errorf("discord posts %v", got)
	}
	if got := slack.await(t, 1, 3*time.Second); got[0]["text"] != "disco: Pull t2 finished" {
		t.Errorf("slack posts %v", got)
	}
	if got := tasks.await(t, 2, 3*time.Second); got[0]["id"] != "t1" || got[1]["id"] != "t2" || got[0]["kind"] != "EVENT_KIND_TASK" {
		t.Errorf("event posts %v", got)
	}
	time.Sleep(100 * time.Millisecond)
	if len(discord.posts()) != 2 || len(slack.posts()) != 1 || len(tasks.posts()) != 2 || len(off.posts()) != 0 {
		t.Errorf("posts beyond what each webhook names: discord %d, slack %d, tasks %d, off %d", len(discord.posts()), len(slack.posts()), len(tasks.posts()), len(off.posts()))
	}
}

// A test posts one sample of the first event the webhook names and answers the status it got
func TestWebhookTest(t *testing.T) {
	h := &hook{status: []int{http.StatusNoContent, http.StatusForbidden}}
	srv := httptest.NewServer(h)
	defer srv.Close()
	n := &Notifier{Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	hook := &v1.Webhook{Name: "chat", Url: srv.URL, Format: v1.WebhookFormat_WEBHOOK_FORMAT_SLACK, Events: []v1.NotifyEvent{v1.NotifyEvent_NOTIFY_EVENT_TASK_FAILED}}
	if status, err := n.Test(context.Background(), hook); err != nil || status != http.StatusNoContent {
		t.Fatalf("status %d err %v", status, err)
	}
	if got := h.posts(); len(got) != 1 || got[0]["text"] != "Test notification failed" {
		t.Fatalf("sample %v", got)
	}
	if status, err := n.Test(context.Background(), hook); err != nil || status != http.StatusForbidden {
		t.Fatalf("a refusing webhook: status %d err %v", status, err)
	}
	bare := &v1.Webhook{Url: srv.URL, Format: v1.WebhookFormat_WEBHOOK_FORMAT_DISCORD}
	if status, err := n.Test(context.Background(), bare); err != nil || status != http.StatusOK {
		t.Fatalf("a webhook holding a URL alone: status %d err %v", status, err)
	}
	if got := h.posts(); len(got) != 3 || got[2]["content"] != "Test notification finished" {
		t.Fatalf("sample of a webhook naming no event %v", got)
	}
	hook.Url = "http://127.0.0.1:1/nowhere"
	if _, err := n.Test(context.Background(), hook); err == nil {
		t.Fatal("a webhook nobody listens on answered")
	}
}
