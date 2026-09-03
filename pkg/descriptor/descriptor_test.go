package descriptor

import (
	"os"
	"path/filepath"
	"testing"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/spec"
)

func builder(t *testing.T) *Builder {
	t.Helper()
	c, err := spec.Load(os.DirFS(filepath.Join("..", "..", "spec")))
	if err != nil {
		t.Fatal(err)
	}
	b, err := New(c.Formats, c.Archs)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func moeRaw(arch string) *v1.RawModel {
	raw := &v1.RawModel{FormatId: "gguf", Group: "Q4", Metadata: map[string]string{
		"general.architecture":            arch,
		arch + ".block_count":             "2",
		arch + ".embedding_length":        "1024",
		arch + ".attention.head_count":    "16",
		arch + ".attention.head_count_kv": "4",
		arch + ".context_length":          "4096",
		arch + ".expert_count":            "8",
		arch + ".attention.kv_lora_rank":  "512",
		arch + ".rope.dimension_count":    "64",
		"tokenizer.ggml.tokens.length":    "1000",
		"general.name":                    "Test Model",
	}}
	raw.Tensors = []*v1.TensorInfo{
		{Name: "token_embd.weight", Bytes: 1000, Elements: 100},
		{Name: "blk.0.attn_q.weight", Bytes: 200, Elements: 20},
		{Name: "blk.0.ffn_up_exps.weight", Bytes: 800, Elements: 80},
		{Name: "blk.1.attn_q.weight", Bytes: 200, Elements: 20},
		{Name: "blk.1.ffn_up_exps.weight", Bytes: 800, Elements: 80},
		{Name: "output_norm.weight", Bytes: 10, Elements: 1},
		{Name: "output.weight", Bytes: 1000, Elements: 100},
		{Name: "weird.tensor", Bytes: 5, Elements: 1},
	}
	return raw
}

func TestBuild(t *testing.T) {
	d, err := builder(t).Build(moeRaw("qwen3moe"))
	if err != nil {
		t.Fatal(err)
	}
	p := d.GetParams()
	if d.GetArchitecture() != "qwen3moe" || p["n_layer"] != 2 || p["n_head_kv"] != 4 || p["head_dim"] != 64 || p["head_dim_v"] != 64 || p["n_vocab"] != 1000 || p["n_expert"] != 8 {
		t.Fatalf("params %v", p)
	}
	if d.GetArchSpecId() != "default" || d.GetMetadata()["general.name"] != "Test Model" {
		t.Fatalf("arch %q metadata %v", d.GetArchSpecId(), d.GetMetadata())
	}
	kinds := map[string]uint64{}
	for _, g := range d.GetGroups() {
		kinds[g.GetId()] = g.GetBytes()
	}
	want := map[string]uint64{"embedding": 1000, "layer.0": 200, "experts.0": 800, "layer.1": 200, "experts.1": 800, "output": 1010, "other": 5}
	for k, v := range want {
		if kinds[k] != v {
			t.Errorf("%s=%d want %d (%v)", k, kinds[k], v, kinds)
		}
	}
	if d.GetTotalBytes() != 4015 || d.GetParameterCount() != 402 {
		t.Fatalf("totals %d %d", d.GetTotalBytes(), d.GetParameterCount())
	}
}

func TestArchMatchAndDerive(t *testing.T) {
	b := builder(t)
	raw := moeRaw("deepseek2")
	delete(raw.Metadata, "deepseek2.block_count")
	d, err := b.Build(raw)
	if err != nil {
		t.Fatal(err)
	}
	if d.GetArchSpecId() != "mla" || d.GetParams()["n_layer"] != 2 {
		t.Fatalf("arch %q params %v", d.GetArchSpecId(), d.GetParams())
	}
	f := b.Formulas("mla")
	v, err := f["cache_per_token"].Float(map[string]any{"n_layer": 2.0, "kv_lora_rank": 512.0, "rope_dim": 64.0})
	if err != nil || v != 1152 {
		t.Fatalf("formula %v %v", v, err)
	}
	if _, err := b.Build(&v1.RawModel{FormatId: "nope"}); err == nil {
		t.Fatal("unknown format should fail")
	}
}
