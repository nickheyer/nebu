import type { HeaderTab } from '#lib/shell/chrome.svelte.js';

// The tabs Serve carries across its own routes and the runtime catalog it took in
export const serveTabs: HeaderTab[] = [
	{ id: 'instances', label: 'Instances', href: '/serve' },
	{ id: 'runtimes', label: 'Runtimes', href: '/runtimes' }
];
