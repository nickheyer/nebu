<script lang="ts">
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import { NeoPill } from '#lib/neo/pill/index.js';
	import { bytes } from '#lib/format.js';
	import { live } from '#lib/live/events.svelte.js';
	import { FitVerdict, type FitRow, type InspectResponse, type MemoryPlan } from '#lib/proto/nebu/v1/estimate_pb.js';
	import { ModelKind } from '#lib/proto/nebu/v1/model_pb.js';
	import type { RuntimeStatus } from '#lib/proto/nebu/v1/runtime_pb.js';
	import Empty from '#lib/ui/Empty.svelte';
	import KeyRows from '#lib/ui/KeyRows.svelte';
	import List from '#lib/ui/List.svelte';
	import ListRow from '#lib/ui/ListRow.svelte';
	import MemoryBars from '#lib/ui/MemoryBars.svelte';
	import Pop from '#lib/ui/Pop.svelte';
	import Skeleton from '#lib/ui/Skeleton.svelte';
	import StatePill from '#lib/ui/StatePill.svelte';
	import Tip from '#lib/ui/Tip.svelte';
	import { poolBars } from '#lib/ui/memory.js';
	import type { Lamp } from '#lib/ui/status.js';
	import { contextLabel, placementLabel, placementPool, verdictLabel, verdictLamp } from '#lib/pages/models/models.js';
	import ScalesIcon from 'phosphor-svelte/lib/ScalesIcon';

	// Runtime rows of fit pills for one group, planned against free memory when free is set
	let {
		group,
		result,
		error,
		runtimes,
		free,
		launchLabel = 'Launch',
		onlaunch
	}: {
		group: string;
		result: InspectResponse | undefined;
		error: string;
		runtimes: RuntimeStatus[];
		free: boolean;
		launchLabel?: string;
		onlaunch: (runtimeId: string, context: number) => void;
	} = $props();

	const rows = $derived((result?.rows ?? []).filter((r) => r.group === group));
	const descriptor = $derived(result?.descriptors.find((d) => d.group === group));
	// Warnings of planning and header reads, the resolve warnings showing with the files instead
	const warnings = $derived((result?.warnings ?? []).filter((w) => !(result?.model?.warnings ?? []).includes(w)));
	const byRuntime = $derived.by(() => {
		const map = new Map<string, FitRow[]>();
		for (const r of rows) map.set(r.runtimeId, [...(map.get(r.runtimeId) ?? []), r]);
		return [...map.entries()].map(([id, list]) => ({ id, name: runtimeName(id), list: [...list].sort((a, b) => (a.context || Infinity) - (b.context || Infinity)) }));
	});
	// Runtimes taking this format and kind whose host requirements are unmet, which the daemon leaves out of the plans
	const blocked = $derived.by(() => {
		const d = descriptor;
		if (!d) return [];
		const kind = d.kind === ModelKind.UNSPECIFIED ? ModelKind.LANGUAGE : d.kind;
		return runtimes.filter((r) => r.runtime && !r.compatible && r.runtime.kind === kind && r.runtime.formats.includes(d.formatId) && !byRuntime.some((b) => b.id === r.runtime?.id));
	});
	const poolLabels = $derived(new Map(poolBars(live.host, [], []).map((b) => [b.id, b.label])));

	function runtimeName(id: string): string {
		const rt = runtimes.find((x) => x.runtime?.id === id)?.runtime;
		return rt?.name || id;
	}

	function planOf(row: FitRow): MemoryPlan | undefined {
		return free ? row.free : row.plan;
	}

	// The best verdict a runtime reaches at any context, lighting its row
	function bestLamp(list: FitRow[]): Lamp {
		const verdicts = list.map((r) => planOf(r)?.verdict);
		if (verdicts.includes(FitVerdict.FITS)) return 'success';
		if (verdicts.includes(FitVerdict.PARTIAL)) return 'warning';
		if (verdicts.includes(FitVerdict.NO)) return 'error';
		return 'muted';
	}

	// The context a row plans, the largest fitting one read from the plan when the row asked for it
	function contextOf(row: FitRow): number {
		return row.context || Number(planOf(row)?.params['n_ctx'] ?? 0);
	}

	function launch(close: () => void, runtimeId: string, row: FitRow) {
		close();
		onlaunch(runtimeId, contextOf(row));
	}

	function pillLabel(row: FitRow): string {
		const n = contextOf(row);
		return row.context ? contextLabel(n) : `max ${contextLabel(n)}`;
	}
</script>

{#snippet pills(name: string, id: string, list: FitRow[])}
	<div class="pills">
		{#each list as row, i (i)}
			{@const plan = planOf(row)}
			<Pop placement="bottom-start">
				{#snippet content(close)}
					{#if plan}
						<div class="planhead">
							<span class="plantitle">{name}, {pillLabel(row)}</span>
							<StatePill label={verdictLabel(plan.verdict)} lamp={verdictLamp(plan.verdict)} />
						</div>
						<MemoryBars profile={live.host} instances={live.instances.values()} slots={live.slots.values()} {plan} only={plan.pools.map((p) => p.poolId)} />
						<KeyRows
							dense
							rows={[
								...plan.placements.map((p) => ({ label: placementLabel(p), value: `${bytes(p.bytes)} in ${placementPool(p, plan, poolLabels)}` })),
								{ label: 'overhead', value: bytes(plan.overheadBytes) },
								{ label: 'detail', value: plan.detail, tip: plan.detail }
							]}
						/>
						<div class="launch">
							<NeoButton class="primary" elevation={2} onclick={() => launch(close, id, row)}>{launchLabel}</NeoButton>
						</div>
					{:else}
						<Empty text="No plan for this context" row />
					{/if}
				{/snippet}
				<Tip facts={plan ? [{ label: 'weights', value: bytes(plan.weightsBytes) }, { label: 'cache', value: bytes(plan.cacheBytes) }] : undefined} tag="span">
					<NeoPill size="small" tinted color={verdictLamp(plan?.verdict) === 'muted' ? undefined : verdictLamp(plan?.verdict)} label={pillLabel(row)} aria-label="{pillLabel(row)} {verdictLabel(plan?.verdict)}" />
				</Tip>
			</Pop>
		{/each}
	</div>
{/snippet}

{#if error}
	<Empty icon={ScalesIcon} text="Fit failed to plan" hint={error} />
{:else if !result}
	<Skeleton rows={3} primary="16ch" secondary={false} facts={0} action={false} menu={false} />
{:else if byRuntime.length === 0 && blocked.length === 0}
	<Empty icon={ScalesIcon} text="No compatible runtime" action="Runtimes" href="/runtimes" />
{:else}
	<List head={{ primary: 'Runtime', instrument: 'Context' }} compactBelow={440}>
		{#each byRuntime as rt (rt.id)}
			{#snippet instrument()}{@render pills(rt.name, rt.id, rt.list)}{/snippet}
			<ListRow lamp={bestLamp(rt.list)} primary={rt.name} primaryWidth="16ch" {instrument} />
		{/each}
		{#each blocked as r (r.runtime?.id)}
			{#snippet unmet()}
				<Tip text={r.unmet.join('\n')} tag="span"><StatePill label="unmet" /></Tip>
			{/snippet}
			<ListRow lamp="muted" shape="hollow" primary={r.runtime?.name || r.runtime?.id || ''} primaryWidth="16ch" instrument={unmet} />
		{/each}
	</List>
	{#if warnings.length}
		<List head={{ primary: 'Warnings' }}>
			{#each warnings as w (w)}
				<ListRow lamp="warning" primary={w} primaryKind="message" />
			{/each}
		</List>
	{/if}
{/if}

<style>
	.pills {
		display: flex;
		flex: 1 1 auto;
		flex-wrap: wrap;
		gap: var(--nebu-space-2);
		align-items: center;
		min-width: 0;
		padding: var(--nebu-space-1) 0;
	}
	.planhead {
		display: flex;
		gap: var(--nebu-space-2);
		align-items: center;
		justify-content: space-between;
	}
	.plantitle {
		font-weight: 500;
	}
	.launch {
		display: flex;
		justify-content: flex-end;
	}
</style>
