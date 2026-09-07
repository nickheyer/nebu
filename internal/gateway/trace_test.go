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
)

// A runtime that streams two words and a usage line in the OpenAI shape
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

	// A passed through stream is read on its way past
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

	// A translated request keeps both bodies and the answer
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

	// A refusal is traced with its status and reason
	resp, _ = http.Post(srv.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{"model":"nope","messages":[]}`))
	io.ReadAll(resp.Body)
	resp.Body.Close()
	tr = waitTrace(t, g, resp.Header.Get(traceHeader))
	if tr.GetStatus() != 404 || !strings.Contains(tr.GetError(), "not running") || tr.GetRoute() != "nope" {
		t.Fatalf("refusal trace %v", tr)
	}

	// Lists are newest first without bodies, one route's when asked
	all := g.Traces().List("", 0)
	if len(all) != 3 || all[0].GetRoute() != "nope" || all[0].GetRequest() != "" {
		t.Fatalf("list %v", all)
	}
	if only := g.Traces().List("m1", 1); len(only) != 1 || only[0].GetRoute() != "m1" {
		t.Fatalf("route list %v", only)
	}
	// Every trace reached the stream twice, once starting and once done
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
}

func TestRecorderRing(t *testing.T) {
	r := NewRecorder(nil, 2)
	for _, id := range []string{"a", "b", "c"} {
		r.Start(&v1.Trace{Id: id, Route: "m"})
	}
	if _, ok := r.Get("a"); ok {
		t.Fatal("oldest should be dropped")
	}
	list := r.List("", 0)
	if len(list) != 2 || list[0].GetId() != "c" || list[1].GetId() != "b" {
		t.Fatalf("ring order %v", list)
	}
	r.Finish(&v1.Trace{Id: "b", Route: "m", Status: 200})
	if got, _ := r.Get("b"); got.GetStatus() != 200 || got.GetFinishedAt() == nil {
		t.Fatalf("finish %v", got)
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
