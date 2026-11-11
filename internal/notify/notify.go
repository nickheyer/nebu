// Package notify tells webhooks when models fail and tasks fail or finish.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nickheyer/nebu/pkg/events"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

const (
	postTimeout = 15 * time.Second
	// Webhook queue capacity. Posts past it are dropped.
	queueSize = 64
	// Maximum webhook attempts, with backoff for connection and server errors.
	attempts     = 4
	retryBackoff = 2 * time.Second
	// Finished tasks remembered so a repeated event posts once
	doneTasks = 1024
)

// Posts each event a webhook names to it, in the body its format asks for
type Notifier struct {
	Events *events.Bus
	Log    *slog.Logger
	// The name of this host in a line of text, none when unset
	Host func() string

	mu       sync.Mutex
	webhooks []*v1.Webhook
}

// One event bound for one webhook
type post struct {
	hook *v1.Webhook
	body []byte
}

// Replaces the webhooks told from now on
func (n *Notifier) Set(webhooks []*v1.Webhook) {
	kept := make([]*v1.Webhook, len(webhooks))
	for i, w := range webhooks {
		kept[i] = proto.Clone(w).(*v1.Webhook)
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	n.webhooks = kept
}

// The webhooks told now
func (n *Notifier) Webhooks() []*v1.Webhook {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.webhooks
}

// Posts events to the webhooks set at the time of each, until cancellation.
// A bounded queue keeps slow webhooks off the event loop and counts dropped posts.
func (n *Notifier) Run(ctx context.Context) {
	sub := n.Events.Subscribe(ctx, []v1.EventKind{v1.EventKind_EVENT_KIND_INSTANCE, v1.EventKind_EVENT_KIND_TASK})
	queue := make(chan post, queueSize)
	done := make(chan struct{})
	client := &http.Client{Timeout: postTimeout}
	go func() {
		defer close(done)
		for p := range queue {
			n.deliver(ctx, client, p)
		}
	}()
	defer func() {
		close(queue)
		<-done
	}()
	seen := &history{instances: map[string]v1.InstanceState{}, tasks: map[string]struct{}{}}
	var dropped uint64
	for {
		var ev *v1.Event
		select {
		case <-ctx.Done():
			return
		case ev = <-sub.Events():
		}
		if d := sub.Dropped(); d != dropped {
			n.Log.Warn("webhook events dropped behind a slow subscriber", "count", d-dropped)
			dropped = d
		}
		if ev.GetSeq() == 0 {
			continue
		}
		what := seen.classify(ev)
		if what == v1.NotifyEvent_NOTIFY_EVENT_UNSPECIFIED {
			continue
		}
		for _, hook := range n.Webhooks() {
			if !hook.GetEnabled() || !slices.Contains(hook.GetEvents(), what) {
				continue
			}
			body, err := n.body(hook.GetFormat(), what, ev)
			if err != nil {
				n.Log.Warn("webhook event not encoded", "webhook", hook.GetName(), "err", err)
				continue
			}
			select {
			case queue <- post{hook: hook, body: body}:
			default:
				n.Log.Warn("webhook queue full, event not posted", "webhook", hook.GetName(), "kind", ev.GetKind(), "id", ev.GetId())
			}
		}
	}
}

// What the notifier has already told, so each failure and each finished task is told once
type history struct {
	instances map[string]v1.InstanceState
	tasks     map[string]struct{}
	order     []string
}

// The notification an event calls for, unspecified for one that calls for none
func (h *history) classify(ev *v1.Event) v1.NotifyEvent {
	if in := ev.GetInstance(); in != nil {
		id, state := in.GetId(), in.GetState()
		if ev.GetAction() == v1.EventAction_EVENT_ACTION_DELETED {
			delete(h.instances, id)
			return v1.NotifyEvent_NOTIFY_EVENT_UNSPECIFIED
		}
		failed := state == v1.InstanceState_INSTANCE_STATE_FAILED && h.instances[id] != state
		h.instances[id] = state
		if failed {
			return v1.NotifyEvent_NOTIFY_EVENT_MODEL_FAILED
		}
		return v1.NotifyEvent_NOTIFY_EVENT_UNSPECIFIED
	}
	t := ev.GetTask()
	var what v1.NotifyEvent
	switch t.GetState() {
	case v1.TaskState_TASK_STATE_FAILED:
		what = v1.NotifyEvent_NOTIFY_EVENT_TASK_FAILED
	case v1.TaskState_TASK_STATE_SUCCEEDED:
		what = v1.NotifyEvent_NOTIFY_EVENT_TASK_SUCCEEDED
	default:
		return v1.NotifyEvent_NOTIFY_EVENT_UNSPECIFIED
	}
	if _, told := h.tasks[t.GetId()]; told {
		return v1.NotifyEvent_NOTIFY_EVENT_UNSPECIFIED
	}
	h.tasks[t.GetId()] = struct{}{}
	if h.order = append(h.order, t.GetId()); len(h.order) > doneTasks {
		delete(h.tasks, h.order[0])
		h.order = h.order[1:]
	}
	return what
}

// The body a format carries an event in
func (n *Notifier) body(format v1.WebhookFormat, what v1.NotifyEvent, ev *v1.Event) ([]byte, error) {
	switch format {
	case v1.WebhookFormat_WEBHOOK_FORMAT_DISCORD:
		return json.Marshal(map[string]string{"content": n.line(what, ev)})
	case v1.WebhookFormat_WEBHOOK_FORMAT_SLACK:
		return json.Marshal(map[string]string{"text": n.line(what, ev)})
	}
	return protojson.MarshalOptions{UseProtoNames: true}.Marshal(ev)
}

// An event as one line, the host first when it has a name, such as disco: Pull qwen failed: no room
func (n *Notifier) line(what v1.NotifyEvent, ev *v1.Event) string {
	var subject, outcome, detail string
	switch what {
	case v1.NotifyEvent_NOTIFY_EVENT_MODEL_FAILED:
		subject, outcome, detail = ev.GetInstance().GetName(), "failed", ev.GetInstance().GetError()
	case v1.NotifyEvent_NOTIFY_EVENT_TASK_FAILED:
		subject, outcome, detail = taskName(ev.GetTask()), "failed", ev.GetTask().GetError()
	default:
		subject, outcome = taskName(ev.GetTask()), "finished"
	}
	text := subject + " " + outcome
	if detail != "" {
		text += ": " + detail
	}
	if n.Host != nil {
		if host := n.Host(); host != "" {
			text = host + ": " + text
		}
	}
	return text
}

// A task by its title, else its kind
func taskName(t *v1.Task) string {
	if title := strings.TrimSpace(t.GetTitle()); title != "" {
		return title
	}
	return t.GetKind()
}

// Posts one sample of the first event a webhook names, answering the status it got
func (n *Notifier) Test(ctx context.Context, hook *v1.Webhook) (int, error) {
	what := v1.NotifyEvent_NOTIFY_EVENT_TASK_SUCCEEDED
	if len(hook.GetEvents()) > 0 {
		what = hook.GetEvents()[0]
	}
	ev := &v1.Event{Kind: v1.EventKind_EVENT_KIND_TASK, Action: v1.EventAction_EVENT_ACTION_UPDATED, Id: "test"}
	switch what {
	case v1.NotifyEvent_NOTIFY_EVENT_MODEL_FAILED:
		ev.Kind = v1.EventKind_EVENT_KIND_INSTANCE
		ev.Payload = &v1.Event_Instance{Instance: &v1.Instance{Id: "test", Name: "Test notification", State: v1.InstanceState_INSTANCE_STATE_FAILED}}
	case v1.NotifyEvent_NOTIFY_EVENT_TASK_FAILED:
		ev.Payload = &v1.Event_Task{Task: &v1.Task{Id: "test", Kind: "test", Title: "Test notification", State: v1.TaskState_TASK_STATE_FAILED}}
	default:
		ev.Payload = &v1.Event_Task{Task: &v1.Task{Id: "test", Kind: "test", Title: "Test notification", State: v1.TaskState_TASK_STATE_SUCCEEDED}}
	}
	body, err := n.body(hook.GetFormat(), what, ev)
	if err != nil {
		return 0, err
	}
	status, err := send(ctx, &http.Client{Timeout: postTimeout}, hook.GetUrl(), body)
	if err != nil {
		return 0, fmt.Errorf("%s did not answer: %w", hook.GetUrl(), err)
	}
	return status, nil
}

// Posts to one webhook, retrying connection and server errors.
func (n *Notifier) deliver(ctx context.Context, client *http.Client, p post) {
	name, url := p.hook.GetName(), p.hook.GetUrl()
	for attempt := 1; attempt <= attempts; attempt++ {
		status, err := send(ctx, client, url, p.body)
		if err == nil && status < http.StatusInternalServerError {
			if status >= http.StatusMultipleChoices {
				n.Log.Warn("webhook refused the event", "webhook", name, "url", url, "status", status)
			}
			return
		}
		if attempt == attempts || ctx.Err() != nil {
			n.Log.Warn("webhook gave up", "webhook", name, "url", url, "status", status, "err", err, "attempts", attempt)
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(retryBackoff * time.Duration(attempt)):
		}
	}
}

func send(ctx context.Context, client *http.Client, url string, body []byte) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "nebu")
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	resp.Body.Close()
	return resp.StatusCode, nil
}
