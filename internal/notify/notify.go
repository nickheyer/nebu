// Package notify announces findings and failures beyond the UI.
package notify

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/nickheyer/nebu/pkg/events"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

const postTimeout = 15 * time.Second

// Posts one JSON document per event that matters to every webhook
type Notifier struct {
	Webhooks []string
	Events   *events.Bus
	Log      *slog.Logger
	// Set to change the client, tests do
	Client *http.Client
}

// Follows the bus until ctx ends, nothing to do without webhooks
func (n *Notifier) Run(ctx context.Context) {
	if len(n.Webhooks) == 0 {
		return
	}
	sub := n.Events.Subscribe(ctx, []v1.EventKind{v1.EventKind_EVENT_KIND_FINDING, v1.EventKind_EVENT_KIND_INSTANCE})
	seen := map[string]v1.InstanceState{}
	for {
		var ev *v1.Event
		select {
		case <-ctx.Done():
			return
		case ev = <-sub.Events():
		}
		if ev.GetSeq() == 0 {
			continue
		}
		switch p := ev.GetPayload().(type) {
		case *v1.Event_Finding:
			if ev.GetAction() == v1.EventAction_EVENT_ACTION_CREATED {
				n.post(ctx, ev)
			}
		case *v1.Event_Instance:
			id, state := p.Instance.GetId(), p.Instance.GetState()
			if ev.GetAction() == v1.EventAction_EVENT_ACTION_DELETED {
				delete(seen, id)
				continue
			}
			// Only the step into failed, not every write while failed
			if state == v1.InstanceState_INSTANCE_STATE_FAILED && seen[id] != state {
				n.post(ctx, ev)
			}
			seen[id] = state
		}
	}
}

// Sends the event to every webhook, each failure logged and left behind
func (n *Notifier) post(ctx context.Context, ev *v1.Event) {
	body, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(ev)
	if err != nil {
		return
	}
	client := n.Client
	if client == nil {
		client = &http.Client{Timeout: postTimeout}
	}
	for _, url := range n.Webhooks {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			n.Log.Warn("webhook", "url", url, "err", err)
			continue
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "nebu")
		resp, err := client.Do(req)
		if err != nil {
			n.Log.Warn("webhook", "url", url, "err", err)
			continue
		}
		resp.Body.Close()
		if resp.StatusCode >= http.StatusMultipleChoices {
			n.Log.Warn("webhook", "url", url, "status", resp.StatusCode)
		}
	}
}
