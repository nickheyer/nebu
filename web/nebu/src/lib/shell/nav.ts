import type { Component } from 'svelte';
import type { IconComponentProps } from 'phosphor-svelte';
import ChatsCircleIcon from 'phosphor-svelte/lib/ChatsCircleIcon';
import CubeIcon from 'phosphor-svelte/lib/CubeIcon';
import EngineIcon from 'phosphor-svelte/lib/EngineIcon';
import GaugeIcon from 'phosphor-svelte/lib/GaugeIcon';
import GearIcon from 'phosphor-svelte/lib/GearIcon';
import HardDrivesIcon from 'phosphor-svelte/lib/HardDrivesIcon';
import ListChecksIcon from 'phosphor-svelte/lib/ListChecksIcon';
import MagnifyingGlassIcon from 'phosphor-svelte/lib/MagnifyingGlassIcon';
import PlayIcon from 'phosphor-svelte/lib/PlayIcon';
import PulseIcon from 'phosphor-svelte/lib/PulseIcon';
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
	// Whether the page sits in the mobile bottom bar
	bottom?: boolean;
};

const operators: Role[] = ['operator', 'admin'];
const everyone: Role[] = ['inference', 'operator', 'admin'];

export const pages: NavPage[] = [
	{ id: 'overview', path: '/', label: 'Overview', icon: GaugeIcon, roles: operators, bottom: true },
	{ id: 'chat', path: '/chat', label: 'Chat', icon: ChatsCircleIcon, roles: everyone, bottom: true },
	{ id: 'models', path: '/models', label: 'Models', icon: CubeIcon, roles: operators },
	{ id: 'discover', path: '/discover', label: 'Discover', icon: MagnifyingGlassIcon, roles: operators },
	{ id: 'runtimes', path: '/runtimes', label: 'Runtimes', icon: EngineIcon, roles: operators },
	{ id: 'serve', path: '/serve', label: 'Serve', icon: PlayIcon, roles: operators, bottom: true },
	{ id: 'gateway', path: '/gateway', label: 'Gateway', icon: TreeStructureIcon, roles: operators },
	{ id: 'traffic', path: '/traffic', label: 'Traffic', icon: PulseIcon, roles: operators, bottom: true },
	{ id: 'mesh', path: '/mesh', label: 'Mesh', icon: ShareNetworkIcon, roles: operators },
	{ id: 'bots', path: '/bots', label: 'Bots', icon: RobotIcon, roles: operators },
	{ id: 'tasks', path: '/tasks', label: 'Tasks', icon: ListChecksIcon, roles: operators },
	{ id: 'host', path: '/host', label: 'Host', icon: HardDrivesIcon, roles: operators },
	{ id: 'settings', path: '/settings', label: 'Settings', icon: GearIcon, roles: everyone }
];

// The page a path belongs to, by its first segment
export function pageOf(pathname: string): NavPage | undefined {
	const first = '/' + (pathname.split('/')[1] ?? '');
	return pages.find((p) => p.path === first);
}

export function allowed(role: Role): NavPage[] {
	return pages.filter((p) => p.roles.includes(role));
}

// Where a role lands after sign in
export function home(role: Role): string {
	return role === 'inference' ? '/chat' : '/';
}
