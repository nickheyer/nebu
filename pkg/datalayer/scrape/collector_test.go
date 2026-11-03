package scrape

import (
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"

	"github.com/nickheyer/nebu/pkg/datalayer"
)

const vllmText = `# HELP vllm:num_requests_running Number of requests in model execution batches.
# TYPE vllm:num_requests_running gauge
vllm:num_requests_running{engine="0",model_name="m"} 3.0
# HELP vllm:num_requests_waiting Number of requests waiting to be processed.
# TYPE vllm:num_requests_waiting gauge
vllm:num_requests_waiting{engine="0",model_name="m"} 5.0
# HELP vllm:kv_cache_usage_perc KV-cache usage. 1 means 100 percent usage.
# TYPE vllm:kv_cache_usage_perc gauge
vllm:kv_cache_usage_perc{engine="0",model_name="m"} 0.42
# HELP vllm:cache_config_info Information of the LLMEngine CacheConfig
# TYPE vllm:cache_config_info gauge
vllm:cache_config_info{block_size="16",cache_dtype="auto",engine="0",num_cpu_blocks="None",num_gpu_blocks="27000"} 1.0
# HELP vllm:lora_requests_info Running stats on lora requests.
# TYPE vllm:lora_requests_info gauge
vllm:lora_requests_info{max_lora="4",running_lora_adapters="",waiting_lora_adapters=""} 1.7e+09
vllm:lora_requests_info{max_lora="4",running_lora_adapters=" sql-lora, chat-lora ,",waiting_lora_adapters="tool-lora"} 1.70000005e+09
vllm:lora_requests_info{max_lora="2",running_lora_adapters="old",waiting_lora_adapters=""} 1.6e+09
# HELP vllm:prompt_tokens_total Number of prefill tokens processed.
# TYPE vllm:prompt_tokens_total counter
vllm:prompt_tokens_total{engine="0",model_name="m"} 100.0
# HELP vllm:time_to_first_token_seconds Histogram of time to first token in seconds.
# TYPE vllm:time_to_first_token_seconds histogram
vllm:time_to_first_token_seconds_bucket{engine="0",le="0.001",model_name="m"} 0.0
vllm:time_to_first_token_seconds_bucket{engine="0",le="+Inf",model_name="m"} 2.0
vllm:time_to_first_token_seconds_count{engine="0",model_name="m"} 2.0
vllm:time_to_first_token_seconds_sum{engine="0",model_name="m"} 0.3
`

const sglangText = `# HELP sglang:num_running_reqs The number of running requests.
# TYPE sglang:num_running_reqs gauge
sglang:num_running_reqs{engine_type="unified",model_name="m",moe_ep_rank="0",pp_rank="0",tp_rank="0"} 2.0
# HELP sglang:num_queue_reqs The number of requests in the waiting queue.
# TYPE sglang:num_queue_reqs gauge
sglang:num_queue_reqs{engine_type="unified",model_name="m",moe_ep_rank="0",pp_rank="0",tp_rank="0"} 7.0
# HELP sglang:token_usage The token usage.
# TYPE sglang:token_usage gauge
sglang:token_usage{engine_type="unified",model_name="m",moe_ep_rank="0",pp_rank="0",tp_rank="0"} 0.25
# HELP sglang:page_size KV cache page size in tokens.
# TYPE sglang:page_size gauge
sglang:page_size{engine_type="unified",model_name="m",moe_ep_rank="0",pp_rank="0",tp_rank="0"} 1.0
# HELP sglang:num_pages Number of KV cache pages.
# TYPE sglang:num_pages gauge
sglang:num_pages{engine_type="unified",model_name="m",moe_ep_rank="0",pp_rank="0",tp_rank="0"} 500000.0
`

const tritonText = `# HELP nv_trt_llm_request_metrics TRT LLM request metrics
# TYPE nv_trt_llm_request_metrics gauge
nv_trt_llm_request_metrics{model="tensorrt_llm",request_type="context",version="1"} 1
nv_trt_llm_request_metrics{model="tensorrt_llm",request_type="scheduled",version="1"} 6
nv_trt_llm_request_metrics{model="tensorrt_llm",request_type="max",version="1"} 512
nv_trt_llm_request_metrics{model="tensorrt_llm",request_type="active",version="1"} 6
nv_trt_llm_request_metrics{model="tensorrt_llm",request_type="waiting",version="1"} 9
# HELP nv_trt_llm_kv_cache_block_metrics TRT LLM KV cache block metrics
# TYPE nv_trt_llm_kv_cache_block_metrics gauge
nv_trt_llm_kv_cache_block_metrics{kv_cache_block_type="fraction",model="tensorrt_llm",version="1"} 0.5
nv_trt_llm_kv_cache_block_metrics{kv_cache_block_type="tokens_per",model="tensorrt_llm",version="1"} 64
nv_trt_llm_kv_cache_block_metrics{kv_cache_block_type="used",model="tensorrt_llm",version="1"} 3000
nv_trt_llm_kv_cache_block_metrics{kv_cache_block_type="free",model="tensorrt_llm",version="1"} 3000
nv_trt_llm_kv_cache_block_metrics{kv_cache_block_type="max",model="tensorrt_llm",version="1"} 6000
`

const trtllmServeText = `# HELP trtllm_num_requests_waiting Number of queued requests
# TYPE trtllm_num_requests_waiting gauge
trtllm_num_requests_waiting{engine_type="pytorch",model_name="m"} 4.0
# HELP trtllm_num_requests_running Number of active requests
# TYPE trtllm_num_requests_running gauge
trtllm_num_requests_running{engine_type="pytorch",model_name="m"} 8.0
# HELP trtllm_kv_cache_utilization KV cache utilization
# TYPE trtllm_kv_cache_utilization gauge
trtllm_kv_cache_utilization{dp_rank="0",engine_type="pytorch",model_name="m"} 0.2
trtllm_kv_cache_utilization{dp_rank="1",engine_type="pytorch",model_name="m"} 0.4
# HELP trtllm_kv_cache_tokens_per_block Number of tokens per KV cache block
# TYPE trtllm_kv_cache_tokens_per_block gauge
trtllm_kv_cache_tokens_per_block{dp_rank="0",engine_type="pytorch",model_name="m"} 32.0
trtllm_kv_cache_tokens_per_block{dp_rank="1",engine_type="pytorch",model_name="m"} 32.0
# HELP trtllm_kv_cache_max_blocks Maximum number of KV cache blocks
# TYPE trtllm_kv_cache_max_blocks gauge
trtllm_kv_cache_max_blocks{dp_rank="0",engine_type="pytorch",model_name="m"} 1000.0
trtllm_kv_cache_max_blocks{dp_rank="1",engine_type="pytorch",model_name="m"} 1000.0
`

const llamaText = `# HELP llamacpp:prompt_tokens_total Number of prompt tokens processed, excluding cached tokens
# TYPE llamacpp:prompt_tokens_total counter
llamacpp:prompt_tokens_total 400
# HELP llamacpp:requests_processing Number of requests processing
# TYPE llamacpp:requests_processing gauge
llamacpp:requests_processing 2
# HELP llamacpp:requests_deferred Number of requests deferred
# TYPE llamacpp:requests_deferred gauge
llamacpp:requests_deferred 1
`

var llamaSlots = []Slot{
	{IsProcessing: true, NCtx: 4096, NPromptTokens: 1000},
	{IsProcessing: true, NCtx: 4096, NPromptTokens: 1048},
	{IsProcessing: false, NCtx: 4096, NPromptTokens: 3000},
	{NCtx: 4096},
}

var llamaAdapters = []Adapter{{Name: "sql", Applied: true}, {Name: "chat", Applied: false}}

const nemoText = `# HELP ray_serve_deployment_queued_queries queued
# TYPE ray_serve_deployment_queued_queries gauge
ray_serve_deployment_queued_queries{application="app",deployment="d"} 3
# HELP ray_serve_replica_processing_queries processing
# TYPE ray_serve_replica_processing_queries gauge
ray_serve_replica_processing_queries{deployment="d",replica="r1"} 2
ray_serve_replica_processing_queries{deployment="d",replica="r2"} 4
`

// A test endpoint carrying labels and a capacity attribute
type withCapacity struct {
	labels   labeled
	capacity int
}

func (c withCapacity) ID() datalayer.EndpointID   { return "e" }
func (c withCapacity) Pool() datalayer.PoolID     { return "p" }
func (c withCapacity) Address() datalayer.Address { return datalayer.Address{} }
func (c withCapacity) Labels() map[string]string  { return c.labels }
func (c withCapacity) Ready() bool                { return true }
func (c withCapacity) Attribute(name string) (any, bool) {
	if name == datalayer.CollectorCapacity && c.capacity > 0 {
		return c.capacity, true
	}
	return nil, false
}

// A test endpoint carrying only labels
type labeled map[string]string

func (l labeled) ID() datalayer.EndpointID     { return "e" }
func (l labeled) Pool() datalayer.PoolID       { return "p" }
func (l labeled) Address() datalayer.Address   { return datalayer.Address{} }
func (l labeled) Labels() map[string]string    { return l }
func (l labeled) Ready() bool                  { return true }
func (l labeled) Attribute(string) (any, bool) { return nil, false }

func parse(t *testing.T, text string) map[string]*dto.MetricFamily {
	t.Helper()
	fams, err := decode(strings.NewReader(text), expfmt.FmtText)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return fams
}

func caps(t *testing.T, runtime string) datalayer.MetricCapabilities {
	t.Helper()
	c, ok := Capabilities(runtime)
	if !ok {
		t.Fatalf("no capabilities for %s", runtime)
	}
	return c
}

func set(names ...string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, n := range names {
		out[n] = struct{}{}
	}
	return out
}

func TestExtractPerRuntime(t *testing.T) {
	at := time.Unix(1700000000, 0)
	eight := 8
	tests := []struct {
		name     string
		runtime  string
		text     string
		slots    []Slot
		adapters []Adapter
		maxLoRA  *int
		ep       datalayer.Endpoint
		want     datalayer.ServerMetrics
	}{
		{"vllm", RuntimeVLLM, vllmText, nil, nil, nil, labeled{}, datalayer.ServerMetrics{
			QueuedRequests: 5, RunningRequests: 3, KVCacheUtilization: 0.42, BlockSize: 16, NumGPUBlocks: 27000,
			MaxLoRA: 4, RunningAdapters: set("sql-lora", "chat-lora"), WaitingAdapters: set("tool-lora"),
		}},
		{"sglang", RuntimeSGLang, sglangText, nil, []Adapter{{Name: "sql", Applied: true}}, &eight, labeled{}, datalayer.ServerMetrics{
			QueuedRequests: 7, RunningRequests: 2, KVCacheUtilization: 0.25, BlockSize: 1, NumGPUBlocks: 500000,
			MaxLoRA: 8, RunningAdapters: set("sql"), WaitingAdapters: set(),
		}},
		{"triton", RuntimeTritonTRTLLM, tritonText, nil, nil, nil, labeled{}, datalayer.ServerMetrics{
			QueuedRequests: 9, RunningRequests: 6, KVCacheUtilization: 0.5, BlockSize: 64, NumGPUBlocks: 6000,
		}},
		{"trtllm-serve", RuntimeTRTLLMServe, trtllmServeText, nil, nil, nil, labeled{}, datalayer.ServerMetrics{
			QueuedRequests: 4, RunningRequests: 8, KVCacheUtilization: 0.3, BlockSize: 32, NumGPUBlocks: 2000,
		}},
		{"llamacpp split slots", RuntimeLlamaCpp, llamaText, llamaSlots, llamaAdapters, nil, labeled{datalayer.LabelKVCapacity: "16384"}, datalayer.ServerMetrics{
			QueuedRequests: 1, RunningRequests: 2, KVCacheUtilization: 0.125, BlockSize: 1, NumGPUBlocks: 16384,
			MaxLoRA: 2, RunningAdapters: set("sql"), WaitingAdapters: set(),
		}},
		{"llamacpp shared pool", RuntimeLlamaCpp, llamaText, llamaSlots, llamaAdapters, nil, labeled{datalayer.LabelKVCapacity: "4096"}, datalayer.ServerMetrics{
			QueuedRequests: 1, RunningRequests: 2, KVCacheUtilization: 0.5, BlockSize: 1, NumGPUBlocks: 4096,
			MaxLoRA: 2, RunningAdapters: set("sql"), WaitingAdapters: set(),
		}},
		{"nemo static caches", RuntimeNeMo, nemoText, nil, nil, nil, withCapacity{labels: labeled{datalayer.LabelKVCapacity: "131072"}, capacity: 16}, datalayer.ServerMetrics{
			QueuedRequests: 3, RunningRequests: 6, KVCacheUtilization: 0.375, BlockSize: 8192, NumGPUBlocks: 16,
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := &Sample{Families: parse(t, tc.text), Slots: tc.slots, Adapters: tc.adapters, MaxLoRA: tc.maxLoRA, Capabilities: caps(t, tc.runtime), ScrapedAt: at}
			got, err := Collector{}.Extract(tc.ep, s)
			if err != nil {
				t.Fatal(err)
			}
			m := got.(*datalayer.ServerMetrics)
			tc.want.UpdatedAt = at
			if math.Abs(m.KVCacheUtilization-tc.want.KVCacheUtilization) > 1e-9 {
				t.Fatalf("utilization = %v, want %v", m.KVCacheUtilization, tc.want.KVCacheUtilization)
			}
			m.KVCacheUtilization = tc.want.KVCacheUtilization
			if !reflect.DeepEqual(*m, tc.want) {
				t.Fatalf("got  %+v\nwant %+v", *m, tc.want)
			}
		})
	}
}

func TestUtilizationPercentOrFraction(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		want    float64
		wantErr error
	}{
		{"fraction", "0.37", 0.37, nil},
		{"full fraction", "1", 1, nil},
		{"zero", "0", 0, nil},
		{"percentage", "37", 0.37, nil},
		{"full percentage", "100", 1, nil},
		{"above percentage", "150", 0, ErrBadValue},
		{"negative", "-0.1", 0, ErrBadValue},
		{"not a number", "NaN", 0, ErrBadValue},
	}
	c := datalayer.MetricCapabilities{QueuedRequests: "q", RunningRequests: "r", KVCacheUtilization: "kv"}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			text := "# TYPE q gauge\nq 0\n# TYPE r gauge\nr 0\n# TYPE kv gauge\nkv " + tc.value + "\n"
			got, err := Collector{}.Extract(labeled{}, &Sample{Families: parse(t, text), Capabilities: c})
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if err == nil && math.Abs(got.(*datalayer.ServerMetrics).KVCacheUtilization-tc.want) > 1e-9 {
				t.Fatalf("utilization = %v, want %v", got.(*datalayer.ServerMetrics).KVCacheUtilization, tc.want)
			}
		})
	}
}

func TestLoRAPicksNewestSeries(t *testing.T) {
	head := "# TYPE q gauge\nq 0\n# TYPE r gauge\nr 0\n# TYPE kv gauge\nkv 0\n# TYPE lora gauge\n"
	c := datalayer.MetricCapabilities{
		QueuedRequests: "q", RunningRequests: "r", KVCacheUtilization: "kv",
		LoRAInfo: "lora", MaxLoRALabel: "max_lora", RunningAdaptersLabel: "running", WaitingAdaptersLabel: "waiting",
	}
	tests := []struct {
		name        string
		series      string
		wantMax     int
		wantRunning map[string]struct{}
		wantWaiting map[string]struct{}
		wantErr     error
	}{
		{
			name: "newest of three wins",
			series: `lora{max_lora="1",running="a",waiting=""} 100
lora{max_lora="3",running=" b , c ",waiting="d,, e"} 300
lora{max_lora="2",running="f",waiting=""} 200
`,
			wantMax: 3, wantRunning: set("b", "c"), wantWaiting: set("d", "e"),
		},
		{
			name:    "newest is idle",
			series:  "lora{max_lora=\"4\",running=\"a\",waiting=\"\"} 100\nlora{max_lora=\"4\",running=\"\",waiting=\"\"} 101\n",
			wantMax: 4, wantRunning: set(), wantWaiting: set(),
		},
		{
			name:    "malformed max",
			series:  "lora{max_lora=\"four\",running=\"\",waiting=\"\"} 100\n",
			wantErr: ErrBadValue,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Collector{}.Extract(labeled{}, &Sample{Families: parse(t, head+tc.series), Capabilities: c})
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if err != nil {
				return
			}
			m := got.(*datalayer.ServerMetrics)
			if m.MaxLoRA != tc.wantMax || !reflect.DeepEqual(m.RunningAdapters, tc.wantRunning) || !reflect.DeepEqual(m.WaitingAdapters, tc.wantWaiting) {
				t.Fatalf("got max=%d running=%v waiting=%v", m.MaxLoRA, m.RunningAdapters, m.WaitingAdapters)
			}
		})
	}
}

func TestExtractFailures(t *testing.T) {
	full := datalayer.MetricCapabilities{QueuedRequests: "q", RunningRequests: "r", KVCacheUtilization: "kv"}
	slots := datalayer.MetricCapabilities{QueuedRequests: "q", RunningRequests: "r", SlotsPath: "/slots"}
	matched := full
	matched.QueuedRequestsMatch = map[string]string{"kind": "waiting"}
	withInfo := full
	withInfo.CacheConfigInfo, withInfo.BlockSizeLabel, withInfo.NumBlocksLabel = "info", "bs", "nb"
	withGauges := full
	withGauges.BlockSize, withGauges.NumBlocks = "bs", "nb"
	withAdapters := full
	withAdapters.AdaptersPath, withAdapters.AdaptersFormat, withAdapters.MaxLoRAPath, withAdapters.MaxLoRAField = "/v1/models", datalayer.AdaptersOpenAI, "/server_info", "max_loras_per_batch"
	static := datalayer.MetricCapabilities{QueuedRequests: "q", RunningRequests: "r", StaticCache: true}
	const qr = "# TYPE q gauge\nq 0\n# TYPE r gauge\nr 0\n"
	const qrkv = qr + "# TYPE kv gauge\nkv 0\n"
	tests := []struct {
		name    string
		caps    datalayer.MetricCapabilities
		text    string
		slots   []Slot
		labels  labeled
		wantErr error
	}{
		{"missing queue gauge", full, "# TYPE r gauge\nr 1\n# TYPE kv gauge\nkv 0\n", nil, nil, ErrMissingGauge},
		{"missing kv gauge", full, "# TYPE q gauge\nq 1\n# TYPE r gauge\nr 1\n", nil, nil, ErrMissingGauge},
		{"no series matches the selector", matched, "# TYPE q gauge\nq{kind=\"running\"} 1\n# TYPE r gauge\nr 1\n# TYPE kv gauge\nkv 0\n", nil, nil, ErrMissingGauge},
		{"histogram where a gauge belongs", full, "# TYPE q histogram\nq_bucket{le=\"+Inf\"} 1\nq_sum 1\nq_count 1\n# TYPE r gauge\nr 1\n# TYPE kv gauge\nkv 0\n", nil, nil, ErrNotGauge},
		{"incomplete capabilities", datalayer.MetricCapabilities{QueuedRequests: "q"}, "", nil, nil, ErrIncomplete},
		{"slots without a kv-capacity label", slots, qr, llamaSlots, labeled{}, ErrNoKVCapacity},
		{"slots with a capacity label that is not a count", slots, qr, llamaSlots, labeled{datalayer.LabelKVCapacity: "lots"}, ErrNoKVCapacity},
		{"slot context beyond the kv capacity", slots, qr, llamaSlots, labeled{datalayer.LabelKVCapacity: "1024"}, ErrSlotContext},
		{"slot tokens beyond the kv capacity", slots, qr, []Slot{{IsProcessing: true, NCtx: 1024, NPromptTokens: 1000}, {IsProcessing: true, NCtx: 1024, NPromptTokens: 1000}}, labeled{datalayer.LabelKVCapacity: "1024"}, ErrSlotsOverflow},
		{"cache config info declared but absent", withInfo, qrkv, nil, labeled{}, ErrMissingGauge},
		{"cache config info with an empty label", withInfo, qrkv + "# TYPE info gauge\ninfo{bs=\"16\",nb=\"\"} 1\n", nil, labeled{}, ErrBadValue},
		{"cache config info with a None label", withInfo, qrkv + "# TYPE info gauge\ninfo{bs=\"None\",nb=\"100\"} 1\n", nil, labeled{}, ErrBadValue},
		{"block size gauge declared but absent", withGauges, qrkv + "# TYPE nb gauge\nnb 100\n", nil, labeled{}, ErrMissingGauge},
		{"block count gauge declared but absent", withGauges, qrkv + "# TYPE bs gauge\nbs 16\n", nil, labeled{}, ErrMissingGauge},
		{"static cache without a capacity attribute", static, qr, nil, labeled{datalayer.LabelKVCapacity: "4096"}, ErrNoCapacity},
		{"adapter cap document without the field", withAdapters, qrkv, nil, labeled{}, ErrNoMaxLoRA},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Collector{}.Extract(tc.labels, &Sample{Families: parse(t, tc.text), Slots: tc.slots, Adapters: []Adapter{{Name: "a", Applied: true}}, Capabilities: tc.caps})
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
		})
	}
	if _, err := (Collector{}).Extract(labeled{}, "not a sample"); !errors.Is(err, ErrSampleType) {
		t.Fatalf("wrong sample type err = %v", err)
	}
}

func TestCapabilitiesTable(t *testing.T) {
	for _, rt := range []string{RuntimeVLLM, RuntimeSGLang, RuntimeTritonTRTLLM, RuntimeTRTLLMServe, RuntimeLlamaCpp, RuntimeNeMo} {
		t.Run(rt, func(t *testing.T) {
			c := caps(t, rt)
			if !c.Complete() || c.Path == "" || c.HealthPath == "" {
				t.Fatalf("%s capabilities incomplete: %+v", rt, c)
			}
		})
	}
	if _, ok := Capabilities("unknown"); ok {
		t.Fatal("unknown runtime resolved")
	}
	c := caps(t, RuntimeTritonTRTLLM)
	c.QueuedRequestsMatch["request_type"] = "mutated"
	if caps(t, RuntimeTritonTRTLLM).QueuedRequestsMatch["request_type"] != "waiting" {
		t.Fatal("caller mutation reached the table")
	}
}

// A static cache endpoint whose running requests exceed its capacity is refused, one at capacity is full
func TestStaticCacheBounds(t *testing.T) {
	c := caps(t, RuntimeNeMo)
	fams := parse(t, nemoText)
	if _, err := (Collector{}).Extract(withCapacity{labels: labeled{datalayer.LabelKVCapacity: "8192"}, capacity: 4}, &Sample{Families: fams, Capabilities: c}); !errors.Is(err, ErrSlotsOverflow) {
		t.Fatalf("six running on four caches err = %v", err)
	}
	if _, err := (Collector{}).Extract(withCapacity{capacity: 6}, &Sample{Families: fams, Capabilities: c}); !errors.Is(err, ErrNoKVCapacity) {
		t.Fatalf("no cells label err = %v", err)
	}
	got, err := (Collector{}).Extract(withCapacity{labels: labeled{datalayer.LabelKVCapacity: "24576"}, capacity: 6}, &Sample{Families: fams, Capabilities: c})
	if err != nil {
		t.Fatal(err)
	}
	if m := got.(*datalayer.ServerMetrics); m.KVCacheUtilization != 1 || m.BlockSize != 4096 || m.NumGPUBlocks != 6 {
		t.Fatalf("full static cache %+v", *m)
	}
}

// One gauge family as prometheus_client writes it, one sample per label set
func gauge(name string, series ...string) string {
	out := "# TYPE " + name + " gauge\n"
	for _, s := range series {
		out += name + s + "\n"
	}
	return out
}

// SGLang rank layouts count each scheduler replica once, totals only under priority scheduling
func TestSGLangRankLayouts(t *testing.T) {
	const (
		tp0   = `{engine_type="unified",model_name="m",moe_ep_rank="0",pp_rank="0",tp_rank="0"}`
		tp1   = `{engine_type="unified",model_name="m",moe_ep_rank="0",pp_rank="0",tp_rank="1"}`
		pp1   = `{engine_type="unified",model_name="m",moe_ep_rank="0",pp_rank="1",tp_rank="0"}`
		prio  = `{engine_type="unified",model_name="m",moe_ep_rank="0",pp_rank="0",priority="",tp_rank="0"}`
		prio1 = `{engine_type="unified",model_name="m",moe_ep_rank="0",pp_rank="0",priority="1",tp_rank="0"}`
		prio2 = `{engine_type="unified",model_name="m",moe_ep_rank="0",pp_rank="0",priority="2",tp_rank="0"}`
		d0t0  = `{dp_rank="0",engine_type="unified",model_name="m",moe_ep_rank="0",pp_rank="0",tp_rank="0"}`
		d0t1  = `{dp_rank="0",engine_type="unified",model_name="m",moe_ep_rank="0",pp_rank="0",tp_rank="1"}`
		d1t0  = `{dp_rank="1",engine_type="unified",model_name="m",moe_ep_rank="0",pp_rank="0",tp_rank="0"}`
		d1t1  = `{dp_rank="1",engine_type="unified",model_name="m",moe_ep_rank="0",pp_rank="0",tp_rank="1"}`
		d1t2  = `{dp_rank="1",engine_type="unified",model_name="m",moe_ep_rank="0",pp_rank="0",tp_rank="2"}`
		d1t3  = `{dp_rank="1",engine_type="unified",model_name="m",moe_ep_rank="0",pp_rank="0",tp_rank="3"}`
	)
	tests := []struct {
		name                    string
		text                    string
		queued, running, blocks int
		blockSize               int
		kv                      float64
	}{
		{"tensor parallel ranks each emit the page gauges", gauge("sglang:num_running_reqs", tp0+" 3") + gauge("sglang:num_queue_reqs", tp0+" 4") + gauge("sglang:token_usage", tp0+" 0.5") +
			gauge("sglang:page_size", tp0+" 16", tp1+" 16") + gauge("sglang:num_pages", tp0+" 1000", tp1+" 1000"), 4, 3, 1000, 16, 0.5},
		{"pipeline stages each log the same requests", gauge("sglang:num_running_reqs", tp0+" 3", pp1+" 3") + gauge("sglang:num_queue_reqs", tp0+" 4", pp1+" 4") + gauge("sglang:token_usage", tp0+" 0.5", pp1+" 0.5") +
			gauge("sglang:page_size", tp0+" 16", pp1+" 16") + gauge("sglang:num_pages", tp0+" 1000", pp1+" 1000"), 4, 3, 1000, 16, 0.5},
		{"priority scheduling adds per priority series beside the total", gauge("sglang:num_running_reqs", prio+" 5", prio1+" 3", prio2+" 2") + gauge("sglang:num_queue_reqs", prio+" 6", prio1+" 6", prio2+" 0") +
			gauge("sglang:token_usage", prio+" 0.25") + gauge("sglang:page_size", prio+" 1") + gauge("sglang:num_pages", prio+" 2000"), 6, 5, 2000, 1, 0.25},
		{"data parallel attention groups each own a queue and pool", gauge("sglang:num_running_reqs", d0t0+" 1", d1t2+" 2") + gauge("sglang:num_queue_reqs", d0t0+" 3", d1t2+" 4") + gauge("sglang:token_usage", d0t0+" 0.2", d1t2+" 0.4") +
			gauge("sglang:page_size", d0t0+" 1", d0t1+" 1", d1t2+" 1", d1t3+" 1") + gauge("sglang:num_pages", d0t0+" 1000", d0t1+" 1000", d1t2+" 1000", d1t3+" 1000"), 7, 3, 2000, 1, 0.3},
		{"data parallel replicas with every scheduler logging", gauge("sglang:num_running_reqs", d0t0+" 1", d0t1+" 1", d1t0+" 2", d1t1+" 2") + gauge("sglang:num_queue_reqs", d0t0+" 3", d0t1+" 3", d1t0+" 4", d1t1+" 4") +
			gauge("sglang:token_usage", d0t0+" 0.2", d0t1+" 0.2", d1t0+" 0.4", d1t1+" 0.4") + gauge("sglang:page_size", d0t0+" 1", d0t1+" 1", d1t0+" 1", d1t1+" 1") +
			gauge("sglang:num_pages", d0t0+" 1000", d0t1+" 1000", d1t0+" 1000", d1t1+" 1000"), 7, 3, 2000, 1, 0.3},
	}
	eight := 8
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Collector{}.Extract(labeled{}, &Sample{Families: parse(t, tc.text), MaxLoRA: &eight, Capabilities: caps(t, RuntimeSGLang)})
			if err != nil {
				t.Fatal(err)
			}
			m := got.(*datalayer.ServerMetrics)
			if m.QueuedRequests != tc.queued || m.RunningRequests != tc.running || m.NumGPUBlocks != tc.blocks || m.BlockSize != tc.blockSize || math.Abs(m.KVCacheUtilization-tc.kv) > 1e-9 {
				t.Fatalf("got queued %d running %d blocks %d of %d kv %v, want %d %d %d of %d kv %v", m.QueuedRequests, m.RunningRequests, m.NumGPUBlocks, m.BlockSize, m.KVCacheUtilization, tc.queued, tc.running, tc.blocks, tc.blockSize, tc.kv)
			}
		})
	}
}

// A matcher with an empty value selects series lacking the label as well as series carrying it empty
func TestEmptyMatcherSelectsAbsentLabel(t *testing.T) {
	c := datalayer.MetricCapabilities{QueuedRequests: "q", QueuedRequestsMatch: map[string]string{"priority": ""}, RunningRequests: "r", KVCacheUtilization: "kv"}
	const rest = "# TYPE r gauge\nr 0\n# TYPE kv gauge\nkv 0\n"
	tests := []struct {
		name    string
		series  string
		want    int
		wantErr error
	}{
		{"series without the label", "q 4\n", 4, nil},
		{"empty label beside per priority series", "q{priority=\"\"} 4\nq{priority=\"1\"} 3\n", 4, nil},
		{"only labeled series", "q{priority=\"1\"} 3\n", 0, ErrMissingGauge},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Collector{}.Extract(labeled{}, &Sample{Families: parse(t, "# TYPE q gauge\n"+tc.series+rest), Capabilities: c})
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if err == nil && got.(*datalayer.ServerMetrics).QueuedRequests != tc.want {
				t.Fatalf("queued %d, want %d", got.(*datalayer.ServerMetrics).QueuedRequests, tc.want)
			}
		})
	}
}

// Info gauge blocks count once per replica when a replica label is named and once per series otherwise
func TestCacheInfoCountsEachReplicaOnce(t *testing.T) {
	text := "# TYPE q gauge\nq 0\n# TYPE r gauge\nr 0\n# TYPE kv gauge\nkv 0\n" + gauge("info", `{bs="16",engine="0",nb="100",rank="0"}`+" 1", `{bs="16",engine="0",nb="100",rank="1"}`+" 1", `{bs="16",engine="1",nb="200",rank="0"}`+" 1")
	tests := []struct {
		name    string
		replica string
		want    int
	}{
		{"replica label", "engine", 300},
		{"no replica label", "", 400},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := datalayer.MetricCapabilities{QueuedRequests: "q", RunningRequests: "r", KVCacheUtilization: "kv", CacheConfigInfo: "info", BlockSizeLabel: "bs", NumBlocksLabel: "nb", ReplicaLabel: tc.replica}
			got, err := Collector{}.Extract(labeled{}, &Sample{Families: parse(t, text), Capabilities: c})
			if err != nil {
				t.Fatal(err)
			}
			if m := got.(*datalayer.ServerMetrics); m.NumGPUBlocks != tc.want || m.BlockSize != 16 {
				t.Fatalf("blocks %d of %d, want %d of 16", m.NumGPUBlocks, m.BlockSize, tc.want)
			}
		})
	}
}

// Unified and per slot llama.cpp layouts read the same use of the labeled pool, a listing without the label is refused
func TestLlamaCppLayouts(t *testing.T) {
	busy := func(nctx int) []Slot {
		return []Slot{{IsProcessing: true, NCtx: nctx, NPromptTokens: 1500}, {IsProcessing: true, NCtx: nctx, NPromptTokens: 548}, {NCtx: nctx, NPromptTokens: 4000}, {NCtx: nctx}}
	}
	tests := []struct {
		name    string
		slots   []Slot
		labels  labeled
		wantErr error
	}{
		{"unified, every slot the whole pool", busy(16384), labeled{datalayer.LabelKVCapacity: "16384"}, nil},
		{"per slot, each a quarter of the pool", busy(4096), labeled{datalayer.LabelKVCapacity: "16384"}, nil},
		{"unified without a kv-capacity label", busy(16384), labeled{}, ErrNoKVCapacity},
		{"per slot without a kv-capacity label", busy(4096), labeled{}, ErrNoKVCapacity},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Collector{}.Extract(tc.labels, &Sample{Families: parse(t, llamaText), Slots: tc.slots, Capabilities: caps(t, RuntimeLlamaCpp)})
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if err != nil {
				return
			}
			if m := got.(*datalayer.ServerMetrics); m.KVCacheUtilization != 0.125 || m.NumGPUBlocks != 16384 || m.BlockSize != 1 {
				t.Fatalf("kv %v of %d cells of %d", m.KVCacheUtilization, m.NumGPUBlocks, m.BlockSize)
			}
		})
	}
}
