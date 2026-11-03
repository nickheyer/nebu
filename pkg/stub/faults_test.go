package stub

import (
	"net/http"
	"slices"
	"testing"
	"time"
)

func TestFaultInjection(t *testing.T) {
	s, srv := start(t, Config{DefaultMaxTokens: 2})
	control := func(body any) reply { return mustCall(t, "POST", srv.URL+"/stub/control", body) }
	chat := func() reply { return mustCall(t, "POST", srv.URL+"/v1/chat/completions", chatBody("", "hi", nil)) }

	t.Run("fail rate", func(t *testing.T) {
		if st := decodeBody[FaultState](t, control(map[string]any{"fail_rate": 1})); st.FailRate != 1 {
			t.Fatalf("state %+v", st)
		}
		e := wantError(t, chat(), http.StatusInternalServerError, "injected_failure")
		if e.Error.Type != "server_error" {
			t.Fatalf("type %q", e.Error.Type)
		}
		control(map[string]any{"fail_rate": 0.5})
		var got []int
		for range 4 {
			got = append(got, chat().status)
		}
		if !slices.Equal(got, []int{200, 500, 200, 500}) {
			t.Fatalf("statuses %v", got)
		}
		control(map[string]any{"fail_rate": 0})
	})

	t.Run("pause and reject above queue", func(t *testing.T) {
		st := decodeBody[FaultState](t, control(map[string]any{"action": "pause", "reject_above_queue": 0}))
		if !st.Paused || st.RejectAboveQueue != 0 {
			t.Fatalf("state %+v", st)
		}
		if r := mustCall(t, "GET", srv.URL+"/health", nil); r.status != http.StatusServiceUnavailable {
			t.Fatalf("health while paused %d", r.status)
		}
		queued := chatAsync(srv.URL, "")
		eventually(t, 5*time.Second, "request to queue while paused", func() bool { return s.Stats().Queued == 1 })
		wantError(t, chat(), http.StatusTooManyRequests, "rate_limit_exceeded")
		control(map[string]any{"action": "resume", "reject_above_queue": -1})
		if r := <-queued; r.status != http.StatusOK {
			t.Fatalf("queued request after resume %d %s", r.status, r.body)
		}
	})

	t.Run("drop connections", func(t *testing.T) {
		if _, err := s.Control(Control{DropConnections: ptr(1)}); err != nil {
			t.Fatal(err)
		}
		client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
		resp, err := client.Post(srv.URL+"/v1/completions", "application/json", nil)
		if err == nil {
			resp.Body.Close()
			t.Fatalf("dropped request answered %d", resp.StatusCode)
		}
		if r := chat(); r.status != http.StatusOK {
			t.Fatalf("request after the drop %d", r.status)
		}
		if st := decodeBody[FaultState](t, mustCall(t, "GET", srv.URL+"/stub/control", nil)); st.DropConnections != 0 {
			t.Fatalf("drops left %d", st.DropConnections)
		}
	})

	t.Run("invalid control", func(t *testing.T) {
		for _, body := range []any{
			map[string]any{"fail_rate": 2},
			map[string]any{"action": "jump"},
			map[string]any{"latency_multiplier": 0},
			map[string]any{"drop_connections": -1},
			rawBody("{"),
		} {
			wantError(t, control(body), http.StatusBadRequest, "")
		}
		if st := decodeBody[FaultState](t, mustCall(t, "GET", srv.URL+"/stub/control", nil)); st.FailRate != 0 || st.LatencyMultiplier != 1 || st.Paused {
			t.Fatalf("rejected updates changed state %+v", st)
		}
	})
}

func TestLatencyMultiplier(t *testing.T) {
	s, srv := start(t, Config{BaseLatency: 100 * time.Millisecond, Mode: ModeRatio, Speedup: 2})
	body := map[string]any{"prompt": "a", "max_tokens": 1}
	complete(t, srv.URL, body)
	if _, err := s.Control(Control{LatencyMultiplier: ptr(3.0)}); err != nil {
		t.Fatal(err)
	}
	complete(t, srv.URL, body)
	recs := s.Stats().Records
	if base := recs[0].TTFT; base < 100*time.Millisecond || base > 160*time.Millisecond {
		t.Fatalf("baseline TTFT %v", base)
	}
	if slow := recs[1].TTFT; slow < 300*time.Millisecond || slow > 400*time.Millisecond {
		t.Fatalf("tripled TTFT %v", slow)
	}
}

func TestCloseFailsPending(t *testing.T) {
	s, srv := start(t, Config{})
	s.Pause()
	pending := chatAsync(srv.URL, "")
	eventually(t, 5*time.Second, "request to queue", func() bool { return s.Stats().Queued == 1 })
	s.Close()
	wantError(t, <-pending, http.StatusServiceUnavailable, "shutting_down")
	wantError(t, mustCall(t, "POST", srv.URL+"/v1/completions", map[string]any{"prompt": "a"}), http.StatusServiceUnavailable, "shutting_down")
	if _, open := <-s.Served(); open {
		t.Fatal("Served channel open after Close")
	}
}
