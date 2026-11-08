<script lang="ts">
	import type { Snippet } from 'svelte';
	import { provideList, type ListHead } from '#lib/ui/list.svelte.js';

	// Rows sharing one set of columns, with an optional head naming them
	let { head, label, children }: { head?: ListHead; label?: string; children: Snippet } = $props();

	const layout = provideList();
</script>

<div class="list" role="group" aria-label={label} style:grid-template-columns={layout.template} bind:clientWidth={layout.width}>
	{#if head}
		<div class="listhead" aria-hidden="true">
			{#if layout.has('leading')}<span></span>{/if}
			{#if layout.has('dot')}<span></span>{/if}
			<span>{head.primary ?? ''}</span>
			{#if layout.has('secondary')}<span>{head.secondary ?? ''}</span>{/if}
			{#if layout.has('instrument')}<span>{head.instrument ?? ''}</span>{/if}
			{#if !layout.compact}
				{#each { length: layout.factCount } as _, i (i)}<span class="end">{head.facts?.[i] ?? ''}</span>{/each}
			{/if}
			{#if layout.has('action')}<span class="end">{head.action ?? ''}</span>{/if}
			{#if layout.has('menu')}<span></span>{/if}
			{#if layout.has('caret')}<span></span>{/if}
		</div>
	{/if}
	{@render children()}
</div>

<style>
	.list {
		display: grid;
		column-gap: var(--nebu-space-4);
		align-items: stretch;
		min-width: 0;
	}
	/* Anything in the list that is not a row, a skeleton, an empty line, a load more, spans every column */
	.list > :global(:not(.rowwrap, .listhead)) {
		grid-column: 1 / -1;
	}
	.listhead {
		display: grid;
		grid-template-columns: subgrid;
		grid-column: 1 / -1;
		align-items: center;
		min-height: 34px;
		padding: 0 var(--nebu-row-inset, var(--nebu-space-2));
		font-size: var(--nebu-text-caption);
		font-weight: 500;
		color: var(--nebu-text-muted);
		white-space: nowrap;
		border-top: 1px solid var(--nebu-border);
		border-bottom: 1px solid var(--nebu-border);
		background: var(--nebu-surface-sunken);
	}
	.end {
		text-align: right;
	}
</style>
