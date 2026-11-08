<script lang="ts">
	import type { Snippet } from 'svelte';
	import InfoTip from '#lib/ui/InfoTip.svelte';

	// A raised surface for one block of a page, a title row over a body tables fill edge to edge
	let {
		title,
		count,
		info,
		tools,
		children,
		footer,
		flush = false,
		grow = false,
		pad = 'normal'
	}: {
		title?: string;
		count?: number | string;
		info?: string;
		// Quiet controls for the block, filters, a sort, one secondary button
		tools?: Snippet;
		children: Snippet;
		// A row pinned under the body, a form's commit row
		footer?: Snippet;
		// Whether the body runs edge to edge, for lists and screens
		flush?: boolean;
		// Whether the card stretches to the height its column leaves
		grow?: boolean;
		pad?: 'normal' | 'roomy';
	} = $props();
</script>

<section class="card" class:grow class:roomy={pad === 'roomy'} aria-label={title}>
	{#if title !== undefined || tools}
		<header class="head" class:bare={title === undefined}>
			{#if title !== undefined}
				<div class="heading">
					<h2 class="title">{title}</h2>
					{#if count !== undefined}<span class="count">{count}</span>{/if}
					{#if info}<InfoTip text={info} />{/if}
				</div>
			{/if}
			{#if tools}<div class="tools">{@render tools()}</div>{/if}
		</header>
	{/if}
	<div class="body" class:flush class:headed={title !== undefined || tools !== undefined}>
		{@render children()}
	</div>
	{#if footer}<footer class="foot">{@render footer()}</footer>{/if}
</section>

<style>
	.card {
		--nebu-card-pad: var(--nebu-card-pad-base, var(--nebu-space-5));
		--nebu-row-inset: var(--nebu-card-pad);

		display: flex;
		flex-direction: column;
		min-width: 0;
		background: var(--neo-background-color);
		border-radius: var(--neo-border-radius-md);
		box-shadow: var(--neo-box-shadow-raised-2);
	}
	.card.roomy {
		--nebu-card-pad: var(--nebu-space-6);
	}
	.card.grow {
		flex: 1 1 auto;
	}
	/* The tools drop under the title when the two no longer share the width */
	.head {
		display: flex;
		flex-wrap: wrap;
		gap: var(--nebu-space-2) var(--nebu-space-3);
		align-items: center;
		justify-content: space-between;
		min-height: 56px;
		padding: var(--nebu-space-3) var(--nebu-card-pad);
	}
	/* A card without a title gives its whole head to the tools, a search bar running the full width */
	.head.bare > .tools {
		flex: 1 1 auto;
		justify-content: flex-start;
	}
	.heading {
		display: flex;
		flex: 0 1 auto;
		gap: var(--nebu-space-2);
		align-items: baseline;
		min-width: 0;
	}
	.title {
		font-size: var(--nebu-text-heading);
		white-space: nowrap;
	}
	.count {
		font-size: var(--nebu-text-secondary);
		font-variant-numeric: tabular-nums;
		color: var(--nebu-text-muted);
	}
	/* Filters and sorts in a card's title row run at control height, not form field height */
	.tools {
		--neo-input-padding: 0.375rem 0.75rem;

		display: flex;
		flex: 0 1 auto;
		flex-wrap: wrap;
		gap: var(--nebu-space-2);
		align-items: center;
		justify-content: flex-end;
		min-width: 0;
	}
	.body {
		display: flex;
		flex: 1 1 auto;
		flex-direction: column;
		gap: var(--nebu-space-4);
		min-width: 0;
		padding: var(--nebu-card-pad);
	}
	.body.headed {
		padding-top: 0;
	}
	.body.flush {
		gap: 0;
		padding: 0 0 var(--nebu-space-2);
	}
	.body.flush:not(.headed) {
		padding-top: var(--nebu-space-2);
	}
	.foot {
		display: flex;
		gap: var(--nebu-space-3);
		align-items: center;
		padding: var(--nebu-space-3) var(--nebu-card-pad);
		border-top: 1px solid var(--nebu-border);
	}
</style>
