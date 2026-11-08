import type { HeaderTab } from '#lib/shell/chrome.svelte.js';

export const gatewayTabs = ['routes', 'pools', 'objectives', 'schedulers', 'imports'] as const;
export type GatewayTab = (typeof gatewayTabs)[number];
export type GroupTab = 'traffic' | GatewayTab;

export function isGatewayTab(s: string): s is GatewayTab {
	return (gatewayTabs as readonly string[]).includes(s);
}

const groupTabs: { id: GroupTab; label: string; href: string }[] = [
	{ id: 'traffic', label: 'Traffic', href: '/traffic' },
	{ id: 'routes', label: 'Routes', href: '/gateway/routes' },
	{ id: 'pools', label: 'Pools', href: '/gateway/pools' },
	{ id: 'objectives', label: 'Objectives', href: '/gateway/objectives' },
	{ id: 'schedulers', label: 'Schedulers', href: '/gateway/schedulers' },
	{ id: 'imports', label: 'Imports', href: '/gateway/imports' }
];

// The tabs Gateway carries across its own routes and the traffic it took in, every count arriving at once
export function gatewayHeaderTabs(counts: Record<GroupTab, number> | undefined): HeaderTab[] {
	return groupTabs.map((t) => ({ ...t, count: counts?.[t.id] }));
}
