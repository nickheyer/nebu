<script lang="ts">
	import type { MemoryPlan } from '#lib/proto/nebu/v1/estimate_pb.js';
	import type { HostProfile } from '#lib/proto/nebu/v1/host_pb.js';
	import type { Instance } from '#lib/proto/nebu/v1/instance_pb.js';
	import type { Slot } from '#lib/proto/nebu/v1/slot_pb.js';
	import { bytes, bytesIn, byteUnit, thin } from '#lib/format.js';
	import Bar from '#lib/ui/Bar.svelte';
	import MemoryLegend from '#lib/ui/MemoryLegend.svelte';
	import Str from '#lib/ui/Str.svelte';
	import { poolBars, type PoolBar } from '#lib/ui/memory.js';

	// One row per memory pool with its label, devices, bar and used plus planned over total
	let {
		profile,
		instances = [],
		slots = [],
		plan,
		legend = true,
		only
	}: {
		profile: HostProfile | undefined;
		instances?: Iterable<Instance>;
		slots?: Iterable<Slot>;
		plan?: MemoryPlan;
		legend?: boolean;
		only?: string[];
	} = $props();

	function chosen(b: PoolBar): boolean {
		return !only || b.devices.some((d) => only.includes(d.id)) || only.includes(b.id);
	}

	const bars = $derived(poolBars(profile, instances, slots, plan).filter(chosen));
	const kinds = $derived(bars.flatMap((b) => b.segments.filter((s) => s.bytes > 0).map((s) => s.kind)));
</script>

<div class="frame">
	<div class="bars">
		{#each bars as bar, i (bar.id)}
			<div class="pool" style:--i={i}>
				<span class="tag">{bar.label}</span>
				<span class="name"><Str value={bar.name} kind="name" tip={bar.devices.map((d) => d.name).join('\n')} width="100%" /></span>
				<span class="track"><Bar segments={bar.segments} total={bar.total} format={(n) => bytes(n)} /></span>
				<span class="used">{bytesIn(bar.used + bar.planned, bar.total)}<span class="of">{thin}/{thin}{bytesIn(bar.total, bar.total)}{thin}{byteUnit(bar.total)}</span></span>
			</div>
		{/each}
	</div>
	{#if bars.length === 0}
		<p class="secondary">No memory found</p>
	{/if}
	{#if legend && bars.length}
		<MemoryLegend {kinds} />
	{/if}
</div>

<style>
	.frame {
		display: flex;
		flex-direction: column;
		gap: var(--nebu-space-2);
		min-width: 0;
		container-type: inline-size;
	}
	/* Labels, names, bars and readings each share a column, the names as wide as the longest up to a cap */
	.bars {
		display: grid;
		grid-template-columns: max-content fit-content(20rem) minmax(6rem, 1fr) max-content;
		gap: var(--nebu-space-2) var(--nebu-space-3);
		min-width: 0;
	}
	.pool {
		display: grid;
		grid-template-columns: subgrid;
		grid-column: 1 / -1;
		align-items: center;
		min-height: 28px;
		animation: nebu-reveal 260ms var(--nebu-settle) backwards;
		animation-delay: calc(var(--i) * 40ms);
	}
	.tag {
		font-size: var(--nebu-text-secondary);
		font-weight: 500;
		letter-spacing: 0.02em;
	}
	.name {
		display: inline-flex;
		min-width: 0;
		font-size: var(--nebu-text-secondary);
		color: var(--nebu-text-muted);
	}
	.track {
		display: flex;
		align-items: center;
		min-width: 0;
	}
	.used {
		font-size: var(--nebu-text-secondary);
		font-variant-numeric: tabular-nums;
		text-align: right;
		white-space: nowrap;
	}
	.of {
		color: var(--nebu-text-muted);
	}
	/* A narrow frame gives each bar its own line under the label, the name and the reading */
	@container (max-width: 479px) {
		.bars {
			grid-template-columns: max-content minmax(0, 1fr) max-content;
		}
		.track {
			grid-row: 2;
			grid-column: 1 / -1;
		}
		.used {
			grid-row: 1;
			grid-column: 3;
		}
	}
</style>
