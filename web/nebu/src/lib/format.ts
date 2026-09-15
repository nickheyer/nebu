import type { Timestamp } from '@bufbuild/protobuf/wkt';
import { timestampDate } from '@bufbuild/protobuf/wkt';

export type Tone = 'ok' | 'warn' | 'bad' | 'info' | 'accent' | 'neutral';

const binary = ['B', 'KiB', 'MiB', 'GiB', 'TiB', 'PiB'];
const decimal = ['B', 'KB', 'MB', 'GB', 'TB', 'PB'];

function scaled(n: bigint | number | undefined | null, base: number, units: string[], digits: number): string {
  if (n === undefined || n === null) return '–';
  let v = typeof n === 'bigint' ? Number(n) : n;
  if (!v) return '0 B';
  let i = 0;
  while (v >= base && i < units.length - 1) {
    v /= base;
    i++;
  }
  return (i === 0 ? v.toFixed(0) : v.toFixed(digits)) + ' ' + units[i];
}

// Formats bytes of memory, the binary units memory is sold in
export function bytes(n: bigint | number | undefined | null, digits = 1): string {
  return scaled(n, 1024, binary, digits);
}

// Formats bytes on disk, the decimal units drives and downloads count in
export function storage(n: bigint | number | undefined | null, digits = 1): string {
  return scaled(n, 1000, decimal, digits);
}

// Formats a share of a budget in the budget's unit, 0.4 / 12 GiB, so the pair reads as one figure; past
// the budget it reads as the budget plus the excess, 63+169 / 63 GiB, the way a pool fills and then overflows
function ratio(a: bigint | number | undefined | null, b: bigint | number, base: number, units: string[]): string {
  const x = Number(a ?? 0);
  let y = Number(b);
  let i = 0;
  while (y >= base && i < units.length - 1) {
    y /= base;
    i++;
  }
  const part = x / base ** i;
  const fmt = (v: number) => (v >= 10 || v === 0 ? v.toFixed(0) : v.toFixed(1));
  const used = part > y && y > 0 ? `${y.toFixed(0)}+${fmt(part - y)}` : fmt(part);
  return `${used} / ${y.toFixed(0)} ${units[i]}`;
}

// Memory used against memory held, in binary units
export function ratioBytes(a: bigint | number | undefined | null, b: bigint | number): string {
  return ratio(a, b, 1024, binary);
}

// Bytes to land against disk free, in the decimal units drives are sold in
export function ratioStorage(a: bigint | number | undefined | null, b: bigint | number): string {
  return ratio(a, b, 1000, decimal);
}

// Formats a byte delta with its sign
export function deltaBytes(n: number): string {
  return (n < 0 ? '-' : '+') + bytes(Math.abs(n));
}

// Formats a count with separators, compact above ten thousand
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

// A noun with its count, one model or three models
export function plural(n: number | bigint, one: string, many = one + 's'): string {
  const v = Number(n);
  return `${v.toLocaleString()} ${v === 1 ? one : many}`;
}

// Lower cases a generated enum name, NEW_REVISION becomes new revision
export function enumLabel(values: Record<number, string>, v: number | undefined): string {
  const raw = values[v ?? 0] ?? 'UNSPECIFIED';
  return raw.toLowerCase().replace(/_/g, ' ');
}

// The enum name with a capital, Ready or Not installed
export function stateLabel(values: Record<number, string>, v: number | undefined): string {
  const s = enumLabel(values, v);
  return s.charAt(0).toUpperCase() + s.slice(1);
}

const moving = new Set(['starting', 'running', 'pending', 'swapping', 'draining', 'stopping']);

// Whether a state names something still in motion
export function inMotion(values: Record<number, string>, v: number | undefined): boolean {
  return moving.has(enumLabel(values, v));
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
    case 'skipped':
      return 'warn';
    case 'failed':
    case 'fail':
    case 'canceled':
    case 'no':
      return 'bad';
    default:
      return 'neutral';
  }
}

// Formats a timestamp as local time
export function when(ts?: Timestamp): string {
  if (!ts) return '–';
  return timestampDate(ts).toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' });
}

// Formats a timestamp as a clock time with seconds, the date in front when it is not today
export function clockTime(ts: Timestamp | undefined, now: number = Date.now()): string {
  if (!ts) return '–';
  const d = timestampDate(ts);
  const time = d.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit', second: '2-digit' });
  const today = new Date(now);
  const sameDay = d.getFullYear() === today.getFullYear() && d.getMonth() === today.getMonth() && d.getDate() === today.getDate();
  return sameDay ? time : `${d.toLocaleDateString(undefined, { month: 'short', day: 'numeric' })} ${time}`;
}

// Formats a timestamp relative to now, now passed so callers stay reactive
export function ago(ts: Timestamp | undefined, now: number = Date.now()): string {
  if (!ts) return '–';
  const s = Math.max(0, (now - timestampDate(ts).getTime()) / 1000);
  if (s < 5) return 'just now';
  if (s < 60) return Math.floor(s) + 's ago';
  if (s < 3600) return Math.floor(s / 60) + 'm ago';
  if (s < 86400) return Math.floor(s / 3600) + 'h ago';
  const days = Math.floor(s / 86400);
  if (days <= 30) return days + 'd ago';
  if (days < 365) {
    const months = Math.floor(days / 30.44);
    return months < 2 ? Math.floor(days / 7) + 'w ago' : months + 'mo ago';
  }
  return Math.floor(days / 365.25) + 'y ago';
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

// Sorts by a string key
export function byName<T>(key: (t: T) => string) {
  return (a: T, b: T) => key(a).localeCompare(key(b));
}

// Sorts newest first by the timestamp a key picks
export function newestFirst<T>(key: (t: T) => Timestamp | undefined) {
  return (a: T, b: T) => Number((key(b)?.seconds ?? 0n) - (key(a)?.seconds ?? 0n));
}

// Formats a context length like 32k
export function ctx(n: number): string {
  if (n >= 1048576 && n % 1048576 === 0) return n / 1048576 + 'M';
  if (n >= 1024 && n % 1024 === 0) return n / 1024 + 'k';
  return n.toLocaleString();
}

// Keeps both ends of a long path or id, the middle folded
export function middle(text: string, max = 40): string {
  if (text.length <= max) return text;
  const head = Math.ceil((max - 1) * 0.55);
  return text.slice(0, head) + '…' + text.slice(text.length - (max - 1 - head));
}

// The last segment of a repository or path
export function tail(text: string): string {
  return text.split('/').filter(Boolean).pop() || text;
}

// Milliseconds between two timestamps, undefined when either is missing
export function millisBetween(from?: Timestamp, to?: Timestamp): number | undefined {
  if (!from || !to) return undefined;
  return timestampDate(to).getTime() - timestampDate(from).getTime();
}

// Formats a span in milliseconds, seconds past one second
export function ms(n: number | undefined): string {
  if (n === undefined || n === null || Number.isNaN(n)) return '–';
  if (n < 1000) return `${Math.round(n)} ms`;
  if (n < 60000) return `${(n / 1000).toFixed(n < 10000 ? 2 : 1)} s`;
  return `${Math.floor(n / 60000)}m ${Math.round((n % 60000) / 1000)}s`;
}

// Tokens per second from a count and a span in milliseconds, nothing for a span too short to mean anything
export function rate(tokens: number | undefined, millis: number | undefined): string {
  if (!tokens || !millis || millis < 50) return '–';
  return `${(tokens / (millis / 1000)).toFixed(1)} tok/s`;
}

// Bytes as a whole number of GiB for a field, empty for zero
export function gib(n: bigint | number | undefined): string {
  if (!n) return '';
  const v = Number(n) / 1024 ** 3;
  return Number.isInteger(v) ? String(v) : v.toFixed(2).replace(/\.?0+$/, '');
}

// A GiB figure typed in a field back to bytes, zero for blank or malformed
export function fromGib(text: string): bigint {
  const v = parseFloat(text);
  if (!Number.isFinite(v) || v <= 0) return 0n;
  return BigInt(Math.round(v * 1024 ** 3));
}

// Lays a command out one flag per line, a flag and its value together
export function commandLines(argv: string[]): string {
  if (argv.length === 0) return '';
  const lines: string[] = [argv[0]];
  for (let i = 1; i < argv.length; i++) {
    const arg = argv[i];
    const next = argv[i + 1];
    if (arg.startsWith('-') && next !== undefined && !/^-[A-Za-z]/.test(next)) {
      lines.push(`${arg} ${next}`);
      i++;
    } else {
      lines.push(arg);
    }
  }
  return lines.join(' \\\n  ');
}

// Pretty prints JSON text, returning it untouched when it is not JSON
export function prettyJson(text: string): string {
  try {
    return JSON.stringify(JSON.parse(text), null, 2);
  } catch {
    return text;
  }
}

// A line of words with identifiers among them: the identifiers are set in mono and never break across lines
export interface Segment {
  text: string;
  mono?: boolean;
}

export const words = (text: string): Segment => ({ text });
export const ident = (text: string): Segment => ({ text, mono: true });

// Identifiers as a list, commas between them
export function idents(ids: string[]): Segment[] {
  return ids.flatMap((id, i) => (i ? [words(', '), ident(id)] : [ident(id)]));
}

// A line as plain text
export function lineText(segments: Segment[]): string {
  return segments.map((s) => s.text).join('');
}
