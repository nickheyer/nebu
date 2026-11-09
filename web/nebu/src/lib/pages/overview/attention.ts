import type { Live } from '#lib/live/events.svelte.js';
import { ConditionStatus, type Condition } from '#lib/proto/nebu/v1/meta_pb.js';
import { BotState } from '#lib/proto/nebu/v1/bot_pb.js';
import { InstanceState } from '#lib/proto/nebu/v1/instance_pb.js';
import { FormationState } from '#lib/proto/nebu/v1/mesh_pb.js';
import { SlotState } from '#lib/proto/nebu/v1/slot_pb.js';
import { TaskState } from '#lib/proto/nebu/v1/task_pb.js';
import { ToolServerState } from '#lib/proto/nebu/v1/tools_pb.js';
import { date, daysUntil } from '#lib/format.js';
import { reasonWords, type Lamp } from '#lib/ui/status.js';

export type Attention = { id: string; lamp: Lamp; title: string; reason: string; detail?: string; href: string };

function falseOnes(conditions: Condition[] | undefined): Condition[] {
	return (conditions ?? []).filter((c) => c.status === ConditionStatus.FALSE);
}

// Everything on the machine that needs a person, failures before warnings
export function attention(live: Live, now = Date.now()): Attention[] {
	const out: Attention[] = [];
	const add = (id: string, lamp: Lamp, title: string, reason: string, href: string) => out.push({ id, lamp, title, reason, href });
	// One row per resource with a false condition, its first reason in words and every one in the tooltip
	const conditions = (prefix: string, title: string, list: Condition[] | undefined, href: string) => {
		const off = falseOnes(list);
		const first = off[0];
		if (!first) return;
		const detail = off.map((c) => `${reasonWords(c.type)} ${c.message || reasonWords(c.reason)}`.trim()).join('\n');
		out.push({ id: `${prefix}:conditions`, lamp: 'warning', title, reason: reasonWords(first.reason) || first.message, detail, href });
	};
	for (const i of live.instances.values()) {
		const href = `/serve/${i.id}`;
		if (i.state === InstanceState.FAILED) add(`instance:${i.id}`, 'error', i.name, i.triage[0]?.hint || i.triage[0]?.summary || i.error || 'failed', href);
		conditions(`instance:${i.id}`, i.name, i.conditions, href);
	}
	for (const s of live.slots.values()) {
		const href = `/serve/${s.instanceId || ''}`;
		if (s.state === SlotState.FAILED && !live.instances.has(s.instanceId)) add(`slot:${s.id}`, 'error', s.name, s.error || 'failed', '/serve');
		conditions(`slot:${s.id}`, s.name, s.conditions, s.instanceId ? href : '/serve');
	}
	for (const r of live.routes.values()) conditions(`route:${r.name}`, r.name, r.status?.conditions, `/gateway/routes/${encodeURIComponent(r.name)}`);
	for (const p of live.pools.values()) {
		const href = `/gateway/pools/${encodeURIComponent(p.id)}`;
		if ((p.status?.readyEndpoints ?? 0) === 0 && falseOnes(p.status?.conditions).length === 0) add(`pool:${p.id}:ready`, 'warning', p.id, 'no ready endpoints', href);
		conditions(`pool:${p.id}`, p.id, p.status?.conditions, href);
	}
	for (const o of live.objectives.values()) conditions(`objective:${o.name}`, o.name, o.status?.conditions, `/gateway/objectives/${encodeURIComponent(o.name)}`);
	for (const c of live.schedulerConfigs.values()) conditions(`scheduler:${c.name}`, c.name, c.status?.conditions, `/gateway/schedulers/${encodeURIComponent(c.name)}`);
	for (const i of live.installs.values()) conditions(`install:${i.id}`, `${i.runtimeId} ${i.version}`.trim(), i.status?.conditions, `/runtimes/${encodeURIComponent(i.runtimeId)}`);
	for (const n of live.nodes.values()) conditions(`node:${n.id}`, n.name, n.conditions, `/mesh/nodes/${encodeURIComponent(n.id)}`);
	for (const f of live.formations.values()) {
		const href = `/mesh/formations/${encodeURIComponent(f.id)}`;
		if (f.state === FormationState.FAILED) add(`formation:${f.id}`, 'error', f.name, f.error || 'failed', href);
		conditions(`formation:${f.id}`, f.name, f.conditions, href);
	}
	const store = live.store;
	if (store && store.maxBytes > 0n && store.blobBytes + store.partialBytes + store.cacheBytes > store.maxBytes) add('store', 'warning', 'store', 'over its limit', '/models');
	const days = daysUntil(live.mesh?.mesh?.certificateExpiresAt, now);
	if (days !== undefined && days < 30) add('certificate', days < 7 ? 'error' : 'warning', 'certificate', days < 0 ? 'expired' : `expires in ${days} days`, '/mesh/nodes');
	for (const t of live.toolServers.values()) {
		if (t.state === ToolServerState.FAILED) add(`tool:${t.ref || t.server?.name}`, 'error', t.server?.name ?? '', t.error || 'failed', '/settings/tools');
	}
	for (const b of live.bots.values()) {
		if (b.state === BotState.FAILED || b.state === BotState.DEGRADED) add(`bot:${b.id}`, b.state === BotState.FAILED ? 'error' : 'warning', b.name, b.error || 'degraded', `/bots/${b.id}/status`);
	}
	for (const t of live.tasks.values()) {
		const finished = date(t.finishedAt)?.getTime() ?? 0;
		if (t.state === TaskState.FAILED && now - finished < 3_600_000) add(`task:${t.id}`, 'error', t.title, t.error || 'failed', `/tasks/${t.id}`);
	}
	return out.sort((a, b) => Number(b.lamp === 'error') - Number(a.lamp === 'error'));
}
