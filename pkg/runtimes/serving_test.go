package runtimes

import (
	"bytes"
	"encoding/binary"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nickheyer/nebu/pkg/estimate"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// Writes a tensorless GGUF file holding string and bool metadata
func writeGGUF(t *testing.T, path string, strs map[string]string, bools map[string]bool) {
	t.Helper()
	var b bytes.Buffer
	b.WriteString("GGUF")
	binary.Write(&b, binary.LittleEndian, uint32(3))
	binary.Write(&b, binary.LittleEndian, uint64(0))
	binary.Write(&b, binary.LittleEndian, uint64(len(strs)+len(bools)))
	str := func(s string) {
		binary.Write(&b, binary.LittleEndian, uint64(len(s)))
		b.WriteString(s)
	}
	for k, v := range strs {
		str(k)
		binary.Write(&b, binary.LittleEndian, uint32(8))
		str(v)
	}
	for k, v := range bools {
		str(k)
		binary.Write(&b, binary.LittleEndian, uint32(7))
		flag := byte(0)
		if v {
			flag = 1
		}
		b.WriteByte(flag)
	}
	if b.Len()%32 == 0 {
		b.WriteByte(0)
	}
	if err := os.WriteFile(path, b.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

// An NVIDIA GPU with a name and memory, as a host probe reports it
func card(id, index, name string, gib uint64) *v1.Device {
	return &v1.Device{Id: id, Kind: v1.DeviceKind_DEVICE_KIND_GPU, Vendor: "nvidia", Name: name, MemoryTotalBytes: gib << 30, Facts: map[string]string{"index": index}}
}

// A host with three GPUs and a plan that priced the second and third
func plannedHost() (*v1.HostProfile, *v1.MemoryPlan) {
	profile := &v1.HostProfile{
		Devices: []*v1.Device{card("GPU-a", "0", "NVIDIA RTX 4090", 24), card("GPU-b", "1", "NVIDIA H100 80GB HBM3", 80), card("GPU-c", "2", "NVIDIA H100 80GB HBM3", 80)},
		Pools: []*v1.MemoryPool{
			{Id: "GPU-a", Kind: v1.PoolKind_POOL_KIND_DEVICE, DeviceId: "GPU-a"},
			{Id: "GPU-b", Kind: v1.PoolKind_POOL_KIND_DEVICE, DeviceId: "GPU-b"},
			{Id: "GPU-c", Kind: v1.PoolKind_POOL_KIND_DEVICE, DeviceId: "GPU-c"},
			{Id: "host", Kind: v1.PoolKind_POOL_KIND_HOST},
		},
	}
	plan := &v1.MemoryPlan{
		Pools: []*v1.PoolUsage{
			{PoolId: "GPU-c", Kind: v1.PoolKind_POOL_KIND_DEVICE, UsedBytes: 10},
			{PoolId: "GPU-b", Kind: v1.PoolKind_POOL_KIND_DEVICE, UsedBytes: 10},
			{PoolId: "GPU-a", Kind: v1.PoolKind_POOL_KIND_DEVICE, UsedBytes: 0},
			{PoolId: "host", Kind: v1.PoolKind_POOL_KIND_HOST, UsedBytes: 5},
		},
		Placements: []*v1.GroupPlacement{
			{Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_EMBEDDING, PoolId: "host", Bytes: 5},
			{Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_LAYER, PoolId: "GPU-b", Bytes: 10},
		},
		Params: map[string]string{"n_ctx": "8192"},
	}
	return profile, plan
}

// Every runtime outside a slot pins the devices the plan priced, in host order, and fails when it cannot name them
func TestLaunchesPinThePlannedDevices(t *testing.T) {
	profile, plan := plannedHost()
	weights := filepath.Join(t.TempDir(), "w.gguf")
	for _, rt := range []Runtime{LlamaCpp{}, VLLM{}, SGLang{}, NeMo{}, SDCpp{}} {
		params, err := Resolve(rt, nil)
		if err != nil {
			t.Fatal(err)
		}
		in := Launch{Name: "m", Params: params, Artifacts: map[string]string{"weights": weights, "weights_dir": "/w", "prepared_dir": t.TempDir()}, Host: "127.0.0.1", Port: 9, Install: Install{Path: "/bin/x", Dir: "/opt"}, Plan: plan, Profile: profile, Descriptor: layered(4), InstallRecord: recorded(nil)}
		cmd, err := rt.Launch(in)
		if err != nil {
			t.Fatalf("%s: %v", rt.ID(), err)
		}
		if cmd.Env["CUDA_VISIBLE_DEVICES"] != "GPU-b,GPU-c" || cmd.Env["GGML_VK_VISIBLE_DEVICES"] != "1,2" || len(cmd.Devices) != 2 || cmd.Devices[0].GetId() != "GPU-b" {
			t.Errorf("%s pins %v devices %v", rt.ID(), cmd.Env, cmd.Devices)
		}
		if scope := ScopeOf(in, cmd); strings.Join(scope.Devices, ",") != "GPU-b,GPU-c" {
			t.Errorf("%s templates see devices %v", rt.ID(), scope.Devices)
		}
		in.Profile = nil
		if _, err := rt.Launch(in); err == nil || !strings.Contains(err.Error(), "no host profile") {
			t.Errorf("%s without a profile: %v", rt.ID(), err)
		}
		in.Profile = &v1.HostProfile{Pools: profile.GetPools()}
		if _, err := rt.Launch(in); err == nil || !strings.Contains(err.Error(), "does not list") {
			t.Errorf("%s with a profile missing the devices: %v", rt.ID(), err)
		}
		// A slot's assignment wins over the plan
		in.Profile, in.Devices = profile, []*v1.Device{profile.GetDevices()[0]}
		if cmd, err := rt.Launch(in); err != nil || cmd.Env["CUDA_VISIBLE_DEVICES"] != "GPU-a" {
			t.Errorf("%s with a slot's device: %v %v", rt.ID(), cmd, err)
		}
	}
	// NeMo's own device flag follows the pinned devices too
	params, _ := Resolve(NeMo{}, nil)
	cmd, err := NeMo{}.Launch(Launch{Name: "m", Params: params, Artifacts: map[string]string{"weights_dir": "/w"}, Host: "127.0.0.1", Port: 9, Install: Install{Path: "/bin/x", Dir: "/opt"}, Plan: plan, Profile: profile})
	if err != nil || cmd.Params["cuda_visible_devices"] != "1,2" {
		t.Fatalf("nemo device flag %v %v", cmd, err)
	}
}

// Each runtime declares the gauges it serves under the names the scraper reads
func TestMetricNames(t *testing.T) {
	cases := []struct {
		rt                       Runtime
		queued, running, kv, cfg string
		slots                    string
	}{
		{LlamaCpp{}, "llamacpp:requests_deferred", "llamacpp:requests_processing", "", "", "/slots"},
		{VLLM{}, "vllm:num_requests_waiting", "vllm:num_requests_running", "vllm:kv_cache_usage_perc", "vllm:cache_config_info", ""},
		{SGLang{}, "sglang:num_queue_reqs", "sglang:num_running_reqs", "sglang:token_usage", "", ""},
		{NeMo{}, "ray_serve_deployment_queued_queries", "ray_serve_replica_processing_queries", "", "", ""},
	}
	for _, c := range cases {
		m := c.rt.Metrics()
		if m.GetQueuedRequests() != c.queued || m.GetRunningRequests() != c.running || m.GetKvCacheUtilization() != c.kv || m.GetCacheConfigInfo() != c.cfg || m.GetSlotsPath() != c.slots || m.GetMetricsPath() != "/metrics" || m.GetHealthPath() != c.rt.Health().Path {
			t.Errorf("%s metrics %v", c.rt.ID(), m)
		}
	}
	v := VLLM{}.Metrics()
	if v.GetBlockSizeLabel() != "block_size" || v.GetNumBlocksLabel() != "num_gpu_blocks" || v.GetLoraInfo() != "vllm:lora_requests_info" || v.GetMaxLoraLabel() != "max_lora" || v.GetRunningAdaptersLabel() != "running_lora_adapters" || v.GetWaitingAdaptersLabel() != "waiting_lora_adapters" {
		t.Fatalf("vllm labels %v", v)
	}
	if s := (SGLang{}).Metrics(); s.GetBlockSize() != "sglang:page_size" || s.GetNumBlocks() != "sglang:num_pages" || s.GetReplicaLabel() != "dp_rank" || !maps.Equal(s.GetQueuedRequestsMatch(), map[string]string{"priority": ""}) ||
		!maps.Equal(s.GetRunningRequestsMatch(), map[string]string{"priority": ""}) || s.GetAdaptersPath() != "/v1/models" || s.GetAdaptersFormat() != "openai" || s.GetMaxLoraPath() != "/server_info" || s.GetMaxLoraField() != "max_loras_per_batch" {
		t.Fatalf("sglang labels %v", s)
	}
	if l := (LlamaCpp{}).Metrics(); l.GetAdaptersPath() != "/lora-adapters" || l.GetAdaptersFormat() != "llamacpp" {
		t.Fatalf("llama.cpp adapters %v", l)
	}
	if n := (NeMo{}).Metrics(); !n.GetStaticCache() {
		t.Fatal("NeMo holds static caches")
	}
	if (SDCpp{}).Metrics() != nil {
		t.Fatal("sd-server serves no metrics")
	}
	if (NeMo{}).MetricsPort(nil, 9000) != 9001 || (VLLM{}).MetricsPort(nil, 9000) != 9000 || (NeMo{}).PortSpan(Launch{}) != 2 {
		t.Fatal("NeMo's metrics sit on the port after the served one")
	}
}

// Capacity reads each endpoint's concurrency param, a data parallel rank being its own endpoint, KV capacity llama.cpp's context alone
func TestCapacity(t *testing.T) {
	cases := []struct {
		rt     Runtime
		params map[string]string
		want   uint32
	}{
		{LlamaCpp{}, map[string]string{"n_parallel": "4"}, 4},
		{LlamaCpp{}, map[string]string{}, 0},
		{VLLM{}, map[string]string{"max_num_seqs": "256"}, 256},
		{VLLM{}, map[string]string{"max_num_seqs": "256", "data_parallel_size": "2"}, 256},
		{SGLang{}, map[string]string{"max_running_requests": "48"}, 48},
		{SGLang{}, map[string]string{"max_running_requests": "48", "dp_size": "3"}, 48},
		{SGLang{}, map[string]string{}, 0},
		{NeMo{}, map[string]string{"max_batch_size": "32"}, 32},
		{NeMo{}, map[string]string{"max_batch_size": "8", "num_replicas": "2"}, 16},
		{SDCpp{}, nil, 1},
	}
	for _, c := range cases {
		if got := c.rt.Capacity(c.params); got != c.want {
			t.Errorf("%s %v: capacity %d, want %d", c.rt.ID(), c.params, got, c.want)
		}
	}
	plan := &v1.MemoryPlan{Params: map[string]string{"n_ctx": "8192"}}
	unified := map[string]string{"kv_unified": "on", "n_ctx": "16384"}
	if (LlamaCpp{}).KVCapacity(unified, plan) != 16384 || (LlamaCpp{}).KVCapacity(map[string]string{"kv_unified": "on"}, plan) != 8192 || (LlamaCpp{}).KVCapacity(map[string]string{"kv_unified": "on"}, nil) != 0 {
		t.Fatal("a unified llama.cpp pool holds its recorded context, or else the planned one")
	}
	if (LlamaCpp{}).KVCapacity(map[string]string{"n_ctx": "16384"}, plan) != 16384 || (LlamaCpp{}).KVCapacity(map[string]string{"kv_unified": "off"}, plan) != 8192 {
		t.Fatal("split slots share the recorded pool, else the planned one")
	}
	if (LlamaCpp{}).KVCapacity(map[string]string{"kv_unified": "on", "kv_unified_per_slot": "4096", "n_parallel": "4"}, nil) != 16384 || (LlamaCpp{}).KVCapacity(map[string]string{"kv_unified_per_slot": "4096"}, nil) != 0 {
		t.Fatal("a per slot limit sizes a unified pool to slots times the limit when no context is recorded")
	}
	if (NeMo{}).KVCapacity(map[string]string{"n_ctx": "4096", "max_batch_size": "8", "num_replicas": "2"}, nil) != 65536 || (NeMo{}).KVCapacity(map[string]string{"max_batch_size": "2"}, nil) != 8192 {
		t.Fatal("NeMo holds one full context per batched sequence")
	}
	for _, rt := range []Runtime{VLLM{}, SGLang{}, SDCpp{}} {
		if rt.KVCapacity(map[string]string{"n_ctx": "4096"}, plan) != 0 {
			t.Errorf("%s reports KV in blocks or not at all", rt.ID())
		}
	}
}

// llama.cpp and SGLang read the counts they pick themselves back from their logs
func TestDeferredCounts(t *testing.T) {
	slots := (LlamaCpp{}).Policy().Deferred["n_parallel"]
	if v, ok := slots([]string{"srv    load_model: loading model", "srv          init: initializing slots, n_slots = 4"}); !ok || v != "4" {
		t.Fatalf("llama.cpp slots %q %v", v, ok)
	}
	if _, ok := slots([]string{"n_slots = x"}); ok {
		t.Fatal("no slot line")
	}
	running := (SGLang{}).Policy().Deferred["max_running_requests"]
	if v, ok := running([]string{"[2026-11-02 10:00:00] max_total_num_tokens=500000, chunked_prefill_size=8192, max_prefill_tokens=16384, max_running_requests=2048, context_len=32768"}); !ok || v != "2048" {
		t.Fatalf("sglang running %q %v", v, ok)
	}
	if _, ok := running([]string{"max_running_requests=None"}); ok {
		t.Fatal("an unset cap is no count")
	}
	// A slot count left to llama.cpp is not recorded, so the log fills it, and its pool is unified as llama.cpp unifies it
	params, _ := Resolve(LlamaCpp{}, map[string]string{"n_parallel": "-1"})
	cmd, err := LlamaCpp{}.Launch(Launch{Name: "m", Params: params, Artifacts: map[string]string{"weights": filepath.Join(t.TempDir(), "w.gguf")}, Host: "127.0.0.1", Port: 9, Install: Install{Path: "/bin/llama-server"}})
	if err != nil || !strings.Contains(strings.Join(cmd.Args, " "), "--parallel -1") || cmd.Params["n_parallel"] != "" || cmd.Params["kv_unified"] != "on" {
		t.Fatalf("auto slots %v %v", cmd, err)
	}
}

// vLLM passes its sequence cap, LoRA environment and modules, sleep dev mode, and gRPC through the Rust frontend
func TestVLLMLaunchServing(t *testing.T) {
	rt := VLLM{}
	launch := func(overrides map[string]string, devices []*v1.Device, facts map[string]string) (*Command, error) {
		params, err := Resolve(rt, overrides)
		if err != nil {
			t.Fatal(err)
		}
		return rt.Launch(Launch{Name: "m", Params: params, Artifacts: map[string]string{"weights_dir": "/w"}, Host: "127.0.0.1", Port: 8000, Install: Install{Path: "/venv/bin/vllm"}, Devices: devices, InstallRecord: recorded(facts)})
	}
	for _, c := range []struct {
		device *v1.Device
		want   string
	}{
		{card("g", "0", "NVIDIA H100 80GB HBM3", 80), "1024"},
		{card("g", "0", "NVIDIA A100-SXM4-80GB", 80), "256"},
		{card("g", "0", "NVIDIA GB10", 120), "256"},
		{card("g", "0", "NVIDIA B200", 180), "1024"},
		{card("g", "0", "NVIDIA RTX 4090", 24), "256"},
	} {
		cmd, err := launch(nil, []*v1.Device{c.device}, nil)
		if err != nil || cmd.Params["max_num_seqs"] != c.want || !strings.Contains(strings.Join(cmd.Args, " "), "--max-num-seqs "+c.want) {
			t.Errorf("%s: %v %v", c.device.GetName(), cmd, err)
		}
	}
	if _, err := launch(nil, nil, nil); err == nil || !strings.Contains(err.Error(), "max_num_seqs is unset") {
		t.Fatalf("no device to size the cap from: %v", err)
	}
	gpu := []*v1.Device{card("g", "0", "NVIDIA RTX 4090", 24)}
	cmd, err := launch(nil, gpu, nil)
	if err != nil || !strings.Contains(strings.Join(cmd.Args, " "), "--enable-prefix-caching") || cmd.Env["VLLM_ALLOW_RUNTIME_LORA_UPDATING"] != "" || cmd.Env["VLLM_SERVER_DEV_MODE"] != "" {
		t.Fatalf("defaults %v %v", cmd, err)
	}
	cmd, err = launch(map[string]string{"lora_modules": "sql=/a/sql, chat=/a/chat"}, gpu, nil)
	line := ""
	if err == nil {
		line = strings.Join(cmd.Args, " ")
	}
	if err != nil || !strings.Contains(line, "--enable-lora") || !strings.Contains(line, "--max-loras 4") || !strings.Contains(line, "--lora-modules sql=/a/sql chat=/a/chat --kv-events-config") || cmd.Env["VLLM_ALLOW_RUNTIME_LORA_UPDATING"] != "True" {
		t.Fatalf("adapters at launch %s %v %v", line, cmd, err)
	}
	if cmd, err = launch(map[string]string{"lora_modules": "sql=/a/sql", "max_loras": "2"}, gpu, nil); err != nil || !strings.Contains(strings.Join(cmd.Args, " "), "--max-loras 2") {
		t.Fatalf("a set adapter cap stands %v %v", cmd, err)
	}
	if _, err = launch(map[string]string{"lora_modules": "sql"}, gpu, nil); err == nil || !strings.Contains(err.Error(), "name=path") {
		t.Fatalf("a module without a path: %v", err)
	}
	if cmd, err = launch(map[string]string{"enable_lora": "true"}, gpu, nil); err != nil || cmd.Env["VLLM_ALLOW_RUNTIME_LORA_UPDATING"] != "True" || strings.Contains(strings.Join(cmd.Args, " "), "--max-loras") {
		t.Fatalf("runtime adapters alone %v %v", cmd, err)
	}
	if cmd, err = launch(map[string]string{"enable_sleep_mode": "true"}, gpu, nil); err != nil || cmd.Env["VLLM_SERVER_DEV_MODE"] != "1" || !strings.Contains(strings.Join(cmd.Args, " "), "--enable-sleep-mode") {
		t.Fatalf("sleep mode %v %v", cmd, err)
	}
	if _, err = launch(map[string]string{"grpc": "true"}, gpu, nil); err == nil || !strings.Contains(err.Error(), "--grpc-port") {
		t.Fatalf("grpc on an install without the Rust frontend: %v", err)
	}
	cmd, err = launch(map[string]string{"grpc": "true"}, gpu, map[string]string{factGRPCPort: "--grpc-port"})
	if err != nil || !strings.HasSuffix(strings.Join(cmd.Args, " "), "--grpc-port 8001") || cmd.Env["VLLM_USE_RUST_FRONTEND"] != "1" || cmd.Params["grpc_port"] != "8001" {
		t.Fatalf("grpc %v %v", cmd, err)
	}
	if rt.AppProtocol(cmd.Params) != v1.AppProtocol_APP_PROTOCOL_GRPC || rt.GRPCPort(cmd.Params, 8000) != 8001 || rt.PortSpan(Launch{Params: estimate.Params{"grpc": true}}) != 4 {
		t.Fatal("a gRPC launch is spoken to over gRPC on the port after its HTTP port, its KV event ports after that")
	}
	if cmd, err = launch(map[string]string{"enable_prefix_caching": "off"}, gpu, nil); err != nil || strings.Contains(strings.Join(cmd.Args, " "), "--kv-events-config") {
		t.Fatalf("no prefix cache publishes no events %v %v", cmd, err)
	}
	if rt.AppProtocol(nil) != v1.AppProtocol_APP_PROTOCOL_HTTP || rt.GRPCPort(nil, 8000) != 0 || rt.PortSpan(Launch{}) != 3 {
		t.Fatal("an HTTP launch")
	}
	for _, p := range rt.Probes() {
		if p.Key == factGRPCPort {
			if v, ok := p.Parse("  --grpc-port GRPC_PORT  Enable the Rust frontend's additional gRPC"); !ok || v != "--grpc-port" {
				t.Fatalf("grpc probe %q %v", v, ok)
			}
			return
		}
	}
	t.Fatal("no grpc probe")
}

// SGLang serves metrics by default, takes adapters at launch and runtime, and serves its native gRPC beside HTTP
func TestSGLangLaunchServing(t *testing.T) {
	rt := SGLang{}
	launch := func(overrides map[string]string, facts map[string]string) (*Command, error) {
		params, err := Resolve(rt, overrides)
		if err != nil {
			t.Fatal(err)
		}
		return rt.Launch(Launch{Name: "m", Params: params, Artifacts: map[string]string{"weights_dir": "/w"}, Host: "127.0.0.1", Port: 30000, Install: Install{Path: "/venv/bin/python"}, InstallRecord: recorded(facts)})
	}
	cmd, err := launch(nil, nil)
	if err != nil || !strings.Contains(strings.Join(cmd.Args, " "), "--enable-metrics") || strings.Contains(strings.Join(cmd.Args, " "), "--disable-radix-cache") {
		t.Fatalf("defaults %v %v", cmd, err)
	}
	cmd, err = launch(map[string]string{"lora_paths": "sql=/a/sql", "lora_target_modules": "q_proj, v_proj", "max_lora_rank": "64", "max_loras_per_batch": "4"}, nil)
	line := ""
	if err == nil {
		line = strings.Join(cmd.Args, " ")
	}
	for _, want := range []string{"--enable-lora", "--max-lora-rank 64", "--max-loras-per-batch 4", "--lora-paths sql=/a/sql", "--lora-target-modules q_proj v_proj"} {
		if !strings.Contains(line, want) {
			t.Errorf("missing %s in %s %v", want, line, err)
		}
	}
	if _, err = launch(map[string]string{"grpc": "true"}, nil); err == nil || !strings.Contains(err.Error(), "--grpc-port") {
		t.Fatalf("grpc on an install without the native server: %v", err)
	}
	cmd, err = launch(map[string]string{"grpc": "true"}, map[string]string{factGRPCPort: "--grpc-port"})
	if err != nil || !strings.HasSuffix(strings.Join(cmd.Args, " "), "--grpc-port 30001") || rt.AppProtocol(cmd.Params) != v1.AppProtocol_APP_PROTOCOL_GRPC || rt.GRPCPort(cmd.Params, 30000) != 30001 {
		t.Fatalf("grpc %v %v", cmd, err)
	}
}

// NeMo starts its own Ray cluster with the deploy script's settings and its metrics export pinned after the served port
func TestNeMoRayBootstrap(t *testing.T) {
	params, err := Resolve(NeMo{}, map[string]string{"num_cpus": "16", "include_dashboard": "true", "runtime_env": `{"env_vars":{"A":"1"}}`})
	if err != nil {
		t.Fatal(err)
	}
	cmd, err := NeMo{}.Launch(Launch{Name: "m", Params: params, Artifacts: map[string]string{"weights_dir": "/w"}, Host: "127.0.0.1", Port: 9000, Install: Install{Path: "/opt/deploy_ray_inframework.py", Dir: "/opt"}})
	if err != nil {
		t.Fatal(err)
	}
	if cmd.Command != "/opt/venv/bin/python" || cmd.Args[0] != "-c" || cmd.Args[1] != nemoBootstrap || cmd.Args[3] != "/opt/deploy_ray_inframework.py" || cmd.Params["metrics_port"] != "9001" {
		t.Fatalf("bootstrap %v", cmd.Args)
	}
	for _, want := range []string{`"address":"local"`, `"_metrics_export_port":9001`, `"num_gpus":1`, `"num_cpus":16`, `"include_dashboard":true`, `"runtime_env":{"env_vars":{"A":"1"}}`} {
		if !strings.Contains(cmd.Args[2], want) {
			t.Errorf("ray init %s lacks %s", cmd.Args[2], want)
		}
	}
	if !strings.Contains(strings.Join(cmd.Args[3:], " "), "--megatron_checkpoint /w --host 127.0.0.1 --port 9000") {
		t.Fatalf("script args %v", cmd.Args[3:])
	}
	params["runtime_env"] = "{"
	if _, err := (NeMo{}).Launch(Launch{Name: "m", Params: params, Artifacts: map[string]string{"weights_dir": "/w"}, Host: "127.0.0.1", Port: 9000, Install: Install{Path: "/x", Dir: "/opt"}}); err == nil || !strings.Contains(err.Error(), "runtime_env") {
		t.Fatalf("a bad runtime_env: %v", err)
	}
}

// A port rule counts a bind failure on any port the launch binds and ignores one naming another port
func TestPortTaken(t *testing.T) {
	cases := []struct {
		rt     Runtime
		params map[string]string
		lines  []string
		want   bool
	}{
		{VLLM{}, nil, []string{"ERROR: [Errno 98] error while attempting to bind on address ('127.0.0.1', 8000): address already in use"}, true},
		{VLLM{}, nil, []string{"OSError: [Errno 98] Address already in use"}, true},
		{VLLM{}, nil, []string{"ERROR: [Errno 98] error while attempting to bind on address ('127.0.0.1', 29500): address already in use"}, false},
		{VLLM{}, map[string]string{"grpc": "true"}, []string{"error while attempting to bind on address ('0.0.0.0', 8001): address already in use"}, true},
		{VLLM{}, nil, []string{"error while attempting to bind on address ('0.0.0.0', 8001): address already in use"}, true},
		{VLLM{}, nil, []string{"error while attempting to bind on address ('0.0.0.0', 8003): address already in use"}, false},
		{SGLang{}, nil, []string{"INFO: Started server process", "[Errno 98] Address already in use"}, true},
		{NeMo{}, nil, []string{"address ('0.0.0.0', 8001): address already in use"}, true},
		{LlamaCpp{}, nil, []string{"srv  start: couldn't bind HTTP server socket, hostname: 127.0.0.1, port: 8000"}, true},
		{LlamaCpp{}, nil, []string{"srv  start: couldn't bind HTTP server socket, hostname: 127.0.0.1, port: 8080"}, false},
		{LlamaCpp{}, nil, []string{"load_model: loaded"}, false},
		{SDCpp{}, nil, []string{"[INFO ] main.cpp:400 - listening on: http://127.0.0.1:8000"}, true},
		{SDCpp{}, nil, []string{"[INFO ] main.cpp:400 - listening on: http://127.0.0.1:8002"}, false},
	}
	for i, c := range cases {
		params, err := Resolve(c.rt, c.params)
		if err != nil {
			t.Fatal(err)
		}
		if got := PortTaken(c.rt, c.rt.PortSpan(Launch{Params: params}), 8000, c.lines); got != c.want {
			t.Errorf("case %d %s: %v, want %v", i, c.rt.ID(), got, c.want)
		}
		if c.rt.PortRule().ID != portRuleID {
			t.Errorf("%s rule id %s", c.rt.ID(), c.rt.PortRule().ID)
		}
	}
}

// The inventory names context per request, quantization, accelerators, modalities and where the weights run
func TestInventory(t *testing.T) {
	profile, plan := plannedHost()
	desc := &v1.Descriptor{Kind: v1.ModelKind_MODEL_KIND_LANGUAGE, Params: map[string]float64{"n_ctx_train": 32768}, Precision: &v1.Precision{Label: "BF16"},
		Groups: []*v1.TensorGroup{{Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_LAYER}, {Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_VISION}}}
	in := Launch{Descriptor: desc, Plan: plan, Profile: profile}
	inv, err := VLLM{}.Inventory(in, map[string]string{"n_ctx": "16384"})
	if err != nil || inv.GetContextLength() != 16384 || inv.GetQuantization() != "BF16" || inv.GetAccelerator() != "2x NVIDIA H100 80GB HBM3" || strings.Join(inv.GetModalities(), ",") != "text,image" || inv.GetThroughputClass() != "device" {
		t.Fatalf("vllm %v %v", inv, err)
	}
	if inv, err = (VLLM{}).Inventory(in, map[string]string{"quantization": "fp8", "limit_mm_per_prompt": `{"image":0}`}); err != nil || inv.GetContextLength() != 32768 || inv.GetQuantization() != "fp8" || strings.Join(inv.GetModalities(), ",") != "text" {
		t.Fatalf("vllm unset context, quantized, images off %v %v", inv, err)
	}
	if inv, err = (VLLM{}).Inventory(in, map[string]string{"limit_mm_per_prompt": `{"image":{"count":2}}`}); err != nil || strings.Join(inv.GetModalities(), ",") != "text,image" {
		t.Fatalf("vllm image limit object %v %v", inv, err)
	}
	if inv, err = (VLLM{}).Inventory(in, map[string]string{"cpu_offload_gb": "4"}); err != nil || inv.GetThroughputClass() != "offload" {
		t.Fatalf("vllm weights offloaded by param %v %v", inv, err)
	}
	if _, err = (VLLM{}).Inventory(in, map[string]string{"limit_mm_per_prompt": `[1]`}); err == nil {
		t.Fatal("a limit that is not an object")
	}
	if inv, err = (SGLang{}).Inventory(in, nil); err != nil || inv.GetContextLength() != 32768 {
		t.Fatalf("sglang %v %v", inv, err)
	}
	if inv, err = (NeMo{}).Inventory(in, nil); err != nil || inv.GetContextLength() != 4096 || strings.Join(inv.GetModalities(), ",") != "text" {
		t.Fatalf("nemo %v %v", inv, err)
	}
	// llama.cpp splits the pool over slots unless it is unified, and reads the projector's encoders
	dir := t.TempDir()
	mmproj := filepath.Join(dir, "mmproj.gguf")
	writeGGUF(t, mmproj, map[string]string{"general.architecture": "clip"}, map[string]bool{"clip.has_vision_encoder": true, "clip.has_audio_encoder": true})
	for _, c := range []struct {
		params map[string]string
		want   uint32
	}{
		{map[string]string{"n_ctx": "16384", "n_parallel": "4"}, 4096},
		{map[string]string{"n_ctx": "16384", "n_parallel": "4", "kv_unified": "on"}, 16384},
		{map[string]string{"n_ctx": "16384", "n_parallel": "4", "kv_unified": "on", "kv_unified_per_slot": "8192"}, 8192},
		{map[string]string{"n_parallel": "4"}, 32768},
	} {
		if inv, err := (LlamaCpp{}).Inventory(in, c.params); err != nil || inv.GetContextLength() != c.want {
			t.Errorf("llama.cpp %v: %v %v, want %d", c.params, inv, err, c.want)
		}
	}
	if inv, err = (LlamaCpp{}).Inventory(in, map[string]string{"mmproj": mmproj}); err != nil || strings.Join(inv.GetModalities(), ",") != "text,image,audio" {
		t.Fatalf("llama.cpp projector %v %v", inv, err)
	}
	if _, err = (LlamaCpp{}).Inventory(in, map[string]string{"mmproj": filepath.Join(dir, "missing.gguf")}); err == nil {
		t.Fatal("a projector that cannot be read")
	}
	// Layers on host memory make an offload run, host placement a host run on cpu
	spill := &v1.MemoryPlan{Pools: plan.GetPools(), Placements: append(plan.GetPlacements(), &v1.GroupPlacement{Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_LAYER, PoolId: "host", Bytes: 1})}
	if inv, err = (LlamaCpp{}).Inventory(Launch{Descriptor: desc, Plan: spill, Profile: profile}, nil); err != nil || inv.GetThroughputClass() != "offload" {
		t.Fatalf("offload %v %v", inv, err)
	}
	if inv, err = (LlamaCpp{}).Inventory(Launch{Descriptor: desc, Placement: v1.Placement_PLACEMENT_HOST}, nil); err != nil || inv.GetAccelerator() != "cpu" || inv.GetThroughputClass() != "host" {
		t.Fatalf("host %v %v", inv, err)
	}
	if _, err = (LlamaCpp{}).Inventory(Launch{Descriptor: desc}, nil); err == nil || !strings.Contains(err.Error(), "no host profile") {
		t.Fatalf("nothing names the devices: %v", err)
	}
	// A run without a restriction sees every GPU of the host
	diffusion := Launch{Descriptor: &v1.Descriptor{Kind: v1.ModelKind_MODEL_KIND_DIFFUSION, Generates: []string{"image", "video"}}, Profile: profile}
	if inv, err = (SDCpp{}).Inventory(diffusion, map[string]string{"offload": "off"}); err != nil || inv.GetContextLength() != 0 || inv.GetAccelerator() != "1x NVIDIA RTX 4090 + 2x NVIDIA H100 80GB HBM3" || strings.Join(inv.GetModalities(), ",") != "text,image,video" || inv.GetThroughputClass() != "device" {
		t.Fatalf("sdcpp %v %v", inv, err)
	}
	if inv, err = (SDCpp{}).Inventory(diffusion, map[string]string{"offload": "on"}); err != nil || inv.GetThroughputClass() != "offload" {
		t.Fatalf("sdcpp offloading %v %v", inv, err)
	}
}

// Every LoRA file beside the model loads unapplied, an explicit adapter list replaces the scan
func TestLlamaCppAdaptersBesideTheModel(t *testing.T) {
	dir := t.TempDir()
	weights := filepath.Join(dir, "model.gguf")
	writeGGUF(t, weights, map[string]string{"general.type": "model"}, nil)
	writeGGUF(t, filepath.Join(dir, "sql.gguf"), map[string]string{"general.type": "adapter", "adapter.type": "lora"}, nil)
	writeGGUF(t, filepath.Join(dir, "cv.gguf"), map[string]string{"general.type": "adapter", "adapter.type": "control_vector"}, nil)
	writeGGUF(t, filepath.Join(dir, "mmproj.gguf"), map[string]string{"general.type": "mmproj"}, nil)
	os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o644)
	rt := LlamaCpp{}
	params, _ := Resolve(rt, nil)
	in := Launch{Name: "m", Params: params, Artifacts: map[string]string{"weights": weights}, Host: "127.0.0.1", Port: 9, Install: Install{Path: "/bin/llama-server"}}
	cmd, err := rt.Launch(in)
	sql := filepath.Join(dir, "sql.gguf")
	if err != nil || !strings.Contains(strings.Join(cmd.Args, " "), "--lora "+sql+" ") || !strings.Contains(strings.Join(cmd.Args, " "), "--lora-init-without-apply") || cmd.Params["lora"] != sql {
		t.Fatalf("adapters beside %v %v", cmd, err)
	}
	if rt.Adapters("http://x", cmd.Params) == nil {
		t.Fatal("a launch with adapters switches them")
	}
	params, _ = Resolve(rt, map[string]string{"lora": "/other.gguf"})
	in.Params = params
	if cmd, err = rt.Launch(in); err != nil || strings.Contains(strings.Join(cmd.Args, " "), sql) || strings.Contains(strings.Join(cmd.Args, " "), "--lora-init-without-apply") {
		t.Fatalf("listed adapters replace the scan %v %v", cmd, err)
	}
	os.WriteFile(filepath.Join(dir, "broken.gguf"), []byte("nope"), 0o644)
	in.Params, _ = Resolve(rt, nil)
	if _, err = rt.Launch(in); err == nil || !strings.Contains(err.Error(), "broken.gguf") {
		t.Fatalf("an unreadable candidate: %v", err)
	}
	if rt.Adapters("http://x", map[string]string{}) != nil {
		t.Fatal("no adapters, nothing to switch")
	}
}

// Only runtimes and launches that switch adapters or sleep hand out clients
func TestClientsFollowTheLaunch(t *testing.T) {
	on := map[string]string{"enable_lora": "true", "enable_sleep_mode": "true"}
	if (VLLM{}).Adapters("http://x", on) == nil || (VLLM{}).Adapters("http://x", nil) != nil || (SGLang{}).Adapters("http://x", on) == nil || (SGLang{}).Adapters("http://x", nil) != nil {
		t.Fatal("named adapters follow enable_lora")
	}
	if (VLLM{}).Sleeper("http://x", on) == nil || (VLLM{}).Sleeper("http://x", nil) != nil {
		t.Fatal("sleep follows enable_sleep_mode")
	}
	for _, rt := range []Runtime{LlamaCpp{}, SGLang{}, NeMo{}, SDCpp{}} {
		if rt.Sleeper("http://x", on) != nil {
			t.Errorf("%s does not sleep", rt.ID())
		}
	}
	for _, rt := range []Runtime{NeMo{}, SDCpp{}} {
		if rt.Adapters("http://x", map[string]string{"enable_lora": "true", "lora": "/a.gguf"}) != nil {
			t.Errorf("%s loads no adapters at runtime", rt.ID())
		}
	}
	for _, rt := range []Runtime{LlamaCpp{}, NeMo{}, SDCpp{}} {
		if rt.AppProtocol(map[string]string{"grpc": "true"}) != v1.AppProtocol_APP_PROTOCOL_HTTP || rt.GRPCPort(map[string]string{"grpc": "true"}, 9) != 0 {
			t.Errorf("%s serves HTTP alone", rt.ID())
		}
	}
}

// A pinned device without memory of its own is refused by every runtime, and the accelerator class counts the devices with memory alone
func TestDevicesWithoutMemoryAreNeverPinned(t *testing.T) {
	profile, plan := plannedHost()
	igpu := &v1.Device{Id: "intel-0000:00:02.0", Kind: v1.DeviceKind_DEVICE_KIND_GPU, Vendor: "intel", Name: "AlderLake-S GT1", Facts: map[string]string{"index": "1", "memory": "shared"}}
	profile.Devices = append(profile.Devices, igpu)
	weights := filepath.Join(t.TempDir(), "w.gguf")
	for _, rt := range []Runtime{LlamaCpp{}, VLLM{}, SGLang{}, NeMo{}, SDCpp{}} {
		params, err := Resolve(rt, nil)
		if err != nil {
			t.Fatal(err)
		}
		in := Launch{Name: "m", Params: params, Artifacts: map[string]string{"weights": weights, "weights_dir": "/w", "prepared_dir": t.TempDir()}, Host: "127.0.0.1", Port: 9, Install: Install{Path: "/bin/x", Dir: "/opt"}, Plan: plan, Profile: profile, Devices: []*v1.Device{igpu}, Descriptor: layered(4), InstallRecord: recorded(nil)}
		if _, err := rt.Launch(in); err == nil || !strings.Contains(err.Error(), `device "intel-0000:00:02.0" (AlderLake-S GT1) has no memory of its own, so a launch cannot pin it`) {
			t.Errorf("%s pins a device without memory: %v", rt.ID(), err)
		}
		in.Profile = nil
		if _, err := rt.Launch(in); err == nil || !strings.Contains(err.Error(), "has no memory of its own") {
			t.Errorf("%s without a profile pins a device without memory: %v", rt.ID(), err)
		}
	}
	none := &v1.MemoryPlan{Pools: []*v1.PoolUsage{{PoolId: "host", Kind: v1.PoolKind_POOL_KIND_HOST, UsedBytes: 5}}}
	got, err := acceleratorOf(Launch{Plan: none, Profile: profile})
	if err != nil || got != "1x NVIDIA RTX 4090 + 2x NVIDIA H100 80GB HBM3" {
		t.Fatalf("accelerator over every device with memory: %q %v", got, err)
	}
}
