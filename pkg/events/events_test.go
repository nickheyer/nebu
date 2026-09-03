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
	ev := b.Publish(v1.EventKind_EVENT_KIND_SLOT, v1.EventAction_EVENT_ACTION_CREATED, "s1", &v1.Event_Slot{Slot: slot})
	b.Publish(v1.EventKind_EVENT_KIND_TASK, v1.EventAction_EVENT_ACTION_UPDATED, "t1", &v1.Event_Task{Task: &v1.Task{Id: "t1"}})
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

func TestSlowSubscriberDropsOldest(t *testing.T) {
	b := New()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := b.Subscribe(ctx, nil)
	for i := 0; i < subscriberBuffer+10; i++ {
		b.Publish(v1.EventKind_EVENT_KIND_TASK, v1.EventAction_EVENT_ACTION_UPDATED, "t", nil)
	}
	if s.Dropped() != 10 {
		t.Fatalf("dropped %d want 10", s.Dropped())
	}
	first := <-s.Events()
	if first.GetSeq() != 11 {
		t.Fatalf("oldest retained seq %d want 11", first.GetSeq())
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
