<script lang="ts">
	import type { HostProfile } from '#lib/proto/nebu/v1/host_pb.js';
	import Slider from '#lib/ui/fields/Slider.svelte';
	import { poolBars } from '#lib/ui/memory.js';

	const gib = 2 ** 30;

	// One slider per memory pool for the most a slot may take from it, none at zero and all of it at the rail's end
	let { value = $bindable({}), profile }: { value?: Record<string, number>; profile: HostProfile | undefined } = $props();

	// The rail ends on the pool's size as the memory bars write it, to a tenth of a gigabyte
	const pools = $derived(poolBars(profile, [], []).map((b) => ({ id: b.id, label: [b.label, b.name].filter(Boolean).join(' '), total: b.total, top: Math.round((b.total / gib) * 10) / 10 })));

	function read(id: string, total: number, top: number): number {
		const n = value[id] ?? total;
		return n >= total ? top : Math.min(Number((top - 0.1).toFixed(1)), Math.round((n / gib) * 10) / 10);
	}

	function write(id: string, total: number, top: number, n: number | undefined) {
		value = { ...value, [id]: n === undefined || n >= top ? total : Math.round(n * gib) };
	}
</script>

<div class="pools">
	{#each pools as p (p.id)}
		<Slider bind:value={() => read(p.id, p.total, p.top), (n) => write(p.id, p.total, p.top, n)} min={0} max={p.top} step={0.1} unit="GB" label={p.label} names={[{ value: 0, label: 'none' }, { value: p.top, label: `all ${p.top} GB` }]} />
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
