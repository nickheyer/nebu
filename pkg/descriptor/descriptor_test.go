package descriptor

import (
	"testing"

	"github.com/nickheyer/nebu/pkg/archs"
	"github.com/nickheyer/nebu/pkg/formats"
	"github.com/nickheyer/nebu/pkg/formats/all"
	"github.com/nickheyer/nebu/pkg/precision"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

func builder(t *testing.T) *Builder {
	t.Helper()
	fmts, err := all.Registry()
	if err != nil {
		t.Fatal(err)
	}
	families, err := archs.New(archs.All())
	if err != nil {
		t.Fatal(err)
	}
	return &Builder{Formats: fmts, Archs: families, Scale: precision.Bits{}}
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

// The cache per token of a descriptor's family at no particular context
func perToken(t *testing.T, b *Builder, d *v1.Descriptor) float64 {
	t.Helper()
	family := b.Family(d)
	if family == nil {
		t.Fatalf("no family for %q", d.GetFamily())
	}
	v, err := family.CachePerToken(formats.ParamsOf(d.GetParams()), archs.Run{})
	if err != nil {
		t.Fatal(err)
	}
	return v
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
	if d.GetFamily() != "default" || d.GetMetadata()["general.name"] != "Test Model" {
		t.Fatalf("family %q metadata %v", d.GetFamily(), d.GetMetadata())
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
	if d.GetFamily() != "mla" || d.GetParams()["n_layer"] != 2 {
		t.Fatalf("family %q params %v", d.GetFamily(), d.GetParams())
	}
	if v := perToken(t, b, d); v != 2*(512+64) {
		t.Fatalf("mla cache per token %v", v)
	}
	if _, err := b.Build(&v1.RawModel{FormatId: "nope"}); err == nil {
		t.Fatal("unknown format should fail")
	}
}

func TestArchClaimsOnlyWhatItsFormulasCover(t *testing.T) {
	b := builder(t)
	raw := func(extra map[string]string) *v1.RawModel {
		r := &v1.RawModel{FormatId: "safetensors", Group: "default", Metadata: map[string]string{
			"model_type": "deepseek_v4", "num_hidden_layers": "4", "hidden_size": "1024", "num_attention_heads": "16", "num_key_value_heads": "1", "head_dim": "512", "qk_rope_head_dim": "64",
		}, Tensors: []*v1.TensorInfo{{Name: "model.layers.0.attn.weight", Bytes: 8, Elements: 2}}}
		for k, v := range extra {
			r.Metadata[k] = v
		}
		return r
	}
	// No latent rank, so the MLA family cannot plan it and the default family takes it
	d, err := b.Build(raw(nil))
	if err != nil || d.GetFamily() != "default" {
		t.Fatalf("without kv_lora_rank got %q %v", d.GetFamily(), err)
	}
	if v := perToken(t, b, d); v != 4*1*(512+512) {
		t.Fatalf("default cache per token %v", v)
	}
	d, err = b.Build(raw(map[string]string{"kv_lora_rank": "512"}))
	if err != nil || d.GetFamily() != "mla" {
		t.Fatalf("with kv_lora_rank got %q %v", d.GetFamily(), err)
	}
	d, err = b.Build(raw(map[string]string{"text_config.kv_lora_rank": "512"}))
	if err != nil || d.GetFamily() != "mla" || d.GetParams()["kv_lora_rank"] != 512 {
		t.Fatalf("nested kv_lora_rank got %q %v %v", d.GetFamily(), d.GetParams(), err)
	}
}

func fp8Raw() *v1.RawModel {
	raw := &v1.RawModel{FormatId: "safetensors", Group: "default", Metadata: map[string]string{
		"architectures":                    "DeepseekV4ForCausalLM",
		"torch_dtype":                      "bfloat16",
		"quantization_config.quant_method": "fp8",
		"expert_dtype":                     "fp4",
		"num_hidden_layers":                "1",
		"hidden_size":                      "8",
		"num_attention_heads":              "2",
	}}
	raw.Tensors = []*v1.TensorInfo{
		{Name: "embed_tokens.weight", Dtype: "BF16", Bytes: 32, Elements: 16},
		{Name: "layers.0.attn.wq.weight", Dtype: "F8_E4M3", Bytes: 64, Elements: 64},
		{Name: "layers.0.ffn.experts.0.w1.weight", Dtype: "I8", Bytes: 64, Elements: 64},
		{Name: "layers.0.ffn.experts.0.w1.scale", Dtype: "F8_E8M0", Bytes: 2, Elements: 2},
		{Name: "layers.0.ffn.shared_experts.w1.weight", Dtype: "F8_E4M3", Bytes: 16, Elements: 16},
	}
	return raw
}

func TestPackedExpertsAndQuantPrecision(t *testing.T) {
	d, err := builder(t).Build(fp8Raw())
	if err != nil {
		t.Fatal(err)
	}
	// The fp4 experts hold two weights per byte, nothing else is packed
	if d.GetParameterCount() != 16+64+128+2+16 || d.GetTotalBytes() != 178 {
		t.Fatalf("params %d bytes %d", d.GetParameterCount(), d.GetTotalBytes())
	}
	p := d.GetPrecision()
	if p.GetBits() != 6 || p.GetLabel() != "6-bit fp8, fp4 experts" || p.GetLevel() != 4 {
		t.Fatalf("precision %v", p)
	}
	// The dtype is only the answer when no quantization config named one
	raw := fp8Raw()
	delete(raw.Metadata, "quantization_config.quant_method")
	delete(raw.Metadata, "expert_dtype")
	if d, err = builder(t).Build(raw); err != nil {
		t.Fatal(err)
	}
	if d.GetParameterCount() != 162 || d.GetPrecision().GetLabel() != "16-bit bfloat16" {
		t.Fatalf("params %d precision %v", d.GetParameterCount(), d.GetPrecision())
	}
	raw.Metadata["quantization_config.bits"] = "4"
	raw.Metadata["quantization_config.quant_method"] = "gptq"
	raw.Tensors = append(raw.Tensors, &v1.TensorInfo{Name: "layers.0.attn.wk.qweight", Dtype: "I32", Bytes: 64, Elements: 16})
	if d, err = builder(t).Build(raw); err != nil {
		t.Fatal(err)
	}
	if d.GetParameterCount() != 162+128 || d.GetPrecision().GetLabel() != "4-bit gptq" {
		t.Fatalf("params %d precision %v", d.GetParameterCount(), d.GetPrecision())
	}
}

func TestPerLayerEmbeddingAndHybridInterval(t *testing.T) {
	b := builder(t)
	raw := moeRaw("qwen3next")
	raw.Metadata["qwen3next.full_attention_interval"] = "4"
	raw.Metadata["qwen3next.block_count"] = "8"
	raw.Tensors = append(raw.Tensors,
		&v1.TensorInfo{Name: "per_layer_token_embd.weight", Bytes: 5000, Elements: 500},
		&v1.TensorInfo{Name: "output_hc_norm.weight", Bytes: 40, Elements: 4},
		&v1.TensorInfo{Name: "output_hc_up.weight", Bytes: 60, Elements: 6},
	)
	d, err := b.Build(raw)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]uint64{}
	for _, g := range d.GetGroups() {
		kinds[g.GetId()] = g.GetBytes()
	}
	if kinds["embedding"] != 6000 || kinds["output"] != 1110 || kinds["other"] != 5 {
		t.Fatalf("per layer embedding and hyper connection output misplaced: %v", kinds)
	}
	if d.GetParams()["attn_interval"] != 4 || d.GetFamily() != "default" {
		t.Fatalf("params %v family %q", d.GetParams(), d.GetFamily())
	}
	// Eight layers with a cache on one in four is two layers of cache: 2 * 4 heads * (64 + 64)
	if v := perToken(t, b, d); v != 1024 {
		t.Fatalf("cache_per_token %v", v)
	}
	// Without the interval every layer keeps a cache
	delete(raw.Metadata, "qwen3next.full_attention_interval")
	d, err = b.Build(raw)
	if err != nil {
		t.Fatal(err)
	}
	if v := perToken(t, b, d); v != 4096 {
		t.Fatalf("cache_per_token %v", v)
	}
}

// Encoders, projectors, and prediction heads each keep their own kind, whether the header names
// them apart or numbers them past the layers its config counts, and the shared expert every token
// uses stays with its layer rather than joining the routed experts a run can offload
func TestEncodersAndDraftHeads(t *testing.T) {
	b := builder(t)
	raw := &v1.RawModel{FormatId: "safetensors", Group: "default", Metadata: map[string]string{
		"architectures":            "DeepseekV4ForCausalLM",
		"num_hidden_layers":        "2",
		"num_nextn_predict_layers": "1",
		"hidden_size":              "1024",
		"num_attention_heads":      "16",
		"num_key_value_heads":      "1",
		"head_dim":                 "64",
		"vocab_size":               "1000",
		"torch_dtype":              "bfloat16",
	}}
	names := map[string]string{
		"embed.weight":                                 "embedding",
		"layers.0.attn.wq_a.weight":                    "layer.0",
		"layers.0.ffn.shared_experts.w1.weight":        "layer.0",
		"layers.0.ffn.experts.0.w1.weight":             "experts.0",
		"layers.0.ffn.experts.1.w1.weight":             "experts.0",
		"layers.1.attn.wq_a.weight":                    "layer.1",
		"layers.1.ffn.experts.0.w1.weight":             "experts.1",
		"layers.2.attn.wq_a.weight":                    "draft.2",
		"layers.2.ffn.experts.0.w1.weight":             "draft.2",
		"mtp.0.attn.wq_a.weight":                       "draft.0",
		"mtp.0.ffn.experts.3.w1.weight":                "draft.0",
		"vision.blocks.0.attn.wqkv.weight":             "vision",
		"vision.patch_embed.proj.weight":               "vision",
		"vision_tower.vision_model.encoder.layers.3.x": "vision",
		"multi_modal_projector.linear_1.weight":        "vision",
		"aligner.w1.weight":                            "vision",
		"image_newline":                                "vision",
		"audio_tower.layers.0.self_attn.k_proj.weight": "audio",
		"head.weight":                                  "output",
		"norm.weight":                                  "output",
		"hc_head_base":                                 "output",
	}
	for name := range names {
		raw.Tensors = append(raw.Tensors, &v1.TensorInfo{Name: name, Bytes: 8, Elements: 4})
	}
	d, err := b.Build(raw)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]uint64{}
	for _, g := range d.GetGroups() {
		got[g.GetId()] = g.GetBytes()
	}
	want := map[string]uint64{}
	for _, id := range names {
		want[id] += 8
	}
	for id, bytes := range want {
		if got[id] != bytes {
			t.Errorf("%s=%d want %d", id, got[id], bytes)
		}
	}
	for id := range got {
		if _, ok := want[id]; !ok {
			t.Errorf("unexpected group %s", id)
		}
	}
	if d.GetParams()["n_layer"] != 2 || d.GetParams()["n_layer_draft"] != 1 {
		t.Fatalf("params %v", d.GetParams())
	}
	// Without a layer count in the config nothing says where the heads start, so a numbered layer stays a layer
	delete(raw.Metadata, "num_hidden_layers")
	d, err = b.Build(raw)
	if err != nil {
		t.Fatal(err)
	}
	if d.GetParams()["n_layer"] != 3 {
		t.Fatalf("layers seen should count the numbered layers alone, got %v", d.GetParams()["n_layer"])
	}
	for _, g := range d.GetGroups() {
		if g.GetId() == "draft.2" {
			t.Fatal("a layer past an unknown count cannot be a draft")
		}
	}

	// A GGUF block count includes the prediction heads, which sit in the last blocks
	gg := &v1.RawModel{FormatId: "gguf", Group: "Q4_K_M", Metadata: map[string]string{
		"general.architecture":            "glm4moe",
		"glm4moe.block_count":             "3",
		"glm4moe.nextn_predict_layers":    "1",
		"glm4moe.embedding_length":        "1024",
		"glm4moe.attention.head_count":    "16",
		"glm4moe.attention.head_count_kv": "4",
		"glm4moe.context_length":          "4096",
		"tokenizer.ggml.tokens.length":    "1000",
	}}
	gnames := map[string]string{
		"token_embd.weight":          "embedding",
		"blk.0.attn_q.weight":        "layer.0",
		"blk.0.ffn_up_exps.weight":   "experts.0",
		"blk.1.attn_q.weight":        "layer.1",
		"blk.2.attn_q.weight":        "draft.2",
		"blk.2.ffn_up_exps.weight":   "draft.2",
		"blk.2.nextn.eh_proj.weight": "draft.2",
		"v.blk.0.attn_q.weight":      "vision",
		"mm.0.weight":                "vision",
		"a.blk.0.attn_q.weight":      "audio",
		"mm.a.mlp.0.weight":          "audio",
		"output.weight":              "output",
	}
	for name := range gnames {
		gg.Tensors = append(gg.Tensors, &v1.TensorInfo{Name: name, Bytes: 8, Elements: 4})
	}
	d, err = b.Build(gg)
	if err != nil {
		t.Fatal(err)
	}
	got = map[string]uint64{}
	for _, g := range d.GetGroups() {
		got[g.GetId()] = g.GetBytes()
	}
	want = map[string]uint64{}
	for _, id := range gnames {
		want[id] += 8
	}
	for id, bytes := range want {
		if got[id] != bytes {
			t.Errorf("gguf %s=%d want %d", id, got[id], bytes)
		}
	}
	for id := range got {
		if _, ok := want[id]; !ok {
			t.Errorf("gguf unexpected group %s", id)
		}
	}
	if d.GetPrecision().GetBits() != 4 || d.GetPrecision().GetLabel() != "4-bit Q4_K_M" {
		t.Fatalf("gguf precision %v", d.GetPrecision())
	}
}
