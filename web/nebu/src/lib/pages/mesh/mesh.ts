import { FitVerdict, LinkClass, LinkClassSchema } from '#lib/proto/nebu/v1/estimate_pb.js';
import { DeviceKind } from '#lib/proto/nebu/v1/host_pb.js';
import { AdmissionSide, AdmissionState, NodeState, type Admission, type Candidate, type Formation, type Link, type NearbyNode, type Node, type Seat } from '#lib/proto/nebu/v1/mesh_pb.js';
import type { Live } from '#lib/live/events.svelte.js';
import { bandwidth, bytes, enumLabel, micros, ms, plural, thin, when } from '#lib/format.js';
import type { HeaderTab } from '#lib/shell/chrome.svelte.js';
import type { DotShape, Lamp } from '#lib/ui/status.js';

export const meshTabs = ['nodes', 'links', 'formations', 'nearby', 'federation', 'tuning'] as const;
export type MeshTab = (typeof meshTabs)[number];

const tabLabels: Record<MeshTab, string> = { nodes: 'Nodes', links: 'Links', formations: 'Formations', nearby: 'Nearby', federation: 'Federation', tuning: 'Tuning' };

export function isMeshTab(s: string): s is MeshTab {
	return (meshTabs as readonly string[]).includes(s);
}

export const meshHeaderTabs: HeaderTab[] = meshTabs.map((t) => ({ id: t, label: tabLabels[t], href: `/mesh/${t}` }));

// Join and export tokens live at most a month
export const tokenTtlMax = 2_678_400;

export function linkLamp(c: LinkClass): Lamp {
	switch (c) {
		case LinkClass.FABRIC:
		case LinkClass.FAST:
			return 'success';
		case LinkClass.LAN:
			return 'primary';
		case LinkClass.SLOW:
			return 'warning';
		default:
			return 'muted';
	}
}

export function linkLabel(c: LinkClass): string {
	return c === LinkClass.UNSPECIFIED ? '' : enumLabel(LinkClassSchema, c);
}

// The slowest link a node has measured to any peer
export function worstLink(n: Node): Link | undefined {
	let worst: Link | undefined;
	for (const l of n.links) if (!worst || l.class > worst.class) worst = l;
	return worst;
}

export function linkTip(l: Link): string {
	return [
		`p95 rtt ${micros(l.rttP95Us)}`,
		`aggregate ${bandwidth(l.aggregateBytesPerSecond)}`,
		l.interface ? `interface ${l.interface} ${l.interfaceBitsPerSecond ? bandwidth(Number(l.interfaceBitsPerSecond) / 8) : ''}`.trim() : '',
		l.rdmaDevice ? `rdma ${l.rdmaDevice}` : '',
		l.mtu ? `mtu ${l.mtu}` : '',
		`subnet ${l.subnet ? 'same' : 'routed'}`,
		l.measuredAt ? `measured ${when(l.measuredAt)}` : '',
		l.bandwidthHeld ? 'bandwidth held' : ''
	]
		.filter(Boolean)
		.join('\n');
}

export function peerName(live: Live, id: string): string {
	return live.nodes.get(id)?.name ?? id;
}

export function deviceCount(n: Node): string {
	const devices = n.profile?.devices ?? [];
	const gpus = devices.filter((d) => d.kind === DeviceKind.GPU).length;
	const others = devices.filter((d) => d.kind === DeviceKind.ACCELERATOR).length;
	if (gpus || others) return [gpus ? `${gpus}${thin}GPU` : '', others ? `${others}${thin}ACC` : ''].filter(Boolean).join(' ');
	return 'CPU only';
}

export function nodeShape(s: NodeState): DotShape {
	return s === NodeState.READY ? 'filled' : 'hollow';
}

export function nodeTip(n: Node): string {
	return [n.address ? `address ${n.address}` : '', `${n.os} ${n.arch}`.trim(), n.seenAt ? `seen ${when(n.seenAt)}` : ''].filter(Boolean).join('\n');
}

export function formationTip(f: Formation): string {
	return [`model ${f.repo}${f.group ? ` ${f.group}` : ''}`, `runtime ${f.runtimeId}`, `conductor ${f.conductorName || f.conductor}`, f.endpoint ? `endpoint ${f.endpoint}` : '', `created ${when(f.createdAt)}`].filter(Boolean).join('\n');
}

export function seatName(live: Live, s: Seat): string {
	return s.nodeName || live.nodes.get(s.nodeId)?.name || s.nodeId;
}

export function seatKey(s: Seat): string {
	return `${s.nodeId}:${s.role}:${s.rank}`;
}

// The layer range a seat holds, or every layer
export function seatLayers(s: Seat): string {
	return s.layerTo ? `layers ${s.layerFrom} to ${s.layerTo}` : 'every layer';
}

export function seatTip(s: Seat): string {
	return [s.deviceIds.length ? `devices ${s.deviceIds.map((d, i) => `${d} ${bytes(s.deviceBytes[i] ?? 0n)}`).join(', ')}` : '', s.transport ? `transport ${s.transport}` : '', s.address ? `address ${s.address}` : ''].filter(Boolean).join('\n');
}

export function verdictLamp(v: FitVerdict): Lamp {
	return v === FitVerdict.FITS ? 'success' : v === FitVerdict.PARTIAL ? 'warning' : 'error';
}

export function candidateTip(c: Candidate): string {
	return [`score ${c.score.toFixed(2)}`, `prefill ${ms(c.prefillSeconds * 1000)}`, `decode ${ms(c.decodeSecondsPerToken * 1000)} per token`, c.reason].filter(Boolean).join('\n');
}

export function speedup(n: number): string {
	return n ? `${n.toFixed(1)}${thin}x` : '';
}

export function admissionTip(a: Admission): string {
	return [a.by ? `by ${a.by}` : '', a.detail, `created ${when(a.createdAt)}`].filter(Boolean).join('\n');
}

export function settled(a: Admission): boolean {
	return a.state !== AdmissionState.PENDING;
}

export function sideWord(a: Admission): string {
	return a.side === AdmissionSide.MEMBER ? 'candidate' : 'member';
}

export function membership(n: NearbyNode): string {
	return n.member ? 'member' : n.meshName;
}

// Whether a nearby node belongs to no mesh at all
export function outside(n: NearbyNode): boolean {
	return !n.member && !n.meshHash;
}

export function members(n: number): string {
	return plural(n, 'member');
}

export function streamRate(bytesPerSecond: number): string {
	const v = bytesPerSecond / 1e9;
	return `${v >= 100 ? v.toFixed(0) : v.toFixed(1)}${thin}GB/s`;
}

export function flopsRate(flops: number): string {
	const v = flops / 1e12;
	return `${v >= 100 ? v.toFixed(0) : v.toFixed(1)}${thin}TFLOPS`;
}

export function planFormationHref(): string {
	return '/serve/launch?plan=1';
}
