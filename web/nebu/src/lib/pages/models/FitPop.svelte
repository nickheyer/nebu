<script lang="ts">
	import { NeoButton, NeoButtonGroup } from '#lib/neo/buttons/index.js';
	import { bytes } from '#lib/format.js';
	import { live } from '#lib/live/events.svelte.js';
	import { FitVerdict, type FitRow, type MemoryPlan } from '#lib/proto/nebu/v1/estimate_pb.js';
	import Empty from '#lib/ui/Empty.svelte';
	import KeyRows from '#lib/ui/KeyRows.svelte';
	import MemoryBars from '#lib/ui/MemoryBars.svelte';
	import StatePill from '#lib/ui/StatePill.svelte';
	import Tip from '#lib/ui/Tip.svelte';
	import Switch from '#lib/ui/fields/Switch.svelte';
	import { poolBars } from '#lib/ui/memory.js';
	import { contextLabel, placementLabel, placementPool, verdictLabel, verdictLamp } from '#lib/pages/models/models.js';

	// A weight group's plan per context, the context chosen among them and the memory it is measured against
	let { name, rows, free = $bindable(false) }: { name: string; rows: FitRow[]; free?: boolean } = $props();

	const sorted = $derived([...rows].sort((a, b) => (a.context || Infinity) - (b.context || Infinity)));
	let picked = $state<number | undefined>(undefined);
	const poolLabels = $derived(new Map(poolBars(live.host, [], []).map((b) => [b.id, b.label])));

	function planOf(row: FitRow): MemoryPlan | undefined {
		return free ? row.free : row.plan;
	}

	// The context a row plans, the largest fitting one read from the plan when the row asked for it
	function contextOf(row: FitRow): number {
		return row.context || Number(planOf(row)?.params['n_ctx'] ?? 0);
	}

	function pillLabel(row: FitRow): string {
		const n = contextOf(row);
		return row.context ? contextLabel(n) : `max ${contextLabel(n)}`;
	}

	// The largest context that fits, else the largest partly offloaded, else the smallest asked
	const best = $derived.by(() => {
		const by = (v: FitVerdict) => sorted.map((r, i) => (planOf(r)?.verdict === v ? i : -1)).filter((i) => i >= 0).at(-1);
		return by(FitVerdict.FITS) ?? by(FitVerdict.PARTIAL) ?? 0;
	});
	const index = $derived(picked !== undefined && picked < sorted.length ? picked : best);
	const row = $derived(sorted[index]);
	const plan = $derived(row ? planOf(row) : undefined);
</script>

<div class="fit">
	<div class="head">
		<span class="name">{name}</span>
		<StatePill label={verdictLabel(plan?.verdict)} lamp={verdictLamp(plan?.verdict)} />
	</div>
	{#if sorted.length > 1}
		<NeoButtonGroup elevation={-1} role="radiogroup" aria-label="Context">
			{#each sorted as r, i (i)}
				{@const v = planOf(r)?.verdict}
				<Tip text={verdictLabel(v)} tag="span">
					<NeoButton role="radio" aria-checked={i === index} checked={i === index} toggle color={verdictLamp(v) === 'muted' ? undefined : verdictLamp(v)} onclick={() => (picked = i)}>{pillLabel(r)}</NeoButton>
				</Tip>
			{/each}
		</NeoButtonGroup>
	{:else if row}
		<span class="secondary">{pillLabel(row)} context</span>
	{/if}
	<Switch bind:checked={free} label="Against free memory" info="Plans against the memory left beside what runs now instead of each pool's total" />
	{#if plan}
		<MemoryBars profile={live.host} instances={live.instances.values()} slots={live.slots.values()} {plan} only={plan.pools.map((p) => p.poolId)} />
		<KeyRows
			dense
			rows={[
				...plan.placements.map((p) => ({ label: placementLabel(p), value: `${bytes(p.bytes)} in ${placementPool(p, plan, poolLabels)}` })),
				{ label: 'overhead', value: bytes(plan.overheadBytes) },
				{ label: 'detail', value: plan.detail, tip: plan.detail }
			]}
		/>
	{:else}
		<Empty text="No plan for this context" row />
	{/if}
</div>

<style>
	.fit {
		display: flex;
		flex-direction: column;
		gap: var(--nebu-space-3);
		min-width: 0;
	}
	.head {
		display: flex;
		gap: var(--nebu-space-2);
		align-items: center;
		justify-content: space-between;
	}
	.name {
		font-weight: 500;
	}
</style>
