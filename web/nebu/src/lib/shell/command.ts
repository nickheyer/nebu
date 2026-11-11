import type { Component } from 'svelte';
import type { IconComponentProps } from 'phosphor-svelte';
import type { Live } from '#lib/live/events.svelte.js';
import type { Role } from '#lib/auth/session.svelte.js';
import { enumLabel } from '#lib/format.js';
import { InstanceStateSchema } from '#lib/proto/nebu/v1/instance_pb.js';
import { SlotStateSchema } from '#lib/proto/nebu/v1/slot_pb.js';
import { RouteStateSchema } from '#lib/proto/nebu/v1/gateway_pb.js';
import { TaskStateSchema } from '#lib/proto/nebu/v1/task_pb.js';
import { allowed, type NavPage } from '#lib/shell/nav.js';
import { modelPath } from '#lib/pages/models/models.js';
import CubeIcon from 'phosphor-svelte/lib/CubeIcon';
import EngineIcon from 'phosphor-svelte/lib/EngineIcon';
import FlowArrowIcon from 'phosphor-svelte/lib/FlowArrowIcon';
import LightningIcon from 'phosphor-svelte/lib/LightningIcon';
import ListChecksIcon from 'phosphor-svelte/lib/ListChecksIcon';
import MagnifyingGlassIcon from 'phosphor-svelte/lib/MagnifyingGlassIcon';
import PlayIcon from 'phosphor-svelte/lib/PlayIcon';
import RobotIcon from 'phosphor-svelte/lib/RobotIcon';
import ShareNetworkIcon from 'phosphor-svelte/lib/ShareNetworkIcon';
import SignpostIcon from 'phosphor-svelte/lib/SignpostIcon';
import SquaresFourIcon from 'phosphor-svelte/lib/SquaresFourIcon';
import StackIcon from 'phosphor-svelte/lib/StackIcon';
import UserIcon from 'phosphor-svelte/lib/UserIcon';
import TargetIcon from 'phosphor-svelte/lib/TargetIcon';
import WrenchIcon from 'phosphor-svelte/lib/WrenchIcon';

export type Entry = {
	id: string;
	group: string;
	label: string;
	detail?: string;
	icon: Component<IconComponentProps>;
	href?: string;
	run?: () => void;
};

const tabs: { page: string; label: string; href: string }[] = [
	{ page: 'host', label: 'Tasks', href: '/tasks' },
	{ page: 'host', label: 'Logs', href: '/logs' },
	{ page: 'models', label: 'Discover', href: '/discover' },
	{ page: 'serve', label: 'Runtimes', href: '/runtimes' },
	{ page: 'gateway', label: 'Traffic', href: '/traffic' },
	{ page: 'gateway', label: 'Routes', href: '/gateway/routes' },
	{ page: 'gateway', label: 'Pools', href: '/gateway/pools' },
	{ page: 'gateway', label: 'Objectives', href: '/gateway/objectives' },
	{ page: 'gateway', label: 'Schedulers', href: '/gateway/schedulers' },
	{ page: 'gateway', label: 'Imports', href: '/gateway/imports' },
	{ page: 'mesh', label: 'Nodes', href: '/mesh/nodes' },
	{ page: 'mesh', label: 'Links', href: '/mesh/links' },
	{ page: 'mesh', label: 'Formations', href: '/mesh/formations' },
	{ page: 'mesh', label: 'Nearby', href: '/mesh/nearby' },
	{ page: 'mesh', label: 'Federation', href: '/mesh/federation' },
	{ page: 'mesh', label: 'Tuning', href: '/mesh/tuning' },
	{ page: 'settings', label: 'Sources', href: '/settings/sources' },
	{ page: 'settings', label: 'Tools', href: '/settings/tools' },
	{ page: 'settings', label: 'Notifications', href: '/settings/notifications' },
	{ page: 'settings', label: 'Accounts', href: '/settings/accounts' }
];

function pageEntries(role: Role): Entry[] {
	const open = allowed(role);
	const byId = new Map(open.map((p) => [p.id, p]));
	const out: Entry[] = open.map((p: NavPage) => ({ id: `page:${p.id}`, group: 'Pages', label: p.label, icon: p.icon, href: p.path }));
	for (const t of tabs) {
		const p = byId.get(t.page);
		if (p) out.push({ id: `tab:${t.href}`, group: 'Pages', label: t.label, detail: p.label, icon: p.icon, href: t.href });
	}
	out.push({ id: 'page:profile', group: 'Pages', label: 'Profile', icon: UserIcon, href: '/profile' });
	return out;
}

// Every record the live mirror holds with the page that opens it
function recordEntries(live: Live): Entry[] {
	const out: Entry[] = [];
	for (const s of live.slots.values()) out.push({ id: `slot:${s.id}`, group: 'Instances', label: s.name, detail: enumLabel(SlotStateSchema, s.state), icon: PlayIcon, href: s.instanceId ? `/serve/${encodeURIComponent(s.instanceId)}` : `/serve?slot=${encodeURIComponent(s.id)}` });
	for (const i of live.instances.values()) if (i.slotId === '') out.push({ id: `instance:${i.id}`, group: 'Instances', label: i.name, detail: enumLabel(InstanceStateSchema, i.state), icon: PlayIcon, href: `/serve/${encodeURIComponent(i.id)}` });
	for (const r of live.routes.values()) out.push({ id: `route:${r.name}`, group: 'Routes', label: r.name, detail: enumLabel(RouteStateSchema, r.status?.state ?? 0), icon: SignpostIcon, href: `/gateway/routes/${encodeURIComponent(r.name)}` });
	for (const p of live.pools.values()) out.push({ id: `pool:${p.id}`, group: 'Pools', label: p.id, detail: p.spec?.model, icon: StackIcon, href: `/gateway/pools/${encodeURIComponent(p.id)}` });
	for (const o of live.objectives.values()) out.push({ id: `objective:${o.name}`, group: 'Objectives', label: o.name, icon: TargetIcon, href: `/gateway/objectives/${encodeURIComponent(o.name)}` });
	for (const c of live.schedulerConfigs.values()) out.push({ id: `scheduler:${c.name}`, group: 'Schedulers', label: c.name, icon: FlowArrowIcon, href: `/gateway/schedulers/${encodeURIComponent(c.name)}` });
	for (const m of live.models.values()) out.push({ id: `model:${m.sourceId}/${m.repo}/${m.group}`, group: 'Models', label: m.repo, detail: m.group || m.formatId, icon: CubeIcon, href: modelPath(m) });
	const runtimes = new Set([...live.installs.values()].map((i) => i.runtimeId));
	for (const id of runtimes) out.push({ id: `runtime:${id}`, group: 'Runtimes', label: id, icon: EngineIcon, href: `/runtimes/${encodeURIComponent(id)}` });
	for (const b of live.bots.values()) out.push({ id: `bot:${b.id}`, group: 'Bots', label: b.name, icon: RobotIcon, href: `/bots/${b.id}/status` });
	for (const n of live.nodes.values()) out.push({ id: `node:${n.id}`, group: 'Nodes', label: n.name, detail: n.address, icon: ShareNetworkIcon, href: `/mesh/nodes/${encodeURIComponent(n.id)}` });
	for (const f of live.formations.values()) out.push({ id: `formation:${f.id}`, group: 'Formations', label: f.name, icon: SquaresFourIcon, href: `/mesh/formations/${encodeURIComponent(f.id)}` });
	for (const t of live.toolServers.values()) out.push({ id: `tool:${t.ref || t.server?.name}`, group: 'Tool servers', label: t.server?.name || t.ref, icon: WrenchIcon, href: '/settings/tools' });
	const tasks = [...live.tasks.values()].sort((a, b) => Number(b.createdAt?.seconds ?? 0n) - Number(a.createdAt?.seconds ?? 0n)).slice(0, 20);
	for (const t of tasks) out.push({ id: `task:${t.id}`, group: 'Tasks', label: t.title, detail: enumLabel(TaskStateSchema, t.state), icon: ListChecksIcon, href: `/tasks/${t.id}` });
	return out;
}

// Pages and records the palette offers within the person's reach
export function entries(live: Live, role: Role, reads: boolean): Entry[] {
	return reads ? [...pageEntries(role), ...recordEntries(live)] : pageEntries(role);
}

// Shortcuts to page actions
export function actionEntries(reads: boolean, query: string, toggleTheme: () => void): Entry[] {
	const out: Entry[] = [];
	if (reads) {
		out.push({ id: 'act:launch', group: 'Actions', label: 'Launch', detail: 'Serve', icon: LightningIcon, href: '/serve/launch' });
		out.push({ id: 'act:slot', group: 'Actions', label: 'New slot', detail: 'Serve', icon: LightningIcon, href: '/serve?slot=new' });
		out.push({ id: 'act:route', group: 'Actions', label: 'New route', detail: 'Gateway', icon: LightningIcon, href: '/gateway/routes/new' });
		out.push({ id: 'act:pool', group: 'Actions', label: 'New pool', detail: 'Gateway', icon: LightningIcon, href: '/gateway/pools/new' });
		if (query.trim()) out.push({ id: 'act:discover', group: 'Actions', label: `Search sources for ${query.trim()}`, detail: 'Discover', icon: MagnifyingGlassIcon, href: `/discover?q=${encodeURIComponent(query.trim())}` });
	}
	out.push({ id: 'act:theme', group: 'Actions', label: 'Switch theme', detail: 'Appearance', icon: LightningIcon, run: toggleTheme });
	return out;
}

// How well typed words match a text, higher for word starts and prefixes
function score(text: string, words: string[]): number {
	const hay = text.toLowerCase();
	let total = 0;
	for (const w of words) {
		const at = hay.indexOf(w);
		if (at < 0) return 0;
		total += at === 0 ? 3 : /[\s/._-]/.test(hay[at - 1] ?? '') ? 2 : 1;
	}
	return total;
}

// Entries matching every typed word, best first
export function rank(list: Entry[], query: string, limit = 40): Entry[] {
	const words = query.toLowerCase().split(/\s+/).filter(Boolean);
	if (words.length === 0) return list.slice(0, limit);
	return list
		.map((e) => ({ e, s: score(e.label, words) * 2 + score(e.detail ?? '', words) + score(e.group, words) }))
		.filter((x) => x.s > 0)
		.sort((a, b) => b.s - a.s)
		.slice(0, limit)
		.map((x) => x.e);
}

