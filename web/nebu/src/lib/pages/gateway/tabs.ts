import type { HeaderTab } from '#lib/shell/chrome.svelte.js';

export const gatewayTabs = ['routes', 'pools', 'objectives', 'schedulers', 'imports'] as const;
export type GatewayTab = (typeof gatewayTabs)[number];
export type GroupTab = 'traffic' | GatewayTab;

export function isGatewayTab(s: string): s is GatewayTab {
	return (gatewayTabs as readonly string[]).includes(s);
}

export const gatewayHeaderTabs: (HeaderTab & { id: GroupTab })[] = [
	{ id: 'traffic', label: 'Traffic', href: '/traffic' },
	{ id: 'routes', label: 'Routes', href: '/gateway/routes' },
	{ id: 'pools', label: 'Pools', href: '/gateway/pools' },
	{ id: 'objectives', label: 'Objectives', href: '/gateway/objectives' },
	{ id: 'schedulers', label: 'Schedulers', href: '/gateway/schedulers' },
	{ id: 'imports', label: 'Imports', href: '/gateway/imports' }
];
