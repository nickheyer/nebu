package blueprint

import (
	"sort"
	"strings"

	"github.com/nickheyer/nebu/pkg/formats/diffusion"
)

// Family defines a model pipeline, its component slots, and source repositories.
type Family struct {
	ID   string
	Name string
	// Pipeline type: image, video, or language.
	Pipeline string
	// Output description.
	Makes string
	Fills []Fill
	// Canonical repositories, searched after the model repository.
	Canonical []string
}

// Language is the blueprint for models served by language runtimes.
const Language = "language"

func req(slot string, p Part) Fill { return Fill{Part: p, Slot: slot, Required: true} }
func opt(slot string, p Part) Fill { return Fill{Part: p, Slot: slot} }

func only(f Fill, when func(p diffusion.Profile, group string) bool) Fill {
	f.When = when
	return f
}

// Returns the language model projector used as a vision tower.
func vision(llm Part) Part {
	return Part{Kind: "projector", Name: llm.Name + " vision tower", Names: llm.Names, Arch: llm.Arch, Params: llm.Params, Repos: llm.Repos}
}

func denoiser(name string, names []string, repos ...string) Part {
	return Part{Kind: "diffusion", Name: name, Names: names, Repos: repos}
}

func variant(v string) func(p diffusion.Profile, group string) bool {
	return func(p diffusion.Profile, _ string) bool { return p.Variant == v }
}

func notVariant(v string) func(p diffusion.Profile, group string) bool {
	return func(p diffusion.Profile, _ string) bool { return p.Variant != v }
}

func named(words ...string) func(p diffusion.Profile, group string) bool {
	return func(_ diffusion.Profile, group string) bool {
		g := strings.ToLower(group)
		for _, w := range words {
			if strings.Contains(g, w) {
				return true
			}
		}
		return false
	}
}

func unnamed(words ...string) func(p diffusion.Profile, group string) bool {
	is := named(words...)
	return func(p diffusion.Profile, group string) bool { return !is(p, group) }
}

var families = []Family{
	{ID: "sd1", Name: "Stable Diffusion 1.x", Pipeline: "image", Makes: "512px images with inpainting, depth, and upscaler variants",
		Fills:     []Fill{req(SlotVAE, vaeSD), req(SlotTextEncoderClipL, clipL)},
		Canonical: []string{"stable-diffusion-v1-5/stable-diffusion-v1-5", "runwayml/stable-diffusion-inpainting", "stabilityai/sd-vae-ft-mse-original"}},
	{ID: "sd2", Name: "Stable Diffusion 2.x", Pipeline: "image", Makes: "512px and 768px images with depth, inpainting, x4 upscaler, and unCLIP variants",
		Fills:     []Fill{req(SlotVAE, vaeSD), req(SlotTextEncoderClipH, clipH)},
		Canonical: []string{"stabilityai/stable-diffusion-2-1", "stabilityai/stable-diffusion-x4-upscaler"}},
	{ID: "sdxl", Name: "Stable Diffusion XL", Pipeline: "image", Makes: "1024px images with refiner, inpainting, Turbo, and Lightning variants",
		Fills:     []Fill{req(SlotVAE, vaeSDXL), req(SlotTextEncoderClipL, clipL), req(SlotTextEncoderClipG, clipG)},
		Canonical: []string{"stabilityai/stable-diffusion-xl-base-1.0", "stabilityai/stable-diffusion-xl-refiner-1.0", "madebyollin/sdxl-vae-fp16-fix"}},
	{ID: "kolors", Name: "Kolors", Pipeline: "image", Makes: "1024px images from an SDXL UNet with ChatGLM3 text",
		Fills:     []Fill{req(SlotVAE, vaeSDXL), req(SlotTextEncoderLLM, llmChatGLM3_6B)},
		Canonical: []string{"Kwai-Kolors/Kolors"}},
	{ID: "sd3", Name: "Stable Diffusion 3 and 3.5", Pipeline: "image", Makes: "1024px images (3.5 Large, Large Turbo, Medium)",
		Fills:     []Fill{req(SlotVAE, vaeSD3), req(SlotTextEncoderClipL, clipL), req(SlotTextEncoderClipG, clipG), opt(SlotTextEncoderT5, t5XXL)},
		Canonical: []string{"stabilityai/stable-diffusion-3.5-large", "Comfy-Org/stable-diffusion-3.5-fp8"}},
	{ID: "flux", Name: "FLUX.1", Pipeline: "image", Makes: "1024px images (dev, schnell, Fill, Depth, Canny, Redux, Kontext, Krea)",
		Fills:     []Fill{req(SlotVAE, vaeFLUX), req(SlotTextEncoderClipL, clipL), req(SlotTextEncoderT5, t5XXL)},
		Canonical: []string{"black-forest-labs/FLUX.1-dev", "black-forest-labs/FLUX.1-schnell", "comfyanonymous/flux_text_encoders", "black-forest-labs/FLUX.1-Kontext-dev", "black-forest-labs/FLUX.1-Fill-dev", "black-forest-labs/FLUX.1-Redux-dev", "city96/FLUX.1-dev-gguf"}},
	{ID: "chroma", Name: "Chroma", Pipeline: "image", Makes: "1024px images (Chroma1-HD, Chroma1-Base, Chroma1-Flash)",
		Fills:     []Fill{req(SlotVAE, vaeFLUX), req(SlotTextEncoderT5, t5XXL)},
		Canonical: []string{"lodestones/Chroma1-HD"}},
	{ID: "chroma_radiance", Name: "Chroma Radiance", Pipeline: "image", Makes: "images directly in pixel space",
		Fills:     []Fill{req(SlotTextEncoderT5, t5XXL)},
		Canonical: []string{"lodestones/Chroma1-Radiance"}},
	{ID: "flux2", Name: "FLUX.2", Pipeline: "image", Makes: "up to 4MP images and edits from multiple references (dev)",
		Fills:     []Fill{req(SlotVAE, vaeFLUX2), req(SlotTextEncoderLLM, llmMistralSmall32), opt(SlotTextEncoderVision, vision(llmMistralSmall32))},
		Canonical: []string{"black-forest-labs/FLUX.2-dev", "unsloth/Mistral-Small-3.2-24B-Instruct-2506-GGUF"}},
	{ID: "flux2_klein", Name: "FLUX.2 klein", Pipeline: "image", Makes: "up to 4MP images (klein 4B and klein 9B distilled transformers)",
		Fills:     []Fill{req(SlotVAE, vaeFLUX2), only(req(SlotTextEncoderLLM, llmQwen3_4B), unnamed("9b")), only(req(SlotTextEncoderLLM, llmQwen3_8B), named("9b"))},
		Canonical: []string{"black-forest-labs/FLUX.2-klein-4B", "Comfy-Org/flux2-klein-4B", "black-forest-labs/FLUX.2-klein-9B", "Comfy-Org/flux2-klein-9B"}},
	{ID: "hidream_i1", Name: "HiDream-I1 and HiDream-E1", Pipeline: "image", Makes: "1024px images (I1 Full, Dev, Fast) and instruction edits (E1)",
		Fills:     []Fill{req(SlotVAE, vaeFLUX), req(SlotTextEncoderClipL, clipL), req(SlotTextEncoderClipG, clipG), req(SlotTextEncoderT5, t5XXL), req(SlotTextEncoderLLM, llmLlama31_8B)},
		Canonical: []string{"HiDream-ai/HiDream-I1-Full", "HiDream-ai/HiDream-E1-1"}},
	{ID: "hidream_o1", Name: "HiDream-O1", Pipeline: "image", Makes: "images from one bundled checkpoint holding the transformer with its text encoders and VAE",
		Canonical: []string{"HiDream-ai/HiDream-O1"}},
	{ID: "qwen_image", Name: "Qwen-Image and Qwen-Image-Edit", Pipeline: "image", Makes: "1328px images with rendered text, edits from multiple images (Edit, Edit-2509, Edit-2511), and Lightning distilled variants",
		Fills:     []Fill{req(SlotVAE, vaeQwenImage), req(SlotTextEncoderLLM, llmQwen25VL7B), opt(SlotTextEncoderVision, vision(llmQwen25VL7B))},
		Canonical: []string{"Qwen/Qwen-Image", "Qwen/Qwen-Image-Edit-2509", "Comfy-Org/Qwen-Image_ComfyUI", "lightx2v/Qwen-Image-Lightning", "city96/Qwen-Image-gguf"}},
	{ID: "qwen_image21", Name: "Qwen-Image 2.1", Pipeline: "image", Makes: "images up to 2048px with rendered text, edits from reference images, and RGBA output with transparency",
		Fills:     []Fill{req(SlotVAE, vaeQwenImage21), req(SlotTextEncoderLLM, llmQwen3VL8B), opt(SlotTextEncoderVision, vision(llmQwen3VL8B))},
		Canonical: []string{"Qwen/Qwen-Image-2.1", "Comfy-Org/Qwen-Image-2.1", "leejet/Qwen-Image-2.1-GGUF", "Qwen/Qwen3-VL-8B-Instruct-GGUF"}},
	{ID: "z_image", Name: "Z-Image", Pipeline: "image", Makes: "1024px images (Turbo at 8 steps, Base, Edit)",
		Fills:     []Fill{req(SlotVAE, vaeFLUX), req(SlotTextEncoderLLM, llmQwen3_4B)},
		Canonical: []string{"Tongyi-MAI/Z-Image-Turbo", "Comfy-Org/z_image_turbo"}},
	{ID: "lumina2", Name: "Lumina-Image 2.0", Pipeline: "image", Makes: "1024px images from a Next-DiT with Gemma-2-2B text",
		Fills:     []Fill{req(SlotVAE, vaeFLUX), req(SlotTextEncoderLLM, llmGemma2_2B)},
		Canonical: []string{"Alpha-VLLM/Lumina-Image-2.0", "Comfy-Org/Lumina_Image_2.0_Repackaged"}},
	{ID: "sana", Name: "Sana and Sana 1.5", Pipeline: "image", Makes: "1024px to 4096px images at low memory",
		Fills:     []Fill{req(SlotVAE, vaeSana), req(SlotTextEncoderLLM, llmGemma2_2B)},
		Canonical: []string{"Efficient-Large-Model/Sana_1600M_1024px_diffusers"}},
	{ID: "pixart", Name: "PixArt-α and PixArt-Σ", Pipeline: "image", Makes: "1024px images from a DiT with T5-XXL text",
		Fills:     []Fill{only(req(SlotVAE, vaeSD), unnamed("sigma")), only(req(SlotVAE, vaeSDXL), named("sigma")), req(SlotTextEncoderT5, t5XXL)},
		Canonical: []string{"PixArt-alpha/PixArt-XL-2-1024-MS", "PixArt-alpha/PixArt-Sigma-XL-2-1024-MS"}},
	{ID: "hunyuan_dit", Name: "Hunyuan-DiT", Pipeline: "image", Makes: "1024px images from a DiT with bilingual CLIP and mT5 text",
		Fills:     []Fill{req(SlotVAE, vaeSDXL), req(SlotTextEncoderClipL, clipHunyuan), req(SlotTextEncoderT5, mT5XL)},
		Canonical: []string{"Tencent-Hunyuan/HunyuanDiT-v1.2-Diffusers"}},
	{ID: "cogview4", Name: "CogView4 and CogView3-Plus", Pipeline: "image", Makes: "images from a 6B DiT with GLM-4-9B text",
		Fills:     []Fill{req(SlotVAE, vaeCogView4), req(SlotTextEncoderLLM, llmGLM4_9B)},
		Canonical: []string{"THUDM/CogView4-6B"}},
	{ID: "kandinsky22", Name: "Kandinsky 2.2", Pipeline: "image", Makes: "images decoded from CLIP ViT-bigG image embeddings a prior maps text to",
		Fills:     []Fill{req(SlotStagePrior, denoiser("the Kandinsky 2.2 prior", []string{"prior"}, "kandinsky-community/kandinsky-2-2-prior")), req(SlotVAE, vaeMoVQ), opt(SlotImageClipVision, clipVisionBigG)},
		Canonical: []string{"kandinsky-community/kandinsky-2-2-decoder", "kandinsky-community/kandinsky-2-2-prior"}},
	{ID: "kandinsky3", Name: "Kandinsky 3", Pipeline: "image", Makes: "images from a UNet with FLAN-UL2 text",
		Fills:     []Fill{req(SlotVAE, vaeMoVQ), req(SlotTextEncoderT5, flanUL2)},
		Canonical: []string{"kandinsky-community/kandinsky-3"}},
	{ID: "deepfloyd", Name: "DeepFloyd IF", Pipeline: "image", Makes: "a pixel space cascade from 64px to 1024px",
		Fills:     []Fill{req(SlotTextEncoderT5, t5XXL), req(SlotStageDecoder, denoiser("IF-II L, 64 to 256px", []string{"if-ii", "if_ii", "ifii"}, "DeepFloyd/IF-II-L-v1.0")), opt(SlotStageUpscaler, denoiser("IF-III, the SD x4 upscaler to 1024px", []string{"x4-upscaler", "x4_upscaler", "upscaler"}, "stabilityai/stable-diffusion-x4-upscaler"))},
		Canonical: []string{"DeepFloyd/IF-I-XL-v1.0", "DeepFloyd/IF-II-L-v1.0"}},
	{ID: "stable_cascade", Name: "Stable Cascade", Pipeline: "image", Makes: "images from Stage C in the Würstchen latent, decoded by Stage B and Stage A",
		Fills:     []Fill{req(SlotStageDecoder, denoiser("Stage B, to the 4x latent", []string{"stage_b", "stage-b", "stageb"}, "stabilityai/stable-cascade")), req(SlotVAE, vaeStageA), req(SlotTextEncoderClipG, clipG), opt(SlotImageClipVision, clipVisionBigG)},
		Canonical: []string{"stabilityai/stable-cascade"}},
	{ID: "ovis_image", Name: "Ovis-Image", Pipeline: "image", Makes: "images from a 7B DiT with the Ovis 2.5 language model",
		Fills:     []Fill{req(SlotVAE, vaeFLUX), req(SlotTextEncoderLLM, llmOvis25)},
		Canonical: []string{"AIDC-AI/Ovis-Image", "Comfy-Org/Ovis-Image"}},
	{ID: "longcat", Name: "LongCat-Image", Pipeline: "image", Makes: "images and edits from a 6B DiT with Qwen2.5-VL-7B text",
		Fills:     []Fill{req(SlotVAE, vaeFLUX), req(SlotTextEncoderLLM, llmQwen25VL7B), opt(SlotTextEncoderVision, vision(llmQwen25VL7B))},
		Canonical: []string{"meituan-longcat/LongCat-Image"}},
	{ID: "krea2", Name: "Krea 2", Pipeline: "image", Makes: "images from the Krea 2 transformer with Qwen3-VL-4B text",
		Fills:     []Fill{req(SlotVAE, vaeWan21), req(SlotTextEncoderLLM, llmQwen3VL4B)},
		Canonical: []string{"Comfy-Org/Krea-2"}},
	{ID: "ernie_image", Name: "ERNIE-Image", Pipeline: "image", Makes: "images from the ERNIE-Image transformer with Ministral 3 text",
		Fills:     []Fill{req(SlotVAE, vaeERNIE), req(SlotTextEncoderLLM, llmMinistral3_3B)},
		Canonical: []string{"Comfy-Org/ERNIE-Image"}},
	{ID: "anima", Name: "Anima", Pipeline: "image", Makes: "images from the Anima transformer with Qwen3-0.6B text",
		Fills:     []Fill{req(SlotVAE, vaeQwenImage), req(SlotTextEncoderLLM, llmQwen3_0_6B)},
		Canonical: []string{"circlestone-labs/Anima"}},
	{ID: "boogu_image", Name: "Boogu-Image", Pipeline: "image", Makes: "images from double and single stream layers with Qwen3-VL-8B text",
		Fills: []Fill{req(SlotVAE, vaeFLUX), req(SlotTextEncoderLLM, llmQwen3VL8B), opt(SlotTextEncoderVision, vision(llmQwen3VL8B))}},
	{ID: "mage_flow", Name: "Mage-Flow", Pipeline: "image", Makes: "images from the Mage-Flow transformer with Qwen3-VL-4B text",
		Fills:     []Fill{req(SlotVAE, vaeMageFlow), req(SlotTextEncoderLLM, llmQwen3VL4B), opt(SlotTextEncoderVision, vision(llmQwen3VL4B))},
		Canonical: []string{"microsoft/Mage-Flow"}},
	{ID: "ideogram4", Name: "Ideogram 4", Pipeline: "image", Makes: "images from a conditional transformer with a separate unconditional one for guidance",
		Fills:     []Fill{req(SlotDenoiserUncond, denoiser("the unconditional transformer", []string{"uncond"}, "ideogram-ai/ideogram-4-fp8")), req(SlotVAE, vaeFLUX2), req(SlotTextEncoderLLM, llmQwen3VL8B)},
		Canonical: []string{"ideogram-ai/ideogram-4-fp8"}},
	{ID: "sefi_image", Name: "SeFi-Image", Pipeline: "image", Makes: "images from the SeFi transformer",
		Fills: []Fill{req(SlotVAE, vaeFLUX2), req(SlotTextEncoderLLM, llmSeFi)}},
	{ID: "lens", Name: "Lens", Pipeline: "image", Makes: "images from the Lens transformer with gpt-oss-20b text",
		Fills:     []Fill{req(SlotVAE, vaeFLUX2), req(SlotTextEncoderLLM, llmGPTOSS20B), req(SlotTokenizer, tokenizerGPTOSS)},
		Canonical: []string{"openai/gpt-oss-20b"}},
	{ID: "pid", Name: "PixelDiT", Pipeline: "image", Makes: "images from a pixel space DiT with Gemma-2-2B text",
		Fills:     []Fill{req(SlotVAE, vaePiD), req(SlotTextEncoderLLM, llmGemma2_2B), req(SlotTokenizer, tokenizerGemma2)},
		Canonical: []string{"nvidia/PiD", "Comfy-Org/PixelDiT"}},
	{ID: "minit2i", Name: "MiniT2I", Pipeline: "image", Makes: "images from a small transformer with FLAN-T5-Large text",
		Fills: []Fill{req(SlotTextEncoderT5, flanT5Large)}},
	{ID: "sensenova_u1", Name: "SenseNova U1", Pipeline: "image", Makes: "images from one unified checkpoint holding the transformer with its encoders and autoencoder"},
	{ID: "svd", Name: "Stable Video Diffusion", Pipeline: "video", Makes: "14 or 25 frames from one image at 576x1024",
		Fills:     []Fill{req(SlotVAE, vaeSVD), req(SlotImageClipVision, clipVisionH)},
		Canonical: []string{"stabilityai/stable-video-diffusion-img2vid-xt-1-1"}},
	{ID: "wan", Name: "Wan 2.1 and 2.2", Pipeline: "video", Makes: "480p and 720p clips at 16 or 24 fps (T2V, I2V, FLF2V, TI2V, S2V, VACE, Fun, Phantom, Animate)",
		Fills: []Fill{
			only(req(SlotVAE, vaeWan21), notVariant("ti2v")),
			only(req(SlotVAE, vaeWan22), variant("ti2v")),
			req(SlotTextEncoderT5, umt5XXL),
			only(opt(SlotDenoiserHighNoise, denoiser("the high noise expert of the A14B pair", nil, "Comfy-Org/Wan_2.2_ComfyUI_Repackaged", "Wan-AI/Wan2.2-T2V-A14B", "Wan-AI/Wan2.2-I2V-A14B")), lowNoise),
			only(req(SlotImageClipVision, clipVisionH), func(p diffusion.Profile, _ string) bool { return p.ImageInput && p.Variant == "" }),
			only(req(SlotAudioEncoder, audioWav2Vec2), func(p diffusion.Profile, _ string) bool { return p.AudioInput }),
		},
		Canonical: []string{"Wan-AI/Wan2.1-T2V-14B", "Wan-AI/Wan2.1-I2V-14B-720P", "Wan-AI/Wan2.1-VACE-14B", "Comfy-Org/Wan_2.1_ComfyUI_repackaged", "city96/Wan2.1-T2V-14B-gguf", "city96/umt5-xxl-encoder-gguf", "Wan-AI/Wan2.2-T2V-A14B", "Wan-AI/Wan2.2-I2V-A14B", "Wan-AI/Wan2.2-TI2V-5B", "Wan-AI/Wan2.2-S2V-14B", "Wan-AI/Wan2.2-Animate-14B", "Comfy-Org/Wan_2.2_ComfyUI_Repackaged"}},
	{ID: "hunyuan_video", Name: "HunyuanVideo", Pipeline: "video", Makes: "720p clips of 129 frames at 24 fps (T2V, I2V, Avatar, Custom)",
		Fills:     []Fill{req(SlotVAE, vaeHunyuanVideo), req(SlotTextEncoderLLM, llmLLaVALlama3), req(SlotTextEncoderClipL, clipL)},
		Canonical: []string{"tencent/HunyuanVideo", "tencent/HunyuanVideo-I2V", "Comfy-Org/HunyuanVideo_repackaged", "city96/HunyuanVideo-gguf"}},
	{ID: "hunyuan_video_15", Name: "HunyuanVideo 1.5", Pipeline: "video", Makes: "480p and 720p clips with a 1080p super resolution stage (T2V and I2V)",
		Fills:     []Fill{req(SlotVAE, vaeHunyuanVideo15), req(SlotTextEncoderLLM, llmQwen25VL7B), req(SlotTextEncoderGlyph, byT5Small)},
		Canonical: []string{"tencent/HunyuanVideo-1.5", "Comfy-Org/HunyuanVideo_1.5_repackaged"}},
	{ID: "ltxv", Name: "LTX-Video 0.9.x", Pipeline: "video", Makes: "768x512 to 1216x704 clips at 24 to 30 fps in real time (2B and 13B)",
		Fills:     []Fill{req(SlotVAE, vaeLTXV), req(SlotTextEncoderT5, t5XXL)},
		Canonical: []string{"Lightricks/LTX-Video", "Lightricks/LTX-Video-0.9.8-13B-distilled"}},
	{ID: "ltx2", Name: "LTX-2, LTX-2.3, LTX-2.5", Pipeline: "video", Makes: "joint audio and video clips, 1080p at 24 fps, up to 4K with the upscaler",
		Fills:     []Fill{req(SlotVAE, vaeLTX2), opt(SlotVAEAudio, audioVAELTX2), req(SlotTextEncoderLLM, llmGemma3_12B), opt(SlotConnector, connectorsLTX2)},
		Canonical: []string{"Lightricks/LTX-2", "Lightricks/LTX-2.5", "unsloth/LTX-2.3-GGUF"}},
	{ID: "minimax_h3", Name: "MiniMax-H3", Pipeline: "video", Makes: "video and stereo audio with 24 fps latent frames (T2VA, I2VA, FL2VA, Ref2VA)",
		Fills:     []Fill{req(SlotVAE, vaeMiniMaxH3), opt(SlotVAEAudio, audioVAEMiniMaxH3), req(SlotTextEncoderLLM, llmQwen3VL32B), opt(SlotTextEncoderVision, vision(llmQwen3VL32B))},
		Canonical: []string{"Comfy-Org/MiniMax-H3", "leejet/MiniMax-H3-GGUF"}},
	{ID: "lingbot_video", Name: "LingBot-Video", Pipeline: "video", Makes: "clips from the LingBot video transformer with Qwen3-VL-4B text",
		Fills: []Fill{req(SlotVAE, vaeWan21), req(SlotTextEncoderLLM, llmQwen3VL4B)}},
	{ID: "cogvideox", Name: "CogVideoX and CogVideoX 1.5", Pipeline: "video", Makes: "480p 49 frame clips (2B, 5B) and 768p 81 frame clips (1.5 5B), with T2V and I2V",
		Fills:     []Fill{req(SlotVAE, vaeCogVideoX), req(SlotTextEncoderT5, t5XXL)},
		Canonical: []string{"THUDM/CogVideoX-5b", "THUDM/CogVideoX1.5-5B-I2V"}},
	{ID: "mochi", Name: "Mochi 1", Pipeline: "video", Makes: "clips from a 10B asymmetric DiT",
		Fills:     []Fill{req(SlotVAE, vaeMochi), req(SlotTextEncoderT5, t5XXL)},
		Canonical: []string{"genmo/mochi-1-preview"}},
	{ID: "open_sora", Name: "Open-Sora 1.x and 2.0", Pipeline: "video", Makes: "T2V and I2V at 256p and 768p",
		Fills:     []Fill{req(SlotVAE, vaeOpenSora), req(SlotTextEncoderT5, t5XXL), opt(SlotTextEncoderClipL, clipL)},
		Canonical: []string{"hpcai-tech/Open-Sora-v2"}},
	{ID: "allegro", Name: "Allegro", Pipeline: "video", Makes: "clips from a 2.8B DiT",
		Fills:     []Fill{req(SlotVAE, vaeAllegro), req(SlotTextEncoderT5, t5XXL)},
		Canonical: []string{"rhymes-ai/Allegro"}},
	{ID: "step_video", Name: "Step-Video-T2V and Step-Video-TI2V", Pipeline: "video", Makes: "clips from a 30B DiT",
		Fills:     []Fill{req(SlotVAE, vaeStepVideo), req(SlotTextEncoderLLM, llmStep), req(SlotTextEncoderClipL, clipHunyuan)},
		Canonical: []string{"stepfun-ai/stepvideo-t2v"}},
	{ID: "magi", Name: "MAGI-1", Pipeline: "video", Makes: "clips from an autoregressive chunk denoising transformer",
		Fills:     []Fill{req(SlotVAE, vaeMAGI), req(SlotTextEncoderT5, t5XXL)},
		Canonical: []string{"sand-ai/MAGI-1"}},
	{ID: "pyramid_flow", Name: "Pyramid Flow", Pipeline: "video", Makes: "clips from an SD3 derived MMDiT with pyramidal flow matching",
		Fills:     []Fill{req(SlotVAE, vaePyramid), req(SlotTextEncoderT5, t5XXL), req(SlotTextEncoderClipL, clipL)},
		Canonical: []string{"rain1011/pyramid-flow-miniflux"}},
	{ID: "easyanimate", Name: "EasyAnimate", Pipeline: "video", Makes: "clips from a Wan style DiT with Qwen2-VL-7B text",
		Fills:     []Fill{req(SlotVAE, vaeEasyAnimate), req(SlotTextEncoderLLM, llmQwen2VL7B)},
		Canonical: []string{"alibaba-pai/EasyAnimateV5.1-12b-zh"}},
	{ID: "ovi", Name: "Ovi", Pipeline: "video", Makes: "joint audio and video, 5 seconds at 24 fps",
		Fills:     []Fill{req(SlotVAE, vaeOvi), req(SlotVAEAudio, audioVAEMMAudio), req(SlotTextEncoderT5, umt5XXL)},
		Canonical: []string{"chetwinlow1/Ovi"}},
	{ID: "cosmos", Name: "Cosmos Predict and Transfer", Pipeline: "video", Makes: "world simulation from text and past frames",
		Fills:     []Fill{req(SlotVAE, vaeCosmos), only(req(SlotTextEncoderT5, t5XXL), unnamed("predict2", "predict-2", "predict_2")), only(req(SlotTextEncoderLLM, llmCosmosReason), named("predict2", "predict-2", "predict_2"))},
		Canonical: []string{"nvidia/Cosmos-Predict2-2B-Video2World", "nvidia/Cosmos-Transfer1-7B"}},
	{ID: Language, Name: "Language model", Pipeline: "language", Makes: "text, vectors, scores, transcripts, or actions from weights served by a language runtime",
		Fills: []Fill{
			{Kind: "weights", Name: "weights", Slot: SlotWeights, Required: true},
			{Kind: "config", Name: "architecture config", Slot: SlotConfig, Required: true},
			{Kind: "tokenizer", Name: "tokenizer and chat template", Slot: SlotTokenizer},
			{Kind: "projector", Name: "vision or audio projector", Slot: SlotProjector},
		}},
}

var byID = func() map[string]*Family {
	out := map[string]*Family{}
	for i := range families {
		out[families[i].ID] = &families[i]
	}
	return out
}()

// Families returns blueprints in table order.
func Families() []Family { return append([]Family(nil), families...) }

// Get returns a family by ID, or nil if unknown.
func Get(id string) *Family { return byID[id] }

// IDs returns sorted family IDs.
func IDs() []string {
	out := make([]string, 0, len(families))
	for _, f := range families {
		out = append(out, f.ID)
	}
	sort.Strings(out)
	return out
}

// Fills returns the slots that apply to a checkpoint.
func Fills(p diffusion.Profile, group string) []Fill {
	f := Get(diffusion.Canonical(p.Family))
	if f == nil {
		return nil
	}
	var out []Fill
	for _, fill := range f.Fills {
		if fill.When != nil && !fill.When(p, group) {
			continue
		}
		out = append(out, fill)
	}
	return out
}

// Needs returns component slots not bundled in the checkpoint.
func Needs(p diffusion.Profile, group string) []Fill {
	var out []Fill
	for _, fill := range Fills(p, group) {
		if p.Slots[fill.Slot] {
			continue
		}
		switch fill.Slot {
		case SlotVAE:
			if p.VAE {
				continue
			}
		case SlotTextEncoderClipL, SlotTextEncoderClipG, SlotTextEncoderClipH, SlotTextEncoderT5, SlotTextEncoderLLM, SlotTextEncoderVision, SlotTextEncoderGlyph:
			if p.TextEncoder {
				continue
			}
		case SlotImageClipVision:
			if p.ClipVision {
				continue
			}
		}
		out = append(out, fill)
	}
	return out
}

// Where returns component sources, falling back to the family repositories.
func Where(f *Family, fill Fill) []string {
	if len(fill.Repos) > 0 {
		return append([]string(nil), fill.Repos...)
	}
	if f == nil {
		return nil
	}
	return append([]string(nil), f.Canonical...)
}

// Published returns unique source repositories, with canonical sources first.
func Published(f *Family) []string {
	var out []string
	seen := map[string]bool{}
	add := func(repo string) {
		if repo != "" && !seen[repo] {
			seen[repo] = true
			out = append(out, repo)
		}
	}
	if f == nil {
		return nil
	}
	for _, r := range f.Canonical {
		add(r)
	}
	for _, fill := range f.Fills {
		for _, r := range fill.Repos {
			add(r)
		}
	}
	return out
}
