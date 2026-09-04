import type { Facet, SearchHit, SourceCapabilities, SourceStatus } from '$proto/source_pb';
import { SourceKind } from '$proto/source_pb';
import type { RuntimeStatus } from '$proto/runtime_pb';
import type { FitRow } from '$proto/estimate_pb';
import { FitVerdict } from '$proto/estimate_pb';
import type { Descriptor } from '$proto/model_pb';
import type { Tone } from './format';
import { ctx } from './format';

// Short labels for source kinds
export function kindLabel(kind: SourceKind | undefined): string {
  switch (kind) {
    case SourceKind.HUGGINGFACE:
      return 'Hugging Face';
    case SourceKind.MODELSCOPE:
      return 'ModelScope';
    case SourceKind.OLLAMA:
      return 'Ollama';
    case SourceKind.CIVITAI:
      return 'Civitai';
    case SourceKind.OCI:
      return 'OCI registry';
    case SourceKind.KAGGLE:
      return 'Kaggle';
    case SourceKind.NGC:
      return 'NVIDIA NGC';
    case SourceKind.CSGHUB:
      return 'CSGHub';
    case SourceKind.MIRROR:
      return 'Mirror';
    case SourceKind.LOCAL:
      return 'Local';
    default:
      return 'Source';
  }
}

// The name a source goes by, its kind refined by where it points
export function sourceName(s: SourceStatus): string {
  const kind = s.source?.kind;
  const where = (s.capabilities?.webUrl || s.source?.endpoint || '').toLowerCase();
  if (kind === SourceKind.OCI && where.includes('docker')) return 'Docker Hub';
  if (kind === SourceKind.CSGHUB && where.includes('opencsg')) return 'OpenCSG';
  return kindLabel(kind);
}

// Names every source by id, adding the id where two share a name
export function sourceLabels(statuses: SourceStatus[]): Map<string, string> {
  const counts = new Map<string, number>();
  for (const s of statuses) counts.set(sourceName(s), (counts.get(sourceName(s)) ?? 0) + 1);
  const out = new Map<string, string>();
  for (const s of statuses) {
    const id = s.source?.id ?? '';
    const name = sourceName(s);
    out.set(id, (counts.get(name) ?? 0) > 1 ? `${name} · ${id}` : name);
  }
  return out;
}

// Finds the label a source gave a facet value, falling back to the id
export function facetValueLabel(caps: SourceCapabilities | undefined, facetId: string, value: string): string {
  const f = caps?.facets.find((x) => x.id === facetId);
  return f?.values.find((v) => v.id === value)?.label ?? humanize(value);
}

// Turns text-generation into Text generation
export function humanize(id: string): string {
  const s = id.replace(/[-_]+/g, ' ').trim();
  return s ? s.charAt(0).toUpperCase() + s.slice(1) : s;
}

// Splits a comma separated multi value
export function splitValues(v: string | undefined): string[] {
  return (v ?? '')
    .split(',')
    .map((s) => s.trim())
    .filter(Boolean);
}

export function joinValues(vs: string[]): string {
  return vs.join(',');
}

// Groups facet values by their group label, preserving order
export function groupValues(f: Facet): { group: string; values: Facet['values'] }[] {
  const out: { group: string; values: Facet['values'] }[] = [];
  for (const v of f.values) {
    let g = out.find((x) => x.group === v.group);
    if (!g) out.push((g = { group: v.group, values: [] }));
    g.values.push(v);
  }
  return out;
}

// Reports whether typed text looks like a repository for this source
export function looksLikeRepo(caps: SourceCapabilities | undefined, text: string): boolean {
  const t = text.trim();
  if (!t || !caps?.repoPattern) return false;
  try {
    return new RegExp(caps.repoPattern).test(t);
  } catch {
    return false;
  }
}

// Picks the source with a given id, else the first
export function pickSource(statuses: SourceStatus[], id: string): SourceStatus | undefined {
  return statuses.find((s) => s.source?.id === id) ?? statuses[0];
}

// Reports whether the source can flip a sort, most catalogs only order descending
export function sortReversible(caps: SourceCapabilities | undefined, sortId: string): boolean {
  return !!caps?.sorts.find((s) => s.id === sortId)?.reversible;
}

// The most useful size to show for a hit
export function hitSize(h: SearchHit): { kind: 'params' | 'bytes' | 'none'; value: bigint } {
  if (h.parameters > 0n) return { kind: 'params', value: h.parameters };
  if (h.sizeBytes > 0n) return { kind: 'bytes', value: h.sizeBytes };
  return { kind: 'none', value: 0n };
}

// Hub housekeeping tags that say nothing about the model
const noiseTags = new Set(['endpoints_compatible', 'eval-results', 'autotrain_compatible', 'text-generation-inference', 'custom_code', 'model-index', 'has_space', 'safetensors', 'gguf', 'pytorch', 'jax', 'tf']);

// Tags worth showing on a card, namespaced, housekeeping, and language codes hidden
export function displayTags(h: SearchHit, max = 5): string[] {
  const skip = new Set([h.task, h.library, h.license, ...h.formats].filter(Boolean));
  const seen = new Set<string>();
  return h.tags.filter((t) => !t.includes(':') && !skip.has(t) && !noiseTags.has(t) && !/^[a-z]{2,3}$/.test(t) && !seen.has(t) && seen.add(t)).slice(0, max);
}

// Orders weight groups for choosing: what fits first, then the largest, which keeps the most quality
export function orderDescriptors(descriptors: Descriptor[], rows: FitRow[]): Descriptor[] {
  const rank = (d: Descriptor) => {
    const f = fitSummary(rows, d.group);
    return f ? (f.verdict === FitVerdict.FITS ? 2 : f.verdict === FitVerdict.PARTIAL ? 1 : 0) : -1;
  };
  return [...descriptors].sort((a, b) => rank(b) - rank(a) || Number(b.totalBytes - a.totalBytes));
}

// What each weight format is, for people who have not met them
export const formatBlurb: Record<string, string> = {
  gguf: 'GGUF packs the whole model into one file per precision, usually quantized to fit in less memory',
  safetensors: 'Safetensors shards hold the original checkpoint next to its config, usually at full 16-bit precision',
  nemo: 'A NeMo checkpoint packed as one .nemo archive, the older NVIDIA layout',
  nemo2: 'A NeMo 2 checkpoint directory, a model config beside a distributed checkpoint, the NVIDIA layout NeMo serves'
};

// Runtimes able to serve any of the formats, the ones this host can run first
export function runtimesFor(formats: string[], runtimes: RuntimeStatus[]): { id: string; name: string; compatible: boolean }[] {
  const out: { id: string; name: string; compatible: boolean }[] = [];
  for (const r of runtimes) {
    const m = r.manifest;
    if (m && m.formats.some((f) => formats.includes(f))) out.push({ id: m.id, name: m.name || m.id, compatible: r.compatible });
  }
  return out.sort((a, b) => Number(b.compatible) - Number(a.compatible));
}

// Plain words for how a weight group is stored
export interface Precision {
  // Short name such as 4-bit or 16-bit bfloat16
  label: string;
  // What choosing it means for size and quality
  blurb: string;
  // Quality retained, 0 unknown through 5 full
  level: number;
  tone: Tone;
}

const quantRe = /(?:^|[-_./])((?:UD-)?(?:I?Q\d(?:_[0-9A-Z]+)*|BF16|F16|F32|MXFP4|TQ\d_\d))(?:[-_./]|$)/i;

const bitsByLevel: [number, number, string, string][] = [
  [32, 5, '32-bit float', 'The original weights at full size. Largest of all, nothing lost.'],
  [16, 5, '16-bit', 'Full quality, the precision the model was trained at. Needs the most memory.'],
  [8, 4, '8-bit', 'Near lossless. Half the size of 16-bit with quality most people cannot tell apart.'],
  [6, 4, '6-bit', 'Very close to full quality at about a third of the 16-bit size.'],
  [5, 3, '5-bit', 'Small quality loss, slightly smaller than 6-bit.'],
  [4, 3, '4-bit', 'The usual choice. About a quarter of the 16-bit size with modest quality loss.'],
  [3, 2, '3-bit', 'Noticeably lower quality. For when 4-bit does not fit.'],
  [2, 1, '2-bit', 'Heavy quality loss. A last resort for tight memory.'],
  [1, 1, '1-bit', 'Extreme compression. Expect degraded answers.']
];

// Reads the bit width a quant name encodes, 0 when it names none
function quantBits(quant: string): number {
  const m = quant.match(/^(?:UD-)?(?:I?Q|TQ)(\d)/);
  if (m) return Number(m[1]);
  if (quant === 'F32') return 32;
  if (quant === 'F16' || quant === 'BF16') return 16;
  if (quant === 'MXFP4') return 4;
  return 0;
}

// Adds the flavour a quant name carries beyond its bit width
function quantNote(quant: string): string {
  const notes: string[] = [];
  if (quant.startsWith('UD-')) notes.push('Unsloth dynamic, key layers kept at higher precision');
  if (/^IQ/.test(quant)) notes.push('importance-matrix quant, better quality than plain quants at the same bits');
  if (/_K_XL$/.test(quant)) notes.push('extra large K-quant, the most careful of its bit width');
  else if (/_K_L$/.test(quant)) notes.push('large K-quant, more of the sensitive layers kept precise');
  else if (/_K_M$/.test(quant)) notes.push('medium K-quant, the recommended balance');
  else if (/_K_S$/.test(quant)) notes.push('small K-quant, a bit smaller and a bit rougher');
  else if (/^Q\d_[01]$/.test(quant)) notes.push('older quant scheme, a K-quant at the same bits is usually better');
  if (quant === 'BF16') notes.push('bfloat16, the training format on modern GPUs');
  if (quant === 'F16') notes.push('float16, the training format on older GPUs');
  return notes.join('; ');
}

// Describes a weight group's precision from its name, headers, and measured bits per weight
export function precision(d: Descriptor): Precision {
  const quant = (d.group.match(quantRe)?.[1] ?? '').toUpperCase();
  const meta = d.metadata;
  const method = (meta['quantization_config.quant_method'] ?? '').toUpperCase();
  const dtype = meta['torch_dtype'] || meta['dtype'] || '';
  let bits = Number(meta['quantization_config.bits'] ?? 0) || quantBits(quant);
  if (!bits && d.bitsPerWeight > 0) bits = d.bitsPerWeight >= 12 ? Math.round(d.bitsPerWeight) : Math.floor(d.bitsPerWeight);
  const row = bitsByLevel.find(([b]) => bits >= b);
  if (!row) return { label: 'Unknown precision', blurb: 'The headers did not say how the weights are stored.', level: 0, tone: 'neutral' };
  let label = row[2];
  if (method) label += ' ' + method;
  else if (quant) label += ' ' + quant;
  else if (dtype) label += ' ' + dtype;
  const note = method ? `${method} keeps quality close to the original at this bit width` : quantNote(quant);
  const blurb = note ? `${row[3]} ${note.charAt(0).toUpperCase() + note.slice(1)}.` : row[3];
  const tones: Tone[] = ['neutral', 'bad', 'warn', 'accent', 'ok', 'ok'];
  return { label, blurb, level: row[1], tone: tones[row[1]] };
}

// The one line answer to whether a weight group fits this host
export interface FitSummary {
  verdict: FitVerdict;
  // The longest context that earns the verdict
  context: number;
  runtime: string;
  label: string;
  tone: Tone;
}

// Summarises the fit rows of one group, the best verdict at the longest context
export function fitSummary(rows: FitRow[], group: string): FitSummary | null {
  const rank = (v: FitVerdict) => (v === FitVerdict.FITS ? 2 : v === FitVerdict.PARTIAL ? 1 : 0);
  let best: FitRow | null = null;
  for (const r of rows) {
    if (r.group !== group || !r.plan) continue;
    if (!best || rank(r.plan.verdict) > rank(best.plan!.verdict) || (rank(r.plan.verdict) === rank(best.plan!.verdict) && r.context > best.context)) best = r;
  }
  if (!best?.plan) return null;
  const v = best.plan.verdict;
  if (v === FitVerdict.FITS) return { verdict: v, context: best.context, runtime: best.runtimeId, label: `Fits up to ${ctx(best.context)} context`, tone: 'ok' };
  if (v === FitVerdict.PARTIAL) return { verdict: v, context: best.context, runtime: best.runtimeId, label: 'Partly, spills into system RAM', tone: 'warn' };
  return { verdict: v, context: best.context, runtime: best.runtimeId, label: 'Too big for this host', tone: 'bad' };
}
