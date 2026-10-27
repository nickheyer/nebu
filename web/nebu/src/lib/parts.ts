import { ArtifactRole, ModelKind, type Descriptor } from '$proto/model_pb';
import type { StoredArtifact, StoredModel } from '$proto/store_pb';
import type { Param } from '$proto/runtime_pb';
import { live, modelKey } from './state.svelte';
import { weightsName } from './catalog';
import { canonical, isStoreRef, kindOf, partOf, partWord, storeRef } from './diffusion';
import { byName, tail } from './format';

// A stored group the daemon resolves to the file a path parameter wants, or one stored file
export interface Candidate {
  value: string;
  label: string;
  // Why it suits the parameter, empty for a file offered as any other
  reason: string;
  // The file the value loads and its size, empty when only the daemon can tell
  path: string;
  sizeBytes: bigint;
  // The group the list shows the candidate under
  heading: string;
  model: StoredModel;
  rank: number;
}

// What a value names once chosen
export interface Chosen {
  label: string;
  detail: string;
  sizeBytes: bigint;
  stored: boolean;
}

const slotLabels: Record<string, string> = {
  denoiser: 'denoiser',
  'denoiser.high_noise': 'high noise denoiser',
  'denoiser.uncond': 'unconditional denoiser',
  refiner: 'refiner',
  'stage.prior': 'prior stage',
  'stage.decoder': 'decoder stage',
  vae: 'VAE',
  'vae.audio': 'audio VAE',
  'vae.tiny': 'tiny VAE',
  'text_encoder.clip_l': 'CLIP-L text encoder',
  'text_encoder.clip_g': 'CLIP-G text encoder',
  'text_encoder.clip_h': 'CLIP-H text encoder',
  'text_encoder.t5': 'T5 text encoder',
  'text_encoder.llm': 'language model text encoder',
  'text_encoder.llm.vision': 'text encoder vision tower',
  'text_encoder.glyph': 'glyph text encoder',
  'image_encoder.clip_vision': 'CLIP vision encoder',
  audio_encoder: 'audio encoder',
  tokenizer: 'tokenizer',
  connector: 'embeddings connectors',
  safety: 'safety checker'
};

export function slotLabel(slot: string): string {
  return slotLabels[slot] ?? slot;
}

// The part the model itself bundles for a parameter, as a label, empty when it bundles none
export function bundled(model: StoredModel | null, p: Param): string {
  if (!model || !p.slots.length) return '';
  const d = model.descriptor;
  const declared = pipelineSlots(d);
  const inFile = new Set((d?.metadata['diffusion.slots'] ?? '').split(',').filter(Boolean));
  const slot = p.slots.find((s) => declared[s] || inFile.has(s));
  return slot ? slotLabel(slot) : '';
}

const pipelinePrefix = 'pipeline.';

export function isPipeline(d: Descriptor | undefined): boolean {
  return !!d?.metadata['pipeline.class'];
}

// The blueprint slot a declared pipeline component fills, as the daemon reads the component key
function slotOfKey(key: string, cls: string): string {
  const k = key.toLowerCase();
  const c = cls.toLowerCase();
  if (k === 'transformer_2' || k === 'unet_2') return 'denoiser.high_noise';
  if (k.startsWith('prior')) return 'stage.prior';
  if (k === 'decoder') return 'stage.decoder';
  if (k.startsWith('transformer') || k.startsWith('unet') || c.includes('transformer') || c.includes('unet')) {
    if (k.includes('uncond')) return 'denoiser.uncond';
    if (k.includes('refiner')) return 'refiner';
    return 'denoiser';
  }
  if (k.includes('vae') || k === 'movq' || k === 'vqvae' || c.includes('autoencoder')) {
    if (k.includes('audio') || c.includes('audio')) return 'vae.audio';
    if (c.includes('tiny')) return 'vae.tiny';
    return 'vae';
  }
  if (k.startsWith('text_encoder')) {
    switch (canonical(cls)) {
      case 't5':
        return 'text_encoder.t5';
      case 'clip_l':
        return 'text_encoder.clip_l';
      case 'clip_g':
        return 'text_encoder.clip_g';
      case 'clip_h':
        return 'text_encoder.clip_h';
    }
    return 'text_encoder.llm';
  }
  if (k.startsWith('image_encoder')) return 'image_encoder.clip_vision';
  if (k.startsWith('audio_encoder') || k.startsWith('speech_encoder')) return 'audio_encoder';
  if (k.startsWith('tokenizer')) return 'tokenizer';
  if (k === 'safety_checker') return 'safety';
  return '';
}

// Subfolders a pipeline declares, by the blueprint slot each fills
export function pipelineSlots(d: Descriptor | undefined): Record<string, string> {
  const m = d?.metadata ?? {};
  const parts: Record<string, string> = {};
  const classes: Record<string, string> = {};
  for (const [k, v] of Object.entries(m)) {
    if (!k.startsWith(pipelinePrefix) || k === 'pipeline.class') continue;
    const rest = k.slice(pipelinePrefix.length);
    if (rest.endsWith('.class')) classes[rest.slice(0, -'.class'.length)] = v;
    else if (!rest.endsWith('.vision')) parts[rest] = v;
  }
  const wan = canonical(m['diffusion.family'] ?? '') === 'wan' && !!parts['transformer_2'];
  const out: Record<string, string> = {};
  for (const key of Object.keys(parts).sort()) {
    let slot = slotOfKey(key, classes[key] ?? '');
    if (!slot) continue;
    if (wan && key.toLowerCase() === 'transformer') slot = 'denoiser.high_noise';
    if (wan && key.toLowerCase() === 'transformer_2') slot = 'denoiser';
    if (!(slot in out)) out[slot] = parts[key];
    if (slot === 'text_encoder.llm' && m[pipelinePrefix + key + '.vision'] === 'true' && !('text_encoder.llm.vision' in out)) out['text_encoder.llm.vision'] = parts[key];
  }
  return out;
}

function roleFiles(m: StoredModel, role: ArtifactRole): StoredArtifact[] {
  return m.artifacts.filter((a) => a.artifact?.role === role);
}

function tokenizerOf(m: StoredModel): StoredArtifact | undefined {
  return m.artifacts.find((a) => a.artifact?.role === ArtifactRole.TOKENIZER && tail(a.artifact.path) === 'tokenizer.json');
}

function sizeOf(files: StoredArtifact[]): bigint {
  return files.reduce((n, a) => n + (a.artifact?.sizeBytes ?? 0n), 0n);
}

function dirOf(path: string): string {
  const i = Math.max(path.lastIndexOf('/'), path.lastIndexOf('\\'));
  return i > 0 ? path.slice(0, i) : path;
}

// The index of a sharded group, the single file, or the folder holding every shard for a directory
function weightsFile(files: StoredArtifact[], index: StoredArtifact | undefined, directory: boolean): { path: string; sizeBytes: bigint } | undefined {
  if (!files.length) return undefined;
  if (directory) {
    const dirs = new Set(files.map((a) => dirOf(a.path)));
    return dirs.size === 1 ? { path: dirOf(files[0].path), sizeBytes: sizeOf(files) } : undefined;
  }
  if (files.length === 1) return { path: files[0].path, sizeBytes: sizeOf(files) };
  return index ? { path: index.path, sizeBytes: sizeOf(files) } : undefined;
}

// The file a stored group loads for a parameter, as the daemon resolves a store reference
export function loads(m: StoredModel, p: Param): { path: string; sizeBytes: bigint } | undefined {
  const picks = p.picks;
  if (isPipeline(m.descriptor)) {
    const declared = pipelineSlots(m.descriptor);
    const slots = [...p.slots];
    if (picks === '' || picks === 'model' || picks === 'diffusion') slots.push('denoiser');
    for (const slot of slots) {
      const sub = declared[slot];
      if (!sub) continue;
      const under = m.artifacts.filter((a) => (a.artifact?.path ?? '').startsWith(sub + '/'));
      if (slot === 'tokenizer') {
        const t = under.find((a) => tail(a.artifact?.path ?? '') === 'tokenizer.json');
        return t ? { path: t.path, sizeBytes: t.artifact?.sizeBytes ?? 0n } : undefined;
      }
      return weightsFile(
        under.filter((a) => a.artifact?.role === ArtifactRole.WEIGHTS),
        under.find((a) => a.artifact?.role === ArtifactRole.INDEX),
        p.directory
      );
    }
    return undefined;
  }
  switch (picks) {
    case 'tokenizer': {
      const t = tokenizerOf(m);
      return t ? { path: t.path, sizeBytes: t.artifact?.sizeBytes ?? 0n } : undefined;
    }
    case 'projector': {
      const f = roleFiles(m, ArtifactRole.PROJECTOR)[0];
      return f ? { path: f.path, sizeBytes: f.artifact?.sizeBytes ?? 0n } : undefined;
    }
    case 'draft': {
      const f = roleFiles(m, ArtifactRole.DRAFT)[0];
      if (f) return { path: f.path, sizeBytes: f.artifact?.sizeBytes ?? 0n };
      break;
    }
    case 'checkpoint':
      if (m.path) return { path: m.path, sizeBytes: m.bytes };
  }
  return weightsFile(roleFiles(m, ArtifactRole.WEIGHTS), roleFiles(m, ArtifactRole.INDEX)[0], p.directory);
}

export function groupHeading(m: StoredModel): string {
  return `${tail(m.repo)} · ${weightsName(m.group, m.formatId)}`;
}

// Why a stored group suits a parameter and how well, undefined when it does not
function suits(p: Param, m: StoredModel, self: StoredModel | null): { reason: string; rank: number } | undefined {
  const d = m.descriptor;
  const kind = kindOf(d);
  const own = !!self && modelKey(self) === modelKey(m);
  const sameArch = !!self?.descriptor?.architecture && d?.architecture === self.descriptor.architecture;
  const smaller = !!self?.descriptor?.parameterCount && !!d?.parameterCount && d.parameterCount < self.descriptor.parameterCount;
  switch (p.picks) {
    case 'projector':
      if (roleFiles(m, ArtifactRole.PROJECTOR).length) return { reason: 'holds a projector', rank: own ? 0 : 1 };
      if (kind === ModelKind.COMPONENT && partOf(d) === 'projector') return { reason: 'vision projector', rank: 1 };
      return undefined;
    case 'draft':
      if (own) return undefined;
      if (roleFiles(m, ArtifactRole.DRAFT).length) return { reason: 'MTP head file', rank: 0 };
      if (kind !== ModelKind.LANGUAGE) return undefined;
      if (self && m.formatId !== self.formatId) return undefined;
      if (sameArch) return { reason: smaller ? 'same architecture, smaller' : 'same architecture', rank: smaller ? 1 : 2 };
      return { reason: 'language model', rank: smaller ? 3 : 4 };
    case 'checkpoint':
      if (own || kind !== ModelKind.LANGUAGE || m.formatId !== 'safetensors') return undefined;
      return sameArch ? { reason: smaller ? 'same architecture, smaller' : 'same architecture', rank: 1 } : { reason: 'language model', rank: 3 };
    case 'tokenizer':
      return tokenizerOf(m) ? { reason: 'holds tokenizer.json', rank: own ? 0 : 1 } : undefined;
    case 'model':
    case 'diffusion':
      if (own) return undefined;
      if (kind === ModelKind.DIFFUSION) return { reason: 'diffusion model', rank: 1 };
      return pipelineSlots(d)['denoiser'] ? { reason: 'pipeline denoiser', rank: 1 } : undefined;
    default: {
      if (kind === ModelKind.COMPONENT && partOf(d) === p.picks) return { reason: partWord(d), rank: 1 };
      const declared = pipelineSlots(d);
      const slot = p.slots.find((s) => declared[s]);
      if (slot) return { reason: `bundled ${slotLabel(slot)}`, rank: own ? 0 : 2 };
      return undefined;
    }
  }
}

// Files of the part's own role, offered one by one beside their group
function partFiles(p: Param, m: StoredModel): { file: StoredArtifact; reason: string }[] {
  switch (p.picks) {
    case 'projector':
      return roleFiles(m, ArtifactRole.PROJECTOR).map((file) => ({ file, reason: 'projector file' }));
    case 'draft':
      return roleFiles(m, ArtifactRole.DRAFT).map((file) => ({ file, reason: 'MTP heads' }));
    case 'tokenizer': {
      const t = tokenizerOf(m);
      return t ? [{ file: t, reason: 'tokenizer' }] : [];
    }
  }
  return [];
}

function stored(): StoredModel[] {
  return [...live.models.values()].sort(byName((m) => m.repo + m.group));
}

// What suits the parameter, ranked, and every other stored file for the cases nothing suits
export function candidates(p: Param, self: StoredModel | null): { suggested: Candidate[]; others: Candidate[] } {
  const suggested: Candidate[] = [];
  const others: Candidate[] = [];
  const seen = new Set<string>();
  const push = (list: Candidate[], c: Candidate) => {
    if (seen.has(c.value)) return;
    seen.add(c.value);
    list.push(c);
  };
  const models = stored();
  for (const m of models) {
    const heading = groupHeading(m);
    const fit = suits(p, m, self);
    if (fit) {
      const file = loads(m, p);
      push(suggested, { value: storeRef(m), label: heading, reason: fit.reason, path: file?.path ?? '', sizeBytes: file?.sizeBytes ?? 0n, heading: '', model: m, rank: fit.rank });
    }
    for (const { file, reason } of partFiles(p, m)) {
      push(suggested, { value: file.path, label: tail(file.artifact?.path || file.path), reason, path: file.path, sizeBytes: file.artifact?.sizeBytes ?? 0n, heading, model: m, rank: fit ? fit.rank : 2 });
    }
  }
  suggested.sort((a, b) => a.rank - b.rank || a.label.localeCompare(b.label));
  for (const m of models) {
    const heading = groupHeading(m);
    for (const a of m.artifacts) {
      push(others, { value: a.path, label: tail(a.artifact?.path || a.path), reason: '', path: a.path, sizeBytes: a.artifact?.sizeBytes ?? 0n, heading, model: m, rank: 9 });
    }
  }
  return { suggested, others };
}

// Finds the stored group a reference names
export function refModel(value: string): StoredModel | undefined {
  if (!isStoreRef(value)) return undefined;
  const rest = value.slice('store://'.length);
  const hash = rest.lastIndexOf('#');
  if (hash < 0) return undefined;
  const group = rest.slice(hash + 1);
  const repoPath = rest.slice(0, hash);
  const slash = repoPath.indexOf('/');
  if (slash < 0) return undefined;
  return live.models.get(modelKey({ sourceId: repoPath.slice(0, slash), repo: repoPath.slice(slash + 1), group }));
}

// Describes a value as the stored group and file it loads, a stored file, or a bare path
export function describe(value: string, p: Param): Chosen | undefined {
  const v = value.trim();
  if (!v) return undefined;
  if (v.toLowerCase() === 'none') return { label: 'None', detail: 'loads nothing', sizeBytes: 0n, stored: true };
  if (v.toLowerCase() === 'auto') return { label: 'Auto', detail: '', sizeBytes: 0n, stored: true };
  const m = refModel(v);
  if (m) {
    const file = loads(m, p);
    return { label: groupHeading(m), detail: file ? tail(file.path) : 'the file it loads', sizeBytes: file?.sizeBytes ?? 0n, stored: true };
  }
  for (const model of live.models.values()) {
    const a = model.artifacts.find((x) => x.path === v);
    if (a) return { label: tail(a.artifact?.path || v), detail: groupHeading(model), sizeBytes: a.artifact?.sizeBytes ?? 0n, stored: true };
    if (model.path && v === model.path) return { label: groupHeading(model), detail: 'its directory', sizeBytes: model.bytes, stored: true };
  }
  return { label: tail(v), detail: dirOf(v) === v ? '' : dirOf(v), sizeBytes: 0n, stored: false };
}
