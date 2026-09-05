import type { Facet, SearchHit, SourceCapabilities, SourceStatus } from '$proto/source_pb';
import { ConfigType, SourceKind } from '$proto/source_pb';
import type { RuntimeStatus } from '$proto/runtime_pb';
import type { FitRow } from '$proto/estimate_pb';
import { FitVerdict } from '$proto/estimate_pb';
import type { Descriptor } from '$proto/model_pb';
import type { Tone } from './format';
import { ctx, verdictWord } from './format';

// What the daemon calls the provider behind a source
function providerName(s: SourceStatus | undefined): string {
  return s?.capabilities?.name || 'Source';
}

// What a source is called, its id when it was never named
export function sourceLabel(s: SourceStatus | undefined): string {
  return s?.source?.name || s?.source?.id || '';
}

// Names every source by id, adding the id where two share a name
export function sourceLabels(statuses: SourceStatus[]): Map<string, string> {
  const counts = new Map<string, number>();
  for (const s of statuses) counts.set(sourceLabel(s), (counts.get(sourceLabel(s)) ?? 0) + 1);
  const out = new Map<string, string>();
  for (const s of statuses) {
    const id = s.source?.id ?? '';
    const name = sourceLabel(s);
    out.set(id, (counts.get(name) ?? 0) > 1 ? `${name} · ${id}` : name);
  }
  return out;
}

// One provider with every source configured for it, in the daemon's order
export interface ProviderGroup {
  kind: SourceKind;
  name: string;
  sources: SourceStatus[];
}

// Groups sources by provider, keeping the daemon's order of first appearance
export function groupByProvider(statuses: SourceStatus[]): ProviderGroup[] {
  const out: ProviderGroup[] = [];
  for (const s of statuses) {
    const kind = s.source?.kind ?? SourceKind.UNSPECIFIED;
    let g = out.find((x) => x.kind === kind);
    if (!g) out.push((g = { kind, name: providerName(s), sources: [] }));
    g.sources.push(s);
  }
  return out;
}

// A provider tab's label: the one source's own name, or the provider's when it has several
export function groupLabel(g: ProviderGroup): string {
  return g.sources.length === 1 ? sourceLabel(g.sources[0]) : g.name;
}

// The short name a kind travels under in a URL, such as huggingface or oci
export function kindParam(kind: SourceKind): string {
  return (SourceKind[kind] ?? '').toLowerCase();
}

// Reads a kind back from its short name, unspecified when unknown
export function parseKind(param: string): SourceKind {
  const v = SourceKind[param.toUpperCase() as keyof typeof SourceKind];
  return typeof v === 'number' ? v : SourceKind.UNSPECIFIED;
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

// Picks the provider group of a kind, else the one holding the source, else the first
export function pickGroup(groups: ProviderGroup[], kind: SourceKind, sourceId: string): ProviderGroup | undefined {
  return groups.find((g) => g.kind === kind) ?? groups.find((g) => g.sources.some((s) => s.source?.id === sourceId)) ?? groups[0];
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

const hiddenPatterns = new WeakMap<SourceCapabilities, RegExp[]>();

// The source's housekeeping tag patterns, compiled once per capabilities object and matched whole
function hiddenTags(caps: SourceCapabilities | undefined): RegExp[] {
  if (!caps) return [];
  let res = hiddenPatterns.get(caps);
  if (!res) hiddenPatterns.set(caps, (res = caps.hiddenTags.map((p) => new RegExp(`^(?:${p})$`))));
  return res;
}

// Tags worth showing on a card: what the card already says and the source's housekeeping hidden
export function displayTags(h: SearchHit, caps: SourceCapabilities | undefined, max = 5): string[] {
  const skip = new Set([h.task, h.library, h.license, ...h.formats].filter(Boolean));
  const hidden = hiddenTags(caps);
  const seen = new Set<string>();
  return h.tags.filter((t) => !skip.has(t) && !hidden.some((re) => re.test(t)) && !seen.has(t) && seen.add(t)).slice(0, max);
}

// The chips a source declares for a hit: text, tooltip, a bool showing its label, a comma list one chip each
export function hitChips(h: SearchHit, caps: SourceCapabilities | undefined): { text: string; title: string }[] {
  const out: { text: string; title: string }[] = [];
  for (const f of caps?.hitFields ?? []) {
    const value = h.extra[f.name];
    if (!value) continue;
    const title = f.description || f.label;
    if (f.type === ConfigType.BOOL) {
      if (value === 'true') out.push({ text: f.label, title });
      continue;
    }
    for (const part of new Set(value.split(',').map((p) => p.trim()).filter(Boolean))) out.push({ text: part, title });
  }
  return out;
}

// Orders weight groups for choosing: what fits first, then the largest, which keeps the most quality
export function orderDescriptors(descriptors: Descriptor[], rows: FitRow[]): Descriptor[] {
  const rank = (d: Descriptor) => {
    const f = fitSummary(rows, d.group);
    return f ? (f.verdict === FitVerdict.FITS ? 2 : f.verdict === FitVerdict.PARTIAL ? 1 : 0) : -1;
  };
  return [...descriptors].sort((a, b) => rank(b) - rank(a) || Number(b.totalBytes - a.totalBytes));
}

// The tone of a precision level, 0 unknown through 5 full
export function precisionTone(level: number): Tone {
  return (['neutral', 'bad', 'warn', 'accent', 'ok', 'ok'] as Tone[])[level] ?? 'neutral';
}

// Runtimes able to serve any of the formats, the ones this host can run first
export function runtimesFor(formats: string[], runtimes: RuntimeStatus[]): { id: string; name: string; compatible: boolean }[] {
  const out: { id: string; name: string; compatible: boolean }[] = [];
  for (const r of runtimes) {
    const m = r.manifest;
    if (m && m.formats.some((f) => formats.includes(f))) out.push({ id: m.id, name: m.name || m.id, compatible: r.compatible });
  }
  return out.sort((a, b) => Number(b.compatible) - Number(a.compatible));
}

// The one line answer to whether a weight group fits this host
interface FitSummary {
  verdict: FitVerdict;
  // The longest context that earns the verdict
  context: number;
  runtime: string;
  label: string;
  tone: Tone;
}

// A group's best verdict at its longest context, against all memory or free memory
export function fitSummary(rows: FitRow[], group: string, free = false): FitSummary | null {
  const rank = (v: FitVerdict) => (v === FitVerdict.FITS ? 2 : v === FitVerdict.PARTIAL ? 1 : 0);
  const planOf = (r: FitRow) => (free ? r.free : r.plan);
  let best: FitRow | null = null;
  for (const r of rows) {
    const p = planOf(r);
    if (r.group !== group || !p) continue;
    if (!best || rank(p.verdict) > rank(planOf(best)!.verdict) || (rank(p.verdict) === rank(planOf(best)!.verdict) && r.context > best.context)) best = r;
  }
  const plan = best ? planOf(best) : undefined;
  if (!best || !plan) return null;
  const v = plan.verdict;
  const word = verdictWord(v);
  const now = free ? ' right now' : '';
  const base = { verdict: v, context: best.context, runtime: best.runtimeId };
  if (v === FitVerdict.FITS) return { ...base, label: `${word} up to ${ctx(best.context)} context${now}`, tone: 'ok' };
  if (v === FitVerdict.PARTIAL) return { ...base, label: `${word}${now}, spills into system RAM`, tone: 'warn' };
  return { ...base, label: `${word} for ${free ? 'what is free right now' : 'this host'}`, tone: 'bad' };
}
