// How values spread along a rail, evenly or with every doubling the same distance
export type Curve = 'linear' | 'geometric';

export type Span = { min: number; max: number; step: number; curve: Curve };

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
