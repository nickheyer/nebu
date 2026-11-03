package stub

import (
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Values one metrics scrape reports
type gauges struct {
	waiting  int
	running  int
	usage    float64
	resident string
	pending  string
	changed  time.Time
}

// Scrape values read under the lock
func (e *engine) gauges() gauges {
	e.mu.Lock()
	defer e.mu.Unlock()
	pending := append(append([]*lora{}, e.loras.loading...), e.loras.waiting...)
	return gauges{
		waiting:  len(e.queue),
		running:  len(e.running),
		usage:    float64(e.kv.used()) / float64(len(e.kv.blocks)),
		resident: joinNames(e.loras.resident),
		pending:  joinNames(pending),
		changed:  e.changed,
	}
}

// Prometheus text format writer
type exposition struct{ b strings.Builder }

// Writes help, type and one sample, labels given as name value pairs
func (x *exposition) gauge(name, help string, v float64, labels ...string) {
	x.b.WriteString("# HELP " + name + " " + help + "\n# TYPE " + name + " gauge\n" + name)
	if len(labels) > 0 {
		x.b.WriteByte('{')
		for i := 0; i+1 < len(labels); i += 2 {
			if i > 0 {
				x.b.WriteByte(',')
			}
			x.b.WriteString(labels[i] + `="` + escapeLabel(labels[i+1]) + `"`)
		}
		x.b.WriteByte('}')
	}
	x.b.WriteString(" " + strconv.FormatFloat(v, 'g', -1, 64) + "\n")
}

// Backslash escapes quote, backslash and newline in a value
func escapeLabel(v string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(v)
}

// Labels an SGLang scheduler of data parallel rank 0 writes on every series, sorted as prometheus_client exposes them
func sglangLabels(model string) []string {
	return []string{"dp_rank", "0", "engine_type", "unified", "model_name", model, "moe_ep_rank", "0", "pp_rank", "0", "tp_rank", "0"}
}

// Writes the queue, running, KV usage, cache geometry and adapter gauges under the configured runtime's names
func (s *Stub) handleMetrics(w http.ResponseWriter, r *http.Request) {
	g := s.eng.gauges()
	model := s.cfg.Model
	var x exposition
	switch s.cfg.Names {
	case NamesVLLM:
		x.gauge("vllm:num_requests_waiting", "Number of requests waiting to be processed.", float64(g.waiting), "model_name", model)
		x.gauge("vllm:num_requests_running", "Number of requests in model execution batches.", float64(g.running), "model_name", model)
		x.gauge("vllm:kv_cache_usage_perc", "KV-cache usage. 1 means 100 percent usage.", g.usage, "model_name", model)
		x.gauge("vllm:cache_config_info", "Information of the LLMEngine CacheConfig", 1,
			"block_size", strconv.Itoa(s.cfg.BlockSize), "num_gpu_blocks", strconv.Itoa(s.cfg.NumGPUBlocks))
		x.gauge("vllm:lora_requests_info", "Running stats on lora requests.", float64(g.changed.UnixNano())/1e9,
			"max_lora", strconv.Itoa(s.cfg.MaxLoras), "running_lora_adapters", g.resident, "waiting_lora_adapters", g.pending)
	case NamesSGLang:
		labels := sglangLabels(model)
		x.gauge("sglang:num_queue_reqs", "The number of requests in the waiting queue.", float64(g.waiting), labels...)
		x.gauge("sglang:num_running_reqs", "The number of running requests.", float64(g.running), labels...)
		x.gauge("sglang:token_usage", "The token usage.", g.usage, labels...)
		x.gauge("sglang:page_size", "KV cache page size in tokens.", float64(s.cfg.BlockSize), labels...)
		x.gauge("sglang:num_pages", "Number of KV cache pages.", float64(s.cfg.NumGPUBlocks), labels...)
	case NamesLlamaCpp:
		x.gauge("llamacpp:requests_processing", "Number of requests processing", float64(g.running))
		x.gauge("llamacpp:requests_deferred", "Number of requests deferred", float64(g.waiting))
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	io.WriteString(w, x.b.String())
}
