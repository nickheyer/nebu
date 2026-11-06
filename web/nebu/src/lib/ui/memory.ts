import { bytes } from '#lib/format.js';
import type { MemoryPlan } from '#lib/proto/nebu/v1/estimate_pb.js';
import { DeviceKind, PoolKind, type Device, type HostProfile, type MemoryPool } from '#lib/proto/nebu/v1/host_pb.js';
import { InstanceState, type Instance } from '#lib/proto/nebu/v1/instance_pb.js';
import { SlotState, type Slot } from '#lib/proto/nebu/v1/slot_pb.js';
import type { Segment } from '#lib/ui/Bar.svelte';

export type PoolBar = {
	id: string;
	kind: PoolKind;
	// Short label such as GPU0 or CPU
	label: string;
	// The device name, several joined when the pool is shared
	name: string;
	// The total in a short form such as 80G
	size: string;
	total: number;
	free: number;
	used: number;
	segments: Segment[];
	devices: Device[];
};

function terminal(s: InstanceState): boolean {
	return s === InstanceState.STOPPED || s === InstanceState.FAILED || s === InstanceState.UNSPECIFIED;
}

// A byte count as the spec writes device sizes, such as 80G or 512G
export function shortBytes(n: bigint | number): string {
	let v = Number(n);
	const units = ['B', 'K', 'M', 'G', 'T', 'P'];
	let i = 0;
	while (v >= 1000 && i < units.length - 1) {
		v /= 1024;
		i++;
	}
	return `${Math.round(v)}${units[i]}`;
}

function kindLabel(kind: DeviceKind): string {
	switch (kind) {
		case DeviceKind.GPU:
			return 'GPU';
		case DeviceKind.CPU:
			return 'CPU';
		case DeviceKind.ACCELERATOR:
			return 'ACC';
		default:
			return 'DEV';
	}
}

// A device label from its kind and the index its facts give, else its place among its kind
export function deviceLabel(profile: HostProfile | undefined, d: Device): string {
	const kind = kindLabel(d.kind);
	if (d.kind === DeviceKind.CPU) return kind;
	const index = d.facts['index'] ?? String((profile?.devices ?? []).filter((x) => x.kind === d.kind).indexOf(d));
	return `${kind}${index}`;
}

// One bar per memory pool with its segments, instances measured by process or planned, reservations by slot
export function poolBars(profile: HostProfile | undefined, instances: Iterable<Instance>, slots: Iterable<Slot>, plan?: MemoryPlan): PoolBar[] {
	if (!profile) return [];
	const live = [...instances].filter((i) => !terminal(i.state));
	const held = [...slots];
	return profile.pools.map((pool) => {
		const devices = profile.devices.filter((d) => d.poolId === pool.id).sort((a, b) => Number(b.kind === DeviceKind.CPU) - Number(a.kind === DeviceKind.CPU));
		const deviceIds = new Set(devices.map((d) => d.id));
		const total = Number(pool.totalBytes);
		const free = Number(pool.freeBytes);
		const used = Math.max(0, total - free);
		const byInstance = new Map<string, number>();
		let other = 0;
		for (const pu of profile.processes) {
			if (!deviceIds.has(pu.deviceId)) continue;
			if (pu.instanceId) byInstance.set(pu.instanceName || pu.instanceId, (byInstance.get(pu.instanceName || pu.instanceId) ?? 0) + Number(pu.bytes));
			else other += Number(pu.bytes);
		}
		for (const inst of live) {
			if (byInstance.has(inst.name)) continue;
			const planned = inst.plan?.pools.find((p) => p.poolId === pool.id);
			if (planned && planned.usedBytes > 0n) byInstance.set(inst.name, Number(planned.usedBytes));
		}
		let instancesBytes = 0;
		const segments: Segment[] = [];
		for (const [name, n] of byInstance) {
			instancesBytes += n;
			segments.push({ kind: 'instance', label: name, bytes: n });
		}
		const unaccounted = Math.max(0, used - instancesBytes - other);
		if (other + unaccounted > 0) segments.push({ kind: 'other', label: 'other', bytes: other + unaccounted });
		let reserved = 0;
		for (const slot of held) {
			const occupied = slot.instanceId !== '' && (slot.state === SlotState.READY || slot.state === SlotState.STARTING || slot.state === SlotState.SWAPPING || slot.state === SlotState.DRAINING);
			if (occupied || slot.memoryBytes === 0n || slot.deviceIds.length === 0) continue;
			const share = slot.deviceIds.filter((id) => deviceIds.has(id)).length;
			if (share === 0) continue;
			const n = (Number(slot.memoryBytes) * share) / slot.deviceIds.length;
			reserved += n;
			segments.push({ kind: 'reservation', label: `reserved ${slot.name}`, bytes: n });
		}
		if (plan) {
			const planned = plan.pools.find((p) => p.poolId === pool.id);
			if (planned && planned.usedBytes > 0n) segments.push({ kind: 'planned', label: 'planned', bytes: Number(planned.usedBytes) });
		}
		segments.push({ kind: 'free', label: 'free', bytes: Math.max(0, free - reserved) });
		const cpu = devices.find((d) => d.kind === DeviceKind.CPU);
		const label = pool.kind === PoolKind.DEVICE ? (devices[0] ? deviceLabel(profile, devices[0]) : pool.id) : pool.kind === PoolKind.UNIFIED ? 'Unified' : 'CPU';
		const name = pool.kind === PoolKind.DEVICE ? (devices[0]?.name ?? '') : devices.map((d) => d.name).filter(Boolean).join(', ') || (cpu?.name ?? '');
		return { id: pool.id, kind: pool.kind, label, name, size: shortBytes(pool.totalBytes), total, free, used, segments, devices };
	});
}

export function poolSummary(bar: PoolBar): string {
	return `${bytes(bar.used)} of ${bytes(bar.total)}`;
}

export function poolOf(profile: HostProfile | undefined, id: string): MemoryPool | undefined {
	return profile?.pools.find((p) => p.id === id);
}
