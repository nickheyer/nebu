package stub

import (
	"context"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type sample struct {
	labels map[string]string
	value  float64
}

// Samples keyed by metric name after checking every family is typed gauge
func scrape(t *testing.T, base string) map[string]sample {
	t.Helper()
	r := mustCall(t, "GET", base+"/metrics", nil)
	if r.status != http.StatusOK || !strings.HasPrefix(r.header.Get("Content-Type"), "text/plain; version=0.0.4") {
		t.Fatalf("metrics status %d content type %q", r.status, r.header.Get("Content-Type"))
	}
	out := map[string]sample{}
	types := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(r.body)), "\n") {
		if rest, ok := strings.CutPrefix(line, "# TYPE "); ok {
			name, typ, _ := strings.Cut(rest, " ")
			types[name] = typ
			continue
		}
		if strings.HasPrefix(line, "#") {
			continue
		}
		name, s := parseSample(t, line)
		if _, dup := out[name]; dup {
			t.Fatalf("metric %s repeated", name)
		}
		out[name] = s
	}
	for name := range out {
		if types[name] != "gauge" {
			t.Fatalf("metric %s typed %q", name, types[name])
		}
	}
	return out
}

// Name, labels and value of one exposition line
func parseSample(t *testing.T, line string) (string, sample) {
	t.Helper()
	i := strings.IndexAny(line, "{ ")
	if i < 0 {
		t.Fatalf("bad sample %q", line)
	}
	name, rest := line[:i], line[i:]
	labels := map[string]string{}
	if rest[0] == '{' {
		rest = rest[1:]
		for rest[0] != '}' {
			eq := strings.Index(rest, `="`)
			key := rest[:eq]
			rest = rest[eq+2:]
			var val strings.Builder
			for j := 0; ; j++ {
				if rest[j] == '\\' {
					j++
					if rest[j] == 'n' {
						val.WriteByte('\n')
					} else {
						val.WriteByte(rest[j])
					}
					continue
				}
				if rest[j] == '"' {
					rest = rest[j+1:]
					break
				}
				val.WriteByte(rest[j])
			}
			labels[key] = val.String()
			rest = strings.TrimPrefix(rest, ",")
		}
		rest = rest[1:]
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(rest), 64)
	if err != nil {
		t.Fatalf("bad value in %q: %v", line, err)
	}
	return name, sample{labels: labels, value: v}
}

func TestMetricsNames(t *testing.T) {
	sglang := map[string]string{"dp_rank": "0", "engine_type": "unified", "model_name": "stub-model", "moe_ep_rank": "0", "pp_rank": "0", "tp_rank": "0"}
	cases := []struct {
		names  Names
		want   []string
		config string
		labels []string
		values map[string]float64
		every  map[string]string
	}{
		{NamesVLLM, []string{"vllm:cache_config_info", "vllm:kv_cache_usage_perc", "vllm:lora_requests_info", "vllm:num_requests_running", "vllm:num_requests_waiting"},
			"vllm:cache_config_info", []string{"block_size", "16", "num_gpu_blocks", "4096"}, nil, nil},
		{NamesSGLang, []string{"sglang:num_pages", "sglang:num_queue_reqs", "sglang:num_running_reqs", "sglang:page_size", "sglang:token_usage"}, "", nil,
			map[string]float64{"sglang:page_size": 16, "sglang:num_pages": 4096, "sglang:num_queue_reqs": 0, "sglang:num_running_reqs": 0, "sglang:token_usage": 0}, sglang},
		{NamesLlamaCpp, []string{"llamacpp:requests_deferred", "llamacpp:requests_processing"}, "", nil,
			map[string]float64{"llamacpp:requests_deferred": 0, "llamacpp:requests_processing": 0}, map[string]string{}},
	}
	for _, c := range cases {
		t.Run(string(c.names), func(t *testing.T) {
			before := time.Now()
			_, srv := start(t, Config{Names: c.names, MaxLoras: 3, Adapters: []Adapter{{"a", "/a"}, {"b", "/b"}}})
			got := scrape(t, srv.URL)
			if names := slices.Sorted(maps.Keys(got)); !slices.Equal(names, c.want) {
				t.Fatalf("metric names %v, want %v", names, c.want)
			}
			if c.config != "" {
				s := got[c.config]
				if s.value != 1 || len(s.labels) != 2 || s.labels[c.labels[0]] != c.labels[1] || s.labels[c.labels[2]] != c.labels[3] {
					t.Fatalf("%s %+v", c.config, s)
				}
			}
			for name, v := range c.values {
				if got[name].value != v || !maps.Equal(got[name].labels, c.every) {
					t.Fatalf("%s %+v, want %v with labels %v", name, got[name], v, c.every)
				}
			}
			if c.names != NamesVLLM {
				return
			}
			l := got["vllm:lora_requests_info"]
			want := map[string]string{"max_lora": "3", "running_lora_adapters": "a,b", "waiting_lora_adapters": ""}
			if !maps.Equal(l.labels, want) {
				t.Fatalf("lora labels %v", l.labels)
			}
			if l.value < float64(before.Unix()-1) || l.value > float64(time.Now().Unix()+1) {
				t.Fatalf("lora timestamp %v", l.value)
			}
		})
	}
}

func TestMetricsUnderLoad(t *testing.T) {
	cases := []struct {
		names                     Names
		waiting, running, kvUsage string
	}{
		{NamesVLLM, "vllm:num_requests_waiting", "vllm:num_requests_running", "vllm:kv_cache_usage_perc"},
		{NamesSGLang, "sglang:num_queue_reqs", "sglang:num_running_reqs", "sglang:token_usage"},
		{NamesLlamaCpp, "llamacpp:requests_deferred", "llamacpp:requests_processing", ""},
	}
	for _, c := range cases {
		t.Run(string(c.names), func(t *testing.T) {
			s, srv := start(t, Config{Names: c.names, MaxNumSeqs: 1, BlockSize: 4, NumGPUBlocks: 10, DecodePerToken: 10 * time.Second})
			ctx, cancel := context.WithCancel(context.Background())
			var wg sync.WaitGroup
			for range 3 {
				wg.Go(func() {
					call(ctx, "POST", srv.URL+"/v1/completions", map[string]any{"prompt": "a b c d e", "max_tokens": 3})
				})
			}
			eventually(t, 5*time.Second, "one running and two queued", func() bool {
				st := s.Stats()
				return st.Running == 1 && st.Queued == 2
			})
			got := scrape(t, srv.URL)
			usage, hasUsage := got[c.kvUsage]
			if got[c.waiting].value != 2 || got[c.running].value != 1 || hasUsage != (c.kvUsage != "") || (hasUsage && usage.value != 0.2) {
				t.Fatalf("waiting %v running %v usage %v", got[c.waiting].value, got[c.running].value, got[c.kvUsage].value)
			}
			cancel()
			wg.Wait()
			eventually(t, 5*time.Second, "cancelled requests to leave", func() bool { return s.Stats().InFlight == 0 })
			got = scrape(t, srv.URL)
			if got[c.waiting].value != 0 || got[c.running].value != 0 || got[c.kvUsage].value != 0 {
				t.Fatalf("idle waiting %v running %v usage %v", got[c.waiting].value, got[c.running].value, got[c.kvUsage].value)
			}
		})
	}
}
