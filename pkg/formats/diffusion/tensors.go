package diffusion

import (
	"math"
	"sort"
	"strconv"
	"strings"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// Tensor names across the checkpoints stable-diffusion.cpp loads: the original layouts single files
// and ComfyUI split files keep, and the diffusers layouts. Every name lands in the part of the
// pipeline it belongs to, the denoiser, the autoencoder, or a text encoder, so a descriptor sizes
// each part and a launch knows which parts a checkpoint bundles. The family of a denoiser is read
// the way stable-diffusion.cpp reads it in ModelLoader::get_sd_version, the same signature tensors
// and the same shapes, so nebu and the runtime agree on what a file is.

// The prefixes stable-diffusion.cpp gives a denoiser it loads, and the ones original checkpoints carry
var denoiserPrefixes = []string{"model.diffusion_model.uncond.", "model.diffusion_model.", "model.high_noise_diffusion_model.", "diffusion_model.", "unet.", "transformer."}

// Names only a diffusion pipeline uses, safe to claim in any format: the denoiser, the autoencoder,
// and the text encoders as single file checkpoints and ComfyUI split files name them
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

// Names a checkpoint without a config may hold that language models name their own tensors by too,
// claimed only where no language model can be: the stacks of Wan, Lumina, and Cosmos, a T5 or a
// language model standing alone as a text encoder, and an autoencoder's own encoder and decoder
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

// Kind is the part of a diffusion pipeline a tensor loads into by a name only such a pipeline uses,
// false for any other name; a format that also reads language models asks this before its own rules
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

// Any is Kind with the names a checkpoint without a config may hold as well, for a file that is
// never a language model, a lone checkpoint or a GGUF that kept its checkpoint's names
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

// What a checkpoint holds, read once from its tensor names and shapes
type Profile struct {
	// The family the denoiser belongs to, empty without a denoiser
	Family string
	// The finer version stable-diffusion.cpp tells apart within the family, empty for the plain one
	Variant string
	// The part a checkpoint without a denoiser is, empty when it has one
	Component string
	// Whether the checkpoint bundles each part beside its denoiser
	VAE, TextEncoder, ClipVision bool
	// Whether the denoiser conditions on an image, so an image to video run needs an image encoder
	ImageInput bool
	// Whether the denoiser conditions on audio, so a run needs an audio encoder
	AudioInput bool
	// Transformer blocks counted, and the width of a block's attention projection
	Blocks    float64
	Embedding float64
	// The tensors summed by the kind they load into
	Kinds map[v1.TensorGroupKind]uint64
}

// The dimension stable-diffusion.cpp calls ne[i]: the innermost dimension is ne[0], and every
// dimension past the fourth folds into ne[3], the way ggml holds a tensor of more than four
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

// The name without the container a loader or a checkpoint put around the denoiser
func bare(name string) string {
	for _, p := range denoiserPrefixes {
		if strings.HasPrefix(name, p) {
			return name[len(p):]
		}
	}
	return name
}

// Scan reads the profile of a checkpoint from its tensors, the group name lending the words its tensors cannot
func Scan(tensors []*v1.TensorInfo, group string) Profile {
	p := Profile{Kinds: map[v1.TensorGroupKind]uint64{}}
	// A LoRA, a control net, or an adapter names the denoiser it patches, so it is told first from what it adds
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
		// The first block's query projection is square at the model width, a fused qkv three widths tall
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
	p.Component = component(p.Kinds, names, tokenWidth(tensors), strings.ToLower(group))
	return p
}

// How many blocks a family stacks, from the stacks its denoiser counts in
func stackDepth(family string, maxIndex map[string]int) float64 {
	switch family {
	case "sd1", "sd2", "sdxl", "svd":
		return float64(maxIndex["input_blocks"] + maxIndex["output_blocks"] + 1)
	case "sd3":
		return float64(maxIndex["joint_blocks"])
	case "flux", "chroma", "chroma_radiance", "flux2", "flux2_klein", "hunyuan_video", "ovis_image", "longcat", "sefi_image":
		return float64(maxIndex["double_blocks"] + maxIndex["single_blocks"] + maxIndex["transformer_blocks"] + maxIndex["single_transformer_blocks"])
	case "hidream_o1":
		return float64(maxIndex["double_stream_blocks"] + maxIndex["single_stream_blocks"])
	case "boogu_image":
		return float64(maxIndex["double_stream_layers"] + maxIndex["single_stream_layers"])
	case "z_image", "ernie_image", "ideogram4", "minit2i", "pid":
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

// What the signature tensors said, gathered over every name the way stable-diffusion.cpp gathers them
type signals struct {
	family, variant        string
	imageInput, audioInput bool
}

// Reads the family off the signature tensors and shapes stable-diffusion.cpp reads them off, in the same order
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
	case has("transformer_blocks.0.img_mod.1.weight"):
		if dim(find("img_in.weight"), 0) == 128 {
			return signals{family: "mage_flow", imageInput: true}
		}
		if has("time_text_embed.addition_t_embedding.weight") {
			return signals{family: "qwen_image", variant: "layered"}
		}
		return signals{family: "qwen_image", imageInput: true}
	case has("txt_in.individual_token_refiner.blocks.0.adaLN_modulation.1.weight"):
		return signals{family: "hunyuan_video", imageInput: true}
	case has("llm_adapter.blocks.0.cross_attn.q_proj.weight"):
		return signals{family: "anima"}
	case has("dual_time_embed.semantic_embedder.linear_1.weight"):
		return signals{family: "sefi_image"}
	case has("double_blocks.0.img_mlp.gate_proj.weight"):
		return signals{family: "ovis_image"}
	case has("cap_embedder.0.weight"):
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
		// A UNet split from its text encoders: the width of its cross attention says which family trained it
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

// A file that patches or steers a denoiser rather than being one: a LoRA by its low rank pairs, a
// control net by its own copy of the input blocks, an adapter by the projector it adds; empty for anything else
func helperOf(tensors []*v1.TensorInfo, group string) string {
	for _, t := range tensors {
		name := t.GetName()
		switch {
		case strings.Contains(name, ".lora_A."), strings.Contains(name, ".lora_B."), strings.Contains(name, ".lora_up."), strings.Contains(name, ".lora_down."), strings.HasPrefix(name, "lora_unet_"), strings.HasPrefix(name, "lora_te"), strings.HasPrefix(name, "lora_transformer_"), strings.Contains(name, ".hada_w1_"), strings.Contains(name, ".dora_scale"):
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

// Whether every tensor is a bare vector rather than a module's weight, the way a textual inversion names clip_l and clip_g
func flat(tensors []*v1.TensorInfo) bool {
	for _, t := range tensors {
		if strings.Contains(t.GetName(), ".") {
			return false
		}
	}
	return true
}

// The part a checkpoint without a denoiser is: an autoencoder, a text or image encoder, an upscaler, or a projector
func component(kinds map[v1.TensorGroupKind]uint64, names map[string]bool, width uint64, group string) string {
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
			if width == 1280 || width == 0 && clipG(names) {
				return "clip_g"
			}
			return "clip_l"
		}
		return "text_encoder"
	case kinds[v1.TensorGroupKind_TENSOR_GROUP_KIND_VISION] > 0:
		return "clip_vision"
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

// CLIP-G keeps a projection and a 1280 wide embedding, CLIP-L neither; a name alone tells them apart when the shape is gone
func clipG(names map[string]bool) bool {
	return names["text_projection.weight"] || names["text_model.text_projection.weight"] || names["text_projection"]
}

// The width of a CLIP text encoder's token embedding, 768 for CLIP-L and 1280 for CLIP-G, zero without one
func tokenWidth(tensors []*v1.TensorInfo) uint64 {
	for _, t := range tensors {
		if strings.HasSuffix(t.GetName(), "token_embedding.weight") {
			return ne(t, 0)
		}
		if strings.HasSuffix(t.GetName(), "text_model.embeddings.position_embedding.weight") {
			return ne(t, 0)
		}
	}
	return 0
}

// One file a family loads beside its denoiser, by the run param naming it
type Part struct {
	// The param the file is named by, vae, t5xxl, clip_l, clip_g, llm, llm_vision, clip_vision, audio_encoder, audio_vae, high_noise_model, uncond_model, embeddings_connectors, tokenizer
	Param string
	// Whether a run cannot start without it
	Required bool
}

// The parts each family loads beside a standalone denoiser, as stable-diffusion.cpp builds each family's
// pipeline and its docs list the files: a checkpoint bundling a part drops the need for it
var needs = map[string][]Part{
	"sd1":             {{"vae", false}, {"clip_l", true}},
	"sd2":             {{"vae", false}, {"clip_l", true}},
	"sdxl":            {{"vae", false}, {"clip_l", true}, {"clip_g", true}},
	"svd":             {{"vae", true}, {"clip_vision", true}},
	"sd3":             {{"vae", true}, {"clip_l", true}, {"clip_g", true}, {"t5xxl", true}},
	"flux":            {{"vae", true}, {"clip_l", true}, {"t5xxl", true}},
	"chroma":          {{"vae", true}, {"t5xxl", true}},
	"chroma_radiance": {{"t5xxl", true}},
	"flux2":           {{"vae", true}, {"llm", true}},
	"flux2_klein":     {{"vae", true}, {"llm", true}},
	"sefi_image":      {{"vae", true}, {"llm", true}},
	"lens":            {{"vae", true}, {"llm", true}, {"tokenizer", true}},
	"ovis_image":      {{"vae", true}, {"llm", true}},
	"wan":             {{"vae", true}, {"t5xxl", true}, {"high_noise_model", false}},
	"lingbot_video":   {{"vae", true}, {"llm", true}},
	"krea2":           {{"vae", true}, {"llm", true}},
	"qwen_image":      {{"vae", true}, {"llm", true}, {"llm_vision", false}},
	"longcat":         {{"vae", true}, {"llm", true}, {"llm_vision", false}},
	"mage_flow":       {{"vae", true}, {"llm", true}, {"llm_vision", false}},
	"boogu_image":     {{"vae", true}, {"llm", true}, {"llm_vision", false}},
	"ernie_image":     {{"vae", true}, {"llm", true}},
	"z_image":         {{"vae", true}, {"llm", true}},
	"anima":           {{"vae", true}, {"llm", true}},
	"hunyuan_video":   {{"vae", true}, {"llm", true}, {"t5xxl", true}},
	"ltx2":            {{"vae", true}, {"llm", true}, {"audio_vae", false}, {"embeddings_connectors", false}},
	"minimax_h3":      {{"vae", true}, {"llm", true}, {"audio_vae", false}, {"llm_vision", false}},
	"hidream_o1":      {},
	"minit2i":         {{"t5xxl", true}},
	"pid":             {{"vae", true}, {"llm", true}, {"tokenizer", true}},
	"ideogram4":       {{"vae", true}, {"llm", true}, {"uncond_model", true}},
	"sensenova_u1":    {},
}

// Needs lists the parts a checkpoint loads beside itself: its family's parts less the ones it bundles,
// plus what its inputs ask for, an image encoder for a Wan image to video model and an audio encoder
// for a speech to video one
func Needs(p Profile) []Part {
	var out []Part
	for _, part := range needs[Canonical(p.Family)] {
		switch part.Param {
		case "vae":
			if p.VAE {
				continue
			}
		case "clip_l", "clip_g", "t5xxl", "llm":
			if p.TextEncoder {
				continue
			}
		case "llm_vision":
			if p.TextEncoder {
				continue
			}
		}
		out = append(out, part)
	}
	if Canonical(p.Family) == "wan" {
		if p.ImageInput && p.Variant != "i2v" && p.Variant != "ti2v" && p.Variant != "s2v" && p.Variant != "vace" && !p.ClipVision {
			out = append(out, Part{"clip_vision", true})
		}
		if p.AudioInput {
			out = append(out, Part{"audio_encoder", true})
		}
	}
	return out
}

// Families that make video, and whether each also makes a still image on its own
var video = map[string]bool{"wan": true, "hunyuan_video": false, "ltx2": false, "minimax_h3": false, "lingbot_video": false, "svd": false}

// Generates says what a family makes, image, video, or both, in the words the descriptor carries
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

// Modes says which of the server's generation modes a family answers, img_gen and vid_gen
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

// The names a GGUF header, a diffusers config, or stable-diffusion.cpp's own version list gives a family, folded to the ids the profile uses
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
	"hunyuan_video": "hunyuan_video", "hyvid": "hunyuan_video", "hunyuanvideo": "hunyuan_video", "hunyuanvideotransformer3dmodel": "hunyuan_video", "hunyuan_video_1.5": "hunyuan_video",
	"anima": "anima", "anima2": "anima",
	"ltx2": "ltx2", "ltxav": "ltx2", "ltx-2": "ltx2", "ltx2.3": "ltx2", "ltx-2.3": "ltx2", "ltx2.5": "ltx2", "ltx-2.5": "ltx2", "ltxv": "ltx2", "ltx": "ltx2", "ltxvideotransformer3dmodel": "ltx2", "ltxav_transformer": "ltx2",
	"minimax_h3": "minimax_h3", "minimax-h3": "minimax_h3", "minimaxh3": "minimax_h3",
	"hidream_o1": "hidream_o1", "hidream-o1": "hidream_o1", "hidream": "hidream_o1", "hidreamimagetransformer2dmodel": "hidream_o1",
	"z_image": "z_image", "zimage": "z_image", "z-image": "z_image", "lumina2": "z_image", "lumina": "z_image", "lumina2transformer2dmodel": "z_image",
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
	"autoencoderkl": "vae", "autoencoderklwan": "vae", "autoencoderklqwenimage": "vae", "autoencoderklltxvideo": "vae", "autoencoderklhunyuanvideo": "vae", "autoencoderklcosmos": "vae", "autoencoderklflux2": "vae", "autoencoderklmagvit": "vae", "vae": "vae",
	"audio_vae": "audio_vae", "autoencoderklltxaudio": "audio_vae",
	"autoencodertiny": "taesd", "taesd": "taesd", "taehv": "taesd", "tae": "taesd",
	"clipvisionmodelwithprojection": "clip_vision", "clipvisionmodel": "clip_vision", "siglipvisionmodel": "clip_vision", "clip_vision": "clip_vision", "clip-vision": "clip_vision",
	"wav2vec2model": "audio_encoder", "wav2vec2": "audio_encoder", "audio_encoder": "audio_encoder",
	"embeddings_connectors": "embeddings_connectors", "connectors": "embeddings_connectors",
	"lora": "lora", "controlnet": "controlnet", "controlnetmodel": "controlnet", "ip_adapter": "ip_adapter", "ip-adapter": "ip_adapter", "photo_maker": "photo_maker", "photomaker": "photo_maker", "pulid": "pulid", "motion_module": "motion_module", "motionadapter": "motion_module", "embedding": "embedding", "textual_inversion": "embedding",
	"upscaler": "upscaler", "esrgan": "upscaler", "realesrgan": "upscaler", "detector": "detector", "yolo": "detector",
	"llm": "llm", "text_encoder": "text_encoder",
}

// Canonical folds the names a GGUF header, a diffusers config, or stable-diffusion.cpp gives a family or a part into the id the profile uses
func Canonical(architecture string) string {
	a := strings.ToLower(strings.TrimSpace(architecture))
	if c, ok := canonical[a]; ok {
		return c
	}
	return a
}

// Families stable-diffusion.cpp samples with, the ones a descriptor may name as a diffusion model
var denoisers = func() []string {
	out := make([]string, 0, len(needs))
	for f := range needs {
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}()

// Denoiser says whether an architecture, canonical or as a header names it, is a family stable-diffusion.cpp samples with
func Denoiser(architecture string) bool {
	c := Canonical(architecture)
	for _, f := range denoisers {
		if f == c {
			return true
		}
	}
	return false
}

// Families lists every family stable-diffusion.cpp samples with, by canonical id
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

// The parts a run may name, each by the param naming it, and what a checkpoint of it is in words
var parts = map[string]string{
	"vae":                   "a VAE",
	"audio_vae":             "an audio VAE",
	"taesd":                 "a tiny autoencoder",
	"t5":                    "a T5 text encoder",
	"clip_l":                "a CLIP-L text encoder",
	"clip_g":                "a CLIP-G text encoder",
	"llm":                   "a language model text encoder",
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

// Component says whether an architecture names a part loaded beside a denoiser rather than a model served on its own
func Component(architecture string) bool {
	_, ok := parts[Canonical(architecture)]
	return ok
}

// Describe puts a part's architecture into words, a pipeline part when the name is not one the profile knows
func Describe(architecture string) string {
	if w, ok := parts[Canonical(architecture)]; ok {
		return w
	}
	return "a pipeline part"
}
