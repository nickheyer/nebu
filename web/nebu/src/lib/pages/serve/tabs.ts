import type { HeaderTab } from '#lib/shell/chrome.svelte.js';

export const serveTabs: HeaderTab[] = [
	{ id: 'instances', label: 'Instances', href: '/serve' },
	{ id: 'runtimes', label: 'Runtimes', href: '/runtimes' }
];
