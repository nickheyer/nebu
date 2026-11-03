package notify

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
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
	n := &Notifier{Webhooks: urls, Events: bus, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
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

// Without webhooks the notifier returns at once and never subscribes
func TestRunWithoutWebhooks(t *testing.T) {
	bus := events.New()
	var subscribed atomic.Bool
	n := &Notifier{Events: bus, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	done := make(chan struct{})
	go func() {
		defer close(done)
		n.Run(context.Background())
		subscribed.Store(false)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run without webhooks did not return")
	}
}

// Failure transitions are detected per instance, the deletion of one clearing what was seen
func TestMatters(t *testing.T) {
	seen := map[string]v1.InstanceState{}
	kind, updated := v1.EventKind_EVENT_KIND_INSTANCE, v1.EventAction_EVENT_ACTION_UPDATED
	for i, tc := range []struct {
		ev   *v1.Event
		want bool
	}{
		{events.Event(kind, updated, "i1", instance("i1", v1.InstanceState_INSTANCE_STATE_READY)), false},
		{events.Event(kind, updated, "i1", instance("i1", v1.InstanceState_INSTANCE_STATE_FAILED)), true},
		{events.Event(kind, updated, "i1", instance("i1", v1.InstanceState_INSTANCE_STATE_FAILED)), false},
		{events.Event(kind, updated, "i1", instance("i1", v1.InstanceState_INSTANCE_STATE_STOPPED)), false},
		{events.Event(kind, updated, "i1", instance("i1", v1.InstanceState_INSTANCE_STATE_FAILED)), true},
		{events.Event(kind, v1.EventAction_EVENT_ACTION_DELETED, "i1", instance("i1", v1.InstanceState_INSTANCE_STATE_FAILED)), false},
		{events.Event(kind, updated, "i1", instance("i1", v1.InstanceState_INSTANCE_STATE_FAILED)), true},
		{events.Event(v1.EventKind_EVENT_KIND_TASK, updated, "t1", &v1.Task{Id: "t1"}), false},
	} {
		if got := matters(tc.ev, seen); got != tc.want {
			t.Fatalf("event %d matters %v, want %v", i, got, tc.want)
		}
	}
}
