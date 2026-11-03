package config

import (
	"strings"
	"testing"
)

// Fairness quotas load per id and by default, and a limited quota without a window is refused
func TestQuotaSettings(t *testing.T) {
	cfg, err := load(t, "gateway:\n  flow_control:\n    quota:\n      tokens: 5\n      window_ms: 1000\n    quotas:\n      vip:\n        requests: 1\n        window_ms: 60000\n", nil)
	if err != nil {
		t.Fatal(err)
	}
	fc := cfg.GetGateway().GetFlowControl()
	if fc.GetQuota().GetTokens() != 5 || fc.GetQuota().GetWindowMs() != 1000 || fc.GetQuotas()["vip"].GetRequests() != 1 || fc.GetQuotas()["vip"].GetWindowMs() != 60000 {
		t.Fatalf("quotas %v %v", fc.GetQuota(), fc.GetQuotas())
	}
	cases := []string{
		"gateway:\n  flow_control:\n    quota:\n      tokens: 5\n",
		"gateway:\n  flow_control:\n    quotas:\n      vip:\n        requests: 2\n",
	}
	for _, text := range cases {
		if _, err := load(t, text, nil); err == nil || !strings.Contains(err.Error(), "window_ms") {
			t.Errorf("%q: want an error naming window_ms, got %v", text, err)
		}
	}
	if _, err := load(t, "gateway:\n  flow_control:\n    quota:\n      window_ms: 1000\n", nil); err != nil {
		t.Fatalf("a window without limits: %v", err)
	}
}
