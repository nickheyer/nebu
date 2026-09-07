import type { ConfigField, Facet, SearchHit, SourceCapabilities, SourceStatus } from '$proto/source_pb';
import { ConfigType, SourceKind } from '$proto/source_pb';
import type { FitRow } from '$proto/estimate_pb';
import { FitVerdict } from '$proto/estimate_pb';
import type { Descriptor, Precision } from '$proto/model_pb';
import { count, ctx } from './format';

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

// A provider's label: the one source's own name, or the provider's when it has several
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

// The label a source gave a facet value, falling back to the id
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

// Whether typed text looks like a repository for this source
export function looksLikeRepo(caps: SourceCapabilities | undefined, text: string): boolean {
  const t = text.trim();
  if (!t || !caps?.repoPattern) return false;
  try {
    return new RegExp(caps.repoPattern).test(t);
  } catch {
    return false;
  }
}

// The provider group of a kind, else the one holding the source, else the first
export function pickGroup(groups: ProviderGroup[], kind: SourceKind, sourceId: string): ProviderGroup | undefined {
  return groups.find((g) => g.kind === kind) ?? groups.find((g) => g.sources.some((s) => s.source?.id === sourceId)) ?? groups[0];
}

// Whether the source can flip a sort, most catalogs only order descending
export function sortReversible(caps: SourceCapabilities | undefined, sortId: string): boolean {
  return !!caps?.sorts.find((s) => s.id === sortId)?.reversible;
}

// The most useful size to show for a hit
export function hitSize(h: SearchHit): { kind: 'params' | 'bytes' | 'none'; value: bigint } {
  if (h.parameters > 0n) return { kind: 'params', value: h.parameters };
  if (h.sizeBytes > 0n) return { kind: 'bytes', value: h.sizeBytes };
  return { kind: 'none', value: 0n };
}

// A key that tells two weight groups apart even when their names collide across formats
export function descriptorKey(d: Descriptor): string {
  return `${d.formatId}\0${d.group}`;
}

// Orders weight groups for choosing: what fits first, then the largest, which keeps the most quality
//
// Read at one context length when given, else at the best each group reaches.
export function orderDescriptors(descriptors: Descriptor[], rows: FitRow[], context = 0): Descriptor[] {
  const score = (v: FitVerdict | undefined) => (v === undefined ? -1 : v === FitVerdict.FITS ? 2 : v === FitVerdict.PARTIAL ? 1 : 0);
  const rank = (d: Descriptor) => {
    if (context) return score(rowAt(rows, d.group, context)?.plan?.verdict);
    return Math.max(-1, ...rows.filter((r) => r.group === d.group && r.plan).map((r) => score(r.plan!.verdict)));
  };
  return [...descriptors].sort((a, b) => rank(b) - rank(a) || Number(b.totalBytes - a.totalBytes));
}

// The row planning one group at one context length, on the one runtime the rows were filtered to
export function rowAt(rows: FitRow[], group: string, context: number): FitRow | undefined {
  return rows.find((r) => r.group === group && r.context === context);
}

// The width alone, 8-bit out of 8-bit Q8_0, since the group name already says the rest
export function precisionShort(p: Precision | undefined): string {
  if (!p) return '';
  return p.label.match(/^\d+-bit(?: float)?/)?.[0] ?? p.label;
}

// What a weight group is called: its name, or its format when the spec gave it none worth reading
export function weightsName(group: string, formatId = ''): string {
  return group === 'default' ? formatId || 'weights' : group;
}

// Whether a hit is gated on a source that holds no token, so opening it would only fail
export function locked(h: SearchHit | null, caps: SourceCapabilities | undefined): boolean {
  return !!h?.gated && !caps?.tokenPresent;
}

// The chips a hit carries beyond its columns, as the source declares them: a flag shows its label, a comma list one chip each
export function hitChips(h: SearchHit, caps: SourceCapabilities | undefined): string[] {
  const out: string[] = [];
  for (const f of caps?.hitFields ?? []) {
    const v = h.extra[f.name];
    if (v === undefined || v === '') continue;
    if (f.type === ConfigType.BOOL) {
      if (v === 'true') out.push(f.label || f.name);
      continue;
    }
    for (const part of splitValues(v)) out.push(chipText(f, part));
  }
  return out;
}

// A bare number needs its field to read, 262144 under context becoming 256k context
function chipText(f: ConfigField, v: string): string {
  if (!/^\d+$/.test(v)) return v;
  const n = Number(v);
  const word = (f.label || f.name).toLowerCase();
  return f.name === 'context' ? `${ctx(n)} ${word}` : `${count(n)} ${word}`;
}
