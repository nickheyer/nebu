import { cubicOut } from 'svelte/easing';
import type { TransitionConfig } from 'svelte/transition';
import { viewport } from '#lib/state/viewport.svelte.js';

// Whether the deck animates, off only under the system reduced motion preference
export const motion = {
	get enabled(): boolean {
		return !viewport.reducedMotion;
	}
};

// The settle curve every reveal shares
export const settle = 'cubic-bezier(0.22, 0.9, 0.24, 1)';

type RevealParams = { delay?: number; duration?: number; y?: number; x?: number; scale?: number };

// Fades a block in from below with a blur that clears as it lands
export function reveal(node: Element, { delay = 0, duration = 260, y = 8, x = 0, scale = 1 }: RevealParams = {}): TransitionConfig {
	if (!motion.enabled) return { duration: 0 };
	const style = getComputedStyle(node);
	const opacity = Number(style.opacity);
	const transform = style.transform === 'none' ? '' : style.transform;
	return {
		delay,
		duration,
		easing: cubicOut,
		css: (t, u) => `opacity: ${t * opacity}; filter: blur(${u * 4}px); transform: ${transform} translate(${u * x}px, ${u * y}px) scale(${1 - (1 - scale) * u});`
	};
}

// Swings a panel in from its right edge on a slight perspective
export function hatch(node: Element, { delay = 0, duration = 320 }: RevealParams = {}): TransitionConfig {
	if (!motion.enabled) return { duration: 0 };
	return {
		delay,
		duration,
		easing: cubicOut,
		css: (t, u) => `opacity: ${t}; transform-origin: 100% 50%; transform: perspective(1400px) rotateY(${u * -6}deg) translateX(${u * 12}px); filter: blur(${u * 3}px);`
	};
}

// Grows an element from its left edge
export function grow(node: Element, { delay = 0, duration = 600 }: RevealParams = {}): TransitionConfig {
	if (!motion.enabled) return { duration: 0 };
	return {
		delay,
		duration,
		easing: cubicOut,
		css: (t) => `transform-origin: 0 50%; transform: scaleX(${t});`
	};
}

// The delay a row takes in a staggered reveal, capped for long lists
export function stagger(index: number, step = 22, cap = 12): number {
	return Math.min(index, cap) * step;
}
