package estimate

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/nickheyer/nebu/pkg/eval"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/spec"
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
	in.Host = host(1*gib, 64*gib)
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
