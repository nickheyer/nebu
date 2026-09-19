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

// Use binary units for memory.
export function bytes(n: bigint | number | undefined | null, digits = 1): string {
  return scaled(n, 1024, binary, digits);
}

// Use decimal units for storage.
export function storage(n: bigint | number | undefined | null, digits = 1): string {
  return scaled(n, 1000, decimal, digits);
}

// Use the budget's unit for both values, such as 0.4 / 12 GiB.
// Show overflow as capacity plus excess, such as 63+169 / 63 GiB.
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

export function ratioBytes(a: bigint | number | undefined | null, b: bigint | number): string {
  return ratio(a, b, 1024, binary);
}

export function ratioStorage(a: bigint | number | undefined | null, b: bigint | number): string {
  return ratio(a, b, 1000, decimal);
}

export function deltaBytes(n: number): string {
  return (n < 0 ? '-' : '+') + bytes(Math.abs(n));
}

// Use compact notation above ten thousand.
export function count(n: bigint | number | undefined): string {
  if (n === undefined) return '–';
  const v = Number(n);
  if (v >= 1_000_000_000) return (v / 1_000_000_000).toFixed(1) + 'B';
  if (v >= 1_000_000) return (v / 1_000_000).toFixed(1) + 'M';
  if (v >= 10_000) return (v / 1_000).toFixed(1) + 'K';
  return v.toLocaleString();
}

// Format parameter counts as 7.6B.
export function params(n: bigint | number | undefined): string {
  if (!n) return '–';
  const v = Number(n);
  if (v >= 1e12) return (v / 1e12).toFixed(1) + 'T';
  if (v >= 1e9) return (v / 1e9).toFixed(1) + 'B';
  if (v >= 1e6) return (v / 1e6).toFixed(0) + 'M';
  return v.toLocaleString();
}

export function pct(a: bigint | number | undefined, b: bigint | number | undefined): number {
  const x = Number(a ?? 0);
  const y = Number(b ?? 0);
  if (!y) return 0;
  return Math.max(0, Math.min(100, (x / y) * 100));
}

export function plural(n: number | bigint, one: string, many = one + 's'): string {
  const v = Number(n);
  return `${v.toLocaleString()} ${v === 1 ? one : many}`;
}

export function enumLabel(values: Record<number, string>, v: number | undefined): string {
  const raw = values[v ?? 0] ?? 'UNSPECIFIED';
  return raw.toLowerCase().replace(/_/g, ' ');
}

export function stateLabel(values: Record<number, string>, v: number | undefined): string {
  const s = enumLabel(values, v);
  return s.charAt(0).toUpperCase() + s.slice(1);
}

const moving = new Set(['starting', 'running', 'pending', 'swapping', 'draining', 'stopping']);

export function inMotion(values: Record<number, string>, v: number | undefined): boolean {
  return moving.has(enumLabel(values, v));
}

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

export function when(ts?: Timestamp): string {
  if (!ts) return '–';
  return timestampDate(ts).toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' });
}

// Include the date for timestamps outside today.
export function clockTime(ts: Timestamp | undefined, now: number = Date.now()): string {
  if (!ts) return '–';
  const d = timestampDate(ts);
  const time = d.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit', second: '2-digit' });
  const today = new Date(now);
  const sameDay = d.getFullYear() === today.getFullYear() && d.getMonth() === today.getMonth() && d.getDate() === today.getDate();
  return sameDay ? time : `${d.toLocaleDateString(undefined, { month: 'short', day: 'numeric' })} ${time}`;
}

// Pass now explicitly to update relative times reactively.
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

// Accept newline- or comma-separated name=value pairs.
export function parsePairs(text: string): Record<string, string> {
  const out: Record<string, string> = {};
  for (const line of text.split(/[\n,]/)) {
    const i = line.indexOf('=');
    if (i > 0) out[line.slice(0, i).trim()] = line.slice(i + 1).trim();
  }
  return out;
}

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

// Parse sizes such as 8GiB. Return zero for empty or invalid input.
export function parseBytes(text: string): bigint {
  const m = text.trim().match(/^([0-9]*\.?[0-9]+)\s*([A-Za-z]*)$/);
  if (!m) return 0n;
  const unit = multipliers[m[2].toLowerCase()];
  if (unit === undefined) return 0n;
  return BigInt(Math.round(parseFloat(m[1]) * unit));
}

export function byName<T>(key: (t: T) => string) {
  return (a: T, b: T) => key(a).localeCompare(key(b));
}

export function newestFirst<T>(key: (t: T) => Timestamp | undefined) {
  return (a: T, b: T) => Number((key(b)?.seconds ?? 0n) - (key(a)?.seconds ?? 0n));
}

export function ctx(n: number): string {
  if (n >= 1048576 && n % 1048576 === 0) return n / 1048576 + 'M';
  if (n >= 1024 && n % 1024 === 0) return n / 1024 + 'k';
  return n.toLocaleString();
}

// Truncate the middle of a path or ID.
export function middle(text: string, max = 40): string {
  if (text.length <= max) return text;
  const head = Math.ceil((max - 1) * 0.55);
  return text.slice(0, head) + '…' + text.slice(text.length - (max - 1 - head));
}

export function tail(text: string): string {
  return text.split('/').filter(Boolean).pop() || text;
}

export function millisBetween(from?: Timestamp, to?: Timestamp): number | undefined {
  if (!from || !to) return undefined;
  return timestampDate(to).getTime() - timestampDate(from).getTime();
}

export function ms(n: number | undefined): string {
  if (n === undefined || n === null || Number.isNaN(n)) return '–';
  if (n < 1000) return `${Math.round(n)} ms`;
  if (n < 60000) return `${(n / 1000).toFixed(n < 10000 ? 2 : 1)} s`;
  return `${Math.floor(n / 60000)}m ${Math.round((n % 60000) / 1000)}s`;
}

// Omit rates for intervals too short to measure.
export function rate(tokens: number | undefined, millis: number | undefined): string {
  if (!tokens || !millis || millis < 50) return '–';
  return `${(tokens / (millis / 1000)).toFixed(1)} tok/s`;
}

export function gib(n: bigint | number | undefined): string {
  if (!n) return '';
  const v = Number(n) / 1024 ** 3;
  return Number.isInteger(v) ? String(v) : v.toFixed(2).replace(/\.?0+$/, '');
}

// Return zero for empty or invalid input.
export function fromGib(text: string): bigint {
  const v = parseFloat(text);
  if (!Number.isFinite(v) || v <= 0) return 0n;
  return BigInt(Math.round(v * 1024 ** 3));
}

// Keep each flag and its value on the same line.
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

// Leave non-JSON text unchanged.
export function prettyJson(text: string): string {
  try {
    return JSON.stringify(JSON.parse(text), null, 2);
  } catch {
    return text;
  }
}

export interface Segment {
  text: string;
  mono?: boolean;
}

export const words = (text: string): Segment => ({ text });
export const ident = (text: string): Segment => ({ text, mono: true });

export function idents(ids: string[]): Segment[] {
  return ids.flatMap((id, i) => (i ? [words(', '), ident(id)] : [ident(id)]));
}

export function lineText(segments: Segment[]): string {
  return segments.map((s) => s.text).join('');
}
