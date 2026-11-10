<script lang="ts">
	import { bytes } from '#lib/format.js';
	import { live } from '#lib/live/events.svelte.js';
	import type { Model } from '#lib/proto/nebu/v1/model_pb.js';
	import Empty from '#lib/ui/Empty.svelte';
	import RowAction from '#lib/ui/RowAction.svelte';
	import StatePill from '#lib/ui/StatePill.svelte';
	import Tip from '#lib/ui/Tip.svelte';
	import PullButton from '#lib/pages/discover/PullButton.svelte';
	import { weightGroups } from '#lib/pages/discover/discover.js';
	import ContextLegend from '#lib/pages/models/ContextLegend.svelte';
	import ContextPill from '#lib/pages/models/ContextPill.svelte';
	import { Contexts } from '#lib/pages/models/context.svelte.js';
	import { launchHref } from '#lib/pages/models/models.js';
	import CheckCircleIcon from 'phosphor-svelte/lib/CheckCircleIcon';
	import CubeIcon from 'phosphor-svelte/lib/CubeIcon';

	// Every weight group of the repository with the largest context it reaches here, its size and its action
	let { sourceId, repo, revision, model, nodeId = '' }: { sourceId: string; repo: string; revision: string; model: Model; nodeId?: string } = $props();

	const groups = $derived(weightGroups(model));
	const stored = $derived(new Set([...live.models.values()].filter((m) => m.sourceId === sourceId && m.repo === repo).map((m) => m.group)));
	const contexts = new Contexts();
</script>

{#if groups.length === 0}
	<Empty icon={CubeIcon} text="No weights" />
{:else}
	<div class="table">
		<div class="head listhead" aria-hidden="true">
			<span></span>
			<span>Weights</span>
			<span>Context</span>
			<span class="end">Size</span>
			<span></span>
		</div>
		{#each groups as g (g.name)}
			{@const name = g.name || g.formatId}
			<div class="row">
				<span class="cell icon"><CubeIcon size={18} /></span>
				<span class="cell name">
					<span class="text mono">{name}</span>
					{#if g.name}<StatePill label={g.formatId} />{/if}
					{#if stored.has(g.name)}<Tip text="Stored" tag="span"><span class="stored"><CheckCircleIcon size={16} weight="fill" /></span></Tip>{/if}
				</span>
				<span class="cell"><ContextPill {contexts} of={{ sourceId, repo, revision, group: g.name }} /></span>
				<span class="cell end size">{bytes(g.bytes)}</span>
				<span class="cell end act">
					{#if stored.has(g.name)}
						<RowAction label="Launch" href={launchHref({ sourceId, repo, group: g.name })} />
					{:else}
						<PullButton {sourceId} {repo} {revision} group={g.name} {name} {nodeId} />
					{/if}
				</span>
			</div>
		{/each}
	</div>
	<ContextLegend />
{/if}

<style>
	/* Icon, name, context, size and action share one set of columns, the name free to shrink */
	.table {
		display: grid;
		grid-template-columns: max-content minmax(0, 1fr) max-content max-content max-content;
		column-gap: var(--nebu-space-3);
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
		min-height: 40px;
		padding-top: 2px;
		padding-bottom: 2px;
		border-bottom: 1px solid var(--nebu-border);
		animation: nebu-reveal 220ms var(--nebu-settle) backwards;
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
	.text {
		font-size: 13px;
		overflow-wrap: anywhere;
	}
	.stored {
		display: inline-flex;
		flex: 0 0 auto;
		color: var(--neo-color-success);
	}
	.size {
		min-width: 7ch;
		font-size: var(--nebu-text-secondary);
		font-variant-numeric: tabular-nums;
		color: var(--nebu-text-muted);
		white-space: nowrap;
	}
	.act {
		min-width: 36px;
	}
</style>
