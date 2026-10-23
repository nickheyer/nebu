package events

import (
	"context"
	"testing"
	"time"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

func TestPublishSubscribeFilter(t *testing.T) {
	b := New()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	all := b.Subscribe(ctx, nil)
	slots := b.Subscribe(ctx, []v1.EventKind{v1.EventKind_EVENT_KIND_SLOT})
	slot := &v1.Slot{Id: "s1", Name: "main"}
	ev := b.Publish(v1.EventKind_EVENT_KIND_SLOT, v1.EventAction_EVENT_ACTION_CREATED, "s1", slot)
	b.Publish(v1.EventKind_EVENT_KIND_TASK, v1.EventAction_EVENT_ACTION_UPDATED, "t1", &v1.Task{Id: "t1"})
	if ev.GetSeq() != 1 || ev.GetSlot().GetName() != "main" {
		t.Fatalf("published event wrong: %v", ev)
	}
	slot.Name = "changed"
	got := <-all.Events()
	if got.GetSlot().GetName() != "main" {
		t.Fatal("payload should be a copy")
	}
	if (<-all.Events()).GetKind() != v1.EventKind_EVENT_KIND_TASK {
		t.Fatal("second event should be the task")
	}
	if (<-slots.Events()).GetId() != "s1" {
		t.Fatal("slot subscriber should get the slot")
	}
	select {
	case ev := <-slots.Events():
		t.Fatalf("slot subscriber got %v", ev)
	case <-time.After(50 * time.Millisecond):
	}
}

// A full queue takes a record's newer update in place of its pending one, and drops the oldest
// event only when every pending record is a different one
func TestSlowSubscriberCoalescesThenDropsOldest(t *testing.T) {
	b := New()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := b.Subscribe(ctx, nil)
	drain := func(from uint64) (n int, last uint64) {
		last = from
		for {
			select {
			case ev := <-s.Events():
				if ev.GetSeq() <= last {
					t.Fatalf("seq %d after %d", ev.GetSeq(), last)
				}
				last, n = ev.GetSeq(), n+1
			case <-time.After(100 * time.Millisecond):
				return n, last
			}
		}
	}
	published := uint64(subscriberBuffer + 10)
	for i := uint64(0); i < published; i++ {
		b.Publish(v1.EventKind_EVENT_KIND_TASK, v1.EventAction_EVENT_ACTION_UPDATED, "t", nil)
	}
	n, last := drain(0)
	if s.Dropped() != 0 || last != published || n > subscriberBuffer+1 {
		t.Fatalf("same record: dropped %d, last seq %d of %d, delivered %d", s.Dropped(), last, published, n)
	}
	for i := uint64(0); i < published; i++ {
		b.Publish(v1.EventKind_EVENT_KIND_TASK, v1.EventAction_EVENT_ACTION_UPDATED, "t"+string(rune('a'+i%26))+string(rune('a'+i/26)), nil)
	}
	n, last = drain(published)
	if s.Dropped() == 0 || uint64(n)+s.Dropped() != published || last != 2*published {
		t.Fatalf("distinct records: dropped %d, delivered %d of %d, last seq %d", s.Dropped(), n, published, last)
	}
}

func TestUnsubscribeOnCancel(t *testing.T) {
	b := New()
	ctx, cancel := context.WithCancel(context.Background())
	b.Subscribe(ctx, nil)
	cancel()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		b.mu.Lock()
		n := len(b.subs)
		b.mu.Unlock()
		if n == 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("subscriber not removed after cancel")
}

func TestNilBusIsSafe(t *testing.T) {
	var b *Bus
	if b.Publish(v1.EventKind_EVENT_KIND_TASK, v1.EventAction_EVENT_ACTION_CREATED, "x", nil) != nil {
		t.Fatal("nil bus should publish nothing")
	}
	s := b.Subscribe(context.Background(), nil)
	if s == nil || s.Events() == nil {
		t.Fatal("nil bus subscribe should still return a subscription")
	}
}
