<script lang="ts">
	import { NeoPill } from '#lib/neo/pill/index.js';
	import { reason, rpc } from '#lib/api/transport.js';
	import { bytes, plural } from '#lib/format.js';
	import { live } from '#lib/live/events.svelte.js';
	import { FitVerdict, type FitRow, type InspectResponse } from '#lib/proto/nebu/v1/estimate_pb.js';
	import type { Model } from '#lib/proto/nebu/v1/model_pb.js';
	import Bone from '#lib/ui/Bone.svelte';
	import Dot from '#lib/ui/Dot.svelte';
	import Empty from '#lib/ui/Empty.svelte';
	import Pop from '#lib/ui/Pop.svelte';
	import RowAction from '#lib/ui/RowAction.svelte';
	import StatePill from '#lib/ui/StatePill.svelte';
	import Tip from '#lib/ui/Tip.svelte';
	import type { Lamp } from '#lib/ui/status.js';
	import PullPop from '#lib/pages/discover/PullPop.svelte';
	import { weightGroups } from '#lib/pages/discover/discover.js';
	import FitPop from '#lib/pages/models/FitPop.svelte';
	import { contextLabel, launchHref } from '#lib/pages/models/models.js';
	import CubeIcon from 'phosphor-svelte/lib/CubeIcon';

	// Every weight group of the repository with its size, the largest context it fits at in the Fit column and the action that stores or launches it
	let {
		sourceId,
		repo,
		revision,
		model,
		free = $bindable(false),
		nodeId = ''
	}: { sourceId: string; repo: string; revision: string; model: Model; free?: boolean; nodeId?: string } = $props();

	type Fit = { result?: InspectResponse; error?: string };
	type Summary = { lamp: Lamp; label: string; tip: string; rows: FitRow[] };

	const groups = $derived(weightGroups(model));
	const stored = $derived(new Set([...live.models.values()].filter((m) => m.sourceId === sourceId && m.repo === repo).map((m) => m.group)));
	let fits = $state<Record<string, Fit>>({});
	let fitKey = '';

	// One inspect per group, three in flight at a time in list order, so rows fill as headers are read
	$effect(() => {
		const names = groups.map((g) => g.name);
		const key = [sourceId, repo, revision, ...names].join('\n');
		if (key === fitKey) return;
		fitKey = key;
		fits = {};
		const queue = [...names];
		let active = 0;
		const next = () => {
			while (active < 3 && queue.length) {
				const g = queue.shift()!;
				active++;
				rpc.estimate
					.inspect({ sourceId, repo, revision, groups: [g] })
					.then((res) => {
						if (fitKey === key) fits[g] = { result: res };
					})
					.catch((err) => {
						if (fitKey === key) fits[g] = { error: reason(err) };
					})
					.finally(() => {
						active--;
						if (fitKey === key) next();
					});
			}
		};
		next();
	});

	function rank(row: FitRow): number {
		const v = (free ? row.free : row.plan)?.verdict;
		return v === FitVerdict.FITS ? 3 : v === FitVerdict.PARTIAL ? 2 : v === FitVerdict.NO ? 1 : 0;
	}

	// The largest context the group fits at on this host, one plan kept per context since every runtime prices the same weights alike
	function summarize(group: string): Summary | undefined {
		const byContext = new Map<number, FitRow>();
		for (const r of (fits[group]?.result?.rows ?? []).filter((r) => r.group === group)) {
			const prev = byContext.get(r.context);
			if (!prev || rank(r) > rank(prev)) byContext.set(r.context, r);
		}
		if (byContext.size === 0) return undefined;
		const rows = [...byContext.values()];
		const plans = rows.map((r) => ({ context: r.context || Number((free ? r.free : r.plan)?.params['n_ctx'] ?? 0), plan: free ? r.free : r.plan }));
		const fitting = plans.filter((p) => p.plan?.verdict === FitVerdict.FITS).map((p) => p.context);
		const partial = plans.filter((p) => p.plan?.verdict === FitVerdict.PARTIAL).map((p) => p.context);
		if (fitting.length) {
			const best = Math.max(...fitting);
			return { rows, lamp: 'success', label: contextLabel(best), tip: `Fits at ${contextLabel(best)} context` };
		}
		if (partial.length) {
			const best = Math.max(...partial);
			return { rows, lamp: 'warning', label: contextLabel(best), tip: `Partly offloaded at ${contextLabel(best)} context` };
		}
		if (plans.some((p) => p.plan)) return { rows, lamp: 'error', label: 'no fit', tip: 'Does not fit at any context' };
		return { rows, lamp: 'muted', label: 'no plan', tip: 'No plan' };
	}

	// What the row's lamp says, filled once stored and lit by the best fit the group reaches
	function dotTip(summary: Summary | undefined, isStored: boolean): string {
		const lamp = summary?.lamp ?? 'muted';
		const fit = lamp === 'success' ? 'fits on this host' : lamp === 'warning' ? 'fits partly offloaded' : lamp === 'error' ? 'does not fit on this host' : 'not planned yet';
		return `${isStored ? 'Stored' : 'Not stored'}, ${fit}`;
	}
</script>

{#snippet pill(fit: Fit | undefined, summary: Summary | undefined, name: string)}
	{#if !fit}
		<Bone width="4rem" height="20px" />
	{:else if fit.error}
		<Tip text={fit.error} tag="span"><StatePill label="unplanned" lamp="warning" /></Tip>
	{:else if !summary}
		<StatePill label="no runtime" />
	{:else}
		<Pop placement="bottom-start" width="26rem">
			{#snippet content()}
				<FitPop {name} rows={summary.rows} bind:free />
			{/snippet}
			<Tip text={summary.tip} tag="span">
				<button class="pillbutton" type="button" aria-label="{summary.label}, {summary.tip}">
					<NeoPill size="small" tinted color={summary.lamp === 'muted' ? undefined : summary.lamp} label={summary.label} />
				</button>
			</Tip>
		</Pop>
	{/if}
{/snippet}

{#if groups.length === 0}
	<Empty icon={CubeIcon} text="No weights" />
{:else}
	<div class="wrap">
		<div class="table">
			<div class="head listhead" aria-hidden="true">
				<span></span>
				<span></span>
				<span>Weights</span>
				<span class="wide">Files</span>
				<span class="wide">Fit</span>
				<span class="end">Size</span>
				<span></span>
			</div>
			{#each groups as g (g.name)}
				{@const fit = fits[g.name]}
				{@const summary = summarize(g.name)}
				{@const isStored = stored.has(g.name)}
				{@const name = g.name || g.formatId}
				<div class="row">
					<span class="cell icon"><CubeIcon size={20} /></span>
					<span class="cell"><Tip text={dotTip(summary, isStored)} tag="span"><Dot lamp={summary?.lamp ?? 'muted'} shape={isStored ? 'filled' : 'hollow'} label={dotTip(summary, isStored)} /></Tip></span>
					<span class="cell name">
						<span class="text">{name}</span>
						<StatePill label={g.formatId} />
						{#if isStored}<StatePill label="Stored" lamp="success" />{/if}
					</span>
					<span class="cell meta wide">{plural(g.files, 'file')}</span>
					<span class="cell wide">{@render pill(fit, summary, name)}</span>
					<span class="cell end size">{bytes(g.bytes)}</span>
					<span class="cell end">
						{#if isStored}
							<RowAction label="Launch" href={launchHref({ sourceId, repo, group: g.name })} />
						{:else}
							<PullPop {sourceId} {repo} {revision} group={g.name} groupBytes={g.bytes} {nodeId} />
						{/if}
					</span>
					<span class="cell under narrow">
						{@render pill(fit, summary, name)}
						<span class="meta">{plural(g.files, 'file')}</span>
					</span>
				</div>
			{/each}
		</div>
	</div>
{/if}

<style>
	.wrap {
		min-width: 0;
		container-type: inline-size;
	}
	/* Icon, lamp, name, files, fit, size and action share one set of columns, the name free to shrink */
	.table {
		display: grid;
		grid-template-columns: max-content max-content minmax(0, 1fr) max-content max-content max-content max-content;
		column-gap: var(--nebu-space-4);
		min-width: 0;
	}
	.head,
	.row {
		display: grid;
		grid-template-columns: subgrid;
		grid-column: 1 / -1;
		align-items: center;
		padding: 0 var(--nebu-row-inset, var(--nebu-space-2));
	}
	.head {
		min-height: 34px;
		font-size: var(--nebu-text-caption);
		color: var(--nebu-text-muted);
		white-space: nowrap;
		background: var(--nebu-surface-sunken);
		border-top: 1px solid var(--nebu-border);
		border-bottom: 1px solid var(--nebu-border);
	}
	.row {
		min-height: 44px;
		padding-top: var(--nebu-space-1);
		padding-bottom: var(--nebu-space-1);
		border-bottom: 1px solid var(--nebu-border);
		animation: nebu-reveal 260ms var(--nebu-settle) backwards;
	}
	.cell {
		display: flex;
		align-items: center;
		min-width: 0;
	}
	.end {
		justify-content: flex-end;
		text-align: right;
	}
	.icon {
		color: var(--nebu-text-muted);
	}
	.name {
		gap: 6px;
	}
	.name .text {
		overflow-wrap: anywhere;
	}
	.meta {
		font-size: var(--nebu-text-secondary);
		color: var(--nebu-text-muted);
		white-space: nowrap;
	}
	/* The fit pill is the button that opens its plan */
	.pillbutton {
		display: inline-flex;
		padding: 0;
		font: inherit;
		color: inherit;
		background: none;
		border: 0;
		border-radius: 999px;
		cursor: pointer;
	}
	.pillbutton:hover {
		filter: brightness(1.15);
	}
	.pillbutton:focus-visible {
		outline: 1px solid var(--neo-color-primary);
		outline-offset: 2px;
	}
	.size {
		font-size: var(--nebu-text-secondary);
		font-variant-numeric: tabular-nums;
		color: var(--nebu-text-muted);
		white-space: nowrap;
	}
	.narrow {
		display: none;
	}
	/* A narrow panel keeps name, size and action on one line and sets the fit pill under the name */
	@container (max-width: 579px) {
		.table {
			grid-template-columns: max-content max-content minmax(0, 1fr) max-content max-content;
		}
		.wide {
			display: none;
		}
		.narrow {
			display: flex;
			grid-column: 3 / -1;
			flex-wrap: wrap;
			gap: var(--nebu-space-1) var(--nebu-space-2);
			align-items: center;
			padding-bottom: var(--nebu-space-1);
		}
	}
</style>
