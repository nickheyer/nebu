package estimate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
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
