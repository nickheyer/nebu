<script lang="ts">
	import type { Snippet } from 'svelte';
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
		menu?: Snippet<[PoolBar]>;
		expand?: Snippet<[PoolBar]>;
		expanded?: Set<string>;
	} = $props();

	function chosen(b: PoolBar): boolean {
		return !only || b.devices.some((d) => only.includes(d.id)) || only.includes(b.id);
	}

	const bars = $derived(poolBars(profile, instances, slots, plan).filter(chosen));
	const kinds = $derived(bars.flatMap((b) => b.segments.filter((s) => s.bytes > 0).map((s) => s.kind)));
</script>

<div class="bars">
	{#each bars as bar, i (bar.id)}
		<div class="pool" style:--i={i}>
			<div class="ident">
				<span class="label">{bar.label}</span>
				<span class="name"><Str value={bar.name} kind="name" tip={bar.devices.map((d) => d.name).join('\n')} width="100%" /></span>
				{#if menu}<span class="menu">{@render menu(bar)}</span>{/if}
			</div>
			<div class="gauge">
				<div class="track">
					<Bar segments={bar.segments} total={bar.total} format={(n) => bytes(n)} />
				</div>
				<span class="used">{bytesIn(bar.used + bar.planned, bar.total)}<span class="of">{thin}/{thin}{bytesIn(bar.total, bar.total)}{thin}{byteUnit(bar.total)}</span></span>
			</div>
		</div>
		{#if expand && expanded.has(bar.id)}
			<div class="expanded">{@render expand(bar)}</div>
		{/if}
	{/each}
	{#if bars.length === 0}
		<p class="secondary">No memory found</p>
	{/if}
	{#if legend && bars.length}
		<MemoryLegend {kinds} />
	{/if}
</div>

<style>
	.bars {
		display: flex;
		flex-direction: column;
		gap: var(--nebu-space-2);
		min-width: 0;
		container-type: inline-size;
	}
	.pool {
		display: flex;
		gap: var(--nebu-space-3);
		align-items: center;
		min-height: 28px;
		animation: nebu-reveal 260ms var(--nebu-settle) backwards;
		animation-delay: calc(var(--i) * 40ms);
	}
	.ident {
		display: flex;
		flex: 0 0 30ch;
		gap: var(--nebu-space-3);
		align-items: center;
		min-width: 0;
	}
	.label {
		flex: 0 0 5ch;
		font-size: var(--nebu-text-secondary);
		font-weight: 500;
		letter-spacing: 0.02em;
	}
	.name {
		display: inline-flex;
		flex: 1 1 auto;
		min-width: 0;
		font-size: var(--nebu-text-secondary);
		color: var(--nebu-text-muted);
	}
	.menu {
		display: inline-flex;
		flex: 0 0 auto;
	}
	.gauge {
		display: flex;
		flex: 1 1 auto;
		gap: var(--nebu-space-3);
		align-items: center;
		min-width: 0;
	}
	.track {
		display: flex;
		flex: 1 1 auto;
		align-items: center;
		min-width: 4rem;
	}
	.used {
		flex: 0 0 auto;
		min-width: 14ch;
		font-size: var(--nebu-text-secondary);
		font-variant-numeric: tabular-nums;
		text-align: right;
		white-space: nowrap;
	}
	.of {
		color: var(--nebu-text-muted);
	}
	.secondary {
		font-size: var(--nebu-text-secondary);
		color: var(--nebu-text-muted);
	}
	.expanded {
		padding: 0 0 var(--nebu-space-2) 5ch;
	}
	@container (max-width: 639px) {
		.pool {
			flex-direction: column;
			gap: var(--nebu-space-1);
			align-items: stretch;
			padding-bottom: var(--nebu-space-1);
		}
		.ident {
			flex: 0 0 auto;
		}
		.menu {
			margin-left: auto;
		}
		.used {
			min-width: 0;
		}
	}
</style>
