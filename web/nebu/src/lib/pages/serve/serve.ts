import { InstanceState } from '#lib/proto/nebu/v1/instance_pb.js';
import type { Slot } from '#lib/proto/nebu/v1/slot_pb.js';
import type { Placement } from '#lib/proto/nebu/v1/estimate_pb.js';
import type { Policy } from '#lib/proto/nebu/v1/gateway_pb.js';
import type { Task } from '#lib/proto/nebu/v1/task_pb.js';
import { date, elapsed, ms, num } from '#lib/format.js';
import type { Live } from '#lib/live/events.svelte.js';
import type { KeyRow } from '#lib/ui/KeyRows.svelte';
import type { Install } from '#lib/proto/nebu/v1/runtime_pb.js';

// The burst an empty box resolves to, the gateway's own or the rate in effect rounded up
export function burstFallback(policy: Policy | undefined, gateway: Policy | undefined): number | undefined {
	return gateway?.burst || Math.ceil(policy?.requestsPerSecond || gateway?.requestsPerSecond || 0) || undefined;
}

// An install by its version, the name of its binary when it reports none
export function installLabel(i: Install): string {
	return i.version || (i.path.split('/').pop() ?? i.id);
}

// The model phrase a run request names, repository and group
export function modelPhrase(r: { repo: string; group: string } | undefined): string {
	if (!r) return '';
	return r.group ? `${r.repo} ${r.group}` : r.repo;
}

// The reference a path param names a stored group by, which a launch resolves to the file the param loads
export function storeRef(m: { sourceId: string; repo: string; group: string }): string {
	return `store://${m.sourceId}/${m.repo}#${m.group}`;
}

// A path param's value as a form shows it, a stored group by its repository and group and a file by its path
export function partLabel(value: string): string {
	const ref = /^store:\/\/[^/]+\/(.+)#(.*)$/.exec(value);
	return ref ? modelPhrase({ repo: ref[1]!, group: ref[2]! }) : value;
}

// The Serve panel holding a slot's settings, new for the create form
export function slotHref(id: string): string {
	return `/serve?slot=${encodeURIComponent(id)}`;
}

export function terminal(s: InstanceState): boolean {
	return s === InstanceState.STOPPED || s === InstanceState.FAILED;
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
		{ label: 'concurrent requests', value: p.maxInFlight ? num(p.maxInFlight) : '' },
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

// Every way a model key splits into source, repository and group, a repository of two words first since most are
export function keySplits(key: string): { sourceId: string; repo: string; group: string }[] {
	const [sourceId = '', ...rest] = key.split('/');
	const cuts = [2, ...rest.map((_, i) => i + 1).filter((n) => n !== 2)].filter((n) => n <= rest.length);
	return cuts.map((n) => ({ sourceId, repo: rest.slice(0, n).join('/'), group: rest.slice(n).join('/') }));
}

// The whole update a slot takes, since an update replaces every field it carries
export function slotUpdate(s: Slot, patch: { position?: number; placement?: Placement }) {
	return {
		id: s.id,
		name: '',
		description: s.description,
		deviceIds: s.deviceIds,
		memoryBytes: s.memoryBytes,
		poolBytes: s.poolBytes,
		runtimeId: s.runtimeId,
		installId: s.installId,
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
