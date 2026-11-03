package stub

import (
	"testing"
	"time"
)

func TestServedEventsAndStats(t *testing.T) {
	s, srv := start(t, Config{Records: 1})
	events := s.Served()
	complete(t, srv.URL, map[string]any{"prompt": "a b", "max_tokens": 2}, "X-Request-Id", "r1")
	complete(t, srv.URL, map[string]any{"prompt": "a b c", "max_tokens": 1}, "X-Request-Id", "r2")
	for _, want := range []Record{{ID: "r1", PromptTokens: 2, CompletionTokens: 2}, {ID: "r2", PromptTokens: 3, CompletionTokens: 1}} {
		select {
		case got := <-events:
			if got.ID != want.ID || got.PromptTokens != want.PromptTokens || got.CompletionTokens != want.CompletionTokens || got.Model != "stub-model" || got.Total < got.TTFT {
				t.Fatalf("event %+v, want %+v", got, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("no event for %s", want.ID)
		}
	}
	st := decodeBody[Stats](t, mustCall(t, "GET", srv.URL+"/stub/stats", nil))
	if st.Served != 2 || len(st.Records) != 1 || st.Records[0].ID != "r2" {
		t.Fatalf("stats %+v", st)
	}
	if direct := s.Stats(); direct.Served != 2 || len(direct.Records) != 1 {
		t.Fatalf("direct stats %+v", direct)
	}
}
