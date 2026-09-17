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

// The runtime defaults with a fixed context and a full width cache, so cache bytes are exact, under the overrides given
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
	// Every pool says how large it is and what was free, and the plan says when the host was read
	for _, pu := range plan.GetPools() {
		if pu.GetTotalBytes() == 0 || pu.GetTotalBytes() != pu.GetCapacityBytes() {
			t.Fatalf("pool totals %+v", pu)
		}
	}
	if plan.GetPlannedAt() == nil {
		t.Fatal("a plan carries the time it was made")
	}
}

// A plan against free memory caps every pool at what was free and still reports the whole pool
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

// An auto context takes the largest step of the grid that keeps the verdict of the smallest, capped by the trained length
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
	// A header that names no trained length solves to the floor of the grid
	p, params := policy(t, "llamacpp")
	plan, err := p.Plan(input(descriptor(4, 100*mib, 0), host(24*gib, 64*gib), params))
	if err != nil || plan.GetParams()["n_ctx"] != "256" {
		t.Fatalf("no trained length: %v %v", plan.GetParams(), err)
	}
}

// The states report every param's bounds under the plan, and a value cache the runtime refuses without flash attention
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
	// The cache types follow the weights: full width weights keep a full width cache, quantized weights an 8 bit one
	auto := params.Clone()
	auto["n_ctx"] = int64(1024)
	plan, err = p.Plan(input(d, host(24*gib, 64*gib), auto))
	if err != nil || plan.GetParams()["cache_type_k"] != "q8_0" || plan.GetParams()["cache_type_v"] != "q8_0" {
		t.Fatalf("quantized weights: %v %v", plan.GetParams(), err)
	}
	d.BitsPerWeight = 16
	plan, err = p.Plan(input(d, host(24*gib, 64*gib), auto))
	if err != nil || plan.GetParams()["cache_type_k"] != "f16" || plan.GetParams()["cache_type_v"] != "f16" {
		t.Fatalf("full width weights: %v %v", plan.GetParams(), err)
	}
}

func TestHuman(t *testing.T) {
	if estimate.Human(1536*mib) != "1.5 GiB" || estimate.Human(10) != "10 B" {
		t.Fatal(estimate.Human(1536*mib), estimate.Human(10))
	}
}

// A run the daemon measured on real hardware: the descriptor it planned, the
// params it launched with, and what the runtime and the device reported
type measuredRun struct {
	Runtime         string            `json:"runtime"`
	Descriptor      json.RawMessage   `json:"descriptor"`
	Params          map[string]string `json:"params"`
	DeviceFreeBytes uint64            `json:"device_free_bytes"`
	HostFreeBytes   uint64            `json:"host_free_bytes"`
	Measured        map[string]uint64 `json:"measured"`
}

// Replans every measured run with the params it ran with and holds the plan to what the card reported
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

// A header without the heads the cache formula needs fails by naming them, never with the family's own words
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

// A model no fit holds is laid out the way the host would take it: the device fills to its
// brim, the rest flows into host memory, and what nothing holds overflows past the last pool
func TestNoFitFillsDeviceThenSpillsAndOverflows(t *testing.T) {
	p, params := policy(t, "vllm")
	// Twenty-eight 1 GiB layers plus cache and overhead, on a 12 GiB card with 16 GiB beside it
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

	// With host memory to spare the device still fills whole and the host takes the rest within its capacity
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

	// Two cards fill in turn before anything reaches the host, and the solved counts say what landed on device
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

// A kind the policy loads only under some params stays on disk: out of the weights, out of every
// pool, and listed as skipped, until the params turn it on
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
	// llama.cpp and NeMo never draft with them
	for _, id := range []string{"llamacpp", "nemo"} {
		p, params := policy(t, id)
		plan, err := p.Plan(input(d, host(24*gib, 64*gib), defaults(params, nil)))
		if err != nil || plan.GetWeightsBytes() != uint64(base) || len(plan.GetSkipped()) != 1 {
			t.Fatalf("%s: %+v %v", id, plan, err)
		}
	}
}

// Placements carry weights alone, so the pool's remainder reads as cache and overhead
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

// Params read back the way a run resolves them
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
