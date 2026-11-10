package db

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// A fixed instant traces start around
var traceBase = time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)

// A trace setting every field, its first tool call setting every tool call field
func fullTrace(id, route string, started time.Time) *v1.Trace {
	return &v1.Trace{
		Id: id, Route: route, InstanceId: "in-1", SlotId: "slot-1",
		ClientApi: v1.ApiFlavor_API_FLAVOR_ANTHROPIC, UpstreamApi: v1.ApiFlavor_API_FLAVOR_OPENAI,
		Path: "/v1/messages", Kind: v1.TraceKind_TRACE_KIND_CHAT, Stream: true,
		StartedAt:    timestamppb.New(started),
		FirstByteAt:  timestamppb.New(started.Add(40 * time.Millisecond)),
		FirstTokenAt: timestamppb.New(started.Add(55*time.Millisecond + 7)),
		FinishedAt:   timestamppb.New(started.Add(2*time.Second + 3)),
		Status:       502, Error: "upstream reset", PromptTokens: 120, CompletionTokens: 48, Stop: "tool",
		RequestBytes: 2048, ResponseBytes: 4096,
		Request: `{"messages":[{"role":"user","content":"hi"}]}`, UpstreamRequest: `{"model":"m","messages":[]}`, Response: "hello there",
		Translated: true, Remote: "10.0.0.7:5122",
		ToolCalls: []*v1.TraceToolCall{
			{Id: "call-1", Name: "read_file", Arguments: `{"path":"a"}`, Server: "fs", Result: "contents", Failed: true, TookMs: 31},
			{Id: "call-2", Name: "search", Arguments: "{}"},
		},
		Forwarded: true, NodeId: "node-b", ConductorTrace: "tr-remote", Seats: []string{"in-1", "in-2"}, Relay: "prefill+decode",
		DraftOffered: 64, DraftAccepted: 40, CachedTokens: 96, QueuedBehind: 3, Endpoint: "127.0.0.1:8081", PoolId: "pool-a",
		Profile: "default", Score: 0.875, QueueWaitMs: 12, FairnessId: "team-a", Priority: -2, Objective: "interactive",
		ObjectiveAttained: true, FailedOpen: true, Fallbacks: []*v1.TraceFallback{{Address: "127.0.0.1:8082", Reason: "connection refused"}, {Address: "127.0.0.1:8083", Reason: "503", RequestWritten: true, BytesToClient: 12}}, RequestId: "req-9", EntryNode: "node-a",
		Owner: "alice", Filters: []*v1.TraceFilter{{Name: "ready", Dropped: []string{"127.0.0.1:8084"}}}, Candidates: []*v1.TraceScore{{Endpoint: "127.0.0.1:8081", Score: 0.875, ByScorer: map[string]float64{"queue": 0.5}}},
	}
}

// A minimal trace on a route started at an offset from the base instant
func bareTrace(id, route string, offset time.Duration) *v1.Trace {
	return &v1.Trace{Id: id, Route: route, Kind: v1.TraceKind_TRACE_KIND_GENERATE, StartedAt: timestamppb.New(traceBase.Add(offset)), Request: "req " + id, Response: "resp " + id}
}

// The trace as a listing returns it, bodies cleared
func listed(t *v1.Trace) *v1.Trace {
	c := proto.Clone(t).(*v1.Trace)
	c.Request, c.UpstreamRequest, c.Response = "", "", ""
	return c
}

// Fails unless every field of the message is populated
func requireFull(t *testing.T, m protoreflect.Message) {
	t.Helper()
	fields := m.Descriptor().Fields()
	for i := 0; i < fields.Len(); i++ {
		if !m.Has(fields.Get(i)) {
			t.Fatalf("sample %s leaves %s unset", m.Descriptor().Name(), fields.Get(i).Name())
		}
	}
}

// Fails unless the listing holds exactly the wanted traces in order
func sameTraces(t *testing.T, got []*v1.Trace, want ...*v1.Trace) {
	t.Helper()
	ids := func(ts []*v1.Trace) string {
		var out []string
		for _, tr := range ts {
			out = append(out, tr.GetId())
		}
		return strings.Join(out, ",")
	}
	if len(got) != len(want) {
		t.Fatalf("listed %s, want %s", ids(got), ids(want))
	}
	for i := range want {
		if !proto.Equal(got[i], listed(want[i])) {
			t.Fatalf("listed %s, want %s\nat %d got %v\nwant %v", ids(got), ids(want), i, got[i], listed(want[i]))
		}
	}
}

func putTraces(t *testing.T, d *DB, owner string, traces ...*v1.Trace) {
	t.Helper()
	for _, tr := range traces {
		if err := d.PutTrace(context.Background(), owner, tr); err != nil {
			t.Fatal(err)
		}
	}
}

func listTraces(t *testing.T, d *DB, q TraceQuery) []*v1.Trace {
	t.Helper()
	got, err := d.ListTraces(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestTraces(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name string
		run  func(t *testing.T, d *DB)
	}{
		{"put then get keeps every field, tool call and body", func(t *testing.T, d *DB) {
			want := fullTrace("t1", "llama", traceBase.Add(123456789))
			want.Response = strings.Repeat("token ", 64<<10)
			requireFull(t, want.ProtoReflect())
			requireFull(t, want.GetToolCalls()[0].ProtoReflect())
			putTraces(t, d, "alice", want)
			got, err := d.GetTrace(ctx, "alice", false, "t1")
			if err != nil {
				t.Fatal(err)
			}
			if !proto.Equal(got, want) {
				t.Fatalf("got %v\nwant %v", got, want)
			}
		}},
		{"list leaves bodies off and orders newest first across sub second starts", func(t *testing.T, d *DB) {
			a, b, c := bareTrace("a", "r", 100*time.Millisecond), bareTrace("b", "r", 120*time.Millisecond), bareTrace("c", "r", 500*time.Millisecond)
			e, f := bareTrace("e", "r", time.Second), bareTrace("f", "r", 1250*time.Millisecond)
			full := fullTrace("g", "r", traceBase.Add(-time.Hour))
			putTraces(t, d, "alice", c, f, a, e, full, b)
			got := listTraces(t, d, TraceQuery{Owner: "alice"})
			sameTraces(t, got, f, e, c, b, a, full)
			for _, tr := range got {
				if tr.GetRequest() != "" || tr.GetUpstreamRequest() != "" || tr.GetResponse() != "" {
					t.Fatalf("listing of %s carries bodies", tr.GetId())
				}
			}
			if len(got[5].GetToolCalls()) != 2 || got[5].GetScore() != 0.875 {
				t.Fatalf("listing dropped fields %v", got[5])
			}
		}},
		{"another owner's trace is hidden unless every owner is asked for", func(t *testing.T, d *DB) {
			a, b := bareTrace("a1", "r", 0), bareTrace("b1", "r", time.Second)
			putTraces(t, d, "alice", a)
			putTraces(t, d, "bob", b)
			sameTraces(t, listTraces(t, d, TraceQuery{Owner: "alice"}), a)
			sameTraces(t, listTraces(t, d, TraceQuery{Owner: "bob"}), b)
			sameTraces(t, listTraces(t, d, TraceQuery{Owner: "carol"}))
			sameTraces(t, listTraces(t, d, TraceQuery{}))
			sameTraces(t, listTraces(t, d, TraceQuery{AllOwners: true}), b, a)
			sameTraces(t, listTraces(t, d, TraceQuery{Owner: "alice", AllOwners: true}), b, a)
			for _, g := range []struct {
				owner string
				any   bool
				found bool
			}{{"alice", false, true}, {"bob", false, false}, {"", false, false}, {"bob", true, true}, {"", true, true}} {
				got, err := d.GetTrace(ctx, g.owner, g.any, "a1")
				switch {
				case g.found && err != nil:
					t.Fatalf("owner %q any %v: %v", g.owner, g.any, err)
				case g.found && got.GetRequest() != a.GetRequest():
					t.Fatalf("owner %q any %v got %v", g.owner, g.any, got)
				case !g.found && !IsNotFound(err):
					t.Fatalf("owner %q any %v got %v, %v, want not found", g.owner, g.any, got, err)
				}
			}
		}},
		{"route filter keeps one route's traces", func(t *testing.T, d *DB) {
			l1, q1, l2, none := bareTrace("l1", "llama", 0), bareTrace("q1", "qwen", time.Second), bareTrace("l2", "llama", 2*time.Second), bareTrace("n1", "", 3*time.Second)
			putTraces(t, d, "alice", l1, q1, none)
			putTraces(t, d, "bob", l2)
			sameTraces(t, listTraces(t, d, TraceQuery{Owner: "alice", Route: "llama"}), l1)
			sameTraces(t, listTraces(t, d, TraceQuery{AllOwners: true, Route: "llama"}), l2, l1)
			sameTraces(t, listTraces(t, d, TraceQuery{AllOwners: true, Route: "qwen"}), q1)
			sameTraces(t, listTraces(t, d, TraceQuery{AllOwners: true, Route: "mistral"}))
			sameTraces(t, listTraces(t, d, TraceQuery{AllOwners: true}), none, l2, q1, l1)
		}},
		{"kinds filter keeps traces of any listed kind", func(t *testing.T, d *DB) {
			chat, embed, image := bareTrace("c1", "r", 0), bareTrace("e1", "r", time.Second), bareTrace("i1", "r", 2*time.Second)
			chat.Kind, embed.Kind, image.Kind = v1.TraceKind_TRACE_KIND_CHAT, v1.TraceKind_TRACE_KIND_EMBED, v1.TraceKind_TRACE_KIND_IMAGE
			putTraces(t, d, "alice", chat, embed, image)
			sameTraces(t, listTraces(t, d, TraceQuery{Owner: "alice", Kinds: []v1.TraceKind{v1.TraceKind_TRACE_KIND_EMBED}}), embed)
			sameTraces(t, listTraces(t, d, TraceQuery{Owner: "alice", Kinds: []v1.TraceKind{v1.TraceKind_TRACE_KIND_CHAT, v1.TraceKind_TRACE_KIND_IMAGE}}), image, chat)
			sameTraces(t, listTraces(t, d, TraceQuery{Owner: "alice", Kinds: []v1.TraceKind{v1.TraceKind_TRACE_KIND_VIDEO}}))
			sameTraces(t, listTraces(t, d, TraceQuery{Owner: "alice"}), image, embed, chat)
		}},
		{"limit keeps the newest matches", func(t *testing.T, d *DB) {
			var all []*v1.Trace
			for i := range 5 {
				all = append(all, bareTrace(fmt.Sprintf("t%d", i), "r", time.Duration(i)*time.Millisecond))
			}
			other := bareTrace("o", "other", time.Hour)
			putTraces(t, d, "alice", append(all, other)...)
			sameTraces(t, listTraces(t, d, TraceQuery{Owner: "alice", Route: "r", Limit: 2}), all[4], all[3])
			sameTraces(t, listTraces(t, d, TraceQuery{Owner: "alice", Limit: 1}), other)
			sameTraces(t, listTraces(t, d, TraceQuery{Owner: "alice", Route: "r", Limit: 10}), all[4], all[3], all[2], all[1], all[0])
			sameTraces(t, listTraces(t, d, TraceQuery{Owner: "alice", Route: "r"}), all[4], all[3], all[2], all[1], all[0])
			if _, err := d.ListTraces(ctx, TraceQuery{Owner: "alice", Limit: -1}); err == nil {
				t.Fatal("negative limit listed")
			}
		}},
		{"put with a stored id replaces the trace, owner and bodies", func(t *testing.T, d *DB) {
			first := fullTrace("t1", "llama", traceBase)
			putTraces(t, d, "alice", first)
			second := bareTrace("t1", "qwen", time.Minute)
			second.Status, second.Request, second.Response = 200, "second request", "second response"
			putTraces(t, d, "bob", second)
			got, err := d.GetTrace(ctx, "bob", false, "t1")
			if err != nil {
				t.Fatal(err)
			}
			if !proto.Equal(got, second) {
				t.Fatalf("got %v\nwant %v", got, second)
			}
			if _, err := d.GetTrace(ctx, "alice", false, "t1"); !IsNotFound(err) {
				t.Fatalf("former owner still reads it, %v", err)
			}
			sameTraces(t, listTraces(t, d, TraceQuery{AllOwners: true}), second)
			sameTraces(t, listTraces(t, d, TraceQuery{AllOwners: true, Route: "llama"}))
		}},
		{"prune removes only traces started before the cutoff and counts them", func(t *testing.T, d *DB) {
			old, half := bareTrace("old", "r", -time.Hour), bareTrace("half", "r", 500*time.Millisecond)
			at, after := bareTrace("at", "r", time.Second), bareTrace("after", "r", 1500*time.Millisecond)
			putTraces(t, d, "alice", old, after)
			putTraces(t, d, "bob", half, at)
			n, err := d.PruneTraces(ctx, traceBase.Add(time.Second))
			if err != nil {
				t.Fatal(err)
			}
			if n != 2 {
				t.Fatalf("pruned %d, want 2", n)
			}
			sameTraces(t, listTraces(t, d, TraceQuery{AllOwners: true}), after, at)
			if _, err := d.GetTrace(ctx, "", true, "half"); !IsNotFound(err) {
				t.Fatalf("pruned trace still read, %v", err)
			}
			if n, err := d.PruneTraces(ctx, traceBase.Add(time.Second).In(time.FixedZone("east", 5*3600))); err != nil || n != 0 {
				t.Fatalf("second prune %d, %v", n, err)
			}
		}},
		{"put refuses a trace without an id or a valid start", func(t *testing.T, d *DB) {
			noStart := bareTrace("s", "r", 0)
			noStart.StartedAt = nil
			badStart := bareTrace("b", "r", 0)
			badStart.StartedAt = &timestamppb.Timestamp{Seconds: 1, Nanos: -1}
			for _, tr := range []*v1.Trace{nil, bareTrace("", "r", 0), noStart, badStart} {
				if err := d.PutTrace(ctx, "alice", tr); err == nil {
					t.Fatalf("stored %v", tr)
				}
			}
			sameTraces(t, listTraces(t, d, TraceQuery{AllOwners: true}))
			if _, err := d.GetTrace(ctx, "", true, "missing"); !IsNotFound(err) {
				t.Fatalf("absent trace gave %v", err)
			}
		}},
		{"concurrent puts and lists all land", func(t *testing.T, d *DB) {
			var wg sync.WaitGroup
			for w := range 8 {
				wg.Go(func() {
					for i := range 20 {
						tr := bareTrace(fmt.Sprintf("w%d-%d", w, i), fmt.Sprintf("r%d", w%2), time.Duration(w*20+i)*time.Millisecond)
						if err := d.PutTrace(ctx, "alice", tr); err != nil {
							t.Error(err)
							return
						}
					}
				})
			}
			for range 4 {
				wg.Go(func() {
					for range 20 {
						if _, err := d.ListTraces(ctx, TraceQuery{Owner: "alice", Limit: 5}); err != nil {
							t.Error(err)
							return
						}
					}
				})
			}
			wg.Wait()
			if got := listTraces(t, d, TraceQuery{Owner: "alice"}); len(got) != 160 {
				t.Fatalf("stored %d, want 160", len(got))
			}
		}},
	}
	d, _ := open(t)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := d.sql.ExecContext(ctx, `DELETE FROM traces`); err != nil {
				t.Fatal(err)
			}
			tc.run(t, d)
		})
	}
}

// A fresh store and one upgraded from the release before traces both match schema.sql and keep traces
func TestTracesMigration(t *testing.T) {
	dropLegacy := func(ctx context.Context, d *DB) error { return d.DropLegacyChatFiles(ctx) }
	cases := []struct {
		name    string
		prepare func(t *testing.T, path string)
	}{
		{"fresh", func(t *testing.T, path string) {}},
		{"upgraded", func(t *testing.T, path string) { openBefore(t, path, "20261103090000_traces").Close() }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "nebu.db")
			tc.prepare(t, path)
			d, err := Open(path, dropLegacy)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { d.Close() })
			if len(d.Drift) != 0 {
				t.Fatalf("drift after migrating: %v", d.Drift)
			}
			if d.Baselined != "" {
				t.Fatalf("baselined: %s", d.Baselined)
			}
			want := fullTrace("t1", "llama", traceBase)
			putTraces(t, d, "alice", want)
			got, err := d.GetTrace(context.Background(), "alice", false, "t1")
			if err != nil {
				t.Fatal(err)
			}
			if !proto.Equal(got, want) {
				t.Fatalf("got %v\nwant %v", got, want)
			}
		})
	}
}
