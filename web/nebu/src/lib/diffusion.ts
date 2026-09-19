import { ModelKind, type Descriptor } from '$proto/model_pb';
import type { StoredModel } from '$proto/store_pb';
import { ArtifactRole } from '$proto/model_pb';

// Normalize header, Diffusers, and stable-diffusion.cpp names to daemon IDs.
const canonicalNames: Record<string, string> = {
  t5encoder: 't5', t5: 't5', umt5: 't5', byt5: 't5', t5encodermodel: 't5', umt5encodermodel: 't5', 'flan-t5': 't5',
  cliptextmodel: 'clip_l', clip_l: 'clip_l', 'clip-l': 'clip_l',
  cliptextmodelwithprojection: 'clip_g', clip_g: 'clip_g', 'clip-g': 'clip_g',
  autoencoderkl: 'vae', autoencoderklwan: 'vae', autoencoderklqwenimage: 'vae', autoencoderklltxvideo: 'vae', autoencoderklhunyuanvideo: 'vae', autoencoderklcosmos: 'vae', autoencoderklflux2: 'vae', autoencoderklmagvit: 'vae', vae: 'vae',
  autoencoderklltxaudio: 'audio_vae', audio_vae: 'audio_vae', autoencodertiny: 'taesd', taesd: 'taesd', taehv: 'taesd', tae: 'taesd',
  clipvisionmodelwithprojection: 'clip_vision', clipvisionmodel: 'clip_vision', siglipvisionmodel: 'clip_vision', clip_vision: 'clip_vision', 'clip-vision': 'clip_vision',
  wav2vec2model: 'audio_encoder', wav2vec2: 'audio_encoder', audio_encoder: 'audio_encoder',
  connectors: 'embeddings_connectors', embeddings_connectors: 'embeddings_connectors',
  lora: 'lora', controlnet: 'controlnet', controlnetmodel: 'controlnet', ip_adapter: 'ip_adapter', 'ip-adapter': 'ip_adapter', photo_maker: 'photo_maker', photomaker: 'photo_maker', pulid: 'pulid', motion_module: 'motion_module', motionadapter: 'motion_module', embedding: 'embedding', textual_inversion: 'embedding', upscaler: 'upscaler', esrgan: 'upscaler', realesrgan: 'upscaler', detector: 'detector', yolo: 'detector',
  sd1: 'sd1', 'sd1.5': 'sd1', sd15: 'sd1', unet2dconditionmodel: 'sd1', 'stable-diffusion': 'sd1', sd2: 'sd2', 'sd2.1': 'sd2', sdxl: 'sdxl', 'sdxl-turbo': 'sdxl', ssd1b: 'sdxl', vega: 'sdxl', svd: 'svd',
  sd3: 'sd3', 'sd3.5': 'sd3', sd35: 'sd3', sd3transformer2dmodel: 'sd3', mmdit: 'sd3',
  flux: 'flux', flux1: 'flux', 'flux.1': 'flux', fluxtransformer2dmodel: 'flux', kontext: 'flux', chroma: 'chroma', chromatransformer2dmodel: 'chroma', chroma_radiance: 'chroma_radiance', 'chroma-radiance': 'chroma_radiance', 'chroma1-radiance': 'chroma_radiance',
  flux2: 'flux2', 'flux.2': 'flux2', flux2transformer2dmodel: 'flux2', flux2_klein: 'flux2_klein', 'flux2-klein': 'flux2_klein', klein: 'flux2_klein',
  wan: 'wan', wan2: 'wan', 'wan2.1': 'wan', 'wan2.2': 'wan', wantransformer3dmodel: 'wan', wanvacetransformer3dmodel: 'wan', vace: 'wan', lingbot_video: 'lingbot_video', lingbot: 'lingbot_video', 'lingbot-video': 'lingbot_video',
  qwen_image: 'qwen_image', qwenimage: 'qwen_image', 'qwen-image': 'qwen_image', qwenimagetransformer2dmodel: 'qwen_image', qwen_image_edit: 'qwen_image',
  hunyuan_video: 'hunyuan_video', hyvid: 'hunyuan_video', hunyuanvideo: 'hunyuan_video', hunyuanvideotransformer3dmodel: 'hunyuan_video', 'hunyuan_video_1.5': 'hunyuan_video', anima: 'anima', anima2: 'anima',
  ltx2: 'ltx2', ltxav: 'ltx2', 'ltx-2': 'ltx2', 'ltx2.3': 'ltx2', 'ltx-2.3': 'ltx2', 'ltx2.5': 'ltx2', 'ltx-2.5': 'ltx2', ltxv: 'ltx2', ltx: 'ltx2', ltxvideotransformer3dmodel: 'ltx2',
  minimax_h3: 'minimax_h3', 'minimax-h3': 'minimax_h3', minimaxh3: 'minimax_h3', hidream_o1: 'hidream_o1', 'hidream-o1': 'hidream_o1', hidream: 'hidream_o1', hidreamimagetransformer2dmodel: 'hidream_o1',
  z_image: 'z_image', zimage: 'z_image', 'z-image': 'z_image', lumina2: 'z_image', lumina: 'z_image', lumina2transformer2dmodel: 'z_image',
  boogu_image: 'boogu_image', boogu: 'boogu_image', 'boogu-image': 'boogu_image', ovis_image: 'ovis_image', ovis: 'ovis_image', 'ovis-image': 'ovis_image', ernie_image: 'ernie_image', ernie: 'ernie_image', 'ernie-image': 'ernie_image',
  lens: 'lens', 'lens-turbo': 'lens', minit2i: 'minit2i', longcat: 'longcat', longcat_image: 'longcat', 'longcat-image': 'longcat', pid: 'pid', pixeldit: 'pid', 'pid1.5': 'pid',
  ideogram4: 'ideogram4', 'ideogram-4': 'ideogram4', ideogram: 'ideogram4', sefi_image: 'sefi_image', sefi: 'sefi_image', 'sefi-image': 'sefi_image', krea2: 'krea2', 'krea-2': 'krea2', krea_2: 'krea2',
  mage_flow: 'mage_flow', 'mage-flow': 'mage_flow', mageflow: 'mage_flow', mage: 'mage_flow', sensenova_u1: 'sensenova_u1', sensenova_u1_5: 'sensenova_u1', 'sensenova-u1.5': 'sensenova_u1', sensenova: 'sensenova_u1',
  llm: 'llm', text_encoder: 'text_encoder'
};

export function canonical(architecture: string): string {
  const a = architecture.trim().toLowerCase();
  return canonicalNames[a] ?? a;
}

const partWords: Record<string, string> = {
  vae: 'VAE', audio_vae: 'audio VAE', taesd: 'tiny autoencoder', t5: 'T5 text encoder', clip_l: 'CLIP-L text encoder', clip_g: 'CLIP-G text encoder', llm: 'language model text encoder', text_encoder: 'text encoder', clip_vision: 'CLIP vision encoder', audio_encoder: 'audio encoder', embeddings_connectors: 'embeddings connectors',
  lora: 'LoRA', controlnet: 'ControlNet', ip_adapter: 'IP-Adapter', photo_maker: 'PhotoMaker model', pulid: 'PuLID weights', motion_module: 'AnimateDiff motion module', embedding: 'textual inversion embedding', upscaler: 'upscaler', detector: 'ADetailer detector'
};

// Prefer tensor metadata over the configured architecture.
export function partOf(d: Descriptor | undefined): string {
  return d?.metadata['diffusion.component'] || canonical(d?.architecture ?? '');
}

export function partWord(d: Descriptor | undefined): string {
  return partWords[partOf(d)] ?? 'pipeline part';
}

// Label diffusion outputs and components. Leave language models unlabeled.
export function kindLabel(d: Descriptor | undefined): string {
  switch (d?.kind) {
    case ModelKind.DIFFUSION:
      return d.generates.length ? d.generates.join(' + ') : 'diffusion';
    case ModelKind.COMPONENT:
      return partWord(d);
  }
  return '';
}

export function isComponent(d: Descriptor | undefined): boolean {
  return d?.kind === ModelKind.COMPONENT;
}

// Default to a language model when the descriptor has no kind.
export function kindOf(d: Descriptor | undefined): ModelKind {
  return d?.kind || ModelKind.LANGUAGE;
}

export function hitKindLabel(kind: ModelKind): string {
  switch (kind) {
    case ModelKind.DIFFUSION:
      return 'image/video';
    case ModelKind.COMPONENT:
      return 'part';
    case ModelKind.LANGUAGE:
      return 'language';
  }
  return '';
}

export function kindWord(kind: ModelKind): string {
  switch (kind) {
    case ModelKind.DIFFUSION:
      return 'diffusion';
    case ModelKind.COMPONENT:
      return 'component';
    default:
      return 'language';
  }
}

// sd-server requires tokenizer.json.
function tokenizerOf(m: StoredModel): string {
  return m.artifacts.find((a) => a.artifact?.role === ArtifactRole.TOKENIZER && a.artifact.path.endsWith('tokenizer.json'))?.path ?? '';
}

// Resolve a path parameter to its stored file or directory.
export function pickedPath(m: StoredModel, picks: string): string {
  if (picks === 'tokenizer') return tokenizerOf(m);
  if (picks === 'checkpoint') return m.path;
  const role = picks === 'projector' ? ArtifactRole.PROJECTOR : ArtifactRole.WEIGHTS;
  const file = m.artifacts.find((a) => a.artifact?.role === role)?.path ?? '';
  if (!file) return '';
  if (picks === 'lora' || picks === 'embedding' || picks === 'upscaler') return file.slice(0, file.lastIndexOf('/'));
  return file;
}

export function picksModel(m: StoredModel, picks: string): boolean {
  const d = m.descriptor;
  const arch = partOf(d);
  switch (picks) {
    case '':
    case 'model':
      return true;
    case 'llm':
      return d?.kind === ModelKind.LANGUAGE || (d?.kind === ModelKind.COMPONENT && arch === 'llm');
    case 'projector':
      return m.artifacts.some((a) => a.artifact?.role === ArtifactRole.PROJECTOR);
    case 'tokenizer':
      return tokenizerOf(m) !== '';
    case 'checkpoint':
      return d?.kind === ModelKind.LANGUAGE && m.formatId === 'safetensors' && m.path !== '';
    case 'diffusion':
      return d?.kind === ModelKind.DIFFUSION;
    default:
      return d?.kind === ModelKind.COMPONENT && arch === picks;
  }
}
