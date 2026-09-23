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

// Fixture in the layout FastVideo publishes: a modular pipeline index alone, sharded components,
// a reference transformer another repository hosts, and diffusers tensor names.
func modular(transformerNames ...string) (map[string][]byte, *v1.Model) {
	if len(transformerNames) == 0 {
		transformerNames = []string{"transformer_blocks.0.attn.to_q.weight", "proj_in.weight"}
	}
	first := map[string][]uint64{transformerNames[0]: {8, 8}}
	second := map[string][]uint64{}
	for _, n := range transformerNames[1:] {
		second[n] = []uint64{8, 8}
	}
	files := map[string][]byte{
		"modular_model_index.json": []byte(`{"_blocks_class_name":"MiniMaxH3Blocks","_class_name":"MiniMaxH3ModularPipeline","transformer":["diffusers","MiniMaxH3Transformer3DModel",{"pretrained_model_name_or_path":"FastVideo/FastVideo-FastH3-8-Step-V2","subfolder":"transformer"}],"transformer_ref":["diffusers","MiniMaxH3Transformer3DModel",{"pretrained_model_name_or_path":"MiniMaxAI/MiniMax-H3","subfolder":"transformer_ref"}],"vae":["diffusers","AutoencoderKLMiniMaxH3",{"subfolder":"vae"}],"audio_vae":["diffusers","AutoencoderKLMiniMaxH3Audio",{"subfolder":"audio_vae"}],"text_encoder":["transformers","Qwen3VLForConditionalGeneration",{"subfolder":"text_encoder"}],"tokenizer":["transformers","Qwen2TokenizerFast",{"subfolder":"tokenizer"}],"processor":["transformers","Qwen3VLProcessor",{"subfolder":"processor"}],"scheduler":["diffusers","MiniMaxH3Scheduler",{"subfolder":"scheduler"}],"audio_scheduler":["diffusers","MiniMaxH3Scheduler",{"subfolder":"audio_scheduler"}]}`),
		"transformer/config.json":  []byte(`{"_class_name":"MiniMaxH3Transformer3DModel","hidden_size":5376,"num_layers":50}`),
		"transformer/diffusion_pytorch_model-00001-of-00002.safetensors": safetensors(first, "BF16"),
		"transformer/diffusion_pytorch_model-00002-of-00002.safetensors": safetensors(second, "BF16"),
		"transformer/diffusion_pytorch_model.safetensors.index.json":     []byte(`{}`),
		"vae/config.json": []byte(`{"_class_name":"AutoencoderKLMiniMaxH3","latent_channels":24}`),
		"vae/diffusion_pytorch_model-00001-of-00002.safetensors": safetensors(map[string][]uint64{"decoder.conv_in.weight": {8, 24, 3, 3, 3}}, "F32"),
		"vae/diffusion_pytorch_model-00002-of-00002.safetensors": safetensors(map[string][]uint64{"encoder.conv_in.weight": {8, 3, 3, 3, 3}}, "F32"),
		"vae/diffusion_pytorch_model.safetensors.index.json":     []byte(`{}`),
		"audio_vae/config.json":                                  []byte(`{"_class_name":"AutoencoderKLMiniMaxH3Audio"}`),
		"audio_vae/diffusion_pytorch_model.safetensors":          safetensors(map[string][]uint64{"encoder.block.0.weight": {4, 4}, "decoder.block.0.weight": {4, 4}}, "F32"),
		"text_encoder/config.json":                               []byte(`{"architectures":["Qwen3VLForConditionalGeneration"],"text_config":{"hidden_size":5120},"vision_config":{"depth":27}}`),
		"text_encoder/model-00001-of-00002.safetensors":          safetensors(map[string][]uint64{"model.language_model.layers.0.self_attn.q_proj.weight": {8, 8}}, "BF16"),
		"text_encoder/model-00002-of-00002.safetensors":          safetensors(map[string][]uint64{"model.visual.blocks.0.attn.qkv.weight": {8, 8}}, "BF16"),
		"text_encoder/model.safetensors.index.json":              []byte(`{}`),
		"tokenizer/tokenizer.json":                               []byte(`{}`),
		"processor/tokenizer.json":                               []byte(`{}`),
		"scheduler/scheduler_config.json":                        []byte(`{"_class_name":"MiniMaxH3Scheduler","_diffusers_version":"0.36.0.dev0","shift":10.0}`),
		"audio_scheduler/scheduler_config.json":                  []byte(`{"_class_name":"MiniMaxH3Scheduler","shift":3.0}`),
		"fastvideo_inference.json":                               []byte(`{"video_scheduler_shift":10.0}`),
		"README.md":                                              []byte(`# hi`),
	}
	m := &v1.Model{Repo: "FastVideo/FastVideo-FastH3-8-Step-V2"}
	for p, data := range files {
		m.Artifacts = append(m.Artifacts, &v1.Artifact{Path: p, SizeBytes: uint64(len(data))})
	}
	return files, m
}

func TestModularPipeline(t *testing.T) {
	files, m := modular()
	r := registry(t)
	if err := r.Lay(context.Background(), opener(files), m); err != nil {
		t.Fatal(err)
	}
	if len(m.GetTrees()) != 1 || m.GetTrees()[0].GetRoot() != "" || m.GetTrees()[0].GetClassName() != "MiniMaxH3ModularPipeline" {
		t.Fatalf("trees %+v", m.GetTrees())
	}
	if p := m.GetTrees()[0].GetParts(); p["transformer"] != "transformer" || p["transformer_ref"] != "transformer_ref" || p["scheduler"] != "scheduler" {
		t.Fatalf("parts %v", p)
	}
	r.Classify(m)
	groups := r.Groups(m)
	if len(groups) != 1 || groups[0].Name != "transformer" {
		t.Fatalf("groups %+v", groups)
	}
	g := groups[0]
	if len(g.Weights) != 7 || len(g.Files[v1.ArtifactRole_ARTIFACT_ROLE_INDEX]) != 3 {
		t.Fatalf("%d weights, files %v", len(g.Weights), g.Files)
	}
	for _, a := range m.GetArtifacts() {
		switch a.GetPath() {
		case "README.md", "fastvideo_inference.json":
			if a.GetFormatId() == "diffusers" {
				t.Errorf("%s belongs to no pipeline", a.GetPath())
			}
		case "modular_model_index.json":
			if a.GetFormatId() != "diffusers" || a.GetRole() != v1.ArtifactRole_ARTIFACT_ROLE_CONFIG {
				t.Errorf("the modular index is the pipeline config, classified %q %v", a.GetFormatId(), a.GetRole())
			}
		default:
			if a.GetFormatId() != "diffusers" {
				t.Errorf("%s belongs to the pipeline, classified %q %v", a.GetPath(), a.GetFormatId(), a.GetRole())
			}
		}
	}
	var f Format
	raw, err := f.Read(context.Background(), opener(files), g)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := raw.GetMetadata()[prefix+"transformer_ref"]; ok {
		t.Fatal("a transformer another repository hosts is not a part of this pipeline")
	}
	if raw.GetMetadata()[prefix+"scheduler"] != "scheduler" || raw.GetMetadata()["scheduler.shift"] != "10.0" || raw.GetMetadata()["audio_scheduler.shift"] != "3.0" {
		t.Fatalf("scheduler config %v", raw.GetMetadata())
	}
	meta := f.Metadata(raw)
	if meta[diffusion.KeyFamily] != "minimax_h3" || meta[diffusion.KeyDetected] != "false" || meta[diffusion.KeyFlowShift] != "10.0" {
		t.Fatalf("diffusers names identify no family and the scheduler sets the shift: %v", meta)
	}
	d := &v1.Descriptor{Architecture: f.Architecture(raw), Metadata: meta}
	if !diffusion.Undetected(d) {
		t.Fatal("undetected")
	}
	if shift, ok := diffusion.FlowShift(d); !ok || shift != 10 {
		t.Fatalf("flow shift %v %v", shift, ok)
	}
	slots := Slots(d)
	if slots[blueprint.SlotDenoiser] != "transformer" || slots[blueprint.SlotVAE] != "vae" || slots[blueprint.SlotVAEAudio] != "audio_vae" || slots[blueprint.SlotTextEncoderLLM] != "text_encoder" || slots[blueprint.SlotTextEncoderVision] != "text_encoder" || slots[blueprint.SlotTokenizer] != "tokenizer" {
		t.Fatalf("slots %v", slots)
	}
	// The original tensor names identify the family.
	files, m = modular("video_patch_proj.weight", "audio_patch_proj.weight", "blocks.0.attn.qkv_proj.weight")
	if err := r.Lay(context.Background(), opener(files), m); err != nil {
		t.Fatal(err)
	}
	r.Classify(m)
	raw, err = f.Read(context.Background(), opener(files), r.Groups(m)[0])
	if err != nil {
		t.Fatal(err)
	}
	if meta := f.Metadata(raw); meta[diffusion.KeyFamily] != "minimax_h3" || meta[diffusion.KeyDetected] != "true" {
		t.Fatalf("original names: %v", meta)
	}
	// A scheduler that shifts by resolution declares no fixed shift.
	files["scheduler/scheduler_config.json"] = []byte(`{"_class_name":"FlowMatchEulerDiscreteScheduler","shift":3.0,"use_dynamic_shifting":true,"base_shift":0.5,"max_shift":1.15}`)
	raw, err = f.Read(context.Background(), opener(files), r.Groups(m)[0])
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := f.Metadata(raw)[diffusion.KeyFlowShift]; ok {
		t.Fatal("dynamic shifting declares no flow shift")
	}
	// A directory holding both indexes is laid out from model_index.json.
	files["model_index.json"] = []byte(`{"_class_name":"MiniMaxH3Pipeline","transformer":["diffusers","MiniMaxH3Transformer3DModel"],"vae":["diffusers","AutoencoderKLMiniMaxH3"]}`)
	m.Artifacts = append(m.Artifacts, &v1.Artifact{Path: "model_index.json", SizeBytes: 10})
	if err := r.Lay(context.Background(), opener(files), m); err != nil {
		t.Fatal(err)
	}
	if len(m.GetTrees()) != 1 || m.GetTrees()[0].GetClassName() != "MiniMaxH3Pipeline" {
		t.Fatalf("trees %+v", m.GetTrees())
	}
	r.Classify(m)
	raw, err = f.Read(context.Background(), opener(files), r.Groups(m)[0])
	if err != nil || raw.GetMetadata()[KeyClass] != "MiniMaxH3Pipeline" {
		t.Fatalf("read %v %v", raw.GetMetadata()[KeyClass], err)
	}
}
