import type { Instance } from '#lib/proto/nebu/v1/instance_pb.js';
import { InstanceState } from '#lib/proto/nebu/v1/instance_pb.js';
import type { Slot } from '#lib/proto/nebu/v1/slot_pb.js';
import type { Placement } from '#lib/proto/nebu/v1/estimate_pb.js';
import type { Policy } from '#lib/proto/nebu/v1/gateway_pb.js';
import type { Task } from '#lib/proto/nebu/v1/task_pb.js';
import { date, elapsed, ms, num } from '#lib/format.js';
import type { Live } from '#lib/live/events.svelte.js';
import type { KeyRow } from '#lib/ui/KeyRows.svelte';

// The model phrase a run request names, repository and group
export function modelPhrase(r: { repo: string; group: string } | undefined): string {
	if (!r) return '';
	return r.group ? `${r.repo} ${r.group}` : r.repo;
}

// The Serve panel holding a slot's settings, new for the create form
export function slotHref(id: string): string {
	return `/serve?slot=${encodeURIComponent(id)}`;
}

export function terminal(s: InstanceState): boolean {
	return s === InstanceState.STOPPED || s === InstanceState.FAILED;
}

// The in flight count of the route a slot answers on
export function slotInFlight(live: Live, slot: Slot): number | undefined {
	return live.routes.get(slot.name)?.status?.inFlight;
}

export function instanceInFlight(live: Live, inst: Instance): number | undefined {
	return live.routes.get(inst.name)?.status?.inFlight;
}

// Percent of the slot's traffic the incoming occupant takes, the last target of a split route
export function canaryShare(live: Live, slot: Slot): number | undefined {
	const targets = live.routes.get(slot.name)?.spec?.rules[0]?.targets ?? [];
	if (targets.length < 2) return undefined;
	const total = targets.reduce((n, t) => n + t.weight, 0);
	return total > 0 ? Math.round((targets[targets.length - 1]!.weight / total) * 100) : undefined;
}

// One row per policy field a slot sets
export function policyRows(p: Policy | undefined): KeyRow[] {
	if (!p) return [];
	return [
		{ label: 'max in flight', value: p.maxInFlight ? num(p.maxInFlight) : '' },
		{
			label: 'requests per second',
			value: p.requestsPerSecond ? String(p.requestsPerSecond) : ''
		},
		{ label: 'burst', value: p.burst ? num(p.burst) : '' },
		{
			label: 'request timeout',
			value: p.requestTimeoutMs ? ms(p.requestTimeoutMs) : ''
		},
		{
			label: 'upstream timeout',
			value: p.upstreamTimeoutMs ? ms(p.upstreamTimeoutMs) : ''
		}
	].filter((r) => r.value !== '');
}

// Splits a model key of source, repository and group, the repository holding one slash of its own
export function splitKey(key: string): {
	sourceId: string;
	repo: string;
	group: string;
} {
	const parts = key.split('/');
	if (parts.length < 3)
		return {
			sourceId: parts[0] ?? '',
			repo: parts.slice(1).join('/'),
			group: ''
		};
	return {
		sourceId: parts[0]!,
		repo: parts.slice(1, 3).join('/'),
		group: parts.slice(3).join('/')
	};
}

// The whole update a slot takes, since an update replaces every field it carries
export function slotUpdate(s: Slot, patch: { position?: number; placement?: Placement }) {
	return {
		id: s.id,
		name: '',
		description: s.description,
		deviceIds: s.deviceIds,
		memoryBytes: s.memoryBytes,
		runtimeId: s.runtimeId,
		params: s.params,
		policy: s.policy,
		profile: s.profile,
		placement: s.placement,
		aliases: s.aliases,
		args: s.args,
		position: s.position,
		...patch
	};
}

// How long the swap task's current canary step has left, from the deadline the daemon reports
export function canaryTimeLeft(task: Task | undefined, now: number): string {
	const until = date(task?.progress?.until);
	return until ? elapsed(until.getTime() - now) : '';
}
