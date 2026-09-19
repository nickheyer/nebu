package diffusers

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nickheyer/nebu/pkg/blueprint"
	"github.com/nickheyer/nebu/pkg/formats"
	"github.com/nickheyer/nebu/pkg/formats/diffusion"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/sources"
)

func safetensors(names map[string][]uint64, dtype string) []byte {
	header := map[string]any{}
	var off uint64
	for name, shape := range names {
		n := uint64(1)
		for _, d := range shape {
			n *= d
		}
		size := n * 2
		if dtype == "F32" {
			size = n * 4
		}
		header[name] = map[string]any{"dtype": dtype, "shape": shape, "data_offsets": []uint64{off, off + size}}
		off += size
	}
	js, _ := json.Marshal(header)
	out := make([]byte, 8)
	binary.LittleEndian.PutUint64(out, uint64(len(js)))
	return append(append(out, js...), make([]byte, off)...)
}

type memBlob struct {
	*strings.Reader
}

func (memBlob) Close() error { return nil }

func opener(files map[string][]byte) formats.Opener {
	return func(_ context.Context, a *v1.Artifact) (sources.Blob, error) {
		return memBlob{strings.NewReader(string(files[a.GetPath()]))}, nil
	}
}

// MiniMax-H3 fixture with root and nested pipelines.
func minimax() (map[string][]byte, *v1.Model) {
	files := map[string][]byte{
		"model_index.json":        []byte(`{"_class_name":"MiniMaxH3ModularPipeline","text_encoder":["transformers","Qwen3VLForConditionalGeneration",{"subfolder":"text_encoder"}],"tokenizer":["transformers","Qwen2TokenizerFast",{"subfolder":"tokenizer"}],"processor":["transformers","Qwen3VLProcessor",{"subfolder":"processor"}],"vae":["diffusers","AutoencoderKLMiniMaxH3",{"subfolder":"vae"}],"audio_vae":["diffusers","AutoencoderKLMiniMaxH3Audio",{"subfolder":"audio_vae"}],"transformer":["diffusers","MiniMaxH3Transformer3DModel",{"subfolder":"transformer"}],"transformer_ref":["diffusers","MiniMaxH3Transformer3DModel",{"subfolder":"transformer_ref"}],"scheduler":["diffusers","MiniMaxH3Scheduler",{"subfolder":"scheduler"}]}`),
		"transformer/config.json": []byte(`{"_class_name":"MiniMaxH3Transformer3DModel","hidden_size":5376,"num_layers":50}`),
		"transformer/diffusion_pytorch_model-00001-of-00002.safetensors": safetensors(map[string][]uint64{"blocks.0.attn.to_q.weight": {8, 8}}, "BF16"),
		"transformer/diffusion_pytorch_model-00002-of-00002.safetensors": safetensors(map[string][]uint64{"blocks.1.attn.to_q.weight": {8, 8}}, "BF16"),
		"transformer/diffusion_pytorch_model.safetensors.index.json":     []byte(`{}`),
		"vae/config.json":                                     []byte(`{"_class_name":"AutoencoderKLMiniMaxH3","latent_channels":24}`),
		"vae/diffusion_pytorch_model.safetensors":             safetensors(map[string][]uint64{"decoder.conv_in.weight": {8, 24, 3, 3, 3}}, "F32"),
		"audio_vae/config.json":                               []byte(`{"_class_name":"AutoencoderKLMiniMaxH3Audio"}`),
		"audio_vae/diffusion_pytorch_model.safetensors":       safetensors(map[string][]uint64{"encoder.block.0.weight": {4, 4}, "decoder.block.0.weight": {4, 4}}, "F32"),
		"text_encoder/config.json":                            []byte(`{"architectures":["Qwen3VLForConditionalGeneration"],"text_config":{"hidden_size":5120},"vision_config":{"depth":27}}`),
		"text_encoder/model.safetensors":                      safetensors(map[string][]uint64{"model.layers.0.self_attn.q_proj.weight": {8, 8}, "model.visual.blocks.0.attn.qkv.weight": {8, 8}}, "BF16"),
		"tokenizer/tokenizer.json":                            []byte(`{}`),
		"tokenizer/tokenizer_config.json":                     []byte(`{}`),
		"processor/tokenizer.json":                            []byte(`{}`),
		"scheduler/scheduler_config.json":                     []byte(`{}`),
		"transformer_ref/config.json":                         []byte(`{"_class_name":"MiniMaxH3Transformer3DModel","hidden_size":5376,"num_layers":50}`),
		"transformer_ref/diffusion_pytorch_model.safetensors": safetensors(map[string][]uint64{"blocks.0.attn.to_q.weight": {8, 8}}, "F16"),
		"FL2VA/model_index.json":                              []byte(`{"_class_name":"MiniMaxH3Pipeline","text_encoder":["transformers","MiniMaxH3Qwen3VLHFEncoder"],"tokenizer":["transformers","Qwen2TokenizerFast"],"video_vae":["diffusers","MiniMaxH3VideoVAE"],"audio_vae":["diffusers","MiniMaxH3AudioVAE"],"scheduler":null,"transformer":["diffusers","MiniMaxH3DiTModel"]}`),
		"FL2VA/transformer/config.json":                       []byte(`{"_class_name":"MiniMaxH3DiTModel","hidden_size":5376,"num_layers":50}`),
		"FL2VA/transformer/model.safetensors":                 safetensors(map[string][]uint64{"blocks.0.attn.to_q.weight": {8, 8}}, "BF16"),
		"FL2VA/video_vae/config.json":                         []byte(`{"_class_name":"MiniMaxH3VideoVAE","source_path":"source"}`),
		"FL2VA/video_vae/klvae.py":                            []byte(`pass`),
		"FL2VA/video_vae/source/config.json":                  []byte(`{"_class_name":"AutoencoderKLLegacy"}`),
		"FL2VA/video_vae/source/model.safetensors":            safetensors(map[string][]uint64{"decoder.conv_in.weight": {8, 24, 3, 3, 3}}, "F32"),
		"FL2VA/audio_vae/config.json":                         []byte(`{"_class_name":"MiniMaxH3AudioVAE"}`),
		"FL2VA/audio_vae/config.yaml":                         []byte(`a: 1`),
		"FL2VA/audio_vae/dac_utils.py":                        []byte(`pass`),
		"FL2VA/audio_vae/model.safetensors":                   safetensors(map[string][]uint64{"encoder.block.0.weight": {4, 4}}, "F32"),
		"FL2VA/text_encoder/config.json":                      []byte(`{"architectures":["Qwen3VLForConditionalGeneration"],"vision_config":{"depth":27}}`),
		"FL2VA/text_encoder/model.safetensors":                safetensors(map[string][]uint64{"model.layers.0.self_attn.q_proj.weight": {8, 8}}, "BF16"),
		"FL2VA/tokenizer/tokenizer.json":                      []byte(`{}`),
		"README.md":                                           []byte(`# hi`),
		"assets/demo.png":                                     []byte(`png`),
	}
	m := &v1.Model{Repo: "MiniMaxAI/MiniMax-H3"}
	for p, data := range files {
		m.Artifacts = append(m.Artifacts, &v1.Artifact{Path: p, SizeBytes: uint64(len(data))})
	}
	return files, m
}

func registry(t *testing.T) *formats.Registry {
	t.Helper()
	r, err := formats.New([]formats.Format{Format{}})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestLayAndClassify(t *testing.T) {
	files, m := minimax()
	r := registry(t)
	if err := r.Lay(context.Background(), opener(files), m); err != nil {
		t.Fatal(err)
	}
	if len(m.GetTrees()) != 2 || m.GetTrees()[0].GetRoot() != "FL2VA" || m.GetTrees()[1].GetRoot() != "" || m.GetTrees()[1].GetClassName() != "MiniMaxH3ModularPipeline" {
		t.Fatalf("trees %+v", m.GetTrees())
	}
	if p := m.GetTrees()[0].GetParts(); p["video_vae"] != "video_vae" || p["transformer"] != "transformer" || p["scheduler"] != "" {
		t.Fatalf("parts %v", p)
	}
	r.Classify(m)
	groups := r.Groups(m)
	names := map[string]*formats.Group{}
	var order []string
	for _, g := range groups {
		names[g.Name] = g
		order = append(order, g.Name)
	}
	if strings.Join(order, ",") != "FL2VA,transformer,transformer_ref" {
		t.Fatalf("groups %v", order)
	}
	fl, base, ref := names["FL2VA"], names["transformer"], names["transformer_ref"]
	// Variants share auxiliary components and retain only their own denoiser.
	if len(base.Weights) != 5 || len(base.Files[v1.ArtifactRole_ARTIFACT_ROLE_CONFIG]) != 6 || len(base.Files[v1.ArtifactRole_ARTIFACT_ROLE_TOKENIZER]) != 3 || len(base.Files[v1.ArtifactRole_ARTIFACT_ROLE_INDEX]) != 1 || base.Root != "" {
		t.Fatalf("transformer %d weights %v", len(base.Weights), base.Files)
	}
	if len(ref.Weights) != 4 || len(ref.Files[v1.ArtifactRole_ARTIFACT_ROLE_CONFIG]) != 6 || len(ref.Files[v1.ArtifactRole_ARTIFACT_ROLE_INDEX]) != 0 || ref.Root != "" {
		t.Fatalf("transformer_ref %d weights %v", len(ref.Weights), ref.Files)
	}
	for _, g := range []*formats.Group{base, ref} {
		for _, a := range append(g.Weights, g.Files[v1.ArtifactRole_ARTIFACT_ROLE_CONFIG]...) {
			other := "transformer_ref/"
			if g == ref {
				other = "transformer/"
			}
			if strings.HasPrefix(a.GetPath(), other) {
				t.Errorf("%s holds %s, the other variant's denoiser", g.Name, a.GetPath())
			}
		}
	}
	if len(fl.Weights) != 4 || fl.Root != "FL2VA" || len(fl.Files[v1.ArtifactRole_ARTIFACT_ROLE_CODE]) != 2 {
		t.Fatalf("FL2VA %d weights root %q code %v", len(fl.Weights), fl.Root, fl.Files[v1.ArtifactRole_ARTIFACT_ROLE_CODE])
	}
	for _, a := range m.GetArtifacts() {
		switch {
		case a.GetPath() == "README.md", strings.HasPrefix(a.GetPath(), "assets/"):
			if a.GetFormatId() == "diffusers" {
				t.Errorf("%s belongs to no pipeline", a.GetPath())
			}
		default:
			if a.GetFormatId() != "diffusers" {
				t.Errorf("%s belongs to a pipeline, classified %q %v", a.GetPath(), a.GetFormatId(), a.GetRole())
			}
		}
	}
	// Component prefixes determine placement. The denoiser class determines family.
	raw, err := Format{}.Read(context.Background(), opener(files), fl)
	if err != nil {
		t.Fatal(err)
	}
	var f Format
	if f.Architecture(raw) != "MiniMaxH3DiTModel" || diffusion.Canonical(f.Architecture(raw)) != "minimax_h3" {
		t.Fatalf("architecture %q", f.Architecture(raw))
	}
	kinds := map[v1.TensorGroupKind]int{}
	for _, tn := range raw.GetTensors() {
		k, _ := f.Tensor(tn.GetName())
		kinds[k]++
	}
	if kinds[v1.TensorGroupKind_TENSOR_GROUP_KIND_DIFFUSION] != 1 || kinds[v1.TensorGroupKind_TENSOR_GROUP_KIND_VAE] != 2 || kinds[v1.TensorGroupKind_TENSOR_GROUP_KIND_TEXT_ENCODER] != 1 {
		t.Fatalf("kinds %v", kinds)
	}
	if p := f.Params(raw); p.Layers != 50 || p.Embedding != 5376 {
		t.Fatalf("params %+v", p)
	}
	if w := f.Precision(raw, ""); w.Bits != 16 {
		t.Fatalf("precision %+v", w)
	}
	meta := f.Metadata(raw)
	if meta[diffusion.KeyFamily] != "minimax_h3" || meta[diffusion.KeyVAE] != "true" || meta[diffusion.KeyTextEncoder] != "true" || meta[diffusion.KeyGenerates] != "video" || meta[KeyClass] != "MiniMaxH3Pipeline" {
		t.Fatalf("metadata %v", meta)
	}
	if meta[diffusion.KeySlots] != "denoiser,text_encoder.llm,text_encoder.llm.vision,tokenizer,vae,vae.audio" {
		t.Fatalf("slots %q", meta[diffusion.KeySlots])
	}
	d := &v1.Descriptor{Architecture: f.Architecture(raw), Metadata: meta}
	slots := Slots(d)
	if !Pipeline(d) || slots[blueprint.SlotDenoiser] != "transformer" || slots[blueprint.SlotVAE] != "video_vae" || slots[blueprint.SlotVAEAudio] != "audio_vae" || slots[blueprint.SlotTextEncoderLLM] != "text_encoder" || slots[blueprint.SlotTextEncoderVision] != "text_encoder" || slots[blueprint.SlotTokenizer] != "tokenizer" {
		t.Fatalf("slots %v", slots)
	}
	// Subfolder files include weights, config, and code.
	if under := Under(fl, "video_vae"); len(under) != 4 {
		t.Fatalf("under video_vae %d", len(under))
	}
	// Each variant uses only its own denoiser class, config, and precision.
	for _, tc := range []struct {
		g    *formats.Group
		bits uint32
	}{{base, 16}, {ref, 16}} {
		raw, err := Format{}.Read(context.Background(), opener(files), tc.g)
		if err != nil {
			t.Fatal(err)
		}
		meta := f.Metadata(raw)
		if _, other := meta[prefix+"transformer_ref"]; other == (tc.g == base) {
			t.Fatalf("%s: declared %v", tc.g.Name, meta)
		}
		if _, own := meta[prefix+tc.g.Name]; !own || denoiserKey(raw.GetMetadata()) != tc.g.Name {
			t.Fatalf("%s: denoiser %q of %v", tc.g.Name, denoiserKey(raw.GetMetadata()), meta)
		}
		if p := f.Params(raw); p.Layers != 50 || p.Embedding != 5376 {
			t.Fatalf("%s: params %+v", tc.g.Name, p)
		}
		if w := f.Precision(raw, ""); w.Bits != tc.bits {
			t.Fatalf("%s: precision %+v", tc.g.Name, w)
		}
		if s := Slots(&v1.Descriptor{Architecture: f.Architecture(raw), Metadata: meta}); s[blueprint.SlotDenoiser] != tc.g.Name || s[blueprint.SlotTextEncoderVision] != "text_encoder" {
			t.Fatalf("%s: slots %v", tc.g.Name, s)
		}
		kinds := map[v1.TensorGroupKind]int{}
		for _, tn := range raw.GetTensors() {
			k, _ := f.Tensor(tn.GetName())
			kinds[k]++
		}
		if kinds[v1.TensorGroupKind_TENSOR_GROUP_KIND_VISION] != 1 || kinds[v1.TensorGroupKind_TENSOR_GROUP_KIND_TEXT_ENCODER] != 1 {
			t.Fatalf("%s: embedded vision tower placement: %v", tc.g.Name, kinds)
		}
	}
	// Resolve sharded components by index and tokenizers by tokenizer.json. Parent directory names
	// must not affect component matching.
	stored := &v1.StoredModel{Artifacts: []*v1.StoredArtifact{
		{Artifact: &v1.Artifact{Path: "transformer/diffusion_pytorch_model-00001-of-00002.safetensors", Role: v1.ArtifactRole_ARTIFACT_ROLE_WEIGHTS}, Path: "/store/transformer/transformer/a.safetensors"},
		{Artifact: &v1.Artifact{Path: "transformer/diffusion_pytorch_model-00002-of-00002.safetensors", Role: v1.ArtifactRole_ARTIFACT_ROLE_WEIGHTS}, Path: "/store/transformer/transformer/b.safetensors"},
		{Artifact: &v1.Artifact{Path: "transformer/diffusion_pytorch_model.safetensors.index.json", Role: v1.ArtifactRole_ARTIFACT_ROLE_INDEX}, Path: "/store/transformer/transformer/index.json"},
		{Artifact: &v1.Artifact{Path: "vae/diffusion_pytorch_model.safetensors", Role: v1.ArtifactRole_ARTIFACT_ROLE_WEIGHTS}, Path: "/store/transformer/vae/a.safetensors"},
		{Artifact: &v1.Artifact{Path: "tokenizer/tokenizer.json", Role: v1.ArtifactRole_ARTIFACT_ROLE_TOKENIZER}, Path: "/store/transformer/tokenizer/tokenizer.json"},
	}}
	if w, idx, _ := Files(stored, "transformer"); len(w) != 2 || idx != "/store/transformer/transformer/index.json" {
		t.Fatalf("files %v %q", w, idx)
	}
	if w, _, _ := Files(stored, "vae"); len(w) != 1 || w[0] != "/store/transformer/vae/a.safetensors" {
		t.Fatalf("vae files %v", w)
	}
	if _, _, tok := Files(stored, "tokenizer"); tok != "/store/transformer/tokenizer/tokenizer.json" {
		t.Fatalf("tokenizer %q", tok)
	}
	if k := keyOfPath(map[string]string{"transformer": "transformer", "vae": "vae"}, "/store/transformer/vae/a.safetensors"); k != "vae" {
		t.Fatalf("the directory nearest the file keys it, got %q", k)
	}
}

func TestWanPairSlots(t *testing.T) {
	d := &v1.Descriptor{Metadata: map[string]string{KeyClass: "WanPipeline", diffusion.KeyFamily: "wan", prefix + "transformer": "transformer", prefix + "transformer.class": "WanTransformer3DModel", prefix + "transformer_2": "transformer_2", prefix + "transformer_2.class": "WanTransformer3DModel", prefix + "vae": "vae", prefix + "vae.class": "AutoencoderKLWan", prefix + "text_encoder": "text_encoder", prefix + "text_encoder.class": "UMT5EncoderModel"}}
	s := Slots(d)
	if s[blueprint.SlotDenoiser] != "transformer_2" || s[blueprint.SlotDenoiserHighNoise] != "transformer" || s[blueprint.SlotTextEncoderT5] != "text_encoder" || s[blueprint.SlotVAE] != "vae" {
		t.Fatalf("wan %v", s)
	}
	flux := &v1.Descriptor{Metadata: map[string]string{KeyClass: "FluxPipeline", diffusion.KeyFamily: "flux", prefix + "transformer": "transformer", prefix + "transformer.class": "FluxTransformer2DModel", prefix + "text_encoder": "text_encoder", prefix + "text_encoder.class": "CLIPTextModel", prefix + "text_encoder_2": "text_encoder_2", prefix + "text_encoder_2.class": "T5EncoderModel"}}
	s = Slots(flux)
	if s[blueprint.SlotDenoiser] != "transformer" || s[blueprint.SlotTextEncoderClipL] != "text_encoder" || s[blueprint.SlotTextEncoderT5] != "text_encoder_2" {
		t.Fatalf("flux %v", s)
	}
}
