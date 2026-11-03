package cli

import (
	"strings"
	"testing"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// Pools show prints a failing Scraped condition with its error text and each static endpoint's KV capacity
func TestRenderPoolScrapeFailure(t *testing.T) {
	msg := "endpoint llama/10.1.0.1:8080 fails its metrics scrape since 2026-11-03T12:00:00Z, metrics: endpoint lacks a positive kv-capacity label: llama/10.1.0.1:8080"
	p := &v1.Pool{Id: "llama", Spec: &v1.PoolSpec{Model: "m", RuntimeId: "llamacpp", TargetPorts: []uint32{8080},
		StaticEndpoints: []*v1.StaticEndpoint{{Address: "10.1.0.1:8080", KvCapacity: 65536}}},
		Status: &v1.PoolStatus{Conditions: []*v1.Condition{{Type: "Scraped", Status: v1.ConditionStatus_CONDITION_STATUS_FALSE, Reason: "ScrapeFailing", Message: msg}}}}
	var b strings.Builder
	renderPool(&b, p)
	for _, want := range []string{"Scraped", "ScrapeFailing", msg, "KV CAPACITY", "65536"} {
		if !strings.Contains(b.String(), want) {
			t.Fatalf("output lacks %q:\n%s", want, b.String())
		}
	}
}
