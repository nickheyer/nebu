import { TraceKind, type Trace } from '#lib/proto/nebu/v1/gateway_pb.js';
import { date, ms, num, pct, perMinute } from '#lib/format.js';
import type { FactItem } from '#lib/ui/Facts.svelte';
import type { Fact } from '#lib/ui/Tip.svelte';

export type Window = '1m' | '5m' | '15m' | 'range';

export const windowMs: Record<Exclude<Window, 'range'>, number> = { '1m': 60_000, '5m': 300_000, '15m': 900_000 };

export function isWindow(s: string | null | undefined): s is Window {
	return s === '1m' || s === '5m' || s === '15m' || s === 'range';
}

// Seconds before now a range starts when the link names no start
export const rangeStartDefault = 3600;

// The span the page opens on for a remembered window, a range without a start reaching back an hour
export function landingMs(w: string): number {
	if (!isWindow(w)) return windowMs['5m'];
	return w === 'range' ? rangeStartDefault * 1000 : windowMs[w];
}

// Bytes of each body a trace keeps before the daemon cuts it
const bodyCap = 1 << 20;

export function started(t: Trace): number {
	return date(t.startedAt)?.getTime() ?? 0;
}

export function finished(t: Trace): number | undefined {
	return date(t.finishedAt)?.getTime();
}

export function firstToken(t: Trace): number | undefined {
	return date(t.firstTokenAt)?.getTime();
}

export function ttft(t: Trace): number | undefined {
	const f = firstToken(t);
	return f === undefined ? undefined : f - started(t);
}

// Milliseconds per output token after the first, undefined without enough tokens
export function tpot(t: Trace): number | undefined {
	const f = firstToken(t);
	const end = finished(t);
	if (f === undefined || end === undefined || t.completionTokens < 2) return undefined;
	return (end - f) / (t.completionTokens - 1);
}

export function percentile(values: number[], p: number): number | undefined {
	if (values.length === 0) return undefined;
	const sorted = [...values].sort((a, b) => a - b);
	return sorted[Math.min(sorted.length - 1, Math.floor((sorted.length - 1) * p))];
}

export function isShed(t: Trace): boolean {
	return t.status === 429 || t.status === 503;
}

export function failed(t: Trace): boolean {
	return t.status >= 400 || t.error !== '';
}

export function kindWord(k: TraceKind): string {
	switch (k) {
		case TraceKind.CHAT:
			return 'chat';
		case TraceKind.GENERATE:
			return 'generate';
		case TraceKind.EMBED:
			return 'embed';
		case TraceKind.COUNT:
			return 'count';
		case TraceKind.IMAGE:
			return 'image';
		case TraceKind.VIDEO:
			return 'video';
		case TraceKind.OTHER:
			return 'other';
		default:
			return 'unknown';
	}
}

export const traceKinds = [TraceKind.CHAT, TraceKind.GENERATE, TraceKind.EMBED, TraceKind.COUNT, TraceKind.IMAGE, TraceKind.VIDEO, TraceKind.OTHER];

// Items split into equal buckets across a span, each bucket reduced to one value
export function buckets<T>(items: T[], at: (item: T) => number, from: number, to: number, count: number, value: (list: T[]) => number): number[] {
	const size = Math.max(1, (to - from) / count);
	const groups: T[][] = Array.from({ length: count }, () => []);
	for (const it of items) {
		const i = Math.floor((at(it) - from) / size);
		if (i >= 0 && i < count) groups[i]!.push(it);
	}
	return groups.map(value);
}

// Points each reading's spark holds across its window
export const sparkBuckets = 24;

// Request rate, error share, first token and per token times of the traces in a span, each with its spark
export function windowFacts(traces: Trace[], from: number, end: number, extra: Fact[] = []): FactItem[] {
	const done = traces.filter((t) => finished(t) !== undefined);
	const minutes = Math.max(1 / 60, (end - from) / 60_000);
	const defined = (v: number | undefined): v is number => v !== undefined;
	const ttfts = traces.map(ttft).filter(defined);
	const tpots = traces.map(tpot).filter(defined);
	const waits = traces.map((t) => t.queueWaitMs).filter((v) => v > 0);
	const errorRate = done.length ? (done.filter(failed).length / done.length) * 100 : 0;
	const median = (values: number[]) => percentile(values, 0.5) ?? 0;
	const split = (value: (l: Trace[]) => number) => buckets(traces, started, from, end, sparkBuckets, value);
	const errors = split((l) => l.filter(failed).length);
	return [
		{
			id: 'rate',
			label: 'Requests',
			value: perMinute(traces.length / minutes),
			spark: split((l) => l.length),
			facts: [{ label: 'in window', value: num(traces.length) }, { label: 'rejected', value: num(traces.filter(isShed).length) }, ...extra]
		},
		{
			id: 'errors',
			label: 'Errors',
			value: pct(errorRate, 1),
			lamp: errorRate >= 5 ? 'error' : errorRate > 0 ? 'warning' : undefined,
			spark: errors,
			facts: [
				{ label: '4xx', value: num(done.filter((t) => t.status >= 400 && t.status < 500).length) },
				{ label: '5xx', value: num(done.filter((t) => t.status >= 500).length) }
			]
		},
		{
			id: 'ttft',
			label: 'First token',
			value: ttfts.length ? ms(median(ttfts)) : '—',
			spark: split((l) => median(l.map(ttft).filter(defined))),
			facts: [
				{ label: 'p50', value: ttfts.length ? ms(median(ttfts)) : '—' },
				{ label: 'p95', value: ttfts.length ? ms(percentile(ttfts, 0.95)) : '—' },
				{ label: 'queue wait p95', value: waits.length ? ms(percentile(waits, 0.95)) : '—' }
			]
		},
		{
			id: 'tpot',
			label: 'Per token',
			value: tpots.length ? ms(median(tpots)) : '—',
			spark: split((l) => median(l.map(tpot).filter(defined))),
			facts: [
				{ label: 'p50', value: tpots.length ? ms(median(tpots)) : '—' },
				{ label: 'p95', value: tpots.length ? ms(percentile(tpots, 0.95)) : '—' }
			]
		}
	];
}

// Whether a kept body lost its tail to the cap, against the byte count the trace recorded when it has one
export function capped(text: string, recorded?: bigint): boolean {
	const n = new TextEncoder().encode(text).length;
	if (recorded !== undefined && recorded > 0n) return BigInt(n) < recorded;
	return n > bodyCap - 4;
}

// A moment as a short date and time, the date left out for today
export function stamp(ms: number, now = Date.now()): string {
	const d = new Date(ms);
	const today = new Date(now).toDateString() === d.toDateString();
	return d.toLocaleString(undefined, today ? { hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false } : { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit', hour12: false });
}

// The lane a trace runs in on the timeline, its endpoint, the node it went to, or none
export function laneOf(t: Trace, nodeName: (id: string) => string): { name: string; address: boolean } {
	if (t.endpoint) return { name: t.endpoint, address: true };
	if (t.nodeId) return { name: `node ${nodeName(t.nodeId)}`, address: false };
	return { name: 'not routed', address: false };
}

export type Mark = { id: 'forwarded' | 'translated' | 'failedOpen' | 'fallbacks'; tip: string; count?: number };

// The small marks after a route for what the request went through
export function marks(t: Trace, nodeName: (id: string) => string): Mark[] {
	const out: Mark[] = [];
	if (t.forwarded) out.push({ id: 'forwarded', tip: `Forwarded to ${nodeName(t.nodeId)}` });
	if (t.translated) out.push({ id: 'translated', tip: 'Translated' });
	if (t.failedOpen) out.push({ id: 'failedOpen', tip: 'Failed open' });
	if (t.fallbacks.length) out.push({ id: 'fallbacks', tip: `${t.fallbacks.length} ${t.fallbacks.length === 1 ? 'fallback' : 'fallbacks'}`, count: t.fallbacks.length });
	return out;
}
