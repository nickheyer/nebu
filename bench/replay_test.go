package bench

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nickheyer/nebu/pkg/requestcontrol"
	"github.com/nickheyer/nebu/pkg/stub"
)

// A ratio mode stub with a 30 ms first token and 10 ms per later token of wall time
func ratioStub(t *testing.T, id string) *stub.Stub {
	t.Helper()
	st, err := stub.New(stub.Config{ID: id, Mode: stub.ModeRatio, Speedup: 10, BaseLatency: 300 * time.Millisecond, DecodePerToken: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	return st
}

// Serves stubs in turn, naming the one that answered in the served endpoint header as a gateway does
func roundRobin(stubs ...*stub.Stub) http.Handler {
	var n atomic.Uint64
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i := int(n.Add(1)-1) % len(stubs)
		w.Header().Set(requestcontrol.HeaderServed, fmt.Sprintf("stub-%d", i))
		stubs[i].ServeHTTP(w, r)
	})
}

// Requests spaced gap apart, each asking for out tokens
func evenTrace(n int, gap time.Duration, out int, stream bool) []Request {
	reqs := make([]Request, n)
	for i := range reqs {
		reqs[i] = Request{OffsetMS: ms(time.Duration(i) * gap), Model: "stub-model", PromptTokens: 20, MaxTokens: out, Stream: stream}
	}
	return reqs
}

func TestReplayMeasuresStubStream(t *testing.T) {
	srv := httptest.NewServer(roundRobin(ratioStub(t, "a"), ratioStub(t, "b")))
	defer srv.Close()
	reqs := evenTrace(12, 5*time.Millisecond, 6, true)
	rep, err := ReplayRequests(context.Background(), srv.URL, "", reqs, Config{Name: "ratio"})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Name != "ratio" || rep.Requests != 12 || rep.Succeeded != 12 || rep.Streamed != 12 || rep.Errors+rep.Shed+rep.Unavailable != 0 {
		t.Fatalf("report %+v", rep)
	}
	if rep.OutputTokens != 12*6 || rep.PromptTokens <= 0 {
		t.Fatalf("output tokens %d prompt tokens %d", rep.OutputTokens, rep.PromptTokens)
	}
	if rep.TTFT.Count != 12 || rep.TPOT.Count != 12 || rep.E2E.Count != 12 {
		t.Fatalf("populations ttft %d tpot %d e2e %d", rep.TTFT.Count, rep.TPOT.Count, rep.E2E.Count)
	}
	if rep.TTFT.P50 < 30 || rep.TTFT.P50 > 120 {
		t.Fatalf("TTFT p50 %v ms, the stub takes 30", rep.TTFT.P50)
	}
	if rep.TPOT.P50 < 8 || rep.TPOT.P50 > 30 {
		t.Fatalf("TPOT p50 %v ms, the stub takes 10", rep.TPOT.P50)
	}
	if rep.E2E.P50 < rep.TTFT.P50 || rep.E2E.P50 < 30+5*10 {
		t.Fatalf("E2E p50 %v ms under TTFT %v plus five tokens", rep.E2E.P50, rep.TTFT.P50)
	}
	if rep.Endpoints["stub-0"] != 6 || rep.Endpoints["stub-1"] != 6 || rep.EndpointCV() != 0 {
		t.Fatalf("endpoints %v", rep.Endpoints)
	}
	if rep.RequestsPerSecond <= 0 || rep.OutputTokensPerSecond <= rep.RequestsPerSecond {
		t.Fatalf("rates %v req/s %v tok/s", rep.RequestsPerSecond, rep.OutputTokensPerSecond)
	}
	if want := float64(rep.Succeeded) / (rep.DurationMS / 1000); math.Abs(rep.RequestsPerSecond-want) > 1e-6 {
		t.Fatalf("req/s %v, succeeded over duration gives %v", rep.RequestsPerSecond, want)
	}
}

func TestReplayMeasuresWholeAnswers(t *testing.T) {
	srv := httptest.NewServer(ratioStub(t, "a"))
	defer srv.Close()
	rep, err := ReplayRequests(context.Background(), srv.URL, "", evenTrace(4, time.Millisecond, 5, false), Config{})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Succeeded != 4 || rep.Streamed != 0 || rep.TTFT.Count != 0 || rep.TPOT.Count != 0 || rep.E2E.Count != 4 || rep.OutputTokens != 20 {
		t.Fatalf("report %+v", rep)
	}
	if rep.E2E.P50 < 30+4*10 {
		t.Fatalf("E2E p50 %v ms, the stub takes 70", rep.E2E.P50)
	}
}

func TestReplayCountsTokensWithoutUsage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body chatRequest
		json.NewDecoder(r.Body).Decode(&body)
		if body.Stream {
			io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"\"}}]}\n\n")
			for _, word := range []string{"one", " two", " three"} {
				fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", word)
			}
			io.WriteString(w, "data: [DONE]\n\n")
			return
		}
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"one two three four"}}]}`)
	}))
	defer srv.Close()
	reqs := []Request{{Prompt: "a", Stream: true}, {Prompt: "b"}}
	rep, err := ReplayRequests(context.Background(), srv.URL, "", reqs, Config{})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Succeeded != 2 || rep.OutputTokens != 3+4 || rep.TTFT.Count != 1 || rep.TPOT.Count != 1 {
		t.Fatalf("report %+v", rep)
	}
}

func TestReplayClassifiesFailures(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body chatRequest
		json.NewDecoder(r.Body).Decode(&body)
		switch body.Model {
		case "shed":
			http.Error(w, `{"error":{"message":"queue full"}}`, http.StatusTooManyRequests)
		case "unavailable":
			http.Error(w, `{"error":{"message":"no endpoint"}}`, http.StatusServiceUnavailable)
		case "boom":
			http.Error(w, "kaput", http.StatusInternalServerError)
		case "cut":
			io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"x\"}}]}\n\n")
		case "garbled":
			io.WriteString(w, "data: {nope\n\n")
		case "event":
			io.WriteString(w, "data: {\"error\":{\"message\":\"engine died\"}}\n\ndata: [DONE]\n\n")
		default:
			io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"x\"}}]}\n\ndata: [DONE]\n\n")
		}
	}))
	defer srv.Close()
	var reqs []Request
	for _, m := range []string{"shed", "shed", "unavailable", "boom", "cut", "garbled", "event", "ok"} {
		reqs = append(reqs, Request{Model: m, Prompt: "p", Stream: true})
	}
	rep, err := ReplayRequests(context.Background(), srv.URL, "", reqs, Config{})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Shed != 2 || rep.Unavailable != 1 || rep.Errors != 4 || rep.Succeeded != 1 {
		t.Fatalf("shed %d unavailable %d errors %d succeeded %d", rep.Shed, rep.Unavailable, rep.Errors, rep.Succeeded)
	}
	if rep.Statuses[429] != 2 || rep.Statuses[503] != 1 || rep.Statuses[500] != 1 || len(rep.Statuses) != 3 {
		t.Fatalf("statuses %v", rep.Statuses)
	}
	joined := strings.Join(rep.Failures, "\n")
	for _, want := range []string{"status 429 queue full", "status 503 no endpoint", "status 500 kaput", "stream ended before [DONE]", "malformed stream chunk", "stream error: engine died"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("failures lack %q\n%s", want, joined)
		}
	}
	if len(rep.Failures) != 6 {
		t.Fatalf("repeated failures kept twice %q", rep.Failures)
	}
	if rep.E2E.Count != 1 {
		t.Fatalf("E2E counts %d answers, failures included", rep.E2E.Count)
	}
}

func TestReplaySendsHeadersAndBody(t *testing.T) {
	type seen struct {
		header http.Header
		body   map[string]any
	}
	var mu sync.Mutex
	var got []seen
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		got = append(got, seen{r.Header.Clone(), body})
		mu.Unlock()
		io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":3,"completion_tokens":1}}`)
	}))
	defer srv.Close()
	reqs := []Request{{Model: "m", Messages: []Message{{Role: "system", Content: "s"}, {Role: "user", Content: "u"}}, MaxTokens: 9, Objective: "interactive", FairnessID: "tenant-2"}}
	if _, err := ReplayRequests(context.Background(), srv.URL+"/", "secret", reqs, Config{}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("%d requests arrived", len(got))
	}
	h, b := got[0].header, got[0].body
	if h.Get("Authorization") != "Bearer secret" || h.Get(requestcontrol.HeaderObjective) != "interactive" || h.Get(requestcontrol.HeaderFairnessID) != "tenant-2" || !strings.HasPrefix(h.Get(requestcontrol.HeaderRequestID), "bench-") {
		t.Fatalf("headers %v", h)
	}
	if b["model"] != "m" || b["max_tokens"] != float64(9) || b["stream"] != false || b["stream_options"] != nil || len(b["messages"].([]any)) != 2 {
		t.Fatalf("body %v", b)
	}
	body, _ := chatBody(0, Request{PromptTokens: 7, Stream: true})
	var parsed chatRequest
	json.Unmarshal(body, &parsed)
	if text, _ := parsed.Messages[0].Content.(string); len(strings.Fields(text)) != 7 || parsed.StreamOptions == nil || !parsed.StreamOptions.IncludeUsage {
		t.Fatalf("token count body %s", body)
	}
	body, _ = chatBody(0, Request{Messages: []Message{{Role: "user", Content: "look", Images: []string{"https://x/a.png", "data:image/png;base64,AAAA"}}, {Role: "user", Images: []string{"https://x/b.png"}}}})
	want := `"messages":[{"role":"user","content":[{"type":"text","text":"look"},{"type":"image_url","image_url":{"url":"https://x/a.png"}},{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}}]},{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://x/b.png"}}]}]`
	if !strings.Contains(string(body), want) {
		t.Fatalf("image body %s", body)
	}
}

func TestReplayFollowsArrivals(t *testing.T) {
	var mu sync.Mutex
	var at []time.Time
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		at = append(at, time.Now())
		mu.Unlock()
		io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}))
	defer srv.Close()
	reqs := []Request{{OffsetMS: 400, Prompt: "c"}, {OffsetMS: 0, Prompt: "a"}, {OffsetMS: 200, Prompt: "b"}}
	before := time.Now()
	rep, err := ReplayRequests(context.Background(), srv.URL, "", reqs, Config{Speed: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(at) != 3 {
		t.Fatalf("%d arrivals", len(at))
	}
	// The replay starts after before and never dispatches early, so no arrival precedes its scaled offset from before
	for i, want := range []time.Duration{0, 100 * time.Millisecond, 200 * time.Millisecond} {
		if got := at[i].Sub(before); got < want {
			t.Fatalf("arrival %d came %v after the replay began, want %v at speed two", i, got, want)
		}
		if got := at[i].Sub(at[0]); got > want+60*time.Millisecond {
			t.Fatalf("arrival %d came %v after the first, want %v at speed two", i, got, want)
		}
	}
	if rep.Lag.Count != 3 || rep.Lag.Max > 50 {
		t.Fatalf("dispatch lag %+v", rep.Lag)
	}
	if rep.DurationMS < 200 {
		t.Fatalf("duration %v ms for a 200 ms trace", rep.DurationMS)
	}
}

func TestReplayBoundsConcurrency(t *testing.T) {
	var cur, peak atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := cur.Add(1)
		for p := peak.Load(); n > p && !peak.CompareAndSwap(p, n); p = peak.Load() {
		}
		time.Sleep(30 * time.Millisecond)
		cur.Add(-1)
		io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}))
	defer srv.Close()
	rep, err := ReplayRequests(context.Background(), srv.URL, "", evenTrace(8, 0, 1, false), Config{Concurrency: 2})
	if err != nil {
		t.Fatal(err)
	}
	if peak.Load() != 2 || rep.Succeeded != 8 || rep.Concurrency != 2 {
		t.Fatalf("peak %d succeeded %d", peak.Load(), rep.Succeeded)
	}
	if rep.Lag.Max < 60 {
		t.Fatalf("queued requests left %v ms late at most, four rounds of 30 ms precede the last", rep.Lag.Max)
	}
}

func TestReplayStopsOnCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := ReplayRequests(ctx, srv.URL, "", []Request{{Prompt: "a"}, {OffsetMS: 10000, Prompt: "b"}}, Config{})
	if err == nil || !strings.Contains(err.Error(), "after 1 of 2") {
		t.Fatalf("err %v", err)
	}
}

func TestReplayTimesOutSlowAnswers(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	defer srv.Close()
	rep, err := ReplayRequests(context.Background(), srv.URL, "", []Request{{Prompt: "a"}}, Config{Timeout: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Errors != 1 || !strings.Contains(rep.Failures[0], "deadline exceeded") {
		t.Fatalf("report %+v", rep)
	}
}

func TestReplayRefuses(t *testing.T) {
	ok := []Request{{Prompt: "a"}}
	cases := []struct {
		name string
		url  string
		reqs []Request
		cfg  Config
	}{
		{"no scheme", "localhost:1", ok, Config{}},
		{"other scheme", "ftp://h", ok, Config{}},
		{"no host", "http://", ok, Config{}},
		{"negative speed", "http://h", ok, Config{Speed: -1}},
		{"infinite speed", "http://h", ok, Config{Speed: math.Inf(1)}},
		{"negative concurrency", "http://h", ok, Config{Concurrency: -1}},
		{"negative timeout", "http://h", ok, Config{Timeout: -1}},
		{"no requests", "http://h", nil, Config{}},
		{"invalid request", "http://h", []Request{{}}, Config{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ReplayRequests(context.Background(), tc.url, "", tc.reqs, tc.cfg); err == nil {
				t.Fatal("replay accepted")
			}
		})
	}
}

func TestReplayReadsTraceFile(t *testing.T) {
	srv := httptest.NewServer(ratioStub(t, "a"))
	defer srv.Close()
	path := t.TempDir() + "/t.jsonl"
	if err := WriteFile(path, evenTrace(3, time.Millisecond, 2, true)); err != nil {
		t.Fatal(err)
	}
	rep, err := Replay(context.Background(), srv.URL, "", path, Config{Name: "file"})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Trace != path || rep.Succeeded != 3 {
		t.Fatalf("report %+v", rep)
	}
	if _, err := Replay(context.Background(), srv.URL, "", path+".missing", Config{}); err == nil {
		t.Fatal("a missing trace replayed")
	}
}
