import { errorMessage } from './chatClient';
import { gatewayCredentials } from './auth.svelte';
import { deleteImages, type Attachment } from './images';
import { readLocal, writeLocal } from './persist';

export interface MediaRequest {
  model: string;
  prompt: string;
  negative_prompt?: string;
  width?: number;
  height?: number;
  steps?: number;
  cfg_scale?: number;
  guidance?: number;
  flow_shift?: number;
  seed?: number;
  sampler?: string;
  scheduler?: string;
  clip_skip?: number;
  strength?: number;
  init_image?: string;
  vae_tiling?: boolean;
  output_format?: string;
  n?: number;
  frames?: number;
  fps?: number;
  end_image?: string;
  temporal_tiling?: boolean;
  high_noise?: { steps?: number; cfg_scale?: number; sampler?: string; scheduler?: string; flow_shift?: number };
  // Paths are relative to the runtime's LoRA directory.
  lora?: { path: string; multiplier: number; is_high_noise?: boolean }[];
}

// Parse comma-separated name:weight pairs. Default weight is 1.
// The high: prefix selects the high noise stage.
export function parseLoras(text: string): MediaRequest['lora'] {
  const out: NonNullable<MediaRequest['lora']> = [];
  for (const part of text.split(',')) {
    let item = part.trim();
    if (!item) continue;
    const high = item.startsWith('high:');
    if (high) item = item.slice(5).trim();
    const i = item.lastIndexOf(':');
    let path = item;
    let multiplier = 1;
    if (i > 0) {
      const n = parseFloat(item.slice(i + 1));
      if (!Number.isNaN(n)) {
        path = item.slice(0, i).trim();
        multiplier = n;
      }
    }
    if (path) out.push({ path, multiplier, ...(high ? { is_high_noise: true } : {}) });
  }
  return out.length ? out : undefined;
}

export function lorasText(list: MediaRequest['lora']): string {
  return (list ?? []).map((l) => `${l.is_high_noise ? 'high:' : ''}${l.path}${l.multiplier === 1 ? '' : ':' + l.multiplier}`).join(', ');
}

export interface VideoObject {
  id: string;
  status: 'queued' | 'in_progress' | 'completed' | 'failed';
  model: string;
  size?: string;
  frames?: number;
  fps?: number;
  seconds?: number;
  output_format: string;
  mime_type: string;
  created_at: number;
  completed_at?: number | null;
  error?: { code: string; message: string } | null;
  trace?: string;
  bytes?: number;
}

// Sampler defaults from the model's capabilities endpoint.
export interface SampleDefaults {
  scheduler: string;
  sample_method: string;
  sample_steps: number;
  eta: number | null;
  shifted_timestep: number;
  flow_shift: number | null;
  guidance: { txt_cfg: number; img_cfg: number | null; distilled_guidance: number; slg: { layers: number[]; layer_start: number; layer_end: number; scale: number } };
}

export interface ModeDefaults {
  prompt: string;
  negative_prompt: string;
  clip_skip: number;
  width: number;
  height: number;
  strength: number;
  seed: number;
  batch_count?: number;
  video_frames?: number;
  fps?: number;
  moe_boundary?: number;
  sample_params: SampleDefaults;
  high_noise_sample_params?: SampleDefaults;
  output_format: string;
  output_compression: number;
}

export interface Capabilities {
  supported_modes: string[];
  samplers: string[];
  schedulers: string[];
  defaults_by_mode: Record<string, ModeDefaults>;
  output_formats_by_mode: Record<string, string[]>;
  limits: { min_width?: number; max_width?: number; min_height?: number; max_height?: number; max_batch_count?: number };
  loras?: { name: string }[];
}

// Capabilities key: img_gen or vid_gen.
export const modeKey = (mode: 'image' | 'video') => (mode === 'video' ? 'vid_gen' : 'img_gen');

// Hide sentinel values for automatic sampler and scheduler selection.
export function namedChoice(value: string | undefined): string {
  return value && value !== 'default' ? value : '';
}

export function figure(n: number | null | undefined, digits = 2): string {
  if (n === null || n === undefined || !Number.isFinite(n)) return '';
  return String(Math.round(n * 10 ** digits) / 10 ** digits);
}

export interface Sent {
  path: string;
  headers: Record<string, string>;
  body: string;
  status: number;
  trace: string;
}

function headers(key: string, json = true): Record<string, string> {
  const h: Record<string, string> = {};
  if (json) h['Content-Type'] = 'application/json';
  if (key) h.Authorization = 'Bearer ' + key;
  return h;
}

async function refuse(resp: Response): Promise<never> {
  throw new Error(`${resp.status}: ${errorMessage(await resp.text())}`);
}

export async function capabilities(base: string, model: string, key: string, signal: AbortSignal): Promise<Capabilities> {
  const resp = await fetch(base + '/sdcpp/v1/capabilities', { headers: { ...headers(key, false), 'X-Nebu-Model': model }, signal, credentials: gatewayCredentials() });
  if (!resp.ok) await refuse(resp);
  return (await resp.json()) as Capabilities;
}

export async function generateImages(base: string, req: MediaRequest, key: string, signal: AbortSignal, onSent?: (s: Sent) => void): Promise<{ images: string[]; format: string }> {
  const body = JSON.stringify(req);
  const h = headers(key);
  const resp = await fetch(base + '/v1/images/generations', { method: 'POST', headers: h, body, signal, credentials: gatewayCredentials() });
  onSent?.({ path: '/v1/images/generations', headers: h, body, status: resp.status, trace: resp.headers.get('X-Nebu-Trace') ?? '' });
  if (!resp.ok) await refuse(resp);
  const parsed = (await resp.json()) as { output_format: string; data: { b64_json: string }[] };
  return { images: parsed.data.map((d) => d.b64_json), format: parsed.output_format || 'png' };
}

export async function createVideo(base: string, req: MediaRequest, key: string, signal: AbortSignal, onSent?: (s: Sent) => void): Promise<VideoObject> {
  const body = JSON.stringify(req);
  const h = headers(key);
  const resp = await fetch(base + '/v1/videos', { method: 'POST', headers: h, body, signal, credentials: gatewayCredentials() });
  onSent?.({ path: '/v1/videos', headers: h, body, status: resp.status, trace: resp.headers.get('X-Nebu-Trace') ?? '' });
  if (!resp.ok) await refuse(resp);
  return (await resp.json()) as VideoObject;
}

export async function getVideo(base: string, id: string, key: string, signal?: AbortSignal): Promise<VideoObject> {
  const resp = await fetch(`${base}/v1/videos/${id}`, { headers: headers(key, false), signal, credentials: gatewayCredentials() });
  if (!resp.ok) await refuse(resp);
  return (await resp.json()) as VideoObject;
}

export async function fetchVideo(base: string, id: string, key: string, signal?: AbortSignal): Promise<Blob> {
  const resp = await fetch(`${base}/v1/videos/${id}/content`, { headers: headers(key, false), signal, credentials: gatewayCredentials() });
  if (!resp.ok) await refuse(resp);
  return resp.blob();
}

export async function deleteVideo(base: string, id: string, key: string): Promise<void> {
  const resp = await fetch(`${base}/v1/videos/${id}`, { method: 'DELETE', headers: headers(key, false), credentials: gatewayCredentials() });
  if (!resp.ok && resp.status !== 404) await refuse(resp);
}

export interface Generation {
  id: string;
  kind: 'image' | 'video';
  model: string;
  createdAt: number;
  elapsedMs?: number;
  request: MediaRequest;
  files: Attachment[];
  // Input image and optional final video frame.
  inputs?: Attachment[];
  // Model defaults captured when the request was sent.
  applied?: { steps?: number; cfg?: number; sampler?: string; scheduler?: string };
  format: string;
  mime: string;
  video?: { id: string; status: VideoObject['status']; frames?: number; fps?: number };
  trace?: string;
  error?: string;
}

const historyKey = 'nebu.generate.history';
export const historyLimit = 200;

export function readHistory(): Generation[] {
  try {
    const list = JSON.parse(readLocal(historyKey) || '[]') as Generation[];
    return Array.isArray(list) ? list : [];
  } catch {
    return [];
  }
}

// Delete files when their generations leave the history.
export function writeHistory(list: Generation[]) {
  const kept = list.slice(0, historyLimit);
  const dropped = list.slice(historyLimit).flatMap((g) => [...g.files, ...(g.inputs ?? [])].map((f) => f.id));
  if (dropped.length) deleteImages(dropped).catch(() => {});
  writeLocal(historyKey, JSON.stringify(kept));
}

export function historyFileIds(): string[] {
  return readHistory().flatMap((g) => [...g.files, ...(g.inputs ?? [])].map((f) => f.id));
}
