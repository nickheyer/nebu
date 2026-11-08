import type { HeaderTab } from '#lib/shell/chrome.svelte.js';

// The tabs Models carries across the stored library and the source search it took in
export const modelsTabs: HeaderTab[] = [
	{ id: 'library', label: 'Library', href: '/models' },
	{ id: 'discover', label: 'Discover', href: '/discover' }
];
