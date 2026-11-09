package gateway

import (
	"context"
	"net/http"
	"time"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// Longest wait for a trace to finish once its answer arrived
const traceFinishWait = 30 * time.Second

// The finished trace recorded for the request a response answers, false when the answer names none or it never finished
func (g *Gateway) Trace(resp *http.Response) (*v1.Trace, bool) {
	id := resp.Header.Get(TraceHeader)
	if id == "" {
		return nil, false
	}
	ctx := context.Background()
	if resp.Request != nil {
		ctx = resp.Request.Context()
	}
	ctx, cancel := context.WithTimeout(ctx, traceFinishWait)
	defer cancel()
	sub := g.events.Subscribe(ctx, []v1.EventKind{v1.EventKind_EVENT_KIND_TRACE})
	if t, ok, err := g.traces.Get(ctx, Viewer{All: true}, id); err == nil && ok && t.GetFinishedAt() != nil {
		return t, true
	}
	for {
		select {
		case <-ctx.Done():
			return nil, false
		case ev := <-sub.Events():
			if ev.GetId() == id && ev.GetAction() == v1.EventAction_EVENT_ACTION_UPDATED && ev.GetTrace().GetFinishedAt() != nil {
				return ev.GetTrace(), true
			}
		}
	}
}
