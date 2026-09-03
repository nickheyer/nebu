import type { Timestamp } from '@bufbuild/protobuf/wkt';
import { timestampDate } from '@bufbuild/protobuf/wkt';
import { FitVerdict } from '$proto/estimate_pb';

export type Tone = 'ok' | 'warn' | 'bad' | 'info' | 'accent' | 'neutral';

const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB', 'PiB'];

// Formats bytes for people
export function bytes(n: bigint | number | undefined | null, digits = 1): string {
  if (n === undefined || n === null) return '–';
  let v = typeof n === 'bigint' ? Number(n) : n;
  if (!v) return '0 B';
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return (i === 0 ? v.toFixed(0) : v.toFixed(digits)) + ' ' + units[i];
}

// Formats a count with thousands separators, compact above a million
export function count(n: bigint | number | undefined): string {
  if (n === undefined) return '–';
  const v = Number(n);
  if (v >= 1_000_000_000) return (v / 1_000_000_000).toFixed(1) + 'B';
  if (v >= 1_000_000) return (v / 1_000_000).toFixed(1) + 'M';
  if (v >= 10_000) return (v / 1_000).toFixed(1) + 'K';
  return v.toLocaleString();
}

// Formats a parameter count like 7.6B
export function params(n: bigint | number | undefined): string {
  if (!n) return '–';
  const v = Number(n);
  if (v >= 1e12) return (v / 1e12).toFixed(1) + 'T';
  if (v >= 1e9) return (v / 1e9).toFixed(1) + 'B';
  if (v >= 1e6) return (v / 1e6).toFixed(0) + 'M';
  return v.toLocaleString();
}

// Percentage of a over b, clamped
export function pct(a: bigint | number | undefined, b: bigint | number | undefined): number {
  const x = Number(a ?? 0);
  const y = Number(b ?? 0);
  if (!y) return 0;
  return Math.max(0, Math.min(100, (x / y) * 100));
}

// Lower cases an enum value name with its prefix removed
export function enumLabel(values: Record<number, string>, v: number | undefined): string {
  const raw = values[v ?? 0] ?? 'UNSPECIFIED';
  return raw.replace(/^[A-Z_]+?_(?=[A-Z]+$|[A-Z]+_)/, '').toLowerCase().replace(/_/g, ' ');
}

// Maps a state name onto a color tone
export function tone(state: string): Tone {
  switch (state.toLowerCase()) {
    case 'ready':
    case 'succeeded':
    case 'ok':
    case 'fits':
      return 'ok';
    case 'starting':
    case 'running':
    case 'pending':
    case 'swapping':
    case 'draining':
    case 'stopping':
    case 'partial':
    case 'warn':
      return 'warn';
    case 'failed':
    case 'fail':
    case 'canceled':
    case 'no':
      return 'bad';
    case 'empty':
    case 'stopped':
    case 'skipped':
      return 'neutral';
    default:
      return 'neutral';
  }
}

export function verdictLabel(v: FitVerdict | undefined): string {
  switch (v) {
    case FitVerdict.FITS:
      return 'fits';
    case FitVerdict.PARTIAL:
      return 'partial';
    case FitVerdict.NO:
      return 'no';
    default:
      return 'unknown';
  }
}

export function verdictTone(v: FitVerdict | undefined): Tone {
  return tone(verdictLabel(v));
}

// Formats a timestamp as local time
export function when(ts?: Timestamp): string {
  if (!ts) return '–';
  return timestampDate(ts).toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' });
}

// Formats a timestamp as a clock time only
export function clock(ts?: Timestamp): string {
  if (!ts) return '–';
  return timestampDate(ts).toLocaleTimeString(undefined, { timeStyle: 'medium' });
}

// Formats a timestamp relative to now, now is passed so callers can stay reactive
export function ago(ts: Timestamp | undefined, now: number = Date.now()): string {
  if (!ts) return '–';
  const s = Math.max(0, (now - timestampDate(ts).getTime()) / 1000);
  if (s < 5) return 'just now';
  if (s < 60) return Math.floor(s) + 's ago';
  if (s < 3600) return Math.floor(s / 60) + 'm ago';
  if (s < 86400) return Math.floor(s / 3600) + 'h ago';
  return Math.floor(s / 86400) + 'd ago';
}

// Formats the span between two timestamps, or since the first until now
export function duration(from?: Timestamp, to?: Timestamp, now: number = Date.now()): string {
  if (!from) return '–';
  const end = to ? timestampDate(to).getTime() : now;
  let s = Math.max(0, Math.round((end - timestampDate(from).getTime()) / 1000));
  if (s < 60) return s + 's';
  const m = Math.floor(s / 60);
  s -= m * 60;
  if (m < 60) return `${m}m ${s}s`;
  const h = Math.floor(m / 60);
  return `${h}h ${m - h * 60}m`;
}

// Shortens an id for display
export function shortId(id: string | undefined, n = 8): string {
  if (!id) return '';
  return id.length > n ? id.slice(0, n) : id;
}

// Splits name=value pairs typed one per line or comma separated
export function parsePairs(text: string): Record<string, string> {
  const out: Record<string, string> = {};
  for (const line of text.split(/[\n,]/)) {
    const i = line.indexOf('=');
    if (i > 0) out[line.slice(0, i).trim()] = line.slice(i + 1).trim();
  }
  return out;
}

// Joins pairs back into editable text
export function pairsText(map: Record<string, string> | undefined): string {
  return Object.entries(map ?? {})
    .map(([k, v]) => `${k}=${v}`)
    .join('\n');
}

const multipliers: Record<string, number> = {
  '': 1,
  b: 1,
  k: 1024,
  kb: 1024,
  kib: 1024,
  m: 1 << 20,
  mb: 1 << 20,
  mib: 1 << 20,
  g: 1 << 30,
  gb: 1 << 30,
  gib: 1 << 30,
  t: 2 ** 40,
  tb: 2 ** 40,
  tib: 2 ** 40
};

// Parses a size such as 8GiB into bytes, 0 when blank or malformed
export function parseBytes(text: string): bigint {
  const m = text.trim().match(/^([0-9]*\.?[0-9]+)\s*([A-Za-z]*)$/);
  if (!m) return 0n;
  const unit = multipliers[m[2].toLowerCase()];
  if (unit === undefined) return 0n;
  return BigInt(Math.round(parseFloat(m[1]) * unit));
}

// Splits org/name into its parts
export function splitRepo(repo: string): { org: string; name: string } {
  const i = repo.lastIndexOf('/');
  if (i < 0) return { org: '', name: repo };
  return { org: repo.slice(0, i), name: repo.slice(i + 1) };
}

// Sorts by a string key
export function byName<T>(key: (t: T) => string) {
  return (a: T, b: T) => key(a).localeCompare(key(b));
}

// Sorts newest first by a timestamp field
export function newestFirst<T extends { createdAt?: Timestamp }>(a: T, b: T): number {
  return Number((b.createdAt?.seconds ?? 0n) - (a.createdAt?.seconds ?? 0n));
}

// Formats a context length like 32k
export function ctx(n: number): string {
  if (n >= 1024 && n % 1024 === 0) return n / 1024 + 'k';
  return n.toLocaleString();
}
