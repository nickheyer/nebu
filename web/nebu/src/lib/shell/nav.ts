import type { Component } from 'svelte';
import type { IconComponentProps } from 'phosphor-svelte';
import ChatsCircleIcon from 'phosphor-svelte/lib/ChatsCircleIcon';
import CubeIcon from 'phosphor-svelte/lib/CubeIcon';
import GaugeIcon from 'phosphor-svelte/lib/GaugeIcon';
import GearIcon from 'phosphor-svelte/lib/GearIcon';
import HardDrivesIcon from 'phosphor-svelte/lib/HardDrivesIcon';
import PlayIcon from 'phosphor-svelte/lib/PlayIcon';
import RobotIcon from 'phosphor-svelte/lib/RobotIcon';
import ShareNetworkIcon from 'phosphor-svelte/lib/ShareNetworkIcon';
import TreeStructureIcon from 'phosphor-svelte/lib/TreeStructureIcon';
import type { Role } from '#lib/auth/session.svelte.js';

export type NavPage = {
	id: string;
	path: string;
	label: string;
	icon: Component<IconComponentProps>;
	roles: Role[];
	// First path segments the page owns, its tabs living on routes of their own
	match: string[];
	// Whether the page sits in the mobile bottom bar
	bottom?: boolean;
};

const operators: Role[] = ['operator', 'admin'];
const everyone: Role[] = ['inference', 'operator', 'admin'];

export const pages: NavPage[] = [
	{ id: 'overview', path: '/', label: 'Overview', icon: GaugeIcon, roles: operators, match: ['/'], bottom: true },
	{ id: 'chat', path: '/chat', label: 'Chat', icon: ChatsCircleIcon, roles: everyone, match: ['/chat'], bottom: true },
	{ id: 'models', path: '/models', label: 'Models', icon: CubeIcon, roles: operators, match: ['/models', '/discover'] },
	{ id: 'serve', path: '/serve', label: 'Serve', icon: PlayIcon, roles: operators, match: ['/serve', '/runtimes'], bottom: true },
	{ id: 'gateway', path: '/traffic', label: 'Gateway', icon: TreeStructureIcon, roles: operators, match: ['/gateway', '/traffic'], bottom: true },
	{ id: 'mesh', path: '/mesh', label: 'Mesh', icon: ShareNetworkIcon, roles: operators, match: ['/mesh'] },
	{ id: 'bots', path: '/bots', label: 'Bots', icon: RobotIcon, roles: operators, match: ['/bots'] },
	{ id: 'host', path: '/host', label: 'Host', icon: HardDrivesIcon, roles: operators, match: ['/host', '/tasks'] },
	{ id: 'settings', path: '/settings', label: 'Settings', icon: GearIcon, roles: everyone, match: ['/settings'] }
];

// The page a path belongs to, by its first segment
export function pageOf(pathname: string): NavPage | undefined {
	const first = '/' + (pathname.split('/')[1] ?? '');
	return pages.find((p) => p.match.includes(first));
}

export function allowed(role: Role): NavPage[] {
	return pages.filter((p) => p.roles.includes(role));
}

// Where a role lands after sign in
export function home(role: Role): string {
	return role === 'inference' ? '/chat' : '/';
}
