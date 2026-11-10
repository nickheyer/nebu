import type { HeaderTab } from '#lib/shell/chrome.svelte.js';

// The tabs Host carries across its own routes and the task list it took in
export const hostTabs: HeaderTab[] = [
	{ id: 'overview', label: 'Overview', href: '/' },
	{ id: 'tasks', label: 'Tasks', href: '/tasks' },
	{ id: 'logs', label: 'Logs', href: '/logs' }
];
