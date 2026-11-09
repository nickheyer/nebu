package runtimes

import "testing"

// Each runtime reports the devices an expert layer spreads over from its recorded params, one when experts are replicated
func TestExpertParallel(t *testing.T) {
	cases := []struct {
		name   string
		rt     Runtime
		params map[string]string
		want   int
	}{
		{"vllm replicated experts", VLLM{}, map[string]string{"tensor_parallel_size": "4"}, 1},
		{"vllm expert parallel over tp", VLLM{}, map[string]string{"tensor_parallel_size": "4", "enable_expert_parallel": "true"}, 4},
		{"vllm expert parallel over tp and dp", VLLM{}, map[string]string{"tensor_parallel_size": "2", "data_parallel_size": "4", "enable_expert_parallel": "true"}, 8},
		{"sglang default", SGLang{}, nil, 1},
		{"sglang ep_size", SGLang{}, map[string]string{"ep_size": "8"}, 8},
		{"nemo expert tensor parallel", NeMo{}, map[string]string{"expert_tensor_parallel_size": "2"}, 2},
		{"llamacpp", LlamaCpp{}, map[string]string{"n_cpu_moe": "4"}, 1},
		{"sdcpp", SDCpp{}, nil, 1},
	}
	for _, c := range cases {
		if got := c.rt.ExpertParallel(c.params); got != c.want {
			t.Errorf("%s: %d, want %d", c.name, got, c.want)
		}
	}
}
