package diffusion

import (
	"math"
	"sort"
	"strconv"
	"strings"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// Tensor signatures for original, ComfyUI, and diffusers layouts. Family detection follows
// stable-diffusion.cpp ModelLoader::get_sd_version.

// Denoiser prefixes used by stable-diffusion.cpp and original checkpoints.
var denoiserPrefixes = []string{"model.diffusion_model.uncond.", "model.diffusion_model.", "model.high_noise_diffusion_model.", "diffusion_model.", "unet.", "transformer."}

// Tensor patterns specific to diffusion models.
var (
	strictDenoiser = []string{
		"model.diffusion_model.", "model.high_noise_diffusion_model.", "diffusion_model.", "unet.",
		// UNet
		"input_blocks.", "middle_block.", "output_blocks.", "time_embed.", "label_emb.",
		"down_blocks.", "mid_block.", "up_blocks.", "conv_norm_out.", "add_embedding.",
		// Flux, Chroma, HunyuanVideo, Ovis
		"double_blocks.", "single_blocks.", "img_in.", "txt_in.", "time_in.", "vector_in.", "guidance_in.", "final_layer.", "distilled_guidance_layer.", "nerf_final_layer_conv.", "double_stream_modulation_img.", "double_stream_modulation_txt.", "single_stream_modulation.",
		// SD3
		"joint_blocks.", "x_embedder.", "context_embedder.", "t_embedder.", "y_embedder.",
		// Wan, LingBot
		"patch_embedding.", "patch_embedder.", "text_embedding.", "time_projection.", "img_emb.", "vace_blocks.", "vace_patch_embedding.", "audio_injector.", "casual_audio_encoder.",
		// Qwen Image, Mage-Flow, LTX, and diffusers transformers
		"transformer_blocks.", "single_transformer_blocks.", "time_text_embed.", "txt_norm.", "adaln_single.", "caption_projection.", "patchify_proj.", "scale_shift_table", "audio_patchify_proj.", "audio_caption_projection.", "video_patch_proj.", "audio_patch_proj.",
		// Z-Image, ERNIE, Boogu, Lens, Krea2, PiD, Ideogram, SeFi, Anima, MiniT2I, HiDream-O1
		"noise_refiner.", "context_refiner.", "cap_embedder.", "double_stream_layers.", "single_stream_layers.", "double_stream_blocks.", "single_stream_blocks.", "init_x_linear.", "cond_seq_linear.",
		"txtfusion.", "text_fusion.", "embed_image_indicator", "dual_time_embed.", "llm_adapter.", "net.lq_proj.", "net.img_embedder.", "net.blocks.", "net.x_embedder.",
	}
	strictVAE         = []string{"first_stage_model.", "vae.", "tae.", "quant_conv.", "post_quant_conv."}
	strictTextEncoder = []string{"cond_stage_model.", "conditioner.", "text_encoders.", "te.", "clip_l.", "clip_g.", "text_model."}
	strictVision      = []string{"clip_vision.", "vision_model.", "visual_projection."}
	strictAudio       = []string{"wav2vec2.", "feature_extractor.", "feature_projection.", "masked_spec_embed", "encoder.pos_conv_embed."}
)

// Patterns shared with language models, used only for known diffusion checkpoints.
var (
	looseDenoiser = []string{
		"blocks.", "head.", "out.", "net.", "layers.", "conv_in.", "conv_out.", "time_embedding.", "pos_embed", "norm_out.", "proj_out.", "proj_in.", "register_tokens", "modF.", "x_pad_token", "cap_pad_token", "caption_projection",
	}
	looseVAE         = []string{"encoder.", "decoder.", "conv1.", "conv2."}
	looseTextEncoder = []string{
		"text_projection", "logit_scale",
		"encoder.block.", "encoder.embed_tokens.", "encoder.final_layer_norm", "shared.",
		"transformer.resblocks.", "token_embedding.", "positional_embedding", "ln_final.",
		"model.layers.", "model.embed_tokens.", "model.norm.", "lm_head.", "model.language_model.", "language_model.", "model.visual.", "visual.",
	}
	looseAudio = []string{"encoder.layers."}
)

// Kind classifies tensor names specific to diffusion pipelines.
func Kind(name string) (v1.TensorGroupKind, bool) {
	switch {
	case hasAny(name, strictTextEncoder):
		return v1.TensorGroupKind_TENSOR_GROUP_KIND_TEXT_ENCODER, true
	case hasAny(name, strictVision):
		return v1.TensorGroupKind_TENSOR_GROUP_KIND_VISION, true
	case hasAny(name, strictAudio):
		return v1.TensorGroupKind_TENSOR_GROUP_KIND_AUDIO, true
	case hasAny(name, strictVAE):
		return v1.TensorGroupKind_TENSOR_GROUP_KIND_VAE, true
	case hasAny(name, strictDenoiser):
		return v1.TensorGroupKind_TENSOR_GROUP_KIND_DIFFUSION, true
	}
	return v1.TensorGroupKind_TENSOR_GROUP_KIND_UNSPECIFIED, false
}

// Any also accepts names shared with language models. Use only for known diffusion checkpoints.
func Any(name string) (v1.TensorGroupKind, bool) {
	if kind, ok := Kind(name); ok {
		return kind, true
	}
	switch {
	case hasAny(name, looseTextEncoder):
		return v1.TensorGroupKind_TENSOR_GROUP_KIND_TEXT_ENCODER, true
	case hasAny(name, looseAudio):
		return v1.TensorGroupKind_TENSOR_GROUP_KIND_AUDIO, true
	case hasAny(name, looseVAE):
		return v1.TensorGroupKind_TENSOR_GROUP_KIND_VAE, true
	case hasAny(name, looseDenoiser):
		return v1.TensorGroupKind_TENSOR_GROUP_KIND_DIFFUSION, true
	}
	return v1.TensorGroupKind_TENSOR_GROUP_KIND_UNSPECIFIED, false
}

func hasAny(name string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// Checkpoint properties inferred from tensor names and shapes.
type Profile struct {
	// Denoiser family, empty if absent.
	Family string
	// Variant within the family, empty for the base variant.
	Variant string
	// Component kind for checkpoints without a denoiser.
	Component string
	// Bundled components.
	VAE, TextEncoder, ClipVision bool
	// Bundled slot IDs, nil for standalone files.
	Slots map[string]bool
	// Whether the denoiser requires image conditioning.
	ImageInput bool
	// Whether the denoiser requires audio conditioning.
	AudioInput bool
	// Transformer block count and attention projection width.
	Blocks    float64
	Embedding float64
	// Standalone encoder embedding width and vocabulary size.
	Width float64
	Vocab float64
	// Standalone autoencoder latent channels and 3D convolution flag.
	LatentChannels float64
	VideoVAE       bool
	// Tensor bytes by placement kind.
	Kinds map[v1.TensorGroupKind]uint64
}

// Returns ggml ne[i], counting from the innermost dimension. Dimensions beyond four are folded into
// ne[3].
func ne(t *v1.TensorInfo, i int) uint64 {
	shape := t.GetShape()
	n := len(shape)
	if n == 0 {
		return 0
	}
	if i < 3 {
		if i >= n {
			return 1
		}
		return shape[n-1-i]
	}
	if n < 4 {
		return 1
	}
	out := uint64(1)
	for _, d := range shape[:n-3] {
		out *= d
	}
	return out
}

// Strips denoiser wrapper prefixes.
func bare(name string) string {
	for _, p := range denoiserPrefixes {
		if strings.HasPrefix(name, p) {
			return name[len(p):]
		}
	}
	return name
}

// Scan infers checkpoint properties from tensors and the group name.
func Scan(tensors []*v1.TensorInfo, group string) Profile {
	p := Profile{Kinds: map[v1.TensorGroupKind]uint64{}}
	// Detect adapters first because their tensor names include the target denoiser.
	if helper := helperOf(tensors, strings.ToLower(group)); helper != "" {
		p.Component = helper
		for _, t := range tensors {
			p.Kinds[v1.TensorGroupKind_TENSOR_GROUP_KIND_OTHER] += t.GetBytes()
		}
		return p
	}
	names := map[string]bool{}
	maxIndex := map[string]int{}
	var qElements uint64
	for _, t := range tensors {
		name := t.GetName()
		kind, ok := Any(name)
		if !ok {
			kind = v1.TensorGroupKind_TENSOR_GROUP_KIND_OTHER
		}
		p.Kinds[kind] += t.GetBytes()
		if kind != v1.TensorGroupKind_TENSOR_GROUP_KIND_DIFFUSION {
			names[name] = true
			continue
		}
		b := bare(name)
		names[b] = true
		for _, stack := range []string{"double_blocks", "single_blocks", "joint_blocks", "blocks", "transformer_blocks", "single_transformer_blocks", "layers", "double_stream_blocks", "single_stream_blocks", "double_stream_layers", "single_stream_layers", "input_blocks", "output_blocks", "double_layers", "single_layers"} {
			if rest, ok := strings.CutPrefix(b, stack+"."); ok {
				if i, _, ok := strings.Cut(rest, "."); ok {
					if n, err := strconv.Atoi(i); err == nil && n+1 > maxIndex[stack] {
						maxIndex[stack] = n + 1
					}
				}
			}
		}
		// Query projections are square at model width. Fused QKV projections are three times taller.
		if qElements == 0 {
			switch {
			case strings.HasSuffix(b, ".0.self_attn.q.weight"), strings.HasSuffix(b, ".0.attn.to_q.weight"), strings.HasSuffix(b, ".0.attn1.to_q.weight"), strings.HasSuffix(b, ".0.attention.to_q.weight"):
				qElements = t.GetElements()
			case strings.HasSuffix(b, ".0.x_block.attn.qkv.weight"), strings.HasSuffix(b, ".0.img_attn.qkv.weight"), strings.HasSuffix(b, ".0.attention.qkv.weight"):
				qElements = t.GetElements() / 3
			}
		}
	}
	if qElements > 0 {
		p.Embedding = math.Round(math.Sqrt(float64(qElements)))
	}
	p.VAE = p.Kinds[v1.TensorGroupKind_TENSOR_GROUP_KIND_VAE] > 0
	p.TextEncoder = p.Kinds[v1.TensorGroupKind_TENSOR_GROUP_KIND_TEXT_ENCODER] > 0
	p.ClipVision = p.Kinds[v1.TensorGroupKind_TENSOR_GROUP_KIND_VISION] > 0
	if p.Kinds[v1.TensorGroupKind_TENSOR_GROUP_KIND_DIFFUSION] > 0 {
		f := detect(tensors)
		p.Family, p.Variant, p.ImageInput, p.AudioInput = f.family, f.variant, f.imageInput, f.audioInput
		p.Blocks = stackDepth(f.family, maxIndex)
		if p.Family != "" {
			return p
		}
	}
	p.Width, p.Vocab = encoderShape(tensors)
	p.LatentChannels, p.VideoVAE = latentShape(tensors)
	p.Component = component(p.Kinds, names, p.Width, strings.ToLower(group))
	return p
}

// Infers encoder width and vocabulary from token embeddings, or vision width from the class
// embedding or projection.
func encoderShape(tensors []*v1.TensorInfo) (width, vocab float64) {
	for _, t := range tensors {
		name := t.GetName()
		switch {
		case strings.HasSuffix(name, "token_embedding.weight"), strings.HasSuffix(name, "shared.weight"), strings.HasSuffix(name, "embed_tokens.weight"), strings.HasSuffix(name, "wte.weight"):
			if len(t.GetShape()) == 2 {
				return float64(ne(t, 0)), float64(ne(t, 1))
			}
		}
	}
	for _, t := range tensors {
		name := t.GetName()
		switch {
		case strings.HasSuffix(name, "embeddings.class_embedding"):
			return float64(ne(t, 0)), 0
		case strings.HasSuffix(name, "visual_projection.weight"):
			return float64(ne(t, 0)), 0
		}
	}
	return 0, 0
}

// Standalone autoencoder latent channels and 3D convolution flag.
func latentShape(tensors []*v1.TensorInfo) (channels float64, video bool) {
	for _, t := range tensors {
		name := t.GetName()
		for _, suffix := range []string{"decoder.conv_in.weight", "decoder.conv1.weight", "decoder.conv_in.conv.weight", "decoder.conv_in.conv.conv.weight"} {
			if !strings.HasSuffix(name, suffix) {
				continue
			}
			shape := t.GetShape()
			if len(shape) < 4 {
				continue
			}
			return float64(shape[1]), len(shape) == 5
		}
	}
	return 0, false
}

// Counts blocks in the family's denoiser stacks.
func stackDepth(family string, maxIndex map[string]int) float64 {
	switch family {
	case "sd1", "sd2", "sdxl", "svd":
		return float64(maxIndex["input_blocks"] + maxIndex["output_blocks"] + 1)
	case "sd3":
		return float64(maxIndex["joint_blocks"])
	case "flux", "chroma", "chroma_radiance", "flux2", "flux2_klein", "hunyuan_video", "hunyuan_video_15", "ovis_image", "longcat", "sefi_image":
		return float64(maxIndex["double_blocks"] + maxIndex["single_blocks"] + maxIndex["transformer_blocks"] + maxIndex["single_transformer_blocks"])
	case "hidream_o1":
		return float64(maxIndex["double_stream_blocks"] + maxIndex["single_stream_blocks"])
	case "boogu_image":
		return float64(maxIndex["double_stream_layers"] + maxIndex["single_stream_layers"])
	case "z_image", "lumina2", "ernie_image", "ideogram4", "minit2i", "pid":
		return float64(maxIndex["layers"])
	}
	best := 0
	for _, n := range maxIndex {
		if n > best {
			best = n
		}
	}
	return float64(best)
}

// Signature tensor flags used for family detection.
type signals struct {
	family, variant        string
	imageInput, audioInput bool
}

// Detects families using stable-diffusion.cpp's signature order.
func detect(tensors []*v1.TensorInfo) signals {
	has := func(sub string) bool {
		for _, t := range tensors {
			if strings.Contains(t.GetName(), sub) {
				return true
			}
		}
		return false
	}
	find := func(suffixes ...string) *v1.TensorInfo {
		for _, t := range tensors {
			for _, s := range suffixes {
				if t.GetName() == s || strings.HasSuffix(t.GetName(), "."+s) {
					return t
				}
			}
		}
		return nil
	}
	dim := func(t *v1.TensorInfo, i int) uint64 {
		if t == nil {
			return 0
		}
		return ne(t, i)
	}
	tokenEmbedding := find("cond_stage_model.transformer.text_model.embeddings.token_embedding.weight", "cond_stage_model.model.token_embedding.weight", "text_model.embeddings.token_embedding.weight", "te.text_model.embeddings.token_embedding.weight", "conditioner.embedders.0.model.token_embedding.weight", "conditioner.embedders.0.transformer.text_model.embeddings.token_embedding.weight")
	inputBlock := find("input_blocks.0.0.weight", "img_in.weight", "unet.conv_in.weight", "conv_in.weight")
	contextEmbedding := find("txt_in.weight", "context_embedder.weight")
	isFlux := has("double_blocks.") || has("single_transformer_blocks.")
	isFlux2 := has("double_stream_modulation_img.lin.weight")
	isWan := has("blocks.0.cross_attn.norm_k.weight")
	isS2V := has("casual_audio_encoder.weights") || has("audio_injector.injector.0.q.weight")
	isUnet := has("input_blocks.") || has("unet.down_blocks.")
	multipleEncoders := has("conditioner.embedders.1") || has("cond_stage_model.1") || has("te.1")
	isXL := isUnet && multipleEncoders
	hasImgEmb := has("img_emb")
	hasMiddleBlock1 := has("middle_block.1.") || has("unet.mid_block.resnets.1.")
	hasOutputBlock311 := has("output_blocks.3.1.transformer_blocks.1") || has("unet.up_blocks.1.attentions.0.transformer_blocks.1")
	hasOutputBlock71 := has("output_blocks.7.1") || has("unet.up_blocks.2.attentions.1")
	hasAttn1024 := dim(find("output_blocks.7.1.transformer_blocks.0.attn1.to_k.weight"), 0) == 1024
	patchChannels := dim(find("patch_embedding.weight"), 3)
	isInpaint := dim(inputBlock, 2) == 9
	isIP2P := dim(inputBlock, 2) == 8
	switch {
	case has("net.lq_proj.latent_proj.0.weight"):
		return signals{family: "pid", imageInput: true}
	case has("embed_image_indicator.weight"):
		return signals{family: "ideogram4"}
	case has("txtfusion.projector.weight") || has("text_fusion.projector.weight"):
		return signals{family: "krea2"}
	case has("nerf_final_layer_conv."):
		return signals{family: "chroma_radiance"}
	case has("joint_blocks."):
		return signals{family: "sd3"}
	case has("x_embedder.proj1.weight") && has("language_model.layers.0.self_attn.q_proj.weight"):
		return signals{family: "hidream_o1"}
	case has("transformer_blocks.0.attn.norm_added_q.weight") && has("transformer_blocks.0.img_mlp.w1.weight"):
		return signals{family: "lens"}
	case has("net.img_embedder.proj1.weight"):
		return signals{family: "minit2i"}
	case has("language_model.model.layers.0.self_attn.q_proj_mot_gen.weight"):
		return signals{family: "sensenova_u1"}
	case has("txt_in.text_norm.weight"):
		return signals{family: "qwen_image21", imageInput: true}
	case has("transformer_blocks.0.img_mod.1.weight"):
		if dim(find("img_in.weight"), 0) == 128 {
			return signals{family: "mage_flow", imageInput: true}
		}
		if has("time_text_embed.addition_t_embedding.weight") {
			return signals{family: "qwen_image", variant: "layered"}
		}
		return signals{family: "qwen_image", imageInput: true}
	case has("txt_in.individual_token_refiner.blocks.0.adaLN_modulation.1.weight"):
		// HunyuanVideo uses 4096-wide LLaVA-llama-3 states. Version 1.5 uses 3584-wide Qwen2.5-VL states.
		if dim(find("txt_in.input_embedder.weight"), 0) == 4096 {
			return signals{family: "hunyuan_video", imageInput: true}
		}
		return signals{family: "hunyuan_video_15", imageInput: true}
	case has("llm_adapter.blocks.0.cross_attn.q_proj.weight"):
		return signals{family: "anima"}
	case has("dual_time_embed.semantic_embedder.linear_1.weight"):
		return signals{family: "sefi_image"}
	case has("double_blocks.0.img_mlp.gate_proj.weight"):
		return signals{family: "ovis_image"}
	case has("cap_embedder.0.weight"):
		// Lumina-Image 2.0 uses 2304-wide Gemma-2-2B captions. Z-Image uses 2560-wide Qwen3-4B captions.
		if dim(find("cap_embedder.0.weight"), 0) == 2304 {
			return signals{family: "lumina2"}
		}
		return signals{family: "z_image"}
	case has("double_stream_layers.0.img_instruct_attn.processor.img_to_q.weight"):
		return signals{family: "boogu_image", imageInput: true}
	case has("layers.0.adaLN_sa_ln.weight"):
		return signals{family: "ernie_image"}
	case has("adaln_single.emb.timestep_embedder.linear_1.bias"):
		return signals{family: "ltx2", imageInput: true}
	case has("video_patch_proj.weight") && has("audio_patch_proj.weight"):
		return signals{family: "minimax_h3", imageInput: true, audioInput: true}
	case has("patch_embedder.weight") && !isUnet:
		return signals{family: "lingbot_video", imageInput: true}
	case isWan:
		switch {
		case isS2V:
			return signals{family: "wan", variant: "s2v", imageInput: true, audioInput: true}
		case patchChannels == 184320 && !hasImgEmb:
			return signals{family: "wan", variant: "i2v", imageInput: true}
		case patchChannels == 147456 && !hasImgEmb:
			return signals{family: "wan", variant: "ti2v", imageInput: true}
		case has("vace_blocks.") || has("vace_patch_embedding."):
			return signals{family: "wan", variant: "vace", imageInput: true}
		}
		return signals{family: "wan", imageInput: hasImgEmb}
	case has("input_blocks.8.0.time_mixer.mix_factor"):
		return signals{family: "svd", imageInput: true}
	case isXL:
		switch {
		case isInpaint:
			return signals{family: "sdxl", variant: "inpaint", imageInput: true}
		case isIP2P:
			return signals{family: "sdxl", variant: "pix2pix", imageInput: true}
		case !hasMiddleBlock1 && !hasOutputBlock311:
			return signals{family: "sdxl", variant: "vega"}
		case !hasMiddleBlock1:
			return signals{family: "sdxl", variant: "ssd1b"}
		}
		return signals{family: "sdxl"}
	case isFlux && !isFlux2:
		if dim(contextEmbedding, 0) == 3584 {
			return signals{family: "longcat", imageInput: true}
		}
		if has("distilled_guidance_layer.") {
			return signals{family: "chroma"}
		}
		switch dim(inputBlock, 0) {
		case 384:
			return signals{family: "flux", variant: "fill", imageInput: true}
		case 128:
			return signals{family: "flux", variant: "controls", imageInput: true}
		case 196:
			return signals{family: "flux", variant: "flex2", imageInput: true}
		}
		return signals{family: "flux", imageInput: true}
	case isFlux2:
		if has("single_blocks.47.linear1.weight") {
			return signals{family: "flux2", imageInput: true}
		}
		return signals{family: "flux2_klein", imageInput: true}
	case dim(tokenEmbedding, 0) == 768 && isUnet:
		switch {
		case isInpaint:
			return signals{family: "sd1", variant: "inpaint", imageInput: true}
		case isIP2P:
			return signals{family: "sd1", variant: "pix2pix", imageInput: true}
		case !hasMiddleBlock1 && !hasOutputBlock71:
			return signals{family: "sd1", variant: "sdxs"}
		case !hasMiddleBlock1:
			return signals{family: "sd1", variant: "tiny_unet"}
		}
		return signals{family: "sd1"}
	case dim(tokenEmbedding, 0) == 1024 && isUnet:
		switch {
		case isInpaint:
			return signals{family: "sd2", variant: "inpaint", imageInput: true}
		case !hasMiddleBlock1 && hasAttn1024:
			return signals{family: "sd2", variant: "sdxs"}
		case !hasMiddleBlock1:
			return signals{family: "sd2", variant: "tiny_unet"}
		}
		return signals{family: "sd2"}
	case isUnet:
		// Cross-attention width identifies the family of a standalone UNet.
		switch dim(find("input_blocks.1.1.transformer_blocks.0.attn2.to_k.weight", "down_blocks.0.attentions.0.transformer_blocks.0.attn2.to_k.weight"), 0) {
		case 2048:
			return signals{family: "sdxl"}
		case 1024:
			return signals{family: "sd2"}
		}
		return signals{family: "sd1"}
	}
	return signals{}
}

// Detects LoRA pairs, ControlNet input blocks, and adapter projections.
func helperOf(tensors []*v1.TensorInfo, group string) string {
	for _, t := range tensors {
		name := t.GetName()
		switch {
		case loraSegment(name), strings.HasPrefix(name, "lora_unet_"), strings.HasPrefix(name, "lora_te"), strings.HasPrefix(name, "lora_transformer_"), strings.Contains(name, ".hada_w1_"), strings.Contains(name, ".dora_scale"):
			return "lora"
		case strings.HasPrefix(name, "control_model."), strings.HasPrefix(name, "controlnet_"), strings.HasPrefix(name, "controlnet."):
			return "controlnet"
		case strings.HasPrefix(name, "image_proj."), strings.HasPrefix(name, "ip_adapter."):
			return "ip_adapter"
		case strings.HasPrefix(name, "id_encoder."), strings.HasPrefix(name, "pmid."):
			return "photo_maker"
		case strings.HasPrefix(name, "pulid_ca."), strings.HasPrefix(name, "pulid_encoder"):
			return "pulid"
		case strings.Contains(name, "motion_modules."), strings.Contains(name, "temporal_transformer."):
			return "motion_module"
		case strings.HasPrefix(name, "string_to_param"), strings.HasPrefix(name, "emb_params"):
			return "embedding"
		}
	}
	if len(tensors) > 0 && len(tensors) <= 4 && flat(tensors) && (strings.Contains(group, "embedding") || strings.Contains(group, "textual")) {
		return "embedding"
	}
	return ""
}

// LoRA tensor segment names used by PEFT, kohya, and diffusers.
var loraSegments = []string{"lora_a", "lora_b", "lora_up", "lora_down"}

// Matches LoRA tensor segments without case sensitivity.
func loraSegment(name string) bool {
	for _, seg := range strings.Split(name, ".") {
		lower := strings.ToLower(seg)
		for _, s := range loraSegments {
			if lower == s {
				return true
			}
		}
	}
	return false
}

// Textual inversions contain bare vectors such as clip_l and clip_g.
func flat(tensors []*v1.TensorInfo) bool {
	for _, t := range tensors {
		if strings.Contains(t.GetName(), ".") {
			return false
		}
	}
	return true
}

// Identifies standalone autoencoders, encoders, upscalers, and projectors.
func component(kinds map[v1.TensorGroupKind]uint64, names map[string]bool, width float64, group string) string {
	switch {
	case named(names, "conv_first.", "upconv1.", "conv_body.", "body.", "model.0.", "model.1.sub."):
		return "upscaler"
	case kinds[v1.TensorGroupKind_TENSOR_GROUP_KIND_TEXT_ENCODER] > 0:
		switch {
		case named(names, "encoder.block.", "shared.", "text_encoders.t5xxl."):
			return "t5"
		case named(names, "model.layers.", "model.language_model.", "language_model.", "text_encoders.llm.", "model.embed_tokens."):
			return "llm"
		case named(names, "transformer.resblocks.", "clip_g."):
			return "clip_g"
		case named(names, "text_model.", "clip_l.", "cond_stage_model.", "conditioner.", "te."):
			switch {
			case width == 1280 || width == 0 && clipG(names):
				return "clip_g"
			case width == 1024:
				return "clip_h"
			}
			return "clip_l"
		}
		return "text_encoder"
	case kinds[v1.TensorGroupKind_TENSOR_GROUP_KIND_VISION] > 0:
		return "clip_vision"
	case kinds[v1.TensorGroupKind_TENSOR_GROUP_KIND_AUDIO] > 0 && named(names, "decoder."):
		// An encoder with a decoder identifies an audio autoencoder.
		return "audio_vae"
	case kinds[v1.TensorGroupKind_TENSOR_GROUP_KIND_AUDIO] > 0:
		return "audio_encoder"
	case kinds[v1.TensorGroupKind_TENSOR_GROUP_KIND_VAE] > 0:
		switch {
		case strings.Contains(group, "audio"):
			return "audio_vae"
		case strings.Contains(group, "tae") || named(names, "tae."):
			return "taesd"
		}
		return "vae"
	case strings.Contains(group, "connector") || named(names, "connectors.", "text_embedding_projection.", "embeddings_connectors."):
		return "embeddings_connectors"
	case strings.Contains(group, "upscaler") || strings.Contains(group, "upsampler"):
		return "upscaler"
	case strings.Contains(group, "yolo") || strings.Contains(group, "adetailer") || strings.Contains(group, "detector"):
		return "detector"
	}
	return ""
}

func named(names map[string]bool, prefixes ...string) bool {
	for n := range names {
		for _, p := range prefixes {
			if strings.HasPrefix(n, p) {
				return true
			}
		}
	}
	return false
}

// CLIP-G has a projection and a 1280-wide embedding. Fall back to names when shapes are
// unavailable.
func clipG(names map[string]bool) bool {
	return names["text_projection.weight"] || names["text_model.text_projection.weight"] || names["text_projection"]
}

// Video families and their support for still images.
var video = map[string]bool{"wan": true, "hunyuan_video": false, "hunyuan_video_15": false, "ltxv": false, "ltx2": false, "minimax_h3": false, "lingbot_video": false, "svd": false, "cogvideox": false, "mochi": false, "open_sora": false, "allegro": false, "step_video": false, "magi": false, "pyramid_flow": false, "easyanimate": false, "ovi": false, "cosmos": false}

// Generates returns the family's output types: image, video, or both.
func Generates(family string) []string {
	if family == "" {
		return nil
	}
	if still, isVideo := video[Canonical(family)]; isVideo {
		if still {
			return []string{"image", "video"}
		}
		return []string{"video"}
	}
	return []string{"image"}
}

// Modes returns supported server modes: img_gen and vid_gen.
func Modes(family string) []string {
	var out []string
	for _, g := range Generates(family) {
		switch g {
		case "image":
			out = append(out, "img_gen")
		case "video":
			out = append(out, "vid_gen")
		}
	}
	return out
}

// Architecture aliases from GGUF, diffusers, and stable-diffusion.cpp.
var canonical = map[string]string{
	"sd1": "sd1", "sd1.5": "sd1", "sd15": "sd1", "sd1_inpaint": "sd1", "sd1_pix2pix": "sd1", "sd1_tiny_unet": "sd1", "sdxs_512_ds": "sd1", "unet2dconditionmodel": "sd1", "stable-diffusion": "sd1",
	"sd2": "sd2", "sd2.1": "sd2", "sd2_inpaint": "sd2", "sd2_tiny_unet": "sd2", "sdxs_09": "sd2",
	"sdxl": "sdxl", "sdxl-turbo": "sdxl", "sdxl_inpaint": "sdxl", "sdxl_pix2pix": "sdxl", "sdxl_vega": "sdxl", "sdxl_ssd1b": "sdxl", "ssd1b": "sdxl", "vega": "sdxl",
	"svd": "svd", "stable-video-diffusion": "svd",
	"sd3": "sd3", "sd3.5": "sd3", "sd35": "sd3", "sd3transformer2dmodel": "sd3", "mmdit": "sd3",
	"flux": "flux", "flux1": "flux", "flux.1": "flux", "flux_fill": "flux", "flux_controls": "flux", "flex_2": "flux", "flex2": "flux", "fluxtransformer2dmodel": "flux", "kontext": "flux",
	"chroma": "chroma", "chromatransformer2dmodel": "chroma",
	"chroma_radiance": "chroma_radiance", "chroma-radiance": "chroma_radiance", "chroma1-radiance": "chroma_radiance",
	"flux2": "flux2", "flux.2": "flux2", "flux2transformer2dmodel": "flux2",
	"flux2_klein": "flux2_klein", "flux2-klein": "flux2_klein", "klein": "flux2_klein",
	"wan": "wan", "wan2": "wan", "wan2.1": "wan", "wan2.2": "wan", "wan2_2_i2v": "wan", "wan2_2_ti2v": "wan", "wan2_2_s2v": "wan", "wantransformer3dmodel": "wan", "wanvacetransformer3dmodel": "wan", "vace": "wan",
	"lingbot_video": "lingbot_video", "lingbot": "lingbot_video", "lingbot-video": "lingbot_video",
	"qwen_image": "qwen_image", "qwenimage": "qwen_image", "qwen-image": "qwen_image", "qwen_image_layered": "qwen_image", "qwenimagetransformer2dmodel": "qwen_image", "qwen_image_edit": "qwen_image",
	"qwen_image21": "qwen_image21", "qwen_image_21": "qwen_image21", "qwen_image_2.1": "qwen_image21", "qwen_image_2_1": "qwen_image21", "qwen-image-2.1": "qwen_image21", "qwen_image2.1": "qwen_image21", "qwenimage21": "qwen_image21", "qwenimage2.1": "qwen_image21", "qwenimage21transformer2dmodel": "qwen_image21",
	"hunyuan_video": "hunyuan_video", "hyvid": "hunyuan_video", "hunyuanvideo": "hunyuan_video", "hunyuanvideotransformer3dmodel": "hunyuan_video", "skyreels_v1": "hunyuan_video",
	"hunyuan_video_15": "hunyuan_video_15", "hunyuan_video_1.5": "hunyuan_video_15", "hunyuanvideo1.5": "hunyuan_video_15", "hunyuanvideo-1.5": "hunyuan_video_15", "hunyuanvideo_1.5": "hunyuan_video_15", "hunyuanvideo15": "hunyuan_video_15", "hunyuanvideo15transformer3dmodel": "hunyuan_video_15",
	"anima": "anima", "anima2": "anima",
	"ltx2": "ltx2", "ltxav": "ltx2", "ltx-2": "ltx2", "ltx2.3": "ltx2", "ltx-2.3": "ltx2", "ltx2.5": "ltx2", "ltx-2.5": "ltx2", "ltx": "ltx2", "ltxav_transformer": "ltx2",
	"ltxv": "ltxv", "ltx-video": "ltxv", "ltx_video": "ltxv", "ltxvideo": "ltxv", "ltxvideotransformer3dmodel": "ltxv",
	"minimax_h3": "minimax_h3", "minimax-h3": "minimax_h3", "minimaxh3": "minimax_h3",
	"hidream_o1": "hidream_o1", "hidream-o1": "hidream_o1",
	"hidream_i1": "hidream_i1", "hidream-i1": "hidream_i1", "hidream_e1": "hidream_i1", "hidream-e1": "hidream_i1", "hidream": "hidream_i1", "hidreamimagetransformer2dmodel": "hidream_i1",
	"stablediffusion": "sd1", "stablediffusioninpaint": "sd1", "stablediffusion2": "sd2", "stablediffusionxl": "sdxl", "stablediffusionxlinpaint": "sdxl", "stablediffusion3": "sd3", "stablediffusion35": "sd3",
	"hidreamimage": "hidream_i1", "kandinskyv22": "kandinsky22", "wanvideo": "wan", "ltxcondition": "ltxv",
	"z_image": "z_image", "zimage": "z_image", "z-image": "z_image",
	"lumina2": "lumina2", "lumina": "lumina2", "lumina-image-2.0": "lumina2", "lumina_image_2.0": "lumina2", "lumina-image-2": "lumina2", "lumina2transformer2dmodel": "lumina2",
	"kolors": "kolors", "kwai-kolors": "kolors",
	"sana": "sana", "sana1.5": "sana", "sana_1.5": "sana", "sanatransformer2dmodel": "sana",
	"pixart": "pixart", "pixart-alpha": "pixart", "pixart_alpha": "pixart", "pixart-sigma": "pixart", "pixart_sigma": "pixart", "pixartalpha": "pixart", "pixartsigma": "pixart", "pixarttransformer2dmodel": "pixart",
	"hunyuan_dit": "hunyuan_dit", "hunyuan-dit": "hunyuan_dit", "hunyuandit": "hunyuan_dit", "hunyuandit2dmodel": "hunyuan_dit",
	"cogview4": "cogview4", "cogview3": "cogview4", "cogview": "cogview4", "cogview4transformer2dmodel": "cogview4", "cogview3plustransformer2dmodel": "cogview4",
	"kandinsky22": "kandinsky22", "kandinsky-2-2": "kandinsky22", "kandinsky2.2": "kandinsky22", "kandinsky_2_2": "kandinsky22",
	"kandinsky3": "kandinsky3", "kandinsky-3": "kandinsky3", "kandinsky_3": "kandinsky3", "kandinsky3unet": "kandinsky3",
	"deepfloyd": "deepfloyd", "deepfloyd-if": "deepfloyd", "if-i": "deepfloyd", "if_i": "deepfloyd",
	"stable_cascade": "stable_cascade", "stable-cascade": "stable_cascade", "stablecascade": "stable_cascade", "wurstchen": "stable_cascade", "würstchen": "stable_cascade", "stablecascadeunet": "stable_cascade",
	"playground": "sdxl", "playground-v2.5": "sdxl", "playground_v2.5": "sdxl",
	"cogvideox": "cogvideox", "cogvideo": "cogvideox", "cogvideox1.5": "cogvideox", "cogvideoxtransformer3dmodel": "cogvideox",
	"mochi": "mochi", "mochi-1": "mochi", "mochi_1": "mochi", "mochitransformer3dmodel": "mochi",
	"open_sora": "open_sora", "open-sora": "open_sora", "opensora": "open_sora",
	"allegro": "allegro", "allegrotransformer3dmodel": "allegro",
	"step_video": "step_video", "step-video": "step_video", "stepvideo": "step_video",
	"magi": "magi", "magi-1": "magi", "magi_1": "magi",
	"pyramid_flow": "pyramid_flow", "pyramid-flow": "pyramid_flow", "pyramidflow": "pyramid_flow",
	"easyanimate": "easyanimate", "easyanimatetransformer3dmodel": "easyanimate",
	"ovi":    "ovi",
	"cosmos": "cosmos", "cosmos-predict": "cosmos", "cosmos_predict": "cosmos", "cosmostransformer3dmodel": "cosmos",
	"skyreels_v2": "wan", "skyreels-v2": "wan", "skyreels_a2": "wan",
	"boogu_image": "boogu_image", "boogu": "boogu_image", "boogu-image": "boogu_image",
	"ovis_image": "ovis_image", "ovis": "ovis_image", "ovis-image": "ovis_image",
	"ernie_image": "ernie_image", "ernie": "ernie_image", "ernie-image": "ernie_image",
	"lens": "lens", "lens-turbo": "lens",
	"minit2i": "minit2i",
	"longcat": "longcat", "longcat_image": "longcat", "longcat-image": "longcat",
	"pid": "pid", "pixeldit": "pid", "pid1.5": "pid",
	"ideogram4": "ideogram4", "ideogram-4": "ideogram4", "ideogram": "ideogram4",
	"sefi_image": "sefi_image", "sefi": "sefi_image", "sefi-image": "sefi_image",
	"krea2": "krea2", "krea-2": "krea2", "krea_2": "krea2",
	"mage_flow": "mage_flow", "mage-flow": "mage_flow", "mageflow": "mage_flow", "mage": "mage_flow",
	"sensenova_u1": "sensenova_u1", "sensenova_u1_5": "sensenova_u1", "sensenova-u1.5": "sensenova_u1", "sensenova": "sensenova_u1",
	"t5encoder": "t5", "t5": "t5", "umt5": "t5", "t5encodermodel": "t5", "umt5encodermodel": "t5", "byt5": "t5", "flan-t5": "t5",
	"cliptextmodel": "clip_l", "clip_l": "clip_l", "clip-l": "clip_l",
	"cliptextmodelwithprojection": "clip_g", "clip_g": "clip_g", "clip-g": "clip_g",
	"autoencoderkl": "vae", "autoencoderklwan": "vae", "autoencoderklqwenimage": "vae", "autoencoderklqwenimage21": "vae", "autoencoderklltxvideo": "vae", "autoencoderklhunyuanvideo": "vae", "autoencoderklcosmos": "vae", "autoencoderklflux2": "vae", "autoencoderklmagvit": "vae", "vae": "vae",
	"audio_vae": "audio_vae", "autoencoderklltxaudio": "audio_vae",
	"autoencodertiny": "taesd", "taesd": "taesd", "taehv": "taesd", "tae": "taesd",
	"clipvisionmodelwithprojection": "clip_vision", "clipvisionmodel": "clip_vision", "siglipvisionmodel": "clip_vision", "clip_vision": "clip_vision", "clip-vision": "clip_vision",
	"wav2vec2model": "audio_encoder", "wav2vec2": "audio_encoder", "audio_encoder": "audio_encoder",
	"embeddings_connectors": "embeddings_connectors", "connectors": "embeddings_connectors",
	"lora": "lora", "controlnet": "controlnet", "controlnetmodel": "controlnet", "ip_adapter": "ip_adapter", "ip-adapter": "ip_adapter", "photo_maker": "photo_maker", "photomaker": "photo_maker", "pulid": "pulid", "motion_module": "motion_module", "motionadapter": "motion_module", "embedding": "embedding", "textual_inversion": "embedding",
	"upscaler": "upscaler", "esrgan": "upscaler", "realesrgan": "upscaler", "detector": "detector", "yolo": "detector",
	"clip_h": "clip_h", "clip-h": "clip_h",
	"tokenizer": "tokenizer",
	"llm":       "llm", "text_encoder": "text_encoder",
}

// Canonical resolves architecture aliases and recognized class names. Unknown names pass through
// unchanged.
func Canonical(architecture string) string {
	a := strings.ToLower(strings.TrimSpace(architecture))
	if c, ok := canonical[a]; ok {
		return c
	}
	if c := classNamed(a); c != "" {
		return c
	}
	return a
}

// Class suffixes, longest first.
var classSuffixes = []string{"modularpipeline", "pipeline", "transformer3dmodel", "transformer2dmodel", "unet3dconditionmodel", "unet2dconditionmodel", "ditmodel", "3dmodel", "2dmodel", "model"}

// Infers a family or component kind from a class name, or returns empty.
func classNamed(a string) string {
	for _, suffix := range classSuffixes {
		if stem, ok := strings.CutSuffix(a, suffix); ok && stem != "" {
			if c, known := canonical[stem]; known {
				return c
			}
		}
	}
	switch {
	case strings.Contains(a, "autoencoder") || strings.Contains(a, "vae"):
		switch {
		case strings.Contains(a, "audio"):
			return "audio_vae"
		case strings.Contains(a, "tiny"):
			return "taesd"
		}
		return "vae"
	case strings.Contains(a, "textmodelwithprojection"):
		return "clip_g"
	case strings.Contains(a, "cliptextmodel"):
		return "clip_l"
	case strings.Contains(a, "t5") && strings.Contains(a, "encoder"):
		return "t5"
	case strings.Contains(a, "visionmodel") || strings.Contains(a, "siglip") || strings.Contains(a, "clipvision"):
		return "clip_vision"
	case strings.Contains(a, "wav2vec2") || strings.Contains(a, "hubert") || strings.Contains(a, "audioencoder"):
		return "audio_encoder"
	case strings.Contains(a, "forconditionalgeneration") || strings.Contains(a, "forcausallm") || strings.HasSuffix(a, "encoder"):
		return "llm"
	}
	return ""
}

// Canonical denoiser family IDs.
var denoisers = func() []string {
	out := []string{
		"sd1", "sd2", "sdxl", "kolors", "sd3", "flux", "chroma", "chroma_radiance", "flux2", "flux2_klein", "hidream_i1", "hidream_o1",
		"qwen_image", "qwen_image21", "z_image", "lumina2", "sana", "pixart", "hunyuan_dit", "cogview4", "kandinsky22", "kandinsky3", "deepfloyd", "stable_cascade",
		"ovis_image", "longcat", "krea2", "ernie_image", "anima", "boogu_image", "mage_flow", "ideogram4", "sefi_image", "lens", "pid", "minit2i", "sensenova_u1",
		"svd", "wan", "hunyuan_video", "hunyuan_video_15", "ltxv", "ltx2", "minimax_h3", "lingbot_video", "cogvideox", "mochi", "open_sora", "allegro",
		"step_video", "magi", "pyramid_flow", "easyanimate", "ovi", "cosmos",
	}
	sort.Strings(out)
	return out
}()

// Denoiser reports whether an architecture resolves to a denoiser family.
func Denoiser(architecture string) bool {
	c := Canonical(architecture)
	for _, f := range denoisers {
		if f == c {
			return true
		}
	}
	return false
}

// Families returns supported denoiser family IDs.
func Families() []string { return append([]string(nil), denoisers...) }

// Sorted canonical names of denoiser families
func Spellings() []string {
	out := make([]string, 0, len(canonical))
	for spelling, family := range canonical {
		if Denoiser(family) {
			out = append(out, spelling)
		}
	}
	sort.Strings(out)
	return out
}

// Component descriptions by runtime parameter.
var parts = map[string]string{
	"vae":                   "a VAE",
	"audio_vae":             "an audio VAE",
	"taesd":                 "a tiny autoencoder",
	"t5":                    "a T5 text encoder",
	"clip_l":                "a CLIP-L text encoder",
	"clip_g":                "a CLIP-G text encoder",
	"clip_h":                "a CLIP-H text encoder",
	"llm":                   "a language model text encoder",
	"tokenizer":             "a tokenizer",
	"text_encoder":          "a text encoder",
	"clip_vision":           "a CLIP vision encoder",
	"audio_encoder":         "an audio encoder",
	"embeddings_connectors": "the embeddings connectors of an LTX-2.3 text encoder",
	"lora":                  "a LoRA",
	"controlnet":            "a ControlNet",
	"ip_adapter":            "an IP-Adapter",
	"photo_maker":           "a PhotoMaker model",
	"pulid":                 "PuLID weights",
	"motion_module":         "an AnimateDiff motion module",
	"embedding":             "a textual inversion embedding",
	"upscaler":              "an upscaler",
	"detector":              "an ADetailer detector",
}

// Component reports whether an architecture is a standalone pipeline component.
func Component(architecture string) bool {
	_, ok := parts[Canonical(architecture)]
	return ok
}

// Describe returns a component label, defaulting to pipeline part.
func Describe(architecture string) string {
	if w, ok := parts[Canonical(architecture)]; ok {
		return w
	}
	return "a pipeline part"
}
