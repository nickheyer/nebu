// Package events is the in-process bus behind the watch stream.
package events

import (
	"context"
	"sync"
	"sync/atomic"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Events a subscriber holds before a record's newer update takes the place of its pending one
const subscriberBuffer = 256

// Kinds whose every event is a record of its own, never a later state of an earlier one
var appendOnly = map[v1.EventKind]bool{v1.EventKind_EVENT_KIND_BOT_ACTIVITY: true}

// Fans events out to subscribers, each behind a bounded queue
type Bus struct {
	mu   sync.Mutex
	seq  atomic.Uint64
	subs map[*Subscription]struct{}
}

// One subscriber's queue and the channel it is read through
type Subscription struct {
	kinds   map[v1.EventKind]bool
	mu      sync.Mutex
	queue   []*v1.Event
	wake    chan struct{}
	out     chan *v1.Event
	dropped atomic.Uint64
}

// Builds a bus
func New() *Bus { return &Bus{subs: map[*Subscription]struct{}{}} }

// Publishes one event with its record, nil bus safe
func (b *Bus) Publish(kind v1.EventKind, action v1.EventAction, id string, record proto.Message) *v1.Event {
	if b == nil {
		return nil
	}
	ev := Event(kind, action, id, record)
	ev.Seq = b.seq.Add(1)
	b.mu.Lock()
	defer b.mu.Unlock()
	for s := range b.subs {
		if len(s.kinds) > 0 && !s.kinds[kind] {
			continue
		}
		s.push(ev)
	}
	return ev
}

// Subscribes to kinds, all when empty, until ctx ends, a nil bus delivering nothing
func (b *Bus) Subscribe(ctx context.Context, kinds []v1.EventKind) *Subscription {
	s := &Subscription{kinds: map[v1.EventKind]bool{}, wake: make(chan struct{}, 1), out: make(chan *v1.Event)}
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
		s.pump(ctx)
		b.mu.Lock()
		delete(b.subs, s)
		b.mu.Unlock()
	}()
	return s
}

// Queues an event, a full queue replacing the record's newest pending update or dropping its oldest event
func (s *Subscription) push(ev *v1.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.queue) >= subscriberBuffer {
		if !appendOnly[ev.GetKind()] {
			for i := len(s.queue) - 1; i >= 0; i-- {
				if q := s.queue[i]; q.GetKind() == ev.GetKind() && q.GetId() == ev.GetId() {
					s.queue[i] = ev
					s.signal()
					return
				}
			}
		}
		s.queue = s.queue[1:]
		s.dropped.Add(1)
	}
	s.queue = append(s.queue, ev)
	s.signal()
}

// Wakes the pump without blocking
func (s *Subscription) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Hands queued events to the channel in order until ctx ends
func (s *Subscription) pump(ctx context.Context) {
	for {
		s.mu.Lock()
		var ev *v1.Event
		if len(s.queue) > 0 {
			ev, s.queue[0] = s.queue[0], nil
			s.queue = s.queue[1:]
			if len(s.queue) == 0 {
				s.queue = nil
			}
		}
		s.mu.Unlock()
		if ev == nil {
			select {
			case <-ctx.Done():
				return
			case <-s.wake:
			}
			continue
		}
		select {
		case <-ctx.Done():
			return
		case s.out <- ev:
		}
	}
}

// Delivers events, closed never, so select on ctx alongside it
func (s *Subscription) Events() <-chan *v1.Event { return s.out }

// Counts events dropped for this subscriber
func (s *Subscription) Dropped() uint64 { return s.dropped.Load() }

// Builds an event around a copy of the record, in whichever payload slot holds its type
func Event(kind v1.EventKind, action v1.EventAction, id string, record proto.Message) *v1.Event {
	ev := &v1.Event{Kind: kind, Action: action, Id: id, At: timestamppb.Now()}
	if record == nil || !record.ProtoReflect().IsValid() {
		return ev
	}
	msg := ev.ProtoReflect()
	slots := msg.Descriptor().Oneofs().ByName("payload").Fields()
	name := record.ProtoReflect().Descriptor().FullName()
	for i := 0; i < slots.Len(); i++ {
		if fd := slots.Get(i); fd.Message() != nil && fd.Message().FullName() == name {
			msg.Set(fd, protoreflect.ValueOfMessage(proto.Clone(record).ProtoReflect()))
			break
		}
	}
	return ev
}
