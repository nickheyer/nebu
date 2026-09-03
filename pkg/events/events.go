// Package events is the in-process bus behind the watch stream.
package events

import (
	"context"
	"sync"
	"sync/atomic"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const subscriberBuffer = 256

// Fans events out to subscribers, dropping oldest for slow ones
type Bus struct {
	mu   sync.Mutex
	seq  atomic.Uint64
	subs map[*Subscription]struct{}
}

// One subscriber's queue
type Subscription struct {
	bus     *Bus
	kinds   map[v1.EventKind]bool
	ch      chan *v1.Event
	dropped atomic.Uint64
}

// Builds a bus
func New() *Bus { return &Bus{subs: map[*Subscription]struct{}{}} }

// Publishes one event with its record, nil bus safe
func (b *Bus) Publish(kind v1.EventKind, action v1.EventAction, id string, payload any) *v1.Event {
	if b == nil {
		return nil
	}
	ev := &v1.Event{Seq: b.seq.Add(1), Kind: kind, Action: action, Id: id, At: timestamppb.Now()}
	setPayload(ev, payload)
	b.mu.Lock()
	defer b.mu.Unlock()
	for s := range b.subs {
		if len(s.kinds) > 0 && !s.kinds[kind] {
			continue
		}
		select {
		case s.ch <- ev:
		default:
			select {
			case <-s.ch:
				s.dropped.Add(1)
			default:
			}
			select {
			case s.ch <- ev:
			default:
			}
		}
	}
	return ev
}

// Subscribes to kinds, all when empty, until ctx ends
func (b *Bus) Subscribe(ctx context.Context, kinds []v1.EventKind) *Subscription {
	s := &Subscription{bus: b, kinds: map[v1.EventKind]bool{}, ch: make(chan *v1.Event, subscriberBuffer)}
	for _, k := range kinds {
		s.kinds[k] = true
	}
	if b == nil {
		return s
	}
	b.mu.Lock()
	b.subs[s] = struct{}{}
	b.mu.Unlock()
	go func() {
		<-ctx.Done()
		b.mu.Lock()
		delete(b.subs, s)
		b.mu.Unlock()
	}()
	return s
}

// Delivers events, closed never, so select on ctx alongside it
func (s *Subscription) Events() <-chan *v1.Event { return s.ch }

// Counts events dropped for this subscriber
func (s *Subscription) Dropped() uint64 { return s.dropped.Load() }

// Copies the record into the event so mutation cannot leak
func setPayload(ev *v1.Event, p any) {
	switch t := p.(type) {
	case *v1.Event_Host:
		ev.Payload = &v1.Event_Host{Host: proto.Clone(t.Host).(*v1.HostProfile)}
	case *v1.Event_Task:
		ev.Payload = &v1.Event_Task{Task: proto.Clone(t.Task).(*v1.Task)}
	case *v1.Event_Instance:
		ev.Payload = &v1.Event_Instance{Instance: proto.Clone(t.Instance).(*v1.Instance)}
	case *v1.Event_Slot:
		ev.Payload = &v1.Event_Slot{Slot: proto.Clone(t.Slot).(*v1.Slot)}
	case *v1.Event_Route:
		ev.Payload = &v1.Event_Route{Route: proto.Clone(t.Route).(*v1.Route)}
	case *v1.Event_Install:
		ev.Payload = &v1.Event_Install{Install: proto.Clone(t.Install).(*v1.Install)}
	case *v1.Event_Build:
		ev.Payload = &v1.Event_Build{Build: proto.Clone(t.Build).(*v1.Build)}
	case *v1.Event_Model:
		ev.Payload = &v1.Event_Model{Model: proto.Clone(t.Model).(*v1.StoredModel)}
	case *v1.Event_Watch:
		ev.Payload = &v1.Event_Watch{Watch: proto.Clone(t.Watch).(*v1.Watch)}
	case *v1.Event_Finding:
		ev.Payload = &v1.Event_Finding{Finding: proto.Clone(t.Finding).(*v1.Finding)}
	}
}
