package blueprint

import "github.com/nickheyer/nebu/pkg/formats/diffusion"

// Part identifies the component needed for a slot.
type Part struct {
	// Component kind from the model headers.
	Kind string
	// Component label.
	Name string
	// Accepted names in file, group, or repository names.
	Names []string
	// Accepted architecture ID substrings.
	Arch []string
	// Expected parameter counts, with a tolerance of one third.
	Params []float64
	// Text encoder vocabulary size.
	Vocab float64
	// Encoder embedding width.
	Width float64
	// Autoencoder latent channels.
	Channels float64
	// Whether the autoencoder uses 3D convolutions over frames.
	Video bool
	// Source repositories, searched after the model and family repositories.
	Repos []string
}

// Fill assigns a component to a family slot.
type Fill struct {
	Part
	Slot     string
	Required bool
	// Checkpoint filter. Nil applies to all checkpoints.
	When func(p diffusion.Profile, group string) bool
}

// Shared components.
var (
	vaeSD             = Part{Kind: "vae", Name: "kl-f8 VAE, 4 channels", Names: []string{"sd-vae", "vae-ft", "kl-f8", "sd15", "sd1.5", "v1-5", "sd_vae", "sd-vae-ft-mse", "vae-ft-mse"}, Channels: 4, Repos: []string{"stabilityai/sd-vae-ft-mse-original", "stable-diffusion-v1-5/stable-diffusion-v1-5"}}
	vaeSDXL           = Part{Kind: "vae", Name: "SDXL VAE", Names: []string{"sdxl", "sd_xl", "sdxl-vae", "sdxl_vae", "xl"}, Channels: 4, Repos: []string{"madebyollin/sdxl-vae-fp16-fix", "stabilityai/stable-diffusion-xl-base-1.0"}}
	vaeSD3            = Part{Kind: "vae", Name: "SD3 VAE, 16 channels", Names: []string{"sd3", "sd3.5", "sd35"}, Channels: 16, Repos: []string{"stabilityai/stable-diffusion-3.5-large", "Comfy-Org/stable-diffusion-3.5-fp8"}}
	vaeFLUX           = Part{Kind: "vae", Name: "FLUX ae.safetensors, 16 channels", Names: []string{"flux1", "flux.1", "flux-1", "flux1-vae", "flux_vae"}, Channels: 16, Repos: []string{"black-forest-labs/FLUX.1-schnell", "black-forest-labs/FLUX.1-dev", "Comfy-Org/Lumina_Image_2.0_Repackaged", "Comfy-Org/z_image_turbo"}}
	vaeFLUX2          = Part{Kind: "vae", Name: "FLUX.2 autoencoder", Names: []string{"flux2", "flux.2", "flux-2"}, Repos: []string{"black-forest-labs/FLUX.2-dev", "Comfy-Org/flux2-klein-4B"}}
	vaeWan21          = Part{Kind: "vae", Name: "Wan 2.1 VAE, 16 channels", Names: []string{"wan_2.1", "wan2.1", "wan_2_1", "wan21"}, Channels: 16, Video: true, Repos: []string{"Comfy-Org/Wan_2.1_ComfyUI_repackaged", "Comfy-Org/Wan_2.2_ComfyUI_Repackaged", "Wan-AI/Wan2.1-T2V-14B"}}
	vaeWan22          = Part{Kind: "vae", Name: "Wan 2.2 VAE, 48 channels", Names: []string{"wan_2.2", "wan2.2", "wan_2_2", "wan22"}, Channels: 48, Video: true, Repos: []string{"Comfy-Org/Wan_2.2_ComfyUI_Repackaged", "Wan-AI/Wan2.2-TI2V-5B"}}
	vaeQwenImage      = Part{Kind: "vae", Name: "Qwen-Image VAE, 16 channels", Names: []string{"qwen_image", "qwen-image", "qwenimage"}, Channels: 16, Video: true, Repos: []string{"Comfy-Org/Qwen-Image_ComfyUI", "Qwen/Qwen-Image"}}
	vaeQwenImage21    = Part{Kind: "vae", Name: "Qwen-Image 2.1 VAE, 64 channels", Names: []string{"qwen_image_2.1", "qwen-image-2.1", "qwen_image_21", "qwen_image21", "qwenimage21"}, Channels: 64, Video: true, Repos: []string{"Comfy-Org/Qwen-Image-2.1", "Qwen/Qwen-Image-2.1"}}
	vaeHunyuanVideo   = Part{Kind: "vae", Name: "HunyuanVideo 3D causal VAE, 16 channels", Names: []string{"hunyuan_video", "hunyuanvideo", "hunyuan"}, Channels: 16, Video: true, Repos: []string{"Comfy-Org/HunyuanVideo_repackaged", "tencent/HunyuanVideo"}}
	vaeHunyuanVideo15 = Part{Kind: "vae", Name: "HunyuanVideo 1.5 VAE", Names: []string{"hunyuanvideo1.5", "hunyuan_video_1.5", "hunyuanvideo_1.5", "hunyuanvideo15"}, Video: true, Repos: []string{"Comfy-Org/HunyuanVideo_1.5_repackaged", "tencent/HunyuanVideo-1.5"}}
	vaeLTX2           = Part{Kind: "vae", Name: "LTX-2 video VAE", Names: []string{"ltx", "ltx2", "ltx-2", "ltx_2"}, Video: true, Repos: []string{"Lightricks/LTX-2.5", "Lightricks/LTX-2", "unsloth/LTX-2.3-GGUF"}}
	vaeLTXV           = Part{Kind: "vae", Name: "LTX causal VAE, 128 channels", Names: []string{"ltx", "ltxv", "ltx-video"}, Channels: 128, Video: true, Repos: []string{"Lightricks/LTX-Video"}}
	vaeMiniMaxH3      = Part{Kind: "vae", Name: "MiniMax-H3 video VAE", Names: []string{"minimax", "minimax_h3", "minimax-h3", "h3"}, Video: true, Repos: []string{"Comfy-Org/MiniMax-H3"}}
	vaeMageFlow       = Part{Kind: "vae", Name: "the Mage-Flow VAE", Names: []string{"mage", "mage_flow", "mage-flow"}, Repos: []string{"microsoft/Mage-Flow"}}
	vaeERNIE          = Part{Kind: "vae", Name: "the ERNIE-Image VAE", Names: []string{"ernie"}, Repos: []string{"Comfy-Org/ERNIE-Image"}}
	vaePiD            = Part{Kind: "vae", Name: "the PixelDiT autoencoder", Names: []string{"pid", "pixeldit"}, Repos: []string{"nvidia/PiD", "Comfy-Org/PixelDiT"}}
	vaeSVD            = Part{Kind: "vae", Name: "SD VAE with a temporal decoder", Names: []string{"svd", "stable-video-diffusion", "img2vid"}, Channels: 4, Repos: []string{"stabilityai/stable-video-diffusion-img2vid-xt-1-1", "stabilityai/stable-video-diffusion-img2vid-xt"}}
	vaeSana           = Part{Kind: "vae", Name: "DC-AE, 32 channels", Names: []string{"dc-ae", "dc_ae", "dcae", "sana"}, Channels: 32, Repos: []string{"Efficient-Large-Model/Sana_1600M_1024px_diffusers", "mit-han-lab/dc-ae-f32c32-sana-1.0-diffusers"}}
	vaeCogView4       = Part{Kind: "vae", Name: "CogView4 VAE, 16 channels", Names: []string{"cogview4", "cogview"}, Channels: 16, Repos: []string{"THUDM/CogView4-6B"}}
	vaeCogVideoX      = Part{Kind: "vae", Name: "CogVideoX 3D causal VAE, 16 channels", Names: []string{"cogvideox", "cogvideo"}, Channels: 16, Video: true, Repos: []string{"THUDM/CogVideoX-5b", "THUDM/CogVideoX1.5-5B-I2V"}}
	vaeMochi          = Part{Kind: "vae", Name: "Mochi AsymmVAE, 12 channels", Names: []string{"mochi"}, Channels: 12, Video: true, Repos: []string{"genmo/mochi-1-preview"}}
	vaeAllegro        = Part{Kind: "vae", Name: "Allegro VAE", Names: []string{"allegro"}, Video: true, Repos: []string{"rhymes-ai/Allegro"}}
	vaeStepVideo      = Part{Kind: "vae", Name: "Step Video-VAE", Names: []string{"stepvideo", "step-video", "step_video"}, Video: true, Repos: []string{"stepfun-ai/stepvideo-t2v"}}
	vaeMAGI           = Part{Kind: "vae", Name: "MAGI VAE", Names: []string{"magi"}, Video: true, Repos: []string{"sand-ai/MAGI-1"}}
	vaePyramid        = Part{Kind: "vae", Name: "Pyramid Flow causal video VAE", Names: []string{"pyramid", "pyramid-flow", "pyramid_flow"}, Video: true, Repos: []string{"rain1011/pyramid-flow-miniflux"}}
	vaeEasyAnimate    = Part{Kind: "vae", Name: "EasyAnimate VAE", Names: []string{"easyanimate"}, Video: true, Repos: []string{"alibaba-pai/EasyAnimateV5.1-12b-zh"}}
	vaeOpenSora       = Part{Kind: "vae", Name: "Open-Sora VAE", Names: []string{"opensora", "open-sora", "open_sora"}, Video: true, Repos: []string{"hpcai-tech/Open-Sora-v2"}}
	vaeMoVQ           = Part{Kind: "vae", Name: "MoVQ", Names: []string{"movq", "kandinsky"}, Repos: []string{"kandinsky-community/kandinsky-2-2-decoder", "kandinsky-community/kandinsky-3"}}
	vaeStageA         = Part{Kind: "vae", Name: "Stage A VQGAN", Names: []string{"stage_a", "stage-a", "cascade"}, Repos: []string{"stabilityai/stable-cascade"}}
	vaeOvi            = Part{Kind: "vae", Name: "Wan 2.2 VAE", Names: []string{"wan_2.2", "wan2.2", "wan22", "ovi"}, Channels: 48, Video: true, Repos: []string{"chetwinlow1/Ovi", "Comfy-Org/Wan_2.2_ComfyUI_Repackaged"}}
	vaeCosmos         = Part{Kind: "vae", Name: "Cosmos tokenizer", Names: []string{"cosmos"}, Video: true, Repos: []string{"nvidia/Cosmos-Predict2-2B-Video2World", "Comfy-Org/Cosmos_Predict2_repackaged"}}

	audioVAELTX2      = Part{Kind: "audio_vae", Name: "the LTX-2 audio VAE", Names: []string{"ltx", "ltx2", "ltx-2", "audio"}, Repos: []string{"Lightricks/LTX-2.5", "Lightricks/LTX-2"}}
	audioVAEMiniMaxH3 = Part{Kind: "audio_vae", Name: "the MiniMax-H3 audio VAE, stereo 32 kHz", Names: []string{"minimax", "minimax_h3", "minimax-h3", "h3", "audio"}, Repos: []string{"Comfy-Org/MiniMax-H3"}}
	audioVAEMMAudio   = Part{Kind: "audio_vae", Name: "the MMAudio VAE", Names: []string{"mmaudio", "ovi", "audio"}, Repos: []string{"chetwinlow1/Ovi"}}

	clipL       = Part{Kind: "clip_l", Name: "OpenAI CLIP ViT-L/14 text tower, 768 wide", Width: 768, Repos: []string{"comfyanonymous/flux_text_encoders", "Comfy-Org/stable-diffusion-3.5-fp8"}}
	clipG       = Part{Kind: "clip_g", Name: "OpenCLIP ViT-bigG/14 text tower, 1280 wide", Width: 1280, Repos: []string{"Comfy-Org/stable-diffusion-3.5-fp8", "stabilityai/stable-diffusion-xl-base-1.0"}}
	clipH       = Part{Kind: "clip_h", Name: "OpenCLIP ViT-H/14 text tower, 1024 wide", Width: 1024, Repos: []string{"stabilityai/stable-diffusion-2-1"}}
	clipHunyuan = Part{Kind: "clip_l", Name: "the bilingual Hunyuan CLIP text tower", Names: []string{"hunyuan", "clip_text_encoder"}, Repos: []string{"Tencent-Hunyuan/HunyuanDiT-v1.2-Diffusers"}}

	t5XXL       = Part{Kind: "t5", Name: "T5-XXL encoder, 4096 wide", Names: []string{"t5xxl", "t5-xxl", "t5_xxl", "t5-v1_1-xxl", "t5_v1_1_xxl"}, Vocab: 32128, Width: 4096, Repos: []string{"comfyanonymous/flux_text_encoders", "Comfy-Org/stable-diffusion-3.5-fp8", "city96/t5-v1_1-xxl-encoder-gguf"}}
	umt5XXL     = Part{Kind: "t5", Name: "UMT5-XXL encoder", Names: []string{"umt5", "umt5_xxl", "umt5-xxl", "umt5xxl"}, Vocab: 256384, Width: 4096, Repos: []string{"Comfy-Org/Wan_2.1_ComfyUI_repackaged", "Comfy-Org/Wan_2.2_ComfyUI_Repackaged", "city96/umt5-xxl-encoder-gguf"}}
	flanT5Large = Part{Kind: "t5", Name: "FLAN-T5-Large encoder", Names: []string{"flan-t5-large", "flan_t5_large", "flan-t5", "t5-large"}, Vocab: 32128, Width: 1024, Repos: []string{"google/flan-t5-large"}}
	flanUL2     = Part{Kind: "t5", Name: "FLAN-UL2 encoder", Names: []string{"flan-ul2", "flan_ul2", "ul2"}, Vocab: 32128, Width: 4096, Repos: []string{"google/flan-ul2", "kandinsky-community/kandinsky-3"}}
	mT5XL       = Part{Kind: "t5", Name: "mT5-XL encoder", Names: []string{"mt5", "mt5-xl", "mt5_xl"}, Vocab: 250112, Width: 2048, Repos: []string{"Tencent-Hunyuan/HunyuanDiT-v1.2-Diffusers"}}
	byT5Small   = Part{Kind: "t5", Name: "byT5-small glyph encoder", Names: []string{"byt5", "byt5_small", "byt5-small", "glyph"}, Vocab: 384, Width: 1472, Repos: []string{"Comfy-Org/HunyuanVideo_1.5_repackaged", "google/byt5-small"}}

	llmQwen25VL7B     = Part{Kind: "llm", Name: "Qwen2.5-VL-7B-Instruct", Names: []string{"qwen2.5-vl-7b", "qwen2.5_vl_7b", "qwen_2.5_vl_7b", "qwen2_5_vl_7b", "qwen25vl7b"}, Arch: []string{"qwen2vl", "qwen2_5_vl", "qwen2.5-vl", "qwen2.5_vl"}, Params: []float64{7.6e9, 8.3e9}, Repos: []string{"Comfy-Org/Qwen-Image_ComfyUI", "Comfy-Org/HunyuanVideo_1.5_repackaged", "unsloth/Qwen2.5-VL-7B-Instruct-GGUF", "mradermacher/Qwen2.5-VL-7B-Instruct-GGUF", "Qwen/Qwen2.5-VL-7B-Instruct"}}
	llmQwen2VL7B      = Part{Kind: "llm", Name: "Qwen2-VL-7B", Names: []string{"qwen2-vl-7b", "qwen2_vl_7b", "qwen2vl7b"}, Arch: []string{"qwen2vl", "qwen2_vl"}, Params: []float64{7.6e9, 8.3e9}, Repos: []string{"alibaba-pai/EasyAnimateV5.1-12b-zh", "Qwen/Qwen2-VL-7B-Instruct"}}
	llmQwen3VL4B      = Part{Kind: "llm", Name: "Qwen3-VL-4B", Names: []string{"qwen3-vl-4b", "qwen3_vl_4b", "qwen_3_vl_4b", "qwen3vl4b"}, Arch: []string{"qwen3vl", "qwen3_vl"}, Params: []float64{4.4e9}, Repos: []string{"Comfy-Org/Krea-2", "Qwen/Qwen3-VL-4B-Instruct-GGUF", "Qwen/Qwen3-VL-4B-Instruct"}}
	llmQwen3VL8B      = Part{Kind: "llm", Name: "Qwen3-VL-8B-Instruct", Names: []string{"qwen3-vl-8b", "qwen3_vl_8b", "qwen_3_vl_8b", "qwen3vl8b"}, Arch: []string{"qwen3vl", "qwen3_vl"}, Params: []float64{8.8e9}, Repos: []string{"Comfy-Org/Qwen-Image-2.1", "Qwen/Qwen3-VL-8B-Instruct-GGUF", "unsloth/Qwen3-VL-8B-Instruct-GGUF", "Qwen/Qwen3-VL-8B-Instruct"}}
	llmQwen3VL32B     = Part{Kind: "llm", Name: "Qwen3-VL-32B truncated to 50 language layers", Names: []string{"qwen3-vl-32b", "qwen3_vl_32b", "minimax_h3", "minimax-h3", "minimax"}, Arch: []string{"qwen3vl", "qwen3_vl"}, Params: []float64{33e9, 27e9}, Repos: []string{"Comfy-Org/MiniMax-H3", "leejet/MiniMax-H3-GGUF"}}
	llmQwen3_4B       = Part{Kind: "llm", Name: "Qwen3-4B", Names: []string{"qwen3-4b", "qwen3_4b", "qwen_3_4b", "qwen3_4b_instruct", "qwen3-4b-instruct"}, Arch: []string{"qwen3"}, Params: []float64{4e9}, Repos: []string{"Comfy-Org/z_image_turbo", "Comfy-Org/flux2-klein-4B", "unsloth/Qwen3-4B-Instruct-2507-GGUF", "unsloth/Qwen3-4B-GGUF", "Qwen/Qwen3-4B"}}
	llmQwen3_8B       = Part{Kind: "llm", Name: "Qwen3-8B", Names: []string{"qwen3-8b", "qwen3_8b", "qwen_3_8b"}, Arch: []string{"qwen3"}, Params: []float64{8.2e9}, Repos: []string{"Comfy-Org/flux2-klein-9B", "unsloth/Qwen3-8B-GGUF", "Qwen/Qwen3-8B"}}
	llmQwen3_0_6B     = Part{Kind: "llm", Name: "Qwen3-0.6B", Names: []string{"qwen3-0.6b", "qwen3_0.6b", "qwen3-0_6b", "qwen3_0_6b", "qwen_3_0.6b"}, Arch: []string{"qwen3"}, Params: []float64{0.6e9}, Repos: []string{"circlestone-labs/Anima", "mradermacher/Qwen3-0.6B-Base-GGUF", "Qwen/Qwen3-0.6B"}}
	llmMistralSmall32 = Part{Kind: "llm", Name: "Mistral Small 3.2 24B", Names: []string{"mistral-small-3.2", "mistral_small_3.2", "mistral-small-3-2", "mistral_small", "mistral-small"}, Arch: []string{"mistral3", "mistral"}, Params: []float64{24e9}, Repos: []string{"black-forest-labs/FLUX.2-dev", "unsloth/Mistral-Small-3.2-24B-Instruct-2506-GGUF", "mistralai/Mistral-Small-3.2-24B-Instruct-2506"}}
	llmMinistral3_3B  = Part{Kind: "llm", Name: "Ministral 3 3B Instruct", Names: []string{"ministral-3-3b", "ministral_3_3b", "ministral3", "ministral"}, Arch: []string{"ministral", "mistral3", "mistral"}, Params: []float64{3e9}, Repos: []string{"Comfy-Org/ERNIE-Image", "unsloth/Ministral-3-3B-Instruct-2512-GGUF"}}
	llmLlama31_8B     = Part{Kind: "llm", Name: "Llama-3.1-8B-Instruct", Names: []string{"llama-3.1-8b", "llama_3.1_8b", "llama3.1-8b", "llama-3_1-8b", "llama_3_1_8b"}, Arch: []string{"llama"}, Params: []float64{8e9}, Repos: []string{"HiDream-ai/HiDream-I1-Full", "unsloth/Meta-Llama-3.1-8B-Instruct-GGUF", "meta-llama/Llama-3.1-8B-Instruct"}}
	llmLLaVALlama3    = Part{Kind: "llm", Name: "LLaVA-llama-3-8B", Names: []string{"llava_llama3", "llava-llama-3", "llava_llama_3", "llava-llama3", "llava"}, Arch: []string{"llava", "llama"}, Params: []float64{8e9}, Repos: []string{"Comfy-Org/HunyuanVideo_repackaged", "tencent/HunyuanVideo"}}
	llmGemma2_2B      = Part{Kind: "llm", Name: "Gemma-2-2B", Names: []string{"gemma-2-2b", "gemma_2_2b", "gemma2-2b", "gemma2_2b", "gemma-2-2b-it", "gemma_2_2b_it"}, Arch: []string{"gemma2"}, Params: []float64{2.6e9}, Repos: []string{"Comfy-Org/Lumina_Image_2.0_Repackaged", "Comfy-Org/PixelDiT", "unsloth/gemma-2-2b-it-GGUF", "google/gemma-2-2b-it"}}
	llmGemma3_12B     = Part{Kind: "llm", Name: "Gemma-3-12B or Gemma-4-12B with its projection layers", Names: []string{"gemma-3-12b", "gemma3-12b", "gemma_3_12b", "gemma3_12b", "gemma-4-12b", "gemma4-12b", "gemma_4_12b", "gemma4_12b", "ltx-2", "ltx2", "ltx_2"}, Arch: []string{"gemma3", "gemma4"}, Params: []float64{12e9}, Repos: []string{"Lightricks/LTX-2.5", "Lightricks/LTX-2", "unsloth/LTX-2.3-GGUF", "unsloth/gemma-3-12b-it-GGUF"}}
	llmGLM4_9B        = Part{Kind: "llm", Name: "GLM-4-9B", Names: []string{"glm-4-9b", "glm_4_9b", "glm4-9b", "glm4_9b"}, Arch: []string{"glm4", "chatglm"}, Params: []float64{9.4e9}, Repos: []string{"THUDM/CogView4-6B", "THUDM/glm-4-9b"}}
	llmChatGLM3_6B    = Part{Kind: "llm", Name: "ChatGLM3-6B", Names: []string{"chatglm3", "chatglm3-6b", "chatglm3_6b", "kolors"}, Arch: []string{"chatglm"}, Params: []float64{6.2e9}, Repos: []string{"Kwai-Kolors/Kolors"}}
	llmGPTOSS20B      = Part{Kind: "llm", Name: "gpt-oss-20b", Names: []string{"gpt-oss-20b", "gpt_oss_20b", "gptoss20b", "gpt-oss"}, Arch: []string{"gpt-oss", "gptoss", "gpt_oss"}, Params: []float64{21e9}, Repos: []string{"unsloth/gpt-oss-20b-GGUF", "openai/gpt-oss-20b"}}
	llmOvis25         = Part{Kind: "llm", Name: "the Ovis 2.5 language model", Names: []string{"ovis", "ovis2.5", "ovis_2.5", "ovis-2.5"}, Arch: []string{"qwen3", "ovis"}, Params: []float64{8.2e9}, Repos: []string{"Comfy-Org/Ovis-Image", "AIDC-AI/Ovis-Image"}}
	llmSeFi           = Part{Kind: "llm", Name: "the SeFi-Image text encoder as the release ships", Names: []string{"sefi"}}
	llmStep           = Part{Kind: "llm", Name: "Step-LLM 19B", Names: []string{"step_llm", "step-llm", "stepllm", "stepvideo"}, Params: []float64{19e9}, Repos: []string{"stepfun-ai/stepvideo-t2v"}}
	llmCosmosReason   = Part{Kind: "llm", Name: "Cosmos-Reason 1", Names: []string{"cosmos-reason", "cosmos_reason", "reason1", "cosmos"}, Repos: []string{"nvidia/Cosmos-Predict2-2B-Video2World", "Comfy-Org/Cosmos_Predict2_repackaged"}}

	clipVisionH    = Part{Kind: "clip_vision", Name: "CLIP ViT-H/14 image encoder", Names: []string{"clip_vision_h", "clip-vision-h", "vit-h", "vit_h", "vit-h-14", "open_clip", "openclip"}, Width: 1280, Repos: []string{"Comfy-Org/Wan_2.1_ComfyUI_repackaged", "Comfy-Org/sigclip_vision_384", "stabilityai/stable-video-diffusion-img2vid-xt-1-1"}}
	clipVisionBigG = Part{Kind: "clip_vision", Name: "CLIP ViT-bigG/14 image encoder", Names: []string{"clip_vision_g", "clip-vision-g", "bigg", "vit-bigg", "vit_bigg"}, Width: 1664, Repos: []string{"stabilityai/stable-cascade", "kandinsky-community/kandinsky-2-2-prior"}}

	audioWav2Vec2 = Part{Kind: "audio_encoder", Name: "wav2vec2 large", Names: []string{"wav2vec2", "wav2vec", "wav2vec2_large"}, Repos: []string{"Comfy-Org/Wan_2.2_ComfyUI_Repackaged", "facebook/wav2vec2-large-xlsr-53"}}

	connectorsLTX2 = Part{Kind: "embeddings_connectors", Name: "the LTX-2 embeddings connectors", Names: []string{"connector", "connectors", "ltx"}, Repos: []string{"unsloth/LTX-2.3-GGUF", "Lightricks/LTX-2.5"}}

	tokenizerGPTOSS = Part{Kind: "tokenizer", Name: "the gpt-oss tokenizer.json", Names: []string{"gpt-oss", "gpt_oss", "gptoss"}, Arch: []string{"gpt-oss", "gptoss", "gpt_oss"}, Repos: []string{"openai/gpt-oss-20b"}}
	tokenizerGemma2 = Part{Kind: "tokenizer", Name: "the Gemma-2-2B tokenizer.json", Names: []string{"gemma-2-2b", "gemma_2_2b", "gemma2-2b", "gemma2"}, Arch: []string{"gemma2"}, Repos: []string{"google/gemma-2-2b", "unsloth/gemma-2-2b-it"}}

	taesd = Part{Kind: "taesd", Name: "TAESD, TAESDXL, TAESD3, TAEF1, or TAEHV", Names: []string{"taesd", "taesdxl", "taesd3", "taef1", "taehv", "taew2.1", "tae"}, Repos: []string{"madebyollin/taesd", "madebyollin/taesdxl", "madebyollin/taesd3", "madebyollin/taef1", "madebyollin/taehv"}}
)

// Reports whether the group is the low noise expert of a paired denoiser.
func lowNoise(_ diffusion.Profile, group string) bool { return HighOf(group) != "" }

// Returns the high noise expert name, or empty if the name has no noise level.
func HighOf(group string) string {
	lower := lower(group)
	for _, pair := range [][2]string{{"low_noise", "high_noise"}, {"lownoise", "highnoise"}, {"low-noise", "high-noise"}} {
		if i := indexOf(lower, pair[0]); i >= 0 {
			return lower[:i] + pair[1] + lower[i+len(pair[0]):]
		}
	}
	return ""
}
