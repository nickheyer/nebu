import type { HeaderTab } from '#lib/shell/chrome.svelte.js';

export const serveTabs: HeaderTab[] = [
	{ id: 'slots', label: 'Slots', href: '/serve' },
	{ id: 'runtimes', label: 'Runtimes', href: '/runtimes' }
];
