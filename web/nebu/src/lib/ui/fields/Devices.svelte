<script lang="ts">
	import { NeoCheckbox } from '#lib/neo/inputs/index.js';
	import { bytes } from '#lib/format.js';
	import { DeviceKind, type Device, type HostProfile } from '#lib/proto/nebu/v1/host_pb.js';
	import FieldLabel from '#lib/ui/fields/FieldLabel.svelte';
	import { deviceLabel } from '#lib/ui/memory.js';

	// One checkbox per device a slot may use, accelerators then the CPU, each with the memory of its pool
	let { value = $bindable([]), profile, label, info, disabled = false }: { value?: string[]; profile: HostProfile | undefined; label?: string; info?: string; disabled?: boolean } = $props();

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
	<FieldLabel {label} {info} />
	{#if devices.length === 0}
		<span class="secondary">None</span>
	{:else}
		<div class="devices">
			{#each devices as d, i (d.id)}
				{#snippet line()}
					<span class="line">
						<span class="devlabel">{deviceLabel(profile, d)}</span>
						<span class="name">{d.name || d.id}</span>
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
		animation: nebu-reveal 240ms var(--nebu-settle) backwards;
		animation-delay: calc(var(--i) * 40ms);
	}
	/* The checkbox row fills the field so the size can sit at the far end of every line */
	.device :global(.neo-checkbox-container),
	.device :global(.neo-checkbox-container .neo-label-container),
	.device :global(.neo-checkbox-container .neo-label) {
		flex: 1 1 auto;
		width: auto;
		min-width: 0;
		max-width: none;
	}
	.line {
		display: grid;
		grid-template-columns: 5ch minmax(0, 1fr) auto;
		gap: 0 var(--nebu-space-2);
		align-items: baseline;
		width: 100%;
		min-width: 0;
	}
	.devlabel {
		font-size: var(--nebu-text-secondary);
	}
	.name {
		min-width: 0;
		overflow-wrap: anywhere;
	}
	.size {
		font-size: var(--nebu-text-secondary);
		font-variant-numeric: tabular-nums;
		color: var(--nebu-text-muted);
		white-space: nowrap;
	}
</style>
