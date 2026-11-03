package gateway

import (
	"testing"

	"github.com/nickheyer/nebu/pkg/datalayer"
	"github.com/nickheyer/nebu/pkg/prefixcache"
)

// The prefix index is sized from scraped KV blocks, else from the kv-capacity label, in chunks of the configured size
func TestFitSizesIndexFromBlocksOrLabel(t *testing.T) {
	h := newHarness(t)
	h.store.PutPool(datalayer.Pool{ID: "p", Model: "m", Runtime: "vllm"})
	tests := []struct {
		name    string
		metrics *datalayer.ServerMetrics
		label   string
		want    int
	}{
		{"scraped blocks", &datalayer.ServerMetrics{NumGPUBlocks: 1000, BlockSize: 16}, "", 1000 * 16 / prefixcache.DefaultChunkTokens},
		{"scraped blocks outrank the label", &datalayer.ServerMetrics{NumGPUBlocks: 1000, BlockSize: 16}, "32", 1000 * 16 / prefixcache.DefaultChunkTokens},
		{"label when the scrape reports no blocks", &datalayer.ServerMetrics{NumGPUBlocks: 0, BlockSize: 0}, "8192", 8192 / prefixcache.DefaultChunkTokens},
		{"label without metrics", nil, "4096", 4096 / prefixcache.DefaultChunkTokens},
		{"neither keeps the default", nil, "", 0},
		{"unparsable label keeps the default", nil, "lots", 0},
	}
	for i, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			id := datalayer.EndpointID("e" + string(rune('a'+i)))
			labels := map[string]string{}
			if tc.label != "" {
				labels[datalayer.LabelKVCapacity] = tc.label
			}
			h.store.UpsertEndpoint(datalayer.EndpointSpec{ID: id, Pool: "p", Address: datalayer.Address{Host: "h", Port: 9000 + i}, Labels: labels, Ready: true})
			if tc.metrics != nil {
				h.store.SetAttribute(id, datalayer.CollectorMetrics, tc.metrics)
			}
			ep, _ := h.store.Endpoint(id)
			sized := map[datalayer.EndpointID]int{}
			h.g.fit(ep, sized)
			if sized[id] != tc.want {
				t.Fatalf("sized %d blocks, want %d", sized[id], tc.want)
			}
		})
	}
}
