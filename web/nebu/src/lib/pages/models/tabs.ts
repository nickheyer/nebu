import type { HeaderTab } from '#lib/shell/chrome.svelte.js';

export const modelsTabs: HeaderTab[] = [
	{ id: 'library', label: 'Library', href: '/models' },
	{ id: 'discover', label: 'Discover', href: '/discover' }
];
