import type { Live } from '#lib/live/events.svelte.js';
import { ago, enumLabel, ms, num, pct } from '#lib/format.js';
import { RouteBalance, type GatewayStatus, type RewriteRule, type Route } from '#lib/proto/nebu/v1/gateway_pb.js';
import { AppProtocolSchema, FailureModeSchema, type Endpoint, type Pool } from '#lib/proto/nebu/v1/pool_pb.js';
import { ConditionStatus, type Condition } from '#lib/proto/nebu/v1/meta_pb.js';
import type { Fact } from '#lib/ui/Tip.svelte';
import type { DotShape, Lamp } from '#lib/ui/status.js';

export const gatewayTabs = ['routes', 'pools', 'objectives', 'schedulers', 'imports'] as const;
export type GatewayTab = (typeof gatewayTabs)[number];

export function isGatewayTab(s: string): s is GatewayTab {
	return (gatewayTabs as readonly string[]).includes(s);
}

// Whether a person authored the record rather than a slot, instance, formation or node deriving it
export function isUserOwned(ownerKind: string): boolean {
	return ownerKind === '' || ownerKind === 'user';
}

// The owner of a derived record as a label such as slot llama-70b, user for an authored one
export function ownerLabel(ownerKind: string, ownerId: string): string {
	return isUserOwned(ownerKind) ? 'user' : `${ownerKind} ${ownerId}`;
}

// A listener address with a wildcard host cut to its bare port
export function shortAddr(addr: string): string {
	const m = /^(?:0\.0\.0\.0|\[::\]|::|\*)?(:\d+)$/.exec(addr);
	return m ? m[1]! : addr;
}

// One target's share of its rule's traffic before balancing
export function targetShare(rule: RewriteRule, weight: number): string {
	const total = rule.targets.reduce((n, t) => n + t.weight, 0);
	return pct((weight / Math.max(1, total)) * 100);
}

// The target phrase of a route, the pool, n pools, or the node it forwards to
export function routeTarget(live: Live, r: Route): string {
	if (r.nodeId) return `forwarded ${live.nodes.get(r.nodeId)?.name ?? r.nodeId}`;
	const pools = r.status?.poolIds ?? [];
	if (pools.length === 1) return pools[0]!;
	if (pools.length > 1) return `${pools.length} pools`;
	return 'no pool';
}

// The one action a route's owner allows and where it leads
export function routeAction(live: Live, r: Route): { label: string; href: string } {
	switch (r.ownerKind) {
		case 'slot': {
			const instanceId = live.slots.get(r.ownerId)?.instanceId;
			return { label: 'Open slot', href: instanceId ? `/serve/${instanceId}` : '/serve' };
		}
		case 'instance':
			return { label: 'Open instance', href: `/serve/${r.ownerId}` };
		case 'formation':
			return { label: 'Open formation', href: `/mesh/formations/${encodeURIComponent(r.ownerId)}` };
		case 'node':
			return { label: 'Open node', href: `/mesh/nodes/${encodeURIComponent(r.ownerId)}` };
		default:
			return { label: 'Edit', href: `/gateway/routes/${encodeURIComponent(r.name)}` };
	}
}

// Owner and launch inventory of a route, one per line
export function routeTip(r: Route): string {
	const inv = r.status?.inventory;
	return [
		`owner ${ownerLabel(r.ownerKind, r.ownerId)}`,
		`context ${inv?.contextLength ? num(inv.contextLength) : 'unknown'}`,
		`quantization ${inv?.quantization || 'unknown'}`,
		`accelerator ${inv?.accelerator || 'unknown'}`,
		`modalities ${inv?.modalities.join(', ') || 'unknown'}`
	].join('\n');
}

export function balanceWord(b: RouteBalance | undefined): string {
	switch (b) {
		case RouteBalance.READY_ENDPOINTS:
			return 'ready endpoints';
		case RouteBalance.FREE_CAPACITY:
			return 'free capacity';
		default:
			return 'weights';
	}
}

export function poolLamp(p: Pool | undefined): Lamp {
	const ready = p?.status?.readyEndpoints ?? 0;
	if (ready > 0) return 'success';
	if (p?.status?.conditions.some((c) => c.status === ConditionStatus.FALSE)) return 'error';
	return (p?.status?.totalEndpoints ?? 0) > 0 ? 'warning' : 'muted';
}

export function poolShape(p: Pool | undefined): DotShape {
	if ((p?.status?.readyEndpoints ?? 0) > 0) return 'filled';
	return (p?.status?.totalEndpoints ?? 0) > 0 ? 'half' : 'hollow';
}

// Ready endpoints over all of them, or the pool's absence when a rule names one the gateway lacks
export function poolWord(p: Pool | undefined): string {
	if (!p) return 'missing';
	return `${p.status?.readyEndpoints ?? 0} / ${p.status?.totalEndpoints ?? 0} ready`;
}

// Runtime, accelerator, owner and how the gateway speaks to the pool, one per line
export function poolTip(p: Pool): string {
	return [
		`runtime ${p.spec?.runtimeId || 'any'}`,
		`accelerator ${p.spec?.accelerator || 'any'}`,
		`owner ${ownerLabel(p.ownerKind, p.ownerId)}`,
		`export ${exported(p) ? 'yes' : 'no'}`,
		`protocol ${enumLabel(AppProtocolSchema, p.spec?.appProtocol ?? 0)}`,
		`failure mode ${enumLabel(FailureModeSchema, p.spec?.failureMode ?? 0)}`,
		`scheduler config ${p.spec?.schedulerConfig || 'built in'}`
	].join('\n');
}

// Whether members of the mesh and federated meshes are offered the pool
export function exported(p: Pool): boolean {
	return p.spec?.export === true;
}

// Placement, load and predictions of one endpoint, one per line
export function endpointTip(live: Live, e: Endpoint, now: number): string {
	return [
		`node ${e.nodeId ? (live.nodes.get(e.nodeId)?.name ?? e.nodeId) : 'local'}`,
		`instance ${e.instanceId ? (live.instances.get(e.instanceId)?.name ?? e.instanceId) : 'static'}`,
		`role ${e.role || 'any'}, rank ${e.rank}`,
		`assumed ${num(e.assumedRequests)}`,
		`adapters ${e.metrics?.runningAdapters.join(', ') || 'none'}`,
		`predicted TTFT ${ms(e.predictedTtftMs)}, TPOT ${ms(e.predictedTpotMs)}`,
		`metrics ${e.metrics?.updatedAt ? ago(e.metrics.updatedAt, now) : 'never scraped'}`
	].join('\n');
}

// The lamp a set of conditions lights, red on any false, green when all hold
export function conditionsLamp(conditions: Condition[] | undefined): Lamp {
	if (!conditions?.length) return 'muted';
	if (conditions.some((c) => c.status === ConditionStatus.FALSE)) return 'error';
	return conditions.every((c) => c.status === ConditionStatus.TRUE) ? 'success' : 'warning';
}

export function priorityBand(p: number): string {
	if (p > 0) return 'sheds last';
	if (p < 0) return 'sheds first';
	return 'default';
}

const compactFormat = new Intl.NumberFormat(undefined, { notation: 'compact', maximumFractionDigits: 1 });

// A large count in a short form such as 1.2M
export function compact(n: bigint | number | undefined): string {
	return compactFormat.format(Number(n ?? 0));
}

// Listeners with their shared marks, the metrics address and the default policy
export function statusFacts(s: GatewayStatus): Fact[] {
	const p = s.policy;
	return [
		...s.listeners.map((l, i) => ({ label: s.listeners.length > 1 ? `listener ${i + 1}` : 'listener', value: `${l.addr}${l.shared ? ' shared' : ''}` })),
		{ label: 'metrics', value: s.metricsAddr || 'off' },
		{ label: 'in flight', value: p?.maxInFlight ? num(p.maxInFlight) : 'unlimited' },
		{ label: 'rate', value: p?.requestsPerSecond ? `${p.requestsPerSecond} req/s, burst ${p.burst ? num(p.burst) : 'auto'}` : 'unlimited' },
		{ label: 'timeouts', value: `request ${p?.requestTimeoutMs ? ms(p.requestTimeoutMs) : 'none'}, upstream ${p?.upstreamTimeoutMs ? ms(p.upstreamTimeoutMs) : 'none'}` }
	];
}

export const schedulerSkeleton = `# A scheduler configuration the gateway runs pool requests through
# plugins:
#   - type: queue-scorer
#   - type: kv-cache-utilization-scorer
#   - type: prefix-cache-scorer
# schedulingProfiles:
#   - name: default
#     plugins:
#       - pluginRef: queue-scorer
#         weight: 2
#       - pluginRef: kv-cache-utilization-scorer
#         weight: 1
plugins: []
schedulingProfiles: []
`;
