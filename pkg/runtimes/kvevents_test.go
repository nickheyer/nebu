package runtimes

import "testing"

// Each rank's recorded params resolve the KV event and replay addresses the launch bound for it
func TestKVEventAddresses(t *testing.T) {
	cases := []struct {
		name           string
		rt             KVEventPublisher
		params         map[string]string
		port, rank     int
		events, replay string
	}{
		{"vllm one rank", VLLM{}, map[string]string{}, 8000, 0, "127.0.0.1:8001", "127.0.0.1:8002"},
		{"vllm dense rank 1", VLLM{}, map[string]string{"data_parallel_size": "2"}, 8001, 1, "127.0.0.1:8003", "127.0.0.1:8005"},
		{"vllm coordinated rank 1 serving grpc", VLLM{}, map[string]string{"data_parallel_size": "4", "grpc": "true", "data_parallel_rpc_port": "8008"}, 8002, 1, "127.0.0.1:8010", "127.0.0.1:8014"},
		{"vllm without prefix caching", VLLM{}, map[string]string{"enable_prefix_caching": "off"}, 8000, 0, "", ""},
		{"sglang one rank", SGLang{}, map[string]string{}, 30000, 0, "127.0.0.1:30001", "127.0.0.1:30002"},
		{"sglang rank 1 serving grpc", SGLang{}, map[string]string{"dp_size": "2", "grpc": "true"}, 30002, 1, "127.0.0.1:30005", "127.0.0.1:30007"},
		{"sglang without the radix cache", SGLang{}, map[string]string{"disable_radix_cache": "true"}, 30000, 0, "", ""},
	}
	for _, c := range cases {
		events, replay := c.rt.KVEvents(c.params, "127.0.0.1", c.port, c.rank)
		if events != c.events || replay != c.replay {
			t.Errorf("%s: %s %s, want %s %s", c.name, events, replay, c.events, c.replay)
		}
	}
	for _, rt := range []Runtime{LlamaCpp{}, NeMo{}, SDCpp{}} {
		if _, ok := rt.(KVEventPublisher); ok {
			t.Errorf("%s publishes no KV events", rt.ID())
		}
	}
}
