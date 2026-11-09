import type { DescEnum } from '@bufbuild/protobuf';
import { timestampDate, type Timestamp } from '@bufbuild/protobuf/wkt';

// Thin space between a number and its unit
export const thin = ' ';

const byteUnits = ['B', 'KB', 'MB', 'GB', 'TB', 'PB'];

// A byte count in binary steps labelled as the spec draws them, such as 42 GB
export function bytes(n: bigint | number | undefined, digits?: number): string {
	let v = Number(n ?? 0);
	let i = 0;
	while (v >= 1024 && i < byteUnits.length - 1) {
		v /= 1024;
		i++;
	}
	const d = digits ?? (i === 0 ? 0 : v >= 100 ? 0 : v >= 10 ? 1 : 1);
	return `${v.toFixed(d)}${thin}${byteUnits[i]}`;
}

// The number alone in the unit the total takes, so a pair reads 68 / 80 GB
export function bytesIn(n: bigint | number | undefined, total: bigint | number | undefined): string {
	let t = Number(total ?? 0);
	let i = 0;
	while (t >= 1024 && i < byteUnits.length - 1) {
		t /= 1024;
		i++;
	}
	const v = Number(n ?? 0) / 1024 ** i;
	return v.toFixed(v >= 100 || v === 0 ? 0 : 1);
}

export function byteUnit(total: bigint | number | undefined): string {
	let t = Number(total ?? 0);
	let i = 0;
	while (t >= 1024 && i < byteUnits.length - 1) {
		t /= 1024;
		i++;
	}
	return byteUnits[i] ?? 'B';
}

export function percent(done: bigint | number | undefined, total: bigint | number | undefined): number | undefined {
	const t = Number(total ?? 0);
	if (t <= 0) return undefined;
	return Math.min(100, (Number(done ?? 0) / t) * 100);
}

export function pct(value: number | undefined, digits = 0): string {
	if (value === undefined || Number.isNaN(value)) return '';
	return `${value.toFixed(digits)}${thin}%`;
}

const grouped = new Intl.NumberFormat(undefined, { maximumFractionDigits: 0 });

export function num(n: bigint | number | undefined): string {
	return grouped.format(Number(n ?? 0));
}

// Milliseconds as the spec writes them, 180 ms under a second and 1.2 s above
export function ms(value: number | undefined): string {
	const v = value ?? 0;
	if (v < 1000) return `${Math.round(v)}${thin}ms`;
	if (v < 60_000) return `${(v / 1000).toFixed(1)}${thin}s`;
	return `${Math.floor(v / 60_000)}${thin}min ${Math.round((v % 60_000) / 1000)}${thin}s`;
}

export function seconds(value: number | undefined): string {
	return ms((value ?? 0) * 1000);
}

export function date(ts: Timestamp | undefined): Date | undefined {
	return ts ? timestampDate(ts) : undefined;
}

const relative = new Intl.RelativeTimeFormat(undefined, { numeric: 'always', style: 'narrow' });
const steps: [Intl.RelativeTimeFormatUnit, number][] = [
	['year', 31536000],
	['month', 2592000],
	['week', 604800],
	['day', 86400],
	['hour', 3600],
	['minute', 60]
];

// A time as distance from now within nine characters, such as 3h ago
export function ago(ts: Timestamp | Date | undefined, now = Date.now()): string {
	const d = ts instanceof Date ? ts : date(ts);
	if (!d) return '';
	const secs = (d.getTime() - now) / 1000;
	for (const [unit, size] of steps) {
		if (Math.abs(secs) >= size) return relative.format(Math.round(secs / size), unit);
	}
	return relative.format(Math.round(secs), 'second');
}

export function when(ts: Timestamp | Date | undefined): string {
	const d = ts instanceof Date ? ts : date(ts);
	return d ? d.toLocaleString() : '';
}

export function clock(ts: Timestamp | Date | undefined): string {
	const d = ts instanceof Date ? ts : date(ts);
	return d ? d.toLocaleTimeString(undefined, { hour12: false }) : '';
}

// Days until a time, negative once passed
export function daysUntil(ts: Timestamp | undefined, now = Date.now()): number | undefined {
	const d = date(ts);
	if (!d) return undefined;
	return Math.floor((d.getTime() - now) / 86_400_000);
}

// The enum value's name without the shared prefix, lowercased with spaces
export function enumLabel(desc: DescEnum, value: number): string {
	const name = desc.value[value]?.name ?? String(value);
	const prefix = desc.sharedPrefix ?? '';
	return name.slice(prefix.length).toLowerCase().replaceAll('_', ' ');
}

// Every value of an enum but the unspecified one, as choices with their labels
export function enumOptions(desc: DescEnum): { value: number; label: string }[] {
	return desc.values.filter((v) => v.number !== 0).map((v) => ({ value: v.number, label: enumLabel(desc, v.number) }));
}

export function duration(from: Timestamp | undefined, to: Timestamp | undefined, now = Date.now()): string {
	const a = date(from);
	if (!a) return '';
	const b = date(to)?.getTime() ?? now;
	return elapsed(Math.max(0, b - a.getTime()));
}

// A span of milliseconds as 42 s, 4m 12s or 2h 05m
export function elapsed(millis: number): string {
	const s = Math.max(0, Math.round(millis / 1000));
	if (s < 60) return `${s}${thin}s`;
	if (s < 3600) return `${Math.floor(s / 60)}m ${String(s % 60).padStart(2, '0')}s`;
	if (s < 86400) return `${Math.floor(s / 3600)}h ${String(Math.floor((s % 3600) / 60)).padStart(2, '0')}m`;
	return `${Math.floor(s / 86400)}d ${Math.floor((s % 86400) / 3600)}h`;
}

export function uptime(since: Timestamp | undefined, now = Date.now()): string {
	const d = date(since);
	return d ? elapsed(now - d.getTime()) : '';
}

// The first eight characters of an id
export function shortId(id: string | undefined): string {
	return (id ?? '').slice(0, 8);
}

export function capitalize(s: string): string {
	return s ? s[0]!.toUpperCase() + s.slice(1) : s;
}

const acronyms = new Set(['id', 'pci', 'os', 'cpu', 'gpu', 'ram', 'api', 'url', 'uuid', 'pnp', 'vram', 'oci', 'tls', 'numa']);

// A fact key as a label, pci_bus_id reading PCI bus ID
export function factLabel(key: string): string {
	const words = key.split(/[._]+/).filter(Boolean);
	return words.map((w, i) => (acronyms.has(w) ? w.toUpperCase() : i === 0 ? capitalize(w) : w)).join(' ');
}

export function plural(n: number, one: string, many = `${one}s`): string {
	return `${num(n)} ${n === 1 ? one : many}`;
}

// A rate per minute as the spec draws it, 42 /min, a full space holding off the slash's overhang
export function perMinute(n: number): string {
	return `${n >= 10 || n === 0 ? Math.round(n) : n.toFixed(1)} /min`;
}

export function tokensPerSecond(n: number | undefined): string {
	if (!n) return '';
	return `${n >= 100 ? Math.round(n) : n.toFixed(1)}${thin}tok/s`;
}

// A bandwidth in bits per second from a byte rate
export function bandwidth(bytesPerSecond: bigint | number | undefined): string {
	let v = Number(bytesPerSecond ?? 0) * 8;
	const units = ['b/s', 'kb/s', 'Mb/s', 'Gb/s', 'Tb/s'];
	let i = 0;
	while (v >= 1000 && i < units.length - 1) {
		v /= 1000;
		i++;
	}
	return `${v.toFixed(v >= 10 || i === 0 ? 0 : 1)}${thin}${units[i]}`;
}

// Microseconds as 85 us or 1.2 ms
export function micros(us: number | undefined): string {
	const v = us ?? 0;
	if (v < 1000) return `${Math.round(v)}${thin}us`;
	return ms(v / 1000);
}
