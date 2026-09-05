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

const subscriberBuffer = 256

// Fans events out to subscribers, dropping oldest for slow ones
type Bus struct {
	mu   sync.Mutex
	seq  atomic.Uint64
	subs map[*Subscription]struct{}
}

// One subscriber's queue
type Subscription struct {
	kinds   map[v1.EventKind]bool
	ch      chan *v1.Event
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

// Subscribes to kinds, all when empty, until ctx ends, a nil bus delivering nothing
func (b *Bus) Subscribe(ctx context.Context, kinds []v1.EventKind) *Subscription {
	s := &Subscription{kinds: map[v1.EventKind]bool{}, ch: make(chan *v1.Event, subscriberBuffer)}
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
