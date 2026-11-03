import type { DescEnum } from '@bufbuild/protobuf';
import { timestampDate, type Timestamp } from '@bufbuild/protobuf/wkt';

const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB', 'PiB'];

export function bytes(n: bigint | number | undefined): string {
	let v = Number(n ?? 0);
	let i = 0;
	while (v >= 1024 && i < units.length - 1) {
		v /= 1024;
		i++;
	}
	return `${v.toFixed(i === 0 ? 0 : v >= 100 ? 0 : 1)} ${units[i]}`;
}

export function percent(done: bigint | number | undefined, total: bigint | number | undefined): number | undefined {
	const t = Number(total ?? 0);
	if (t <= 0) return undefined;
	return Math.min(100, (Number(done ?? 0) / t) * 100);
}

export function date(ts: Timestamp | undefined): Date | undefined {
	return ts ? timestampDate(ts) : undefined;
}

const relative = new Intl.RelativeTimeFormat(undefined, { numeric: 'auto' });
const steps: [Intl.RelativeTimeFormatUnit, number][] = [
	['year', 31536000],
	['month', 2592000],
	['week', 604800],
	['day', 86400],
	['hour', 3600],
	['minute', 60]
];

// A time as distance from now, such as 3 hours ago
export function ago(ts: Timestamp | undefined, now = Date.now()): string {
	const d = date(ts);
	if (!d) return '';
	const secs = (d.getTime() - now) / 1000;
	for (const [unit, size] of steps) {
		if (Math.abs(secs) >= size) return relative.format(Math.round(secs / size), unit);
	}
	return relative.format(Math.round(secs), 'second');
}

export function when(ts: Timestamp | undefined): string {
	const d = date(ts);
	return d ? d.toLocaleString() : '';
}

// The enum value's name without the shared prefix, lowercased with spaces
export function enumLabel(desc: DescEnum, value: number): string {
	const name = desc.value[value]?.name ?? String(value);
	const prefix = desc.sharedPrefix ?? '';
	return name.slice(prefix.length).toLowerCase().replaceAll('_', ' ');
}

export function duration(from: Timestamp | undefined, to: Timestamp | undefined, now = Date.now()): string {
	const a = date(from);
	if (!a) return '';
	const b = date(to)?.getTime() ?? now;
	const s = Math.max(0, Math.round((b - a.getTime()) / 1000));
	if (s < 60) return `${s}s`;
	if (s < 3600) return `${Math.floor(s / 60)}m ${s % 60}s`;
	return `${Math.floor(s / 3600)}h ${Math.floor((s % 3600) / 60)}m`;
}
