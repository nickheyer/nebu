<script lang="ts">
	import { NeoCheckbox } from '#lib/neo/inputs/index.js';
	import { bytes } from '#lib/format.js';
	import { DeviceKind, type Device, type HostProfile } from '#lib/proto/nebu/v1/host_pb.js';
	import FieldLabel from '#lib/ui/fields/FieldLabel.svelte';
	import { deviceLabel } from '#lib/ui/memory.js';

	// One checkbox per device a slot may use, accelerators then the CPU, each with the memory of its pool
	let { value = $bindable([]), profile, label, disabled = false }: { value?: string[]; profile: HostProfile | undefined; label?: string; disabled?: boolean } = $props();

	const devices = $derived.by((): Device[] => {
		if (!profile) return [];
		return [...profile.devices].sort((a, b) => Number(a.kind === DeviceKind.CPU) - Number(b.kind === DeviceKind.CPU));
	});

	function poolBytes(d: Device): number {
		if (d.memoryTotalBytes > 0n) return Number(d.memoryTotalBytes);
		return Number(profile?.pools.find((p) => p.id === d.poolId)?.totalBytes ?? 0n);
	}

	function toggle(id: string, on: boolean) {
		const set = new Set(value);
		if (on) set.add(id);
		else set.delete(id);
		value = (profile?.devices ?? []).map((d) => d.id).filter((id) => set.has(id));
	}
</script>

<div class="field">
	<FieldLabel {label} />
	{#if devices.length === 0}
		<span class="secondary">—</span>
	{:else}
		<div class="devices">
			{#each devices as d, i (d.id)}
				{#snippet line()}
					<span class="line">
						<span class="devlabel">{deviceLabel(profile, d)}</span>
						<span class="name ellipsis">{d.name || d.id}</span>
						<span class="size">{bytes(poolBytes(d))}</span>
					</span>
				{/snippet}
				<div class="device" style:--i={i}>
					<NeoCheckbox checked={value.includes(d.id)} {disabled} label={line} elevation={-1} onchange={(e) => toggle(d.id, (e.currentTarget as HTMLInputElement).checked)} />
				</div>
			{/each}
		</div>
	{/if}
</div>

<style>
	.field {
		display: flex;
		flex-direction: column;
		gap: 4px;
		min-width: 0;
	}
	.devices {
		display: flex;
		flex-direction: column;
		gap: 2px;
		min-width: 0;
	}
	.device {
		display: flex;
		min-width: 0;
		animation: nebu-reveal 240ms var(--nebu-settle) both;
		animation-delay: calc(var(--i) * 40ms);
	}
	.line {
		display: inline-flex;
		gap: var(--nebu-space-2);
		align-items: baseline;
		min-width: 0;
		max-width: 100%;
	}
	.devlabel {
		flex: 0 0 5ch;
		font-size: var(--nebu-text-secondary);
	}
	.name {
		min-width: 0;
	}
	.size {
		flex: 0 0 auto;
		font-size: var(--nebu-text-secondary);
		font-variant-numeric: tabular-nums;
		color: var(--nebu-text-muted);
	}
</style>
