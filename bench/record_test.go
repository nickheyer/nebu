package bench

import (
	"bytes"
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// A gateway holding a pool with a resident adapter, a route to it and traces of every kind
func recordGateway(t *testing.T, t0 time.Time) *fakeGateway {
	t.Helper()
	g := newFakeGateway(t, "default")
	at := func(d time.Duration) *timestamppb.Timestamp { return timestamppb.New(t0.Add(d)) }
	g.locked(func() {
		g.pools["p"] = &v1.Pool{Id: "p", Spec: &v1.PoolSpec{Model: "stub-model"}, Status: &v1.PoolStatus{Endpoints: []*v1.Endpoint{
			{Id: "p/a", Labels: map[string]string{"served": "alias"}, Metrics: &v1.EndpointMetrics{RunningAdapters: []string{"lora-a"}}},
		}}}
		g.routes["bench-model"] = &v1.Route{Name: "bench-model", Spec: &v1.RouteSpec{Rules: []*v1.RewriteRule{{Targets: []*v1.RewriteTarget{{PoolId: "p", Model: "stub-model", Weight: 1}}}}}}
		g.traces = []*v1.Trace{
			{Id: "early", Route: "bench-model", Kind: v1.TraceKind_TRACE_KIND_CHAT, StartedAt: at(-time.Hour), PoolId: "p", Path: "/v1/chat/completions",
				Request: `{"model":"bench-model","messages":[{"role":"user","content":"old"}]}`},
			{Id: "chat", Route: "bench-model", Kind: v1.TraceKind_TRACE_KIND_CHAT, ClientApi: v1.ApiFlavor_API_FLAVOR_OPENAI, Path: "/v1/chat/completions",
				StartedAt: at(0), Stream: true, PromptTokens: 30, CompletionTokens: 12, FairnessId: "tenant-1", Objective: "interactive", PoolId: "p",
				Request:         `{"model":"bench-model","max_tokens":50,"messages":[{"role":"system","content":"be brief"},{"role":"user","content":[{"type":"text","text":"hello"},{"type":"image_url","image_url":{"url":"https://x/cat.png"}}]},{"role":"tool","tool_call_id":"c1","content":"42"},{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,iVBORw0KGgo="}}]}]}`,
				UpstreamRequest: `{"model":"stub-model"}`},
			{Id: "embed", Route: "bench-model", Kind: v1.TraceKind_TRACE_KIND_EMBED, StartedAt: at(100 * time.Millisecond), PoolId: "p"},
			{Id: "generate", Route: "lora-a", Kind: v1.TraceKind_TRACE_KIND_GENERATE, ClientApi: v1.ApiFlavor_API_FLAVOR_OLLAMA, Path: "/api/generate",
				StartedAt: at(250 * time.Millisecond), PromptTokens: 6, PoolId: "p", Priority: 3,
				Request: `{"model":"lora-a","system":"you are terse","prompt":"why is the sky blue","stream":false,"options":{"num_predict":64}}`, UpstreamRequest: `{"model":"stub-model"}`},
			{Id: "capped", Route: "alias", Kind: v1.TraceKind_TRACE_KIND_CHAT, Path: "/v1/chat/completions", StartedAt: at(time.Second), PromptTokens: 77, CompletionTokens: 9, PoolId: "p",
				Request: `{"model":"alias","messages":[{"role":"user","content":"trunc`},
		}
	})
	return g
}

func TestRecordMapsTraces(t *testing.T) {
	t0 := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	g := recordGateway(t, t0)
	var out bytes.Buffer
	sum, err := Record(context.Background(), g.api, &out, RecordOptions{Limit: 100, Since: t0.Add(-time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if sum.Listed != 4 || sum.Recorded != 3 || !reflect.DeepEqual(sum.Excluded, map[string]int{"embed": 1}) {
		t.Fatalf("summary %+v", sum)
	}
	got, err := Read(&out)
	if err != nil {
		t.Fatal(err)
	}
	want := []Request{
		{OffsetMS: 0, Model: "bench-model", Stream: true, PromptTokens: 30, MaxTokens: 12, Objective: "interactive", FairnessID: "tenant-1", Messages: []Message{
			{Role: "system", Content: "be brief"}, {Role: "user", Content: "hello", Images: []string{"https://x/cat.png"}}, {Role: "user", Content: "42"}, {Role: "user", Images: []string{"data:image/png;base64,iVBORw0KGgo="}},
		}},
		{OffsetMS: 250, Model: "lora-a", Adapter: "lora-a", PromptTokens: 6, MaxTokens: 64, Objective: "3", Messages: []Message{
			{Role: "system", Content: "you are terse"}, {Role: "user", Content: "why is the sky blue"},
		}},
		{OffsetMS: 1000, Model: "alias", PromptTokens: 77, MaxTokens: 9},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("recorded\n%+v\nwant\n%+v", got, want)
	}
}

func TestRecordFiltersAndRefuses(t *testing.T) {
	t0 := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	g := recordGateway(t, t0)
	var out bytes.Buffer
	sum, err := Record(context.Background(), g.api, &out, RecordOptions{Limit: 2, Route: "bench-model"})
	if err != nil {
		t.Fatal(err)
	}
	if sum.Listed != 2 || sum.Recorded != 1 || sum.Excluded["embed"] != 1 || strings.Count(out.String(), "\n") != 1 {
		t.Fatalf("summary %+v output %s", sum, out.String())
	}
	if _, err := Record(context.Background(), g.api, &out, RecordOptions{}); err == nil {
		t.Fatal("a zero limit recorded")
	}
	g.locked(func() { delete(g.pools, "p") })
	if _, err := Record(context.Background(), g.api, &out, RecordOptions{Limit: 10}); err == nil || !strings.Contains(err.Error(), "pool p no longer exists") {
		t.Fatalf("err %v", err)
	}
	g.locked(func() {
		g.pools["p"] = &v1.Pool{Id: "p", Spec: &v1.PoolSpec{Model: "stub-model"}}
		g.traces = []*v1.Trace{{Id: "bare", Kind: v1.TraceKind_TRACE_KIND_CHAT, StartedAt: timestamppb.New(t0), PoolId: "p", Request: "{"}}
	})
	if _, err := Record(context.Background(), g.api, &out, RecordOptions{Limit: 10}); err == nil || !strings.Contains(err.Error(), "no prompt tokens") {
		t.Fatalf("err %v", err)
	}
}

func TestAdapterOf(t *testing.T) {
	pools := map[string]*v1.Pool{"p": {Id: "p", Spec: &v1.PoolSpec{Model: "base"}, Status: &v1.PoolStatus{Endpoints: []*v1.Endpoint{
		{Labels: map[string]string{"served": "base-alias", "adapter": "pinned"}, Metrics: &v1.EndpointMetrics{RunningAdapters: []string{"run"}, WaitingAdapters: []string{"wait"}}},
	}}}}
	bases := baseNames{"p": {"public": true}}
	cases := []struct {
		name  string
		pool  string
		names []string
		want  string
	}{
		{"no pool", "", []string{"x"}, ""},
		{"no names", "p", []string{"", ""}, ""},
		{"base model", "p", []string{"base"}, ""},
		{"served alias", "p", []string{"base-alias"}, ""},
		{"route base", "p", []string{"public"}, ""},
		{"running adapter", "p", []string{"run"}, "run"},
		{"waiting adapter", "p", []string{"wait"}, "wait"},
		{"pinned adapter", "p", []string{"pinned"}, "pinned"},
		{"adapter behind a base rewrite", "p", []string{"base", "run"}, "run"},
		{"unloaded adapter", "p", []string{"gone", "public"}, "gone"},
		{"base ahead of a route name", "p", []string{"", "base", "unknown-route"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := adapterOf(tc.pool, tc.names, pools, bases)
			if err != nil || got != tc.want {
				t.Fatalf("got %q, %v, want %q", got, err, tc.want)
			}
		})
	}
}

func TestBaseNamesOf(t *testing.T) {
	routes := []*v1.Route{
		{Name: "user", Spec: &v1.RouteSpec{Rules: []*v1.RewriteRule{{Match: "m", Targets: []*v1.RewriteTarget{{PoolId: "p", Model: "target"}}}}}},
		{Name: "owned", OwnerKind: "slot", Spec: &v1.RouteSpec{Rules: []*v1.RewriteRule{{Match: "slot-name", Targets: []*v1.RewriteTarget{{PoolId: "q", Model: "q-model"}}}}}},
		{Name: "plain", Spec: &v1.RouteSpec{Rules: []*v1.RewriteRule{{Targets: []*v1.RewriteTarget{{PoolId: "r"}}}}}},
	}
	want := baseNames{"p": {"target": true}, "q": {"q-model": true, "slot-name": true}, "r": {"plain": true}}
	if got := baseNamesOf(routes); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
}
