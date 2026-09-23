# Blueprints

A blueprint defines a pipeline's required slots, optional slots, and adapters. Files or directories fill slots. Adapters patch their weights. A run needs every required slot filled.

## Slots

| Slot | Purpose | Files |
| --- | --- | --- |
| `denoiser` | Predicts noise, velocity, or flow in latent or pixel space. UNet, DiT, MMDiT, or packed transformer | single safetensors, `diffusion_models/*.safetensors` split file, `unet/` or `transformer/` diffusers subfolder, GGUF |
| `denoiser.high_noise` | High noise expert, selected by timestep | as `denoiser` |
| `denoiser.uncond` | Unconditional branch of classifier free guidance | as `denoiser` |
| `refiner` | Second denoiser for the final part of the schedule | as `denoiser` |
| `stage.prior` | Maps text to an image embedding or coarse latent | as `denoiser` |
| `stage.decoder` | Decodes the prior's output to a full latent or image | as `denoiser` |
| `stage.upscaler` | Raises the resolution of the previous stage's output | as `denoiser` |
| `vae` | Encodes and decodes between pixels and latents | single safetensors, `vae/` subfolder, `vae/*.safetensors` split file |
| `vae.audio` | Encodes and decodes between audio and latents | as `vae` |
| `vae.tiny` | Preview and low memory decoder for the same latent space | `taesd*.safetensors`, `taef1`, `taesd3`, `taehv` |
| `text_encoder.clip_l` | OpenAI CLIP ViT-L/14 text tower, 768 wide, penultimate layer, pooled output | `clip_l.safetensors`, `text_encoder/` |
| `text_encoder.clip_g` | OpenCLIP ViT-bigG/14 text tower, 1280 wide, penultimate layer, pooled output | `clip_g.safetensors`, `text_encoder_2/` |
| `text_encoder.clip_h` | OpenCLIP ViT-H/14 text tower, 1024 wide | `text_encoder/` |
| `text_encoder.t5` | A T5 family encoder: T5-XXL, UMT5-XXL, FLAN-T5, byT5, mT5, FLAN-UL2 | `t5xxl_*.safetensors`, `umt5_xxl_*.safetensors`, GGUF, `text_encoder_3/` |
| `text_encoder.llm` | Hidden states from selected layers of a decoder-only language model | `text_encoders/*.safetensors`, GGUF, `text_encoder/` |
| `text_encoder.llm.vision` | Vision tower and projector of a vision-language text encoder | `mmproj-*.gguf`, `llm_vision`, bundled in the encoder file |
| `text_encoder.glyph` | Encodes rendered text using byT5 or glyph CLIP | `text_encoders/byt5_*.safetensors` |
| `image_encoder.clip_vision` | CLIP or SigLIP image embeddings for conditioning | `clip_vision_h.safetensors`, `clip_vision_g.safetensors`, `image_encoder/`, `sigclip_vision_patch14_384.safetensors` |
| `image_encoder.dino` | DINOv2 patch features for 3D and reference pipelines | `image_encoder/` |
| `audio_encoder` | Audio conditioning via wav2vec2, Whisper, HuBERT, CLAP, or Synchformer | `audio_encoders/*.safetensors` |
| `face_encoder` | Identity embeddings via InsightFace ArcFace, antelopev2, buffalo_l, or EVA-CLIP | `.onnx` bundle, `.safetensors` |
| `tokenizer` | Text tokenizer for an encoder or language model | `tokenizer.json`, `tokenizer.model`, `tokenizer_config.json`, `special_tokens_map.json`, `vocab.json` + `merges.txt`, `spiece.model` |
| `connector` | Projection layers between an encoder and the denoiser, shipped separately | `embeddings_connectors`, `*_with_proj_*.safetensors` |
| `weights` | Parameters of a standalone language, embedding, speech, or vision model | safetensors shards + `model.safetensors.index.json`, GGUF, `.bin`, `.pt`, `.nemo`, `.onnx`, MLX directory |
| `config` | Architecture of `weights` | `config.json`, `params.json`, `generation_config.json`, `preprocessor_config.json`, `processor_config.json`, `chat_template.jinja` |
| `projector` | Separately packaged vision or audio projector for a multimodal language model | `mmproj-*.gguf` |
| `draft` | Smaller model with the same tokenizer for speculative decoding | as `weights` |
| `codec` | Converts between tokens or mel and waveform | `.safetensors`, `.pt`, `.onnx` |
| `speaker` | Speaker embedding, style vector, or reference clip | `.pt`, `.npy`, `.wav` |
| `preprocessor` | Extracts depth, pose, edges, normals, segmentation, or lineart for control | `.pth`, `.onnx`, `.safetensors` |
| `detector` | Finds regions for a second pass | `.pt` YOLO, SAM `.pth` |
| `upscaler` | Pixel super resolution after decoding: ESRGAN, RealESRGAN, SwinIR, HAT, DAT, SPAN, OmniSR | `.pth`, `.safetensors` |
| `safety` | Classifies decoded outputs | `safety_checker/`, `feature_extractor/` |

## Adapters

| Adapter | Target | Files |
| --- | --- | --- |
| `lora` | Low rank pairs on `denoiser`, `text_encoder`, or `weights` linear layers. Names: `lora_A`/`lora_B`, `lora_down`/`lora_up`, or `lora_unet_*`/`lora_te_*`, with `alpha` scalars | single safetensors, `adapter_model.safetensors` + `adapter_config.json` |
| `lycoris` | LoHa, LoKr, DyLoRA, GLoRA, full dimension `diff` blocks, and IA3 on the same layers as `lora` | single safetensors |
| `dora` | Weight decomposed LoRA: a `lora` plus `dora_scale` magnitude vectors | single safetensors |
| `model_patch` | A dense delta applied to a `denoiser`: a ControlNet union patch, a Fun ControlNet, a relight patch | `model_patches/*.safetensors` |
| `controlnet` | Injects a `preprocessor` signal through a copy of the denoiser's encoder half or a small control adapter | `control_*.safetensors`, `controlnet/` |
| `t2i_adapter` | A light encoder that adds a spatial signal at the denoiser's down blocks | `t2iadapter_*.safetensors` |
| `ip_adapter` | Injects `image_encoder.clip_vision` embeddings through cross attention. Variants: Plus, Plus-Face, FaceID | `ip-adapter*.safetensors`, `ip-adapter*.bin` |
| `photo_maker` | An ID encoder fusing face crops into the text embedding, plus a `lora` | `photomaker-v*.bin` |
| `instant_id` | A `face_encoder`, an image projection into cross attention, and a keypoint `controlnet` | `ip-adapter.bin` + `ControlNetModel/` |
| `pulid` | EVA-CLIP plus InsightFace identity, cross attention adapters | `pulid_*.safetensors` |
| `redux` | A SigLIP `image_encoder` and projector that turns an image into T5 style tokens | `flux1-redux-dev.safetensors` |
| `motion_module` | Temporal attention inserted between the blocks of an image denoiser | `mm_sd_v15_v2.ckpt`, `mm_sdxl_v10_beta.ckpt`, `v3_sd15_mm.ckpt` |
| `motion_lora` | A `lora` on a `motion_module` for camera motion | single safetensors |
| `embedding` | Textual inversion vectors for one or more text encoders. Keys: `string_to_param`, `emb_params`, or `clip_l` and `clip_g` | `.pt`, `.safetensors` |
| `hypernetwork` | Small networks that modulate attention keys and values of an SD1 or SD2 denoiser | `.pt` |
| `control_vector` | Activation additions to a language model's residual stream | `.gguf` |
| `mtp_head` | Extra prediction heads for multi token prediction, stored past the last layer | bundled in `weights` |

## Settings

Settings default to family values and can be overridden per run.

| Setting | Values |
| --- | --- |
| `prediction` | `eps`, `v`, `edm_v`, `sd3_flow`, `flux_flow`, `wan_flow`, `ltx_flow`, family specific flow variants |
| `schedule` | `discrete`, `karras`, `exponential`, `ays`, `gits`, `sgm_uniform`, `simple`, `kl_optimal`, `beta`, `flux`, `flux2`, `ltx2`, `logit_normal`, `lcm`, custom sigmas |
| `sampler` | `euler`, `euler_a`, `heun`, `dpm2`, `dpm++2s_a`, `dpm++2m`, `dpm++2m_sde`, `dpm++3m_sde`, `ipndm`, `lcm`, `ddim`, `tcd`, `res_multistep`, `res_2s`, `er_sde`, `unipc`, `lms` |
| `guidance` | classifier free guidance scale, distilled guidance scale, image guidance scale, skip layer guidance, perturbed attention guidance, adaptive projected guidance |
| `steps` | sampling steps, fixed at 1, 2, 4, or 8 for distilled variants |
| `shift` | flow matching timestep shift |
| `clip_skip` | which layer of a CLIP text encoder to read |
| `size` | width, height, frames, fps, latent frame packing |
| `cache` | step caching across the denoiser: TeaCache, EasyCache, MagCache, FBCache, TaylorSeer, DBCache |
| `precision` | weight dtype, activation dtype, quantization, per tensor type rules |
| `placement` | device per slot, host offload, mmap, split mode |

## File forms

| Form | Holds | Marker |
| --- | --- | --- |
| transformers directory | `weights` shards, `config`, `tokenizer`, processor configs | `config.json` |
| diffusers directory | one subfolder per slot: `transformer/` or `unet/`, `vae/`, `text_encoder*/`, `tokenizer*/`, `scheduler/`, `image_encoder/`, `feature_extractor/`, `safety_checker/` | `model_index.json` |
| ComfyUI split files | one file per slot under `diffusion_models/`, `vae/`, `text_encoders/`, `clip_vision/`, `audio_encoders/`, `loras/`, `embeddings/`, `model_patches/`, `controlnet/`, `upscale_models/` | repository name ends in `ComfyUI`, `Comfy-Org` organisation |
| single file checkpoint | a `denoiser` alone, or a `denoiser` with `vae` and text encoders bundled under `first_stage_model.`, `cond_stage_model.`, `conditioner.`, `text_encoders.` prefixes | one `.safetensors`, `.ckpt`, `.sft` |
| GGUF | one slot per file, original tensor names, `Q2_K` through `Q8_0`, `F16`, `BF16`, `F32`. Language model headers include `config` and `tokenizer`. Projectors use `mmproj-*.gguf` | `.gguf` |
| PEFT adapter directory | a `lora` on `weights` | `adapter_config.json` + `adapter_model.safetensors` |
| sentence-transformers directory | `weights`, `tokenizer`, pooling and normalisation modules | `modules.json`, `1_Pooling/config.json` |
| NeMo archive | `weights`, `config`, `tokenizer` in one tar | `.nemo` |
| ONNX directory | exported graph and external data | `model.onnx`, `model.onnx_data` |
| MLX directory | `weights` shards and `config` in MLX layout | `weights.*.safetensors`, `config.json` |
| CTranslate2 directory | converted `weights` for faster-whisper and OPUS translation | `model.bin`, `vocabulary.json` |
| TensorRT engine | a compiled graph tied to one device and driver | `.engine`, `.plan` |
| Core ML package | a compiled graph for Apple devices | `.mlpackage`, `.mlmodelc` |
| ExLlama directory | `weights` quantized to EXL2 or EXL3 | `config.json` + `.safetensors` with `q_weight` tensors |
| quantized transformers directory | `weights` quantized with GPTQ, AWQ, bitsandbytes, FP8, NVFP4, MXFP4, compressed-tensors, TorchAO, HQQ | `quantization_config` in `config.json` |
| Nunchaku directory | a `denoiser` quantized with SVDQuant int4 or fp4 plus low rank branches | `transformer_blocks.*.qweight` |

# Text

## Causal language model

A decoder-only transformer generates text one token at a time.

- `weights`: decoder stack, embeddings, final norm, tied or untied output head
- `config`: architecture, vocabulary size, context length, rope parameters, attention layout, quantization
- `tokenizer`: BPE, SentencePiece, or tiktoken vocabulary and chat template for turns, system prompts, tool calls, and reasoning spans
- `draft`: a smaller model sharing the tokenizer, for speculative decoding
- `mtp_head`: bundled multi token prediction heads, used as a self draft
- `lora`: PEFT adapters on attention and MLP projections, applied at load or served per request
- `control_vector`: steering additions per layer
- Settings: temperature, top-k, top-p, min-p, typical-p, repetition and presence penalties, DRY, mirostat, grammar or JSON schema constraints, stop strings, max tokens, context length, KV cache dtype and quantization, prompt caching, batch size, parallel sequences, tensor and pipeline parallelism, expert parallelism, flash attention
- Uses: chat completions, completions, tokenize, embeddings of hidden states, logprobs, tool calling, structured output

### Families

| Family | Tokenizer | Attention | Experts | Architecture notes |
| --- | --- | --- | --- | --- |
| Llama 1, 2 | SentencePiece BPE | MHA (7B, 13B), GQA (70B) | dense | rope, SwiGLU, RMSNorm |
| Llama 3, 3.1, 3.2, 3.3 | tiktoken 128k | GQA | dense | rope with llama3 scaling, 128k context |
| Llama 4 Scout, Maverick | tiktoken | GQA, interleaved rope and no-rope layers, chunked attention | 16 and 128 routed experts plus a shared expert | early fusion vision |
| Mistral 7B, Small, Medium, Large, Ministral 3 | SentencePiece then tekken tiktoken | GQA, sliding window | dense | |
| Mixtral 8x7B, 8x22B | SentencePiece | GQA | 8 experts, top 2 | |
| Magistral, Devstral, Codestral | tekken | GQA | dense | reasoning and code tunes of Mistral |
| Qwen 1.5, 2, 2.5 | tiktoken style BPE 151k | GQA, qkv bias | dense, Qwen2-MoE and Qwen2.5-MoE with shared expert | |
| Qwen 3 | BPE 151k | GQA, q and k norm | dense 0.6B to 32B, 30B-A3B and 235B-A22B with 128 experts top 8 | thinking mode toggled by template |
| Qwen 3 Next | BPE | hybrid gated DeltaNet and gated attention | 512 experts top 10 plus shared | MTP heads |
| Gemma 1, 2 | SentencePiece 256k | MQA (1), GQA with alternating local and global (2) | dense | logit soft capping, GeGLU, tied embeddings |
| Gemma 3 | SentencePiece 262k | GQA, 5 local per 1 global, 128k context | dense | vision through SigLIP |
| Gemma 3n | SentencePiece | per layer embeddings, KV sharing | dense with MatFormer nesting | audio and vision towers bundled |
| Phi 2, 3, 3.5, 4, Phi-4-mini | GPT-2 BPE then tiktoken | MHA (2), GQA | dense, Phi-3.5-MoE 16 experts top 2 | partial rope, fused qkv |
| DeepSeek V2, V2.5 | BPE | MLA with compressed KV, decoupled rope | 160 experts top 6 plus 2 shared | |
| DeepSeek V3, R1, V3.1, V3.2 | BPE 129k | MLA, sparse attention indexer in V3.2 | 256 experts top 8 plus 1 shared, sigmoid routing | MTP head, FP8 native weights |
| DeepSeek Coder, Math, Distill | Llama or Qwen tokenizers | as base | as base | |
| gpt-oss 20B, 120B | o200k tiktoken | GQA, alternating sliding window, attention sinks | 32 and 128 experts top 4 | MXFP4 expert weights, harmony chat format, reasoning effort |
| GPT-2, GPT-J, GPT-NeoX, Pythia, OPT, BLOOM | GPT-2 BPE | MHA, learned or rotary positions | dense | |
| Falcon 7B, 40B, 180B, Falcon 3, Falcon-H1 | BPE | MQA, GQA, H1 hybrid Mamba | dense | |
| Granite 3, 3.1, 3.3, 4 | GPT-NeoX BPE | GQA, Granite 4 hybrid Mamba-2 and attention | 4 dense and MoE sizes | |
| OLMo 1, 2, 3, OLMoE | GPT-NeoX BPE | MHA, GQA | OLMoE 64 experts top 8 | |
| Command R, R+, R7B, A | BPE 256k | GQA, parallel attention and MLP, sliding window (R7B) | dense | |
| GLM 4, 4.5, 4.6, 4.7, ChatGLM | BPE | GQA, partial rope | GLM 4.5 and later 128 experts plus shared | MTP head |
| InternLM 2, 2.5, 3 | SentencePiece | GQA, fused wqkv | dense | |
| Yi, Yi 1.5 | SentencePiece 64k | GQA | dense | |
| Nemotron 4, Nemotron-H, Nemotron 3 | SentencePiece or tiktoken | GQA, H hybrid Mamba-2 | Nemotron 3 MoE | squared ReLU MLP |
| MiniMax-Text-01, M1, M2 | BPE | lightning linear attention interleaved with softmax | 32 and 256 experts | |
| Kimi K2, K2 Thinking | BPE 160k | MLA | 384 experts top 8 plus 1 shared | MuonClip trained, INT4 QAT release |
| Hunyuan Large, A13B, 7B | BPE | GQA, cross layer attention (Large) | 1 shared plus 64 routed experts | |
| ERNIE 4.5 | BPE | GQA | 64 text and 64 vision experts, modality routing | |
| Seed-OSS 36B | BPE | GQA | dense | thinking budget |
| SmolLM 2, 3 | GPT-2 BPE | GQA, no-rope every 4th layer in SmolLM3 | dense | |
| StableLM 2, Zephyr | Arcade100k BPE | MHA, parallel blocks | dense | |
| Baichuan 2 | SentencePiece | MHA with ALiBi (13B) | dense | |
| Aya 23, Aya Expanse | Cohere BPE | GQA | dense | |
| Jamba, Jamba 1.5 | SentencePiece | 1 attention per 7 Mamba blocks | 16 experts top 2 | |
| Mamba, Mamba-2, Codestral Mamba | GPT-NeoX BPE | selective state space, no attention | dense | recurrent state instead of KV cache |
| RWKV 4, 5, 6, 7 | RWKV world vocabulary | linear attention with time mixing | dense | |
| xLSTM 7B | BPE | sLSTM and mLSTM blocks | dense | |
| Zamba, Zamba 2 | Mistral tokenizer | shared attention block over Mamba | dense | |
| Exaone 3, 3.5, 4 | BPE | GQA | dense | |
| Arcee, Solar, Upstage | Llama tokenizer | GQA, depth upscaled | dense | |
| Bielik, EuroLLM, Salamandra, Teuken, Viking | regional BPE | GQA | dense | |
| Reka Flash, Reka Edge | BPE | GQA | dense | |
| Dots.llm1, Ling, Ring, MiMo, Pangu, Step 3, Longcat Flash | BPE | GQA or MLA | MoE with shared experts | zero computation experts (Longcat) |
| TinyLlama, OpenLLaMA, Vicuna, WizardLM, Hermes, Dolphin, OpenChat, Nous, Orca | as base | as base | as base | instruction tunes of Llama and Mistral |

### Quantized forms of `weights`

| Form | Tool | Notes |
| --- | --- | --- |
| GGUF `Q2_K` to `Q8_0`, `IQ1_S` to `IQ4_XS`, `TQ1_0`, `TQ2_0` | llama.cpp | k-quants with per block scales, i-quants with importance matrix, ternary |
| GPTQ 4-bit, 8-bit | AutoGPTQ, GPTQModel | group size, act order, desc act, marlin kernels |
| AWQ 4-bit | AutoAWQ, llm-compressor | activation aware scaling, GEMM and GEMV layouts |
| EXL2, EXL3 | exllamav2, exllamav3 | mixed bits per weight to a target average |
| bitsandbytes NF4, FP4, int8 | transformers | double quantization, compute dtype |
| FP8 e4m3, e5m2 | vLLM, SGLang, TensorRT-LLM | per tensor or per block scales, KV cache in FP8 |
| NVFP4, MXFP4 | vLLM, TensorRT-LLM | 4-bit floating point with block scales, Blackwell kernels |
| compressed-tensors | llm-compressor | W8A8 int8, W4A16, 2:4 sparsity |
| HQQ, TorchAO, Quanto | transformers | int2 to int8 without calibration |
| MLX 4-bit, 8-bit | mlx-lm | group quantization for Apple silicon |
| ONNX int4, int8 | Olive, onnxruntime-genai | DirectML, CPU, NPU targets |

## Vision-language model

A vision tower projects image or video features into a language model's embedding space for text generation.

- `weights`: the language model
- `image_encoder`: vision tower, bundled in transformers `weights` or GGUF `projector`
- `projector`: maps vision features to language embeddings using an MLP, perceiver resampler, pixel shuffle, cross attention, or DeepStack mergers
- `config`: the processor config with image size, patch size, tiling, min and max pixels, video frame sampling
- `tokenizer`: with image, video, and box tokens
- Settings: as causal language model, plus max image tiles, max pixels, frames per video, fps sampling, image token budget

| Family | Vision tower | Projector | Notes |
| --- | --- | --- | --- |
| LLaVA 1.5, 1.6 NeXT, OneVision, Video | CLIP ViT-L/14 336, SigLIP So400m | 2 layer MLP | AnyRes tiling |
| Qwen2-VL, Qwen2.5-VL | native resolution ViT with 2D rope, window attention (2.5) | patch merger 2x2 | absolute time rope for video, boxes in text |
| Qwen3-VL | SigLIP2 style ViT | DeepStack merging into 3 language layers | interleaved MRoPE, video timestamps |
| Llama 3.2 Vision | ViT-H | cross attention layers every 4th block | image tokens through gated cross attention |
| Gemma 3 | SigLIP 400M | average pooling to 256 tokens | pan and scan tiling |
| PaliGemma 1, 2 | SigLIP So400m | linear | prefix full attention over image |
| Pixtral, Mistral Small 3.1, 3.2, Medium 3 | Pixtral ViT with 2D rope | MLP with break tokens | native resolution |
| InternVL 2, 2.5, 3, 3.5 | InternViT 300M, 6B | pixel shuffle then MLP | dynamic tiling to 448 |
| MiniCPM-V 2.6, 4, 4.5, MiniCPM-o | SigLIP So400m | perceiver resampler | slice tiling, video through 3D resampler |
| Phi-3.5 Vision, Phi-4 multimodal | CLIP ViT-L/14 336 | HD transform, MLP | LoRA mixture per modality (Phi-4) |
| Idefics 2, 3, SmolVLM | SigLIP So400m | perceiver (2), pixel shuffle (3, Smol) | |
| Molmo | CLIP ViT-L/14 336 | MLP with overlapping crops | pointing |
| Florence-2 | DaViT | encoder-decoder over prompt tokens | detection, segmentation, OCR, captioning as text |
| Kimi-VL | MoonViT native resolution | MLP | MoE language model |
| GLM-4.1V, GLM-4.5V | AIMv2 style ViT | MLP | thinking mode |
| DeepSeek-VL2 | SigLIP So400m | MLP | MoE language model, dynamic tiling |
| Ovis 2, 2.5 | SigLIP or NaViT | visual embedding table by probabilistic tokens | |
| Llama 4 | MetaCLIP ViT | MLP | early fusion |
| Gemma 3n | MobileNet-V5 | | |
| Granite Vision, Aya Vision, Ministral 3, Magistral | SigLIP | MLP | |
| NVLM, Eagle 2, Cosmos-Reason | InternViT, SigLIP, ConvNeXt | tile tagging | |
| BLIP-2, InstructBLIP | EVA ViT-g | Q-Former | |
| CogVLM, CogVLM2, GLM-4V | EVA-CLIP | visual expert layers | |
| Fuyu | none, patches embedded directly | linear | |
| Kosmos-2, 2.5 | CLIP ViT-L | resampler | grounding, OCR |
| Chameleon, Anole, Emu3 | VQ image tokenizer | shared vocabulary | generates images too |
| GOT-OCR 2, Nougat, TrOCR, Donut, Pix2Struct, UDOP, LayoutLM | ViT, Swin, DaViT | encoder-decoder | document text and layout |

Uses: chat completions with image and video content parts, grounding, OCR, document parsing.

## Audio-language model

An audio encoder projects speech or sound features into a language model's embedding space for text generation.

- `weights`: the language model
- `audio_encoder`: Whisper, wav2vec2, or a custom conformer, bundled in `weights` or GGUF `projector`
- `projector`: stacked frames through an MLP or a Q-Former
- `config`: the feature extractor with sample rate, mel bins, window length
- `tokenizer`: with audio placeholder tokens

| Family | Audio encoder | Notes |
| --- | --- | --- |
| Qwen2-Audio, Qwen2.5-Omni thinker, Qwen3-Omni | Whisper large encoder | speech, sound, music understanding |
| Voxtral Mini, Small | Whisper style encoder | Mistral language model, transcription mode |
| Ultravox | Whisper encoder | Llama or Gemma language model, projector only trained |
| Kimi-Audio | Whisper plus semantic tokenizer | generates speech tokens too |
| Step-Audio 2, GLM-4-Voice, Baichuan-Audio | speech tokenizer | speech in and out through a flow decoder |
| Gemma 3n | USM based encoder | |
| Phi-4 multimodal | conformer | speech LoRA |
| SALMONN, Audio Flamingo 2, 3 | Whisper plus BEATs | sound and music |
| Moshi, Hibiki | Mimi codec | full duplex speech, Helium language model |

## Omni model

Generates text and speech from text, images, video, or audio.

- `weights`: the thinker, a vision-language and audio-language model
- `weights.talker`: a second decoder that emits speech tokens from the thinker's hidden states
- `codec`: turns speech tokens into waveform, a DiT plus vocoder or a codec decoder
- Families: Qwen2.5-Omni and Qwen3-Omni (thinker, talker, Token2Wav DiT with BigVGAN), MiniCPM-o (SigLIP, Whisper, CosyVoice decoder), Baichuan-Omni, VITA, Ming-Omni, Ola

## Encoder-decoder model

An encoder and a decoder with cross attention transform text.

- `weights`: encoder, decoder, shared embeddings
- `config`, `tokenizer`
- Families: T5, FLAN-T5, UL2, FLAN-UL2, mT5, umT5, byT5, T5Gemma, BART, mBART, Pegasus, Marian OPUS-MT, NLLB-200, M2M-100, SeamlessM4T text, MADLAD-400, ProphetNet, LED, LongT5, CodeT5, Switch Transformer
- Uses: translation, summarization, text2text generation, and as `text_encoder.t5` in diffusion blueprints

## Encoder-only model

Produces labels, spans, or vectors from text.

- `weights`: encoder stack with a task head
- `config`, `tokenizer`
- Heads: sequence classification, token classification, extractive question answering, masked language modeling, multiple choice, next sentence prediction
- Families: BERT, RoBERTa, DeBERTa v2 and v3, ELECTRA, ALBERT, DistilBERT, XLM-R, mDeBERTa, CamemBERT, ModernBERT, EuroBERT, NeoBERT, Longformer, BigBird, Funnel, CANINE, ByT5 encoder, Nomic BERT, GTE BERT, Jina BERT, MosaicBERT
- Uses: text classification, sentiment, NER, POS, zero-shot classification through NLI, fill-mask, extractive QA, table QA (TAPAS), toxicity and moderation classifiers

## Embedding model

Encodes text as a fixed length vector.

- `weights`: an encoder or decoder backbone
- `config`, `tokenizer`
- Pooling: CLS, mean, last token, or weighted mean, set in `1_Pooling/config.json`
- Normalisation and dimension truncation: Matryoshka dimensions, binary and int8 quantized outputs
- Instructions: query and document prefixes, task instructions in the prompt
- Families: sentence-transformers all-MiniLM and all-mpnet, BGE 1.5, BGE-M3 (dense, sparse, ColBERT heads), GTE, GTE-Qwen2, E5, multilingual-E5, E5-Mistral, Nomic Embed 1, 1.5, 2 MoE, Jina Embeddings 2, 3, 4, Arctic Embed, mxbai-embed, Stella, NV-Embed, SFR-Embedding, Qwen3-Embedding, EmbeddingGemma, Voyage open weights, Granite Embedding, LaBSE, Instructor, GritLM, Linq-Embed, KaLM, Snowflake, Cohere embed open weights
- Uses: embeddings, sentence similarity, retrieval, clustering, classification by nearest centroid

## Reranker

Scores a document's relevance to a query.

- `weights`: a cross encoder, or a decoder scored on a yes token
- `config`, `tokenizer`
- Families: BGE Reranker v2 (M3, Gemma, MiniCPM), Jina Reranker 1, 2, Qwen3-Reranker, mxbai-rerank, MS MARCO MiniLM cross encoders, ColBERT rerank mode, monoT5, RankZephyr, RankGPT open weights, Cohere rerank open weights, LiT5

## Late interaction retriever

Encodes query and document tokens as vectors, scored by MaxSim.

- `weights`: an encoder with a linear projection to 128 dimensions
- `config`, `tokenizer`
- Families: ColBERT v2, ColBERTv2 PLAID, Jina ColBERT v2, answerai-colbert-small, ColPali and ColQwen2 (documents as page images through a vision-language backbone), ColSmol, ColModernVBERT

## Sparse retriever

Encodes text as a sparse vector weighted over the vocabulary.

- `weights`: a masked language model head over the vocabulary
- Families: SPLADE v2, v3, SPLADE++ ensemble, opensearch neural sparse, BGE-M3 sparse head, uniCOIL

## Speculative decoding pair

A target causal language model and a faster proposer sharing its tokenizer.

- `weights`: the target
- `draft`: a smaller model of the same family, or n-gram lookup, or prompt lookup
- `mtp_head`: the target's own multi token heads
- `weights.medusa`, `weights.eagle`: decoding heads trained on target hidden states (EAGLE 1, 2, 3, Medusa 1, 2, HASS, Hydra)
- Settings: draft tokens per step, acceptance threshold, tree width

## Reward, guard, and judge model

Scores or labels text and conversations.

- `weights`: a decoder with a scalar head, or a classifier, or a chat model prompted to judge
- Families: Llama Guard 1 to 4, ShieldGemma 1, 2, Granite Guardian, WildGuard, Nemotron Safety Guard, Qwen3Guard, Skywork Reward, ArmoRM, INF-ORM, Nemotron Reward, RM-R1, Prometheus 2, JudgeLM, GRM, PairRM, OpenAssistant reward models, Detoxify, toxic-bert, NSFW text classifiers, prompt injection classifiers (ProtectAI DeBERTa, PromptGuard 1, 2)

## Diffusion language model

Generates text by repeatedly unmasking or denoising a sequence.

- `weights`: a bidirectional transformer over masked tokens
- `config`, `tokenizer`
- Settings: denoising steps, block length, remasking strategy, confidence threshold
- Families: LLaDA 1, 1.5, 2.0, LLaDA-V, Dream 7B, DiffuCoder, Mercury open weights, SEDD, MDLM, Gemini Diffusion open weights, Fast-dLLM caches

## Code model

A causal language model trained for code with fill in the middle tokens.

- `weights`, `config`, `tokenizer` with prefix, suffix, middle, and file separator tokens
- Families: Qwen2.5-Coder, Qwen3-Coder, DeepSeek-Coder V2, Codestral, Devstral, StarCoder 2, CodeLlama, CodeGemma, Granite Code, Yi-Coder, Seed-Coder, Kimi-Dev, GLM-4.6 coder, gpt-oss for code, OpenCoder, CodeQwen, WizardCoder, Magicoder, Refact, Replit code, SantaCoder, CodeGen
- Uses: completions with fill in the middle, chat, repository level context, agentic tool use

## Long context and memory forms

- Context extension: rope scaling (linear, NTK, YaRN, dynamic NTK, LongRoPE), ALiBi, no-rope layers, sliding windows, attention sinks
- KV cache: full, quantized (q8_0, q4_0, FP8), paged, prefix shared, offloaded to host, SWA rolling
- Recurrent state: Mamba, Mamba-2, RWKV, DeltaNet, GLA, Lightning attention, xLSTM carry a state instead of a growing cache

## Other text pipelines

| Pipeline | Slots | Families |
| --- | --- | --- |
| Named entity recognition | encoder-only with token head, GLiNER span matching to label embeddings | GLiNER, spaCy transformers, Flair, BERT-NER, UniNER |
| Relation and event extraction | encoder-only or generative | REBEL, GLiREL, UIE |
| Keyword and topic | embedding model plus clustering | KeyBERT, BERTopic |
| Grammar and spelling | encoder-decoder | Grammarly CoEdIT, T5 grammar |
| Text detoxification and style transfer | encoder-decoder | ParaDetox, BART paraphrase |
| Language identification | classifier | fastText lid, XLM-R lid, GlotLID |
| Tokenizer only repositories | `tokenizer` alone | tiktoken exports, SentencePiece models |
| Datasets and benchmarks | no slots (evaluation inputs) | |

# Image

## Latent diffusion text-to-image

Text encoders embed the prompt. The denoiser converts noise to a latent using a schedule and guidance. The VAE decodes the image.

- `denoiser`, required
- `vae`, required, may be bundled
- family text encoders, required, may be bundled
- `image_encoder.clip_vision`, when an image conditions the run
- `vae.tiny`, for previews and low memory decoding
- `refiner`, `denoiser.high_noise`, `denoiser.uncond`, when the family is staged
- Adapters: `lora`, `lycoris`, `dora`, `embedding`, `controlnet`, `t2i_adapter`, `ip_adapter`, `photo_maker`, `instant_id`, `pulid`, `redux`, `model_patch`, `hypernetwork`
- Post: `upscaler`, `detector` for a second detail pass, `safety`
- Settings: `prediction`, `schedule`, `sampler`, `steps`, `guidance`, `shift`, `clip_skip`, `size`, `cache`, `precision`, `placement`

### Stable Diffusion 1.x

- Output: 512px images, inpainting, depth, and upscaler variants
- `denoiser`: UNet, 860M, 4 latent channels, cross attention at 768
- `vae`: kl-f8, 4 channels, 8x downsampling, `vae-ft-mse` and `vae-ft-ema` replacements
- `text_encoder.clip_l`: OpenAI CLIP ViT-L/14, layer chosen by `clip_skip`
- Bundled single files carry all three under `model.diffusion_model.`, `first_stage_model.`, `cond_stage_model.transformer.`
- Inpainting variant takes 9 input channels: latent, mask, masked latent
- Settings: `eps` prediction, discrete schedule, 20 to 30 steps, guidance 7, `euler_a` or `dpm++2m`
- Adapters: `lora` on attention and MLP of both `denoiser` and `text_encoder.clip_l`, `lycoris`, `embedding` of 768 wide vectors, `hypernetwork`, `controlnet` per signal (canny, depth, openpose, lineart, softedge, scribble, seg, normal, mlsd, shuffle, tile, inpaint, ip2p, qrcode), `t2i_adapter`, `ip_adapter` with `image_encoder.clip_vision` ViT-H, `motion_module` for video. `photo_maker` requires SDXL
- Distilled variants: LCM (`lcm` sampler, 4 to 8 steps, guidance 1 to 2, as a `lora` or a merged `denoiser`), Hyper-SD, TCD, PCM, DMD2, SD-Turbo (SD2.1 based), Nitro-SD, LCM-LoRA
- Canonical: `stable-diffusion-v1-5/stable-diffusion-v1-5`, `runwayml/stable-diffusion-inpainting`, `stabilityai/sd-vae-ft-mse-original`

### Stable Diffusion 2.x

- Output: 512px (base) and 768px (v) images, depth, inpainting, x4 upscaler, unCLIP variants
- `denoiser`: UNet, 865M, cross attention at 1024
- `vae`: kl-f8, 4 channels, shared with SD1
- `text_encoder.clip_h`: OpenCLIP ViT-H/14, penultimate layer
- Settings: `eps` for 512-base, `v` for 768-v and its finetunes, 20 to 30 steps, guidance 7
- Adapters: SD1 adapters with 1024 wide `embedding` vectors, SD2 `controlnet`
- x4 upscaler variant: `stage.upscaler` conditioned on a low resolution image with noise level
- unCLIP variant: `image_encoder.clip_vision` ViT-H or ViT-L image embeddings in place of text
- Canonical: `stabilityai/stable-diffusion-2-1`, `stabilityai/stable-diffusion-x4-upscaler`

### Stable Diffusion XL

- Output: 1024px images, refiner, inpainting, Turbo, Lightning variants
- `denoiser`: UNet, 2.6B, 4 latent channels, cross attention at 2048, size and crop conditioning through added time embeddings
- `vae`: kl-f8, 4 channels, `sdxl-vae-fp16-fix` for half precision decoding
- `text_encoder.clip_l`: penultimate hidden states, 768
- `text_encoder.clip_g`: penultimate hidden states, 1280, with pooled output. CLIP-L and CLIP-G states concatenate to 2048
- `refiner`: an SDXL refiner UNet using `text_encoder.clip_g` only, applied over the last 20% of the schedule with aesthetic score conditioning
- Bundled single files carry `conditioner.embedders.0.` (CLIP-L) and `conditioner.embedders.1.` (CLIP-G)
- Settings: `eps` prediction (`edm_v` for Playground v2.5), 25 to 40 steps, guidance 5 to 7. Turbo: 1 to 4 steps, guidance 0. Lightning: 2, 4, 8 steps, guidance 1. LCM: 4 to 8 steps. Hyper-SD: 1 to 8 steps
- Adapters: `lora` on both text encoders and the UNet, `lycoris`, `embedding` with `clip_l` and `clip_g` keys, `controlnet` (canny, depth, openpose, union, promax, tile, inpaint, qrcode, scribble, Xinsir, diffusers, TTPlanet), `controlnet` in ControlLoRA and ControlNet-LLLite forms, `t2i_adapter`, `ip_adapter` (base, plus, plus-face, FaceID with `face_encoder`) with `image_encoder.clip_vision` ViT-H or ViT-bigG, `photo_maker` v1 and v2, `instant_id`, `pulid`, `motion_module` for video (HotShot-XL, AnimateDiff-SDXL)
- Derived families: Pony Diffusion V6, Illustrious, NoobAI, Animagine, Juggernaut, RealVisXL, DreamShaper XL, SDXL Turbo, SDXL Lightning, Playground v2 and v2.5, Kolors (with its own text encoder, below), Segmind SSD-1B and Vega (pruned UNet), KOALA
- Canonical: `stabilityai/stable-diffusion-xl-base-1.0`, `stabilityai/stable-diffusion-xl-refiner-1.0`, `madebyollin/sdxl-vae-fp16-fix`

### Kolors

- `denoiser`: SDXL UNet with cross attention at 4096
- `vae`: SDXL VAE
- `text_encoder.llm`: ChatGLM3-6B, penultimate hidden states
- Adapters: Kolors `controlnet`, `ip_adapter` with ViT-bigG, `lora`
- Canonical: `Kwai-Kolors/Kolors`

### Stable Diffusion 3 and 3.5

- Output: 1024px images, 3.5 Large, Large Turbo, Medium
- `denoiser`: MMDiT, 2B (3 Medium), 2.5B MMDiT-X (3.5 Medium), 8B (3.5 Large), 16 latent channels
- `vae`: kl-f8, 16 channels
- `text_encoder.clip_l`: hidden states and pooled
- `text_encoder.clip_g`: hidden states and pooled
- `text_encoder.t5`: T5-XXL encoder, 4096 wide, 77 or 256 tokens. Optional with zeroed T5 tokens
- Settings: `sd3_flow` prediction, shift 3, 28 steps, guidance 4.5 to 7, Large Turbo 4 steps at guidance 0, skip layer guidance on layers 7, 8, 9 for Medium
- Adapters: `lora`, `controlnet` (canny, depth, blur, tile), `ip_adapter` with SigLIP
- Canonical: `stabilityai/stable-diffusion-3.5-large`, `Comfy-Org/stable-diffusion-3.5-fp8`

### FLUX.1

- Output: 1024px images, dev, schnell, Fill, Depth, Canny, Redux, Kontext, Krea
- `denoiser`: 12B transformer of 19 double stream blocks and 38 single stream blocks, 64 packed latent channels (16 channels at 2x2 patch), guidance embedding on dev, 3D rope
- `vae`: `ae.safetensors`, 16 channels, shared between dev and schnell
- `text_encoder.clip_l`: pooled output only, 768
- `text_encoder.t5`: T5-XXL encoder, 512 tokens on dev, 256 on schnell
- Settings: `flux_flow` prediction with resolution dependent shift (`flux` schedule), dev 20 to 30 steps with distilled guidance 3.5 and classifier free guidance 1, schnell 1 to 4 steps with guidance 0, `euler`
- Variants: Fill (inpaint and outpaint, 384 input channels: latent, masked latent, mask), Depth and Canny (control latents concatenated, also as `lora`), Kontext (edit with the reference image's latent tokens sequenced beside the target), Krea (aesthetic finetune), Lite (pruned 8B), Turbo Alpha (`lora`, 8 steps), Nunchaku int4 and fp4 forms, GGUF forms, FP8 forms
- Adapters: `lora` on double and single blocks (kohya `lora_unet_` names, diffusers `transformer.` names, `lora_A` and `lora_B` or `lora_down` and `lora_up`), `controlnet` (XLabs, InstantX Union and Union Pro and Pro 2, Shakker, Jasper depth, BFL Depth and Canny LoRA form), `ip_adapter` (XLabs with ViT-L, InstantX with SigLIP), `pulid` (EVA-CLIP plus InsightFace), `redux` (SigLIP So400m 384 plus projector). No `photo_maker` or `embedding` support
- Canonical: `black-forest-labs/FLUX.1-dev`, `black-forest-labs/FLUX.1-schnell`, `comfyanonymous/flux_text_encoders`, `black-forest-labs/FLUX.1-Kontext-dev`, `black-forest-labs/FLUX.1-Fill-dev`, `black-forest-labs/FLUX.1-Redux-dev`, `city96/FLUX.1-dev-gguf`

### Chroma

- Output: 1024px images, Chroma1-HD, Chroma1-Base, Chroma1-Flash
- `denoiser`: FLUX.1-schnell derived, 8.9B, modulation replaced by a distilled guidance layer, no guidance embedding
- `vae`: FLUX `ae.safetensors`
- `text_encoder.t5`: T5-XXL encoder, no CLIP
- Settings: `flux_flow`, 20 to 40 steps, classifier free guidance 3 to 5, `euler` or `res_multistep`, Flash variant 8 steps
- Adapters: `lora`, FLUX `controlnet`
- Canonical: `lodestones/Chroma1-HD`

### Chroma Radiance

- Output: images directly in pixel space
- `denoiser`: Chroma transformer with a NeRF style pixel head, patch embedding from RGB
- No `vae`
- `text_encoder.t5`: T5-XXL encoder
- Canonical: `lodestones/Chroma1-Radiance`

### FLUX.2

- Output: up to 4MP images and multi reference edits, dev, klein 4B, klein 9B, pro
- `denoiser`: 32B transformer (dev) with 8 double and 48 single blocks, klein 4B and 9B distilled transformers
- `vae`: the FLUX.2 autoencoder, `FLUX.2-dev` `ae.safetensors`, a new latent space from FLUX.1
- `text_encoder.llm`: Mistral Small 3.2 24B for dev, hidden states from intermediate layers, Qwen3-4B for klein 4B, Qwen3-8B for klein 9B
- `text_encoder.llm.vision`: the Pixtral tower of Mistral Small 3.2 for reference images on dev
- Settings: `flux_flow` with the `flux2` schedule, dev 28 to 50 steps at guidance 4, klein 4 steps at guidance 1
- Adapters: `lora`, FLUX.2 `controlnet`
- Canonical: `black-forest-labs/FLUX.2-dev`, `black-forest-labs/FLUX.2-klein-4B`, `Comfy-Org/flux2-klein-4B`, `unsloth/Mistral-Small-3.2-24B-Instruct-2506-GGUF`

### HiDream-I1 and HiDream-E1

- Output: 1024px images (I1 Full, Dev, Fast) and instruction edits (E1)
- `denoiser`: 17B sparse MMDiT with mixture of experts MLPs, double and single stream blocks
- `vae`: FLUX `ae.safetensors`
- `text_encoder.clip_l`, `text_encoder.clip_g`: pooled outputs
- `text_encoder.t5`: T5-XXL encoder
- `text_encoder.llm`: Llama-3.1-8B-Instruct, hidden states from every layer, 128 tokens
- Settings: `flux_flow`, Full 50 steps guidance 5, Dev 28 steps guidance 0, Fast 16 steps guidance 0, `uni_pc` or `lcm` sampler
- Adapters: `lora`
- Canonical: `HiDream-ai/HiDream-I1-Full`, `HiDream-ai/HiDream-E1-1`

### HiDream-O1

- Transformer, text encoders, and VAE bundled in one checkpoint that fills every slot
- Canonical: `HiDream-ai/HiDream-O1`

### Qwen-Image and Qwen-Image-Edit

- Output: 1328px images with rendered text, Edit, Edit-2509, Edit-2511 multi image edits, Lightning distills
- `denoiser`: 20B MMDiT, 60 blocks, 16 latent channels at 2x2 patch
- `vae`: Qwen-Image VAE, 16 channels, Wan derived
- `text_encoder.llm`: Qwen2.5-VL-7B-Instruct, hidden states from the last layers after a fixed system prompt. `qwen_image_layers` drops layers from the end
- `text_encoder.llm.vision`: Qwen2.5-VL vision tower for Edit reference images. GGUF encoders require `mmproj`
- Settings: `flux_flow` with shift 3, 50 steps at guidance 4, Lightning `lora` 4 or 8 steps at guidance 1
- Adapters: `lora` (Lightning, style, character), `controlnet` (InstantX union, DiffSynth), `ip_adapter`, EliGen entity control
- Canonical: `Qwen/Qwen-Image`, `Qwen/Qwen-Image-Edit-2509`, `Comfy-Org/Qwen-Image_ComfyUI`, `lightx2v/Qwen-Image-Lightning`, `city96/Qwen-Image-gguf`

### Qwen-Image 2.1

- Output: images up to 2048px with rendered text, edits from reference images, RGBA output when the prompt asks for transparency
- `denoiser`: 7B transformer, 32 blocks, 64 latent channels at 1x1 patch, identified by `txt_in.text_norm.weight`
- `vae`: Qwen-Image 2.1 VAE, 64 channels, 3D convolutions with a singleton temporal kernel. The Qwen-Image and Wan 2.2 VAEs do not fit it
- `text_encoder.llm`: Qwen3-VL-8B-Instruct
- `text_encoder.llm.vision`: Qwen3-VL-8B vision tower for reference images. GGUF encoders require `mmproj`
- Settings: `flux` scheduler with shift 3, 40 steps at guidance 6, dimensions divisible by 32
- Canonical: `Qwen/Qwen-Image-2.1`, `Comfy-Org/Qwen-Image-2.1`, `leejet/Qwen-Image-2.1-GGUF`, `Qwen/Qwen3-VL-8B-Instruct-GGUF`

### Z-Image

- Output: 1024px images, Turbo (8 steps), Base, Edit
- `denoiser`: 6B single stream DiT
- `vae`: FLUX 16 channel `ae.safetensors`
- `text_encoder.llm`: Qwen3-4B, hidden states
- Settings: `flux_flow`, Turbo 8 steps at guidance 1, Base 28 steps at guidance 4
- Adapters: `lora`
- Canonical: `Tongyi-MAI/Z-Image-Turbo`, `Comfy-Org/z_image_turbo`

### Lumina-Image 2.0 and Lumina-Next

- `denoiser`: Next-DiT, 2.6B (2.0), 2B (Next)
- `vae`: FLUX 16 channel `ae.safetensors` (2.0), SDXL VAE (Next)
- `text_encoder.llm`: Gemma-2-2B (2.0), Gemma-2B (Next)
- Settings: flow matching, 30 to 50 steps, guidance 4, `euler`
- Adapters: `lora`
- Canonical: `Alpha-VLLM/Lumina-Image-2.0`

### Sana and Sana 1.5

- Output: 1024px to 4096px images at low memory
- `denoiser`: linear DiT, 0.6B, 1.6B, 4.8B, with Mix-FFN
- `vae`: DC-AE, 32 channels at 32x downsampling
- `text_encoder.llm`: Gemma-2-2B-IT, hidden states
- Settings: flow matching, 20 steps, guidance 4.5 with PAG, Sana-Sprint 1 to 4 steps
- Adapters: `lora`, `controlnet`
- Canonical: `Efficient-Large-Model/Sana_1600M_1024px_diffusers`

### PixArt-α and PixArt-Σ

- `denoiser`: DiT with cross attention, 0.6B
- `vae`: SD1 kl-f8 (α), SDXL VAE (Σ)
- `text_encoder.t5`: T5-XXL encoder, 120 tokens (α), 300 tokens (Σ)
- Settings: `eps` (α) and `v` style DPM (Σ), 20 steps, guidance 4.5
- Adapters: `lora`, `controlnet` (Σ)
- Canonical: `PixArt-alpha/PixArt-XL-2-1024-MS`, `PixArt-alpha/PixArt-Sigma-XL-2-1024-MS`

### Hunyuan-DiT

- `denoiser`: DiT with rope, 1.5B
- `vae`: SDXL VAE
- `text_encoder.clip_l` form: a bilingual CLIP text tower
- `text_encoder.t5`: mT5-XL encoder
- Settings: `v` prediction, 50 steps, guidance 6
- Adapters: `lora`, `controlnet`, `ip_adapter`
- Canonical: `Tencent-Hunyuan/HunyuanDiT-v1.2-Diffusers`

### CogView4 and CogView3-Plus

- `denoiser`: DiT, 6B (4)
- `vae`: CogView4 VAE, 16 channels
- `text_encoder.llm`: GLM-4-9B, hidden states
- Settings: flow matching, 50 steps, guidance 3.5
- Canonical: `THUDM/CogView4-6B`

### Kandinsky 2.2, 3, and 4

- 2.2: `stage.prior` mapping text to CLIP ViT-bigG image embeddings, `stage.decoder` UNet conditioned on those embeddings, `vae` MoVQ, `image_encoder.clip_vision` ViT-bigG for image mixing, `controlnet` depth
- 3: `denoiser` UNet with cross attention at 4096, `text_encoder.t5` FLAN-UL2 encoder, `vae` MoVQ
- 4: a flow transformer with its own VAE and text encoder in one diffusers directory
- Canonical: `kandinsky-community/kandinsky-2-2-decoder`, `kandinsky-community/kandinsky-3`

### DeepFloyd IF

- Pixel space cascade
- `stage.prior` IF-I XL, 64px, text through `text_encoder.t5` T5-XXL encoder
- `stage.decoder` IF-II L, 64 to 256px, same text
- `stage.upscaler` IF-III, the SD x4 upscaler to 1024px, with `vae` and `text_encoder.clip_h`
- No `vae` for stages I and II
- Canonical: `DeepFloyd/IF-I-XL-v1.0`, `DeepFloyd/IF-II-L-v1.0`

### Stable Cascade

- `stage.prior` Stage C, 3.6B or 1B, in the Würstchen 42x compressed latent, text through `text_encoder.clip_g` and pooled
- `stage.decoder` Stage B, 1.5B or 700M, to the 4x latent
- `vae` Stage A, a VQGAN
- `image_encoder.clip_vision` ViT-bigG for image prompting, EfficientNet encoder for Stage C training latents
- Adapters: `lora` on Stage C, `controlnet` on Stage C
- Canonical: `stabilityai/stable-cascade`

### Playground v2.5

- SDXL blueprint with EDM training: `denoiser` SDXL UNet, `vae` fine tuned, `text_encoder.clip_l`, `text_encoder.clip_g`
- Settings: `edm_v`, 25 steps, guidance 3
- Canonical: `playgroundai/playground-v2.5-1024px-aesthetic`

### Ovis-Image

- `denoiser`: 7B DiT
- `vae`: FLUX schnell `ae.safetensors`
- `text_encoder.llm`: Ovis 2.5 language model
- Canonical: `AIDC-AI/Ovis-Image`, `Comfy-Org/Ovis-Image`

### LongCat-Image

- `denoiser`: 6B DiT
- `vae`: FLUX `ae.safetensors`
- `text_encoder.llm`: Qwen2.5-VL-7B
- `text_encoder.llm.vision`: for the Edit variant
- Canonical: `meituan-longcat/LongCat-Image`

### Krea 2

- `denoiser`: Krea 2 transformer
- `vae`: Wan 2.1 VAE
- `text_encoder.llm`: Qwen3-VL-4B
- Canonical: `Comfy-Org/Krea-2`

### ERNIE-Image

- `denoiser`: ERNIE-Image transformer
- `vae`: the ERNIE-Image VAE
- `text_encoder.llm`: Ministral 3 3B Instruct
- Canonical: `Comfy-Org/ERNIE-Image`

### Anima

- `denoiser`: Anima transformer
- `vae`: Qwen-Image VAE
- `text_encoder.llm`: Qwen3-0.6B
- Canonical: `circlestone-labs/Anima`

### Boogu-Image

- `denoiser`: double and single stream layers
- `vae`: FLUX `ae.safetensors`
- `text_encoder.llm`: Qwen3-VL-8B-Instruct
- `text_encoder.llm.vision`: for reference images

### Mage-Flow

- `denoiser`: Mage-Flow transformer
- `vae`: the Mage-Flow VAE
- `text_encoder.llm`: Qwen3-VL-4B
- `text_encoder.llm.vision`: for reference images
- Canonical: `microsoft/Mage-Flow`

### Ideogram 4

- `denoiser`: the conditional transformer
- `denoiser.uncond`: a separate unconditional transformer for the guidance branch, required
- `vae`: FLUX.2 autoencoder
- `text_encoder.llm`: Qwen3-VL-8B-Instruct
- Canonical: `ideogram-ai/ideogram-4-fp8`

### SeFi-Image

- `denoiser`: SeFi transformer, `sefi_flow` prediction
- `vae`: FLUX.2 autoencoder
- `text_encoder.llm`: encoder supplied by the release

### Lens

- `denoiser`: Lens transformer
- `vae`: FLUX.2 autoencoder
- `text_encoder.llm`: gpt-oss-20b, hidden states
- `tokenizer`: the gpt-oss `tokenizer.json`, passed apart from a GGUF encoder
- Canonical: `openai/gpt-oss-20b` for the encoder

### PixelDiT

- `denoiser`: pixel space DiT, `lcm` sampler
- `vae`: the PixelDiT autoencoder
- `text_encoder.llm`: Gemma-2-2B encoder
- `tokenizer`: Gemma-2-2B `tokenizer.json`
- Canonical: `nvidia/PiD`, `Comfy-Org/PixelDiT`

### MiniT2I

- `denoiser`: a small transformer with `minit2i_flow` prediction
- `text_encoder.t5`: FLAN-T5-Large encoder
- No separate `vae`

### SenseNova U1

- Transformer, encoders, and autoencoder in one checkpoint, `sensenova_u1_flow` prediction

### Würstchen, Latent Consistency, and amused

- Würstchen v2: the Stable Cascade blueprint at its first release
- LCM Dreamshaper v7: SD1 blueprint with `lcm` sampler, 4 steps
- aMUSEd: masked image transformer over VQ tokens with `text_encoder.clip_l`, 12 steps without diffusion

## Image editing

Uses the text-to-image blueprint with an input image encoded as latent tokens, concatenated channels, or vision tokens.

| Family | Denoiser base | How the input enters | Encoders |
| --- | --- | --- | --- |
| InstructPix2Pix | SD1.5 UNet with 8 input channels | concatenated latent, image guidance scale | `clip_l` |
| CosXL Edit | SDXL EDM UNet | concatenated latent | `clip_l`, `clip_g` |
| FLUX.1 Kontext | FLUX.1 | reference latent tokens with offset positions | `clip_l`, `t5` |
| FLUX.1 Fill | FLUX.1 with 384 input channels | masked latent and mask | `clip_l`, `t5` |
| FLUX.2 | FLUX.2 | up to 8 reference latents | Mistral Small 3.2 with vision |
| Qwen-Image-Edit, 2509, 2511 | Qwen-Image | VAE latent plus Qwen2.5-VL vision tokens, several references | Qwen2.5-VL |
| Qwen-Image 2.1 | Qwen-Image 2.1 | VAE latent plus Qwen3-VL vision tokens, several references | Qwen3-VL-8B |
| HiDream-E1 | HiDream-I1 | reference latent tokens | four encoders of I1 |
| OmniGen 1, 2 | a Phi-3 decoder (1), Qwen2.5-VL plus a diffusion head (2) | interleaved image and text tokens | bundled |
| BAGEL | Qwen2.5 MoT with a SigLIP tower and a FLUX VAE | unified understanding and generation | bundled |
| Step1X-Edit | Qwen2.5-VL plus a DiT | vision tokens as conditioning | Qwen2.5-VL |
| ICEdit, DreamO, UNO, In-Context LoRA | FLUX.1 `lora` | side by side layout | `clip_l`, `t5` |
| ACE++ | FLUX.1 Fill `lora` | masked reference | `clip_l`, `t5` |
| SDXL and SD1 inpainting | 9 channel UNets | masked latent and mask | family encoders |
| Kandinsky 2.2 inpainting | decoder with mask channels | mask | prior embeddings |
| LaMa, MAT, ZITS | pixel space inpainting networks, no diffusion | image and mask | none |
| Paint by Example | SD1 UNet with CLIP image embeddings | reference through `image_encoder.clip_vision` | none |

## Control

A spatial signal from a `preprocessor` steers the denoiser through an adapter.

- `preprocessor` networks: Depth Anything v1, v2, v3, MiDaS, ZoeDepth, DPT, Marigold, Lotus (depth), OpenPose, DWPose, RTMPose, ViTPose, Sapiens (pose), Canny, HED, PiDiNet, TEED, Anyline (edges), LineArt, LineArt Anime, MangaLine (lines), OneFormer, SegFormer, UniFormer, SAM (segmentation), NormalBae, DSINE, Sapiens normal (normals), MLSD (lines), Tile blur, color shuffle, recolor, brightness (image ops)
- `controlnet` per family: SD1.5 (lllyasviel 1.0 and 1.1, QR, tile, brightness), SD2.1 (thibaud), SDXL (diffusers, Xinsir union and promax, TTPlanet tile, MistoLine, ControlNet-LLLite, ControlLoRA), SD3 (InstantX, Stability canny depth blur), FLUX.1 (XLabs, InstantX Union, Shakker, Jasper, BFL Depth and Canny full and LoRA), FLUX.2, Qwen-Image (InstantX union, DiffSynth), Hunyuan-DiT, Kolors, Sana, PixArt-Σ, Stable Cascade, HunyuanVideo, Wan Fun, MiniMax-H3 Fun union `model_patch`
- `t2i_adapter`: SD1.5 and SDXL for canny, depth, sketch, openpose, lineart, color
- Settings: control strength, start and end percent, guess mode, union control type index

## Identity and reference

Preserves a subject's identity from a reference image.

| Adapter | Encoders | Families |
| --- | --- | --- |
| `ip_adapter` | `image_encoder.clip_vision` ViT-H (SD1.5, SDXL), ViT-bigG (SDXL), ViT-L (XLabs FLUX), SigLIP (InstantX FLUX, SD3), FaceID variants add `face_encoder` InsightFace | SD1.5, SDXL, Kolors, SD3, FLUX.1, Qwen-Image |
| `photo_maker` v1, v2 | its own ID encoder over `image_encoder.clip_vision`, `face_encoder` InsightFace on v2, plus a `lora` | SDXL |
| `instant_id` | `face_encoder` antelopev2, an image projection, a keypoint `controlnet` | SDXL |
| `pulid` | EVA-CLIP-L-14-336, `face_encoder` InsightFace antelopev2, facexlib parsing | SDXL, FLUX.1 |
| `redux` | SigLIP So400m 384 with projector | FLUX.1 |
| InfiniteYou, UNO, DreamO, OmniControl, EasyControl | `face_encoder` or reference latents through a `lora` or a `controlnet` | FLUX.1 |
| IP-Adapter Instruct, IP-Composition, Style Aligned, StyleAlign | `image_encoder.clip_vision` | SD1.5, SDXL |
| Wan Phantom, SkyReels-A2, VACE reference, MiniMax-H3 Ref2VA | reference latents through the video denoiser | video families |

## Distillation forms

A distilled variant changes settings and sometimes ships as an adapter.

| Form | Ships as | Steps | Guidance | Sampler |
| --- | --- | --- | --- | --- |
| LCM, LCM-LoRA | merged `denoiser` or `lora` | 4 to 8 | 1 to 2 | `lcm` |
| SDXL Turbo, SD Turbo | `denoiser` | 1 to 4 | 0 | `euler_a` |
| SDXL Lightning | `denoiser` or `lora`, 2, 4, 8 step files | 2, 4, 8 | 1 | `euler` with `sgm_uniform` |
| Hyper-SD | `lora` per step count, and CFG variants | 1, 2, 4, 8, 12 | 0 or 5 | `euler` or `tcd` |
| TCD | `lora` | 4 to 8 | 1 | `tcd` |
| PCM | `lora` per step count | 2 to 16 | 1 to 5 | `euler` |
| DMD2 | `denoiser` or `lora` | 1 to 4 | 0 or 1 | `lcm` or `euler` |
| FLUX.1 schnell, Turbo Alpha, FLUX.2 klein | `denoiser` or `lora` | 1 to 8 | 0 or 1 | `euler` |
| Qwen-Image Lightning | `lora` | 4 or 8 | 1 | `euler` with shift 3 |
| Z-Image Turbo | `denoiser` | 8 | 1 | `euler` |
| SD3.5 Large Turbo | `denoiser` | 4 | 0 | `euler` |
| Sana-Sprint | `denoiser` | 1 to 4 | 0 | `euler` |
| Nitro-SD, Nitro-Fusion | `denoiser` | 1 | 0 | `euler` |
| Wan, HunyuanVideo, MiniMax-H3, LTX distills (lightx2v, CausVid, Self-Forcing, FusionX, turbo, DMD) | `lora` or `denoiser` | 3 to 8 | 1 | `euler` or `lcm`, custom sigmas |

## Post-processing

- `upscaler`: ESRGAN, RealESRGAN x2 x4 and anime, RealESRGAN video, BSRGAN, SwinIR, HAT, DAT, DRCT, SPAN, OmniSR, 4x-UltraSharp, 4x-NMKD, 4x-AnimeSharp, 4x-FaceUpDAT, Remacri, Siax, LSDIR, PLKSR, RealPLKSR, ATD, RGT, SRFormer, GRL, latent upscalers (SD latent x2, x1.5 nearest exact, bicubic, `stage.upscaler` SD x4)
- Highres fix: a second sampling pass at a larger size with an `upscaler` or a latent upscale and a denoise fraction
- `detector` for detail passes: YOLOv8 face, hand, and person detectors, MediaPipe face mesh, SAM and SAM 2 for masks, masked second pass with separate prompt and adapters (ADetailer)
- Face restoration: GFPGAN, CodeFormer, RestoreFormer, GPEN, AuraFace
- Background removal: RMBG 1.4 and 2.0, BiRefNet, InSPyReNet, U2Net, ISNet, BEN
- `vae.tiny`: TAESD, TAESDXL, TAESD3, TAEF1, TAEHV, TAEW2.1, TAE for Hunyuan
- `safety`: the SD safety checker with its CLIP feature extractor, Falconsai NSFW, CompVis NSFW, LAION aesthetic predictors, watermark detectors, PickScore, HPSv2, ImageReward, CLIP score
- Metadata: prompt and settings written to PNG text chunks and EXIF

## Autoregressive and masked image generation

A language model generates image tokens from text. A VQ or VAE decoder produces the image.

| Family | Image tokenizer | Backbone | Notes |
| --- | --- | --- | --- |
| Janus, Janus-Pro | VQ tokenizer for generation, SigLIP for understanding | DeepSeek LLM | decoupled encoders |
| Emu3, Emu3.5 | SBER-MoVQGAN | Llama style | unified next token |
| Infinity, VAR | multi scale residual VQ | GPT | next scale prediction |
| Lumina-mGPT, Lumina-mGPT 2.0 | Chameleon VQ | Chameleon | |
| Chameleon, Anole | VQ | Llama | |
| Show-o, Show-o2 | MAGVIT-v2 | Phi | masked plus autoregressive |
| MAR, RAR, LlamaGen, Open-MAGVIT2, HART | VQ or continuous | GPT | diffusion loss head (MAR) |
| aMUSEd, Meissonic, MaskGIT, Muse open reimplementations | VQGAN | masked transformer | parallel decoding |
| Parti and DALL-E open reimplementations | ViT-VQGAN, dVAE | seq2seq | |
| BLIP3-o, Ming-UniVision, Nexus-Gen | CLIP feature diffusion | Qwen | |

## Image understanding

| Pipeline | Slots | Families |
| --- | --- | --- |
| Image classification | `weights` with a class head, `config` with labels and preprocessing | ViT, DeiT, BEiT, ConvNeXt, ConvNeXt V2, EfficientNet, ResNet, RegNet, Swin, Swin V2, MobileNet V2 to V4, MobileViT, EVA, DINOv2 linear probes, timm models |
| Zero-shot classification | `image_encoder` and `text_encoder` of a contrastive pair | CLIP, OpenCLIP, SigLIP, SigLIP 2, EVA-CLIP, MetaCLIP, AIMv2, DFN, MobileCLIP, ALIGN, BLIP ITM, Chinese CLIP, Jina CLIP, TULIP, PE Core |
| Image embeddings | `image_encoder` alone | CLIP towers, DINO, DINOv2, DINOv3, SigLIP, I-JEPA, MAE, ViT features |
| Object detection | `weights` with box heads, `config` with labels | DETR, Deformable DETR, Conditional DETR, DINO-DETR, RT-DETR, RT-DETR v2, D-FINE, DEIM, YOLOS, YOLOv5 to YOLO11 and YOLO26, YOLO-World, OWL-ViT, OWLv2, Grounding DINO 1.0 and 1.5 and 1.6, Florence-2, MM-Grounding-DINO, LLMDet |
| Segmentation | `weights` with mask heads, points, boxes, or masks for promptable models | SAM, SAM 2, SAM 2.1, SAM 3, MobileSAM, EfficientSAM, SAM-HQ, Mask2Former, MaskFormer, SegFormer, OneFormer, UPerNet, BEiT seg, DPT seg, Grounded SAM, EVF-SAM, CLIPSeg, BiRefNet, SegGPT |
| Depth estimation | `weights` regressing depth or disparity | Depth Anything v1, v2, v3, DPT, MiDaS, ZoeDepth, Marigold, Lotus, Metric3D, UniDepth, MoGe, Depth Pro, GeoWizard, DepthCrafter (video) |
| Surface normals and matting | `weights` | NormalBae, DSINE, StableNormal, Marigold normals, ViTMatte, MatAnyone |
| Pose and keypoints | `weights` | OpenPose, DWPose, RTMPose, RTMO, ViTPose, ViTPose++, Sapiens, YOLO pose, MediaPipe, HRNet, SuperPoint, SuperGlue, LightGlue, LoFTR, Roma |
| OCR and documents | encoder-decoder or vision-language | TrOCR, Donut, Nougat, GOT-OCR 2, PaddleOCR PP-OCRv4 and v5, EasyOCR, Surya, Marker, DocTR, Florence-2 OCR, olmOCR, MinerU, SmolDocling, dots.ocr, DeepSeek-OCR, PaddleOCR-VL, Chandra, Granite Docling, LayoutLMv3, UDOP, Pix2Struct, DocOwl, TextMonkey, table transformers |
| Captioning and VQA | vision-language model | BLIP, BLIP-2, GIT, Florence-2, PaliGemma, LLaVA, Qwen-VL, InternVL, Moondream, SmolVLM, JoyCaption, WD tagger (ViT, SwinV2, ConvNeXt taggers), CLIP Interrogator, DeepDanbooru, Pixtral, Gemma 3 |
| Image quality and aesthetics | `weights` regressing a score | LAION aesthetic v2, aesthetic-shadow, MUSIQ, NIMA, CLIP-IQA, Q-Align, TOPIQ, ImageReward, PickScore, HPSv2, HPSv3, VQAScore |
| Face | `face_encoder`, detectors, parsers, landmarks | InsightFace buffalo_l and antelopev2, ArcFace, AdaFace, RetinaFace, SCRFD, YOLO face, facexlib, BiSeNet parsing, FAN landmarks, DECA, EMOCA, SMIRK |
| Image retrieval and deduplication | embeddings plus index | CLIP, DINOv2, SigLIP, ColPali (documents), imagededup, pHash |
| Image forensics and watermarks | classifiers and decoders | AI generated image detectors, Stable Signature, Tree-Ring, invisible-watermark, SynthID open detectors, Perth |

## 3D from images and text

| Family | Slots | Notes |
| --- | --- | --- |
| Hunyuan3D 2.0, 2.1, 2.5, 3.0 | `image_encoder.dino` DINOv2, shape `denoiser` DiT over a ShapeVAE latent, `vae` ShapeVAE decoding to SDF or mesh, paint `denoiser` for PBR textures with a multi view diffusion stage, `preprocessor` background removal | image to mesh with textures |
| TRELLIS, TRELLIS.2 | `image_encoder.dino` DINOv2, sparse structure flow transformer, structured latent flow transformer, `vae` SLAT decoders to Gaussians, radiance fields, and meshes | image and text to 3D |
| TripoSR, TripoSG, TripoSF | `image_encoder.dino`, a triplane or VAE decoder | feed forward |
| InstantMesh, CRM, LGM, Wonder3D, Era3D, Unique3D, SV3D, Zero123, Zero123++, MVDream, ImageDream, SyncDreamer | multi view diffusion `denoiser` on an SD or SVD base, a reconstruction network (LRM, NeuS, Gaussian) | image to multi view to mesh |
| Stable Fast 3D, SF3D, SPAR3D, Stable Point Aware 3D | `image_encoder.dino`, point diffusion, mesh decoder with UV and materials | |
| Shap-E, Point-E | `text_encoder.clip_l`, latent diffusion over implicit function or point cloud parameters | |
| MeshAnything, MeshGPT, LLaMA-Mesh, PivotMesh, DeepMesh, BPT | mesh token language models | artist style topology |
| Gaussian splatting and NeRF tools | `weights` per scene | 3DGS, Nerfstudio checkpoints, Splatfacto, gsplat, DUSt3R, MASt3R, VGGT, MapAnything, Fast3R, CUT3R, MoGe, Depth Anything 3 |
| Human and avatar | SMPL, SMPL-X, MANO, FLAME parameters, `weights` of regressors | HMR 2.0, 4D-Humans, SMPLer-X, WHAM, TokenHMR, GVHMR, Sapiens, ECON, ICON, PIFuHD, InstantAvatar, GaussianAvatar |
| Texture and material | `denoiser` on an SD base with UV or multi view conditioning | Paint3D, TEXTure, Text2Tex, Hunyuan3D-Paint, MaterialFusion, RGBX, IntrinsicAnything |

# Video

## Latent video diffusion

Text encoders embed the prompt. The denoiser generates a latent volume, which a 3D VAE decodes into frames. Image inputs use first or last frame latents, reference tokens, or image embeddings. Joint audio-video families add an audio latent stream.

- `denoiser`, required, plus `denoiser.high_noise` for families split by timestep
- `vae`, required causal 3D autoencoder, plus `vae.audio` for audio-video families
- family text encoders, required
- `image_encoder.clip_vision`, where image-to-video uses image embeddings
- `audio_encoder`, where speech drives motion
- `vae.tiny` for previews
- Adapters: `lora`, `motion_lora`, `controlnet`, `model_patch`, `embedding`
- Post: `upscaler` (video), frame interpolation, `detector`
- Settings: `prediction`, `schedule`, `sampler`, `steps`, `guidance`, `shift`, `size` with `frames` and `fps`, `cache`, `precision`, `placement`, MoE boundary, temporal and spatial VAE tiling

### Wan 2.1

- Output: 480p and 720p clips, 81 frames at 16 fps, T2V 1.3B, T2V 14B, I2V 14B 480p and 720p, FLF2V 14B, VACE 1.3B and 14B, Fun-Control and Fun-InP, Phantom
- `denoiser`: DiT with cross attention, 1.3B or 14B, 16 latent channels at 1x2x2 patch, 3D rope
- `vae`: Wan 2.1 VAE, 16 channels, 4x temporal and 8x spatial compression
- `text_encoder.t5`: UMT5-XXL encoder, 512 tokens
- `image_encoder.clip_vision`: CLIP ViT-H/14 (open_clip xlm-roberta-large-ViT-H-14) for I2V, FLF2V, and Fun-InP. Unused by 2.2 I2V
- Variant inputs: I2V concatenates the first frame latent and a mask, FLF2V adds the last frame, VACE takes a control video, a mask, and reference images through its own context blocks, Fun-Control takes a control video (depth, pose, canny, trajectory), Phantom takes subject reference latents
- Settings: `wan_flow` prediction, shift 5 (480p) or 8 (720p), 30 to 50 steps at guidance 5 to 6, `unipc` or `euler`
- Adapters: `lora` (CausVid, Self-Forcing, lightx2v 4 step, FusionX, AccVid, camera control, style), `controlnet` (Wan Fun), `model_patch`
- Distills: CausVid `lora` 4 to 8 steps guidance 1, Self-Forcing 4 steps, lightx2v 4 steps
- Canonical: `Wan-AI/Wan2.1-T2V-14B`, `Wan-AI/Wan2.1-I2V-14B-720P`, `Wan-AI/Wan2.1-VACE-14B`, `Comfy-Org/Wan_2.1_ComfyUI_repackaged`, `city96/Wan2.1-T2V-14B-gguf`, `city96/umt5-xxl-encoder-gguf`

### Wan 2.2

- Output: 480p to 720p clips at 16 or 24 fps, T2V-A14B and I2V-A14B as expert pairs, TI2V-5B, S2V-14B, Animate-14B, Fun-Control and Fun-InP 2.2
- `denoiser`: low noise expert of the A14B pair, or one dense DiT for TI2V-5B
- `denoiser.high_noise`: high noise expert of the A14B pair, switched at MoE boundary 0.875
- `vae`: Wan 2.1 VAE for the A14B pair, Wan 2.2 VAE, 48 channels at 4x16x16 compression, for TI2V-5B
- `text_encoder.t5`: UMT5-XXL encoder
- `audio_encoder`: wav2vec2 large for S2V, speech to video
- Animate: pose through DWPose, face crops, a relight `lora`, and the character reference latent
- Settings: `wan_flow`, shift 5 to 8, 20 to 40 steps split across the two experts at guidance 3.5 to 4 (high) and 3 to 4 (low), TI2V-5B 50 steps at guidance 5, `euler` or `unipc`
- Adapters: `lora` per expert (lightx2v 4 step high and low, FusionX), Fun `controlnet`
- Canonical: `Wan-AI/Wan2.2-T2V-A14B`, `Wan-AI/Wan2.2-I2V-A14B`, `Wan-AI/Wan2.2-TI2V-5B`, `Wan-AI/Wan2.2-S2V-14B`, `Wan-AI/Wan2.2-Animate-14B`, `Comfy-Org/Wan_2.2_ComfyUI_Repackaged`

### HunyuanVideo

- Output: 720p clips of 129 frames at 24 fps, T2V, I2V (v1 and v2 with 720p), Avatar, Custom, HunyuanVideo-Foley for audio
- `denoiser`: 13B dual stream then single stream transformer, 16 latent channels at 1x2x2 patch, guidance embedding
- `vae`: Hunyuan 3D causal VAE, 16 channels, 4x8x8 compression
- `text_encoder.llm`: LLaVA-llama-3-8B, hidden states after a fixed system prompt, vision tower for I2V images
- `text_encoder.clip_l`: pooled output
- Settings: `flux_flow` style with shift 7, 30 to 50 steps at distilled guidance 6 and classifier free guidance 1, `euler`
- Adapters: `lora` (FastVideo 6 step, style, camera), `ip_adapter` (IP2V), Hunyuan `controlnet`
- Canonical: `tencent/HunyuanVideo`, `tencent/HunyuanVideo-I2V`, `Comfy-Org/HunyuanVideo_repackaged`, `city96/HunyuanVideo-gguf`

### HunyuanVideo 1.5

- Output: 480p and 720p clips with a 1080p super resolution stage, T2V and I2V, distilled 480p variants
- `denoiser`: 8.3B DiT with selective and sliding tile attention
- `stage.upscaler`: the 1080p super resolution DiT over the 720p latent
- `vae`: HunyuanVideo 1.5 VAE
- `text_encoder.llm`: Qwen2.5-VL-7B, hidden states, its vision tower for I2V
- `text_encoder.glyph`: byT5-small for rendered text
- Settings: flow matching, 50 steps at guidance 6, distilled 8 steps
- Canonical: `tencent/HunyuanVideo-1.5`, `Comfy-Org/HunyuanVideo_1.5_repackaged`

### LTX-Video 0.9.x

- Output: 768x512 to 1216x704 clips at 24 to 30 fps in real time, 2B and 13B, distilled and dev, ICLoRA controls
- `denoiser`: DiT with 128 latent channels, 2B or 13B, timestep conditioned
- `vae`: LTX causal VAE, 128 channels, 8x32x32 compression, with a denoising decoder conditioned on timestep
- `text_encoder.t5`: T5-XXL encoder (PixArt's)
- Latent upscaler: the spatial latent upsampler used for the 13B two stage pass
- Settings: `ltx_flow`, 25 to 40 steps at guidance 3 with STG, distilled 8 steps at guidance 1 with fixed sigmas
- Adapters: `lora` (ICLoRA depth, pose, canny, detailer), LTX `controlnet`
- Canonical: `Lightricks/LTX-Video`, `Lightricks/LTX-Video-0.9.8-13B-distilled`

### LTX-2, LTX-2.3, LTX-2.5

- Output: joint audio and video clips, 1080p at 24 fps, up to 4K with the upscaler, T2AV, I2AV, distilled variants
- `denoiser`: the LTX-2 audio-video transformer with separate video and audio streams
- `vae`: the LTX-2 video VAE
- `vae.audio`: the LTX-2 audio VAE
- `text_encoder.llm`: Gemma-3-12B (LTX-2) and Gemma-4-12B (LTX-2.5) shipped with projection layers
- `connector`: projects encoder hidden states into both streams, separate file for GGUF encoders
- Latent upscalers: spatial and temporal upsamplers for the second pass
- Settings: `ltx2` schedule, 30 to 40 steps at guidance 3, distilled 8 steps
- Adapters: `lora` (ICLoRA, camera, style), LTX-2 `controlnet`
- Canonical: `Lightricks/LTX-2`, `Lightricks/LTX-2.5`, `unsloth/LTX-2.3-GGUF`

### MiniMax-H3

- Output: joint video and stereo audio, 24 fps latent frames, T2VA, I2VA with a first frame, FL2VA with first and last frames, Ref2VA with reference images, videos, and audio
- `denoiser`: the packed audio-video DiT, `fl2va` and `ref2va` releases, full and pruned, original time embedder or AdaLN curve table
- `vae`: the MiniMax-H3 video VAE
- `vae.audio`: the MiniMax-H3 audio VAE, stereo 32 kHz
- `text_encoder.llm`: Qwen3-VL-32B truncated to 50 language layers and exported without the final norm
- `text_encoder.llm.vision`: the Qwen3-VL vision tower with its three DeepStack mergers, bundled with the encoder or supplied separately
- Qwen3-VL receives reference images, video at 2 fps, then audio. Full rate video latents condition the denoiser
- Settings: flow matching, 3 to 8 steps with the turbo `lora` at guidance 1 and fixed sigmas, 50 steps at guidance 5 without
- Adapters: `lora` (Comfy-Org turbo 4 and 8 step for fl2v and ref2v, lightx2v dynamic rank resizes, TaoMate 3 step EMA, VDN-H3 8 step extracts, pruned `lora` requires a pruned `denoiser`), `embedding` (the minimaxh3 effect embeddings), `model_patch` (Fun ControlNet union)
- Canonical: `Comfy-Org/MiniMax-H3` (`diffusion_models/`, `vae/`, `text_encoders/`, `loras/`, `embeddings/`, `model_patches/`), `leejet/MiniMax-H3-GGUF`, `drbaph/MiniMax-H3-Turbo-Lora-ComfyUI` (LoRAs only)

### LingBot-Video

- `denoiser`: the LingBot video transformer
- `vae`: Wan 2.1 VAE
- `text_encoder.llm`: Qwen3-VL-4B

### CogVideoX and CogVideoX 1.5

- Output: 480p 49 frame clips (2B, 5B), 768p 81 frame clips (1.5 5B), T2V, I2V, Fun and Tora controls
- `denoiser`: expert transformer with 3D full attention, 2B or 5B
- `vae`: CogVideoX 3D causal VAE, 16 channels, 4x8x8
- `text_encoder.t5`: T5-XXL encoder, 226 tokens
- Settings: `v` prediction with DDIM or DPM, 50 steps at guidance 6
- Adapters: `lora`, `controlnet` (TheDenk), Tora trajectory
- Canonical: `THUDM/CogVideoX-5b`, `THUDM/CogVideoX1.5-5B-I2V`

### Mochi 1

- `denoiser`: 10B asymmetric DiT, 48 latent channels at 12 channels packed
- `vae`: AsymmVAE, 12 channels, 6x8x8
- `text_encoder.t5`: T5-XXL encoder
- Settings: flow matching, 64 steps at guidance 4.5
- Canonical: `genmo/mochi-1-preview`

### SkyReels V1, V2, A2

- V1: the HunyuanVideo blueprint fine tuned for humans, T2V and I2V
- V2: the Wan 2.1 blueprint at 1.3B, 5B, 14B with diffusion forcing for unbounded length, DF and standard T2V and I2V releases
- A2: the Wan 2.1 blueprint with reference latents for subject composition
- Canonical: `Skywork/SkyReels-V2-DF-14B-720P`

### Open-Sora 1.x and 2.0

- 1.x: STDiT denoiser, `vae` OpenSora VAE (4x8x8), `text_encoder.t5` T5-XXL
- 2.0: 11B MMDiT, `vae` HunyuanVideo VAE, `text_encoder.t5` T5-XXL and `text_encoder.clip_l`, T2V and I2V at 256p and 768p
- Canonical: `hpcai-tech/Open-Sora-v2`

### Allegro

- `denoiser`: 2.8B DiT
- `vae`: Allegro VAE, 4x8x8
- `text_encoder.t5`: T5-XXL encoder
- Canonical: `rhymes-ai/Allegro`

### Step-Video-T2V and Step-Video-TI2V

- `denoiser`: 30B DiT
- `vae`: Step Video-VAE, 16x16x8 compression
- `text_encoder.llm` Step-LLM 19B and a bilingual `text_encoder.clip_l` form (Hunyuan-CLIP)
- Canonical: `stepfun-ai/stepvideo-t2v`

### MAGI-1

- `denoiser`: autoregressive chunk denoising transformer, 4.5B and 24B, distilled forms
- `vae`: MAGI VAE, 8x8x4
- `text_encoder.t5`: T5-XXL encoder
- Canonical: `sand-ai/MAGI-1`

### Pyramid Flow

- `denoiser`: SD3 derived MMDiT with pyramidal flow matching, 2B and 5B (miniFLUX)
- `vae`: its own causal video VAE
- `text_encoder.t5` T5-XXL and `text_encoder.clip_l`
- Canonical: `rain1011/pyramid-flow-miniflux`

### EasyAnimate and CogVideoX-Fun

- EasyAnimate V5 and V5.1: a Wan style DiT, `vae` its own, `text_encoder.llm` Qwen2-VL-7B, control and inpaint variants
- CogVideoX-Fun: the CogVideoX blueprint with inpaint channels and control video inputs
- Canonical: `alibaba-pai/EasyAnimateV5.1-12b-zh`

### Latte, ModelScope T2V, Zeroscope, VideoCrafter, Show-1, LaVie, AnimateLCM, Hotshot

- Latte: DiT with `text_encoder.t5` T5-XXL and SD VAE
- ModelScope T2V, Zeroscope v2 576w and XL: 3D UNet, `text_encoder.clip_h`, SD `vae`
- VideoCrafter 1 and 2, DynamiCrafter: 3D UNet, `text_encoder.clip_h`, SD `vae`, `image_encoder.clip_vision` for I2V
- Show-1, LaVie: cascades of pixel and latent UNets with T5 and CLIP
- Canonical: `cerspense/zeroscope_v2_XL`, `VideoCrafter/VideoCrafter2`

### AnimateDiff

- Output: 16 to 32 frame clips from an SD1.5 or SDXL image blueprint
- `denoiser`: the SD1.5 or SDXL UNet
- `motion_module`: mm_sd_v14, mm_sd_v15, mm_sd_v15_v2, v3_sd15_mm with its v3 adapter `lora`, Temporaldiff, AnimateLCM, mm_sdxl_v10_beta, HotShot-XL
- `motion_lora`: pan, zoom, tilt, roll
- `vae`, `text_encoder.clip_l` (and `clip_g` for SDXL) of the base
- SparseCtrl: `controlnet` over keyframes (rgb, scribble)
- Base image adapters: `lora`, `controlnet`, `ip_adapter`, `embedding`
- Settings: as the base, plus context window length and overlap for longer clips, FreeInit, FreeNoise
- Canonical: `guoyww/animatediff`, `guoyww/animatediff-motion-adapter-v1-5-3`

### Stable Video Diffusion

- Output: 14 (SVD) or 25 (SVD-XT, 1.1) frames from one image at 576x1024
- `denoiser`: UNet with temporal layers, `edm_v` style prediction with sigma conditioning, motion bucket and fps conditioning
- `vae`: SD VAE with a temporal decoder
- `image_encoder.clip_vision`: CLIP ViT-H/14 image embeddings, no text
- Canonical: `stabilityai/stable-video-diffusion-img2vid-xt-1-1`

### Ovi

- Output: joint audio and video, 5 seconds at 24 fps
- `denoiser`: twin backbone of a Wan 2.2 5B video stream and an audio stream with cross fusion
- `vae`: Wan 2.2 VAE
- `vae.audio`: the MMAudio VAE
- `text_encoder.t5`: UMT5-XXL encoder
- Canonical: `chetwinlow1/Ovi`

### Cosmos Predict and Transfer

- Predict 1 and 2: diffusion (7B, 14B) and autoregressive (4B, 12B) world models, `vae` Cosmos tokenizer CV8x8x8 (continuous) or DV8x16x16 (discrete), `text_encoder.t5` T5-XXL (Predict 1) or Reason 1 (Predict 2), video to world with past frames as context
- Transfer 1 and 2: Predict with `controlnet` branches for depth, segmentation, edges, blur, keypoints, and HD map
- Canonical: `nvidia/Cosmos-Predict2-2B-Video2World`, `nvidia/Cosmos-Transfer1-7B`

### Autoregressive and streaming video

- CausVid, Self-Forcing, LongLive, StreamDiT, Rolling Forcing, Krea Realtime: the Wan 1.3B or 14B blueprint distilled into a causal denoiser with a KV cache over frames, 4 steps per chunk, real time
- MAGI-1, SkyReels-V2 DF, Diffusion Forcing Transformer: chunked denoising with noise levels per chunk
- Matrix-Game 1 and 2, Hunyuan-GameCraft, Oasis, Genie open reimplementations, Yume, Mirage: action conditioned world models over Wan or custom bases with keyboard and mouse inputs
- Video language models generating video tokens: Emu3, VideoPoet open reimplementations, Loong

## Talking heads and speech-driven video

Animates a face image or reference video to match speech.

| Family | Base | Audio side | Notes |
| --- | --- | --- | --- |
| Wan 2.2 S2V | Wan 2.2 14B | `audio_encoder` wav2vec2 | full body, reference image |
| MultiTalk, InfiniteTalk | Wan 2.1 I2V 14B plus an audio adapter | `audio_encoder` wav2vec2 | several speakers, unbounded length |
| FantasyTalking, OmniAvatar, StableAvatar | Wan 2.1 I2V 14B or 1.3B plus a `lora` and audio projection | `audio_encoder` wav2vec2 | |
| HunyuanVideo-Avatar, Hunyuan Portrait | HunyuanVideo 13B | `audio_encoder` Whisper | emotion control |
| Hallo, Hallo2, Hallo3 | SD1.5 UNet with temporal layers (1, 2), CogVideoX (3) | `audio_encoder` wav2vec2 | |
| EchoMimic, EchoMimic V2, V3 | SD1.5 UNet with reference net | `audio_encoder` Whisper | pose and hand |
| LatentSync 1.5, 1.6 | SD1.5 UNet in lip region, SyncNet supervision | `audio_encoder` Whisper | video to video lip sync |
| MuseTalk 1.5 | VAE plus a lip UNet | `audio_encoder` Whisper | real time |
| SadTalker | 3DMM coefficients from audio, face renderer | `audio_encoder` wav2vec2 style | one image |
| LivePortrait | appearance extractor, motion extractor, warping module, SPADE decoder, stitching and retargeting | none, driven by a video | expression transfer |
| AniPortrait, Sonic, Ditto, JoyGen, MEMO, Loopy, Float, DreamTalk, Real3D-Portrait, GeneFace++, V-Express | SD1.5 or custom bases | Whisper or wav2vec2 | |
| Wav2Lip, Wav2Lip GAN, VideoReTalking, DINet, TalkLip | GAN lip generators | mel spectrogram | classic |
| Kling and HeyGen style closed services | none on the hub | | |

## Video editing and control

| Family | Base | Input | Notes |
| --- | --- | --- | --- |
| Wan VACE 1.3B and 14B | Wan 2.1 | control video, mask, reference images | inpaint, outpaint, extend, pose, depth, restyle |
| Wan Fun-Control 2.1 and 2.2 | Wan | depth, pose, canny, trajectory, camera | |
| Wan 2.2 Animate | Wan 2.2 | pose and face from a driving video, character image | replace or animate |
| Wan Phantom | Wan 2.1 | subject references | |
| UniAnimate-Wan, Wan MoCap, Uni3C, ReCamMaster, CameraCtrl, MotionCtrl | Wan or SVD | pose sequences, camera trajectories | |
| HunyuanVideo Custom, Hunyuan Avatar, HunyuanVideo Control | HunyuanVideo | subject, audio, pose | |
| LTX ICLoRA depth, pose, canny, LTX-2 controls | LTX | control latents | |
| CogVideoX-Fun, Tora, CogVideoX ControlNet | CogVideoX | control video, trajectories | |
| AnimateDiff ControlNet, SparseCtrl, Champ, MagicAnimate, Animate Anyone, MusePose, MimicMotion, Moore Animate Anyone, UniAnimate | SD1.5 UNet with reference net and pose guider | DWPose sequences | dance and pose transfer |
| Video-P2P, TokenFlow, RAVE, Rerender A Video, FRESCO, CoDeF, AnyV2V, Go-with-the-Flow, VideoDirector | SD1.5 or SDXL with cross frame attention | source video | restyle |
| SeedEdit video, Runway Aleph, Luma Modify | none on the hub | | |
| Video inpainting and removal | ProPainter, E2FGVI, DiffuEraser, MiniMax-Remover, VideoPainter | mask sequences | object removal |
| Video colorization | DeOldify video, ColorMNet, LatentColorization | | |
| Video matting | RobustVideoMatting, MatAnyone, BiRefNet video | | |

## Video post-processing

- `upscaler` for video: Real-ESRGAN video, RealBasicVSR, BasicVSR++, SeedVR, SeedVR2 (DiT restoration with its VAE), FlashVSR, STAR, Upscale-A-Video, VEnhancer, Topaz style closed
- Frame interpolation: RIFE 4.x (flownet), FILM, GIMM-VFI, AMT, EMA-VFI, IFRNet, XVFI, Practical-RIFE
- Stabilization and deflicker: all-in-one deflicker, DeFlicker diffusion, ProPainter
- `vae.tiny` previews: TAEHV, TAEW2.1, TAE Hunyuan, TAE LTX
- Encoding: ffmpeg to mp4, webm, gif, webp, prores, with audio muxed from `vae.audio` output

## Video understanding

| Pipeline | Slots | Families |
| --- | --- | --- |
| Video question answering and captioning | vision-language model with a frame sampler. `config`: fps, max frames, token budget | Qwen2-VL, Qwen2.5-VL, Qwen3-VL, InternVL 3 and 3.5, LLaVA-Video, LLaVA-OneVision, VideoLLaMA 2 and 3, Video-XL 2, InternVideo 2.5, Tarsier 2, Apollo, Oryx, LongVU, VideoChat-Flash, Gemma 3, MiniCPM-V 4.5, Kimi-VL, GLM-4.5V, PLLaVA, Video-CCAM, VILA, NVILA, Cosmos-Reason 1 and 2 |
| Video embeddings and retrieval | `image_encoder` with temporal pooling | CLIP4Clip, X-CLIP, ViCLIP, InternVideo2 embeddings, LanguageBind, VideoPrism, V-JEPA 2, Perception Encoder |
| Action recognition | `weights` with a class head | VideoMAE, VideoMAE V2, TimeSformer, ViViT, X3D, SlowFast, MoViNet, UniFormer, Hiera |
| Tracking and segmentation | promptable models across frames | SAM 2, SAM 2.1, SAM 3, DEVA, Cutie, XMem, CoTracker 3, TAPIR, BootsTAP, SpaTracker, ByteTrack, BoT-SORT, DeepSORT |
| Pose in video | per frame `preprocessor` with tracking | DWPose, RTMPose, ViTPose, Sapiens, WHAM, GVHMR, 4D-Humans |
| Depth and geometry in video | `weights` | Video Depth Anything, DepthCrafter, ChronoDepth, RollingDepth, Depth Any Video, MegaSaM, MonST3R, CUT3R, VGGT, Aether |
| Temporal grounding and event detection | vision-language or dedicated heads | TimeChat, VTimeLLM, Momentor, UniVTG, Moment-DETR |
| Speech and sound in video | see Audio | Whisper for transcripts, MMAudio for sound, pyannote for speakers |

# Audio

## Speech recognition

Transcribes audio to text.

- `weights`: an encoder-decoder (Whisper, Canary), a CTC encoder (wav2vec2, Parakeet CTC), a transducer (Parakeet TDT, Conformer RNNT), or an audio-language model
- `config`: the feature extractor with sample rate, mel bins, window, hop, chunk length
- `tokenizer`: with language and task tokens, timestamps
- Adapters: `lora` (Whisper fine tunes), speaker adaptation
- Settings: language, task (transcribe or translate), beam size, temperature fallback, VAD, word timestamps, hotwords, chunking, batch size

| Family | Form | Notes |
| --- | --- | --- |
| Whisper tiny to large-v3, large-v3-turbo | encoder-decoder, 80 or 128 mel | 99 languages, timestamps, faster-whisper CTranslate2, whisper.cpp GGML, insanely-fast, Distil-Whisper, CrisperWhisper, Whisper-Turbo, WhisperX with alignment and diarization |
| Parakeet TDT 0.6B v2 and v3, CTC 1.1B, RNNT | FastConformer `.nemo` | English and 25 European languages, punctuation |
| Canary 1B, Canary Qwen 2.5B | encoder-decoder `.nemo`, FastConformer with Qwen3 for Canary Qwen | transcription and translation |
| wav2vec2, wav2vec2-BERT, HuBERT CTC, XLS-R, MMS-1B | CTC encoders | 1100 languages (MMS) with language adapters |
| Moonshine, Moonshine v2 | small encoder-decoder | streaming, on device |
| Voxtral Mini and Small | audio-language model | Mistral decoder, transcription mode |
| Qwen3-ASR, Qwen2-Audio, Kimi-Audio | audio-language model | |
| SenseVoice, FunASR Paraformer | non autoregressive encoders | emotion and event tags |
| Granite Speech, Phi-4 multimodal, Gemma 3n | audio-language model | |
| Conformer-CTC, Citrinet, Squeezeformer, Zipformer (icefall, sherpa) | CTC and transducer | streaming k2 models |
| Vosk, Kaldi, DeepSpeech, Coqui STT, Silero STT | classic | |
| Seamless M4T v2, SeamlessStreaming | encoder-decoder over speech and text | speech to text and speech to speech translation |
| Forced alignment: MMS-FA, wav2vec2 alignment, WhisperX, Montreal Forced Aligner, Charsiu | CTC encoders | word and phone timestamps |
| Voice activity: Silero VAD, pyannote segmentation, WebRTC VAD, TEN VAD | small classifiers | |

## Text to speech

A text frontend feeds an acoustic model that produces codec tokens or mel frames. A codec or vocoder decodes the waveform.

- `weights`: the acoustic model
- `codec`: the neural codec decoder or vocoder
- `speaker`: a style vector, a speaker embedding, or a reference clip with its transcript
- `tokenizer`: graphemes, phonemes through a phonemizer (espeak-ng, misaki, g2p), or BPE
- `text_encoder`: a description encoder for prompted voices
- Adapters: `lora` on the acoustic model for a voice
- Settings: speed, temperature, top-p, repetition penalty over codec tokens, emotion and style tags, chunking of long text, streaming

| Family | Acoustic model | Codec or vocoder | Voice input | Notes |
| --- | --- | --- | --- | --- |
| Kokoro 82M | StyleTTS2 decoder with ISTFTNet | ISTFTNet inside | style vector `.pt` per voice | misaki and espeak-ng phonemes |
| F5-TTS, E2-TTS | DiT (F5) or flat UNet (E2) flow matching over mel with ConvNeXt text | Vocos or BigVGAN | reference clip and transcript | zero shot |
| XTTS v2 | GPT-2 over DVAE tokens with a perceiver speaker encoder | HiFi-GAN decoder | reference clip | 17 languages |
| Fish Speech 1.5, OpenAudio S1 | dual autoregressive Qwen based text to semantic | Firefly-GAN VQ decoder | reference clip | emotion tags |
| Orpheus 3B | Llama-3.2-3B over SNAC tokens | SNAC 24 kHz | named voices, reference | paralinguistic tags |
| CosyVoice 2, 3 | Qwen2-0.5B to FSQ speech tokens, flow matching to mel | HiFT | reference clip, CAM++ speaker embedding | streaming, instruct |
| Chatterbox, Chatterbox Multilingual | T3 Llama backbone to S3 tokens, S3Gen flow | HiFi-GAN in S3Gen | reference clip, voice encoder | exaggeration control, Perth watermark |
| Dia 1.6B | encoder-decoder over DAC tokens | DAC | audio prompt | dialogue tags, non verbal sounds |
| Bark | text to semantic, semantic to coarse, coarse to fine GPTs | EnCodec 24 kHz | speaker prompt `.npz` | music and sound effects in text |
| Sesame CSM 1B | Llama-1B backbone with a 100M decoder over Mimi tokens | Mimi | conversation context clips | |
| Parler-TTS Mini and Large | T5 description encoder with a decoder over DAC tokens | DAC | text description of the voice | |
| Zonos v0.1 | transformer or hybrid Mamba over DAC tokens | DAC autoencoder | speaker embedding | emotion vector |
| VibeVoice 1.5B, 7B | Qwen2.5 with a diffusion head over 7.5 Hz acoustic and semantic tokens | its tokenizers | reference clip | 90 minute multi speaker |
| Higgs Audio v2 | Llama-3.2-3B with a dual FFN over a unified tokenizer | its tokenizer | reference clip | |
| IndexTTS 1.5, 2 | GPT over semantic codec, S2Mel flow | BigVGAN 2 | reference clip, emotion clip | duration control |
| Spark-TTS | Qwen2.5-0.5B over BiCodec tokens | BiCodec | reference clip, attribute tokens | |
| Kyutai TTS, Unmute | Moshi style over Mimi | Mimi | voice embeddings | streaming |
| Qwen3-TTS, MiniMax open weights, Step-Audio-TTS | audio-language decoders | codec | reference | |
| MaskGCT, MegaTTS 3, Llasa, Muyan, NeuTTS Air, Kani, VoxCPM, GPT-SoVITS 2, 3, 4 | masked or AR over semantic tokens | Vocos, BigVGAN, HiFi-GAN | reference clip | |
| Piper, VITS, MMS-TTS, SpeechT5, FastSpeech 2, Tacotron 2, Glow-TTS, Matcha-TTS, StyleTTS 2, Tortoise, Coqui TTS models, Silero TTS, MeloTTS, OpenVoice | end to end or two stage | HiFi-GAN, Vocos, WaveGlow, inside VITS | speaker id or clip | classic and small |
| Voice conversion: RVC v2, Seed-VC, Applio, OpenVoice tone color, FreeVC, kNN-VC, DDSP-SVC, so-vits-svc, Diff-SVC | content encoder (HuBERT, ContentVec, WavLM) with a pitch extractor (RMVPE, CREPE, FCPE) into a generator | HiFi-GAN or NSF-HiFiGAN | target speaker `weights` and optional feature index | |

## Codecs and vocoders

| Codec | Rate and bands | Notes |
| --- | --- | --- |
| EnCodec 24 kHz, 32 kHz, 48 kHz | 1.5 to 24 kbps, RVQ | Bark, MusicGen, AudioGen |
| DAC (Descript) 16, 24, 44 kHz | RVQ | Dia, Parler, Zonos, Stable Audio (Oobleck VAE) |
| SNAC 24 kHz, 44 kHz | hierarchical, 0.98 kbps | Orpheus |
| Mimi | 12.5 Hz semantic plus acoustic, 1.1 kbps | Moshi, CSM, Kyutai |
| WavTokenizer, X-codec, X-codec2, BigCodec, TiCodec, SemantiCodec, SpeechTokenizer, FunCodec, Vevo tokenizers, BiCodec, S3 tokenizer, Firefly, TAAE | single or few codebooks | Llasa, YuE, Spark, Fish |
| Vocoders: HiFi-GAN, BigVGAN, BigVGAN 2, Vocos, iSTFTNet, HiFT, WaveGlow, UnivNet, NSF-HiFiGAN, APNet 2, Fre-GAN, MelGAN, WaveRNN, WaveNet | mel to waveform | F5, Kokoro, IndexTTS, RVC |
| Audio VAEs: Stable Audio Oobleck, AudioLDM VAE, MMAudio VAE, ACE-Step DCAE, MiniMax-H3 audio VAE, LTX-2 audio VAE, DiffRhythm VAE | continuous latents for diffusion | |

## Music and sound generation

Generates audio from text, lyrics, a melody, or video.

| Family | Conditioning | Generator | Decoder | Notes |
| --- | --- | --- | --- | --- |
| MusicGen small, medium, large, melody, stereo | `text_encoder.t5` T5-base, chroma from a melody clip | autoregressive transformer with delay pattern | EnCodec 32 kHz | 30 second windows, continuation |
| Stable Audio Open 1.0, Small, Stable Audio 2 | `text_encoder.t5` T5-base and timing embeddings | DiT | Oobleck VAE | 47 seconds at 44.1 kHz |
| ACE-Step 1 and 1.5 | UMT5 lyric encoder and a text encoder | linear DiT | DCAE and vocoder | full songs, `lora` voices, RapMachine |
| YuE | lyrics and genre tags | stage 1 Llama-7B over xcodec semantic tokens, stage 2 upsampler | xcodec vocoder | full songs with vocals |
| DiffRhythm, DiffRhythm 2 | lyrics with timestamps, style prompt through MuQ-MuLan | DiT flow | its VAE | 4 minute songs |
| SongGen, JAM, LeVo, SongBloom, HeartMuLa, Mureka open weights | lyrics and style | AR or diffusion | codec | |
| MAGNeT | `text_encoder.t5` | masked transformer over EnCodec tokens | EnCodec | non autoregressive |
| AudioLDM 1, 2, Tango 1, 2, Make-An-Audio 2, AudioGen | CLAP, FLAN-T5, GPT-2 over AudioMAE features (LDM2) | latent diffusion UNet | AudioLDM VAE and HiFi-GAN | sound effects |
| Stable Audio Open for SFX, TangoFlux, EzAudio, AudioX, ThinkSound, Kling-Foley open, HunyuanVideo-Foley | text, video frames | flow DiT | VAE and vocoder | video to audio |
| MMAudio | `image_encoder.clip_vision` over frames, Synchformer for sync, `text_encoder.t5` for text | flow DiT | its VAE and BigVGAN | video to audio with sync |
| Riffusion, Mustango, Noise2Music reimplementations, MusicLDM | CLAP or T5 | spectrogram diffusion on SD | Griffin-Lim or HiFi-GAN | |
| Jukebox | genre, artist, lyrics | VQ-VAE with sparse transformers | VQ-VAE decoder | |
| MIDI and symbolic: Anticipatory Music Transformer, MuseNet reimplementations, Text2MIDI, NotaGen | text or events | token transformers | synthesizer | |

## Audio understanding

| Pipeline | Slots | Families |
| --- | --- | --- |
| Audio classification and tagging | `weights` with a class head over mel or waveform | AST, BEATs, PANNs, EfficientAT, HTS-AT, Audio Spectrogram Transformer, YAMNet, VGGish, wav2vec2 classifiers, WavLM, Whisper encoder probes, Dasheng, CED |
| Zero-shot audio classification and retrieval | contrastive audio and text towers | CLAP (LAION, Microsoft), MuQ-MuLan, Wav2CLIP, AudioCLIP, ImageBind audio, LanguageBind audio |
| Speaker embedding and verification | `speaker` extractors | ECAPA-TDNN, WavLM-SV, CAM++, ResNet speaker models, TitaNet, x-vector, pyannote embedding, WeSpeaker |
| Diarization | segmentation plus embedding plus clustering | pyannote 3.1, pyannote community, NeMo MSDD and Sortformer, diart, WhisperX diarization |
| Source separation | `weights` producing stems | Demucs v4 htdemucs and htdemucs_ft and 6s, Mel-Band RoFormer, BS-RoFormer, MDX-Net, UVR models, Spleeter, Open-Unmix, SCNet, Music Source Separation Training models |
| Speech enhancement and dereverberation | `weights` | DeepFilterNet 2 and 3, Resemble Enhance, VoiceFixer, FRCRN, MP-SENet, SepFormer, Demucs denoiser, NVIDIA CleanUNet, AudioSR (super resolution), FlashSR, NU-Wave 2, Apollo restoration |
| Music information retrieval | `weights` | MERT, MuQ, Jukebox probes, madmom, Basic Pitch (transcription), MT3, Omnizart, essentia models, beat trackers, chord and key estimators |
| Audio to MIDI and score | encoder-decoder | Basic Pitch, MT3, YourMT3, Onsets and Frames |
| Emotion and paralinguistics | classifiers | emotion2vec, wav2vec2 emotion, SpeechBrain emotion, SenseVoice tags |
| Audio watermarking and deepfake detection | encoders and decoders | AudioSeal, Perth, WavMark, SilentCipher, AASIST, RawNet 2, wav2vec2 antispoofing |
| Keyword spotting and wake words | small classifiers | openWakeWord, Porcupine open models, Silero, KWS on MatchboxNet, Google speech commands models |
| Lyrics and music captioning | audio-language models | LP-MusicCaps, MU-LLaMA, Qwen2-Audio, Audio Flamingo 3, SALMONN, Pengi, LTU |

## Speech language models and duplex speech

Processes and generates speech through codec tokens, using text for intermediate reasoning.

- `weights`: the language model, audio tokens in its vocabulary or through a projector
- `codec`: encoder for input and decoder for output, Mimi, SNAC, or a family tokenizer
- `speaker`: reference for the output voice
- Families: Moshi and Hibiki (Helium 7B over Mimi, full duplex), Kyutai Unmute (STT, LLM, TTS pipeline), Qwen2.5-Omni and Qwen3-Omni talkers, GLM-4-Voice, Step-Audio 2, Kimi-Audio, Baichuan-Audio, Mini-Omni 1 and 2, LLaMA-Omni 1 and 2, Freeze-Omni, SLAM-Omni, Spirit LM, VITA-Audio, Ming-Omni, Voila, PersonaPlex, Sesame CSM as the output side

# Cross-modal and other

## Contrastive embedding pairs

Two encoders trained in a shared embedding space. Either can run alone.

| Family | Towers | Notes |
| --- | --- | --- |
| CLIP, OpenCLIP, MetaCLIP, DFN, EVA-CLIP, MobileCLIP, TinyCLIP | image ViT or ConvNeXt, text transformer | `text_encoder.clip_l`, `clip_g`, `clip_h`, `image_encoder.clip_vision` |
| SigLIP, SigLIP 2, SigLIP 2 NaFlex | image ViT, text transformer, sigmoid loss | `image_encoder` of PaliGemma, Gemma 3, Idefics, InstantX adapters, Redux |
| AIMv2, PE Core, TULIP, Jina CLIP v1 and v2, Nomic Embed Vision, BGE Visualized, VLM2Vec, E5-V, GME, jina-embeddings-v4, ColPali family | image and text or one shared backbone | multimodal retrieval |
| CLAP, MuQ-MuLan, Wav2CLIP | audio and text | audio retrieval, `text_encoder` for AudioLDM |
| ImageBind, LanguageBind, OmniBind, UniBind | image, text, audio, depth, thermal, IMU, video | one embedding space |
| VideoCLIP, X-CLIP, ViCLIP, InternVideo2 CLIP, VideoPrism | video and text | |
| BLIP ITM and ITC heads, ALBEF | fused and contrastive | matching scores |

## Any-to-any and unified models

| Family | Understanding side | Generation side |
| --- | --- | --- |
| Qwen2.5-Omni, Qwen3-Omni | vision and audio towers into a thinker | talker to speech |
| BAGEL | SigLIP into Qwen2.5 MoT | FLUX VAE latents by rectified flow inside the same transformer |
| Janus-Pro, Emu3, Emu3.5, Lumina-mGPT, Chameleon, Anole, Show-o, Show-o2, Liquid, VILA-U, Transfusion reimplementations, MetaQuery, BLIP3-o, Ming-UniVision, Ming-Omni, UniWorld, OmniGen 2, Nexus-Gen, Ovis-U1, Uni-MoE | image and text tokens | image tokens or a diffusion head |
| MiniCPM-o 2.6, Baichuan-Omni 1.5, VITA 1.5, Ola, Ming-Lite-Omni, AnyGPT, NExT-GPT, CoDi, CoDi-2, Unified-IO 2 | image, video, audio | text, speech, image |
| SpeechGPT, Spirit LM, AudioPaLM reimplementations | speech and text tokens | speech and text tokens |

## Robotics and embodied models

Generates actions from camera frames, proprioception, and instructions.

- `weights`: a vision-language backbone with an action head: diffusion policy, flow matching, autoregressive action tokens, or an action expert transformer
- `image_encoder`: SigLIP, DINOv2, or the backbone's own tower
- `config`: action space, normalisation statistics, camera names, chunk size
- Families: OpenVLA, OpenVLA-OFT, π0 and π0-FAST and π0.5 (PaliGemma with a flow action expert), GR00T N1 and N1.5 (Eagle backbone with a diffusion action head), SmolVLA, RT-1 and RT-2 reimplementations, Octo, RDT-1B, Diffusion Policy, ACT, LeRobot policies (ACT, Diffusion, TDMPC, VQ-BeT, SmolVLA, π0), UniVLA, CogACT, SpatialVLA, Helix and Gemini Robotics closed, Genie Envisioner, VJEPA 2-AC, Cosmos-Predict for world simulation, MolmoAct, Isaac GR00T dataset models
- Uses: action chunks per observation at a control rate

## Time series

- `weights`: a transformer or MLP over patched or tokenized series
- `config`: context length, prediction length, quantization bins, covariate handling
- Families: Chronos and Chronos-Bolt (T5 over binned tokens, Bolt direct multi step), TimesFM 1 and 2 and 2.5, Moirai 1 and 2 and Moirai-MoE, Lag-Llama, TimeGPT open reimplementations, Toto, TiRex, Sundial, Time-MoE, TTM (Tiny Time Mixers), PatchTST, PatchTSMixer, Informer, Autoformer, TimeSeriesTransformer, N-BEATS, N-HiTS, TabPFN-TS
- Uses: forecasting with quantiles, anomaly scores, imputation, classification

## Tabular

- `weights`: an in context transformer over rows or a gradient boosting export
- Families: TabPFN v1 and v2 and TabPFN-2.5, TabICL, TabDPT, CARTE, TabuLa, XTab, TabNet, FT-Transformer, SAINT, XGBoost, LightGBM, CatBoost, scikit-learn pickles and ONNX
- Uses: classification, regression, imputation, synthetic rows

## Science

| Domain | Pipeline | Families |
| --- | --- | --- |
| Protein language models | encoder-only or causal over amino acids, embeddings, masked prediction, structure heads | ESM-2, ESM-3, ESM C, ProtT5, ProtBERT, ProGen 2 and 3, ProtGPT2, Ankh, SaProt, AMPLIFY, xTrimoPGLM |
| Protein structure | folding trunks with structure modules, `weights`, MSA or single sequence input | AlphaFold 2 and 3 open reimplementations (OpenFold, ColabFold, Boltz-1 and Boltz-2, Chai-1, Protenix, HelixFold 3), ESMFold, OmegaFold, RoseTTAFold and RoseTTAFold All-Atom |
| Protein design and generation | diffusion or flow over backbones and sequences | RFdiffusion, RFdiffusion All-Atom, Chroma (Generate Biomedicines), ProteinMPNN, LigandMPNN, Genie 2, FrameDiff, FrameFlow, Multiflow, ESM-3 generation, EvoDiff, DPLM, ProtGPT2 |
| Small molecules | SMILES and graph language models, property predictors, docking | ChemBERTa, MolT5, MolFormer, Uni-Mol, GROVER, MolGPT, DiffDock and DiffDock-L, EquiBind, TorsionDiff, MolDiff, GeoDiff, Boltz-2 affinity, MACE, Orb, UMA and OMol25, MatterSim, SevenNet, CHGNet, M3GNet (interatomic potentials) |
| Genomics | causal or masked over nucleotides | Nucleotide Transformer, DNABERT 1 and 2, HyenaDNA, Evo 1 and 2, Caduceus, GENA-LM, Enformer, Borzoi, AlphaGenome open reimplementations, scGPT and Geneformer and scFoundation (single cell) |
| Medical imaging | classifiers, segmenters, VLMs | MedSAM, MedSAM 2, SAM-Med3D, nnU-Net, TotalSegmentator, MONAI bundles, CheXagent, LLaVA-Med, MedGemma, BiomedCLIP, RadFM, CXR-Foundation, Path Foundation, UNI, CONCH, Virchow, Prov-GigaPath (pathology), RETFound, Med-PaLM open reimplementations |
| Weather and climate | transformers or graph networks over gridded fields | GraphCast, GenCast, Aurora, Pangu-Weather, FourCastNet, FengWu, ClimaX, Prithvi WxC, NeuralGCM, ECMWF AIFS, Stormer |
| Earth observation | encoders over satellite bands | Prithvi EO 1 and 2, SatMAE, Clay, DOFA, SSL4EO, Scale-MAE, TerraMind, AlphaEarth open reimplementations, SpectralGPT |
| Math and formal | causal language models with proof assistants | DeepSeek-Prover 1.5 and 2, Goedel-Prover, Kimina-Prover, Lean models, AlphaProof reimplementations, Qwen2.5-Math, DeepSeek-Math, InternLM-Math |

## Reinforcement learning and games

- `weights`: policy and value networks, or a world model
- `config`: observation and action spaces, environment id
- Families: Stable-Baselines3 zoo (PPO, SAC, TD3, DQN, A2C), CleanRL, Decision Transformer, Trajectory Transformer, Gato reimplementations, MuZero and EfficientZero reimplementations, Dreamer V3, TD-MPC2, Agent57 reimplementations, Leela Chess Zero and Maia (chess), KataGo (go), Stockfish NNUE networks, OpenSpiel policies, Minecraft VPT and STEVE-1 and MineDojo, Atari and MuJoCo and Procgen zoo checkpoints, Sample Factory, RLlib exports, IsaacGym and IsaacLab policies, Unitree and Booster locomotion policies

## Graph and structured data

- `weights`: message passing or graph transformers
- Families: GraphSAGE, GAT, GIN, Graphormer, GraphGPS, GNN exports from PyG and DGL, knowledge graph embeddings (TransE, RotatE, ComplEx), GraphMAE, OFA, GraphGPT, molecule graphs under Science

## Visual document and GUI agents

Extracts data or chooses actions from screenshots.

- `weights`: a vision-language model with grounding, trained on click and type actions
- `config`: screen resolution scaling, action vocabulary
- Families: UI-TARS 1.5 and 2, OS-Atlas, ShowUI, Aguvis, UGround, SeeClick, CogAgent, Ferret-UI, OmniParser (YOLO icon detection plus Florence-2 captions), GUI-Actor, Qwen2.5-VL and Qwen3-VL computer use, Holo1 and Holo2, Fara-7B, GLM-4.5V agents, Magma, ScreenAI reimplementations, Mind2Web models, WebVoyager style agents, Browser Use models

## Interpretability and training artifacts

| Artifact | Holds | Families |
| --- | --- | --- |
| Sparse autoencoders | encoder and decoder over one layer's residual stream | Gemma Scope, Llama Scope, OpenAI GPT-2 SAEs, EleutherAI SAEs, SAELens releases, Anthropic style feature dictionaries reimplemented |
| Probes and steering | linear probes, `control_vector` sets | representation engineering vectors, refusal directions, truthfulness probes |
| Training checkpoints | intermediate `weights` with optimizer state | Pythia suite, OLMo checkpoints, LLM360 Amber and K2, SmolLM intermediate, DCLM |
| Tokenizers alone | `tokenizer` | tiktoken exports, SentencePiece models, byte level BPEs |
| Data filters and classifiers | small encoders | FineWeb-Edu classifier, DCLM fastText, NSFW text filters, quality scorers, language id |
| Distillation teachers and logits | logit caches | Gemma 2 and Llama distillation sets |
| Adapters and merges | `lora`, TIES and DARE and SLERP merges as full `weights` | mergekit outputs, model soups |
| Quantization calibration | importance matrices, calibration sets | llama.cpp `imatrix.dat`, AWQ scales, GPTQ calibration |
