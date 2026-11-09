<script lang="ts">
	import type { Snippet } from 'svelte';
	import { untrack } from 'svelte';
	import { provideList, type ListHead } from '#lib/ui/list.svelte.js';

	// Rows sharing one set of columns under an optional head, folding to compact rows under the given width
	let { head, label, compactBelow, children }: { head?: ListHead; label?: string; compactBelow?: number; children: Snippet } = $props();

	const layout = provideList(untrack(() => compactBelow));
</script>

<div class="list" role="group" aria-label={label} style:grid-template-columns={layout.template} bind:clientWidth={layout.width}>
	{#if head}
		<div class="listhead" aria-hidden="true">
			{#if layout.has('leading')}<span></span>{/if}
			{#if layout.has('dot')}<span></span>{/if}
			<span>{head.primary ?? ''}</span>
			{#if layout.has('secondary')}<span>{head.secondary ?? ''}</span>{/if}
			{#if layout.has('instrument')}<span>{head.instrument ?? ''}</span>{/if}
			{#each { length: layout.shownFacts } as _, i (i)}<span class="end">{head.facts?.[i] ?? ''}</span>{/each}
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
	.list > :global(:not(.rowwrap, .listhead)) {
		grid-column: 1 / -1;
	}
	/* Rows settle in one after another */
	.list > :global(.rowwrap),
	.list > :global(.sortrow) {
		animation: nebu-reveal 260ms var(--nebu-settle) backwards;
	}
	.list > :global(:nth-child(2)) { animation-delay: 22ms; }
	.list > :global(:nth-child(3)) { animation-delay: 44ms; }
	.list > :global(:nth-child(4)) { animation-delay: 66ms; }
	.list > :global(:nth-child(5)) { animation-delay: 88ms; }
	.list > :global(:nth-child(6)) { animation-delay: 110ms; }
	.list > :global(:nth-child(7)) { animation-delay: 132ms; }
	.list > :global(:nth-child(8)) { animation-delay: 154ms; }
	.list > :global(:nth-child(9)) { animation-delay: 176ms; }
	.list > :global(:nth-child(10)) { animation-delay: 198ms; }
	.list > :global(:nth-child(11)) { animation-delay: 220ms; }
	.list > :global(:nth-child(12)) { animation-delay: 242ms; }
	.list > :global(:nth-child(n + 13)) { animation-delay: 264ms; }
	.listhead {
		display: grid;
		grid-template-columns: subgrid;
		grid-column: 1 / -1;
		align-items: center;
		min-height: 34px;
		padding: 0 var(--nebu-row-inset, var(--nebu-space-2));
		font-size: var(--nebu-text-caption);
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
