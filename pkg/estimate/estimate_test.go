package estimate_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/nickheyer/nebu/pkg/archs"
	"github.com/nickheyer/nebu/pkg/estimate"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/runtimes"
	"google.golang.org/protobuf/encoding/protojson"
)

const (
	mib = 1 << 20
	gib = 1 << 30
)

// The policy and typed default params of one shipped runtime
func policy(t *testing.T, id string) (*estimate.Policy, estimate.Params) {
	t.Helper()
	for _, rt := range runtimes.All() {
		if rt.ID() != id {
			continue
		}
		params, err := runtimes.Resolve(rt, nil)
		if err != nil {
			t.Fatal(err)
		}
		return rt.Policy(), params
	}
	t.Fatalf("runtime %s missing", id)
	return nil, nil
}

// Runtime defaults with fixed context and unquantized cache for exact byte counts.
func defaults(params estimate.Params, overrides map[string]any) estimate.Params {
	out := params.Clone()
	if out.IsAuto("n_ctx") {
		out["n_ctx"] = int64(8192)
	}
	for _, k := range []string{"cache_type_k", "cache_type_v"} {
		if _, ok := out[k]; ok {
			out[k] = "f16"
		}
	}
	for k, v := range overrides {
		out[k] = v
	}
	return out
}

func descriptor(layers int, layerBytes, expertBytes uint64) *v1.Descriptor {
	d := &v1.Descriptor{Params: map[string]float64{
		"n_layer": float64(layers), "n_head_kv": 8, "head_dim": 128, "head_dim_v": 128, "n_embd": 4096, "n_vocab": 150000,
	}}
	d.Groups = append(d.Groups, &v1.TensorGroup{Id: "embedding", Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_EMBEDDING, Layer: -1, Bytes: 300 * mib})
	for i := 0; i < layers; i++ {
		d.Groups = append(d.Groups, &v1.TensorGroup{Id: "layer." + strconv.Itoa(i), Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_LAYER, Layer: int32(i), Bytes: layerBytes})
		if expertBytes > 0 {
			d.Groups = append(d.Groups, &v1.TensorGroup{Id: "experts." + strconv.Itoa(i), Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_EXPERTS, Layer: int32(i), Bytes: expertBytes})
		}
	}
	d.Groups = append(d.Groups, &v1.TensorGroup{Id: "output", Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_OUTPUT, Layer: -1, Bytes: 300 * mib})
	return d
}

func host(device, hostMem uint64) *v1.HostProfile {
	p := &v1.HostProfile{}
	if device > 0 {
		p.Pools = append(p.Pools, &v1.MemoryPool{Id: "gpu0", Kind: v1.PoolKind_POOL_KIND_DEVICE, TotalBytes: device})
	}
	if hostMem > 0 {
		p.Pools = append(p.Pools, &v1.MemoryPool{Id: "host", Kind: v1.PoolKind_POOL_KIND_HOST, TotalBytes: hostMem})
	}
	return p
}

func input(d *v1.Descriptor, h *v1.HostProfile, params estimate.Params) estimate.Input {
	return estimate.Input{Descriptor: d, Family: archs.Default{}, Host: h, Params: params}
}

func TestDenseFits(t *testing.T) {
	p, params := policy(t, "llamacpp")
	plan, err := p.Plan(input(descriptor(28, 100*mib, 0), host(12*gib, 64*gib), defaults(params, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if plan.GetVerdict() != v1.FitVerdict_FIT_VERDICT_FITS || plan.GetParams()["n_gpu_layers"] != "29" {
		t.Fatalf("plan %+v", plan)
	}
	if plan.GetCacheBytes() != 8192*28*8*256*2 {
		t.Fatalf("cache %d", plan.GetCacheBytes())
	}
	if plan.GetWeightsBytes() != 28*100*mib+600*mib {
		t.Fatalf("weights %d", plan.GetWeightsBytes())
	}
	var dev, hostUsed uint64
	for _, pu := range plan.GetPools() {
		if pu.GetKind() == v1.PoolKind_POOL_KIND_DEVICE {
			dev = pu.GetUsedBytes()
		} else {
			hostUsed = pu.GetUsedBytes()
		}
	}
	if hostUsed != 300*mib || dev < 28*100*mib {
		t.Fatalf("pools dev=%d host=%d", dev, hostUsed)
	}
	// Plans retain pool capacity, free memory, and profile timestamp.
	for _, pu := range plan.GetPools() {
		if pu.GetTotalBytes() == 0 || pu.GetTotalBytes() != pu.GetCapacityBytes() {
			t.Fatalf("pool totals %+v", pu)
		}
	}
	if plan.GetPlannedAt() == nil {
		t.Fatal("a plan carries the time it was made")
	}
}

func TestFreePlansCarryTheWholePool(t *testing.T) {
	p, params := policy(t, "llamacpp")
	h := host(12*gib, 64*gib)
	h.Pools[0].FreeBytes = 6 * gib
	h.Pools[1].FreeBytes = 32 * gib
	in := input(descriptor(28, 100*mib, 0), h, defaults(params, nil))
	in.Free = true
	plan, err := p.Plan(in)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.GetAgainstFree() {
		t.Fatal("against free")
	}
	for _, pu := range plan.GetPools() {
		want := map[string][2]uint64{"gpu0": {12 * gib, 6 * gib}, "host": {64 * gib, 32 * gib}}[pu.GetPoolId()]
		if pu.GetTotalBytes() != want[0] || pu.GetFreeBytes() != want[1] || pu.GetCapacityBytes() != want[1] {
			t.Fatalf("pool %+v", pu)
		}
	}
}

func TestDensePartialAndFixed(t *testing.T) {
	p, params := policy(t, "llamacpp")
	in := input(descriptor(28, 500*mib, 0), host(8*gib, 64*gib), defaults(params, nil))
	plan, err := p.Plan(in)
	if err != nil {
		t.Fatal(err)
	}
	if plan.GetVerdict() != v1.FitVerdict_FIT_VERDICT_PARTIAL {
		t.Fatalf("plan %+v", plan)
	}
	n, _ := strconv.Atoi(plan.GetParams()["n_gpu_layers"])
	if n <= 0 || n >= 29 {
		t.Fatalf("solved layers %d", n)
	}
	in.Params = defaults(params, map[string]any{"n_gpu_layers": int64(10)})
	plan, err = p.Plan(in)
	if err != nil {
		t.Fatal(err)
	}
	if plan.GetParams()["n_gpu_layers"] != "10" {
		t.Fatalf("fixed override ignored %v", plan.GetParams())
	}
	in.Host = host(256*mib, 64*gib)
	in.Params = defaults(params, nil)
	plan, err = p.Plan(in)
	if err != nil {
		t.Fatal(err)
	}
	if plan.GetVerdict() != v1.FitVerdict_FIT_VERDICT_NO || plan.GetDetail() == "" {
		t.Fatalf("plan %+v", plan)
	}
	// A host with no device memory holds the whole model in host memory
	in.Host = host(0, 64*gib)
	plan, err = p.Plan(in)
	if err != nil || plan.GetVerdict() != v1.FitVerdict_FIT_VERDICT_FITS || plan.GetParams()["n_gpu_layers"] != "0" || len(plan.GetPools()) != 1 || plan.GetPools()[0].GetKind() != v1.PoolKind_POOL_KIND_HOST {
		t.Fatalf("no device pools: %+v %v", plan, err)
	}
}

func TestMoESpillsExpertsFirst(t *testing.T) {
	p, params := policy(t, "llamacpp")
	plan, err := p.Plan(input(descriptor(48, 50*mib, 400*mib), host(12*gib, 64*gib), defaults(params, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if plan.GetVerdict() != v1.FitVerdict_FIT_VERDICT_PARTIAL || plan.GetParams()["n_gpu_layers"] != "49" {
		t.Fatalf("plan %+v", plan)
	}
	cpuMoe, _ := strconv.Atoi(plan.GetParams()["n_cpu_moe"])
	if cpuMoe <= 0 || cpuMoe >= 48 {
		t.Fatalf("n_cpu_moe %d", cpuMoe)
	}
	var expertsDevice, expertsHost uint32
	for _, pl := range plan.GetPlacements() {
		if pl.GetKind() == v1.TensorGroupKind_TENSOR_GROUP_KIND_EXPERTS {
			if pl.GetPoolId() == "device" {
				expertsDevice = pl.GetCount()
			} else {
				expertsHost = pl.GetCount()
			}
		}
	}
	if expertsHost != uint32(cpuMoe) || expertsDevice+expertsHost != 48 {
		t.Fatalf("placements %v", plan.GetPlacements())
	}
}

func TestDeviceOnlyPolicy(t *testing.T) {
	p, params := policy(t, "vllm")
	plan, err := p.Plan(input(descriptor(28, 100*mib, 0), host(12*gib, 64*gib), defaults(params, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if plan.GetVerdict() != v1.FitVerdict_FIT_VERDICT_FITS {
		t.Fatalf("plan %+v", plan)
	}
	plan, err = p.Plan(input(descriptor(28, 1*gib, 0), host(12*gib, 64*gib), defaults(params, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if plan.GetVerdict() != v1.FitVerdict_FIT_VERDICT_NO {
		t.Fatalf("plan %+v", plan)
	}
}

func TestAutoContextSolvesToTheLargestThatFits(t *testing.T) {
	for _, id := range []string{"llamacpp", "vllm", "sglang", "nemo"} {
		p, params := policy(t, id)
		if !params.IsAuto("n_ctx") {
			t.Fatalf("%s: n_ctx should default to auto", id)
		}
		d := descriptor(4, 100*mib, 0)
		d.Params["n_ctx_train"] = 4096
		plan, err := p.Plan(input(d, host(24*gib, 64*gib), params))
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		if plan.GetVerdict() != v1.FitVerdict_FIT_VERDICT_FITS || plan.GetParams()["n_ctx"] != "4096" {
			t.Fatalf("%s: a roomy host takes the trained context: %v", id, plan.GetParams())
		}
		// A tight device gives a smaller context on the grid, never below the floor
		plan, err = p.Plan(input(d, host(3*gib+512*mib, 64*gib), params))
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		n, _ := strconv.Atoi(plan.GetParams()["n_ctx"])
		if n < 256 || n%256 != 0 {
			t.Fatalf("%s: solved context %d off the grid", id, n)
		}
	}
	// A header that names no trained length solves to the floor llama.cpp's fit keeps
	p, params := policy(t, "llamacpp")
	plan, err := p.Plan(input(descriptor(4, 100*mib, 0), host(24*gib, 64*gib), params))
	if err != nil || plan.GetParams()["n_ctx"] != "4096" {
		t.Fatalf("no trained length: %v %v", plan.GetParams(), err)
	}
	// A trained length under the floor is what llama.cpp runs, and parallel sequences each take one
	short := descriptor(4, 100*mib, 0)
	short.Params["n_ctx_train"] = 2048
	if plan, err = p.Plan(input(short, host(24*gib, 64*gib), params)); err != nil || plan.GetParams()["n_ctx"] != "2048" {
		t.Fatalf("short trained length: %v %v", plan.GetParams(), err)
	}
	four := params.Clone()
	four["n_parallel"] = int64(4)
	if plan, err = p.Plan(input(short, host(24*gib, 64*gib), four)); err != nil || plan.GetParams()["n_ctx"] != "8192" {
		t.Fatalf("four sequences: %v %v", plan.GetParams(), err)
	}
}

func TestParamStatesAndRefusal(t *testing.T) {
	p, params := policy(t, "llamacpp")
	d := descriptor(4, 100*mib, 0)
	d.Params["n_ctx_train"] = 8192
	in := input(d, host(24*gib, 64*gib), defaults(params, nil))
	plan, err := p.Plan(in)
	if err != nil {
		t.Fatal(err)
	}
	states, refusal := p.ParamStates(in, plan)
	if refusal != "" {
		t.Fatalf("nothing to refuse: %s", refusal)
	}
	byName := map[string]*v1.ParamState{}
	for _, s := range states {
		byName[s.GetName()] = s
	}
	if byName["n_ctx"].GetMax() != 8192 || byName["n_gpu_layers"].GetMax() != 4 || byName["n_ubatch"].GetMax() != 2048 {
		t.Fatalf("bounds %v", states)
	}
	// Parallel sequences each hold the trained length, so a set context may reach their sum
	four := input(d, host(24*gib, 64*gib), defaults(params, map[string]any{"n_parallel": int64(4), "n_ctx": int64(32768)}))
	plan, err = p.Plan(four)
	if err != nil || plan.GetParams()["n_ctx"] != "32768" {
		t.Fatalf("the set context is planned as set: %v %v", plan.GetParams(), err)
	}
	if states, _ := p.ParamStates(four, plan); states[0].GetName() != "n_ctx" || states[0].GetMax() != 32768 {
		t.Fatalf("four sequences bound the context at four trained lengths: %v", states)
	}
	in.Params = defaults(params, map[string]any{"flash_attn": "off", "cache_type_v": "q8_0"})
	if _, err := p.Plan(in); !errors.Is(err, estimate.ErrRefused) {
		t.Fatalf("a quantized value cache without flash attention should be refused: %v", err)
	}
	in.SkipRules = true
	plan, err = p.Plan(in)
	if err != nil {
		t.Fatal(err)
	}
	_, refusal = p.ParamStates(in, plan)
	if !strings.Contains(refusal, "q8_0") {
		t.Fatalf("the refusal should name the choice: %q", refusal)
	}
	for _, s := range states {
		if s.GetName() == "cache_type_v" && len(s.GetDisabled()) != 0 {
			t.Fatal("with flash attention on nothing is disabled")
		}
	}
	states, _ = p.ParamStates(in, plan)
	for _, s := range states {
		if s.GetName() == "cache_type_v" && len(s.GetDisabled()) != 5 {
			t.Fatalf("every quantized type is disabled without flash attention: %v", s)
		}
	}
	// Empty cache types stay empty and size the cache as llama.cpp's f16 does
	sized := func(k, v string) (uint64, map[string]string) {
		ps := params.Clone()
		ps["n_ctx"] = int64(1024)
		ps["cache_type_k"], ps["cache_type_v"] = k, v
		plan, err := p.Plan(input(d, host(24*gib, 64*gib), ps))
		if err != nil {
			t.Fatal(err)
		}
		return plan.GetCacheBytes(), plan.GetParams()
	}
	unset, solved := sized("", "")
	f16, _ := sized("f16", "f16")
	q8, _ := sized("q8_0", "q8_0")
	if unset != f16 || q8 >= f16 || solved["cache_type_k"] != "" || solved["cache_type_v"] != "" {
		t.Fatalf("unset %d f16 %d q8 %d params %v", unset, f16, q8, solved)
	}
}

func TestHuman(t *testing.T) {
	if estimate.Human(1536*mib) != "1.5 GiB" || estimate.Human(10) != "10 B" {
		t.Fatal(estimate.Human(1536*mib), estimate.Human(10))
	}
}

// Measured run with descriptor, launch parameters, and runtime and device allocations.
type measuredRun struct {
	Runtime         string            `json:"runtime"`
	Descriptor      json.RawMessage   `json:"descriptor"`
	Params          map[string]string `json:"params"`
	DeviceFreeBytes uint64            `json:"device_free_bytes"`
	HostFreeBytes   uint64            `json:"host_free_bytes"`
	Measured        map[string]uint64 `json:"measured"`
}

func TestPlansMatchMeasuredRuns(t *testing.T) {
	families, err := archs.New(archs.All())
	if err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(filepath.Join("testdata", "measured", "*.json"))
	if len(files) == 0 {
		t.Fatal("no measured runs")
	}
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		var run measuredRun
		if err := json.Unmarshal(data, &run); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		d := &v1.Descriptor{}
		if err := protojson.Unmarshal(run.Descriptor, d); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		family := families.Get(d.GetFamily())
		if family == nil {
			t.Fatalf("%s: no family %q", file, d.GetFamily())
		}
		p, params := policy(t, run.Runtime)
		for k, v := range run.Params {
			if n, err := strconv.ParseInt(v, 10, 64); err == nil {
				params[k] = n
			} else {
				params[k] = v
			}
		}
		host := &v1.HostProfile{Pools: []*v1.MemoryPool{
			{Id: "gpu", Kind: v1.PoolKind_POOL_KIND_DEVICE, TotalBytes: run.DeviceFreeBytes, FreeBytes: run.DeviceFreeBytes},
			{Id: "host", Kind: v1.PoolKind_POOL_KIND_HOST, TotalBytes: run.HostFreeBytes, FreeBytes: run.HostFreeBytes},
		}}
		plan, err := p.Plan(estimate.Input{Descriptor: d, Family: family, Host: host, Params: params, Free: true})
		if err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		within := func(name string, got, want uint64, low, high float64) {
			ratio := float64(got) / float64(want)
			if ratio < low || ratio > high {
				t.Errorf("%s: %s planned %s, measured %s, ratio %.3f outside [%.2f, %.2f]", filepath.Base(file), name, estimate.Human(got), estimate.Human(want), ratio, low, high)
			}
		}
		cache := run.Measured["device.cache"] + run.Measured["host.cache"]
		within("cache", plan.GetCacheBytes(), cache, 0.99, 1.01)
		weights := run.Measured["device.weights"] + run.Measured["host.weights"]
		within("weights", plan.GetWeightsBytes(), weights, 0.99, 1.01)
		// The device total may run a little over what the card reported, never under
		within("device", estimate.PlannedDevice(plan), run.Measured[estimate.DeviceUsedKey], 1.0, 1.08)
	}
}

func TestSlidingWindowAndSpannedDevices(t *testing.T) {
	// A Gemma 3 shaped model: 62 layers, a 1024 token window on five of every six
	d := &v1.Descriptor{Architecture: "gemma3", Family: "gemma3", Params: map[string]float64{"n_layer": 62, "n_head_kv": 16, "head_dim": 128, "head_dim_v": 128, "n_embd": 5376, "n_vocab": 262208, "n_swa": 1024}}
	for i := int32(0); i < 62; i++ {
		d.Groups = append(d.Groups, &v1.TensorGroup{Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_LAYER, Layer: i, Bytes: 100 * mib})
	}
	p, params := policy(t, "llamacpp")
	host := &v1.HostProfile{Pools: []*v1.MemoryPool{{Id: "g", Kind: v1.PoolKind_POOL_KIND_DEVICE, TotalBytes: 80 * gib}, {Id: "h", Kind: v1.PoolKind_POOL_KIND_HOST, TotalBytes: 80 * gib}}}
	plan, err := p.Plan(estimate.Input{Descriptor: d, Family: archs.Gemma3{}, Host: host, Params: defaults(params, map[string]any{"n_ctx": int64(32768)})})
	if err != nil {
		t.Fatal(err)
	}
	full := uint64(32768) * 62 * 16 * 256 * 2
	// Ten full layers hold 32k tokens, the other fifty-two hold the window plus a batch
	want := uint64(32768)*10*16*256*2 + uint64(1024+512)*52*16*256*2
	if plan.GetCacheBytes() != want || plan.GetCacheBytes() >= full/2 {
		t.Fatalf("sliding window cache %s, want %s of a full %s", estimate.Human(plan.GetCacheBytes()), estimate.Human(want), estimate.Human(full))
	}
	plan, err = p.Plan(estimate.Input{Descriptor: d, Family: archs.Default{}, Host: host, Params: defaults(params, map[string]any{"n_ctx": int64(32768)})})
	if err != nil || plan.GetCacheBytes() != full {
		t.Fatalf("a full cache arch ignores the window: %s %v", estimate.Human(plan.GetCacheBytes()), err)
	}
	// vLLM at tensor parallel one sees one device, llama.cpp spreads over both
	two := &v1.HostProfile{Pools: []*v1.MemoryPool{{Id: "a", Kind: v1.PoolKind_POOL_KIND_DEVICE, TotalBytes: 8 * gib}, {Id: "b", Kind: v1.PoolKind_POOL_KIND_DEVICE, TotalBytes: 8 * gib}, {Id: "h", Kind: v1.PoolKind_POOL_KIND_HOST, TotalBytes: 64 * gib}}}
	small := &v1.Descriptor{Architecture: "llama", Family: "default", Params: map[string]float64{"n_layer": 4, "n_head_kv": 8, "head_dim": 128, "head_dim_v": 128, "n_embd": 4096, "n_vocab": 32000}}
	for i := int32(0); i < 4; i++ {
		small.Groups = append(small.Groups, &v1.TensorGroup{Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_LAYER, Layer: i, Bytes: 2 * gib})
	}
	vp, vparams := policy(t, "vllm")
	one, err := vp.Plan(input(small, two, defaults(vparams, map[string]any{"n_ctx": int64(1024)})))
	if err != nil || one.GetVerdict() != v1.FitVerdict_FIT_VERDICT_NO || len(one.GetPools()) != 2 {
		t.Fatalf("tensor parallel one should plan on one device: %v %v", one, err)
	}
	both, err := vp.Plan(input(small, two, defaults(vparams, map[string]any{"n_ctx": int64(1024), "tensor_parallel_size": int64(2)})))
	if err != nil || both.GetVerdict() != v1.FitVerdict_FIT_VERDICT_FITS || len(both.GetPools()) != 3 {
		t.Fatalf("tensor parallel two should span both: %v %v", both, err)
	}
	spread, err := p.Plan(input(small, two, defaults(params, map[string]any{"n_ctx": int64(1024)})))
	if err != nil || spread.GetVerdict() != v1.FitVerdict_FIT_VERDICT_FITS {
		t.Fatalf("llama.cpp spreads over every device: %v %v", spread, err)
	}
}

func TestPlanNamesMissingParams(t *testing.T) {
	p, params := policy(t, "llamacpp")
	d := descriptor(4, 100*mib, 0)
	delete(d.Params, "n_head_kv")
	_, err := p.Plan(input(d, host(24*gib, 64*gib), defaults(params, nil)))
	var missing *estimate.MissingError
	if !errors.As(err, &missing) || strings.Join(missing.Names, ",") != "n_head_kv" || !strings.Contains(err.Error(), "the header gives no n_head_kv") {
		t.Fatalf("want a missing n_head_kv, got %v", err)
	}
	delete(d.Params, "n_layer")
	_, err = p.Plan(input(d, host(24*gib, 64*gib), defaults(params, nil)))
	if !errors.As(err, &missing) || strings.Join(missing.Names, ",") != "n_layer,n_head_kv" || !strings.Contains(err.Error(), "n_layer or n_head_kv") {
		t.Fatalf("want both named, got %v", err)
	}
	in := input(descriptor(4, 100*mib, 0), host(24*gib, 64*gib), defaults(params, nil))
	in.Family = nil
	if _, err := p.Plan(in); !errors.As(err, &missing) {
		t.Fatalf("no family should be a missing fact: %v", err)
	}
}

func usage(plan *v1.MemoryPlan, id string) *v1.PoolUsage {
	for _, pu := range plan.GetPools() {
		if pu.GetPoolId() == id {
			return pu
		}
	}
	return nil
}

func TestNoFitFillsDeviceThenSpillsAndOverflows(t *testing.T) {
	p, params := policy(t, "vllm")
	// 28 GiB of layers plus cache and overhead, on 12 GiB VRAM and 16 GiB RAM.
	plan, err := p.Plan(input(descriptor(28, gib, 0), host(12*gib, 16*gib), defaults(params, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if plan.GetVerdict() != v1.FitVerdict_FIT_VERDICT_NO {
		t.Fatalf("plan %+v", plan)
	}
	total := plan.GetWeightsBytes() + plan.GetCacheBytes() + plan.GetOverheadBytes()
	gpu, ram := usage(plan, "gpu0"), usage(plan, "host")
	if gpu == nil || ram == nil || gpu.GetUsedBytes() != 12*gib || gpu.GetCapacityBytes() != 12*gib {
		t.Fatalf("device should fill to its capacity: %v", plan.GetPools())
	}
	if ram.GetUsedBytes() != total-12*gib || ram.GetUsedBytes() <= ram.GetCapacityBytes() {
		t.Fatalf("host should take the rest and overflow: %v of %v", ram.GetUsedBytes(), total)
	}
	if estimate.PlannedDevice(plan) != 12*gib {
		t.Fatalf("planned device %d", estimate.PlannedDevice(plan))
	}
	if !strings.Contains(plan.GetDetail(), "more than") {
		t.Fatalf("detail %q", plan.GetDetail())
	}
	var device, hostSide uint32
	var placed uint64
	for _, pl := range plan.GetPlacements() {
		placed += pl.GetBytes()
		if pl.GetKind() != v1.TensorGroupKind_TENSOR_GROUP_KIND_LAYER {
			continue
		}
		if pl.GetPoolId() == "device" {
			device = pl.GetCount()
		} else {
			hostSide = pl.GetCount()
		}
	}
	if device == 0 || hostSide == 0 || device+hostSide != 28 || placed != plan.GetWeightsBytes() {
		t.Fatalf("layers should split across the sides, weights alone placed: %v", plan.GetPlacements())
	}

	// Fill device memory first, then use available host capacity.
	plan, err = p.Plan(input(descriptor(28, gib, 0), host(12*gib, 64*gib), defaults(params, nil)))
	if err != nil {
		t.Fatal(err)
	}
	gpu, ram = usage(plan, "gpu0"), usage(plan, "host")
	if plan.GetVerdict() != v1.FitVerdict_FIT_VERDICT_NO || gpu.GetUsedBytes() != 12*gib || ram.GetUsedBytes() != total-12*gib || ram.GetUsedBytes() > ram.GetCapacityBytes() {
		t.Fatalf("plan %+v", plan)
	}
	if !strings.Contains(plan.GetDetail(), "device need") {
		t.Fatalf("detail %q", plan.GetDetail())
	}

	// Fill both devices before host memory and report device counts.
	lp, lparams := policy(t, "llamacpp")
	two := &v1.HostProfile{Pools: []*v1.MemoryPool{{Id: "a", Kind: v1.PoolKind_POOL_KIND_DEVICE, TotalBytes: 8 * gib}, {Id: "b", Kind: v1.PoolKind_POOL_KIND_DEVICE, TotalBytes: 8 * gib}, {Id: "h", Kind: v1.PoolKind_POOL_KIND_HOST, TotalBytes: 4 * gib}}}
	plan, err = lp.Plan(input(descriptor(28, gib, 0), two, defaults(lparams, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if plan.GetVerdict() != v1.FitVerdict_FIT_VERDICT_NO || usage(plan, "a").GetUsedBytes() != 8*gib || usage(plan, "b").GetUsedBytes() != 8*gib || usage(plan, "h").GetUsedBytes() <= 4*gib {
		t.Fatalf("plan %+v", plan)
	}
	n, _ := strconv.Atoi(plan.GetParams()["n_gpu_layers"])
	if n <= 0 || n >= 28 {
		t.Fatalf("n_gpu_layers should say what landed on device, got %d", n)
	}
	// A host with no device memory lands everything in host memory and fits there
	plan, err = lp.Plan(input(descriptor(4, 100*mib, 0), host(0, 64*gib), defaults(lparams, nil)))
	if err != nil || plan.GetVerdict() != v1.FitVerdict_FIT_VERDICT_FITS || usage(plan, "host").GetUsedBytes() == 0 {
		t.Fatalf("no device: %+v %v", plan, err)
	}
}

func TestUnloadedKindsStayOnDisk(t *testing.T) {
	d := descriptor(4, 100*mib, 0)
	for i := int32(0); i < 3; i++ {
		d.Groups = append(d.Groups, &v1.TensorGroup{Id: "draft." + strconv.Itoa(int(i)), Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_DRAFT, Layer: i, Bytes: gib})
	}
	d.Groups = append(d.Groups, &v1.TensorGroup{Id: "vision", Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_VISION, Layer: -1, Bytes: 500 * mib})
	base := 4*100*mib + 600*mib + 500*mib
	for _, id := range []string{"sglang", "vllm"} {
		p, params := policy(t, id)
		plan, err := p.Plan(input(d, host(24*gib, 64*gib), defaults(params, nil)))
		if err != nil {
			t.Fatal(err)
		}
		if plan.GetWeightsBytes() != uint64(base) || len(plan.GetSkipped()) != 1 || plan.GetSkipped()[0].GetKind() != v1.TensorGroupKind_TENSOR_GROUP_KIND_DRAFT || plan.GetSkipped()[0].GetBytes() != 3*gib || plan.GetSkipped()[0].GetCount() != 3 || plan.GetSkipped()[0].GetPoolId() != "" {
			t.Fatalf("%s: drafts should stay on disk: weights %d skipped %v", id, plan.GetWeightsBytes(), plan.GetSkipped())
		}
		for _, pl := range plan.GetPlacements() {
			if pl.GetKind() == v1.TensorGroupKind_TENSOR_GROUP_KIND_DRAFT {
				t.Fatalf("%s: a skipped kind must not be placed: %v", id, plan.GetPlacements())
			}
		}
		var vision bool
		for _, pl := range plan.GetPlacements() {
			vision = vision || (pl.GetKind() == v1.TensorGroupKind_TENSOR_GROUP_KIND_VISION && pl.GetPoolId() == "device" && pl.GetBytes() == 500*mib)
		}
		if !vision {
			t.Fatalf("%s: the vision tower loads on device: %v", id, plan.GetPlacements())
		}
		on := map[string]any{"speculative_algorithm": "NEXTN"}
		if id == "vllm" {
			on = map[string]any{"speculative_config": `{"method":"mtp","num_speculative_tokens":1}`}
		}
		plan, err = p.Plan(input(d, host(24*gib, 64*gib), defaults(params, on)))
		if err != nil {
			t.Fatal(err)
		}
		if plan.GetWeightsBytes() != uint64(base)+3*gib || len(plan.GetSkipped()) != 0 {
			t.Fatalf("%s: drafts should load once speculative decoding is on: weights %d skipped %v", id, plan.GetWeightsBytes(), plan.GetSkipped())
		}
		if estimate.PlannedDevice(plan) < uint64(base)+3*gib {
			t.Fatalf("%s: drafts should sit on device: %d", id, estimate.PlannedDevice(plan))
		}
	}
	// NeMo never drafts with them
	p, params := policy(t, "nemo")
	plan, err := p.Plan(input(d, host(24*gib, 64*gib), defaults(params, nil)))
	if err != nil || plan.GetWeightsBytes() != uint64(base) || len(plan.GetSkipped()) != 1 {
		t.Fatalf("nemo: %+v %v", plan, err)
	}
}

func TestLlamaCppLoadsWhatTheParamsName(t *testing.T) {
	p, params := policy(t, "llamacpp")
	// Four trunk layers, one MTP head layer past them, and the projector stored with the model
	d := descriptor(4, 100*mib, 0)
	d.Params["n_layer"], d.Params["n_layer_draft"] = 5, 1
	d.Groups = append(d.Groups, &v1.TensorGroup{Id: "draft.4", Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_DRAFT, Layer: 4, Bytes: 200 * mib})
	d.Groups = append(d.Groups, &v1.TensorGroup{Id: "vision", Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_VISION, Layer: -1, Bytes: 500 * mib})
	trunk := uint64(4*100*mib + 600*mib)
	// Eight KV heads of 128 wide keys and values, f16, over 8192 tokens
	perLayer := uint64(8192 * 8 * 256 * 2)
	plan, err := p.Plan(input(d, host(24*gib, 64*gib), defaults(params, nil)))
	if err != nil || plan.GetWeightsBytes() != trunk || plan.GetCacheBytes() != 4*perLayer || len(plan.GetSkipped()) != 2 {
		t.Fatalf("the projector and heads stay on disk until named: %+v %v", plan, err)
	}
	// draft-mtp loads the heads, whose context keeps an f16 cache of its own layer
	plan, err = p.Plan(input(d, host(24*gib, 64*gib), defaults(params, map[string]any{"spec_type": "draft-mtp"})))
	if err != nil || plan.GetWeightsBytes() != trunk+200*mib || plan.GetCacheBytes() != 5*perLayer {
		t.Fatalf("mtp heads: weights %d cache %d %v", plan.GetWeightsBytes(), plan.GetCacheBytes(), err)
	}
	var heads bool
	for _, pl := range plan.GetPlacements() {
		heads = heads || (pl.GetKind() == v1.TensorGroupKind_TENSOR_GROUP_KIND_DRAFT && pl.GetPoolId() == "device" && pl.GetBytes() == 200*mib)
	}
	if !heads {
		t.Fatalf("the heads sit on device: %v", plan.GetPlacements())
	}
	// A named projector loads whole at its file's size, the stored one staying on disk
	projector := filepath.Join(t.TempDir(), "mmproj-other.gguf")
	if err := os.WriteFile(projector, make([]byte, 3*mib), 0o644); err != nil {
		t.Fatal(err)
	}
	plan, err = p.Plan(input(d, host(24*gib, 64*gib), defaults(params, map[string]any{"mmproj": projector})))
	if err != nil || plan.GetWeightsBytes() != trunk+3*mib {
		t.Fatalf("named projector: %+v %v", plan, err)
	}
	if _, err := p.Plan(input(d, host(24*gib, 64*gib), defaults(params, map[string]any{"mmproj": projector + ".gone"}))); !errors.Is(err, estimate.ErrRefused) || !strings.Contains(err.Error(), "projector") {
		t.Fatalf("a missing projector is refused: %v", err)
	}
	// draft-mtp on a model without heads is refused before launch
	if _, err := p.Plan(input(descriptor(4, 100*mib, 0), host(24*gib, 64*gib), defaults(params, map[string]any{"spec_type": "draft-mtp"}))); !errors.Is(err, estimate.ErrRefused) || !strings.Contains(err.Error(), "MTP heads") {
		t.Fatalf("mtp without heads: %v", err)
	}
	// A draft model loads its trunk and own cache on device, an assistant sharing the target's cache keeps none
	draft := &v1.Descriptor{Architecture: "llama", Params: map[string]float64{"n_layer": 2, "n_head_kv": 4, "head_dim": 64, "head_dim_v": 64}}
	for i := int32(0); i < 2; i++ {
		draft.Groups = append(draft.Groups, &v1.TensorGroup{Id: "layer." + strconv.Itoa(int(i)), Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_LAYER, Layer: i, Bytes: 50 * mib})
	}
	draft.Groups = append(draft.Groups, &v1.TensorGroup{Id: "output", Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_OUTPUT, Layer: -1, Bytes: 10 * mib})
	in := input(d, host(24*gib, 64*gib), defaults(params, map[string]any{"spec_type": "draft-simple", "draft_model": "/drafts/small.gguf"}))
	in.Draft, in.DraftFamily = draft, archs.Default{}
	plan, err = p.Plan(in)
	if err != nil || plan.GetWeightsBytes() != trunk+110*mib || plan.GetCacheBytes() != 4*perLayer+uint64(8192*2*4*128*2) {
		t.Fatalf("draft model: weights %d cache %d %v", plan.GetWeightsBytes(), plan.GetCacheBytes(), err)
	}
	draft.Architecture = "gemma4-assistant"
	if plan, err = p.Plan(in); err != nil || plan.GetCacheBytes() != 4*perLayer {
		t.Fatalf("an assistant shares the target's cache: %d %v", plan.GetCacheBytes(), err)
	}
	// An unset method takes the one the draft file is made for, and a set one stays as set
	if plan, err = p.Plan(input(d, host(24*gib, 64*gib), defaults(params, nil))); err != nil || plan.GetParams()["spec_type"] != "none" {
		t.Fatalf("no draft model drafts nothing: %v %v", plan.GetParams()["spec_type"], err)
	}
	in.Params = defaults(params, map[string]any{"draft_model": "/drafts/small.gguf"})
	for arch, want := range map[string]string{"llama": "draft-simple", "gemma4-assistant": "draft-mtp", "dflash": estimate.Auto} {
		draft.Architecture = arch
		if plan, err = p.Plan(in); err != nil || plan.GetParams()["spec_type"] != want {
			t.Fatalf("%s draft: %v %v", arch, plan.GetParams()["spec_type"], err)
		}
	}
	nextn := &v1.Descriptor{Architecture: "qwen35", Params: map[string]float64{"n_layer": 1, "n_layer_draft": 1, "n_head_kv": 4, "head_dim": 64, "head_dim_v": 64},
		Groups: []*v1.TensorGroup{{Id: "draft.0", Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_DRAFT, Layer: 0, Bytes: 20 * mib}}}
	in.Draft = nextn
	if plan, err = p.Plan(in); err != nil || plan.GetParams()["spec_type"] != "draft-mtp" {
		t.Fatalf("a nextn head file: %v %v", plan.GetParams()["spec_type"], err)
	}
	in.Draft, draft.Architecture = draft, "gemma4-assistant"
	in.Params = defaults(params, map[string]any{"spec_type": "draft-simple", "draft_model": "/drafts/small.gguf"})
	if plan, err = p.Plan(in); err != nil || plan.GetParams()["spec_type"] != "draft-simple" {
		t.Fatalf("a set method is kept: %v %v", plan.GetParams()["spec_type"], err)
	}
	in.Params = defaults(params, map[string]any{"spec_type": "none", "draft_model": "/drafts/small.gguf"})
	if _, err := p.Plan(in); !errors.Is(err, estimate.ErrRefused) || !strings.Contains(err.Error(), "idle") {
		t.Fatalf("none beside a draft model is refused: %v", err)
	}
	// A seat's share of a solved descriptor takes no file the params name a second time
	in.Part, in.Params = true, defaults(params, map[string]any{"mmproj": projector})
	if plan, err = p.Plan(in); err != nil || plan.GetWeightsBytes() != trunk {
		t.Fatalf("a part adds no files: %+v %v", plan, err)
	}
}

func TestPlacementsHoldWeightsAlone(t *testing.T) {
	p, params := policy(t, "llamacpp")
	plan, err := p.Plan(input(descriptor(28, 100*mib, 0), host(12*gib, 64*gib), defaults(params, nil)))
	if err != nil || plan.GetVerdict() != v1.FitVerdict_FIT_VERDICT_FITS {
		t.Fatalf("plan %+v %v", plan, err)
	}
	var placed uint64
	for _, pl := range plan.GetPlacements() {
		placed += pl.GetBytes()
	}
	if placed != plan.GetWeightsBytes() {
		t.Fatalf("placed %d weights %d", placed, plan.GetWeightsBytes())
	}
	var used uint64
	for _, pu := range plan.GetPools() {
		used += pu.GetUsedBytes()
	}
	if used != plan.GetWeightsBytes()+plan.GetCacheBytes()+plan.GetOverheadBytes() {
		t.Fatalf("pools %d should hold weights, cache, and overhead", used)
	}
}

func TestParamsAccessors(t *testing.T) {
	p := estimate.Params{"i": int64(3), "f": 2.5, "s": "x", "b": true, "a": estimate.Auto}
	if p.Int("i") != 3 || p.Int("f") != 2 || p.Float("i") != 3 || p.Float("f") != 2.5 || p.Str("s") != "x" || !p.Bool("b") || !p.IsAuto("a") || p.IsAuto("s") || p.Int("missing") != 0 {
		t.Fatalf("accessors %v", p)
	}
	c := p.Clone()
	c["i"] = int64(9)
	if p.Int("i") != 3 {
		t.Fatal("clone should not share")
	}
}

// A hybrid model's cache follows its full attention layers: a range holding only recurrent layers
// carries no cache, one holding a full layer carries that layer's whole cache, and the plan sizes
// each layer's cache by its own kind rather than the average
func TestHybridLayersCarryTheirOwnCache(t *testing.T) {
	params := map[string]float64{"n_layer": 8, "n_head_kv": 8, "head_dim": 128, "head_dim_v": 128, "n_embd": 4096, "n_vocab": 32000, "attn_interval": 4}
	rng := func(from, to int32) *v1.Descriptor {
		d := &v1.Descriptor{Architecture: "qwen3next", Family: "default", Params: params}
		for i := from; i < to; i++ {
			d.Groups = append(d.Groups, &v1.TensorGroup{Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_LAYER, Layer: i, Bytes: 100 * mib})
		}
		return d
	}
	p, defaultsOf := policy(t, "llamacpp")
	host := &v1.HostProfile{Pools: []*v1.MemoryPool{{Id: "g", Kind: v1.PoolKind_POOL_KIND_DEVICE, TotalBytes: 80 * gib}, {Id: "h", Kind: v1.PoolKind_POOL_KIND_HOST, TotalBytes: 80 * gib}}}
	plan := func(d *v1.Descriptor) *v1.MemoryPlan {
		out, err := p.Plan(estimate.Input{Descriptor: d, Family: archs.Default{}, Host: host, Params: defaults(defaultsOf, map[string]any{"n_ctx": int64(8192)})})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	fullLayer := uint64(8192) * 8 * 256 * 2
	if got := plan(rng(0, 3)).GetCacheBytes(); got != 0 {
		t.Fatalf("recurrent layers 0-2 carry no cache: %s", estimate.Human(got))
	}
	if got := plan(rng(3, 4)).GetCacheBytes(); got != fullLayer {
		t.Fatalf("layer 3 keeps a full cache: %s, want %s", estimate.Human(got), estimate.Human(fullLayer))
	}
	if got := plan(rng(0, 8)).GetCacheBytes(); got != 2*fullLayer {
		t.Fatalf("the whole model keeps two: %s", estimate.Human(got))
	}
	// The solver keeps the highest layers on the device. Layers 1-3 need their weights, layer 3's
	// full cache, and the overhead; the averaged cache would put a quarter of it on each and need
	// less, so a device with room between the two keeps two layers by the family's shape
	overhead := plan(rng(0, 4)).GetOverheadBytes()
	shaped := 3*100*mib + fullLayer + overhead
	averaged := 3*100*mib + 3*fullLayer/4 + overhead
	device := func(room uint64) *v1.HostProfile {
		return &v1.HostProfile{Pools: []*v1.MemoryPool{{Id: "g", Kind: v1.PoolKind_POOL_KIND_DEVICE, TotalBytes: uint64(float64(room)/0.95) + 2}, {Id: "h", Kind: v1.PoolKind_POOL_KIND_HOST, TotalBytes: 80 * gib}}}
	}
	out, err := p.Plan(estimate.Input{Descriptor: rng(0, 4), Family: archs.Default{}, Host: device(4*100*mib + fullLayer + overhead), Params: defaults(defaultsOf, map[string]any{"n_ctx": int64(8192)})})
	if err != nil || out.GetVerdict() != v1.FitVerdict_FIT_VERDICT_FITS {
		t.Fatalf("layers 0-3 fit with the one full cache: %v %v", out.GetVerdict(), err)
	}
	out, err = p.Plan(estimate.Input{Descriptor: rng(0, 4), Family: archs.Default{}, Host: device((shaped + averaged) / 2), Params: defaults(defaultsOf, map[string]any{"n_ctx": int64(8192)})})
	if err != nil || out.GetVerdict() != v1.FitVerdict_FIT_VERDICT_PARTIAL || out.GetParams()["n_gpu_layers"] != "2" {
		t.Fatalf("between the shaped and the averaged need, two layers stay: %v %v %v", out.GetVerdict(), out.GetParams()["n_gpu_layers"], err)
	}
}
