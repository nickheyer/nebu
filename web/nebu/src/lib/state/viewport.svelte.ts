import { MediaQuery } from 'svelte/reactivity';

const narrow = new MediaQuery('(max-width: 720px)');
const wide = new MediaQuery('(min-width: 1600px)');
const reduced = new MediaQuery('(prefers-reduced-motion: reduce)');

// The three widths the spec lays every page out for
export const viewport = {
	get mobile(): boolean {
		return narrow.current;
	},
	get ultrawide(): boolean {
		return wide.current;
	},
	get standard(): boolean {
		return !narrow.current && !wide.current;
	},
	get reducedMotion(): boolean {
		return reduced.current;
	}
};
