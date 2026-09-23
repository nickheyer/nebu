package diffusion

import (
	"strconv"
	"testing"

	"github.com/nickheyer/nebu/pkg/formats"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

func tensors(names ...string) []*v1.TensorInfo {
	out := make([]*v1.TensorInfo, 0, len(names))
	for _, n := range names {
		out = append(out, &v1.TensorInfo{Name: n, Bytes: 2, Elements: 1})
	}
	return out
}

func shaped(name string, shape ...uint64) *v1.TensorInfo {
	return &v1.TensorInfo{Name: name, Bytes: 2, Elements: formats.Elements(shape), Shape: shape}
}

// Creates unique tensor entries with optional shapes.
func withShapes(names []string, shapes ...*v1.TensorInfo) []*v1.TensorInfo {
	byName := map[string]*v1.TensorInfo{}
	for _, s := range shapes {
		byName[s.GetName()] = s
	}
	out := make([]*v1.TensorInfo, 0, len(names)+len(shapes))
	seen := map[string]bool{}
	for _, n := range names {
		if s, ok := byName[n]; ok {
			out = append(out, s)
		} else {
			out = append(out, tensors(n)[0])
		}
		seen[n] = true
	}
	for _, s := range shapes {
		if !seen[s.GetName()] {
			out = append(out, s)
		}
	}
	return out
}

func stack(prefix string, n int, suffix string) []string {
	var out []string
	for i := 0; i < n; i++ {
		out = append(out, prefix+"."+strconv.Itoa(i)+"."+suffix)
	}
	return out
}

func TestKind(t *testing.T) {
	want := map[string]v1.TensorGroupKind{
		"model.diffusion_model.input_blocks.0.0.weight":                             v1.TensorGroupKind_TENSOR_GROUP_KIND_DIFFUSION,
		"double_blocks.3.img_attn.qkv.weight":                                       v1.TensorGroupKind_TENSOR_GROUP_KIND_DIFFUSION,
		"blocks.0.self_attn.q.weight":                                               v1.TensorGroupKind_TENSOR_GROUP_KIND_DIFFUSION,
		"joint_blocks.1.x_block.attn.qkv.weight":                                    v1.TensorGroupKind_TENSOR_GROUP_KIND_DIFFUSION,
		"first_stage_model.decoder.conv_in.weight":                                  v1.TensorGroupKind_TENSOR_GROUP_KIND_VAE,
		"decoder.up.3.block.0.conv1.weight":                                         v1.TensorGroupKind_TENSOR_GROUP_KIND_VAE,
		"cond_stage_model.transformer.text_model.final_layer_norm.weight":           v1.TensorGroupKind_TENSOR_GROUP_KIND_TEXT_ENCODER,
		"conditioner.embedders.1.model.transformer.resblocks.0.attn.in_proj_weight": v1.TensorGroupKind_TENSOR_GROUP_KIND_TEXT_ENCODER,
		"encoder.block.23.layer.1.DenseReluDense.wi_0.weight":                       v1.TensorGroupKind_TENSOR_GROUP_KIND_TEXT_ENCODER,
		"model.layers.27.self_attn.q_proj.weight":                                   v1.TensorGroupKind_TENSOR_GROUP_KIND_TEXT_ENCODER,
		"vision_model.encoder.layers.0.self_attn.k_proj.weight":                     v1.TensorGroupKind_TENSOR_GROUP_KIND_VISION,
		"feature_extractor.conv_layers.0.conv.weight":                               v1.TensorGroupKind_TENSOR_GROUP_KIND_AUDIO,
	}
	for name, kind := range want {
		if got, ok := Any(name); !ok || got != kind {
			t.Errorf("Any(%s) = %v %v, want %v", name, got, ok, kind)
		}
	}
	for _, name := range []string{"blk.3.attn_q.weight", "token_embd.weight", "output.weight"} {
		if _, ok := Any(name); ok {
			t.Errorf("Any(%s) should say nothing about a language model tensor", name)
		}
	}
	for _, name := range []string{"layers.0.attention.wq.weight", "model.layers.0.self_attn.q_proj.weight", "blocks.0.attn.weight", "head.weight", "shared.weight"} {
		if _, ok := Kind(name); ok {
			t.Errorf("Kind(%s) must not claim a name a language model uses", name)
		}
	}
}

func TestScanFamilies(t *testing.T) {
	sd1 := append(stack("model.diffusion_model.input_blocks", 12, "0.weight"), "model.diffusion_model.middle_block.1.weight", "model.diffusion_model.output_blocks.7.1.weight", "first_stage_model.decoder.conv_in.weight")
	cases := []struct {
		group   string
		tensors []*v1.TensorInfo
		family  string
		variant string
		vae     bool
		text    bool
		image   bool
	}{
		{"v1-5-pruned-emaonly", withShapes(sd1, shaped("cond_stage_model.transformer.text_model.embeddings.token_embedding.weight", 49408, 768), shaped("model.diffusion_model.input_blocks.0.0.weight", 320, 4, 3, 3)), "sd1", "", true, true, false},
		{"sd-v1-5-inpainting", withShapes(sd1, shaped("cond_stage_model.transformer.text_model.embeddings.token_embedding.weight", 49408, 768), shaped("model.diffusion_model.input_blocks.0.0.weight", 320, 9, 3, 3)), "sd1", "inpaint", true, true, true},
		{"v2-1_768-ema-pruned", append(tensors(sd1...), shaped("cond_stage_model.model.token_embedding.weight", 49408, 1024)), "sd2", "", true, true, false},
		{"sd_xl_base_1.0", append(tensors(sd1...), shaped("conditioner.embedders.0.transformer.text_model.embeddings.token_embedding.weight", 49408, 768), shaped("conditioner.embedders.1.model.token_embedding.weight", 49408, 1280)), "sdxl", "", true, true, false},
		{"sd3.5_large", tensors(append(stack("model.diffusion_model.joint_blocks", 9, "x_block.attn.qkv.weight"), "model.diffusion_model.x_embedder.proj.weight")...), "sd3", "", false, false, false},
		{"flux1-dev", append(tensors(stack("double_blocks", 19, "img_attn.qkv.weight")...), shaped("img_in.weight", 3072, 64)), "flux", "", false, false, true},
		{"flux1-fill-dev", append(tensors(stack("double_blocks", 19, "img_attn.qkv.weight")...), shaped("img_in.weight", 3072, 384)), "flux", "fill", false, false, true},
		{"chroma-unlocked-v40", tensors(append(stack("double_blocks", 19, "img_attn.qkv.weight"), "distilled_guidance_layer.in_proj.weight")...), "chroma", "", false, false, false},
		{"flux2-dev", tensors(append(stack("double_blocks", 8, "img_attn.qkv.weight"), "double_stream_modulation_img.lin.weight", "single_blocks.47.linear1.weight")...), "flux2", "", false, false, true},
		{"flux-2-klein-4b", tensors(append(stack("double_blocks", 8, "img_attn.qkv.weight"), "double_stream_modulation_img.lin.weight")...), "flux2_klein", "", false, false, true},
		{"wan2.1_t2v_1.3B_fp16", append(tensors(stack("blocks", 30, "self_attn.q.weight")...), tensors("blocks.0.cross_attn.norm_k.weight", "head.head.weight")[0], shaped("patch_embedding.weight", 1536, 16, 1, 2, 2)), "wan", "", false, false, false},
		{"wan2.1_i2v_480p_14B", append(tensors(append(stack("blocks", 40, "self_attn.q.weight"), "blocks.0.cross_attn.norm_k.weight", "img_emb.proj.0.weight")...), shaped("patch_embedding.weight", 5120, 36, 1, 2, 2)), "wan", "", false, false, true},
		{"wan2.2_i2v_high_noise_14B", append(tensors(append(stack("blocks", 40, "self_attn.q.weight"), "blocks.0.cross_attn.norm_k.weight")...), shaped("patch_embedding.weight", 5120, 36, 1, 2, 2)), "wan", "i2v", false, false, true},
		{"wan2.2_ti2v_5B", append(tensors(append(stack("blocks", 30, "self_attn.q.weight"), "blocks.0.cross_attn.norm_k.weight")...), shaped("patch_embedding.weight", 3072, 48, 1, 2, 2)), "wan", "ti2v", false, false, true},
		{"wan2.2_s2v_14B", tensors(append(stack("blocks", 40, "self_attn.q.weight"), "blocks.0.cross_attn.norm_k.weight", "audio_injector.injector.0.q.weight")...), "wan", "s2v", false, false, true},
		{"qwen_image_bf16", tensors(append(stack("transformer_blocks", 60, "attn.to_q.weight"), "transformer_blocks.0.img_mod.1.weight", "img_in.weight")...), "qwen_image", "", false, false, true},
		{"qwen-image-2.1-UC-Q8_0", tensors(append(stack("transformer_blocks", 32, "attn.to_q.weight"), "txt_in.text_norm.weight", "txt_in.in_layer.weight", "img_in.weight")...), "qwen_image21", "", false, false, true},
		{"hunyuanvideo1.5_720p_t2v", withShapes(append(stack("double_blocks", 20, "img_attn_qkv.weight"), "txt_in.individual_token_refiner.blocks.0.adaLN_modulation.1.weight"), shaped("txt_in.input_embedder.weight", 3072, 3584)), "hunyuan_video_15", "", false, false, true},
		{"hunyuan_video_t2v_720p_bf16", withShapes(append(stack("double_blocks", 20, "img_attn_qkv.weight"), "txt_in.individual_token_refiner.blocks.0.adaLN_modulation.1.weight"), shaped("txt_in.input_embedder.weight", 3072, 4096)), "hunyuan_video", "", false, false, true},
		{"lumina_2_model_bf16", withShapes(append(stack("layers", 30, "attention.qkv.weight"), "cap_embedder.0.weight"), shaped("cap_embedder.0.weight", 2304)), "lumina2", "", false, false, false},
		{"ltx-2.3-22b-dev", tensors(append(stack("transformer_blocks", 48, "attn1.to_q.weight"), "adaln_single.emb.timestep_embedder.linear_1.bias")...), "ltx2", "", false, false, true},
		{"z_image_turbo", withShapes(append(stack("layers", 30, "attention.qkv.weight"), "cap_embedder.0.weight"), shaped("cap_embedder.0.weight", 2560)), "z_image", "", false, false, false},
		{"minimax_h3_fl2va", tensors("video_patch_proj.weight", "audio_patch_proj.weight", "blocks.0.attn.to_q.weight"), "minimax_h3", "", false, false, true},
		{"lingbot-video-dense-1.3b", tensors(append(stack("blocks", 30, "self_attn.q.weight"), "patch_embedder.weight")...), "lingbot_video", "", false, false, true},
		{"ideogram4_fp8", tensors(append(stack("layers", 30, "attention.qkv.weight"), "embed_image_indicator.weight")...), "ideogram4", "", false, false, false},
		{"lens_bf16", tensors("transformer_blocks.0.attn.norm_added_q.weight", "transformer_blocks.0.img_mlp.w1.weight"), "lens", "", false, false, false},
	}
	for _, c := range cases {
		p := Scan(c.tensors, c.group)
		if p.Family != c.family || p.Variant != c.variant || p.VAE != c.vae || p.TextEncoder != c.text || p.ImageInput != c.image || p.Component != "" {
			t.Errorf("%s: got %+v, want family %s variant %q vae %v text %v image %v", c.group, p, c.family, c.variant, c.vae, c.text, c.image)
		}
	}
}

func TestScanComponents(t *testing.T) {
	cases := []struct {
		group   string
		tensors []*v1.TensorInfo
		want    string
	}{
		{"wan_2.1_vae", tensors("encoder.conv1.weight", "decoder.head.0.gamma", "conv1.weight"), "vae"},
		{"ae", tensors("encoder.conv_in.weight", "decoder.conv_out.weight", "quant_conv.weight"), "vae"},
		{"ltx-2.5-audio-vae-bf16", tensors("encoder.conv_in.weight", "decoder.conv_out.weight"), "audio_vae"},
		{"taesd", tensors("encoder.0.weight", "decoder.3.conv.0.weight"), "taesd"},
		{"umt5-xxl-encoder", tensors("encoder.block.0.layer.0.SelfAttention.q.weight", "shared.weight", "encoder.final_layer_norm.weight"), "t5"},
		{"clip_l", []*v1.TensorInfo{shaped("text_model.embeddings.token_embedding.weight", 49408, 768), shaped("text_model.encoder.layers.0.mlp.fc1.weight", 3072, 768)}, "clip_l"},
		{"clip_g", []*v1.TensorInfo{shaped("text_model.embeddings.token_embedding.weight", 49408, 1280), shaped("text_projection.weight", 1280, 1280)}, "clip_g"},
		{"qwen_2.5_vl_7b", tensors("model.layers.0.self_attn.q_proj.weight", "model.embed_tokens.weight", "visual.blocks.0.attn.qkv.weight"), "llm"},
		{"clip_vision_h", tensors("vision_model.encoder.layers.0.self_attn.k_proj.weight", "visual_projection.weight"), "clip_vision"},
		{"wav2vec2_large_english_fp16", tensors("feature_extractor.conv_layers.0.conv.weight", "encoder.layers.0.attention.k_proj.weight"), "audio_encoder"},
		{"lightx2v_4steps_lora", tensors("diffusion_model.blocks.0.self_attn.q.lora_A.weight", "diffusion_model.blocks.0.self_attn.q.lora_B.weight"), "lora"},
		{"adapter_model", tensors("blocks.0.attn.qkv_proj.lora_a", "blocks.0.attn.qkv_proj.lora_b", "token_refiner.blocks.0.mlp.fc1.lora_a"), "lora"},
		{"kohya", tensors("lora_unet_double_blocks_0_img_attn_qkv.lora_down.weight", "lora_unet_double_blocks_0_img_attn_qkv.lora_up.weight"), "lora"},
		{"control_v11p_sd15_canny", tensors("control_model.input_blocks.0.0.weight"), "controlnet"},
		{"RealESRGAN_x4plus", tensors("conv_first.weight", "body.0.rdb1.conv1.weight"), "upscaler"},
		{"ip-adapter_sd15", tensors("image_proj.proj.weight", "ip_adapter.1.to_k_ip.weight"), "ip_adapter"},
		{"ltx-2.3-22b-dev_embeddings_connectors", tensors("connectors.0.weight"), "embeddings_connectors"},
	}
	for _, c := range cases {
		p := Scan(c.tensors, c.group)
		if p.Component != c.want || p.Family != "" {
			t.Errorf("%s: got component %q family %q, want %s", c.group, p.Component, p.Family, c.want)
		}
	}
}

func TestCanonical(t *testing.T) {
	if got := Generates("wan"); len(got) != 2 {
		t.Fatalf("wan makes images and video: %v", got)
	}
	if Canonical("hyvid") != "hunyuan_video" || Canonical("hunyuan_video_1.5") != "hunyuan_video_15" || Canonical("FluxTransformer2DModel") != "flux" || Canonical("HiDreamImageTransformer2DModel") != "hidream_i1" || Canonical("Lumina2Transformer2DModel") != "lumina2" || Canonical("LTXVideoTransformer3DModel") != "ltxv" || Canonical("QwenImage21Transformer2DModel") != "qwen_image21" || Canonical("qwen_image_2.1") != "qwen_image21" || Canonical("AutoencoderKLQwenImage21") != "vae" {
		t.Fatal("canonical names")
	}
	if !Denoiser("sdxl") || !Denoiser("cogvideox") || !Component("T5EncoderModel") || !Component("clip_h") || !Component("tokenizer") || Component("llama") {
		t.Fatal("denoisers and components")
	}
	for _, f := range Families() {
		if !Denoiser(f) {
			t.Errorf("%s is listed but not a denoiser", f)
		}
	}
}

func TestScanShapes(t *testing.T) {
	umt5 := Scan([]*v1.TensorInfo{shaped("shared.weight", 256384, 4096), shaped("encoder.block.0.layer.0.SelfAttention.q.weight", 4096, 4096)}, "umt5_xxl_fp16")
	if umt5.Component != "t5" || umt5.Vocab != 256384 || umt5.Width != 4096 {
		t.Fatalf("umt5 %+v", umt5)
	}
	t5 := Scan([]*v1.TensorInfo{shaped("shared.weight", 32128, 4096), shaped("encoder.block.0.layer.0.SelfAttention.q.weight", 4096, 4096)}, "t5xxl_fp16")
	if t5.Component != "t5" || t5.Vocab != 32128 {
		t.Fatalf("t5 %+v", t5)
	}
	clipH := Scan([]*v1.TensorInfo{shaped("text_model.embeddings.token_embedding.weight", 49408, 1024), shaped("text_model.encoder.layers.0.mlp.fc1.weight", 4096, 1024)}, "open_clip_vit_h")
	if clipH.Component != "clip_h" || clipH.Width != 1024 {
		t.Fatalf("clip-h %+v", clipH)
	}
	wan := Scan([]*v1.TensorInfo{shaped("encoder.conv1.weight", 96, 3, 3, 3, 3), shaped("decoder.conv1.weight", 384, 16, 3, 3, 3)}, "wan_2.1_vae")
	if wan.Component != "vae" || wan.LatentChannels != 16 || !wan.VideoVAE {
		t.Fatalf("wan vae %+v", wan)
	}
	ae := Scan([]*v1.TensorInfo{shaped("encoder.conv_in.weight", 128, 3, 3, 3), shaped("decoder.conv_in.weight", 512, 16, 3, 3), shaped("quant_conv.weight", 32, 32, 1, 1)}, "ae")
	if ae.Component != "vae" || ae.LatentChannels != 16 || ae.VideoVAE {
		t.Fatalf("flux ae %+v", ae)
	}
	var f Format
	raw := &v1.RawModel{FormatId: "diffusion", Group: "wan_2.1_vae", Tensors: []*v1.TensorInfo{shaped("decoder.conv1.weight", 384, 16, 3, 3, 3)}}
	m := f.Metadata(raw)
	if m[KeyLatentChannels] != "16" || m[KeyVideoVAE] != "true" || m[KeyComponent] != "vae" {
		t.Fatalf("vae metadata %v", m)
	}
	if p := ProfileOf(&v1.Descriptor{Architecture: "vae", Kind: v1.ModelKind_MODEL_KIND_COMPONENT, Metadata: m}); p.LatentChannels != 16 || !p.VideoVAE || p.Component != "vae" {
		t.Fatalf("profile of vae %+v", p)
	}
	if p := f.Params(&v1.RawModel{Group: "umt5", Tensors: []*v1.TensorInfo{shaped("shared.weight", 256384, 4096)}}); p.Vocab != 256384 || p.Embedding != 4096 {
		t.Fatalf("params of an encoder %+v", p)
	}
}

func TestFormat(t *testing.T) {
	var f Format
	for _, p := range []string{"a.safetensors", "dir/b.sft", "c.ckpt", "d.pt", "e.pth"} {
		if c, ok := f.Classify(p); !ok || c.Role != v1.ArtifactRole_ARTIFACT_ROLE_WEIGHTS || c.Group == "" {
			t.Errorf("Classify(%s) = %+v %v", p, c, ok)
		}
	}
	if _, ok := f.Classify("model.gguf"); ok {
		t.Fatal("gguf belongs to its own format")
	}
	raw := &v1.RawModel{FormatId: "diffusion", Group: "wan2.1_t2v_1.3B_fp16", Tensors: append(tensors(append(stack("blocks", 30, "self_attn.q.weight"), "blocks.0.cross_attn.norm_k.weight")...), shaped("patch_embedding.weight", 1536, 16, 1, 2, 2))}
	raw.Tensors[0].Elements = 1536 * 1536
	if got := f.Architecture(raw); got != "wan" {
		t.Fatalf("architecture %q", got)
	}
	if p := f.Params(raw); p.Layers != 30 || p.Embedding != 1536 {
		t.Fatalf("params %+v", p)
	}
	m := f.Metadata(raw)
	if m[KeyFamily] != "wan" || m[KeyVAE] != "false" || m[KeyGenerates] != "image,video" {
		t.Fatalf("metadata %v", m)
	}
	d := &v1.Descriptor{Architecture: "wan", Metadata: m}
	if p := ProfileOf(d); p.Family != "wan" || p.VAE {
		t.Fatalf("profile of descriptor %+v", p)
	}
	var _ formats.Format = f
}

// Denoisers record whether their tensor names identify a family. Components and adapters carry no
// denoiser to detect.
func TestMetadataDetected(t *testing.T) {
	raw := func(names ...string) *v1.RawModel { return &v1.RawModel{Group: "x", Tensors: tensors(names...)} }
	if m := Metadata(raw("model.diffusion_model.video_patch_proj.weight", "model.diffusion_model.audio_patch_proj.weight")); m[KeyDetected] != "true" || m[KeyFamily] != "minimax_h3" {
		t.Fatalf("original names %v", m)
	}
	if m := Metadata(raw("transformer_blocks.0.attn.to_q.weight", "proj_in.weight", "audio_proj_in.weight")); m[KeyDetected] != "false" || m[KeyFamily] != "" {
		t.Fatalf("diffusers names %v", m)
	}
	for _, names := range [][]string{
		{"decoder.conv_in.weight", "encoder.conv_in.weight"},
		{"diffusion_model.blocks.0.attn.q.lora_down.weight", "diffusion_model.blocks.0.attn.q.lora_up.weight"},
		{"model.layers.0.self_attn.q_proj.weight"},
	} {
		if m := Metadata(raw(names...)); m[KeyDetected] != "" {
			t.Fatalf("%v: detected %q", names, m[KeyDetected])
		}
	}
	d := &v1.Descriptor{Metadata: map[string]string{KeyDetected: "false", KeyFlowShift: "10.0"}}
	if !Undetected(d) {
		t.Fatal("undetected")
	}
	if shift, ok := FlowShift(d); !ok || shift != 10 {
		t.Fatalf("flow shift %v %v", shift, ok)
	}
	if Undetected(&v1.Descriptor{}) {
		t.Fatal("no record means nothing to refuse")
	}
	if _, ok := FlowShift(&v1.Descriptor{Metadata: map[string]string{KeyFlowShift: "0"}}); ok {
		t.Fatal("zero is no shift")
	}
}
