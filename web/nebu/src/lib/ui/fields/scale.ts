// How values spread along a rail, evenly or with every doubling the same distance
export type Curve = 'linear' | 'geometric';

export type Span = { min: number; max: number; step: number; curve: Curve };

// A number a form shows by what it means, such as -1 for no limit
export type Named = { value: number; label: string };

// A labelled mark under a rail
export type Tick = { value: number; label: string; at: number };

// Geometric once a rail has too many steps to give each a pixel, linear below that
export function curveOf(min: number, max: number, step: number): Curve {
	return (max - min) / step >= 256 ? 'geometric' : 'linear';
}

// Digits a number writes after its decimal point
export function decimalsOf(n: number): number {
	return (String(n).split('.')[1] ?? '').length;
}

// A value inside the span, written at the step's decimals
function settle(v: number, s: Span): number {
	return Number(Math.min(s.max, Math.max(s.min, v)).toFixed(decimalsOf(s.step)));
}

// Steps between neighbouring stops at a count of steps from zero, keeping its three leading binary digits
function grain(count: number): number {
	const spare = Math.floor(Math.log2(Math.abs(count))) - 2;
	return spare > 0 ? 2 ** spare : 1;
}

// Where a value sits along the rail, zero at its start and one at its end
export function place(v: number, s: Span): number {
	if (s.max <= s.min) return 0;
	const held = Math.min(s.max, Math.max(s.min, v));
	if (s.curve === 'linear') return (held - s.min) / (s.max - s.min);
	return Math.log1p((held - s.min) / s.step) / Math.log1p((s.max - s.min) / s.step);
}

// The value a point along the rail lands on, each end exact and every point between on a round stop
export function land(t: number, s: Span): number {
	if (t <= 0 || s.max <= s.min) return s.min;
	if (t >= 1) return s.max;
	if (s.curve === 'linear') return settle(Math.round((s.min + t * (s.max - s.min)) / s.step) * s.step, s);
	const raw = s.min + Math.expm1(t * Math.log1p((s.max - s.min) / s.step)) * s.step;
	const gap = s.step * grain(raw / s.step);
	return settle(Math.round(raw / gap) * gap, s);
}

// The stop a tenth of the rail away, up or down, or the next one past it
export function stride(v: number, up: boolean, s: Span): number {
	const from = place(v, s);
	const here = settle(v, s);
	for (let reach = 10; reach <= 100; reach++) {
		const next = land(from + (up ? reach : -reach) / 100, s);
		if (next !== here) return next;
	}
	return here;
}

const grouped = new Intl.NumberFormat('en-US');

// A scale mark in few characters, binary thousands for counts stepped in powers of two and the plain number otherwise
export function tickLabel(v: number, s: Span): string {
	const binary = Number.isInteger(s.step) && s.step >= 32 && (s.step & (s.step - 1)) === 0;
	if (binary && v >= 2 ** 20 && v % 2 ** 20 === 0) return `${v / 2 ** 20}M`;
	if (binary && v >= 1024 && v % 1024 === 0) return `${v / 1024}K`;
	return Number.isInteger(v) ? grouped.format(v) : v.toFixed(Math.max(decimalsOf(s.step), decimalsOf(v)));
}

// The values a rail labels, both ends always and every power of two between them on a geometric one
export function marks(s: Span): number[] {
	if (s.max <= s.min) return [s.min];
	const out = [s.min];
	if (s.curve === 'geometric') {
		for (let p = 2 ** Math.ceil(Math.log2(Math.max(s.step, s.min + s.step))); p < s.max; p *= 2) {
			if (p > s.min && Number.isInteger(p / s.step)) out.push(p);
		}
	}
	out.push(s.max);
	return out;
}

// Marks that fit a rail of the given pixels without touching, the ends kept and the inner ones thinned to every nth
export function ticks(s: Span, width: number, charPx: number): Tick[] {
	const all = marks(s).map((value) => ({ value, label: tickLabel(value, s), at: place(value, s) }));
	if (all.length <= 2 || width <= 0) return all;
	const first = all[0]!;
	const last = all[all.length - 1]!;
	const between = all.slice(1, -1);
	const gap = 12;
	const size = (t: Tick) => t.label.length * charPx;
	// An inner mark is centred on its place, the ends sit inside the rail
	const fits = (inner: Tick[]) => {
		let edge = size(first) + gap;
		for (const t of inner) {
			const left = t.at * width - size(t) / 2;
			if (left < edge) return false;
			edge = left + size(t) + gap;
		}
		return edge <= width - size(last);
	};
	for (let every = 1; every <= between.length; every++) {
		for (let from = 0; from < every; from++) {
			const inner = between.filter((_, i) => i % every === from);
			if (inner.length && fits(inner)) return [first, ...inner, last];
		}
	}
	return [first, last];
}
