<script lang="ts">
	import type { Snippet } from 'svelte';
	import { untrack } from 'svelte';
	import Tip from '#lib/ui/Tip.svelte';
	import { provideList, type ListHead } from '#lib/ui/list.svelte.js';
	import ArrowDownIcon from 'phosphor-svelte/lib/ArrowDownIcon';
	import ArrowUpIcon from 'phosphor-svelte/lib/ArrowUpIcon';

	// Rows sharing one set of columns under an optional head, folding to compact rows under the given width
	let { head, label, compactBelow, children }: { head?: ListHead; label?: string; compactBelow?: number; children: Snippet } = $props();

	const layout = provideList(untrack(() => compactBelow));
	$effect.pre(() => {
		layout.head = head;
	});
	const sortable = $derived(head?.facts?.some((f) => typeof f !== 'string' && f.onsort) ?? false);
</script>

<div class="list" role="group" aria-label={label} style:grid-template-columns={layout.template} bind:clientWidth={layout.width}>
	{#if head}
		<div class="listhead" aria-hidden={sortable ? undefined : 'true'}>
			{#if layout.has('leading')}<span></span>{/if}
			{#if layout.has('dot')}<span></span>{/if}
			<span>{head.primary ?? ''}</span>
			{#if layout.has('secondary')}<span>{head.secondary ?? ''}</span>{/if}
			{#if layout.has('instrument')}<span>{head.instrument ?? ''}</span>{/if}
			{#each { length: layout.shownFacts } as _, i (i)}
				{@const f = layout.fact(i)}
				<span class:end={!f?.start} class:orders={f?.onsort}>
					<Tip text={f?.tip} tag="span">
						{#if f?.onsort}
							<button type="button" class="sort" class:sorted={f.sorted} aria-label={`Sort by ${f.label}`} aria-pressed={f.sorted !== undefined} onclick={f.onsort}>
								<span class="words">{f.label}</span>
								<span class="arrow">{#if f.sorted === 'ascending'}<ArrowUpIcon size={12} weight="bold" />{:else}<ArrowDownIcon size={12} weight="bold" />{/if}</span>
							</button>
						{:else}
							{f?.label ?? ''}
						{/if}
					</Tip>
				</span>
			{/each}
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
	/* A heading wider than its column clips inside it, clear of the next heading */
	.listhead > span {
		min-width: 0;
		overflow: hidden;
		text-overflow: ellipsis;
	}
	.end {
		text-align: right;
	}
	.listhead > .orders {
		display: flex;
		align-items: center;
		overflow: visible;
	}
	.orders.end {
		justify-content: flex-end;
	}
	.sort {
		display: inline-flex;
		flex-direction: row-reverse;
		gap: 2px;
		align-items: center;
		max-width: 100%;
		padding: 0;
		font: inherit;
		color: inherit;
		cursor: pointer;
		background: none;
		border: 0;
		border-radius: 2px;
	}
	.orders:not(.end) .sort {
		flex-direction: row;
	}
	.sort:hover,
	.sort.sorted {
		color: var(--neo-text-color);
	}
	.words {
		min-width: 0;
		overflow: hidden;
		text-overflow: ellipsis;
	}
	.arrow {
		display: inline-flex;
		flex: 0 0 auto;
		opacity: 0;
		transition: opacity var(--nebu-normal) var(--nebu-ease);
	}
	.sort:hover .arrow {
		opacity: 0.5;
	}
	.sort.sorted .arrow {
		opacity: 1;
	}
</style>
