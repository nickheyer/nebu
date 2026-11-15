<script lang="ts">
	import { bytes } from '#lib/format.js';
	import { live } from '#lib/live/events.svelte.js';
	import type { Model } from '#lib/proto/nebu/v1/model_pb.js';
	import Empty from '#lib/ui/Empty.svelte';
	import RowAction from '#lib/ui/RowAction.svelte';
	import Tip from '#lib/ui/Tip.svelte';
	import PullButton from '#lib/pages/discover/PullButton.svelte';
	import { weightGroups } from '#lib/pages/discover/discover.js';
	import ContextPill from '#lib/pages/models/ContextPill.svelte';
	import { Contexts } from '#lib/pages/models/context.svelte.js';
	import { launchHref } from '#lib/pages/models/models.js';
	import CheckCircleIcon from 'phosphor-svelte/lib/CheckCircleIcon';
	import CubeIcon from 'phosphor-svelte/lib/CubeIcon';

	// Every weight group of the repository as the file list draws a file, with the largest context it reaches here
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
			<span class="headname">Weights</span>
			<span class="context">Fits in, up to a context of</span>
			<span class="size">Size</span>
			<span class="act"></span>
		</div>
		{#each groups as g (g.name)}
			{@const name = g.name || g.formatId}
			{@const what = contexts.get({ sourceId, repo, revision, group: g.name })?.what}
			<div class="row">
				<span class="icon"><CubeIcon size={18} /></span>
				<span class="name">
					<span class="file"><span class="mono">{name}</span>{#if stored.has(g.name)}<Tip text="Stored" tag="span"><span class="stored" role="img" aria-label="Stored"><CheckCircleIcon size={16} weight="fill" /></span></Tip>{/if}</span>
					{#if what}<span class="what">{what}</span>{/if}
				</span>
				<span class="context"><span class="cap">Fits in</span><ContextPill {contexts} of={{ sourceId, repo, revision, group: g.name }} /></span>
				<span class="size">{bytes(g.bytes)}</span>
				<span class="act">
					{#if stored.has(g.name)}
						<RowAction label="Launch" href={launchHref({ sourceId, repo, group: g.name })} play />
					{:else}
						<PullButton {sourceId} {repo} {revision} group={g.name} {name} {nodeId} />
					{/if}
				</span>
			</div>
		{/each}
	</div>
{/if}

<style>
	/* One grid for the head and every row, so each column lines up down the table */
	.table {
		display: grid;
		grid-template-columns: max-content minmax(0, 1fr) max-content max-content max-content;
		column-gap: var(--nebu-space-3);
		min-width: 0;
		container-type: inline-size;
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
	.headname {
		grid-column: 1 / 3;
	}
	.row {
		min-height: 44px;
		padding-top: var(--nebu-space-1);
		padding-bottom: var(--nebu-space-1);
		border-bottom: 1px solid var(--nebu-border);
		animation: nebu-reveal 220ms var(--nebu-settle) backwards;
	}
	.icon {
		display: inline-flex;
		color: var(--nebu-text-muted);
	}
	/* A file name alone says little, so what the weights are sits under it once they are read */
	.name {
		display: flex;
		flex-direction: column;
		min-width: 0;
		font-size: 13px;
		overflow-wrap: anywhere;
	}
	.file {
		display: inline-flex;
		gap: 6px;
		align-items: center;
		min-width: 0;
	}
	.what {
		font-size: var(--nebu-text-caption);
		color: var(--nebu-text-muted);
	}
	.stored {
		display: inline-flex;
		flex: 0 0 auto;
		color: var(--neo-color-success);
	}
	.context {
		display: inline-flex;
		gap: 6px;
		align-items: center;
	}
	.cap {
		display: none;
		font-size: var(--nebu-text-caption);
		color: var(--nebu-text-muted);
	}
	.size {
		min-width: 7ch;
		font-size: var(--nebu-text-secondary);
		font-variant-numeric: tabular-nums;
		color: var(--nebu-text-muted);
		text-align: right;
		white-space: nowrap;
	}
	.head .size {
		font-size: inherit;
	}
	.act {
		display: inline-flex;
		justify-content: flex-end;
		min-width: 36px;
	}
	/* A narrow table gives the name its own line and sets the fit and size under it, the fit named in place of the head */
	@container (max-width: 34rem) {
		.table {
			grid-template-columns: max-content minmax(0, 1fr) max-content max-content;
		}
		.head {
			display: none;
		}
		.row {
			row-gap: 2px;
		}
		.name {
			grid-column: 2 / 4;
		}
		.act {
			grid-column: 4;
			grid-row: 1 / 3;
		}
		.context {
			grid-column: 2;
		}
		.size {
			grid-column: 3;
		}
		.cap {
			display: inline;
		}
	}
</style>
