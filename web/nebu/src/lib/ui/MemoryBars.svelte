<script lang="ts">
	import type { Snippet } from 'svelte';
	import type { MemoryPlan } from '#lib/proto/nebu/v1/estimate_pb.js';
	import type { HostProfile } from '#lib/proto/nebu/v1/host_pb.js';
	import type { Instance } from '#lib/proto/nebu/v1/instance_pb.js';
	import type { Slot } from '#lib/proto/nebu/v1/slot_pb.js';
	import { bytes, bytesIn, byteUnit } from '#lib/format.js';
	import { viewport } from '#lib/state/viewport.svelte.js';
	import Bar from '#lib/ui/Bar.svelte';
	import MemoryLegend from '#lib/ui/MemoryLegend.svelte';
	import Tip from '#lib/ui/Tip.svelte';
	import { poolBars, type PoolBar } from '#lib/ui/memory.js';

	let {
		profile,
		instances = [],
		slots = [],
		plan,
		legend = true,
		only,
		menu,
		expand,
		expanded = new Set<string>()
	}: {
		profile: HostProfile | undefined;
		instances?: Iterable<Instance>;
		slots?: Iterable<Slot>;
		plan?: MemoryPlan;
		legend?: boolean;
		only?: string[];
		// Trailing controls for one bar, such as an overflow menu
		menu?: Snippet<[PoolBar]>;
		// A section under the bars whose ids are in expanded
		expand?: Snippet<[PoolBar]>;
		expanded?: Set<string>;
	} = $props();

	const bars = $derived(poolBars(profile, instances, slots, plan).filter((b) => !only || b.devices.some((d) => only.includes(d.id)) || only.includes(b.id)));
</script>

<div class="bars" class:mobile={viewport.mobile}>
	{#each bars as bar (bar.id)}
		<div class="pool">
			<div class="head">
				<span class="label">{bar.label}</span>
				<Tip text={bar.name} tag="span" class="name-tip">
					<span class="name ellipsis">{bar.name}</span>
				</Tip>
				<span class="size muted">{bar.size}</span>
			</div>
			<div class="track">
				<Bar segments={bar.segments} total={bar.total} format={(n) => bytes(n)} />
			</div>
			<span class="used">{bytesIn(bar.used, bar.total)} / {bytesIn(bar.total, bar.total)} {byteUnit(bar.total)}</span>
			{#if menu}<span class="menu">{@render menu(bar)}</span>{/if}
		</div>
		{#if expand && expanded.has(bar.id)}
			<div class="expanded">{@render expand(bar)}</div>
		{/if}
	{/each}
	{#if bars.length === 0}
		<span class="secondary">No memory pools probed</span>
	{/if}
	{#if legend && bars.length}
		<MemoryLegend planned={plan !== undefined} />
	{/if}
</div>

<style>
	.bars {
		display: flex;
		flex-direction: column;
		gap: 8px;
		min-width: 0;
	}
	.pool {
		display: flex;
		gap: 12px;
		align-items: center;
		min-height: 28px;
	}
	.head {
		display: flex;
		flex: 0 0 24ch;
		gap: 8px;
		align-items: center;
		min-width: 0;
	}
	.head :global(.name-tip) {
		flex: 1 1 auto;
		min-width: 0;
	}
	.label {
		flex: 0 0 5ch;
		font-weight: 500;
	}
	.name {
		min-width: 0;
		font-size: 13px;
	}
	.size {
		flex: 0 0 auto;
		font-size: 13px;
		font-variant-numeric: tabular-nums;
	}
	.track {
		display: flex;
		flex: 1 1 auto;
		min-width: 6rem;
	}
	.used {
		flex: 0 0 14ch;
		font-size: 13px;
		font-variant-numeric: tabular-nums;
		text-align: right;
		white-space: nowrap;
	}
	.menu {
		display: inline-flex;
		flex: 0 0 auto;
	}
	.expanded {
		padding: 0 0 8px 5ch;
	}
	.mobile .pool {
		flex-wrap: wrap;
		gap: 6px 12px;
	}
	.mobile .head {
		flex: 1 1 auto;
	}
	.mobile .track {
		flex: 1 1 100%;
	}
	.mobile .used {
		flex: 1 1 auto;
		text-align: left;
	}
</style>
