package gateway

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nickheyer/nebu/pkg/events"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Fake OpenAI runtime streaming two words and usage.
func streamingUpstream(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"stream":true`) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"id":"c1","object":"chat.completion","model":"m1","choices":[{"index":0,"message":{"role":"assistant","content":"hello world"},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":2,"total_tokens":9}}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		for _, chunk := range []string{
			`{"id":"c1","object":"chat.completion.chunk","model":"m1","choices":[{"index":0,"delta":{"role":"assistant","content":"hello"}}]}`,
			`{"id":"c1","object":"chat.completion.chunk","model":"m1","choices":[{"index":0,"delta":{"content":" world"},"finish_reason":"stop"}]}`,
			`{"id":"c1","object":"chat.completion.chunk","model":"m1","choices":[],"usage":{"prompt_tokens":7,"completion_tokens":2,"total_tokens":9}}`,
		} {
			io.WriteString(w, "data: "+chunk+"\n\n")
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
		io.WriteString(w, "data: [DONE]\n\n")
	}))
}

func waitTrace(t *testing.T, g *Gateway, id string) *v1.Trace {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if tr, ok := g.Traces().Get(id); ok && tr.GetFinishedAt() != nil {
			return tr
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("trace %s never finished", id)
	return nil
}

func TestTraces(t *testing.T) {
	upstream := streamingUpstream(t)
	defer upstream.Close()
	bus := events.New()
	sub := bus.Subscribe(context.Background(), []v1.EventKind{v1.EventKind_EVENT_KIND_TRACE})
	g := New(tableOf(t, map[string]string{"m1": upstream.URL}), nil, nil, nil, bus, slog.New(slog.NewTextHandler(io.Discard, nil)))
	srv := httptest.NewServer(g.Handler())
	defer srv.Close()

	// Parse proxied streams for tracing.
	resp, err := http.Post(srv.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{"model":"m1","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	io.ReadAll(resp.Body)
	resp.Body.Close()
	id := resp.Header.Get(traceHeader)
	if id == "" {
		t.Fatal("no trace header")
	}
	tr := waitTrace(t, g, id)
	if tr.GetStatus() != 200 || tr.GetKind() != v1.TraceKind_TRACE_KIND_CHAT || !tr.GetStream() || tr.GetTranslated() {
		t.Fatalf("stream trace %v", tr)
	}
	if tr.GetPromptTokens() != 7 || tr.GetCompletionTokens() != 2 || tr.GetStop() != "stop" || tr.GetResponse() != "hello world" {
		t.Fatalf("stream usage %v", tr)
	}
	if tr.GetFirstByteAt() == nil || tr.GetFirstTokenAt() == nil || tr.GetRoute() != "m1" || tr.GetInstanceId() != "inst-m1" {
		t.Fatalf("stream timing %v", tr)
	}
	if !strings.Contains(tr.GetRequest(), `"model":"m1"`) || tr.GetUpstreamRequest() != "" {
		t.Fatalf("stream bodies %v", tr)
	}

	// Translated traces retain client, upstream, and response bodies.
	resp, err = http.Post(srv.URL+"/v1/messages", "application/json", strings.NewReader(`{"model":"m1","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	io.ReadAll(resp.Body)
	resp.Body.Close()
	tr = waitTrace(t, g, resp.Header.Get(traceHeader))
	if !tr.GetTranslated() || tr.GetClientApi() != v1.ApiFlavor_API_FLAVOR_ANTHROPIC || tr.GetUpstreamApi() != v1.ApiFlavor_API_FLAVOR_OPENAI {
		t.Fatalf("translated trace %v", tr)
	}
	if tr.GetPromptTokens() != 7 || tr.GetCompletionTokens() != 2 || tr.GetResponse() != "hello world" || !strings.Contains(tr.GetUpstreamRequest(), `"messages"`) {
		t.Fatalf("translated usage %v", tr)
	}

	// Trace failures with their status and reason.
	resp, _ = http.Post(srv.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{"model":"nope","messages":[]}`))
	io.ReadAll(resp.Body)
	resp.Body.Close()
	tr = waitTrace(t, g, resp.Header.Get(traceHeader))
	if tr.GetStatus() != 404 || !strings.Contains(tr.GetError(), "not running") || tr.GetRoute() != "nope" {
		t.Fatalf("refusal trace %v", tr)
	}

	// Lists omit bodies, sort newest first, and support route filters.
	all := g.Traces().List("", 0)
	if len(all) != 3 || all[0].GetRoute() != "nope" || all[0].GetRequest() != "" {
		t.Fatalf("list %v", all)
	}
	if only := g.Traces().List("m1", 1); len(only) != 1 || only[0].GetRoute() != "m1" {
		t.Fatalf("route list %v", only)
	}
	// Publish each trace at start and completion.
	seen := map[string]int{}
	timeout := time.After(time.Second)
	for len(seen) < 3 || seen[id] < 2 {
		select {
		case ev := <-sub.Events():
			seen[ev.GetId()]++
			if ev.GetTrace().GetRequest() != "" {
				t.Fatal("events carry no bodies")
			}
		case <-timeout:
			t.Fatalf("events %v", seen)
		}
	}

	// Token counts use a separate trace ring and no served request count.
	served := g.Status().GetRequests()
	resp, err = http.Post(srv.URL+"/v1/messages/count_tokens", "application/json", strings.NewReader(`{"model":"m1","messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	io.ReadAll(resp.Body)
	resp.Body.Close()
	tr = waitTrace(t, g, resp.Header.Get(traceHeader))
	if tr.GetKind() != v1.TraceKind_TRACE_KIND_COUNT || tr.GetStatus() != 200 || tr.GetPromptTokens() == 0 {
		t.Fatalf("count trace %v", tr)
	}
	if g.Status().GetRequests() != served {
		t.Fatal("a count was tallied as a request served")
	}
	if len(g.Traces().counts.items) != 1 || len(g.Traces().main.items) != 3 {
		t.Fatalf("count ring %d main ring %d", len(g.Traces().counts.items), len(g.Traces().main.items))
	}
	if all = g.Traces().List("", 0); len(all) != 4 || all[0].GetKind() != v1.TraceKind_TRACE_KIND_COUNT {
		t.Fatalf("merged list %v", all)
	}
}

func TestRecorderRing(t *testing.T) {
	r := NewRecorder(nil, 2, 1)
	at := func(sec int64) *timestamppb.Timestamp { return &timestamppb.Timestamp{Seconds: sec} }
	for i, id := range []string{"a", "b", "c"} {
		r.Start(&v1.Trace{Id: id, Route: "m", StartedAt: at(int64(i))})
	}
	if _, ok := r.Get("a"); ok {
		t.Fatal("oldest should be dropped")
	}
	list := r.List("", 0)
	if len(list) != 2 || list[0].GetId() != "c" || list[1].GetId() != "b" {
		t.Fatalf("ring order %v", list)
	}
	r.Finish(&v1.Trace{Id: "b", Route: "m", Status: 200, StartedAt: at(1)})
	if got, _ := r.Get("b"); got.GetStatus() != 200 || got.GetFinishedAt() == nil {
		t.Fatalf("finish %v", got)
	}
	// Token count traces cannot evict inference traces.
	r.Start(&v1.Trace{Id: "n1", Route: "m", Kind: v1.TraceKind_TRACE_KIND_COUNT, StartedAt: at(3)})
	r.Start(&v1.Trace{Id: "n2", Route: "m", Kind: v1.TraceKind_TRACE_KIND_COUNT, StartedAt: at(4)})
	if _, ok := r.Get("n1"); ok {
		t.Fatal("the count ring should hold one")
	}
	if _, ok := r.Get("b"); !ok {
		t.Fatal("a count dropped an answer")
	}
	list = r.List("", 0)
	if len(list) != 3 || list[0].GetId() != "n2" || list[1].GetId() != "c" || list[2].GetId() != "b" {
		t.Fatalf("merged order %v", list)
	}
	if list = r.List("", 1); len(list) != 1 || list[0].GetId() != "n2" {
		t.Fatalf("merged limit %v", list)
	}
	// List traces by start time across both rings.
	r.Start(&v1.Trace{Id: "n0", Route: "m", Kind: v1.TraceKind_TRACE_KIND_COUNT, StartedAt: at(0)})
	if list = r.List("", 0); list[len(list)-1].GetId() != "n0" {
		t.Fatalf("merge by start %v", list)
	}
}

func TestTableRename(t *testing.T) {
	table := tableOf(t, map[string]string{"main": "http://a", "other": "http://b"})
	if _, err := table.Rename("main", "other"); err == nil {
		t.Fatal("rename onto a taken name should fail")
	}
	if _, err := table.Rename("nope", "x"); err == nil {
		t.Fatal("rename of a missing route should fail")
	}
	r, err := table.Rename("main", "primary")
	if err != nil || r.GetName() != "primary" || r.GetEndpoint() != "http://a" {
		t.Fatalf("rename %v %v", r, err)
	}
	if _, ok := table.Lookup("main"); ok {
		t.Fatal("old name should be gone")
	}
	if got, ok := table.Lookup("primary"); !ok || got.GetInstanceId() != "inst-main" {
		t.Fatalf("new name %v", got)
	}
}
