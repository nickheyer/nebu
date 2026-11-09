package metrics

import (
	"context"
	"testing"
)

// Prompt and output tokens accumulate per fairness id across requests, models and pools
func TestFairnessTokenMeters(t *testing.T) {
	r, preg := newRegistry(t)
	ctx := context.Background()
	r.ObserveRequest(ctx, Request{Model: "m", Pool: "p", FairnessID: "alice", Status: 200, PromptTokens: 10, OutputTokens: 5})
	r.ObserveRequest(ctx, Request{Model: "n", Pool: "q", FairnessID: "alice", Status: 200, PromptTokens: 7, OutputTokens: 3})
	r.ObserveRequest(ctx, Request{Model: "m", Pool: "p", FairnessID: "bob", Status: 503, PromptTokens: 1})
	cases := []struct {
		family string
		want   map[string]float64
	}{
		{"nebu_fairness_prompt_tokens_total", map[string]float64{"fairness_id=alice": 17, "fairness_id=bob": 1}},
		{"nebu_fairness_output_tokens_total", map[string]float64{"fairness_id=alice": 8, "fairness_id=bob": 0}},
	}
	for _, c := range cases {
		t.Run(c.family, func(t *testing.T) {
			got := values(t, preg, c.family)
			if len(got) != len(c.want) {
				t.Fatalf("series %v, want %v", got, c.want)
			}
			for k, v := range c.want {
				if got[k] != v {
					t.Fatalf("%s = %v, want %v in %v", k, got[k], v, got)
				}
			}
		})
	}
}
