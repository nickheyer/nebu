import type { Timestamp } from '@bufbuild/protobuf/wkt';
import { timestampDate } from '@bufbuild/protobuf/wkt';

// Formats bytes for people
export function human(n: bigint | number | undefined): string {
  if (n === undefined) return '-';
  let v = typeof n === 'bigint' ? Number(n) : n;
  if (!v) return '0 B';
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB', 'PiB'];
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return (i === 0 ? v.toFixed(0) : v.toFixed(1)) + ' ' + units[i];
}

// Formats a count with thousands separators
export function count(n: bigint | number | undefined): string {
  if (n === undefined) return '-';
  return Number(n).toLocaleString();
}

// Lower cases an enum value name
export function enumName(values: Record<number, string>, v: number): string {
  return (values[v] ?? 'unspecified').toLowerCase();
}

// Formats a timestamp as local time
export function when(ts?: Timestamp): string {
  if (!ts) return '-';
  return timestampDate(ts).toLocaleString();
}

// Formats a timestamp relative to now
export function ago(ts?: Timestamp): string {
  if (!ts) return '-';
  const s = Math.max(0, (Date.now() - timestampDate(ts).getTime()) / 1000);
  if (s < 60) return Math.floor(s) + 's ago';
  if (s < 3600) return Math.floor(s / 60) + 'm ago';
  if (s < 86400) return Math.floor(s / 3600) + 'h ago';
  return Math.floor(s / 86400) + 'd ago';
}

// Colors a state name
export function tone(state: string): string {
  switch (state) {
    case 'ready':
    case 'succeeded':
    case 'ok':
      return 'bg-emerald-900/70 text-emerald-200';
    case 'starting':
    case 'running':
    case 'pending':
    case 'swapping':
    case 'draining':
    case 'stopping':
      return 'bg-amber-900/70 text-amber-200';
    case 'failed':
    case 'fail':
    case 'canceled':
      return 'bg-red-900/70 text-red-200';
    case 'warn':
      return 'bg-amber-900/70 text-amber-200';
    default:
      return 'bg-zinc-800 text-zinc-300';
  }
}

// Splits name=value pairs typed one per line
export function parsePairs(text: string): Record<string, string> {
  const out: Record<string, string> = {};
  for (const line of text.split(/[\n,]/)) {
    const i = line.indexOf('=');
    if (i > 0) out[line.slice(0, i).trim()] = line.slice(i + 1).trim();
  }
  return out;
}

// Parses a size such as 8GiB into bytes
export function parseBytes(text: string): bigint {
  const m = text.trim().match(/^([0-9]*\.?[0-9]+)\s*([A-Za-z]*)$/);
  if (!m) return 0n;
  const mult: Record<string, number> = { '': 1, b: 1, k: 1024, kb: 1024, kib: 1024, m: 1 << 20, mb: 1 << 20, mib: 1 << 20, g: 1 << 30, gb: 1 << 30, gib: 1 << 30, t: 2 ** 40, tb: 2 ** 40, tib: 2 ** 40 };
  const unit = mult[m[2].toLowerCase()];
  if (unit === undefined) return 0n;
  return BigInt(Math.round(parseFloat(m[1]) * unit));
}
