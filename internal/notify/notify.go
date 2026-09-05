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

const (
	postTimeout = 15 * time.Second
	// Posts waiting for a slow webhook before the oldest is dropped
	queueSize = 1024
	// Attempts per webhook, a failed connection or a server error tried again after a growing pause
	attempts     = 4
	retryBackoff = 2 * time.Second
)

// Posts one JSON document per event that matters to every webhook
type Notifier struct {
	Webhooks []string
	Events   *events.Bus
	Log      *slog.Logger
}

// Follows the bus until ctx ends, nothing to do without webhooks
//
// Posting happens off the bus loop through a bounded queue, so a webhook that
// stalls never costs the loop an event, and what the queue cannot hold is counted.
func (n *Notifier) Run(ctx context.Context) {
	if len(n.Webhooks) == 0 {
		return
	}
	sub := n.Events.Subscribe(ctx, []v1.EventKind{v1.EventKind_EVENT_KIND_FINDING, v1.EventKind_EVENT_KIND_INSTANCE})
	queue := make(chan []byte, queueSize)
	done := make(chan struct{})
	client := &http.Client{Timeout: postTimeout}
	go func() {
		defer close(done)
		for body := range queue {
			n.post(ctx, client, body)
		}
	}()
	defer func() {
		close(queue)
		<-done
	}()
	seen := map[string]v1.InstanceState{}
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
		if ev.GetSeq() == 0 || !n.matters(ev, seen) {
			continue
		}
		body, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(ev)
		if err != nil {
			continue
		}
		select {
		case queue <- body:
		default:
			n.Log.Warn("webhook queue full, event not posted", "kind", ev.GetKind(), "id", ev.GetId())
		}
	}
}

// Picks new findings and each instance's step into failed
func (n *Notifier) matters(ev *v1.Event, seen map[string]v1.InstanceState) bool {
	switch p := ev.GetPayload().(type) {
	case *v1.Event_Finding:
		return ev.GetAction() == v1.EventAction_EVENT_ACTION_CREATED
	case *v1.Event_Instance:
		id, state := p.Instance.GetId(), p.Instance.GetState()
		if ev.GetAction() == v1.EventAction_EVENT_ACTION_DELETED {
			delete(seen, id)
			return false
		}
		failed := state == v1.InstanceState_INSTANCE_STATE_FAILED && seen[id] != state
		seen[id] = state
		return failed
	}
	return false
}

// Sends the document to every webhook, each tried again on a connection or server error
func (n *Notifier) post(ctx context.Context, client *http.Client, body []byte) {
	for _, url := range n.Webhooks {
		for attempt := 1; attempt <= attempts; attempt++ {
			status, err := send(ctx, client, url, body)
			if err == nil && status < http.StatusInternalServerError {
				if status >= http.StatusMultipleChoices {
					n.Log.Warn("webhook refused the event", "url", url, "status", status)
				}
				break
			}
			if attempt == attempts || ctx.Err() != nil {
				n.Log.Warn("webhook gave up", "url", url, "status", status, "err", err, "attempts", attempt)
				break
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(retryBackoff * time.Duration(attempt)):
			}
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
