<script lang="ts">
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import Collapse from '#lib/ui/Collapse.svelte';
	import { NeoPill } from '#lib/neo/pill/index.js';
	import { bytes, enumLabel } from '#lib/format.js';
	import { live } from '#lib/live/events.svelte.js';
	import { FitVerdict, FitVerdictSchema, type FitRow, type InspectResponse, type MemoryPlan } from '#lib/proto/nebu/v1/estimate_pb.js';
	import { ModelKind, TensorGroupKindSchema } from '#lib/proto/nebu/v1/model_pb.js';
	import type { RuntimeStatus } from '#lib/proto/nebu/v1/runtime_pb.js';
	import Empty from '#lib/ui/Empty.svelte';
	import KeyRows from '#lib/ui/KeyRows.svelte';
	import MemoryBars from '#lib/ui/MemoryBars.svelte';
	import Pop from '#lib/ui/Pop.svelte';
	import Skeleton from '#lib/ui/Skeleton.svelte';
	import StatePill from '#lib/ui/StatePill.svelte';
	import Str from '#lib/ui/Str.svelte';
	import Tip from '#lib/ui/Tip.svelte';
	import Switch from '#lib/ui/fields/Switch.svelte';
	import Stack from '#lib/ui/layout/Stack.svelte';
	import { poolBars } from '#lib/ui/memory.js';
	import type { Lamp } from '#lib/ui/status.js';
	import { contextLabel } from '#lib/pages/models/models.js';

	let {
		group,
		result,
		error,
		runtimes,
		launchLabel = 'Launch here',
		onlaunch
	}: {
		group: string;
		result: InspectResponse | undefined;
		error: string;
		runtimes: RuntimeStatus[];
		launchLabel?: string;
		onlaunch: (runtimeId: string, context: number) => void;
	} = $props();

	let free = $state(false);

	const rows = $derived((result?.rows ?? []).filter((r) => r.group === group));
	const descriptor = $derived(result?.descriptors.find((d) => d.group === group));
	// Warnings of planning and header reads, the resolve warnings showing on the Files tab instead
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

	function lampOf(v: FitVerdict | undefined): Lamp {
		return v === FitVerdict.FITS ? 'success' : v === FitVerdict.PARTIAL ? 'warning' : v === FitVerdict.NO ? 'error' : 'muted';
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

{#if error}
	<p class="error-text">{error}</p>
{:else if !result}
	<Skeleton rows={3} height={36} />
{:else}
	<Stack gap={12}>
		<Switch bind:checked={free} label="Against free memory" />
		{#if byRuntime.length === 0 && blocked.length === 0}
			<Empty text="No compatible runtime" action="Runtimes" href="/runtimes" />
		{/if}
		<div class="rows">
			{#each byRuntime as rt (rt.id)}
				<div class="runtime">
					<span class="rtname"><Str value={rt.name} width="100%" weight={500} /></span>
					<div class="pills">
						{#each rt.list as row, i (i)}
							{@const plan = planOf(row)}
							<Pop placement="bottom-start">
								{#snippet content(close)}
									{#if plan}
										<div class="planhead">
											<span class="plantitle">{rt.name}, {pillLabel(row)}</span>
											<StatePill label={enumLabel(FitVerdictSchema, plan.verdict)} lamp={lampOf(plan.verdict)} />
										</div>
										<MemoryBars profile={live.host} instances={live.instances.values()} slots={live.slots.values()} {plan} only={plan.pools.map((p) => p.poolId)} />
										<KeyRows
											dense
											rows={[
												...plan.placements.map((p) => ({ label: `${enumLabel(TensorGroupKindSchema, p.kind)}${p.count > 1 ? ` ${p.count}` : ''}`, value: `${bytes(p.bytes)} in ${p.poolId ? (poolLabels.get(p.poolId) ?? p.poolId) : 'disk'}` })),
												{ label: 'overhead', value: bytes(plan.overheadBytes) },
												{ label: 'detail', value: plan.detail, tip: plan.detail }
											]}
										/>
										<div class="launch">
											<NeoButton class="primary" elevation={2} onclick={() => launch(close, rt.id, row)}>{launchLabel}</NeoButton>
										</div>
									{:else}
										<p class="secondary">No plan</p>
									{/if}
								{/snippet}
								<Tip facts={plan ? [{ label: 'weights', value: bytes(plan.weightsBytes) }, { label: 'cache', value: bytes(plan.cacheBytes) }] : undefined} tag="span">
									<NeoPill size="small" tinted color={lampOf(plan?.verdict) === 'muted' ? undefined : lampOf(plan?.verdict)} label={pillLabel(row)} aria-label="{pillLabel(row)} {plan ? enumLabel(FitVerdictSchema, plan.verdict) : ''}" />
								</Tip>
							</Pop>
						{/each}
					</div>
				</div>
			{/each}
			{#each blocked as r (r.runtime?.id)}
				<div class="runtime">
					<span class="rtname"><Str value={r.runtime?.name || r.runtime?.id || ''} width="100%" color="muted" /></span>
					<Tip text={r.unmet.join('\n')} tag="span">
						<StatePill label="unmet" />
					</Tip>
				</div>
			{/each}
		</div>
		{#if warnings.length}
			<Collapse label="Warnings" count={warnings.length}>
				<Stack gap={4}>
					{#each warnings as w (w)}<Str value={w} kind="message" width="100%" />{/each}
				</Stack>
			</Collapse>
		{/if}
	</Stack>
{/if}

<style>
	.rows {
		display: flex;
		flex-direction: column;
	}
	.runtime {
		display: flex;
		gap: 12px;
		align-items: center;
		min-height: 44px;
		padding: 4px 0;
		border-bottom: 1px solid var(--nebu-border);
	}
	.rtname {
		display: inline-flex;
		flex: 0 0 14ch;
		min-width: 0;
	}
	.pills {
		display: flex;
		flex: 1 1 auto;
		flex-wrap: wrap;
		gap: 6px;
		min-width: 0;
	}
	.planhead {
		display: flex;
		gap: 8px;
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
