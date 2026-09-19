import type { ConfigField, Facet, SearchHit, SourceCapabilities, SourceStatus } from '$proto/source_pb';
import { ConfigType, SourceKind } from '$proto/source_pb';
import type { FitRow, MemoryPlan } from '$proto/estimate_pb';
import { FitVerdict } from '$proto/estimate_pb';
import type { Descriptor, Precision } from '$proto/model_pb';
import { count, ctx } from './format';

function providerName(s: SourceStatus | undefined): string {
  return s?.capabilities?.name || 'Source';
}

export function sourceLabel(s: SourceStatus | undefined): string {
  return s?.source?.name || s?.source?.id || '';
}

// Append source IDs to duplicate names.
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

export interface ProviderGroup {
  kind: SourceKind;
  name: string;
  sources: SourceStatus[];
}

// Preserve provider order from the daemon.
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

// Use the source name for a single source, otherwise the provider name.
export function groupLabel(g: ProviderGroup): string {
  return g.sources.length === 1 ? sourceLabel(g.sources[0]) : g.name;
}

export function kindParam(kind: SourceKind): string {
  return (SourceKind[kind] ?? '').toLowerCase();
}

export function parseKind(param: string): SourceKind {
  const v = SourceKind[param.toUpperCase() as keyof typeof SourceKind];
  return typeof v === 'number' ? v : SourceKind.UNSPECIFIED;
}

export function facetValueLabel(caps: SourceCapabilities | undefined, facetId: string, value: string): string {
  const f = caps?.facets.find((x) => x.id === facetId);
  return f?.values.find((v) => v.id === value)?.label ?? humanize(value);
}

// Turn text-generation into Text generation.
export function humanize(id: string): string {
  const s = id.replace(/[-_]+/g, ' ').trim();
  return s ? s.charAt(0).toUpperCase() + s.slice(1) : s;
}

export function splitValues(v: string | undefined): string[] {
  return (v ?? '')
    .split(',')
    .map((s) => s.trim())
    .filter(Boolean);
}

export function joinValues(vs: string[]): string {
  return vs.join(',');
}

// Preserve facet order within each group.
export function groupValues(f: Facet): { group: string; values: Facet['values'] }[] {
  const out: { group: string; values: Facet['values'] }[] = [];
  for (const v of f.values) {
    let g = out.find((x) => x.group === v.group);
    if (!g) out.push((g = { group: v.group, values: [] }));
    g.values.push(v);
  }
  return out;
}

export function looksLikeRepo(caps: SourceCapabilities | undefined, text: string): boolean {
  const t = text.trim();
  if (!t || !caps?.repoPattern) return false;
  try {
    return new RegExp(caps.repoPattern).test(t);
  } catch {
    return false;
  }
}

// Prefer the requested kind, then the source's provider, then the first provider.
export function pickGroup(groups: ProviderGroup[], kind: SourceKind, sourceId: string): ProviderGroup | undefined {
  return groups.find((g) => g.kind === kind) ?? groups.find((g) => g.sources.some((s) => s.source?.id === sourceId)) ?? groups[0];
}

export function sharedFacet(id: string): boolean {
  return id === 'runtime' || id === 'format';
}

// Most catalogs support descending order only.
export function sortReversible(caps: SourceCapabilities | undefined, sortId: string): boolean {
  return !!caps?.sorts.find((s) => s.id === sortId)?.reversible;
}

// Prefer parameter count, then byte size, then published size labels.
export function hitSize(h: SearchHit): { kind: 'params' | 'bytes' | 'sizes' | 'none'; value: bigint; text: string } {
  if (h.parameters > 0n) return { kind: 'params', value: h.parameters, text: '' };
  if (h.sizeBytes > 0n) return { kind: 'bytes', value: h.sizeBytes, text: '' };
  const sizes = splitValues(h.extra['sizes']);
  if (sizes.length) return { kind: 'sizes', value: 0n, text: sizes.length > 1 ? `${sizes[0]}–${sizes[sizes.length - 1]}` : sizes[0] };
  return { kind: 'none', value: 0n, text: '' };
}

// Group names can repeat across formats.
export function descriptorKey(d: Descriptor): string {
  return `${d.formatId}\0${d.group}`;
}

// Sort by descending precision, then size.
export function orderDescriptors(descriptors: Descriptor[]): Descriptor[] {
  return [...descriptors].sort((a, b) => b.bitsPerWeight - a.bitsPerWeight || Number(b.totalBytes - a.totalBytes) || a.group.localeCompare(b.group));
}

// Recommend the largest group that fits at this context length.
export function recommended(descriptors: Descriptor[], rows: FitRow[], context: number): string {
  const fitting = descriptors.filter((d) => cellPlan(rowAt(rows, d.group, context))?.verdict === FitVerdict.FITS);
  return fitting.sort((a, b) => Number(b.totalBytes - a.totalBytes))[0]?.group ?? '';
}

// Prefer the plan for free memory, matching launch behavior.
export function cellPlan(row: FitRow | undefined): MemoryPlan | undefined {
  return row?.free ?? row?.plan;
}

// Rows are already filtered by runtime. Context zero uses the planner's choice.
export function rowAt(rows: FitRow[], group: string, context: number): FitRow | undefined {
  return rows.find((r) => r.group === group && r.context === context);
}

export function plannedContext(row: FitRow | undefined): number {
  const n = Number(cellPlan(row)?.params['n_ctx'] ?? 0);
  return Number.isFinite(n) ? n : 0;
}

// Omit the quantization name already shown in the group label.
export function precisionShort(p: Precision | undefined): string {
  if (!p) return '';
  return p.label.match(/^\d+-bit(?: float)?/)?.[0] ?? p.label;
}

export function weightsName(group: string, formatId = ''): string {
  return group === 'default' ? formatId || 'weights' : group;
}

export function locked(h: SearchHit | null, caps: SourceCapabilities | undefined): boolean {
  return !!h?.gated && !caps?.tokenPresent;
}

// Deduplicate chip labels across fields. Split comma-separated values into chips.
export function hitChips(h: SearchHit, caps: SourceCapabilities | undefined): string[] {
  const out: string[] = [];
  const add = (text: string) => {
    if (text && !out.includes(text)) out.push(text);
  };
  for (const f of caps?.hitFields ?? []) {
    const v = h.extra[f.name];
    if (v === undefined || v === '') continue;
    if (f.type === ConfigType.BOOL) {
      if (v === 'true') add(f.label || f.name);
      continue;
    }
    for (const part of splitValues(v)) add(chipText(f, part));
  }
  return out;
}

// Include the field name with numeric values, such as 256k context.
function chipText(f: ConfigField, v: string): string {
  if (!/^\d+$/.test(v)) return v;
  const n = Number(v);
  const word = (f.label || f.name).toLowerCase();
  return f.name === 'context' ? `${ctx(n)} ${word}` : `${count(n)} ${word}`;
}
