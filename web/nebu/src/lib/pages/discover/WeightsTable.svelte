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
			<span class="context">Context</span>
			<span class="size">Size</span>
			<span class="act"></span>
		</div>
		{#each groups as g (g.name)}
			{@const name = g.name || g.formatId}
			<div class="row">
				<span class="icon"><CubeIcon size={18} /></span>
				<span class="name mono">{name}</span>
				{#if stored.has(g.name)}<Tip text="Stored" tag="span"><span class="stored"><CheckCircleIcon size={16} weight="fill" /></span></Tip>{/if}
				<span class="context"><ContextPill {contexts} of={{ sourceId, repo, revision, group: g.name }} /></span>
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
	/* Rows share the file list's measures so the two tabs line up */
	.table {
		display: flex;
		flex-direction: column;
		min-width: 0;
	}
	.head,
	.row {
		display: flex;
		gap: 8px;
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
		flex: 1 1 auto;
	}
	.row {
		min-height: 40px;
		padding-top: 2px;
		padding-bottom: 2px;
		border-bottom: 1px solid var(--nebu-border);
		animation: nebu-reveal 220ms var(--nebu-settle) backwards;
	}
	.icon {
		display: inline-flex;
		flex: 0 0 auto;
		color: var(--nebu-text-muted);
	}
	.name {
		flex: 1 1 auto;
		min-width: 0;
		font-size: 13px;
		overflow-wrap: anywhere;
	}
	.stored {
		display: inline-flex;
		flex: 0 0 auto;
		color: var(--neo-color-success);
	}
	.context {
		display: inline-flex;
		flex: 0 0 4.5rem;
	}
	.size {
		flex: 0 0 auto;
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
		flex: 0 0 auto;
		justify-content: flex-end;
		min-width: 36px;
	}
</style>
