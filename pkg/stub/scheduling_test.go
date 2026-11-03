package stub

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// Sends a completion and returns its decoded body
func complete(t *testing.T, base string, body map[string]any, header ...string) wireCompletion {
	t.Helper()
	r := mustCall(t, "POST", base+"/v1/completions", body, header...)
	if r.status != http.StatusOK {
		t.Fatalf("status %d body %s", r.status, r.body)
	}
	return decodeBody[wireCompletion](t, r)
}

func TestPrefixCacheCutsTTFT(t *testing.T) {
	s, srv := start(t, Config{
		Model: "m", BlockSize: 16, NumGPUBlocks: 256, PrefillPerToken: 5 * time.Millisecond,
		Mode: ModeRatio, Speedup: 10, Adapters: []Adapter{{"lo", "/lo"}},
	})
	body := map[string]any{"model": "m", "prompt": promptOf("w", 256), "max_tokens": 1}
	first := complete(t, srv.URL, body)
	second := complete(t, srv.URL, body)
	if first.Usage.PromptTokensDetails.CachedTokens != 0 || second.Usage.PromptTokensDetails.CachedTokens != 240 {
		t.Fatalf("cached tokens %d then %d, want 0 then 240", first.Usage.PromptTokensDetails.CachedTokens, second.Usage.PromptTokensDetails.CachedTokens)
	}
	st := s.Stats()
	r1, r2 := st.Records[0], st.Records[1]
	if r1.TTFT < 1280*time.Millisecond {
		t.Fatalf("cold TTFT %v under the 1280ms prefill", r1.TTFT)
	}
	if r2.TTFT >= r1.TTFT/4 || r2.CachedTokens != 240 {
		t.Fatalf("warm TTFT %v against cold %v with %d cached", r2.TTFT, r1.TTFT, r2.CachedTokens)
	}
	if st.CacheHits != 15 || st.CacheMisses != 15 {
		t.Fatalf("hits %d misses %d", st.CacheHits, st.CacheMisses)
	}
	body["model"] = "lo"
	if c := complete(t, srv.URL, body); c.Usage.PromptTokensDetails.CachedTokens != 0 {
		t.Fatalf("adapter request reused base model blocks, cached %d", c.Usage.PromptTokensDetails.CachedTokens)
	}
}

func TestPrefixCacheEvictsTailFirst(t *testing.T) {
	_, srv := start(t, Config{BlockSize: 4, NumGPUBlocks: 6})
	a := map[string]any{"prompt": promptOf("a", 12), "max_tokens": 1}
	if c := complete(t, srv.URL, a); c.Usage.PromptTokensDetails.CachedTokens != 0 {
		t.Fatalf("cold cached %d", c.Usage.PromptTokensDetails.CachedTokens)
	}
	complete(t, srv.URL, map[string]any{"prompt": promptOf("b", 16), "max_tokens": 1})
	if c := complete(t, srv.URL, a); c.Usage.PromptTokensDetails.CachedTokens != 4 {
		t.Fatalf("after eviction cached %d, want the 4 token head block", c.Usage.PromptTokensDetails.CachedTokens)
	}
	if c := complete(t, srv.URL, a); c.Usage.PromptTokensDetails.CachedTokens != 8 {
		t.Fatalf("after refill cached %d, want 8", c.Usage.PromptTokensDetails.CachedTokens)
	}
}

func TestQueueingAtMaxNumSeqs(t *testing.T) {
	s, srv := start(t, Config{MaxNumSeqs: 2, DecodePerToken: 10 * time.Second})
	var cancels []context.CancelFunc
	var wg sync.WaitGroup
	for range 4 {
		ctx, cancel := context.WithCancel(context.Background())
		cancels = append(cancels, cancel)
		wg.Go(func() {
			call(ctx, "POST", srv.URL+"/v1/completions", map[string]any{"prompt": "a b c", "max_tokens": 5})
		})
		eventually(t, 5*time.Second, "request queued", func() bool { return s.Stats().InFlight == len(cancels) })
	}
	if st := s.Stats(); st.Running != 2 || st.Queued != 2 || st.InFlight != 4 {
		t.Fatalf("running %d queued %d in flight %d", st.Running, st.Queued, st.InFlight)
	}
	cancels[0]()
	eventually(t, 5*time.Second, "a queued request to take the freed slot", func() bool {
		st := s.Stats()
		return st.Running == 2 && st.Queued == 1
	})
	for _, c := range cancels {
		c()
	}
	wg.Wait()

	s2, srv2 := start(t, Config{MaxNumSeqs: 1, DecodePerToken: 200 * time.Millisecond, Mode: ModeRatio, Speedup: 10})
	done := make(chan struct{})
	go func() {
		defer close(done)
		call(context.Background(), "POST", srv2.URL+"/v1/completions", map[string]any{"prompt": "a", "max_tokens": 5}, "X-Request-Id", "first")
	}()
	eventually(t, 5*time.Second, "first request running", func() bool { return s2.Stats().Running == 1 })
	complete(t, srv2.URL, map[string]any{"prompt": "b", "max_tokens": 1}, "X-Request-Id", "second")
	<-done
	recs := s2.Stats().Records
	if len(recs) != 2 || recs[0].ID != "first" || recs[1].ID != "second" {
		t.Fatalf("records %+v", recs)
	}
	if recs[1].TTFT < 600*time.Millisecond {
		t.Fatalf("queued request TTFT %v did not include the 800ms ahead of it", recs[1].TTFT)
	}
}

func TestPreemptionOverHTTP(t *testing.T) {
	s, srv := start(t, Config{BlockSize: 4, NumGPUBlocks: 8, DecodePerToken: 20 * time.Millisecond})
	body := func(p string) map[string]any { return map[string]any{"prompt": promptOf(p, 8), "max_tokens": 12} }
	var first wireCompletion
	done := make(chan struct{})
	go func() {
		defer close(done)
		r, err := call(context.Background(), "POST", srv.URL+"/v1/completions", body("a"))
		if err == nil {
			json.Unmarshal(r.body, &first)
		}
	}()
	eventually(t, 5*time.Second, "first request running", func() bool { return s.Stats().Running == 1 })
	second := complete(t, srv.URL, body("b"))
	<-done
	for _, c := range []wireCompletion{first, second} {
		if len(c.Choices) != 1 || c.Choices[0].Text != words(12, false) || c.Usage.CompletionTokens != 12 {
			t.Fatalf("completion after preemption %+v", c)
		}
	}
	if st := s.Stats(); st.Preempted < 1 || st.Served != 2 {
		t.Fatalf("preempted %d served %d", st.Preempted, st.Served)
	}
}

func TestTimeAccurate(t *testing.T) {
	s, srv := start(t, Config{BaseLatency: 40 * time.Millisecond, PrefillPerToken: 2 * time.Millisecond, DecodePerToken: 10 * time.Millisecond})
	body, _ := json.Marshal(map[string]any{"prompt": "a b c d e", "max_tokens": 6, "stream": true})
	begin := time.Now()
	resp, err := http.Post(srv.URL+"/v1/completions", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var ttft time.Duration
	tokens := 0
	rd := bufio.NewReader(resp.Body)
	for {
		line, err := rd.ReadString('\n')
		if err != nil {
			t.Fatalf("stream ended early after %d tokens: %v", tokens, err)
		}
		data, ok := strings.CutPrefix(strings.TrimSpace(line), "data: ")
		if !ok {
			continue
		}
		if data == "[DONE]" {
			break
		}
		if strings.Contains(data, `"text":" `) {
			tokens++
			if tokens == 1 {
				ttft = time.Since(begin)
			}
		}
	}
	total := time.Since(begin)
	if tokens != 6 {
		t.Fatalf("streamed %d tokens", tokens)
	}
	within := func(what string, got, want, slack time.Duration) {
		if got < want || got > want+slack {
			t.Errorf("%s %v outside [%v, %v]", what, got, want, want+slack)
		}
	}
	within("client TTFT", ttft, 50*time.Millisecond, 30*time.Millisecond)
	within("client total", total, 100*time.Millisecond, 40*time.Millisecond)
	rec := s.Stats().Records[0]
	within("recorded TTFT", rec.TTFT, 50*time.Millisecond, 30*time.Millisecond)
	within("recorded total", rec.Total, 100*time.Millisecond, 40*time.Millisecond)
}

func TestCancelFreesBlocks(t *testing.T) {
	s, srv := start(t, Config{BlockSize: 4, NumGPUBlocks: 16, DecodePerToken: 10 * time.Second})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	body, _ := json.Marshal(map[string]any{"prompt": promptOf("x", 9), "max_tokens": 5, "stream": true})
	req, _ := http.NewRequestWithContext(ctx, "POST", srv.URL+"/v1/completions", bytes.NewReader(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(resp.Body).ReadString('\n')
	if err != nil || !strings.HasPrefix(line, "data: ") {
		t.Fatalf("first event %q %v", line, err)
	}
	if g := s.eng.gauges(); g.usage != 3.0/16 {
		t.Fatalf("usage %v while running, want 3 of 16 blocks", g.usage)
	}
	cancel()
	resp.Body.Close()
	eventually(t, 5*time.Second, "cancelled request to leave", func() bool { return s.Stats().InFlight == 0 })
	if g := s.eng.gauges(); g.usage != 0 {
		t.Fatalf("usage %v after cancel", g.usage)
	}
	if st := s.Stats(); st.Served != 0 {
		t.Fatalf("cancelled request counted served")
	}
}
