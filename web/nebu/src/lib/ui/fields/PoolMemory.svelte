<script lang="ts">
	import type { HostProfile } from '#lib/proto/nebu/v1/host_pb.js';
	import Slider from '#lib/ui/fields/Slider.svelte';
	import { poolBars } from '#lib/ui/memory.js';

	const gib = 2 ** 30;

	// One slider per memory pool for the bytes a slot may take, none at zero, all at its end
	let { value = $bindable({}), profile }: { value?: Record<string, number>; profile: HostProfile | undefined } = $props();

	const pools = $derived(poolBars(profile, [], []).map((b) => ({ id: b.id, label: [b.label, b.name].filter(Boolean).join(' '), total: b.total, top: Math.floor((b.total / gib) * 10) / 10 })));

	function read(id: string, total: number, top: number): number {
		const n = value[id] ?? total;
		return n >= total ? top : Math.min(top, Math.round((n / gib) * 10) / 10);
	}

	function write(id: string, total: number, top: number, n: number | undefined) {
		value = { ...value, [id]: n === undefined || n >= top ? total : Math.round(n * gib) };
	}
</script>

<div class="pools">
	{#each pools as p (p.id)}
		<Slider bind:value={() => read(p.id, p.total, p.top), (n) => write(p.id, p.total, p.top, n)} min={0} max={p.top} step={0.1} unit="GB" label={p.label} />
	{/each}
</div>

<style>
	.pools {
		display: flex;
		flex-direction: column;
		gap: var(--nebu-space-3);
		min-width: 0;
	}
</style>
