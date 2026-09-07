package estimate

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	desc "github.com/nickheyer/nebu/pkg/descriptor"
	"github.com/nickheyer/nebu/pkg/eval"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/spec"
	"google.golang.org/protobuf/encoding/protojson"
)

const (
	mib = 1 << 20
	gib = 1 << 30
)

func policy(t *testing.T, id string) (*Policy, []*v1.Param) {
	t.Helper()
	c, err := spec.Load(os.DirFS(filepath.Join("..", "..", "spec")))
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range c.Runtimes {
		if m.GetId() == id {
			p, err := NewPolicy(m.GetEstimate())
			if err != nil {
				t.Fatal(err)
			}
			return p, m.GetParams()
		}
	}
	t.Fatalf("runtime %s missing", id)
	return nil, nil
}

func defaults(params []*v1.Param, overrides map[string]any) map[string]any {
	out := map[string]any{}
	for _, p := range params {
		v := p.GetDefault()
		switch p.GetType() {
		case v1.ParamType_PARAM_TYPE_INT:
			if n, err := strconv.ParseInt(v, 10, 64); err == nil {
				out[p.GetName()] = n
				continue
			}
		case v1.ParamType_PARAM_TYPE_FLOAT:
			if f, err := strconv.ParseFloat(v, 64); err == nil {
				out[p.GetName()] = f
				continue
			}
		}
		out[p.GetName()] = v
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

func formulas(t *testing.T) map[string]*eval.Expr {
	e, err := eval.Compile("n_layer * n_head_kv * (head_dim + head_dim_v)")
	if err != nil {
		t.Fatal(err)
	}
	return map[string]*eval.Expr{"cache_per_token": e}
}

func TestDenseFits(t *testing.T) {
	p, params := policy(t, "llamacpp")
	plan, err := p.Plan(Input{Descriptor: descriptor(28, 100*mib, 0), Formulas: formulas(t), Host: host(12*gib, 64*gib), Params: defaults(params, nil)})
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
}

func TestDensePartialAndFixed(t *testing.T) {
	p, params := policy(t, "llamacpp")
	in := Input{Descriptor: descriptor(28, 500*mib, 0), Formulas: formulas(t), Host: host(8*gib, 64*gib), Params: defaults(params, nil)}
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
	in.Host = host(0, 64*gib)
	plan, err = p.Plan(in)
	if err != nil || plan.GetVerdict() != v1.FitVerdict_FIT_VERDICT_NO {
		t.Fatalf("no device pools: %+v %v", plan, err)
	}
}

func TestMoESpillsExpertsFirst(t *testing.T) {
	p, params := policy(t, "llamacpp")
	plan, err := p.Plan(Input{Descriptor: descriptor(48, 50*mib, 400*mib), Formulas: formulas(t), Host: host(12*gib, 64*gib), Params: defaults(params, nil)})
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
	plan, err := p.Plan(Input{Descriptor: descriptor(28, 100*mib, 0), Formulas: formulas(t), Host: host(12*gib, 64*gib), Params: defaults(params, nil)})
	if err != nil {
		t.Fatal(err)
	}
	if plan.GetVerdict() != v1.FitVerdict_FIT_VERDICT_FITS {
		t.Fatalf("plan %+v", plan)
	}
	plan, err = p.Plan(Input{Descriptor: descriptor(28, 1*gib, 0), Formulas: formulas(t), Host: host(12*gib, 64*gib), Params: defaults(params, nil)})
	if err != nil {
		t.Fatal(err)
	}
	if plan.GetVerdict() != v1.FitVerdict_FIT_VERDICT_NO {
		t.Fatalf("plan %+v", plan)
	}
}

func TestHuman(t *testing.T) {
	if Human(1536*mib) != "1.5 GiB" || Human(10) != "10 B" {
		t.Fatal(Human(1536*mib), Human(10))
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
	c, err := spec.Load(os.DirFS(filepath.Join("..", "..", "spec")))
	if err != nil {
		t.Fatal(err)
	}
	builder, err := desc.New(c.Formats, c.Archs, c.Precisions)
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
		p, params := policy(t, run.Runtime)
		overrides := map[string]any{}
		for k, v := range run.Params {
			if n, err := strconv.ParseInt(v, 10, 64); err == nil {
				overrides[k] = n
			} else {
				overrides[k] = v
			}
		}
		host := &v1.HostProfile{Pools: []*v1.MemoryPool{
			{Id: "gpu", Kind: v1.PoolKind_POOL_KIND_DEVICE, TotalBytes: run.DeviceFreeBytes, FreeBytes: run.DeviceFreeBytes},
			{Id: "host", Kind: v1.PoolKind_POOL_KIND_HOST, TotalBytes: run.HostFreeBytes, FreeBytes: run.HostFreeBytes},
		}}
		plan, err := p.Plan(Input{Descriptor: d, Formulas: builder.Formulas(d.GetArchSpecId()), Host: host, Params: defaults(params, overrides), Free: true})
		if err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		within := func(name string, got, want uint64, low, high float64) {
			ratio := float64(got) / float64(want)
			if ratio < low || ratio > high {
				t.Errorf("%s: %s planned %s, measured %s, ratio %.3f outside [%.2f, %.2f]", filepath.Base(file), name, Human(got), Human(want), ratio, low, high)
			}
		}
		cache := run.Measured["device.cache"] + run.Measured["host.cache"]
		within("cache", plan.GetCacheBytes(), cache, 0.99, 1.01)
		weights := run.Measured["device.weights"] + run.Measured["host.weights"]
		within("weights", plan.GetWeightsBytes(), weights, 0.99, 1.01)
		// The device total may run a little over what the card reported, never under
		within("device", PlannedDevice(plan), run.Measured[DeviceUsedKey], 1.0, 1.08)
	}
}

func TestSlidingWindowAndSpannedDevices(t *testing.T) {
	c, err := spec.Load(os.DirFS(filepath.Join("..", "..", "spec")))
	if err != nil {
		t.Fatal(err)
	}
	builder, err := desc.New(c.Formats, c.Archs, c.Precisions)
	if err != nil {
		t.Fatal(err)
	}
	// A Gemma 3 shaped model: 62 layers, a 1024 token window on five of every six
	d := &v1.Descriptor{Architecture: "gemma3", Params: map[string]float64{"n_layer": 62, "n_head_kv": 16, "head_dim": 128, "head_dim_v": 128, "n_embd": 5376, "n_vocab": 262208, "n_swa": 1024}}
	for _, a := range c.Archs {
		if a.GetId() == "gemma3" {
			d.ArchSpecId = a.GetId()
		}
	}
	if d.ArchSpecId == "" {
		t.Fatal("gemma3 arch spec missing")
	}
	for i := int32(0); i < 62; i++ {
		d.Groups = append(d.Groups, &v1.TensorGroup{Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_LAYER, Layer: i, Bytes: 100 * mib})
	}
	p, params := policy(t, "llamacpp")
	host := &v1.HostProfile{Pools: []*v1.MemoryPool{{Id: "g", Kind: v1.PoolKind_POOL_KIND_DEVICE, TotalBytes: 80 * gib}, {Id: "h", Kind: v1.PoolKind_POOL_KIND_HOST, TotalBytes: 80 * gib}}}
	plan, err := p.Plan(Input{Descriptor: d, Formulas: builder.Formulas(d.GetArchSpecId()), Host: host, Params: defaults(params, map[string]any{"n_ctx": int64(32768)})})
	if err != nil {
		t.Fatal(err)
	}
	full := uint64(32768) * 62 * 16 * 256 * 2
	// Ten full layers hold 32k tokens, the other fifty-two hold the window plus a batch
	want := uint64(32768)*10*16*256*2 + uint64(1024+512)*52*16*256*2
	if plan.GetCacheBytes() != want || plan.GetCacheBytes() >= full/2 {
		t.Fatalf("sliding window cache %s, want %s of a full %s", Human(plan.GetCacheBytes()), Human(want), Human(full))
	}
	d.ArchSpecId, d.Architecture = "default", "llama"
	plan, err = p.Plan(Input{Descriptor: d, Formulas: builder.Formulas("default"), Host: host, Params: defaults(params, map[string]any{"n_ctx": int64(32768)})})
	if err != nil || plan.GetCacheBytes() != full {
		t.Fatalf("a full cache arch ignores the window: %s %v", Human(plan.GetCacheBytes()), err)
	}

	// vLLM at tensor parallel one sees one device, llama.cpp spreads over both
	two := &v1.HostProfile{Pools: []*v1.MemoryPool{{Id: "a", Kind: v1.PoolKind_POOL_KIND_DEVICE, TotalBytes: 8 * gib}, {Id: "b", Kind: v1.PoolKind_POOL_KIND_DEVICE, TotalBytes: 8 * gib}, {Id: "h", Kind: v1.PoolKind_POOL_KIND_HOST, TotalBytes: 64 * gib}}}
	small := &v1.Descriptor{Architecture: "llama", ArchSpecId: "default", Params: map[string]float64{"n_layer": 4, "n_head_kv": 8, "head_dim": 128, "head_dim_v": 128, "n_embd": 4096, "n_vocab": 32000}}
	for i := int32(0); i < 4; i++ {
		small.Groups = append(small.Groups, &v1.TensorGroup{Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_LAYER, Layer: i, Bytes: 2 * gib})
	}
	vp, vparams := policy(t, "vllm")
	one, err := vp.Plan(Input{Descriptor: small, Formulas: builder.Formulas("default"), Host: two, Params: defaults(vparams, map[string]any{"n_ctx": int64(1024)})})
	if err != nil || one.GetVerdict() != v1.FitVerdict_FIT_VERDICT_NO || len(one.GetPools()) != 2 {
		t.Fatalf("tensor parallel one should plan on one device: %v %v", one, err)
	}
	both, err := vp.Plan(Input{Descriptor: small, Formulas: builder.Formulas("default"), Host: two, Params: defaults(vparams, map[string]any{"n_ctx": int64(1024), "tensor_parallel_size": int64(2)})})
	if err != nil || both.GetVerdict() != v1.FitVerdict_FIT_VERDICT_FITS || len(both.GetPools()) != 3 {
		t.Fatalf("tensor parallel two should span both: %v %v", both, err)
	}
	spread, err := p.Plan(Input{Descriptor: small, Formulas: builder.Formulas("default"), Host: two, Params: defaults(params, map[string]any{"n_ctx": int64(1024)})})
	if err != nil || spread.GetVerdict() != v1.FitVerdict_FIT_VERDICT_FITS {
		t.Fatalf("llama.cpp spreads over every device: %v %v", spread, err)
	}
}

// A header without the heads the cache formula needs fails by naming them, never with the evaluator's own words
func TestPlanNamesMissingParams(t *testing.T) {
	p, params := policy(t, "llamacpp")
	d := descriptor(4, 100*mib, 0)
	delete(d.Params, "n_head_kv")
	_, err := p.Plan(Input{Descriptor: d, Formulas: formulas(t), Host: host(24*gib, 64*gib), Params: defaults(params, nil)})
	var missing *eval.MissingError
	if !errors.As(err, &missing) || strings.Join(missing.Names, ",") != "n_head_kv" || missing.Formula != "cache_per_token" {
		t.Fatalf("want a missing n_head_kv, got %v", err)
	}
	delete(d.Params, "n_embd")
	d.Params["n_head_kv"] = 8
	_, err = p.Plan(Input{Descriptor: d, Formulas: formulas(t), Host: host(24*gib, 64*gib), Params: defaults(params, nil)})
	if !errors.As(err, &missing) || strings.Join(missing.Names, ",") != "n_embd" || missing.Formula != "overhead_bytes" {
		t.Fatalf("want a missing n_embd, got %v", err)
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
	plan, err := p.Plan(Input{Descriptor: descriptor(28, gib, 0), Formulas: formulas(t), Host: host(12*gib, 16*gib), Params: defaults(params, nil)})
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
	if PlannedDevice(plan) != 12*gib {
		t.Fatalf("planned device %d", PlannedDevice(plan))
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
	plan, err = p.Plan(Input{Descriptor: descriptor(28, gib, 0), Formulas: formulas(t), Host: host(12*gib, 64*gib), Params: defaults(params, nil)})
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
	plan, err = lp.Plan(Input{Descriptor: descriptor(28, gib, 0), Formulas: formulas(t), Host: two, Params: defaults(lparams, nil)})
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
	// The embedding llama.cpp keeps in host memory lands there even when the device has room
	plan, err = lp.Plan(Input{Descriptor: descriptor(4, 100*mib, 0), Formulas: formulas(t), Host: host(0, 64*gib), Params: defaults(lparams, nil)})
	if err != nil || plan.GetVerdict() != v1.FitVerdict_FIT_VERDICT_NO || usage(plan, "host").GetUsedBytes() == 0 {
		t.Fatalf("no device: %+v %v", plan, err)
	}
}

// A kind whose when clause does not hold stays on disk: out of the weights, out of every
// pool, and listed as skipped, until the params turn it on
func TestWhenClauseLeavesDraftHeadsOnDisk(t *testing.T) {
	d := descriptor(4, 100*mib, 0)
	for i := int32(0); i < 3; i++ {
		d.Groups = append(d.Groups, &v1.TensorGroup{Id: "draft." + strconv.Itoa(int(i)), Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_DRAFT, Layer: i, Bytes: gib})
	}
	d.Groups = append(d.Groups, &v1.TensorGroup{Id: "vision", Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_VISION, Layer: -1, Bytes: 500 * mib})
	base := 4*100*mib + 600*mib + 500*mib
	for _, id := range []string{"sglang", "vllm"} {
		p, params := policy(t, id)
		plan, err := p.Plan(Input{Descriptor: d, Formulas: formulas(t), Host: host(24*gib, 64*gib), Params: defaults(params, nil)})
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
		plan, err = p.Plan(Input{Descriptor: d, Formulas: formulas(t), Host: host(24*gib, 64*gib), Params: defaults(params, on)})
		if err != nil {
			t.Fatal(err)
		}
		if plan.GetWeightsBytes() != uint64(base)+3*gib || len(plan.GetSkipped()) != 0 {
			t.Fatalf("%s: drafts should load once speculative decoding is on: weights %d skipped %v", id, plan.GetWeightsBytes(), plan.GetSkipped())
		}
		if PlannedDevice(plan) < uint64(base)+3*gib {
			t.Fatalf("%s: drafts should sit on device: %d", id, PlannedDevice(plan))
		}
	}
	// llama.cpp never drafts with them
	p, params := policy(t, "llamacpp")
	plan, err := p.Plan(Input{Descriptor: d, Formulas: formulas(t), Host: host(24*gib, 64*gib), Params: defaults(params, nil)})
	if err != nil || plan.GetWeightsBytes() != uint64(base) || len(plan.GetSkipped()) != 1 {
		t.Fatalf("llama.cpp: %+v %v", plan, err)
	}
}

// Placements carry weights alone, so the pool's remainder reads as cache and overhead
func TestPlacementsHoldWeightsAlone(t *testing.T) {
	p, params := policy(t, "llamacpp")
	plan, err := p.Plan(Input{Descriptor: descriptor(28, 100*mib, 0), Formulas: formulas(t), Host: host(12*gib, 64*gib), Params: defaults(params, nil)})
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
