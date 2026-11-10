package gateway

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/nickheyer/nebu/internal/db"
	"github.com/nickheyer/nebu/pkg/events"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// A trace store refusing the first writes of every trace
type flakyStore struct {
	mu      sync.Mutex
	refuse  int
	tries   map[string]int
	written map[string]*v1.Trace
}

func (s *flakyStore) PutTrace(_ context.Context, owner string, t *v1.Trace) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tries[t.GetId()]++
	if s.tries[t.GetId()] <= s.refuse {
		return errors.New("database is locked")
	}
	s.written[t.GetId()] = t
	return nil
}

func (s *flakyStore) PruneTraces(context.Context, time.Time) (int64, error) { return 0, nil }

func (s *flakyStore) ListTraces(context.Context, db.TraceQuery) ([]*v1.Trace, error) {
	return nil, nil
}

func (s *flakyStore) GetTrace(context.Context, string, bool, string) (*v1.Trace, error) {
	return nil, db.ErrNotFound
}

func (s *flakyStore) attempts(id string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tries[id]
}

func (s *flakyStore) stored(id string) *v1.Trace {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.written[id]
}

// A refused trace write is retried until the store takes it, while running and while draining
func TestRecorderRetriesRefusedWrites(t *testing.T) {
	store := &flakyStore{refuse: 3, tries: map[string]int{}, written: map[string]*v1.Trace{}}
	rec := newRecorder(store, events.New(), slog.New(slog.DiscardHandler), SystemClock, 4)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		rec.run(ctx, time.Hour)
	}()
	running := &v1.Trace{Id: "running", StartedAt: timestamppb.Now()}
	slot := rec.start(running, "alice")
	rec.finish(running, "alice", slot)
	waitFor(t, "the running write to land", func() bool { return store.stored("running") != nil })
	if store.attempts("running") != 4 || store.stored("running").GetOwner() != "alice" {
		t.Fatalf("running trace took %d attempts, stored %v", store.attempts("running"), store.stored("running"))
	}
	cancel()
	<-done
	stopped := &v1.Trace{Id: "stopped", StartedAt: timestamppb.Now()}
	slot = rec.start(stopped, "bob")
	rec.finish(stopped, "bob", slot)
	if store.attempts("stopped") != 4 || store.stored("stopped") == nil {
		t.Fatalf("trace finished after the writer stopped took %d attempts, stored %v", store.attempts("stopped"), store.stored("stopped"))
	}
}

// Every trace finishing while the writer stops reaches the store, queued before the stop or written at once after it
func TestRecorderKeepsTracesFinishingDuringStop(t *testing.T) {
	const finishers, each = 8, 64
	store := &flakyStore{tries: map[string]int{}, written: map[string]*v1.Trace{}}
	rec := newRecorder(store, events.New(), slog.New(slog.DiscardHandler), SystemClock, 4)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		rec.run(ctx, time.Hour)
	}()
	var wg sync.WaitGroup
	for f := range finishers {
		wg.Go(func() {
			for i := range each {
				tr := &v1.Trace{Id: fmt.Sprintf("t%d-%d", f, i), StartedAt: timestamppb.Now()}
				rec.finish(tr, "alice", rec.start(tr, "alice"))
			}
		})
	}
	cancel()
	wg.Wait()
	<-done
	for f := range finishers {
		for i := range each {
			if id := fmt.Sprintf("t%d-%d", f, i); store.stored(id) == nil {
				t.Fatalf("trace %s finished during the stop never reached the store", id)
			}
		}
	}
}

// Summaries carry tool call names and outcomes without their arguments and results
func TestSummaryKeepsBodiesOut(t *testing.T) {
	full := &v1.Trace{Id: "t", Request: "req", UpstreamRequest: "up", Response: "resp", ToolCalls: []*v1.TraceToolCall{
		{Id: "c1", Name: "fake__echo", Arguments: `{"text":"secret"}`, Server: "fake", Result: "echo: secret", TookMs: 3},
		{Id: "c2", Name: "lookup", Arguments: `{"q":"private"}`},
	}}
	s := summary(full)
	if s.GetRequest() != "" || s.GetUpstreamRequest() != "" || s.GetResponse() != "" {
		t.Fatalf("summary carries bodies %v", s)
	}
	if len(s.GetToolCalls()) != 2 || s.GetToolCalls()[0].GetArguments() != "" || s.GetToolCalls()[0].GetResult() != "" || s.GetToolCalls()[1].GetArguments() != "" {
		t.Fatalf("summary carries tool bodies %v", s.GetToolCalls())
	}
	if c := s.GetToolCalls()[0]; c.GetName() != "fake__echo" || c.GetServer() != "fake" || c.GetTookMs() != 3 {
		t.Fatalf("summary lost tool call facts %v", c)
	}
	if full.GetToolCalls()[0].GetArguments() == "" || full.GetRequest() == "" {
		t.Fatal("summary changed the full trace")
	}
}

// Each filter admits a trace matching any value it lists, an empty one admits all, and the filters narrow together
func TestTraceFilterTakesSeveralValues(t *testing.T) {
	chat, embed := v1.TraceKind_TRACE_KIND_CHAT, v1.TraceKind_TRACE_KIND_EMBED
	traces := map[string]*v1.Trace{
		"ok chat":      {Kind: chat, Status: 200, NodeId: "n1", Objective: "fast"},
		"shed chat":    {Kind: chat, Status: 429, NodeId: "n2", EntryNode: "n1"},
		"failed embed": {Kind: embed, Status: 502, NodeId: "n3", Objective: "bulk"},
	}
	cases := []struct {
		name string
		f    TraceFilter
		want []string
	}{
		{"no filter", TraceFilter{}, []string{"failed embed", "ok chat", "shed chat"}},
		{"one kind", TraceFilter{Kinds: []v1.TraceKind{embed}}, []string{"failed embed"}},
		{"two kinds", TraceFilter{Kinds: []v1.TraceKind{chat, embed}}, []string{"failed embed", "ok chat", "shed chat"}},
		{"two status classes", TraceFilter{StatusClasses: []uint32{4, 5}}, []string{"failed embed", "shed chat"}},
		{"a node by answer or entry", TraceFilter{NodeIDs: []string{"n1"}}, []string{"ok chat", "shed chat"}},
		{"two nodes", TraceFilter{NodeIDs: []string{"n2", "n3"}}, []string{"failed embed", "shed chat"}},
		{"two objectives", TraceFilter{Objectives: []string{"fast", "bulk"}}, []string{"failed embed", "ok chat"}},
		{"every filter at once", TraceFilter{Kinds: []v1.TraceKind{chat}, StatusClasses: []uint32{2, 4}, NodeIDs: []string{"n2"}}, []string{"shed chat"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var got []string
			for name, tr := range traces {
				if c.f.admits(tr) {
					got = append(got, name)
				}
			}
			slices.Sort(got)
			if !slices.Equal(got, c.want) {
				t.Fatalf("admitted %v, want %v", got, c.want)
			}
		})
	}
}
