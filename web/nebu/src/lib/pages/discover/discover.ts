import type { Timestamp } from '@bufbuild/protobuf/wkt';
import { ago, bytes, date, enumLabel, num, when } from '#lib/format.js';
import { ArtifactRole, ModelKind, ModelKindSchema, type Model } from '#lib/proto/nebu/v1/model_pb.js';
import { ColumnType, FacetInput, type Facet, type HitColumn, type SearchHit } from '#lib/proto/nebu/v1/source_pb.js';
import type { Fact } from '#lib/ui/ListRow.svelte';

export function repoPath(sourceId: string, repo: string): string {
	return `/discover/${encodeURIComponent(sourceId)}/${encodeURIComponent(repo)}`;
}

const short = new Intl.NumberFormat('en', { notation: 'compact', maximumFractionDigits: 1 });

// A large count as 1.2M or 48K
export function compact(n: bigint | number): string {
	return short.format(Number(n));
}

// Whether a column's values read as words, which line up at the start and cut off when long
export function wordy(c: HitColumn): boolean {
	return c.type === ColumnType.TEXT || c.type === ColumnType.LIST || c.type === ColumnType.FLAG;
}

// What a hit holds under a column: its own field of that name, else what the source set beside them
function held(h: SearchHit, id: string): unknown {
	const field = id.replace(/_(\w)/g, (_, c: string) => c.toUpperCase());
	return field in h ? (h as unknown as Record<string, unknown>)[field] : h.extra[id];
}

// The words of a list, each once
function listed(v: unknown): string {
	const seen = new Set<string>();
	return (Array.isArray(v) ? v.map(String) : String(v ?? '').split(','))
		.map((w) => w.trim())
		.filter((w) => w && !seen.has(w.toLowerCase()) && seen.add(w.toLowerCase()))
		.join(', ');
}

// The longest value a column of words shows whole
const whole = 22;

// A hit's value under a column as its row shows it, a dash where the source states nothing.
// Runtimes go by name, keyed by the bit each holds in a hit's mask.
export function cell(h: SearchHit, c: HitColumn, runtimes: Map<number, string>): Fact {
	const v = held(h, c.id);
	let value = '';
	let tip: string | undefined;
	switch (c.type) {
		case ColumnType.COUNT:
		case ColumnType.BYTES: {
			const n = Number(v ?? 0);
			if (n > 0) [value, tip] = c.type === ColumnType.BYTES ? [bytes(n), undefined] : [compact(n), num(n)];
			break;
		}
		case ColumnType.TIME: {
			const d = typeof v === 'string' ? new Date(v) : date(v as Timestamp | undefined);
			if (d && !Number.isNaN(d.getTime())) [value, tip] = [ago(d), when(d)];
			break;
		}
		case ColumnType.FLAG:
			value = v === true || v === 'true' ? 'Yes' : '';
			break;
		case ColumnType.LIST:
			value = c.id === 'runtimes' ? [...runtimes].filter(([bit]) => (Number(v) & bit) !== 0).map(([, name]) => name).join(', ') : listed(v);
			break;
		default:
			value = c.id === 'kind' ? (v === ModelKind.UNSPECIFIED ? '' : enumLabel(ModelKindSchema, Number(v))) : String(v ?? '');
	}
	if (!wordy(c)) return { value: value || blank, tip };
	return { value: value || blank, tip: value.length > whole ? value : undefined, clip: true };
}

const blank = '\u2014';

// The values most a set filter names in full, the rest counted after them
const named = 2;

// What a facet is set to as its filter chip words it: the names of a choice's values, the ends of a range,
// typed words as typed, and nothing for a toggle, whose name says it all
export function facetText(f: Facet, values: string[]): string {
	if (values.length === 0 || f.input === FacetInput.TOGGLE) return '';
	if (f.input === FacetInput.TEXT) return values.join(', ');
	if (f.input === FacetInput.RANGE) {
		const range = values[0] ?? '';
		const [low = '', high = ''] = range.split('..');
		return f.values.find((v) => v.id === range)?.label ?? (low && high ? `${low} to ${high}` : low ? `${low} or more` : high ? `${high} or less` : range);
	}
	const names = values.map((v) => f.values.find((x) => x.id === v)?.label || v);
	return names.length > named ? `${names.slice(0, named).join(', ')} +${names.length - named}` : names.join(', ');
}

// The last search the Discover page showed, so a repository page links back to it
export const lastSearch = { href: '/discover' };

export type WeightGroup = { name: string; formatId: string; bytes: number };

// Weight groups of a resolved repository in the daemon's order, format then name
export function weightGroups(model: Model | undefined): WeightGroup[] {
	const byName = new Map<string, WeightGroup>();
	for (const a of model?.artifacts ?? []) {
		if (a.role !== ArtifactRole.WEIGHTS) continue;
		const g = byName.get(a.group) ?? { name: a.group, formatId: a.formatId, bytes: 0 };
		g.bytes += Number(a.sizeBytes);
		byName.set(a.group, g);
	}
	return [...byName.values()].sort((x, y) => x.formatId.localeCompare(y.formatId) || x.name.localeCompare(y.name));
}

